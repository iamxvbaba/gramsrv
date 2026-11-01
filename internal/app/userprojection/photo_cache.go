package userprojection

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

// DefaultPhotoCacheTTL is the fallback lifetime of the avatar projection cache; normal correctness
// relies on explicit invalidation after the write side triggers read_model_versions/NOTIFY, and
// the TTL only covers notifications missed outside the process or manual database edits.
const (
	DefaultPhotoCacheTTL        = 24 * time.Hour
	DefaultPhotoCacheMaxEntries = 200_000
)

// combinedPhotoProvider is the underlying provider with both batch and kind photo query
// capabilities (postgres MediaStore satisfies it).
type combinedPhotoProvider interface {
	ProfilePhotoProvider
	ProfilePhotoKindProvider
}

type photoCacheKey struct {
	ownerType domain.PeerType
	ownerID   int64
	kind      domain.ProfilePhotoKind
}

// photoCacheValue includes negative caching: has=false means "already queried, this owner
// has no photo of this kind", so avatar-less owners do not repeatedly hit PG.
type photoCacheValue struct {
	ref domain.ProfilePhotoRef
	has bool
}

// CachedPhotoProvider adds a bounded in-process cache in front of the owner's current
// profile/fallback avatar queries, built on the shared cache primitive (single-entry LRU eviction /
// epoch guard / clone; batches go through GetOrLoadBatch). projectBatch / ForViewers always issue
// CurrentProfilePhotosKind twice per owner batch (profile+fallback), and a base user hit in redis
// does not short-circuit -- high-frequency RPCs that "return users" (getUsers and friends) would run
// those two avatar queries as often as the RPC itself. This caches results (negative ones included)
// by (ownerType, ownerID, kind), so a cached owner does not reach PG.
//
// Note: it only caches owner-only profile/fallback; personal photo is per-viewer
// (contacts.PersonalPhotos), does not go through this provider, and so has no cross-viewer
// mix-up risk. Privacy trimming happens after projection, outside the cache; what is cached
// is the owner's raw photo ref rather than the projection result, so it does not freeze one
// viewer's visibility for other viewers.
type CachedPhotoProvider struct {
	inner combinedPhotoProvider
	cache *readmodelcache.Cache[photoCacheKey, photoCacheValue]
}

// NewCachedPhotoProvider wraps the underlying provider; ttl<=0 uses DefaultPhotoCacheTTL.
func NewCachedPhotoProvider(inner combinedPhotoProvider, ttl time.Duration) *CachedPhotoProvider {
	return NewCachedPhotoProviderWithMaxEntries(inner, ttl, DefaultPhotoCacheMaxEntries)
}

func NewCachedPhotoProviderWithMaxEntries(inner combinedPhotoProvider, ttl time.Duration, maxEntries int) *CachedPhotoProvider {
	return newCachedPhotoProvider(inner, ttl, maxEntries, nil)
}

// newCachedPhotoProviderWithClock allows injecting a clock, only for tests to advance the TTL deterministically; now=nil uses the real clock.
func newCachedPhotoProviderWithClock(inner combinedPhotoProvider, ttl time.Duration, now func() time.Time) *CachedPhotoProvider {
	return newCachedPhotoProvider(inner, ttl, DefaultPhotoCacheMaxEntries, now)
}

func newCachedPhotoProvider(inner combinedPhotoProvider, ttl time.Duration, maxEntries int, now func() time.Time) *CachedPhotoProvider {
	if inner == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultPhotoCacheTTL
	}
	if maxEntries <= 0 {
		maxEntries = DefaultPhotoCacheMaxEntries
	}
	return &CachedPhotoProvider{
		inner: inner,
		cache: readmodelcache.New[photoCacheKey, photoCacheValue](readmodelcache.Config[photoCacheKey, photoCacheValue]{
			MaxEntries: maxEntries,
			TTL:        ttl,
			Now:        now,
			Clone:      clonePhotoCacheValue,
		}),
	}
}

// CurrentProfilePhotosKind is the cached hot path: it first reads the cache per owner and queries the underlying store in a batch only for owners that missed.
func (c *CachedPhotoProvider) CurrentProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error) {
	if c == nil {
		return nil, nil
	}
	keys := make([]photoCacheKey, 0, len(ownerIDs))
	for _, id := range ownerIDs {
		keys = append(keys, photoCacheKey{ownerType: ownerType, ownerID: id, kind: kind})
	}
	values, err := c.cache.GetOrLoadBatch(ctx, keys,
		func(photoCacheKey) (int64, bool) { return 0, true }, // pure TTL, no version gate
		func(ctx context.Context, missing []photoCacheKey) (map[photoCacheKey]photoCacheValue, error) {
			missingIDs := make([]int64, len(missing))
			for i, k := range missing {
				missingIDs[i] = k.ownerID
			}
			refs, err := c.inner.CurrentProfilePhotosKind(ctx, ownerType, missingIDs, kind)
			if err != nil {
				return nil, err
			}
			out := make(map[photoCacheKey]photoCacheValue, len(missing))
			for _, k := range missing {
				ref, has := refs[k.ownerID]
				out[k] = photoCacheValue{ref: ref, has: has}
			}
			return out, nil
		})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]domain.ProfilePhotoRef, len(values))
	for key, v := range values {
		if v.has {
			out[key.ownerID] = v.ref
		}
	}
	return out, nil
}

// CurrentProfilePhotos is the non-kind fallback path — projectBatch only uses it when the
// provider does not implement ProfilePhotoKindProvider, and since this decorator implements
// the kind interface, production never hits this method; it passes straight through without
// caching, keeping behavior unchanged.
func (c *CachedPhotoProvider) CurrentProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	return c.inner.CurrentProfilePhotos(ctx, ownerType, ownerIDs)
}

func (c *CachedPhotoProvider) InvalidateOwner(ownerType domain.PeerType, ownerID int64) {
	if c == nil || ownerID == 0 {
		return
	}
	c.cache.Invalidate(
		photoCacheKey{ownerType: ownerType, ownerID: ownerID, kind: domain.ProfilePhotoKindProfile},
		photoCacheKey{ownerType: ownerType, ownerID: ownerID, kind: domain.ProfilePhotoKindFallback},
	)
}

func (c *CachedPhotoProvider) FlushReadModelCache() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

func clonePhotoCacheValue(v photoCacheValue) photoCacheValue {
	if v.ref.Stripped != nil {
		v.ref.Stripped = append([]byte(nil), v.ref.Stripped...)
	}
	return v
}
