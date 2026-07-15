package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func TestMessagesSavePreparedInlineMessageStoresRegistry(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	botCtx := WithUserID(ctx, f.bot.ID)
	userCtx := WithUserID(ctx, f.owner.ID)

	req := &tg.MessagesSavePreparedInlineMessageRequest{
		Result: preparedInlineArticleResult(),
		UserID: inputUser(f.owner),
	}
	req.SetPeerTypes([]tg.InlineQueryPeerTypeClass{&tg.InlineQueryPeerTypePM{}})
	got, err := f.router.onMessagesSavePreparedInlineMessage(botCtx, req)
	if err != nil {
		t.Fatalf("save prepared inline: %v", err)
	}
	if got.ID == "" || got.ExpireDate <= int(f.router.clock.Now().Unix()) {
		t.Fatalf("save prepared inline = %+v, want id and future expire_date", got)
	}
	if _, err := f.router.onMessagesSavePreparedInlineMessage(userCtx, req); !tgerr.Is(err, "USER_BOT_REQUIRED") {
		t.Fatalf("save prepared inline by user err = %v, want USER_BOT_REQUIRED", err)
	}
	if _, err := f.router.onMessagesSavePreparedInlineMessage(botCtx, &tg.MessagesSavePreparedInlineMessageRequest{
		Result: preparedInlineArticleResult(),
		UserID: &tg.InputUser{UserID: f.owner.ID + 999999, AccessHash: 1},
	}); !tgerr.Is(err, "USER_ID_INVALID") {
		t.Fatalf("save prepared inline bad user err = %v, want USER_ID_INVALID", err)
	}
}

func TestMessagesEditInlineBotMessageRejectsWithoutInlineMessageState(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	req := &tg.MessagesEditInlineBotMessageRequest{
		ID:      validInputBotInlineMessageID(f.owner.ID),
		Message: "edited",
	}
	req.SetFlags()
	if ok, err := f.router.onMessagesEditInlineBotMessage(userCtx, req); ok || !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("edit inline by user = %v,%v, want false,MESSAGE_ID_INVALID", ok, err)
	}
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, req); ok || !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("edit inline by bot = %v,%v, want false,MESSAGE_ID_INVALID", ok, err)
	}
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, &tg.MessagesEditInlineBotMessageRequest{}); ok || !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("edit inline nil id = %v,%v, want false,MESSAGE_ID_INVALID", ok, err)
	}
}

func TestMessagesEditInlineBotMessageEditsPrivateInlineMessage(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	sessions := &captureSessions{}
	f.router.deps.Sessions = sessions
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	queryID, _ := f.router.inlines.registerWithCacheKey(f.router.clock.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID}, inlineCacheKey{query: "editable"})
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID,
		Results: []tg.InputBotInlineResultClass{
			inlineArticleResultWithCallbackMarkup("editable-1", "before edit", "open", []byte("v1")),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline results = %v,%v, want true,nil", ok, err)
	}

	if _, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 991001,
		QueryID:  queryID,
		ID:       "editable-1",
	}); err != nil {
		t.Fatalf("send inline bot result: %v", err)
	}
	feedback := inlineSendFeedbackFromSessions(t, sessions, f.bot.ID)
	if feedback.UserID != f.owner.ID || feedback.Query != "editable" || feedback.ID != "editable-1" {
		t.Fatalf("inline send feedback = user %d query %q id %q", feedback.UserID, feedback.Query, feedback.ID)
	}
	msgID, ok := feedback.GetMsgID()
	if !ok {
		t.Fatalf("inline send feedback missing msg_id")
	}

	editReq := &tg.MessagesEditInlineBotMessageRequest{ID: msgID}
	editReq.SetMessage("after edit")
	editReq.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{
		Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonCallback{Text: "done", Data: []byte("v2")}},
	}}})
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, editReq); err != nil || !ok {
		t.Fatalf("edit inline bot message = %v,%v, want true,nil", ok, err)
	}

	ownerHistory, err := f.router.deps.Messages.GetHistory(ctx, f.owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID},
		Limit:   1,
	})
	if err != nil || len(ownerHistory.Messages) != 1 {
		t.Fatalf("owner history len = %d err=%v, want 1,nil", len(ownerHistory.Messages), err)
	}
	assertInlineEditedMessage(t, ownerHistory.Messages[0], "after edit", f.bot.ID, "done", []byte("v2"))

	peerHistory, err := f.router.deps.Messages.GetHistory(ctx, f.peer.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID},
		Limit:   1,
	})
	if err != nil || len(peerHistory.Messages) != 1 {
		t.Fatalf("peer history len = %d err=%v, want 1,nil", len(peerHistory.Messages), err)
	}
	assertInlineEditedMessage(t, peerHistory.Messages[0], "after edit", f.bot.ID, "done", []byte("v2"))
}

