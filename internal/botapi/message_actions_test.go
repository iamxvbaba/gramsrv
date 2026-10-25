package botapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"telesrv/internal/domain"
)

type fakeMessageActionGateway struct {
	*fakeBotAPIGateway
	action        domain.BotAPIChatAction
	actionChatID  int64
	actionThread  int
	caption       string
	captionMarkup bool
	markup        *domain.MessageReplyMarkup
	markupCalled  bool
}

func (f *fakeMessageActionGateway) BotAPISendChatAction(_ context.Context, _ int64, chatID int64, action domain.BotAPIChatAction, topMessageID int) (bool, error) {
	f.action, f.actionChatID, f.actionThread = action, chatID, topMessageID
	return true, nil
}

func (f *fakeMessageActionGateway) BotAPIEditMessageCaption(_ context.Context, _ int64, _ int64, _ int, caption string, _ []domain.MessageEntity, setReplyMarkup bool, _ *domain.MessageReplyMarkup) (domain.Message, error) {
	f.caption, f.captionMarkup = caption, setReplyMarkup
	return f.editMessage, nil
}

func (f *fakeMessageActionGateway) BotAPIEditMessageReplyMarkup(_ context.Context, _ int64, _ int64, _ int, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	f.markupCalled, f.markup = true, replyMarkup
	return f.editMessage, nil
}

func TestMessageActionEndpoints(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeMessageActionGateway{fakeBotAPIGateway: &fakeBotAPIGateway{
		self: domain.User{ID: 1001, FirstName: "Echo", Username: "echo_bot", Bot: true},
		editMessage: domain.Message{
			ID: 9, OwnerUserID: 1001, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
			From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001}, Date: 1700000003, EditDate: 1700000004,
			Body: "caption", Out: true,
		},
	}}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "sendChatAction", `{"chat_id":2001,"action":"upload_document","message_thread_id":5}`)
	if rec.Code != http.StatusOK || gateway.action != domain.BotAPIChatActionUploadDocument || gateway.actionChatID != 2001 || gateway.actionThread != 5 {
		t.Fatalf("sendChatAction status=%d body=%s gateway=%+v", rec.Code, rec.Body.String(), gateway)
	}
	if rec := performBotAPIRequest(t, h, bots.profile, "sendChatAction", `{"chat_id":2001,"action":"dance"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}

	rec = performBotAPIRequest(t, h, bots.profile, "editMessageCaption", `{"chat_id":2001,"message_id":9,"caption":"<b>caption</b>","parse_mode":"HTML","reply_markup":{"inline_keyboard":[]}}`)
	if rec.Code != http.StatusOK || gateway.caption != "caption" || !gateway.captionMarkup {
		t.Fatalf("editMessageCaption status=%d body=%s gateway=%+v", rec.Code, rec.Body.String(), gateway)
	}
	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !resp.OK {
		t.Fatalf("editMessageCaption response = %s err=%v", rec.Body.String(), err)
	}

	rec = performBotAPIRequest(t, h, bots.profile, "editMessageReplyMarkup", `{"chat_id":2001,"message_id":9,"reply_markup":{"inline_keyboard":[[{"text":"Next","callback_data":"page:2"}]]}}`)
	if rec.Code != http.StatusOK || !gateway.markupCalled || gateway.markup == nil || string(gateway.markup.Inline[0][0].Data) != "page:2" {
		t.Fatalf("editMessageReplyMarkup status=%d body=%s markup=%+v", rec.Code, rec.Body.String(), gateway.markup)
	}
	gateway.markupCalled = false
	rec = performBotAPIRequest(t, h, bots.profile, "editMessageReplyMarkup", `{"chat_id":2001,"message_id":9}`)
	if rec.Code != http.StatusOK || !gateway.markupCalled || gateway.markup != nil {
		t.Fatalf("editMessageReplyMarkup removal status=%d body=%s markup=%+v", rec.Code, rec.Body.String(), gateway.markup)
	}
	if rec := performBotAPIRequest(t, h, bots.profile, "editMessageReplyMarkup", `{"inline_message_id":"abc"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("inline_message_id status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}

	// A gateway without the optional surface keeps answering METHOD_NOT_FOUND.
	plain := (&handler{bots: bots, gateway: gateway.fakeBotAPIGateway}).routes()
	if rec := performBotAPIRequest(t, plain, bots.profile, "sendChatAction", `{"chat_id":2001,"action":"typing"}`); rec.Code != http.StatusNotImplemented {
		t.Fatalf("plain gateway sendChatAction status=%d, want 501", rec.Code)
	}
}
