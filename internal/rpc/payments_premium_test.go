package rpc

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type fakePremiumRPCService struct {
	plans          []domain.PremiumPlan
	issued         domain.PremiumPaymentForm
	purchase       domain.PremiumPurchaseRequest
	purchaseResult domain.PremiumPurchaseResult
	purchaseErr    error
}

func (f *fakePremiumRPCService) BotUserID() int64    { return domain.PremiumBotConfiguredUserID() }
func (f *fakePremiumRPCService) BotUsername() string { return "premiumbot" }
func (f *fakePremiumRPCService) Plans(context.Context) ([]domain.PremiumPlan, error) {
	return append([]domain.PremiumPlan(nil), f.plans...), nil
}
func (f *fakePremiumRPCService) Plan(_ context.Context, months int) (domain.PremiumPlan, error) {
	for _, plan := range f.plans {
		if plan.Months == months {
			return plan, nil
		}
	}
	return domain.PremiumPlan{}, domain.ErrPremiumPlanUnavailable
}
func (f *fakePremiumRPCService) IssuePaymentForm(_ context.Context, form domain.PremiumPaymentForm) (domain.PremiumPaymentForm, error) {
	f.issued = form
	form.ID = 99112233
	return form, nil
}
func (f *fakePremiumRPCService) PurchaseWithDelivery(_ context.Context, req domain.PremiumPurchaseRequest, effects store.DeliveryEffectsBuilder[domain.PremiumPurchaseResult]) (domain.PremiumPurchaseResult, error) {
	f.purchase = req
	// A durable idempotency replay returns the committed result without invoking
	// the builder or appending a second outbox row.
	if f.purchaseErr == nil && !f.purchaseResult.Duplicate {
		if effects == nil {
			return domain.PremiumPurchaseResult{}, store.ErrDeliveryOutboxRequired
		}
		if _, err := effects(f.purchaseResult); err != nil {
			return domain.PremiumPurchaseResult{}, err
		}
	}
	return f.purchaseResult, f.purchaseErr
}
func (f *fakePremiumRPCService) ActiveEntitlements(context.Context, int64, int) ([]domain.PremiumEntitlement, error) {
	return nil, nil
}
func (f *fakePremiumRPCService) PurchaseHistory(context.Context, int64, int) ([]domain.PremiumEntitlement, error) {
	return nil, nil
}
func (f *fakePremiumRPCService) SweepExpiredWithDelivery(_ context.Context, _ int, _ int, effects store.DeliveryEffectsBuilder[[]domain.User]) ([]domain.User, error) {
	if effects == nil {
		return nil, store.ErrDeliveryOutboxRequired
	}
	return nil, nil
}
func (f *fakePremiumRPCService) Grant(context.Context, domain.PremiumAdminGrantRequest) (domain.PremiumEntitlement, domain.User, error) {
	return domain.PremiumEntitlement{}, domain.User{}, nil
}
func (f *fakePremiumRPCService) Revoke(context.Context, domain.PremiumAdminRevokeRequest) (domain.User, error) {
	return domain.User{}, nil
}
func (f *fakePremiumRPCService) Refund(context.Context, domain.PremiumRefundRequest) (domain.PremiumPurchaseResult, error) {
	return domain.PremiumPurchaseResult{}, nil
}

