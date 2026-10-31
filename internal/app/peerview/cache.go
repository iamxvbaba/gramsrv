package peerview

import (
	"context"

	"telesrv/internal/domain"
)

// UserResolver resolves users after applying viewer-specific projection.
type UserResolver interface {
	ByIDs(ctx context.Context, viewerUserID int64, userIDs []int64) ([]domain.User, error)
}

// BatchCache caches projected users only for one update-building batch.
// The cache key includes viewerUserID because contacts, privacy, phone visibility
// and personal/fallback photos are viewer-specific.
type BatchCache struct {
	users UserResolver

	byViewer map[int64]map[int64]domain.User
	missing  map[int64]map[int64]struct{}
}

func NewBatchCache(users UserResolver) *BatchCache {
	return &BatchCache{
		users:    users,
		byViewer: make(map[int64]map[int64]domain.User),
		missing:  make(map[int64]map[int64]struct{}),
	}
}

func (c *BatchCache) UsersForView(ctx context.Context, viewerUserID int64, ids []int64) ([]domain.User, error) {
	unique := uniqueIDs(ids)
	if len(unique) == 0 {
		return nil, nil
	}
	byID := c.viewerUsers(viewerUserID)
	missing := c.viewerMissing(viewerUserID)
	load := make([]int64, 0, len(unique))
	for _, id := range unique {
		if _, ok := byID[id]; ok {
			continue
		}
		if _, ok := missing[id]; ok {
			continue
		}
		if u, ok := domain.SystemUserByID(id); ok {
			byID[id] = u
			continue
		}
		if c.users == nil || viewerUserID == 0 {
			missing[id] = struct{}{}
			continue
		}
		load = append(load, id)
	}
	var err error
	if len(load) > 0 {
		var resolved []domain.User
		resolved, err = c.users.ByIDs(ctx, viewerUserID, load)
		if err == nil {
			found := make(map[int64]struct{}, len(resolved))
			for _, u := range resolved {
				if u.ID == 0 {
					continue
				}
				byID[u.ID] = u
				found[u.ID] = struct{}{}
			}
			for _, id := range load {
				if _, ok := found[id]; !ok {
					missing[id] = struct{}{}
				}
			}
		}
	}
	out := make([]domain.User, 0, len(unique))
	for _, id := range unique {
		if u, ok := byID[id]; ok {
			out = append(out, u)
		}
	}
	return out, err
}

// Prime preheats the projected users of one viewer (the cross-viewer fan-out template folds the
// per-recipient ForViewer into a single ForViewers call and then backfills). Existing ids are not
// overwritten (the already-resolved results of the same batch are kept), so preheating and
// on-demand resolution do not fight each other. What is preheated is the post-projection user set
// before real-time presence is layered on — consistent with the UsersForView cache layer; presence
// is still layered on at the output end by the upper layer, so preheating does not break presence
// freshness.
func (c *BatchCache) Prime(viewerUserID int64, users []domain.User) {
	if c == nil || viewerUserID == 0 || len(users) == 0 {
		return
	}
	byID := c.viewerUsers(viewerUserID)
	for _, u := range users {
		if u.ID == 0 {
			continue
		}
		if _, ok := byID[u.ID]; ok {
			continue
		}
		byID[u.ID] = u
	}
}

// PrimeExpected preheats one viewer with the complete result of a bounded batch
// projection. IDs omitted by the resolver are negative-cached as missing, so a
// later fan-out builder cannot silently fall back to a per-viewer ByIDs query.
// System users remain locally synthesised even when the resolver omits them.
func (c *BatchCache) PrimeExpected(viewerUserID int64, expectedIDs []int64, users []domain.User) {
	if c == nil || viewerUserID == 0 {
		return
	}
	c.Prime(viewerUserID, users)
	byID := c.viewerUsers(viewerUserID)
	missing := c.viewerMissing(viewerUserID)
	for _, id := range uniqueIDs(expectedIDs) {
		if _, ok := byID[id]; ok {
			continue
		}
		if system, ok := domain.SystemUserByID(id); ok {
			byID[id] = system
			continue
		}
		missing[id] = struct{}{}
	}
}

func (c *BatchCache) viewerUsers(viewerUserID int64) map[int64]domain.User {
	if byID, ok := c.byViewer[viewerUserID]; ok {
		return byID
	}
	byID := make(map[int64]domain.User)
	c.byViewer[viewerUserID] = byID
	return byID
}

func (c *BatchCache) viewerMissing(viewerUserID int64) map[int64]struct{} {
	if missing, ok := c.missing[viewerUserID]; ok {
		return missing
	}
	missing := make(map[int64]struct{})
	c.missing[viewerUserID] = missing
	return missing
}

func uniqueIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
