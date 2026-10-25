package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
)

// TestNotifySettingsRoundTripPostgres 回归迁移 0005：per-scope 通知设置真实持久化
// （peer / 类别默认 / 批量 / reset），含可空字段=未设置语义。
func TestNotifySettingsRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPasswordStore(pool)

	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	u, err := users.Create(ctx, domain.User{AccessHash: 92, Phone: "+1663" + suffix + "01", FirstName: "NotifyOwner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM notify_settings WHERE owner_user_id = $1", u.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", u.ID)
	})

	peerA := domain.Peer{Type: domain.PeerTypeUser, ID: 7001}
	peerB := domain.Peer{Type: domain.PeerTypeChannel, ID: 7002}
	scopeA := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peerA}

	// not found.
	if _, found, err := store.GetNotifySettings(ctx, u.ID, scopeA); err != nil || found {
		t.Fatalf("get before save = found %v err %v", found, err)
	}

	// peer A：只设 mute_until + silent（其余 nil=未设置）。
	mute := 2000000000
	silent := true
	if err := store.SaveNotifySettings(ctx, u.ID, scopeA, domain.PeerNotifySettings{MuteUntil: &mute, Silent: &silent}); err != nil {
		t.Fatalf("save scope A: %v", err)
	}
	got, found, err := store.GetNotifySettings(ctx, u.ID, scopeA)
	if err != nil || !found {
		t.Fatalf("get scope A = found %v err %v", found, err)
	}
	if got.MuteUntil == nil || *got.MuteUntil != mute || got.Silent == nil || !*got.Silent {
		t.Fatalf("scope A = %+v, want mute=%d silent=true", got, mute)
	}
	if got.ShowPreviews != nil || got.StoriesMuted != nil {
		t.Fatalf("unset fields must stay nil, got %+v", got)
	}

	// 类别默认（users）独立。
	usersScope := domain.NotifyScope{Kind: domain.NotifyScopeUsers}
	dmute := 100
	if err := store.SaveNotifySettings(ctx, u.ID, usersScope, domain.PeerNotifySettings{MuteUntil: &dmute}); err != nil {
		t.Fatalf("save users default: %v", err)
	}
	if g, found, _ := store.GetNotifySettings(ctx, u.ID, usersScope); !found || g.MuteUntil == nil || *g.MuteUntil != dmute {
		t.Fatalf("users default = %+v found %v, want mute=%d", g, found, dmute)
	}
	// peer A 不受类别默认影响。
	if g, _, _ := store.GetNotifySettings(ctx, u.ID, scopeA); g.MuteUntil == nil || *g.MuteUntil != mute {
		t.Fatalf("scope A polluted by users default: %+v", g)
	}

	// upsert 覆盖 + 批量（mute_until 是 TL int32，取接近上限的合法值）。
	mute2 := 2100000000
	if err := store.SaveNotifySettings(ctx, u.ID, domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peerB}, domain.PeerNotifySettings{MuteUntil: &mute2}); err != nil {
		t.Fatalf("save scope B: %v", err)
	}
	batch, err := store.GetPeerNotifySettings(ctx, u.ID, []domain.Peer{peerA, peerB, {Type: domain.PeerTypeUser, ID: 9999}})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("batch len = %d, want 2 (peerA+peerB, 9999 unset)", len(batch))
	}
	if a, ok := batch[peerA]; !ok || a.MuteUntil == nil || *a.MuteUntil != mute {
		t.Fatalf("batch peerA = %+v ok %v", a, ok)
	}
	if b, ok := batch[peerB]; !ok || b.MuteUntil == nil || *b.MuteUntil != mute2 {
		t.Fatalf("batch peerB = %+v ok %v", b, ok)
	}

	// AllPeerNotifySettings：返全部整-peer 设置（peerA+peerB），排除类别默认（per-user notify 缓存的加载源）。
	all, err := store.AllPeerNotifySettings(ctx, u.ID)
	if err != nil {
		t.Fatalf("all peer notify settings: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("AllPeerNotifySettings = %d, want 2 (peerA+peerB, 类别默认不计)", len(all))
	}
	if a, ok := all[peerA]; !ok || a.MuteUntil == nil || *a.MuteUntil != mute {
		t.Fatalf("AllPeerNotifySettings peerA = %+v ok %v", a, ok)
	}

	// ListNotifyExceptions：只返 per-peer 非默认（peerA+peerB），排除类别默认。
	exceptions, err := store.ListNotifyExceptions(ctx, u.ID)
	if err != nil {
		t.Fatalf("list exceptions: %v", err)
	}
	if len(exceptions) != 2 {
		t.Fatalf("exceptions = %d, want 2 (peerA+peerB, 类别默认不计)", len(exceptions))
	}
	seen := map[domain.Peer]bool{}
	for _, ex := range exceptions {
		seen[ex.Peer] = true
	}
	if !seen[peerA] || !seen[peerB] {
		t.Fatalf("exceptions peers = %v, want peerA+peerB", seen)
	}

	// reset 清空全部作用域。
	for _, id := range []int{42, 43} {
		if err := store.SaveNotifySettings(ctx, u.ID, domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peerB, TopicID: id}, domain.PeerNotifySettings{MuteUntil: &mute}); err != nil {
			t.Fatal(err)
		}
	}
	topics, err := store.GetForumTopicNotifySettings(ctx, u.ID, peerB, []int{1, 42})
	if err != nil || len(topics) != 1 || topics[42].MuteUntil == nil || *topics[42].MuteUntil != mute {
		t.Fatalf("forum batch = %+v, err %v", topics, err)
	}
	for _, isolated := range []struct {
		owner int64
		peer  domain.Peer
	}{{u.ID + 1, peerB}, {u.ID, peerA}} {
		settings, err := store.GetForumTopicNotifySettings(ctx, isolated.owner, isolated.peer, []int{42})
		if err != nil || len(settings) != 0 {
			t.Fatalf("isolated forum settings = %+v, err %v", settings, err)
		}
	}
	if err := store.ResetNotifySettingsWithDelivery(ctx, u.ID, func(snapshot storepkg.NotifySettingsResetSnapshot) ([]storepkg.DeliveryEffect, error) {
		return []storepkg.DeliveryEffect{storepkg.AbsoluteDeliveryEffect(storepkg.DeliveryOutboxEnqueue{
			TargetUserID: snapshot.OwnerUserID, Payload: []byte{1}, RecoveryPolicy: storepkg.OutboxRecoveryAbsoluteReload,
		})}, nil
	}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if ex, _ := store.ListNotifyExceptions(ctx, u.ID); len(ex) != 0 {
		t.Fatalf("exceptions after reset = %d, want 0", len(ex))
	}
	if _, found, _ := store.GetNotifySettings(ctx, u.ID, scopeA); found {
		t.Fatalf("scope A must be gone after reset")
	}
	if _, found, _ := store.GetNotifySettings(ctx, u.ID, usersScope); found {
		t.Fatalf("users default must be gone after reset")
	}
}