func TestMessagesEditInlineBotMessageRewritesPrivateMedia(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	sessions := &captureSessions{}
	f.router.deps.Sessions = sessions
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	queryID, _ := f.router.inlines.registerWithCacheKey(f.router.clock.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID}, inlineCacheKey{query: "editable-media"})
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID,
		Results: []tg.InputBotInlineResultClass{
			inlineArticleResultWithCallbackMarkup("editable-media-1", "caption before media edit", "open", []byte("media-v1")),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline media result = %v,%v, want true,nil", ok, err)
	}
	if _, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 991003,
		QueryID:  queryID,
		ID:       "editable-media-1",
	}); err != nil {
		t.Fatalf("send inline bot media result: %v", err)
	}
	msgID, ok := inlineSendFeedbackFromSessions(t, sessions, f.bot.ID).GetMsgID()
	if !ok {
		t.Fatalf("inline send feedback missing msg_id")
	}

	badEdit := &tg.MessagesEditInlineBotMessageRequest{ID: msgID}
	badEdit.SetMedia(&tg.InputMediaDice{Emoticon: "🎲"})
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, badEdit); ok || !tgerr.Is(err, "MEDIA_INVALID") {
		t.Fatalf("unsupported inline media edit = %v,%v, want false,MEDIA_INVALID", ok, err)
	}

	editReq := &tg.MessagesEditInlineBotMessageRequest{ID: msgID}
	editReq.SetMedia(&tg.InputMediaContact{
		PhoneNumber: f.peer.Phone,
		FirstName:   "Peer",
		LastName:    "Edited",
		Vcard:       "BEGIN:VCARD\nFN:Peer Edited\nEND:VCARD",
	})
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, editReq); err != nil || !ok {
		t.Fatalf("edit inline bot media = %v,%v, want true,nil", ok, err)
	}

	for _, tc := range []struct {
		name   string
		userID int64
		peer   domain.Peer
	}{
		{name: "owner", userID: f.owner.ID, peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID}},
		{name: "peer", userID: f.peer.ID, peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}},
	} {
		history, err := f.router.deps.Messages.GetHistory(ctx, tc.userID, domain.MessageFilter{
			HasPeer: true,
			Peer:    tc.peer,
			Limit:   1,
		})
		if err != nil || len(history.Messages) != 1 {
			t.Fatalf("%s history len = %d err=%v, want 1,nil", tc.name, len(history.Messages), err)
		}
		msg := history.Messages[0]
		if msg.Body != "caption before media edit" || msg.ViaBotID != f.bot.ID {
			t.Fatalf("%s edited message = %+v, want original caption via bot", tc.name, msg)
		}
		if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindContact || msg.Media.Contact == nil {
			t.Fatalf("%s edited media = %+v, want contact", tc.name, msg.Media)
		}
		if msg.Media.Contact.PhoneNumber != f.peer.Phone || msg.Media.Contact.UserID != f.peer.ID {
			t.Fatalf("%s edited contact = %+v, want peer contact", tc.name, msg.Media.Contact)
		}
		assertDomainInlineReplyMarkup(t, msg.ReplyMarkup, "open", []byte("media-v1"))
	}
}

func TestMessagesPaymentQueryAnswersRejectWithoutPaymentState(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	botCtx := WithUserID(ctx, f.bot.ID)
	userCtx := WithUserID(ctx, f.owner.ID)

	if ok, err := f.router.onMessagesSetBotShippingResults(botCtx, &tg.MessagesSetBotShippingResultsRequest{QueryID: 42}); ok || !tgerr.Is(err, "QUERY_ID_INVALID") {
		t.Fatalf("set shipping = %v,%v, want false,QUERY_ID_INVALID", ok, err)
	}
	if ok, err := f.router.onMessagesSetBotShippingResults(userCtx, &tg.MessagesSetBotShippingResultsRequest{QueryID: 42}); ok || !tgerr.Is(err, "USER_BOT_REQUIRED") {
		t.Fatalf("set shipping by user = %v,%v, want false,USER_BOT_REQUIRED", ok, err)
	}
	precheckout := &tg.MessagesSetBotPrecheckoutResultsRequest{QueryID: 43}
	precheckout.SetSuccess(true)
	if ok, err := f.router.onMessagesSetBotPrecheckoutResults(botCtx, precheckout); ok || !tgerr.Is(err, "QUERY_ID_INVALID") {
		t.Fatalf("set precheckout = %v,%v, want false,QUERY_ID_INVALID", ok, err)
	}
	if ok, err := f.router.onMessagesSetBotPrecheckoutResults(userCtx, precheckout); ok || !tgerr.Is(err, "USER_BOT_REQUIRED") {
		t.Fatalf("set precheckout by user = %v,%v, want false,USER_BOT_REQUIRED", ok, err)
	}
}

