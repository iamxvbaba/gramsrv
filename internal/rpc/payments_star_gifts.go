package rpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
)

// Star gift（payments.* 礼物 RPC）：目录 / 购买表单 / 发送 / 收礼列表 / 展示切换 / 转换回 Stars。
// 扣费经原子礼物聚合账本；用户礼物走私聊服务消息，频道礼物落 saved gift/admin log，
// 并由持久 notification job 向启用通知的礼物管理员投递私聊 service message。

func starGiftInvalidErr() error { return tgerr.New(400, "STARGIFT_INVALID") }

// starGiftSlugInvalidErr 对齐官方 payments.getUniqueStarGiftValueInfo 的
// STARGIFT_SLUG_INVALID：只有“slug 本身无效”用它，其它语义仍走 STARGIFT_INVALID。
func starGiftSlugInvalidErr() error { return tgerr.New(400, "STARGIFT_SLUG_INVALID") }

func devStarsTopupOptions() []tg.StarsTopupOption {
	return []tg.StarsTopupOption{
		{Stars: 1000, Currency: "USD", Amount: 99},
		{Stars: 2500, Currency: "USD", Amount: 199},
		{Stars: 5000, Currency: "USD", Amount: 399},
	}
}

func devStarsGiftOptions() []tg.StarsGiftOption {
	// No store_product is advertised: telesrv has no Google/Apple product and
	// sideloaded Android clients must use the invoice checkout path.
	return []tg.StarsGiftOption{
		{Stars: 1000, Currency: "USD", Amount: 99},
		{Stars: 2500, Currency: "USD", Amount: 199},
		{Stars: 5000, Currency: "USD", Amount: 399},
	}
}

func devStarsGiveawayOptions() []tg.StarsGiveawayOption {
	return []tg.StarsGiveawayOption{
		{Default: true, Stars: 1000, YearlyBoosts: 4, Currency: "USD", Amount: 99, Winners: []tg.StarsGiveawayWinnersOption{
			{Default: true, Users: 1, PerUserStars: 1000}, {Users: 2, PerUserStars: 500},
			{Users: 5, PerUserStars: 200}, {Users: 10, PerUserStars: 100},
		}},
		{Stars: 2500, YearlyBoosts: 10, Currency: "USD", Amount: 199, Winners: []tg.StarsGiveawayWinnersOption{
			{Default: true, Users: 1, PerUserStars: 2500}, {Users: 5, PerUserStars: 500}, {Users: 10, PerUserStars: 250},
		}},
		{Stars: 5000, YearlyBoosts: 20, Currency: "USD", Amount: 399, Winners: []tg.StarsGiveawayWinnersOption{
			{Default: true, Users: 1, PerUserStars: 5000}, {Users: 5, PerUserStars: 1000}, {Users: 10, PerUserStars: 500},
		}},
	}
}

func (r *Router) devStarsFiatPaymentForm(
	buyerUserID int64,
	form domain.StarsPurchaseForm,
	title, description string,
	users []domain.User,
) *tg.PaymentsPaymentForm {
	return &tg.PaymentsPaymentForm{
		FormID: form.FormID,
		BotID:  domain.OfficialSystemUserID,
		Title:  title, Description: description,
		Invoice: tg.Invoice{
			Test: true, Currency: form.Currency,
			Prices: []tg.LabeledPrice{{Label: branding.StarsName(), Amount: form.Amount}},
		},
		ProviderID: domain.OfficialSystemUserID,
		URL: r.publicLinkQuery("payments/dev-stars", url.Values{
			"form_id": []string{strconv.FormatInt(form.FormID, 10)},
		}),
		Users: r.tgUsersForViewer(buyerUserID, users),
	}
}

func validDevStarsPaymentCredentials(credentials tg.InputPaymentCredentialsClass, formID int64) bool {
	value, ok := credentials.(*tg.InputPaymentCredentials)
	if !ok || value == nil || value.Save || formID == 0 {
		return false
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(value.Data.Data), &payload); err != nil || len(payload) != 2 {
		return false
	}
	return payload["type"] == "telesrv_dev" && payload["form_id"] == strconv.FormatInt(formID, 10)
}

// devPaymentsErr reports a denied dev/fiat purchase. The local checkout is
// denied by default (AllowDevPayments); the purchase simply does not exist
// here, matching the NOT_IMPLEMENTED policy used for unconfigured purchases.
func (r *Router) devPaymentsErr() error {
	if r.cfg.AllowDevPayments {
		return nil
	}
	return notImplementedErr()
}

func (r *Router) onPaymentsGetStarsGiveawayOptions(ctx context.Context) ([]tg.StarsGiveawayOption, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return devStarsGiveawayOptions(), nil
}

func (r *Router) onPaymentsGetGiveawayInfo(ctx context.Context, req *tg.PaymentsGetGiveawayInfoRequest) (tg.PaymentsGiveawayInfoClass, error) {
	if req == nil || req.MsgID <= 0 {
		return nil, messageIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID <= 0 {
		return nil, peerIDInvalidErr()
	}
	service, ok := r.deps.Stars.(starsGiveawayInfoService)
	if !ok {
		return nil, notImplementedErr()
	}
	info, err := service.GetGiveawayInfo(ctx, userID, peer.ID, req.MsgID, int(r.clock.Now().Unix()))
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return nil, messageIDInvalidErr()
		}
		return nil, starsPurchaseErr(err)
	}
	out := &tg.PaymentsGiveawayInfo{StartDate: info.StartDate}
	if info.Participating {
		out.SetParticipating(true)
	}
	if info.PreparingResults {
		out.SetPreparingResults(true)
	}
	if info.JoinedTooEarlyDate > 0 {
		out.SetJoinedTooEarlyDate(info.JoinedTooEarlyDate)
	}
	if info.AdminDisallowedChatID > 0 {
		out.SetAdminDisallowedChatID(info.AdminDisallowedChatID)
	}
	if info.DisallowedCountry != "" {
		out.SetDisallowedCountry(info.DisallowedCountry)
	}
	return out, nil
}