func TestBotAPIGiftPremiumSubscriptionUsesCatalogAndStableRequestKey(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	bot, err := users.Create(ctx, domain.User{
		AccessHash: 8201, Phone: "15550008201", FirstName: "Bot Buyer", Bot: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 8202, Phone: "15550008202", FirstName: "Recipient",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.PremiumPlan{
		Months: 3, DurationDays: 90, AmountStars: 750, Enabled: true,
		SortOrder: 10, Label: "3 months", Version: 2,
	}
	premium := &fakePremiumRPCService{
		plans: []domain.PremiumPlan{plan},
		purchaseResult: domain.PremiumPurchaseResult{
			User:    recipient,
			Balance: domain.StarsBalance{UserID: bot.ID, Balance: 250},
		},
	}
	router := New(Config{DevStarsPaymentsEnabled: true}, Deps{
		Users:   appusers.NewService(users),
		Premium: premium,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})

	ok, err := router.BotAPIGiftPremiumSubscription(ctx, bot.ID, recipient.ID, 3, 750,
		domain.PremiumGiftMessage{Text: "Enjoy"}, "update_42")
	if err != nil || !ok {
		t.Fatalf("giftPremiumSubscription = %v,%v", ok, err)
	}
	wantKey := "botapi-premium:" + strconv.FormatInt(bot.ID, 10) + ":update_42"
	if premium.issued.IdempotencyKey != wantKey ||
		premium.issued.BuyerUserID != bot.ID || premium.issued.RecipientUserID != recipient.ID ||
		premium.issued.AmountStars != plan.AmountStars || premium.issued.PlanVersion != plan.Version {
		t.Fatalf("issued form = %+v", premium.issued)
	}
	if premium.purchase.CommandKey != wantKey || premium.purchase.FormID != 99112233 ||
		premium.purchase.RecipientUserID != recipient.ID {
		t.Fatalf("purchase = %+v", premium.purchase)
	}

	if _, err := router.BotAPIGiftPremiumSubscription(ctx, bot.ID, recipient.ID, 3, 751,
		domain.PremiumGiftMessage{}, "update_43"); err == nil || err.Error() != "STAR_COUNT_INVALID" {
		t.Fatalf("wrong catalog price err=%v", err)
	}
}

func premiumRPCTestRouter(t *testing.T) (*Router, *fakePremiumRPCService, *memory.UserStore, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	buyer, err := users.Create(ctx, domain.User{AccessHash: 8101, Phone: "15550008101", FirstName: "Buyer"})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 8102, Phone: "15550008102", FirstName: "Recipient"})
	if err != nil {
		t.Fatal(err)
	}
	premium := &fakePremiumRPCService{plans: []domain.PremiumPlan{{
		Months: 3, DurationDays: 90, AmountStars: 750, Enabled: true,
		SortOrder: 10, Label: "3 months", Version: 2,
	}}}
	now := time.Unix(1_800_000_000, 0)
	router := New(Config{DevStarsPaymentsEnabled: true}, Deps{
		Users:   appusers.NewService(users),
		Premium: premium,
	}, zaptest.NewLogger(t), fixedClock{now: now})
	return router, premium, users, buyer, recipient
}

func TestPremiumGiftOptionsAndPaymentFormUseServerCatalog(t *testing.T) {
	r, premium, _, buyer, recipient := premiumRPCTestRouter(t)
	ctx := WithUserID(context.Background(), buyer.ID)

	options, err := r.onPaymentsGetPremiumGiftCodeOptions(ctx, &tg.PaymentsGetPremiumGiftCodeOptionsRequest{})
	if err != nil {
		t.Fatalf("gift options: %v", err)
	}
	if len(options) != 2 || options[0].Users != 1 || options[0].Months != 3 ||
		options[0].Currency != "USD" || options[0].Amount != 750 ||
		options[1].Currency != "XTR" || options[1].Amount != 750 {
		t.Fatalf("gift options = %+v", options)
	}
	invoice := &tg.InputInvoicePremiumGiftStars{
		UserID: &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Months: 3,
	}
	invoice.SetMessage(tg.TextWithEntities{
		Text:     "Enjoy 😀",
		Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 6, Length: 2}},
	})
	result, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		t.Fatalf("payment form: %v", err)
	}
	form, ok := result.(*tg.PaymentsPaymentFormStars)
	if !ok {
		t.Fatalf("payment form = %T", result)
	}
	if form.FormID != 99112233 || form.BotID != premium.BotUserID() ||
		form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 ||
		form.Invoice.Prices[0].Amount != 750 || len(form.Users) != 2 {
		t.Fatalf("payment form = %+v", form)
	}
	if premium.issued.BuyerUserID != buyer.ID || premium.issued.RecipientUserID != recipient.ID ||
		premium.issued.PlanVersion != 2 || premium.issued.Message.Text != "Enjoy 😀" {
		t.Fatalf("issued intent = %+v", premium.issued)
	}
}

