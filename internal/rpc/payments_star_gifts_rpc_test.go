package rpc

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/tlprofile"
	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appprivacy "telesrv/internal/app/privacy"
	appstargifts "telesrv/internal/app/stargifts"
	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type starsTopupRPCStore struct {
	*memory.StarsStore
	nextFormID int64
	forms      map[int64]domain.StarsPurchaseForm
	settled    map[int64]domain.StarsPurchaseResult
	channel    domain.Channel
}

func devStarsCredentials(formID int64) *tg.InputPaymentCredentials {
	return &tg.InputPaymentCredentials{Data: tg.DataJSON{Data: fmt.Sprintf(`{"type":"telesrv_dev","form_id":"%d"}`, formID)}}
}

func newStarsTopupRPCStore() *starsTopupRPCStore {
	return &starsTopupRPCStore{
		StarsStore: memory.NewStarsStore(), nextFormID: 91000,
		forms: make(map[int64]domain.StarsPurchaseForm), settled: make(map[int64]domain.StarsPurchaseResult),
	}
}

func (s *starsTopupRPCStore) IssueStarsPurchaseForm(_ context.Context, form domain.StarsPurchaseForm) (domain.StarsPurchaseForm, error) {
	s.nextFormID++
	form.FormID = s.nextFormID
	s.forms[form.FormID] = form
	return form, nil
}

func (s *starsTopupRPCStore) PurchaseStars(ctx context.Context, req domain.StarsPurchaseRequest) (domain.StarsPurchaseResult, error) {
	form, ok := s.forms[req.FormID]
	if !ok || form.Kind != req.Kind || form.BuyerUserID != req.BuyerUserID ||
		form.RecipientUserID != req.RecipientUserID || form.SpendPurposePeer != req.SpendPurposePeer ||
		form.Stars != req.Stars || form.Currency != req.Currency || form.Amount != req.Amount ||
		!reflect.DeepEqual(form.Giveaway, req.Giveaway) {
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormInvalid
	}
	if result, ok := s.settled[req.FormID]; ok {
		result.Duplicate = true
		return result, nil
	}
	if req.Kind == domain.StarsPurchaseGiveaway {
		g := req.Giveaway
		result := domain.StarsPurchaseResult{TransactionID: fmt.Sprintf("stars-giveaway-test:%d", req.FormID)}
		channel := s.channel
		if channel.ID == 0 {
			channel = domain.Channel{ID: g.BoostPeer.ID, Megagroup: true}
		}
		result.ChannelSend = domain.SendChannelMessageResult{
			Channel: channel,
			Message: domain.ChannelMessage{ChannelID: g.BoostPeer.ID, ID: 71, RandomID: g.RandomID, SenderUserID: req.BuyerUserID,
				Date: req.Date, Pts: 9, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindGiveaway, Giveaway: &domain.MessageGiveaway{
					Channels: []int64{g.BoostPeer.ID}, Quantity: g.Users, Stars: req.Stars, UntilDate: g.UntilDate,
				}}},
			Event: domain.ChannelUpdateEvent{ChannelID: g.BoostPeer.ID, Type: domain.ChannelUpdateNewMessage, Pts: 9, PtsCount: 1, Date: req.Date},
		}
		result.ChannelSend.Event.Message = result.ChannelSend.Message
		s.settled[req.FormID] = result
		return result, nil
	}
	if req.Kind != domain.StarsPurchaseTopup {
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormInvalid
	}
	balance, err := s.StarsStore.Credit(ctx, req.BuyerUserID, req.Stars, domain.StarsReasonTopup,
		req.SpendPurposePeer, req.Date, "Stars top-up", "test purchase")
	if err != nil {
		return domain.StarsPurchaseResult{}, err
	}
	result := domain.StarsPurchaseResult{Balance: balance, TransactionID: fmt.Sprintf("stars-topup-test:%d", req.FormID)}
	s.settled[req.FormID] = result
	return result, nil
}

func (s *starsTopupRPCStore) GetStarsGiveawayInfo(_ context.Context, viewerUserID, channelID int64, messageID, _ int) (domain.StarsGiveawayInfo, error) {
	if viewerUserID <= 0 || channelID != s.channel.ID || messageID != 71 {
		return domain.StarsGiveawayInfo{}, domain.ErrMessageIDInvalid
	}
	return domain.StarsGiveawayInfo{StartDate: 1_700_000_200, Participating: true}, nil
}

func starGiftTestRouter(t *testing.T) (*Router, domain.User, domain.User, domain.StarGift) {
	return starGiftTestRouterWithPremium(t, false)
}

func starGiftTestRouterWithPremium(t *testing.T, requirePremium bool) (*Router, domain.User, domain.User, domain.StarGift) {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	msgStore := memory.NewMessageStore(dialogs)
	channelStore := memory.NewChannelStore()
	sender, err := users.Create(ctx, domain.User{AccessHash: 7101, Phone: "15550007101", FirstName: "Sender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 7102, Phone: "15550007102", FirstName: "Recipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	gift := domain.StarGift{
		ID: 8001, RevisionID: 9001, Stars: 50, ConvertStars: 50, Title: "Cake", RequirePremium: requirePremium,
		Sticker: domain.Document{ID: 700, AccessHash: 7, DCID: 2, MimeType: "application/x-tgsticker", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
	}
	giftStore := memory.NewStarGiftStore()
	giftStore.SeedCatalog([]domain.StarGift{gift})
	gifts := appstargifts.NewService(giftStore, nil, 2)
	starsStore := newStarsTopupRPCStore()
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398, PublicBaseURL: "https://links.example.test", AllowDevPayments: true}, Deps{
		Users:    appusers.NewService(users),
		Messages: appmessages.NewService(msgStore, dialogs),
		Channels: appchannels.NewService(channelStore),
		Stars:    appstars.NewService(starsStore, appstars.WithStartingGrant(1000), appstars.WithPurchaseStore(starsStore)),
		Gifts:    gifts,
	}, zaptest.NewLogger(t), clock.System)
	return r, sender, recipient, gift
}

func TestStarGiftPurchaseRequiresActivePremium(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouterWithPremium(t, true)
	ctx := WithUserID(context.Background(), sender.ID)
	inv := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, GiftID: gift.ID}
	if _, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv}); !tgerr.Is(err, "PREMIUM_ACCOUNT_REQUIRED") {
		t.Fatalf("non-premium gift form err = %v, want PREMIUM_ACCOUNT_REQUIRED", err)
	}
	premium, ok := r.deps.Users.(UserPremiumService)
	if !ok {
		t.Fatalf("users service %T does not implement premium grants", r.deps.Users)
	}
	if _, err := premium.GrantPremium(context.Background(), sender.ID, 1); err != nil {
		t.Fatalf("grant premium: %v", err)
	}
	formRes, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("premium gift form: %v", err)
	}
	form, ok := formRes.(*tg.PaymentsPaymentFormStarGift)
	if !ok {
		t.Fatalf("premium gift form = %T", formRes)
	}
	if _, err := r.onPaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv}); err != nil {
		t.Fatalf("premium gift purchase: %v", err)
	}
}

func TestStarGiftSupportOnlyGate(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	msgStore := memory.NewMessageStore(dialogs)
	channelStore := memory.NewChannelStore()
	regular, err := users.Create(ctx, domain.User{AccessHash: 7111, Phone: "15550007111", FirstName: "Regular"})
	if err != nil {
		t.Fatalf("create regular: %v", err)
	}
	helper, err := users.Create(ctx, domain.User{AccessHash: 7112, Phone: "15550007112", FirstName: "Support", Support: true})
	if err != nil {
		t.Fatalf("create support: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 7113, Phone: "15550007113", FirstName: "Recipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	gift := domain.StarGift{
		ID: 8002, RevisionID: 9002, Stars: 50, ConvertStars: 50, Title: "Beta", SupportOnly: true,
		Sticker: domain.Document{ID: 701, AccessHash: 8, DCID: 2, MimeType: "application/x-tgsticker", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
	}
	giftStore := memory.NewStarGiftStore()
	giftStore.SeedCatalog([]domain.StarGift{gift})
	gifts := appstargifts.NewService(giftStore, nil, 2)
	starsStore := newStarsTopupRPCStore()
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398, PublicBaseURL: "https://links.example.test", AllowDevPayments: true}, Deps{
		Users:    appusers.NewService(users),
		Messages: appmessages.NewService(msgStore, dialogs),
		Channels: appchannels.NewService(channelStore),
		Stars:    appstars.NewService(starsStore, appstars.WithStartingGrant(1000), appstars.WithPurchaseStore(starsStore)),
		Gifts:    gifts,
	}, zaptest.NewLogger(t), clock.System)

	inv := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, GiftID: gift.ID}

	regularCtx := WithUserID(ctx, regular.ID)
	checkRes, err := r.onPaymentsCheckCanSendGift(regularCtx, &tg.PaymentsCheckCanSendGiftRequest{GiftID: gift.ID})
	if err != nil {
		t.Fatalf("regular checkCanSendGift err=%v", err)
	}
	if _, ok := checkRes.(*tg.PaymentsCheckCanSendGiftResultFail); !ok {
		t.Fatalf("regular checkCanSendGift result=%T, want ResultFail (sold out)", checkRes)
	}

	supportCtx := WithUserID(ctx, helper.ID)
	checkResSupport, err := r.onPaymentsCheckCanSendGift(supportCtx, &tg.PaymentsCheckCanSendGiftRequest{GiftID: gift.ID})
	if err != nil {
		t.Fatalf("support checkCanSendGift err=%v", err)
	}
	if _, ok := checkResSupport.(*tg.PaymentsCheckCanSendGiftResultOk); !ok {
		t.Fatalf("support checkCanSendGift result=%T, want ResultOk", checkResSupport)
	}
	formRes, err := r.onPaymentsGetPaymentForm(supportCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("support getPaymentForm: %v", err)
	}
	form, ok := formRes.(*tg.PaymentsPaymentFormStarGift)
	if !ok {
		t.Fatalf("support gift form=%T", formRes)
	}
	if _, err := r.onPaymentsSendStarsForm(supportCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv}); err != nil {
		t.Fatalf("support gift purchase: %v", err)
	}
	if _, err := r.onPaymentsSendStarsForm(regularCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv}); !tgerr.Is(err, "STARGIFT_USAGE_LIMITED") {
		t.Fatalf("regular sendStarsForm err=%v, want STARGIFT_USAGE_LIMITED", err)
	}
}

func TestStarsGiveawayCatalogFormSettlementReplayAndInfo(t *testing.T) {
	ctx := context.Background()
	now := 1_700_000_100
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 7201, Phone: "15550007201", FirstName: "GiveawayOwner"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 7202, Phone: "15550007202", FirstName: "GiveawayMember"})
	if err != nil {
		t.Fatal(err)
	}
	channelStore := memory.NewChannelStore()
	created, err := channelStore.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Giveaway Group", Megagroup: true,
		MemberUserIDs: []int64{member.ID}, Date: now - 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	starsStore := newStarsTopupRPCStore()
	starsStore.channel = created.Channel
	r := New(Config{DC: 2, PublicBaseURL: "https://links.example.test", AllowDevPayments: true}, Deps{
		Users: appusers.NewService(users), Channels: appchannels.NewService(channelStore),
		Stars: appstars.NewService(starsStore, appstars.WithStartingGrant(0), appstars.WithPurchaseStore(starsStore)),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(int64(now), 0)})
	ownerCtx := WithUserID(ctx, owner.ID)
	options, err := r.onPaymentsGetStarsGiveawayOptions(ownerCtx)
	if err != nil || len(options) != 3 || len(options[0].Winners) < 2 || options[0].StoreProduct != "" {
		t.Fatalf("giveaway options=%+v err=%v", options, err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	purpose := &tg.InputStorePaymentStarsGiveaway{
		WinnersAreVisible: true, Stars: options[0].Stars, BoostPeer: peer,
		RandomID: 7201001, UntilDate: now + 3600, Currency: options[0].Currency,
		Amount: options[0].Amount, Users: options[0].Winners[1].Users,
	}
	invoice := &tg.InputInvoiceStars{Purpose: purpose}
	validated, err := r.onPaymentsValidateRequestedInfo(ownerCtx, &tg.PaymentsValidateRequestedInfoRequest{Invoice: invoice})
	if err != nil || validated == nil || !validated.Zero() {
		t.Fatalf("validate giveaway requested info=%+v err=%v", validated, err)
	}
	if len(starsStore.forms) != 0 || len(starsStore.settled) != 0 {
		t.Fatalf("giveaway validation mutated store: forms=%d settled=%d", len(starsStore.forms), len(starsStore.settled))
	}
	formClass, err := r.onPaymentsGetPaymentForm(ownerCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		t.Fatalf("get giveaway payment form: %v", err)
	}
	form, ok := formClass.(*tg.PaymentsPaymentForm)
	if !ok || form.FormID == 0 || !form.Invoice.Test || form.Invoice.Currency != purpose.Currency ||
		len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != purpose.Amount {
		t.Fatalf("giveaway payment form=%T %+v", formClass, formClass)
	}
	if _, err := r.onPaymentsSendStarsForm(ownerCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: invoice}); !tgerr.Is(err, "PAYMENT_CREDENTIALS_INVALID") {
		t.Fatalf("sendStarsForm fiat giveaway err=%v", err)
	}
	resultClass, err := r.onPaymentsSendPaymentForm(ownerCtx, &tg.PaymentsSendPaymentFormRequest{
		FormID: form.FormID, Invoice: invoice, Credentials: devStarsCredentials(form.FormID),
	})
	if err != nil {
		t.Fatalf("send giveaway form: %v", err)
	}
	result, ok := resultClass.(*tg.PaymentsPaymentResult)
	if !ok {
		t.Fatalf("giveaway result=%T", resultClass)
	}
	updates, ok := result.Updates.(*tg.Updates)
	if !ok || len(updates.Updates) == 0 {
		t.Fatalf("giveaway updates=%T %+v", result.Updates, result.Updates)
	}
	launchID := 0
	for _, update := range updates.Updates {
		newChannel, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok || newChannel.PtsCount != 1 {
			continue
		}
		message, ok := newChannel.Message.(*tg.Message)
		if !ok {
			t.Fatalf("giveaway launch message=%T", newChannel.Message)
		}
		media, ok := message.Media.(*tg.MessageMediaGiveaway)
		if !ok || media.Stars != purpose.Stars || media.Quantity != purpose.Users || media.UntilDate != purpose.UntilDate {
			t.Fatalf("giveaway launch media=%T %+v", message.Media, message.Media)
		}
		launchID = message.ID
	}
	if launchID == 0 {
		t.Fatalf("giveaway updates missing updateNewChannelMessage: %+v", updates.Updates)
	}
	if _, err := r.onPaymentsSendPaymentForm(ownerCtx, &tg.PaymentsSendPaymentFormRequest{
		FormID: form.FormID, Invoice: invoice, Credentials: devStarsCredentials(form.FormID),
	}); err != nil {
		t.Fatalf("Android sendPaymentForm replay: %v", err)
	}
	infoClass, err := r.onPaymentsGetGiveawayInfo(ownerCtx, &tg.PaymentsGetGiveawayInfoRequest{Peer: peer, MsgID: launchID})
	info, ok := infoClass.(*tg.PaymentsGiveawayInfo)
	if err != nil || !ok || !info.Participating || info.StartDate == 0 {
		t.Fatalf("get giveaway info=%T %+v err=%v", infoClass, infoClass, err)
	}
	memberPurpose := *purpose
	memberPurpose.RandomID++
	if _, err := r.onPaymentsGetPaymentForm(WithUserID(ctx, member.ID), &tg.PaymentsGetPaymentFormRequest{
		Invoice: &tg.InputInvoiceStars{Purpose: &memberPurpose},
	}); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member giveaway form err=%v, want CHAT_ADMIN_REQUIRED", err)
	}
}

