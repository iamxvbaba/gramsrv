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

func (r *Router) privatePaidStarsCharge(
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
	charged := requirement.paidStars
	required := charged * int64(messageCount)
	if allowPaidStars < required {
		return 0, allowPaymentRequiredErr(required)
	}
	return charged, nil
}

// ensurePaidStarsBalance rejects a paid album or forward the sender cannot cover
// before its first message commits. Each message is still debited in its own send
// transaction, so a concurrent spend can stop the batch later; the client then
// retries the unsent random_ids and the committed ones replay without a new charge.
func (r *Router) ensurePaidStarsBalance(ctx context.Context, senderUserID, perMessage int64, messageCount int) error {
	if perMessage <= 0 || messageCount < 1 || r.deps.Stars == nil {
		return nil
	}
	if perMessage > math.MaxInt64/int64(messageCount) {
		return starsAmountInvalidErr()
	}
	balance, err := r.deps.Stars.GetBalance(ctx, senderUserID)
	if err != nil {
		return starsErr(err)
	}
	if balance.Balance < perMessage*int64(messageCount) {
		return balanceTooLowErr()
	}
	return nil
}

func (r *Router) ensurePrivateContactAllowed(
	ctx context.Context,
	senderUserID, recipientUserID, allowPaidStars int64,
	messageCount int,
) error {
	_, err := r.privatePaidStarsCharge(ctx, senderUserID, recipientUserID, allowPaidStars, messageCount)
	return err
}
