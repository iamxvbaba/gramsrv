package stargifts

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

// collectibleScheduleStore is implemented by stores that support deferred
// collectible pools (PostgreSQL). Other stores publish every pool at once.
type collectibleScheduleStore interface {
	PendingCollectibleRevision(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, bool, error)
	PendingCollectibleAvailability(ctx context.Context, giftIDs []int64) (map[int64]domain.StarGiftCollectibleAvailability, error)
	ActivateDueCollectibleRevisions(ctx context.Context) ([]int64, error)
	ReleaseCollectiblePoolNow(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, error)
}

func (s *Service) scheduleStore() (collectibleScheduleStore, bool) {
	if s == nil || s.store == nil {
		return nil, false
	}
	st, ok := s.store.(collectibleScheduleStore)
	return st, ok
}

// PendingCollectible returns the gift's scheduled pool that is not live yet.
func (s *Service) PendingCollectible(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, bool, error) {
	st, ok := s.scheduleStore()
	if !ok || giftID <= 0 {
		return domain.StarGiftCollectibleRevision{}, false, nil
	}
	return st.PendingCollectibleRevision(ctx, giftID)
}

// PendingCollectibleAvailability reports scheduled-but-not-live pools by gift.
func (s *Service) PendingCollectibleAvailability(ctx context.Context, giftIDs []int64) (map[int64]domain.StarGiftCollectibleAvailability, error) {
	st, ok := s.scheduleStore()
	if !ok || len(giftIDs) == 0 {
		return map[int64]domain.StarGiftCollectibleAvailability{}, nil
	}
	return st.PendingCollectibleAvailability(ctx, giftIDs)
}

// ReleaseCollectiblePoolNow opens the gift's scheduled pool immediately.
func (s *Service) ReleaseCollectiblePoolNow(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, error) {
	st, ok := s.scheduleStore()
	if !ok {
		return domain.StarGiftCollectibleRevision{}, fmt.Errorf("star gift store does not support scheduled collectible pools")
	}
	if giftID <= 0 {
		return domain.StarGiftCollectibleRevision{}, domain.ErrStarGiftCollectibleInvalid
	}
	revision, err := st.ReleaseCollectiblePoolNow(ctx, giftID)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return revision, err
}

// ActivateDueCollectibleRevisions opens every scheduled pool whose time has
// come. It is driven by the collectible drop dispatcher.
func (s *Service) ActivateDueCollectibleRevisions(ctx context.Context) ([]int64, error) {
	st, ok := s.scheduleStore()
	if !ok {
		return nil, nil
	}
	giftIDs, err := st.ActivateDueCollectibleRevisions(ctx)
	if err == nil && len(giftIDs) > 0 {
		s.InvalidateStarGiftCatalog()
	}
	return giftIDs, err
}
