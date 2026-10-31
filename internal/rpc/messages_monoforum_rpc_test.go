package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestMonoforumSendUpdatesIncludesCompleteMessageUserEnvelope(t *testing.T) {
	const (
		viewerID    = int64(1000000201)
		savedPeerID = int64(1000000202)
		viaBotID    = int64(1000000203)
		replyPeerID = int64(1000000204)
		quoteUserID = int64(1000000205)
		monoforumID = int64(1000000291)
		messageID   = 41
		messagePts  = 51
	)
	users := map[int64]domain.User{}
	for _, id := range []int64{viewerID, savedPeerID, viaBotID, replyPeerID, quoteUserID} {
		users[id] = domain.User{ID: id, FirstName: "projected"}
	}
	router := New(Config{}, Deps{Users: mapUsersService{users: users}}, zaptest.NewLogger(t), clock.System)
	savedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: savedPeerID}
	message := domain.ChannelMessage{
		ChannelID: monoforumID, ID: messageID, SenderUserID: viewerID,
		From: domain.Peer{Type: domain.PeerTypeUser, ID: viewerID}, SavedPeer: savedPeer,
		ViaBotID: viaBotID, Date: 1700000600,
		ReplyTo: &domain.MessageReply{
			MessageID: 40,
			Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: replyPeerID},
			QuoteEntities: []domain.MessageEntity{{
				Type: domain.MessageEntityMentionName, UserID: quoteUserID,
			}},
		},
	}
	result := domain.SendChannelMessageResult{
		Channel: domain.Channel{ID: monoforumID, Monoforum: true},
		Message: message,
		Event: domain.ChannelUpdateEvent{
			ChannelID: monoforumID, Type: domain.ChannelUpdateNewMessage,
			Pts: messagePts, PtsCount: 1, Message: message,
		},
	}

	updatesClass, err := router.monoforumSendUpdatesStrict(context.Background(), viewerID, result.Channel, savedPeer, result)
	if err != nil {
		t.Fatalf("monoforumSendUpdatesStrict: %v", err)
	}
	updates, ok := updatesClass.(*tg.Updates)
	if !ok || updates == nil {
		t.Fatalf("monoforumSendUpdates = %T, want *tg.Updates", updates)
	}
	got := make(map[int64]bool, len(updates.Users))
	for _, item := range updates.Users {
		if user, ok := item.(*tg.User); ok {
			got[user.ID] = true
		}
	}
	for _, id := range []int64{viewerID, savedPeerID, viaBotID, replyPeerID, quoteUserID} {
		if !got[id] {
			t.Fatalf("Users = %+v, missing referenced user %d", got, id)
		}
	}
}

func TestMonoforumSendUpdatesFailsClosedOnIncompleteUserEnvelope(t *testing.T) {
	const (
		viewerID    = int64(1000000301)
		savedPeerID = int64(1000000302)
		missingBot  = int64(1000000303)
		monoforumID = int64(1000000391)
	)
	router := New(Config{}, Deps{Users: mapUsersService{users: map[int64]domain.User{
		viewerID:    {ID: viewerID, FirstName: "viewer"},
		savedPeerID: {ID: savedPeerID, FirstName: "saved peer"},
	}}}, zaptest.NewLogger(t), clock.System)
	savedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: savedPeerID}
	message := domain.ChannelMessage{
		ChannelID: monoforumID, ID: 1, SenderUserID: viewerID,
		From: domain.Peer{Type: domain.PeerTypeUser, ID: viewerID}, SavedPeer: savedPeer,
		ViaBotID: missingBot, Date: 1700000700,
	}
	result := domain.SendChannelMessageResult{
		Channel: domain.Channel{ID: monoforumID, Monoforum: true}, Message: message,
		Event: domain.ChannelUpdateEvent{ChannelID: monoforumID, Type: domain.ChannelUpdateNewMessage, Pts: 2, PtsCount: 1, Message: message},
	}

	updates, err := router.monoforumSendUpdatesStrict(context.Background(), viewerID, result.Channel, savedPeer, result)
	if !errors.Is(err, ErrDurableUserProjectionIncomplete) || updates != nil {
		t.Fatalf("monoforum strict envelope = %T, %v; want nil ErrDurableUserProjectionIncomplete", updates, err)
	}
}

