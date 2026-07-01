package rpc

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

const (
	stickerCatalogCacheMaxEntries = 8
	// stickerCatalogCacheTTL 对齐客户端 trending/贴纸目录 ~5min 刷新率：TTL 内 getAllStickers/
	// getFeaturedStickers/getStickers(emoji 索引重建) 复用同一份集目录，避免每请求 ListStickerSets。
	stickerCatalogCacheTTL = 5 * time.Minute
)

// stickerCatalogCache 缓存 kind→贴纸集目录（全局、非 per-viewer：ListStickerSets 无用户维度）。
// 内建 TTL + singleflight，让"先查后判 hash"的目录类 RPC 在 hash 命中时不打 PG。
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

// stickerCatalogSets 返回某 kind 的（缓存的）贴纸集目录。Files 缺失或出错返回 nil。
// 返回的切片只读（调用方不得修改元素），需要派生时各自计算 hash/过滤。
func (r *Router) stickerCatalogSets(ctx context.Context, kind domain.StickerSetKind) []domain.StickerSet {
	if r.deps.Files == nil {
		return nil
	}
	if r.stickerCatalog == nil {
		sets, err := r.deps.Files.ListStickerSets(ctx, kind)
		if err != nil {
			return nil
		}
		return sets
	}
	sets, err := r.stickerCatalog.cache.GetOrLoad(ctx, kind, func() ([]domain.StickerSet, error) {
		return r.deps.Files.ListStickerSets(ctx, kind)
	})
	if err != nil {
		return nil
	}
	return sets
}

func (r *Router) invalidateStickerCatalog(kind domain.StickerSetKind) {
	if r.stickerCatalog == nil || r.stickerCatalog.cache == nil {
		return
	}
	r.stickerCatalog.cache.Invalidate(kind)
}