func TestNativePremiumGiftCodeUsesFiatCheckoutAndRejectsReplay(t *testing.T) {
	r, premium, _, buyer, recipient := premiumRPCTestRouter(t)
	ctx := WithUserID(context.Background(), buyer.ID)
	option := tg.PremiumGiftCodeOption{Users: 1, Months: 3, Currency: "USD", Amount: 750}
	invoice := &tg.InputInvoicePremiumGiftCode{
		Purpose: &tg.InputStorePaymentPremiumGiftCode{
			Users:    []tg.InputUserClass{&tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}},
			Currency: "USD", Amount: 750,
		},
		Option: option,
	}
	resolved, err := r.resolvePremiumInvoice(ctx, buyer.ID, invoice)
	if err != nil {
		t.Fatalf("resolve fiat Premium gift: %v", err)
	}
	if resolved.EffectiveDebitStars() || resolved.EffectivePaymentCurrency() != "USD" ||
		resolved.EffectivePaymentAmount() != 750 {
		t.Fatalf("resolved fiat invoice = %+v", resolved)
	}
	form, err := r.premiumPaymentForm(ctx, buyer.ID, invoice)
	if err != nil {
		t.Fatalf("fiat Premium payment form: %v", err)
	}
	if _, ok := form.(*tg.PaymentsPaymentForm); !ok {
		t.Fatalf("fiat Premium form = %T, want payments.paymentForm", form)
	}
	if premium.issued.EffectiveDebitStars() || premium.issued.PaymentCurrency != "USD" {
		t.Fatalf("issued fiat form = %+v", premium.issued)
	}

	premium.purchaseResult = domain.PremiumPurchaseResult{Duplicate: true}
	_, err = r.sendPremiumStarsForm(ctx, buyer.ID, 99112233, invoice)
	if !tgerr.Is(err, "INVOICE_ALREADY_PAID") {
		t.Fatalf("paid invoice replay err=%v, want INVOICE_ALREADY_PAID", err)
	}
}

// A denied fiat checkout must be rejected up front: the reproduction used to
// reach IssuePaymentForm first, which wrote a durable premium_payment_intents
// row even though the request was going to fail with NOT_IMPLEMENTED.
func TestDeniedFiatPremiumFormNeverIssuesPaymentIntent(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	buyer, err := users.Create(ctx, domain.User{AccessHash: 8301, Phone: "15550008301", FirstName: "Buyer"})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 8302, Phone: "15550008302", FirstName: "Recipient"})
	if err != nil {
		t.Fatal(err)
	}
	premium := &fakePremiumRPCService{plans: []domain.PremiumPlan{{
		Months: 3, DurationDays: 90, AmountStars: 750, Enabled: true,
		SortOrder: 10, Label: "3 months", Version: 2,
	}}}
	router := New(Config{DevStarsPaymentsEnabled: false}, Deps{
		Users:   appusers.NewService(users),
		Premium: premium,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})

	invoice := &tg.InputInvoicePremiumGiftCode{
		Purpose: &tg.InputStorePaymentPremiumGiftCode{
			Users:    []tg.InputUserClass{&tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}},
			Currency: "USD", Amount: 750,
		},
		Option: tg.PremiumGiftCodeOption{Users: 1, Months: 3, Currency: "USD", Amount: 750},
	}
	for i := 0; i < 2; i++ {
		if _, err := router.premiumPaymentForm(ctx, buyer.ID, invoice); !tgerr.Is(err, "NOT_IMPLEMENTED") {
			t.Fatalf("denied fiat form attempt %d err=%v, want NOT_IMPLEMENTED", i+1, err)
		}
	}
	if premium.issued.ID != 0 {
		t.Fatalf("denied fiat checkout issued a payment intent: %+v", premium.issued)
	}
}

func TestPremiumBotMessageInvoiceUsesStableSingleUseKey(t *testing.T) {
	const (
		buyerID = int64(1000000001)
		botID   = int64(1250000015)
	)
	input := &tg.InputInvoiceMessage{MsgID: 119}
	want := "premiumbot-message:1000000001:1250000015:119"
	if got := premiumPaymentFormIdempotencyKey(buyerID, botID, input); got != want {
		t.Fatalf("message invoice key=%q, want %q", got, want)
	}
	if got := premiumPaymentFormIdempotencyKey(buyerID, botID, input); got != want {
		t.Fatalf("repeated message invoice key=%q, want %q", got, want)
	}
	if got := premiumPaymentFormIdempotencyKey(buyerID, botID, &tg.InputInvoiceMessage{MsgID: 120}); got == want {
		t.Fatalf("different message reused key %q", got)
	}
	if got := premiumPaymentFormIdempotencyKey(buyerID, botID, &tg.InputInvoicePremiumGiftStars{}); got != "" {
		t.Fatalf("native invoice key=%q, want empty", got)
	}
	if err := premiumPaymentErr(domain.ErrPremiumInvoiceAlreadyPaid); !tgerr.Is(err, "INVOICE_ALREADY_PAID") {
		t.Fatalf("already-paid mapping=%v, want INVOICE_ALREADY_PAID", err)
	}
}

