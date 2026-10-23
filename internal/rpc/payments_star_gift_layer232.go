package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Layer 232 star gift and wallet methods.
//
// payments.deleteStarGift is fully implemented. withdrawStarGift,
// depositStarGift, displayStarGift and wallet.setCardStickers move or display
// TON NFTs held in the user's wallet: they need an on-chain NFT inventory,
// deposit escrow and transfer relay that this deployment does not have (see
// internal/app/wallet/README.md). Like the other unsupported wallet
// capabilities they authenticate, rate-limit and answer WALLET_UNAVAILABLE
// instead of pretending a chain operation happened.

func (r *Router) registerStarGiftLayer232(d *tlprofile.Dispatcher) {
	registerRPC[*tg.PaymentsDeleteStarGiftRequest](d, tlprofile.SemanticMethodPaymentsDeleteStarGift, func(ctx context.Context, req *tg.PaymentsDeleteStarGiftRequest) (any, error) {
		return r.onPaymentsDeleteStarGift(ctx, req)
	})
	registerRPC[*tg.PaymentsWithdrawStarGiftRequest](d, tlprofile.SemanticMethodPaymentsWithdrawStarGift, func(ctx context.Context, _ *tg.PaymentsWithdrawStarGiftRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "gift-withdraw", 20)
	})
	registerRPC[*tg.PaymentsDepositStarGiftRequest](d, tlprofile.SemanticMethodPaymentsDepositStarGift, func(ctx context.Context, _ *tg.PaymentsDepositStarGiftRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "gift-deposit", 20)
	})
	registerRPC[*tg.PaymentsDisplayStarGiftRequest](d, tlprofile.SemanticMethodPaymentsDisplayStarGift, func(ctx context.Context, _ *tg.PaymentsDisplayStarGiftRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "gift-display", 60)
	})
	registerRPC[*tg.WalletSetCardStickersRequest](d, tlprofile.SemanticMethodWalletSetCardStickers, func(ctx context.Context, _ *tg.WalletSetCardStickersRequest) (any, error) {
		return nil, r.walletCapabilityUnsupported(ctx, "card-stickers", 60)
	})
}

type starGiftDeleter interface {
	DeleteSaved(ctx context.Context, ref domain.SavedStarGiftRef) error
}

// onPaymentsDeleteStarGift removes an ordinary gift from its owner's profile.
// Like TDesktop's CanDeleteSavedStarGift, unique, limited and auction gifts
// cannot be deleted; channel gifts need the gift-management admin right.
func (r *Router) onPaymentsDeleteStarGift(ctx context.Context, req *tg.PaymentsDeleteStarGiftRequest) (bool, error) {
	if req == nil {
		return false, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.userIsBot(ctx, userID) {
		return false, botMethodInvalidErr()
	}
	deleter, ok := r.deps.Gifts.(starGiftDeleter)
	if r.deps.Gifts == nil || !ok {
		return false, notImplementedErr()
	}
	ref, ok, err := r.starGiftRefFromInput(ctx, userID, req.Stargift)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, starGiftInvalidErr()
	}
	if err := r.ensureCanManageStarGiftOwner(ctx, userID, ref.Owner); err != nil {
		return false, err
	}
	saved, found, err := r.deps.Gifts.GetSaved(ctx, ref)
	if err != nil {
		return false, internalErr()
	}
	if !found || saved.UniqueGiftID != 0 || saved.Converted || !saved.LifecycleStatus.Live() {
		return false, starGiftInvalidErr()
	}
	gift, found, err := r.deps.Gifts.GiftRevisionByID(ctx, saved.RevisionID)
	if err != nil {
		return false, internalErr()
	}
	if !found {
		gift, found, err = r.deps.Gifts.GiftByID(ctx, saved.GiftID)
		if err != nil {
			return false, internalErr()
		}
	}
	if !found || gift.Limited || gift.Auction {
		return false, starGiftInvalidErr()
	}
	if err := deleter.DeleteSaved(ctx, ref); err != nil {
		if errors.Is(err, domain.ErrStarGiftNotFound) || errors.Is(err, domain.ErrStarGiftUnavailable) {
			return false, starGiftInvalidErr()
		}
		r.log.Warn("delete star gift failed", zap.Int64("user_id", userID), zap.Error(err))
		return false, internalErr()
	}
	r.invalidateStarGiftOwnerProjection(ref.Owner)
	r.invalidateStarGiftOwner(ref.Owner)
	return true, nil
}
