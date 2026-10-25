package store

import (
	"context"

	"telesrv/internal/domain"
)

// GifCatalogStore owns the bounded curated catalog served by the built-in @gif.
type GifCatalogStore interface {
	CreateGifCatalogEntry(ctx context.Context, entry domain.GifCatalogEntry) (domain.GifCatalogEntry, error)
	// GifCatalogSeedMatches reports independent filename/content matches. A
	// filename match with a different digest is an operator-visible conflict;
	// a digest match under another name is an idempotent rename.
	GifCatalogSeedMatches(ctx context.Context, filename, sha256 string) (filenameMatch, digestMatch bool, err error)
	ListGifCatalog(ctx context.Context, onlyEnabled bool) ([]domain.GifCatalogEntry, error)
	SetGifCatalogEnabled(ctx context.Context, id int64, enabled bool) (bool, error)
	SetGifCatalogSortOrder(ctx context.Context, id int64, order int) (bool, error)
	SetGifCatalogCategory(ctx context.Context, id int64, category string) (bool, error)
	DeleteGifCatalogEntry(ctx context.Context, id int64) (bool, error)
}
