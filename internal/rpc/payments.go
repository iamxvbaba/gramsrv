package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// registerPayments 注册 payments.* RPC：Stars 本地账本（余额/流水真实化）+ 其余
// gift/auction/revenue 第一阶段兼容桩。
func (r *Router) registerPayments(d *tlprofile.Dispatcher) {
	registerRPC[*tg.PaymentsCanPurchaseStoreRequest](d, tlprofile.SemanticMethodPaymentsCanPurchaseStore, func(ctx context.Context, req *tg.PaymentsCanPurchaseStoreRequest) (any, error) {
		return r.onPaymentsCanPurchaseStore(ctx, req)
	})
	registerRPC[*tg.PaymentsAssignPlayMarketTransactionRequest](d, tlprofile.SemanticMethodPaymentsAssignPlayMarketTransaction, func(ctx context.Context, req *tg.PaymentsAssignPlayMarketTransactionRequest) (any, error) {
		return r.onPaymentsAssignPlayMarketTransaction(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsGiftOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsGiftOptions, func(ctx context.Context, req *tg.PaymentsGetStarsGiftOptionsRequest) (any, error) {
		return r.onPaymentsGetStarsGiftOptions(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsGiveawayOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsGiveawayOptions, func(ctx context.Context, _ *tg.PaymentsGetStarsGiveawayOptionsRequest) (any, error) {
		return r.onPaymentsGetStarsGiveawayOptions(ctx)
	})
	registerRPC[*tg.PaymentsGetGiveawayInfoRequest](d, tlprofile.SemanticMethodPaymentsGetGiveawayInfo, func(ctx context.Context, req *tg.PaymentsGetGiveawayInfoRequest) (any, error) {
		return r.onPaymentsGetGiveawayInfo(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsTopupOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsTopupOptions, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsTopupOptionsRequest) (any,

		// Premium gift options come from the same versioned XTR catalog used by
		// payment forms and settlement.
		error) {
		return devStarsTopupOptions(), nil
	})
	registerRPC[*tg.PaymentsGetPremiumGiftCodeOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetPremiumGiftCodeOptions, func(ctx context.Context, req *tg.PaymentsGetPremiumGiftCodeOptionsRequest) (any, error) {
		return r.onPaymentsGetPremiumGiftCodeOptions(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsStatusRequest](d, tlprofile.SemanticMethodPaymentsGetStarsStatus, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsStatusRequest) (any, error) {
		return r.onPaymentsGetStarsStatus(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetStarsSubscriptionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsSubscriptions, func(ctx context.Context, req *tg.PaymentsGetStarsSubscriptionsRequest) (any, error) {
		return r.onPaymentsGetStarsSubscriptions(ctx, req)
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
	registerRPC[*tg.PaymentsValidateRequestedInfoRequest](d, tlprofile.SemanticMethodPaymentsValidateRequestedInfo, func(ctx context.Context, req *tg.PaymentsValidateRequestedInfoRequest) (any, error) {
		return r.onPaymentsValidateRequestedInfo(ctx, req)
	})
	registerRPC[*tg.PaymentsSendStarsFormRequest](d, tlprofile.SemanticMethodPaymentsSendStarsForm, func(ctx context.Context, layerRequest *tg.PaymentsSendStarsFormRequest) (any, error) {
		return r.onPaymentsSendStarsForm(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsSendPaymentFormRequest](d, tlprofile.SemanticMethodPaymentsSendPaymentForm, func(ctx context.Context, req *tg.PaymentsSendPaymentFormRequest) (any, error) {
		return r.onPaymentsSendPaymentForm(ctx, req)
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
		return &tg.PaymentsStarsRevenueAdsAccountURL{URL: r.publicLink("")}, nil
	})
	registerRPC[*tg.PaymentsGetStarsRevenueStatsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsRevenueStats, func(ctx context.Context, req *tg.PaymentsGetStarsRevenueStatsRequest) (any, error) {
		return r.onPaymentsGetStarsRevenueStats(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsRevenueWithdrawalURLRequest](d, tlprofile.SemanticMethodPaymentsGetStarsRevenueWithdrawalURL, func(ctx context.Context, req *tg.PaymentsGetStarsRevenueWithdrawalURLRequest) (any, error) {
		return r.onPaymentsGetStarsRevenueWithdrawalURL(ctx, req)
	})

}

func (r *Router) onPaymentsCanPurchaseStore(ctx context.Context, _ *tg.PaymentsCanPurchaseStoreRequest) (bool, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return false, internalErr()
	}
	// telesrv deliberately exposes no Google Play products or receipt verifier.
	// DrKLO is steered to the invoice flow by appConfig; if a stale client still
	// reaches this preflight, fail closed instead of authorizing an unverifiable
	// external charge.
	return false, nil
}

func (r *Router) onPaymentsAssignPlayMarketTransaction(ctx context.Context, _ *tg.PaymentsAssignPlayMarketTransactionRequest) (tg.UpdatesClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return nil, tgerr.New(400, "STORE_PAYMENT_UNAVAILABLE")
}

// onPaymentsGetStarsRevenueStats exposes real channel Star Gift proceeds from
// the same peer-scoped ledger as getStarsStatus/getStarsTransactions, and real
// bot invoice proceeds from the bot Stars wallet.
//
// Personal user revenue remains the bounded compatibility response: a plain
// user has no revenue bucket distinct from their own spendable Stars balance,
// and reporting the latter as income would show spending as earnings.
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
		// The bot wallet is XTR-only: there is no TON variant of bot invoice
		// proceeds, so a ton=true request keeps the bounded stub.
		if ton || owner.Type != domain.PeerTypeUser {
			return tdesktop.StarsRevenueStats(ton), nil
		}
		// Re-resolve the peer: a bot wallet is owned by the viewer, so it has
		// to be admitted before the personal-user stub is returned.
		botID, createdAt, ok, err := r.botStarsWalletOwner(ctx, userID, req.Peer)
		if err != nil {
			return nil, err
		}
		if !ok {
			return tdesktop.StarsRevenueStats(ton), nil
		}
		return r.botStarsRevenueStats(ctx, userID, botID, createdAt, ton)
	}
	if r.deps.Channels == nil {
		return nil, peerIDInvalidErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, owner.ID)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	if view.Self.Role != domain.ChannelRoleCreator &&
		(view.Self.Role != domain.ChannelRoleAdmin || !view.Self.AdminRights.PostMessages) {
		return nil, tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	}
	isCreator := view.Self.Role == domain.ChannelRoleCreator && view.Channel.CreatorUserID == userID
	if !isCreator && view.Self.Role == domain.ChannelRoleCreator {
		return nil, tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	}
	ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
	if !ok {
		return tdesktop.StarsRevenueStats(ton), nil
	}
	var balance int64
	overallRevenue := int64(0)
	if ton {
		balance, err = ledger.ChannelTonBalance(ctx, owner.ID)
	} else {
		balance, err = ledger.ChannelStarsBalance(ctx, owner.ID)
	}
	if err != nil {
		return nil, internalErr()
	}
	overallRevenue = balance
	if overall, ok := r.deps.Gifts.(channelRevenueOverallReader); ok {
		if ton {
			overallRevenue, err = overall.ChannelTonOverallRevenue(ctx, owner.ID)
		} else {
			overallRevenue, err = overall.ChannelStarsOverallRevenue(ctx, owner.ID)
		}
		if err != nil {
			return nil, internalErr()
		}
	}
	stats := tdesktop.StarsRevenueStats(ton)
	var amount tg.StarsAmountClass = &tg.StarsAmount{Amount: balance}
	var overallAmount tg.StarsAmountClass = &tg.StarsAmount{Amount: overallRevenue}
	if ton {
		amount = &tg.StarsTonAmount{Amount: balance}
		overallAmount = &tg.StarsTonAmount{Amount: overallRevenue}
	}
	// Current/available are the spendable channel balance. Overall remains the
	// positive lifetime revenue even after creator claims add debit entries.
	stats.Status.CurrentBalance = amount
	stats.Status.AvailableBalance = amount
	stats.Status.OverallRevenue = overallAmount
	issuer, withdrawalAvailable := r.deps.Gifts.(channelRevenueWithdrawalIssuer)
	stats.Status.WithdrawalEnabled = isCreator && balance > 0 && withdrawalAvailable && issuer.ChannelRevenueWithdrawalAvailable()
	return stats, nil
}

// botStarsRevenueStats reports a bot's own Stars wallet, which holds the XTR
// invoice proceeds and is deliberately separate from the bot user identity's
// personal balance.
//
// Only the bot's owner may read it: revenue is operator business data, and
// payments.getStarsStatus is the method that exposes a principal's own
// spendable balance. A non-owner asking for a peer's wallet gets
// USER_PERMISSION_DENIED rather than a zeroed stub, so an operator cannot
// mistake "not yours" for "you earned nothing".
func (r *Router) botStarsRevenueStats(ctx context.Context, viewerID, botUserID int64, createdAt time.Time, ton bool) (*tg.PaymentsStarsRevenueStats, error) {
	stats := tdesktop.StarsRevenueStats(ton)
	ledger, ok := r.deps.Gifts.(botStarsWalletReader)
	if !ok {
		return stats, nil
	}
	balance, err := ledger.BotStarsBalance(ctx, botUserID)
	if err != nil {
		return nil, internalErr()
	}
	overallRevenue, err := ledger.BotStarsOverallRevenue(ctx, botUserID)
	if err != nil {
		return nil, internalErr()
	}
	amount := tg.StarsAmountClass(&tg.StarsAmount{Amount: balance})
	overallAmount := tg.StarsAmountClass(&tg.StarsAmount{Amount: overallRevenue})
	if ton {
		amount = &tg.StarsTonAmount{Amount: balance}
		overallAmount = &tg.StarsTonAmount{Amount: overallRevenue}
	}
	// Bot wallet funds are spent through the bot's own invoices and gifts, not
	// withdrawn to a personal ledger, so withdrawal stays disabled exactly like
	// a creator channel without a configured withdrawal provider.
	stats.Status.CurrentBalance = amount
	stats.Status.AvailableBalance = amount
	stats.Status.OverallRevenue = overallAmount
	stats.Status.WithdrawalEnabled = false
	// The TON wallet is a separate bucket that telesrv does not model, so the
	// chart keeps the compatibility error for a ton=true request.
	if ton {
		return stats, nil
	}
	graph, err := r.botStarsRevenueGraph(ctx, ledger, botUserID, createdAt, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, err
	}
	stats.RevenueGraph = graph
	return stats, nil
}

// botStarsRevenueGraph renders the wallet journal as the inline-JSON chart the
// official clients expect.
//
// Layer 228 ships no line/bar StatsGraph constructor: statsGraph is the only
// data-carrying form and its payload is a DataJSON object. The client parses
// {"columns":[["x",<ms>,...],["revenue",<v>,...]],"yTickFormatter":"XTR"}
// directly out of the response, so nothing has to be fetched over HTTP.
//
// An empty series must stay a statsGraphError: the renderer logs "Empty
// columns list" and draws nothing for a column array without an x column.
func (r *Router) botStarsRevenueGraph(ctx context.Context, ledger botStarsWalletReader, botUserID int64, createdAt time.Time, now int) (tg.StatsGraphClass, error) {
	const (
		bucketSeconds = 86400
		maxPoints     = 366
		// minPoints is the smallest window the official stack renderer draws
		// correctly; see the padding below.
		minPoints = 3
	)
	// Anchor to UTC midnight so the daily buckets line up with the day boundary
	// the client assumes when it derives a day label from an x value.
	today := (now / bucketSeconds) * bucketSeconds
	// The axis starts at the day the bot was created, not at a fixed lookback:
	// a bot three days old must not be drawn against a year of empty space.
	// The window is still capped so a long-lived bot stays bounded.
	earliest := today - (maxPoints-1)*bucketSeconds
	from := today
	if !createdAt.IsZero() {
		createdDay := int(createdAt.Unix()) / bucketSeconds * bucketSeconds
		switch {
		case createdDay < earliest:
			from = earliest
		case createdDay > today:
			// Clock skew or a creation date in the future: never plot a window
			// that ends before it starts.
			from = today
		default:
			from = createdDay
		}
	}
	// The stack renderer derives the bar step from xPercentage[1] and reads that
	// index unguarded, so fewer than three points is not merely ugly: one point
	// is an out-of-bounds read and two points collapse the bar step to zero.
	// Pad a young bot's window instead of shipping a broken chart.
	if from > today-(minPoints-1)*bucketSeconds {
		from = today - (minPoints-1)*bucketSeconds
	}
	points, err := ledger.BotStarsRevenueSeries(ctx, botUserID, from, today)
	if err != nil {
		return nil, internalErr()
	}
	if len(points) == 0 {
		return &tg.StatsGraphError{Error: "Not enough data to display."}, nil
	}
	// A stack bar needs equal-length columns, so gaps are filled with zero
	// rather than leaving a shorter y column the renderer would truncate.
	byDay := make(map[int]domain.BotStarsRevenuePoint, len(points))
	for _, point := range points {
		byDay[point.DayStart] = point
	}
	// Column 0 is the shared x axis, then one column per stacked series.
	columns := make([][]any, 0, 3)
	xColumn := make([]any, 0, maxPoints)
	xColumn = append(xColumn, "x")
	revenueColumn := make([]any, 0, maxPoints)
	revenueColumn = append(revenueColumn, "revenue")
	for day := from; day <= today; day += bucketSeconds {
		point := byDay[day]
		// The renderer derives its default one-day step from milliseconds.
		xColumn = append(xColumn, int64(day)*1000)
		revenueColumn = append(revenueColumn, point.Amount)
	}
	// One line only. A second line is what makes the client draw its legend
	// buttons (ChartWidget::setupFilterButtons bails out at lines.size() <= 1),
	// and an extra row of buttons under the chart reads as a rendering glitch.
	// Spend and refunds stay in the transaction list, where they belong, and the
	// overview numbers already carry the balance.
	columns = append(columns, xColumn, revenueColumn)
	payload, err := json.Marshal(map[string]any{
		"columns": columns,
		// XTR selects the Stars formatter in the client; without it the axis
		// renders as a plain number and the chart looks like fiat.
		"yTickFormatter": "XTR",
		// TDesktop paints each line from the colorKey via
		// FillLineColorsByKey, so without this it falls back to a black bar.
		// GOLDEN is the theme key the client itself uses for Stars; the hex is
		// only the pre-theme fallback and is the palette value #eba52d. Android
		// overrides the color with its own yellow either way.
		"colors": map[string]string{"revenue": "GOLDEN#eba52d"},
		// An absent name leaves the point-details label blank, and the client
		// does not localize a server-supplied string.
		"names": map[string]string{"revenue": "Revenue"},
	})
	if err != nil {
		return nil, internalErr()
	}
	return &tg.StatsGraph{JSON: tg.DataJSON{Data: string(payload)}}, nil
}

// botStarsWalletStatus serves payments.getStarsStatus for a bot wallet peer by
// reporting the wallet balance in the same starsStatus envelope the personal
// and channel paths use.
//
// The TON wallet is a separate bucket that telesrv does not model, so a
// ton=true request returns a zeroed envelope instead of the XTR balance.
func (r *Router) botStarsWalletStatus(ctx context.Context, req *tg.PaymentsGetStarsStatusRequest) (*tg.PaymentsStarsStatus, bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, true, internalErr()
	}
	// handled must stay true on an error: the caller only propagates the error
	// when it considers the peer handled, so a false here would silently drop a
	// permission denial and fall through to the personal-user path.
	botID, _, ok, err := r.botStarsWalletOwner(ctx, userID, req.Peer)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, false, nil
	}
	ton := req.GetTon()
	if ton {
		return emptyStarsStatus(&tg.StarsTonAmount{}), true, nil
	}
	ledger, supported := r.deps.Gifts.(botStarsWalletReader)
	if !supported {
		return emptyStarsStatus(&tg.StarsAmount{}), true, nil
	}
	balance, err := ledger.BotStarsBalance(ctx, botID)
	if err != nil {
		return nil, true, internalErr()
	}
	out := emptyStarsStatus(&tg.StarsAmount{Amount: balance})
	// The wallet belongs to a bot, so hand the viewer that user object; the
	// client renders the balance next to the bot it is showing.
	if bot, found, err := r.deps.Users.ByID(ctx, userID, botID); err == nil && found {
		out.Users = tgUsersForViewer(userID, []domain.User{bot})
	}
	return out, true, nil
}

// botStarsWalletReader is the optional bot-wallet projection. Gateways without
// the lifecycle store keep returning the zeroed compatibility response.
type botStarsWalletReader interface {
	BotStarsBalance(ctx context.Context, botUserID int64) (int64, error)
	BotStarsOverallRevenue(ctx context.Context, botUserID int64) (int64, error)
	BotStarsRevenueSeries(ctx context.Context, botUserID int64, fromUnix, toUnix int) ([]domain.BotStarsRevenuePoint, error)
	BotStarsTransactions(ctx context.Context, botUserID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error)
}

// botStarsWalletTransactions serves payments.getStarsTransactions for a bot
// wallet peer. handled=false means the peer is not a bot wallet and the caller
// must fall through to the personal/channel path.
//
// The TON wallet is a different bucket that telesrv does not model, so a
// ton=true request returns a zeroed envelope instead of an XTR history.
func (r *Router) botStarsWalletTransactions(ctx context.Context, req *tg.PaymentsGetStarsTransactionsRequest) (*tg.PaymentsStarsStatus, bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, true, internalErr()
	}
	// handled must stay true on an error: the caller only propagates the error
	// when it considers the peer handled, so a false here would silently drop a
	// permission denial and fall through to the personal-user path.
	botID, _, ok, err := r.botStarsWalletOwner(ctx, userID, req.Peer)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		return nil, false, nil
	}
	ton := req.GetTon()
	if ton {
		return emptyStarsStatus(&tg.StarsTonAmount{}), true, nil
	}
	query, err := starsTransactionQuery(req)
	if err != nil {
		return nil, true, err
	}
	ledger, supported := r.deps.Gifts.(botStarsWalletReader)
	if !supported {
		return emptyStarsStatus(&tg.StarsAmount{}), true, nil
	}
	page, err := ledger.BotStarsTransactions(ctx, botID, query)
	if err != nil {
		return nil, true, internalErr()
	}
	out := emptyStarsStatus(&tg.StarsAmount{Amount: page.Balance})
	if txns := tgStarsTransactions(page.Transactions); len(txns) > 0 {
		out.SetHistory(txns)
	}
	// The last page must omit next_offset, otherwise the client pages forever.
	if page.NextOffset != "" {
		out.SetNextOffset(page.NextOffset)
	}
	// Counterparties are payers, so the viewer needs their user objects or the
	// history renders as bare ids.
	if len(page.Transactions) > 0 {
		userIDs := make([]int64, 0, len(page.Transactions))
		for _, txn := range page.Transactions {
			if txn.Peer.Type == domain.PeerTypeUser {
				userIDs = append(userIDs, txn.Peer.ID)
			}
		}
		out.Users = tgUsersForViewer(userID, r.domainUsersForIDs(ctx, userID, uniqueInt64(userIDs)))
	}
	return out, true, nil
}

// botStarsWalletOwner resolves an input peer to the bot whose wallet the
// viewer is allowed to read.
//
// ok=false means the peer is not a bot wallet at all (a plain user or a
// channel), so the caller falls back to its normal path. A bot wallet the
// viewer does not own is an error rather than a zeroed stub, so an operator
// cannot mistake "not yours" for "you earned nothing".
// botStarsWalletOwner returns the bot id plus its creation day, which anchors
// the revenue chart. A zero createdAt means the record predates the field and
// the caller falls back to a single-day window.
func (r *Router) botStarsWalletOwner(ctx context.Context, viewerID int64, input tg.InputPeerClass) (botUserID int64, createdAt time.Time, ok bool, err error) {
	owner, err := r.checkedDomainPeerFromInputPeer(ctx, viewerID, input)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	// A plain user peer has no separate revenue bucket: getStarsStatus is the
	// method that exposes a principal's own spendable balance.
	if owner.Type != domain.PeerTypeUser || owner.ID == viewerID {
		return 0, time.Time{}, false, nil
	}
	if r.deps.Users == nil || r.deps.Bots == nil {
		return 0, time.Time{}, false, nil
	}
	bot, found, err := r.deps.Users.ByID(ctx, viewerID, owner.ID)
	if err != nil {
		return 0, time.Time{}, false, internalErr()
	}
	if !found || !bot.Bot {
		return 0, time.Time{}, false, nil
	}
	owns, err := r.deps.Bots.OwnsBot(ctx, viewerID, owner.ID)
	if err != nil {
		return 0, time.Time{}, false, internalErr()
	}
	if !owns {
		return 0, time.Time{}, false, tgerr.New(403, "USER_PERMISSION_DENIED")
	}
	return owner.ID, bot.CreatedAt, true, nil
}

type channelRevenueWithdrawalIssuer interface {
	ChannelRevenueWithdrawalAvailable() bool
	IssueChannelRevenueWithdrawal(ctx context.Context, req domain.ChannelRevenueWithdrawalRequest) (domain.ChannelRevenueWithdrawal, error)
}

type channelRevenueOverallReader interface {
	ChannelStarsOverallRevenue(ctx context.Context, channelID int64) (int64, error)
	ChannelTonOverallRevenue(ctx context.Context, channelID int64) (int64, error)
}

func (r *Router) onPaymentsGetStarsRevenueWithdrawalURL(ctx context.Context, req *tg.PaymentsGetStarsRevenueWithdrawalURLRequest) (*tg.PaymentsStarsRevenueWithdrawalURL, error) {
	if req == nil || r.deps.Account == nil || r.deps.Auth == nil || r.deps.Channels == nil {
		return nil, peerIDInvalidErr()
	}
	issuer, ok := r.deps.Gifts.(channelRevenueWithdrawalIssuer)
	if !ok || !issuer.ChannelRevenueWithdrawalAvailable() {
		return nil, tgerr.New(400, "STARS_REVENUE_WITHDRAWAL_UNAVAILABLE")
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	owner, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if owner.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, owner.ID)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	// Revenue belongs to the channel entity. Only its current creator may bind
	// a claim to their personal ledger; PostMessages admins remain read-only.
	if view.Self.Role != domain.ChannelRoleCreator || view.Channel.CreatorUserID != userID {
		return nil, tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	}
	passwordState, err := r.deps.Account.RevenueWithdrawalPasswordState(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	if !passwordState.HasPassword {
		return nil, tgerr.New(400, "PASSWORD_MISSING")
	}
	if _, ok := req.Password.(*tg.InputCheckPasswordSRP); !ok {
		return nil, passwordHashInvalidErr()
	}
	if err := r.deps.Account.CheckPassword(ctx, userID, domainPasswordCheck(req.Password)); err != nil {
		return nil, passwordErr(err)
	}
	now := r.clock.Now()
	if wait := revenueWithdrawalFreshWait(passwordState.PasswordChangedAt, now); wait < 0 {
		return nil, internalErr()
	} else if wait > 0 {
		return nil, tgerr.New(400, fmt.Sprintf("PASSWORD_TOO_FRESH_%d", wait))
	}
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok || authKeyID == ([8]byte{}) {
		return nil, authKeyUnregisteredErr()
	}
	authorization, found, err := r.deps.Auth.Authorization(ctx, authKeyID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || authorization.AuthKeyID != authKeyID || authorization.UserID != userID || authorization.PasswordPending {
		return nil, authKeyUnregisteredErr()
	}
	if wait := revenueWithdrawalFreshWait(authorization.CreatedAt, now); wait < 0 {
		return nil, internalErr()
	} else if wait > 0 {
		return nil, tgerr.New(400, fmt.Sprintf("SESSION_TOO_FRESH_%d", wait))
	}
	amount, hasAmount := req.GetAmount()
	if hasAmount && amount <= 0 {
		return nil, starsAmountInvalidErr()
	}
	if !hasAmount {
		amount = 0
	}
	currency := domain.ChannelRevenueStars
	if req.GetTon() {
		currency = domain.ChannelRevenueTON
	}
	issued, err := issuer.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: owner.ID, CreatorUserID: userID, Currency: currency, Amount: amount,
		PasswordChangedAt: passwordState.PasswordChangedAt, AuthKeyID: authKeyID,
		AuthorizationCreatedAt: authorization.CreatedAt, Date: int(now.Unix()),
	})
	if err != nil {
		var passwordStateChanged *domain.ChannelRevenuePasswordStateChangedError
		var authorizationStateChanged *domain.ChannelRevenueAuthorizationStateChangedError
		switch {
		case errors.As(err, &passwordStateChanged):
			if !passwordStateChanged.HasPassword {
				return nil, tgerr.New(400, "PASSWORD_MISSING")
			}
			if wait := revenueWithdrawalFreshWait(passwordStateChanged.PasswordChangedAt, now); wait > 0 {
				return nil, tgerr.New(400, fmt.Sprintf("PASSWORD_TOO_FRESH_%d", wait))
			}
			return nil, internalErr()
		case errors.As(err, &authorizationStateChanged):
			if !authorizationStateChanged.HasAuthorization || !authorizationStateChanged.OwnerMatches || authorizationStateChanged.PasswordPending {
				return nil, authKeyUnregisteredErr()
			}
			if wait := revenueWithdrawalFreshWait(authorizationStateChanged.CreatedAt, now); wait > 0 {
				return nil, tgerr.New(400, fmt.Sprintf("SESSION_TOO_FRESH_%d", wait))
			}
			return nil, internalErr()
		case errors.Is(err, domain.ErrChannelRevenueInsufficient):
			return nil, balanceTooLowErr()
		case errors.Is(err, domain.ErrChannelRevenueWithdrawalInvalid):
			return nil, starsAmountInvalidErr()
		default:
			return nil, internalErr()
		}
	}
	if issued.URL == "" {
		return nil, internalErr()
	}
	return &tg.PaymentsStarsRevenueWithdrawalURL{URL: issued.URL}, nil
}

const revenueWithdrawalFreshness = 24 * time.Hour

// revenueWithdrawalFreshWait returns -1 when the durable timestamp is missing.
// Otherwise it rounds up so the client cannot retry one fractional second early.
func revenueWithdrawalFreshWait(changedAt, now time.Time) int {
	if changedAt.IsZero() || now.IsZero() {
		return -1
	}
	remaining := changedAt.Add(revenueWithdrawalFreshness).Sub(now)
	if remaining <= 0 {
		return 0
	}
	return int((remaining + time.Second - 1) / time.Second)
}

type channelGiftLedgerReader interface {
	ChannelStarsBalance(ctx context.Context, channelID int64) (int64, error)
	ChannelStarsTransactions(ctx context.Context, channelID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error)
	ChannelTonBalance(ctx context.Context, channelID int64) (int64, error)
	ChannelTonTransactions(ctx context.Context, channelID int64, query domain.StarsTransactionQuery) (domain.TonTransactionPage, error)
}

// onPaymentsGetStarsStatus 返回请求 peer 的 Stars/本地 TON 余额。个人与频道账本
// 严格隔离；频道读取要求 Star Gift 管理权限，不能把频道收益投影到执行 RPC 的管理员。
// 响应必须是 payments.starsStatus（balance/chats/users 都是必填，空 vector 即可）——
// 两端客户端无条件读取 balance（DrKLO StarsAmount 反序列化 / TDesktop vbalance()）。
func (r *Router) onPaymentsGetStarsStatus(ctx context.Context, req *tg.PaymentsGetStarsStatusRequest) (*tg.PaymentsStarsStatus, error) {
	if req == nil {
		return nil, peerIDInvalidErr()
	}
	// A bot wallet is addressed by the bot peer, which the shared star-gift
	// owner check rejects because it admits only self and channels.
	//
	// TDesktop reaches the bot earn screen through this method: it renders the
	// wallet balance in the bot profile and hides the whole balance section
	// while that balance is zero. Without this branch the request fails, the
	// section stays hidden and the revenue chart is never even requested.
	if status, handled, err := r.botStarsWalletStatus(ctx, req); handled {
		return status, err
	}
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

// onPaymentsGetStarsSubscriptions returns the authoritative current balance
// with an empty subscription page. telesrv does not create recurring Stars
// subscriptions yet; returning a well-shaped terminal page lets both official
// clients finish loading the Stars screen without inventing subscription state.
func (r *Router) onPaymentsGetStarsSubscriptions(ctx context.Context, req *tg.PaymentsGetStarsSubscriptionsRequest) (*tg.PaymentsStarsStatus, error) {
	if req == nil || len(req.Offset) > domain.MaxStarsTransactionsOffsetBytes {
		return nil, inputRequestInvalidErr()
	}
	userID, owner, err := r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	if owner.Type != domain.PeerTypeUser || owner.ID != userID {
		return nil, peerIDInvalidErr()
	}
	if r.deps.Stars == nil {
		return emptyStarsStatus(&tg.StarsAmount{}), nil
	}
	balance, err := r.deps.Stars.GetBalance(ctx, userID)
	if err != nil {
		return nil, starsErr(err)
	}
	return emptyStarsStatus(&tg.StarsAmount{Amount: balance.Balance}), nil
}

// onPaymentsGetStarsTransactions 返回 keyset 分页的 Stars 流水（同 starsStatus 信封）。
// 末页必须省略 next_offset（flag 不置），否则 DrKLO 会无限翻页。
func (r *Router) onPaymentsGetStarsTransactions(ctx context.Context, req *tg.PaymentsGetStarsTransactionsRequest) (*tg.PaymentsStarsStatus, error) {
	if req == nil {
		return nil, peerIDInvalidErr()
	}
	// A bot wallet is addressed by the bot peer itself, which the shared
	// star-gift owner check rejects because it only admits self and channels.
	// Resolve it first so the bot history reaches the same starsStatus envelope
	// the official Stars screen pages through.
	if status, handled, err := r.botStarsWalletTransactions(ctx, req); handled {
		return status, err
	}
	userID, owner, err := r.starGiftTransactionLedgerOwner(ctx, req)
	if err != nil {
		return nil, err
	}
	query, err := starsTransactionQuery(req)
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
		if ton {
			page, err := ledger.ChannelTonTransactions(ctx, owner.ID, query)
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
		page, err := ledger.ChannelStarsTransactions(ctx, owner.ID, query)
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
		page, err := r.deps.Gifts.TonTransactions(ctx, userID, query)
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
	page, err := r.deps.Stars.ListTransactions(ctx, userID, query)
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

func starsTransactionQuery(req *tg.PaymentsGetStarsTransactionsRequest) (domain.StarsTransactionQuery, error) {
	if req == nil {
		return domain.StarsTransactionQuery{}, inputRequestInvalidErr()
	}
	inbound, outbound := req.GetInbound(), req.GetOutbound()
	if inbound && outbound {
		return domain.StarsTransactionQuery{}, inputRequestInvalidErr()
	}
	if _, ok := req.GetSubscriptionID(); ok {
		// Stars subscriptions are not part of the current business model. Do not
		// silently return the unfiltered ledger for a requested subscription.
		return domain.StarsTransactionQuery{}, subscriptionIDInvalidErr()
	}
	direction := domain.StarsTransactionDirectionAll
	if inbound {
		direction = domain.StarsTransactionDirectionIncoming
	} else if outbound {
		direction = domain.StarsTransactionDirectionOutgoing
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxStarsTransactionsLimit {
		limit = domain.MaxStarsTransactionsLimit
	}
	return domain.StarsTransactionQuery{
		Offset:    req.Offset,
		Limit:     limit,
		Direction: direction,
		Ascending: req.GetAscending(),
	}, nil
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
		case domain.StarsReasonPaidMessage:
			item.SetPaidMessages(1)
		case domain.StarsReasonGift:
			item.Gift = true
		case domain.StarsReasonGiftUpgrade:
			// Telegram Desktop treats stargift_upgrade as a promise that the
			// optional stargift field contains a unique gift and immediately
			// dereferences its model document while building the history row.
			// The compact ledger projection currently has no StarGift payload,
			// so advertising the flag produces a client-side access violation.
			// Keep the transaction visible through its title/description and only
			// restore this flag together with a complete unique-gift projection.
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
		case domain.StarsReasonPremium:
			if t.PremiumMonths > 0 {
				item.SetPremiumGiftMonths(t.PremiumMonths)
			}
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
	case domain.StarsReasonGrant, domain.StarsReasonTopup, domain.StarsReasonDonationDeposit:
		return &tg.StarsTransactionPeerFragment{}
	case domain.StarsReasonPremium:
		return &tg.StarsTransactionPeerPremiumBot{}
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
