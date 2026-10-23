package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Paid private messages, https://core.telegram.org/api/paid-messages.

func (r *Router) registerPaidMessages(d *tlprofile.Dispatcher) {
	registerRPC[*tg.AccountGetPaidMessagesRevenueRequest](d, tlprofile.SemanticMethodAccountGetPaidMessagesRevenue, func(ctx context.Context, req *tg.AccountGetPaidMessagesRevenueRequest) (any, error) {
		return r.onAccountGetPaidMessagesRevenue(ctx, req)
	})
	registerRPC[*tg.AccountToggleNoPaidMessagesExceptionRequest](d, tlprofile.SemanticMethodAccountToggleNoPaidMessagesException, func(ctx context.Context, req *tg.AccountToggleNoPaidMessagesExceptionRequest) (any, error) {
		return r.onAccountToggleNoPaidMessagesException(ctx, req)
	})
}

// afterPrivatePaidMessages adds the sender's new Stars balance to the send
// result and tells the recipient about the Stars they earned.
func (r *Router) afterPrivatePaidMessages(ctx context.Context, senderUserID, recipientUserID, senderBalance int64, updates *tg.Updates) *tg.Updates {
	if updates != nil {
		updates.Updates = append(updates.Updates, &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: senderBalance}})
	}
	r.pushStarsBalance(ctx, recipientUserID)
	return updates
}

func (r *Router) pushStarsBalance(ctx context.Context, userID int64) {
	if r.deps.Stars == nil || userID == 0 {
		return
	}
	balance, err := r.deps.Stars.GetBalance(ctx, userID)
	if err != nil {
		r.log.Warn("paid messages: load stars balance", zap.Int64("user_id", userID), zap.Error(err))
		return
	}
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance.Balance}}},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
	})
}

func (r *Router) paidMessagesTargetUser(ctx context.Context, currentUserID int64, input tg.InputUserClass) (int64, error) {
	ids, err := r.userIDsFromInputUsers(ctx, currentUserID, []tg.InputUserClass{input})
	if err != nil {
		return 0, err
	}
	if len(ids) != 1 || ids[0] == currentUserID {
		return 0, userIDInvalidErr()
	}
	return ids[0], nil
}

func (r *Router) onAccountGetPaidMessagesRevenue(ctx context.Context, req *tg.AccountGetPaidMessagesRevenueRequest) (*tg.AccountPaidMessagesRevenue, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return nil, internalErr()
	}
	if _, ok := req.GetParentPeer(); ok {
		// Monoforum revenue is kept on the channel; nothing is owed here.
		return &tg.AccountPaidMessagesRevenue{}, nil
	}
	payer, err := r.paidMessagesTargetUser(ctx, userID, req.UserID)
	if err != nil {
		return nil, err
	}
	if r.deps.PaidMessages == nil {
		return &tg.AccountPaidMessagesRevenue{}, nil
	}
	total, err := r.deps.PaidMessages.PaidMessagesRevenue(ctx, userID, payer)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AccountPaidMessagesRevenue{StarsAmount: total}, nil
}

// onAccountToggleNoPaidMessagesException lets the recipient stop (or, with
// require_payment, resume) charging one user, optionally refunding what that
// user already paid.
func (r *Router) onAccountToggleNoPaidMessagesException(ctx context.Context, req *tg.AccountToggleNoPaidMessagesExceptionRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return false, internalErr()
	}
	if _, ok := req.GetParentPeer(); ok {
		return false, peerIDInvalidErr()
	}
	if r.deps.Privacy == nil {
		return false, internalErr()
	}
	target, err := r.paidMessagesTargetUser(ctx, userID, req.UserID)
	if err != nil {
		return false, err
	}
	var rules domain.PrivacyRules
	var changed bool
	if req.RequirePayment {
		rules, changed, err = r.deps.Privacy.RemoveAllowUser(ctx, userID, domain.PrivacyKeyNoPaidMessages, target)
	} else {
		rules, changed, err = r.deps.Privacy.AddAllowUser(ctx, userID, domain.PrivacyKeyNoPaidMessages, target)
	}
	if err != nil {
		return false, internalErr()
	}
	if changed {
		r.invalidateRPCProjectionForUser(userID)
		r.invalidateRPCProjectionForUser(target)
		r.peerSettingsProjectionCache.DeletePair(target, domain.Peer{Type: domain.PeerTypeUser, ID: userID})
		r.pushUserUpdates(ctx, userID, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdatePrivacy{Key: tgPrivacyKey(rules.Key), Rules: tgPrivacyRules(rules.Rules)}},
			Users:   []tg.UserClass{},
			Chats:   []tg.ChatClass{},
			Date:    int(r.clock.Now().Unix()),
		})
		r.pushPaidMessagesPeerUser(ctx, target, userID)
	}
	if req.RefundCharged && !req.RequirePayment && r.deps.PaidMessages != nil {
		refund, err := r.deps.PaidMessages.RefundPaidMessages(ctx, userID, target, int(r.clock.Now().Unix()))
		if err != nil {
			r.log.Warn("paid messages refund failed", zap.Int64("user_id", userID), zap.Int64("payer", target), zap.Error(err))
			return false, internalErr()
		}
		if refund.Count > 0 {
			r.pushStarsBalance(ctx, userID)
			r.pushStarsBalance(ctx, target)
			r.sendPaidMessagesRefundedMessage(ctx, userID, target, refund)
		}
	}
	return true, nil
}