type starsPurchaseService interface {
	IssuePurchaseForm(context.Context, domain.StarsPurchaseForm) (domain.StarsPurchaseForm, error)
	Purchase(context.Context, domain.StarsPurchaseRequest) (domain.StarsPurchaseResult, error)
}

type starsGiveawayInfoService interface {
	GetGiveawayInfo(context.Context, int64, int64, int, int) (domain.StarsGiveawayInfo, error)
}

func userGiftUnavailableErr() error { return tgerr.New(400, "USER_GIFT_UNAVAILABLE") }

func (r *Router) onPaymentsGetStarsGiftOptions(ctx context.Context, req *tg.PaymentsGetStarsGiftOptionsRequest) ([]tg.StarsGiftOption, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req != nil {
		if input, ok := req.GetUserID(); ok {
			if _, err := r.starsGiftRecipient(ctx, userID, input); err != nil {
				return nil, err
			}
		}
	}
	return devStarsGiftOptions(), nil
}

func (r *Router) starsGiftRecipient(ctx context.Context, buyerUserID int64, input tg.InputUserClass) (domain.User, error) {
	if inputUserIsEmpty(input) || r.deps.Users == nil {
		return domain.User{}, userIDInvalidErr()
	}
	recipient, found, err := r.userFromInput(ctx, buyerUserID, input)
	if err != nil {
		return domain.User{}, internalErr()
	}
	if !found || recipient.ID <= 0 || recipient.Deleted {
		return domain.User{}, userIDInvalidErr()
	}
	if recipient.ID == buyerUserID {
		return domain.User{}, userGiftUnavailableErr()
	}
	blocked, err := r.peerBlocksUser(ctx, buyerUserID, recipient.ID)
	if err != nil {
		return domain.User{}, err
	}
	if blocked {
		return domain.User{}, userGiftUnavailableErr()
	}
	return recipient, nil
}

func validateStarsGiftOption(purpose *tg.InputStorePaymentStarsGift) (tg.StarsGiftOption, error) {
	if purpose == nil || purpose.UserID == nil || purpose.Stars <= 0 || purpose.Amount <= 0 || purpose.Currency == "" {
		return tg.StarsGiftOption{}, starsAmountInvalidErr()
	}
	for _, option := range devStarsGiftOptions() {
		if option.Stars == purpose.Stars && option.Currency == purpose.Currency && option.Amount == purpose.Amount {
			return option, nil
		}
	}
	return tg.StarsGiftOption{}, starsFormAmountMismatchErr()
}

func starsGiftPurpose(inv *tg.InputInvoiceStars) (*tg.InputStorePaymentStarsGift, bool) {
	if inv == nil {
		return nil, false
	}
	purpose, ok := inv.Purpose.(*tg.InputStorePaymentStarsGift)
	return purpose, ok && purpose != nil
}

func starsGiveawayPurpose(inv *tg.InputInvoiceStars) (*tg.InputStorePaymentStarsGiveaway, bool) {
	if inv == nil {
		return nil, false
	}
	purpose, ok := inv.Purpose.(*tg.InputStorePaymentStarsGiveaway)
	return purpose, ok && purpose != nil
}

