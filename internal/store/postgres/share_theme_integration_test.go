package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestShareThemeStorePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{AccessHash: 71, Phone: "+1997" + suffix + "01", FirstName: "ThemeOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM peer_share_themes WHERE peer_type='user' AND peer_id=$1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})
	s := NewShareThemeStore(pool)

	if _, found, err := s.GetShareTheme(ctx, peer); err != nil || found {
		t.Fatalf("initial theme = %v, %v; want default", found, err)
	}
	theme := &domain.ShareTheme{Kind: domain.ShareThemeEmojiBackdrop, BackdropID: 5,
		Emojis: []domain.ShareThemeEmoji{{Emoticon: "🔥"}, {DocumentID: 42}}}
	if err := s.SetShareTheme(ctx, peer, theme); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetShareTheme(ctx, peer)
	if err != nil || !found || got.Kind != domain.ShareThemeEmojiBackdrop || got.BackdropID != 5 ||
		len(got.Emojis) != 2 || got.Emojis[0].Emoticon != "🔥" || got.Emojis[1].DocumentID != 42 {
		t.Fatalf("theme = %+v, %v, %v", got, found, err)
	}
	// Replace with a backdrop-only theme.
	if err := s.SetShareTheme(ctx, peer, &domain.ShareTheme{Kind: domain.ShareThemeEmojiBackdrop, BackdropID: 9}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetShareTheme(ctx, peer); got.BackdropID != 9 || len(got.Emojis) != 0 {
		t.Fatalf("replaced theme = %+v", got)
	}
	// A gift theme must reference an existing unique gift.
	if err := s.SetShareTheme(ctx, peer, &domain.ShareTheme{Kind: domain.ShareThemeGift, UniqueGiftID: 1 << 60}); err == nil {
		t.Fatal("gift theme for a missing unique gift was stored")
	}
	if err := s.SetShareTheme(ctx, peer, &domain.ShareTheme{Kind: domain.ShareThemeEmojiBackdrop}); !errors.Is(err, domain.ErrShareThemeInvalid) {
		t.Fatalf("empty theme err = %v", err)
	}
	if err := s.SetShareTheme(ctx, peer, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetShareTheme(ctx, peer); found {
		t.Fatal("reset theme still stored")
	}
	if _, err := s.ShareThemeBackdrops(ctx); err != nil {
		t.Fatalf("backdrops: %v", err)
	}
}
