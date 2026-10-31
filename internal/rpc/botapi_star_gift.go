package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// BotAPIAvailableGifts implements the HTTP Bot API getAvailableGifts method on
// top of the same catalog MTProto payments.getStarGifts serves, so a bot picking
// a gift id over HTTP picks an id the MTProto checkout would accept.
//
// Availability itself is not filtered here: the enabled catalog is returned
// as-is, including sold-out and not-yet-released gifts, because remaining_count
// is the signal a bot needs to tell "sold out" from "gone". The purchase
// transaction behind sendGift is where availability is enforced under a lock.
func (r *Router) BotAPIAvailableGifts(ctx context.Context) ([]domain.StarGift, error) {
	if r == nil || r.deps.Gifts == nil {
		return nil, errors.New("STAR_GIFT_UNAVAILABLE")
	}
	return r.deps.Gifts.Catalog(ctx)
}

// starGiftPurchaseFormTTLSeconds is the fixed form validity period. StarGiftPurchaseForm's
// validation requires ExpiresAt to be exactly IssuedAt+600 (both the PostgreSQL store and the
// in-memory adapter enforce this), so the same constant is used here instead of scattered
// literals of 600.
const starGiftPurchaseFormTTLSeconds = 600

// botAPIStarGiftCommandKey maps the Bot API's Idempotency-Key/request_id to the star-gift
// payment table's command_key. The prefix includes botID, so two bots using the same
// user-provided key do not conflict; the key only accepts the character set the Bot API has
// already validated, and the 256 length cap is validated again in PurchaseStarGift.
func botAPIStarGiftCommandKey(botID int64, requestID string) string {
	return fmt.Sprintf("botapi-stargift:%d:%s", botID, strings.TrimSpace(requestID))
}

