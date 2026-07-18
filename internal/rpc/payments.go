package rpc

import (
	"context"
	"errors"
	"strconv"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// registerPayments 注册 payments.* RPC：Stars 本地账本（余额/流水真实化）+ 其余
// gift/auction/revenue 第一阶段兼容桩。
func (r *Router) registerPayments(d *tlprofile.Dispatcher) {
	registerRPC[*tg.PaymentsGetStarsTopupOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsTopupOptions, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsTopupOptionsRequest) (any,

		// premium 订阅赠送 telesrv 不实现（无支付流），返回空选项。关键作用：TDesktop 送礼框
		// ShowStarGiftBox 的 ready() 门控要求 getPremiumGiftCodeOptions 成功返回(on_next)才置
		// premiumGiftsReady=true，否则整框不弹出——此前返 NOT_IMPLEMENTED 导致点生日礼物无反应。
		// 空列表即解门，星礼物正常发送；premium 区段另由 userFull.disallow_premium_gifts=true 隐藏。
		error) {
		return devStarsTopupOptions(), nil
	})
	registerRPC[*tg.PaymentsGetPremiumGiftCodeOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetPremiumGiftCodeOptions, func(ctx context.Context, req *tg.PaymentsGetPremiumGiftCodeOptionsRequest) (any, error) {
		return []tg.PremiumGiftCodeOption{}, nil
	})
	registerRPC[*tg.PaymentsGetStarsStatusRequest](d, tlprofile.SemanticMethodPaymentsGetStarsStatus, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsStatusRequest) (any, error) {
		return r.onPaymentsGetStarsStatus(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetStarsTransactionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsTransactions, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsTransactionsRequest) (any, error) {
		return r.onPaymentsGetStarsTransactions(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsCheckCanSendGiftRequest](d, tlprofile.SemanticMethodPaymentsCheckCanSendGift, func(ctx context.Context, req *tg.PaymentsCheckCanSendGiftRequest) (any, error) {
		return r.onPaymentsCheckCanSendGift(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftActiveAuctionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftActiveAuctions, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftActiveAuctionsRequest) (any, error) {
		return r.onPaymentsGetStarGiftActiveAuctions(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetStarGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetStarGifts, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftsRequest) (any, error) {
		return r.onPaymentsGetStarGifts(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.PaymentsGetStarGiftUpgradePreviewRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftUpgradePreview, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftUpgradePreviewRequest) (any, error) {
		return r.onPaymentsGetStarGiftUpgradePreview(ctx, layerRequest.
			GiftID)
	})
	registerRPC[*tg.PaymentsGetStarGiftUpgradeAttributesRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftUpgradeAttributes, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftUpgradeAttributesRequest) (any, error) {
		return r.onPaymentsGetStarGiftUpgradeAttributes(ctx, layerRequest.GiftID)
	})
	registerRPC[*tg.PaymentsGetUniqueStarGiftRequest](d, tlprofile.SemanticMethodPaymentsGetUniqueStarGift, func(ctx context.Context, layerRequest *tg.PaymentsGetUniqueStarGiftRequest) (any, error) {
		return r.onPaymentsGetUniqueStarGift(ctx, layerRequest.
			Slug)
	})
	registerRPC[*tg.PaymentsGetUniqueStarGiftValueInfoRequest](d, tlprofile.SemanticMethodPaymentsGetUniqueStarGiftValueInfo, func(ctx context.Context, req *tg.PaymentsGetUniqueStarGiftValueInfoRequest) (any, error) {
		return r.onPaymentsGetUniqueStarGiftValueInfo(ctx, req)
	})
	registerRPC[*tg.PaymentsGetResaleStarGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetResaleStarGifts, func(ctx context.Context, req *tg.PaymentsGetResaleStarGiftsRequest) (any, error) {
		return r.onPaymentsGetResaleStarGifts(ctx, req)
	})
	registerRPC[*tg.PaymentsGetPaymentFormRequest](d, tlprofile.SemanticMethodPaymentsGetPaymentForm, func(ctx context.Context, layerRequest *tg.PaymentsGetPaymentFormRequest) (any, error) {
		return r.onPaymentsGetPaymentForm(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsSendStarsFormRequest](d, tlprofile.SemanticMethodPaymentsSendStarsForm, func(ctx context.Context, layerRequest *tg.PaymentsSendStarsFormRequest) (any, error) {
		return r.onPaymentsSendStarsForm(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetSavedStarGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetSavedStarGifts, func(ctx context.Context, layerRequest *tg.PaymentsGetSavedStarGiftsRequest) (any, error) {
		return r.onPaymentsGetSavedStarGifts(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetSavedStarGiftRequest](d, tlprofile.SemanticMethodPaymentsGetSavedStarGift, func(ctx context.Context, layerRequest *tg.PaymentsGetSavedStarGiftRequest) (any, error) {
		return r.onPaymentsGetSavedStarGift(ctx, layerRequest.
			Stargift)
	})
	registerRPC[*tg.PaymentsSaveStarGiftRequest](d, tlprofile.SemanticMethodPaymentsSaveStarGift, func(ctx context.Context, layerRequest *tg.PaymentsSaveStarGiftRequest) (any, error) {
		return r.onPaymentsSaveStarGift(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsConvertStarGiftRequest](d, tlprofile.SemanticMethodPaymentsConvertStarGift, func(ctx context.Context, layerRequest *tg.PaymentsConvertStarGiftRequest) (any, error) {
		return r.onPaymentsConvertStarGift(ctx, layerRequest.
			Stargift)
	})
	registerRPC[*tg.PaymentsUpgradeStarGiftRequest](d, tlprofile.SemanticMethodPaymentsUpgradeStarGift, func(ctx context.Context, layerRequest *tg.PaymentsUpgradeStarGiftRequest) (any, error) {
		return r.onPaymentsUpgradeStarGift(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsUpdateStarGiftPriceRequest](d, tlprofile.SemanticMethodPaymentsUpdateStarGiftPrice, func(ctx context.Context, req *tg.PaymentsUpdateStarGiftPriceRequest) (any, error) {
		return r.onPaymentsUpdateStarGiftPrice(ctx, req)
	})
	registerRPC[*tg.PaymentsTransferStarGiftRequest](d, tlprofile.SemanticMethodPaymentsTransferStarGift, func(ctx context.Context, req *tg.PaymentsTransferStarGiftRequest) (any, error) {
		return r.onPaymentsTransferStarGift(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftWithdrawalURLRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftWithdrawalURL, func(ctx context.Context, req *tg.PaymentsGetStarGiftWithdrawalURLRequest) (any, error) {
		return r.onPaymentsGetStarGiftWithdrawalURL(ctx, req)
	})
	registerRPC[*tg.PaymentsSendStarGiftOfferRequest](d, tlprofile.SemanticMethodPaymentsSendStarGiftOffer, func(ctx context.Context, req *tg.PaymentsSendStarGiftOfferRequest) (any, error) {
		return r.onPaymentsSendStarGiftOffer(ctx, req)
	})
	registerRPC[*tg.PaymentsResolveStarGiftOfferRequest](d, tlprofile.SemanticMethodPaymentsResolveStarGiftOffer, func(ctx context.Context, req *tg.PaymentsResolveStarGiftOfferRequest) (any, error) {
		return r.onPaymentsResolveStarGiftOffer(ctx, req)
	})
	registerRPC[*tg.PaymentsGetCraftStarGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetCraftStarGifts, func(ctx context.Context, req *tg.PaymentsGetCraftStarGiftsRequest) (any, error) {
		return r.onPaymentsGetCraftStarGifts(ctx, req)
	})
	registerRPC[*tg.PaymentsCraftStarGiftRequest](d, tlprofile.SemanticMethodPaymentsCraftStarGift, func(ctx context.Context, req *tg.PaymentsCraftStarGiftRequest) (any, error) {
		return r.onPaymentsCraftStarGift(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftAuctionStateRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftAuctionState, func(ctx context.Context, req *tg.PaymentsGetStarGiftAuctionStateRequest) (any, error) {
		return r.onPaymentsGetStarGiftAuctionState(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftAuctionAcquiredGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftAuctionAcquiredGifts, func(ctx context.Context, req *tg.PaymentsGetStarGiftAuctionAcquiredGiftsRequest) (any, error) {
		return r.onPaymentsGetStarGiftAuctionAcquiredGifts(ctx, req)
	})
	registerRPC[*tg.PaymentsToggleChatStarGiftNotificationsRequest](d, tlprofile.SemanticMethodPaymentsToggleChatStarGiftNotifications, func(ctx context.Context, req *tg.PaymentsToggleChatStarGiftNotificationsRequest) (any, error) {
		return r.onPaymentsToggleChatStarGiftNotifications(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftCollectionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftCollections, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftCollectionsRequest) (any, error) {
		return r.onPaymentsGetStarGiftCollections(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsCreateStarGiftCollectionRequest](d, tlprofile.SemanticMethodPaymentsCreateStarGiftCollection, func(ctx context.Context, layerRequest *tg.PaymentsCreateStarGiftCollectionRequest) (any, error) {
		return r.onPaymentsCreateStarGiftCollection(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsUpdateStarGiftCollectionRequest](d, tlprofile.SemanticMethodPaymentsUpdateStarGiftCollection, func(ctx context.Context, layerRequest *tg.PaymentsUpdateStarGiftCollectionRequest) (any, error) {
		return r.onPaymentsUpdateStarGiftCollection(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsDeleteStarGiftCollectionRequest](d, tlprofile.SemanticMethodPaymentsDeleteStarGiftCollection, func(ctx context.Context, layerRequest *tg.PaymentsDeleteStarGiftCollectionRequest) (any, error) {
		return r.onPaymentsDeleteStarGiftCollection(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsReorderStarGiftCollectionsRequest](d, tlprofile.SemanticMethodPaymentsReorderStarGiftCollections, func(ctx context.Context, layerRequest *tg.PaymentsReorderStarGiftCollectionsRequest) (any, error) {
		return r.onPaymentsReorderStarGiftCollections(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsToggleStarGiftsPinnedToTopRequest](d, tlprofile.SemanticMethodPaymentsToggleStarGiftsPinnedToTop, func(ctx context.Context, layerRequest *tg.PaymentsToggleStarGiftsPinnedToTopRequest) (any, error) {
		return r.onPaymentsToggleStarGiftsPinnedToTop(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetStarsRevenueAdsAccountURLRequest](d, tlprofile.SemanticMethodPaymentsGetStarsRevenueAdsAccountURL, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsRevenueAdsAccountURLRequest) (any, error) {
		peer := layerRequest.
			Peer
		_ = peer

		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer); err != nil {
			return nil, err
		}
		return &tg.PaymentsStarsRevenueAdsAccountURL{URL: "https://ads.telegram.org/"}, nil
	})
	registerRPC[*tg.PaymentsGetStarsRevenueStatsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsRevenueStats, func(ctx context.Context, req *tg.PaymentsGetStarsRevenueStatsRequest) (any, error) {
		return r.onPaymentsGetStarsRevenueStats(ctx, req)
	})

}

// onPaymentsGetStarsRevenueStats exposes real channel Star Gift proceeds from
// the same peer-scoped ledger as getStarsStatus/getStarsTransactions. Personal
// and bot revenue remain the bounded compatibility response because their
// revenue bucket is distinct from the general Stars balance and is not modeled.
func (r *Router) onPaymentsGetStarsRevenueStats(ctx context.Context, req *tg.PaymentsGetStarsRevenueStatsRequest) (*tg.PaymentsStarsRevenueStats, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req == nil {
		return nil, peerIDInvalidErr()
	}
	owner, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	ton := req.GetTon()
	if owner.Type != domain.PeerTypeChannel {
		return tdesktop.StarsRevenueStats(ton), nil
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, owner); err != nil {
		return nil, err
	}
	ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
	if !ok {
		return tdesktop.StarsRevenueStats(ton), nil
	}
	var balance int64
	if ton {
		balance, err = ledger.ChannelTonBalance(ctx, owner.ID)
	} else {
		balance, err = ledger.ChannelStarsBalance(ctx, owner.ID)
	}
	if err != nil {
		return nil, internalErr()
	}
	stats := tdesktop.StarsRevenueStats(ton)
	var amount tg.StarsAmountClass = &tg.StarsAmount{Amount: balance}
	if ton {
		amount = &tg.StarsTonAmount{Amount: balance}
	}
	// Channel ledgers currently only receive collectible conversion/marketplace
	// proceeds and have no withdrawal/debit path, so balance equals lifetime
	// revenue. Withdrawal stays disabled because no external payout exists.
	stats.Status.CurrentBalance = amount
	stats.Status.AvailableBalance = amount
	stats.Status.OverallRevenue = amount
	return stats, nil
}

type channelGiftLedgerReader interface {
	ChannelStarsBalance(ctx context.Context, channelID int64) (int64, error)
	ChannelStarsTransactions(ctx context.Context, channelID int64, offset string, limit int) (domain.StarsTransactionPage, error)
	ChannelTonBalance(ctx context.Context, channelID int64) (int64, error)
	ChannelTonTransactions(ctx context.Context, channelID int64, offset string, limit int) (domain.TonTransactionPage, error)
}

// onPaymentsGetStarsStatus 返回请求 peer 的 Stars/本地 TON 余额。个人与频道账本
// 严格隔离；频道读取要求 Star Gift 管理权限，不能把频道收益投影到执行 RPC 的管理员。
// 响应必须是 payments.starsStatus（balance/chats/users 都是必填，空 vector 即可）——
// 两端客户端无条件读取 balance（DrKLO StarsAmount 反序列化 / TDesktop vbalance()）。
func (r *Router) onPaymentsGetStarsStatus(ctx context.Context, req *tg.PaymentsGetStarsStatusRequest) (*tg.PaymentsStarsStatus, error) {
	userID, owner, err := r.starGiftLedgerOwner(ctx, req)
	if err != nil {
		return nil, err
	}
	ton := req != nil && req.GetTon()
	if owner.Type == domain.PeerTypeChannel {
		ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
		if !ok {
			if ton {
				return emptyStarsStatus(&tg.StarsTonAmount{}), nil
			}
			return emptyStarsStatus(&tg.StarsAmount{}), nil
		}
		var balance int64
		if ton {
			balance, err = ledger.ChannelTonBalance(ctx, owner.ID)
		} else {
			balance, err = ledger.ChannelStarsBalance(ctx, owner.ID)
		}
		if err != nil {
			return nil, internalErr()
		}
		var amount tg.StarsAmountClass = &tg.StarsAmount{Amount: balance}
		if ton {
			amount = &tg.StarsTonAmount{Amount: balance}
		}
		out := emptyStarsStatus(amount)
		out.Chats = r.tgChatsForChannelIDs(ctx, userID, []int64{owner.ID})
		return out, nil
	}
	if ton {
		if r.deps.Gifts == nil {
			return emptyStarsStatus(&tg.StarsTonAmount{}), nil
		}
		balance, err := r.deps.Gifts.TonBalance(ctx, userID)
		if err != nil {
			return nil, internalErr()
		}
		return emptyStarsStatus(&tg.StarsTonAmount{Amount: balance}), nil
	}
	if r.deps.Stars == nil {
		return emptyStarsStatus(&tg.StarsAmount{}), nil
	}
	bal, err := r.deps.Stars.GetBalance(ctx, userID)
	if err != nil {
		return nil, starsErr(err)
	}
	return emptyStarsStatus(&tg.StarsAmount{Amount: bal.Balance}), nil
}

// onPaymentsGetStarsTransactions 返回 keyset 分页的 Stars 流水（同 starsStatus 信封）。
// 末页必须省略 next_offset（flag 不置），否则 DrKLO 会无限翻页。
func (r *Router) onPaymentsGetStarsTransactions(ctx context.Context, req *tg.PaymentsGetStarsTransactionsRequest) (*tg.PaymentsStarsStatus, error) {
	userID, owner, err := r.starGiftTransactionLedgerOwner(ctx, req)
	if err != nil {
		return nil, err
	}
	offset, limit := "", domain.MaxStarsTransactionsLimit
	if req != nil {
		offset = req.Offset
		if req.Limit > 0 {
			limit = req.Limit
		}
	}
	ton := req != nil && req.GetTon()
	if owner.Type == domain.PeerTypeChannel {
		ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
		if !ok {
			if ton {
				return emptyStarsStatus(&tg.StarsTonAmount{}), nil
			}
			return emptyStarsStatus(&tg.StarsAmount{}), nil
		}
		if ton {
			page, err := ledger.ChannelTonTransactions(ctx, owner.ID, offset, limit)
			if err != nil {
				return nil, internalErr()
			}
			out := emptyStarsStatus(&tg.StarsTonAmount{Amount: page.Balance})
			if txns := tgTonTransactions(page.Transactions); len(txns) > 0 {
				out.SetHistory(txns)
			}
			if page.NextOffset != "" {
				out.SetNextOffset(page.NextOffset)
			}
			r.enrichChannelTonLedgerStatus(ctx, userID, owner.ID, page.Transactions, out)
			return out, nil
		}
		page, err := ledger.ChannelStarsTransactions(ctx, owner.ID, offset, limit)
		if err != nil {
			return nil, internalErr()
		}
		out := emptyStarsStatus(&tg.StarsAmount{Amount: page.Balance})
		if txns := tgStarsTransactions(page.Transactions); len(txns) > 0 {
			out.SetHistory(txns)
		}
		if page.NextOffset != "" {
			out.SetNextOffset(page.NextOffset)
		}
		r.enrichChannelStarsLedgerStatus(ctx, userID, owner.ID, page.Transactions, out)
		return out, nil
	}
	if ton {
		if r.deps.Gifts == nil {
			return emptyStarsStatus(&tg.StarsTonAmount{}), nil
		}
		page, err := r.deps.Gifts.TonTransactions(ctx, userID, offset, limit)
		if err != nil {
			return nil, internalErr()
		}
		out := emptyStarsStatus(&tg.StarsTonAmount{Amount: page.Balance})
		if txns := tgTonTransactions(page.Transactions); len(txns) > 0 {
			out.SetHistory(txns)
		}
		if page.NextOffset != "" {
			out.SetNextOffset(page.NextOffset)
		}
		ids := make([]int64, 0)
		for _, txn := range page.Transactions {
			if txn.Peer.Type == domain.PeerTypeUser {
				ids = append(ids, txn.Peer.ID)
			}
		}
		out.Users = tgUsersForViewer(userID, r.domainUsersForIDs(ctx, userID, uniqueInt64(ids)))
		return out, nil
	}
	if r.deps.Stars == nil {
		return emptyStarsStatus(&tg.StarsAmount{}), nil
	}
	page, err := r.deps.Stars.ListTransactions(ctx, userID, offset, limit)
	if err != nil {
		return nil, starsErr(err)
	}
	out := emptyStarsStatus(&tg.StarsAmount{Amount: page.Balance})
	if txns := tgStarsTransactions(page.Transactions); len(txns) > 0 {
		out.SetHistory(txns)
	}
	if page.NextOffset != "" {
		out.SetNextOffset(page.NextOffset)
	}
	// 富化流水中提到的用户对手方（频道对手方进 Chats 留待 paid reaction 阶段）。
	if ids := starsTransactionUserIDs(page.Transactions); len(ids) > 0 {
		out.Users = tgUsersForViewer(userID, r.domainUsersForIDs(ctx, userID, ids))
	}
	return out, nil
}

func (r *Router) starGiftLedgerOwner(ctx context.Context, req *tg.PaymentsGetStarsStatusRequest) (int64, domain.Peer, error) {
	if req == nil {
		return 0, domain.Peer{}, peerIDInvalidErr()
	}
	return r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
}

func (r *Router) starGiftTransactionLedgerOwner(ctx context.Context, req *tg.PaymentsGetStarsTransactionsRequest) (int64, domain.Peer, error) {
	if req == nil {
		return 0, domain.Peer{}, peerIDInvalidErr()
	}
	return r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
}

func (r *Router) starGiftLedgerOwnerForPeer(ctx context.Context, input tg.InputPeerClass) (int64, domain.Peer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, domain.Peer{}, internalErr()
	}
	owner, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return 0, domain.Peer{}, err
	}
	if owner.Type == domain.PeerTypeUser {
		if owner.ID != userID {
			return 0, domain.Peer{}, peerIDInvalidErr()
		}
		return userID, owner, nil
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, owner); err != nil {
		return 0, domain.Peer{}, err
	}
	return userID, owner, nil
}

func (r *Router) enrichChannelStarsLedgerStatus(ctx context.Context, viewerID, ownerChannelID int64, txns []domain.StarsTransaction, out *tg.PaymentsStarsStatus) {
	userIDs := make([]int64, 0, len(txns))
	channelIDs := []int64{ownerChannelID}
	for _, txn := range txns {
		switch txn.Peer.Type {
		case domain.PeerTypeUser:
			userIDs = append(userIDs, txn.Peer.ID)
		case domain.PeerTypeChannel:
			channelIDs = append(channelIDs, txn.Peer.ID)
		}
	}
	out.Users = tgUsersForViewer(viewerID, r.domainUsersForIDs(ctx, viewerID, uniqueInt64(userIDs)))
	out.Chats = r.tgChatsForChannelIDs(ctx, viewerID, uniqueInt64(channelIDs))
}

func (r *Router) enrichChannelTonLedgerStatus(ctx context.Context, viewerID, ownerChannelID int64, txns []domain.TonTransaction, out *tg.PaymentsStarsStatus) {
	userIDs := make([]int64, 0, len(txns))
	channelIDs := []int64{ownerChannelID}
	for _, txn := range txns {
		switch txn.Peer.Type {
		case domain.PeerTypeUser:
			userIDs = append(userIDs, txn.Peer.ID)
		case domain.PeerTypeChannel:
			channelIDs = append(channelIDs, txn.Peer.ID)
		}
	}
	out.Users = tgUsersForViewer(viewerID, r.domainUsersForIDs(ctx, viewerID, uniqueInt64(userIDs)))
	out.Chats = r.tgChatsForChannelIDs(ctx, viewerID, uniqueInt64(channelIDs))
}

// emptyStarsStatus 构造一个合法的最小 payments.starsStatus（chats/users 非空 vector 但可空）。
func emptyStarsStatus(balance tg.StarsAmountClass) *tg.PaymentsStarsStatus {
	return &tg.PaymentsStarsStatus{
		Balance: balance,
		Chats:   []tg.ChatClass{},
		Users:   []tg.UserClass{},
	}
}

// tgStarsTransactions 把账本流水投影为 tg.StarsTransaction（amount 带符号：借记为负）。
func tgStarsTransactions(in []domain.StarsTransaction) []tg.StarsTransaction {
	out := make([]tg.StarsTransaction, 0, len(in))
	for _, t := range in {
		item := tg.StarsTransaction{
			ID:     strconv.FormatInt(t.ID, 10),
			Amount: &tg.StarsAmount{Amount: t.Amount},
			Date:   t.Date,
			Peer:   tgStarsTransactionPeer(t),
		}
		if t.Title != "" {
			item.SetTitle(t.Title)
		}
		if t.Description != "" {
			item.SetDescription(t.Description)
		}
		switch t.Reason {
		case domain.StarsReasonReaction:
			item.Reaction = true
		case domain.StarsReasonGift:
			item.Gift = true
		case domain.StarsReasonGiftUpgrade:
			item.StargiftUpgrade = true
		case domain.StarsReasonGiftResale:
			item.StargiftResale = true
		case domain.StarsReasonGiftPrepaid:
			item.StargiftPrepaidUpgrade = true
		case domain.StarsReasonGiftDrop:
			item.StargiftDropOriginalDetails = true
		case domain.StarsReasonGiftAuction:
			item.StargiftAuctionBid = true
		case domain.StarsReasonGiftOffer:
			item.Offer = true
		}
		out = append(out, item)
	}
	return out
}

func tgTonTransactions(in []domain.TonTransaction) []tg.StarsTransaction {
	out := make([]tg.StarsTransaction, 0, len(in))
	for _, t := range in {
		item := tg.StarsTransaction{ID: strconv.FormatInt(t.ID, 10), Amount: &tg.StarsTonAmount{Amount: t.Amount},
			Date: t.Date, Peer: tgStarsTransactionPeer(domain.StarsTransaction{Peer: t.Peer, Reason: t.Reason})}
		if t.Amount > 0 {
			item.Refund = true
		}
		if t.Title != "" {
			item.SetTitle(t.Title)
		}
		if t.Description != "" {
			item.SetDescription(t.Description)
		}
		switch t.Reason {
		case domain.StarsReasonGiftResale:
			item.StargiftResale = true
		case domain.StarsReasonGiftOffer:
			item.Offer = true
		case domain.StarsReasonGiftAuction:
			item.StargiftAuctionBid = true
		case domain.StarsReasonGiftPrepaid:
			item.StargiftPrepaidUpgrade = true
		case domain.StarsReasonGiftDrop:
			item.StargiftDropOriginalDetails = true
		}
		out = append(out, item)
	}
	return out
}

// tgStarsTransactionPeer 选择对手方构造器：grant/topup 走 Fragment（站外充值轨），
// 真实 peer 走 starsTransactionPeer，其余兜底 Unsupported（Peer 字段必填，不可为 nil）。
func tgStarsTransactionPeer(t domain.StarsTransaction) tg.StarsTransactionPeerClass {
	switch t.Reason {
	case domain.StarsReasonGrant, domain.StarsReasonTopup:
		return &tg.StarsTransactionPeerFragment{}
	}
	if t.Peer.Type != "" && t.Peer.ID != 0 {
		if p := tgPeer(t.Peer); p != nil {
			return &tg.StarsTransactionPeer{Peer: p}
		}
	}
	return &tg.StarsTransactionPeerUnsupported{}
}

// starsTransactionUserIDs 收集流水中去重的用户类对手方 id。
func starsTransactionUserIDs(in []domain.StarsTransaction) []int64 {
	seen := make(map[int64]struct{}, len(in))
	ids := make([]int64, 0, len(in))
	for _, t := range in {
		if t.Peer.Type != domain.PeerTypeUser || t.Peer.ID == 0 {
			continue
		}
		if _, ok := seen[t.Peer.ID]; ok {
			continue
		}
		seen[t.Peer.ID] = struct{}{}
		ids = append(ids, t.Peer.ID)
	}
	return ids
}

// starsErr 把 Stars 账本领域错误映射为客户端可识别的 tgerr（仿 premiumBoostErr）。
func starsErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStarsInsufficient):
		return balanceTooLowErr()
	case errors.Is(err, domain.ErrStarsInvalidAmount):
		return starsAmountInvalidErr()
	default:
		return internalErr()
	}
}
