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

// TestPushSkipsConnReboundToOtherUser locks in the fix for the cross-account
// delivery window: pushToUserWithSender rechecks c.userID outside the lock
// before sending. It simulates the window where a connection is concurrently
// rebound between "collect conns" and "send" (the atomic userID changed but the
// byUser index is not yet updated), verifying that an update belonging to userA
// is not delivered to a connection that has changed hands to userB.
func TestPushSkipsConnReboundToOtherUser(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userA, userB = int64(100), int64(200)
	mk := func(sid int64, authKey byte) *Conn {
		c := &Conn{
			sessionID:       sid,
			authKeyID:       [8]byte{authKey},
			outbound:        make(chan *outboundOp, 4),
			outboundControl: make(chan *outboundOp, 4),
			outboundStop:    make(chan struct{}),
		}
		c.userID.Store(userA)
		c.userIDResolved.Store(true)
		c.receivesUpdates.Store(true)
		if err := c.FreezeLayerProfile(tlprofile.ProfileCanonical); err != nil {
			t.Fatal(err)
		}
		sm.Register(c)
		return c
	}
	stale := mk(1, 1) // registered as userA then rebound to userB
	live := mk(2, 2)  // always userA

	// Change the atomic userID directly without touching the byUser index:
	// reproduce the rebound window between lock release and send.
	stale.userID.Store(userB)

	// Use the best-effort push: it shares pushToUserWithSender (including the
	// userID recheck) with the normal push, but only enqueues without waiting for
	// op.done, so we can observe "delivered vs skipped" without starting an
	// outbound actor (ops go into the buffered c.outbound).
	if _, err := sm.PushToUserExceptSessionBestEffort(context.Background(), userA, 0, proto.MessageFromServer, &tg.UpdatesTooLong{}, time.Second); err != nil {
		t.Fatalf("push: %v", err)
	}
	if n := len(stale.outbound); n != 0 {
		t.Fatalf("rebound conn received %d ops, want 0 (must skip cross-account delivery)", n)
	}
	if n := len(live.outbound); n != 1 {
		t.Fatalf("live conn received %d ops, want 1", n)
	}
}

// TestRegisterEvictsAtSessionCap locks in the per-auth_key session count cap:
// registering a new session beyond maxSessionsPerAuthKey evicts an existing
// session, preventing an adversarial client from blowing up the index with a
// flood of session_ids.
func TestRegisterEvictsAtSessionCap(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	authKey := [8]byte{7}
	for i := 1; i <= maxSessionsPerAuthKey+5; i++ {
		// Naked Conn (no outbound/rpc channels): on eviction Close() is a safe no-op.
		sm.Register(&Conn{sessionID: int64(i), authKeyID: authKey})
	}
	sm.mu.RLock()
	perKey := len(sm.byAuthKey[authKey])
	total := len(sm.bySession)
	sm.mu.RUnlock()
	if perKey != maxSessionsPerAuthKey {
		t.Fatalf("sessions for auth key = %d, want cap %d", perKey, maxSessionsPerAuthKey)
	}
	if total != maxSessionsPerAuthKey {
		t.Fatalf("total online = %d, want %d", total, maxSessionsPerAuthKey)
	}
}
