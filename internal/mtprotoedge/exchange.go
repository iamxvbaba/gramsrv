package mtprotoedge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/exchange"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
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
// 使其能从头读取握手消息。成功后将 auth key + server salt 落入 AuthKeyStore。
func (s *Server) handleExchange(ctx context.Context, conn transport.Conn, first *bin.Buffer) (*bin.Buffer, error) {
	if s.key.Zero() {
		s.log.Error("Key exchange requested but server RSA key is not configured")
		return nil, s.sendProtoError(ctx, conn, codec.CodeAuthKeyNotFound)
	}

	buffered := newBufferedConn(conn)
	buffered.push(first)

	start := s.clock.Now()
	res, err := exchange.NewExchanger(buffered, s.dc).
		WithClock(s.clock).
		WithRand(s.rand).
		WithLogger(s.log.Named("exchange")).
		Server(s.key).
		Run(ctx)
	if err != nil {
		if isEncryptedFrameDuringExchange(err) {
			replay := buffered.lastFrame()
			if replay != nil {
				s.log.Debug("Key exchange interrupted by encrypted frame; replaying as existing session")
				return replay, nil
			}
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
		zap.Object("auth_key", res.Key),
		zap.Int64("server_salt", res.ServerSalt),
		zap.Duration("dur", s.clock.Now().Sub(start)),
	)

	return nil, s.authKeys.Save(ctx, authKeyData(res.Key, res.ServerSalt, s.clock.Now().Unix()))
}

func isEncryptedFrameDuringExchange(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unexpected auth_key_id") && strings.Contains(msg, "plaintext message")
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

// bufferedConn 包装 transport.Conn，可把已读取的帧重新交给后续 Recv。
//
// 用于密钥交换：serveConn 已读首帧用于 peek auth_key_id，再 push 回来交给 exchange。
type bufferedConn struct {
	transport.Conn
	mu      sync.Mutex
	pending []bin.Buffer
	last    bin.Buffer
}

func newBufferedConn(conn transport.Conn) *bufferedConn {
	return &bufferedConn{Conn: conn}
}

func (c *bufferedConn) push(b *bin.Buffer) {
	c.mu.Lock()
	c.pending = append(c.pending, bin.Buffer{Buf: b.Copy()})
	c.mu.Unlock()
}

// Recv 优先返回已 push 的帧（FIFO），耗尽后读取底层连接。
func (c *bufferedConn) Recv(ctx context.Context, b *bin.Buffer) error {
	for {
		c.mu.Lock()
		if len(c.pending) > 0 {
			e := c.pending[0]
			c.pending = c.pending[1:]
			c.last.ResetTo(e.Copy())
			c.mu.Unlock()
			b.ResetTo(e.Buf)
		} else {
			c.mu.Unlock()
			if err := c.Conn.Recv(ctx, b); err != nil {
				return err
			}
			c.mu.Lock()
			c.last.ResetTo(b.Copy())
			c.mu.Unlock()
		}

		if isUnencryptedMsgsAckFrame(b) {
			continue
		}
		return nil
	}
}

func isUnencryptedMsgsAckFrame(frame *bin.Buffer) bool {
	authKeyID, err := peekAuthKeyID(frame)
	if err != nil || authKeyID != emptyAuthKeyID {
		return false
	}

	var msg proto.UnencryptedMessage
	copy := &bin.Buffer{Buf: frame.Copy()}
	if err := msg.Decode(copy); err != nil {
		return false
	}
	payload := &bin.Buffer{Buf: msg.MessageData}
	id, err := payload.PeekID()
	if err != nil {
		return false
	}
	return id == mt.MsgsAckTypeID
}

func (c *bufferedConn) lastFrame() *bin.Buffer {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last.Len() == 0 {
		return nil
	}
	return &bin.Buffer{Buf: c.last.Copy()}
}
