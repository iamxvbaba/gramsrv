package mtprotoedge

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// TestPushTransientSkipsNotReadySession locks in the invariant: a transient push
// (typing/presence) skips a not-ready session directly and does not enter pending;
// an ordinary durable push does enter pending. Regression for transient updates
// sharing the pending queue with durable ones, being wrongly dropped by
// aging/overflow/exhausted retries, and not being recoverable via getDifference.
func TestPushTransientSkipsNotReadySession(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(100)
	c := &Conn{
		sessionID:       7,
		authKeyID:       [8]byte{7},
		outbound:        make(chan *outboundOp, 4),
		outboundControl: make(chan *outboundOp, 4),
		outboundStop:    make(chan struct{}),
	}
	c.userID.Store(userID)
	c.userIDResolved.Store(true)
	// receivesUpdates stays false: the session is not ready (has not yet getState'd to establish the sync baseline).
	sm.Register(c)
	key := connSessionKey(c)

	// transient: not ready → skip, do not enqueue.
	if _, err := sm.PushToUserTransientExceptAuthKeySession(context.Background(), userID, [8]byte{}, 0, proto.MessageFromServer, &tg.UpdatesTooLong{}, 0); err != nil {
		t.Fatalf("transient push: %v", err)
	}
	sm.mu.RLock()
	n := len(sm.pending[key])
	sm.mu.RUnlock()
	if n != 0 {
		t.Fatalf("transient push queued %d pending, want 0 (must skip not-ready session)", n)
	}

	// durable (ordinary): not ready → enter pending (drained once ready; if dropped, caught by getDifference).
	if _, err := sm.PushToUserExceptSession(context.Background(), userID, 0, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
		t.Fatalf("durable push: %v", err)
	}
	sm.mu.RLock()
	n = len(sm.pending[key])
	sm.mu.RUnlock()
	if n != 1 {
		t.Fatalf("durable push queued %d pending, want 1", n)
	}
}

// Constructor compatibility comes from generated profile metadata, not a
// hard-coded minimum layer. Old/unknown sessions are skipped without encoding,
// disconnecting or queuing, while every generated compatible profile receives.
func TestPushTransientCompatibleSkipsUnavailableAndUnknownProfiles(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(101)
	makeConn := func(sessionID int64, profile tlprofile.Profile, known bool) *Conn {
		c := &Conn{
			sessionID: sessionID, authKeyID: [8]byte{byte(sessionID)},
			outbound: make(chan *outboundOp, 2), outboundControl: make(chan *outboundOp, 2),
			outboundStop: make(chan struct{}),
		}
		c.userID.Store(userID)
		c.userIDResolved.Store(true)
		c.receivesUpdates.Store(true)
		if known {
			if err := c.FreezeLayerProfile(profile); err != nil {
				t.Fatal(err)
			}
		}
		if err := sm.Register(c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	old := makeConn(1, tlprofile.Profile227, true)
	introduced := makeConn(2, tlprofile.Profile228, true)
	unknown := makeConn(3, 0, false)
	newer := makeConn(4, tlprofile.Profile229, true)

	message := tg.EphemeralMessage{
		ID: 7, FromID: &tg.PeerUser{UserID: 2001}, PeerID: &tg.PeerChannel{ChannelID: 3001},
		ReceiverID: userID, Date: 1_900_000_000, Message: "private",
	}
	updates := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewEphemeralMessage{Message: message}}, Date: 1_900_000_000}
	sent, err := sm.PushToUserTransientCompatible(context.Background(), userID, tlprofile.SemanticTypeUpdateNewEphemeralMessage, proto.MessageFromServer, updates, time.Second)
	if err != nil || sent != 2 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	if len(old.outbound) != 0 || len(unknown.outbound) != 0 || len(introduced.outbound) != 1 || len(newer.outbound) != 1 {
		t.Fatalf("queues old=%d unknown=%d introduced=%d newer=%d", len(old.outbound), len(unknown.outbound), len(introduced.outbound), len(newer.outbound))
	}
	if old.isRetired() || unknown.isRetired() {
		t.Fatal("unsupported transient update retired an old/unknown session")
	}
	for _, c := range []*Conn{old, introduced, unknown, newer} {
		sm.mu.RLock()
		pending := len(sm.pending[connSessionKey(c)])
		sm.mu.RUnlock()
		if pending != 0 {
			t.Fatalf("session %d queued %d transient updates", c.sessionID, pending)
		}
	}
}
