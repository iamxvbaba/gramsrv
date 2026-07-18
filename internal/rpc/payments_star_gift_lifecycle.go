package rpc

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func (r *Router) starGiftTransferPaymentForm(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftTransfer) (tg.PaymentsPaymentFormClass, error) {
	target, to, err := r.starGiftPaidTransferTarget(ctx, userID, inv)
	if err != nil {
		return nil, err
	}
	return &tg.PaymentsPaymentFormStarGift{FormID: starGiftLifecycleFormID("transfer", userID,
		target.ID, target.Owner.Type, target.Owner.ID, to.Type, to.ID, target.TransferStars, target.CanTransferAt),
		Invoice: tg.Invoice{Currency: "XTR", Prices: []tg.LabeledPrice{{Label: "Collectible gift transfer", Amount: target.TransferStars}}}}, nil
}

func (r *Router) sendStarGiftTransferForm(ctx context.Context, userID, formID int64, inv *tg.InputInvoiceStarGiftTransfer) (tg.PaymentsPaymentResultClass, error) {
	target, to, err := r.starGiftPaidTransferTarget(ctx, userID, inv)
	if err != nil {
		return nil, err
	}
	wantFormID := starGiftLifecycleFormID("transfer", userID,
		target.ID, target.Owner.Type, target.Owner.ID, to.Type, to.ID, target.TransferStars, target.CanTransferAt)
	if formID == 0 || formID != wantFormID {
		return nil, starsFormAmountMismatchErr()
	}
	if r.deps.Stars != nil {
		if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
			return nil, starsErr(err)
		}
	}
	result, err := r.deps.Gifts.Transfer(ctx, domain.StarGiftTransferRequest{ActorUserID: userID,
		Ref: domain.SavedStarGiftRef{Owner: target.Owner, MsgID: target.MsgID, SavedID: target.SavedID}, To: to,
		ChargeStars: target.TransferStars, FormID: formID, CommandKey: fmt.Sprintf("paid-transfer:%d:%d", target.ID, formID),
		Date: int(r.clock.Now().Unix()), OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionIDOrZero(ctx)})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	r.invalidateStarGiftOwner(target.Owner)
	r.invalidateStarGiftOwner(to)
	return &tg.PaymentsPaymentResult{Updates: r.starGiftTransferUpdates(ctx, userID, result, false)}, nil
}

func (r *Router) starGiftPaidTransferTarget(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftTransfer) (domain.SavedStarGift, domain.Peer, error) {
	if inv == nil || r.deps.Gifts == nil {
		return domain.SavedStarGift{}, domain.Peer{}, starGiftInvalidErr()
	}
	ref, ok, err := r.starGiftRefFromInput(ctx, userID, inv.Stargift)
	if err != nil || !ok {
		return domain.SavedStarGift{}, domain.Peer{}, starGiftInvalidErr()
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, ref.Owner); err != nil {
		return domain.SavedStarGift{}, domain.Peer{}, err
	}
	to, err := r.checkedDomainPeerFromInputPeer(ctx, userID, inv.ToID)
	if err != nil {
		return domain.SavedStarGift{}, domain.Peer{}, err
	}
	saved, found, err := r.deps.Gifts.GetSaved(ctx, ref)
	if err != nil {
		return domain.SavedStarGift{}, domain.Peer{}, internalErr()
	}
	if !found || saved.UniqueGiftID == 0 || !saved.LifecycleStatus.Live() || saved.TransferStars <= 0 || saved.Owner == to {
		return domain.SavedStarGift{}, domain.Peer{}, starGiftInvalidErr()
	}
	return saved, to, nil
}

func (r *Router) starGiftResalePaymentForm(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftResale) (tg.PaymentsPaymentFormClass, error) {
	gift, to, amount, err := r.starGiftResaleTarget(ctx, userID, inv)
	if err != nil {
		return nil, err
	}
	currency := string(amount.Currency)
	return &tg.PaymentsPaymentFormStarGift{FormID: starGiftLifecycleFormID("resale", userID,
		gift.ID, gift.Owner.Type, gift.Owner.ID, to.Type, to.ID, amount.Currency, amount.Amount, gift.ResellVersion),
		Invoice: tg.Invoice{Currency: currency, Prices: []tg.LabeledPrice{{Label: "Collectible gift resale", Amount: amount.Amount}}}}, nil
}

func (r *Router) sendStarGiftResaleForm(ctx context.Context, userID, formID int64, inv *tg.InputInvoiceStarGiftResale) (tg.PaymentsPaymentResultClass, error) {
	gift, to, amount, err := r.starGiftResaleTarget(ctx, userID, inv)
	if err != nil {
		return nil, err
	}
	wantFormID := starGiftLifecycleFormID("resale", userID,
		gift.ID, gift.Owner.Type, gift.Owner.ID, to.Type, to.ID, amount.Currency, amount.Amount, gift.ResellVersion)
	if formID == 0 || formID != wantFormID {
		return nil, starsFormAmountMismatchErr()
	}
	if amount.Currency == domain.StarGiftCurrencyTON {
		if _, err := r.deps.Gifts.TonBalance(ctx, userID); err != nil {
			return nil, internalErr()
		}
	} else if r.deps.Stars != nil {
		if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
			return nil, starsErr(err)
		}
	}
	result, err := r.deps.Gifts.PurchaseResale(ctx, domain.StarGiftResalePurchaseRequest{BuyerUserID: userID,
		Slug: gift.Slug, To: to, Amount: amount, FormID: formID, CommandKey: fmt.Sprintf("resale:%d:%d", gift.ID, formID),
		Date: int(r.clock.Now().Unix()), OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionIDOrZero(ctx)})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	r.invalidateStarGiftOwner(gift.Owner)
	r.invalidateStarGiftOwner(to)
	return &tg.PaymentsPaymentResult{Updates: r.starGiftTransferUpdates(ctx, userID, result, amount.Currency == domain.StarGiftCurrencyTON)}, nil
}

