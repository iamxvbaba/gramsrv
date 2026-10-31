package rpc

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

const (
	stickerCatalogCacheMaxEntries = 8
	// stickerCatalogCacheTTL aligns with the client's ~5min trending/sticker-catalog
	// refresh rate: within the TTL, getAllStickers/
	// getFeaturedStickers/getStickers(emoji index rebuild) reuse the same set
	// catalog, avoiding a ListStickerSets per request.
	stickerCatalogCacheTTL = 5 * time.Minute
)

// stickerCatalogCache caches kind->sticker set catalog (global, not per-viewer:
// ListStickerSets has no user dimension). Built-in TTL + singleflight let the
// "look up then compare hash" catalog RPCs avoid hitting PG when the hash matches.
type stickerCatalogCache struct {
	cache *readmodelcache.Cache[domain.StickerSetKind, []domain.StickerSet]
}

func newStickerCatalogCache(now func() time.Time) *stickerCatalogCache {
	return &stickerCatalogCache{
		cache: readmodelcache.New[domain.StickerSetKind, []domain.StickerSet](readmodelcache.Config[domain.StickerSetKind, []domain.StickerSet]{
			MaxEntries: stickerCatalogCacheMaxEntries,
			TTL:        stickerCatalogCacheTTL,
			Now:        now,
		}),
	}
}

// stickerCatalogSets returns the (cached) sticker set catalog for a kind. Returns
// nil when Files is missing or errors. The returned slice is read-only (callers
// must not modify elements); derive hash/filter separately when needed.
func (r *Router) stickerCatalogSets(ctx context.Context, kind domain.StickerSetKind) []domain.StickerSet {
	sets, _ := r.loadStickerCatalogSets(ctx, kind)
	return sets
}

// loadStickerCatalogSets preserves dependency and storage errors for callers
// which must distinguish an empty public catalog from an unavailable one.
func (r *Router) loadStickerCatalogSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	if r.deps.Files == nil {
		return nil, internalErr()
	}
	if r.stickerCatalog == nil {
		return r.deps.Files.ListStickerSets(ctx, kind)
	}
	return r.stickerCatalog.cache.GetOrLoad(ctx, kind, func() ([]domain.StickerSet, error) {
		return r.deps.Files.ListStickerSets(ctx, kind)
	})
}

func (r *Router) invalidateStickerCatalog(kind domain.StickerSetKind) {
	if r.stickerCatalog == nil || r.stickerCatalog.cache == nil {
		return
	}
	r.stickerCatalog.cache.Invalidate(kind)
}
