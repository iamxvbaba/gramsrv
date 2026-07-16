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
	saved, preview, err := r.starGiftUpgradeTarget(ctx, userID, inv.Stargift)
	if err != nil {
		return nil, err
	}
	wantFormID := starGiftUpgradeFormID(userID, saved.ID, preview.UpgradeStars, inv.KeepOriginalDetails)
	if formID == 0 || formID != wantFormID {
		return nil, starsFormAmountMismatchErr()
	}
	result, err := r.deps.Gifts.Upgrade(ctx, domain.StarGiftUpgradeRequest{
		UserID: userID, Ref: domain.SavedStarGiftRef{Owner: saved.Owner, MsgID: saved.MsgID},
		KeepOriginalDetails: inv.KeepOriginalDetails, ChargeStars: preview.UpgradeStars,
		FormID: formID, CommandKey: fmt.Sprintf("paid:%d:%d:%t", saved.ID, formID, inv.KeepOriginalDetails),
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
	saved, _, err := r.starGiftUpgradeTarget(ctx, userID, req.Stargift)
	if err != nil {
		return nil, err
	}
	if saved.PrepaidUpgradeStars <= 0 {
		return nil, starGiftInvalidErr()
	}
	result, err := r.deps.Gifts.Upgrade(ctx, domain.StarGiftUpgradeRequest{
		UserID: userID, Ref: domain.SavedStarGiftRef{Owner: saved.Owner, MsgID: saved.MsgID},
		KeepOriginalDetails: req.KeepOriginalDetails, RequirePrepaid: true,
		CommandKey: fmt.Sprintf("prepaid:%d:%t", saved.ID, req.KeepOriginalDetails),
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
	if r.deps.Gifts == nil {
		return domain.SavedStarGift{}, domain.StarGiftUpgradePreview{}, notImplementedErr()
	}
	ref, ok, err := r.starGiftRefFromInput(ctx, userID, input)
	if err != nil {
		return domain.SavedStarGift{}, domain.StarGiftUpgradePreview{}, err
	}
	if !ok || ref.Owner.Type != domain.PeerTypeUser || ref.Owner.ID != userID {
		// Channel gift upgrades require a channel pts aggregate and are not silently
		// routed through the private-message transaction.
		return domain.SavedStarGift{}, domain.StarGiftUpgradePreview{}, starGiftInvalidErr()
	}
	saved, found, err := r.deps.Gifts.GetSaved(ctx, ref)
	if err != nil {
		return domain.SavedStarGift{}, domain.StarGiftUpgradePreview{}, internalErr()
	}
	if !found || saved.Converted || saved.UniqueGiftID != 0 {
		return domain.SavedStarGift{}, domain.StarGiftUpgradePreview{}, starGiftInvalidErr()
	}
	preview, found, err := r.deps.Gifts.CollectiblePreview(ctx, saved.GiftID)
	if err != nil {
		return domain.SavedStarGift{}, domain.StarGiftUpgradePreview{}, internalErr()
	}
	if !found || preview.UpgradeStars <= 0 || preview.Issued >= preview.SupplyTotal {
		return domain.SavedStarGift{}, domain.StarGiftUpgradePreview{}, starGiftInvalidErr()
	}
	return saved, preview, nil
}

func (r *Router) tgStarGiftUpgradeUpdates(ctx context.Context, ownerUserID int64, result domain.StarGiftUpgradeResult, includeBalance bool) *tg.Updates {
	message, event := result.Send.RecipientMessage, result.Send.RecipientEvent
	if result.Send.SenderMessage.OwnerUserID == ownerUserID {
		message, event = result.Send.SenderMessage, result.Send.SenderEvent
	}
	updates := tgPrivateMessageUpdates(event, message, 0, false,
		r.usersForMessageUpdate(ctx, ownerUserID, message),
		r.chatsForMessageUpdate(ctx, ownerUserID, message))
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

func tgStarGiftAttribute(attribute domain.StarGiftCollectibleAttribute) tg.StarGiftAttributeClass {
	rarity := &tg.StarGiftAttributeRarity{Permille: attribute.RarityPermille}
	switch attribute.Kind {
	case domain.StarGiftCollectibleModel:
		document := tg.DocumentClass(&tg.DocumentEmpty{})
		if attribute.Document != nil {
			document = tgDocument(*attribute.Document)
		}
		return &tg.StarGiftAttributeModel{Name: attribute.Name, Document: document, Rarity: rarity}
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
		ID: unique.ID, GiftID: unique.GiftID, Title: unique.Title, Slug: unique.Slug, Num: unique.Num,
		Attributes: attributes, AvailabilityIssued: unique.AvailabilityIssued, AvailabilityTotal: unique.AvailabilityTotal,
	}
	if owner := tgPeer(unique.Owner); owner != nil {
		out.SetOwnerID(owner)
	}
	return out
}