type uniqueGiftRPCService struct {
	GiftsService
	unique domain.UniqueStarGift
}

func (s *uniqueGiftRPCService) UniqueBySlug(_ context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	return s.unique, slug == s.unique.Slug, nil
}

type starGiftMessageAliasRPCService struct {
	GiftsService
	viewerUserID int64
	msgID        int
	ref          domain.SavedStarGiftRef
}

func (s *starGiftMessageAliasRPCService) ResolveUserMessageRef(_ context.Context, viewerUserID int64, msgID int) (domain.SavedStarGiftRef, bool, error) {
	if viewerUserID == s.viewerUserID && msgID == s.msgID {
		return s.ref, true, nil
	}
	return domain.SavedStarGiftRef{}, false, nil
}

func TestStarGiftUserInputResolvesOnlyExplicitChannelNotificationAlias(t *testing.T) {
	channelRef := domain.SavedStarGiftRef{
		Owner:   domain.Peer{Type: domain.PeerTypeChannel, ID: 8801},
		SavedID: 91,
	}
	service := &starGiftMessageAliasRPCService{viewerUserID: 7102, msgID: 44, ref: channelRef}
	r := New(Config{DC: 2}, Deps{Gifts: service}, zaptest.NewLogger(t), clock.System)

	resolved, ok, err := r.starGiftRefFromInput(context.Background(), 7102, &tg.InputSavedStarGiftUser{MsgID: 44})
	if err != nil || !ok || resolved != channelRef {
		t.Fatalf("channel notification alias = %+v ok=%v err=%v", resolved, ok, err)
	}
	fallback, ok, err := r.starGiftRefFromInput(context.Background(), 7102, &tg.InputSavedStarGiftUser{MsgID: 45})
	wantFallback := domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 7102}, MsgID: 45}
	if err != nil || !ok || fallback != wantFallback {
		t.Fatalf("unknown notification alias = %+v ok=%v err=%v, want user fallback %+v", fallback, ok, err, wantFallback)
	}
}

type starGiftNotificationSettingsRPCService struct {
	GiftsService
	enabled bool
}

func (s *starGiftNotificationSettingsRPCService) NotificationsEnabled(context.Context, int64, int64) (bool, error) {
	return s.enabled, nil
}

type craftStarGiftRPCService struct {
	GiftsService
	uniques   map[string]domain.UniqueStarGift
	saved     map[int64]domain.SavedStarGift
	result    domain.StarGiftCraftResult
	craftReq  domain.StarGiftCraftRequest
	craftCall int
}

func (s *craftStarGiftRPCService) UniqueBySlug(_ context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	unique, ok := s.uniques[slug]
	return unique, ok, nil
}

func (s *craftStarGiftRPCService) GetSaved(_ context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error) {
	for _, saved := range s.saved {
		if saved.Owner != ref.Owner {
			continue
		}
		if ref.Slug != "" {
			unique, ok := s.uniques[ref.Slug]
			if ok && unique.ID == saved.UniqueGiftID {
				return saved, true, nil
			}
			continue
		}
		if saved.MsgID == ref.MsgID || saved.UpgradeMsgID == ref.MsgID {
			return saved, true, nil
		}
	}
	return domain.SavedStarGift{}, false, nil
}

func (s *craftStarGiftRPCService) Craft(_ context.Context, req domain.StarGiftCraftRequest) (domain.StarGiftCraftResult, error) {
	s.craftCall++
	s.craftReq = req
	return s.result, nil
}

func TestCraftStarGiftAcceptsOfficialSlugAndCanonicalizesAliases(t *testing.T) {
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 7102}
	service := &craftStarGiftRPCService{
		uniques: map[string]domain.UniqueStarGift{
			"official-8001-2": {ID: 902, Slug: "official-8001-2", Owner: owner, SourceSavedGiftID: 52},
		},
		saved: map[int64]domain.SavedStarGift{
			50: {ID: 50, Owner: owner, MsgID: 115, UniqueGiftID: 901, UpgradeMsgID: 116},
			52: {ID: 52, Owner: owner, MsgID: 111, UniqueGiftID: 902, UpgradeMsgID: 112},
		},
		result: domain.StarGiftCraftResult{Chance: 500, SourceEdits: []domain.EditedMessageForUser{{
			UserID:  owner.ID,
			Message: domain.Message{ID: 116, OwnerUserID: owner.ID, Peer: owner, From: owner, Date: 100},
			Event: domain.UpdateEvent{UserID: owner.ID, Type: domain.UpdateEventEditMessage, Pts: 41, PtsCount: 1,
				Date: 100, Message: domain.Message{ID: 116, OwnerUserID: owner.ID, Peer: owner, From: owner, Date: 100}},
		}}},
	}
	r := New(Config{DC: 2}, Deps{Gifts: service}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), owner.ID)
	updates, err := r.onPaymentsCraftStarGift(ctx, &tg.PaymentsCraftStarGiftRequest{Stargift: []tg.InputSavedStarGiftClass{
		&tg.InputSavedStarGiftUser{MsgID: 115},
		&tg.InputSavedStarGiftSlug{Slug: "OFFICIAL-8001-2"},
	}})
	if err != nil || updates == nil {
		t.Fatalf("craft mixed official refs: updates=%T err=%v", updates, err)
	}
	if service.craftCall != 1 || service.craftReq.CommandKey != "rpc:50,52" || len(service.craftReq.Refs) != 2 ||
		service.craftReq.Refs[1].Slug != "official-8001-2" {
		t.Fatalf("craft request = %+v calls=%d", service.craftReq, service.craftCall)
	}
	full, ok := updates.(*tg.Updates)
	if !ok || len(full.Updates) != 2 {
		t.Fatalf("craft failure updates = %T %#v", updates, updates)
	}
	if edit, ok := full.Updates[0].(*tg.UpdateEditMessage); !ok || edit.Pts != 41 || edit.PtsCount != 1 {
		t.Fatalf("craft failure source update = %T %#v", full.Updates[0], full.Updates[0])
	}
	if _, ok := full.Updates[1].(*tg.UpdateStarGiftCraftFail); !ok {
		t.Fatalf("craft terminal update = %T %#v", full.Updates[1], full.Updates[1])
	}

	service.craftCall = 0
	_, err = r.onPaymentsCraftStarGift(ctx, &tg.PaymentsCraftStarGiftRequest{Stargift: []tg.InputSavedStarGiftClass{
		&tg.InputSavedStarGiftUser{MsgID: 111},
		&tg.InputSavedStarGiftSlug{Slug: "official-8001-2"},
	}})
	if !tgerr.Is(err, "STARGIFT_INVALID") || service.craftCall != 0 {
		t.Fatalf("duplicate aliases err=%v craft calls=%d", err, service.craftCall)
	}

	updates, err = r.onPaymentsCraftStarGift(ctx, &tg.PaymentsCraftStarGiftRequest{Stargift: []tg.InputSavedStarGiftClass{
		&tg.InputSavedStarGiftUser{MsgID: 116},
	}})
	if err != nil || updates == nil || service.craftCall != 1 || service.craftReq.CommandKey != "rpc:50" ||
		len(service.craftReq.Refs) != 1 || service.craftReq.Refs[0].MsgID != 116 {
		t.Fatalf("upgrade message alias craft: updates=%T req=%+v err=%v calls=%d", updates, service.craftReq, err, service.craftCall)
	}
}

func TestCraftStarGiftSuccessCarriesClientSafeCraftMessageOnWire(t *testing.T) {
	r, _, ownerUser, _ := starGiftTestRouter(t)
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	model := collectibleRPCAttribute(domain.StarGiftCollectibleModel, 7801, "Crafted Model")
	model.Crafted = true
	model.RarityKind = domain.StarGiftRarityUncommon
	model.RarityPermille = 0
	pattern := collectibleRPCAttribute(domain.StarGiftCollectiblePattern, 7802, "Pattern")
	backdrop := collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, 7803, "Backdrop")
	unique := domain.UniqueStarGift{
		ID: 9901, GiftID: 8001, SourceSavedGiftID: 51, Title: "Crafted Gift",
		Slug: "crafted-gift-1", Num: 1, Owner: owner, Crafted: true,
		Model: model, Pattern: pattern, Backdrop: backdrop,
		AvailabilityIssued: 1, AvailabilityTotal: 100,
	}
	outputAction := &domain.MessageStarGiftUniqueAction{
		Gift: unique, FromUserID: owner.ID, Saved: true, Craft: true,
	}
	outputMessage := domain.Message{
		ID: 120, OwnerUserID: owner.ID, Peer: owner, From: owner, Out: true, Date: 1700000100,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftUnique, StarGiftUnique: outputAction,
		}},
	}
	outputEvent := domain.UpdateEvent{
		UserID: owner.ID, Type: domain.UpdateEventNewMessage, Pts: 42, PtsCount: 1,
		Date: outputMessage.Date, Message: outputMessage,
	}
	sourceMessage := outputMessage
	sourceMessage.ID = 119
	sourceMessage.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
		Kind:           domain.MessageServiceActionStarGiftUnique,
		StarGiftUnique: &domain.MessageStarGiftUniqueAction{Gift: unique, FromUserID: owner.ID, Saved: true},
	}}
	sourceEvent := domain.UpdateEvent{
		UserID: owner.ID, Type: domain.UpdateEventEditMessage, Pts: 41, PtsCount: 1,
		Date: outputMessage.Date, Message: sourceMessage,
	}
	service := &craftStarGiftRPCService{
		uniques: map[string]domain.UniqueStarGift{unique.Slug: unique},
		saved: map[int64]domain.SavedStarGift{
			51: {ID: 51, Owner: owner, MsgID: 118, UpgradeMsgID: 119, UniqueGiftID: unique.ID},
		},
		result: domain.StarGiftCraftResult{
			Success: true, Gift: &unique,
			Send: domain.SendPrivateTextResult{
				SenderMessage: outputMessage,
				SenderEvent:   outputEvent,
			},
			SourceEdits: []domain.EditedMessageForUser{{
				UserID: owner.ID, Message: sourceMessage, Event: sourceEvent,
			}},
		},
	}
	r.deps.Gifts = service
	updatesClass, err := r.onPaymentsCraftStarGift(WithUserID(context.Background(), owner.ID),
		&tg.PaymentsCraftStarGiftRequest{Stargift: []tg.InputSavedStarGiftClass{
			&tg.InputSavedStarGiftSlug{Slug: unique.Slug},
		}})
	if err != nil {
		t.Fatalf("craft success: %v", err)
	}
	updates, ok := updatesClass.(*tg.Updates)
	if !ok || len(updates.Updates) != 2 {
		t.Fatalf("craft success updates = %T %#v", updatesClass, updatesClass)
	}
	if edit, ok := updates.Updates[0].(*tg.UpdateEditMessage); !ok || edit.Pts != sourceEvent.Pts {
		t.Fatalf("craft source update = %T %#v", updates.Updates[0], updates.Updates[0])
	}
	if created, ok := updates.Updates[1].(*tg.UpdateNewMessage); !ok || created.Pts != outputEvent.Pts {
		t.Fatalf("craft result update = %T %#v", updates.Updates[1], updates.Updates[1])
	}

	wire := &bin.Buffer{}
	if err := tlprofile.EncodeObject(tlprofile.Profile228, updates, wire); err != nil {
		t.Fatalf("encode Layer 228 craft result: %v", err)
	}
	decodedObject, err := tlprofile.DecodeObject(tlprofile.Profile228, &bin.Buffer{Buf: wire.Copy()}, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("decode Layer 228 craft result: %v", err)
	}
	decoded, ok := decodedObject.(*tg.Updates)
	if !ok || len(decoded.Updates) != 2 {
		t.Fatalf("decoded craft result = %T %#v", decodedObject, decodedObject)
	}
	created, ok := decoded.Updates[1].(*tg.UpdateNewMessage)
	if !ok {
		t.Fatalf("decoded terminal update = %T", decoded.Updates[1])
	}
	message, ok := created.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("decoded craft message = %T", created.Message)
	}
	action, ok := message.Action.(*tg.MessageActionStarGiftUnique)
	if !ok || !action.Craft || !action.Saved {
		t.Fatalf("decoded craft action = %T %#v", message.Action, message.Action)
	}
	gift, ok := action.Gift.(*tg.StarGiftUnique)
	if !ok || !gift.Crafted || gift.ID != unique.ID || gift.Slug != unique.Slug || len(gift.Attributes) < 3 {
		t.Fatalf("decoded crafted gift = %T %#v", action.Gift, action.Gift)
	}
	wireModel, ok := gift.Attributes[0].(*tg.StarGiftAttributeModel)
	if !ok || !wireModel.Crafted {
		t.Fatalf("decoded crafted model = %T %#v", gift.Attributes[0], gift.Attributes[0])
	}
	if _, ok := wireModel.Document.(*tg.Document); !ok {
		t.Fatalf("decoded crafted model document = %T", wireModel.Document)
	}
	wirePattern, ok := gift.Attributes[1].(*tg.StarGiftAttributePattern)
	if !ok {
		t.Fatalf("decoded crafted pattern = %T", gift.Attributes[1])
	}
	if _, ok := wirePattern.Document.(*tg.Document); !ok {
		t.Fatalf("decoded crafted pattern document = %T", wirePattern.Document)
	}
}

