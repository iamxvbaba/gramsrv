package mtprotoedge

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/transport"

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
func (s *Server) handleEncrypted(ctx context.Context, tc transport.Conn, cs *connState, current *Conn, keyData store.AuthKeyData, b *bin.Buffer) (*Conn, error) {
	key := crypto.AuthKey{Value: crypto.Key(keyData.Value), ID: keyData.ID}

	data, err := s.cipher.DecryptFromBuffer(key, b)
	if err != nil {
		return current, fmt.Errorf("decrypt: %w", err)
	}

	if data.Salt != keyData.ServerSalt {
		c := current
		temp := false
		if c == nil || c.sessionID != data.SessionID {
			c = s.newConn(tc, key, data.SessionID, keyData.ServerSalt)
			temp = true
		}
		err := s.sendBadServerSalt(ctx, c, data.MessageID, data.SeqNo, keyData.ServerSalt)
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
		current = s.newConn(tc, key, data.SessionID, keyData.ServerSalt)
		s.conns.Register(current)
	}

	if err := s.sessions.Save(ctx, store.SessionData{
		ID:        data.SessionID,
		AuthKeyID: key.ID,
		Salt:      keyData.ServerSalt,
		LastSeen:  s.clock.Now().Unix(),
	}); err != nil {
		return current, fmt.Errorf("save session: %w", err)
	}

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

	content := clientMessageNeedsAck(typeID)
	if record, ok := cs.seenRecord(data.MessageID); ok {
		s.log.Debug("Duplicate msg_id; re-ack only", zap.Int64("msg_id", data.MessageID))
		if resent, err := current.ResendByRequest(ctx, data.MessageID); err != nil {
			return current, err
		} else if resent {
			s.log.Debug("Resent cached rpc_result for duplicate msg_id", zap.Int64("msg_id", data.MessageID))
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
	err := c.enqueueInboundRPC(ctx, inboundRPC{
		method: method,
		size:   len(body),
		run: func(taskCtx context.Context) error {
			// body 已是 enqueueRPC 入参的独立副本（dispatch 里 b.Copy()），且每个任务只 run 一次，
			// 无需再 append 拷贝；直接复用，省掉一份 inbound 在途内存。
			if err := s.handleRPC(taskCtx, c, msgID, &bin.Buffer{Buf: body}); err != nil {
				s.log.Info("RPC async handler failed",
					zap.Int64("msg_id", msgID),
					zap.String("auth_key_id", hex.EncodeToString(c.authKeyID[:])),
					zap.Int64("session_id", c.sessionID),
					zap.Error(err),
				)
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

	start := s.clock.Now()
	result, err := s.rpc.Dispatch(ctx, c.authKeyID, c.sessionID, b)
	dur := s.clock.Now().Sub(start)
	s.metrics.RPCHandled(method, dur, err)

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
	var buf bin.Buffer
	if err := result.Encode(&buf); err != nil {
		return fmt.Errorf("encode rpc result: %w", err)
	}
	return c.Send(ctx, proto.MessageServerResponse, &proto.Result{
		RequestMessageID: reqMsgID,
		Result:           buf.Raw(),
	})
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
func (s *Server) sendNewSessionCreated(ctx context.Context, c *Conn, firstMsgID int64) error {
	return c.SendAsync(ctx, proto.MessageFromServer, &mt.NewSessionCreated{
		FirstMsgID: firstMsgID,
		UniqueID:   s.sessionUID,
		ServerSalt: c.salt,
	})
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
	if clientMessageNeedsAck(typeID) {
		if seqNo%2 == 0 {
			return badMsgSeqNotOdd
		}
	} else if seqNo%2 != 0 {
		return badMsgSeqNotEven
	}
	return 0
}

func clientMessageNeedsAck(typeID uint32) bool {
	switch typeID {
	case proto.MessageContainerTypeID,
		mt.MsgsAckTypeID,
		mt.PingDelayDisconnectRequestTypeID,
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