func (r *Router) starGiftResaleTarget(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftResale) (domain.UniqueStarGift, domain.Peer, domain.StarGiftAmount, error) {
	if inv == nil || r.deps.Gifts == nil || strings.TrimSpace(inv.Slug) == "" {
		return domain.UniqueStarGift{}, domain.Peer{}, domain.StarGiftAmount{}, starGiftInvalidErr()
	}
	gift, found, err := r.deps.Gifts.UniqueBySlug(ctx, inv.Slug)
	if err != nil {
		return domain.UniqueStarGift{}, domain.Peer{}, domain.StarGiftAmount{}, internalErr()
	}
	if !found || gift.ResellAmount == nil || gift.Burned || gift.OwnerAddress != "" {
		return domain.UniqueStarGift{}, domain.Peer{}, domain.StarGiftAmount{}, starGiftInvalidErr()
	}
	to, err := r.checkedDomainPeerFromInputPeer(ctx, userID, inv.ToID)
	if err != nil {
		return domain.UniqueStarGift{}, domain.Peer{}, domain.StarGiftAmount{}, err
	}
	wantCurrency := domain.StarGiftCurrencyStars
	if inv.Ton {
		wantCurrency = domain.StarGiftCurrencyTON
	}
	if gift.ResellAmount.Currency != wantCurrency || gift.Owner == to {
		return domain.UniqueStarGift{}, domain.Peer{}, domain.StarGiftAmount{}, starGiftInvalidErr()
	}
	return gift, to, *gift.ResellAmount, nil
}

func (r *Router) starGiftAuctionBidPaymentForm(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftAuctionBid) (tg.PaymentsPaymentFormClass, error) {
	state, peer, delta, err := r.starGiftAuctionBidTarget(ctx, userID, inv)
	if err != nil {
		return nil, err
	}
	return &tg.PaymentsPaymentFormStars{FormID: starGiftLifecycleFormID("auction", userID,
		state.Gift.ID, peer.Type, peer.ID, inv.BidAmount, state.Version),
		BotID: domain.OfficialSystemUserID, Title: state.Gift.Title, Description: "Collectible gift auction bid",
		Invoice: tg.Invoice{Currency: "XTR", Prices: []tg.LabeledPrice{{Label: "Auction bid", Amount: delta}}},
		Users:   tgUsersForViewer(userID, []domain.User{domain.OfficialSystemUser()})}, nil
}

func (r *Router) sendStarGiftAuctionBidForm(ctx context.Context, userID, formID int64, inv *tg.InputInvoiceStarGiftAuctionBid) (tg.PaymentsPaymentResultClass, error) {
	state, peer, _, err := r.starGiftAuctionBidTarget(ctx, userID, inv)
	if err != nil {
		return nil, err
	}
	wantFormID := starGiftLifecycleFormID("auction", userID,
		state.Gift.ID, peer.Type, peer.ID, inv.BidAmount, state.Version)
	if formID == 0 || formID != wantFormID {
		return nil, starsFormAmountMismatchErr()
	}
	if r.deps.Stars != nil {
		if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
			return nil, starsErr(err)
		}
	}
	message := ""
	if text, ok := inv.GetMessage(); ok {
		message = clampGiftMessage(text.Text)
	}
	newState, balance, err := r.deps.Gifts.BidAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: userID,
		GiftID: inv.GiftID, Peer: peer, BidAmount: inv.BidAmount, HideName: inv.HideName, Message: message,
		UpdateBid: inv.UpdateBid, FormID: formID, Date: int(r.clock.Now().Unix())})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	updates := emptyGiftUpdates(r.clock.Now().Unix())
	updates.Updates = append(updates.Updates,
		&tg.UpdateStarGiftAuctionState{GiftID: inv.GiftID, State: tgStarGiftAuctionState(newState)},
		&tg.UpdateStarGiftAuctionUserState{GiftID: inv.GiftID, UserState: tgStarGiftAuctionUserState(newState.UserState)})
	appendStarGiftBalanceUpdate(updates, domain.StarGiftCurrencyStars, balance.Balance)
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

func (r *Router) starGiftAuctionBidTarget(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftAuctionBid) (domain.StarGiftAuction, domain.Peer, int64, error) {
	if inv == nil || r.deps.Gifts == nil || inv.GiftID <= 0 || inv.BidAmount <= 0 {
		return domain.StarGiftAuction{}, domain.Peer{}, 0, starGiftInvalidErr()
	}
	state, err := r.deps.Gifts.AuctionState(ctx, userID, inv.GiftID, "", int(r.clock.Now().Unix()))
	if err != nil {
		return domain.StarGiftAuction{}, domain.Peer{}, 0, starGiftLifecycleErr(err)
	}
	oldAmount := state.UserState.BidAmount
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
	if inv.UpdateBid {
		if oldAmount <= 0 || inv.HideName {
			return domain.StarGiftAuction{}, domain.Peer{}, 0, starGiftInvalidErr()
		}
		if _, ok := inv.GetPeer(); ok {
			return domain.StarGiftAuction{}, domain.Peer{}, 0, starGiftInvalidErr()
		}
		if _, ok := inv.GetMessage(); ok {
			return domain.StarGiftAuction{}, domain.Peer{}, 0, starGiftInvalidErr()
		}
		peer = state.UserState.BidPeer
	} else {
		if oldAmount > 0 {
			return domain.StarGiftAuction{}, domain.Peer{}, 0, starGiftInvalidErr()
		}
		if inputPeer, ok := inv.GetPeer(); ok {
			peer, err = r.checkedDomainPeerFromInputPeer(ctx, userID, inputPeer)
			if err != nil {
				return domain.StarGiftAuction{}, domain.Peer{}, 0, err
			}
		}
	}
	if peer.Type == domain.PeerTypeChannel {
		if err := r.checkStarGiftOwnerPermission(ctx, userID, peer); err != nil {
			return domain.StarGiftAuction{}, domain.Peer{}, 0, err
		}
	}
	minimum := state.MinBidAmount
	if oldAmount > 0 {
		minimum = state.UserState.MinBidAmount
	}
	if inv.BidAmount < minimum || inv.BidAmount <= oldAmount {
		return domain.StarGiftAuction{}, domain.Peer{}, 0, starGiftInvalidErr()
	}
	return state, peer, inv.BidAmount - oldAmount, nil
}

