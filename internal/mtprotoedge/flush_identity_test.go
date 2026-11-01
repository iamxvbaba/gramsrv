package mtprotoedge

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

// TestRunFlushDiscardsBatchOnIdentitySwitch verifies that when a connection
// changes hands while a flush is in progress (logout/account switch causing
// c.userID != owner), the staged data belonging to the old account is discarded,
// not sent to the new account, and not requeued into pending. This is a P0 found
// by adversarial review: an in-flight batch escaping the pending cleanup of
// bind/unbind.
func TestRunFlushDiscardsBatchOnIdentitySwitch(t *testing.T) {
	sm := NewSessionManager(nil)
	raw := [8]byte{7}
	const sessionID = int64(99)
	key := sessionKey{authKeyID: raw, sessionID: sessionID}
	c := &Conn{
		sessionID:       sessionID,
		authKeyID:       raw,
		outbound:        make(chan *outboundOp, 4),
		outboundControl: make(chan *outboundOp, 4),
		outboundStop:    make(chan struct{}),
	}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, sessionID, 100) // current account A=100

	// Session not ready: two pushes go into pending.
	for i := 0; i < 2; i++ {
		if err := sm.PushToSessionForAuthKey(context.Background(), raw, sessionID, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
			t.Fatalf("queue pending: %v", err)
		}
	}
	sm.mu.RLock()
	pending := len(sm.pending[key])
	sm.mu.RUnlock()
	if pending != 2 {
		t.Fatalf("pending = %d, want 2", pending)
	}

	// Simulate "flush already started (flushing=true, owner=old account A), but by
	// the time it runs the connection has switched to B".
	sm.mu.Lock()
	sm.flushing[key] = true
	sm.mu.Unlock()
	c.userID.Store(200) // new account B after the switch

	sm.runFlush(c, key, 100, 0) // owner=old account A, current userID=B → must discard

	if n := len(c.outbound); n != 0 {
		t.Fatalf("sent %d pushes to new owner, want 0 (discarded)", n)
	}
	sm.mu.RLock()
	pendingAfter := len(sm.pending[key])
	flushing := sm.flushing[key]
	sm.mu.RUnlock()
	if pendingAfter != 0 {
		t.Fatalf("pending = %d after identity switch, want 0 (discarded, not requeued)", pendingAfter)
	}
	if flushing {
		t.Fatal("flushing flag not cleared after discard")
	}
	if c.receivesUpdates.Load() {
		t.Fatal("receivesUpdates set despite identity switch")
	}
}
