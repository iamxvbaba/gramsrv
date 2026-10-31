package rpc

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// botGiftService is a fake GiftsService dedicated to the Bot API star-gift path: it implements
// only the four methods sendGift actually touches, and satisfies rpc.GiftsService via an embedded
// interface for the rest.
type botGiftService struct {
	GiftsService
	gift        domain.StarGift
	giftFound   bool
	catalogErr  error
	settled     domain.StarGiftPurchaseResult
	settledOK   bool
	settledErr  error
	issued      domain.StarGiftPurchaseForm
	issuedCalls int
	issuedErr   error
	purchases   []domain.StarGiftPurchaseRequest
	purchase    domain.StarGiftPurchaseResult
	purchaseErr error
	formID      int64
}

func (s *botGiftService) Catalog(context.Context) ([]domain.StarGift, error) {
	return []domain.StarGift{s.gift}, s.catalogErr
}

func (s *botGiftService) GiftByID(context.Context, int64) (domain.StarGift, bool, error) {
	return s.gift, s.giftFound, nil
}

func (s *botGiftService) IssuePurchaseForm(_ context.Context, form domain.StarGiftPurchaseForm) (domain.StarGiftPurchaseForm, error) {
	if s.issuedErr != nil {
		return domain.StarGiftPurchaseForm{}, s.issuedErr
	}
	s.issued = form
	s.issuedCalls++
	form.FormID = s.formID
	return form, nil
}

func (s *botGiftService) SettledStarGiftPurchase(_ context.Context, _ domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, bool, error) {
	return s.settled, s.settledOK, s.settledErr
}

func (s *botGiftService) Purchase(_ context.Context, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, error) {
	s.purchases = append(s.purchases, req)
	return s.purchase, s.purchaseErr
}

// botStarGiftFixture provides a real bot account, an ordinary user and a channel the bot can
// access, to verify sendGift's recipient validation (cannot gift itself, channel needs access).
type botStarGiftFixture struct {
	ctx       context.Context
	router    *Router
	gifts     *botGiftService
	botID     int64
	ownerID   int64
	channelID int64
}

func newBotStarGiftFixture(t *testing.T, gifts *botGiftService) botStarGiftFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1001, Phone: "15550008101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	bot, _, err := memory.NewBotStore(userStore).CreateBotAccount(ctx,
		domain.User{AccessHash: 2001, FirstName: "GiftBot", Username: "GiftBot"},
		domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "secret"})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	channels := appchannels.NewService(memory.NewChannelStore())
	channel, err := channels.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		Title: "Gifts", MemberUserIDs: []int64{bot.ID}, Date: 10,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	router := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channels,
		Gifts:    gifts,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})
	return botStarGiftFixture{
		ctx: ctx, router: router, gifts: gifts,
		botID: bot.ID, ownerID: owner.ID, channelID: channel.Channel.ID,
	}
}

// botAPIGiftChannelChatID reproduces the Bot API channel chat_id encoding (-100xxxxxxxxxx).
func botAPIGiftChannelChatID(channelID int64) int64 {
	return -botAPIChannelChatIDBase - channelID
}

func sellableBotGift() domain.StarGift {
	return domain.StarGift{ID: 6011, RevisionID: 77, Stars: 15, AvailabilityRemains: 100}
}