// BotAPISendStarGift implements the HTTP Bot API sendGift: a bot buys and sends a star gift with
// its own Stars balance, following exactly the same catalog validation, payment form, star-gift
// ledger and entitlement transaction as MTProto payments.sendStarsForm.
//
// userID and chatID are the official mutually-exclusive recipient selectors; the chat_id encoding
// reuses the single site-wide botAPIPeerFromChatID, avoiding the Bot API layer copying its own
// -100 offset rule.
//
// Idempotency semantics match giftPremiumSubscription: retrying the same request with the same
// request_id returns true and does not charge again; the same key with a different
// recipient/gift/upgrade/text reports IDEMPOTENCY_KEY_INVALID. With no key the handler generates
// a random key, so every call charges again -- consistent with the official "no idempotency"
// behavior, and it will not mistake two different requests for a replay.
func (r *Router) BotAPISendStarGift(
	ctx context.Context,
	botID int64,
	giftID int64,
	userID int64,
	chatID int64,
	payForUpgrade bool,
	message domain.PremiumGiftMessage,
	requestID string,
) (bool, error) {
	if r == nil || r.deps.Gifts == nil || r.deps.Users == nil {
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", nil)
	}
	bot, found, err := r.deps.Users.ByID(ctx, botID, botID)
	if err != nil {
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	}
	if !found || !bot.Bot {
		return false, r.botAPIStarGiftErr(botID, "BOT_INVALID", nil)
	}
	// Exactly one of user_id and chat_id must be given (the Bot API layer has already validated
	// this; this is a second backstop): both empty means no recipient, and both filled would
	// quietly interpret chat_id as the recipient.
	to, valid := botAPIStarGiftPeer(userID, chatID)
	if !valid {
		return false, r.botAPIStarGiftErr(botID, "CHAT_ID_INVALID", nil)
	}
	if err := r.botAPIStarGiftRecipient(ctx, bot.ID, to); err != nil {
		return false, err
	}
	if !(domain.PremiumGiftMessage{Text: message.Text, Entities: message.Entities}).Valid() {
		return false, r.botAPIStarGiftErr(botID, "ENTITY_BOUNDS_INVALID", nil)
	}
	gift, found, err := r.deps.Gifts.GiftByID(ctx, giftID)
	if err != nil {
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	}
	if !found {
		return false, r.botAPIStarGiftErr(botID, "GIFT_NOT_AVAILABLE", nil)
	}
	now := int(r.clock.Now().Unix())
	switch {
	case gift.SoldOut, gift.Limited && gift.AvailabilityRemains <= 0,
		gift.LockedUntilDate > now, gift.Auction:
		// These four cases go through payments.checkCanSendGift's fail branch on the MTProto side;
		// here they are unified into a single error to avoid leaking the distinction between
		// "sold out" and "not yet released" to the bot.
		return false, r.botAPIStarGiftErr(botID, "GIFT_NOT_AVAILABLE", nil)
	case gift.SupportOnly && !r.viewerSupport(ctx, bot.ID):
		return false, r.botAPIStarGiftErr(botID, "GIFT_NOT_AVAILABLE", nil)
	}
	// The bot itself has no Telegram Premium, so RequirePremium gifts are essentially unavailable
	// to a bot. Pass the real membership status here rather than hard-coding true to bypass the
	// same check in prepareStarGiftPurchase: the PremiumGiftMessage path
	// (giftPremiumSubscription) is the official way to grant it to a bot.
	buyerPremium := r.viewerPremium(ctx, bot.ID)
	if gift.RequirePremium && !buyerPremium {
		return false, r.botAPIStarGiftErr(botID, "PREMIUM_ACCOUNT_REQUIRED", nil)
	}
	upgradeStars := int64(0)
	if payForUpgrade {
		if gift.UpgradeStars <= 0 || gift.UpgradeIssued >= gift.UpgradeTotal {
			return false, r.botAPIStarGiftErr(botID, "GIFT_UPGRADE_UNAVAILABLE", nil)
		}
		upgradeStars = gift.UpgradeStars
	}
	purchase := domain.StarGiftPurchaseRequest{
		BuyerUserID:  bot.ID,
		BuyerPremium: buyerPremium,
		// The bot's Stars are the wallet balance it earned settling invoices (bot_stars_balances),
		// not its personal balance: the bot's user identity has no row in stars_balances at all.
		// Without this flag the charge would go through the personal ledger and always return
		// insufficient balance, making the wallet income look nonexistent.
		BuyerIsBot:      true,
		To:              to,
		GiftID:          gift.ID,
		RevisionID:      gift.RevisionID,
		IncludeUpgrade:  payForUpgrade,
		Message:         message.Text,
		MessageEntities: message.Entities,
		ChargeStars:     gift.Stars + upgradeStars,
		CommandKey:      botAPIStarGiftCommandKey(bot.ID, requestID),
		Date:            now,
		// OriginAuthKeyID makes the ledger record this money as a Bot API origin rather than some
		// MTProto session: the bot's Stars spending should not be counted into any user's session
		// statistics.
		// OriginAuthKeyID/OriginSessionID are not set here: these two fields are MTProto session
		// identity, used to exclude the exact originating session from dispatch (to avoid echo).
		// Bot API has no MTProto session; filling only the auth key while leaving session 0 forms a
		// "half pair", and enqueueDispatch rejects the whole transaction with
		// errInvalidDispatchOutboxExclusionPair -- so sendGift would be a constant 500. The bot's
		// message delivery goes through the Bot API queue and does not need to exclude any MTProto
		// session anyway. Bot API-origin accounting is handled by BuyerIsBot (wallet routing) and
		// has nothing to do with these two fields.
	}
	if to.Type == domain.PeerTypeUser {
		purchase.RecipientBlocked, err = r.peerBlocksUser(ctx, bot.ID, to.ID)
		if err != nil {
			return false, err
		}
		purchase.RecipientUnsaved, err = r.starGiftRecipientUnsaved(ctx, bot.ID, to)
		if err != nil {
			return false, err
		}
	}
	// The replay check must happen before issuing the form: every Bot API retry gets a new
	// form_id, while the replay comparison inside PurchaseStarGift requires the form_id to match,
	// so if the check came after issuance a retry would be treated as an amount/command mismatch
	// and error out.
	if settled, found, err := r.deps.Gifts.SettledStarGiftPurchase(ctx, purchase); err != nil {
		if errors.Is(err, domain.ErrStarGiftIdempotencyConflict) {
			return false, r.botAPIStarGiftErr(botID, "IDEMPOTENCY_KEY_INVALID", nil)
		}
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	} else if found {
		if settled.Duplicate {
			r.invalidateStarGiftOwner(to)
		}
		return true, nil
	}
	form, err := r.deps.Gifts.IssuePurchaseForm(ctx, domain.StarGiftPurchaseForm{
		BuyerUserID:     bot.ID,
		To:              to,
		GiftID:          gift.ID,
		RevisionID:      gift.RevisionID,
		IncludeUpgrade:  payForUpgrade,
		Message:         message.Text,
		MessageEntities: message.Entities,
		ChargeStars:     purchase.ChargeStars,
		IssuedAt:        now,
		ExpiresAt:       now + starGiftPurchaseFormTTLSeconds,
	})
	if err != nil {
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	}
	purchase.FormID = form.FormID
	if _, err := r.deps.Gifts.Purchase(ctx, purchase); err != nil {
		if errors.Is(err, domain.ErrStarGiftIdempotencyConflict) {
			return false, r.botAPIStarGiftErr(botID, "IDEMPOTENCY_KEY_INVALID", nil)
		}
		// On a concurrent same-key retry, only one of the two transactions gets the command_key;
		// the loser first hits a form_id comparison failure. At that point the payment has actually
		// succeeded, so re-read the command table and report the completed send as success rather
		// than letting the bot think it wasted its money.
		if _, found, replayErr := r.deps.Gifts.SettledStarGiftPurchase(ctx, purchase); replayErr == nil && found {
			r.invalidateStarGiftOwner(to)
			return true, nil
		}
		return false, r.botAPIStarGiftPurchaseErr(bot.ID, err)
	}
	r.invalidateStarGiftOwner(to)
	return true, nil
}

