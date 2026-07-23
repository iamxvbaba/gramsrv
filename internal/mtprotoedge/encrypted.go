package mtprotoedge

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime/debug"
	"time"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/transport"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/observability/dbtrace"
	"telesrv/internal/postresponse"
	"telesrv/internal/store"
)

// connState 是单连接的 MTProto 运行态。
type connState struct {
	// createdFloor is the smallest client msg_id covered by the latest
	// new_session_created notification for this server-side session generation.
	// It only moves down: official clients resend every request below first_msg_id,
	// so advertising an outer container id while accepting smaller inner ids would
	// orphan the original rpc_result messages.
	createdFloor int64
	seen         map[int64]clientMsgRecord // 已处理的 client msg_id，用于幂等和 msgs_state_req
	order        []int64
	minSeen      int64
	maxSeen      int64
	// maxContentMsgID/maxContentSeqNo 是已接受 content 消息的 msg_id / seq_no 高水位，
	// 供 validateSeq 的 O(1) 快路径使用（客户端正常发送严格递增）。二者只增不减、
	// 不随 seen 淘汰回退——快路径只接受「全扫描也必然接受」的子集，其余回落全扫描。
	maxContentMsgID int64
	maxContentSeqNo int32
}

type clientMsgRecord struct {
	state   byte
	seqNo   int32
	content bool
	// service is the constructor class admitted with this msg_id. A duplicate
	// uses this committed class and never decodes/executes its replacement body.
	service bool
}

func newConnState() *connState {
	return &connState{
		seen:    make(map[int64]clientMsgRecord),
		minSeen: math.MaxInt64,
	}
}

func (cs *connState) reset() {
	next := newConnState()
	*cs = *next
}

const (
	maxTrackedClientMsgIDs = 400
	// maxContainerMessages bounds per-frame recursive work and ack growth. Official clients batch
	// far fewer messages; 1024 leaves ample headroom while preventing a 16 MiB frame of zero-body
	// container entries from expanding into tens of MiB of Go objects.
	maxContainerMessages = 1024
	// maxDispatchDepth bounds gzip/container wrapper recursion. Normal shapes are RPC, gzip(RPC),
	// container(RPC...) and gzip(container(...)); deeper nesting has no compatibility value.
	maxDispatchDepth = 4
	// gotd already caps each gzip expansion at 10 MiB. This cumulative cap prevents several nested
	// gzip layers in one transport frame from repeatedly allocating/decompressing that allowance.
	maxDispatchExpandedBytes   = 32 << 20
	maxSingleGZIPExpandedBytes = 10 << 20
	// MTProto service vectors operate on bounded connection tracking tables. Accepting more IDs
	// only burns decode/CPU and cannot improve the result.
	maxServiceMessageIDs = 4096
	// Charge each container entry for the decoded proto.Message view plus the staged connState,
	// action and ACK descriptors retained by the single-pass inbound plan. Bodies remain zero-copy
	// views of the already charged plaintext frame/gzip expansion; RPC copies have a separate batch
	// admission budget.
	containerDescriptorBudgetBytes = 192

	msgStateUnknown         byte = 1
	msgStateNotReceived     byte = 2
	msgStateNotReceivedHigh byte = 3
	msgStateReceived        byte = 4

	badMsgIDTooLow      = 16
	badMsgIDTooHigh     = 17
	badMsgIDInvalidBits = 18
	badMsgSeqTooLow     = 32
	badMsgSeqTooHigh    = 33
	badMsgSeqNotEven    = 34
	badMsgSeqNotOdd     = 35
	badMsgContainer     = 64
)

var errActivationAuthKeyRejected = errors.New("activation auth key no longer exists")

// handleEncrypted 解密加密消息，按需注册连接，处理服务消息并分发明文 payload。
// 返回（可能新建/更新的）当前连接对象，供 serveConn 维护生命周期。
// fetchedKey 非 nil 表示本帧的 auth key 是刚从 AuthKeyStore 查出的（首帧/换 auth key/被销毁
// 后回落）；为 nil 表示走连接缓存快路径——serveConn 判定 current 仍持同一未销毁的 auth key，
// 直接复用 current.key/current.salt 解密。任何 provisional 在 claim 建立后、发 required
// control 前都会最终回查 AuthKeyStore，使外部撤销与 activation 线性化。
// plain 是 serveConn 持有的复用明文缓冲，frame 的 slice 仅在下一帧解密前有效。
func (s *Server) handleEncrypted(ctx context.Context, tc transport.Conn, cs *connState, current *Conn, fetchedKey *store.AuthKeyData, b, plain *bin.Buffer) (*Conn, error) {
	var key crypto.AuthKey
	var serverSalt int64
	var authKeyExpiresAt int
	if fetchedKey != nil {
		key = crypto.AuthKey{Value: crypto.Key(fetchedKey.Value), ID: fetchedKey.ID}
		serverSalt = fetchedKey.ServerSalt
		authKeyExpiresAt = fetchedKey.ExpiresAt
	} else {
		// 快路径：复用已建立连接缓存的密钥与盐（同一 auth key 的后续帧，含同连接换 session）。
		key = current.key
		serverSalt = current.salt
		authKeyExpiresAt = current.authKeyExpiresAt
	}

	frame, err := decryptClientFrame(key, b, plain)
	if err != nil {
		return current, fmt.Errorf("decrypt: %w", err)
	}

	// 首个加密消息（即使 salt 尚未修正）或 session 变化时创建并保留唯一的
	// provisional Conn。同一物理 transport 换 session 必须先不可逆地 fence/drain
	// 旧 writer，再把物理 lease 原子转交给新 generation。为每个 bad_server_salt
	// 临时创建 Conn 会在同一 socket 上启动多个 outbound actor，Android 的启动重试
	// 风暴随即变成并发写和重复结果放大。
	if current == nil || current.sessionID != frame.sessionID || current.authKeyID != key.ID {
		var previousLayer LayerProfileSnapshot
		if current != nil {
			if current.authKeyID == key.ID {
				previousLayer = current.LayerProfileState()
			}
			cs.reset()
			current.beginTerminalShutdown()
			s.conns.Unregister(current)
			if !current.waitOutboundShutdownUntil(forceCloseBatchTimeout) {
				return current, errors.New("previous session outbound writer did not stop")
			}
			nextLease, ok := current.transferTransportOwnership()
			if !ok {
				return current, ErrConnClosed
			}
			current = s.newConnWithLease(nextLease, key, frame.sessionID, serverSalt)
		} else {
			current = s.newConn(tc, key, frame.sessionID, serverSalt)
		}
		current.authKeyExpiresAt = authKeyExpiresAt
		// Same-session evidence is restored as explicit; auth-key metadata is only
		// an inherited default and can be corrected by the next invokeWithLayer.
		if s.rpc != nil {
			if layer, ok := s.rpc.NegotiatedLayer(current.authKeyID, current.sessionID); ok {
				current.setLegacyClientLayer(layer)
			}
		}
		fetchedLayer := 0
		if fetchedKey != nil {
			fetchedLayer = fetchedKey.Layer
		}
		if err := s.seedInitialLayerProfile(ctx, current, fetchedLayer, previousLayer); err != nil {
			return current, fmt.Errorf("seed connection layer profile: %w", err)
		}
	}

	if frame.salt != serverSalt {
		// bad_server_salt 是修正后重试的物理屏障：payload 与加密 envelope 都必须携带
		// 同一个权威 salt，写失败则该 provisional/active Conn 不得继续接收状态。
		return current, s.sendBadServerSalt(ctx, current, frame.messageID, frame.seqNo, serverSalt)
	}

	body := frame.data
	typeID, err := (&bin.Buffer{Buf: body}).PeekID()
	if err != nil {
		return current, fmt.Errorf("peek encrypted payload type id: %w", err)
	}
	plan, err := s.preflightInbound(cs, frame.messageID, frame.seqNo, body)
	if err != nil {
		var bad *dispatchBadMsgError
		if errors.As(err, &bad) {
			s.log.Debug("Sending bad_msg_notification",
				zap.Int64("msg_id", bad.msgID),
				zap.Int32("seq_no", bad.seqNo),
				zap.Uint32("type_id", typeID),
				zap.Int("code", bad.code),
			)
			return current, s.sendBadMsg(ctx, current, bad.msgID, bad.seqNo, bad.code)
		}
		return current, err
	}
	defer plan.close()
	if err := s.prepareInboundRPCBatch(ctx, current, plan); err != nil {
		if errors.Is(err, errDestroyAuthKeyMustBeExclusive) {
			s.log.Debug("Rejecting mixed destroy_auth_key container",
				zap.Int64("msg_id", frame.messageID),
				zap.Int32("seq_no", frame.seqNo),
			)
			return current, s.sendBadMsg(ctx, current, frame.messageID, frame.seqNo, badMsgContainer)
		}
		return current, err
	}
	if err := sendQuickAckIfRequested(ctx, current.transport, key, frame.plaintext, s.writeTimeout); err != nil {
		return current, err
	}

	moveCreatedFloor := cs.createdFloor == 0 || plan.logicalMin < cs.createdFloor
	claimPending := false
	if current.lifecycleState() == connLifecycleProvisional {
		if !moveCreatedFloor {
			return current, errors.New("provisional session has no new_session_created boundary")
		}
		if err := s.conns.BeginActivation(current); err != nil {
			return current, err
		}
		claimPending = true
		defer func() {
			if claimPending {
				s.conns.AbortActivation(current)
			}
		}()

		// BeginActivation has installed current in claimsByAuth, which is the shared
		// linearization domain with auth-key revocation. A delete that completed before
		// the claim is visible here as !found; a delete after this read must observe and
		// fence the claim. This final check intentionally covers every activation path:
		// first correct-salt frame, retained bad-salt provisional and session transfer.
		fresh, found, getErr := s.authKeys.Get(ctx, current.authKeyID)
		if getErr != nil {
			return current, fmt.Errorf("revalidate activation auth key: %w", getErr)
		}
		if !found || fresh.ID != current.authKeyID || fresh.Value != [256]byte(current.key.Value) || authKeyProtocolUnavailable(fresh.ExpiresAt, s.clock.Now()) {
			// Send the terminal protocol error while the claim still owns a live writer;
			// the deferred abort then fences and removes it before serveConn returns.
			if sendErr := s.sendTerminalProtoError(ctx, current, codec.CodeAuthKeyNotFound); sendErr != nil {
				return current, sendErr
			}
			return current, errActivationAuthKeyRejected
		}
		// Re-resolve inherited Layer only after the activation claim is visible.
		// This closes the bind-vs-connect window for temporary keys without ever
		// replacing explicit invokeWithLayer evidence admitted above.
		if err := s.refreshActivatedInheritedLayerProfile(ctx, current, fresh.Layer); err != nil {
			return current, fmt.Errorf("refresh claimed connection layer profile: %w", err)
		}
		if current.isRetired() || !current.isPhysicalTransportCurrentOpen() {
			return current, ErrConnClosed
		}
	}
	if moveCreatedFloor {
		s.log.Debug("Sending new_session_created",
			zap.Int64("first_msg_id", plan.logicalMin),
			zap.Int64("outer_msg_id", frame.messageID),
			zap.Int32("seq_no", frame.seqNo),
		)
		if err := s.sendNewSessionCreated(ctx, current, plan.logicalMin); err != nil {
			return current, err
		}
	}
	if !current.isPhysicalTransportCurrentOpen() {
		return current, ErrConnClosed
	}
	if claimPending {
		if err := s.conns.PublishActivation(current); err != nil {
			return current, err
		}
		claimPending = false
	}
	if moveCreatedFloor {
		cs.createdFloor = plan.logicalMin
	}
	plan.commitState(cs)

	if err := s.executeInboundPlan(ctx, cs, current, plan); err != nil {
		return current, err
	}
	if err := plan.commitRewrapAliases(s); err != nil {
		return current, err
	}
	if err := plan.commitRPCBatch(); err != nil {
		return current, err
	}
	if len(plan.ackIDs) > 0 {
		if err := s.sendAck(ctx, current, plan.ackIDs...); err != nil {
			return current, err
		}
	}
	return current, nil
}

