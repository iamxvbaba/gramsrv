package rpc

import (
	"context"
	"math"

	"github.com/iamxvbaba/td/tg"
)

type privateContactRequirement struct {
	paidStars      int64
	requirePremium bool
}

type privacyContactFreeEvaluator interface {
	CanContactForFreeBatch(ctx context.Context, ownerUserIDs []int64, viewerUserID int64) (map[int64]bool, error)
}

type privacyViewerPremiumEvaluator interface {
	ViewerIsPremium(ctx context.Context, viewerUserID int64) (bool, error)
}

func applyPrivateContactRestrictionToUser(user *tg.User, restriction privateContactRequirement) {
	if user == nil {
		return
	}
	user.SetContactRequirePremium(restriction.requirePremium)
	if restriction.paidStars > 0 {
		user.SetSendPaidMessagesStars(restriction.paidStars)
	} else {
		user.Flags2.Unset(15)
		user.SendPaidMessagesStars = 0
	}
}

func applyPrivateContactRestrictionToUserFull(full *tg.UserFull, restriction privateContactRequirement) {
	if full == nil {
		return
	}
	full.SetContactRequirePremium(restriction.requirePremium)
	if restriction.paidStars > 0 {
		full.SetSendPaidMessagesStars(restriction.paidStars)
	} else {
		full.Flags2.Unset(14)
		full.SendPaidMessagesStars = 0
	}
}

// privateContactRequirementFor returns the recipient's current restriction
// from two in-memory read models:
//   - account settings: base premium/paid requirement;
//   - privacy/contact facts: contacts and PrivacyKeyNoPaidMessages exceptions.
//
// PostgreSQL is only a bounded cache-miss loader. Local writes are
// write-through and cross-instance changes invalidate + prewarm both models.
func (r *Router) privateContactRestrictionFor(
	ctx context.Context,
	senderUserID, recipientUserID int64,
) (privateContactRequirement, error) {
	if r == nil || senderUserID == 0 || recipientUserID == 0 || senderUserID == recipientUserID {
		return privateContactRequirement{}, nil
	}
	if evaluator, ok := r.deps.Privacy.(privacyContactFreeEvaluator); ok {
		free, err := evaluator.CanContactForFreeBatch(ctx, []int64{recipientUserID}, senderUserID)
		if err != nil {
			return privateContactRequirement{}, internalErr()
		}
		if free[recipientUserID] {
			return privateContactRequirement{}, nil
		}
	}
	settings, err := r.cachedAccountSettings(ctx, recipientUserID)
	if err != nil {
		return privateContactRequirement{}, internalErr()
	}
	global := settings.GlobalPrivacy
	if global.NoncontactPeersPaidStars > 0 {
		return privateContactRequirement{paidStars: global.NoncontactPeersPaidStars}, nil
	}
	return privateContactRequirement{requirePremium: global.NewNoncontactPeersRequirePremium}, nil
}

func (r *Router) viewerIsPremiumForPrivacy(ctx context.Context, viewerUserID int64) (bool, error) {
	var err error
	premium := false
	if evaluator, ok := r.deps.Privacy.(privacyViewerPremiumEvaluator); ok {
		premium, err = evaluator.ViewerIsPremium(ctx, viewerUserID)
		if err != nil {
			return false, internalErr()
		}
	} else if r.deps.Users != nil {
		user, found, loadErr := r.deps.Users.ByID(ctx, viewerUserID, viewerUserID)
		if loadErr != nil {
			return false, internalErr()
		}
		premium = found && user.PremiumActiveAt(r.clock.Now().Unix())
	}
	return premium, nil
}

func (r *Router) ensurePrivateContactAllowed(
	ctx context.Context,
	senderUserID, recipientUserID, allowPaidStars int64,
	messageCount int,
) error {
	if allowPaidStars < 0 || messageCount < 1 {
		return starsAmountInvalidErr()
	}
	requirement, err := r.privateContactRestrictionFor(ctx, senderUserID, recipientUserID)
	if err != nil {
		return err
	}
	if requirement.requirePremium {
		premium, err := r.viewerIsPremiumForPrivacy(ctx, senderUserID)
		if err != nil {
			return err
		}
		if !premium {
			return premiumAccountRequiredErr()
		}
		return nil
	}
	if requirement.paidStars <= 0 {
		return nil
	}
	if requirement.paidStars > math.MaxInt64/int64(messageCount) {
		return starsAmountInvalidErr()
	}
	required := requirement.paidStars * int64(messageCount)
	if allowPaidStars < required {
		return allowPaymentRequiredErr(required)
	}
	// The privacy gate and no-paid exception are complete here. The separate
	// private paid-message ledger is not part of the current message store yet;
	// never accept an authorization without an atomic debit.
	return paymentUnsupportedErr()
}
