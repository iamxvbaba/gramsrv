package store

import (
	"context"

	"telesrv/internal/domain"
)

// ShareThemeStore persists Layer 232 share themes of users and channels.
//
// SetShareTheme replaces the peer's theme; nil restores the default theme.
// ShareThemeBackdrops lists every distinct backdrop of published collectible
// revisions, ordered by backdrop ID.
type ShareThemeStore interface {
	GetShareTheme(ctx context.Context, peer domain.Peer) (domain.ShareTheme, bool, error)
	SetShareTheme(ctx context.Context, peer domain.Peer, theme *domain.ShareTheme) error
	ShareThemeBackdrops(ctx context.Context) ([]domain.ShareThemeBackdrop, error)
}