func (r *Router) starGiftPrepaidUpgradePaymentForm(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftPrepaidUpgrade) (tg.PaymentsPaymentFormClass, error) {
	owner, target, price, err := r.starGiftPrepaidUpgradeTarget(ctx, userID, inv)
	if err != nil {
		return nil, err
	}
	formID := starGiftLifecycleFormID("prepay-upgrade", userID, owner.Type, owner.ID, target.ID, inv.Hash, price)
	return &tg.PaymentsPaymentFormStarGift{FormID: formID,
		Invoice: tg.Invoice{Currency: "XTR", Prices: []tg.LabeledPrice{{Label: "Prepaid collectible gift upgrade", Amount: price}}}}, nil
}

func (r *Router) sendStarGiftPrepaidUpgradeForm(ctx context.Context, userID, formID int64, inv *tg.InputInvoiceStarGiftPrepaidUpgrade) (tg.PaymentsPaymentResultClass, error) {
	if inv == nil || r.deps.Gifts == nil || formID == 0 {
		return nil, starGiftInvalidErr()
	}
	owner, target, price, targetErr := r.starGiftPrepaidUpgradeTarget(ctx, userID, inv)
	commandKey := fmt.Sprintf("prepay-upgrade:%s:%d", strings.TrimSpace(inv.Hash), formID)
	if targetErr == nil {
		if formID != starGiftLifecycleFormID("prepay-upgrade", userID, owner.Type, owner.ID, target.ID, inv.Hash, price) {
			return nil, starsFormAmountMismatchErr()
		}
	} else {
		var err error
		owner, err = r.checkedDomainPeerFromInputPeer(ctx, userID, inv.Peer)
		if err != nil {
			return nil, err
		}
		price = 0 // accepted only by the store's exact replay path
	}
	if r.deps.Stars != nil {
		if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
			return nil, starsErr(err)
		}
	}
	result, err := r.deps.Gifts.PrepayUpgrade(ctx, domain.StarGiftPrepaidUpgradeRequest{PayerUserID: userID,
		Owner: owner, Hash: strings.TrimSpace(inv.Hash), ChargeStars: price, FormID: formID, CommandKey: commandKey,
		Date: int(r.clock.Now().Unix()), OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionIDOrZero(ctx)})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	updates := r.starGiftSendUpdates(ctx, userID, result.Send)
	appendStarGiftBalanceUpdate(updates, domain.StarGiftCurrencyStars, result.Balance.Balance)
	r.invalidateStarGiftOwner(owner)
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

func (r *Router) starGiftPrepaidUpgradeTarget(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftPrepaidUpgrade) (domain.Peer, domain.SavedStarGift, int64, error) {
	if inv == nil || r.deps.Gifts == nil || strings.TrimSpace(inv.Hash) == "" {
		return domain.Peer{}, domain.SavedStarGift{}, 0, starGiftInvalidErr()
	}
	owner, err := r.checkedDomainPeerFromInputPeer(ctx, userID, inv.Peer)
	if err != nil {
		return domain.Peer{}, domain.SavedStarGift{}, 0, err
	}
	target, price, err := r.deps.Gifts.PrepaidUpgradeTarget(ctx, owner, strings.TrimSpace(inv.Hash))
	if err != nil {
		return domain.Peer{}, domain.SavedStarGift{}, 0, starGiftLifecycleErr(err)
	}
	return owner, target, price, nil
}

func (r *Router) starGiftDropDetailsPaymentForm(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftDropOriginalDetails) (tg.PaymentsPaymentFormClass, error) {
	target, err := r.starGiftDropDetailsTarget(ctx, userID, inv)
	if err != nil {
		return nil, err
	}
	formID := starGiftLifecycleFormID("drop-details", userID, target.ID, target.UniqueGiftID, target.DropOriginalDetailsStars)
	return &tg.PaymentsPaymentFormStarGift{FormID: formID,
		Invoice: tg.Invoice{Currency: "XTR", Prices: []tg.LabeledPrice{{Label: "Remove collectible gift original details", Amount: target.DropOriginalDetailsStars}}}}, nil
}