func TestNativePremiumSubscriptionMapsMonthlyAndUpgradePlans(t *testing.T) {
	r, premium, _, buyer, _ := premiumRPCTestRouter(t)
	premium.plans = []domain.PremiumPlan{
		{Months: 12, DurationDays: 365, AmountStars: 2400, Enabled: true, SortOrder: 5, Label: "1 year", Version: 3},
		{Months: 1, DurationDays: 30, AmountStars: 300, Enabled: true, SortOrder: 50, Label: "1 month", Version: 7},
		{Months: 6, DurationDays: 180, AmountStars: 1500, Enabled: false, SortOrder: 1, Label: "disabled", Version: 9},
	}
	ctx := WithUserID(context.Background(), buyer.ID)
	invoice := func(upgrade bool) tg.InputInvoiceClass {
		return &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentPremiumSubscription{Upgrade: upgrade}}
	}
	monthly, err := r.resolvePremiumInvoice(ctx, buyer.ID, invoice(false))
	if err != nil || monthly.Months != 1 || monthly.AmountStars != 300 || monthly.PlanVersion != 7 {
		t.Fatalf("native monthly invoice = %+v err=%v", monthly, err)
	}
	yearly, err := r.resolvePremiumInvoice(ctx, buyer.ID, invoice(true))
	if err != nil || yearly.Months != 12 || yearly.AmountStars != 2400 || yearly.PlanVersion != 3 {
		t.Fatalf("native upgrade invoice = %+v err=%v", yearly, err)
	}
	if _, err := r.resolvePremiumInvoice(ctx, buyer.ID, &tg.InputInvoiceStars{
		Purpose: &tg.InputStorePaymentPremiumSubscription{Restore: true},
	}); !tgerr.Is(err, "INVOICE_INVALID") {
		t.Fatalf("unverifiable native restore err=%v, want INVOICE_INVALID", err)
	}
}

func TestPremiumGiftInvoiceRejectsSelfBotAndBadEntityBounds(t *testing.T) {
	r, _, users, buyer, recipient := premiumRPCTestRouter(t)
	ctx := WithUserID(context.Background(), buyer.ID)
	deleted, err := users.Create(context.Background(), domain.User{
		AccessHash: 8103, Phone: "15550008103", FirstName: "Deleted", Deleted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	system := domain.PremiumBotUser()

	for name, invoice := range map[string]*tg.InputInvoicePremiumGiftStars{
		"self": {
			UserID: &tg.InputUser{UserID: buyer.ID, AccessHash: buyer.AccessHash},
			Months: 3,
		},
		"bad access hash": {
			UserID: &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash + 1},
			Months: 3,
		},
		"deleted": {
			UserID: &tg.InputUser{UserID: deleted.ID, AccessHash: deleted.AccessHash},
			Months: 3,
		},
		"system bot": {
			UserID: &tg.InputUser{UserID: system.ID, AccessHash: system.AccessHash},
			Months: 3,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice}); err == nil {
				t.Fatal("invalid Premium gift invoice was accepted")
			}
		})
	}

	badEntity := &tg.InputInvoicePremiumGiftStars{
		UserID: &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Months: 3,
	}
	badEntity.SetMessage(tg.TextWithEntities{
		Text:     "😀",
		Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 1, Length: 2}},
	})
	if _, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: badEntity}); err == nil {
		t.Fatal("out-of-bounds UTF-16 gift entity was accepted")
	}
}

func TestPremiumGiftInvoiceRespectsRecipientGiftPrivacy(t *testing.T) {
	r, _, _, buyer, recipient := premiumRPCTestRouter(t)
	settingsStore := memory.NewPasswordStore()
	account := appaccount.NewService(settingsStore, appaccount.WithAccountSettings(settingsStore))
	r.deps.Account = account
	if _, err := account.SetGlobalPrivacy(context.Background(), recipient.ID, domain.GlobalPrivacy{
		DisallowedGifts: domain.DisallowedGifts{PremiumGifts: true},
	}); err != nil {
		t.Fatalf("set recipient gift privacy: %v", err)
	}
	ctx := WithUserID(context.Background(), buyer.ID)
	full, err := r.buildUserFullProjection(ctx, buyer.ID, recipient)
	if err != nil {
		t.Fatalf("build privacy-aware userFull: %v", err)
	}
	gifts, ok := full.GetDisallowedGifts()
	if !ok || !gifts.DisallowPremiumGifts {
		t.Fatalf("userFull disallowed_gifts=%+v ok=%v", gifts, ok)
	}
	_, err = r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{
		Invoice: &tg.InputInvoicePremiumGiftStars{
			UserID: &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
			Months: 3,
		},
	})
	if !tgerr.Is(err, "USER_PRIVACY_RESTRICTED") {
		t.Fatalf("privacy-restricted Premium gift err=%v", err)
	}
}

