package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestNotifySettingsRoundTripPostgres regression for migration 0005: per-scope notify settings are really persisted
// (peer / category default / batch / reset), including nullable fields = unset semantics.
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

	// peer A: only mute_until + silent are set (the rest nil = unset).
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

	// The users category default is independent.
	usersScope := domain.NotifyScope{Kind: domain.NotifyScopeUsers}
	dmute := 100
	if err := store.SaveNotifySettings(ctx, u.ID, usersScope, domain.PeerNotifySettings{MuteUntil: &dmute}); err != nil {
		t.Fatalf("save users default: %v", err)
	}
	if g, found, _ := store.GetNotifySettings(ctx, u.ID, usersScope); !found || g.MuteUntil == nil || *g.MuteUntil != dmute {
		t.Fatalf("users default = %+v found %v, want mute=%d", g, found, dmute)
	}
	// peer A is unaffected by the category default.
	if g, _, _ := store.GetNotifySettings(ctx, u.ID, scopeA); g.MuteUntil == nil || *g.MuteUntil != mute {
		t.Fatalf("scope A polluted by users default: %+v", g)
	}

	// upsert overwrite + batch (mute_until is a TL int32; use a valid value near the upper bound).
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

	// AllPeerNotifySettings: returns all whole-peer settings (peerA+peerB), excluding category defaults (the load source for the per-user notify cache).
	all, err := store.AllPeerNotifySettings(ctx, u.ID)
	if err != nil {
		t.Fatalf("all peer notify settings: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("AllPeerNotifySettings = %d, want 2 (peerA+peerB, category defaults excluded)", len(all))
	}
	if a, ok := all[peerA]; !ok || a.MuteUntil == nil || *a.MuteUntil != mute {
		t.Fatalf("AllPeerNotifySettings peerA = %+v ok %v", a, ok)
	}

	// ListNotifyExceptions: returns only non-default per-peer entries (peerA+peerB), excluding category defaults.
	exceptions, err := store.ListNotifyExceptions(ctx, u.ID)
	if err != nil {
		t.Fatalf("list exceptions: %v", err)
	}
	if len(exceptions) != 2 {
		t.Fatalf("exceptions = %d, want 2 (peerA+peerB, category defaults excluded)", len(exceptions))
	}
	seen := map[domain.Peer]bool{}
	for _, ex := range exceptions {
		seen[ex.Peer] = true
	}
	if !seen[peerA] || !seen[peerB] {
		t.Fatalf("exceptions peers = %v, want peerA+peerB", seen)
	}

	// reset clears all scopes.
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
	if err := store.ResetNotifySettings(ctx, u.ID); err != nil {
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
