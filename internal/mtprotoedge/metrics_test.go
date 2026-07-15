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

// TestMetricsHooks 验证 M5：连接、握手、RPC 的 metrics 钩子被正确调用。
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
	// new_session_created 已经跨过 required-control 物理写屏障；msgs_ack 仍是异步控制帧，
	// 因而这里只轮询等待最终发送计数。具体 boundary→RPC 顺序由 session boundary 测试锁定。
	deadline := time.Now().Add(2 * time.Second)
	for m.outbound.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := m.outbound.Load(); got < 3 {
		t.Errorf("OutboundSend called %d times, want >= 3", got)
	}
}
