package postgres

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestThemeStoreRoundTripPostgres regresses migration 0019: persistence of custom cloud themes and
// per-user install lists, including a JSONB settings round-trip, slug uniqueness, update of a missing
// theme, and install ordering -- aligned with the memory implementation's behavior.
func TestThemeStoreRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewThemeStore(pool)
	users := NewUserStore(pool)
	suffix := randomSuffix(t)

	creator, err := users.Create(ctx, domain.User{AccessHash: 71, Phone: "+1665" + suffix + "01", FirstName: "ThemeCreator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM theme_user_installs WHERE user_id = $1", creator.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM themes WHERE creator_user_id = $1", creator.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", creator.ID)
	})

	a := domain.Theme{
		ID:            randomThemeID(),
		AccessHash:    9988,
		CreatorUserID: creator.ID,
		Slug:          "pg-alpha-" + suffix,
		Title:         "Alpha",
		DocumentID:    7777,
		Settings: []domain.ThemeSettingsSpec{{
			BaseTheme:         domain.ThemeBaseNight,
			AccentColor:       0x3997d3,
			OutboxAccentColor: 0x56b7e8,
			HasOutboxAccent:   true,
			MessageColors:     []int{0xd4f1ff, 0xb9e4ff},
			Wallpaper:         &domain.ThemeWallpaperSpec{BackgroundColors: []int{0x111, 0x222}, Dark: true},
		}},
		CreatedAt: 1700000000,
	}
	if err := store.CreateTheme(ctx, a); err != nil {
		t.Fatalf("create theme: %v", err)
	}

	// JSONB settings round-trip.
	got, ok, err := store.GetThemeByID(ctx, a.ID)
	if err != nil || !ok {
		t.Fatalf("get by id = ok %v err %v", ok, err)
	}
	if len(got.Settings) != 1 || got.Settings[0].BaseTheme != domain.ThemeBaseNight ||
		got.Settings[0].AccentColor != 0x3997d3 || !got.Settings[0].HasOutboxAccent ||
		got.Settings[0].Wallpaper == nil || !got.Settings[0].Wallpaper.Dark {
		t.Fatalf("settings round-trip mismatch: %+v", got.Settings)
	}

	// Slug uniqueness conflict -> ErrThemeSlugTaken.
	dup := a
	dup.ID = randomThemeID()
	if err := store.CreateTheme(ctx, dup); !errors.Is(err, domain.ErrThemeSlugTaken) {
		t.Fatalf("dup slug err = %v, want ErrThemeSlugTaken", err)
	}

	// get by slug.
	if bySlug, ok, _ := store.GetThemeBySlug(ctx, a.Slug); !ok || bySlug.ID != a.ID {
		t.Fatalf("get by slug = ok %v id %d", ok, bySlug.ID)
	}

	// Update of a missing theme -> ErrThemeNotFound.
	ghost := a
	ghost.ID = randomThemeID()
	ghost.Slug = "ghost-" + suffix
	if err := store.UpdateTheme(ctx, ghost); !errors.Is(err, domain.ErrThemeNotFound) {
		t.Fatalf("update missing err = %v, want ErrThemeNotFound", err)
	}

	// increment installs.
	if err := store.IncrementInstalls(ctx, a.ID); err != nil {
		t.Fatalf("increment: %v", err)
	}
	if got, _, _ := store.GetThemeByID(ctx, a.ID); got.InstallsCount != 1 {
		t.Fatalf("installs = %d, want 1", got.InstallsCount)
	}

	// Install list order.
	b := a
	b.ID = randomThemeID()
	b.Slug = "pg-beta-" + suffix
	if err := store.CreateTheme(ctx, b); err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := store.SetInstalled(ctx, creator.ID, b.ID, false); err != nil {
		t.Fatalf("install b: %v", err)
	}
	if err := store.SetInstalled(ctx, creator.ID, a.ID, true); err != nil {
		t.Fatalf("install a: %v", err)
	}
	list, err := store.ListInstalledByUser(ctx, creator.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("list installed = %d err %v, want 2", len(list), err)
	}
	if list[0].ID != b.ID || list[1].ID != a.ID {
		t.Fatalf("install order = [%d,%d], want [%d,%d]", list[0].ID, list[1].ID, b.ID, a.ID)
	}

	// Remove.
	if err := store.RemoveInstalled(ctx, creator.ID, b.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if list, _ := store.ListInstalledByUser(ctx, creator.ID); len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("after remove = %+v, want [a]", list)
	}

	// ListThemesForUser: created ∪ installed. Both a and b were created by creator, so even though b
	// was removed from the install list it should still be returned through the creator branch (cross-device getThemes sync relies on it).
	forUser, err := store.ListThemesForUser(ctx, creator.ID)
	if err != nil {
		t.Fatalf("list themes for user: %v", err)
	}
	gotIDs := map[int64]bool{}
	for _, t := range forUser {
		gotIDs[t.ID] = true
	}
	if !gotIDs[a.ID] || !gotIDs[b.ID] {
		t.Fatalf("ListThemesForUser = %d themes (a=%v b=%v), want both creator themes", len(forUser), gotIDs[a.ID], gotIDs[b.ID])
	}
}

// randomThemeID generates a positive 63-bit id (avoiding collision with the static catalog's reserved range).
func randomThemeID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:]) & 0x7fffffffffffffff)
}
