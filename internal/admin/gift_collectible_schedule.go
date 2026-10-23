package admin

import (
	"context"
	"fmt"
	"strconv"

	"telesrv/internal/domain"
)

const ActionReleaseCollectiblePool = "gifts.release_collectible_pool"

// collectibleScheduler is the part of the gifts service that manages
// scheduled collectible pools.
type collectibleScheduler interface {
	PendingCollectible(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, bool, error)
	ReleaseCollectiblePoolNow(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, error)
}

// ReleaseCollectiblePoolRequest opens a gift's scheduled pool immediately.
type ReleaseCollectiblePoolRequest struct {
	CommandMeta
	GiftID int64 `json:"gift_id"`
}

func (s *Service) collectibleScheduler() (collectibleScheduler, error) {
	if s == nil || s.gifts == nil {
		return nil, fmt.Errorf("gifts dependency is not configured")
	}
	scheduler, ok := s.gifts.(collectibleScheduler)
	if !ok {
		return nil, fmt.Errorf("gifts dependency does not support scheduled collectible pools")
	}
	return scheduler, nil
}

// PendingGiftCollectible returns the gift's scheduled pool that is not live yet.
func (s *Service) PendingGiftCollectible(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, bool, error) {
	scheduler, err := s.collectibleScheduler()
	if err != nil {
		return domain.StarGiftCollectibleRevision{}, false, err
	}
	return scheduler.PendingCollectible(ctx, giftID)
}

func (s *Service) ReleaseCollectiblePool(ctx context.Context, req ReleaseCollectiblePoolRequest) (CommandResult, error) {
	scheduler, err := s.collectibleScheduler()
	if err != nil {
		return CommandResult{}, err
	}
	if req.GiftID <= 0 {
		return CommandResult{}, domain.ErrStarGiftCollectibleInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionReleaseCollectiblePool, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"gift_id": strconv.FormatInt(req.GiftID, 10)}
		if req.DryRun {
			if _, found, err := scheduler.PendingCollectible(ctx, req.GiftID); err != nil {
				return CommandResult{Details: details}, err
			} else if !found {
				return CommandResult{Details: details}, domain.ErrStarGiftCollectibleUnavailable
			}
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		revision, err := scheduler.ReleaseCollectiblePoolNow(ctx, req.GiftID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["revision_id"] = strconv.FormatInt(revision.ID, 10)
		details["revision"] = revision.Revision
		details["supply_total"] = revision.SupplyTotal
		return CommandResult{Message: "collectible pool released", Details: details}, nil
	})
}