// botAPIStarGiftPeer resolves the official mutually-exclusive user_id/chat_id pair into a recipient peer.
func botAPIStarGiftPeer(userID, chatID int64) (domain.Peer, bool) {
	if userID != 0 {
		return domain.Peer{Type: domain.PeerTypeUser, ID: userID}, true
	}
	if chatID == 0 {
		return domain.Peer{}, false
	}
	return botAPIPeerFromChatID(chatID)
}

// botAPIStarGiftRecipient validates the gift recipient. Gifting a user must exclude the bot
// itself, other bots, frozen (Deleted) accounts and system accounts; gifting a channel must
// explicitly verify the bot really has access to that channel.
func (r *Router) botAPIStarGiftRecipient(ctx context.Context, botID int64, to domain.Peer) error {
	switch to.Type {
	case domain.PeerTypeUser:
		if to.ID <= 0 || to.ID == botID {
			return r.botAPIStarGiftErr(botID, "USER_ID_INVALID", nil)
		}
		recipient, found, err := r.deps.Users.ByID(ctx, botID, to.ID)
		if err != nil {
			return r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
		}
		if !found || recipient.ID <= 0 || recipient.Bot || recipient.Deleted || domain.IsSystemUserID(recipient.ID) {
			return r.botAPIStarGiftErr(botID, "USER_ID_INVALID", nil)
		}
		return nil
	case domain.PeerTypeChannel:
		if to.ID <= 0 || r.deps.Channels == nil {
			return r.botAPIStarGiftErr(botID, "CHAT_ID_INVALID", nil)
		}
		// Channel gifts go through appendStarGiftAdminLogTx + notification and do not pass the
		// normal send-message permission check, so the bot's access to the channel must be verified
		// here; otherwise any bot that knows a channel_id could spam gifts into an unfamiliar
		// channel and drain its own Stars.
		if _, err := r.deps.Channels.ResolveChannel(ctx, botID, to.ID); err != nil {
			return r.botAPIStarGiftErr(botID, "CHAT_ID_INVALID", err)
		}
		return nil
	default:
		return r.botAPIStarGiftErr(botID, "CHAT_ID_INVALID", nil)
	}
}

// botAPIStarGiftPurchaseErr translates a transaction-layer rejection into a Bot API error code.
func (r *Router) botAPIStarGiftPurchaseErr(botID int64, err error) error {
	switch {
	case errors.Is(err, domain.ErrStarsInsufficient):
		return r.botAPIStarGiftErr(botID, "BALANCE_TOO_LOW", nil)
	case errors.Is(err, domain.ErrPremiumRequired):
		return r.botAPIStarGiftErr(botID, "PREMIUM_ACCOUNT_REQUIRED", nil)
	case errors.Is(err, domain.ErrStarGiftCollectibleUnavailable),
		errors.Is(err, domain.ErrStarGiftCollectibleSoldOut):
		return r.botAPIStarGiftErr(botID, "GIFT_UPGRADE_UNAVAILABLE", nil)
	case errors.Is(err, domain.ErrStarGiftRecipientUnavailable):
		// A frozen account cannot receive any gift. The official API has no matching error code, so a restriction-class error is the closest fit.
		return r.botAPIStarGiftErr(botID, "USER_PRIVACY_RESTRICTED", nil)
	case errors.Is(err, domain.ErrStarGiftInvalid),
		errors.Is(err, domain.ErrStarGiftUnavailable),
		errors.Is(err, domain.ErrStarGiftFormExpired),
		errors.Is(err, domain.ErrStarGiftFormPurposeInvalid),
		errors.Is(err, domain.ErrStarGiftFormAmountMismatch):
		return r.botAPIStarGiftErr(botID, "GIFT_NOT_AVAILABLE", nil)
	case errors.Is(err, domain.ErrChannelInvalid),
		errors.Is(err, domain.ErrChannelPrivate),
		errors.Is(err, domain.ErrChannelUserBanned),
		errors.Is(err, domain.ErrChannelWriteForbidden),
		errors.Is(err, domain.ErrChannelAdminRequired):
		return r.botAPIStarGiftErr(botID, botAPIChannelSendErr(err).Error(), nil)
	default:
		return r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	}
}

// botAPIStarGiftErr assembles an error that apiErrorDescription turns back into a Bot API error
// code. When cause is non-nil it is also logged: the Bot API only returns the code to the bot, so
// the internal reason would be lost here.
func (r *Router) botAPIStarGiftErr(botID int64, code string, cause error) error {
	if cause != nil && r != nil && r.log != nil {
		r.log.Warn("bot api star gift request failed", zap.Int64("bot_user_id", botID),
			zap.String("code", code), zap.Error(cause))
	}
	return errors.New(code)
}
