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
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/transport"

	"telesrv/internal/compat/layerwire"
	"telesrv/internal/observability/dbtrace"
	"telesrv/internal/postresponse"
	"telesrv/internal/store"
)

// connState 是单连接的 MTProto 运行态。
type connState struct {
	sentCreated bool
	seen        map[int64]clientMsgRecord // 已处理的 client msg_id，用于幂等和 msgs_state_req
	order       []int64
	minSeen     int64
	maxSeen     int64
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
	// A decoded container descriptor is 48 bytes on 64-bit Go today. Charge 64 bytes per entry
	// before allocating the exact-size slice so allocator rounding and future field growth remain
	// inside the process-wide inbound budget. Message bodies stay as zero-copy views of the already
	// charged plaintext frame/gzip expansion.
	containerDescriptorBudgetBytes = 64

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

// handleEncrypted 解密加密消息，按需注册连接，处理服务消息并分发明文 payload。
// 返回（可能新建/更新的）当前连接对象，供 serveConn 维护生命周期。
// fetchedKey 非 nil 表示本帧的 auth key 是刚从 AuthKeyStore 查出的（首帧/换 auth key/被销毁
// 后回落）；为 nil 表示走快路径——serveConn 判定 current 仍持同一未销毁的 auth key，直接复用
// current.key/current.salt 解密，既不回查 AuthKeyStore 也不重建 store.AuthKeyData。
// plain 是 serveConn 持有的复用明文缓冲，frame 的 slice 仅在下一帧解密前有效。
func (s *Server) handleEncrypted(ctx context.Context, tc transport.Conn, cs *connState, current *Conn, fetchedKey *store.AuthKeyData, b, plain *bin.Buffer) (*Conn, error) {
	var key crypto.AuthKey
	var serverSalt int64
	if fetchedKey != nil {
		key = crypto.AuthKey{Value: crypto.Key(fetchedKey.Value), ID: fetchedKey.ID}
		serverSalt = fetchedKey.ServerSalt
	} else {
		// 快路径：复用已建立连接缓存的密钥与盐（同一 auth key 的后续帧，含同连接换 session）。
		key = current.key
		serverSalt = current.salt
	}

	frame, err := decryptClientFrame(key, b, plain)
	if err != nil {
		return current, fmt.Errorf("decrypt: %w", err)
	}

	if frame.salt != serverSalt {
		c := current
		temp := false
		if c == nil || c.sessionID != frame.sessionID || c.authKeyID != key.ID {
			c = s.newConn(tc, key, frame.sessionID, serverSalt)
			temp = true
		}
		err := s.sendBadServerSalt(ctx, c, frame.messageID, frame.seqNo, serverSalt)
		if temp {
			c.Close()
		}
		return current, err
	}

	// 首个加密消息或 session 变化时（重新）注册连接到 SessionManager。
	if current == nil || current.sessionID != frame.sessionID || current.authKeyID != key.ID {
		if current != nil {
			cs.reset()
		}
		if current != nil {
			s.conns.Unregister(current)
			current.Close()
		}
		current = s.newConn(tc, key, frame.sessionID, serverSalt)
		// 注册即播种协商 layer：新 Conn 的 clientLayer 为 0（=canonical 227），若等到
		// 首条 RPC 的 Dispatch 返回后才刷新，重连老客户端在首条 RPC handler 执行期间
		// 收到的 pending flush / 并发 push 会漏降级。进程内重连时 rpc 层留有
		// (auth_key, session) / auth_key 两级协商记录，这里一次查询即可闭合该空窗。
		if s.rpc != nil {
			if layer, ok := s.rpc.NegotiatedLayer(current.authKeyID, current.sessionID); ok {
				current.SetClientLayer(layer)
			}
		}
		s.conns.Register(current)
	}

	s.maybePersistSession(ctx, current, frame.sessionID, key.ID, serverSalt)

	body := frame.data
	typeID, err := (&bin.Buffer{Buf: body}).PeekID()
	if err != nil {
		return current, fmt.Errorf("peek encrypted payload type id: %w", err)
	}
	if code := validateClientEnvelope(s.clock.Now(), frame.messageID, frame.seqNo, typeID); code != 0 {
		s.log.Debug("Sending bad_msg_notification",
			zap.Int64("msg_id", frame.messageID),
			zap.Int32("seq_no", frame.seqNo),
			zap.Uint32("type_id", typeID),
			zap.Int("code", code),
		)
		return current, s.sendBadMsg(ctx, current, frame.messageID, frame.seqNo, code)
	}
	if err := sendQuickAckIfRequested(ctx, tc, key, frame.plaintext, s.writeTimeout); err != nil {
		return current, err
	}

	content := clientMessageNeedsAck(typeID)
	if record, ok := cs.seenRecord(frame.messageID); ok {
		s.log.Debug("Duplicate msg_id; replay cached result if available", zap.Int64("msg_id", frame.messageID))
		if err := s.replayRPCResultByRequest(ctx, current, frame.messageID); err != nil {
			return current, err
		}
		if !record.content {
			return current, nil
		}
		return current, s.sendAck(ctx, current, frame.messageID)
	}
	if code := cs.validateSeq(frame.messageID, frame.seqNo, content); code != 0 {
		s.log.Debug("Sending bad_msg_notification",
			zap.Int64("msg_id", frame.messageID),
			zap.Int32("seq_no", frame.seqNo),
			zap.Uint32("type_id", typeID),
			zap.Int("code", code),
		)
		return current, s.sendBadMsg(ctx, current, frame.messageID, frame.seqNo, code)
	}
	cs.track(frame.messageID, frame.seqNo, content, msgStateReceived)

	if !cs.sentCreated {
		cs.sentCreated = true
		s.log.Debug("Sending new_session_created", zap.Int64("msg_id", frame.messageID), zap.Int32("seq_no", frame.seqNo))
		if err := s.sendNewSessionCreated(ctx, current, frame.messageID); err != nil {
			return current, err
		}
	}

	var acks []int64
	if err := s.dispatch(ctx, cs, current, frame.messageID, frame.seqNo, &bin.Buffer{Buf: body}, &acks); err != nil {
		return current, err
	}
	if len(acks) > 0 {
		if err := s.sendAck(ctx, current, acks...); err != nil {
			return current, err
		}
	}
	return current, nil
}

// sessionSaveMinInterval 是单连接持久化 session 记录的最小间隔。把原本「每帧一次 Redis SET」
// 去抖到固定间隔——session 是软状态（生产无热读路径），只需周期刷新 last_seen/续 TTL。
const sessionSaveMinInterval = 30 * time.Second

// maybePersistSession 按 sessionSaveMinInterval 去抖持久化 session，失败只告警不断连。
// 原实现每帧同步 Save 且失败即断连：N 连接×帧率的 Redis 写放大 + Redis 抖动级联断连。
func (s *Server) maybePersistSession(ctx context.Context, c *Conn, sessionID int64, authKeyID [8]byte, salt int64) {
	if c == nil {
		return
	}
	now := s.clock.Now().Unix()
	if last := c.lastSessionSaveUnix.Load(); last != 0 && now-last < int64(sessionSaveMinInterval/time.Second) {
		return
	}
	c.lastSessionSaveUnix.Store(now)
	if err := s.sessions.Save(ctx, store.SessionData{
		ID:        sessionID,
		AuthKeyID: authKeyID,
		Salt:      salt,
		LastSeen:  now,
	}); err != nil {
		s.log.Warn("Persist session failed (non-fatal)",
			zap.Int64("session_id", sessionID),
			zap.Error(err),
		)
	}
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

// dispatch 处理一条明文消息：解包 container/gzip，处理服务消息，其余转 RPC 路由。
// content-related 消息（ping、RPC）的 msg_id 会收集到 acks 以便统一确认。
func (s *Server) dispatch(ctx context.Context, cs *connState, c *Conn, msgID int64, seqNo int32, b *bin.Buffer, acks *[]int64) error {
	expanded := 0
	return s.dispatchWithBudget(ctx, cs, c, msgID, seqNo, b, acks, dispatchBudget{expanded: &expanded})
}

type dispatchBudget struct {
	depth          int
	containerDepth int
	expanded       *int
}

func (s *Server) dispatchWithBudget(ctx context.Context, cs *connState, c *Conn, msgID int64, seqNo int32, b *bin.Buffer, acks *[]int64, budget dispatchBudget) error {
	if budget.depth > maxDispatchDepth {
		return fmt.Errorf("mtproto wrapper depth %d exceeds %d", budget.depth, maxDispatchDepth)
	}
	id, err := b.PeekID()
	if err != nil {
		return fmt.Errorf("peek type id: %w", err)
	}
	ackContent := func() {
		if clientMessageNeedsAck(id) {
			*acks = append(*acks, msgID)
		}
	}

	switch id {
	case proto.GZIPTypeID:
		data, releaseExpansion, err := s.decodeGZIPWithGlobalBudget(b)
		if err != nil {
			return fmt.Errorf("decode gzip: %w", err)
		}
		defer releaseExpansion()
		*budget.expanded += len(data)
		if *budget.expanded > maxDispatchExpandedBytes {
			return fmt.Errorf("cumulative gzip expansion %d exceeds %d", *budget.expanded, maxDispatchExpandedBytes)
		}
		budget.depth++
		return s.dispatchWithBudget(ctx, cs, c, msgID, seqNo, &bin.Buffer{Buf: data}, acks, budget)

	case proto.MessageContainerTypeID:
		if budget.containerDepth != 0 {
			return s.sendBadMsg(ctx, c, msgID, seqNo, badMsgContainer)
		}
		count, err := containerMessageCount(b)
		if err != nil {
			return fmt.Errorf("decode container count: %w", err)
		}
		if count > maxContainerMessages {
			return s.sendBadMsg(ctx, c, msgID, seqNo, badMsgContainer)
		}
		container, releaseContainer, err := s.decodeMessageContainerViews(b, count)
		if err != nil {
			return fmt.Errorf("decode container: %w", err)
		}
		defer releaseContainer()
		if code := validateClientContainer(msgID, seqNo, container); code != 0 {
			return s.sendBadMsg(ctx, c, msgID, seqNo, code)
		}
		budget.depth++
		budget.containerDepth++
		for i := range container.Messages {
			m := container.Messages[i]
			typeID, err := (&bin.Buffer{Buf: m.Body}).PeekID()
			if err != nil {
				return fmt.Errorf("peek container message type id: %w", err)
			}
			content := clientMessageNeedsAck(typeID)
			if record, ok := cs.seenRecord(m.ID); ok {
				if err := s.replayRPCResultByRequest(ctx, c, m.ID); err != nil {
					return err
				}
				if record.content {
					*acks = append(*acks, m.ID)
				}
				continue
			}
			if code := cs.validateSeq(m.ID, int32(m.SeqNo), content); code != 0 {
				return s.sendBadMsg(ctx, c, m.ID, int32(m.SeqNo), code)
			}
			cs.track(m.ID, int32(m.SeqNo), content, msgStateReceived)
			if err := s.dispatchWithBudget(ctx, cs, c, m.ID, int32(m.SeqNo), &bin.Buffer{Buf: m.Body}, acks, budget); err != nil {
				return err
			}
		}
		return nil

	case mt.PingRequestTypeID:
		var ping mt.PingRequest
		if err := ping.Decode(b); err != nil {
			return fmt.Errorf("decode ping: %w", err)
		}
		ackContent()
		return s.sendPong(ctx, c, msgID, ping.PingID)

	case mt.PingDelayDisconnectRequestTypeID:
		var ping mt.PingDelayDisconnectRequest
		if err := ping.Decode(b); err != nil {
			return fmt.Errorf("decode ping_delay_disconnect: %w", err)
		}
		ackContent()
		return s.sendPong(ctx, c, msgID, ping.PingID)

	case mt.GetFutureSaltsRequestTypeID:
		var req mt.GetFutureSaltsRequest
		if err := req.Decode(b); err != nil {
			return fmt.Errorf("decode get_future_salts: %w", err)
		}
		ackContent()
		return s.sendFutureSalts(ctx, c, msgID, req.Num)

	case mt.MsgsAckTypeID:
		if err := validateFirstVectorCount(b, maxServiceMessageIDs); err != nil {
			return fmt.Errorf("msgs_ack vector: %w", err)
		}
		var ack mt.MsgsAck
		if err := ack.Decode(b); err != nil {
			return fmt.Errorf("decode msgs_ack: %w", err)
		}
		c.AckServerMessages(ack.MsgIDs)
		s.log.Debug("Received msgs_ack", zap.Int64s("msg_ids", ack.MsgIDs))
		return nil

	case mt.MsgsStateReqTypeID:
		if err := validateFirstVectorCount(b, maxServiceMessageIDs); err != nil {
			return fmt.Errorf("msgs_state_req vector: %w", err)
		}
		var req mt.MsgsStateReq
		if err := req.Decode(b); err != nil {
			return fmt.Errorf("decode msgs_state_req: %w", err)
		}
		ackContent()
		outgoing, err := c.OutgoingStateInfo(ctx, req.MsgIDs)
		if err != nil {
			return err
		}
		return s.sendMsgsStateInfo(ctx, c, msgID, mergeStateInfo(outgoing, cs.stateInfo(req.MsgIDs)))

	case mt.MsgResendReqTypeID:
		if err := validateFirstVectorCount(b, maxServiceMessageIDs); err != nil {
			return fmt.Errorf("msg_resend_req vector: %w", err)
		}
		var req mt.MsgResendReq
		if err := req.Decode(b); err != nil {
			return fmt.Errorf("decode msg_resend_req: %w", err)
		}
		ackContent()
		outgoing, err := c.ResendMessages(ctx, req.MsgIDs)
		if err != nil {
			return err
		}
		return s.sendMsgsStateInfo(ctx, c, msgID, mergeStateInfo(outgoing, cs.stateInfo(req.MsgIDs)))

	case mt.MsgsStateInfoTypeID:
		reqMsgID, info, err := msgsStateInfoView(b)
		if err != nil {
			return fmt.Errorf("decode msgs_state_info: %w", err)
		}
		s.log.Debug("Received msgs_state_info", zap.Int64("req_msg_id", reqMsgID), zap.Int("len", len(info)))
		return nil

	case mt.MsgsAllInfoTypeID:
		count, info, err := msgsAllInfoView(b)
		if err != nil {
			return fmt.Errorf("decode msgs_all_info: %w", err)
		}
		if len(info) != count {
			return fmt.Errorf("decode msgs_all_info: info length %d does not match msg_ids %d", len(info), count)
		}
		s.log.Debug("Received msgs_all_info", zap.Int("msg_ids", count), zap.Int("len", len(info)))
		return nil

	case mt.DestroySessionRequestTypeID:
		var req mt.DestroySessionRequest
		if err := req.Decode(b); err != nil {
			return fmt.Errorf("decode destroy_session: %w", err)
		}
		ackContent()
		return s.sendDestroySession(ctx, c, req.SessionID)

	case mt.HTTPWaitRequestTypeID:
		var req mt.HTTPWaitRequest
		if err := req.Decode(b); err != nil {
			return fmt.Errorf("decode http_wait: %w", err)
		}
		s.log.Debug("Received http_wait",
			zap.Int("max_delay", req.MaxDelay),
			zap.Int("wait_after", req.WaitAfter),
			zap.Int("max_wait", req.MaxWait),
		)
		return nil

	case mt.RPCDropAnswerRequestTypeID:
		var req mt.RPCDropAnswerRequest
		if err := req.Decode(b); err != nil {
			return fmt.Errorf("decode rpc_drop_answer: %w", err)
		}
		ackContent()
		s.log.Debug("Received rpc_drop_answer", zap.Int64("req_msg_id", req.ReqMsgID))
		return s.sendResult(ctx, c, msgID, &mt.RPCAnswerUnknown{})

	case destroyAuthKeyRequestTypeID:
		var req destroyAuthKeyRequest
		if err := req.Decode(b); err != nil {
			return err
		}
		ackContent()
		s.log.Debug("Received destroy_auth_key", zap.String("auth_key_id", c.authKeyHex))
		// 真正销毁：删密钥库记录（每帧回查，删除后该 key 的入站帧立即失效）并主动
		// 断开同 key 的其他连接——出站推送用连接持有的密钥副本加密、不回查密钥库，
		// 不断开的话被销毁 key 的空闲连接仍能持续收到推送。发起连接除外：响应要
		// 先送达，它的下一帧会因密钥缺失自然断开。授权（authorizations）不在此清理，
		// destroy_auth_key 是 PFS 密钥轮换的清理动作，不等于登出。
		if err := s.authKeys.Delete(ctx, c.authKeyID); err != nil {
			s.log.Warn("Delete auth key failed", zap.String("auth_key_id", c.authKeyHex), zap.Error(err))
			return c.SendAsync(ctx, proto.MessageServerResponse, &destroyAuthKeyFail{})
		}
		// 标记密钥已销毁：发起连接被 CloseSessionsForRawAuthKeyExcept 排除（响应需先送达），
		// 它下一帧不能再走 serveConn 的密钥复用快路径，须回落到 Get→AuthKeyNotFound 自然失效。
		c.keyDestroyed.Store(true)
		s.conns.CloseSessionsForRawAuthKeyExcept(c.authKeyID, c.sessionID)
		return c.SendAsync(ctx, proto.MessageServerResponse, &destroyAuthKeyOk{})

	default:
		ackContent()
		return s.enqueueRPC(ctx, c, msgID, id, b)
	}
}

// decodeGZIPWithGlobalBudget reserves the maximum single-wrapper output before
// decompression starts. Once the actual size is known the excess reservation is
// returned, while the actual output remains charged through recursive dispatch.
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
	payload, _, err := tlBytesView(b.Buf[4:], -1)
	return payload, err
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
// copies. Bodies are immutable views of b and stay alive only for this synchronous dispatch;
// enqueueRPC takes its own budgeted copy before returning. Only the exact-size descriptor slice
// is new memory, and that allocation is reserved globally first.
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
	return proto.MessageContainer{Messages: messages}, release, nil
}

func msgsStateInfoView(b *bin.Buffer) (int64, []byte, error) {
	if b == nil || len(b.Buf) < 12 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	if got := binary.LittleEndian.Uint32(b.Buf[:4]); got != mt.MsgsStateInfoTypeID {
		return 0, nil, fmt.Errorf("unexpected constructor %#x", got)
	}
	info, _, err := tlBytesView(b.Buf[12:], maxServiceMessageIDs)
	if err != nil {
		return 0, nil, err
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
	info, _, err := tlBytesView(b.Buf[offset:], maxServiceMessageIDs)
	if err != nil {
		return 0, nil, err
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

// enqueueRPC 把一条 RPC 请求交给连接的 inbound 调度器。typeID 由 dispatch 传入
// （已 PeekID 过一次），method 只解析一次并随任务透传，避免同一请求三处重复 PeekID/typeName。
func (s *Server) enqueueRPC(ctx context.Context, c *Conn, msgID int64, typeID uint32, request *bin.Buffer) error {
	method := s.typeName(typeID)
	if cached, ok := s.cachedRPCResult(c, msgID); ok {
		s.log.Info("RPC duplicate replay from session cache",
			zap.String("method", method),
			zap.Int64("msg_id", msgID),
			zap.String("auth_key_id", c.authKeyHex),
			zap.Int64("session_id", c.sessionID),
		)
		return c.SendEncoded(ctx, proto.MessageServerResponse, cached)
	}
	// 两级条数/字节预算必须先于 Copy：对抗客户端不能用大量满尺寸请求在“判断队列满”
	// 之前制造一轮无上限的临时 body 分配。reservation 在 commit/abort 间唯一持有预算。
	reservation, err := c.reserveInboundRPC(ctx, method, request.Len())
	if err != nil {
		return s.handleInboundRPCAdmissionError(ctx, c, msgID, method, err)
	}
	defer reservation.abort()
	body := request.Copy()
	responseGate := &rpcResponseGate{}
	timeoutResponse := func() {
		if !responseGate.tryTimeout() {
			return
		}
		// 原 task context 已到期，使用有界的新 context 回显明确的可重试超时；
		// 500 保持 TDesktop 默认重试语义，错误名区分于容量型 FLOOD_WAIT。
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
	err = reservation.commit(inboundRPC{
		method:    method,
		size:      len(body),
		onTimeout: timeoutResponse,
		run: func(taskCtx context.Context) error {
			// body 是预算成功后生成的独立副本，且每个任务只 run 一次，
			// 无需再 append 拷贝；直接复用，省掉一份 inbound 在途内存。
			if err := s.handleRPC(taskCtx, c, msgID, method, &bin.Buffer{Buf: body}, responseGate); err != nil {
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
	})
	return s.handleInboundRPCAdmissionError(ctx, c, msgID, method, err)
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
func (s *Server) handleRPC(ctx context.Context, c *Conn, msgID int64, method string, b *bin.Buffer, responseGate *rpcResponseGate) error {
	if s.rpc == nil {
		s.log.Warn("No RPC handler configured; dropping request", zap.String("method", method))
		return nil
	}

	ctx = postresponse.WithCallbacks(ctx)
	ctx, dbStats := dbtrace.WithStats(ctx)
	start := s.clock.Now()
	result, err := s.rpc.Dispatch(ctx, c.authKeyID, c.sessionID, b)
	dur := s.clock.Now().Sub(start)
	s.metrics.RPCHandled(method, dur, err)
	// 刷新本连接协商 layer（invokeWithLayer/initConnection 已被 Dispatch 处理并登记），
	// 供 rpc_result 与后续 push 出站降级使用。仅在确实观测到 layer 时更新——缓存被驱逐
	// 时 NegotiatedLayer 返回 ok=false，此时必须保留连接已记住的 layer，绝不覆盖成默认值，
	// 否则长连接老客户端的条目被驱逐后会被误降回 227。
	if layer, ok := s.rpc.NegotiatedLayer(c.authKeyID, c.sessionID); ok {
		c.SetClientLayer(layer)
	}

	fields := make([]zap.Field, 0, 12)
	fields = append(fields,
		zap.String("method", method),
		zap.String("auth_key_id", c.authKeyHex),
		zap.Int64("session_id", c.sessionID),
		zap.Int64("msg_id", msgID),
		zap.Duration("dur", dur),
	)
	if businessAuthKeyHex, ok := c.BusinessAuthKeyHex(); ok {
		fields = append(fields, zap.String("business_auth_key_id", businessAuthKeyHex))
	}
	if userID := c.UserID(); userID != 0 {
		fields = append(fields, zap.Int64("user_id", userID))
	}
	fields = dbtrace.AppendZapFields(fields, "", dbStats.Snapshot())

	if ctxErr := ctx.Err(); ctxErr != nil {
		// A canceled request context means neither a success nor an error can be delivered
		// with this expired context. In particular, do not cache a late successful result and
		// hand it to outbound: a past write deadline would correctly poison that transport and
		// could prevent the scheduler's fresh-context RPC_TIMEOUT response from being sent.
		cancelFields := append(fields, zap.NamedError("context_error", ctxErr))
		if err != nil {
			cancelFields = append(cancelFields, zap.NamedError("dispatch_error", err))
		}
		s.log.Info("RPC canceled", cancelFields...)
		return ctxErr
	}
	// A deadline callback may have already emitted RPC_TIMEOUT while Dispatch was returning.
	// Claim the single normal-response slot before serializing any success/error rpc_result.
	if responseGate != nil && !responseGate.tryNormal() {
		s.log.Info("RPC result suppressed after timeout", fields...)
		return context.DeadlineExceeded
	}

	if err != nil {
		var rpcErr *tgerr.Error
		if errors.As(err, &rpcErr) {
			s.log.Info("RPC error", append(fields, zap.Int("code", rpcErr.Code), zap.String("error", rpcErr.Message))...)
			return s.sendResult(ctx, c, msgID, &mt.RPCError{
				ErrorCode:    rpcErr.Code,
				ErrorMessage: rpcErr.Message,
			})
		}
		s.log.Info("RPC internal error", append(fields, zap.Error(err))...)
		return s.sendResult(ctx, c, msgID, &mt.RPCError{
			ErrorCode:    500,
			ErrorMessage: "INTERNAL",
		})
	}

	s.log.Info("RPC handled", fields...)
	if err := s.sendResult(ctx, c, msgID, result); err != nil {
		return err
	}
	postresponse.Run(ctx)
	return nil
}

// rpcResponseGate guarantees exactly one terminal rpc_result per request.  A running deadline
// races legitimately with a handler completing at the boundary; whichever path claims state
// first owns the response, and the other path becomes a no-op.
type rpcResponseGate struct {
	state atomic.Uint32
}

func (g *rpcResponseGate) tryNormal() bool {
	return g == nil || g.state.CompareAndSwap(0, 1)
}

func (g *rpcResponseGate) tryTimeout() bool {
	return g != nil && g.state.CompareAndSwap(0, 2)
}

// sendResult 把 RPC 结果包成 rpc_result 并加密回发。
func (s *Server) sendResult(ctx context.Context, c *Conn, reqMsgID int64, result bin.Encoder) error {
	encoded, err := s.encodeRPCResult(c, reqMsgID, result)
	if err != nil {
		return err
	}
	s.storeRPCResult(c, reqMsgID, encoded)
	return c.SendEncoded(ctx, proto.MessageServerResponse, encoded)
}

// encodeRPCResult 编码 rpc_result。内层对象与 rpc_result 头（type_id + req_msg_id）
// 一次性编码进同一 buffer——旧实现先编码内层、再经 proto.Result.Encode 整体拷贝一遍，
// 每条响应多一份全量 body 拷贝。内层按连接协商 layer 降级（layer==227 直通，零开销），
// 降级改写字节时才重建整条消息。降级失败 fail-safe：记日志并发送 canonical 字节——
// 宁可老客户端对个别长尾对象渲染异常，也不让连接/流崩。
func (s *Server) encodeRPCResult(c *Conn, reqMsgID int64, result bin.Encoder) (*encodedOutboundMessage, error) {
	const headerLen = 4 + 8 // rpc_result#f35c6d01 type_id + req_msg_id
	var buf bin.Buffer
	buf.PutID(proto.ResultTypeID)
	buf.PutLong(reqMsgID)
	if err := result.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode rpc result: %w", err)
	}
	if layer := c.ClientLayer(); layer < layerwire.CanonicalLayer {
		inner := buf.Buf[headerLen:]
		if down, err := layerwire.Transcode(inner, layer); err != nil {
			s.log.Warn("layerwire downgrade failed; sending canonical rpc_result",
				zap.Int("layer", layer), zap.Int64("req_msg_id", reqMsgID), zap.Error(err))
		} else if !sameBacking(down, inner) {
			var rebuilt bin.Buffer
			rebuilt.PutID(proto.ResultTypeID)
			rebuilt.PutLong(reqMsgID)
			rebuilt.Put(down)
			buf = rebuilt
		}
	}
	return &encodedOutboundMessage{
		typeID:   proto.ResultTypeID,
		body:     buf.Raw(),
		reqMsgID: reqMsgID,
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
		return err
	} else if resent {
		s.log.Debug("Resent connection cached rpc_result for duplicate msg_id", zap.Int64("msg_id", reqMsgID))
		return nil
	}
	if cached, ok := s.cachedRPCResult(c, reqMsgID); ok {
		if err := c.SendEncoded(ctx, proto.MessageServerResponse, cached); err != nil {
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
	return c.SendAsync(ctx, proto.MessageFromServer, &mt.NewSessionCreated{
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
	removed := false
	if sessionID != c.sessionID {
		removed = s.conns.DestroySessionForAuthKey(c.authKeyID, sessionID)
		if err := s.sessions.Delete(ctx, sessionID); err != nil {
			s.log.Debug("Delete session record failed",
				zap.String("auth_key_id", c.authKeyHex),
				zap.Int64("session_id", sessionID),
				zap.Error(err),
			)
		}
	}
	if removed {
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
	return c.SendPriority(ctx, proto.MessageFromServer, &mt.BadServerSalt{
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
	if msgID == 0 || proto.MessageID(msgID).Type() != proto.MessageFromClient {
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

func validateClientContainer(containerMsgID int64, containerSeqNo int32, container proto.MessageContainer) int {
	for _, m := range container.Messages {
		if m.ID >= containerMsgID || int32(m.SeqNo) > containerSeqNo {
			return badMsgContainer
		}
		typeID, err := (&bin.Buffer{Buf: m.Body}).PeekID()
		if err != nil {
			return badMsgContainer
		}
		if typeID == proto.MessageContainerTypeID {
			return badMsgContainer
		}
		if code := validateClientContainerEnvelope(m.ID, int32(m.SeqNo), typeID); code != 0 {
			return badMsgContainer
		}
	}
	return 0
}

func validateClientContainerEnvelope(msgID int64, seqNo int32, typeID uint32) int {
	if msgID == 0 || proto.MessageID(msgID).Type() != proto.MessageFromClient {
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
	case mt.PingDelayDisconnectRequestTypeID,
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

func (cs *connState) track(msgID int64, seqNo int32, content bool, state byte) {
	cs.seen[msgID] = clientMsgRecord{
		state:   state,
		seqNo:   seqNo,
		content: content,
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