type upgradeReplayRPCService struct {
	GiftsService
	saved        domain.SavedStarGift
	receipt      domain.StarGiftUpgradeReceipt
	result       domain.StarGiftUpgradeResult
	upgradeCalls int
	previewCalls int
	lastRequest  domain.StarGiftUpgradeRequest
}

func (s *upgradeReplayRPCService) GetSaved(_ context.Context, _ domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error) {
	return s.saved, true, nil
}

func (s *upgradeReplayRPCService) UpgradeReceipt(_ context.Context, userID int64, _ string) (domain.StarGiftUpgradeReceipt, bool, error) {
	if userID != s.receipt.UserID {
		return domain.StarGiftUpgradeReceipt{}, false, nil
	}
	return s.receipt, true, nil
}

func (s *upgradeReplayRPCService) CollectiblePreview(context.Context, int64) (domain.StarGiftUpgradePreview, bool, error) {
	s.previewCalls++
	return domain.StarGiftUpgradePreview{}, false, nil
}

func (s *upgradeReplayRPCService) Upgrade(_ context.Context, req domain.StarGiftUpgradeRequest) (domain.StarGiftUpgradeResult, error) {
	s.upgradeCalls++
	s.lastRequest = req
	return s.result, nil
}

func collectibleRPCAttribute(kind domain.StarGiftCollectibleAttributeKind, id int64, name string) domain.StarGiftCollectibleAttribute {
	attribute := domain.StarGiftCollectibleAttribute{
		Kind: kind, Name: name, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
	}
	if kind == domain.StarGiftCollectibleBackdrop {
		attribute.BackdropID = int(id)
		attribute.CenterColor = 0x112233
		attribute.EdgeColor = 0x223344
		attribute.PatternColor = 0x334455
		attribute.TextColor = 0xffffff
		return attribute
	}
	attribute.Document = &domain.Document{
		ID: id, AccessHash: id + 1, FileReference: []byte("collectible-rpc"), Date: 1700000000,
		MimeType: "application/x-tgsticker", Size: 3, DCID: 2,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}, {Kind: domain.DocAttrFilename, FileName: "gift.tgs"}},
	}
	if kind == domain.StarGiftCollectiblePattern {
		attribute.Document.Attributes[0] = domain.DocumentAttribute{Kind: domain.DocAttrCustomEmoji, TextColor: true}
		attribute.Document.Thumbs = []domain.PhotoSize{{Kind: domain.PhotoSizeKindPath, Type: "j", Bytes: []byte{1}}}
	}
	attribute.Animation = &domain.StarGiftAnimation{
		SourceName: "gift.tgs", SourceFormat: domain.StarGiftAnimationTGS,
		JSON: []byte(`{"v":"5.7"}`), TGS: []byte("tgs"), SHA256: make([]byte, 32), Width: 512, Height: 512,
	}
	attribute.Blob = &domain.FileBlob{LocationKey: "doc:test", Backend: domain.MediaBackendLocalFS, ObjectKey: "test", Size: 3}
	return attribute
}

func TestSavedStarGiftProjectionCombinesHistoricalCatalogWithCurrentCollectibleAvailability(t *testing.T) {
	historical := domain.StarGift{
		ID: 8001, RevisionID: 9001, Stars: 50, ConvertStars: 25, Title: "Historical Cake",
		Sticker: domain.Document{ID: 700, AccessHash: 7, DCID: 2, MimeType: "application/x-tgsticker"},
	}
	saved := domain.SavedStarGift{GiftID: historical.ID, RevisionID: historical.RevisionID, MsgID: 44, Date: 100, ConvertStars: historical.ConvertStars}
	availability := map[int64]domain.StarGiftCollectibleAvailability{
		historical.ID: {UpgradeStars: 75, SupplyTotal: 500, Issued: 12},
	}

	projected := tgSavedStarGifts(0, []domain.SavedStarGift{saved}, map[int64]domain.StarGift{historical.RevisionID: historical}, availability, 0)
	if len(projected) != 1 || !projected[0].CanUpgrade {
		t.Fatalf("saved gift = %#v, want current pool to make historical gift upgradable", projected)
	}
	gift, ok := projected[0].Gift.(*tg.StarGift)
	if !ok {
		t.Fatalf("saved gift inner = %T, want *tg.StarGift", projected[0].Gift)
	}
	if gift.Title != historical.Title || gift.Stars != historical.Stars || gift.ConvertStars != historical.ConvertStars {
		t.Fatalf("historical snapshot changed: %#v", gift)
	}
	if upgradeStars, ok := gift.GetUpgradeStars(); !ok || upgradeStars != 75 {
		t.Fatalf("upgrade_stars = %d ok=%v, want current price 75", upgradeStars, ok)
	}
	for _, profile := range []tlprofile.Profile{tlprofile.Profile227, tlprofile.Profile228} {
		wire := &tg.PaymentsSavedStarGifts{Count: 1, Gifts: projected, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
		encoded := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, wire, encoded); err != nil {
			t.Fatalf("encode Layer %d saved gift: %v", profile, err)
		}
		decodedObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: encoded.Buf}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode Layer %d saved gift: %v", profile, err)
		}
		decoded, ok := decodedObject.(*tg.PaymentsSavedStarGifts)
		if !ok {
			t.Fatalf("decode Layer %d saved gift type = %T", profile, decodedObject)
		}
		inner, ok := decoded.Gifts[0].Gift.(*tg.StarGift)
		if !ok || !decoded.Gifts[0].CanUpgrade || inner.UpgradeStars != 75 {
			t.Fatalf("Layer %d projection lost upgrade flags: %#v", profile, decoded.Gifts[0])
		}
	}

	availability[historical.ID] = domain.StarGiftCollectibleAvailability{UpgradeStars: 75, SupplyTotal: 500, Issued: 500}
	soldOut := tgSavedStarGifts(0, []domain.SavedStarGift{saved}, map[int64]domain.StarGift{historical.RevisionID: historical}, availability, 0)[0]
	if soldOut.CanUpgrade {
		t.Fatal("sold-out collectible pool must not advertise upgrade")
	}
	if gift, ok := soldOut.Gift.(*tg.StarGift); !ok {
		t.Fatalf("sold-out inner = %T, want *tg.StarGift", soldOut.Gift)
	} else if _, ok := gift.GetUpgradeStars(); ok {
		t.Fatal("sold-out catalog projection must not expose upgrade_stars")
	}
	saved.PrepaidUpgradeStars = 75
	soldOutPrepaid := tgSavedStarGifts(0, []domain.SavedStarGift{saved}, map[int64]domain.StarGift{historical.RevisionID: historical}, availability, 0)[0]
	if soldOutPrepaid.CanUpgrade {
		t.Fatal("sold-out prepaid gift must not advertise an upgrade the aggregate will reject")
	}
	if _, ok := soldOutPrepaid.GetUpgradeStars(); ok {
		t.Fatal("sold-out prepaid gift must not expose stale prepaid upgrade_stars")
	}
}

// An auction-acquired gift must be priced at the winning bid, both on the
// profile card and inside the award service message. The catalog revision only
// holds the auction's starting price (which is also min_bid_amount), so
// projecting it made every win render as "won with a bid of <starting price>".
func TestAuctionAcquiredStarGiftProjectsWinningBid(t *testing.T) {
	catalog := domain.StarGift{
		ID: 8101, RevisionID: 9101, Stars: 50, Title: "Auction Cake",
		Sticker: domain.Document{ID: 710, AccessHash: 7, DCID: 2, MimeType: "application/x-tgsticker"},
	}
	revisions := map[int64]domain.StarGift{catalog.RevisionID: catalog}
	saved := domain.SavedStarGift{GiftID: catalog.ID, RevisionID: catalog.RevisionID, MsgID: 51, Date: 100,
		GiftNum: 3, PaidStars: 1275}

	projected := tgSavedStarGifts(0, []domain.SavedStarGift{saved}, revisions, nil, 0)
	if len(projected) != 1 {
		t.Fatalf("projected = %#v, want one saved gift", projected)
	}
	gift, ok := projected[0].Gift.(*tg.StarGift)
	if !ok {
		t.Fatalf("saved gift inner = %T, want *tg.StarGift", projected[0].Gift)
	}
	if gift.Stars != saved.PaidStars {
		t.Fatalf("auction gift stars = %d, want winning bid %d", gift.Stars, saved.PaidStars)
	}
	if num, ok := projected[0].GetGiftNum(); !ok || num != 3 {
		t.Fatalf("gift_num = %d ok=%v, want release number 3", num, ok)
	}

	// Ordinary gifts keep projecting the immutable historical catalog price.
	saved.PaidStars = 0
	plain := tgSavedStarGifts(0, []domain.SavedStarGift{saved}, revisions, nil, 0)[0]
	inner, ok := plain.Gift.(*tg.StarGift)
	if !ok || inner.Stars != catalog.Stars {
		t.Fatalf("non-auction gift = %#v, want catalog price %d", plain.Gift, catalog.Stars)
	}

	action, ok := tgMessageActionStarGift(&domain.MessageStarGiftAction{
		GiftID: catalog.ID, Stars: 1275, Title: catalog.Title, Sticker: &catalog.Sticker,
		AuctionAcquired: true, GiftNum: 3, Saved: true,
	}).(*tg.MessageActionStarGift)
	if !ok {
		t.Fatal("award action is not messageActionStarGift")
	}
	if !action.AuctionAcquired {
		t.Fatal("award action must set auction_acquired")
	}
	awarded, ok := action.Gift.(*tg.StarGift)
	if !ok || awarded.Stars != 1275 {
		t.Fatalf("award action gift = %#v, want winning bid 1275", action.Gift)
	}
}

func TestSavedStarGiftProjectionPreservesCollectibleLifecycle(t *testing.T) {
	const (
		giftID     = int64(8001)
		revision   = int64(9001)
		readyAt    = 1_780_000_123
		exportAt   = 1_780_000_200
		transferAt = 1_780_000_300
		resellAt   = 1_780_000_400
	)
	unique := domain.UniqueStarGift{ID: 9901, GiftID: giftID, Title: "Craftable", Slug: "craftable-1", Num: 1,
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 7102}, CraftChancePermille: 250}
	saved := domain.SavedStarGift{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 7102}, GiftID: giftID, RevisionID: revision,
		MsgID: 44, Date: 100, UniqueGiftID: unique.ID, Unique: &unique,
		CanExportAt: exportAt, TransferStars: 25, CanTransferAt: transferAt, CanResellAt: resellAt,
		DropOriginalDetailsStars: 30, CanCraftAt: readyAt,
	}
	projected := tgSavedStarGifts(0, []domain.SavedStarGift{saved}, nil, nil, 0)
	if len(projected) != 1 {
		t.Fatalf("saved lifecycle projection count = %d", len(projected))
	}
	assertLifecycle := func(t *testing.T, item tg.SavedStarGift) {
		t.Helper()
		if value, ok := item.GetCanExportAt(); !ok || value != exportAt {
			t.Fatalf("can_export_at = %d set=%v", value, ok)
		}
		if value, ok := item.GetTransferStars(); !ok || value != 25 {
			t.Fatalf("transfer_stars = %d set=%v", value, ok)
		}
		if value, ok := item.GetCanTransferAt(); !ok || value != transferAt {
			t.Fatalf("can_transfer_at = %d set=%v", value, ok)
		}
		if value, ok := item.GetCanResellAt(); !ok || value != resellAt {
			t.Fatalf("can_resell_at = %d set=%v", value, ok)
		}
		if value, ok := item.GetDropOriginalDetailsStars(); !ok || value != 30 {
			t.Fatalf("drop_original_details_stars = %d set=%v", value, ok)
		}
		if value, ok := item.GetCanCraftAt(); !ok || value != readyAt {
			t.Fatalf("can_craft_at = %d set=%v", value, ok)
		}
	}
	assertLifecycle(t, projected[0])
	zero := tgSavedStarGifts(0, []domain.SavedStarGift{{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 7102}, GiftID: giftID, RevisionID: revision,
		MsgID: 45, Date: 101, UniqueGiftID: unique.ID, Unique: &unique,
	}}, nil, nil, 0)[0]
	if _, ok := zero.GetCanExportAt(); ok {
		t.Fatal("zero can_export_at must be absent")
	}
	if _, ok := zero.GetTransferStars(); ok {
		t.Fatal("zero transfer_stars must be absent")
	}
	if _, ok := zero.GetCanTransferAt(); ok {
		t.Fatal("zero can_transfer_at must be absent")
	}
	if _, ok := zero.GetCanResellAt(); ok {
		t.Fatal("zero can_resell_at must be absent")
	}
	if _, ok := zero.GetDropOriginalDetailsStars(); ok {
		t.Fatal("zero drop_original_details_stars must be absent")
	}
	if _, ok := zero.GetCanCraftAt(); ok {
		t.Fatal("zero can_craft_at must be absent")
	}
	channelSaved := saved
	channelSaved.Owner = domain.Peer{Type: domain.PeerTypeChannel, ID: 8102}
	channelSaved.MsgID = 0
	channelSaved.SavedID = 51
	channelProjected := tgSavedStarGifts(0, []domain.SavedStarGift{channelSaved}, nil, nil, 0)[0]
	if _, ok := channelProjected.GetCanExportAt(); ok {
		t.Fatal("channel can_export_at must be absent until channel export is executable")
	}
	if _, ok := channelProjected.GetCanCraftAt(); ok {
		t.Fatal("channel can_craft_at must be absent until channel Craft is executable")
	}
	if value, ok := channelProjected.GetSavedID(); !ok || value != channelSaved.SavedID {
		t.Fatalf("channel saved_id = %d set=%v", value, ok)
	}
	for _, profile := range []tlprofile.Profile{tlprofile.Profile225, tlprofile.Profile226, tlprofile.Profile227, tlprofile.Profile228} {
		wire := &tg.PaymentsSavedStarGifts{Count: 1, Gifts: projected, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
		encoded := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, wire, encoded); err != nil {
			t.Fatalf("encode Layer %d saved lifecycle: %v", profile, err)
		}
		decodedObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: encoded.Buf}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode Layer %d saved lifecycle: %v", profile, err)
		}
		decoded, ok := decodedObject.(*tg.PaymentsSavedStarGifts)
		if !ok || len(decoded.Gifts) != 1 {
			t.Fatalf("decode Layer %d saved lifecycle type = %T", profile, decodedObject)
		}
		assertLifecycle(t, decoded.Gifts[0])

		channelWire := &tg.PaymentsSavedStarGifts{Count: 1, Gifts: []tg.SavedStarGift{channelProjected}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
		channelEncoded := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, channelWire, channelEncoded); err != nil {
			t.Fatalf("encode Layer %d channel saved lifecycle: %v", profile, err)
		}
		channelDecodedObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: channelEncoded.Buf}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode Layer %d channel saved lifecycle: %v", profile, err)
		}
		channelDecoded, ok := channelDecodedObject.(*tg.PaymentsSavedStarGifts)
		if !ok || len(channelDecoded.Gifts) != 1 {
			t.Fatalf("decode Layer %d channel saved lifecycle type = %T", profile, channelDecodedObject)
		}
		if _, ok := channelDecoded.Gifts[0].GetCanCraftAt(); ok {
			t.Fatalf("Layer %d channel saved gift exposed can_craft_at", profile)
		}
		if _, ok := channelDecoded.Gifts[0].GetCanExportAt(); ok {
			t.Fatalf("Layer %d channel saved gift exposed can_export_at", profile)
		}
	}
}