func TestBotAPISendStarGiftUsesCatalogAndStableCommandKey(t *testing.T) {
	f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: true, formID: 99112233})

	ok, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, false,
		domain.PremiumGiftMessage{Text: "Enjoy"}, "update_42")
	if err != nil || !ok {
		t.Fatalf("sendGift = %v, %v", ok, err)
	}
	if len(f.gifts.purchases) != 1 {
		t.Fatalf("purchases = %d, want 1", len(f.gifts.purchases))
	}
	purchase := f.gifts.purchases[0]
	wantCommand := "botapi-stargift:" + strconv.FormatInt(f.botID, 10) + ":update_42"
	if purchase.BuyerUserID != f.botID || purchase.GiftID != 6011 || purchase.RevisionID != 77 ||
		purchase.ChargeStars != 15 || purchase.FormID != 99112233 ||
		purchase.CommandKey != wantCommand {
		t.Fatalf("purchase = %+v, want command key %q", purchase, wantCommand)
	}
	// Bot API has no MTProto session: filling only OriginAuthKeyID while session is 0 forms a
	// dispatch-exclusion "half pair", and PurchaseStarGift would fail the whole transaction at
	// enqueueDispatch. These two fields must both be zero.
	if purchase.OriginAuthKeyID != ([8]byte{}) || purchase.OriginSessionID != 0 {
		t.Fatalf("purchase origin = %v/%d, want a zero pair", purchase.OriginAuthKeyID, purchase.OriginSessionID)
	}
	if purchase.To != (domain.Peer{Type: domain.PeerTypeUser, ID: f.ownerID}) {
		t.Fatalf("purchase recipient = %+v", purchase.To)
	}
	if purchase.Message != "Enjoy" || purchase.Date != 1_800_000_000 {
		t.Fatalf("purchase message/date = %q, %d", purchase.Message, purchase.Date)
	}
	// The form must carry the same price and item as the payment request, otherwise the in-transaction FOR UPDATE validation rejects the form.
	if f.gifts.issued.ChargeStars != purchase.ChargeStars || f.gifts.issued.GiftID != 6011 ||
		f.gifts.issued.RevisionID != 77 || f.gifts.issued.BuyerUserID != f.botID ||
		f.gifts.issued.ExpiresAt != f.gifts.issued.IssuedAt+600 {
		t.Fatalf("issued form = %+v", f.gifts.issued)
	}
	// The charge must go through the bot's wallet (bot_stars_balances), not the personal ledger
	// where it has no row at all: omitting this flag makes sendGift return BALANCE_TOO_LOW for any
	// balance.
	if !purchase.BuyerIsBot {
		t.Fatal("purchase must be marked as a bot buyer so the wallet is charged")
	}
}

func TestBotAPISendStarGiftSendsToChannelChatID(t *testing.T) {
	f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: true, formID: 12})

	ok, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, 0,
		botAPIGiftChannelChatID(f.channelID), false, domain.PremiumGiftMessage{}, "chan_1")
	if err != nil || !ok {
		t.Fatalf("channel sendGift = %v, %v", ok, err)
	}
	if want := (domain.Peer{Type: domain.PeerTypeChannel, ID: f.channelID}); f.gifts.purchases[0].To != want {
		t.Fatalf("purchase recipient = %+v, want %+v", f.gifts.purchases[0].To, want)
	}
}

func TestBotAPISendStarGiftChargesUpgradePrice(t *testing.T) {
	gift := sellableBotGift()
	gift.UpgradeStars, gift.UpgradeTotal, gift.UpgradeIssued = 10, 500, 120
	f := newBotStarGiftFixture(t, &botGiftService{gift: gift, giftFound: true, formID: 4242})

	if _, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, true,
		domain.PremiumGiftMessage{}, "up_1"); err != nil {
		t.Fatalf("sendGift upgrade: %v", err)
	}
	purchase := f.gifts.purchases[0]
	if !purchase.IncludeUpgrade || purchase.ChargeStars != 25 {
		t.Fatalf("upgrade purchase = %+v", purchase)
	}
	if f.gifts.issued.ChargeStars != 25 || !f.gifts.issued.IncludeUpgrade {
		t.Fatalf("upgrade form = %+v", f.gifts.issued)
	}
}

func TestBotAPISendStarGiftReplaysSettledCommandWithoutCharging(t *testing.T) {
	f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: true, formID: 5,
		settled: domain.StarGiftPurchaseResult{Duplicate: true}, settledOK: true})

	for range 2 {
		ok, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, false,
			domain.PremiumGiftMessage{}, "same_key")
		if err != nil || !ok {
			t.Fatalf("sendGift replay = %v, %v", ok, err)
		}
	}
	// A replay must completely bypass form issuance and charging, otherwise the same key would be charged twice.
	if len(f.gifts.purchases) != 0 || f.gifts.issuedCalls != 0 {
		t.Fatalf("replay charged again: purchases=%d issued=%d", len(f.gifts.purchases), f.gifts.issuedCalls)
	}
}