func sendQuickAckIfRequested(ctx context.Context, tc transport.Conn, key crypto.AuthKey, plaintext []byte, writeTimeout time.Duration) error {
	q, ok := tc.(quickAckTransport)
	if !ok || !q.ConsumeQuickAckRequested() {
		return nil
	}
	token := clientQuickAckToken(key, plaintext)
	deadline := time.Time{}
	if writeTimeout > 0 {
		deadline = time.Now().Add(writeTimeout)
	}
	if d, ok := ctx.Deadline(); ok && (deadline.IsZero() || d.Before(deadline)) {
		deadline = d
	}
	if dq, ok := tc.(deadlineQuickAckTransport); ok {
		return dq.SendQuickAckDeadline(deadline, token)
	}
	if deadline.IsZero() {
		return q.SendQuickAck(ctx, token)
	}
	sendCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return q.SendQuickAck(sendCtx, token)
}

// clientQuickAckToken 按 Android MTProto v2 公式计算 quick ack：SHA256(auth_key[88:120] +
// 完整明文)[:4]。plaintext 直接来自解密复用缓冲（decryptClientFrame.plaintext），
// 与旧实现「把解密结果重编码一遍再哈希」字节一致但零拷贝。
func clientQuickAckToken(key crypto.AuthKey, plaintext []byte) uint32 {
	h := sha256.New()
	_, _ = h.Write(key.Value[88:120])
	_, _ = h.Write(plaintext)
	sum := h.Sum(nil)
	return binary.LittleEndian.Uint32(sum[:4]) &^ quickAckResponseFlag
}

// dispatchBadMsgError carries a protocol-level rejection discovered during the
// side-effect-free wrapper/container preflight. The caller emits the single
// bad_msg_notification only after the whole container has been inspected.
type dispatchBadMsgError struct {
	msgID int64
	seqNo int32
	code  int
}

func (e *dispatchBadMsgError) Error() string {
	return fmt.Sprintf("bad client message %d/%d: code %d", e.msgID, e.seqNo, e.code)
}

// decodeGZIPWithGlobalBudget reserves the maximum single-wrapper output before
// decompression starts. Once the actual size is known the excess reservation is
// returned, while the actual output remains charged until the inbound plan is
// executed or aborted.
// This closes the gap where every connection read goroutine could otherwise hold
// an unaccounted 10 MiB expansion before the shared RPC scheduler saw the body.
func (s *Server) decodeGZIPWithGlobalBudget(b *bin.Buffer) ([]byte, func(), error) {
	compressed, err := gzipPackedBytesView(b)
	if err != nil {
		return nil, func() {}, err
	}
	reserved := int64(0)
	release := func() {
		if reserved > 0 && s.frameBudget != nil {
			s.frameBudget.release(reserved)
			reserved = 0
		}
	}
	if s.frameBudget != nil {
		reserved, err = s.frameBudget.reserve(maxSingleGZIPExpandedBytes, 0)
		if err != nil {
			return nil, func() {}, err
		}
	}

	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		release()
		return nil, func() {}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(r, maxSingleGZIPExpandedBytes+1))
	closeErr := r.Close()
	if readErr != nil {
		release()
		return nil, func() {}, readErr
	}
	if closeErr != nil {
		release()
		return nil, func() {}, closeErr
	}
	if len(data) > maxSingleGZIPExpandedBytes {
		release()
		return nil, func() {}, fmt.Errorf("gzip expansion %d exceeds %d", len(data), maxSingleGZIPExpandedBytes)
	}
	if reserved > int64(len(data)) {
		s.frameBudget.release(reserved - int64(len(data)))
		reserved = int64(len(data))
	}
	return data, release, nil
}

// gzipPackedBytesView parses the TL bytes envelope without copying the compressed
// payload. proto.GZIP.Decode calls bin.Buffer.Bytes, which duplicates the compressed
// frame before allocating the decompressed result.
func gzipPackedBytesView(b *bin.Buffer) ([]byte, error) {
	if b == nil || len(b.Buf) < 5 {
		return nil, io.ErrUnexpectedEOF
	}
	if binary.LittleEndian.Uint32(b.Buf[:4]) != proto.GZIPTypeID {
		return nil, fmt.Errorf("unexpected gzip constructor %#x", binary.LittleEndian.Uint32(b.Buf[:4]))
	}
	payload, consumed, err := tlBytesView(b.Buf[4:], -1)
	if err != nil {
		return nil, err
	}
	if 4+consumed != len(b.Buf) {
		return nil, fmt.Errorf("gzip_packed has %d trailing bytes", len(b.Buf)-(4+consumed))
	}
	return payload, nil
}