func TestChannelUniqueActionSuppressesUnavailableReadinessAcrossProfiles(t *testing.T) {
	const readyAt = 1_780_000_123
	unique := domain.UniqueStarGift{
		ID: 9902, GiftID: 8002, Title: "Channel Craftable", Slug: "channel-craftable-1", Num: 1,
		Owner: domain.Peer{Type: domain.PeerTypeChannel, ID: 8102}, CraftChancePermille: 250,
	}
	action := tgMessageActionStarGiftUnique(&domain.MessageStarGiftUniqueAction{
		Gift: unique, Peer: unique.Owner, SavedID: 52, Saved: true,
		CanExportAt: readyAt - 1, CanCraftAt: readyAt,
	}).(*tg.MessageActionStarGiftUnique)
	if _, ok := action.GetCanExportAt(); ok {
		t.Fatal("channel unique action must not expose can_export_at")
	}
	if _, ok := action.GetCanCraftAt(); ok {
		t.Fatal("channel unique action must not expose can_craft_at")
	}
	projectedGift, ok := action.Gift.(*tg.StarGiftUnique)
	if !ok || projectedGift.CraftChancePermille != unique.CraftChancePermille {
		t.Fatalf("channel unique gift lost intrinsic Craft chance: %#v", action.Gift)
	}
	for _, profile := range []tlprofile.Profile{tlprofile.Profile225, tlprofile.Profile226, tlprofile.Profile227, tlprofile.Profile228} {
		wire := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, action, wire); err != nil {
			t.Fatalf("encode Layer %d channel unique action: %v", profile, err)
		}
		decodedObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: wire.Buf}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode Layer %d channel unique action: %v", profile, err)
		}
		decoded, ok := decodedObject.(*tg.MessageActionStarGiftUnique)
		if !ok {
			t.Fatalf("decode Layer %d channel unique action type = %T", profile, decodedObject)
		}
		if _, ok := decoded.GetCanCraftAt(); ok {
			t.Fatalf("Layer %d channel unique action exposed can_craft_at", profile)
		}
		if _, ok := decoded.GetCanExportAt(); ok {
			t.Fatalf("Layer %d channel unique action exposed can_export_at", profile)
		}
		gift, ok := decoded.Gift.(*tg.StarGiftUnique)
		if !ok || gift.CraftChancePermille != unique.CraftChancePermille {
			t.Fatalf("Layer %d channel unique gift = %#v", profile, decoded.Gift)
		}
	}
}

func TestUniqueGiftSenderMirrorHidesOwnerLifecycleControls(t *testing.T) {
	const (
		ownerID  = int64(7102)
		senderID = int64(7101)
	)
	action := &domain.MessageStarGiftUniqueAction{
		Gift: domain.UniqueStarGift{
			ID: 9903, GiftID: 8003, Title: "Owned gift", Slug: "owned-gift-1", Num: 1,
			Owner: domain.Peer{Type: domain.PeerTypeUser, ID: ownerID},
		},
		TransferStars: 25, ResaleAmount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 100},
		CanExportAt: 100, CanTransferAt: 101, CanResellAt: 102,
		DropOriginalDetailsStars: 25, CanCraftAt: 103,
	}

	owner := tgMessageActionStarGiftUniqueForViewer(action, ownerID).(*tg.MessageActionStarGiftUnique)
	if value, ok := owner.GetDropOriginalDetailsStars(); !ok || value != 25 {
		t.Fatalf("owner drop_original_details_stars = %d set=%v", value, ok)
	}

	sender := tgMessageActionStarGiftUniqueForViewer(action, senderID).(*tg.MessageActionStarGiftUnique)
	if _, ok := sender.GetDropOriginalDetailsStars(); ok {
		t.Fatal("sender mirror exposed drop_original_details_stars")
	}
	if _, ok := sender.GetTransferStars(); ok {
		t.Fatal("sender mirror exposed transfer_stars")
	}
	if _, ok := sender.GetResaleAmount(); ok {
		t.Fatal("sender mirror exposed resale_amount")
	}
	if _, ok := sender.GetCanExportAt(); ok {
		t.Fatal("sender mirror exposed can_export_at")
	}
	if _, ok := sender.GetCanTransferAt(); ok {
		t.Fatal("sender mirror exposed can_transfer_at")
	}
	if _, ok := sender.GetCanResellAt(); ok {
		t.Fatal("sender mirror exposed can_resell_at")
	}
	if _, ok := sender.GetCanCraftAt(); ok {
		t.Fatal("sender mirror exposed can_craft_at")
	}
}

func TestStarGiftLifecycleCraftUnavailableError(t *testing.T) {
	if err := starGiftLifecycleErr(domain.ErrStarGiftCraftUnavailable); !tgerr.Is(err, "STARGIFT_CRAFT_UNAVAILABLE") {
		t.Fatalf("craft unavailable mapping = %v", err)
	}
}

func TestMessageStarGiftProjectionSeparatesPaidPriceFromPrepaidAmount(t *testing.T) {
	ordinary, ok := tgMessageActionStarGift(&domain.MessageStarGiftAction{
		GiftID: 8001, Stars: 50, ConvertStars: 25, CanUpgrade: true, UpgradePriceStars: 75,
	}).(*tg.MessageActionStarGift)
	if !ok {
		t.Fatalf("ordinary action = %T", ordinary)
	}
	ordinaryGift, ok := ordinary.Gift.(*tg.StarGift)
	if !ok {
		t.Fatalf("ordinary inner gift = %T", ordinary.Gift)
	}
	if price, set := ordinaryGift.GetUpgradeStars(); !set || price != 75 {
		t.Fatalf("ordinary inner upgrade_stars = %d set=%v, want paid price 75", price, set)
	}
	if amount, set := ordinary.GetUpgradeStars(); set || amount != 0 || ordinary.PrepaidUpgrade {
		t.Fatalf("ordinary outer upgrade_stars = %d set=%v prepaid=%v, want absent", amount, set, ordinary.PrepaidUpgrade)
	}

	prepaid, ok := tgMessageActionStarGift(&domain.MessageStarGiftAction{
		GiftID: 8001, Stars: 50, ConvertStars: 25, CanUpgrade: true, PrepaidUpgrade: true,
		UpgradePriceStars: 75, UpgradeStars: 75,
	}).(*tg.MessageActionStarGift)
	if !ok {
		t.Fatalf("prepaid action = %T", prepaid)
	}
	if amount, set := prepaid.GetUpgradeStars(); !set || amount != 75 || !prepaid.PrepaidUpgrade {
		t.Fatalf("prepaid outer upgrade_stars = %d set=%v prepaid=%v, want 75", amount, set, prepaid.PrepaidUpgrade)
	}
	upgraded, ok := tgMessageActionStarGift(&domain.MessageStarGiftAction{
		GiftID: 8001, Stars: 50, ConvertStars: 25, UpgradeMsgID: 88,
	}).(*tg.MessageActionStarGift)
	if !ok {
		t.Fatalf("upgraded action = %T", upgraded)
	}
	if msgID, set := upgraded.GetUpgradeMsgID(); !set || msgID != 88 {
		t.Fatalf("upgrade_msg_id = %d set=%v, want 88", msgID, set)
	}
	for _, profile := range []tlprofile.Profile{tlprofile.Profile227, tlprofile.Profile228} {
		wire := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, ordinary, wire); err != nil {
			t.Fatalf("encode Layer %d ordinary action: %v", profile, err)
		}
		decodedObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: wire.Buf}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode Layer %d ordinary action: %v", profile, err)
		}
		decoded, ok := decodedObject.(*tg.MessageActionStarGift)
		if !ok {
			t.Fatalf("decode Layer %d action = %T", profile, decodedObject)
		}
		inner, ok := decoded.Gift.(*tg.StarGift)
		if !ok || inner.UpgradeStars != 75 || decoded.UpgradeStars != 0 || decoded.PrepaidUpgrade {
			t.Fatalf("Layer %d ordinary action lost paid/prepaid split: %#v", profile, decoded)
		}

		upgradedWire := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, upgraded, upgradedWire); err != nil {
			t.Fatalf("encode Layer %d upgraded action: %v", profile, err)
		}
		decodedUpgradedObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: upgradedWire.Buf}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode Layer %d upgraded action: %v", profile, err)
		}
		decodedUpgraded, ok := decodedUpgradedObject.(*tg.MessageActionStarGift)
		if !ok || !decodedUpgraded.Upgraded || decodedUpgraded.UpgradeMsgID != 88 || decodedUpgraded.CanUpgrade {
			t.Fatalf("Layer %d upgraded action lost transition flags: %#v", profile, decodedUpgradedObject)
		}
	}
}

func TestStarGiftPrepaidUpgradeProjectionIsViewerScoped(t *testing.T) {
	const (
		senderID = int64(7101)
		ownerID  = int64(7102)
		giftID   = int64(8101)
		revision = int64(9101)
	)
	action := &domain.MessageStarGiftAction{
		GiftID: giftID, PeerUserID: ownerID, To: domain.Peer{Type: domain.PeerTypeUser, ID: ownerID},
		CanUpgrade: true, PrepaidUpgradeHash: "prepaid-upgrade-hash-0123456789",
	}
	ownerMessage := domain.Message{
		ID: 20, OwnerUserID: ownerID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: senderID},
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGift, StarGift: action,
		}},
	}
	ownerAction := tgMessage(ownerMessage).(*tg.MessageService).Action.(*tg.MessageActionStarGift)
	if !ownerAction.CanUpgrade {
		t.Fatal("owner message lost receiver-only can_upgrade")
	}
	if hash, ok := ownerAction.GetPrepaidUpgradeHash(); ok || hash != "" {
		t.Fatalf("owner message exposed prepaid hash %q set=%v", hash, ok)
	}

	senderMessage := ownerMessage
	senderMessage.OwnerUserID = senderID
	senderMessage.Peer = domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}
	senderMessage.Out = true
	senderAction := tgMessage(senderMessage).(*tg.MessageService).Action.(*tg.MessageActionStarGift)
	if senderAction.CanUpgrade {
		t.Fatal("sender message exposed receiver-only can_upgrade")
	}
	if hash, ok := senderAction.GetPrepaidUpgradeHash(); !ok || hash != action.PrepaidUpgradeHash {
		t.Fatalf("sender message prepaid hash = %q set=%v", hash, ok)
	}

	saved := domain.SavedStarGift{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}, GiftID: giftID, RevisionID: revision,
		MsgID: 20, Date: 100, PrepaidUpgradeHash: action.PrepaidUpgradeHash,
	}
	catalog := map[int64]domain.StarGift{revision: {ID: giftID, RevisionID: revision}}
	availability := map[int64]domain.StarGiftCollectibleAvailability{giftID: {UpgradeStars: 25, SupplyTotal: 10}}
	ownerSaved := tgSavedStarGifts(ownerID, []domain.SavedStarGift{saved}, catalog, availability, 0)[0]
	if hash, ok := ownerSaved.GetPrepaidUpgradeHash(); ok || hash != "" {
		t.Fatalf("owner saved gift exposed prepaid hash %q set=%v", hash, ok)
	}
	viewerSaved := tgSavedStarGifts(senderID, []domain.SavedStarGift{saved}, catalog, availability, 0)[0]
	if hash, ok := viewerSaved.GetPrepaidUpgradeHash(); !ok || hash != action.PrepaidUpgradeHash {
		t.Fatalf("non-owner saved gift prepaid hash = %q set=%v", hash, ok)
	}
}

func TestStarGiftUpgradeRPCReplaysCommittedReceiptAfterTerminalTransition(t *testing.T) {
	r, sender, owner, gift := starGiftTestRouter(t)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	saved := domain.SavedStarGift{
		ID: 47, Owner: ownerPeer, FromUserID: sender.ID, GiftID: gift.ID, RevisionID: gift.RevisionID,
		MsgID: 105, UniqueGiftID: 9200000000000004,
	}
	result := domain.StarGiftUpgradeResult{
		Saved: saved, Unique: domain.UniqueStarGift{ID: saved.UniqueGiftID, GiftID: gift.ID, Owner: ownerPeer},
		Balance: domain.StarsBalance{UserID: owner.ID, Balance: 1000}, Duplicate: true,
		Send: domain.SendPrivateTextResult{
			RecipientMessage: domain.Message{ID: 107, OwnerUserID: owner.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}, From: domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}, Date: 1700000001},
			RecipientEvent:   domain.UpdateEvent{UserID: owner.ID, Pts: 42, PtsCount: 1, Date: 1700000001},
		},
	}
	service := &upgradeReplayRPCService{GiftsService: r.deps.Gifts, saved: saved, result: result,
		receipt: domain.StarGiftUpgradeReceipt{UserID: owner.ID, SourceSavedGiftID: saved.ID,
			UniqueGiftID: saved.UniqueGiftID, RequirePrepaid: true, KeepOriginalDetails: true, BalanceAfter: 1000}}
	r.deps.Gifts = service
	ctx := WithUserID(context.Background(), owner.ID)
	if _, err := r.onPaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{
		KeepOriginalDetails: true, Stargift: &tg.InputSavedStarGiftUser{MsgID: saved.MsgID},
	}); err != nil {
		t.Fatalf("replay prepaid upgrade after terminal transition: %v", err)
	}
	if service.upgradeCalls != 1 || service.previewCalls != 0 || !service.lastRequest.RequirePrepaid || service.lastRequest.ChargeStars != 0 {
		t.Fatalf("prepaid replay calls=%d preview=%d req=%+v", service.upgradeCalls, service.previewCalls, service.lastRequest)
	}

	const paidFormID int64 = -7611777087885039132
	service.receipt = domain.StarGiftUpgradeReceipt{UserID: owner.ID, SourceSavedGiftID: saved.ID,
		FormID: paidFormID, UniqueGiftID: saved.UniqueGiftID, ChargeStars: 25,
		KeepOriginalDetails: true, BalanceAfter: 975}
	service.upgradeCalls, service.previewCalls = 0, 0
	if _, err := r.sendStarGiftUpgradeForm(ctx, owner.ID, paidFormID, &tg.InputInvoiceStarGiftUpgrade{
		KeepOriginalDetails: true, Stargift: &tg.InputSavedStarGiftUser{MsgID: saved.MsgID},
	}); err != nil {
		t.Fatalf("replay paid upgrade after terminal transition: %v", err)
	}
	if service.upgradeCalls != 1 || service.previewCalls != 0 || service.lastRequest.ChargeStars != 25 || service.lastRequest.FormID != paidFormID {
		t.Fatalf("paid replay calls=%d preview=%d req=%+v", service.upgradeCalls, service.previewCalls, service.lastRequest)
	}
}

