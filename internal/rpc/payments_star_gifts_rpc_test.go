package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appstargifts "telesrv/internal/app/stargifts"
	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type stubGiftCatalog struct{ gifts []domain.StarGift }

func (s stubGiftCatalog) BuildStarGiftCatalog(_ context.Context) ([]domain.StarGift, error) {
	return s.gifts, nil
}

func starGiftTestRouter(t *testing.T) (*Router, domain.User, domain.User, domain.StarGift) {
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
		ID: 8001, Stars: 50, ConvertStars: 50, Title: "Cake",
		Sticker: domain.Document{ID: 700, AccessHash: 7, DCID: 2, MimeType: "image/webp"},
	}
	gifts := appstargifts.NewService(memory.NewStarGiftStore(), stubGiftCatalog{[]domain.StarGift{gift}})
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(users),
		Messages: appmessages.NewService(msgStore, dialogs),
		Channels: appchannels.NewService(channelStore),
		Stars:    appstars.NewService(memory.NewStarsStore(), appstars.WithStartingGrant(1000)),
		Gifts:    gifts,
	}, zaptest.NewLogger(t), clock.System)
	return r, sender, recipient, gift
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
		Peer:   &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		GiftID: gift.ID,
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
	savedID, ok := savedRes.Gifts[0].GetSavedID()
	if !ok || savedID <= 0 {
		t.Fatalf("saved gift saved_id = %d ok %v, want positive", savedID, ok)
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
	gift := domain.StarGift{ID: 8002, Stars: 5000, ConvertStars: 5000, Title: "Expensive",
		Sticker: domain.Document{ID: 701, AccessHash: 7, DCID: 2, MimeType: "image/webp"}}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(users),
		Messages: appmessages.NewService(msgStore, dialogs),
		Stars:    appstars.NewService(memory.NewStarsStore(), appstars.WithStartingGrant(1000)), // < 5000
		Gifts:    appstargifts.NewService(memory.NewStarGiftStore(), stubGiftCatalog{[]domain.StarGift{gift}}),
	}, zaptest.NewLogger(t), clock.System)
	senderCtx := WithUserID(ctx, sender.ID)
	inv := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, GiftID: gift.ID}
	if _, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{Invoice: inv}); err == nil {
		t.Fatalf("over-budget gift should error BALANCE_TOO_LOW")
	}
	// 余额未变。
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 1000 {
		t.Fatalf("sender balance = %d, want 1000 unchanged", bal.Balance)
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
	form, ok := formRes.(*tg.PaymentsPaymentFormStars)
	if !ok {
		t.Fatalf("form = %T, want *tg.PaymentsPaymentFormStars", formRes)
	}
	if form.FormID != starsTopupFormID(sender.ID, opt.Stars, opt.Currency, opt.Amount) {
		t.Fatalf("form id = %d, want deterministic topup id", form.FormID)
	}
	if form.BotID != domain.OfficialSystemUserID || len(form.Users) != 1 {
		t.Fatalf("form bot/users = %d/%d, want official system user", form.BotID, len(form.Users))
	}
	if form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != opt.Stars {
		t.Fatalf("form invoice = %+v, want XTR + 1 price %d", form.Invoice, opt.Stars)
	}

	if _, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID + 1, Invoice: inv}); !tgerr.Is(err, "STARS_FORM_AMOUNT_MISMATCH") {
		t.Fatalf("sendStarsForm bad form err = %v, want STARS_FORM_AMOUNT_MISMATCH", err)
	}
	if bal, _ := r.deps.Stars.GetBalance(ctx, sender.ID); bal.Balance != 1000 {
		t.Fatalf("balance after bad form = %d, want 1000 unchanged", bal.Balance)
	}

	payRes, err := r.onPaymentsSendStarsForm(senderCtx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: inv})
	if err != nil {
		t.Fatalf("sendStarsForm topup: %v", err)
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
	page, err := r.deps.Stars.ListTransactions(ctx, sender.ID, "", 10)
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