// tlBytesView validates one TL bytes envelope and returns a view into the caller-owned buffer.
// maxPayload < 0 means that the enclosing frame budget is the only size limit. The limit is
// checked from the encoded length before touching the payload, so service messages cannot make
// generated decoders allocate an attacker-selected []byte first and validate it afterwards.
func tlBytesView(raw []byte, maxPayload int) ([]byte, int, error) {
	if len(raw) < 1 {
		return nil, 0, io.ErrUnexpectedEOF
	}
	header, size := 1, int(raw[0])
	if size == 254 {
		if len(raw) < 4 {
			return nil, 0, io.ErrUnexpectedEOF
		}
		header = 4
		size = int(raw[1]) | int(raw[2])<<8 | int(raw[3])<<16
	} else if size == 255 {
		return nil, 0, errors.New("invalid TL bytes length marker 255")
	}
	if maxPayload >= 0 && size > maxPayload {
		return nil, 0, fmt.Errorf("TL bytes length %d exceeds %d", size, maxPayload)
	}
	padded := (header + size + 3) &^ 3
	if size < 0 || padded < header || len(raw) < padded {
		return nil, 0, io.ErrUnexpectedEOF
	}
	return raw[header : header+size : header+size], padded, nil
}

// decodeMessageContainerViews parses the container without proto.Message.Decode's per-body
// copies. Bodies stay as immutable views through the single-pass inbound plan; batch admission
// takes independent RPC copies before the backing frame/expansion is released. The descriptor
// reservation also covers staged state, actions and ACK metadata retained by that plan.
func (s *Server) decodeMessageContainerViews(b *bin.Buffer, count int) (proto.MessageContainer, func(), error) {
	release := func() {}
	if b == nil || len(b.Buf) < 8 {
		return proto.MessageContainer{}, release, io.ErrUnexpectedEOF
	}
	if got := binary.LittleEndian.Uint32(b.Buf[:4]); got != proto.MessageContainerTypeID {
		return proto.MessageContainer{}, release, fmt.Errorf("unexpected constructor %#x", got)
	}
	declared := int(int32(binary.LittleEndian.Uint32(b.Buf[4:8])))
	if declared != count || count < 0 || count > maxContainerMessages {
		return proto.MessageContainer{}, release, fmt.Errorf("invalid message count %d", declared)
	}

	reserved := int64(0)
	if count > 0 && s.frameBudget != nil {
		var err error
		reserved, err = s.frameBudget.reserve(int64(count*containerDescriptorBudgetBytes), 0)
		if err != nil {
			return proto.MessageContainer{}, release, err
		}
		release = func() {
			if reserved > 0 {
				s.frameBudget.release(reserved)
				reserved = 0
			}
		}
	}

	messages := make([]proto.Message, count)
	offset := 8
	for i := range messages {
		if len(b.Buf)-offset < 16 {
			release()
			return proto.MessageContainer{}, func() {}, io.ErrUnexpectedEOF
		}
		id := int64(binary.LittleEndian.Uint64(b.Buf[offset : offset+8]))
		seqNo := int32(binary.LittleEndian.Uint32(b.Buf[offset+8 : offset+12]))
		bodyLen := int(int32(binary.LittleEndian.Uint32(b.Buf[offset+12 : offset+16])))
		offset += 16
		if bodyLen < 0 || bodyLen > 1024*1024 {
			release()
			return proto.MessageContainer{}, func() {}, fmt.Errorf("message length %d is invalid", bodyLen)
		}
		if bodyLen > len(b.Buf)-offset {
			release()
			return proto.MessageContainer{}, func() {}, io.ErrUnexpectedEOF
		}
		bodyEnd := offset + bodyLen
		messages[i] = proto.Message{
			ID:    id,
			SeqNo: int(seqNo),
			Bytes: bodyLen,
			Body:  b.Buf[offset:bodyEnd:bodyEnd],
		}
		offset = bodyEnd
	}
	if offset != len(b.Buf) {
		release()
		return proto.MessageContainer{}, func() {}, fmt.Errorf("message container has %d trailing bytes", len(b.Buf)-offset)
	}
	return proto.MessageContainer{Messages: messages}, release, nil
}

func msgsStateInfoView(b *bin.Buffer) (int64, []byte, error) {
	if b == nil || len(b.Buf) < 12 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	if got := binary.LittleEndian.Uint32(b.Buf[:4]); got != mt.MsgsStateInfoTypeID {
		return 0, nil, fmt.Errorf("unexpected constructor %#x", got)
	}
	info, consumed, err := tlBytesView(b.Buf[12:], maxServiceMessageIDs)
	if err != nil {
		return 0, nil, err
	}
	if 12+consumed != len(b.Buf) {
		return 0, nil, fmt.Errorf("msgs_state_info has %d trailing bytes", len(b.Buf)-(12+consumed))
	}
	return int64(binary.LittleEndian.Uint64(b.Buf[4:12])), info, nil
}

func msgsAllInfoView(b *bin.Buffer) (int, []byte, error) {
	if err := validateFirstVectorCount(b, maxServiceMessageIDs); err != nil {
		return 0, nil, fmt.Errorf("vector: %w", err)
	}
	count := int(int32(binary.LittleEndian.Uint32(b.Buf[8:12])))
	// count is already non-negative and capped, but check remaining bytes before multiplying into
	// an offset so malformed frames cannot produce an out-of-bounds slice.
	if count > (len(b.Buf)-12)/8 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	offset := 12 + count*8
	info, consumed, err := tlBytesView(b.Buf[offset:], maxServiceMessageIDs)
	if err != nil {
		return 0, nil, err
	}
	if offset+consumed != len(b.Buf) {
		return 0, nil, fmt.Errorf("msgs_all_info has %d trailing bytes", len(b.Buf)-(offset+consumed))
	}
	return count, info, nil
}

func containerMessageCount(b *bin.Buffer) (int, error) {
	if b == nil || len(b.Buf) < 8 {
		return 0, io.ErrUnexpectedEOF
	}
	if binary.LittleEndian.Uint32(b.Buf[:4]) != proto.MessageContainerTypeID {
		return 0, fmt.Errorf("unexpected constructor %#x", binary.LittleEndian.Uint32(b.Buf[:4]))
	}
	count := int(int32(binary.LittleEndian.Uint32(b.Buf[4:8])))
	if count < 0 {
		return 0, fmt.Errorf("negative message count %d", count)
	}
	return count, nil
}

func validateFirstVectorCount(b *bin.Buffer, max int) error {
	if b == nil || len(b.Buf) < 12 {
		return io.ErrUnexpectedEOF
	}
	if got := binary.LittleEndian.Uint32(b.Buf[4:8]); got != bin.TypeVector {
		return fmt.Errorf("unexpected vector constructor %#x", got)
	}
	count := int(int32(binary.LittleEndian.Uint32(b.Buf[8:12])))
	if count < 0 {
		return fmt.Errorf("negative vector count %d", count)
	}
	if count > max {
		return fmt.Errorf("vector count %d exceeds %d", count, max)
	}
	return nil
}

func mergeStateInfo(primary, fallback []byte) []byte {
	if len(primary) == 0 {
		return fallback
	}
	info := make([]byte, len(fallback))
	copy(info, fallback)
	for i, state := range primary {
		if i >= len(info) {
			break
		}
		if state != 0 {
			info[i] = state
		}
	}
	return info
}

