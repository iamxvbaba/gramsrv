package admin

import (
	"context"
	"fmt"
	"strconv"

	"telesrv/internal/domain"
)

const (
	ActionSetStarGiftSupply         = "gifts.set_supply"
	ActionSetStarGiftDropProtection = "gifts.set_drop_protection"
	ActionDeleteStarGift            = "gifts.delete"
)

// giftQuickSettings is the part of the gifts service behind the per-gift
// quick settings in the admin panel.
type giftQuickSettings interface {
	SetCatalogSupply(ctx context.Context, giftID int64, limited bool, total, issued int) (bool, error)
	SetDropProtection(ctx context.Context, giftID int64, limitedPerUser bool, perUserTotal, rateLimitCount, rateLimitWindowSeconds int) (bool, error)
	PreviewDeleteStarGift(ctx context.Context, giftID int64) (domain.StarGiftDeleteResult, error)
	DeleteStarGift(ctx context.Context, giftID int64) (domain.StarGiftDeleteResult, error)
}

func (s *Service) giftQuickSettings() (giftQuickSettings, error) {
	if s == nil || s.gifts == nil {
		return nil, fmt.Errorf("gifts dependency is not configured")
	}
	quick, ok := s.gifts.(giftQuickSettings)
	if !ok {
		return nil, fmt.Errorf("gifts dependency does not support quick settings")
	}
	return quick, nil
}

// SetStarGiftSupplyRequest edits a gift's own "X of Y sold" cap in place.
// Limited=false removes the cap; Limited=true needs Total > 0 and
// 0 <= Issued <= Total.
type SetStarGiftSupplyRequest struct {
	CommandMeta
	GiftID             int64 `json:"gift_id"`
	Limited            bool  `json:"limited"`
	AvailabilityTotal  int   `json:"availability_total,omitempty"`
	AvailabilityIssued int   `json:"availability_issued,omitempty"`
}

func (s *Service) SetStarGiftSupply(ctx context.Context, req SetStarGiftSupplyRequest) (CommandResult, error) {
	quick, err := s.giftQuickSettings()
	if err != nil {
		return CommandResult{}, err
	}
	if req.GiftID <= 0 || (req.Limited && (req.AvailabilityTotal <= 0 || req.AvailabilityIssued < 0 || req.AvailabilityIssued > req.AvailabilityTotal)) {
		return CommandResult{}, domain.ErrStarGiftInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetStarGiftSupply, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"gift_id": strconv.FormatInt(req.GiftID, 10), "limited": req.Limited,
			"availability_total": req.AvailabilityTotal, "availability_issued": req.AvailabilityIssued,
		}
		if req.DryRun {
			return CommandResult{Message: "star gift supply change validated", Details: details}, nil
		}
		changed, err := quick.SetCatalogSupply(ctx, req.GiftID, req.Limited, req.AvailabilityTotal, req.AvailabilityIssued)
		details["changed"] = changed
		return CommandResult{Message: "star gift supply updated", Details: details}, err
	})
}

// SetStarGiftDropProtectionRequest edits a gift's anti-scalping limits: a
// lifetime cap per buyer and/or at most RateLimitCount purchases per buyer
// within any RateLimitWindowSeconds window. Both default to off.
type SetStarGiftDropProtectionRequest struct {
	CommandMeta
	GiftID                 int64 `json:"gift_id"`
	LimitedPerUser         bool  `json:"limited_per_user"`
	PerUserTotal           int   `json:"per_user_total,omitempty"`
	RateLimitCount         int   `json:"rate_limit_count,omitempty"`
	RateLimitWindowSeconds int   `json:"rate_limit_window_seconds,omitempty"`
}

