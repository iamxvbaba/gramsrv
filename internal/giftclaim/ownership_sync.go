package giftclaim

import (
	"context"
	"math/big"
	"time"

	"go.uber.org/zap"
)

const ownershipLookupTimeout = 12 * time.Second

// RunOwnershipSync follows ownership transfers for NFTs minted by the active
// collection. When a wallet changes, the gift is detached from the old profile,
// hosted by @relayer and left ready for the new owner to claim with TON Proof.
func (s *Service) RunOwnershipSync(ctx context.Context) {
	if s == nil || s.store == nil || s.verifier == nil {
		return
	}
	cursor := int64(0)
	run := func() {
		next, complete, err := s.syncOwnershipPage(ctx, cursor)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("TON gift ownership sync failed", zap.Error(err))
			}
			return
		}
		if complete {
			cursor = 0
		} else {
			cursor = next
		}
	}
	run()
	ticker := time.NewTicker(s.ownershipSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Service) syncOwnershipPage(ctx context.Context, afterID int64) (int64, bool, error) {
	gifts, err := s.store.ListOnChainGifts(ctx, afterID, s.ownershipSyncBatch)
	if err != nil {
		return afterID, false, err
	}
	next := afterID
	for _, gift := range gifts {
		if gift.ID > next {
			next = gift.ID
		}
		lookupCtx, cancel := context.WithTimeout(ctx, ownershipLookupTimeout)
		owner, activeCollection, lookupErr := s.verifier.CurrentNFTOwner(
			lookupCtx, s.collection, big.NewInt(gift.ID), gift.GiftAddress,
		)
		cancel()
		if lookupErr != nil {
			s.logger.Warn("read TON gift owner", zap.Int64("gift_id", gift.ID), zap.String("slug", gift.Slug), zap.Error(lookupErr))
			continue
		}
		// Immutable NFTs of prior collection deployments are deliberately left
		// untouched (including owl-1, owl-7 and owl-8).
		if !activeCollection || owner == gift.OwnerAddress {
			continue
		}
		changed, updateErr := s.store.ReconcileOnChainOwner(ctx, gift.ID, gift.GiftAddress, gift.OwnerAddress, owner)
		if updateErr != nil {
			s.logger.Warn("reconcile TON gift owner", zap.Int64("gift_id", gift.ID), zap.String("slug", gift.Slug), zap.Error(updateErr))
			continue
		}
		if changed {
			s.logger.Info("TON gift returned to @relayer after transfer", zap.Int64("gift_id", gift.ID),
				zap.String("slug", gift.Slug), zap.String("previous_wallet", gift.OwnerAddress), zap.String("wallet", owner))
		}
	}
	return next, len(gifts) < s.ownershipSyncBatch, nil
}
