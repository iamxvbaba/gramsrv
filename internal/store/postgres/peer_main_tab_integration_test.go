package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestPeerMainTabStorePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPeerMainTabStore(pool)
	user := domain.Peer{Type: domain.PeerTypeUser, ID: 1_780_990_001}
	channel := domain.Peer{Type: domain.PeerTypeChannel, ID: 1_780_990_001}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM peer_main_tabs WHERE peer_id = $1`, user.ID)
	})

	if tab, err := store.MainTab(ctx, user); err != nil || tab != "" {
		t.Fatalf("unset tab = %q err %v, want empty", tab, err)
	}
	if err := store.SetMainTab(ctx, user, "gifts"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMainTab(ctx, channel, "posts"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMainTab(ctx, user, "media"); err != nil {
		t.Fatal(err)
	}
	if tab, err := store.MainTab(ctx, user); err != nil || tab != "media" {
		t.Fatalf("user tab = %q err %v, want media (updated in place)", tab, err)
	}
	if tab, err := store.MainTab(ctx, channel); err != nil || tab != "posts" {
		t.Fatalf("channel tab = %q err %v, want posts (keyed by peer type)", tab, err)
	}
	if err := store.SetMainTab(ctx, user, ""); err != nil {
		t.Fatal(err)
	}
	if tab, err := store.MainTab(ctx, user); err != nil || tab != "" {
		t.Fatalf("cleared tab = %q err %v, want empty", tab, err)
	}
}
