package mtprotoedge

import (
	"testing"
	"time"

	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
)

func TestEncryptedConnectionSwitchesAuthKeyEvenWhenSessionIDIsReused(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})
	connA, authA, cipherA := dialHandshake(t, addr, dc, pub)
	_, authB, cipherB := dialHandshake(t, addr, dc, pub)
	msgID := proto.NewMessageIDGen(time.Now)

	sendEncrypted(t, connA, cipherA, authA, msgID.New(proto.MessageFromClient), &mt.PingRequest{PingID: 1})
	for range 3 { // new_session_created + pong + msgs_ack; leave no A-key frame on the socket.
		readServerMessage(t, connA, cipherA, authA.AuthKey)
	}

	// Reuse A's session id on the same physical TCP socket, but encrypt with the independently
	// established key B and B's salt. Session identity is (raw auth_key_id, session_id): comparing
	// session_id alone would keep A's cached key/user identity and encrypt the reply with A.
	body := encodeClientMessageBodyForTest(t, &mt.PingRequest{PingID: 2})
	sendEncryptedWithSessionSaltAndSeq(
		t,
		connA,
		cipherB,
		authB,
		authA.SessionID,
		authB.ServerSalt,
		msgID.New(proto.MessageFromClient),
		1,
		body,
	)
	seenPong := false
	for range 3 {
		_, typeID, _ := readServerMessage(t, connA, cipherB, authB.AuthKey)
		seenPong = seenPong || typeID == mt.PongTypeID
	}
	if !seenPong {
		t.Fatal("new auth key did not receive pong")
	}

	oldKey := sessionKey{authKeyID: authA.AuthKey.ID, sessionID: authA.SessionID}
	newKey := sessionKey{authKeyID: authB.AuthKey.ID, sessionID: authA.SessionID}
	srv.conns.mu.RLock()
	_, oldAlive := srv.conns.bySession[oldKey]
	current := srv.conns.bySession[newKey]
	srv.conns.mu.RUnlock()
	if oldAlive || current == nil || current.authKeyID != authB.AuthKey.ID {
		t.Fatalf("registry after key switch: old_alive=%v current=%v", oldAlive, current != nil)
	}
}
