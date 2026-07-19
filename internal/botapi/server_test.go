package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestAnswerWebAppQueryParsesArticleAndCallsService(t *testing.T) {
	webapps := &fakeWebAppService{}
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	h := (&handler{bots: bots, webapps: webapps}).routes()
	body := `{
		"web_app_query_id": "web-query-1",
		"result": {
			"type": "article",
			"id": "share-1",
			"title": "Share",
			"description": "from mini app",
			"url": "https://example.com/share",
			"input_message_content": {
				"message_text": "hello mini app",
				"disable_web_page_preview": true,
				"entities": [{"type": "bold", "offset": 0, "length": 5}]
			},
			"reply_markup": {
				"inline_keyboard": [[
					{"text": "Open", "url": "https://example.com/open"},
					{"text": "Tap", "callback_data": "cb"}
				]]
			}
		}
	}`
	rec := performBotAPIRequest(t, h, bots.profile, "answerWebAppQuery", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response ok = false: %s", rec.Body.String())
	}
	if !webapps.answerCalled || webapps.answerBotID != bots.profile.BotUserID || webapps.answerQueryID != "web-query-1" {
		t.Fatalf("answer call = %#v", webapps)
	}
	got := webapps.answerResult
	if got.ID != "share-1" || got.Type != "article" || got.Message != "hello mini app" || !got.NoWebpage || got.URL != "https://example.com/share" {
		t.Fatalf("result = %#v", got)
	}
	if len(got.Entities) != 1 || got.Entities[0].Type != domain.MessageEntityBold {
		t.Fatalf("entities = %#v", got.Entities)
	}
	if got.ReplyMarkup == nil || len(got.ReplyMarkup.Inline) != 1 || len(got.ReplyMarkup.Inline[0]) != 2 {
		t.Fatalf("reply markup = %#v", got.ReplyMarkup)
	}
}