// newInboundRPCTask builds the exactly-once timeout/result gate shared by the
// single-message and atomic container-batch admission paths. body must already
// be an independently owned, budgeted copy.
func (s *Server) newInboundRPCTask(c *Conn, msgID int64, method string, body []byte, owner *rpcResultOwnerLease) inboundRPC {
	timeoutResponse := func() {
		// 只有尚未进入 handler 的排队请求会走这里。运行中的请求只取消
		// context，等 handler 收敛后再决定成功或 RPC_TIMEOUT，避免客户端用
		// 新 msg_id 重试时与旧业务提交并发。
		writeTimeout := c.writeTimeout
		if writeTimeout <= 0 || writeTimeout > 5*time.Second {
			writeTimeout = 5 * time.Second
		}
		responseCtx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		defer cancel()
		if sendErr := s.sendResult(responseCtx, c, msgID, &mt.RPCError{
			ErrorCode:    500,
			ErrorMessage: "RPC_TIMEOUT",
		}); sendErr != nil && !isClientDisconnect(sendErr) {
			s.log.Debug("Send RPC timeout failed",
				zap.String("method", method),
				zap.Int64("msg_id", msgID),
				zap.String("auth_key_id", c.authKeyHex),
				zap.Int64("session_id", c.sessionID),
				zap.Error(sendErr),
			)
		}
	}
	return inboundRPC{
		method:    method,
		size:      len(body),
		onTimeout: timeoutResponse,
		release: func() {
			if owner == nil {
				return
			}
			if owner.Abort() {
				// connState already remembers this request. If a committed task exits
				// without publishing any terminal rpc_result, a same-Conn retransmit
				// would otherwise be ACKed forever. Force a fresh physical generation
				// where the request can be admitted again.
				c.fenceUndeliveredRPCResult()
			}
		},
		run: func(taskCtx context.Context) error {
			// body 是预算成功后生成的独立副本，且每个任务只 run 一次，
			// 无需再 append 拷贝；直接复用，省掉一份 inbound 在途内存。
			if err := s.handleRPC(taskCtx, c, msgID, method, &bin.Buffer{Buf: body}, owner); err != nil {
				fields := []zap.Field{
					zap.Int64("msg_id", msgID),
					zap.String("auth_key_id", c.authKeyHex),
					zap.Int64("session_id", c.sessionID),
					zap.Error(err),
				}
				if isClientDisconnect(err) {
					s.log.Debug("RPC async handler canceled", fields...)
				} else {
					s.log.Info("RPC async handler failed", fields...)
				}
				return err
			}
			return nil
		},
	}
}

func (s *Server) handleInboundRPCAdmissionError(ctx context.Context, c *Conn, msgID int64, method string, err error) error {
	if errors.Is(err, ErrInboundRPCQueueFull) {
		s.log.Debug("Inbound RPC capacity exhausted",
			zap.String("method", method),
			zap.Int64("msg_id", msgID),
			zap.String("auth_key_id", c.authKeyHex),
			zap.Int64("session_id", c.sessionID),
		)
		return s.sendResult(ctx, c, msgID, &mt.RPCError{
			ErrorCode:    420,
			ErrorMessage: "FLOOD_WAIT_1",
		})
	}
	return err
}

// handleRPC 把明文 RPC 请求交给 RPC 路由，并将结果或错误包成 rpc_result 回发。
func (s *Server) handleRPC(ctx context.Context, c *Conn, msgID int64, method string, b *bin.Buffer, owner *rpcResultOwnerLease) error {
	if s.rpc == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.log.Warn("No RPC handler configured", zap.String("method", method))
		return s.publishRPCResult(c, msgID, method, owner, &mt.RPCError{
			ErrorCode:    500,
			ErrorMessage: "NOT_IMPLEMENTED",
		}, nil)
	}

	ctx = postresponse.WithCallbacks(ctx)
	ctx, dbStats := dbtrace.WithStats(ctx)
	// legacyRPC is an unexported package-test hook, but its result still has to
	// obey the production exact-codec invariant. Admit a defensive copy using
	// the generated current profile before the legacy router consumes b.
	admissionBody := &bin.Buffer{Buf: append([]byte(nil), b.Buf...)}
	admitted, err := tlprofile.NewDispatcher().AdmitDefault(
		tlprofile.ProfileCanonical,
		admissionBody,
		inboundLayerDecodeLimits,
	)
	if err != nil {
		return fmt.Errorf("admit legacy test RPC through generated codec: %w", err)
	}
	start := s.clock.Now()
	effectiveMethod := method
	var (
		result      bin.Encoder
		dispatchErr error
	)
	if detailed, ok := s.rpc.(legacyRPCHandlerWithMethod); ok {
		var innerMethod string
		result, innerMethod, dispatchErr = detailed.DispatchWithMethod(ctx, c.authKeyID, c.sessionID, b)
		if innerMethod != "" {
			effectiveMethod = innerMethod
		}
	} else {
		result, dispatchErr = s.rpc.Dispatch(ctx, c.authKeyID, c.sessionID, b)
	}
	if dispatchErr == nil && result != nil && !isLayerInvariantRPCResultEncoder(result) {
		if _, exact := result.(exactLayerRPCResultEncoder); !exact {
			result = &legacyTestRPCResultEncoder{call: admitted.Call(), result: result}
		}
	}
	dur := s.clock.Now().Sub(start)
	s.metrics.RPCHandled(effectiveMethod, dur, dispatchErr)
	// 刷新本连接由 invokeWithLayer 证明并冻结的 exact-session layer。ok=false
	// 表示仍无协议证据；设备/授权元数据和其它 session 都不具备回填资格。
	if layer, ok := s.rpc.NegotiatedLayer(c.authKeyID, c.sessionID); ok {
		c.setLegacyClientLayer(layer)
	}

	fields := make([]zap.Field, 0, 12)
	fields = append(fields,
		zap.String("method", effectiveMethod),
		zap.String("auth_key_id", c.authKeyHex),
		zap.Int64("session_id", c.sessionID),
		zap.Int64("msg_id", msgID),
		zap.Duration("dur", dur),
	)
	if effectiveMethod != method {
		fields = append(fields, zap.String("outer_method", method))
	}
	if businessAuthKeyHex, ok := c.BusinessAuthKeyHex(); ok {
		fields = append(fields, zap.String("business_auth_key_id", businessAuthKeyHex))
	}
	if userID := c.UserID(); userID != 0 {
		fields = append(fields, zap.Int64("user_id", userID))
	}
	fields = dbtrace.AppendZapFields(fields, "", dbStats.Snapshot())

	if ctxErr := ctx.Err(); ctxErr != nil {
		// A running request owns its terminal response until Dispatch returns. If
		// useful work completed despite cancellation, preserve that success; otherwise
		// a deadline becomes RPC_TIMEOUT only now, after the handler has converged.
		// Plain connection cancellation remains retryable on the replacement.
		var terminal bin.Encoder
		runPostResponse := false
		if dispatchErr == nil && result != nil {
			terminal = result
			runPostResponse = true
		} else if errors.Is(ctxErr, context.DeadlineExceeded) {
			terminal = &mt.RPCError{ErrorCode: 500, ErrorMessage: "RPC_TIMEOUT"}
		}
		if terminal != nil {
			var after func()
			if runPostResponse {
				after = postresponse.Take(context.WithoutCancel(ctx))
			}
			if sendErr := s.publishRPCResult(c, msgID, effectiveMethod, owner, terminal, after); sendErr != nil {
				s.log.Debug("Publish canceled RPC result failed", append(fields, zap.Error(sendErr))...)
			}
		}
		cancelFields := append(fields, zap.NamedError("context_error", ctxErr))
		if dispatchErr != nil {
			cancelFields = append(cancelFields, zap.NamedError("dispatch_error", dispatchErr))
		}
		s.log.Info("RPC canceled", cancelFields...)
		return ctxErr
	}

	if dispatchErr != nil {
		var rpcErr *tgerr.Error
		if errors.As(dispatchErr, &rpcErr) {
			s.log.Info("RPC error", append(fields, zap.Int("code", rpcErr.Code), zap.String("error", rpcErr.Message))...)
			return s.publishRPCResult(c, msgID, effectiveMethod, owner, &mt.RPCError{
				ErrorCode:    rpcErr.Code,
				ErrorMessage: rpcErr.Message,
			}, nil)
		}
		s.log.Info("RPC internal error", append(fields, zap.Error(dispatchErr))...)
		return s.publishRPCResult(c, msgID, effectiveMethod, owner, &mt.RPCError{
			ErrorCode:    500,
			ErrorMessage: "INTERNAL",
		}, nil)
	}

	s.log.Info("RPC handled", fields...)
	return s.publishRPCResult(c, msgID, effectiveMethod, owner, result, postresponse.Take(ctx))
}

var errRPCResultRetentionHandoff = errors.New("mtproto rpc result retention handoff failed")

type rpcResultRetentionHandoff func(*encodedOutboundMessage, error) error

