package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func (r *Router) onPaymentsGetPremiumGiftCodeOptions(
	ctx context.Context,
	req *tg.PaymentsGetPremiumGiftCodeOptionsRequest,
) ([]tg.PremiumGiftCodeOption, error) {
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !authorized || userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	if r.userIsBot(ctx, userID) {
		return nil, botMethodInvalidErr()
	}
	if r.deps.Premium == nil {
		return []tg.PremiumGiftCodeOption{}, nil
	}
	if req != nil {
		if peer, ok := req.GetBoostPeer(); ok {
			if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer); err != nil {
				return nil, err
			}
		}
	}
	plans, err := r.deps.Premium.Plans(ctx)
	if err != nil {
		return nil, internalErr()
	}
	out := make([]tg.PremiumGiftCodeOption, 0, len(plans)*2)
	for _, plan := range plans {
		if !plan.Valid() || !plan.Enabled {
			continue
		}
		fiat := tg.PremiumGiftCodeOption{
			Users: 1, Months: plan.Months,
			Currency: plan.EffectiveFiatCurrency(), Amount: plan.EffectiveFiatAmount(),
		}
		if plan.StoreProduct != "" {
			fiat.SetStoreProduct(plan.StoreProduct)
			fiat.SetStoreQuantity(plan.StoreQuantity)
		}
		out = append(out, fiat, tg.PremiumGiftCodeOption{
			Users:    1,
			Months:   plan.Months,
			Currency: domain.PremiumCurrencyStars,
			Amount:   plan.AmountStars,
		})
	}
	return out, nil
}