func (r *Router) validateStarsGiveawayPurpose(ctx context.Context, userID int64, purpose *tg.InputStorePaymentStarsGiveaway) (tg.StarsGiveawayOption, *domain.StarsGiveawayPurchase, error) {
	if purpose == nil || purpose.Stars <= 0 || purpose.Amount <= 0 || purpose.Currency == "" ||
		purpose.RandomID == 0 || purpose.Users <= 0 || purpose.UntilDate <= 0 || purpose.BoostPeer == nil {
		return tg.StarsGiveawayOption{}, nil, purposeInvalidErr()
	}
	var matched tg.StarsGiveawayOption
	var perUserStars int64
	found := false
	for _, option := range devStarsGiveawayOptions() {
		if option.Stars != purpose.Stars || option.Currency != purpose.Currency || option.Amount != purpose.Amount {
			continue
		}
		for _, winners := range option.Winners {
			if winners.Users == purpose.Users {
				matched, perUserStars, found = option, winners.PerUserStars, true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return tg.StarsGiveawayOption{}, nil, starsFormAmountMismatchErr()
	}
	now := int(r.clock.Now().Unix())
	if purpose.UntilDate <= now || purpose.UntilDate > now+7*24*60*60 || len(purpose.AdditionalPeers) > 10 ||
		len(purpose.CountriesISO2) > 10 || len([]rune(purpose.PrizeDescription)) > 128 {
		return tg.StarsGiveawayOption{}, nil, purposeInvalidErr()
	}
	boostPeer, err := r.starsGiveawayAdminChannel(ctx, userID, purpose.BoostPeer)
	if err != nil {
		return tg.StarsGiveawayOption{}, nil, err
	}
	additional := make([]domain.Peer, 0, len(purpose.AdditionalPeers))
	seenChannels := map[int64]struct{}{boostPeer.ID: struct{}{}}
	for _, input := range purpose.AdditionalPeers {
		peer, err := r.starsGiveawayAdminChannel(ctx, userID, input)
		if err != nil {
			return tg.StarsGiveawayOption{}, nil, err
		}
		if _, exists := seenChannels[peer.ID]; exists {
			return tg.StarsGiveawayOption{}, nil, purposeInvalidErr()
		}
		seenChannels[peer.ID] = struct{}{}
		additional = append(additional, peer)
	}
	countries := append([]string(nil), purpose.CountriesISO2...)
	seenCountries := make(map[string]struct{}, len(countries))
	for _, country := range countries {
		if len(country) != 2 || country != strings.ToUpper(country) || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return tg.StarsGiveawayOption{}, nil, purposeInvalidErr()
		}
		if _, exists := seenCountries[country]; exists {
			return tg.StarsGiveawayOption{}, nil, purposeInvalidErr()
		}
		seenCountries[country] = struct{}{}
	}
	return matched, &domain.StarsGiveawayPurchase{
		BoostPeer: boostPeer, AdditionalPeers: additional, CountriesISO2: countries,
		PrizeDescription: purpose.PrizeDescription, RandomID: purpose.RandomID, UntilDate: purpose.UntilDate,
		Users: purpose.Users, PerUserStars: perUserStars, YearlyBoosts: matched.YearlyBoosts,
		OnlyNewSubscribers: purpose.OnlyNewSubscribers, WinnersAreVisible: purpose.WinnersAreVisible,
	}, nil
}

func (r *Router) starsGiveawayAdminChannel(ctx context.Context, userID int64, input tg.InputPeerClass) (domain.Peer, error) {
	if r.deps.Channels == nil {
		return domain.Peer{}, notImplementedErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return domain.Peer{}, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID <= 0 {
		return domain.Peer{}, peerIDInvalidErr()
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, peer.ID)
	if err != nil {
		return domain.Peer{}, channelInvalidErr(err)
	}
	if view.Self.Status != domain.ChannelMemberActive || !channelMemberIsAdmin(view.Self) ||
		(view.Channel.Broadcast && !view.Self.CanPostChannelMessages()) {
		return domain.Peer{}, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	return peer, nil
}

func (r *Router) starsGiveawayPaymentForm(ctx context.Context, buyerUserID int64, purpose *tg.InputStorePaymentStarsGiveaway) (tg.PaymentsPaymentFormClass, error) {
	_, giveaway, err := r.validateStarsGiveawayPurpose(ctx, buyerUserID, purpose)
	if err != nil {
		return nil, err
	}
	service, ok := r.deps.Stars.(starsPurchaseService)
	if !ok {
		return nil, notImplementedErr()
	}
	if err := r.devPaymentsErr(); err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	form, err := service.IssuePurchaseForm(ctx, domain.StarsPurchaseForm{
		Kind: domain.StarsPurchaseGiveaway, BuyerUserID: buyerUserID, Giveaway: giveaway,
		Stars: purpose.Stars, Currency: purpose.Currency, Amount: purpose.Amount,
		IssuedAt: now, ExpiresAt: now + 600,
	})
	if err != nil {
		return nil, starsPurchaseErr(err)
	}
	return r.devStarsFiatPaymentForm(buyerUserID, form,
		"Stars giveaway", "Launch a Stars giveaway",
		[]domain.User{domain.OfficialSystemUser()}), nil
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
		Gifts: tgStarGifts(catalog, r.clock.Now().Unix()),
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{},
	}, nil
}

// onPaymentsGetPaymentForm 处理 Stars 专用 invoice：
//   - inputInvoiceStarGift 返回 paymentFormStarGift。
//   - inputInvoiceStars(top-up/gift/giveaway) 返回普通 test paymentForm，由本地 dev
//     provider WebView 生成 form-bound credentials 后走 sendPaymentForm。
//
// 崩溃约束：star gift invoice 必须返 paymentFormStarGift#b425cfe1（TDesktop 单分支 match），
// Stars 法币购买表单 Invoice.Prices 必须非空且保持套餐 currency/amount；不能返回
// paymentFormStars，否则 TDesktop 会把它解释为花现有 XTR，Android 会打开空 URL WebView。
func (r *Router) onPaymentsGetPaymentForm(ctx context.Context, req *tg.PaymentsGetPaymentFormRequest) (tg.PaymentsPaymentFormClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}

	switch inv := req.Invoice.(type) {
	case *tg.InputInvoiceMessage:
		// A bot's own invoice wins over the Premium path: the premium handler
		// only accepts the built-in Premium bot, so a third-party bot invoice
		// would otherwise be rejected as an unknown invoice.
		if form, err := r.botInvoicePaymentForm(ctx, userID, inv.Peer, inv.MsgID); err == nil {
			return form, nil
		} else if !isInvoiceLookupMiss(err) {
			// Logged as well, so a failure here is distinguishable from the
			// miss path above when reading the server log.
			if r.log != nil {
				r.log.Warn("bot invoice form error",
					zap.Int64("viewer_id", userID),
					zap.Int("msg_id", inv.MsgID),
					zap.Error(err),
				)
			}
			return nil, err
		}
		return r.premiumPaymentForm(ctx, userID, inv)
	case *tg.InputInvoicePremiumGiftStars, *tg.InputInvoicePremiumGiftCode:
		return r.premiumPaymentForm(ctx, userID, inv)
	case *tg.InputInvoiceStars:
		if inv != nil {
			if _, ok := inv.Purpose.(*tg.InputStorePaymentPremiumSubscription); ok {
				return r.premiumPaymentForm(ctx, userID, inv)
			}
		}
	}

	if inv, ok := req.Invoice.(*tg.InputInvoiceStars); ok {
		if purpose, gift := starsGiftPurpose(inv); gift {
			return r.starsGiftPaymentForm(ctx, userID, purpose)
		}
		if purpose, giveaway := starsGiveawayPurpose(inv); giveaway {
			return r.starsGiveawayPaymentForm(ctx, userID, purpose)
		}
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
		return r.starsTopupPaymentForm(ctx, userID, purpose)
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
	var giftMessageEntities []domain.MessageEntity
	if m, ok := inv.GetMessage(); ok {
		giftMessage = m.Text
		giftMessageEntities = domainMessageEntities(m.Entities)
		if !(domain.PremiumGiftMessage{Text: giftMessage, Entities: giftMessageEntities}).Valid() {
			return nil, starGiftInvalidErr()
		}
	}
	now := int(r.clock.Now().Unix())
	form, err := r.deps.Gifts.IssuePurchaseForm(ctx, domain.StarGiftPurchaseForm{
		BuyerUserID: userID, To: peer, GiftID: gift.ID, RevisionID: gift.RevisionID,
		IncludeUpgrade: inv.IncludeUpgrade, HideName: inv.HideName, Message: giftMessage, MessageEntities: giftMessageEntities,
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

// onPaymentsValidateRequestedInfo is the read-only pre-submit gate used by
// TDesktop's ordinary payment-form checkout. Direct Stars purchase invoices
// advertise no requested information or flexible shipping, so a valid result
// deliberately carries neither an ID nor shipping options. Package, recipient
// and giveaway permissions are revalidated without issuing a form or writing
// ledger/message/update state. Other invoice families remain unsupported here
// rather than being accepted as an empty generic bot invoice.
func (r *Router) onPaymentsValidateRequestedInfo(ctx context.Context, req *tg.PaymentsValidateRequestedInfoRequest) (*tg.PaymentsValidatedRequestedInfo, error) {
	if req == nil || req.Invoice == nil {
		return nil, inputRequestInvalidErr()
	}
	// The forms returned by devStarsFiatPaymentForm request no personal data.
	// Reject even present-but-empty flags so a caller cannot make data appear
	// validated when this module neither stores nor forwards it. Save is safe as
	// a no-op only while the information object is exactly empty.
	if !req.Info.Zero() {
		return nil, tgerr.New(400, "REQUESTED_INFO_INVALID")
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if inv, ok := req.Invoice.(*tg.InputInvoicePremiumGiftCode); ok {
		if _, err := r.resolvePremiumInvoice(ctx, userID, inv); err != nil {
			return nil, err
		}
		return &tg.PaymentsValidatedRequestedInfo{}, nil
	}
	inv, ok := req.Invoice.(*tg.InputInvoiceStars)
	if !ok || inv == nil {
		return nil, notImplementedErr()
	}
	if purpose, ok := starsGiftPurpose(inv); ok {
		if _, err := validateStarsGiftOption(purpose); err != nil {
			return nil, err
		}
		if _, err := r.starsGiftRecipient(ctx, userID, purpose.UserID); err != nil {
			return nil, err
		}
		return &tg.PaymentsValidatedRequestedInfo{}, nil
	}
	if purpose, ok := starsGiveawayPurpose(inv); ok {
		if _, _, err := r.validateStarsGiveawayPurpose(ctx, userID, purpose); err != nil {
			return nil, err
		}
		return &tg.PaymentsValidatedRequestedInfo{}, nil
	}
	purpose, ok := starsTopupPurpose(inv)
	if !ok {
		return nil, notImplementedErr()
	}
	if _, _, err := r.validateStarsTopupPurpose(ctx, userID, purpose); err != nil {
		return nil, err
	}
	return &tg.PaymentsValidatedRequestedInfo{}, nil
}

func (r *Router) starsGiftPaymentForm(ctx context.Context, buyerUserID int64, purpose *tg.InputStorePaymentStarsGift) (tg.PaymentsPaymentFormClass, error) {
	if _, err := validateStarsGiftOption(purpose); err != nil {
		return nil, err
	}
	recipient, err := r.starsGiftRecipient(ctx, buyerUserID, purpose.UserID)
	if err != nil {
		return nil, err
	}
	service, ok := r.deps.Stars.(starsPurchaseService)
	if !ok {
		return nil, notImplementedErr()
	}
	if err := r.devPaymentsErr(); err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	form, err := service.IssuePurchaseForm(ctx, domain.StarsPurchaseForm{
		Kind: domain.StarsPurchaseGift, BuyerUserID: buyerUserID, RecipientUserID: recipient.ID,
		Stars: purpose.Stars, Currency: purpose.Currency, Amount: purpose.Amount,
		IssuedAt: now, ExpiresAt: now + 600,
	})
	if err != nil {
		return nil, starsPurchaseErr(err)
	}
	users := []domain.User{domain.OfficialSystemUser(), recipient}
	return r.devStarsFiatPaymentForm(buyerUserID, form,
		"Gift Stars", "Gift Stars to a friend", users), nil
}

// onPaymentsSendStarsForm 只处理真正以 XTR/TON 付款的 Star Gift invoice。
// 直接购买 Stars 是法币 test paymentForm，必须携带 dev provider credentials 走
// sendPaymentForm；禁止把它伪装为 sendStarsForm 后无凭证铸币。
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

	if msgInv, ok := req.Invoice.(*tg.InputInvoiceMessage); ok {
		// A bot invoice is settled here too, not only over sendPaymentForm:
		// TDesktop pays a stars invoice with sendStarsForm, so routing this
		// straight to the Premium handler answered INVOICE_INVALID for every
		// bot invoice. A miss falls through so Premium invoices still work.
		res, handled, err := r.botInvoiceSettle(ctx, userID, &tg.PaymentsSendPaymentFormRequest{
			FormID:  req.FormID,
			Invoice: msgInv,
		}, msgInv)
		if handled {
			return res, err
		}
		return r.sendPremiumStarsForm(ctx, userID, req.FormID, msgInv)
	}

	switch inv := req.Invoice.(type) {
	case *tg.InputInvoicePremiumGiftStars:
		return r.sendPremiumStarsForm(ctx, userID, req.FormID, inv)
	case *tg.InputInvoiceStars:
		if inv != nil {
			if _, ok := inv.Purpose.(*tg.InputStorePaymentPremiumSubscription); ok {
				return r.sendPremiumStarsForm(ctx, userID, req.FormID, inv)
			}
		}
	}

	if _, ok := req.Invoice.(*tg.InputInvoiceStars); ok {
		return nil, tgerr.New(400, "PAYMENT_CREDENTIALS_INVALID")
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
	if gift.SupportOnly && !r.viewerSupport(ctx, userID) {
		return nil, tgerr.New(400, "STARGIFT_USAGE_LIMITED")
	}
	upgradeStars := int64(0)
	if inv.IncludeUpgrade {
		if gift.UpgradeStars <= 0 || gift.UpgradeIssued >= gift.UpgradeTotal {
			return nil, starGiftInvalidErr()
		}
		upgradeStars = gift.UpgradeStars
	}
	giftMessage := ""
	var giftMessageEntities []domain.MessageEntity
	if m, ok := inv.GetMessage(); ok {
		giftMessage = m.Text
		giftMessageEntities = domainMessageEntities(m.Entities)
		if !(domain.PremiumGiftMessage{Text: giftMessage, Entities: giftMessageEntities}).Valid() {
			return nil, starGiftInvalidErr()
		}
	}
	now := int(r.clock.Now().Unix())
	purchaseReq := domain.StarGiftPurchaseRequest{BuyerUserID: userID, BuyerPremium: buyerPremium, To: peer,
		GiftID: gift.ID, RevisionID: gift.RevisionID, IncludeUpgrade: inv.IncludeUpgrade, HideName: inv.HideName,
		Message: giftMessage, MessageEntities: giftMessageEntities,
		ChargeStars: gift.Stars + upgradeStars, FormID: req.FormID, CommandKey: fmt.Sprintf("purchase:%d", req.FormID), Date: now,
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionIDOrZero(ctx)}
	recipientBlocked := false
	recipientUnsaved := false
	if peer.Type == domain.PeerTypeUser {
		recipientBlocked, err = r.peerBlocksUser(ctx, userID, peer.ID)
		if err != nil {
			return nil, internalErr()
		}
		recipientUnsaved, err = r.starGiftRecipientUnsaved(ctx, userID, peer)
		if err != nil {
			return nil, err
		}
	}
	if capability, ok := r.deps.Gifts.(interface{ AtomicPurchaseConfigured() bool }); ok && !capability.AtomicPurchaseConfigured() {
		if err := r.deps.Gifts.ValidatePurchaseForm(ctx, purchaseReq); err != nil {
			return nil, starGiftLifecycleErr(err)
		}
		if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
			return nil, starsErr(err)
		}
		return r.sendStarGiftMemoryPurchase(ctx, userID, peer, gift, inv, giftMessage, giftMessageEntities, upgradeStars)
	}
	if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
		return nil, starsErr(err)
	}
	purchaseReq.RecipientBlocked = recipientBlocked
	purchaseReq.RecipientUnsaved = recipientUnsaved
	result, err := r.deps.Gifts.Purchase(ctx, purchaseReq)
	if err != nil {
		return nil, starGiftLifecycleErr(err)
	}
	updates := r.starGiftSendUpdates(ctx, userID, result.Send)
	appendStarGiftBalanceUpdate(updates, domain.StarGiftCurrencyStars, result.Balance.Balance)
	r.invalidateStarGiftOwner(peer)
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

// onPaymentsSendPaymentForm is the common Android/TDesktop fiat checkout path.
// The local dev provider does not charge an external card; its WebView emits a
// form-bound marker, and the persisted form/purpose remains the authoritative
// package and idempotency boundary.
func (r *Router) onPaymentsSendPaymentForm(ctx context.Context, req *tg.PaymentsSendPaymentFormRequest) (tg.PaymentsPaymentResultClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	// A bot invoice is settled for real Stars, so it is handled before the
	// dev-payment gates below: those require a local WebView provider and
	// form-bound credentials, which an XTR purchase never carries.
	if inv, ok := req.Invoice.(*tg.InputInvoiceMessage); ok {
		if result, settled, err := r.botInvoiceSettle(ctx, userID, req, inv); settled {
			return result, err
		}
	}
	if err := r.devPaymentsErr(); err != nil {
		return nil, err
	}
	if !validDevStarsPaymentCredentials(req.Credentials, req.FormID) {
		return nil, tgerr.New(400, "PAYMENT_CREDENTIALS_INVALID")
	}
	if _, present := req.GetRequestedInfoID(); present || req.RequestedInfoID != "" {
		return nil, tgerr.New(400, "REQUESTED_INFO_ID_INVALID")
	}
	if _, present := req.GetShippingOptionID(); present || req.ShippingOptionID != "" {
		return nil, tgerr.New(400, "SHIPPING_OPTION_INVALID")
	}
	if _, present := req.GetTipAmount(); present || req.TipAmount != 0 {
		return nil, tgerr.New(400, "TIP_AMOUNT_INVALID")
	}
	if inv, ok := req.Invoice.(*tg.InputInvoicePremiumGiftCode); ok {
		return r.sendPremiumStarsForm(ctx, userID, req.FormID, inv)
	}
	inv, ok := req.Invoice.(*tg.InputInvoiceStars)
	if !ok {
		return nil, notImplementedErr()
	}
	if purpose, ok := starsGiftPurpose(inv); ok {
		return r.sendStarsGiftPurchase(ctx, userID, req.FormID, purpose)
	}
	if purpose, ok := starsGiveawayPurpose(inv); ok {
		return r.sendStarsGiveawayPurchase(ctx, userID, req.FormID, purpose)
	}
	return r.sendStarsTopupForm(ctx, userID, req.FormID, inv)
}

func (r *Router) sendStarsGiftPurchase(ctx context.Context, buyerUserID, formID int64, purpose *tg.InputStorePaymentStarsGift) (tg.PaymentsPaymentResultClass, error) {
	if formID == 0 {
		return nil, formIDEmptyErr()
	}
	if _, err := validateStarsGiftOption(purpose); err != nil {
		return nil, err
	}
	recipient, err := r.starsGiftRecipient(ctx, buyerUserID, purpose.UserID)
	if err != nil {
		return nil, err
	}
	service, ok := r.deps.Stars.(starsPurchaseService)
	if !ok {
		return nil, notImplementedErr()
	}
	result, err := service.Purchase(ctx, domain.StarsPurchaseRequest{
		StarsPurchaseForm: domain.StarsPurchaseForm{
			FormID: formID, Kind: domain.StarsPurchaseGift, BuyerUserID: buyerUserID, RecipientUserID: recipient.ID,
			Stars: purpose.Stars, Currency: purpose.Currency, Amount: purpose.Amount,
		},
		Date: int(r.clock.Now().Unix()), OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionIDOrZero(ctx),
	})
	if err != nil {
		return nil, starsPurchaseErr(err)
	}
	updates := r.starGiftSendUpdates(ctx, buyerUserID, result.Send)
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

func (r *Router) sendStarsGiveawayPurchase(ctx context.Context, buyerUserID, formID int64, purpose *tg.InputStorePaymentStarsGiveaway) (tg.PaymentsPaymentResultClass, error) {
	if formID == 0 {
		return nil, formIDEmptyErr()
	}
	_, giveaway, err := r.validateStarsGiveawayPurpose(ctx, buyerUserID, purpose)
	if err != nil {
		return nil, err
	}
	service, ok := r.deps.Stars.(starsPurchaseService)
	if !ok {
		return nil, notImplementedErr()
	}
	result, err := service.Purchase(ctx, domain.StarsPurchaseRequest{
		StarsPurchaseForm: domain.StarsPurchaseForm{
			FormID: formID, Kind: domain.StarsPurchaseGiveaway, BuyerUserID: buyerUserID, Giveaway: giveaway,
			Stars: purpose.Stars, Currency: purpose.Currency, Amount: purpose.Amount,
		},
		Date: int(r.clock.Now().Unix()), OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionIDOrZero(ctx),
	})
	if err != nil {
		if errors.Is(err, domain.ErrChannelInvalid) || errors.Is(err, domain.ErrChannelPrivate) ||
			errors.Is(err, domain.ErrChannelWriteForbidden) || errors.Is(err, domain.ErrChannelAdminRequired) ||
			errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
			return nil, channelInvalidErr(err)
		}
		return nil, starsPurchaseErr(err)
	}
	updates := r.channelMessageUpdatesWithPeerCache(ctx, buyerUserID, result.ChannelSend, 0, newViewerPeerCache(r))
	if updates == nil {
		updates = &tg.Updates{Date: int(r.clock.Now().Unix())}
	}
	if !result.Duplicate {
		r.enqueueChannelMessageFanout(ctx, buyerUserID, result.ChannelSend, nil)
		r.pushChannelDiscussionUpdate(ctx, buyerUserID, result.ChannelSend.Discussion)
	}
	return &tg.PaymentsPaymentResult{Updates: updates}, nil
}

func starsPurchaseErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStarsPurchaseFormExpired):
		return tgerr.New(400, "FORM_EXPIRED")
	case errors.Is(err, domain.ErrStarsPurchaseFormInvalid):
		return starsFormAmountMismatchErr()
	case errors.Is(err, domain.ErrStarsGiftUnavailable):
		return userGiftUnavailableErr()
	default:
		return internalErr()
	}
}

func (r *Router) sendStarGiftMemoryPurchase(ctx context.Context, userID int64, peer domain.Peer, gift domain.StarGift,
	inv *tg.InputInvoiceStarGift, giftMessage string, giftMessageEntities []domain.MessageEntity, upgradeStars int64) (tg.PaymentsPaymentResultClass, error) {
	purchaseStars := gift.Stars + upgradeStars
	balance, err := r.deps.Stars.Debit(ctx, userID, purchaseStars, domain.StarsReasonGift, peer, "Star gift", gift.Title)
	if err != nil {
		return nil, starsErr(err)
	}
	var updates *tg.Updates
	switch peer.Type {
	case domain.PeerTypeUser:
		_, updates, err = r.sendStarGiftToUser(ctx, userID, peer.ID, gift, inv.HideName, giftMessage, giftMessageEntities, upgradeStars)
	case domain.PeerTypeChannel:
		_, updates, err = r.sendStarGiftToChannel(ctx, userID, peer.ID, gift, inv.HideName, giftMessage, giftMessageEntities, upgradeStars)
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

func (r *Router) starsTopupPaymentForm(ctx context.Context, userID int64, purpose *tg.InputStorePaymentStarsTopup) (tg.PaymentsPaymentFormClass, error) {
	service, ok := r.deps.Stars.(starsPurchaseService)
	if !ok {
		return nil, notImplementedErr()
	}
	if err := r.devPaymentsErr(); err != nil {
		return nil, err
	}
	_, peer, err := r.validateStarsTopupPurpose(ctx, userID, purpose)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	form, err := service.IssuePurchaseForm(ctx, domain.StarsPurchaseForm{
		Kind: domain.StarsPurchaseTopup, BuyerUserID: userID,
		SpendPurposePeer: peer,
		Stars:            purpose.Stars, Currency: purpose.Currency, Amount: purpose.Amount,
		IssuedAt: now, ExpiresAt: now + 600,
	})
	if err != nil {
		return nil, starsPurchaseErr(err)
	}
	return r.devStarsFiatPaymentForm(userID, form,
		branding.StarsName(), branding.ProductName()+" dev Stars top-up",
		[]domain.User{domain.OfficialSystemUser()}), nil
}

func (r *Router) sendStarsTopupForm(ctx context.Context, userID, formID int64, inv *tg.InputInvoiceStars) (tg.PaymentsPaymentResultClass, error) {
	purpose, ok := starsTopupPurpose(inv)
	if !ok {
		return nil, notImplementedErr()
	}
	if formID == 0 {
		return nil, formIDEmptyErr()
	}
	service, ok := r.deps.Stars.(starsPurchaseService)
	if !ok {
		return nil, notImplementedErr()
	}
	_, peer, err := r.validateStarsTopupPurpose(ctx, userID, purpose)
	if err != nil {
		return nil, err
	}
	result, err := service.Purchase(ctx, domain.StarsPurchaseRequest{
		StarsPurchaseForm: domain.StarsPurchaseForm{
			FormID: formID, Kind: domain.StarsPurchaseTopup, BuyerUserID: userID,
			SpendPurposePeer: peer,
			Stars:            purpose.Stars, Currency: purpose.Currency, Amount: purpose.Amount,
		},
		Date: int(r.clock.Now().Unix()), OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionIDOrZero(ctx),
	})
	if err != nil {
		return nil, starsPurchaseErr(err)
	}
	return &tg.PaymentsPaymentResult{Updates: starsBalanceUpdates(result.Balance.Balance, r.clock.Now().Unix())}, nil
}

func (r *Router) sendStarGiftToUser(ctx context.Context, senderID, recipientID int64, gift domain.StarGift, hideName bool, message string, messageEntities []domain.MessageEntity, prepaidUpgradeStars int64) (domain.SavedStarGiftRef, *tg.Updates, error) {
	unsaved, err := r.starGiftRecipientUnsaved(ctx, senderID, domain.Peer{Type: domain.PeerTypeUser, ID: recipientID})
	if err != nil {
		return domain.SavedStarGiftRef{}, nil, err
	}
	prepaidUpgradeHash := ""
	if prepaidUpgradeStars == 0 && gift.UpgradeStars > 0 && gift.UpgradeIssued < gift.UpgradeTotal {
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return domain.SavedStarGiftRef{}, nil, err
		}
		prepaidUpgradeHash = base64.RawURLEncoding.EncodeToString(token[:])
	}
	// 2. 投递礼物服务消息到收礼人私聊（双盒 + 推送）。
	send, err := r.deliverStarGift(ctx, senderID, recipientID, gift, hideName, message, messageEntities, prepaidUpgradeStars, prepaidUpgradeHash)
	if err != nil {
		return domain.SavedStarGiftRef{}, nil, err
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
		Unsaved:             unsaved,
		ConvertStars:        gift.ConvertStars,
		PrepaidUpgradeStars: prepaidUpgradeStars,
		PrepaidUpgradeHash:  prepaidUpgradeHash,
		Message:             message,
		MessageEntities:     append([]domain.MessageEntity(nil), messageEntities...),
	}); err != nil {
		return domain.SavedStarGiftRef{}, nil, err
	}
	// 收礼人 stargifts_count 变化 → 失效其 userFull 投影，资料页 Gifts 区段才会出现。
	r.invalidateRPCProjectionForUser(recipientID)

	ref := domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: recipientID}, MsgID: send.RecipientMessage.ID}
	users := r.usersForMessageUpdate(ctx, senderID, send.SenderMessage)
	chats := r.chatsForMessageUpdate(ctx, senderID, send.SenderMessage)
	return ref, tgPrivateMessageUpdates(send.SenderEvent, send.SenderMessage, 0, false, users, chats), nil
}