func TestStarGiftCollectiblePreviewUpgradeFormUniqueAndServiceProjection(t *testing.T) {
	r, sender, owner, gift := starGiftTestRouter(t)
	ctx := context.Background()
	ownerCtx := WithUserID(ctx, owner.ID)
	giftService, ok := r.deps.Gifts.(*appstargifts.Service)
	if !ok {
		t.Fatalf("gift service = %T", r.deps.Gifts)
	}
	model := collectibleRPCAttribute(domain.StarGiftCollectibleModel, 8101, "Aurora")
	modelTwo := collectibleRPCAttribute(domain.StarGiftCollectibleModel, 8104, "Aurora Two")
	crafted := collectibleRPCAttribute(domain.StarGiftCollectibleModel, 8103, "Crafted Aurora")
	crafted.Crafted = true
	crafted.RarityKind = domain.StarGiftRarityLegendary
	crafted.RarityPermille = 0
	pattern := collectibleRPCAttribute(domain.StarGiftCollectiblePattern, 8102, "Orbit")
	patternTwo := collectibleRPCAttribute(domain.StarGiftCollectiblePattern, 8105, "Orbit Two")
	backdrop := collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, 1, "Midnight")
	backdropTwo := collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, 2, "Daylight")
	if _, err := giftService.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: gift.ID, UpgradeStars: 75, SupplyTotal: 500, SlugPrefix: "cake",
		Models: []domain.StarGiftCollectibleAttribute{model, crafted, modelTwo}, Patterns: []domain.StarGiftCollectibleAttribute{pattern, patternTwo},
		Backdrops: []domain.StarGiftCollectibleAttribute{backdrop, backdropTwo}, Actor: "test", CommandID: "collectible-rpc",
	}); err != nil {
		t.Fatalf("publish collectible pool: %v", err)
	}
	if _, err := r.deps.Gifts.RecordSavedGift(ctx, domain.SavedStarGift{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, FromUserID: sender.ID,
		GiftID: gift.ID, RevisionID: gift.RevisionID, MsgID: 444, Date: 1700000000, ConvertStars: gift.ConvertStars,
	}); err != nil {
		t.Fatalf("record upgrade target: %v", err)
	}

	preview, err := r.onPaymentsGetStarGiftUpgradePreview(ownerCtx, gift.ID)
	if err != nil || len(preview.SampleAttributes) != 6 {
		t.Fatalf("upgrade preview = %#v err %v", preview, err)
	}
	attributes, err := r.onPaymentsGetStarGiftUpgradeAttributes(ownerCtx, gift.ID)
	if err != nil || len(attributes.Attributes) != 7 {
		t.Fatalf("upgrade attributes = %#v err %v", attributes, err)
	}
	craftedTG, ok := attributes.Attributes[1].(*tg.StarGiftAttributeModel)
	if !ok || !craftedTG.Crafted {
		t.Fatalf("crafted attribute = %T %#v", attributes.Attributes[1], attributes.Attributes[1])
	}
	if _, ok := craftedTG.Rarity.(*tg.StarGiftAttributeRarityLegendary); !ok {
		t.Fatalf("crafted rarity = %T", craftedTG.Rarity)
	}
	invoice := &tg.InputInvoiceStarGiftUpgrade{Stargift: &tg.InputSavedStarGiftUser{MsgID: 444}}
	formClass, err := r.onPaymentsGetPaymentForm(ownerCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		t.Fatalf("get upgrade payment form: %v", err)
	}
	form, ok := formClass.(*tg.PaymentsPaymentFormStarGift)
	if !ok || form.FormID == 0 || form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != 75 {
		t.Fatalf("upgrade payment form = %T %#v", formClass, formClass)
	}

	unique := domain.UniqueStarGift{
		ID: 9200000000000001, GiftID: gift.ID, Title: gift.Title, Slug: "cake-1", Num: 1,
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		Model: model, Pattern: pattern, Backdrop: backdrop,
		AvailabilityIssued: 1, AvailabilityTotal: 500, KeepOriginalDetails: true,
		OriginalFromUserID: sender.ID, OriginalOwner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		OriginalDate: 1700000000, OriginalMessage: "hello",
	}
	r.deps.Gifts = &uniqueGiftRPCService{GiftsService: r.deps.Gifts, unique: unique}
	uniqueResponse, err := r.onPaymentsGetUniqueStarGift(WithUserID(ctx, sender.ID), unique.Slug)
	if err != nil {
		t.Fatalf("get unique gift = %#v err %v", uniqueResponse, err)
	}
	uniqueGift, ok := uniqueResponse.Gift.(*tg.StarGiftUnique)
	if !ok || uniqueGift.Slug != unique.Slug || len(uniqueGift.Attributes) != 4 || len(uniqueResponse.Users) != 2 {
		t.Fatalf("get unique gift = %#v", uniqueResponse)
	}

	message := domain.Message{Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
		Kind: domain.MessageServiceActionStarGiftUnique,
		StarGiftUnique: &domain.MessageStarGiftUniqueAction{
			Gift: unique, FromUserID: sender.ID, Peer: unique.Owner, SavedID: 444, Upgrade: true, Saved: true,
		},
	}}}
	action, ok := tgMessageServiceAction(message).(*tg.MessageActionStarGiftUnique)
	if !ok {
		t.Fatalf("unique service action type = %T", tgMessageServiceAction(message))
	}
	projectedGift, giftOK := action.Gift.(*tg.StarGiftUnique)
	if !giftOK || !action.Upgrade || !action.Saved || projectedGift.ID != unique.ID || projectedGift.Slug != unique.Slug {
		t.Fatalf("unique service action = %#v", tgMessageServiceAction(message))
	}
	if peer, ok := action.GetPeer(); !ok {
		t.Fatal("unique service action missing owner peer")
	} else if user, ok := peer.(*tg.PeerUser); !ok || user.UserID != owner.ID {
		t.Fatalf("unique service action peer = %#v", peer)
	}
	if savedID, ok := action.GetSavedID(); !ok || savedID != 444 {
		t.Fatalf("unique service action saved_id = %d set=%v, want 444", savedID, ok)
	}
	for _, profile := range []tlprofile.Profile{tlprofile.Profile227, tlprofile.Profile228} {
		responseWire := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, uniqueResponse, responseWire); err != nil {
			t.Fatalf("encode Layer %d unique response: %v", profile, err)
		}
		decodedResponseObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: responseWire.Buf}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode Layer %d unique response: %v", profile, err)
		}
		decodedResponse, ok := decodedResponseObject.(*tg.PaymentsUniqueStarGift)
		if !ok {
			t.Fatalf("decode Layer %d unique response type = %T", profile, decodedResponseObject)
		}
		decodedGift, ok := decodedResponse.Gift.(*tg.StarGiftUnique)
		if !ok || decodedGift.Slug != unique.Slug || len(decodedGift.Attributes) != 4 {
			t.Fatalf("Layer %d unique response lost fields: %#v", profile, decodedResponse.Gift)
		}

		actionWire := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, action, actionWire); err != nil {
			t.Fatalf("encode Layer %d unique action: %v", profile, err)
		}
		decodedActionObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: actionWire.Buf}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode Layer %d unique action: %v", profile, err)
		}
		decodedAction, ok := decodedActionObject.(*tg.MessageActionStarGiftUnique)
		if !ok {
			t.Fatalf("decode Layer %d unique action type = %T", profile, decodedActionObject)
		}
		if decodedActionGift, ok := decodedAction.Gift.(*tg.StarGiftUnique); !ok || !decodedAction.Upgrade || decodedAction.SavedID != 444 || decodedActionGift.Slug != unique.Slug {
			t.Fatalf("Layer %d unique action lost fields: %#v", profile, decodedAction)
		}
	}
}

func TestStarGiftUpgradePreviewBoundsRandomSampleWithoutShrinkingFullAttributes(t *testing.T) {
	r, _, owner, gift := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	giftService, ok := r.deps.Gifts.(*appstargifts.Service)
	if !ok {
		t.Fatalf("gift service = %T", r.deps.Gifts)
	}

	models := make([]domain.StarGiftCollectibleAttribute, 0, 7)
	patterns := make([]domain.StarGiftCollectibleAttribute, 0, 6)
	backdrops := make([]domain.StarGiftCollectibleAttribute, 0, 6)
	for i := 0; i < 6; i++ {
		models = append(models, collectibleRPCAttribute(domain.StarGiftCollectibleModel, int64(8200+i), fmt.Sprintf("Model %d", i)))
		patterns = append(patterns, collectibleRPCAttribute(domain.StarGiftCollectiblePattern, int64(8300+i), fmt.Sprintf("Pattern %d", i)))
		backdrops = append(backdrops, collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, int64(8400+i), fmt.Sprintf("Backdrop %d", i)))
	}
	crafted := collectibleRPCAttribute(domain.StarGiftCollectibleModel, 8299, "Crafted")
	crafted.Crafted = true
	crafted.RarityKind = domain.StarGiftRarityLegendary
	crafted.RarityPermille = 0
	models = append(models, crafted)
	if _, err := giftService.PublishCollectibleRevision(context.Background(), domain.StarGiftCollectibleWrite{
		GiftID: gift.ID, UpgradeStars: 75, SupplyTotal: 500, SlugPrefix: "bounded-preview",
		Models: models, Patterns: patterns, Backdrops: backdrops, Actor: "test", CommandID: "bounded-preview",
	}); err != nil {
		t.Fatalf("publish collectible pool: %v", err)
	}

	preview, err := r.onPaymentsGetStarGiftUpgradePreview(ctx, gift.ID)
	if err != nil {
		t.Fatalf("get bounded upgrade preview: %v", err)
	}
	counts := map[domain.StarGiftCollectibleAttributeKind]int{}
	identities := map[string]struct{}{}
	for _, attribute := range preview.SampleAttributes {
		var kind domain.StarGiftCollectibleAttributeKind
		var identity string
		switch value := attribute.(type) {
		case *tg.StarGiftAttributeModel:
			kind = domain.StarGiftCollectibleModel
			if value.Crafted {
				t.Fatal("ordinary upgrade preview included crafted model")
			}
			identity = fmt.Sprintf("model:%d", value.Document.GetID())
		case *tg.StarGiftAttributePattern:
			kind = domain.StarGiftCollectiblePattern
			identity = fmt.Sprintf("pattern:%d", value.Document.GetID())
		case *tg.StarGiftAttributeBackdrop:
			kind = domain.StarGiftCollectibleBackdrop
			identity = fmt.Sprintf("backdrop:%d", value.BackdropID)
		default:
			t.Fatalf("preview attribute = %T", attribute)
		}
		if _, duplicate := identities[identity]; duplicate {
			t.Fatalf("duplicate preview identity %q", identity)
		}
		identities[identity] = struct{}{}
		counts[kind]++
	}
	if len(preview.SampleAttributes) != 9 || counts[domain.StarGiftCollectibleModel] != 3 ||
		counts[domain.StarGiftCollectiblePattern] != 3 || counts[domain.StarGiftCollectibleBackdrop] != 3 {
		t.Fatalf("bounded preview count = %d kinds=%v, want three per kind", len(preview.SampleAttributes), counts)
	}

	all, err := r.onPaymentsGetStarGiftUpgradeAttributes(ctx, gift.ID)
	if err != nil || len(all.Attributes) != 19 {
		t.Fatalf("complete upgrade attributes = %d err=%v, want 19", len(all.Attributes), err)
	}
}

