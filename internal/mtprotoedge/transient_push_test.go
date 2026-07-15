package mtprotoedge

import (
	"context"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
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
