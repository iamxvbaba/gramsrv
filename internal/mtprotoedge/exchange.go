package mtprotoedge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/exchange"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto/codec"
	"github.com/gotd/td/transport"

	"telesrv/internal/store"
)

// emptyAuthKeyID 是未加密消息（密钥交换）的 auth_key_id（全零）。
var emptyAuthKeyID [8]byte

// peekAuthKeyID 读取消息前 8 字节的 auth_key_id，不消费 buffer。
func peekAuthKeyID(b *bin.Buffer) (id [8]byte, err error) {
	err = b.PeekN(id[:], len(id))
	return id, err
}

// handleExchange 在收到 auth_key_id==0 的首帧后执行服务端 MTProto 密钥交换。
//
// first 是已读取的首帧（req_pq*），通过 bufferedConn 交还给 exchange 流程，
// 使其能从头读取握手消息。auth key + server salt 会在 DhGenOk 发出前落入
// AuthKeyStore；持久化失败时不向客户端确认握手成功。
func (s *Server) handleExchange(ctx context.Context, conn transport.Conn, first *bin.Buffer) (*bin.Buffer, error) {
	if s.key.Zero() {
		s.log.Error("Key exchange requested but server RSA key is not configured")
		return nil, s.sendProtoError(ctx, conn, codec.CodeAuthKeyNotFound)
	}

	buffered := newBufferedConn(conn)
	buffered.push(first)

	// 给整个密钥交换设总时长上界。HandshakeIdleTimeout 只约束单次读 idle，对一个持续发包的
	// 客户端无效——若客户端陷入「ResPQ→nonce 失步→重发 req_pq」的握手重启死循环，无界的
	// serverExchange 会对每个 req_pq 盲回 ResPQ、永不收敛地空转刷日志/占 CPU。超时即放弃本次
	// 握手并断开，客户端重连发起全新握手（无残留相位差）即恢复。
	runCtx := ctx
	if s.handshakeMaxDur > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, s.handshakeMaxDur)
		defer cancel()
	}

	start := s.clock.Now()
	res, err := s.runServerExchange(runCtx, buffered)
	if err != nil {
		// gotd v0.158：握手中读到非零 auth_key_id 帧（客户端用既有 auth key 而非重新交换）
		// 经类型化 UnexpectedEncryptedError 暴露并随附原始帧（旧版仅靠错误文案匹配，升级后失效）。
		// 把该帧当既有会话首帧 replay，切勿回 -404——TDesktop 会判定 temp key 被销毁、丢弃并
		// 重跑密钥交换，引发重连/重交换风暴。
		var encErr *exchange.UnexpectedEncryptedError
		if errors.As(err, &encErr) {
			replay := encErr.Frame
			if len(replay) > 0 {
				s.log.Debug("Key exchange interrupted by encrypted frame; replaying as existing session")
				return &bin.Buffer{Buf: replay}, nil
			}
		}
		// req_pq 帧数超界（客户端握手重启死循环）：瞬断，促客户端重连发起全新握手。
		if errors.Is(err, errTooManyHandshakeReqPQ) {
			s.log.Info("Key exchange aborted: too many req_pq retries (client handshake restart loop)",
				zap.Int("max", maxHandshakeReqPQ))
			return nil, err
		}
		// 仅本握手的总时长上界到点（ctx 自身未取消）：放弃并断开，促客户端重连。
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			s.log.Info("Key exchange aborted: exceeded max duration (possible client req_pq restart loop)",
				zap.Duration("max", s.handshakeMaxDur))
			return nil, err
		}
		var exErr *exchange.ServerExchangeError
		if errors.As(err, &exErr) {
			s.log.Info("Key exchange rejected", zap.Int32("code", exErr.Code), zap.Error(err))
			return nil, s.sendProtoError(ctx, conn, exErr.Code)
		}
		return nil, fmt.Errorf("key exchange: %w", err)
	}

	s.metrics.HandshakeDone(s.clock.Now().Sub(start))
	s.log.Info("Key exchange completed",
		zap.Int64("auth_key_id", res.Key.IntID()),
		zap.Int64("server_salt", res.ServerSalt),
		zap.Duration("dur", s.clock.Now().Sub(start)),
	)

	return nil, nil
}

// authKeyData 把握手结果转换为 store 记录。
func authKeyData(key crypto.AuthKey, salt, createdAt int64) store.AuthKeyData {
	return store.AuthKeyData{
		ID:         key.ID,
		Value:      [256]byte(key.Value),
		ServerSalt: salt,
		CreatedAt:  createdAt,
	}
}

// sendProtoError 向客户端发送 transport 级协议错误（-code）。
func (s *Server) sendProtoError(ctx context.Context, conn transport.Conn, code int32) error {
	var buf bin.Buffer
	buf.PutInt32(-code)

	ctx, cancel := context.WithTimeout(ctx, s.writeTimeout)
	defer cancel()
	if err := conn.Send(ctx, &buf); err != nil {
		return fmt.Errorf("send proto error %d: %w", code, err)
	}
	return nil
}