// publishRPCResult ends the inbound worker's ownership at bounded egress
// admission. Physical delivery is thereafter owned either by the single
// outbound actor or, under retained-byte saturation, by a fenced completed-cache
// entry that the replacement connection can replay without rerunning business.
func (s *Server) publishRPCResult(
	c *Conn,
	reqMsgID int64,
	method string,
	owner *rpcResultOwnerLease,
	result bin.Encoder,
	afterDelivered func(),
) error {
	if result == nil {
		result = &mt.RPCError{ErrorCode: 500, ErrorMessage: "INTERNAL"}
	}
	prepareTimeout := c.writeTimeout
	if prepareTimeout <= 0 || prepareTimeout > 5*time.Second {
		prepareTimeout = 5 * time.Second
	}
	prepareCtx, cancel := context.WithTimeout(context.Background(), prepareTimeout)
	defer cancel()
	prepareEncoded := func(encoded *encodedOutboundMessage) (outboundPriority, bool) {
		if owner != nil && owner.Delivery() != nil {
			// The owner-level delivery coordinator exists before the handler starts, so
			// an initConnection rewrap can retarget even while result encoding is still
			// pending. The encoded body itself remains immutable; the actor clones only
			// the 12-byte rpc_result prefix when it snapshots the physical target.
			encoded.delivery = owner.Delivery()
		}
		if afterDelivered != nil {
			encoded.setDeliveryHook(afterDelivered)
		}
		priority := rpcResultPriority(method, encoded)
		encoded.priority = priority
		if metrics, ok := s.metrics.(RPCResultMetrics); ok {
			metrics.RPCResultPrepared(method, priority.String(), encoded.uncompressedBytes, len(encoded.body), encoded.compressed)
		}
		visible := encoded.compressed || priority == outboundPriorityCritical || priority == outboundPriorityBulk
		return priority, visible
	}

	// A successful business result may never leave the encode slot as an
	// unaccounted []byte. If the primary 512MiB retained-body budget is full, make
	// overload terminal for this physical generation and publish the exact result
	// into the independently bounded completed cache before releasing the slot.
	retainForReplay := func(encoded *encodedOutboundMessage, admissionErr error) error {
		if s == nil || s.rpcResults == nil || c == nil || encoded == nil || reqMsgID == 0 {
			return errors.New("rpc result completed cache is unavailable")
		}
		if int64(len(encoded.body)) > s.rpcResults.completedBytes.max {
			// Every transport-legal result fits the production completed cache by the
			// compile-time invariant in rpc_result_cache.go. A test/custom cache that
			// violates it cannot safely complete this flight, so fail fast while the
			// body is still confined to the encode slot.
			panic(fmt.Sprintf(
				"mtprotoedge: encoded rpc result exceeds completed-cache budget: body=%d max=%d",
				len(encoded.body), s.rpcResults.completedBytes.max,
			))
		}
		priority, visible := prepareEncoded(encoded)
		if owner != nil && !owner.HandOff() {
			return ErrRPCResultFlightInvalid
		}
		started := time.Now()
		encoded.markReplayable()
		// Put may expose a completed result only after the old logical connection
		// is irreversibly unable to accept another same-generation request.
		c.fenceUndeliveredRPCResult()
		s.storeRPCResult(c, reqMsgID, encoded)
		latency := time.Since(started)
		if metrics, ok := s.metrics.(RPCResultMetrics); ok {
			metrics.RPCResultDelivered(method, latency, len(encoded.body), admissionErr)
		}
		resultLogLevel := zap.DebugLevel
		if visible {
			resultLogLevel = zap.InfoLevel
		}
		if checked := s.log.Check(resultLogLevel, "RPC result retained for replay after egress saturation"); checked != nil {
			checked.Write(
				zap.String("method", method), zap.Int64("req_msg_id", reqMsgID),
				zap.Int64("delivered_req_msg_id", encoded.writtenRequestID()),
				zap.String("auth_key_id", c.authKeyHex), zap.Int64("session_id", c.sessionID),
				zap.Int("wire_bytes", len(encoded.body)), zap.Bool("gzip", encoded.compressed),
				zap.String("priority", priority.String()), zap.Error(admissionErr))
		}
		return nil
	}

	encoded, reserved, retained, err := s.encodeRPCResultReservedWithHandoffContext(
		prepareCtx, c, reqMsgID, result, retainForReplay,
	)
	if retained {
		return err
	}
	if errors.Is(err, errRPCResultRetentionHandoff) {
		c.fenceUndeliveredRPCResult()
		return err
	}
	if err != nil {
		s.log.Warn("Encode RPC result failed; publishing INTERNAL",
			zap.String("method", method), zap.Int64("req_msg_id", reqMsgID), zap.Error(err))
		afterDelivered = nil
		encoded, reserved, retained, err = s.encodeRPCResultReservedWithHandoffContext(
			prepareCtx, c, reqMsgID, &mt.RPCError{ErrorCode: 500, ErrorMessage: "INTERNAL"}, retainForReplay,
		)
		if retained {
			return err
		}
		if err != nil {
			c.fenceUndeliveredRPCResult()
			return err
		}
	}
	if encoded == nil || reserved == nil {
		c.fenceUndeliveredRPCResult()
		return errors.New("rpc result encode completed without tracked retention")
	}
	// Until enqueue transfers ownership, every exit must return the retained-byte
	// charge. A successful transfer clears the reservation and makes this a no-op.
	defer reserved.release()
	priority, visible := prepareEncoded(encoded)
	if owner != nil && !owner.HandOff() {
		return ErrRPCResultFlightInvalid
	}

	resultLogLevel := zap.DebugLevel
	if visible {
		// Keep ordinary small RPCs at debug, but make convergence and bulk/gzip
		// delivery visible in the default service logs. These are the
		// responses whose queueing and write latency diagnose startup Updating.
		resultLogLevel = zap.InfoLevel
	}
	egressStarted := time.Now()
	terminal := func(deliveryErr error) {
		latency := time.Since(egressStarted)
		deliveredReqMsgID := encoded.writtenRequestID()
		if metrics, ok := s.metrics.(RPCResultMetrics); ok {
			metrics.RPCResultDelivered(method, latency, len(encoded.body), deliveryErr)
		}
		if deliveryErr != nil {
			encoded.markReplayable()
			c.fenceUndeliveredRPCResult()
			s.storeRPCResult(c, reqMsgID, encoded)
			if checked := s.log.Check(resultLogLevel, "RPC result delivery fenced for replay"); checked != nil {
				checked.Write(
					zap.String("method", method), zap.Int64("req_msg_id", reqMsgID),
					zap.Int64("delivered_req_msg_id", deliveredReqMsgID),
					zap.String("auth_key_id", c.authKeyHex), zap.Int64("session_id", c.sessionID),
					zap.Int("wire_bytes", len(encoded.body)), zap.Bool("gzip", encoded.compressed),
					zap.Error(deliveryErr))
			}
			return
		}
		encoded.markDelivered()
		s.storeRPCResult(c, reqMsgID, encoded)
		if checked := s.log.Check(resultLogLevel, "RPC result delivered"); checked != nil {
			checked.Write(
				zap.String("method", method), zap.Int64("req_msg_id", reqMsgID),
				zap.Int64("delivered_req_msg_id", deliveredReqMsgID),
				zap.String("auth_key_id", c.authKeyHex), zap.Int64("session_id", c.sessionID),
				zap.Int("wire_bytes", len(encoded.body)), zap.Bool("gzip", encoded.compressed),
				zap.Duration("egress_latency", latency))
		}
	}
	encoded.markQueued()
	if err := c.enqueueEncodedDeliveryReserved(prepareCtx, proto.MessageServerResponse, encoded, priority, terminal, reserved); err != nil {
		// HandOff already made the egress path the terminal owner. No bytes were
		// admitted, so fence this generation before publishing a replayable result.
		terminal(err)
		return err
	}
	if checked := s.log.Check(resultLogLevel, "RPC result admitted"); checked != nil {
		checked.Write(
			zap.String("method", method), zap.Int64("req_msg_id", reqMsgID),
			zap.Int("wire_bytes", len(encoded.body)), zap.Int("inner_bytes", encoded.uncompressedBytes),
			zap.Bool("gzip", encoded.compressed), zap.String("priority", priority.String()))
	}
	return nil
}