func TestMessagesBotNoStateExplicitStubsAreRegistered(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	botCtx := WithUserID(ctx, f.bot.ID)

	tests := []struct {
		name string
		req  bin.Encoder
		want string
	}{
		{
			name: "editInlineBotMessage",
			req: &tg.MessagesEditInlineBotMessageRequest{
				ID:      validInputBotInlineMessageID(f.owner.ID),
				Message: "edited",
			},
			want: "MESSAGE_ID_INVALID",
		},
		{
			name: "setBotShippingResults",
			req:  &tg.MessagesSetBotShippingResultsRequest{QueryID: 42},
			want: "QUERY_ID_INVALID",
		},
		{
			name: "setBotPrecheckoutResults",
			req: func() bin.Encoder {
				req := &tg.MessagesSetBotPrecheckoutResultsRequest{QueryID: 43}
				req.SetSuccess(true)
				return req
			}(),
			want: "QUERY_ID_INVALID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in bin.Buffer
			if err := tt.req.Encode(&in); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			if _, err := f.router.Dispatch(botCtx, [8]byte{}, 0, &in); !tgerr.Is(err, tt.want) {
				t.Fatalf("dispatch err = %v, want %s", err, tt.want)
			}
		})
	}
}

func preparedInlineArticleResult() tg.InputBotInlineResultClass {
	return &tg.InputBotInlineResult{
		ID:    "prepared-article",
		Type:  "article",
		Title: "Prepared",
		SendMessage: &tg.InputBotInlineMessageText{
			Message: "prepared text",
		},
	}
}

func inlineArticleResultWithCallbackMarkup(id, message, button string, data []byte) tg.InputBotInlineResultClass {
	return &tg.InputBotInlineResult{
		ID:    id,
		Type:  "article",
		Title: id,
		SendMessage: &tg.InputBotInlineMessageText{
			Message: message,
			ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{
				Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonCallback{Text: button, Data: data}},
			}}},
		},
	}
}

func inlineSendFeedbackFromSessions(t *testing.T, sessions *captureSessions, botID int64) *tg.UpdateBotInlineSend {
	t.Helper()
	snap := sessions.snapshot()
	if snap.userID != botID {
		t.Fatalf("feedback pushed to user %d, want bot %d", snap.userID, botID)
	}
	updates, ok := snap.message.(*tg.Updates)
	if !ok {
		t.Fatalf("feedback message = %T, want *tg.Updates", snap.message)
	}
	for _, update := range updates.Updates {
		if feedback, ok := update.(*tg.UpdateBotInlineSend); ok {
			return feedback
		}
	}
	t.Fatalf("feedback updates missing UpdateBotInlineSend: %+v", updates.Updates)
	return nil
}

func assertInlineEditedMessage(t *testing.T, msg domain.Message, wantBody string, wantBotID int64, wantButton string, wantData []byte) {
	t.Helper()
	if msg.Body != wantBody || msg.ViaBotID != wantBotID {
		t.Fatalf("message body/via = %q/%d, want %q/%d", msg.Body, msg.ViaBotID, wantBody, wantBotID)
	}
	if msg.ReplyMarkup == nil || len(msg.ReplyMarkup.Inline) != 1 || len(msg.ReplyMarkup.Inline[0]) != 1 {
		t.Fatalf("message markup = %+v, want one callback button", msg.ReplyMarkup)
	}
	btn := msg.ReplyMarkup.Inline[0][0]
	if btn.Text != wantButton || string(btn.Data) != string(wantData) {
		t.Fatalf("message button = %q/%q, want %q/%q", btn.Text, string(btn.Data), wantButton, string(wantData))
	}
}
