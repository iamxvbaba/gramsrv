package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) starGiftUpgradePaymentForm(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftUpgrade) (tg.PaymentsPaymentFormClass, error) {
	saved, preview, err := r.starGiftUpgradeTarget(ctx, userID, inv.Stargift)
	if err != nil {
		return nil, err
	}
	return &tg.PaymentsPaymentFormStarGift{
		FormID: starGiftUpgradeFormID(userID, saved.ID, preview.UpgradeStars, inv.KeepOriginalDetails),
		Invoice: tg.Invoice{
			Currency: "XTR",
			Prices:   []tg.LabeledPrice{{Label: "Star gift upgrade", Amount: preview.UpgradeStars}},
		},
	}, nil
}

func (r *Router) sendStarGiftUpgradeForm(ctx context.Context, userID, formID int64, inv *tg.InputInvoiceStarGiftUpgrade) (tg.PaymentsPaymentResultClass, error) {
	saved, err := r.starGiftUpgradeSavedTarget(ctx, userID, inv.Stargift)
	if err != nil {
		return nil, err
	}
	commandKey := fmt.Sprintf("paid:%d:%d:%t", saved.ID, formID, inv.KeepOriginalDetails)
	receipt, replay, err := r.deps.Gifts.UpgradeReceipt(ctx, userID, commandKey)
	if err != nil {
		return nil, internalErr()
	}
	chargeStars := int64(0)
	if replay {
		if receipt.SourceSavedGiftID != saved.ID || receipt.FormID != formID || receipt.RequirePrepaid ||
			receipt.KeepOriginalDetails != inv.KeepOriginalDetails || receipt.ChargeStars <= 0 {
			return nil, starGiftInvalidErr()
		}
		chargeStars = receipt.ChargeStars
	} else {
		preview, err := r.starGiftUpgradePreviewForSaved(ctx, saved)
		if err != nil {
			return nil, err
		}
		wantFormID := starGiftUpgradeFormID(userID, saved.ID, preview.UpgradeStars, inv.KeepOriginalDetails)
		if formID == 0 || formID != wantFormID {
			return nil, starsFormAmountMismatchErr()
		}
		chargeStars = preview.UpgradeStars
	}
	result, err := r.deps.Gifts.Upgrade(ctx, domain.StarGiftUpgradeRequest{
		UserID: userID, Ref: starGiftUpgradeSavedRef(saved),
		KeepOriginalDetails: inv.KeepOriginalDetails, ChargeStars: chargeStars,
		FormID: formID, CommandKey: commandKey,
		Date: int(r.clock.Now().Unix()), OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionIDOrZero(ctx),
	})
	if err != nil {
		return nil, starGiftUpgradeErr(err)
	}
	r.invalidateStarGiftOwnerProjection(saved.Owner)
	updates := r.tgStarGiftUpgradeUpdates(ctx, userID, result, true)
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

func (r *Router) onPaymentsUpgradeStarGift(ctx context.Context, req *tg.PaymentsUpgradeStarGiftRequest) (tg.UpdatesClass, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	saved, err := r.starGiftUpgradeSavedTarget(ctx, userID, req.Stargift)
	if err != nil {
		return nil, err
	}
	commandKey := fmt.Sprintf("prepaid:%d:%t", saved.ID, req.KeepOriginalDetails)
	receipt, replay, err := r.deps.Gifts.UpgradeReceipt(ctx, userID, commandKey)
	if err != nil {
		return nil, internalErr()
	}
	if replay {
		if receipt.SourceSavedGiftID != saved.ID || receipt.FormID != 0 || !receipt.RequirePrepaid ||
			receipt.KeepOriginalDetails != req.KeepOriginalDetails || receipt.ChargeStars != 0 {
			return nil, starGiftInvalidErr()
		}
	} else {
		if _, err := r.starGiftUpgradePreviewForSaved(ctx, saved); err != nil {
			return nil, err
		}
		if saved.PrepaidUpgradeStars <= 0 {
			return nil, starGiftInvalidErr()
		}
	}
	result, err := r.deps.Gifts.Upgrade(ctx, domain.StarGiftUpgradeRequest{
		UserID: userID, Ref: starGiftUpgradeSavedRef(saved),
		KeepOriginalDetails: req.KeepOriginalDetails, RequirePrepaid: true,
		CommandKey: commandKey,
		Date:       int(r.clock.Now().Unix()), OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionIDOrZero(ctx),
	})
	if err != nil {
		return nil, starGiftUpgradeErr(err)
	}
	r.invalidateStarGiftOwnerProjection(saved.Owner)
	return r.tgStarGiftUpgradeUpdates(ctx, userID, result, false), nil
}

func (r *Router) starGiftUpgradeTarget(ctx context.Context, userID int64, input tg.InputSavedStarGiftClass) (domain.SavedStarGift, domain.StarGiftUpgradePreview, error) {
	saved, err := r.starGiftUpgradeSavedTarget(ctx, userID, input)
	if err != nil {
		return domain.SavedStarGift{}, domain.StarGiftUpgradePreview{}, err
	}
	preview, err := r.starGiftUpgradePreviewForSaved(ctx, saved)
	return saved, preview, err
}

func (r *Router) starGiftUpgradeSavedTarget(ctx context.Context, userID int64, input tg.InputSavedStarGiftClass) (domain.SavedStarGift, error) {
	if r.deps.Gifts == nil {
		return domain.SavedStarGift{}, notImplementedErr()
	}
	ref, ok, err := r.starGiftRefFromInput(ctx, userID, input)
	if err != nil {
		return domain.SavedStarGift{}, err
	}
	if !ok {
		return domain.SavedStarGift{}, starGiftInvalidErr()
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, ref.Owner); err != nil {
		return domain.SavedStarGift{}, err
	}
	saved, found, err := r.deps.Gifts.GetSaved(ctx, ref)
	if err != nil {
		return domain.SavedStarGift{}, internalErr()
	}
	if !found {
		return domain.SavedStarGift{}, starGiftInvalidErr()
	}
	return saved, nil
}

func (r *Router) starGiftUpgradePreviewForSaved(ctx context.Context, saved domain.SavedStarGift) (domain.StarGiftUpgradePreview, error) {
	if saved.Converted || saved.UniqueGiftID != 0 {
		return domain.StarGiftUpgradePreview{}, starGiftInvalidErr()
	}
	preview, found, err := r.deps.Gifts.CollectiblePreview(ctx, saved.GiftID)
	if err != nil {
		return domain.StarGiftUpgradePreview{}, internalErr()
	}
	if !found || preview.UpgradeStars <= 0 || preview.Issued >= preview.SupplyTotal {
		return domain.StarGiftUpgradePreview{}, starGiftInvalidErr()
	}
	return preview, nil
}

func starGiftUpgradeSavedRef(saved domain.SavedStarGift) domain.SavedStarGiftRef {
	ref := domain.SavedStarGiftRef{Owner: saved.Owner}
	if saved.Owner.Type == domain.PeerTypeChannel {
		ref.SavedID = saved.SavedID
	} else {
		ref.MsgID = saved.MsgID
	}
	return ref
}

func (r *Router) tgStarGiftUpgradeUpdates(ctx context.Context, ownerUserID int64, result domain.StarGiftUpgradeResult, includeBalance bool) *tg.Updates {
	message, event := result.Send.RecipientMessage, result.Send.RecipientEvent
	if result.Send.SenderMessage.OwnerUserID == ownerUserID {
		message, event = result.Send.SenderMessage, result.Send.SenderEvent
	}
	updates := tgPrivateMessageUpdates(event, message, 0, false,
		r.usersForMessageUpdate(ctx, ownerUserID, message),
		r.chatsForMessageUpdate(ctx, ownerUserID, message))
	for _, edit := range result.SourceEdits {
		if edit.UserID != ownerUserID {
			continue
		}
		if update := tgOtherUpdateFromEvent(edit.Event); update != nil {
			updates.Updates = append(updates.Updates, update)
			if edit.Event.Date > updates.Date {
				updates.Date = edit.Event.Date
			}
		}
	}
	if includeBalance {
		updates.Updates = append(updates.Updates, &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: result.Balance.Balance}})
	}
	return updates
}