func (s *Service) SetStarGiftDropProtection(ctx context.Context, req SetStarGiftDropProtectionRequest) (CommandResult, error) {
	quick, err := s.giftQuickSettings()
	if err != nil {
		return CommandResult{}, err
	}
	if req.GiftID <= 0 || (req.LimitedPerUser && req.PerUserTotal <= 0) ||
		req.RateLimitCount < 0 || req.RateLimitWindowSeconds < 0 || (req.RateLimitCount > 0) != (req.RateLimitWindowSeconds > 0) {
		return CommandResult{}, domain.ErrStarGiftInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetStarGiftDropProtection, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"gift_id": strconv.FormatInt(req.GiftID, 10), "limited_per_user": req.LimitedPerUser, "per_user_total": req.PerUserTotal,
			"rate_limit_count": req.RateLimitCount, "rate_limit_window_seconds": req.RateLimitWindowSeconds,
		}
		if req.DryRun {
			return CommandResult{Message: "star gift drop protection change validated", Details: details}, nil
		}
		changed, err := quick.SetDropProtection(ctx, req.GiftID, req.LimitedPerUser, req.PerUserTotal, req.RateLimitCount, req.RateLimitWindowSeconds)
		details["changed"] = changed
		return CommandResult{Message: "star gift drop protection updated", Details: details}, err
	})
}

// DeleteStarGiftRequest retires a gift: it leaves the catalog for good and
// every ordinary saved copy disappears from its owner's profile. With
// RefundStars each user owner is credited the gift's conversion value.
type DeleteStarGiftRequest struct {
	CommandMeta
	GiftID      int64 `json:"gift_id"`
	RefundStars bool  `json:"refund_stars"`
}

// DeleteStarGift refuses to run while the gift has collectible instances,
// an auction or purchase forms in flight. Refunds are credited after the
// deletion commits, so a retry can never pay twice; a refund that fails is
// reported in the result details.
func (s *Service) DeleteStarGift(ctx context.Context, req DeleteStarGiftRequest) (CommandResult, error) {
	quick, err := s.giftQuickSettings()
	if err != nil {
		return CommandResult{}, err
	}
	if req.GiftID <= 0 {
		return CommandResult{}, domain.ErrStarGiftInvalid
	}
	if req.RefundStars && s.stars == nil {
		return CommandResult{}, fmt.Errorf("stars dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeleteStarGift, 0, domain.Peer{}, req, func() (CommandResult, error) {
		preview, err := quick.PreviewDeleteStarGift(ctx, req.GiftID)
		if err != nil {
			return CommandResult{}, err
		}
		details := deleteStarGiftDetails(preview, req.RefundStars)
		if len(preview.Blockers) > 0 {
			return CommandResult{Details: details}, domain.ErrStarGiftDeleteBlocked
		}
		if req.DryRun {
			return CommandResult{Message: "star gift deletion validated", Details: details}, nil
		}
		result, err := quick.DeleteStarGift(ctx, req.GiftID)
		if err != nil {
			if len(result.Blockers) > 0 {
				details["blockers"] = result.Blockers
			}
			return CommandResult{Details: details}, err
		}
		details = deleteStarGiftDetails(result, req.RefundStars)
		if req.RefundStars {
			refunded := 0
			var failed []string
			for _, refund := range result.Refunds {
				balance, err := s.stars.Credit(ctx, refund.UserID, refund.Stars, domain.StarsReasonAdjust, domain.Peer{},
					"Gift removed", "Refund for "+result.Title)
				if err != nil {
					failed = append(failed, strconv.FormatInt(refund.UserID, 10))
					continue
				}
				refunded++
				_ = s.notifyStarsBalanceChanged(ctx, balance)
			}
			details["refunded_users"] = refunded
			if len(failed) > 0 {
				details["refund_failed_user_ids"] = failed
			}
		}
		return CommandResult{Message: "star gift deleted", Details: details}, nil
	})
}

func deleteStarGiftDetails(result domain.StarGiftDeleteResult, refund bool) map[string]any {
	details := map[string]any{
		"gift_id": strconv.FormatInt(result.GiftID, 10), "title": result.Title, "owners": result.Owners,
		"refund_requested": refund, "refund_eligible_users": len(result.Refunds),
		"refund_total_stars": strconv.FormatInt(result.RefundTotal(), 10),
	}
	if len(result.Blockers) > 0 {
		details["blockers"] = result.Blockers
	}
	return details
}
