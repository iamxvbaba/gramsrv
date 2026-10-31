package mtprotoedge

import (
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/rpc"
)

type countingMetrics struct {
	connOpened atomic.Int64
	connClosed atomic.Int64
	handshakes atomic.Int64
	rpcs       atomic.Int64
	inbound    atomic.Int64
	outbound   atomic.Int64
}

func (m *countingMetrics) ConnOpened()                             { m.connOpened.Add(1) }
func (m *countingMetrics) ConnClosed()                             { m.connClosed.Add(1) }
func (m *countingMetrics) HandshakeDone(time.Duration)             { m.handshakes.Add(1) }
func (m *countingMetrics) RPCHandled(string, time.Duration, error) { m.rpcs.Add(1) }
func (m *countingMetrics) InboundRPCQueued(string, int, int)       {}
func (m *countingMetrics) InboundRPCStarted(string, time.Duration) { m.inbound.Add(1) }
func (m *countingMetrics) InboundRPCDropped(string, string)        {}
func (m *countingMetrics) OutboundSend(uint32, time.Duration, int, error) {
	m.outbound.Add(1)
}
func (m *countingMetrics) OutboundResend(int, error)  {}
func (m *countingMetrics) OutboundDropped(string)     {}
func (m *countingMetrics) OutboundQueueWait(int, int) {}

// TestMetricsHooks verifies M5: the connection, handshake and RPC metrics hooks are called correctly.
func TestMetricsHooks(t *testing.T) {
	const dc = 2
	m := &countingMetrics{}
	router := rpc.New(rpc.Config{DC: dc, IP: "127.0.0.1", Port: 2398}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	addr, pub, _ := startTestServer(t, Options{DC: dc, legacyRPC: router, Metrics: m})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	sendEncrypted(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), &tg.HelpGetConfigRequest{})
	collectReplies(t, conn, cipher, auth.AuthKey, proto.ResultTypeID)

	if got := m.connOpened.Load(); got < 1 {
		t.Errorf("ConnOpened called %d times, want >= 1", got)
	}
	if got := m.handshakes.Load(); got != 1 {
		t.Errorf("HandshakeDone called %d times, want 1", got)
	}
	if got := m.rpcs.Load(); got != 1 {
		t.Errorf("RPCHandled called %d times, want 1", got)
	}
	if got := m.inbound.Load(); got != 1 {
		t.Errorf("InboundRPCStarted called %d times, want 1", got)
	}
	// new_session_created has already crossed the required-control physical write
	// barrier; msgs_ack is still an asynchronous control frame, so here we only poll
	// for the final send count. The exact boundary→RPC order is pinned by the
	// session boundary test.
	deadline := time.Now().Add(2 * time.Second)
	for m.outbound.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := m.outbound.Load(); got < 3 {
		t.Errorf("OutboundSend called %d times, want >= 3", got)
	}
}
