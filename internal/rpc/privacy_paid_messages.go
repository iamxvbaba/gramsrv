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
		// Whoever the recipient wrote to first may answer for free.
		if r.deps.PaidMessages != nil {
			first, err := r.deps.PaidMessages.RecipientMessagedFirst(ctx, recipientUserID, senderUserID)
			if err != nil {
				return privateContactRequirement{}, internalErr()
			}
			if first {
				return privateContactRequirement{}, nil
			}
		}
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

// ensurePrivateContactAllowed checks that the sender may write to the
// recipient and returns the price of one message (0 when free). For paid
// chats allow_paid_stars must cover messageCount messages; only the actual
// price is charged, by the private-send transaction.
func (r *Router) ensurePrivateContactAllowed(
	ctx context.Context,
	senderUserID, recipientUserID, allowPaidStars int64,
	messageCount int,
) (int64, error) {
	if allowPaidStars < 0 || messageCount < 1 {
		return 0, starsAmountInvalidErr()
	}
	requirement, err := r.privateContactRestrictionFor(ctx, senderUserID, recipientUserID)
	if err != nil {
		return 0, err
	}
	if requirement.requirePremium {
		premium, err := r.viewerIsPremiumForPrivacy(ctx, senderUserID)
		if err != nil {
			return 0, err
		}
		if !premium {
			return 0, premiumAccountRequiredErr()
		}
		return 0, nil
	}
	if requirement.paidStars <= 0 {
		return 0, nil
	}
	if requirement.paidStars > math.MaxInt64/int64(messageCount) {
		return 0, starsAmountInvalidErr()
	}
	required := requirement.paidStars * int64(messageCount)
	if allowPaidStars < required {
		return 0, allowPaymentRequiredErr(required)
	}
	return requirement.paidStars, nil
}