// pushPaidMessagesPeerUser refreshes the recipient as seen by viewer, so the
// viewer's client drops (or shows again) the price in the chat.
func (r *Router) pushPaidMessagesPeerUser(ctx context.Context, viewerUserID, recipientUserID int64) {
	if r.deps.Users == nil {
		return
	}
	recipient, found, err := r.deps.Users.ByID(ctx, viewerUserID, recipientUserID)
	if err != nil || !found {
		return
	}
	user := r.tgUser(recipient)
	restriction, err := r.privateContactRestrictionFor(ctx, viewerUserID, recipientUserID)
	if err != nil {
		return
	}
	applyPrivateContactRestrictionToUser(user, restriction)
	r.applyPeerReadModels(ctx, viewerUserID, []tg.UserClass{user}, nil)
	r.pushUserUpdates(ctx, viewerUserID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: recipientUserID}},
		Users:   []tg.UserClass{user},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
	})
}

// sendPaidMessagesRefundedMessage posts messageActionPaidMessagesRefunded
// from the refunding user into the chat.
func (r *Router) sendPaidMessagesRefundedMessage(ctx context.Context, userID, payerUserID int64, refund domain.PaidMessagesRefund) {
	if r.deps.Messages == nil {
		return
	}
	randomID, err := newServiceRandomID()
	if err != nil {
		return
	}
	_, err = r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:    userID,
		RecipientUserID: payerUserID,
		RandomID:        randomID,
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind:                 domain.MessageServiceActionPaidMessagesRefunded,
				PaidMessagesRefunded: &domain.MessagePaidMessagesRefundedAction{Count: refund.Count, Stars: refund.Stars},
			},
		},
		Date: int(r.clock.Now().Unix()),
	})
	if err != nil {
		r.log.Warn("paid messages refund notice failed", zap.Int64("user_id", userID), zap.Error(err))
	}
}

// paidChannelMessageErr maps the supergroup paid-message failures:
// ALLOW_PAYMENT_REQUIRED_<price> or BALANCE_TOO_LOW. Nil for anything else.
func paidChannelMessageErr(err error) error {
	var required *domain.StarsPaymentRequiredError
	switch {
	case errors.As(err, &required) && required.Stars > 0:
		return allowPaymentRequiredErr(required.Stars)
	case errors.Is(err, domain.ErrStarsInsufficient):
		return balanceTooLowErr()
	}
	return nil
}

// supergroupPaidMessagePrice is what userID pays per message in the channel:
// the price of a supergroup that charges for messages, 0 for its admins and
// for every other kind of chat.
func (r *Router) supergroupPaidMessagePrice(ctx context.Context, userID, channelID int64) (int64, error) {
	if r.deps.Channels == nil {
		return 0, nil
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, channelID)
	if err != nil {
		// Access problems are reported by the send itself.
		return 0, nil
	}
	ch := view.Channel
	if ch.SendPaidMessagesStars <= 0 || !ch.Megagroup || ch.Monoforum ||
		view.Self.Role == domain.ChannelRoleCreator || view.Self.Role == domain.ChannelRoleAdmin {
		return 0, nil
	}
	return ch.SendPaidMessagesStars, nil
}

// ensureSupergroupPaidAllowed checks that allow_paid_stars covers count
// messages and returns the per-message price to authorize on each send.
func (r *Router) ensureSupergroupPaidAllowed(ctx context.Context, userID, channelID, allowPaidStars int64, count int) (int64, error) {
	price, err := r.supergroupPaidMessagePrice(ctx, userID, channelID)
	if err != nil || price <= 0 {
		return 0, err
	}
	if count < 1 {
		count = 1
	}
	if required := price * int64(count); allowPaidStars < required {
		return 0, allowPaymentRequiredErr(required)
	}
	return price, nil
}