// TestMonoforumSavedDialogsAndHistory verifies the channel private-message
// (monoforum) read-side RPCs: a manager sees subscriber sub-dialogs via
// getSavedDialogs/getSavedHistory, with parent_peer accepting both the parent
// broadcast channel TDesktop actually sends and the virtual monoforum; a
// subscriber sees only its own sub-dialog via ordinary getHistory.
func TestMonoforumSavedDialogsAndHistory(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550002002", FirstName: "Sub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}

	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	verify := newFakeBotVerifications()
	r := New(Config{}, Deps{
		Users:            appusers.NewService(userStore),
		Channels:         channelSvc,
		BotVerifications: verify,
	}, zaptest.NewLogger(t), clock.System)

	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "DM Broadcast", Broadcast: true, Date: 1000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}

	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: sub.ID}
	if _, err := channelStore.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 1, Message: "hello channel", Date: 1100,
	}); err != nil {
		t.Fatalf("seed monoforum message: %v", err)
	}

	mono, err := channelStore.GetChannelByID(ctx, monoID)
	if err != nil {
		t.Fatalf("get monoforum: %v", err)
	}
	monoInput := &tg.InputPeerChannel{ChannelID: monoID, AccessHash: mono.AccessHash}
	parentInput := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	const monoforumIcon = int64(8800025)
	monoforumPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}
	verify.marks[monoforumPeer] = domain.CustomVerification{
		VerifierBotID:  777000123,
		Peer:           monoforumPeer,
		IconDocumentID: monoforumIcon,
		Description:    "Verified monoforum peer",
	}

	// Clicking the Direct Messages entry in TDesktop first pulls ordinary channel
	// history by the monoforum peer. The main history should return only the
	// monoforum's own service messages, not mixing in saved_peer sub-dialog messages.
	var raw bin.Buffer
	if err := (&tg.MessagesGetHistoryRequest{Peer: monoInput, Limit: 20}).Encode(&raw); err != nil {
		t.Fatalf("encode getHistory(monoforum): %v", err)
	}
	mainEnc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &raw)
	if err != nil {
		t.Fatalf("dispatch getHistory(monoforum): %v", err)
	}
	mainHistory, ok := mainEnc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("getHistory(monoforum) = %T, want *tg.MessagesChannelMessages", mainEnc)
	}
	if len(mainHistory.Messages) != 1 {
		t.Fatalf("main monoforum history = %d msgs, want only the creation service", len(mainHistory.Messages))
	}
	assertMessagesEnvelopeBotVerificationIcon(t, mainHistory, monoforumPeer, monoforumIcon)
	service, ok := mainHistory.Messages[0].(*tg.MessageService)
	if !ok {
		t.Fatalf("main monoforum message = %T, want MessageService", mainHistory.Messages[0])
	}
	// The monoforum's first service message is the creation message; TDesktop
	// renders it for a monoforum as "Direct messages were enabled in this
	// channel." (lng_action_created_monoforum). paid_messages_price only goes into
	// the parent broadcast channel.
	if _, ok := service.Action.(*tg.MessageActionChannelCreate); !ok {
		t.Fatalf("main monoforum action = %T %+v, want MessageActionChannelCreate", service.Action, service.Action)
	}
	seenChats := map[int64]bool{}
	for _, chat := range mainHistory.Chats {
		if ch, ok := chat.(*tg.Channel); ok {
			seenChats[ch.ID] = true
		}
	}
	if !seenChats[monoID] || !seenChats[created.Channel.ID] {
		t.Fatalf("main monoforum chats = %+v, want monoforum %d and parent %d", seenChats, monoID, created.Channel.ID)
	}
	var subscriberRaw bin.Buffer
	if err := (&tg.MessagesGetHistoryRequest{Peer: monoInput, Limit: 20}).Encode(&subscriberRaw); err != nil {
		t.Fatalf("encode non-admin getHistory(monoforum): %v", err)
	}
	subscriberEnc, err := r.Dispatch(WithUserID(ctx, sub.ID), [8]byte{}, 0, &subscriberRaw)
	if err != nil {
		t.Fatalf("non-admin getHistory(monoforum): %v", err)
	}
	subscriberHistory, ok := subscriberEnc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("non-admin getHistory(monoforum) = %T, want *tg.MessagesChannelMessages", subscriberEnc)
	}
	if len(subscriberHistory.Messages) != 1 {
		t.Fatalf("non-admin getHistory(monoforum) = %d msgs, want own sublist message", len(subscriberHistory.Messages))
	}
	subscriberMessage, ok := subscriberHistory.Messages[0].(*tg.Message)
	if !ok || subscriberMessage.Message != "hello channel" {
		t.Fatalf("non-admin history[0] = %#v, want own 'hello channel'", subscriberHistory.Messages[0])
	}
	subscriberSavedPeer, ok := subscriberMessage.GetSavedPeerID()
	if !ok {
		t.Fatalf("non-admin history message missing saved_peer_id")
	}
	if peer, ok := subscriberSavedPeer.(*tg.PeerUser); !ok || peer.UserID != sub.ID {
		t.Fatalf("non-admin history saved_peer_id = %#v, want self %d", subscriberSavedPeer, sub.ID)
	}

	// The manager views the private-message list.
	dreq := &tg.MessagesGetSavedDialogsRequest{}
	// TDesktop SavedSublist::loadAround()'s parentChat()->input() is the parent broadcast channel.
	dreq.SetParentPeer(parentInput)
	dres, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, owner.ID), dreq)
	if err != nil {
		t.Fatalf("getSavedDialogs(monoforum): %v", err)
	}
	sd, ok := dres.(*tg.MessagesSavedDialogs)
	if !ok {
		t.Fatalf("getSavedDialogs = %T, want *tg.MessagesSavedDialogs", dres)
	}
	if len(sd.Dialogs) != 1 {
		t.Fatalf("dialogs = %d, want 1 subscriber sublist", len(sd.Dialogs))
	}
	md, ok := sd.Dialogs[0].(*tg.MonoForumDialog)
	if !ok {
		t.Fatalf("dialog = %T, want *tg.MonoForumDialog", sd.Dialogs[0])
	}
	if pu, ok := md.Peer.(*tg.PeerUser); !ok || pu.UserID != sub.ID {
		t.Fatalf("dialog peer = %#v, want PeerUser %d", md.Peer, sub.ID)
	}
	foundSub := false
	for _, u := range sd.Users {
		if usr, ok := u.(*tg.User); ok && usr.ID == sub.ID {
			foundSub = true
		}
	}
	if !foundSub {
		t.Fatalf("getSavedDialogs users missing subscriber %d", sub.ID)
	}

	// The manager views a subscriber's dialog history.
	hreq := &tg.MessagesGetSavedHistoryRequest{Peer: &tg.InputPeerUser{UserID: sub.ID}}
	hreq.SetParentPeer(parentInput)
	hres, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), hreq)
	if err != nil {
		t.Fatalf("getSavedHistory(monoforum): %v", err)
	}
	assertMessagesEnvelopeBotVerificationIcon(t, hres, monoforumPeer, monoforumIcon)
	var gotMsgs []tg.MessageClass
	switch m := hres.(type) {
	case *tg.MessagesMessages:
		gotMsgs = m.Messages
	case *tg.MessagesMessagesSlice:
		gotMsgs = m.Messages
	case *tg.MessagesChannelMessages:
		gotMsgs = m.Messages
	default:
		t.Fatalf("getSavedHistory = %T, want messages", hres)
	}
	if len(gotMsgs) != 1 {
		t.Fatalf("history = %d msgs, want 1", len(gotMsgs))
	}
	msg, ok := gotMsgs[0].(*tg.Message)
	if !ok {
		t.Fatalf("msg = %T, want *tg.Message", gotMsgs[0])
	}
	if msg.Message != "hello channel" {
		t.Fatalf("body = %q, want 'hello channel'", msg.Message)
	}
	sp, ok := msg.GetSavedPeerID()
	if !ok {
		t.Fatalf("message missing saved_peer_id (client can't group into subscriber sublist)")
	}
	if pu, ok := sp.(*tg.PeerUser); !ok || pu.UserID != sub.ID {
		t.Fatalf("saved_peer_id = %#v, want sub %d", sp, sub.ID)
	}

	// The virtual monoforum peer is still a valid equivalent entry point; the two parents must not land on different data sets.
	directMonoReq := &tg.MessagesGetSavedHistoryRequest{Peer: &tg.InputPeerUser{UserID: sub.ID}}
	directMonoReq.SetParentPeer(monoInput)
	directMonoRes, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), directMonoReq)
	if err != nil {
		t.Fatalf("getSavedHistory(direct monoforum): %v", err)
	}
	directMonoSlice, ok := directMonoRes.(*tg.MessagesMessagesSlice)
	if !ok || len(directMonoSlice.Messages) != 1 {
		t.Fatalf("getSavedHistory(direct monoforum) = %#v, want same single-message topic", directMonoRes)
	}

	// A non-manager (the subscriber itself) viewing the list through the manager entry is denied.
	if _, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, sub.ID), dreq); err == nil {
		t.Fatalf("non-admin getSavedDialogs(monoforum) = nil err, want denied")
	}
}

