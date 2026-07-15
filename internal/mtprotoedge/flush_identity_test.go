package mtprotoedge

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

// TestRunFlushDiscardsBatchOnIdentitySwitch 验证排空进行中连接易主（登出/换号致
// c.userID != owner）时，属于旧账号的暂存被丢弃、不发给新账号、也不回排进 pending。
// 这是对抗审查发现的 P0：在飞 batch 逃过 bind/unbind 的 pending 清理。
func TestRunFlushDiscardsBatchOnIdentitySwitch(t *testing.T) {
	sm := NewSessionManager(nil)
	raw := [8]byte{7}
	const sessionID = int64(99)
	key := sessionKey{authKeyID: raw, sessionID: sessionID}
	c := &Conn{
		sessionID:       sessionID,
		authKeyID:       raw,
		outbound:        make(chan outboundOp, 4),
		outboundControl: make(chan outboundOp, 4),
		outboundStop:    make(chan struct{}),
	}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, sessionID, 100) // 当前账号 A=100

	// session 未就绪：两条推送进 pending。
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

	// 模拟「排空已启动（flushing=true，owner=旧账号 A），但运行到时连接已换号成 B」。
	sm.mu.Lock()
	sm.flushing[key] = true
	sm.mu.Unlock()
	c.userID.Store(200) // 换号后的新账号 B

	sm.runFlush(c, key, 100, 0) // owner=旧账号 A，当前 userID=B → 必须丢弃

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
