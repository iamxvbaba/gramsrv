package mtprotoedge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
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
	"telesrv/internal/store"
)

// connState 是单连接的 MTProto 运行态。
type connState struct {
	sentCreated bool
	seen        map[int64]clientMsgRecord // 已处理的 client msg_id，用于幂等和 msgs_state_req
	order       []int64
	minSeen     int64
	maxSeen     int64
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
func (s *Server) handleEncrypted(ctx context.Context, tc transport.Conn, cs *connState, current *Conn, fetchedKey *store.AuthKeyData, b *bin.Buffer) (*Conn, error) {
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

	data, err := s.cipher.DecryptFromBuffer(key, b)
	if err != nil {
		return current, fmt.Errorf("decrypt: %w", err)
	}

	if data.Salt != serverSalt {
		c := current
		temp := false
		if c == nil || c.sessionID != data.SessionID {
			c = s.newConn(tc, key, data.SessionID, serverSalt)
			temp = true
		}
		err := s.sendBadServerSalt(ctx, c, data.MessageID, data.SeqNo, serverSalt)
		if temp {
			c.Close()
		}
		return current, err
	}

	// 首个加密消息或 session 变化时（重新）注册连接到 SessionManager。
	if current == nil || current.sessionID != data.SessionID {
		if current != nil {
			cs.reset()
		}
		if current != nil {
			s.conns.Unregister(current)
			current.Close()
		}
		current = s.newConn(tc, key, data.SessionID, serverSalt)
		s.conns.Register(current)
	}

	s.maybePersistSession(ctx, current, data.SessionID, key.ID, serverSalt)

	body := data.Data()
	typeID, err := (&bin.Buffer{Buf: body}).PeekID()
	if err != nil {
		return current, fmt.Errorf("peek encrypted payload type id: %w", err)
	}
	if code := validateClientEnvelope(s.clock.Now(), data.MessageID, data.SeqNo, typeID); code != 0 {
		s.log.Debug("Sending bad_msg_notification",
			zap.Int64("msg_id", data.MessageID),
			zap.Int32("seq_no", data.SeqNo),
			zap.Uint32("type_id", typeID),
			zap.Int("code", code),
		)
		return current, s.sendBadMsg(ctx, current, data.MessageID, data.SeqNo, code)
	}
	if err := sendQuickAckIfRequested(ctx, tc, key, data); err != nil {
		return current, err
	}

	content := clientMessageNeedsAck(typeID)
	if record, ok := cs.seenRecord(data.MessageID); ok {
		s.log.Debug("Duplicate msg_id; replay cached result if available", zap.Int64("msg_id", data.MessageID))
		if err := s.replayRPCResultByRequest(ctx, current, data.MessageID); err != nil {
			return current, err
		}
		if !record.content {
			return current, nil
		}
		return current, s.sendAck(ctx, current, data.MessageID)
	}
	if code := cs.validateSeq(data.MessageID, data.SeqNo, content); code != 0 {
		s.log.Debug("Sending bad_msg_notification",
			zap.Int64("msg_id", data.MessageID),
			zap.Int32("seq_no", data.SeqNo),
			zap.Uint32("type_id", typeID),
			zap.Int("code", code),
		)
		return current, s.sendBadMsg(ctx, current, data.MessageID, data.SeqNo, code)
	}
	cs.track(data.MessageID, data.SeqNo, content, msgStateReceived)

	if !cs.sentCreated {
		cs.sentCreated = true
		s.log.Debug("Sending new_session_created", zap.Int64("msg_id", data.MessageID), zap.Int32("seq_no", data.SeqNo))
		if err := s.sendNewSessionCreated(ctx, current, data.MessageID); err != nil {
			return current, err
		}
	}

	var acks []int64
	if err := s.dispatch(ctx, cs, current, data.MessageID, data.SeqNo, &bin.Buffer{Buf: body}, &acks); err != nil {
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

func sendQuickAckIfRequested(ctx context.Context, tc transport.Conn, key crypto.AuthKey, data *crypto.EncryptedMessageData) error {
	q, ok := tc.(quickAckTransport)
	if !ok || !q.ConsumeQuickAckRequested() {
		return nil
	}
	token, err := clientQuickAckToken(key, data)
	if err != nil {
		return err
	}
	return q.SendQuickAck(ctx, token)
}

func clientQuickAckToken(key crypto.AuthKey, data *crypto.EncryptedMessageData) (uint32, error) {
	var plain bin.Buffer
	if err := data.Encode(&plain); err != nil {
		return 0, err
	}
	h := sha256.New()
	_, _ = h.Write(key.Value[88:120])
	_, _ = h.Write(plain.Raw())
	sum := h.Sum(nil)
	return binary.LittleEndian.Uint32(sum[:4]) &^ quickAckResponseFlag, nil
}

// dispatch 处理一条明文消息：解包 container/gzip，处理服务消息，其余转 RPC 路由。
// content-related 消息（ping、RPC）的 msg_id 会收集到 acks 以便统一确认。
func (s *Server) dispatch(ctx context.Context, cs *connState, c *Conn, msgID int64, seqNo int32, b *bin.Buffer, acks *[]int64) error {
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
		var gz proto.GZIP
		if err := gz.Decode(b); err != nil {
			return fmt.Errorf("decode gzip: %w", err)
		}
		return s.dispatch(ctx, cs, c, msgID, seqNo, &bin.Buffer{Buf: gz.Data}, acks)

	case proto.MessageContainerTypeID:
		var container proto.MessageContainer
		if err := container.Decode(b); err != nil {
			return fmt.Errorf("decode container: %w", err)
		}
		if code := validateClientContainer(msgID, seqNo, container); code != 0 {
			return s.sendBadMsg(ctx, c, msgID, seqNo, code)
		}
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
			if err := s.dispatch(ctx, cs, c, m.ID, int32(m.SeqNo), &bin.Buffer{Buf: m.Body}, acks); err != nil {
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
		var ack mt.MsgsAck
		if err := ack.Decode(b); err != nil {
			return fmt.Errorf("decode msgs_ack: %w", err)
		}
		c.AckServerMessages(ack.MsgIDs)
		s.log.Debug("Received msgs_ack", zap.Int64s("msg_ids", ack.MsgIDs))
		return nil

	case mt.MsgsStateReqTypeID:
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
		var info mt.MsgsStateInfo
		if err := info.Decode(b); err != nil {
			return fmt.Errorf("decode msgs_state_info: %w", err)
		}
		s.log.Debug("Received msgs_state_info", zap.Int64("req_msg_id", info.ReqMsgID), zap.Int("len", len(info.Info)))
		return nil

	case mt.MsgsAllInfoTypeID:
		var info mt.MsgsAllInfo
		if err := info.Decode(b); err != nil {
			return fmt.Errorf("decode msgs_all_info: %w", err)
		}
		s.log.Debug("Received msgs_all_info", zap.Int("msg_ids", len(info.MsgIDs)), zap.Int("len", len(info.Info)))
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
		s.log.Debug("Received destroy_auth_key", zap.String("auth_key_id", hex.EncodeToString(c.authKeyID[:])))
		// 真正销毁：删密钥库记录（每帧回查，删除后该 key 的入站帧立即失效）并主动
		// 断开同 key 的其他连接——出站推送用连接持有的密钥副本加密、不回查密钥库，
		// 不断开的话被销毁 key 的空闲连接仍能持续收到推送。发起连接除外：响应要
		// 先送达，它的下一帧会因密钥缺失自然断开。授权（authorizations）不在此清理，
		// destroy_auth_key 是 PFS 密钥轮换的清理动作，不等于登出。
		if err := s.authKeys.Delete(ctx, c.authKeyID); err != nil {
			s.log.Warn("Delete auth key failed", zap.String("auth_key_id", hex.EncodeToString(c.authKeyID[:])), zap.Error(err))
			return c.SendAsync(ctx, proto.MessageServerResponse, &destroyAuthKeyFail{})
		}
		// 标记密钥已销毁：发起连接被 CloseSessionsForRawAuthKeyExcept 排除（响应需先送达），
		// 它下一帧不能再走 serveConn 的密钥复用快路径，须回落到 Get→AuthKeyNotFound 自然失效。
		c.keyDestroyed.Store(true)
		s.conns.CloseSessionsForRawAuthKeyExcept(c.authKeyID, c.sessionID)
		return c.SendAsync(ctx, proto.MessageServerResponse, &destroyAuthKeyOk{})

	default:
		ackContent()
		body := b.Copy()
		return s.enqueueRPC(ctx, c, msgID, body)
	}
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

func (s *Server) enqueueRPC(ctx context.Context, c *Conn, msgID int64, body []byte) error {
	id, _ := (&bin.Buffer{Buf: body}).PeekID()
	method := s.typeName(id)
	if cached, ok := s.cachedRPCResult(c, msgID); ok {
		s.log.Info("RPC duplicate replay from session cache",
			zap.String("method", method),
			zap.Int64("msg_id", msgID),
			zap.String("auth_key_id", hex.EncodeToString(c.authKeyID[:])),
			zap.Int64("session_id", c.sessionID),
		)
		return c.SendEncoded(ctx, proto.MessageServerResponse, cached)
	}
	err := c.enqueueInboundRPC(ctx, inboundRPC{
		method: method,
		size:   len(body),
		run: func(taskCtx context.Context) error {
			// body 已是 enqueueRPC 入参的独立副本（dispatch 里 b.Copy()），且每个任务只 run 一次，
			// 无需再 append 拷贝；直接复用，省掉一份 inbound 在途内存。
			if err := s.handleRPC(taskCtx, c, msgID, &bin.Buffer{Buf: body}); err != nil {
				fields := []zap.Field{
					zap.Int64("msg_id", msgID),
					zap.String("auth_key_id", hex.EncodeToString(c.authKeyID[:])),
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
	if errors.Is(err, ErrInboundRPCQueueFull) {
		s.log.Debug("Inbound RPC queue full",
			zap.String("method", method),
			zap.Int64("msg_id", msgID),
			zap.String("auth_key_id", hex.EncodeToString(c.authKeyID[:])),
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
func (s *Server) handleRPC(ctx context.Context, c *Conn, msgID int64, b *bin.Buffer) error {
	id, _ := b.PeekID()
	method := s.typeName(id)
	if s.rpc == nil {
		s.log.Warn("No RPC handler configured; dropping request", zap.String("method", method))
		return nil
	}

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

	fields := []zap.Field{
		zap.String("method", method),
		zap.String("auth_key_id", hex.EncodeToString(c.authKeyID[:])),
		zap.Int64("session_id", c.sessionID),
		zap.Int64("msg_id", msgID),
		zap.Duration("dur", dur),
	}
	if businessAuthKeyID, ok := c.BusinessAuthKeyID(); ok {
		fields = append(fields, zap.String("business_auth_key_id", hex.EncodeToString(businessAuthKeyID[:])))
	}
	if userID := c.UserID(); userID != 0 {
		fields = append(fields, zap.Int64("user_id", userID))
	}
	fields = dbtrace.AppendZapFields(fields, "", dbStats.Snapshot())

	if ctxErr := ctx.Err(); ctxErr != nil && err != nil {
		// A canceled request context means the result cannot be delivered. Do not
		// turn cancellation-derived handler errors into cacheable rpc_error replies.
		s.log.Info("RPC canceled", append(fields, zap.NamedError("dispatch_error", err), zap.NamedError("context_error", ctxErr))...)
		return ctxErr
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
	return s.sendResult(ctx, c, msgID, result)
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

// encodeRPCResult 编码 rpc_result。proto.Result.Result 是裸 boxed 对象字节，故在包入
// rpc_result 之前对其按连接协商 layer 降级（layer==227 直通，零开销）。降级失败 fail-safe：
// 记日志并发送 canonical 字节——宁可老客户端对个别长尾对象渲染异常，也不让连接/流崩。
func (s *Server) encodeRPCResult(c *Conn, reqMsgID int64, result bin.Encoder) (*encodedOutboundMessage, error) {
	var buf bin.Buffer
	if err := result.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode rpc result: %w", err)
	}
	inner := buf.Raw()
	if layer := c.ClientLayer(); layer < layerwire.CanonicalLayer {
		if down, err := layerwire.Transcode(inner, layer); err != nil {
			s.log.Warn("layerwire downgrade failed; sending canonical rpc_result",
				zap.Int("layer", layer), zap.Int64("req_msg_id", reqMsgID), zap.Error(err))
		} else {
			inner = down
		}
	}
	encoded, err := encodeOutboundMessage(&proto.Result{
		RequestMessageID: reqMsgID,
		Result:           inner,
	})
	if err != nil {
		return nil, err
	}
	return encoded, nil
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
				zap.String("auth_key_id", hex.EncodeToString(c.authKeyID[:])),
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