// sendResult 把 RPC 结果包成 rpc_result 并加密回发。
func (s *Server) sendResult(ctx context.Context, c *Conn, reqMsgID int64, result bin.Encoder) error {
	if result == nil {
		result = &mt.RPCError{ErrorCode: 500, ErrorMessage: "INTERNAL"}
	}
	encoded, err := s.encodeRPCResultContext(ctx, c, reqMsgID, result)
	if err != nil {
		// The business operation has already crossed atomic admission. Convert an
		// invalid result encoder into one deterministic terminal RPC error instead of
		// aborting the flight and allowing a reconnect to execute the operation again.
		s.log.Warn("Encode RPC result failed; sending INTERNAL", zap.Int64("req_msg_id", reqMsgID), zap.Error(err))
		encoded, err = s.encodeRPCResultContext(ctx, c, reqMsgID, &mt.RPCError{
			ErrorCode:    500,
			ErrorMessage: "INTERNAL",
		})
		if err != nil {
			c.fenceUndeliveredRPCResult()
			return err
		}
	}
	if err := c.SendEncoded(ctx, proto.MessageServerResponse, encoded); err != nil {
		// A completed result may be published before delivery only after this logical
		// Conn is irreversibly fenced. SendEncoded has non-writing failure paths
		// (queue/context/scratch deadline); without this terminal barrier a later
		// same-Conn duplicate would be ACKed while no result can ever arrive.
		c.fenceUndeliveredRPCResult()
		encoded.markReplayable()
		s.storeRPCResult(c, reqMsgID, encoded)
		return err
	}
	encoded.markDelivered()
	// On a live Conn, completed means the rpc_result has reached the reliable byte
	// stream. Same-physical duplicates can therefore be ACK-only without data loss.
	s.storeRPCResult(c, reqMsgID, encoded)
	return nil
}

// sendCachedRPCResult preserves the delivery half of the rpc_result invariant
// for completed-flight replays: either the cached result reaches this physical
// byte stream, or this logical Conn is fenced so a replacement may retry it.
func (s *Server) sendCachedRPCResult(ctx context.Context, c *Conn, encoded *encodedOutboundMessage) error {
	return s.sendCachedRPCResultWithHook(ctx, c, encoded, nil)
}

func (s *Server) sendCachedRPCResultWithHook(
	ctx context.Context,
	c *Conn,
	encoded *encodedOutboundMessage,
	afterSuccessfulDelivery func() error,
) error {
	if encoded == nil {
		c.fenceUndeliveredRPCResult()
		return errors.New("nil cached rpc_result")
	}
	attempt, reserved, err := c.cloneRPCResultForRequestReserved(encoded, encoded.reqMsgID, false)
	if err != nil {
		c.failOutboundBudget(err)
		c.fenceUndeliveredRPCResult()
		return err
	}
	// take clears the producer reservation after actor admission. Every earlier
	// return, including a closed connection, must drop the replay pin here.
	defer reserved.release()
	pendingLogicalRestore := attempt.pendingLogicalDeliveryHook()
	var finishRestore func()
	if afterSuccessfulDelivery != nil || pendingLogicalRestore {
		finishRestore = c.beginRPCReplayRestore()
		defer finishRestore()
	}
	// Cached replay owns its delivery-gated state synchronously. Calling the
	// lower send primitive avoids reserving the process-wide asynchronous hook
	// executor; the logical hook is claimed only after this physical write wins.
	if err := c.sendOutboundWithTerminalReserved(
		ctx, proto.MessageServerResponse, nil, attempt, false, nil, reserved,
	); err != nil {
		c.fenceUndeliveredRPCResult()
		attempt.markReplayable()
		return err
	}
	// Physical success is irrevocable even if the caller's send context expires
	// at the same instant. Give the ordered restore its own bounded lifetime.
	restoreCtx, cancelRestore := boundedRPCReplayRestoreContext(context.Background())
	defer cancelRestore()
	logicalRestore, claimErr := attempt.claimLogicalDeliveryHook(restoreCtx, false)
	attempt.markDelivered()
	if claimErr != nil {
		// Another replay owns Claimed/InProgress state (or a retarget still owns
		// the sticky deferral). Fence before the deferred barrier is released; a
		// later physical generation may wait for Done and replay the same bytes.
		c.fenceUndeliveredRPCResult()
		return fmt.Errorf("wait for cached rpc_result logical restore: %w", claimErr)
	}
	return s.runBoundedRPCReplayRestore(
		restoreCtx, c, "cached rpc_result", logicalRestore, afterSuccessfulDelivery,
	)
}

// composeRPCReplayRestore keeps replacement-connection metadata first while
// still guaranteeing that the original handler's delivery-gated cursor/outbox
// work runs after a physical replay even when metadata restoration reports an
// error. runRPCReplayRestore provides panic isolation and terminal fencing for
// the combined ordered transaction.
func composeRPCReplayRestore(logical func(), replacement func() error) func() error {
	if logical == nil && replacement == nil {
		return nil
	}
	return func() (err error) {
		if logical != nil {
			defer logical()
		}
		if replacement != nil {
			return replacement()
		}
		return nil
	}
}

// runRPCReplayRestore is the panic/error boundary executed only by the fixed-
// capacity runner in rpc_replay_restore.go. Replay restore may touch auth/session
// stores and membership state; its caller holds the per-Conn scheduler barrier.
// Any error or panic fences the partially restored physical generation so a
// replacement can retry from the immutable completed result.
func (s *Server) runRPCReplayRestore(c *Conn, source string, restore func() error) (err error) {
	if restore == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("restore replay state after %s: panic: %v", source, recovered)
			if s != nil && s.log != nil {
				s.log.Error("Exact RPC replay state restore panicked",
					zap.String("source", source), zap.ByteString("stack", debug.Stack()), zap.Any("panic", recovered))
			}
		}
		if err != nil {
			if s != nil && s.log != nil {
				s.log.Warn("Exact RPC replay state restore failed", zap.String("source", source), zap.Error(err))
			}
			c.fenceUndeliveredRPCResult()
		}
	}()
	return restore()
}

// encodeRPCResult 编码 rpc_result。内层对象与 rpc_result 头（type_id + req_msg_id）
// 一次性编码进同一 buffer——旧实现先编码内层、再经 proto.Result.Encode 整体拷贝一遍，
// 每条响应多一份全量 body 拷贝。生成式结果携带完整 Layer profile + result TypeRef
// 绑定，后续发送、缓存和重放均只能用于同一精确 profile；package 测试保留的 legacy
// handler 也必须先绑定 generated admitted call，生产路径不存在旧转码桥。
func (s *Server) encodeRPCResult(c *Conn, reqMsgID int64, result bin.Encoder) (*encodedOutboundMessage, error) {
	return s.encodeRPCResultContext(context.Background(), c, reqMsgID, result)
}

func (s *Server) encodeRPCResultContext(ctx context.Context, c *Conn, reqMsgID int64, result bin.Encoder) (*encodedOutboundMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var encoded *encodedOutboundMessage
	err := withOutboundEncodeSlot(ctx, nil, func() error {
		var err error
		encoded, err = s.encodeRPCResultWithoutSlot(ctx, c, reqMsgID, result)
		return err
	})
	return encoded, err
}

// encodeRPCResultReservedContext keeps the process-wide encode slot until the
// completed immutable body is charged to the shared retained-byte budget. This
// closes the otherwise unbounded interval in which every RPC worker could own
// a large encoded result that neither the inbound nor outbound budget tracked.
func (s *Server) encodeRPCResultReservedContext(
	ctx context.Context,
	c *Conn,
	reqMsgID int64,
	result bin.Encoder,
) (*encodedOutboundMessage, *outboundBodyReservation, error) {
	encoded, reserved, _, err := s.encodeRPCResultReservedWithHandoffContext(ctx, c, reqMsgID, result, nil)
	return encoded, reserved, err
}