// TestMonoforumSendMessageWritePath verifies the write side: a subscriber, per
// TDesktop's actual request, sends only to its own sub-dialog with
// peer=monoforum; a manager must explicitly specify the target subscriber;
// suggested_post is persisted and returned; a subscriber cannot write another's
// sub-dialog.
func TestMonoforumSendMessageWritePath(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 21, Phone: "15550003001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550003002", FirstName: "Sub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}

	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	dialogSvc := appdialogs.NewService(memory.NewDialogStore(), channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Dialogs:  dialogSvc,
	}, zaptest.NewLogger(t), clock.System)

	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "DM Broadcast", Broadcast: true, Date: 1000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 10, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	mono, err := channelStore.GetChannelByID(ctx, monoID)
	if err != nil {
		t.Fatalf("get monoforum: %v", err)
	}
	monoInput := &tg.InputPeerChannel{ChannelID: monoID, AccessHash: mono.AccessHash}
	monoChannelInput := &tg.InputChannel{ChannelID: monoID, AccessHash: mono.AccessHash}

	// A subscriber is not a monoforum member, but TDesktop must be able to read the full channel shell when opening the dialog.
	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, sub.ID), monoChannelInput)
	if err != nil {
		t.Fatalf("subscriber getFullChannel(monoforum): %v", err)
	}
	if full == nil || full.FullChat == nil {
		t.Fatalf("subscriber getFullChannel(monoforum) = %#v, want full chat", full)
	}

	// A monoforum can never become an ordinary channel member via join, otherwise it would generate a wrong joined service message.
	if _, err := r.onChannelsJoinChannel(WithUserID(ctx, sub.ID), monoChannelInput); err == nil || !strings.Contains(err.Error(), "CHANNEL_MONOFORUM_UNSUPPORTED") {
		t.Fatalf("subscriber joinChannel(monoforum) err = %v, want CHANNEL_MONOFORUM_UNSUPPORTED", err)
	}

	// TDesktop saves an identical suggested_post draft before sending; it must be writable and recoverable, not turn into CHANNEL_PRIVATE.
	draftSuggested := tg.SuggestedPost{}
	draftSuggested.SetPrice(&tg.StarsAmount{Amount: 10})
	draftSuggested.SetScheduleDate(1_700_100_000)
	draftReq := &tg.MessagesSaveDraftRequest{Peer: monoInput, Message: "pending suggested post"}
	draftReq.SetSuggestedPost(draftSuggested)
	if ok, err := r.onMessagesSaveDraft(WithUserID(ctx, sub.ID), draftReq); err != nil || !ok {
		t.Fatalf("subscriber saveDraft(monoforum) = %v, %v; want true, nil", ok, err)
	}
	storedDraft, found, err := dialogSvc.GetDraft(ctx, sub.ID, domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}, 0)
	if err != nil || !found {
		t.Fatalf("get persisted monoforum draft = %+v, %v, %v; want found", storedDraft, found, err)
	}
	if storedDraft.Message != "pending suggested post" || storedDraft.SuggestedPost == nil || storedDraft.SuggestedPost.Price == nil || storedDraft.SuggestedPost.Price.Amount != 10 || storedDraft.SuggestedPost.ScheduleDate != 1_700_100_000 {
		t.Fatalf("persisted monoforum draft = %+v, want suggested post content", storedDraft)
	}
	tooLow := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "under-authorized", RandomID: 554}
	tooLow.SetAllowPaidStars(9)
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), tooLow); err == nil || !strings.Contains(err.Error(), "ALLOW_PAYMENT_REQUIRED") || !strings.Contains(err.Error(), "(10)") {
		t.Fatalf("under-authorized paid message err = %v, want ALLOW_PAYMENT_REQUIRED_10", err)
	}

	// TDesktop's subscriber request does not carry InputReplyToMonoForum; the server must derive saved_peer=self from the caller.
	subReq := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "hi from sub telesrv://resolve?domain=Owner", RandomID: 555}
	subReq.ClearDraft = true
	subReq.SetAllowPaidStars(20)
	suggestedInput := tg.SuggestedPost{}
	suggestedInput.SetPrice(&tg.StarsAmount{Amount: 10})
	suggestedInput.SetScheduleDate(1_700_100_000)
	subReq.SetSuggestedPost(suggestedInput)
	subUpd, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), subReq)
	if err != nil {
		t.Fatalf("subscriber sendMessage(monoforum): %v", err)
	}
	subUpdates, ok := subUpd.(*tg.Updates)
	if !ok {
		t.Fatalf("subscriber send updates = %T, want *tg.Updates", subUpd)
	}
	var subMessageID int
	var subPaidStars, subBalance int64
	for _, update := range subUpdates.Updates {
		if newMessage, ok := update.(*tg.UpdateNewChannelMessage); ok {
			if message, ok := newMessage.Message.(*tg.Message); ok {
				subMessageID = message.ID
				subPaidStars, _ = message.GetPaidMessageStars()
				var hasAppLink bool
				for _, entity := range message.Entities {
					if url, ok := entity.(*tg.MessageEntityURL); ok && url.Offset == utf16CodeUnitLen("hi from sub ") && url.Length == utf16CodeUnitLen("telesrv://resolve?domain=Owner") {
						hasAppLink = true
					}
				}
				if !hasAppLink {
					t.Fatalf("monoforum message missing configured app-link entity: %+v", message.Entities)
				}
			}
		}
		if balance, ok := update.(*tg.UpdateStarsBalance); ok {
			if amount, ok := balance.Balance.(*tg.StarsAmount); ok {
				subBalance = amount.Amount
			}
		}
	}
	if subMessageID == 0 || subPaidStars != 10 || subBalance != 990 {
		t.Fatalf("subscriber send updates id/paid/balance = %d/%d/%d, want id>0/10/990: %#v", subMessageID, subPaidStars, subBalance, subUpdates.Updates)
	}
	if _, found, err := dialogSvc.GetDraft(ctx, sub.ID, domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}, 0); err != nil || found {
		t.Fatalf("clear_draft after paid send found/err = %v/%v, want false/nil", found, err)
	}
	duplicateUpd, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), subReq)
	if err != nil {
		t.Fatalf("subscriber paid replay: %v", err)
	}
	duplicateUpdates, ok := duplicateUpd.(*tg.Updates)
	if !ok {
		t.Fatalf("subscriber paid replay = %T, want *tg.Updates", duplicateUpd)
	}
	var duplicateBalance int64
	for _, update := range duplicateUpdates.Updates {
		if balance, ok := update.(*tg.UpdateStarsBalance); ok {
			if amount, ok := balance.Balance.(*tg.StarsAmount); ok {
				duplicateBalance = amount.Amount
			}
		}
	}
	if duplicateBalance != 990 {
		t.Fatalf("subscriber paid replay balance = %d, want 990 without a second debit", duplicateBalance)
	}

	// The admin replies to that subscriber's sub-dialog: the same
	// inputReplyToMessage carries both a real reply id and a monoforum target, and
	// both parts must be preserved.
	adminReq := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "admin reply", RandomID: 556}
	adminReply := &tg.InputReplyToMessage{ReplyToMsgID: subMessageID}
	adminReply.SetMonoforumPeerID(&tg.InputPeerUser{UserID: sub.ID})
	adminReq.SetReplyTo(adminReply)
	adminReq.SetAllowPaidStars(100)
	adminUpd, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), adminReq)
	if err != nil {
		t.Fatalf("admin reply sendMessage(monoforum): %v", err)
	}
	if updates, ok := adminUpd.(*tg.Updates); ok {
		for _, update := range updates.Updates {
			if _, balance := update.(*tg.UpdateStarsBalance); balance {
				t.Fatalf("admin free reply emitted a balance debit: %#v", updates.Updates)
			}
			if newMessage, ok := update.(*tg.UpdateNewChannelMessage); ok {
				if message, ok := newMessage.Message.(*tg.Message); ok {
					if stars, paid := message.GetPaidMessageStars(); paid || stars != 0 {
						t.Fatalf("admin reply paid_message_stars = %d/%v, want 0/false", stars, paid)
					}
				}
			}
		}
	}

	// A suggested post with media must go through the same monoforum sub-dialog and not degrade into an ordinary channel message or lose flags.
	mediaSuggested := tg.SuggestedPost{}
	mediaSuggested.SetPrice(&tg.StarsAmount{Amount: 15})
	mediaReq := &tg.MessagesSendMediaRequest{
		Peer: monoInput, RandomID: 558, Message: "media suggestion",
		Media: &tg.InputMediaContact{PhoneNumber: "+15550003003", FirstName: "Media", LastName: "Contact", Vcard: ""},
	}
	mediaReq.SetSuggestedPost(mediaSuggested)
	mediaReq.SetAllowPaidStars(15)
	if _, err := r.onMessagesSendMedia(WithUserID(ctx, sub.ID), mediaReq); err != nil {
		t.Fatalf("subscriber sendMedia(monoforum): %v", err)
	}

	// A subscriber cannot write another (owner)'s sub-dialog.
	sneaky := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "sneaky", RandomID: 557}
	sneaky.SetReplyTo(&tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerUser{UserID: owner.ID}})
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), sneaky); err == nil {
		t.Fatalf("subscriber writing another's sublist = nil err, want REPLY_TO_MONOFORUM_PEER_INVALID")
	}

	// Read history as the manager: the sub-dialog has three entries (subscriber text + admin reply + subscriber media), in reverse order.
	hreq := &tg.MessagesGetSavedHistoryRequest{Peer: &tg.InputPeerUser{UserID: sub.ID}}
	hreq.SetParentPeer(monoInput)
	hres, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), hreq)
	if err != nil {
		t.Fatalf("getSavedHistory after sends: %v", err)
	}
	slice, ok := hres.(*tg.MessagesMessagesSlice)
	if !ok {
		t.Fatalf("getSavedHistory = %T, want *tg.MessagesMessagesSlice", hres)
	}
	if len(slice.Messages) != 3 {
		t.Fatalf("history = %d msgs, want 3 (sub text + admin + sub media)", len(slice.Messages))
	}
	top, ok := slice.Messages[0].(*tg.Message)
	if !ok || top.Message != "media suggestion" {
		t.Fatalf("history[0] = %#v, want newest media suggestion", slice.Messages[0])
	}
	if _, ok := top.Media.(*tg.MessageMediaContact); !ok {
		t.Fatalf("history[0] media = %T, want MessageMediaContact", top.Media)
	}
	if paid, ok := top.GetPaidMessageStars(); !ok || paid != 10 {
		t.Fatalf("media paid_message_stars = %d/%v, want actual configured price 10", paid, ok)
	}
	topSuggested, ok := top.GetSuggestedPost()
	if !ok {
		t.Fatalf("media message missing suggested_post")
	}
	topPrice, ok := topSuggested.GetPrice()
	if !ok {
		t.Fatalf("media suggested_post missing price")
	}
	if stars, ok := topPrice.(*tg.StarsAmount); !ok || stars.Amount != 15 {
		t.Fatalf("media suggested_post price = %#v, want 15 Stars", topPrice)
	}
	adminMessage, ok := slice.Messages[1].(*tg.Message)
	if !ok || adminMessage.Message != "admin reply" {
		t.Fatalf("history[1] = %#v, want admin reply", slice.Messages[1])
	}
	if header, ok := adminMessage.ReplyTo.(*tg.MessageReplyHeader); !ok || header.ReplyToMsgID != subMessageID {
		t.Fatalf("admin reply header = %#v, want reply_to_msg_id %d", adminMessage.ReplyTo, subMessageID)
	}
	suggestedMessage, ok := slice.Messages[2].(*tg.Message)
	if !ok {
		t.Fatalf("history[2] = %T, want *tg.Message", slice.Messages[2])
	}
	suggested, ok := suggestedMessage.GetSuggestedPost()
	if !ok {
		t.Fatalf("subscriber message missing suggested_post")
	}
	price, ok := suggested.GetPrice()
	if !ok {
		t.Fatalf("suggested_post missing price")
	}
	stars, ok := price.(*tg.StarsAmount)
	if !ok || stars.Amount != 10 {
		t.Fatalf("suggested_post price = %#v, want 10 Stars", price)
	}
	if scheduleDate, ok := suggested.GetScheduleDate(); !ok || scheduleDate != 1_700_100_000 {
		t.Fatalf("suggested_post schedule = %d/%v, want 1700100000/true", scheduleDate, ok)
	}
}

