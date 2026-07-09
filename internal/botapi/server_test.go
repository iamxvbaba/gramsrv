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
	deleteCalled      bool
	callbackCalled    bool
	callbackID        string
	fileLocationKey   string
	fileChunks        map[string]domain.FileChunk
}

func (f *fakeBotAPIGateway) BotAPISelf(context.Context, int64) (domain.User, error) {
	return f.self, nil
}

func (f *fakeBotAPIGateway) BotAPIUpdates(_ context.Context, botID int64, offset int64) ([]domain.UpdateEvent, error) {
	f.updateBotID = botID
	f.updateOffset = offset
	return append([]domain.UpdateEvent(nil), f.updates...), nil
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