// encodeRPCResultReservedWithHandoffContext has only two successful ownership
// outcomes for a completed body: a primary outbound reservation, or a caller
// handoff that synchronously installs another bounded owner while the encode slot
// is still held. A failed/no handoff clears encoded before the slot is released.
func (s *Server) encodeRPCResultReservedWithHandoffContext(
	ctx context.Context,
	c *Conn,
	reqMsgID int64,
	result bin.Encoder,
	handoff rpcResultRetentionHandoff,
) (*encodedOutboundMessage, *outboundBodyReservation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var (
		encoded  *encodedOutboundMessage
		reserved *outboundBodyReservation
		retained bool
	)
	err := withOutboundEncodeSlot(ctx, nil, func() error {
		var err error
		encoded, err = s.encodeRPCResultWithoutSlot(ctx, c, reqMsgID, result)
		if err != nil {
			return err
		}
		budget := c.outboundMessageBudget(encoded.typeID, false)
		bytes := len(encoded.body)
		if budget.reserve(bytes) {
			reserved = &outboundBodyReservation{budget: budget, bytes: bytes}
			return nil
		}
		if handoff != nil {
			admissionErr := fmt.Errorf("reserve encoded rpc result: %w", ErrOutboundTrackedBudget)
			if err := handoff(encoded, admissionErr); err != nil {
				encoded = nil
				return fmt.Errorf("%w: %w", errRPCResultRetentionHandoff, errors.Join(admissionErr, err))
			}
			retained = true
			// The handoff owns the only surviving pointer. Do not return a second
			// producer reference after the encode slot releases; the completed cache
			// may independently evict the entry under its bounded policy.
			encoded = nil
			return admissionErr
		}
		// Non-publish callers have no alternate bounded owner. They may wait for
		// the caller's deadline, but on failure the body is discarded in-slot.
		if err := budget.waitReserve(ctx, nil, bytes); err != nil {
			encoded = nil
			return fmt.Errorf("reserve encoded rpc result: %w", err)
		}
		reserved = &outboundBodyReservation{budget: budget, bytes: bytes}
		return nil
	})
	if err != nil && !retained && reserved == nil {
		encoded = nil
	}
	return encoded, reserved, retained, err
}

func (s *Server) encodeRPCResultWithoutSlot(ctx context.Context, c *Conn, reqMsgID int64, result bin.Encoder) (*encodedOutboundMessage, error) {
	var layerBinding *outboundLayerBinding
	exactResult, exactLayerResult := result.(exactLayerRPCResultEncoder)
	layerInvariantResult := isLayerInvariantRPCResultEncoder(result)
	if !exactLayerResult && !layerInvariantResult {
		return nil, ErrOutboundLayerBindingRequired
	}
	if exactLayerResult {
		binding := exactResult.exactLayerRPCResultBinding()
		layerBinding = &binding
		if err := validateOutboundLayerBinding(c, &encodedOutboundMessage{layer: layerBinding}); err != nil {
			return nil, fmt.Errorf("bind exact layer rpc result: %w", err)
		}
	}
	// Encode the ordinary exact/no-gzip path directly behind the rpc_result
	// prefix. This avoids both the old generated Prepare snapshot and another
	// full-body copy merely to prepend the 12-byte envelope.
	var envelope bin.Buffer
	envelope.PutID(proto.ResultTypeID)
	envelope.PutLong(reqMsgID)
	if err := result.Encode(&envelope); err != nil {
		return nil, fmt.Errorf("encode rpc result: %w", err)
	}
	envelopeInner := envelope.Raw()[12:]
	innerBody := envelopeInner
	// Inputs above gotd's decompression ceiling can never be gzip_packed. Reject
	// them before allocating a second transport-envelope-sized buffer.
	if len(innerBody) > rpcResultGZIPMaxInputBytes && len(innerBody) > maxOutboundBodyBytes-12 {
		return nil, fmt.Errorf("%w: body=%d limit=%d", ErrOutboundMessageTooLarge, len(innerBody)+12, maxOutboundBodyBytes)
	}

	wireInner, compressed, err := encodeAdaptiveRPCResultInner(ctx, nil, innerBody)
	if err != nil {
		return nil, fmt.Errorf("compress rpc result: %w", err)
	}
	if len(wireInner) > maxOutboundBodyBytes-12 {
		return nil, fmt.Errorf("%w: body=%d limit=%d", ErrOutboundMessageTooLarge, len(wireInner)+12, maxOutboundBodyBytes)
	}
	body := envelope.Raw()
	if compressed || !sameBacking(wireInner, envelopeInner) {
		var out bin.Buffer
		out.PutID(proto.ResultTypeID)
		out.PutLong(reqMsgID)
		out.Put(wireInner)
		body = out.Raw()
	}
	return &encodedOutboundMessage{
		typeID: proto.ResultTypeID, body: body, reqMsgID: reqMsgID,
		compressed: compressed, uncompressedBytes: len(innerBody), delivery: newRPCResultDelivery(0),
		layer: layerBinding, layerInvariant: layerInvariantResult,
	}, nil
}

func (s *Server) cachedRPCResult(c *Conn, reqMsgID int64) (*encodedOutboundMessage, bool) {
	if s == nil || s.rpcResults == nil || c == nil {
		return nil, false
	}
	return s.rpcResults.Get(c.authKeyID, c.sessionID, reqMsgID)
}

func (s *Server) replayRPCResultByRequest(ctx context.Context, c *Conn, reqMsgID int64) error {
	if c == nil {
		return nil
	}
	if resent, err := c.ResendByRequest(ctx, reqMsgID); err != nil {
		c.fenceUndeliveredRPCResult()
		return err
	} else if resent {
		s.log.Debug("Resent connection cached rpc_result for duplicate msg_id", zap.Int64("msg_id", reqMsgID))
		return nil
	}
	if cached, ok := s.cachedRPCResult(c, reqMsgID); ok {
		if err := s.sendCachedRPCResult(ctx, c, cached); err != nil {
			return err
		}
		s.log.Debug("Resent session cached rpc_result for duplicate msg_id", zap.Int64("msg_id", reqMsgID))
	}
	return nil
}

func (s *Server) storeRPCResult(c *Conn, reqMsgID int64, encoded *encodedOutboundMessage) {
	if s == nil || s.rpcResults == nil || c == nil {
		return
	}
	s.rpcResults.Put(c.authKeyID, c.sessionID, reqMsgID, encoded)
}

// sendPong 回复 mt.PingRequest / mt.PingDelayDisconnectRequest。
func (s *Server) sendPong(ctx context.Context, c *Conn, reqMsgID, pingID int64) error {
	return c.SendAsync(ctx, proto.MessageServerResponse, &mt.Pong{MsgID: reqMsgID, PingID: pingID})
}

// sendFutureSalts 回复 MTProto get_future_salts。
//
// 第一阶段只维护当前 auth key 的权威 server_salt，因此返回当前 salt 的有效窗口。
// 后续如引入 salt rotation，可在这里扩展为多条未来 salt。
func (s *Server) sendFutureSalts(ctx context.Context, c *Conn, reqMsgID int64, num int) error {
	if num < 0 {
		num = 0
	}
	if num > 1 {
		num = 1
	}
	now := int(s.clock.Now().Unix())
	salts := make([]mt.FutureSalt, 0, num)
	if num == 1 {
		salts = append(salts, mt.FutureSalt{
			ValidSince: now - 300,
			ValidUntil: now + 24*60*60,
			Salt:       c.salt,
		})
	}
	return c.SendAsync(ctx, proto.MessageServerResponse, &mt.FutureSalts{
		ReqMsgID: reqMsgID,
		Now:      now,
		Salts:    salts,
	})
}

// sendNewSessionCreated 在连接首个加密消息后通知客户端新 session 已建立。
// unique_id 必须每个 server session 实例独立：客户端按 unique_id 去重，
// 复用同一值会让断线重连后的 new_session_created 被吞掉，错过的差分补拉
// （Android 收到后才调 getDifference）随之丢失。
func (s *Server) sendNewSessionCreated(ctx context.Context, c *Conn, firstMsgID int64) error {
	// This notification changes the client's request map and update recovery
	// state. Unlike best-effort ack/pong traffic, it must be written successfully
	// before the corresponding RPC batch starts executing.
	return c.SendRequiredControl(ctx, proto.MessageFromServer, &mt.NewSessionCreated{
		FirstMsgID: firstMsgID,
		UniqueID:   s.newServerSessionUID(),
		ServerSalt: c.salt,
	})
}

func (s *Server) newServerSessionUID() int64 {
	var b [8]byte
	if _, err := io.ReadFull(s.rand, b[:]); err == nil {
		return int64(binary.LittleEndian.Uint64(b[:]))
	}
	return s.clock.Now().UnixNano()
}

