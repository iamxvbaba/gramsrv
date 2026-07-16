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

// TestPushSkipsConnReboundToOtherUser 锁定跨账号投递窗口的修复：pushToUserWithSender 在锁外
// 发送前复查 c.userID。模拟「收集 conns 后、send 前」连接被并发换绑（atomic userID 改了但
// byUser 索引尚未更新）的窗口，验证本属于 userA 的 update 不会投递到已易主为 userB 的连接。
func TestPushSkipsConnReboundToOtherUser(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userA, userB = int64(100), int64(200)
	mk := func(sid int64, authKey byte) *Conn {
		c := &Conn{
			sessionID:       sid,
			authKeyID:       [8]byte{authKey},
			outbound:        make(chan outboundOp, 4),
			outboundControl: make(chan outboundOp, 4),
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
	stale := mk(1, 1) // 注册为 userA 后被换绑到 userB
	live := mk(2, 2)  // 始终 userA

	// 直接改 atomic userID、不动 byUser 索引：复现锁释放后到 send 前的换绑窗口。
	stale.userID.Store(userB)

	// 走 best-effort 推送：与普通推送共用 pushToUserWithSender（含 userID 复查），但只入队
	// 不等 op.done，故无需起 outbound actor 即可观察「投递 vs 跳过」（op 进 buffered c.outbound）。
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

// TestRegisterEvictsAtSessionCap 锁定单 auth_key 的 session 数上限：超出 maxSessionsPerAuthKey
// 的新 session 注册会驱逐一个现有 session，防对抗客户端用海量 session_id 撑爆索引。
func TestRegisterEvictsAtSessionCap(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	authKey := [8]byte{7}
	for i := 1; i <= maxSessionsPerAuthKey+5; i++ {
		// 裸 Conn（无 outbound/rpc 通道）：被驱逐时 Close() 为安全 no-op。
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