func starGiftUpgradeFormID(userID, savedGiftID, stars int64, keepOriginal bool) int64 {
	id := userID*0x9e3779b1 ^ savedGiftID<<11 ^ stars<<19 ^ 0x55504752414445
	if keepOriginal {
		id ^= 0x4b454550
	}
	if id < 0 {
		id = ^id
	}
	if id == 0 {
		id = 1
	}
	return id
}

func starGiftUpgradeErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStarsInsufficient):
		return starsErr(err)
	case errors.Is(err, domain.ErrStarGiftNotFound),
		errors.Is(err, domain.ErrStarGiftAlreadyConverted),
		errors.Is(err, domain.ErrStarGiftAlreadyUpgraded),
		errors.Is(err, domain.ErrStarGiftCollectibleUnavailable),
		errors.Is(err, domain.ErrStarGiftCollectibleSoldOut),
		errors.Is(err, domain.ErrStarGiftCollectibleInvalid):
		return starGiftInvalidErr()
	default:
		return internalErr()
	}
}

func sessionIDOrZero(ctx context.Context) int64 {
	sessionID, _ := SessionIDFrom(ctx)
	return sessionID
}

func (r *Router) onPaymentsGetStarGiftUpgradePreview(ctx context.Context, giftID int64) (*tg.PaymentsStarGiftUpgradePreview, error) {
	if giftID <= 0 || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	preview, found, err := r.deps.Gifts.CollectiblePreview(ctx, giftID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || preview.Issued >= preview.SupplyTotal {
		return nil, starGiftInvalidErr()
	}
	return &tg.PaymentsStarGiftUpgradePreview{
		SampleAttributes: tgStarGiftPreviewAttributes(preview),
		Prices:           []tg.StarGiftUpgradePrice{},
		NextPrices:       []tg.StarGiftUpgradePrice{},
	}, nil
}

func (r *Router) onPaymentsGetStarGiftUpgradeAttributes(ctx context.Context, giftID int64) (*tg.PaymentsStarGiftUpgradeAttributes, error) {
	if giftID <= 0 || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	preview, found, err := r.deps.Gifts.CollectiblePreview(ctx, giftID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, starGiftInvalidErr()
	}
	return &tg.PaymentsStarGiftUpgradeAttributes{Attributes: tgAllStarGiftAttributes(preview)}, nil
}

func (r *Router) onPaymentsGetUniqueStarGift(ctx context.Context, slug string) (*tg.PaymentsUniqueStarGift, error) {
	if r.deps.Gifts == nil || strings.TrimSpace(slug) == "" {
		return nil, starGiftInvalidErr()
	}
	viewerUserID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	unique, found, err := r.deps.Gifts.UniqueBySlug(ctx, slug)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, starGiftInvalidErr()
	}
	out := &tg.PaymentsUniqueStarGift{
		Gift:  tgUniqueStarGift(unique),
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{},
	}
	switch unique.Owner.Type {
	case domain.PeerTypeUser:
		ids := []int64{unique.Owner.ID}
		if unique.KeepOriginalDetails && !unique.OriginalNameHidden && unique.OriginalFromUserID != 0 && unique.OriginalFromUserID != unique.Owner.ID {
			ids = append(ids, unique.OriginalFromUserID)
		}
		out.Users = tgUsersForViewer(viewerUserID, r.domainUsersForIDs(ctx, viewerUserID, ids))
	case domain.PeerTypeChannel:
		out.Chats = r.tgChatsForChannelIDs(ctx, viewerUserID, []int64{unique.Owner.ID})
	}
	return out, nil
}