// maxHandshakeReqPQ 是一次密钥交换内允许的 req_pq(_multi) 帧数上界。正常握手只发 1 个
// req_pq（含个别客户端的「fake+真」也就 2 个）；客户端因 nonce 失步陷入「收到 ResPQ→立刻
// 重启握手换 nonce 重发 req_pq」死循环时，会在同一连接上无限发 req_pq，而委托给 gotd 的
// serverExchange 会对每个都盲回 ResPQ、永不收敛（见 docs/client-compat-notes.md 的握手风暴）。
// 超过此上界即在 telesrv 传输层瞬断该连接，促客户端重连发起全新握手（无残留相位差）即恢复。
// 留足余量（8）容纳少量正常重连重启。它与 HandshakeMaxDuration 总时长上界互补（按次/按时）。
const maxHandshakeReqPQ = 8

// errTooManyHandshakeReqPQ 表示一次握手内 req_pq 帧数超过 maxHandshakeReqPQ（疑似客户端握手
// 重启死循环）。从 bufferedConn.Recv 抛出，经 serverExchange 透传回 handleExchange 断开连接。
var errTooManyHandshakeReqPQ = errors.New("too many req_pq frames in one handshake (client restart loop)")

// bufferedConn 包装 transport.Conn，可把已读取的帧重新交给后续 Recv。
//
// 用于密钥交换：serveConn 已读首帧用于 peek auth_key_id，再 push 回来交给 exchange。
type bufferedConn struct {
	transport.Conn
	pending    []bin.Buffer
	reqPQCount int // 本次握手已见 req_pq(_multi) 帧数；只在握手期访问（Recv 单 goroutine）
}

func newBufferedConn(conn transport.Conn) *bufferedConn {
	return &bufferedConn{Conn: conn}
}

func (c *bufferedConn) push(b *bin.Buffer) {
	if b == nil {
		return
	}
	// serveConn is synchronously blocked in handleExchange, so the first frame's
	// backing remains stable until the exchange returns. Keep a slice view instead
	// of copying an attacker-sized transport frame.
	buf := b.Buf
	b.Buf = nil // transfer ownership; serveConn must not pin the frame after next Recv releases it
	c.pending = append(c.pending, bin.Buffer{Buf: buf})
}

// Recv 优先返回已 push 的帧（FIFO），耗尽后读取底层连接。
func (c *bufferedConn) Recv(ctx context.Context, b *bin.Buffer) error {
	for {
		if len(c.pending) > 0 {
			e := c.pending[0]
			c.pending[0] = bin.Buffer{}
			c.pending = c.pending[1:]
			b.ResetTo(e.Buf)
		} else {
			if err := c.Conn.Recv(ctx, b); err != nil {
				return err
			}
		}

		if isUnencryptedMsgsAckFrame(b) {
			// The ack is intentionally ignored during exchange. Drop its transport
			// backing and shrink the retained high-water charge before the next Recv;
			// otherwise a large trailing frame can consume global admission budget for
			// the rest of a CPU-heavy key exchange even though no backing remains live.
			b.Buf = nil
			retainInboundFrameBackings(c.Conn, b)
			continue
		}
		// req_pq 计数上界：仅在握手期生效（bufferedConn 只用于密钥交换），且 payload id 探测
		// 与上面的 msgs_ack 跳过同量级开销，不碰加密消息热路径。超界即瞬断，止住握手死循环。
		if isUnencryptedReqPQFrame(b) {
			c.reqPQCount++
			if c.reqPQCount > maxHandshakeReqPQ {
				return errTooManyHandshakeReqPQ
			}
		}
		return nil
	}
}

// unencryptedPayloadID 返回未加密消息（auth_key_id==0）内层 TL payload 的 type id。
// 非未加密消息 / 解码失败时 ok=false。
func unencryptedPayloadID(frame *bin.Buffer) (uint32, bool) {
	if frame == nil || len(frame.Buf) < 24 {
		return 0, false
	}
	if binary.LittleEndian.Uint64(frame.Buf[:8]) != 0 {
		return 0, false
	}
	dataLen := int64(int32(binary.LittleEndian.Uint32(frame.Buf[16:20])))
	if dataLen < 4 || dataLen > int64(len(frame.Buf)-20) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(frame.Buf[20:24]), true
}

func isUnencryptedMsgsAckFrame(frame *bin.Buffer) bool {
	id, ok := unencryptedPayloadID(frame)
	return ok && id == mt.MsgsAckTypeID
}

func isUnencryptedReqPQFrame(frame *bin.Buffer) bool {
	id, ok := unencryptedPayloadID(frame)
	return ok && (id == mt.ReqPqRequestTypeID || id == mt.ReqPqMultiRequestTypeID)
}