func TestBotAPISendStarGiftRejectsIdempotencyKeyReuseForAnotherRequest(t *testing.T) {
	f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: true,
		settledErr: domain.ErrStarGiftIdempotencyConflict})

	_, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, false,
		domain.PremiumGiftMessage{}, "reused")
	if err == nil || err.Error() != "IDEMPOTENCY_KEY_INVALID" {
		t.Fatalf("err = %v, want IDEMPOTENCY_KEY_INVALID", err)
	}
	if len(f.gifts.purchases) != 0 || f.gifts.issuedCalls != 0 {
		t.Fatalf("conflicting key still charged: purchases=%d issued=%d", len(f.gifts.purchases), f.gifts.issuedCalls)
	}
}

func TestBotAPISendStarGiftRecoversConcurrentDuplicateOfSameKey(t *testing.T) {
	// Among concurrent same-key requests, the one that loses the command_key unique constraint
	// first hits a form_id comparison failure. The payment has actually landed by then, so it must
	// be truthfully reported as success rather than letting the bot think it wasted its money.
	f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: true, formID: 7,
		purchaseErr: domain.ErrStarGiftInvalid,
		settled:     domain.StarGiftPurchaseResult{Duplicate: true}, settledOK: true})

	ok, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, false,
		domain.PremiumGiftMessage{}, "race_key")
	if err != nil || !ok {
		t.Fatalf("concurrent duplicate = %v, %v", ok, err)
	}
}

func TestBotAPISendStarGiftRejectsUnsellableCatalogEntries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.StarGift)
		want   string
	}{
		{"sold out", func(g *domain.StarGift) { g.SoldOut = true }, "GIFT_NOT_AVAILABLE"},
		{"exhausted", func(g *domain.StarGift) { g.Limited, g.AvailabilityRemains = true, 0 }, "GIFT_NOT_AVAILABLE"},
		{"not released yet", func(g *domain.StarGift) { g.LockedUntilDate = 1_900_000_000 }, "GIFT_NOT_AVAILABLE"},
		{"auction", func(g *domain.StarGift) { g.Auction = true }, "GIFT_NOT_AVAILABLE"},
		{"support only", func(g *domain.StarGift) { g.SupportOnly = true }, "GIFT_NOT_AVAILABLE"},
		{"premium only", func(g *domain.StarGift) { g.RequirePremium = true }, "PREMIUM_ACCOUNT_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gift := sellableBotGift()
			tc.mutate(&gift)
			f := newBotStarGiftFixture(t, &botGiftService{gift: gift, giftFound: true, formID: 9})

			_, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, false,
				domain.PremiumGiftMessage{}, "k")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %s", err, tc.want)
			}
			if len(f.gifts.purchases) != 0 {
				t.Fatalf("unsellable gift was charged: %d purchases", len(f.gifts.purchases))
			}
		})
	}
}

func TestBotAPISendStarGiftRejectsUnknownGift(t *testing.T) {
	f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: false})

	_, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, false,
		domain.PremiumGiftMessage{}, "k")
	if err == nil || err.Error() != "GIFT_NOT_AVAILABLE" {
		t.Fatalf("err = %v, want GIFT_NOT_AVAILABLE", err)
	}
}

func TestBotAPISendStarGiftRejectsExhaustedUpgrade(t *testing.T) {
	gift := sellableBotGift()
	gift.UpgradeStars, gift.UpgradeTotal, gift.UpgradeIssued = 10, 500, 500
	f := newBotStarGiftFixture(t, &botGiftService{gift: gift, giftFound: true})

	_, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, true,
		domain.PremiumGiftMessage{}, "k")
	if err == nil || err.Error() != "GIFT_UPGRADE_UNAVAILABLE" {
		t.Fatalf("err = %v, want GIFT_UPGRADE_UNAVAILABLE", err)
	}
}

