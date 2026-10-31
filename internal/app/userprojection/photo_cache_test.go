package userprojection

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

type countingPhotoProvider struct {
	kindCalls  int
	queriedIDs []int64
	refs       map[int64]domain.ProfilePhotoRef
}

type blockingFirstPhotoProvider struct {
	started chan struct{}
	release chan struct{}

	mu        sync.Mutex
	firstUsed bool
	first     map[int64]domain.ProfilePhotoRef
	refs      map[int64]domain.ProfilePhotoRef
}

func (p *blockingFirstPhotoProvider) CurrentProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	return p.CurrentProfilePhotosKind(ctx, ownerType, ownerIDs, domain.ProfilePhotoKindProfile)
}

func (p *blockingFirstPhotoProvider) CurrentProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error) {
	p.mu.Lock()
	if !p.firstUsed {
		p.firstUsed = true
		first := clonePhotoRefs(p.first, ownerIDs)
		p.mu.Unlock()
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return first, nil
	}
	out := clonePhotoRefs(p.refs, ownerIDs)
	p.mu.Unlock()
	return out, nil
}

func (p *blockingFirstPhotoProvider) setRef(ownerID int64, ref domain.ProfilePhotoRef) {
	p.mu.Lock()
	p.refs[ownerID] = ref
	p.mu.Unlock()
}

func clonePhotoRefs(in map[int64]domain.ProfilePhotoRef, ownerIDs []int64) map[int64]domain.ProfilePhotoRef {
	out := make(map[int64]domain.ProfilePhotoRef, len(ownerIDs))
	for _, id := range ownerIDs {
		ref, ok := in[id]
		if !ok {
			continue
		}
		out[id] = cloneCachedProfilePhotoRef(ref)
	}
	return out
}

func (p *countingPhotoProvider) CurrentProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	return p.CurrentProfilePhotosKind(ctx, ownerType, ownerIDs, domain.ProfilePhotoKindProfile)
}

func (p *countingPhotoProvider) CurrentProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error) {
	p.kindCalls++
	p.queriedIDs = append(p.queriedIDs, ownerIDs...)
	out := map[int64]domain.ProfilePhotoRef{}
	for _, id := range ownerIDs {
		if ref, ok := p.refs[id]; ok {
			out[id] = ref
		}
	}
	return out, nil
}

func TestCachedPhotoProviderCachesHitsAndMisses(t *testing.T) {
	inner := &countingPhotoProvider{refs: map[int64]domain.ProfilePhotoRef{1: {PhotoID: 111}}}
	now := time.Unix(1000, 0)
	c := newCachedPhotoProviderWithClock(inner, time.Minute, func() time.Time { return now })
	ctx := context.Background()

	// First time: both owners miss, so the underlying store is queried once.
	got, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1, 2}, domain.ProfilePhotoKindProfile)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if got[1].PhotoID != 111 {
		t.Fatalf("owner 1 ref = %+v, want PhotoID 111", got[1])
	}
	if _, ok := got[2]; ok {
		t.Fatalf("owner 2 should have no photo")
	}
	if inner.kindCalls != 1 || len(inner.queriedIDs) != 2 {
		t.Fatalf("first call: kindCalls=%d queried=%v, want 1 call of 2 ids", inner.kindCalls, inner.queriedIDs)
	}

	// Second time (within TTL): all hits in cache (including owner 2's negative result), no further underlying query.
	got, err = c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1, 2}, domain.ProfilePhotoKindProfile)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if got[1].PhotoID != 111 {
		t.Fatalf("cached owner 1 ref = %+v, want PhotoID 111", got[1])
	}
	if inner.kindCalls != 1 {
		t.Fatalf("second call hit DB: kindCalls=%d, want still 1", inner.kindCalls)
	}

	// Different kinds are independent cache keys: fallback should query again.
	if _, err = c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindFallback); err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if inner.kindCalls != 2 {
		t.Fatalf("fallback kind should query: kindCalls=%d, want 2", inner.kindCalls)
	}

	// After the TTL expires it re-queries the underlying store.
	now = now.Add(2 * time.Minute)
	if _, err = c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1, 2}, domain.ProfilePhotoKindProfile); err != nil {
		t.Fatalf("after ttl: %v", err)
	}
	if inner.kindCalls != 3 {
		t.Fatalf("after ttl should re-query: kindCalls=%d, want 3", inner.kindCalls)
	}
}