func (r *Router) premiumPaymentForm(
	ctx context.Context,
	userID int64,
	input tg.InputInvoiceClass,
) (tg.PaymentsPaymentFormClass, error) {
	if r.deps.Premium == nil {
		return nil, notImplementedErr()
	}
	invoice, err := r.resolvePremiumInvoice(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	// The fiat Premium form is the local dev checkout; denied by default so
	// production never grants Premium without a real XTR payment. Reject before
	// IssuePaymentForm — a denied checkout must not leave a durable payment
	// intent behind, even for a valid invoice.
	debitStars := invoice.EffectiveDebitStars()
	if !debitStars {
		if err := r.devPaymentsErr(); err != nil {
			return nil, err
		}
	}
	recipientID := invoice.RecipientUserID
	if invoice.Kind == domain.PremiumPurchaseSelf {
		recipientID = userID
	}
	now := int(r.clock.Now().Unix())
	form, err := r.deps.Premium.IssuePaymentForm(ctx, domain.PremiumPaymentForm{
		IdempotencyKey: premiumPaymentFormIdempotencyKey(userID, r.deps.Premium.BotUserID(), input),
		BuyerUserID:    userID, Kind: invoice.Kind, RecipientUserID: recipientID,
		Months: invoice.Months, DurationDays: invoice.DurationDays,
		AmountStars: invoice.AmountStars, PlanVersion: invoice.PlanVersion,
		PaymentCurrency: invoice.EffectivePaymentCurrency(),
		PaymentAmount:   invoice.EffectivePaymentAmount(), DebitStars: debitStars,
		Message: invoice.Message, IssuedAt: now,
		ExpiresAt: now + domain.PremiumPaymentFormTTLSeconds,
	})
	if err != nil {
		return nil, premiumPaymentErr(err)
	}
	botID := r.deps.Premium.BotUserID()
	userIDs := []int64{botID}
	if invoice.Kind == domain.PremiumPurchaseGift {
		userIDs = append(userIDs, recipientID)
	}
	users := r.domainUsersForIDs(ctx, userID, userIDs)
	if botID == domain.PremiumBotConfiguredUserID() {
		hasBot := false
		for _, user := range users {
			hasBot = hasBot || user.ID == botID
		}
		if !hasBot {
			users = append(users, domain.PremiumBotUser())
		}
	}
	wireInvoice := tg.Invoice{
		Currency: invoice.EffectivePaymentCurrency(),
		Prices: []tg.LabeledPrice{{
			Label:  invoice.Title,
			Amount: invoice.EffectivePaymentAmount(),
		}},
	}
	if invoice.Kind == domain.PremiumPurchaseSelf {
		wireInvoice.SetSubscriptionPeriod(invoice.DurationDays * 24 * 60 * 60)
	}
	if debitStars {
		return &tg.PaymentsPaymentFormStars{
			FormID: form.ID, BotID: botID, Title: invoice.Title,
			Description: invoice.Description, Invoice: wireInvoice,
			Users: tgUsersForViewer(userID, users),
		}, nil
	}
	users = append(users, domain.OfficialSystemUser())
	wireInvoice.Test = true
	return &tg.PaymentsPaymentForm{
		FormID: form.ID, BotID: botID, Title: invoice.Title,
		Description: invoice.Description, Invoice: wireInvoice,
		ProviderID: domain.OfficialSystemUserID,
		URL: r.publicLinkQuery("payments/dev-stars", url.Values{
			"form_id": []string{strconv.FormatInt(form.ID, 10)},
		}),
		Users: tgUsersForViewer(userID, users),
	}, nil
}

func premiumPaymentFormIdempotencyKey(userID, botID int64, input tg.InputInvoiceClass) string {
	invoice, ok := input.(*tg.InputInvoiceMessage)
	if !ok || invoice == nil || invoice.MsgID <= 0 || userID <= 0 || botID <= 0 {
		return ""
	}
	return fmt.Sprintf("premiumbot-message:%d:%d:%d", userID, botID, invoice.MsgID)
}

func (r *Router) sendPremiumStarsForm(
	ctx context.Context,
	userID, formID int64,
	input tg.InputInvoiceClass,
) (tg.PaymentsPaymentResultClass, error) {
	if formID == 0 {
		return nil, formIDEmptyErr()
	}
	if r.deps.Premium == nil {
		return nil, notImplementedErr()
	}
	invoice, err := r.resolvePremiumInvoice(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	recipientID := invoice.RecipientUserID
	if invoice.Kind == domain.PremiumPurchaseSelf {
		recipientID = userID
	}
	recipientBlocked := false
	if invoice.Kind == domain.PremiumPurchaseGift {
		recipientBlocked, err = r.peerBlocksUser(ctx, userID, recipientID)
		if err != nil {
			return nil, internalErr()
		}
	}
	originAuthKeyID, originSessionID := deliveryExclusionFromContext(ctx)
	result, err := r.deps.Premium.PurchaseWithDelivery(ctx, domain.PremiumPurchaseRequest{
		BuyerUserID: userID, FormID: formID, Kind: invoice.Kind,
		RecipientUserID: recipientID, Months: invoice.Months,
		PlanVersion: invoice.PlanVersion, Message: invoice.Message,
		Date: int(r.clock.Now().Unix()), CommandKey: fmt.Sprintf("premium:%d", formID),
		OriginAuthKeyID: originAuthKeyID, OriginSessionID: originSessionID,
		RecipientBlocked: recipientBlocked,
	}, r.premiumPurchaseDeliveryEffects(originAuthKeyID, originSessionID))
	if err != nil {
		return nil, premiumPaymentErr(err)
	}
	if result.Duplicate {
		return nil, tgerr.New(400, "INVOICE_ALREADY_PAID")
	}
	r.invalidatePremiumUserCaches(ctx, result.User.ID)
	r.invalidateRPCProjectionForUser(result.User.ID)
	updates := r.starGiftSendUpdates(ctx, userID, result.Send)
	if result.Form.EffectiveDebitStars() {
		appendStarGiftBalanceUpdate(updates, domain.StarGiftCurrencyStars, result.Balance.Balance)
	}
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

func (r *Router) resolvePremiumInvoice(
	ctx context.Context,
	userID int64,
	input tg.InputInvoiceClass,
) (domain.PremiumInvoice, error) {
	switch invoice := input.(type) {
	case *tg.InputInvoicePremiumGiftCode:
		if invoice == nil || r.deps.Premium == nil {
			return domain.PremiumInvoice{}, tgerr.New(400, "PREMIUM_GIFT_CODE_INVALID")
		}
		purpose, ok := invoice.Purpose.(*tg.InputStorePaymentPremiumGiftCode)
		if !ok || purpose == nil || purpose.BoostPeer != nil || len(purpose.Users) != 1 ||
			invoice.Option.Users != 1 || invoice.Option.Months <= 0 {
			return domain.PremiumInvoice{}, tgerr.New(400, "PREMIUM_GIFT_CODE_INVALID")
		}
		recipient, found, err := r.userFromInput(ctx, userID, purpose.Users[0])
		if err != nil {
			return domain.PremiumInvoice{}, internalErr()
		}
		if !found || recipient.ID <= 0 || recipient.ID == userID || recipient.Bot ||
			recipient.Deleted || domain.IsSystemUserID(recipient.ID) {
			if recipient.ID == userID {
				return domain.PremiumInvoice{}, tgerr.New(400, "PREMIUM_GIFT_SELF_INVALID")
			}
			return domain.PremiumInvoice{}, userIDInvalidErr()
		}
		if err := r.rejectActivePremiumGiftRecipient(recipient); err != nil {
			return domain.PremiumInvoice{}, err
		}
		if restricted, err := r.premiumGiftsRestricted(ctx, recipient.ID); err != nil {
			return domain.PremiumInvoice{}, internalErr()
		} else if restricted {
			return domain.PremiumInvoice{}, tgerr.New(403, "USER_PRIVACY_RESTRICTED")
		}
		plan, err := r.deps.Premium.Plan(ctx, invoice.Option.Months)
		if err != nil {
			return domain.PremiumInvoice{}, premiumPaymentErr(err)
		}
		storeProduct, _ := invoice.Option.GetStoreProduct()
		storeQuantity, _ := invoice.Option.GetStoreQuantity()
		if purpose.Currency != plan.EffectiveFiatCurrency() || purpose.Amount != plan.EffectiveFiatAmount() ||
			invoice.Option.Currency != purpose.Currency || invoice.Option.Amount != purpose.Amount ||
			storeProduct != plan.StoreProduct || storeQuantity != plan.StoreQuantity {
			return domain.PremiumInvoice{}, starsFormAmountMismatchErr()
		}
		message := domain.PremiumGiftMessage{}
		if text, ok := purpose.GetMessage(); ok {
			if len([]rune(text.Text)) > domain.MaxPremiumGiftMessageRunes {
				return domain.PremiumInvoice{}, messageTooLongErr()
			}
			message.Text = text.Text
			message.Entities = domainMessageEntitiesForViewer(userID, text.Entities)
			if len(message.Entities) != len(text.Entities) || !message.Valid() {
				return domain.PremiumInvoice{}, entityBoundsInvalidErr()
			}
		}
		out := premiumInvoiceFromPlan(domain.PremiumPurchaseGift, recipient.ID, plan, message)
		out.PaymentCurrency = plan.EffectiveFiatCurrency()
		out.PaymentAmount = plan.EffectiveFiatAmount()
		out.DebitStars = false
		return out, nil

	case *tg.InputInvoicePremiumGiftStars:
		if invoice == nil || invoice.Months <= 0 {
			return domain.PremiumInvoice{}, tgerr.New(400, "PREMIUM_GIFT_CODE_INVALID")
		}
		recipient, found, err := r.userFromInput(ctx, userID, invoice.UserID)
		if err != nil {
			return domain.PremiumInvoice{}, internalErr()
		}
		if !found || recipient.ID <= 0 || recipient.ID == userID || recipient.Bot ||
			recipient.Deleted || domain.IsSystemUserID(recipient.ID) {
			if recipient.ID == userID {
				return domain.PremiumInvoice{}, tgerr.New(400, "PREMIUM_GIFT_SELF_INVALID")
			}
			return domain.PremiumInvoice{}, userIDInvalidErr()
		}
		if err := r.rejectActivePremiumGiftRecipient(recipient); err != nil {
			return domain.PremiumInvoice{}, err
		}
		if restricted, err := r.premiumGiftsRestricted(ctx, recipient.ID); err != nil {
			return domain.PremiumInvoice{}, internalErr()
		} else if restricted {
			return domain.PremiumInvoice{}, tgerr.New(403, "USER_PRIVACY_RESTRICTED")
		}
		plan, err := r.deps.Premium.Plan(ctx, invoice.Months)
		if err != nil {
			return domain.PremiumInvoice{}, premiumPaymentErr(err)
		}
		message := domain.PremiumGiftMessage{}
		if text, ok := invoice.GetMessage(); ok {
			if len([]rune(text.Text)) > domain.MaxPremiumGiftMessageRunes {
				return domain.PremiumInvoice{}, messageTooLongErr()
			}
			message.Text = text.Text
			message.Entities = domainMessageEntitiesForViewer(userID, text.Entities)
			if len(message.Entities) != len(text.Entities) || !message.Valid() {
				return domain.PremiumInvoice{}, entityBoundsInvalidErr()
			}
		}
		return premiumInvoiceFromPlan(domain.PremiumPurchaseGift, recipient.ID, plan, message), nil

	case *tg.InputInvoiceMessage:
		if invoice == nil || invoice.MsgID <= 0 || r.deps.Messages == nil {
			return domain.PremiumInvoice{}, tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, invoice.Peer)
		if err != nil {
			return domain.PremiumInvoice{}, err
		}
		if peer.Type != domain.PeerTypeUser || peer.ID != r.deps.Premium.BotUserID() {
			return domain.PremiumInvoice{}, tgerr.New(400, "INVOICE_INVALID")
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, []int{invoice.MsgID})
		if err != nil || len(list.Messages) != 1 {
			return domain.PremiumInvoice{}, tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		message := list.Messages[0]
		if message.Peer != peer || message.Media == nil ||
			message.Media.Kind != domain.MessageMediaKindInvoice ||
			message.Media.Invoice == nil || !message.Media.Invoice.Valid() {
			return domain.PremiumInvoice{}, tgerr.New(400, "INVOICE_INVALID")
		}
		out := *message.Media.Invoice
		switch out.Kind {
		case domain.PremiumPurchaseSelf:
			if out.RecipientUserID != 0 {
				return domain.PremiumInvoice{}, tgerr.New(400, "INVOICE_INVALID")
			}
		case domain.PremiumPurchaseGift:
			recipient, found, err := r.deps.Users.ByID(ctx, userID, out.RecipientUserID)
			if err != nil {
				return domain.PremiumInvoice{}, internalErr()
			}
			if !found || recipient.ID <= 0 || recipient.ID == userID || recipient.Bot ||
				recipient.Deleted || domain.IsSystemUserID(recipient.ID) {
				return domain.PremiumInvoice{}, userIDInvalidErr()
			}
			if err := r.rejectActivePremiumGiftRecipient(recipient); err != nil {
				return domain.PremiumInvoice{}, err
			}
			if restricted, err := r.premiumGiftsRestricted(ctx, recipient.ID); err != nil {
				return domain.PremiumInvoice{}, internalErr()
			} else if restricted {
				return domain.PremiumInvoice{}, tgerr.New(403, "USER_PRIVACY_RESTRICTED")
			}
		default:
			return domain.PremiumInvoice{}, tgerr.New(400, "INVOICE_INVALID")
		}
		plan, err := r.deps.Premium.Plan(ctx, out.Months)
		if err != nil {
			return domain.PremiumInvoice{}, premiumPaymentErr(err)
		}
		if plan.Version != out.PlanVersion || plan.AmountStars != out.AmountStars ||
			plan.DurationDays != out.DurationDays {
			return domain.PremiumInvoice{}, starsFormAmountMismatchErr()
		}
		return out, nil

	case *tg.InputInvoiceStars:
		if invoice == nil {
			return domain.PremiumInvoice{}, tgerr.New(400, "INVOICE_INVALID")
		}
		purpose, ok := invoice.Purpose.(*tg.InputStorePaymentPremiumSubscription)
		if !ok || purpose == nil || purpose.Restore {
			return domain.PremiumInvoice{}, tgerr.New(400, "INVOICE_INVALID")
		}
		plans, err := r.deps.Premium.Plans(ctx)
		if err != nil || len(plans) == 0 {
			return domain.PremiumInvoice{}, premiumPaymentErr(domain.ErrPremiumPlanUnavailable)
		}
		// The native store purpose has no product/month field. Telegram uses the
		// upgrade bit to distinguish the longer subscription product, so map a
		// normal purchase to the shortest enabled catalog plan and an upgrade to
		// the longest. This is deterministic even if an administrator reorders the
		// catalog; exact arbitrary durations remain available through @premiumbot.
		plan, ok := nativePremiumSubscriptionPlan(plans, purpose.Upgrade)
		if !ok {
			return domain.PremiumInvoice{}, premiumPaymentErr(domain.ErrPremiumPlanUnavailable)
		}
		return premiumInvoiceFromPlan(domain.PremiumPurchaseSelf, 0, plan, domain.PremiumGiftMessage{}), nil
	default:
		return domain.PremiumInvoice{}, notImplementedErr()
	}
}

func nativePremiumSubscriptionPlan(plans []domain.PremiumPlan, upgrade bool) (domain.PremiumPlan, bool) {
	var selected domain.PremiumPlan
	found := false
	for _, plan := range plans {
		if !plan.Valid() || !plan.Enabled {
			continue
		}
		if !found || (!upgrade && plan.Months < selected.Months) ||
			(upgrade && plan.Months > selected.Months) ||
			(plan.Months == selected.Months && plan.Version > selected.Version) {
			selected = plan
			found = true
		}
	}
	return selected, found
}

func (r *Router) premiumGiftsRestricted(ctx context.Context, userID int64) (bool, error) {
	svc, ok := r.accountSettingsSvc()
	if !ok {
		return false, nil
	}
	settings, err := svc.GetAccountSettings(ctx, userID)
	if err != nil {
		return false, err
	}
	return settings.GlobalPrivacy.DisallowedGifts.PremiumGifts, nil
}

func (r *Router) rejectActivePremiumGiftRecipient(recipient domain.User) error {
	now := r.clock.Now().Unix()
	if !recipient.PremiumActiveAt(now) {
		return nil
	}
	return tgerr.New(420, fmt.Sprintf("PREMIUM_SUB_ACTIVE_UNTIL_%d", recipient.PremiumUntil))
}

func premiumInvoiceFromPlan(
	kind domain.PremiumPurchaseKind,
	recipientID int64,
	plan domain.PremiumPlan,
	message domain.PremiumGiftMessage,
) domain.PremiumInvoice {
	title := fmt.Sprintf("Telegram Premium — %s", plan.Label)
	description := fmt.Sprintf("%d days of Telegram Premium for %d Stars.", plan.DurationDays, plan.AmountStars)
	if kind == domain.PremiumPurchaseGift {
		description = fmt.Sprintf("Gift %d days of Telegram Premium for %d Stars.", plan.DurationDays, plan.AmountStars)
	}
	return domain.PremiumInvoice{
		Kind: kind, RecipientUserID: recipientID, Months: plan.Months,
		DurationDays: plan.DurationDays, AmountStars: plan.AmountStars,
		PlanVersion: plan.Version, Title: title, Description: description,
		StartParam: fmt.Sprintf("buy_%d", plan.Months), Message: message,
	}
}

type premiumUserCacheInvalidator interface {
	InvalidateUsers(ctx context.Context, userIDs ...int64)
}

func (r *Router) invalidatePremiumUserCaches(ctx context.Context, userIDs ...int64) {
	if invalidator, ok := r.deps.Users.(premiumUserCacheInvalidator); ok {
		invalidator.InvalidateUsers(ctx, userIDs...)
	}
}

func premiumPaymentErr(err error) error {
	var active domain.PremiumSubscriptionActiveError
	switch {
	case errors.As(err, &active):
		return tgerr.New(420, fmt.Sprintf("PREMIUM_SUB_ACTIVE_UNTIL_%d", active.Until))
	case errors.Is(err, domain.ErrStarsInsufficient):
		return balanceTooLowErr()
	case errors.Is(err, domain.ErrPremiumFormExpired):
		return formExpiredErr()
	case errors.Is(err, domain.ErrPremiumFormAmountChanged):
		return starsFormAmountMismatchErr()
	case errors.Is(err, domain.ErrPremiumInvoiceAlreadyPaid):
		return tgerr.New(400, "INVOICE_ALREADY_PAID")
	case errors.Is(err, domain.ErrPremiumGiftMessageInvalid):
		return entityBoundsInvalidErr()
	case errors.Is(err, domain.ErrPremiumGiftSelf):
		return tgerr.New(400, "PREMIUM_GIFT_SELF_INVALID")
	case errors.Is(err, domain.ErrPremiumRecipientInvalid):
		return userIDInvalidErr()
	case errors.Is(err, domain.ErrPremiumRecipientRestricted):
		return tgerr.New(403, "USER_PRIVACY_RESTRICTED")
	case errors.Is(err, domain.ErrPremiumPlanInvalid),
		errors.Is(err, domain.ErrPremiumPlanUnavailable),
		errors.Is(err, domain.ErrPremiumFormInvalid):
		return tgerr.New(400, "PREMIUM_GIFT_CODE_INVALID")
	default:
		return internalErr()
	}
}