func TestSavePreparedInlineMessageParsesPeerTypes(t *testing.T) {
	webapps := &fakeWebAppService{preparedID: "prepared-1", preparedExpire: 123456}
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	h := (&handler{bots: bots, webapps: webapps}).routes()
	body := `{
		"user_id": 2001,
		"allow_user_chats": true,
		"allow_channel_chats": true,
		"result": {
			"type": "article",
			"id": "prepared-share",
			"title": "Prepared",
			"input_message_content": {"message_text": "share me"}
		}
	}`
	rec := performBotAPIRequest(t, h, bots.profile, "savePreparedInlineMessage", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !webapps.preparedCalled || webapps.preparedBotID != 1001 || webapps.preparedUserID != 2001 {
		t.Fatalf("prepared call = %#v", webapps)
	}
	wantPeers := []string{store.InlineQueryPeerTypePM, store.InlineQueryPeerTypeBroadcast}
	if !reflect.DeepEqual(webapps.preparedPeerTypes, wantPeers) {
		t.Fatalf("peer types = %#v, want %#v", webapps.preparedPeerTypes, wantPeers)
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			ID             string `json:"id"`
			ExpirationDate int    `json:"expiration_date"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Result.ID != "prepared-1" || resp.Result.ExpirationDate != 123456 {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestAnswerWebAppQueryRejectsUnsupportedResult(t *testing.T) {
	webapps := &fakeWebAppService{}
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	h := (&handler{bots: bots, webapps: webapps}).routes()
	body := `{
		"web_app_query_id": "web-query-1",
		"result": {"type": "photo", "id": "bad", "photo_url": "https://example.com/p.jpg"}
	}`
	rec := performBotAPIRequest(t, h, bots.profile, "answerWebAppQuery", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if webapps.answerCalled {
		t.Fatalf("unsupported result should not call webapp service")
	}
	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.OK || resp.Description != "RESULT_TYPE_INVALID" {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestGetMeUsesGateway(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{
		self: domain.User{ID: 1001, FirstName: "Echo", Username: "echo_bot", Bot: true},
	}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "getMe", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			ID        int64  `json:"id"`
			IsBot     bool   `json:"is_bot"`
			FirstName string `json:"first_name"`
			Username  string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Result.ID != 1001 || !resp.Result.IsBot || resp.Result.Username != "echo_bot" {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestGetUpdatesProjectsIncomingPrivateText(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{
		updates: []domain.UpdateEvent{{
			UserID: 1001,
			Type:   domain.UpdateEventNewMessage,
			Pts:    7,
			Message: domain.Message{
				ID:          3,
				OwnerUserID: 1001,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
				Date:        1700000000,
				Body:        "/start",
				Entities:    []domain.MessageEntity{{Type: domain.MessageEntityBotCommand, Offset: 0, Length: 6}},
			},
			Users: []domain.User{{ID: 2001, FirstName: "Alice", Username: "alice"}},
		}},
	}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "getUpdates", `{"offset":1,"allowed_updates":["message"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID int `json:"update_id"`
			Message  struct {
				MessageID int    `json:"message_id"`
				Text      string `json:"text"`
				From      struct {
					ID        int64  `json:"id"`
					FirstName string `json:"first_name"`
				} `json:"from"`
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
				Entities []struct {
					Type   string `json:"type"`
					Offset int    `json:"offset"`
					Length int    `json:"length"`
				} `json:"entities"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || len(resp.Result) != 1 {
		t.Fatalf("response = %s", rec.Body.String())
	}
	got := resp.Result[0]
	if got.UpdateID != 7 || got.Message.MessageID != 3 || got.Message.Text != "/start" || got.Message.From.ID != 2001 || got.Message.Chat.ID != 2001 || got.Message.Chat.Type != "private" {
		t.Fatalf("update = %#v", got)
	}
	if len(got.Message.Entities) != 1 || got.Message.Entities[0].Type != "bot_command" {
		t.Fatalf("entities = %#v", got.Message.Entities)
	}
	if gateway.updateOffset != 1 {
		t.Fatalf("offset = %d, want 1", gateway.updateOffset)
	}
}

func TestGetUpdatesSkipsOutgoingBotMessage(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{
		updates: []domain.UpdateEvent{{
			UserID: 1001,
			Type:   domain.UpdateEventNewMessage,
			Pts:    8,
			Message: domain.Message{
				ID:          4,
				OwnerUserID: 1001,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
				Date:        1700000001,
				Body:        "sent by bot",
				Out:         true,
			},
		}},
	}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "getUpdates", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool              `json:"ok"`
		Result []json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || len(resp.Result) != 0 {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestSendMessageParsesEntitiesMarkupAndCallsGateway(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{
		self: domain.User{ID: 1001, FirstName: "Echo", Username: "echo_bot", Bot: true},
		sendMessage: domain.Message{
			ID:          9,
			OwnerUserID: 1001,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
			Date:        1700000002,
			Body:        "hello",
			Out:         true,
			Entities:    []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 5}},
			ReplyMarkup: &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{{Type: domain.MarkupButtonCallback, Text: "Tap", Data: []byte("cb")}}}},
		},
	}
	h := (&handler{bots: bots, gateway: gateway}).routes()
	body := `{
		"chat_id": 2001,
		"text": "hello",
		"entities": [{"type":"bold","offset":0,"length":5}],
		"reply_markup": {"inline_keyboard": [[{"text":"Tap","callback_data":"cb"}]]},
		"disable_notification": true,
		"reply_to_message_id": 5
	}`

	rec := performBotAPIRequest(t, h, bots.profile, "sendMessage", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !gateway.sendCalled || gateway.sendBotID != 1001 || gateway.sendChatID != 2001 || gateway.sendText != "hello" || !gateway.sendSilent || gateway.sendReplyTo != 5 {
		t.Fatalf("send call = %#v", gateway)
	}
	if len(gateway.sendEntities) != 1 || gateway.sendEntities[0].Type != domain.MessageEntityBold {
		t.Fatalf("entities = %#v", gateway.sendEntities)
	}
	if gateway.sendMarkup == nil || len(gateway.sendMarkup.Inline) != 1 || len(gateway.sendMarkup.Inline[0]) != 1 || string(gateway.sendMarkup.Inline[0][0].Data) != "cb" {
		t.Fatalf("markup = %#v", gateway.sendMarkup)
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int    `json:"message_id"`
			Text      string `json:"text"`
			From      struct {
				ID    int64 `json:"id"`
				IsBot bool  `json:"is_bot"`
			} `json:"from"`
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					Text         string `json:"text"`
					CallbackData string `json:"callback_data"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Result.MessageID != 9 || resp.Result.Text != "hello" || resp.Result.From.ID != 1001 || !resp.Result.From.IsBot {
		t.Fatalf("response = %s", rec.Body.String())
	}
	if len(resp.Result.ReplyMarkup.InlineKeyboard) != 1 || resp.Result.ReplyMarkup.InlineKeyboard[0][0].CallbackData != "cb" {
		t.Fatalf("reply_markup response = %#v", resp.Result.ReplyMarkup)
	}
}

func TestSendMessageParsesAndProjectsReplyKeyboard(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	markup := &domain.MessageReplyMarkup{
		Type:        domain.MessageReplyMarkupKeyboard,
		Keyboard:    [][]domain.MarkupButton{{{Type: domain.MarkupButtonText, Text: "Help"}, {Type: domain.MarkupButtonText, Text: "Status"}}},
		Resize:      true,
		SingleUse:   true,
		Persistent:  true,
		Placeholder: "Choose an action",
	}
	gateway := &fakeBotAPIGateway{
		self: domain.User{ID: 1001, FirstName: "Echo", Username: "echo_bot", Bot: true},
		sendMessage: domain.Message{
			ID: 10, OwnerUserID: 1001,
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
			From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
			Date: 1700000003, Body: "pick", Out: true, ReplyMarkup: markup,
		},
	}
	h := (&handler{bots: bots, gateway: gateway}).routes()
	rec := performBotAPIRequest(t, h, bots.profile, "sendMessage", `{
		"chat_id":2001,
		"text":"pick",
		"reply_markup":{
			"keyboard":[["Help",{"text":"Status"}]],
			"resize_keyboard":true,
			"one_time_keyboard":true,
			"is_persistent":true,
			"input_field_placeholder":"Choose an action"
		}
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.sendMarkup == nil || gateway.sendMarkup.Kind() != domain.MessageReplyMarkupKeyboard ||
		len(gateway.sendMarkup.Keyboard) != 1 || len(gateway.sendMarkup.Keyboard[0]) != 2 ||
		gateway.sendMarkup.Keyboard[0][0].Text != "Help" || !gateway.sendMarkup.Resize ||
		!gateway.sendMarkup.SingleUse || !gateway.sendMarkup.Persistent || gateway.sendMarkup.Placeholder != "Choose an action" {
		t.Fatalf("gateway reply keyboard = %#v", gateway.sendMarkup)
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			ReplyMarkup json.RawMessage `json:"reply_markup"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Bot API Message.reply_markup only contains InlineKeyboardMarkup; reply keyboards are
	// accepted send parameters but are deliberately absent from the returned Message object.
	if !resp.OK || len(resp.Result.ReplyMarkup) != 0 {
		t.Fatalf("reply keyboard response = %s", rec.Body.String())
	}
}

func TestGetUpdatesProjectsCallbackQuery(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	callback := &domain.BotCallbackQuery{
		ID: 123456, BotUserID: 1001, UserID: 2001,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2001}, MessageID: 9,
		ChatInstance: 9988, Data: []byte("confirm"),
	}
	gateway := &fakeBotAPIGateway{updates: []domain.UpdateEvent{{
		UserID: 1001, Type: domain.UpdateEventBotCallbackQuery, Pts: 77, Date: 1700000004,
		Peer: callback.Peer, BotCallbackQuery: callback,
		Message: domain.Message{
			ID: 9, OwnerUserID: 1001, Peer: callback.Peer,
			From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001}, Date: 1700000003,
			Body: "tap", Out: true,
		},
		Users: []domain.User{{ID: 1001, FirstName: "Echo", Bot: true}, {ID: 2001, FirstName: "Alice"}},
	}}}
	h := (&handler{bots: bots, gateway: gateway}).routes()
	rec := performBotAPIRequest(t, h, bots.profile, "getUpdates", `{"allowed_updates":["callback_query"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID      int `json:"update_id"`
			CallbackQuery struct {
				ID           string `json:"id"`
				Data         string `json:"data"`
				ChatInstance string `json:"chat_instance"`
				From         struct {
					ID int64 `json:"id"`
				} `json:"from"`
				Message struct {
					MessageID int `json:"message_id"`
				} `json:"message"`
			} `json:"callback_query"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || len(resp.Result) != 1 || resp.Result[0].UpdateID != 77 ||
		resp.Result[0].CallbackQuery.ID != "123456" || resp.Result[0].CallbackQuery.Data != "confirm" ||
		resp.Result[0].CallbackQuery.ChatInstance != "9988" || resp.Result[0].CallbackQuery.From.ID != 2001 ||
		resp.Result[0].CallbackQuery.Message.MessageID != 9 {
		t.Fatalf("callback update response = %s", rec.Body.String())
	}
}

func TestInlineCallbackProjectsOpaqueIDAndEditMessageTextUsesIt(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	inline := &domain.BotInlineMessageID{DCID: 2, OwnerID: 2001, ID: 17, AccessHash: 998877}
	callback := &domain.BotCallbackQuery{
		ID: 123456, BotUserID: 1001, UserID: 2001,
		ChatInstance: 9988, Data: []byte("inline"), InlineMessage: inline,
	}
	gateway := &fakeBotAPIGateway{updates: []domain.UpdateEvent{{
		UserID: 1001, Type: domain.UpdateEventBotCallbackQuery, Pts: 78, Date: 1700000004,
		BotCallbackQuery: callback, Users: []domain.User{{ID: 2001, FirstName: "Alice"}},
	}}}
	h := (&handler{bots: bots, gateway: gateway}).routes()
	rec := performBotAPIRequest(t, h, bots.profile, "getUpdates", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("getUpdates status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Result []struct {
			CallbackQuery struct {
				InlineMessageID string          `json:"inline_message_id"`
				Message         json.RawMessage `json:"message"`
			} `json:"callback_query"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || len(response.Result) != 1 {
		t.Fatalf("callback response=%s err=%v", rec.Body.String(), err)
	}
	inlineToken := response.Result[0].CallbackQuery.InlineMessageID
	decoded, err := decodeBotAPIInlineMessageID(inlineToken)
	if err != nil || decoded != *inline || len(response.Result[0].CallbackQuery.Message) != 0 {
		t.Fatalf("inline token=%q decoded=%#v message=%s err=%v", inlineToken, decoded, response.Result[0].CallbackQuery.Message, err)
	}
	edit := performBotAPIRequest(t, h, bots.profile, "editMessageText", `{"inline_message_id":"`+inlineToken+`","text":"updated"}`)
	if edit.Code != http.StatusOK || !gateway.editInlineCalled || gateway.editInlineID != *inline {
		t.Fatalf("edit status=%d body=%s called=%v id=%#v", edit.Code, edit.Body.String(), gateway.editInlineCalled, gateway.editInlineID)
	}
}

func TestReplyMarkupFromAPIReplyKeyboardVariants(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		kind domain.MessageReplyMarkupType
		err  string
	}{
		{name: "remove", raw: `{"remove_keyboard":true,"selective":true}`, kind: domain.MessageReplyMarkupHide},
		{name: "force", raw: `{"force_reply":true,"input_field_placeholder":"Answer"}`, kind: domain.MessageReplyMarkupForceReply},
		{name: "contact", raw: `{"keyboard":[[{"text":"Phone","request_contact":true}]]}`, kind: domain.MessageReplyMarkupKeyboard},
		{name: "filtered users", raw: `{"keyboard":[[{"text":"Premium","request_users":{"request_id":7,"user_is_bot":false,"user_is_premium":true,"max_quantity":2,"request_name":true}}]]}`, kind: domain.MessageReplyMarkupKeyboard},
		{name: "filtered chat", raw: `{"keyboard":[[{"text":"Forum","request_chat":{"request_id":8,"chat_is_channel":false,"chat_is_forum":true,"chat_has_username":false,"chat_is_created":true,"bot_is_member":true,"user_administrator_rights":{"can_manage_chat":true,"can_delete_messages":true},"bot_administrator_rights":{"can_manage_chat":true}}}]]}`, kind: domain.MessageReplyMarkupKeyboard},
		{name: "unsupported legacy user request", raw: `{"keyboard":[[{"text":"User","request_user":{"request_id":1}}]]}`, err: "BUTTON_TYPE_INVALID"},
		{name: "multiple constructors", raw: `{"keyboard":[["A"]],"inline_keyboard":[[{"text":"B","callback_data":"b"}]]}`, err: "BUTTON_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			markup, err := replyMarkupFromAPI(json.RawMessage(tt.raw))
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("error = %v, want %s", err, tt.err)
				}
				return
			}
			if err != nil || markup == nil || markup.Kind() != tt.kind {
				t.Fatalf("markup = %#v err=%v, want kind %s", markup, err, tt.kind)
			}
		})
	}
	if _, err := inlineReplyMarkupFromAPI(json.RawMessage(`{"keyboard":[["A"]]}`)); err == nil || err.Error() != "BUTTON_INVALID" {
		t.Fatalf("inline-only parser error = %v, want BUTTON_INVALID", err)
	}
	if _, err := replyMarkupFromAPI(json.RawMessage(`{"inline_keyboard":[[{"text":"Bad","url":"https://example.com","callback_data":"x"}]]}`)); err == nil || err.Error() != "BUTTON_INVALID" {
		t.Fatalf("multi-constructor inline button error = %v, want BUTTON_INVALID", err)
	}
	filtered, err := replyMarkupFromAPI(json.RawMessage(`{"keyboard":[[{"text":"Premium","request_users":{"request_id":7,"user_is_bot":false,"user_is_premium":true,"max_quantity":2}}]]}`))
	if err != nil || filtered == nil {
		t.Fatalf("filtered users markup=%#v err=%v", filtered, err)
	}
	filter := filtered.Keyboard[0][0].RequestPeerFilter
	if filter == nil || !filter.UserIsBotSet || filter.UserIsBot || !filter.UserIsPremiumSet || !filter.UserIsPremium {
		t.Fatalf("filtered users = %#v", filter)
	}
	webApp, err := replyMarkupFromAPI(json.RawMessage(`{"inline_keyboard":[[{"text":"App","web_app":{"url":"https://example.com"}}]]}`))
	if err != nil || webApp == nil || webApp.Inline[0][0].Type != domain.MarkupButtonWebView {
		t.Fatalf("web_app inline button = %#v err=%v", webApp, err)
	}
}

func TestReplyMarkupFromAPIPreservesSemanticButtonStyles(t *testing.T) {
	reply, err := replyMarkupFromAPI(json.RawMessage(`{"keyboard":[[{"text":"Run","style":"primary","icon_custom_emoji_id":"123"}]]}`))
	if err != nil {
		t.Fatalf("reply markup: %v", err)
	}
	button := reply.Keyboard[0][0]
	if button.Style != domain.MarkupButtonStylePrimary || button.IconCustomEmojiID != 123 {
		t.Fatalf("reply button = %#v", button)
	}
	inline, err := replyMarkupFromAPI(json.RawMessage(`{"inline_keyboard":[[{"text":"Delete","callback_data":"delete","style":"danger","icon_custom_emoji_id":"456"}]]}`))
	if err != nil {
		t.Fatalf("inline markup: %v", err)
	}
	button = inline.Inline[0][0]
	if button.Style != domain.MarkupButtonStyleDanger || button.IconCustomEmojiID != 456 {
		t.Fatalf("inline button = %#v", button)
	}
	projected := apiReplyMarkup(inline)
	rows := projected["inline_keyboard"].([][]map[string]any)
	if rows[0][0]["style"] != "danger" || rows[0][0]["icon_custom_emoji_id"] != "456" {
		t.Fatalf("projected inline button = %#v", rows[0][0])
	}
	if _, err := replyMarkupFromAPI(json.RawMessage(`{"keyboard":[[{"text":"Bad","style":"rainbow"}]]}`)); err == nil || err.Error() != "BUTTON_INVALID" {
		t.Fatalf("invalid style error = %v", err)
	}
}

func TestDeleteWebhookDropsPendingAndWebhookInfoReportsCount(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{pendingCount: 7}
	h := (&handler{bots: bots, gateway: gateway}).routes()
	info := performBotAPIRequest(t, h, bots.profile, "getWebhookInfo", `{}`)
	if info.Code != http.StatusOK || !strings.Contains(info.Body.String(), `"pending_update_count":7`) {
		t.Fatalf("getWebhookInfo status=%d body=%s", info.Code, info.Body.String())
	}
	drop := performBotAPIRequest(t, h, bots.profile, "deleteWebhook", `{"drop_pending_updates":true}`)
	if drop.Code != http.StatusOK || !gateway.dropPending {
		t.Fatalf("deleteWebhook status=%d body=%s drop=%v", drop.Code, drop.Body.String(), gateway.dropPending)
	}
}

func TestSetWebhookPersistsConfigReportsInfoAndConflictsWithPolling(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{pendingCount: 3}
	h := (&handler{bots: bots, gateway: gateway}).routes()
	set := performBotAPIRequest(t, h, bots.profile, "setWebhook", `{
		"url":"https://bot.example.test/hook",
		"secret_token":"safe_secret-1",
		"max_connections":12,
		"allowed_updates":["message","callback_query"],
		"drop_pending_updates":true
	}`)
	if set.Code != http.StatusOK || !gateway.webhookFound || gateway.webhook.URL != "https://bot.example.test/hook" ||
		gateway.webhook.SecretToken != "safe_secret-1" || gateway.webhook.MaxConnections != 12 ||
		len(gateway.webhook.AllowedUpdates) != 2 || !gateway.webhook.AllowedUpdatesSet || !gateway.webhookDrop {
		t.Fatalf("setWebhook status=%d body=%s config=%#v", set.Code, set.Body.String(), gateway.webhook)
	}
	info := performBotAPIRequest(t, h, bots.profile, "getWebhookInfo", `{}`)
	if info.Code != http.StatusOK || !strings.Contains(info.Body.String(), `"url":"https://bot.example.test/hook"`) ||
		!strings.Contains(info.Body.String(), `"max_connections":12`) || !strings.Contains(info.Body.String(), `"pending_update_count":3`) {
		t.Fatalf("getWebhookInfo status=%d body=%s", info.Code, info.Body.String())
	}
	reconfigure := performBotAPIRequest(t, h, bots.profile, "setWebhook", `{"url":"https://bot.example.test/new"}`)
	if reconfigure.Code != http.StatusOK || gateway.webhook.AllowedUpdatesSet {
		t.Fatalf("omitted allowed_updates status=%d body=%s config=%#v", reconfigure.Code, reconfigure.Body.String(), gateway.webhook)
	}
	poll := performBotAPIRequest(t, h, bots.profile, "getUpdates", `{}`)
	if poll.Code != http.StatusConflict || !strings.Contains(poll.Body.String(), "webhook is active") {
		t.Fatalf("getUpdates status=%d body=%s", poll.Code, poll.Body.String())
	}
	del := performBotAPIRequest(t, h, bots.profile, "deleteWebhook", `{}`)
	if del.Code != http.StatusOK || !gateway.webhookDeleted || gateway.webhookFound {
		t.Fatalf("deleteWebhook status=%d body=%s deleted=%v", del.Code, del.Body.String(), gateway.webhookDeleted)
	}
}

func TestSetWebhookRejectsUnsafeParameters(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	h := (&handler{bots: bots, gateway: &fakeBotAPIGateway{}}).routes()
	tests := []struct {
		body string
		want string
	}{
		{`{"url":"http://example.test/hook"}`, "WEBHOOK_URL_INVALID"},
		{`{"url":"https://example.test:444/hook"}`, "WEBHOOK_PORT_NOT_ALLOWED"},
		{`{"url":"https://example.test/hook","secret_token":"bad secret"}`, "SECRET_TOKEN_INVALID"},
		{`{"url":"https://example.test/hook","max_connections":101}`, "MAX_CONNECTIONS_INVALID"},
	}
	for _, tt := range tests {
		rec := performBotAPIRequest(t, h, bots.profile, "setWebhook", tt.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tt.want) {
			t.Fatalf("setWebhook body=%s status=%d response=%s want=%s", tt.body, rec.Code, rec.Body.String(), tt.want)
		}
	}
}

func TestBotAPIPollRegistryRejectsConcurrentPoller(t *testing.T) {
	var polls botAPIPollRegistry
	if !polls.acquire(1001) {
		t.Fatal("first poller was rejected")
	}
	if polls.acquire(1001) {
		t.Fatal("second poller for same bot was accepted")
	}
	if !polls.acquire(1002) {
		t.Fatal("different bot poller was rejected")
	}
	polls.release(1001)
	if !polls.acquire(1001) {
		t.Fatal("poller remained locked after release")
	}
}

func TestSendDocumentMultipartParsesFileAndCaption(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{
		self: domain.User{ID: 1001, FirstName: "Echo", Username: "echo_bot", Bot: true},
		sendMediaMessage: domain.Message{
			ID:          11,
			OwnerUserID: 1001,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
			Date:        1700000006,
			Body:        "doc caption",
			Out:         true,
			Media: &domain.MessageMedia{
				Kind: domain.MessageMediaKindDocument,
				Document: &domain.Document{
					ID:       42,
					MimeType: "text/plain",
					Size:     10,
					Attributes: []domain.DocumentAttribute{{
						Kind:     domain.DocAttrFilename,
						FileName: "note.txt",
					}},
				},
			},
		},
	}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", "2001")
	_ = writer.WriteField("caption", "doc caption")
	part, err := writer.CreateFormFile("document", "note.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("hello file")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	token := domain.FormatBotToken(bots.profile.BotUserID, bots.profile.TokenSecret)
	req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/sendDocument", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !gateway.sendMediaCalled || gateway.sendMediaKind != "document" || gateway.sendMediaChatID != 2001 || gateway.sendMediaCaption != "doc caption" {
		t.Fatalf("send media call = %#v", gateway)
	}
	if gateway.sendMediaFileName != "note.txt" || string(gateway.sendMediaBytes) != "hello file" {
		t.Fatalf("file = name %q bytes %q", gateway.sendMediaFileName, string(gateway.sendMediaBytes))
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Caption  string `json:"caption"`
			Document struct {
				FileName string `json:"file_name"`
				FileID   string `json:"file_id"`
			} `json:"document"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Result.Caption != "doc caption" || resp.Result.Document.FileName != "note.txt" {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestEditDeleteCallbackAndFileEndpoints(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	locationKey := "doc:42"
	fileID := encodeBotAPIFileID(locationKey)
	gateway := &fakeBotAPIGateway{
		self: domain.User{ID: 1001, FirstName: "Echo", Username: "echo_bot", Bot: true},
		editMessage: domain.Message{
			ID:          9,
			OwnerUserID: 1001,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
			Date:        1700000003,
			EditDate:    1700000004,
			Body:        "edited",
			Out:         true,
		},
		fileChunks: map[string]domain.FileChunk{
			locationKey: {Bytes: []byte("hello file"), MimeType: "text/plain", Total: int64(len("hello file"))},
		},
	}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	edit := performBotAPIRequest(t, h, bots.profile, "editMessageText", `{"chat_id":2001,"message_id":9,"text":"edited","reply_markup":{"inline_keyboard":[]}}`)
	if edit.Code != http.StatusOK {
		t.Fatalf("edit status = %d body = %s", edit.Code, edit.Body.String())
	}
	if !gateway.editCalled || !gateway.editSetMarkup {
		t.Fatalf("edit gateway = %#v", gateway)
	}

	del := performBotAPIRequest(t, h, bots.profile, "deleteMessage", `{"chat_id":2001,"message_id":9}`)
	if del.Code != http.StatusOK || !gateway.deleteCalled {
		t.Fatalf("delete status = %d body = %s gateway=%#v", del.Code, del.Body.String(), gateway)
	}

	cb := performBotAPIRequest(t, h, bots.profile, "answerCallbackQuery", `{"callback_query_id":"123","text":"ok"}`)
	if cb.Code != http.StatusOK || !gateway.callbackCalled || gateway.callbackID != "123" {
		t.Fatalf("callback status = %d body = %s gateway=%#v", cb.Code, cb.Body.String(), gateway)
	}

	file := performBotAPIRequest(t, h, bots.profile, "getFile", `{"file_id":"`+fileID+`"}`)
	if file.Code != http.StatusOK {
		t.Fatalf("getFile status = %d body = %s", file.Code, file.Body.String())
	}
	var fileResp struct {
		OK     bool `json:"ok"`
		Result struct {
			FileID   string `json:"file_id"`
			FilePath string `json:"file_path"`
			FileSize int64  `json:"file_size"`
		} `json:"result"`
	}
	if err := json.Unmarshal(file.Body.Bytes(), &fileResp); err != nil {
		t.Fatalf("decode getFile: %v", err)
	}
	if !fileResp.OK || fileResp.Result.FileID != fileID || fileResp.Result.FilePath != fileID || fileResp.Result.FileSize != int64(len("hello file")) || gateway.fileLocationKey != locationKey {
		t.Fatalf("getFile response = %s gateway=%#v", file.Body.String(), gateway)
	}

	token := domain.FormatBotToken(bots.profile.BotUserID, bots.profile.TokenSecret)
	req := httptest.NewRequest(http.MethodGet, "/file/bot"+token+"/"+fileID, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello file" || rec.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("download status=%d content-type=%q body=%q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestAPIMessageProjectsMediaCaptionAndFileID(t *testing.T) {
	msg := domain.Message{
		ID:          3,
		OwnerUserID: 1001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
		Date:        1700000005,
		Body:        "caption",
		Entities:    []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 7}},
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindDocument,
			Document: &domain.Document{
				ID:       42,
				MimeType: "text/plain",
				Size:     10,
				Attributes: []domain.DocumentAttribute{{
					Kind:     domain.DocAttrFilename,
					FileName: "note.txt",
				}},
			},
		},
	}
	projected := apiMessage(msg, []domain.User{{ID: 2001, FirstName: "Alice"}})
	if _, hasText := projected["text"]; hasText {
		t.Fatalf("media message has text field: %#v", projected)
	}
	if projected["caption"] != "caption" {
		t.Fatalf("caption = %#v", projected["caption"])
	}
	if _, ok := projected["caption_entities"].([]map[string]any); !ok {
		t.Fatalf("caption_entities = %#v", projected["caption_entities"])
	}
	document, ok := projected["document"].(map[string]any)
	if !ok {
		t.Fatalf("document = %#v", projected["document"])
	}
	fileID, _ := document["file_id"].(string)
	if locationKey, ok := decodeBotAPIFileID(fileID); !ok || locationKey != "doc:42" {
		t.Fatalf("file_id %q decodes to %q ok=%v", fileID, locationKey, ok)
	}
	if document["file_name"] != "note.txt" || document["mime_type"] != "text/plain" {
		t.Fatalf("document = %#v", document)
	}
}

func TestAPIUpdateProjectsCaptionlessMediaMessage(t *testing.T) {
	item, kind, ok := apiUpdate(domain.UpdateEvent{
		Type: domain.UpdateEventNewMessage,
		Pts:  12,
		Message: domain.Message{
			ID:          4,
			OwnerUserID: 1001,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
			Date:        1700000006,
			Media: &domain.MessageMedia{
				Kind: domain.MessageMediaKindDocument,
				Document: &domain.Document{
					ID:       43,
					MimeType: "application/octet-stream",
					Size:     4,
				},
			},
		},
	})
	if !ok || kind != "message" {
		t.Fatalf("apiUpdate ok=%v kind=%q item=%#v", ok, kind, item)
	}
	msg, ok := item["message"].(map[string]any)
	if !ok {
		t.Fatalf("message = %#v", item["message"])
	}
	if _, hasText := msg["text"]; hasText {
		t.Fatalf("captionless media has text: %#v", msg)
	}
	if _, hasCaption := msg["caption"]; hasCaption {
		t.Fatalf("captionless media has caption: %#v", msg)
	}
	if _, ok := msg["document"].(map[string]any); !ok {
		t.Fatalf("document = %#v", msg["document"])
	}
}

func TestAPIMessageProjectsReplyKeyboardResponses(t *testing.T) {
	base := domain.Message{
		ID: 10, OwnerUserID: 1001, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
		From: domain.Peer{Type: domain.PeerTypeUser, ID: 2001}, Date: 1700000010,
	}
	t.Run("contact", func(t *testing.T) {
		msg := base
		msg.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{
			PhoneNumber: "+12025550123", FirstName: "Alice", LastName: "Example", Vcard: "VCARD", UserID: 2001,
		}}
		contact := apiMessage(msg, nil)["contact"].(map[string]any)
		if contact["phone_number"] != "+12025550123" || contact["user_id"] != int64(2001) {
			t.Fatalf("contact=%#v", contact)
		}
	})
	t.Run("locations", func(t *testing.T) {
		msg := base
		msg.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindGeo, Geo: &domain.MessageGeoPoint{Lat: 1.5, Long: 2.5, AccuracyRadius: 7}}
		location := apiMessage(msg, nil)["location"].(map[string]any)
		if location["latitude"] != 1.5 || location["horizontal_accuracy"] != float64(7) {
			t.Fatalf("location=%#v", location)
		}
		msg.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{
			Geo: domain.MessageGeoPoint{Lat: 3.5, Long: 4.5}, Period: 60, Heading: 90, ProximityNotificationRadius: 25,
		}}
		location = apiMessage(msg, nil)["location"].(map[string]any)
		if location["live_period"] != 60 || location["heading"] != 90 || location["proximity_alert_radius"] != 25 {
			t.Fatalf("live location=%#v", location)
		}
	})
	t.Run("venue", func(t *testing.T) {
		msg := base
		msg.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindVenue, Venue: &domain.MessageVenue{
			Geo: domain.MessageGeoPoint{Lat: 1, Long: 2}, Title: "Cafe", Address: "Main St",
			Provider: "foursquare", VenueID: "place-1", VenueType: "food/cafe",
		}}
		venue := apiMessage(msg, nil)["venue"].(map[string]any)
		if venue["title"] != "Cafe" || venue["foursquare_id"] != "place-1" {
			t.Fatalf("venue=%#v", venue)
		}
	})
	t.Run("poll", func(t *testing.T) {
		msg := base
		msg.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindPoll, Poll: &domain.MessagePoll{
			ID: 77, Question: "Pick", Quiz: true, RevotingDisabled: true,
			Answers: []domain.MessagePollAnswer{{Text: "A", Option: []byte{1}}, {Text: "B", Option: []byte{2}}},
			Results: &domain.MessagePollResults{TotalVoters: 3, Voters: []domain.MessagePollAnswerVoters{
				{Option: []byte{1}, Voters: 1}, {Option: []byte{2}, Voters: 2, Correct: true},
			}, Solution: "Because B"},
		}}
		poll := apiMessage(msg, nil)["poll"].(map[string]any)
		options := poll["options"].([]map[string]any)
		correct := poll["correct_option_ids"].([]int)
		if poll["id"] != "77" || poll["type"] != "quiz" || poll["allows_revoting"] != false ||
			len(options) != 2 || options[1]["voter_count"] != 2 || len(correct) != 1 || correct[0] != 1 {
			t.Fatalf("poll=%#v", poll)
		}
	})
	t.Run("web_app_data", func(t *testing.T) {
		msg := base
		msg.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind:        domain.MessageServiceActionWebViewDataSent,
			WebViewData: &domain.MessageWebViewDataAction{ButtonText: "Open", Data: `{"ok":true}`},
		}}
		data := apiMessage(msg, nil)["web_app_data"].(map[string]any)
		if data["button_text"] != "Open" || data["data"] != `{"ok":true}` {
			t.Fatalf("web_app_data=%#v", data)
		}
	})
	t.Run("shared_peers", func(t *testing.T) {
		msg := base
		sharedPhoto := domain.Photo{ID: 9001, Sizes: []domain.PhotoSize{{
			Kind: domain.PhotoSizeKindDefault, Type: "m", W: 320, H: 320, Size: 4096,
		}}}
		msg.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionRequestedPeer,
			RequestedPeer: &domain.MessageRequestedPeerAction{
				ButtonID: 42, Peers: []domain.Peer{{Type: domain.PeerTypeUser, ID: 3001}},
				Details: []domain.MessageRequestedPeerDetails{{
					Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 3001}, FirstName: "Shared", Username: "shared_user", Photo: &sharedPhoto,
				}},
				NameRequested: true, UsernameRequested: true, PhotoRequested: true,
			},
		}}
		projected := apiMessage(msg, nil)
		usersShared := projected["users_shared"].(map[string]any)
		sharedUsers := usersShared["users"].([]map[string]any)
		if usersShared["request_id"] != 42 || sharedUsers[0]["user_id"] != int64(3001) || sharedUsers[0]["username"] != "shared_user" || len(sharedUsers[0]["photo"].([]map[string]any)) != 1 {
			t.Fatalf("users_shared=%#v", usersShared)
		}
		msg.Media.ServiceAction.RequestedPeer.Peers = []domain.Peer{{Type: domain.PeerTypeChannel, ID: 55}}
		msg.Media.ServiceAction.RequestedPeer.Details = []domain.MessageRequestedPeerDetails{{
			Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 55}, Title: "Shared Chat", Username: "shared_chat",
		}}
		projected = apiMessage(msg, nil)
		chatShared := projected["chat_shared"].(map[string]any)
		if chatShared["request_id"] != 42 || chatShared["chat_id"] != int64(-1000000000055) || chatShared["title"] != "Shared Chat" {
			t.Fatalf("chat_shared=%#v", chatShared)
		}
	})
}