// TestMonoforumForwardSuggestedPostAndMessageMetadataPaths regresses four real
// DrKLO paths: Add Offer creates a proposal with
// forwardMessages+suggested_post, Edit Price/Edit Time replies to the original
// proposal; getMessagesViews and get/send reaction must let a subscriber with no
// member row access its own saved_peer, and must not cross by a guessed id into
// another subscriber's sub-dialog.
func TestMonoforumForwardSuggestedPostAndMessageMetadataPaths(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550004001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550004002", FirstName: "Sub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	other, err := userStore.Create(ctx, domain.User{AccessHash: 33, Phone: "15550004003", FirstName: "Other"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "DM Suggested", Broadcast: true, Date: 2000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	mono, err := channelStore.GetChannelByID(ctx, monoID)
	if err != nil {
		t.Fatalf("get monoforum: %v", err)
	}
	monoInput := &tg.InputPeerChannel{ChannelID: monoID, AccessHash: mono.AccessHash}
	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: sub.ID}
	original, err := channelStore.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer,
		RandomID: 7001, Message: "please publish this", Date: 2001,
	})
	if err != nil {
		t.Fatalf("seed subscriber message: %v", err)
	}
	otherMessage, err := channelStore.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: other.ID,
		SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: other.ID},
		RandomID:  7002, Message: "another subscriber", Date: 2002,
	})
	if err != nil {
		t.Fatalf("seed other subscriber message: %v", err)
	}

	// DrKLO batch-refreshes visible messages with views/replies every 5 seconds.
	// The returned vector must preserve the request positions, must not report
	// CHANNEL_PRIVATE because a synthetic viewer has no channel_members row, and
	// must not let a guessed other saved_peer message be read or incremented.
	messageViews, err := r.onMessagesGetMessagesViews(WithUserID(ctx, sub.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer:      monoInput,
		ID:        []int{original.Message.ID, otherMessage.Message.ID},
		Increment: true,
	})
	if err != nil {
		t.Fatalf("subscriber getMessagesViews(monoforum): %v", err)
	}
	if len(messageViews.Views) != 2 || len(messageViews.Chats) == 0 {
		t.Fatalf("subscriber getMessagesViews = %+v, want two positional views with channel context", messageViews)
	}
	if got, ok := messageViews.Views[0].GetViews(); !ok || got != 1 {
		t.Fatalf("subscriber own message views = %d/%v, want 1/true", got, ok)
	}
	if got, ok := messageViews.Views[1].GetViews(); ok || got != 0 {
		t.Fatalf("subscriber cross-saved-peer views = %d/%v, want 0/false", got, ok)
	}
	repeatedMessageViews, err := r.onMessagesGetMessagesViews(WithUserID(ctx, sub.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer: monoInput, ID: []int{original.Message.ID}, Increment: true,
	})
	if err != nil {
		t.Fatalf("subscriber repeated getMessagesViews(monoforum): %v", err)
	}
	if got, ok := repeatedMessageViews.Views[0].GetViews(); !ok || got != 1 {
		t.Fatalf("subscriber repeated message views = %d/%v, want idempotent 1/true", got, ok)
	}
	adminMessageViews, err := r.onMessagesGetMessagesViews(WithUserID(ctx, owner.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer:      monoInput,
		ID:        []int{original.Message.ID, otherMessage.Message.ID},
		Increment: true,
	})
	if err != nil {
		t.Fatalf("admin getMessagesViews(monoforum): %v", err)
	}
	if got, ok := adminMessageViews.Views[0].GetViews(); !ok || got != 2 {
		t.Fatalf("admin first saved-peer views = %d/%v, want 2/true", got, ok)
	}
	if got, ok := adminMessageViews.Views[1].GetViews(); !ok || got != 1 {
		t.Fatalf("admin second saved-peer views = %d/%v, want 1/true", got, ok)
	}

	// Android sends getMessagesReactions first when opening reaction state. A
	// subscriber has no channel_members row, but its own saved_peer messages must
	// be returned normally with saved_peer_id for the client to group them.
	reactionState, err := r.onMessagesGetMessagesReactions(WithUserID(ctx, sub.ID), &tg.MessagesGetMessagesReactionsRequest{
		Peer: monoInput,
		ID:   []int{original.Message.ID},
	})
	if err != nil {
		t.Fatalf("subscriber getMessagesReactions(monoforum): %v", err)
	}
	reactionUpdates, ok := reactionState.(*tg.Updates)
	if !ok || len(reactionUpdates.Updates) != 1 {
		t.Fatalf("getMessagesReactions = %#v, want one update", reactionState)
	}
	reactionUpdate, ok := reactionUpdates.Updates[0].(*tg.UpdateMessageReactions)
	if !ok {
		t.Fatalf("getMessagesReactions update = %T, want UpdateMessageReactions", reactionUpdates.Updates[0])
	}
	savedPeer, ok := reactionUpdate.GetSavedPeerID()
	if !ok {
		t.Fatalf("getMessagesReactions update missing saved_peer_id")
	}
	if peer, ok := savedPeer.(*tg.PeerUser); !ok || peer.UserID != sub.ID {
		t.Fatalf("reaction saved_peer_id = %#v, want subscriber %d", savedPeer, sub.ID)
	}
	sendReaction := &tg.MessagesSendReactionRequest{Peer: monoInput, MsgID: original.Message.ID}
	sendReaction.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}})
	sentReaction, err := r.onMessagesSendReaction(WithUserID(ctx, sub.ID), sendReaction)
	if err != nil {
		t.Fatalf("subscriber sendReaction(monoforum): %v", err)
	}
	sentReactionUpdates, ok := sentReaction.(*tg.Updates)
	if !ok || len(sentReactionUpdates.Updates) != 1 {
		t.Fatalf("sendReaction = %#v, want one update", sentReaction)
	}
	sentReactionUpdate, ok := sentReactionUpdates.Updates[0].(*tg.UpdateMessageReactions)
	if !ok {
		t.Fatalf("sendReaction update = %T, want UpdateMessageReactions", sentReactionUpdates.Updates[0])
	}
	if saved, ok := sentReactionUpdate.GetSavedPeerID(); !ok {
		t.Fatalf("sendReaction update missing saved_peer_id")
	} else if peer, ok := saved.(*tg.PeerUser); !ok || peer.UserID != sub.ID {
		t.Fatalf("sendReaction saved_peer_id = %#v, want subscriber %d", saved, sub.ID)
	}
	if _, err := r.onMessagesSendReaction(WithUserID(ctx, sub.ID), &tg.MessagesSendReactionRequest{
		Peer: monoInput, MsgID: otherMessage.Message.ID,
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("cross-saved-peer sendReaction err = %v, want MESSAGE_ID_INVALID", err)
	}

	// Add Offer: both the source message and the target dialog are the monoforum,
	// and the own topic is selected by inputReplyToMonoForum(self); this is not an
	// ordinary forum reply root.
	addOffer := &tg.MessagesForwardMessagesRequest{
		FromPeer: monoInput,
		ID:       []int{original.Message.ID},
		RandomID: []int64{8001},
		ToPeer:   monoInput,
	}
	addOffer.SetDropAuthor(true)
	addOffer.SetReplyTo(&tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerSelf{}})
	offer := tg.SuggestedPost{}
	offer.SetPrice(&tg.StarsAmount{Amount: 25})
	offer.SetScheduleDate(2_000_000_000)
	addOffer.SetSuggestedPost(offer)

	addOfferResult, err := r.onMessagesForwardMessages(WithUserID(ctx, sub.ID), addOffer)
	if err != nil {
		t.Fatalf("subscriber Add Offer forwardMessages(monoforum): %v", err)
	}
	proposal, proposalUpdate := requireMonoforumNewChannelMessage(t, addOfferResult)
	if proposal.Message != original.Message.Body {
		t.Fatalf("proposal body = %q, want copied source %q", proposal.Message, original.Message.Body)
	}
	if proposalUpdate.PtsCount != 1 || proposalUpdate.Pts <= original.Message.Pts {
		t.Fatalf("proposal pts/count = %d/%d, want one real channel event after %d", proposalUpdate.Pts, proposalUpdate.PtsCount, original.Message.Pts)
	}
	if _, ok := proposal.GetFwdFrom(); ok {
		t.Fatalf("drop_author proposal unexpectedly has fwd_from: %#v", proposal)
	}
	if saved, ok := proposal.GetSavedPeerID(); !ok {
		t.Fatalf("proposal missing saved_peer_id")
	} else if peer, ok := saved.(*tg.PeerUser); !ok || peer.UserID != sub.ID {
		t.Fatalf("proposal saved_peer_id = %#v, want subscriber %d", saved, sub.ID)
	}
	proposalSuggested, ok := proposal.GetSuggestedPost()
	if !ok {
		t.Fatalf("proposal missing suggested_post")
	}
	if price, ok := proposalSuggested.GetPrice(); !ok {
		t.Fatalf("proposal suggested_post missing price")
	} else if stars, ok := price.(*tg.StarsAmount); !ok || stars.Amount != 25 {
		t.Fatalf("proposal price = %#v, want 25 Stars", price)
	}

	// Edit Price/Edit Time: Android carries reply_to, monoforum_peer_id and a
	// redundant top_msg_id at the same time; the server preserves the real reply,
	// accepts the equal redundant value but does not create a forum topic root.
	editOffer := &tg.MessagesForwardMessagesRequest{
		FromPeer: monoInput,
		ID:       []int{proposal.ID},
		RandomID: []int64{8002},
		ToPeer:   monoInput,
	}
	editOffer.SetDropAuthor(true)
	editReply := &tg.InputReplyToMessage{ReplyToMsgID: proposal.ID}
	editReply.SetMonoforumPeerID(&tg.InputPeerSelf{})
	editOffer.SetReplyTo(editReply)
	editOffer.SetTopMsgID(proposal.ID)
	edited := tg.SuggestedPost{}
	edited.SetPrice(&tg.StarsAmount{Amount: 40})
	edited.SetScheduleDate(2_000_100_000)
	editOffer.SetSuggestedPost(edited)

	editResult, err := r.onMessagesForwardMessages(WithUserID(ctx, sub.ID), editOffer)
	if err != nil {
		t.Fatalf("subscriber Edit Offer forwardMessages(monoforum): %v", err)
	}
	editedProposal, editedUpdate := requireMonoforumNewChannelMessage(t, editResult)
	if editedUpdate.PtsCount != 1 || editedUpdate.Pts != proposalUpdate.Pts+1 {
		t.Fatalf("edited proposal pts/count = %d/%d, want %d/1", editedUpdate.Pts, editedUpdate.PtsCount, proposalUpdate.Pts+1)
	}
	replyHeader, ok := editedProposal.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || replyHeader.ReplyToMsgID != proposal.ID {
		t.Fatalf("edited proposal reply = %#v, want message %d", editedProposal.ReplyTo, proposal.ID)
	}
	if topID, ok := replyHeader.GetReplyToTopID(); ok || topID != 0 {
		t.Fatalf("edited proposal acquired forum top_msg_id = %d/%v, want absent", topID, ok)
	}
	editedSuggested, ok := editedProposal.GetSuggestedPost()
	if !ok {
		t.Fatalf("edited proposal missing suggested_post")
	}
	if price, ok := editedSuggested.GetPrice(); !ok {
		t.Fatalf("edited proposal missing price")
	} else if stars, ok := price.(*tg.StarsAmount); !ok || stars.Amount != 40 {
		t.Fatalf("edited proposal price = %#v, want 40 Stars", price)
	}
	if schedule, ok := editedSuggested.GetScheduleDate(); !ok || schedule != 2_000_100_000 {
		t.Fatalf("edited proposal schedule = %d/%v, want 2000100000/true", schedule, ok)
	}

	beforeReplay, err := channelStore.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{
		MonoforumID: monoID, SavedPeer: subPeer, Limit: 100,
	})
	if err != nil {
		t.Fatalf("history before replay: %v", err)
	}
	replayResult, err := r.onMessagesForwardMessages(WithUserID(ctx, sub.ID), editOffer)
	if err != nil {
		t.Fatalf("exact Edit Offer replay: %v", err)
	}
	replayedProposal, replayedUpdate := requireMonoforumNewChannelMessage(t, replayResult)
	afterReplay, err := channelStore.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{
		MonoforumID: monoID, SavedPeer: subPeer, Limit: 100,
	})
	if err != nil {
		t.Fatalf("history after replay: %v", err)
	}
	if replayedProposal.ID != editedProposal.ID || replayedUpdate.Pts != editedUpdate.Pts || afterReplay.Count != beforeReplay.Count {
		t.Fatalf("exact replay id/pts/count = %d/%d/%d, want %d/%d/%d",
			replayedProposal.ID, replayedUpdate.Pts, afterReplay.Count,
			editedProposal.ID, editedUpdate.Pts, beforeReplay.Count)
	}
	conflict := *editOffer
	conflictingSuggested := tg.SuggestedPost{}
	conflictingSuggested.SetPrice(&tg.StarsAmount{Amount: 41})
	conflict.SetSuggestedPost(conflictingSuggested)
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, sub.ID), &conflict); err == nil || !strings.Contains(err.Error(), "RANDOM_ID_DUPLICATE") {
		t.Fatalf("conflicting Edit Offer replay err = %v, want RANDOM_ID_DUPLICATE", err)
	}

	// An exact id lookup must obey the saved_peer filter and must not use another subscriber's message as a forward source.
	crossSource := &tg.MessagesForwardMessagesRequest{
		FromPeer: monoInput,
		ID:       []int{otherMessage.Message.ID},
		RandomID: []int64{8003},
		ToPeer:   monoInput,
	}
	crossSource.SetDropAuthor(true)
	crossSource.SetReplyTo(&tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerSelf{}})
	crossSource.SetSuggestedPost(offer)
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, sub.ID), crossSource); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("cross-saved-peer forward source err = %v, want MESSAGE_ID_INVALID", err)
	}

	// The manager can see all sub-dialogs, but the target must still be specified explicitly, writing to the same subscriber saved_peer.
	adminOffer := &tg.MessagesForwardMessagesRequest{
		FromPeer: monoInput,
		ID:       []int{original.Message.ID},
		RandomID: []int64{8004},
		ToPeer:   monoInput,
	}
	adminOffer.SetDropAuthor(true)
	adminOffer.SetReplyTo(&tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerUser{UserID: sub.ID}})
	adminOffer.SetSuggestedPost(offer)
	adminResult, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), adminOffer)
	if err != nil {
		t.Fatalf("admin Add Offer forwardMessages(monoforum): %v", err)
	}
	adminProposal, _ := requireMonoforumNewChannelMessage(t, adminResult)
	if saved, ok := adminProposal.GetSavedPeerID(); !ok {
		t.Fatalf("admin proposal missing saved_peer_id")
	} else if peer, ok := saved.(*tg.PeerUser); !ok || peer.UserID != sub.ID {
		t.Fatalf("admin proposal saved_peer_id = %#v, want subscriber %d", saved, sub.ID)
	}
}

func requireMonoforumNewChannelMessage(t *testing.T, updatesClass tg.UpdatesClass) (*tg.Message, *tg.UpdateNewChannelMessage) {
	t.Helper()
	updates, ok := updatesClass.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updatesClass)
	}
	for _, update := range updates.Updates {
		newMessage, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		message, ok := newMessage.Message.(*tg.Message)
		if !ok {
			t.Fatalf("new channel message = %T, want *tg.Message", newMessage.Message)
		}
		return message, newMessage
	}
	t.Fatalf("updates missing UpdateNewChannelMessage: %#v", updates.Updates)
	return nil, nil
}
