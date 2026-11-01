package mtprotoedge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/store"
)

// emptyAuthKeyID is the auth_key_id (all zero) of unencrypted messages (key exchange).
var emptyAuthKeyID [8]byte

// peekAuthKeyID reads the auth_key_id from the first 8 bytes of the message
// without consuming the buffer.
func peekAuthKeyID(b *bin.Buffer) (id [8]byte, err error) {
	err = b.PeekN(id[:], len(id))
	return id, err
}

// handleExchange performs the server-side MTProto key exchange after receiving
// the first frame with auth_key_id==0.
//
// first is the already-read first frame (req_pq*), handed back to the exchange
// flow via bufferedConn so it can read the handshake messages from the start.
// The auth key + server salt are persisted to AuthKeyStore before DhGenOk is
// sent; on persistence failure the handshake is not confirmed to the client.
func (s *Server) handleExchange(ctx context.Context, conn transport.Conn, first *bin.Buffer) (*bin.Buffer, error) {
	if s.key.Zero() {
		s.log.Error("Key exchange requested but server RSA key is not configured")
		return nil, s.sendProtoError(ctx, conn, codec.CodeAuthKeyNotFound)
	}

	buffered := newBufferedConn(conn)
	buffered.push(first)

	// Put an overall duration bound on the whole key exchange. HandshakeIdleTimeout
	// only constrains a single read idle and is useless against a client that keeps
	// sending—if a client falls into a "ResPQ→nonce desync→resent req_pq" handshake
	// restart loop, an unbounded serverExchange would blindly reply ResPQ to every
	// req_pq, spinning forever and flushing logs/burning CPU. On timeout this
	// handshake is abandoned and the connection closed; the client reconnects with
	// a fresh handshake (no residual phase offset) and recovers.
	runCtx := ctx
	if s.handshakeMaxDur > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, s.handshakeMaxDur)
		defer cancel()
	}

	start := s.clock.Now()
	res, err := s.runServerExchange(runCtx, buffered)
	if err != nil {
		// gotd v0.158: reading a non-zero auth_key_id frame mid-handshake (client uses
		// an existing auth key instead of re-exchanging) is surfaced via a typed
		// UnexpectedEncryptedError with the original frame attached (older versions
		// only matched on error text, which broke after the upgrade). Replay that
		// frame as the first frame of an existing session; never reply -404—TDesktop
		// would decide the temp key was destroyed, discard it and rerun the key
		// exchange, triggering a reconnect/re-exchange storm.
		var encErr *exchange.UnexpectedEncryptedError
		if errors.As(err, &encErr) {
			replay := encErr.Frame
			if len(replay) > 0 {
				s.log.Debug("Key exchange interrupted by encrypted frame; replaying as existing session")
				return &bin.Buffer{Buf: replay}, nil
			}
		}
		// req_pq frame count over the bound (client handshake restart loop): drop the
		// connection immediately, prompting the client to reconnect with a fresh handshake.
		if errors.Is(err, errTooManyHandshakeReqPQ) {
			s.log.Info("Key exchange aborted: too many req_pq retries (client handshake restart loop)",
				zap.Int("max", maxHandshakeReqPQ))
			return nil, err
		}
		// Only this handshake's overall duration bound expired (ctx itself is not
		// canceled): abandon and close, prompting the client to reconnect.
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
	// Successful handshakes are already covered by the bounded latency metric.
	// One INFO write per PFS connection turns a 10,000-account login burst into
	// synchronous logger and filesystem pressure.
	s.log.Debug("Key exchange completed",
		zap.Int64("auth_key_id", res.Key.IntID()),
		zap.Int64("server_salt", res.ServerSalt),
		zap.Duration("dur", s.clock.Now().Sub(start)),
	)

	return nil, nil
}

// authKeyData converts a handshake result into a store record.
func authKeyData(key crypto.AuthKey, salt, createdAt int64, expiresAt int) store.AuthKeyData {
	return store.AuthKeyData{
		ID:         key.ID,
		Value:      [256]byte(key.Value),
		ServerSalt: salt,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
	}
}

// sendProtoError sends a transport-level protocol error (-code) to the client.
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

// sendTerminalProtoError serializes a bare transport error after the authenticated
// outbound actor has stopped. A direct write while the actor is still draining can
// otherwise interleave an encrypted update/result after -404 on the same socket.
func (s *Server) sendTerminalProtoError(ctx context.Context, c *Conn, code int32) error {
	if c == nil {
		return errors.New("send terminal protocol error without logical connection")
	}
	c.beginTerminalShutdown()
	if !c.waitOutboundShutdownUntil(forceCloseBatchTimeout) {
		c.closeTransport()
		return errors.New("outbound writer did not stop before terminal protocol error")
	}
	if c.transport == nil {
		return ErrConnClosed
	}
	return s.sendProtoError(ctx, c.transport, code)
}

// maxHandshakeReqPQ is the upper bound on the number of req_pq(_multi) frames
// allowed within one key exchange. A normal handshake sends only 1 req_pq (even
// the "fake + real" of some clients is only 2); when a client falls into a
// "receive ResPQ→immediately restart the handshake with a new nonce and resend
// req_pq" loop due to nonce desync, it sends req_pq infinitely on the same
// connection, and the gotd-delegated serverExchange blindly replies ResPQ to
// every one, never converging (see the handshake storm in
// docs/client-compat-notes.md). Exceeding this bound immediately drops the
// connection at the telesrv transport layer, prompting the client to reconnect
// with a fresh handshake (no residual phase offset) and recover. (8) leaves ample
// headroom for a few normal reconnect restarts. It complements the overall
// HandshakeMaxDuration bound (per-count / per-time).
const maxHandshakeReqPQ = 8

// errTooManyHandshakeReqPQ means the number of req_pq frames in one handshake
// exceeded maxHandshakeReqPQ (suspected client handshake restart loop). It is
// raised from bufferedConn.Recv and propagated through serverExchange back to
// handleExchange to close the connection.
var errTooManyHandshakeReqPQ = errors.New("too many req_pq frames in one handshake (client restart loop)")

// bufferedConn wraps transport.Conn and can hand already-read frames back to a
// subsequent Recv.
//
// Used for the key exchange: serveConn already read the first frame to peek the
// auth_key_id, then pushes it back for the exchange.
type bufferedConn struct {
	transport.Conn
	pending    []bin.Buffer
	reqPQCount int // req_pq(_multi) frames seen in this handshake; accessed only during the handshake (single Recv goroutine)
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

// Recv returns pushed frames first (FIFO), reading the underlying connection once exhausted.
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
		// req_pq count bound: only in effect during the handshake (bufferedConn is
		// used only for the key exchange), and the payload id probe costs the same
		// order as the msgs_ack skip above, not touching the encrypted-message hot
		// path. Over the bound means an immediate drop, stopping the handshake loop.
		if isUnencryptedReqPQFrame(b) {
			c.reqPQCount++
			if c.reqPQCount > maxHandshakeReqPQ {
				return errTooManyHandshakeReqPQ
			}
		}
		return nil
	}
}

// unencryptedPayloadID returns the type id of the inner TL payload of an
// unencrypted message (auth_key_id==0). ok=false for a non-unencrypted message or
// on decode failure.
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