func tgStarGiftPreviewAttributes(preview domain.StarGiftUpgradePreview) []tg.StarGiftAttributeClass {
	out := make([]tg.StarGiftAttributeClass, 0, len(preview.Models)+len(preview.Patterns)+len(preview.Backdrops))
	for _, attribute := range preview.Models {
		if attribute.Crafted {
			continue
		}
		out = append(out, tgStarGiftAttribute(attribute))
	}
	for _, attribute := range preview.Patterns {
		out = append(out, tgStarGiftAttribute(attribute))
	}
	for _, attribute := range preview.Backdrops {
		out = append(out, tgStarGiftAttribute(attribute))
	}
	return out
}

func tgAllStarGiftAttributes(preview domain.StarGiftUpgradePreview) []tg.StarGiftAttributeClass {
	out := make([]tg.StarGiftAttributeClass, 0, len(preview.Models)+len(preview.Patterns)+len(preview.Backdrops))
	for _, attributes := range [][]domain.StarGiftCollectibleAttribute{preview.Models, preview.Patterns, preview.Backdrops} {
		for _, attribute := range attributes {
			out = append(out, tgStarGiftAttribute(attribute))
		}
	}
	return out
}

func tgStarGiftAttribute(attribute domain.StarGiftCollectibleAttribute) tg.StarGiftAttributeClass {
	rarity := tgStarGiftAttributeRarity(attribute)
	switch attribute.Kind {
	case domain.StarGiftCollectibleModel:
		document := tg.DocumentClass(&tg.DocumentEmpty{})
		if attribute.Document != nil {
			document = tgDocument(*attribute.Document)
		}
		return &tg.StarGiftAttributeModel{Name: attribute.Name, Document: document, Rarity: rarity, Crafted: attribute.Crafted}
	case domain.StarGiftCollectiblePattern:
		document := tg.DocumentClass(&tg.DocumentEmpty{})
		if attribute.Document != nil {
			document = tgDocument(*attribute.Document)
		}
		return &tg.StarGiftAttributePattern{Name: attribute.Name, Document: document, Rarity: rarity}
	case domain.StarGiftCollectibleBackdrop:
		return &tg.StarGiftAttributeBackdrop{
			Name: attribute.Name, BackdropID: attribute.BackdropID,
			CenterColor: attribute.CenterColor, EdgeColor: attribute.EdgeColor,
			PatternColor: attribute.PatternColor, TextColor: attribute.TextColor, Rarity: rarity,
		}
	default:
		return &tg.StarGiftAttributeBackdrop{Name: attribute.Name, Rarity: rarity}
	}
}