func TestPremiumGiftInvoiceRejectsActiveRecipient(t *testing.T) {
	r, _, users, buyer, recipient := premiumRPCTestRouter(t)
	const premiumUntil = 1_800_086_400
	if _, err := users.SetPremiumUntil(context.Background(), recipient.ID, premiumUntil); err != nil {
		t.Fatalf("set recipient Premium: %v", err)
	}
	ctx := WithUserID(context.Background(), buyer.ID)
	_, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{
		Invoice: &tg.InputInvoicePremiumGiftStars{
			UserID: &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
			Months: 3,
		},
	})
	want := "PREMIUM_SUB_ACTIVE_UNTIL"
	if !tgerr.Is(err, want) {
		t.Fatalf("active Premium recipient err=%v, want %s", err, want)
	}
	if !tgerr.IsCode(err, 420) {
		t.Fatalf("active Premium recipient err=%v, want code 420", err)
	}
	rpcErr, ok := tgerr.As(err)
	if !ok || rpcErr.Argument != premiumUntil {
		t.Fatalf("active Premium recipient err=%v, want expiry argument %d", err, premiumUntil)
	}
}

func TestPremiumLayer228ConstructorIDsAndGiftActionFlags(t *testing.T) {
	ids := map[string]struct {
		got, want uint32
	}{
		"inputInvoicePremiumGiftStars":         {tg.InputInvoicePremiumGiftStarsTypeID, 0xdabab2ef},
		"payments.paymentFormStars":            {tg.PaymentsPaymentFormStarsTypeID, 0x7bf6b15c},
		"premiumGiftCodeOption":                {tg.PremiumGiftCodeOptionTypeID, 0x257e962b},
		"messageActionGiftPremium":             {tg.MessageActionGiftPremiumTypeID, 0x48e91302},
		"starsTransactionPeerPremiumBot":       {tg.StarsTransactionPeerPremiumBotTypeID, 0x250dbaf8},
		"inputStorePaymentPremiumSubscription": {tg.InputStorePaymentPremiumSubscriptionTypeID, 0xa6751e66},
	}
	for name, id := range ids {
		if id.got != id.want {
			t.Errorf("%s id = %#x, want %#x", name, id.got, id.want)
		}
	}
	invoice := &tg.InputInvoicePremiumGiftStars{
		UserID: &tg.InputUser{UserID: 77, AccessHash: 88},
		Months: 3,
	}
	invoice.SetMessage(tg.TextWithEntities{
		Text: "gift",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 4},
		},
	})
	wireInvoice := &tg.InputInvoicePremiumGiftStars{}
	tlRoundTrip(t, invoice, wireInvoice)
	if !wireInvoice.Flags.Has(0) || wireInvoice.Months != 3 {
		t.Fatalf("Premium invoice flags=%032b months=%d, want message bit 0 and 3 months",
			uint32(wireInvoice.Flags), wireInvoice.Months)
	}
	option := &tg.PremiumGiftCodeOption{Users: 1, Months: 3, Currency: "XTR", Amount: 750}
	wireOption := &tg.PremiumGiftCodeOption{}
	tlRoundTrip(t, option, wireOption)
	if !wireOption.Flags.Zero() || wireOption.Currency != "XTR" || wireOption.Amount != 750 {
		t.Fatalf("XTR Premium option round-trip=%+v, want no store flags", wireOption)
	}
	action := &tg.MessageActionGiftPremium{Currency: "XTR", Amount: 750, Days: 90}
	action.SetMessage(tg.TextWithEntities{Text: "gift", Entities: []tg.MessageEntityClass{}})
	wire := &tg.MessageActionGiftPremium{}
	tlRoundTrip(t, action, wire)
	if !wire.Flags.Has(1) || wire.Flags.Has(0) {
		t.Fatalf("gift action flags = %032b, want message bit 1 only", uint32(wire.Flags))
	}
	message, ok := wire.GetMessage()
	if !ok || message.Text != "gift" || wire.Currency != "XTR" || wire.Amount != 750 || wire.Days != 90 {
		t.Fatalf("gift action round-trip = %+v message=%+v ok=%v", wire, message, ok)
	}
}
