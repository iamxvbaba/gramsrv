package rpc

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"telesrv/internal/app/peerview"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	maxPeerProjectionUsersPerBatch     = 1000
	maxPeerProjectionRecoveryCalls     = 64
	maxPeerProjectionAttemptedOwnerIDs = 16000
)

var (
	ErrDurableUserProjectionIncomplete     = errors.New("durable user projection is incomplete")
	ErrUserProjectionCapacityRecoveryLimit = errors.New("user projection capacity recovery limit exceeded")
)

type userProjectionRecoveryBudget struct {
	maxCalls  int
	maxItems  int
	calls     int
	attempted int
}

func (b *userProjectionRecoveryBudget) consume(items int) error {
	if b == nil {
		return nil
	}
	itemsExceeded := items < 0 || b.attempted > b.maxItems || items > b.maxItems-b.attempted
	if b.calls >= b.maxCalls || itemsExceeded {
		return fmt.Errorf(
			"%w: calls=%d/%d attempted=%d/%d next=%d",
			ErrUserProjectionCapacityRecoveryLimit,
			b.calls,
			b.maxCalls,
			b.attempted,
			b.maxItems,
			items,
		)
	}
	b.calls++
	b.attempted += items
	return nil
}

// viewerPeerCache is a short-lived cache within a single outbox/fanout build. User profiles carry
// contacts, privacy, avatars, and online status, so they must be isolated by viewerUserID and never
// reused across perspectives.
type viewerPeerCache struct {
	r *Router

	users        *peerview.BatchCache
	channels     map[int64]map[int64]domain.Channel
	missingChats map[int64]map[int64]struct{}
}

func newViewerPeerCache(r *Router) *viewerPeerCache {
	var users peerview.UserResolver
	if r != nil {
		users = r.deps.Users
	}
	return &viewerPeerCache{
		r:            r,
		users:        peerview.NewBatchCache(users),
		channels:     make(map[int64]map[int64]domain.Channel),
		missingChats: make(map[int64]map[int64]struct{}),
	}
}

func (c *viewerPeerCache) usersForIDs(ctx context.Context, viewerUserID int64, ids []int64) []domain.User {
	unique := uniquePeerIDs(ids)
	users, err := c.resolveUsersForIDs(ctx, viewerUserID, unique)
	if err != nil && c != nil && c.r != nil {
		c.r.log.Warn("batch resolve users for peer projection failed",
			zap.Int64("viewer_user_id", viewerUserID),
			zap.Int("count", len(unique)),
			zap.Error(err),
		)
	}
	return users
}

// usersForIDsStrict rejects resolver errors and incomplete durable envelopes.
// Callers must not merge a partial projection with raw store users while
// advancing an account/channel difference cursor or dispatching an outbox row.
func (c *viewerPeerCache) usersForIDsStrict(ctx context.Context, viewerUserID int64, ids []int64) ([]domain.User, error) {
	unique := uniquePeerIDs(ids)
	users, err := c.resolveUsersForIDs(ctx, viewerUserID, unique)
	if err != nil {
		return nil, fmt.Errorf("resolve durable users for viewer %d: %w", viewerUserID, err)
	}
	if missingID, missing := missingProjectedUserID(unique, users); missing {
		return nil, fmt.Errorf("%w: viewer_user_id=%d missing_user_id=%d", ErrDurableUserProjectionIncomplete, viewerUserID, missingID)
	}
	return users, nil
}

func (c *viewerPeerCache) resolveUsersForIDs(ctx context.Context, viewerUserID int64, unique []int64) ([]domain.User, error) {
	if c == nil || c.users == nil {
		return nil, nil
	}
	budget := userProjectionRecoveryBudget{
		maxCalls: maxPeerProjectionRecoveryCalls,
		maxItems: maxPeerProjectionAttemptedOwnerIDs,
	}
	users := make([]domain.User, 0, len(unique))
	for start := 0; start < len(unique); start += maxPeerProjectionUsersPerBatch {
		end := start + maxPeerProjectionUsersPerBatch
		if end > len(unique) {
			end = len(unique)
		}
		batch, err := c.usersForIDBatch(ctx, viewerUserID, unique[start:end], &budget)
		if err != nil {
			return nil, err
		}
		users = append(users, batch...)
	}
	if c.r == nil {
		return users, nil
	}
	return c.r.withUsersPresence(users), nil
}

