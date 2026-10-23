package stargifts

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

// quickSettingsStore is implemented by stores that support the admin's
// per-gift quick settings (PostgreSQL).
type quickSettingsStore interface {
	SetCatalogSupply(ctx context.Context, giftID int64, limited bool, total, issued int) (bool, error)
	SetDropProtection(ctx context.Context, giftID int64, limitedPerUser bool, perUserTotal, rateLimitCount, rateLimitWindowSeconds int) (bool, error)
	PreviewStarGiftDelete(ctx context.Context, giftID int64) (domain.StarGiftDeleteResult, error)
	DeleteStarGift(ctx context.Context, giftID int64) (domain.StarGiftDeleteResult, error)
}

func (s *Service) quickSettings() (quickSettingsStore, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("star gift store is not configured")
	}
	st, ok := s.store.(quickSettingsStore)
	if !ok {
		return nil, fmt.Errorf("star gift store does not support quick settings")
	}
	return st, nil
}

// SetCatalogSupply edits the plain gift's sale cap in place.
func (s *Service) SetCatalogSupply(ctx context.Context, giftID int64, limited bool, total, issued int) (bool, error) {
	st, err := s.quickSettings()
	if err != nil {
		return false, err
	}
	changed, err := st.SetCatalogSupply(ctx, giftID, limited, total, issued)
	if err == nil && changed {
		s.InvalidateStarGiftCatalog()
	}
	return changed, err
}

// SetDropProtection edits the per-buyer cap and purchase rate limit.
func (s *Service) SetDropProtection(ctx context.Context, giftID int64, limitedPerUser bool, perUserTotal, rateLimitCount, rateLimitWindowSeconds int) (bool, error) {
	st, err := s.quickSettings()
	if err != nil {
		return false, err
	}
	changed, err := st.SetDropProtection(ctx, giftID, limitedPerUser, perUserTotal, rateLimitCount, rateLimitWindowSeconds)
	if err == nil && changed {
		s.InvalidateStarGiftCatalog()
	}
	return changed, err
}

// PreviewDeleteStarGift reports what DeleteStarGift would do.
func (s *Service) PreviewDeleteStarGift(ctx context.Context, giftID int64) (domain.StarGiftDeleteResult, error) {
	st, err := s.quickSettings()
	if err != nil {
		return domain.StarGiftDeleteResult{}, err
	}
	return st.PreviewStarGiftDelete(ctx, giftID)
}

// DeleteStarGift retires a gift and its ordinary saved copies. It does not
// pay refunds; the result lists them for the caller.
func (s *Service) DeleteStarGift(ctx context.Context, giftID int64) (domain.StarGiftDeleteResult, error) {
	st, err := s.quickSettings()
	if err != nil {
		return domain.StarGiftDeleteResult{}, err
	}
	result, err := st.DeleteStarGift(ctx, giftID)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return result, err
}