func TestCachedPhotoProviderDefaultTTLRetainsLoginRampWorkingSet(t *testing.T) {
	inner := &countingPhotoProvider{refs: map[int64]domain.ProfilePhotoRef{}}
	now := time.Unix(1000, 0)
	c := newCachedPhotoProvider(inner, 0, DefaultPhotoCacheMaxEntries, func() time.Time { return now })
	ctx := context.Background()

	if _, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindProfile); err != nil {
		t.Fatalf("first: %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindProfile); err != nil {
		t.Fatalf("within login ramp: %v", err)
	}
	if inner.kindCalls != 1 {
		t.Fatalf("default TTL expired inside 60s login ramp: calls=%d, want 1", inner.kindCalls)
	}

	now = now.Add(DefaultPhotoCacheTTL)
	if _, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindProfile); err != nil {
		t.Fatalf("after safety TTL: %v", err)
	}
	if inner.kindCalls != 2 {
		t.Fatalf("safety TTL did not reload: calls=%d, want 2", inner.kindCalls)
	}
}

func TestCachedPhotoProviderConfiguredCapacityEvictsOneLRUKey(t *testing.T) {
	inner := &countingPhotoProvider{refs: map[int64]domain.ProfilePhotoRef{}}
	now := time.Unix(1000, 0)
	c := newCachedPhotoProvider(inner, time.Hour, 2, func() time.Time { return now })
	ctx := context.Background()
	read := func(ownerID int64) {
		t.Helper()
		if _, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{ownerID}, domain.ProfilePhotoKindProfile); err != nil {
			t.Fatalf("owner %d: %v", ownerID, err)
		}
	}

	for _, ownerID := range []int64{1, 2, 1, 3, 1, 2} {
		read(ownerID)
	}
	if inner.kindCalls != 4 {
		t.Fatalf("kind calls = %d, want 4 with owner 1 touched and only owner 2 evicted", inner.kindCalls)
	}
	if c.cache.Len() != 2 {
		t.Fatalf("cache entries = %d, want configured capacity 2", c.cache.Len())
	}
}

func TestCachedPhotoProviderInvalidatesOwnerAndFlushes(t *testing.T) {
	inner := &countingPhotoProvider{refs: map[int64]domain.ProfilePhotoRef{1: {PhotoID: 111}}}
	now := time.Unix(1000, 0)
	c := newCachedPhotoProviderWithClock(inner, time.Minute, func() time.Time { return now })
	ctx := context.Background()

	if _, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindProfile); err != nil {
		t.Fatalf("prime profile: %v", err)
	}
	if _, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindFallback); err != nil {
		t.Fatalf("prime fallback: %v", err)
	}
	inner.refs[1] = domain.ProfilePhotoRef{PhotoID: 222}
	c.InvalidateOwner(domain.PeerTypeUser, 1)

	profile, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindProfile)
	if err != nil {
		t.Fatalf("profile after invalidation: %v", err)
	}
	if profile[1].PhotoID != 222 {
		t.Fatalf("profile after invalidation = %+v, want 222", profile[1])
	}
	if _, err = c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindFallback); err != nil {
		t.Fatalf("fallback after invalidation: %v", err)
	}
	if inner.kindCalls != 4 {
		t.Fatalf("InvalidateOwner should drop both kinds: kindCalls=%d, want 4", inner.kindCalls)
	}

	inner.refs[1] = domain.ProfilePhotoRef{PhotoID: 333}
	c.FlushReadModelCache()
	profile, err = c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindProfile)
	if err != nil {
		t.Fatalf("profile after flush: %v", err)
	}
	if profile[1].PhotoID != 333 {
		t.Fatalf("profile after flush = %+v, want 333", profile[1])
	}
}

func TestCachedPhotoProviderDoesNotRefillStaleSnapshotAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	inner := &blockingFirstPhotoProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		first:   map[int64]domain.ProfilePhotoRef{1: {PhotoID: 111}},
		refs:    map[int64]domain.ProfilePhotoRef{1: {PhotoID: 111}},
	}
	c := NewCachedPhotoProvider(inner, time.Minute)

	type readResult struct {
		refs map[int64]domain.ProfilePhotoRef
		err  error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		got, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindProfile)
		resultCh <- readResult{refs: got, err: err}
	}()
	waitForCacheTestSignal(t, inner.started)

	inner.setRef(1, domain.ProfilePhotoRef{PhotoID: 222})
	c.InvalidateOwner(domain.PeerTypeUser, 1)
	close(inner.release)

	var result readResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for photo read")
	}
	if result.err != nil {
		t.Fatalf("photo read: %v", result.err)
	}
	if result.refs[1].PhotoID != 222 {
		t.Fatalf("photo after concurrent invalidation = %+v, want 222", result.refs[1])
	}

	cachedHit, err := c.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{1}, domain.ProfilePhotoKindProfile)
	if err != nil {
		t.Fatalf("cached hit after stale load retry: %v", err)
	}
	if cachedHit[1].PhotoID != 222 {
		t.Fatalf("cached photo after stale load retry = %+v, want 222", cachedHit[1])
	}
}