func (c *viewerPeerCache) usersForIDBatch(ctx context.Context, viewerUserID int64, ids []int64, budget *userProjectionRecoveryBudget) ([]domain.User, error) {
	if err := budget.consume(len(ids)); err != nil {
		return nil, err
	}
	users, err := c.users.UsersForView(ctx, viewerUserID, ids)
	if err == nil || !isSparseProjectionCapacityError(err) || len(ids) < 2 {
		return users, err
	}
	middle := len(ids) / 2
	left, leftErr := c.usersForIDBatch(ctx, viewerUserID, ids[:middle], budget)
	if leftErr != nil {
		return nil, leftErr
	}
	right, rightErr := c.usersForIDBatch(ctx, viewerUserID, ids[middle:], budget)
	if rightErr != nil {
		return nil, rightErr
	}
	return append(left, right...), nil
}

// primeUsers warms the underlying BatchCache per viewer with the results of a one-shot cross-viewer
// projection (ByIDsForViewers), so subsequent per-viewer usersForIDs hits the cache and no longer
// resolves the projection viewer by viewer. Called only by the fan-out warm-up path.
func (c *viewerPeerCache) primeUsers(viewerUserID int64, users []domain.User) {
	if c == nil || c.users == nil {
		return
	}
	c.users.Prime(viewerUserID, users)
}

// primeExpectedUsers follows a caller-side completeness check. PrimeExpected
// also negative-caches omitted system-resolved IDs so later lookups cannot
// silently fall back to a scalar resolver.
func (c *viewerPeerCache) primeExpectedUsers(viewerUserID int64, expectedIDs []int64, users []domain.User) {
	if c == nil || c.users == nil || viewerUserID == 0 {
		return
	}
	c.users.PrimeExpected(viewerUserID, expectedIDs, users)
}

func missingProjectedUserID(expectedIDs []int64, users []domain.User) (int64, bool) {
	found := make(map[int64]struct{}, len(users))
	for _, user := range users {
		if user.ID != 0 {
			found[user.ID] = struct{}{}
		}
	}
	for _, id := range uniquePeerIDs(expectedIDs) {
		if _, system := domain.SystemUserByID(id); system {
			continue
		}
		if _, ok := found[id]; !ok {
			return id, true
		}
	}
	return 0, false
}

func isSparseProjectionCapacityError(err error) bool {
	return errors.Is(err, store.ErrActiveChannelMemberPairsLimit) ||
		errors.Is(err, appusers.ErrBatchUsersLimit) ||
		errors.Is(err, appusers.ErrBatchViewerCells)
}

func (c *viewerPeerCache) channelsForIDs(ctx context.Context, viewerUserID int64, ids []int64) []domain.Channel {
	unique := uniquePeerIDs(ids)
	if c == nil || c.r == nil || len(unique) == 0 || c.r.deps.Channels == nil || viewerUserID == 0 {
		return nil
	}
	byID := c.viewerChannels(viewerUserID)
	missing := c.viewerMissingChannels(viewerUserID)
	load := make([]int64, 0, len(unique))
	for _, id := range unique {
		if _, ok := byID[id]; ok {
			continue
		}
		if _, ok := missing[id]; ok {
			continue
		}
		load = append(load, id)
	}
	if len(load) > 0 {
		views, err := c.r.deps.Channels.GetChannels(ctx, viewerUserID, load)
		if err != nil {
			for _, id := range load {
				missing[id] = struct{}{}
			}
		} else {
			found := make(map[int64]struct{}, len(views))
			for _, view := range views {
				if view.Channel.ID == 0 {
					continue
				}
				byID[view.Channel.ID] = view.Channel
				found[view.Channel.ID] = struct{}{}
			}
			for _, id := range load {
				if _, ok := found[id]; !ok {
					missing[id] = struct{}{}
				}
			}
		}
	}
	out := make([]domain.Channel, 0, len(unique))
	for _, id := range unique {
		if ch, ok := byID[id]; ok {
			out = append(out, ch)
		}
	}
	return out
}

func (c *viewerPeerCache) viewerChannels(viewerUserID int64) map[int64]domain.Channel {
	if byID, ok := c.channels[viewerUserID]; ok {
		return byID
	}
	byID := make(map[int64]domain.Channel)
	c.channels[viewerUserID] = byID
	return byID
}

func (c *viewerPeerCache) viewerMissingChannels(viewerUserID int64) map[int64]struct{} {
	if missing, ok := c.missingChats[viewerUserID]; ok {
		return missing
	}
	missing := make(map[int64]struct{})
	c.missingChats[viewerUserID] = missing
	return missing
}

func uniquePeerIDs(ids []int64) []int64 {
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

func peerIDMapKeys(ids map[int64]struct{}) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	for id := range ids {
		if id != 0 {
			out = append(out, id)
		}
	}
	return out
}
