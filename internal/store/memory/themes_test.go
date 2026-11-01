package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func sampleTheme(id, creator int64, slug string) domain.Theme {
	return domain.Theme{
		ID:            id,
		AccessHash:    id + 1,
		CreatorUserID: creator,
		Slug:          slug,
		Title:         "T",
		DocumentID:    9000 + id,
		Settings: []domain.ThemeSettingsSpec{{
			BaseTheme:     domain.ThemeBaseDay,
			AccentColor:   0x112233,
			MessageColors: []int{0xaa, 0xbb},
			Wallpaper:     &domain.ThemeWallpaperSpec{BackgroundColors: []int{0x1, 0x2}},
		}},
		CreatedAt: 1700000000,
	}
}

func TestMemoryThemeStoreContract(t *testing.T) {
	ctx := context.Background()
	s := NewThemeStore()

	// Miss read → (zero,false,nil).
	if _, ok, err := s.GetThemeByID(ctx, 1); ok || err != nil {
		t.Fatalf("get missing = ok %v err %v", ok, err)
	}

	a := sampleTheme(101, 7, "alpha")
	if err := s.CreateTheme(ctx, a); err != nil {
		t.Fatalf("create a: %v", err)
	}
	// slug conflict.
	if err := s.CreateTheme(ctx, sampleTheme(102, 7, "alpha")); !errors.Is(err, domain.ErrThemeSlugTaken) {
		t.Fatalf("dup slug err = %v, want ErrThemeSlugTaken", err)
	}

	got, ok, err := s.GetThemeByID(ctx, 101)
	if err != nil || !ok {
		t.Fatalf("get a by id = ok %v err %v", ok, err)
	}
	if got.Slug != "alpha" || len(got.Settings) != 1 || got.Settings[0].BaseTheme != domain.ThemeBaseDay {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// Clone isolation: mutating the returned value does not pollute the internal state.
	got.Settings[0].MessageColors[0] = 0xffff
	if again, _, _ := s.GetThemeByID(ctx, 101); again.Settings[0].MessageColors[0] != 0xaa {
		t.Fatalf("clone leak: internal mutated to %#x", again.Settings[0].MessageColors[0])
	}

	if bySlug, ok, _ := s.GetThemeBySlug(ctx, "alpha"); !ok || bySlug.ID != 101 {
		t.Fatalf("get by slug = ok %v id %d", ok, bySlug.ID)
	}

	// Updating a missing theme → ErrThemeNotFound.
	if err := s.UpdateTheme(ctx, sampleTheme(999, 7, "ghost")); !errors.Is(err, domain.ErrThemeNotFound) {
		t.Fatalf("update missing err = %v, want ErrThemeNotFound", err)
	}

	// IncrementInstalls.
	if err := s.IncrementInstalls(ctx, 101); err != nil {
		t.Fatalf("increment: %v", err)
	}
	if got, _, _ := s.GetThemeByID(ctx, 101); got.InstallsCount != 1 {
		t.Fatalf("installs = %d, want 1", got.InstallsCount)
	}

	// The install list is ordered by install time.
	b := sampleTheme(103, 7, "beta")
	if err := s.CreateTheme(ctx, b); err != nil {
		t.Fatalf("create b: %v", err)
	}
	const user = int64(55)
	if err := s.SetInstalled(ctx, user, 103, false); err != nil {
		t.Fatalf("install 103: %v", err)
	}
	if err := s.SetInstalled(ctx, user, 101, true); err != nil {
		t.Fatalf("install 101: %v", err)
	}
	list, err := s.ListInstalledByUser(ctx, user)
	if err != nil || len(list) != 2 {
		t.Fatalf("list installed = %d err %v, want 2", len(list), err)
	}
	if list[0].ID != 103 || list[1].ID != 101 {
		t.Fatalf("install order = [%d,%d], want [103,101]", list[0].ID, list[1].ID)
	}

	// Remove.
	if err := s.RemoveInstalled(ctx, user, 103); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if list, _ := s.ListInstalledByUser(ctx, user); len(list) != 1 || list[0].ID != 101 {
		t.Fatalf("after remove = %+v, want [101]", list)
	}

	// Installing a missing theme → ErrThemeNotFound.
	if err := s.SetInstalled(ctx, user, 88888, false); !errors.Is(err, domain.ErrThemeNotFound) {
		t.Fatalf("install missing theme err = %v, want ErrThemeNotFound", err)
	}
}