func tgStarGiftAttributeRarity(attribute domain.StarGiftCollectibleAttribute) tg.StarGiftAttributeRarityClass {
	switch attribute.RarityKind {
	case domain.StarGiftRarityUncommon:
		return &tg.StarGiftAttributeRarityUncommon{}
	case domain.StarGiftRarityRare:
		return &tg.StarGiftAttributeRarityRare{}
	case domain.StarGiftRarityEpic:
		return &tg.StarGiftAttributeRarityEpic{}
	case domain.StarGiftRarityLegendary:
		return &tg.StarGiftAttributeRarityLegendary{}
	default:
		return &tg.StarGiftAttributeRarity{Permille: attribute.RarityPermille}
	}
}

func tgUniqueStarGift(unique domain.UniqueStarGift) *tg.StarGiftUnique {
	attributes := []tg.StarGiftAttributeClass{
		tgStarGiftAttribute(unique.Model),
		tgStarGiftAttribute(unique.Pattern),
		tgStarGiftAttribute(unique.Backdrop),
	}
	if unique.KeepOriginalDetails && unique.OriginalOwner.ID != 0 {
		original := &tg.StarGiftAttributeOriginalDetails{
			RecipientID: tgPeer(unique.OriginalOwner),
			Date:        unique.OriginalDate,
		}
		if unique.OriginalFromUserID != 0 && !unique.OriginalNameHidden {
			original.SetSenderID(&tg.PeerUser{UserID: unique.OriginalFromUserID})
		}
		if unique.OriginalMessage != "" {
			original.SetMessage(tg.TextWithEntities{Text: unique.OriginalMessage})
		}
		attributes = append(attributes, original)
	}
	out := &tg.StarGiftUnique{
		RequirePremium: unique.RequirePremium, ResaleTonOnly: unique.ResaleTonOnly,
		ThemeAvailable: unique.ThemeAvailable, Burned: unique.Burned, Crafted: unique.Crafted,
		ID: unique.ID, GiftID: unique.GiftID, Title: unique.Title, Slug: unique.Slug, Num: unique.Num,
		Attributes: attributes, AvailabilityIssued: unique.AvailabilityIssued, AvailabilityTotal: unique.AvailabilityTotal,
	}
	if unique.OwnerAddress != "" {
		out.SetOwnerAddress(unique.OwnerAddress)
	} else if owner := tgPeer(unique.Owner); owner != nil {
		out.SetOwnerID(owner)
	} else if unique.OwnerName != "" {
		out.SetOwnerName(unique.OwnerName)
	}
	if unique.GiftAddress != "" {
		out.SetGiftAddress(unique.GiftAddress)
	}
	if unique.ResellAmount != nil {
		out.SetResellAmount([]tg.StarsAmountClass{tgStarGiftAmount(*unique.ResellAmount)})
	}
	if peer := tgPeer(unique.ReleasedBy); peer != nil {
		out.SetReleasedBy(peer)
	}
	if unique.ValueAmount > 0 {
		out.SetValueAmount(unique.ValueAmount)
	}
	if unique.ValueCurrency != "" {
		out.SetValueCurrency(unique.ValueCurrency)
	}
	if unique.ValueUSD > 0 {
		out.SetValueUsdAmount(unique.ValueUSD)
	}
	if peer := tgPeer(unique.ThemePeer); peer != nil {
		out.SetThemePeer(peer)
	}
	if peer := tgPeer(unique.Host); peer != nil {
		out.SetHostID(peer)
	}
	if unique.OfferMinStars > 0 && unique.Owner.Type == domain.PeerTypeUser {
		out.SetOfferMinStars(unique.OfferMinStars)
	}
	if unique.CraftChancePermille > 0 {
		out.SetCraftChancePermille(unique.CraftChancePermille)
	}
	return out
}

func tgStarGiftAmount(amount domain.StarGiftAmount) tg.StarsAmountClass {
	if amount.Currency == domain.StarGiftCurrencyTON {
		return &tg.StarsTonAmount{Amount: amount.Amount}
	}
	return &tg.StarsAmount{Amount: amount.Amount, Nanos: amount.Nanos}
}

func domainStarGiftAmount(amount tg.StarsAmountClass) (domain.StarGiftAmount, bool) {
	switch value := amount.(type) {
	case *tg.StarsAmount:
		if value == nil {
			return domain.StarGiftAmount{}, false
		}
		out := domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: value.Amount, Nanos: value.Nanos}
		return out, out.Valid()
	case *tg.StarsTonAmount:
		if value == nil {
			return domain.StarGiftAmount{}, false
		}
		out := domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: value.Amount}
		return out, out.Valid()
	default:
		return domain.StarGiftAmount{}, false
	}
}