func TestNotifySettingsAndDeliveryCommitAtomicallyPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	notify := NewPasswordStore(pool)
	users := NewUserStore(pool)
	u, err := users.Create(ctx, domain.User{AccessHash: 93, Phone: "+1664" + randomSuffix(t) + "01", FirstName: "NotifyAtomic"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	scope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 7101}}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM edge_delivery_outbox WHERE target_user_id = $1", u.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM notify_settings WHERE owner_user_id = $1", u.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", u.ID)
	})

	mute := 1234
	bad := storepkg.DeliveryOutboxEnqueue{
		TargetUserID: u.ID, RecoveryPolicy: storepkg.OutboxRecoveryAbsoluteReload,
	}
	if err := notify.SaveNotifySettingsWithDelivery(ctx, u.ID, scope, domain.PeerNotifySettings{MuteUntil: &mute}, bad); err == nil {
		t.Fatal("invalid delivery unexpectedly committed")
	}
	if _, found, err := notify.GetNotifySettings(ctx, u.ID, scope); err != nil || found {
		t.Fatalf("setting survived rolled-back delivery: found=%v err=%v", found, err)
	}

	payload := []byte{1, 2, 3, 4}
	good := bad
	good.Payload = payload
	if err := notify.SaveNotifySettingsWithDelivery(ctx, u.ID, scope, domain.PeerNotifySettings{MuteUntil: &mute}, good); err != nil {
		t.Fatalf("save atomic notify delivery: %v", err)
	}
	if _, found, err := notify.GetNotifySettings(ctx, u.ID, scope); err != nil || !found {
		t.Fatalf("committed setting: found=%v err=%v", found, err)
	}
	var gotPayload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM edge_delivery_outbox WHERE target_user_id = $1 ORDER BY id DESC LIMIT 1`, u.ID).Scan(&gotPayload); err != nil {
		t.Fatalf("load committed delivery: %v", err)
	}
	if string(gotPayload) != string(payload) {
		t.Fatalf("delivery payload = %v, want %v", gotPayload, payload)
	}
}