func (r *Router) sendStarGiftDropDetailsForm(ctx context.Context, userID, formID int64, inv *tg.InputInvoiceStarGiftDropOriginalDetails) (tg.PaymentsPaymentResultClass, error) {
	if inv == nil || r.deps.Gifts == nil || formID == 0 {
		return nil, starGiftInvalidErr()
	}
	ref, ok, refErr := r.starGiftRefFromInput(ctx, userID, inv.Stargift)
	if refErr != nil || !ok {
		return nil, starGiftInvalidErr()
	}
	target, targetErr := r.starGiftDropDetailsTarget(ctx, userID, inv)
	charge := int64(0)
	if targetErr == nil {
		charge = target.DropOriginalDetailsStars
		if formID != starGiftLifecycleFormID("drop-details", userID, target.ID, target.UniqueGiftID, charge) {
			return nil, starsFormAmountMismatchErr()
		}
	}
	if r.deps.Stars != nil {
		if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
			return nil, starsErr(err)
		}
	}
	result, err := r.deps.Gifts.DropOriginalDetails(ctx, domain.StarGiftDropOriginalDetailsRequest{UserID: userID,
		Ref: ref, ChargeStars: charge, FormID: formID, CommandKey: fmt.Sprintf("drop-details:%s:%s:%d", ref.Owner.Type, starGiftRefValue(ref), formID),
		Date: int(r.clock.Now().Unix())})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	updates := emptyGiftUpdates(r.clock.Now().Unix())
	appendStarGiftBalanceUpdate(updates, domain.StarGiftCurrencyStars, result.Balance.Balance)
	r.invalidateStarGiftOwner(ref.Owner)
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

func (r *Router) starGiftDropDetailsTarget(ctx context.Context, userID int64, inv *tg.InputInvoiceStarGiftDropOriginalDetails) (domain.SavedStarGift, error) {
	if inv == nil || r.deps.Gifts == nil {
		return domain.SavedStarGift{}, starGiftInvalidErr()
	}
	ref, ok, err := r.starGiftRefFromInput(ctx, userID, inv.Stargift)
	if err != nil || !ok {
		return domain.SavedStarGift{}, starGiftInvalidErr()
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, ref.Owner); err != nil {
		return domain.SavedStarGift{}, err
	}
	target, found, err := r.deps.Gifts.GetSaved(ctx, ref)
	if err != nil {
		return domain.SavedStarGift{}, internalErr()
	}
	if !found || !target.LifecycleStatus.Live() || target.UniqueGiftID <= 0 || target.DropOriginalDetailsStars <= 0 {
		return domain.SavedStarGift{}, starGiftInvalidErr()
	}
	return target, nil
}

func starGiftLifecycleFormID(kind string, values ...any) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("telesrv:star-gift:" + kind + ":v1"))
	for _, value := range values {
		_, _ = fmt.Fprintf(h, ":%v", value)
	}
	id := int64(h.Sum64() & 0x7fffffffffffffff)
	if id == 0 {
		return 1
	}
	return id
}

func (r *Router) onPaymentsCheckCanSendGift(ctx context.Context, req *tg.PaymentsCheckCanSendGiftRequest) (tg.PaymentsCheckCanSendGiftResultClass, error) {
	if req == nil || req.GiftID <= 0 || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	gift, found, err := r.deps.Gifts.GiftByID(ctx, req.GiftID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, starGiftInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	switch {
	case gift.SoldOut || gift.Limited && gift.AvailabilityRemains <= 0:
		return &tg.PaymentsCheckCanSendGiftResultFail{Reason: tg.TextWithEntities{Text: "This gift is sold out."}}, nil
	case gift.LockedUntilDate > now:
		return &tg.PaymentsCheckCanSendGiftResultFail{Reason: tg.TextWithEntities{Text: "This gift is not available yet."}}, nil
	case gift.Auction:
		return &tg.PaymentsCheckCanSendGiftResultFail{Reason: tg.TextWithEntities{Text: "This gift is distributed through an auction."}}, nil
	default:
		return &tg.PaymentsCheckCanSendGiftResultOk{}, nil
	}
}

func (r *Router) onPaymentsGetUniqueStarGiftValueInfo(ctx context.Context, req *tg.PaymentsGetUniqueStarGiftValueInfoRequest) (*tg.PaymentsUniqueStarGiftValueInfo, error) {
	if req == nil || strings.TrimSpace(req.Slug) == "" || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	unique, found, err := r.deps.Gifts.UniqueBySlug(ctx, req.Slug)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, starGiftInvalidErr()
	}
	info, err := r.deps.Gifts.ValueInfo(ctx, unique.ID)
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	out := &tg.PaymentsUniqueStarGiftValueInfo{Currency: info.Currency, Value: info.Value,
		InitialSaleDate: info.InitialSaleDate, InitialSaleStars: info.InitialSaleStars,
		InitialSalePrice: info.InitialSalePrice}
	if info.ValueIsAverage {
		out.SetValueIsAverage(true)
	}
	if info.LastSaleDate > 0 {
		out.SetLastSaleDate(info.LastSaleDate)
		out.SetLastSalePrice(info.LastSalePrice)
	}
	if info.FloorPrice > 0 {
		out.SetFloorPrice(info.FloorPrice)
	}
	if info.AveragePrice > 0 {
		out.SetAveragePrice(info.AveragePrice)
	}
	out.SetListedCount(info.ListedCount)
	return out, nil
}