func TestStarGiftCollectionsCRUDFilterOrderAndPin(t *testing.T) {
	r, _, owner, gift := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	for _, msgID := range []int{101, 102} {
		if _, err := r.deps.Gifts.RecordSavedGift(context.Background(), domain.SavedStarGift{
			Owner: ownerPeer, GiftID: gift.ID, RevisionID: gift.RevisionID,
			MsgID: msgID, Date: 1700000000 + msgID, ConvertStars: gift.ConvertStars,
		}); err != nil {
			t.Fatalf("record saved gift %d: %v", msgID, err)
		}
	}
	ref101 := tg.InputSavedStarGiftClass(&tg.InputSavedStarGiftUser{MsgID: 101})
	ref102 := tg.InputSavedStarGiftClass(&tg.InputSavedStarGiftUser{MsgID: 102})

	first, err := r.onPaymentsCreateStarGiftCollection(ctx, &tg.PaymentsCreateStarGiftCollectionRequest{
		Peer: &tg.InputPeerSelf{}, Title: " Favorites ", Stargift: []tg.InputSavedStarGiftClass{ref101},
	})
	if err != nil {
		t.Fatalf("create first collection: %v", err)
	}
	second, err := r.onPaymentsCreateStarGiftCollection(ctx, &tg.PaymentsCreateStarGiftCollectionRequest{
		Peer: &tg.InputPeerSelf{}, Title: "Archive", Stargift: []tg.InputSavedStarGiftClass{ref102},
	})
	if err != nil {
		t.Fatalf("create second collection: %v", err)
	}
	if first.Title != "Favorites" || first.GiftsCount != 1 || second.GiftsCount != 1 {
		t.Fatalf("created collections = %#v / %#v", first, second)
	}

	listedClass, err := r.onPaymentsGetStarGiftCollections(ctx, &tg.PaymentsGetStarGiftCollectionsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	listed, ok := listedClass.(*tg.PaymentsStarGiftCollections)
	if !ok || len(listed.Collections) != 2 {
		t.Fatalf("list collections = %T %#v, want two", listedClass, listedClass)
	}
	domainCollections, err := r.deps.Gifts.ListCollections(context.Background(), ownerPeer)
	if err != nil {
		t.Fatalf("list domain collections: %v", err)
	}
	if notModified, err := r.onPaymentsGetStarGiftCollections(ctx, &tg.PaymentsGetStarGiftCollectionsRequest{
		Peer: &tg.InputPeerSelf{}, Hash: domain.StarGiftCollectionsHash(domainCollections),
	}); err != nil {
		t.Fatalf("hash list collections: %v", err)
	} else if _, ok := notModified.(*tg.PaymentsStarGiftCollectionsNotModified); !ok {
		t.Fatalf("hash response = %T, want not modified", notModified)
	}

	update := &tg.PaymentsUpdateStarGiftCollectionRequest{Peer: &tg.InputPeerSelf{}, CollectionID: first.CollectionID}
	update.SetTitle("Best")
	update.SetAddStargift([]tg.InputSavedStarGiftClass{ref102})
	update.SetOrder([]tg.InputSavedStarGiftClass{ref102, ref101})
	updated, err := r.onPaymentsUpdateStarGiftCollection(ctx, update)
	if err != nil {
		t.Fatalf("update collection: %v", err)
	}
	if updated.Title != "Best" || updated.GiftsCount != 2 {
		t.Fatalf("updated collection = %#v", updated)
	}

	filteredReq := &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10}
	filteredReq.SetCollectionID(first.CollectionID)
	filtered, err := r.onPaymentsGetSavedStarGifts(ctx, filteredReq)
	if err != nil {
		t.Fatalf("filter saved gifts by collection: %v", err)
	}
	if filtered.Count != 2 || len(filtered.Gifts) != 2 {
		t.Fatalf("filtered gifts count=%d len=%d, want 2/2", filtered.Count, len(filtered.Gifts))
	}
	for _, saved := range filtered.Gifts {
		ids, ok := saved.GetCollectionID()
		if !ok || len(ids) == 0 || ids[0] != first.CollectionID {
			t.Fatalf("saved gift collection projection = %#v", saved)
		}
	}

	if ok, err := r.onPaymentsToggleStarGiftsPinnedToTop(ctx, &tg.PaymentsToggleStarGiftsPinnedToTopRequest{
		Peer: &tg.InputPeerSelf{}, Stargift: []tg.InputSavedStarGiftClass{ref101, ref102},
	}); err != nil || !ok {
		t.Fatalf("pin gifts = %v err %v", ok, err)
	}
	pinned, err := r.onPaymentsGetSavedStarGifts(ctx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
	if err != nil {
		t.Fatalf("list pinned gifts: %v", err)
	}
	if len(pinned.Gifts) != 2 || !pinned.Gifts[0].PinnedToTop || !pinned.Gifts[1].PinnedToTop {
		t.Fatalf("pinned projection = %#v", pinned.Gifts)
	}

	if ok, err := r.onPaymentsReorderStarGiftCollections(ctx, &tg.PaymentsReorderStarGiftCollectionsRequest{
		Peer: &tg.InputPeerSelf{}, Order: []int{second.CollectionID, first.CollectionID},
	}); err != nil || !ok {
		t.Fatalf("reorder collections = %v err %v", ok, err)
	}
	reorderedClass, err := r.onPaymentsGetStarGiftCollections(ctx, &tg.PaymentsGetStarGiftCollectionsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("list reordered collections: %v", err)
	}
	reordered := reorderedClass.(*tg.PaymentsStarGiftCollections)
	if reordered.Collections[0].CollectionID != second.CollectionID {
		t.Fatalf("reordered collections = %#v", reordered.Collections)
	}

	if ok, err := r.onPaymentsDeleteStarGiftCollection(ctx, &tg.PaymentsDeleteStarGiftCollectionRequest{
		Peer: &tg.InputPeerSelf{}, CollectionID: first.CollectionID,
	}); err != nil || !ok {
		t.Fatalf("delete collection = %v err %v", ok, err)
	}
	afterDelete, err := r.onPaymentsGetSavedStarGifts(ctx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
	if err != nil {
		t.Fatalf("list gifts after collection delete: %v", err)
	}
	for _, saved := range afterDelete.Gifts {
		if ids, ok := saved.GetCollectionID(); ok {
			for _, id := range ids {
				if id == first.CollectionID {
					t.Fatalf("deleted collection %d leaked in saved gift %#v", id, saved)
				}
			}
		}
	}
}

// 完整 star gift saga：catalog → getPaymentForm(paymentFormStarGift) → sendStarsForm(扣费+服务消息
// +paymentResult) → 收礼人 getSavedStarGifts → save/convert。
func TestStarGiftSaga(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouter(t)
	ctx := context.Background()
	senderCtx := WithUserID(ctx, sender.ID)
	recipientCtx := WithUserID(ctx, recipient.ID)

	// 1. 目录。
	catRes, err := r.onPaymentsGetStarGifts(senderCtx, 0)
	if err != nil {
		t.Fatalf("getStarGifts: %v", err)
	}
	cat, ok := catRes.(*tg.PaymentsStarGifts)
	if !ok || len(cat.Gifts) != 1 {
		t.Fatalf("catalog = %T %+v, want 1 gift", catRes, catRes)
	}
	if g, ok := cat.Gifts[0].(*tg.StarGift); !ok || g.ID != gift.ID || g.Stars != 50 {
		t.Fatalf("catalog gift = %#v, want id %d stars 50", cat.Gifts[0], gift.ID)
	}
	// 即便 hash 命中也始终回完整目录（DrKLO force-stop 后保留 hash 但丢礼物缓存，
	// 返 NotModified 会让送礼选择器永远空）。
	if again, err := r.onPaymentsGetStarGifts(senderCtx, cat.Hash); err != nil {
		t.Fatalf("getStarGifts hash: %v", err)
	} else if full, ok := again.(*tg.PaymentsStarGifts); !ok || len(full.Gifts) != 1 {
		t.Fatalf("hash match = %T, want full catalog (不返 NotModified)", again)
	}

	inv := &tg.InputInvoiceStarGift{
		HideName: true,
		Peer:     &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		GiftID:   gift.ID,
	}

	// 2. getPaymentForm → paymentFormStarGift（XTR + 非空 prices）。
	formRes, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("getPaymentForm: %v", err)
	}
	form, ok := formRes.(*tg.PaymentsPaymentFormStarGift)
	if !ok {
		t.Fatalf("form = %T, want *tg.PaymentsPaymentFormStarGift (TDesktop 单分支 match)", formRes)
	}
	if form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != 50 {
		t.Fatalf("form invoice = %+v, want XTR + 1 price 50", form.Invoice)
	}

	// 3. sendStarsForm → paymentResult（扣费 + 服务消息 + updateStarsBalance）。
	payRes, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv})
	if err != nil {
		t.Fatalf("sendStarsForm: %v", err)
	}
	pay, ok := payRes.(*tg.PaymentsPaymentResult)
	if !ok {
		t.Fatalf("pay result = %T, want *tg.PaymentsPaymentResult (DrKLO 强转)", payRes)
	}
	updates, ok := pay.Updates.(*tg.Updates)
	if !ok {
		t.Fatalf("pay updates = %T, want *tg.Updates", pay.Updates)
	}
	hasBalance, hasGiftMsg := false, false
	for _, up := range updates.Updates {
		switch u := up.(type) {
		case *tg.UpdateStarsBalance:
			hasBalance = true
			if amt, ok := u.Balance.(*tg.StarsAmount); !ok || amt.Amount != 950 {
				t.Fatalf("updateStarsBalance = %#v, want 950", u.Balance)
			}
		case *tg.UpdateNewMessage:
			if svc, ok := u.Message.(*tg.MessageService); ok {
				if _, ok := svc.Action.(*tg.MessageActionStarGift); ok {
					hasGiftMsg = true
				}
			}
		}
	}
	if !hasBalance || !hasGiftMsg {
		t.Fatalf("pay updates: balance=%v giftMsg=%v, want both (崩溃约束:必返合法 Updates)", hasBalance, hasGiftMsg)
	}
	// 送礼人余额扣 50。
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 950 {
		t.Fatalf("sender balance = %d, want 950", bal.Balance)
	}

	// 4. 收礼人 getSavedStarGifts。
	savedRes, err := r.onPaymentsGetSavedStarGifts(recipientCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("getSavedStarGifts: %v", err)
	}
	if savedRes.Count != 1 || len(savedRes.Gifts) != 1 {
		t.Fatalf("saved gifts = count %d len %d, want 1/1", savedRes.Count, len(savedRes.Gifts))
	}
	saved := savedRes.Gifts[0]
	msgID, ok := saved.GetMsgID()
	if !ok || msgID <= 0 {
		t.Fatalf("saved gift msg_id = %d ok %v, want positive", msgID, ok)
	}
	if g, ok := saved.Gift.(*tg.StarGift); !ok || g.ID != gift.ID {
		t.Fatalf("saved gift inner = %#v, want gift %d", saved.Gift, gift.ID)
	}
	if from, ok := saved.GetFromID(); !ok {
		t.Fatalf("saved gift from = %v, want sender peer", from)
	}
	if !saved.NameHidden {
		t.Fatal("saved gift name_hidden = false, want anonymous gift")
	}
	foundSender := false
	for _, user := range savedRes.Users {
		if got, ok := user.(*tg.User); ok && got.ID == sender.ID {
			foundSender = true
			break
		}
	}
	if !foundSender {
		t.Fatalf("saved gift users = %+v, want anonymous sender %d for receiver", savedRes.Users, sender.ID)
	}

	// 4b. 收礼人 userFull 必须带 stargifts_count（否则客户端资料页 Gifts 区段不出现）。
	fullRes, err := r.onUsersGetFullUser(senderCtx, &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash})
	if err != nil {
		t.Fatalf("getFullUser: %v", err)
	}
	if cnt, ok := fullRes.FullUser.GetStargiftsCount(); !ok || cnt != 1 {
		t.Fatalf("recipient userFull stargifts_count = %d ok %v, want 1 (资料页 Gifts 门控)", cnt, ok)
	}

	// 5. saveStarGift（隐藏）。
	if ok, err := r.onPaymentsSaveStarGift(recipientCtx, &tg.PaymentsSaveStarGiftRequest{
		Unsave: true, Stargift: &tg.InputSavedStarGiftUser{MsgID: msgID},
	}); err != nil || !ok {
		t.Fatalf("saveStarGift hide = %v err %v", ok, err)
	}

	// 6. convertStarGift（转回 Stars，收礼人 +50）。
	recipBefore, _ := r.deps.Stars.GetBalance(ctx, recipient.ID)
	if ok, err := r.onPaymentsConvertStarGift(recipientCtx, &tg.InputSavedStarGiftUser{MsgID: msgID}); err != nil || !ok {
		t.Fatalf("convertStarGift = %v err %v", ok, err)
	}
	recipAfter, _ := r.deps.Stars.GetBalance(ctx, recipient.ID)
	if recipAfter.Balance != recipBefore.Balance+50 {
		t.Fatalf("recipient balance %d -> %d, want +50", recipBefore.Balance, recipAfter.Balance)
	}
	// 转换后从列表消失。
	afterRes, _ := r.onPaymentsGetSavedStarGifts(recipientCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}})
	if afterRes.Count != 0 {
		t.Fatalf("saved gifts after convert = %d, want 0", afterRes.Count)
	}
	// 重复转换被拒。
	if _, err := r.onPaymentsConvertStarGift(recipientCtx, &tg.InputSavedStarGiftUser{MsgID: msgID}); err == nil {
		t.Fatalf("double convert should error")
	}
}

func TestStarGiftAutoSavePrivacyCreatesPendingGiftUntilRecipientApproves(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouter(t)
	ctx := context.Background()
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), memory.NewContactStore())
	if _, err := privacy.SetRules(ctx, recipient.ID, domain.PrivacyKeyStarGiftsAutoSave, []domain.PrivacyRule{
		{Kind: domain.PrivacyRuleDisallowAll},
	}); err != nil {
		t.Fatalf("set gift auto-save privacy: %v", err)
	}
	r.deps.Privacy = privacy
	senderCtx := WithUserID(ctx, sender.ID)
	recipientCtx := WithUserID(ctx, recipient.ID)
	invoice := &tg.InputInvoiceStarGift{
		Peer:   &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		GiftID: gift.ID,
	}
	formClass, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		t.Fatalf("get gift form: %v", err)
	}
	form := formClass.(*tg.PaymentsPaymentFormStarGift)
	if _, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{
		FormID:  form.FormID,
		Invoice: invoice,
	}); err != nil {
		t.Fatalf("send gift: %v", err)
	}

	all, err := r.onPaymentsGetSavedStarGifts(recipientCtx, &tg.PaymentsGetSavedStarGiftsRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 10,
	})
	if err != nil || all.Count != 1 || len(all.Gifts) != 1 {
		t.Fatalf("all saved gifts count=%d len=%d err=%v, want pending gift", all.Count, len(all.Gifts), err)
	}
	if !all.Gifts[0].Unsaved {
		t.Fatal("privacy-denied incoming gift must be pending (unsaved=true), not rejected")
	}
	msgID, ok := all.Gifts[0].GetMsgID()
	if !ok || msgID == 0 {
		t.Fatalf("pending gift msg_id=%d present=%v", msgID, ok)
	}
	visible, err := r.onPaymentsGetSavedStarGifts(recipientCtx, &tg.PaymentsGetSavedStarGiftsRequest{
		Peer:           &tg.InputPeerSelf{},
		ExcludeUnsaved: true,
		Limit:          10,
	})
	if err != nil || visible.Count != 0 || len(visible.Gifts) != 0 {
		t.Fatalf("exclude-unsaved count=%d len=%d err=%v, want hidden pending gift", visible.Count, len(visible.Gifts), err)
	}

	if ok, err := r.onPaymentsSaveStarGift(recipientCtx, &tg.PaymentsSaveStarGiftRequest{
		Stargift: &tg.InputSavedStarGiftUser{MsgID: msgID},
	}); err != nil || !ok {
		t.Fatalf("recipient approve gift = %v err=%v", ok, err)
	}
	visible, err = r.onPaymentsGetSavedStarGifts(recipientCtx, &tg.PaymentsGetSavedStarGiftsRequest{
		Peer:           &tg.InputPeerSelf{},
		ExcludeUnsaved: true,
		Limit:          10,
	})
	if err != nil || visible.Count != 1 || len(visible.Gifts) != 1 || visible.Gifts[0].Unsaved {
		t.Fatalf("approved visible gifts=%+v err=%v, want one saved gift", visible, err)
	}
}