func (r *Router) sendStarGiftToChannel(ctx context.Context, senderID, channelID int64, gift domain.StarGift, hideName bool, message string, messageEntities []domain.MessageEntity, prepaidUpgradeStars int64) (domain.SavedStarGiftRef, *tg.Updates, error) {
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
			MessageEntities:   append([]domain.MessageEntity(nil), messageEntities...),
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
		MessageEntities:     append([]domain.MessageEntity(nil), messageEntities...),
	})
	if err != nil {
		return domain.SavedStarGiftRef{}, nil, err
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
	ref := domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, SavedID: savedID}
	return ref, nil, nil
}

// deliverStarGift 经 SendPrivateText 把 messageActionStarGift 服务消息投递到收礼人私聊。
func (r *Router) deliverStarGift(ctx context.Context, senderID, recipientID int64, gift domain.StarGift, hideName bool, message string, messageEntities []domain.MessageEntity, prepaidUpgradeStars int64, prepaidUpgradeHash string) (domain.SendPrivateTextResult, error) {
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
				MessageEntities:    append([]domain.MessageEntity(nil), messageEntities...),
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
	// Gifts hidden from the profile (unsaved) are visible only to the owner (or a
	// channel admin). Never trust the client's exclude_unsaved flag for other
	// viewers: force-exclude hidden gifts unless the requester manages the owner.
	excludeUnsaved := req.ExcludeUnsaved
	if r.ensureCanManageStarGiftOwner(ctx, userID, owner) != nil {
		excludeUnsaved = true
	}
	collectionID, _ := req.GetCollectionID()
	page, err := r.deps.Gifts.ListSavedFiltered(ctx, domain.SavedStarGiftFilter{
		Owner:               owner,
		ExcludeUnsaved:      excludeUnsaved,
		ExcludeSaved:        req.ExcludeSaved,
		ExcludeUnlimited:    req.ExcludeUnlimited,
		ExcludeUnique:       req.ExcludeUnique,
		ExcludeUpgradable:   req.ExcludeUpgradable,
		ExcludeUnupgradable: req.ExcludeUnupgradable,
		ExcludeHosted:       req.ExcludeHosted,
		SortByValue:         req.SortByValue,
		PeerColorAvailable:  req.PeerColorAvailable,
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
	if settings, ok := r.deps.Gifts.(interface {
		NotificationsEnabled(context.Context, int64, int64) (bool, error)
	}); ok && owner.Type == domain.PeerTypeChannel && r.ensureCanManageStarGiftOwner(ctx, userID, owner) == nil {
		enabled, settingsErr := settings.NotificationsEnabled(ctx, userID, owner.ID)
		if settingsErr != nil {
			return nil, internalErr()
		}
		response.SetChatNotificationsEnabled(enabled)
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
	// A gift hidden from the profile (unsaved) is visible only to the owner or a
	// channel admin. Memoize the manage check per owner to avoid repeat lookups.
	manageCache := make(map[domain.Peer]bool)
	canManageOwner := func(owner domain.Peer) bool {
		if v, ok := manageCache[owner]; ok {
			return v
		}
		v := r.ensureCanManageStarGiftOwner(ctx, userID, owner) == nil
		manageCache[owner] = v
		return v
	}
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
			if g.Unsaved && !canManageOwner(g.Owner) {
				continue
			}
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
		case errors.Is(err, domain.ErrStarGiftNotFound),
			errors.Is(err, domain.ErrStarGiftAlreadyConverted),
			errors.Is(err, domain.ErrStarGiftAlreadyUpgraded),
			errors.Is(err, domain.ErrStarGiftOwnerInvalid),
			errors.Is(err, domain.ErrStarGiftUnavailable):
			// These are known business conditions (e.g. converting an already
			// upgraded/unique gift). Surface a clean client error instead of a
			// 500 INTERNAL_SERVER_ERROR.
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
		if resolver, ok := r.deps.Gifts.(interface {
			ResolveUserMessageRef(context.Context, int64, int) (domain.SavedStarGiftRef, bool, error)
		}); ok {
			resolved, found, err := resolver.ResolveUserMessageRef(ctx, userID, v.MsgID)
			if err != nil {
				return domain.SavedStarGiftRef{}, false, internalErr()
			}
			if found {
				return resolved, resolved.Valid(), nil
			}
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
	if capability, ok := r.deps.Gifts.(interface {
		CrossCraftCapability(context.Context, int64) (bool, int, error)
	}); ok && len(uniqueIDs) > 0 {
		allowed, chance, err := capability.CrossCraftCapability(ctx, viewerUserID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			for i := range gifts {
				gift := &gifts[i]
				if gift.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: viewerUserID}) {
					continue
				}
				gift.CanCraftAt = 0
				if gift.Unique != nil {
					gift.Unique.CraftChancePermille = 0
				}
			}
		} else {
			for i := range gifts {
				gift := &gifts[i]
				if gift.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: viewerUserID}) ||
					!gift.LifecycleStatus.Live() || gift.Unique == nil || gift.Unique.Burned ||
					gift.Unique.OwnerAddress != "" || gift.Unique.GiftAddress != "" {
					continue
				}
				if gift.CanCraftAt == 0 {
					gift.CanCraftAt = 1
				}
				if gift.Unique.CraftChancePermille == 0 {
					gift.Unique.CraftChancePermille = chance
				}
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
	projected := tgSavedStarGifts(viewerUserID, gifts, catalog, availability, r.clock.Now().Unix())
	out := &tg.PaymentsSavedStarGifts{
		Count: count,
		Gifts: projected,
		Chats: []tg.ChatClass{},
	}
	if ids := savedStarGiftUserIDs(viewerUserID, gifts); len(ids) > 0 {
		out.Users = r.tgUsersForViewer(viewerUserID, r.domainUsersForIDs(ctx, viewerUserID, ids))
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
func tgStarGifts(catalog []domain.StarGift, now int64) []tg.StarGiftClass {
	out := make([]tg.StarGiftClass, 0, len(catalog))
	for _, g := range catalog {
		out = append(out, tgStarGift(g, now))
	}
	return out
}

// tgStarGift 把目录项投影为 tg.StarGift（Sticker 须为带 sticker 属性的有效 Document）。
func tgStarGift(g domain.StarGift, now int64) *tg.StarGift {
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
	if g.LockedUntilDate > 0 && int64(g.LockedUntilDate) > now {
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
func tgMessageActionStarGiftForViewer(in *domain.MessageStarGiftAction, viewerUserID int64) tg.MessageActionClass {
	if in == nil {
		return &tg.MessageActionEmpty{}
	}
	ownerUserID := in.PeerUserID
	if ownerUserID == 0 && in.To.Type == domain.PeerTypeUser {
		ownerUserID = in.To.ID
	}
	if ownerUserID <= 0 || viewerUserID <= 0 {
		return tgMessageActionStarGift(in)
	}
	projected := *in
	if viewerUserID == ownerUserID {
		// The owner upgrades through InputSavedStarGift. Exposing the separate
		// prepayment hash makes DrKLO prefer the wrong invoice family and use the
		// private dialog peer (the sender) as the alleged gift owner.
		projected.PrepaidUpgradeHash = ""
	} else {
		// Telegram defines messageActionStarGift.can_upgrade as receiver-only.
		projected.CanUpgrade = false
	}
	return tgMessageActionStarGift(&projected)
}

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
		action.SetMessage(tg.TextWithEntities{Text: in.Message, Entities: tgMessageEntities(in.MessageEntities)})
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
func tgSavedStarGifts(viewerUserID int64, gifts []domain.SavedStarGift, catalog map[int64]domain.StarGift, availability map[int64]domain.StarGiftCollectibleAvailability, now int64) []tg.SavedStarGift {
	out := make([]tg.SavedStarGift, 0, len(gifts))
	for _, g := range gifts {
		item := tg.SavedStarGift{
			Date: g.Date,
			Gift: tgSavedStarGiftGift(g, catalog, availability, now),
		}
		if g.NameHidden {
			item.NameHidden = true
		}
		if g.Unsaved {
			item.Unsaved = true
		}
		if g.FromUserID != 0 && savedStarGiftOriginalDetailsVisible(viewerUserID, g) {
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
		if g.Message != "" && savedStarGiftOriginalDetailsVisible(viewerUserID, g) {
			item.SetMessage(tg.TextWithEntities{Text: g.Message, Entities: tgMessageEntities(g.MessageEntities)})
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
			// This hash starts the separate "prepay someone else's upgrade"
			// invoice. The owner must use InputSavedStarGift instead; otherwise
			// DrKLO prefers the hash path and substitutes the private dialog peer.
			ownerIsViewer := g.Owner.Type == domain.PeerTypeUser && g.Owner.ID == viewerUserID
			if g.PrepaidUpgradeHash != "" && g.PrepaidUpgradeStars == 0 && canIssue && !ownerIsViewer {
				item.SetPrepaidUpgradeHash(g.PrepaidUpgradeHash)
			}
		}
		// The export RPC currently accepts only user-owned collectibles. Do not
		// advertise a channel capability that the write path will reject.
		if g.Owner.Type == domain.PeerTypeUser && g.CanExportAt > 0 {
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
func tgSavedStarGiftGift(g domain.SavedStarGift, catalog map[int64]domain.StarGift, availability map[int64]domain.StarGiftCollectibleAvailability, now int64) tg.StarGiftClass {
	if g.Unique != nil {
		return tgUniqueStarGift(*g.Unique)
	}
	if gift, ok := catalog[g.RevisionID]; ok {
		if current, ok := availability[g.GiftID]; ok {
			gift.UpgradeStars = current.UpgradeStars
			gift.UpgradeTotal = current.SupplyTotal
			gift.UpgradeIssued = current.Issued
		}
		out := tgStarGift(gift, now)
		out.ConvertStars = g.ConvertStars
		// 拍卖中标的礼物必须显示成交价（中标出价），目录 revision 里存的是起拍价。
		if g.PaidStars > 0 {
			out.Stars = g.PaidStars
		}
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

func savedStarGiftOriginalDetailsVisible(viewerUserID int64, gift domain.SavedStarGift) bool {
	if !gift.NameHidden {
		return true
	}
	return gift.Owner.Type == domain.PeerTypeUser && gift.Owner.ID == viewerUserID
}

func savedStarGiftUserIDs(viewerUserID int64, gifts []domain.SavedStarGift) []int64 {
	seen := make(map[int64]struct{}, len(gifts))
	ids := make([]int64, 0, len(gifts))
	for _, g := range gifts {
		if g.Unique != nil {
			for _, peer := range []domain.Peer{g.Unique.Owner, g.Unique.Host} {
				if peer.Type == domain.PeerTypeUser && peer.ID > 0 {
					if _, ok := seen[peer.ID]; !ok {
						seen[peer.ID] = struct{}{}
						ids = append(ids, peer.ID)
					}
				}
			}
		}
		if g.FromUserID == 0 || !savedStarGiftOriginalDetailsVisible(viewerUserID, g) {
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