func (r *Router) onPaymentsGetResaleStarGifts(ctx context.Context, req *tg.PaymentsGetResaleStarGiftsRequest) (*tg.PaymentsResaleStarGifts, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	filter := domain.StarGiftResaleFilter{GiftID: req.GiftID, SortByPrice: req.SortByPrice, SortByNum: req.SortByNum,
		ForCraft: req.ForCraft, StarsOnly: req.StarsOnly, Offset: req.Offset, Limit: req.Limit}
	if filter.Limit <= 0 {
		filter.Limit = domain.MaxSavedStarGiftsLimit
	}
	if attributes, ok := req.GetAttributes(); ok {
		for _, attribute := range attributes {
			switch value := attribute.(type) {
			case *tg.StarGiftAttributeIDModel:
				if value != nil && value.DocumentID > 0 {
					filter.ModelIDs = append(filter.ModelIDs, value.DocumentID)
				}
			case *tg.StarGiftAttributeIDPattern:
				if value != nil && value.DocumentID > 0 {
					filter.PatternIDs = append(filter.PatternIDs, value.DocumentID)
				}
			case *tg.StarGiftAttributeIDBackdrop:
				if value != nil && value.BackdropID > 0 {
					filter.BackdropIDs = append(filter.BackdropIDs, int64(value.BackdropID))
				}
			default:
				return nil, starGiftInvalidErr()
			}
		}
	}
	page, err := r.deps.Gifts.ListResale(ctx, filter)
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	out := &tg.PaymentsResaleStarGifts{Count: page.Count, Gifts: make([]tg.StarGiftClass, 0, len(page.Gifts)),
		Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}
	userIDs, channelIDs := make([]int64, 0), make([]int64, 0)
	for _, gift := range page.Gifts {
		out.Gifts = append(out.Gifts, tgUniqueStarGift(gift))
		if gift.Owner.Type == domain.PeerTypeUser {
			userIDs = append(userIDs, gift.Owner.ID)
		} else if gift.Owner.Type == domain.PeerTypeChannel {
			channelIDs = append(channelIDs, gift.Owner.ID)
		}
	}
	if page.NextOffset != "" {
		out.SetNextOffset(page.NextOffset)
	}
	viewerID, _, _ := r.currentUserID(ctx)
	out.Users = tgUsersForViewer(viewerID, r.domainUsersForIDs(ctx, viewerID, uniqueInt64(userIDs)))
	out.Chats = r.tgChatsForChannelIDs(ctx, viewerID, uniqueInt64(channelIDs))
	if attributesHash, requested := req.GetAttributesHash(); requested {
		preview, found, previewErr := r.deps.Gifts.CollectiblePreview(ctx, req.GiftID)
		if previewErr != nil {
			return nil, internalErr()
		}
		if found {
			hash := int64(preview.Revision)
			out.SetAttributesHash(hash)
			if attributesHash != hash {
				attributes := make([]tg.StarGiftAttributeClass, 0, len(preview.Models)+len(preview.Patterns)+len(preview.Backdrops))
				for _, attribute := range preview.Models {
					attributes = append(attributes, tgStarGiftAttribute(attribute))
				}
				for _, attribute := range preview.Patterns {
					attributes = append(attributes, tgStarGiftAttribute(attribute))
				}
				for _, attribute := range preview.Backdrops {
					attributes = append(attributes, tgStarGiftAttribute(attribute))
				}
				out.SetAttributes(attributes)
			}
		}
	}
	return out, nil
}

func (r *Router) onPaymentsUpdateStarGiftPrice(ctx context.Context, req *tg.PaymentsUpdateStarGiftPriceRequest) (tg.UpdatesClass, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	ref, ok, err := r.starGiftRefFromInput(ctx, userID, req.Stargift)
	if err != nil || !ok {
		return nil, starGiftInvalidErr()
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, ref.Owner); err != nil {
		return nil, err
	}
	var amount *domain.StarGiftAmount
	switch value := req.ResellAmount.(type) {
	case *tg.StarsAmount:
		if value == nil || value.Amount < 0 || value.Nanos != 0 {
			return nil, starGiftInvalidErr()
		}
		if value.Amount > 0 {
			amount = &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: value.Amount}
		}
	case *tg.StarsTonAmount:
		if value == nil || value.Amount < 0 {
			return nil, starGiftInvalidErr()
		}
		if value.Amount > 0 {
			amount = &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: value.Amount}
		}
	default:
		return nil, starGiftInvalidErr()
	}
	if _, err := r.deps.Gifts.SetListing(ctx, domain.StarGiftListingRequest{ActorUserID: userID, Ref: ref,
		Amount: amount, Date: int(r.clock.Now().Unix())}); err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	r.invalidateStarGiftOwner(ref.Owner)
	return emptyGiftUpdates(r.clock.Now().Unix()), nil
}

func (r *Router) onPaymentsTransferStarGift(ctx context.Context, req *tg.PaymentsTransferStarGiftRequest) (tg.UpdatesClass, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	ref, ok, err := r.starGiftRefFromInput(ctx, userID, req.Stargift)
	if err != nil || !ok {
		return nil, starGiftInvalidErr()
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, ref.Owner); err != nil {
		return nil, err
	}
	to, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.ToID)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	result, err := r.deps.Gifts.Transfer(ctx, domain.StarGiftTransferRequest{ActorUserID: userID, Ref: ref, To: to,
		CommandKey: fmt.Sprintf("free:%s:%d:%s:%s:%d", ref.Owner.Type, ref.Owner.ID, starGiftRefValue(ref), to.Type, to.ID),
		Date:       now, OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionIDOrZero(ctx)})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	r.invalidateStarGiftOwner(ref.Owner)
	r.invalidateStarGiftOwner(to)
	return r.starGiftTransferUpdates(ctx, userID, result, false), nil
}

func (r *Router) onPaymentsGetStarGiftWithdrawalURL(ctx context.Context, req *tg.PaymentsGetStarGiftWithdrawalURLRequest) (*tg.PaymentsStarGiftWithdrawalURL, error) {
	if req == nil || r.deps.Gifts == nil || r.deps.Account == nil {
		return nil, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := r.deps.Account.CheckPassword(ctx, userID, domainPasswordCheck(req.Password)); err != nil {
		return nil, passwordErr(err)
	}
	ref, ok, err := r.starGiftRefFromInput(ctx, userID, req.Stargift)
	if err != nil || !ok || ref.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
		return nil, starGiftInvalidErr()
	}
	withdrawal, err := r.deps.Gifts.Withdraw(ctx, domain.StarGiftWithdrawalRequest{UserID: userID, Ref: ref, Date: int(r.clock.Now().Unix())})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	return &tg.PaymentsStarGiftWithdrawalURL{URL: withdrawal.URL}, nil
}