func TestBotAPISendStarGiftRejectsInvalidRecipients(t *testing.T) {
	f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: true, formID: 11})

	// A bot cannot gift itself.
	if _, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.botID, 0, false,
		domain.PremiumGiftMessage{}, "self"); err == nil || err.Error() != "USER_ID_INVALID" {
		t.Fatalf("self gift err = %v, want USER_ID_INVALID", err)
	}
	// Neither the user nor the channel is a valid gift recipient.
	if _, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, 0, 0, false,
		domain.PremiumGiftMessage{}, "none"); err == nil || err.Error() != "CHAT_ID_INVALID" {
		t.Fatalf("no recipient err = %v, want CHAT_ID_INVALID", err)
	}
	// A channel the bot is not in: the gift path does not validate posting rights for us, so the gateway must reject it itself.
	if _, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, 0,
		botAPIGiftChannelChatID(f.channelID+9999), false, domain.PremiumGiftMessage{}, "chan"); err == nil ||
		err.Error() != "CHAT_ID_INVALID" {
		t.Fatalf("unreachable channel err = %v, want CHAT_ID_INVALID", err)
	}
	if len(f.gifts.purchases) != 0 {
		t.Fatalf("invalid recipient was charged: %d purchases", len(f.gifts.purchases))
	}
}

func TestBotAPISendStarGiftRejectsOverlongGiftText(t *testing.T) {
	f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: true, formID: 13})

	_, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, false,
		domain.PremiumGiftMessage{Text: string(make([]rune, domain.MaxPremiumGiftMessageRunes+1))}, "long")
	if err == nil || err.Error() != "ENTITY_BOUNDS_INVALID" {
		t.Fatalf("err = %v, want ENTITY_BOUNDS_INVALID", err)
	}
}

func TestBotAPISendStarGiftMapsPurchaseFailuresToBotAPICodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"balance", domain.ErrStarsInsufficient, "BALANCE_TOO_LOW"},
		{"premium", domain.ErrPremiumRequired, "PREMIUM_ACCOUNT_REQUIRED"},
		{"collectible", domain.ErrStarGiftCollectibleUnavailable, "GIFT_UPGRADE_UNAVAILABLE"},
		{"frozen recipient", domain.ErrStarGiftRecipientUnavailable, "USER_PRIVACY_RESTRICTED"},
		{"sold out in tx", domain.ErrStarGiftUnavailable, "GIFT_NOT_AVAILABLE"},
		{"amount changed", domain.ErrStarGiftFormAmountMismatch, "GIFT_NOT_AVAILABLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: true, formID: 3,
				purchaseErr: errors.Join(tc.err, errors.New("tx detail"))})

			_, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, false,
				domain.PremiumGiftMessage{}, "k")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestBotAPISendStarGiftSurfacesUnexpectedFailureAsUnavailable(t *testing.T) {
	boom := errors.New("star_gift_purchase_commands missing")
	f := newBotStarGiftFixture(t, &botGiftService{gift: sellableBotGift(), giftFound: true, formID: 3,
		purchaseErr: boom, settledErr: boom})

	_, err := f.router.BotAPISendStarGift(f.ctx, f.botID, 6011, f.ownerID, 0, false,
		domain.PremiumGiftMessage{}, "k")
	if err == nil || err.Error() != "STAR_GIFT_UNAVAILABLE" {
		t.Fatalf("err = %v, want STAR_GIFT_UNAVAILABLE", err)
	}
}

func TestBotAPISendStarGiftWithoutServicesReportsUnavailable(t *testing.T) {
	router := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})

	_, err := router.BotAPISendStarGift(context.Background(), 1, 6011, 2, 0, false,
		domain.PremiumGiftMessage{}, "k")
	if err == nil || err.Error() != "STAR_GIFT_UNAVAILABLE" {
		t.Fatalf("err = %v, want STAR_GIFT_UNAVAILABLE", err)
	}
}