// 频道 star gift saga：channel peer 能付款发送，但不生成频道历史消息；
// saved gift 用 inputSavedStarGiftChat.saved_id 定位，Recent Actions 用 admin log 快照承载。
func TestStarGiftChannelSaga(t *testing.T) {
	r, sender, owner, gift := starGiftTestRouter(t)
	ctx := context.Background()
	senderCtx := WithUserID(ctx, sender.ID)
	ownerCtx := WithUserID(ctx, owner.ID)

	created, err := r.deps.Channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Gift Channel",
		Broadcast:     true,
		MemberUserIDs: []int64{sender.ID},
		Date:          1700001000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.Channel
	channelPeer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	channelInput := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	giftService := r.deps.Gifts.(*appstargifts.Service)
	if _, err := giftService.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: gift.ID, UpgradeStars: 75, SupplyTotal: 10, SlugPrefix: "channel-cake",
		Models: []domain.StarGiftCollectibleAttribute{
			collectibleRPCAttribute(domain.StarGiftCollectibleModel, 8201, "Aurora"),
			collectibleRPCAttribute(domain.StarGiftCollectibleModel, 8204, "Aurora Two"),
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			collectibleRPCAttribute(domain.StarGiftCollectiblePattern, 8202, "Orbit"),
			collectibleRPCAttribute(domain.StarGiftCollectiblePattern, 8205, "Orbit Two"),
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, 2, "Midnight"),
			collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, 3, "Daylight"),
		},
		Actor: "test", CommandID: "channel-collectible-rpc",
	}); err != nil {
		t.Fatalf("publish channel collectible pool: %v", err)
	}
	upgradeFormRes, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGift{
		Peer: channelPeer, GiftID: gift.ID, IncludeUpgrade: true,
	}})
	if err != nil {
		t.Fatalf("getPaymentForm(channel include_upgrade): %v", err)
	}
	upgradeForm, ok := upgradeFormRes.(*tg.PaymentsPaymentFormStarGift)
	if !ok || len(upgradeForm.Invoice.Prices) != 1 || upgradeForm.Invoice.Prices[0].Amount != gift.Stars+75 {
		t.Fatalf("channel include_upgrade form = %T %+v, want total %d", upgradeFormRes, upgradeFormRes, gift.Stars+75)
	}
	inv := &tg.InputInvoiceStarGift{
		Peer:   channelPeer,
		GiftID: gift.ID,
	}

	formRes, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("getPaymentForm(channel): %v", err)
	}
	form, ok := formRes.(*tg.PaymentsPaymentFormStarGift)
	if !ok || form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 {
		t.Fatalf("form(channel) = %T %+v, want star gift XTR form", formRes, formRes)
	}

	payRes, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv})
	if err != nil {
		t.Fatalf("sendStarsForm(channel): %v", err)
	}
	pay, ok := payRes.(*tg.PaymentsPaymentResult)
	if !ok {
		t.Fatalf("pay result = %T, want *tg.PaymentsPaymentResult", payRes)
	}
	updates, ok := pay.Updates.(*tg.Updates)
	if !ok {
		t.Fatalf("pay updates = %T, want *tg.Updates", pay.Updates)
	}
	var (
		hasBalance bool
	)
	for _, up := range updates.Updates {
		switch u := up.(type) {
		case *tg.UpdateStarsBalance:
			hasBalance = true
			if amt, ok := u.Balance.(*tg.StarsAmount); !ok || amt.Amount != 950 {
				t.Fatalf("channel updateStarsBalance = %#v, want 950", u.Balance)
			}
		case *tg.UpdateNewChannelMessage:
			t.Fatalf("channel gift must not be pushed as UpdateNewChannelMessage: %#v", u.Message)
		}
	}
	if !hasBalance {
		t.Fatalf("channel pay updates: balance=%v, want updateStarsBalance", hasBalance)
	}

	savedRes, err := r.onPaymentsGetSavedStarGifts(senderCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: channelPeer})
	if err != nil {
		t.Fatalf("getSavedStarGifts(channel): %v", err)
	}
	if savedRes.Count != 1 || len(savedRes.Gifts) != 1 {
		t.Fatalf("channel saved gifts = count %d len %d, want 1/1", savedRes.Count, len(savedRes.Gifts))
	}
	if !savedRes.Gifts[0].CanUpgrade {
		t.Fatal("channel saved gift must advertise upgrade when a collectible pool is available")
	}
	ownerSavedRes, err := r.onPaymentsGetSavedStarGifts(ownerCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: channelPeer})
	if err != nil {
		t.Fatalf("getSavedStarGifts(channel owner): %v", err)
	}
	if enabled, ok := ownerSavedRes.GetChatNotificationsEnabled(); !ok || !enabled {
		t.Fatalf("channel owner notification setting enabled=%v ok=%v, want default true", enabled, ok)
	}
	baseGifts := r.deps.Gifts
	r.deps.Gifts = &starGiftNotificationSettingsRPCService{GiftsService: baseGifts, enabled: false}
	disabledSavedRes, err := r.onPaymentsGetSavedStarGifts(ownerCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: channelPeer})
	r.deps.Gifts = baseGifts
	if err != nil {
		t.Fatalf("getSavedStarGifts(channel notifications disabled): %v", err)
	}
	if enabled, ok := disabledSavedRes.GetChatNotificationsEnabled(); !ok || enabled {
		t.Fatalf("disabled channel notification setting enabled=%v ok=%v, want false/present", enabled, ok)
	}
	savedID, ok := savedRes.Gifts[0].GetSavedID()
	if !ok || savedID <= 0 {
		t.Fatalf("saved gift saved_id = %d ok %v, want positive", savedID, ok)
	}
	baseGifts = r.deps.Gifts
	r.deps.Gifts = &starGiftMessageAliasRPCService{
		GiftsService: baseGifts,
		viewerUserID: sender.ID,
		msgID:        777,
		ref:          domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}, SavedID: savedID},
	}
	_, aliasPermissionErr := r.onPaymentsSaveStarGift(senderCtx, &tg.PaymentsSaveStarGiftRequest{
		Stargift: &tg.InputSavedStarGiftUser{MsgID: 777},
	})
	r.deps.Gifts = baseGifts
	if !tgerr.Is(aliasPermissionErr, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("non-admin channel notification alias err=%v, want CHAT_ADMIN_REQUIRED", aliasPermissionErr)
	}
	if _, ok := savedRes.Gifts[0].GetMsgID(); ok {
		t.Fatalf("channel saved gift should not expose inputSavedStarGiftUser.msg_id")
	}
	nextHistoryID := channel.TopMessageID + 1
	history, err := r.onChannelsGetMessages(ownerCtx, &tg.ChannelsGetMessagesRequest{
		Channel: channelInput,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: nextHistoryID}},
	})
	if err != nil {
		t.Fatalf("get channel message after gift payment: %v", err)
	}
	gotMessages := history.(*tg.MessagesMessages).Messages
	if len(gotMessages) != 1 {
		t.Fatalf("channel getMessages len = %d, want 1 messageEmpty", len(gotMessages))
	}
	if _, ok := gotMessages[0].(*tg.MessageEmpty); !ok {
		t.Fatalf("channel gift leaked into message history as %T", gotMessages[0])
	}

	sendFilter := tg.ChannelAdminLogEventsFilter{}
	sendFilter.SetSend(true)
	adminReq := &tg.ChannelsGetAdminLogRequest{Channel: channelInput, Limit: 10}
	adminReq.SetEventsFilter(sendFilter)
	adminLog, err := r.onChannelsGetAdminLog(ownerCtx, adminReq)
	if err != nil {
		t.Fatalf("getAdminLog(channel gift): %v", err)
	}
	foundAdminGift := false
	for _, event := range adminLog.Events {
		send, ok := event.Action.(*tg.ChannelAdminLogEventActionSendMessage)
		if !ok {
			continue
		}
		svc, ok := send.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		action, ok := svc.Action.(*tg.MessageActionStarGift)
		if !ok {
			continue
		}
		if got, ok := action.GetSavedID(); !ok || got != savedID {
			t.Fatalf("admin log star gift saved_id = %d ok %v, want %d", got, ok, savedID)
		}
		if peer, ok := action.GetPeer(); !ok {
			t.Fatalf("admin log star gift peer missing")
		} else if ch, ok := peer.(*tg.PeerChannel); !ok || ch.ChannelID != channel.ID {
			t.Fatalf("admin log star gift peer = %#v, want channel %d", peer, channel.ID)
		}
		foundAdminGift = true
	}
	if !foundAdminGift {
		t.Fatalf("admin log did not include star gift send_message action")
	}

	oneRes, err := r.onPaymentsGetSavedStarGift(senderCtx, []tg.InputSavedStarGiftClass{
		&tg.InputSavedStarGiftChat{Peer: channelPeer, SavedID: savedID},
	})
	if err != nil || oneRes.Count != 1 || len(oneRes.Gifts) != 1 {
		t.Fatalf("getSavedStarGift(channel) count=%d len=%d err=%v, want 1/1", oneRes.Count, len(oneRes.Gifts), err)
	}

	fullRes, err := r.onChannelsGetFullChannel(ownerCtx, channelInput)
	if err != nil {
		t.Fatalf("getFullChannel: %v", err)
	}
	full, ok := fullRes.FullChat.(*tg.ChannelFull)
	if !ok {
		t.Fatalf("full chat = %T, want *tg.ChannelFull", fullRes.FullChat)
	}
	if cnt, ok := full.GetStargiftsCount(); !ok || cnt != 1 {
		t.Fatalf("channelFull stargifts_count = %d ok %v, want 1", cnt, ok)
	}
	if !full.GetStargiftsAvailable() {
		t.Fatalf("channelFull stargifts_available = false, want true for broadcast channel gift entry")
	}

	if ok, err := r.onPaymentsSaveStarGift(ownerCtx, &tg.PaymentsSaveStarGiftRequest{
		Unsave:   true,
		Stargift: &tg.InputSavedStarGiftChat{Peer: channelPeer, SavedID: savedID},
	}); err != nil || !ok {
		t.Fatalf("saveStarGift(channel hide) = %v err %v", ok, err)
	}
	hiddenRes, err := r.onPaymentsGetSavedStarGifts(senderCtx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: channelPeer, ExcludeUnsaved: true})
	if err != nil || hiddenRes.Count != 0 || len(hiddenRes.Gifts) != 0 {
		t.Fatalf("channel excludeUnsaved after hide = count %d len %d err %v, want 0/0", hiddenRes.Count, len(hiddenRes.Gifts), err)
	}
}

// 余额不足 → sendStarsForm 返回 BALANCE_TOO_LOW（不发礼、不扣费）。
func TestStarGiftInsufficientBalance(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	msgStore := memory.NewMessageStore(dialogs)
	sender, _ := users.Create(ctx, domain.User{AccessHash: 7201, Phone: "15550007201", FirstName: "Poor"})
	recipient, _ := users.Create(ctx, domain.User{AccessHash: 7202, Phone: "15550007202", FirstName: "Rich"})
	gift := domain.StarGift{ID: 8002, RevisionID: 9002, Stars: 5000, ConvertStars: 5000, Title: "Expensive",
		Sticker: domain.Document{ID: 701, AccessHash: 7, DCID: 2, MimeType: "application/x-tgsticker", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}}}
	giftStore := memory.NewStarGiftStore()
	giftStore.SeedCatalog([]domain.StarGift{gift})
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398, PublicBaseURL: "https://links.example.test", AllowDevPayments: true}, Deps{
		Users:    appusers.NewService(users),
		Messages: appmessages.NewService(msgStore, dialogs),
		Stars:    appstars.NewService(memory.NewStarsStore(), appstars.WithStartingGrant(1000)), // < 5000
		Gifts:    appstargifts.NewService(giftStore, nil, 2),
	}, zaptest.NewLogger(t), clock.System)
	senderCtx := WithUserID(ctx, sender.ID)
	inv := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, GiftID: gift.ID}
	formRes, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("get expensive gift form: %v", err)
	}
	form := formRes.(*tg.PaymentsPaymentFormStarGift)
	if _, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv}); !tgerr.Is(err, "BALANCE_TOO_LOW") {
		t.Fatalf("over-budget gift should error BALANCE_TOO_LOW")
	}
	// 余额未变。
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 1000 {
		t.Fatalf("sender balance = %d, want 1000 unchanged", bal.Balance)
	}
}

func TestStarGiftPurchaseFormsAreFreshAndBindPurpose(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)
	inv := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, GiftID: gift.ID}
	firstRes, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("first form: %v", err)
	}
	secondRes, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("second form: %v", err)
	}
	first := firstRes.(*tg.PaymentsPaymentFormStarGift)
	second := secondRes.(*tg.PaymentsPaymentFormStarGift)
	if first.FormID == 0 || second.FormID == 0 || first.FormID == second.FormID {
		t.Fatalf("fresh form ids = %d/%d, want distinct non-zero TL longs", first.FormID, second.FormID)
	}
	if _, err := r.onPaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: first.FormID + second.FormID, Invoice: inv}); !tgerr.Is(err, "FORM_EXPIRED") {
		t.Fatalf("unknown form err=%v, want FORM_EXPIRED", err)
	}
	tampered := *inv
	tampered.HideName = true
	if _, err := r.onPaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: first.FormID, Invoice: &tampered}); !tgerr.Is(err, "PURPOSE_INVALID") {
		t.Fatalf("tampered form err=%v, want PURPOSE_INVALID", err)
	}
}

