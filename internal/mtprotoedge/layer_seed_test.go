package mtprotoedge

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
)

func TestSessionManagerSetClientLayerForAuthKey(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	c := &Conn{sessionID: 42, authKeyID: [8]byte{1, 2, 3}}
	sm.Register(c)
	defer sm.Unregister(c)

	sm.SetClientLayerForAuthKey([8]byte{1, 2, 3}, 42, 225)
	if got := c.ClientLayer(); got != 225 {
		t.Fatalf("ClientLayer = %d, want 225", got)
	}
	// 未注册 session：no-op，不 panic。
	sm.SetClientLayerForAuthKey([8]byte{9}, 1, 220)
	// 非法 layer：忽略，保留已有值。
	sm.SetClientLayerForAuthKey([8]byte{1, 2, 3}, 42, 0)
	if got := c.ClientLayer(); got != 225 {
		t.Fatalf("ClientLayer after layer=0 = %d, want 225", got)
	}
}

type seededLayerRPC struct{}

func (seededLayerRPC) Dispatch(context.Context, [8]byte, int64, *bin.Buffer) (bin.Encoder, error) {
	return &tg.Config{ThisDC: 2}, nil
}

func (seededLayerRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 225, true }

// TestRegisterSeedsNegotiatedLayerBeforeFirstRPC 验证连接注册即从 rpc 层播种协商 layer：
// 只发一条 ping（服务消息，不经 RPC Dispatch），连接的 ClientLayer 就必须是协商值，
// 而不是等首条 RPC 的 Dispatch 返回后才刷新——否则重连老客户端在首条 RPC handler
// 执行期间收到的 pending flush / 并发 push 会按 canonical 227 漏降级。
func TestRegisterSeedsNegotiatedLayerBeforeFirstRPC(t *testing.T) {
	addr, pub, srv := startTestServer(t, Options{DC: 2, RPC: seededLayerRPC{}})
	conn, auth, cipher := dialHandshake(t, addr, 2, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	sendEncrypted(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), &mt.PingRequest{PingID: 7})

	// 等 pong 回来，确保携带注册动作的那一帧已处理完成。
	gotPong := false
	for i := 0; i < 12 && !gotPong; i++ {
		_, id, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
		gotPong = id == mt.PongTypeID
	}
	if !gotPong {
		t.Fatal("missing pong for ping")
	}

	srv.conns.mu.RLock()
	c := srv.conns.bySession[sessionKey{authKeyID: auth.AuthKey.ID, sessionID: auth.SessionID}]
	srv.conns.mu.RUnlock()
	if c == nil {
		t.Fatal("connection not registered")
	}
	if got := c.ClientLayer(); got != 225 {
		t.Fatalf("ClientLayer after registration = %d, want seeded 225", got)
	}
}
