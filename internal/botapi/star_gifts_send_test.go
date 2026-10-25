package botapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func TestSendGiftForwardsUserRecipientAndGiftText(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{starGiftResult: true}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "sendGift",
		`{"gift_id":"6011","user_id":4242,"pay_for_upgrade":true,"text":"Happy <b>birthday</b>","text_parse_mode":"HTML"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || string(resp.Result) != "true" {
		t.Fatalf("response = %s", rec.Body.String())
	}
	if gateway.starGiftBotID != 1001 || gateway.starGiftID != 6011 || gateway.starGiftUserID != 4242 ||
		gateway.starGiftChatID != 0 || !gateway.starGiftPayForUpgrade {
		t.Fatalf("gateway call = bot %d gift %d user %d chat %d upgrade %v",
			gateway.starGiftBotID, gateway.starGiftID, gateway.starGiftUserID,
			gateway.starGiftChatID, gateway.starGiftPayForUpgrade)
	}
	// HTML markup becomes a bold entity over the plain text, exactly like every
	// other Bot API text parameter.
	if gateway.starGiftMessage.Text != "Happy birthday" ||
		len(gateway.starGiftMessage.Entities) != 1 {
		t.Fatalf("gift message = %+v", gateway.starGiftMessage)
	}
	if entity := gateway.starGiftMessage.Entities[0]; entity.Type != domain.MessageEntityBold ||
		entity.Offset != 6 || entity.Length != 8 {
		t.Fatalf("gift entity = %+v", entity)
	}
	// The gateway always receives a usable idempotency key: without one a retried
	// request would be charged twice.
	if !validBotAPIIdempotencyKey(gateway.starGiftRequestID) {
		t.Fatalf("request id = %q", gateway.starGiftRequestID)
	}
}

func TestSendGiftForwardsChannelRecipient(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{starGiftResult: true}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "sendGift",
		`{"gift_id":6012,"chat_id":-1001234567890}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.starGiftChatID != -1001234567890 || gateway.starGiftUserID != 0 || gateway.starGiftID != 6012 {
		t.Fatalf("gateway call = user %d chat %d gift %d",
			gateway.starGiftUserID, gateway.starGiftChatID, gateway.starGiftID)
	}
	if gateway.starGiftPayForUpgrade || gateway.starGiftMessage.Text != "" {
		t.Fatalf("unexpected upgrade/text: %+v", gateway.starGiftMessage)
	}
}

func TestSendGiftAcceptsIdempotencyKeyHeader(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{starGiftResult: true}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	token := domain.FormatBotToken(bots.profile.BotUserID, bots.profile.TokenSecret)
	req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/sendGift",
		strings.NewReader(`{"gift_id":"6011","user_id":4242,"request_id":"order-7"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "order-7")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.starGiftRequestID != "order-7" {
		t.Fatalf("request id = %q, want order-7", gateway.starGiftRequestID)
	}
}

func TestSendGiftRejectsInvalidParameters(t *testing.T) {
	cases := map[string]struct {
		body string
		key  string
	}{
		"missing gift id":     {`{"user_id":4242}`, ""},
		"zero gift id":        {`{"gift_id":"0","user_id":4242}`, ""},
		"garbage gift id":     {`{"gift_id":"abc","user_id":4242}`, ""},
		"no recipient":        {`{"gift_id":"6011"}`, ""},
		"both recipients":     {`{"gift_id":"6011","user_id":4242,"chat_id":-1001234567890}`, ""},
		"garbage user id":     {`{"gift_id":"6011","user_id":"x"}`, ""},
		"garbage chat id":     {`{"gift_id":"6011","chat_id":"-1001x"}`, ""},
		"unsupported entity":  {`{"gift_id":"6011","user_id":4242,"text":"x","text_entities":[{"type":"custom_emoji","offset":0,"length":1}]}`, ""},
		"long text":           {`{"gift_id":"6011","user_id":4242,"text":"` + strings.Repeat("a", domain.MaxPremiumGiftMessageRunes+1) + `"}`, ""},
		"conflicting keys":    {`{"gift_id":"6011","user_id":4242,"request_id":"a"}`, "b"},
		"illegal key charset": {`{"gift_id":"6011","user_id":4242,"request_id":"a/b"}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
			gateway := &fakeBotAPIGateway{starGiftResult: true}
			h := (&handler{bots: bots, gateway: gateway}).routes()

			req := httptest.NewRequest(http.MethodPost,
				"/bot"+domain.FormatBotToken(bots.profile.BotUserID, bots.profile.TokenSecret)+"/sendGift",
				strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			// A request that never reached the gateway must not spend Stars.
			if gateway.starGiftCalled {
				t.Fatalf("gateway was called for an invalid request: %+v", gateway.starGiftMessage)
			}
		})
	}
}

func TestSendGiftMapsGatewayErrorToDescription(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"unavailable gift": {errors.New("GIFT_NOT_AVAILABLE"), "GIFT_NOT_AVAILABLE"},
		"balance":          {errors.New("BALANCE_TOO_LOW"), "BALANCE_TOO_LOW"},
		"upgrade":          {errors.New("GIFT_UPGRADE_UNAVAILABLE"), "GIFT_UPGRADE_UNAVAILABLE"},
		"key reuse":        {errors.New("IDEMPOTENCY_KEY_INVALID"), "IDEMPOTENCY_KEY_INVALID"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
			gateway := &fakeBotAPIGateway{starGiftErr: tc.err}
			h := (&handler{bots: bots, gateway: gateway}).routes()

			rec := performBotAPIRequest(t, h, bots.profile, "sendGift",
				`{"gift_id":"6011","user_id":4242}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			var resp apiResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.OK || resp.Description != tc.want {
				t.Fatalf("description = %q, want %q", resp.Description, tc.want)
			}
		})
	}
}

func TestSendGiftWithoutCatalogReportsMethodNotFound(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	h := (&handler{bots: bots, gateway: &noStarGiftGateway{}}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "sendGift", `{"gift_id":"6011","user_id":4242}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

// A gift subsystem that cannot answer is a server failure, not a bad request: a
// bot must be able to tell "retry later" from "fix your call".
func TestSendGiftSubsystemFailureIsInternalError(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{starGiftErr: errors.New("STAR_GIFT_UNAVAILABLE")}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "sendGift", `{"gift_id":"6011","user_id":4242}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.OK || resp.Description != "STAR_GIFT_UNAVAILABLE" {
		t.Fatalf("response = %s", rec.Body.String())
	}
}