// sendAck 确认收到客户端 content-related 消息。
func (s *Server) sendAck(ctx context.Context, c *Conn, ids ...int64) error {
	return c.SendAsync(ctx, proto.MessageFromServer, &mt.MsgsAck{MsgIDs: ids})
}

// sendMsgsStateInfo 回复 msgs_state_req/msg_resend_req。
func (s *Server) sendMsgsStateInfo(ctx context.Context, c *Conn, reqMsgID int64, info []byte) error {
	return c.SendAsync(ctx, proto.MessageServerResponse, &mt.MsgsStateInfo{ReqMsgID: reqMsgID, Info: info})
}

func (s *Server) sendDestroySession(ctx context.Context, c *Conn, sessionID int64) error {
	removed, removedDurable := false, false
	if sessionID != c.sessionID {
		if deleter, ok := s.layerRPC.(LayerRPCDurableSessionProfileDeleter); ok {
			var err error
			removedDurable, err = deleter.DeleteNegotiatedSessionLayerEvidence(ctx, c.authKeyID, sessionID)
			if err != nil {
				return fmt.Errorf("delete durable exact session Layer evidence: %w", err)
			}
		}
		if s.conns != nil {
			removed = s.conns.DestroySessionForAuthKey(c.authKeyID, sessionID)
		}
	}
	if removed || removedDurable {
		return c.Send(ctx, proto.MessageServerResponse, &mt.DestroySessionOk{SessionID: sessionID})
	}
	return c.Send(ctx, proto.MessageServerResponse, &mt.DestroySessionNone{SessionID: sessionID})
}

// sendBadMsg 通知客户端消息存在协议层错误（msg_id/seqno 非法）。
func (s *Server) sendBadMsg(ctx context.Context, c *Conn, badMsgID int64, badSeqno int32, code int) error {
	return c.SendAsync(ctx, proto.MessageFromServer, &mt.BadMsgNotification{
		BadMsgID:    badMsgID,
		BadMsgSeqno: int(badSeqno),
		ErrorCode:   code,
	})
}

// sendBadServerSalt 通知客户端修正 server_salt（error_code 48）。
func (s *Server) sendBadServerSalt(ctx context.Context, c *Conn, badMsgID int64, badSeqno int32, newSalt int64) error {
	return c.SendRequiredControl(ctx, proto.MessageFromServer, &mt.BadServerSalt{
		BadMsgID:      badMsgID,
		BadMsgSeqno:   int(badSeqno),
		ErrorCode:     48,
		NewServerSalt: newSalt,
	})
}

// typeName 返回 TL TypeID 的可读名称，未知时回退到 hex。
func (s *Server) typeName(id uint32) string {
	if name := s.types.Get(id); name != "" {
		return name
	}
	return fmt.Sprintf("%#x", id)
}

func validateClientEnvelope(now time.Time, msgID int64, seqNo int32, typeID uint32) int {
	if !validClientMessageIDBits(msgID) {
		return badMsgIDInvalidBits
	}
	msgTime := proto.MessageID(msgID).Time()
	if msgTime.Before(now.Add(-300 * time.Second)) {
		return badMsgIDTooLow
	}
	if msgTime.After(now.Add(30 * time.Second)) {
		return badMsgIDTooHigh
	}
	if clientMessageAllowsEitherSeqParity(typeID) {
		return 0
	}
	if clientMessageNeedsAck(typeID) {
		if seqNo%2 == 0 {
			return badMsgSeqNotOdd
		}
	} else if seqNo%2 != 0 {
		return badMsgSeqNotEven
	}
	return 0
}

func validateClientContainerEnvelope(msgID int64, seqNo int32, typeID uint32) int {
	if !validClientMessageIDBits(msgID) {
		return badMsgIDInvalidBits
	}
	if clientMessageAllowsEitherSeqParity(typeID) {
		return 0
	}
	if clientMessageNeedsAck(typeID) {
		if seqNo%2 == 0 {
			return badMsgSeqNotOdd
		}
	} else if seqNo%2 != 0 {
		return badMsgSeqNotEven
	}
	return 0
}

func clientMessageAllowsEitherSeqParity(typeID uint32) bool {
	switch typeID {
	case mt.MsgsAckTypeID,
		mt.PingDelayDisconnectRequestTypeID,
		// get_future_salts 的 seqno 奇偶在客户端间不一致：部分客户端按内容消息发奇数，
		// gotd 按服务消息发偶数。两者都合法（官方服务器都接受），故不在此卡奇偶，避免
		// 误判 bad_msg 触发客户端重连风暴。ack/content 行为仍由 clientMessageNeedsAck 决定。
		mt.GetFutureSaltsRequestTypeID:
		return true
	default:
		return false
	}
}

func clientMessageNeedsAck(typeID uint32) bool {
	switch typeID {
	case proto.MessageContainerTypeID,
		mt.MsgsAckTypeID,
		mt.PingDelayDisconnectRequestTypeID,
		mt.DestroySessionRequestTypeID,
		mt.HTTPWaitRequestTypeID,
		mt.BadMsgNotificationTypeID,
		mt.BadServerSaltTypeID,
		mt.MsgsAllInfoTypeID,
		mt.MsgsStateInfoTypeID,
		mt.MsgDetailedInfoTypeID,
		mt.MsgNewDetailedInfoTypeID:
		return false
	default:
		return true
	}
}

func (cs *connState) seenRecord(msgID int64) (clientMsgRecord, bool) {
	record, ok := cs.seen[msgID]
	return record, ok
}

func (cs *connState) validateSeq(msgID int64, seqNo int32, content bool) int {
	if !content {
		return 0
	}
	// 快路径：msg_id 与 seq_no 都严格高于已接受 content 高水位时，任何已见记录都不可能
	// 与本条构成 too_low/too_high 反转，免去 O(len(seen)) 全扫描（正常客户端恒命中）。
	if msgID > cs.maxContentMsgID && seqNo > cs.maxContentSeqNo {
		return 0
	}
	for seenMsgID, record := range cs.seen {
		if !record.content {
			continue
		}
		if seenMsgID < msgID && record.seqNo >= seqNo {
			return badMsgSeqTooLow
		}
		if seenMsgID > msgID && record.seqNo <= seqNo {
			return badMsgSeqTooHigh
		}
	}
	return 0
}

func (cs *connState) trackInbound(msgID int64, seqNo int32, content, service bool, state byte) {
	cs.seen[msgID] = clientMsgRecord{
		state:   state,
		seqNo:   seqNo,
		content: content,
		service: service,
	}
	if content {
		if msgID > cs.maxContentMsgID {
			cs.maxContentMsgID = msgID
		}
		if seqNo > cs.maxContentSeqNo {
			cs.maxContentSeqNo = seqNo
		}
	}
	cs.order = append(cs.order, msgID)
	if msgID < cs.minSeen {
		cs.minSeen = msgID
	}
	if msgID > cs.maxSeen {
		cs.maxSeen = msgID
	}
	if len(cs.order) > maxTrackedClientMsgIDs {
		oldest := cs.order[0]
		cs.order = cs.order[1:]
		delete(cs.seen, oldest)
		if oldest == cs.minSeen || oldest == cs.maxSeen {
			cs.recomputeRange()
		}
	}
}

func (cs *connState) stateInfo(msgIDs []int64) []byte {
	info := make([]byte, len(msgIDs))
	if len(cs.seen) == 0 {
		for i := range info {
			info[i] = msgStateUnknown
		}
		return info
	}
	for i, id := range msgIDs {
		if id < cs.minSeen {
			info[i] = msgStateUnknown
			continue
		}
		if id > cs.maxSeen {
			info[i] = msgStateNotReceivedHigh
			continue
		}
		record, ok := cs.seen[id]
		if !ok {
			info[i] = msgStateNotReceived
			continue
		}
		info[i] = record.state
	}
	return info
}

func (cs *connState) recomputeRange() {
	cs.minSeen = math.MaxInt64
	cs.maxSeen = 0
	for id := range cs.seen {
		if id < cs.minSeen {
			cs.minSeen = id
		}
		if id > cs.maxSeen {
			cs.maxSeen = id
		}
	}
	if len(cs.seen) == 0 {
		cs.minSeen = math.MaxInt64
	}
}
