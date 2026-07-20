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

// TestPushTransientSkipsNotReadySession 锁定不变量：transient 推送（typing/presence）对
// 未就绪 session 直接跳过、不进 pending；而普通 durable 推送会进 pending。回归 transient
// updates 与 durable 共用 pending 队列、被老化/溢出/重试耗尽误丢且 getDifference 无法补的问题。
func TestPushTransientSkipsNotReadySession(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(100)
	c := &Conn{
		sessionID:       7,
		authKeyID:       [8]byte{7},
		outbound:        make(chan outboundOp, 4),
		outboundControl: make(chan outboundOp, 4),
		outboundStop:    make(chan struct{}),
	}
	c.userID.Store(userID)
	c.userIDResolved.Store(true)
	// receivesUpdates 保持 false：session 未就绪（尚未 getState 建立同步基线）。
	sm.Register(c)
	key := connSessionKey(c)

	// transient：未就绪 → 跳过、不入队。
	if _, err := sm.PushToUserTransientExceptAuthKeySession(context.Background(), userID, [8]byte{}, 0, proto.MessageFromServer, &tg.UpdatesTooLong{}, 0); err != nil {
		t.Fatalf("transient push: %v", err)
	}
	sm.mu.RLock()
	n := len(sm.pending[key])
	sm.mu.RUnlock()
	if n != 0 {
		t.Fatalf("transient push queued %d pending, want 0 (must skip not-ready session)", n)
	}

	// durable（普通）：未就绪 → 入 pending（就绪后排空，丢弃时由 getDifference 兜底）。
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

// Layer-228-only transient constructors must be filtered before encoding. A
// Layer 227 or unknown session is skipped without disconnecting it or queuing
// an unreplayable update, while the ready Layer 228 session receives it.
func TestPushTransientAtLeastLayerSkipsOldAndUnknownProfiles(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(101)
	makeConn := func(sessionID int64, profile tlprofile.Profile, known bool) *Conn {
		c := &Conn{
			sessionID: sessionID, authKeyID: [8]byte{byte(sessionID)},
			outbound: make(chan outboundOp, 2), outboundControl: make(chan outboundOp, 2),
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
	current := makeConn(2, tlprofile.Profile228, true)
	unknown := makeConn(3, 0, false)

	message := tg.EphemeralMessage{
		ID: 7, FromID: &tg.PeerUser{UserID: 2001}, PeerID: &tg.PeerChannel{ChannelID: 3001},
		ReceiverID: userID, Date: 1_900_000_000, Message: "private",
	}
	updates := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewEphemeralMessage{Message: message}}, Date: 1_900_000_000}
	sent, err := sm.PushToUserTransientAtLeastLayer(context.Background(), userID, 228, proto.MessageFromServer, updates, time.Second)
	if err != nil || sent != 1 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	if len(old.outbound) != 0 || len(unknown.outbound) != 0 || len(current.outbound) != 1 {
		t.Fatalf("queues old=%d unknown=%d current=%d", len(old.outbound), len(unknown.outbound), len(current.outbound))
	}
	if old.isRetired() || unknown.isRetired() {
		t.Fatal("unsupported transient update retired an old/unknown session")
	}
	for _, c := range []*Conn{old, current, unknown} {
		sm.mu.RLock()
		pending := len(sm.pending[connSessionKey(c)])
		sm.mu.RUnlock()
		if pending != 0 {
			t.Fatalf("session %d queued %d transient updates", c.sessionID, pending)
		}
	}
}
