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

// TestRegisterSeedsNegotiatedLayerBeforeFirstRPC verifies that a connection,
// on registration, seeds the negotiated layer from the rpc layer: even if only a
// ping is sent (a service message that does not go through RPC Dispatch), the
// connection's ClientLayer must already be the negotiated value, rather than
// being refreshed only after the first RPC's Dispatch returns—otherwise a
// reconnecting old client would receive pending flush / concurrent pushes during
// its first RPC handler and silently downgrade to canonical 227.
func TestRegisterSeedsNegotiatedLayerBeforeFirstRPC(t *testing.T) {
	addr, pub, srv := startTestServer(t, Options{DC: 2, legacyRPC: seededLayerRPC{}})
	conn, auth, cipher := dialHandshake(t, addr, 2, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	sendEncryptedWithSeq(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), 1, &mt.PingRequest{PingID: 7})

	// Wait for the pong, ensuring the frame carrying the registration action has been fully processed.
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
