package mtprotoedge

import (
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestSetSessionChannelMembershipsDetectsConcurrentIncrementalUpdates 验证全量
// membership 同步的丢失更新防护：同步方在读持久成员列表前采样修订号，读取窗口内
// 若发生增量 join/leave（另一设备操作经 Add/RemoveUserChannelMembership 落索引），
// 携带过期修订号的全量替换必须改走并集合并（不得覆盖增量），并保持
// membershipsSynced=false 促使下一条 RPC 重试全量同步收敛。
func TestSetSessionChannelMembershipsDetectsConcurrentIncrementalUpdates(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	c := &Conn{sessionID: 42, authKeyID: raw}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, 42, 100)
	sm.SetReceivesUpdatesForAuthKey(raw, 42, true)

	// 同步方采样修订号后、全量列表落地前，用户在另一台设备加入了频道 7。
	gen := sm.ChannelMembershipGeneration(raw, 42)
	sm.AddUserChannelMembership(100, 7)
	// 基于旧快照的全量列表（只有频道 5，不含 7）携带过期修订号落地。
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{5}, gen)

	if got := sm.OnlineChannelMemberUserIDs(7, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 7 members = %v, want [100]: full replace overwrote the in-window incremental join", got)
	}
	if got := sm.OnlineChannelMemberUserIDs(5, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 5 members = %v, want [100]: merge path must still apply the full list", got)
	}
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session fully ready despite raced membership sync; retry would never happen")
	}

	// 重试：新修订号下的全量同步正常替换并置就绪。
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{5, 7}, sm.ChannelMembershipGeneration(raw, 42))
	if !sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session not ready after clean resync")
	}

	// 反方向：窗口内被移出频道 5，stale 全量含 5 → 合并会短暂保留 stale 条目
	// （fan-out 前的 PG active 复核兜底），但必须保持未就绪等待重试。
	gen = sm.ChannelMembershipGeneration(raw, 42)
	sm.RemoveUserChannelMembership(100, 5)
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{5, 7}, gen)
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session ready despite raced removal during sync")
	}
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{7}, sm.ChannelMembershipGeneration(raw, 42))
	if got := sm.OnlineChannelMemberUserIDs(5, 10); len(got) != 0 {
		t.Fatalf("channel 5 members after resync = %v, want empty", got)
	}
	if !sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session not ready after final resync")
	}
}

// TestRegisterEvictsOldestSessionAtCap 验证同 raw auth_key session 数触顶时驱逐的是
// 建连最早的连接，而不是 map 迭代顺序下的随机一个（随机可能误杀刚建立的活跃连接）。
func TestRegisterEvictsOldestSessionAtCap(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{9}
	base := time.Unix(1_700_000_000, 0)

	const oldestSession = int64(100)
	for i := 0; i < maxSessionsPerAuthKey; i++ {
		sid := int64(i + 1)
		created := base.Add(time.Duration(i+1) * time.Second)
		if sid == oldestSession {
			created = base // 唯一早于所有其它连接的时间戳，且故意不在注册顺序首位。
		}
		sm.Register(&Conn{sessionID: sid, authKeyID: raw, createdAt: created})
	}

	sm.Register(&Conn{sessionID: 9999, authKeyID: raw, createdAt: base.Add(time.Hour)})

	sm.mu.RLock()
	_, oldestAlive := sm.bySession[sessionKey{authKeyID: raw, sessionID: oldestSession}]
	_, newestAlive := sm.bySession[sessionKey{authKeyID: raw, sessionID: 9999}]
	total := len(sm.byAuthKey[raw])
	sm.mu.RUnlock()
	if oldestAlive {
		t.Fatal("oldest session survived eviction at cap")
	}
	if !newestAlive {
		t.Fatal("newly registered session missing after eviction")
	}
	if total != maxSessionsPerAuthKey {
		t.Fatalf("sessions for auth key = %d, want cap %d", total, maxSessionsPerAuthKey)
	}
}
