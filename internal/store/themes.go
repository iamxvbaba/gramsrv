package store

import (
	"context"

	"telesrv/internal/domain"
)

// ThemeStore persists custom cloud themes (account.createTheme etc.) and each user's
// installed/saved theme list. A read miss returns (zero,false,nil); updating/installing a
// nonexistent theme returns domain.ErrThemeNotFound; a slug conflict returns
// domain.ErrThemeSlugTaken. The memory and postgres implementations must behave identically.
type ThemeStore interface {
	CreateTheme(ctx context.Context, t domain.Theme) error
	GetThemeByID(ctx context.Context, id int64) (domain.Theme, bool, error)
	GetThemeBySlug(ctx context.Context, slug string) (domain.Theme, bool, error)
	SlugExists(ctx context.Context, slug string) (bool, error)
	UpdateTheme(ctx context.Context, t domain.Theme) error
	IncrementInstalls(ctx context.Context, id int64) error
	// SetInstalled adds themeID to userID's installed/saved list (upsert, updating dark).
	SetInstalled(ctx context.Context, userID, themeID int64, dark bool) error
	// RemoveInstalled removes from the list (saveTheme unsave). Absent does not error.
	RemoveInstalled(ctx context.Context, userID, themeID int64) error
	ListInstalledByUser(ctx context.Context, userID int64) ([]domain.Theme, error)
	// ListThemesForUser returns all of the user's themes as "created union installed"
	// (deduplicated), for getThemes cross-device sync. Ordered by ascending created_at,id
	// (memory and postgres must match).
	ListThemesForUser(ctx context.Context, userID int64) ([]domain.Theme, error)
}