func TestStarGiftCanPurchaseSameCatalogGiftTwice(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)
	inv := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, GiftID: gift.ID}
	var formIDs []int64
	for i := 0; i < 2; i++ {
		formRes, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
		if err != nil {
			t.Fatalf("get form %d: %v", i, err)
		}
		form := formRes.(*tg.PaymentsPaymentFormStarGift)
		formIDs = append(formIDs, form.FormID)
		if _, err := r.onPaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv}); err != nil {
			t.Fatalf("purchase %d: %v", i, err)
		}
	}
	if formIDs[0] == formIDs[1] {
		t.Fatalf("repeated purchase reused form id %d", formIDs[0])
	}
	saved, err := r.onPaymentsGetSavedStarGifts(WithUserID(context.Background(), recipient.ID), &tg.PaymentsGetSavedStarGiftsRequest{
		Peer: &tg.InputPeerSelf{}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("get recipient gifts: %v", err)
	}
	if saved.Count != 2 || len(saved.Gifts) != 2 {
		t.Fatalf("recipient gifts = count %d len %d, want two independent gifts", saved.Count, len(saved.Gifts))
	}
}

func TestStarsTopupInvoiceFallbackCreditsBalance(t *testing.T) {
	r, sender, _, _ := starGiftTestRouter(t)
	ctx := context.Background()
	senderCtx := WithUserID(ctx, sender.ID)
	opt := devStarsTopupOptions()[1]
	inv := &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsTopup{
		Stars:    opt.Stars,
		Currency: opt.Currency,
		Amount:   opt.Amount,
	}}

	formRes, err := r.onPaymentsGetPaymentForm(senderCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("getPaymentForm topup: %v", err)
	}
	form, ok := formRes.(*tg.PaymentsPaymentForm)
	if !ok {
		t.Fatalf("form = %T, want *tg.PaymentsPaymentForm", formRes)
	}
	if form.FormID == 0 {
		t.Fatal("form id = 0, want persisted random checkout id")
	}
	if form.BotID != domain.OfficialSystemUserID || len(form.Users) != 1 {
		t.Fatalf("form bot/users = %d/%d, want official system user", form.BotID, len(form.Users))
	}
	if !form.Invoice.Test || form.Invoice.Currency != opt.Currency || len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != opt.Amount {
		t.Fatalf("form invoice = %+v, want test %s + 1 price %d", form.Invoice, opt.Currency, opt.Amount)
	}

	if _, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv}); !tgerr.Is(err, "PAYMENT_CREDENTIALS_INVALID") {
		t.Fatalf("sendStarsForm fiat topup err = %v, want PAYMENT_CREDENTIALS_INVALID", err)
	}
	if _, err := r.onPaymentsSendPaymentForm(senderCtx, &tg.PaymentsSendPaymentFormRequest{
		FormID: form.FormID + 1, Invoice: inv, Credentials: devStarsCredentials(form.FormID + 1),
	}); !tgerr.Is(err, "STARS_FORM_AMOUNT_MISMATCH") {
		t.Fatalf("sendPaymentForm bad form err = %v, want STARS_FORM_AMOUNT_MISMATCH", err)
	}
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 1000 {
		t.Fatalf("balance after bad form = %d, want 1000 unchanged", bal.Balance)
	}
	for name, request := range map[string]*tg.PaymentsSendPaymentFormRequest{
		"missing":           {FormID: form.FormID, Invoice: inv},
		"wrong form marker": {FormID: form.FormID, Invoice: inv, Credentials: devStarsCredentials(form.FormID + 1)},
		"saved credentials": {FormID: form.FormID, Invoice: inv, Credentials: &tg.InputPaymentCredentials{
			Save: true, Data: devStarsCredentials(form.FormID).Data,
		}},
	} {
		if _, err := r.onPaymentsSendPaymentForm(senderCtx, request); !tgerr.Is(err, "PAYMENT_CREDENTIALS_INVALID") {
			t.Fatalf("%s credentials err = %v, want PAYMENT_CREDENTIALS_INVALID", name, err)
		}
	}
	withInfo := &tg.PaymentsSendPaymentFormRequest{FormID: form.FormID, Invoice: inv, Credentials: devStarsCredentials(form.FormID)}
	withInfo.SetRequestedInfoID("unexpected")
	if _, err := r.onPaymentsSendPaymentForm(senderCtx, withInfo); !tgerr.Is(err, "REQUESTED_INFO_ID_INVALID") {
		t.Fatalf("requested info err = %v", err)
	}
	withShipping := &tg.PaymentsSendPaymentFormRequest{FormID: form.FormID, Invoice: inv, Credentials: devStarsCredentials(form.FormID)}
	withShipping.SetShippingOptionID("unexpected")
	if _, err := r.onPaymentsSendPaymentForm(senderCtx, withShipping); !tgerr.Is(err, "SHIPPING_OPTION_INVALID") {
		t.Fatalf("shipping option err = %v", err)
	}
	withTip := &tg.PaymentsSendPaymentFormRequest{FormID: form.FormID, Invoice: inv, Credentials: devStarsCredentials(form.FormID)}
	withTip.SetTipAmount(1)
	if _, err := r.onPaymentsSendPaymentForm(senderCtx, withTip); !tgerr.Is(err, "TIP_AMOUNT_INVALID") {
		t.Fatalf("tip err = %v", err)
	}

	payRes, err := r.onPaymentsSendPaymentForm(senderCtx, &tg.PaymentsSendPaymentFormRequest{
		FormID: form.FormID, Invoice: inv, Credentials: devStarsCredentials(form.FormID),
	})
	if err != nil {
		t.Fatalf("sendPaymentForm topup: %v", err)
	}
	pay, ok := payRes.(*tg.PaymentsPaymentResult)
	if !ok {
		t.Fatalf("pay result = %T, want *tg.PaymentsPaymentResult", payRes)
	}
	updates, ok := pay.Updates.(*tg.Updates)
	if !ok {
		t.Fatalf("pay updates = %T, want *tg.Updates", pay.Updates)
	}
	foundBalance := false
	for _, up := range updates.Updates {
		if balance, ok := up.(*tg.UpdateStarsBalance); ok {
			foundBalance = true
			if amt, ok := balance.Balance.(*tg.StarsAmount); !ok || amt.Amount != 3500 {
				t.Fatalf("updateStarsBalance = %#v, want 3500", balance.Balance)
			}
		}
	}
	if !foundBalance {
		t.Fatalf("payment updates missing updateStarsBalance: %#v", updates.Updates)
	}
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 3500 {
		t.Fatalf("balance after topup = %d, want 3500", bal.Balance)
	}
	if _, err := r.onPaymentsSendPaymentForm(senderCtx, &tg.PaymentsSendPaymentFormRequest{
		FormID: form.FormID, Invoice: inv, Credentials: devStarsCredentials(form.FormID),
	}); err != nil {
		t.Fatalf("sendPaymentForm exact replay: %v", err)
	}
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 3500 {
		t.Fatalf("balance after exact replay = %d, want 3500", bal.Balance)
	}
	page, err := r.deps.Stars.ListTransactions(ctx, sender.ID, domain.StarsTransactionQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list transactions: %v", err)
	}
	hasTopup := false
	for _, tx := range page.Transactions {
		if tx.Reason == domain.StarsReasonTopup && tx.Amount == opt.Stars {
			hasTopup = true
		}
	}
	if !hasTopup {
		t.Fatalf("transactions missing topup %d: %+v", opt.Stars, page.Transactions)
	}
}

func TestDevPaymentsDeniedByDefault(t *testing.T) {
	// AllowDevPayments is off unless explicitly enabled, so the local fiat
	// checkout must neither issue a form nor accept telesrv_dev credentials.
	starsStore := newStarsTopupRPCStore()
	r := New(Config{}, Deps{Stars: appstars.NewService(starsStore, appstars.WithStartingGrant(0), appstars.WithPurchaseStore(starsStore))}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1001)
	opt := devStarsTopupOptions()[1]
	inv := &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsTopup{
		Stars: opt.Stars, Currency: opt.Currency, Amount: opt.Amount,
	}}
	if _, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv}); !tgerr.Is(err, "NOT_IMPLEMENTED") {
		t.Fatalf("getPaymentForm topup with dev payments denied err = %v, want NOT_IMPLEMENTED", err)
	}
	if _, err := r.onPaymentsSendPaymentForm(ctx, &tg.PaymentsSendPaymentFormRequest{
		FormID: 55, Invoice: inv, Credentials: devStarsCredentials(55),
	}); !tgerr.Is(err, "NOT_IMPLEMENTED") {
		t.Fatalf("sendPaymentForm with dev payments denied err = %v, want NOT_IMPLEMENTED", err)
	}
}

func TestStarsTopupRejectsUnlistedAmount(t *testing.T) {
	r, sender, _, _ := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)
	inv := &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsTopup{
		Stars:    2501,
		Currency: "USD",
		Amount:   199,
	}}
	_, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if !tgerr.Is(err, "STARS_FORM_AMOUNT_MISMATCH") {
		t.Fatalf("getPaymentForm unlisted err = %v, want STARS_FORM_AMOUNT_MISMATCH", err)
	}
}

func TestSavedStarGiftAnonymousDetailsVisibleOnlyToReceiver(t *testing.T) {
	const (
		senderID   = int64(1780243201)
		receiverID = int64(1780243231)
		viewerID   = int64(1780243999)
	)
	gift := domain.SavedStarGift{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: receiverID},
		FromUserID: senderID,
		GiftID:     7001,
		RevisionID: 8001,
		MsgID:      91,
		Date:       1700000000,
		NameHidden: true,
		Message:    "private gift message",
	}

	// Telegram iOS needs the sender peer to turn a user gift's msg_id into an
	// InputSavedStarGiftUser reference. The protocol exposes hidden original
	// details to the receiver, while keeping them hidden from profile viewers.
	receiverProjection := tgSavedStarGifts(receiverID, []domain.SavedStarGift{gift}, nil, nil, 0)
	if len(receiverProjection) != 1 {
		t.Fatalf("receiver projection len = %d, want 1", len(receiverProjection))
	}
	from, ok := receiverProjection[0].GetFromID()
	fromUser, isUser := from.(*tg.PeerUser)
	if !ok || !isUser || fromUser.UserID != senderID {
		t.Fatalf("receiver from_id = %#v ok=%v, want sender %d", from, ok, senderID)
	}
	message, ok := receiverProjection[0].GetMessage()
	if !ok || message.Text != gift.Message {
		t.Fatalf("receiver message = %#v ok=%v, want private message", message, ok)
	}
	if msgID, ok := receiverProjection[0].GetMsgID(); !ok || msgID != gift.MsgID {
		t.Fatalf("receiver msg_id = %d ok=%v, want %d", msgID, ok, gift.MsgID)
	}
	if ids := savedStarGiftUserIDs(receiverID, []domain.SavedStarGift{gift}); !reflect.DeepEqual(ids, []int64{senderID}) {
		t.Fatalf("receiver user ids = %v, want sender %d", ids, senderID)
	}

	viewerProjection := tgSavedStarGifts(viewerID, []domain.SavedStarGift{gift}, nil, nil, 0)
	if len(viewerProjection) != 1 {
		t.Fatalf("viewer projection len = %d, want 1", len(viewerProjection))
	}
	if from, ok := viewerProjection[0].GetFromID(); ok {
		t.Fatalf("anonymous sender leaked to profile viewer: %#v", from)
	}
	if message, ok := viewerProjection[0].GetMessage(); ok {
		t.Fatalf("anonymous message leaked to profile viewer: %#v", message)
	}
	if ids := savedStarGiftUserIDs(viewerID, []domain.SavedStarGift{gift}); len(ids) != 0 {
		t.Fatalf("anonymous sender user leaked to profile viewer: %v", ids)
	}
}

// tombstoneUsersService overlays the account lifecycle tombstone on top of a live
// users service. In-memory stores have no deletion boundary of their own, so this
// is how the RPC layer is shown a deleted owner.
type tombstoneUsersService struct {
	UsersService
	deletedIDs map[int64]bool
}

func (s tombstoneUsersService) ByID(ctx context.Context, currentUserID, userID int64) (domain.User, bool, error) {
	u, found, err := s.UsersService.ByID(ctx, currentUserID, userID)
	if err != nil || !found || !s.deletedIDs[userID] {
		return u, found, err
	}
	u.Deleted = true
	u.DeletedAt = time.Now().Unix()
	return u.DeletedTombstone(), true, nil
}

// A deleted account is a tombstone everywhere, gifts included: its profile must
// not expose what it once owned, NFT gifts in particular, or the client keeps
// rendering a dead account as a collector.
func TestDeletedAccountProfileHidesGifts(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouter(t)
	ctx := context.Background()
	viewerCtx := WithUserID(ctx, sender.ID)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}
	unique := domain.UniqueStarGift{ID: 9901, GiftID: gift.ID, Title: "Cake", Slug: "cake-1", Num: 1,
		Owner: ownerPeer, GiftAddress: "0xabc", OwnerAddress: "0xdef"}
	if _, err := r.deps.Gifts.RecordSavedGift(ctx, domain.SavedStarGift{
		Owner: ownerPeer, GiftID: gift.ID, RevisionID: gift.RevisionID, MsgID: 91, Date: 1700000091,
		UniqueGiftID: unique.ID, Unique: &unique,
	}); err != nil {
		t.Fatalf("record NFT gift: %v", err)
	}
	req := &tg.PaymentsGetSavedStarGiftsRequest{
		Peer:  &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Limit: 10,
	}
	live, err := r.onPaymentsGetSavedStarGifts(viewerCtx, req)
	if err != nil {
		t.Fatalf("live profile gifts: %v", err)
	}
	if live.Count != 1 || len(live.Gifts) != 1 {
		t.Fatalf("live profile gifts = %d/%d, want the NFT gift to be visible before deletion", live.Count, len(live.Gifts))
	}
	// The by-reference endpoint resolves an owner before reading, so it shares the
	// same tombstone rule. A deleted owner must hide the gift there too.
	r.deps.Users = tombstoneUsersService{UsersService: r.deps.Users, deletedIDs: map[int64]bool{recipient.ID: true}}
	if !r.starGiftOwnerDeleted(ctx, sender.ID, ownerPeer) {
		t.Fatal("deleted gift owner was not recognised as a tombstone")
	}
	if r.starGiftOwnerDeleted(ctx, sender.ID, domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}) {
		t.Fatal("the viewer's own account must never be treated as a tombstone")
	}
	deleted, err := r.onPaymentsGetSavedStarGifts(viewerCtx, req)
	if err != nil {
		t.Fatalf("deleted profile gifts: %v", err)
	}
	if deleted.Count != 0 || len(deleted.Gifts) != 0 {
		t.Fatalf("deleted profile gifts = %d/%d, want an empty tombstone profile", deleted.Count, len(deleted.Gifts))
	}
}