func (r *Router) onPaymentsSendStarGiftOffer(ctx context.Context, req *tg.PaymentsSendStarGiftOfferRequest) (tg.UpdatesClass, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	owner, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	price, ok := domainStarGiftAmount(req.Price)
	if !ok {
		return nil, starGiftInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	result, err := r.deps.Gifts.SendOffer(ctx, domain.StarGiftOfferRequest{BuyerUserID: userID, Owner: owner,
		Slug: req.Slug, Price: price, Duration: req.Duration, RandomID: req.RandomID, Date: now,
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionIDOrZero(ctx)})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	updates := r.starGiftSendUpdates(ctx, userID, result.Send)
	appendStarGiftBalanceUpdate(updates, price.Currency, result.Balance.Balance)
	return updates, nil
}

func (r *Router) onPaymentsResolveStarGiftOffer(ctx context.Context, req *tg.PaymentsResolveStarGiftOfferRequest) (tg.UpdatesClass, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	now := int(r.clock.Now().Unix())
	result, err := r.deps.Gifts.ResolveOffer(ctx, domain.StarGiftResolveOfferRequest{OwnerUserID: userID,
		OfferMsgID: req.OfferMsgID, Decline: req.Decline, Date: now, OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionIDOrZero(ctx)})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	updates := r.starGiftSendUpdates(ctx, userID, result.Send)
	if !req.Decline {
		if result.Offer.Price.Currency == domain.StarGiftCurrencyTON {
			balance, _ := r.deps.Gifts.TonBalance(ctx, userID)
			appendStarGiftBalanceUpdate(updates, domain.StarGiftCurrencyTON, balance)
		} else if r.deps.Stars != nil {
			balance, balanceErr := r.deps.Stars.GetBalance(ctx, userID)
			if balanceErr == nil {
				appendStarGiftBalanceUpdate(updates, domain.StarGiftCurrencyStars, balance.Balance)
			}
		}
	}
	return updates, nil
}

func (r *Router) onPaymentsGetCraftStarGifts(ctx context.Context, req *tg.PaymentsGetCraftStarGiftsRequest) (*tg.PaymentsSavedStarGifts, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	page, err := r.deps.Gifts.ListCraft(ctx, userID, req.GiftID, req.Offset, req.Limit)
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	return r.tgSavedStarGiftsResponse(ctx, userID, page.Gifts, page.Count, page.NextOffset)
}

func (r *Router) onPaymentsCraftStarGift(ctx context.Context, req *tg.PaymentsCraftStarGiftRequest) (tg.UpdatesClass, error) {
	if req == nil || r.deps.Gifts == nil || len(req.Stargift) < 1 || len(req.Stargift) > 4 {
		return nil, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	refs := make([]domain.SavedStarGiftRef, 0, len(req.Stargift))
	commandParts := make([]string, 0, len(req.Stargift))
	seenSavedIDs := make(map[int64]struct{}, len(req.Stargift))
	for _, input := range req.Stargift {
		ref, ok, err := r.starGiftRefFromInput(ctx, userID, input)
		if err != nil || !ok || ref.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
			return nil, starGiftInvalidErr()
		}
		saved, found, err := r.deps.Gifts.GetSaved(ctx, ref)
		if err != nil {
			return nil, internalErr()
		}
		if !found || saved.ID <= 0 || saved.Owner != ref.Owner {
			return nil, starGiftInvalidErr()
		}
		if _, duplicate := seenSavedIDs[saved.ID]; duplicate {
			return nil, starGiftInvalidErr()
		}
		seenSavedIDs[saved.ID] = struct{}{}
		refs = append(refs, ref)
		// Official wire identities (user msg id, channel saved id or collectible
		// slug) identify one durable aggregate and therefore one idempotency key.
		commandParts = append(commandParts, fmt.Sprint(saved.ID))
	}
	result, err := r.deps.Gifts.Craft(ctx, domain.StarGiftCraftRequest{UserID: userID, Refs: refs,
		CommandKey: "rpc:" + strings.Join(commandParts, ","), Date: int(r.clock.Now().Unix()),
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionIDOrZero(ctx)})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	updates := emptyGiftUpdates(r.clock.Now().Unix())
	if result.Success {
		updates = r.starGiftSendUpdates(ctx, userID, result.Send)
	}
	sourceUpdates := make([]tg.UpdateClass, 0, len(result.SourceEdits))
	for _, edit := range result.SourceEdits {
		if edit.UserID != userID {
			continue
		}
		if update := tgOtherUpdateFromEvent(edit.Event); update != nil {
			sourceUpdates = append(sourceUpdates, update)
			updates.Users = append(updates.Users, r.usersForMessageUpdate(ctx, userID, edit.Message)...)
			updates.Chats = append(updates.Chats, r.chatsForMessageUpdate(ctx, userID, edit.Message)...)
			if edit.Event.Date > updates.Date {
				updates.Date = edit.Event.Date
			}
		}
	}
	// Craft source edits reserve pts before the crafted output message, so keep
	// them first in the immediate response as well. Other sessions receive the
	// same durable edit events through outbox/difference.
	updates.Updates = append(sourceUpdates, updates.Updates...)
	if !result.Success {
		updates.Updates = append(updates.Updates, &tg.UpdateStarGiftCraftFail{})
	}
	return updates, nil
}

func (r *Router) onPaymentsGetStarGiftAuctionState(ctx context.Context, req *tg.PaymentsGetStarGiftAuctionStateRequest) (*tg.PaymentsStarGiftAuctionState, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	var giftID int64
	var slug string
	switch value := req.Auction.(type) {
	case *tg.InputStarGiftAuction:
		if value != nil {
			giftID = value.GiftID
		}
	case *tg.InputStarGiftAuctionSlug:
		if value != nil {
			slug = value.Slug
		}
	default:
		return nil, starGiftInvalidErr()
	}
	state, err := r.deps.Gifts.AuctionState(ctx, userID, giftID, slug, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	stateClass := tgStarGiftAuctionState(state)
	if !state.Finished && req.Version == state.Version {
		stateClass = &tg.StarGiftAuctionStateNotModified{}
	}
	return &tg.PaymentsStarGiftAuctionState{Gift: tgStarGift(state.Gift), State: stateClass,
		UserState: tgStarGiftAuctionUserState(state.UserState), Timeout: 30, Users: r.auctionUsers(ctx, userID, state), Chats: []tg.ChatClass{}}, nil
}

func (r *Router) onPaymentsGetStarGiftActiveAuctions(ctx context.Context, req *tg.PaymentsGetStarGiftActiveAuctionsRequest) (tg.PaymentsStarGiftActiveAuctionsClass, error) {
	if req == nil {
		return nil, starGiftInvalidErr()
	}
	if r.deps.Gifts == nil {
		return &tg.PaymentsStarGiftActiveAuctions{Auctions: []tg.StarGiftActiveAuctionState{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	states, err := r.deps.Gifts.ActiveAuctions(ctx, userID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	out := &tg.PaymentsStarGiftActiveAuctions{Auctions: make([]tg.StarGiftActiveAuctionState, 0, len(states)), Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}
	userIDs := make([]int64, 0)
	for _, state := range states {
		out.Auctions = append(out.Auctions, tg.StarGiftActiveAuctionState{Gift: tgStarGift(state.Gift),
			State: tgStarGiftAuctionState(state), UserState: tgStarGiftAuctionUserState(state.UserState)})
		userIDs = append(userIDs, state.TopBidders...)
	}
	out.Users = tgUsersForViewer(userID, r.domainUsersForIDs(ctx, userID, uniqueInt64(userIDs)))
	return out, nil
}

func (r *Router) onPaymentsGetStarGiftAuctionAcquiredGifts(ctx context.Context, req *tg.PaymentsGetStarGiftAuctionAcquiredGiftsRequest) (*tg.PaymentsStarGiftAuctionAcquiredGifts, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	items, err := r.deps.Gifts.AuctionAcquired(ctx, userID, req.GiftID)
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	out := &tg.PaymentsStarGiftAuctionAcquiredGifts{Gifts: make([]tg.StarGiftAuctionAcquiredGift, 0, len(items)),
		Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}
	userIDs, channelIDs := make([]int64, 0), make([]int64, 0)
	for _, item := range items {
		gift := tg.StarGiftAuctionAcquiredGift{NameHidden: item.NameHidden, Peer: tgPeer(item.Peer), Date: item.Date,
			BidAmount: item.BidAmount, Round: item.Round, Pos: item.Pos}
		if item.Message != "" {
			gift.SetMessage(tg.TextWithEntities{Text: item.Message})
		}
		if item.GiftNum > 0 {
			gift.SetGiftNum(item.GiftNum)
		}
		out.Gifts = append(out.Gifts, gift)
		if item.Peer.Type == domain.PeerTypeUser {
			userIDs = append(userIDs, item.Peer.ID)
		} else {
			channelIDs = append(channelIDs, item.Peer.ID)
		}
	}
	out.Users = tgUsersForViewer(userID, r.domainUsersForIDs(ctx, userID, uniqueInt64(userIDs)))
	out.Chats = r.tgChatsForChannelIDs(ctx, userID, uniqueInt64(channelIDs))
	return out, nil
}

func (r *Router) onPaymentsToggleChatStarGiftNotifications(ctx context.Context, req *tg.PaymentsToggleChatStarGiftNotificationsRequest) (bool, error) {
	if req == nil || r.deps.Gifts == nil || r.deps.Channels == nil {
		return false, starGiftInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil || peer.Type != domain.PeerTypeChannel {
		return false, peerIDInvalidErr()
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, peer); err != nil {
		return false, err
	}
	if err := r.deps.Gifts.SetNotifications(ctx, userID, peer.ID, req.Enabled); err != nil {
		return false, starGiftLifecycleErr(err)
	}
	return true, nil
}

func tgStarGiftAuctionState(state domain.StarGiftAuction) tg.StarGiftAuctionStateClass {
	if state.Finished {
		out := &tg.StarGiftAuctionStateFinished{StartDate: state.StartDate, EndDate: state.EndDate, AveragePrice: state.AveragePrice}
		if state.ListedCount > 0 {
			out.SetListedCount(state.ListedCount)
		}
		return out
	}
	levels := make([]tg.AuctionBidLevel, 0, len(state.BidLevels))
	for _, level := range state.BidLevels {
		levels = append(levels, tg.AuctionBidLevel{Pos: level.Pos, Amount: level.Amount, Date: level.Date})
	}
	return &tg.StarGiftAuctionState{Version: state.Version, StartDate: state.StartDate, EndDate: state.EndDate,
		MinBidAmount: state.MinBidAmount, BidLevels: levels, TopBidders: state.TopBidders,
		NextRoundAt: state.NextRoundAt, LastGiftNum: state.LastGiftNum, GiftsLeft: state.GiftsLeft,
		CurrentRound: state.CurrentRound, TotalRounds: state.TotalRounds,
		Rounds: []tg.StarGiftAuctionRoundClass{&tg.StarGiftAuctionRound{Num: 1, Duration: state.RoundDuration}}}
}

func tgStarGiftAuctionUserState(state domain.StarGiftAuctionUserState) tg.StarGiftAuctionUserState {
	out := tg.StarGiftAuctionUserState{AcquiredCount: state.AcquiredCount}
	if state.Returned {
		out.SetReturned(true)
	}
	if state.BidAmount > 0 {
		out.SetBidAmount(state.BidAmount)
		out.SetBidDate(state.BidDate)
		out.SetMinBidAmount(state.MinBidAmount)
		out.SetBidPeer(tgPeer(state.BidPeer))
	}
	return out
}

func (r *Router) auctionUsers(ctx context.Context, viewerID int64, state domain.StarGiftAuction) []tg.UserClass {
	ids := append([]int64(nil), state.TopBidders...)
	if state.UserState.BidPeer.Type == domain.PeerTypeUser {
		ids = append(ids, state.UserState.BidPeer.ID)
	}
	return tgUsersForViewer(viewerID, r.domainUsersForIDs(ctx, viewerID, uniqueInt64(ids)))
}

func (r *Router) starGiftSendUpdates(ctx context.Context, viewerID int64, send domain.SendPrivateTextResult) *tg.Updates {
	message, event := send.SenderMessage, send.SenderEvent
	if send.RecipientMessage.OwnerUserID == viewerID {
		message, event = send.RecipientMessage, send.RecipientEvent
	} else if send.SenderMessage.OwnerUserID != viewerID {
		return emptyGiftUpdates(r.clock.Now().Unix())
	}
	if message.ID <= 0 {
		return emptyGiftUpdates(r.clock.Now().Unix())
	}
	users := r.usersForMessageUpdate(ctx, viewerID, message)
	chats := r.chatsForMessageUpdate(ctx, viewerID, message)
	return tgPrivateMessageUpdates(event, message, 0, false, users, chats)
}

func (r *Router) starGiftTransferUpdates(ctx context.Context, viewerID int64, result domain.StarGiftTransferResult, ton bool) *tg.Updates {
	updates := r.starGiftSendUpdates(ctx, viewerID, result.Send)
	currency := domain.StarGiftCurrencyStars
	if ton {
		currency = domain.StarGiftCurrencyTON
	}
	appendStarGiftBalanceUpdate(updates, currency, result.Balance.Balance)
	return updates
}

func appendStarGiftBalanceUpdate(updates *tg.Updates, currency domain.StarGiftCurrency, balance int64) {
	if updates == nil {
		return
	}
	var amount tg.StarsAmountClass = &tg.StarsAmount{Amount: balance}
	if currency == domain.StarGiftCurrencyTON {
		amount = &tg.StarsTonAmount{Amount: balance}
	}
	updates.Updates = append(updates.Updates, &tg.UpdateStarsBalance{Balance: amount})
}

func emptyGiftUpdates(date int64) *tg.Updates {
	return &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: int(date)}
}

func (r *Router) checkStarGiftOwnerPermission(ctx context.Context, userID int64, owner domain.Peer) error {
	if owner == (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
		return nil
	}
	if owner.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return peerIDInvalidErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, owner.ID)
	if err != nil {
		return channelInvalidErr(err)
	}
	if view.Self.Role == domain.ChannelRoleCreator || view.Self.Role == domain.ChannelRoleAdmin && view.Self.AdminRights.PostMessages {
		return nil
	}
	return tgerr.New(400, "CHAT_ADMIN_REQUIRED")
}

func (r *Router) invalidateStarGiftOwner(owner domain.Peer) {
	if owner.Type == domain.PeerTypeUser {
		r.invalidateRPCProjectionForUser(owner.ID)
	} else if owner.Type == domain.PeerTypeChannel {
		r.invalidateRPCProjectionForChannel(owner.ID)
	}
}

func starGiftRefValue(ref domain.SavedStarGiftRef) string {
	if ref.Slug != "" {
		return "slug:" + strings.ToLower(strings.TrimSpace(ref.Slug))
	}
	if ref.Owner.Type == domain.PeerTypeChannel {
		return fmt.Sprintf("saved:%d", ref.SavedID)
	}
	return fmt.Sprintf("msg:%d", ref.MsgID)
}

func uniqueInt64(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func starGiftLifecycleErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStarGiftFormExpired):
		return formExpiredErr()
	case errors.Is(err, domain.ErrStarGiftFormPurposeInvalid):
		return purposeInvalidErr()
	case errors.Is(err, domain.ErrStarGiftFormAmountMismatch):
		return starsFormAmountMismatchErr()
	case errors.Is(err, domain.ErrStarsInsufficient):
		return tgerr.New(400, "BALANCE_TOO_LOW")
	case errors.Is(err, domain.ErrPremiumRequired):
		return tgerr.New(400, "PREMIUM_ACCOUNT_REQUIRED")
	case errors.Is(err, domain.ErrStarGiftOfferExpired):
		return tgerr.New(400, "STARGIFT_OFFER_EXPIRED")
	case errors.Is(err, domain.ErrStarGiftOwnerInvalid):
		return tgerr.New(400, "STARGIFT_OWNER_INVALID")
	case errors.Is(err, domain.ErrStarGiftWithdrawalUnavailable):
		return tgerr.New(400, "STARGIFT_WITHDRAWAL_UNAVAILABLE")
	case errors.Is(err, domain.ErrStarGiftNotFound), errors.Is(err, domain.ErrStarGiftResaleUnavailable),
		errors.Is(err, domain.ErrStarGiftTransferUnavailable), errors.Is(err, domain.ErrStarGiftOfferInvalid),
		errors.Is(err, domain.ErrStarGiftCraftUnavailable), errors.Is(err, domain.ErrStarGiftAuctionUnavailable),
		errors.Is(err, domain.ErrStarGiftUnavailable), errors.Is(err, domain.ErrStarGiftInvalid),
		errors.Is(err, domain.ErrStarGiftCollectibleUnavailable):
		return starGiftInvalidErr()
	default:
		return internalErr()
	}
}
