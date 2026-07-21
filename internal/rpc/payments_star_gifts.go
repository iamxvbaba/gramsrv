package rpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
)

// Star gift（payments.* 礼物 RPC）：目录 / 购买表单 / 发送 / 收礼列表 / 展示切换 / 转换回 Stars。
// 扣费经 r.deps.Stars 账本；用户礼物走私聊服务消息，频道礼物只落 saved gifts + admin log。

func starGiftInvalidErr() error { return tgerr.New(400, "STARGIFT_INVALID") }

func devStarsTopupOptions() []tg.StarsTopupOption {
	return []tg.StarsTopupOption{
		{Stars: 1000, Currency: "USD", Amount: 99},
		{Stars: 2500, Currency: "USD", Amount: 199},
		{Stars: 5000, Currency: "USD", Amount: 399},
	}
}

// onPaymentsGetStarGifts 返回可购买礼物目录（hash 命中返回 NotModified）。
func (r *Router) onPaymentsGetStarGifts(ctx context.Context, hash int) (tg.PaymentsStarGiftsClass, error) {
	if r.deps.Gifts == nil {
		return &tg.PaymentsStarGifts{Gifts: []tg.StarGiftClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	catalogHash, err := r.deps.Gifts.CatalogHash(ctx)
	if err != nil {
		return nil, internalErr()
	}
	catalog, err := r.deps.Gifts.Catalog(ctx)
	if err != nil {
		return nil, internalErr()
	}
	// 刻意不返回 starGiftsNotModified：目录只有少量静态礼物，而 DrKLO 在 force-stop/重装后
	// 会保留 catalog hash 但丢失礼物缓存——一旦命中 hash 返回 NotModified，送礼选择器就永远空。
	// 始终回完整目录（带宽可忽略），保证客户端无论缓存状态都能渲染礼物网格。
	_ = catalogHash
	return &tg.PaymentsStarGifts{
		Hash:  catalogHash,
		Gifts: tgStarGifts(catalog),
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{},
	}, nil
}

// onPaymentsGetPaymentForm 处理 Stars 专用 invoice：
//   - inputInvoiceStarGift 返回 paymentFormStarGift。
//   - inputInvoiceStars(inputStorePaymentStarsTopup) 返回 paymentFormStars。
//
// 崩溃约束：star gift invoice 必须返 paymentFormStarGift#b425cfe1（TDesktop 单分支 match），
// Stars 表单 Invoice.Prices 必须非空且 Currency=XTR（DrKLO/TDesktop 读 prices.front()）。
func (r *Router) onPaymentsGetPaymentForm(ctx context.Context, req *tg.PaymentsGetPaymentFormRequest) (tg.PaymentsPaymentFormClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}

	if inv, ok := req.Invoice.(*tg.InputInvoiceStars); ok {
		purpose, ok := starsTopupPurpose(inv)
		if !ok {
			return nil, notImplementedErr()
		}
		if r.deps.Stars == nil {
			return nil, notImplementedErr()
		}
		if _, _, err := r.validateStarsTopupPurpose(ctx, userID, purpose); err != nil {
			return nil, err
		}
		return r.starsTopupPaymentForm(userID, purpose), nil
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftUpgrade); ok {
		return r.starGiftUpgradePaymentForm(ctx, userID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftTransfer); ok {
		return r.starGiftTransferPaymentForm(ctx, userID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftResale); ok {
		return r.starGiftResalePaymentForm(ctx, userID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftAuctionBid); ok {
		return r.starGiftAuctionBidPaymentForm(ctx, userID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftPrepaidUpgrade); ok {
		return r.starGiftPrepaidUpgradePaymentForm(ctx, userID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftDropOriginalDetails); ok {
		return r.starGiftDropDetailsPaymentForm(ctx, userID, inv)
	}

	inv, ok := req.Invoice.(*tg.InputInvoiceStarGift)
	if !ok {
		return nil, notImplementedErr()
	}
	if r.deps.Gifts == nil {
		return nil, notImplementedErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, inv.Peer)
	if err != nil {
		return nil, err
	}
	gift, err := r.starGiftFromCatalog(ctx, inv.GiftID)
	if err != nil {
		return nil, err
	}
	if gift.RequirePremium && !r.viewerPremium(ctx, userID) {
		return nil, tgerr400("PREMIUM_ACCOUNT_REQUIRED")
	}
	upgradeStars := int64(0)
	if inv.IncludeUpgrade {
		if gift.UpgradeStars <= 0 || gift.UpgradeIssued >= gift.UpgradeTotal {
			return nil, starGiftInvalidErr()
		}
		upgradeStars = gift.UpgradeStars
	}
	giftMessage := ""
	if m, ok := inv.GetMessage(); ok {
		giftMessage = clampGiftMessage(m.Text)
	}
	now := int(r.clock.Now().Unix())
	form, err := r.deps.Gifts.IssuePurchaseForm(ctx, domain.StarGiftPurchaseForm{
		BuyerUserID: userID, To: peer, GiftID: gift.ID, RevisionID: gift.RevisionID,
		IncludeUpgrade: inv.IncludeUpgrade, HideName: inv.HideName, Message: giftMessage,
		ChargeStars: gift.Stars + upgradeStars, IssuedAt: now, ExpiresAt: now + 600,
	})
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	return &tg.PaymentsPaymentFormStarGift{
		FormID: form.FormID,
		Invoice: tg.Invoice{
			Currency: "XTR",
			Prices:   []tg.LabeledPrice{{Label: giftPriceLabel(gift), Amount: gift.Stars + upgradeStars}},
		},
	}, nil
}

// onPaymentsSendStarsForm 处理 star gift 与 Stars topup：
//   - star gift: Debit→投递/记账，失败补偿退款。
//   - topup: 校验测试包白名单→Credit 本地账本。
//
// 返回 paymentResult{updates}（含 updateStarsBalance；用户礼物还含私聊服务消息）。
// 崩溃约束：必须返回合法 paymentResult{非空 Updates}（DrKLO 强转）。
func (r *Router) onPaymentsSendStarsForm(ctx context.Context, req *tg.PaymentsSendStarsFormRequest) (tg.PaymentsPaymentResultClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}

	if inv, ok := req.Invoice.(*tg.InputInvoiceStars); ok {
		return r.sendStarsTopupForm(ctx, userID, req.FormID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftUpgrade); ok {
		return r.sendStarGiftUpgradeForm(ctx, userID, req.FormID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftTransfer); ok {
		return r.sendStarGiftTransferForm(ctx, userID, req.FormID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftResale); ok {
		return r.sendStarGiftResaleForm(ctx, userID, req.FormID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftAuctionBid); ok {
		return r.sendStarGiftAuctionBidForm(ctx, userID, req.FormID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftPrepaidUpgrade); ok {
		return r.sendStarGiftPrepaidUpgradeForm(ctx, userID, req.FormID, inv)
	}
	if inv, ok := req.Invoice.(*tg.InputInvoiceStarGiftDropOriginalDetails); ok {
		return r.sendStarGiftDropDetailsForm(ctx, userID, req.FormID, inv)
	}

	inv, ok := req.Invoice.(*tg.InputInvoiceStarGift)
	if !ok {
		return nil, notImplementedErr()
	}
	if req.FormID == 0 {
		return nil, formIDEmptyErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, inv.Peer)
	if err != nil {
		return nil, err
	}
	if (peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel) || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	if r.deps.Stars == nil || r.deps.Gifts == nil {
		return nil, notImplementedErr()
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	gift, err := r.starGiftFromCatalog(ctx, inv.GiftID)
	if err != nil {
		return nil, err
	}
	buyerPremium := r.viewerPremium(ctx, userID)
	if gift.RequirePremium && !buyerPremium {
		return nil, tgerr400("PREMIUM_ACCOUNT_REQUIRED")
	}
	upgradeStars := int64(0)
	if inv.IncludeUpgrade {
		if gift.UpgradeStars <= 0 || gift.UpgradeIssued >= gift.UpgradeTotal {
			return nil, starGiftInvalidErr()
		}
		upgradeStars = gift.UpgradeStars
	}
	giftMessage := ""
	if m, ok := inv.GetMessage(); ok {
		giftMessage = clampGiftMessage(m.Text)
	}
	now := int(r.clock.Now().Unix())
	purchaseReq := domain.StarGiftPurchaseRequest{BuyerUserID: userID, BuyerPremium: buyerPremium, To: peer,
		GiftID: gift.ID, RevisionID: gift.RevisionID, IncludeUpgrade: inv.IncludeUpgrade, HideName: inv.HideName, Message: giftMessage,
		ChargeStars: gift.Stars + upgradeStars, FormID: req.FormID, CommandKey: fmt.Sprintf("purchase:%d", req.FormID), Date: now,
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionIDOrZero(ctx)}
	recipientBlocked := false
	if peer.Type == domain.PeerTypeUser {
		recipientBlocked, err = r.peerBlocksUser(ctx, userID, peer.ID)
		if err != nil {
			return nil, internalErr()
		}
	}
	if capability, ok := r.deps.Gifts.(interface{ AtomicPurchaseConfigured() bool }); ok && !capability.AtomicPurchaseConfigured() {
		if err := r.deps.Gifts.ValidatePurchaseForm(ctx, purchaseReq); err != nil {
			return nil, starGiftLifecycleErr(err)
		}
		if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
			return nil, starsErr(err)
		}
		return r.sendStarGiftMemoryPurchase(ctx, userID, peer, gift, inv, giftMessage, upgradeStars)
	}
	if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
		return nil, starsErr(err)
	}
	purchaseReq.RecipientBlocked = recipientBlocked
	result, err := r.deps.Gifts.Purchase(ctx, purchaseReq)
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	updates := r.starGiftSendUpdates(ctx, userID, result.Send)
	appendStarGiftBalanceUpdate(updates, domain.StarGiftCurrencyStars, result.Balance.Balance)
	r.invalidateStarGiftOwner(peer)
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

func (r *Router) sendStarGiftMemoryPurchase(ctx context.Context, userID int64, peer domain.Peer, gift domain.StarGift,
	inv *tg.InputInvoiceStarGift, giftMessage string, upgradeStars int64) (tg.PaymentsPaymentResultClass, error) {
	purchaseStars := gift.Stars + upgradeStars
	balance, err := r.deps.Stars.Debit(ctx, userID, purchaseStars, domain.StarsReasonGift, peer, "Star gift", gift.Title)
	if err != nil {
		return nil, starsErr(err)
	}
	var updates *tg.Updates
	switch peer.Type {
	case domain.PeerTypeUser:
		updates, err = r.sendStarGiftToUser(ctx, userID, peer.ID, gift, inv.HideName, giftMessage, upgradeStars)
	case domain.PeerTypeChannel:
		updates, err = r.sendStarGiftToChannel(ctx, userID, peer.ID, gift, inv.HideName, giftMessage, upgradeStars)
	default:
		err = domain.ErrStarGiftInvalid
	}
	if err != nil {
		r.refundStarGift(ctx, userID, peer, gift, purchaseStars)
		return nil, internalErr()
	}
	if updates == nil {
		updates = emptyGiftUpdates(r.clock.Now().Unix())
	}
	appendStarGiftBalanceUpdate(updates, domain.StarGiftCurrencyStars, balance.Balance)
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

func starsTopupPurpose(inv *tg.InputInvoiceStars) (*tg.InputStorePaymentStarsTopup, bool) {
	if inv == nil {
		return nil, false
	}
	purpose, ok := inv.Purpose.(*tg.InputStorePaymentStarsTopup)
	return purpose, ok && purpose != nil
}

func (r *Router) validateStarsTopupPurpose(ctx context.Context, userID int64, purpose *tg.InputStorePaymentStarsTopup) (tg.StarsTopupOption, domain.Peer, error) {
	if purpose == nil || purpose.Stars <= 0 || purpose.Amount <= 0 || purpose.Currency == "" {
		return tg.StarsTopupOption{}, domain.Peer{}, starsAmountInvalidErr()
	}
	var matched tg.StarsTopupOption
	found := false
	for _, opt := range devStarsTopupOptions() {
		if opt.Stars == purpose.Stars && opt.Currency == purpose.Currency && opt.Amount == purpose.Amount {
			matched = opt
			found = true
			break
		}
	}
	if !found {
		return tg.StarsTopupOption{}, domain.Peer{}, starsFormAmountMismatchErr()
	}
	peer := domain.Peer{}
	if purpose.SpendPurposePeer != nil {
		var err error
		peer, err = r.checkedDomainPeerFromInputPeer(ctx, userID, purpose.SpendPurposePeer)
		if err != nil {
			return tg.StarsTopupOption{}, domain.Peer{}, err
		}
	}
	return matched, peer, nil
}

func (r *Router) starsTopupPaymentForm(userID int64, purpose *tg.InputStorePaymentStarsTopup) *tg.PaymentsPaymentFormStars {
	return &tg.PaymentsPaymentFormStars{
		FormID:      starsTopupFormID(userID, purpose.Stars, purpose.Currency, purpose.Amount),
		BotID:       domain.OfficialSystemUserID,
		Title:       branding.StarsName,
		Description: "telesrv dev Stars top-up",
		Invoice: tg.Invoice{
			Currency: "XTR",
			Prices:   []tg.LabeledPrice{{Label: branding.StarsName, Amount: purpose.Stars}},
		},
		Users: tgUsersForViewer(userID, []domain.User{domain.OfficialSystemUser()}),
	}
}

func (r *Router) sendStarsTopupForm(ctx context.Context, userID, formID int64, inv *tg.InputInvoiceStars) (tg.PaymentsPaymentResultClass, error) {
	purpose, ok := starsTopupPurpose(inv)
	if !ok {
		return nil, notImplementedErr()
	}
	if formID == 0 {
		return nil, formIDEmptyErr()
	}
	if r.deps.Stars == nil {
		return nil, notImplementedErr()
	}
	_, peer, err := r.validateStarsTopupPurpose(ctx, userID, purpose)
	if err != nil {
		return nil, err
	}
	if formID != starsTopupFormID(userID, purpose.Stars, purpose.Currency, purpose.Amount) {
		return nil, starsFormAmountMismatchErr()
	}
	if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
		return nil, starsErr(err)
	}
	balance, err := r.deps.Stars.Credit(ctx, userID, purpose.Stars, domain.StarsReasonTopup, peer, "Stars top-up", "telesrv dev purchase")
	if err != nil {
		return nil, starsErr(err)
	}
	return &tg.PaymentsPaymentResult{Updates: starsBalanceUpdates(balance.Balance, r.clock.Now().Unix())}, nil
}

func (r *Router) sendStarGiftToUser(ctx context.Context, senderID, recipientID int64, gift domain.StarGift, hideName bool, message string, prepaidUpgradeStars int64) (*tg.Updates, error) {
	prepaidUpgradeHash := ""
	if prepaidUpgradeStars == 0 && gift.UpgradeStars > 0 && gift.UpgradeIssued < gift.UpgradeTotal {
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, err
		}
		prepaidUpgradeHash = base64.RawURLEncoding.EncodeToString(token[:])
	}
	// 2. 投递礼物服务消息到收礼人私聊（双盒 + 推送）。
	send, err := r.deliverStarGift(ctx, senderID, recipientID, gift, hideName, message, prepaidUpgradeStars, prepaidUpgradeHash)
	if err != nil {
		return nil, err
	}
	// 3. 记账：收礼人收到一份礼物实例（msg_id = 收礼人侧消息 id）。
	if _, err := r.deps.Gifts.RecordSavedGift(ctx, domain.SavedStarGift{
		Owner:               domain.Peer{Type: domain.PeerTypeUser, ID: recipientID},
		FromUserID:          senderID,
		GiftID:              gift.ID,
		RevisionID:          gift.RevisionID,
		MsgID:               send.RecipientMessage.ID,
		Date:                send.RecipientMessage.Date,
		NameHidden:          hideName,
		Unsaved:             false,
		ConvertStars:        gift.ConvertStars,
		PrepaidUpgradeStars: prepaidUpgradeStars,
		PrepaidUpgradeHash:  prepaidUpgradeHash,
		Message:             message,
	}); err != nil {
		return nil, err
	}
	// 收礼人 stargifts_count 变化 → 失效其 userFull 投影，资料页 Gifts 区段才会出现。
	r.invalidateRPCProjectionForUser(recipientID)

	users := r.usersForMessageUpdate(ctx, senderID, send.SenderMessage)
	chats := r.chatsForMessageUpdate(ctx, senderID, send.SenderMessage)
	return tgPrivateMessageUpdates(send.SenderEvent, send.SenderMessage, 0, false, users, chats), nil
}

func (r *Router) sendStarGiftToChannel(ctx context.Context, senderID, channelID int64, gift domain.StarGift, hideName bool, message string, prepaidUpgradeStars int64) (*tg.Updates, error) {
	now := int(r.clock.Now().Unix())
	sticker := gift.Sticker
	action := domain.ChannelMessageAction{
		Type: domain.ChannelActionStarGift,
		StarGift: &domain.MessageStarGiftAction{
			GiftID:            gift.ID,
			Stars:             gift.Stars,
			ConvertStars:      gift.ConvertStars,
			Title:             gift.Title,
			Sticker:           &sticker,
			Message:           message,
			FromUserID:        senderID,
			NameHidden:        hideName,
			Saved:             true,
			CanUpgrade:        gift.UpgradeStars > 0,
			PrepaidUpgrade:    prepaidUpgradeStars > 0,
			UpgradePriceStars: gift.UpgradeStars,
			UpgradeStars:      prepaidUpgradeStars,
		},
	}
	savedID, err := r.deps.Gifts.RecordSavedGift(ctx, domain.SavedStarGift{
		Owner:               domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		FromUserID:          senderID,
		GiftID:              gift.ID,
		RevisionID:          gift.RevisionID,
		MsgID:               0,
		SavedID:             0,
		Date:                now,
		NameHidden:          hideName,
		Unsaved:             false,
		ConvertStars:        gift.ConvertStars,
		PrepaidUpgradeStars: prepaidUpgradeStars,
		Message:             message,
	})
	if err != nil {
		return nil, err
	}
	action.StarGift.PeerChannelID = channelID
	action.StarGift.SavedID = savedID
	if err := r.deps.Channels.AppendStarGiftAdminLog(ctx, channelID, senderID, savedID, now, action); err != nil {
		r.log.Warn("channel_star_gift_admin_log_failed",
			zap.Int64("sender_id", senderID),
			zap.Int64("channel_id", channelID),
			zap.Int64("saved_id", savedID),
			zap.Error(err),
		)
	}
	r.invalidateRPCProjectionForChannel(channelID)
	return nil, nil
}

// deliverStarGift 经 SendPrivateText 把 messageActionStarGift 服务消息投递到收礼人私聊。
func (r *Router) deliverStarGift(ctx context.Context, senderID, recipientID int64, gift domain.StarGift, hideName bool, message string, prepaidUpgradeStars int64, prepaidUpgradeHash string) (domain.SendPrivateTextResult, error) {
	recipientBlocked, err := r.peerBlocksUser(ctx, senderID, recipientID)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	sessionID, _ := SessionIDFrom(ctx)
	sticker := gift.Sticker
	media := &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGift,
			StarGift: &domain.MessageStarGiftAction{
				GiftID:             gift.ID,
				Stars:              gift.Stars,
				ConvertStars:       gift.ConvertStars,
				Title:              gift.Title,
				Sticker:            &sticker,
				Message:            message,
				FromUserID:         senderID,
				PeerUserID:         recipientID,
				NameHidden:         hideName,
				Saved:              true,
				CanUpgrade:         gift.UpgradeStars > 0,
				PrepaidUpgrade:     prepaidUpgradeStars > 0,
				PrepaidUpgradeHash: prepaidUpgradeHash,
				UpgradePriceStars:  gift.UpgradeStars,
				UpgradeStars:       prepaidUpgradeStars,
			},
		},
	}
	return r.deps.Messages.SendPrivateText(ctx, senderID, domain.SendPrivateTextRequest{
		SenderUserID:     senderID,
		RecipientUserID:  recipientID,
		RandomID:         randomNonZeroInt64(),
		Media:            media,
		Silent:           false,
		Date:             int(r.clock.Now().Unix()),
		OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:  sessionID,
		RecipientBlocked: recipientBlocked,
	})
}

// refundStarGift 补偿退款（投递/记账失败时把已 Debit 的星退回）。
func (r *Router) refundStarGift(ctx context.Context, userID int64, peer domain.Peer, gift domain.StarGift, amount int64) {
	if _, err := r.deps.Stars.Credit(ctx, userID, amount, domain.StarsReasonGift, peer, "Star gift refund", gift.Title); err != nil {
		r.log.Error("star gift refund failed", zap.Int64("user_id", userID), zap.Int64("gift_id", gift.ID), zap.Error(err))
	}
}

// onPaymentsGetSavedStarGifts 返回某 peer 收到的礼物列表（末页省略 next_offset）。
func (r *Router) onPaymentsGetSavedStarGifts(ctx context.Context, req *tg.PaymentsGetSavedStarGiftsRequest) (*tg.PaymentsSavedStarGifts, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	owner, err := r.starGiftOwnerPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Gifts == nil {
		return emptySavedStarGifts(), nil
	}
	collectionID, _ := req.GetCollectionID()
	page, err := r.deps.Gifts.ListSavedFiltered(ctx, domain.SavedStarGiftFilter{
		Owner:               owner,
		ExcludeUnsaved:      req.ExcludeUnsaved,
		ExcludeSaved:        req.ExcludeSaved,
		ExcludeUnlimited:    req.ExcludeUnlimited,
		ExcludeUnique:       req.ExcludeUnique,
		ExcludeUpgradable:   req.ExcludeUpgradable,
		ExcludeUnupgradable: req.ExcludeUnupgradable,
		CollectionID:        collectionID,
		Offset:              req.Offset,
		Limit:               req.Limit,
	})
	if err != nil {
		return nil, internalErr()
	}
	response, err := r.tgSavedStarGiftsResponse(ctx, userID, page.Gifts, page.Count, page.NextOffset)
	if err != nil {
		return nil, internalErr()
	}
	return response, nil
}

// onPaymentsGetSavedStarGift 按 InputSavedStarGift 引用取指定礼物。
func (r *Router) onPaymentsGetSavedStarGift(ctx context.Context, refs []tg.InputSavedStarGiftClass) (*tg.PaymentsSavedStarGifts, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Gifts == nil {
		return emptySavedStarGifts(), nil
	}
	gifts := make([]domain.SavedStarGift, 0, len(refs))
	for _, ref := range refs {
		dref, ok, err := r.starGiftRefFromInput(ctx, userID, ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		g, found, err := r.deps.Gifts.GetSaved(ctx, dref)
		if err != nil {
			return nil, internalErr()
		}
		if found && !g.Converted {
			gifts = append(gifts, g)
		}
	}
	response, err := r.tgSavedStarGiftsResponse(ctx, userID, gifts, len(gifts), "")
	if err != nil {
		return nil, internalErr()
	}
	return response, nil
}

// onPaymentsSaveStarGift 切换礼物在资料的展示（unsave=true 隐藏）。
func (r *Router) onPaymentsSaveStarGift(ctx context.Context, req *tg.PaymentsSaveStarGiftRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Gifts == nil {
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
	updated, err := r.deps.Gifts.ToggleSaved(ctx, ref, req.Unsave)
	if err != nil {
		return false, internalErr()
	}
	if !updated {
		return false, starGiftInvalidErr()
	}
	// 隐藏/展示切换改变展示礼物数 → 失效 owner full 投影。
	r.invalidateStarGiftOwnerProjection(ref.Owner)
	return true, nil
}

// onPaymentsConvertStarGift atomically destroys the regular gift and credits
// the owner-scoped internal Stars ledger. Channel proceeds never leak to the
// acting administrator's personal balance.
func (r *Router) onPaymentsConvertStarGift(ctx context.Context, ref tg.InputSavedStarGiftClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Gifts == nil {
		return false, notImplementedErr()
	}
	dref, ok, err := r.starGiftRefFromInput(ctx, userID, ref)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, starGiftInvalidErr()
	}
	if err := r.ensureCanManageStarGiftOwner(ctx, userID, dref.Owner); err != nil {
		return false, err
	}
	// The isolated memory RPC adapter intentionally has no aggregate store. Keep
	// its conversion primitive usable for tests, but never use this split write
	// path when the production lifecycle coordinator is configured. Channel
	// balances have no memory adapter because crediting an administrator would
	// violate owner-scoped accounting.
	if converter, ok := r.deps.Gifts.(interface {
		AtomicPurchaseConfigured() bool
		Convert(context.Context, domain.SavedStarGiftRef) (domain.SavedStarGift, error)
	}); ok && !converter.AtomicPurchaseConfigured() {
		if dref.Owner.Type != domain.PeerTypeUser || dref.Owner.ID != userID {
			return false, notImplementedErr()
		}
		updated, convertErr := converter.Convert(ctx, dref)
		if convertErr != nil {
			if errors.Is(convertErr, domain.ErrStarGiftNotFound) || errors.Is(convertErr, domain.ErrStarGiftAlreadyConverted) {
				return false, starGiftInvalidErr()
			}
			return false, internalErr()
		}
		if updated.ConvertStars > 0 {
			if _, creditErr := r.deps.Stars.Credit(ctx, userID, updated.ConvertStars, domain.StarsReasonGift,
				dref.Owner, "Star gift conversion", "Converted Star Gift"); creditErr != nil {
				return false, internalErr()
			}
		}
		r.invalidateStarGiftOwnerProjection(dref.Owner)
		return true, nil
	}
	result, err := r.deps.Gifts.ConvertAggregate(ctx, domain.StarGiftConvertRequest{
		ActorUserID: userID,
		Ref:         dref,
		Date:        int(r.clock.Now().Unix()),
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrStarGiftNotFound):
			return false, starGiftInvalidErr()
		case errors.Is(err, domain.ErrStarGiftAlreadyConverted):
			return false, starGiftInvalidErr()
		default:
			return false, internalErr()
		}
	}
	// 转换移除一份展示礼物 → 失效 owner full 投影。
	r.invalidateStarGiftOwnerProjection(result.Saved.Owner)
	return true, nil
}

// ---- helpers ----

func (r *Router) starGiftFromCatalog(ctx context.Context, giftID int64) (domain.StarGift, error) {
	gift, ok, err := r.deps.Gifts.GiftByID(ctx, giftID)
	if err != nil {
		return domain.StarGift{}, internalErr()
	}
	if !ok {
		return domain.StarGift{}, starGiftInvalidErr()
	}
	return gift, nil
}

// starGiftOwnerPeer 解析 getSavedStarGifts 的 owner：inputPeerSelf/空 → 自己，否则解析 user/channel peer。
func (r *Router) starGiftOwnerPeer(ctx context.Context, userID int64, peer tg.InputPeerClass) (domain.Peer, error) {
	switch peer.(type) {
	case nil, *tg.InputPeerSelf:
		return domain.Peer{Type: domain.PeerTypeUser, ID: userID}, nil
	}
	resolved, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil {
		return domain.Peer{}, err
	}
	if (resolved.Type != domain.PeerTypeUser && resolved.Type != domain.PeerTypeChannel) || resolved.ID == 0 {
		return domain.Peer{}, peerIDInvalidErr()
	}
	return resolved, nil
}

func (r *Router) starGiftRefFromInput(ctx context.Context, userID int64, ref tg.InputSavedStarGiftClass) (domain.SavedStarGiftRef, bool, error) {
	switch v := ref.(type) {
	case *tg.InputSavedStarGiftUser:
		if v == nil || v.MsgID <= 0 {
			return domain.SavedStarGiftRef{}, false, nil
		}
		return domain.SavedStarGiftRef{
			Owner: domain.Peer{Type: domain.PeerTypeUser, ID: userID},
			MsgID: v.MsgID,
		}, true, nil
	case *tg.InputSavedStarGiftChat:
		if v == nil || v.SavedID <= 0 {
			return domain.SavedStarGiftRef{}, false, nil
		}
		owner, err := r.checkedDomainPeerFromInputPeer(ctx, userID, v.Peer)
		if err != nil {
			return domain.SavedStarGiftRef{}, false, err
		}
		if owner.Type != domain.PeerTypeChannel || owner.ID == 0 {
			return domain.SavedStarGiftRef{}, false, peerIDInvalidErr()
		}
		return domain.SavedStarGiftRef{Owner: owner, SavedID: v.SavedID}, true, nil
	case *tg.InputSavedStarGiftSlug:
		if v == nil || r.deps.Gifts == nil {
			return domain.SavedStarGiftRef{}, false, nil
		}
		slug := strings.ToLower(strings.TrimSpace(v.Slug))
		if slug == "" || len(slug) > domain.MaxStarGiftSlugBytes {
			return domain.SavedStarGiftRef{}, false, nil
		}
		unique, found, err := r.deps.Gifts.UniqueBySlug(ctx, slug)
		if err != nil {
			return domain.SavedStarGiftRef{}, false, internalErr()
		}
		if !found || unique.Slug == "" || unique.Owner.ID == 0 ||
			(unique.Owner.Type != domain.PeerTypeUser && unique.Owner.Type != domain.PeerTypeChannel) {
			return domain.SavedStarGiftRef{}, false, nil
		}
		resolved := domain.SavedStarGiftRef{Owner: unique.Owner, Slug: strings.ToLower(strings.TrimSpace(unique.Slug))}
		return resolved, resolved.Valid(), nil
	default:
		return domain.SavedStarGiftRef{}, false, nil
	}
}

func (r *Router) ensureCanManageStarGiftOwner(ctx context.Context, userID int64, owner domain.Peer) error {
	if owner.Type == domain.PeerTypeUser {
		if owner.ID != userID {
			return peerIDInvalidErr()
		}
		return nil
	}
	if owner.Type != domain.PeerTypeChannel {
		return peerIDInvalidErr()
	}
	if r.deps.Channels == nil {
		return notImplementedErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, owner.ID)
	if err != nil {
		return channelInvalidErr(err)
	}
	if !channelMemberIsAdmin(view.Self) {
		return channelInvalidErr(domain.ErrChannelAdminRequired)
	}
	return nil
}

func (r *Router) invalidateStarGiftOwnerProjection(owner domain.Peer) {
	switch owner.Type {
	case domain.PeerTypeUser:
		r.invalidateRPCProjectionForUser(owner.ID)
	case domain.PeerTypeChannel:
		r.invalidateRPCProjectionForChannel(owner.ID)
	}
}

func (r *Router) tgSavedStarGiftsResponse(ctx context.Context, viewerUserID int64, gifts []domain.SavedStarGift, count int, nextOffset string) (*tg.PaymentsSavedStarGifts, error) {
	uniqueIDs := make([]int64, 0)
	seenUnique := make(map[int64]struct{})
	for _, gift := range gifts {
		if gift.UniqueGiftID != 0 {
			if _, seen := seenUnique[gift.UniqueGiftID]; !seen {
				seenUnique[gift.UniqueGiftID] = struct{}{}
				uniqueIDs = append(uniqueIDs, gift.UniqueGiftID)
			}
		}
	}
	if len(uniqueIDs) > 0 {
		uniques, err := r.deps.Gifts.UniqueByIDs(ctx, uniqueIDs)
		if err != nil {
			return nil, err
		}
		for i := range gifts {
			if unique, ok := uniques[gifts[i].UniqueGiftID]; ok {
				copy := unique
				gifts[i].Unique = &copy
			}
		}
	}
	catalog, err := r.resolveStarGiftCatalog(ctx, gifts)
	if err != nil {
		return nil, err
	}
	availability, err := r.resolveStarGiftCollectibleAvailability(ctx, gifts)
	if err != nil {
		return nil, err
	}
	projected := tgSavedStarGifts(gifts, catalog, availability)
	out := &tg.PaymentsSavedStarGifts{
		Count: count,
		Gifts: projected,
		Chats: []tg.ChatClass{},
	}
	if ids := savedStarGiftUserIDs(gifts); len(ids) > 0 {
		out.Users = tgUsersForViewer(viewerUserID, r.domainUsersForIDs(ctx, viewerUserID, ids))
	} else {
		out.Users = []tg.UserClass{}
	}
	if nextOffset != "" {
		out.SetNextOffset(nextOffset)
	}
	return out, nil
}

func (r *Router) resolveStarGiftCollectibleAvailability(ctx context.Context, gifts []domain.SavedStarGift) (map[int64]domain.StarGiftCollectibleAvailability, error) {
	out := make(map[int64]domain.StarGiftCollectibleAvailability)
	if r.deps.Gifts == nil {
		return out, nil
	}
	ids := make([]int64, 0, len(gifts))
	seen := make(map[int64]struct{}, len(gifts))
	for _, gift := range gifts {
		if gift.UniqueGiftID != 0 {
			continue
		}
		if _, ok := seen[gift.GiftID]; ok {
			continue
		}
		seen[gift.GiftID] = struct{}{}
		ids = append(ids, gift.GiftID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	return r.deps.Gifts.CollectibleAvailability(ctx, ids)
}

func emptySavedStarGifts() *tg.PaymentsSavedStarGifts {
	return &tg.PaymentsSavedStarGifts{
		Gifts: []tg.SavedStarGift{},
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{},
	}
}

// tgStarGifts 把目录投影为 []tg.StarGiftClass。
func tgStarGifts(catalog []domain.StarGift) []tg.StarGiftClass {
	out := make([]tg.StarGiftClass, 0, len(catalog))
	for _, g := range catalog {
		out = append(out, tgStarGift(g))
	}
	return out
}

// tgStarGift 把目录项投影为 tg.StarGift（Sticker 须为带 sticker 属性的有效 Document）。
func tgStarGift(g domain.StarGift) *tg.StarGift {
	gift := &tg.StarGift{
		Limited: g.Limited, SoldOut: g.SoldOut, Birthday: g.Birthday,
		RequirePremium: g.RequirePremium, LimitedPerUser: g.LimitedPerUser,
		PeerColorAvailable: g.PeerColorAvailable, Auction: g.Auction,
		ID: g.ID, Sticker: tgDocument(g.Sticker), Stars: g.Stars, ConvertStars: g.ConvertStars,
	}
	if g.Limited {
		gift.SetAvailabilityRemains(g.AvailabilityRemains)
		gift.SetAvailabilityTotal(g.AvailabilityTotal)
	}
	if g.AvailabilityResale > 0 {
		gift.SetAvailabilityResale(g.AvailabilityResale)
	}
	// sold_out, first_sale_date and last_sale_date share TL flags.1. The store
	// retains sale timestamps for live gifts as operational facts, but exposing
	// either timestamp would make every client decode the gift as sold out.
	if g.SoldOut {
		gift.SetFirstSaleDate(g.FirstSaleDate)
		gift.SetLastSaleDate(g.LastSaleDate)
	}
	if g.Title != "" {
		gift.SetTitle(g.Title)
	}
	if g.UpgradeStars > 0 && g.UpgradeIssued < g.UpgradeTotal {
		gift.SetUpgradeStars(g.UpgradeStars)
	}
	if g.ResellMinStars > 0 {
		gift.SetResellMinStars(g.ResellMinStars)
	}
	if releasedBy := tgPeer(g.ReleasedBy); releasedBy != nil {
		gift.SetReleasedBy(releasedBy)
	}
	if g.LimitedPerUser {
		gift.SetPerUserTotal(g.PerUserTotal)
		gift.SetPerUserRemains(g.PerUserRemains)
	}
	if g.LockedUntilDate > 0 {
		gift.SetLockedUntilDate(g.LockedUntilDate)
	}
	if g.Auction {
		gift.SetAuctionSlug(g.AuctionSlug)
		gift.SetGiftsPerRound(g.GiftsPerRound)
		gift.SetAuctionStartDate(g.AuctionStartDate)
	}
	if g.UpgradeVariants > 0 {
		gift.SetUpgradeVariants(g.UpgradeVariants)
	}
	if g.Background != nil {
		gift.SetBackground(tg.StarGiftBackground{CenterColor: g.Background.CenterColor,
			EdgeColor: g.Background.EdgeColor, TextColor: g.Background.TextColor})
	}
	return gift
}

// tgMessageActionStarGift 把礼物服务消息载荷投影为 messageActionStarGift。
func tgMessageActionStarGift(in *domain.MessageStarGiftAction) tg.MessageActionClass {
	if in == nil {
		return &tg.MessageActionEmpty{}
	}
	gift := &tg.StarGift{
		ID:           in.GiftID,
		Stars:        in.Stars,
		ConvertStars: in.ConvertStars,
	}
	if in.Sticker != nil {
		gift.Sticker = tgDocument(*in.Sticker)
	} else {
		gift.Sticker = &tg.DocumentEmpty{}
	}
	if in.Title != "" {
		gift.SetTitle(in.Title)
	}
	if in.UpgradePriceStars > 0 {
		gift.SetUpgradeStars(in.UpgradePriceStars)
	}
	action := &tg.MessageActionStarGift{Gift: gift}
	if in.NameHidden {
		action.NameHidden = true
	}
	if in.Saved {
		action.Saved = true
	}
	if in.Converted {
		action.Converted = true
	}
	action.CanUpgrade = in.CanUpgrade
	action.PrepaidUpgrade = in.PrepaidUpgrade
	action.UpgradeSeparate = in.UpgradeSeparate
	action.AuctionAcquired = in.AuctionAcquired
	if in.UpgradeStars > 0 {
		action.SetUpgradeStars(in.UpgradeStars)
	}
	if in.UpgradeMsgID > 0 {
		action.SetUpgradeMsgID(in.UpgradeMsgID)
	}
	if in.PrepaidUpgradeHash != "" {
		action.SetPrepaidUpgradeHash(in.PrepaidUpgradeHash)
	}
	if in.GiftMsgID > 0 {
		action.SetGiftMsgID(in.GiftMsgID)
	}
	if in.GiftNum > 0 {
		action.SetGiftNum(in.GiftNum)
	}
	if to := tgPeer(in.To); to != nil {
		action.SetToID(to)
	}
	if in.ConvertStars > 0 {
		action.SetConvertStars(in.ConvertStars)
	}
	if in.Message != "" {
		action.SetMessage(tg.TextWithEntities{Text: in.Message})
	}
	if in.FromUserID != 0 && !in.NameHidden {
		action.SetFromID(&tg.PeerUser{UserID: in.FromUserID})
	}
	if in.PeerUserID != 0 {
		action.SetPeer(&tg.PeerUser{UserID: in.PeerUserID})
	} else if in.PeerChannelID != 0 {
		action.SetPeer(&tg.PeerChannel{ChannelID: in.PeerChannelID})
	}
	if in.SavedID != 0 {
		action.SetSavedID(in.SavedID)
	}
	return action
}

// resolveStarGiftCatalog 解析这批 saved gift 涉及的不可变目录版本（revisionID → StarGift）。
func (r *Router) resolveStarGiftCatalog(ctx context.Context, gifts []domain.SavedStarGift) (map[int64]domain.StarGift, error) {
	out := make(map[int64]domain.StarGift, len(gifts))
	if r.deps.Gifts == nil {
		return out, nil
	}
	for _, g := range gifts {
		if g.RevisionID == 0 {
			return nil, domain.ErrStarGiftInvalid
		}
		if _, ok := out[g.RevisionID]; ok {
			continue
		}
		gift, found, err := r.deps.Gifts.GiftRevisionByID(ctx, g.RevisionID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, domain.ErrStarGiftInvalid
		}
		out[g.RevisionID] = gift
	}
	return out, nil
}

// tgSavedStarGifts 把已收到礼物实例投影为 []tg.SavedStarGift。
func tgSavedStarGifts(gifts []domain.SavedStarGift, catalog map[int64]domain.StarGift, availability map[int64]domain.StarGiftCollectibleAvailability) []tg.SavedStarGift {
	out := make([]tg.SavedStarGift, 0, len(gifts))
	for _, g := range gifts {
		item := tg.SavedStarGift{
			Date: g.Date,
			Gift: tgSavedStarGiftGift(g, catalog, availability),
		}
		if g.NameHidden {
			item.NameHidden = true
		}
		if g.Unsaved {
			item.Unsaved = true
		}
		if g.FromUserID != 0 && !g.NameHidden {
			item.SetFromID(&tg.PeerUser{UserID: g.FromUserID})
		}
		if g.Owner.Type == domain.PeerTypeUser && g.MsgID > 0 {
			item.SetMsgID(g.MsgID)
		}
		if g.Owner.Type == domain.PeerTypeChannel && g.SavedID > 0 {
			item.SetSavedID(g.SavedID)
		}
		if g.ConvertStars > 0 {
			item.SetConvertStars(g.ConvertStars)
		}
		if g.Message != "" {
			item.SetMessage(tg.TextWithEntities{Text: g.Message})
		}
		if g.UniqueGiftID == 0 {
			current, available := availability[g.GiftID]
			canIssue := available && current.UpgradeStars > 0 && current.Issued < current.SupplyTotal
			if canIssue {
				item.CanUpgrade = true
			}
			if g.PrepaidUpgradeStars > 0 && canIssue {
				item.SetUpgradeStars(g.PrepaidUpgradeStars)
				item.CanUpgrade = true
			}
			if g.PrepaidUpgradeHash != "" && g.PrepaidUpgradeStars == 0 && canIssue {
				item.SetPrepaidUpgradeHash(g.PrepaidUpgradeHash)
			}
		}
		if g.CanExportAt > 0 {
			item.SetCanExportAt(g.CanExportAt)
		}
		if g.TransferStars > 0 {
			item.SetTransferStars(g.TransferStars)
		}
		if g.CanTransferAt > 0 {
			item.SetCanTransferAt(g.CanTransferAt)
		}
		if g.CanResellAt > 0 {
			item.SetCanResellAt(g.CanResellAt)
		}
		if g.DropOriginalDetailsStars > 0 {
			item.SetDropOriginalDetailsStars(g.DropOriginalDetailsStars)
		}
		// Channel Craft execution is not implemented yet. Android uses this field
		// as the entry/capability marker, so only advertise the currently
		// executable user-owned path while retaining the durable DB entitlement.
		if g.Owner.Type == domain.PeerTypeUser && g.CanCraftAt > 0 {
			item.SetCanCraftAt(g.CanCraftAt)
		}
		if g.PinnedOrder > 0 {
			item.PinnedToTop = true
		}
		if len(g.CollectionIDs) > 0 {
			item.SetCollectionID(append([]int(nil), g.CollectionIDs...))
		}
		if g.Unique != nil {
			item.SetGiftNum(g.Unique.Num)
		} else if g.GiftNum > 0 {
			item.SetGiftNum(g.GiftNum)
		}
		out = append(out, item)
	}
	return out
}

// tgSavedStarGiftGift 按收到时的不可变 revision 投影，目录停用或后续改版不影响历史显示。
func tgSavedStarGiftGift(g domain.SavedStarGift, catalog map[int64]domain.StarGift, availability map[int64]domain.StarGiftCollectibleAvailability) tg.StarGiftClass {
	if g.Unique != nil {
		return tgUniqueStarGift(*g.Unique)
	}
	if gift, ok := catalog[g.RevisionID]; ok {
		if current, ok := availability[g.GiftID]; ok {
			gift.UpgradeStars = current.UpgradeStars
			gift.UpgradeTotal = current.SupplyTotal
			gift.UpgradeIssued = current.Issued
		}
		out := tgStarGift(gift)
		out.ConvertStars = g.ConvertStars
		return out
	}
	// resolveStarGiftCatalog 在进入投影前保证每个 revision 都存在；该分支仅保留类型完备性。
	return &tg.StarGift{
		ID:           g.GiftID,
		Sticker:      &tg.DocumentEmpty{},
		Stars:        0,
		ConvertStars: g.ConvertStars,
	}
}

func savedStarGiftUserIDs(gifts []domain.SavedStarGift) []int64 {
	seen := make(map[int64]struct{}, len(gifts))
	ids := make([]int64, 0, len(gifts))
	for _, g := range gifts {
		if g.FromUserID == 0 || g.NameHidden {
			continue
		}
		if _, ok := seen[g.FromUserID]; ok {
			continue
		}
		seen[g.FromUserID] = struct{}{}
		ids = append(ids, g.FromUserID)
	}
	return ids
}

func starsTopupFormID(userID, stars int64, currency string, amount int64) int64 {
	id := userID*0x9e3779b1 ^ (stars << 7) ^ (amount << 13) ^ 0x5354415253
	for _, ch := range currency {
		id = id*131 + int64(ch)
	}
	if id < 0 {
		id = ^id
	}
	if id == 0 {
		id = 0x5354
	}
	return id
}

func starsBalanceUpdates(balance int64, unixDate int64) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance}}},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(unixDate),
	}
}

func giftPriceLabel(g domain.StarGift) string {
	if g.Title != "" {
		return g.Title
	}
	return "Gift"
}

func clampGiftMessage(s string) string {
	runes := []rune(s)
	if len(runes) > domain.MaxStarGiftMessageRunes {
		return string(runes[:domain.MaxStarGiftMessageRunes])
	}
	return s
}
