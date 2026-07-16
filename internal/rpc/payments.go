package rpc

import (
	"context"
	"errors"
	"strconv"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// registerPayments 注册 payments.* RPC：Stars 本地账本（余额/流水真实化）+ 其余
// gift/auction/revenue 第一阶段兼容桩。
func (r *Router) registerPayments(d *tg.ServerDispatcher) {
	d.OnPaymentsGetStarsTopupOptions(func(ctx context.Context) ([]tg.StarsTopupOption, error) {
		return devStarsTopupOptions(), nil
	})
	// premium 订阅赠送 telesrv 不实现（无支付流），返回空选项。关键作用：TDesktop 送礼框
	// ShowStarGiftBox 的 ready() 门控要求 getPremiumGiftCodeOptions 成功返回(on_next)才置
	// premiumGiftsReady=true，否则整框不弹出——此前返 NOT_IMPLEMENTED 导致点生日礼物无反应。
	// 空列表即解门，星礼物正常发送；premium 区段另由 userFull.disallow_premium_gifts=true 隐藏。
	d.OnPaymentsGetPremiumGiftCodeOptions(func(ctx context.Context, req *tg.PaymentsGetPremiumGiftCodeOptionsRequest) ([]tg.PremiumGiftCodeOption, error) {
		return []tg.PremiumGiftCodeOption{}, nil
	})
	d.OnPaymentsGetStarsStatus(r.onPaymentsGetStarsStatus)
	d.OnPaymentsGetStarsTransactions(r.onPaymentsGetStarsTransactions)
	d.OnPaymentsGetStarGiftActiveAuctions(func(ctx context.Context, hash int64) (tg.PaymentsStarGiftActiveAuctionsClass, error) {
		return tdesktop.StarGiftActiveAuctions(), nil
	})
	d.OnPaymentsGetStarGifts(r.onPaymentsGetStarGifts)
	d.OnPaymentsGetStarGiftUpgradePreview(r.onPaymentsGetStarGiftUpgradePreview)
	d.OnPaymentsGetUniqueStarGift(r.onPaymentsGetUniqueStarGift)
	d.OnPaymentsGetPaymentForm(r.onPaymentsGetPaymentForm)
	d.OnPaymentsSendStarsForm(r.onPaymentsSendStarsForm)
	d.OnPaymentsGetSavedStarGifts(r.onPaymentsGetSavedStarGifts)
	d.OnPaymentsGetSavedStarGift(r.onPaymentsGetSavedStarGift)
	d.OnPaymentsSaveStarGift(r.onPaymentsSaveStarGift)
	d.OnPaymentsConvertStarGift(r.onPaymentsConvertStarGift)
	d.OnPaymentsUpgradeStarGift(r.onPaymentsUpgradeStarGift)
	d.OnPaymentsGetStarGiftCollections(r.onPaymentsGetStarGiftCollections)
	d.OnPaymentsCreateStarGiftCollection(r.onPaymentsCreateStarGiftCollection)
	d.OnPaymentsUpdateStarGiftCollection(r.onPaymentsUpdateStarGiftCollection)
	d.OnPaymentsDeleteStarGiftCollection(r.onPaymentsDeleteStarGiftCollection)
	d.OnPaymentsReorderStarGiftCollections(r.onPaymentsReorderStarGiftCollections)
	d.OnPaymentsToggleStarGiftsPinnedToTop(r.onPaymentsToggleStarGiftsPinnedToTop)
	d.OnPaymentsGetStarsRevenueAdsAccountURL(func(ctx context.Context, peer tg.InputPeerClass) (*tg.PaymentsStarsRevenueAdsAccountURL, error) {
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer); err != nil {
			return nil, err
		}
		return &tg.PaymentsStarsRevenueAdsAccountURL{URL: "https://ads.telegram.org/"}, nil
	})
	d.OnPaymentsGetStarsRevenueStats(func(ctx context.Context, req *tg.PaymentsGetStarsRevenueStatsRequest) (*tg.PaymentsStarsRevenueStats, error) {
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		if req == nil {
			return nil, peerIDInvalidErr()
		}
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
			return nil, err
		}
		return tdesktop.StarsRevenueStats(req.GetTon()), nil
	})
}

// onPaymentsGetStarsStatus 返回当前账号的 Stars 余额（首读时惰性授予起始余额）。
// 响应必须是 payments.starsStatus（balance/chats/users 都是必填，空 vector 即可）——
// 两端客户端无条件读取 balance（DrKLO StarsAmount 反序列化 / TDesktop vbalance()）。
func (r *Router) onPaymentsGetStarsStatus(ctx context.Context, req *tg.PaymentsGetStarsStatusRequest) (*tg.PaymentsStarsStatus, error) {
	if req != nil && req.GetTon() {
		// TON 余额未建模：返回 0 nanoton 的合法响应。
		return emptyStarsStatus(&tg.StarsTonAmount{}), nil
	}
	if r.deps.Stars == nil {
		return emptyStarsStatus(&tg.StarsAmount{}), nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
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
	if req != nil && req.GetTon() {
		return emptyStarsStatus(&tg.StarsTonAmount{}), nil
	}
	if r.deps.Stars == nil {
		return emptyStarsStatus(&tg.StarsAmount{}), nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	offset := ""
	limit := domain.MaxStarsTransactionsLimit
	if req != nil {
		offset = req.Offset
		if req.Limit > 0 {
			limit = req.Limit
		}
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
