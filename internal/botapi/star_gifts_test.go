package botapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"telesrv/internal/domain"
)

// noStarGiftGateway is a gateway without a gift catalog, so the optional
// StarGiftGatewayService assertion fails the way it does on a deployment that
// never wired one.
type noStarGiftGateway struct{ GatewayService }

func TestGetAvailableGiftsProjectsCatalogEntries(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{availableGifts: []domain.StarGift{
		{
			ID: 6011, Stars: 15, Sticker: domain.Document{
				ID: 771, MimeType: "image/webp", Size: 4096,
				Attributes: []domain.DocumentAttribute{
					{Kind: domain.DocAttrSticker, W: 512, H: 512, Alt: "🎁"},
				},
			},
		},
		{
			ID: 6012, Stars: 25, Limited: true,
			AvailabilityTotal: 1000, AvailabilityRemains: 734,
			UpgradeStars: 10, UpgradeTotal: 500, UpgradeIssued: 500,
			Sticker: domain.Document{
				ID:         772,
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker, W: 512, H: 512}},
			},
		},
	}}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "getAvailableGifts", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Gifts []map[string]any `json:"gifts"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || len(resp.Result.Gifts) != 2 {
		t.Fatalf("response = %s", rec.Body.String())
	}

	unlimited := resp.Result.Gifts[0]
	if unlimited["id"] != "6011" || unlimited["star_count"] != float64(15) {
		t.Fatalf("unlimited gift = %#v", unlimited)
	}
	// No collectible supply configured means no upgrade price to advertise, and
	// an unlimited gift carries no inventory counters at all.
	if _, ok := unlimited["upgrade_star_count"]; ok {
		t.Fatalf("unlimited gift advertises upgrade_star_count: %#v", unlimited)
	}
	for _, key := range []string{"total_count", "remaining_count"} {
		if _, ok := unlimited[key]; ok {
			t.Fatalf("unlimited gift advertises %s: %#v", key, unlimited)
		}
	}
	sticker, ok := unlimited["sticker"].(map[string]any)
	if !ok || sticker["width"] != float64(512) || sticker["height"] != float64(512) ||
		sticker["type"] != "regular" || sticker["emoji"] != "🎁" || sticker["file_id"] == "" {
		t.Fatalf("gift sticker = %#v", unlimited["sticker"])
	}

	limited := resp.Result.Gifts[1]
	if limited["id"] != "6012" || limited["total_count"] != float64(1000) || limited["remaining_count"] != float64(734) {
		t.Fatalf("limited gift = %#v", limited)
	}
	// Upgrade supply is exhausted, so the price must not be advertised: a bot
	// that trusted it would pick an upgrade the server refuses.
	if _, ok := limited["upgrade_star_count"]; ok {
		t.Fatalf("exhausted upgrade advertises upgrade_star_count: %#v", limited)
	}
}

func TestGetAvailableGiftsKeepsUpgradableGiftPrice(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{availableGifts: []domain.StarGift{{
		ID: 6013, Stars: 25, UpgradeStars: 10, UpgradeTotal: 500, UpgradeIssued: 120,
	}}}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "getAvailableGifts", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Result struct {
			Gifts []map[string]any `json:"gifts"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Result.Gifts) != 1 || resp.Result.Gifts[0]["upgrade_star_count"] != float64(10) {
		t.Fatalf("gifts = %s", rec.Body.String())
	}
}

func TestGetAvailableGiftsWithoutCatalogReportsMethodNotFound(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	h := (&handler{bots: bots, gateway: &noStarGiftGateway{}}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "getAvailableGifts", `{}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestGetAvailableGiftsCatalogFailureIsInternal(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{availableGiftsErr: errors.New("catalog unavailable")}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "getAvailableGifts", `{}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.OK || resp.Description != "INTERNAL_SERVER_ERROR" {
		t.Fatalf("response = %s", rec.Body.String())
	}
}
