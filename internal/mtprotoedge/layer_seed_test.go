package mtprotoedge

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

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
	addr, pub, srv := startTestServer(t, Options{DC: 2, legacyRPC: seededLayerRPC{}})
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
	if got := c.legacyClientLayer(); got != 225 {
		t.Fatalf("ClientLayer after registration = %d, want seeded 225", got)
	}
}