func performBotAPIRequest(t *testing.T, h http.Handler, profile domain.BotProfile, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	token := domain.FormatBotToken(profile.BotUserID, profile.TokenSecret)
	req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/"+method, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

type fakeBotAPIBots struct {
	profile domain.BotProfile
}

func (f *fakeBotAPIBots) BotInfo(context.Context, int64) (domain.BotProfile, bool, error) {
	return f.profile, true, nil
}

func (f *fakeBotAPIBots) SetBotMenuButton(context.Context, int64, domain.BotMenuButton) (int, error) {
	return 0, nil
}

func (f *fakeBotAPIBots) GetBotMenuButton(context.Context, int64) (domain.BotMenuButton, error) {
	return domain.BotMenuButton{Type: domain.BotMenuButtonDefault}, nil
}

func (f *fakeBotAPIBots) BotEmojiStatusPermission(context.Context, int64, int64) (bool, error) {
	return true, nil
}

type fakeWebAppService struct {
	answerCalled  bool
	answerBotID   int64
	answerQueryID string
	answerResult  domain.BotInlineResult

	preparedCalled    bool
	preparedBotID     int64
	preparedUserID    int64
	preparedResult    domain.BotInlineResult
	preparedPeerTypes []string
	preparedID        string
	preparedExpire    int
}

func (f *fakeWebAppService) AnswerWebAppQueryFromBotAPI(_ context.Context, botID int64, queryID string, result domain.BotInlineResult) (string, error) {
	f.answerCalled = true
	f.answerBotID = botID
	f.answerQueryID = queryID
	f.answerResult = result
	return "", nil
}

func (f *fakeWebAppService) SavePreparedInlineMessageFromBotAPI(_ context.Context, botID, userID int64, result domain.BotInlineResult, peerTypes []string) (string, int, error) {
	f.preparedCalled = true
	f.preparedBotID = botID
	f.preparedUserID = userID
	f.preparedResult = result
	f.preparedPeerTypes = append([]string(nil), peerTypes...)
	return f.preparedID, f.preparedExpire, nil
}

type fakeBotAPIGateway struct {
	self domain.User

	updates      []domain.UpdateEvent
	updateBotID  int64
	updateOffset int64

	sendCalled        bool
	sendBotID         int64
	sendChatID        int64
	sendText          string
	sendEntities      []domain.MessageEntity
	sendMarkup        *domain.MessageReplyMarkup
	sendNoWebpage     bool
	sendSilent        bool
	sendReplyTo       int
	sendMessage       domain.Message
	sendMediaCalled   bool
	sendMediaKind     string
	sendMediaChatID   int64
	sendMediaFileName string
	sendMediaBytes    []byte
	sendMediaCaption  string
	sendMediaMessage  domain.Message
	editCalled        bool
	editSetMarkup     bool
	editMessage       domain.Message
	editInlineCalled  bool
	editInlineID      domain.BotInlineMessageID
	deleteCalled      bool
	callbackCalled    bool
	callbackID        string
	fileLocationKey   string
	fileChunks        map[string]domain.FileChunk
	allowedUpdates    []domain.BotAPIUpdateKind
	dropPending       bool
	pendingCount      int
	webhook           domain.BotAPIWebhook
	webhookFound      bool
	webhookDeleted    bool
	webhookDrop       bool
	webhookConfirmed  int64
}

func (f *fakeBotAPIGateway) BotAPISelf(context.Context, int64) (domain.User, error) {
	return f.self, nil
}

func (f *fakeBotAPIGateway) BotAPIUpdates(_ context.Context, botID int64, offset int64) ([]domain.UpdateEvent, error) {
	f.updateBotID = botID
	f.updateOffset = offset
	return append([]domain.UpdateEvent(nil), f.updates...), nil
}

func (f *fakeBotAPIGateway) BotAPISetAllowedUpdates(_ context.Context, _ int64, allowed []domain.BotAPIUpdateKind) error {
	f.allowedUpdates = append([]domain.BotAPIUpdateKind(nil), allowed...)
	return nil
}

func (f *fakeBotAPIGateway) BotAPIDropPendingUpdates(context.Context, int64) error {
	f.dropPending = true
	return nil
}

func (f *fakeBotAPIGateway) BotAPIPendingUpdateCount(context.Context, int64) (int, error) {
	return f.pendingCount, nil
}

func (f *fakeBotAPIGateway) BotAPISetWebhook(_ context.Context, config domain.BotAPIWebhook, dropPending bool) error {
	f.webhook, f.webhookFound, f.webhookDrop = config, true, dropPending
	return nil
}

func (f *fakeBotAPIGateway) BotAPIDeleteWebhook(_ context.Context, _ int64, dropPending bool) error {
	f.webhook, f.webhookFound, f.webhookDeleted, f.webhookDrop = domain.BotAPIWebhook{}, false, true, dropPending
	if dropPending {
		f.dropPending = true
	}
	return nil
}

func (f *fakeBotAPIGateway) BotAPIWebhook(context.Context, int64) (domain.BotAPIWebhook, bool, error) {
	return f.webhook, f.webhookFound, nil
}

func (f *fakeBotAPIGateway) ListDueBotAPIWebhooks(context.Context, int) ([]domain.BotAPIWebhook, error) {
	if !f.webhookFound {
		return nil, nil
	}
	return []domain.BotAPIWebhook{f.webhook}, nil
}

func (f *fakeBotAPIGateway) AcquireBotAPIWebhookLease(context.Context, int64, string, time.Duration) (bool, error) {
	return true, nil
}

func (f *fakeBotAPIGateway) ReleaseBotAPIWebhookLease(context.Context, int64, string) error {
	return nil
}

func (f *fakeBotAPIGateway) RecordBotAPIWebhookFailure(context.Context, int64, string, time.Time, string) error {
	return nil
}

func (f *fakeBotAPIGateway) RecordBotAPIWebhookSuccess(context.Context, int64, string, time.Time) error {
	return nil
}

func (f *fakeBotAPIGateway) ConfirmBotAPIWebhookDelivery(_ context.Context, _ int64, updateID int64) error {
	f.webhookConfirmed = updateID
	return nil
}

func (f *fakeBotAPIGateway) BotAPISendMessage(_ context.Context, botID, chatID int64, text string, entities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview, silent bool, replyToMessageID int) (domain.Message, error) {
	f.sendCalled = true
	f.sendBotID = botID
	f.sendChatID = chatID
	f.sendText = text
	f.sendEntities = append([]domain.MessageEntity(nil), entities...)
	f.sendMarkup = replyMarkup
	f.sendNoWebpage = disableWebPagePreview
	f.sendSilent = silent
	f.sendReplyTo = replyToMessageID
	return f.sendMessage, nil
}

func (f *fakeBotAPIGateway) BotAPISendMedia(_ context.Context, botID, chatID int64, kind, locationKey, remoteURL, fileName, mimeType string, fileBytes []byte, caption string, entities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent bool, replyToMessageID int) (domain.Message, error) {
	f.sendMediaCalled = true
	f.sendMediaKind = kind
	f.sendMediaChatID = chatID
	f.sendMediaFileName = fileName
	f.sendMediaBytes = append([]byte(nil), fileBytes...)
	f.sendMediaCaption = caption
	return f.sendMediaMessage, nil
}

func (f *fakeBotAPIGateway) BotAPIEditMessageText(_ context.Context, botID, chatID int64, messageID int, text string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview bool) (domain.Message, error) {
	f.editCalled = true
	f.editSetMarkup = setReplyMarkup
	return f.editMessage, nil
}

func (f *fakeBotAPIGateway) BotAPIEditInlineMessageText(_ context.Context, _ int64, inlineMessageID domain.BotInlineMessageID, _ string, _ []domain.MessageEntity, _ bool, _ *domain.MessageReplyMarkup, _ bool) (bool, error) {
	f.editInlineCalled, f.editInlineID = true, inlineMessageID
	return true, nil
}

func (f *fakeBotAPIGateway) BotAPIDeleteMessage(context.Context, int64, int64, int) (bool, error) {
	f.deleteCalled = true
	return true, nil
}

func (f *fakeBotAPIGateway) BotAPIAnswerCallbackQuery(_ context.Context, _ int64, callbackQueryID, text, url string, showAlert bool, cacheTime int) (bool, error) {
	f.callbackCalled = true
	f.callbackID = callbackQueryID
	return true, nil
}

func (f *fakeBotAPIGateway) BotAPIGetFile(_ context.Context, _ int64, locationKey string, offset int64, limit int) (domain.FileChunk, bool, error) {
	f.fileLocationKey = locationKey
	chunk, ok := f.fileChunks[locationKey]
	if !ok {
		return domain.FileChunk{}, false, nil
	}
	if offset >= int64(len(chunk.Bytes)) {
		return domain.FileChunk{MimeType: chunk.MimeType, Total: chunk.Total}, true, nil
	}
	end := offset + int64(limit)
	if end > int64(len(chunk.Bytes)) {
		end = int64(len(chunk.Bytes))
	}
	out := chunk
	out.Bytes = append([]byte(nil), chunk.Bytes[offset:end]...)
	return out, true, nil
}
