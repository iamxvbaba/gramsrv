package rpc

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap"

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

	inv, ok := req.Invoice.(*tg.InputInvoiceStarGift)
	if !ok {
		return nil, notImplementedErr()
	}
	if r.deps.Gifts == nil {
		return nil, notImplementedErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, inv.Peer); err != nil {
		return nil, err
	}
	gift, err := r.starGiftFromCatalog(ctx, inv.GiftID)
	if err != nil {
		return nil, err
	}
	return &tg.PaymentsPaymentFormStarGift{
		FormID: starGiftFormID(userID, inv.GiftID),
		Invoice: tg.Invoice{
			Currency: "XTR",
			Prices:   []tg.LabeledPrice{{Label: giftPriceLabel(gift), Amount: gift.Stars}},
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

	inv, ok := req.Invoice.(*tg.InputInvoiceStarGift)
	if !ok {
		return nil, notImplementedErr()
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
	if peer.Type == domain.PeerTypeUser && r.deps.Messages == nil {
		return nil, notImplementedErr()
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	gift, err := r.starGiftFromCatalog(ctx, inv.GiftID)
	if err != nil {
		return nil, err
	}
	giftMessage := ""
	if m, ok := inv.GetMessage(); ok {
		giftMessage = clampGiftMessage(m.Text)
	}

	// 1. Debit 送礼人（不足→BALANCE_TOO_LOW）。
	balance, err := r.deps.Stars.Debit(ctx, userID, gift.Stars, domain.StarsReasonGift, peer, "Star gift", gift.Title)
	if err != nil {
		return nil, starsErr(err)
	}

	var updates *tg.Updates
	switch peer.Type {
	case domain.PeerTypeUser:
		updates, err = r.sendStarGiftToUser(ctx, userID, peer.ID, gift, inv.HideName, giftMessage)
	case domain.PeerTypeChannel:
		updates, err = r.sendStarGiftToChannel(ctx, userID, peer.ID, gift, inv.HideName, giftMessage)
	default:
		err = domain.ErrStarGiftInvalid
	}
	if err != nil {
		r.refundStarGift(ctx, userID, peer, gift)
		return nil, internalErr()
	}

	// 4. 构建送礼人 Updates（服务消息 + updateStarsBalance）。
	if updates != nil {
		updates.Updates = append(updates.Updates, &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance.Balance}})
	} else {
		updates = &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance.Balance}}},
			Users:   []tg.UserClass{},
			Chats:   []tg.ChatClass{},
			Date:    int(r.clock.Now().Unix()),
		}
	}
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
		Title:       "Telegram Stars",
		Description: "telesrv dev Stars top-up",
		Invoice: tg.Invoice{
			Currency: "XTR",
			Prices:   []tg.LabeledPrice{{Label: "Telegram Stars", Amount: purpose.Stars}},
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

func (r *Router) sendStarGiftToUser(ctx context.Context, senderID, recipientID int64, gift domain.StarGift, hideName bool, message string) (*tg.Updates, error) {
	// 2. 投递礼物服务消息到收礼人私聊（双盒 + 推送）。
	send, err := r.deliverStarGift(ctx, senderID, recipientID, gift, hideName, message)
	if err != nil {
		return nil, err
	}
	// 3. 记账：收礼人收到一份礼物实例（msg_id = 收礼人侧消息 id）。
	if _, err := r.deps.Gifts.RecordSavedGift(ctx, domain.SavedStarGift{
		Owner:        domain.Peer{Type: domain.PeerTypeUser, ID: recipientID},
		FromUserID:   senderID,
		GiftID:       gift.ID,
		MsgID:        send.RecipientMessage.ID,
		Date:         send.RecipientMessage.Date,
		NameHidden:   hideName,
		Unsaved:      false,
		ConvertStars: gift.ConvertStars,
		Message:      message,
	}); err != nil {
		return nil, err
	}
	// 收礼人 stargifts_count 变化 → 失效其 userFull 投影，资料页 Gifts 区段才会出现。
	r.invalidateRPCProjectionForUser(recipientID)

	users := r.usersForMessageUpdate(ctx, senderID, send.SenderMessage)
	chats := r.chatsForMessageUpdate(ctx, senderID, send.SenderMessage)
	return tgPrivateMessageUpdates(send.SenderEvent, send.SenderMessage, 0, false, users, chats), nil
}

func (r *Router) sendStarGiftToChannel(ctx context.Context, senderID, channelID int64, gift domain.StarGift, hideName bool, message string) (*tg.Updates, error) {
	now := int(r.clock.Now().Unix())
	sticker := gift.Sticker
	action := domain.ChannelMessageAction{
		Type: domain.ChannelActionStarGift,
		StarGift: &domain.MessageStarGiftAction{
			GiftID:       gift.ID,
			Stars:        gift.Stars,
			ConvertStars: gift.ConvertStars,
			Title:        gift.Title,
			Sticker:      &sticker,
			Message:      message,
			FromUserID:   senderID,
			NameHidden:   hideName,
			Saved:        true,
		},
	}
	savedID, err := r.deps.Gifts.RecordSavedGift(ctx, domain.SavedStarGift{
		Owner:        domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		FromUserID:   senderID,
		GiftID:       gift.ID,
		MsgID:        0,
		SavedID:      0,
		Date:         now,
		NameHidden:   hideName,
		Unsaved:      false,
		ConvertStars: gift.ConvertStars,
		Message:      message,
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
func (r *Router) deliverStarGift(ctx context.Context, senderID, recipientID int64, gift domain.StarGift, hideName bool, message string) (domain.SendPrivateTextResult, error) {
	recipientBlocked, err := r.peerBlocksUser(ctx, senderID, recipientID)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	sticker := gift.Sticker
	media := &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGift,
			StarGift: &domain.MessageStarGiftAction{
				GiftID:       gift.ID,
				Stars:        gift.Stars,
				ConvertStars: gift.ConvertStars,
				Title:        gift.Title,
				Sticker:      &sticker,
				Message:      message,
				FromUserID:   senderID,
				PeerUserID:   recipientID,
				NameHidden:   hideName,
				Saved:        true,
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
		OriginAuthKeyID:  authKeyID,
		OriginSessionID:  sessionID,
		RecipientBlocked: recipientBlocked,
	})
}

// refundStarGift 补偿退款（投递/记账失败时把已 Debit 的星退回）。
func (r *Router) refundStarGift(ctx context.Context, userID int64, peer domain.Peer, gift domain.StarGift) {
	if _, err := r.deps.Stars.Credit(ctx, userID, gift.Stars, domain.StarsReasonGift, peer, "Star gift refund", gift.Title); err != nil {
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
	page, err := r.deps.Gifts.ListSaved(ctx, owner, req.ExcludeUnsaved, req.Offset, req.Limit)
	if err != nil {
		return nil, internalErr()
	}
	return r.tgSavedStarGiftsResponse(ctx, userID, page.Gifts, page.Count, page.NextOffset), nil
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
	return r.tgSavedStarGiftsResponse(ctx, userID, gifts, len(gifts), ""), nil
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

// onPaymentsConvertStarGift 把收到的礼物转换回 Stars（Credit + 标记 converted）。
func (r *Router) onPaymentsConvertStarGift(ctx context.Context, ref tg.InputSavedStarGiftClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Gifts == nil || r.deps.Stars == nil {
		return false, notImplementedErr()
	}
	dref, ok, err := r.starGiftRefFromInput(ctx, userID, ref)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, starGiftInvalidErr()
	}
	if dref.Owner.Type == domain.PeerTypeChannel {
		return false, notImplementedErr()
	}
	saved, err := r.deps.Gifts.Convert(ctx, dref)
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
	if saved.ConvertStars > 0 {
		fromPeer := domain.Peer{Type: domain.PeerTypeUser, ID: saved.FromUserID}
		if _, err := r.deps.Stars.Credit(ctx, userID, saved.ConvertStars, domain.StarsReasonGift, fromPeer, "Star gift conversion", ""); err != nil {
			r.log.Error("star gift convert credit failed", zap.Int64("user_id", userID), zap.Int("msg_id", dref.MsgID), zap.Error(err))
			return false, internalErr()
		}
	}
	// 转换移除一份展示礼物 → 失效 owner full 投影。
	r.invalidateStarGiftOwnerProjection(dref.Owner)
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
	default:
		return domain.SavedStarGiftRef{}, false, nil
	}
}

func (r *Router) ensureCanManageStarGiftOwner(ctx context.Context, userID int64, owner domain.Peer) error {
	if owner.Type != domain.PeerTypeChannel {
		return nil
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

func (r *Router) tgSavedStarGiftsResponse(ctx context.Context, viewerUserID int64, gifts []domain.SavedStarGift, count int, nextOffset string) *tg.PaymentsSavedStarGifts {
	catalog := r.resolveStarGiftCatalog(ctx, gifts)
	out := &tg.PaymentsSavedStarGifts{
		Count: count,
		Gifts: tgSavedStarGifts(gifts, catalog),
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
	return out
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
		ID:           g.ID,
		Sticker:      tgDocument(g.Sticker),
		Stars:        g.Stars,
		ConvertStars: g.ConvertStars,
	}
	if g.Title != "" {
		gift.SetTitle(g.Title)
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

// resolveStarGiftCatalog 解析这批 saved gift 涉及的目录项（giftID → StarGift），供下发完整贴纸/星价。
func (r *Router) resolveStarGiftCatalog(ctx context.Context, gifts []domain.SavedStarGift) map[int64]domain.StarGift {
	out := make(map[int64]domain.StarGift, len(gifts))
	if r.deps.Gifts == nil {
		return out
	}
	for _, g := range gifts {
		if _, ok := out[g.GiftID]; ok {
			continue
		}
		if gift, found, err := r.deps.Gifts.GiftByID(ctx, g.GiftID); err == nil && found {
			out[g.GiftID] = gift
		}
	}
	return out
}

// tgSavedStarGifts 把已收到礼物实例投影为 []tg.SavedStarGift。
func tgSavedStarGifts(gifts []domain.SavedStarGift, catalog map[int64]domain.StarGift) []tg.SavedStarGift {
	out := make([]tg.SavedStarGift, 0, len(gifts))
	for _, g := range gifts {
		item := tg.SavedStarGift{
			Date: g.Date,
			Gift: tgSavedStarGiftGift(g, catalog),
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
		out = append(out, item)
	}
	return out
}

// tgSavedStarGiftGift 把 SavedStarGift 内嵌礼物投影为 tg.StarGift：优先用目录解析出完整贴纸/星价
// （客户端据有效 sticker 渲染），目录缺失（礼物已下架）时兜底最小形态。convert_stars 用实例值。
func tgSavedStarGiftGift(g domain.SavedStarGift, catalog map[int64]domain.StarGift) tg.StarGiftClass {
	if gift, ok := catalog[g.GiftID]; ok {
		out := tgStarGift(gift)
		out.ConvertStars = g.ConvertStars
		return out
	}
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

func starGiftFormID(userID, giftID int64) int64 {
	id := userID*0x9e3779b1 ^ (giftID << 7) ^ 0x5347494654
	if id == 0 {
		id = 0x5347
	}
	return id
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
