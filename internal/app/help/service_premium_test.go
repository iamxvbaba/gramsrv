package help

import (
	"context"
	"encoding/json"
	"testing"

	"telesrv/internal/domain"
)

// TestAppConfigPremiumKeys 断言 premium / Stars 相关 key 完整下发且 hash 已递增：
// premium_purchase_blocked 必须显式为 false——客户端把 star gift「Send a Gift」入口与
// premiumCanBuy()=!premium_purchase_blocked 耦合，置 true 会同时隐藏送礼入口；
// reactions_user_max_premium 必须与服务端 enforcement 档位一致。
func TestAppConfigPremiumKeys(t *testing.T) {
	cfg, notModified, err := (*Service)(nil).GetAppConfig(context.Background(), 0, 0)
	if err != nil || notModified {
		t.Fatalf("GetAppConfig = notModified %v err %v", notModified, err)
	}
	if cfg.Hash != defaultAppConfigHash || cfg.Hash < 10 {
		t.Fatalf("hash = %d, want defaultAppConfigHash(≥10)", cfg.Hash)
	}
	oldCfg, oldNotModified, err := (*Service)(nil).GetAppConfig(context.Background(), 0, defaultAppConfigHash-1)
	if err != nil || oldNotModified || oldCfg.Hash != defaultAppConfigHash {
		t.Fatalf("GetAppConfig(old hash) = hash %d notModified %v err %v, want refreshed config", oldCfg.Hash, oldNotModified, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(cfg.JSON, &decoded); err != nil {
		t.Fatalf("app config json invalid: %v", err)
	}
	if got := decoded["ephemeral_welcome_messages_max"]; got != float64(domain.MaxWelcomeMessagesPerPeer) {
		t.Fatalf("ephemeral_welcome_messages_max = %v, want %d", got, domain.MaxWelcomeMessagesPerPeer)
	}
	if period, ok := decoded["no_forwards_request_expire_period"].(float64); !ok || int(period) != domain.PrivateNoForwardsRequestExpirePeriod {
		t.Fatalf("no_forwards_request_expire_period = %v, want %d", decoded["no_forwards_request_expire_period"], domain.PrivateNoForwardsRequestExpirePeriod)
	}
	if blocked, ok := decoded["premium_purchase_blocked"].(bool); !ok || blocked {
		t.Fatalf("premium_purchase_blocked = %v, want false (star gift 送礼入口耦合此 flag)", decoded["premium_purchase_blocked"])
	}
	if username, ok := decoded["premium_bot_username"].(string); !ok || username != "premiumbot" {
		t.Fatalf("premium_bot_username = %v, want premiumbot", decoded["premium_bot_username"])
	}
	for _, key := range []string{"premium_gift_attach_menu_icon", "premium_gift_text_field_icon"} {
		if enabled, ok := decoded[key].(bool); !ok || !enabled {
			t.Fatalf("%s = %v, want true", key, decoded[key])
		}
	}
	// DrKLO 缺省 starsLocked=true；缺 key 时余额不足送礼会误弹「所在国家无法购买星星」。
	if blocked, ok := decoded["stars_purchase_blocked"].(bool); !ok || blocked {
		t.Fatalf("stars_purchase_blocked = %v, want false (DrKLO starsPurchaseAvailable 据此解锁充值入口)", decoded["stars_purchase_blocked"])
	}
	// DrKLO 缺省 stargiftsBlocked=true 会隐藏 star gift 送礼网格，必须显式下发 false。
	if blocked, ok := decoded["stargifts_blocked"].(bool); !ok || blocked {
		t.Fatalf("stargifts_blocked = %v, want false (DrKLO GiftSheet 据此隐藏礼物网格)", decoded["stargifts_blocked"])
	}
	if available, ok := decoded["giveaway_gifts_purchase_available"].(bool); !ok || !available {
		t.Fatalf("giveaway_gifts_purchase_available = %v, want true", decoded["giveaway_gifts_purchase_available"])
	}
	directCurrencies, ok := decoded["premium_playmarket_direct_currency_list"].([]any)
	if !ok || len(directCurrencies) == 0 || !containsJSONCurrency(directCurrencies, "USD") {
		t.Fatalf("premium_playmarket_direct_currency_list = %#v, want non-empty list containing USD", decoded["premium_playmarket_direct_currency_list"])
	}
	if posting, ok := decoded["rich_message_posting"].(string); !ok || posting != "enabled" {
		t.Fatalf("rich_message_posting = %v, want enabled (TDesktop 富文本编辑入口默认打开)", decoded["rich_message_posting"])
	}
	fragmentPrefixes, ok := decoded["fragment_prefixes"].([]any)
	if !ok || len(fragmentPrefixes) != 1 || fragmentPrefixes[0] != "888" {
		t.Fatalf("fragment_prefixes = %#v, want [\"888\"]", decoded["fragment_prefixes"])
	}
	wantNumbers := map[string]float64{
		"giveaway_boosts_per_premium":               4,
		"giveaway_countries_max":                    10,
		"giveaway_add_peers_max":                    10,
		"giveaway_period_max":                       604800,
		"reactions_user_max_default":                1,
		"reactions_user_max_premium":                3,
		"boosts_channel_level_max":                  100,
		"stargifts_pinned_to_top_limit":             6,
		"stars_stargift_resale_amount_min":          float64(domain.MinStarGiftResaleStars),
		"gift_text_length_max":                      domain.MaxPremiumGiftMessageRunes,
		"about_length_limit_default":                70,
		"about_length_limit_premium":                140,
		"bot_verification_description_length_limit": domain.MaxCustomVerificationDescriptionLength,
		"dialogs_pinned_limit_default":              5,
		"dialogs_pinned_limit_premium":              10,
		"dialogs_folder_pinned_limit_default":       100,
		"dialogs_folder_pinned_limit_premium":       200,
		"saved_dialogs_pinned_limit_default":        5,
		"saved_dialogs_pinned_limit_premium":        100,
		"caption_length_limit_default":              1024,
		"caption_length_limit_premium":              4096,
		"channels_limit_default":                    500,
		"channels_limit_premium":                    1000,
		"dialog_filters_limit_default":              10,
		"dialog_filters_limit_premium":              20,
		"chatlist_update_period":                    3600,
		"chatlist_invites_limit_default":            3,
		"chatlist_invites_limit_premium":            20,
		"chatlists_joined_limit_default":            2,
		"chatlists_joined_limit_premium":            20,
		"upload_max_fileparts_default":              4000,
		"upload_max_fileparts_premium":              8000,
		"aicompose_tone_examples_num":               3,
		"aicompose_tone_title_length_max":           12,
		"aicompose_tone_prompt_length_max":          1024,
		"aicompose_tone_saved_limit_default":        5,
		"aicompose_tone_saved_limit_premium":        20,
		"stories_stealth_future_period":             1500,
		"stories_stealth_past_period":               300,
		"stories_stealth_cooldown_period":           10800,
	}
	for key, want := range wantNumbers {
		got, ok := decoded[key].(float64)
		if !ok || got != want {
			t.Errorf("appConfig[%q] = %v, want %v", key, decoded[key], want)
		}
	}
	// 未实现功能族的 key 不得下发（诱导客户端进入未实现路径）。
	for _, forbidden := range []string{"stories_sent_weekly_limit_default", "premium_invoice_slug"} {
		if _, ok := decoded[forbidden]; ok {
			t.Errorf("appConfig 不应包含 %q", forbidden)
		}
	}
}

func containsJSONCurrency(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestAppConfigUsesConfiguredPremiumBotUsername(t *testing.T) {
	svc := NewService(nil, nil, WithPremiumBotUsername("storefront_bot"))
	cfg, _, err := svc.GetAppConfig(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("GetAppConfig: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(cfg.JSON, &decoded); err != nil {
		t.Fatalf("decode app config: %v", err)
	}
	if got := decoded["premium_bot_username"]; got != "storefront_bot" {
		t.Fatalf("premium_bot_username = %v, want storefront_bot", got)
	}
	if cfg.Hash == defaultAppConfigHash {
		t.Fatalf("custom premium bot hash = %d, want deployment-specific hash", cfg.Hash)
	}
}

func TestAppConfigOmitsMapboxTokenByDefault(t *testing.T) {
	cfg, notModified, err := (*Service)(nil).GetAppConfig(context.Background(), 0, 0)
	if err != nil || notModified {
		t.Fatalf("GetAppConfig = notModified %v err %v", notModified, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(cfg.JSON, &decoded); err != nil {
		t.Fatalf("app config json invalid: %v", err)
	}
	if _, ok := decoded["tdesktop_config_map"]; ok {
		t.Fatal("tdesktop_config_map present without configured Mapbox token")
	}
}

func TestAppConfigUsesConfiguredMapboxTokenAndHash(t *testing.T) {
	svc := NewService(nil, nil, WithMapboxToken("pk.test-token"))
	cfg, notModified, err := svc.GetAppConfig(context.Background(), 0, 0)
	if err != nil || notModified {
		t.Fatalf("GetAppConfig = notModified %v err %v", notModified, err)
	}
	if cfg.Hash == defaultAppConfigHash {
		t.Fatalf("hash = %d, want token-specific hash", cfg.Hash)
	}
	if _, notModified, err := svc.GetAppConfig(context.Background(), 0, cfg.Hash); err != nil || !notModified {
		t.Fatalf("GetAppConfig(hash) = notModified %v err %v, want notModified", notModified, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(cfg.JSON, &decoded); err != nil {
		t.Fatalf("app config json invalid: %v", err)
	}
	configMap, ok := decoded["tdesktop_config_map"].(map[string]any)
	if !ok {
		t.Fatalf("tdesktop_config_map = %T, want object", decoded["tdesktop_config_map"])
	}
	for _, key := range []string{"maps", "geo", "bmaps", "bgeo"} {
		if got := configMap[key]; got != "pk.test-token" {
			t.Fatalf("tdesktop_config_map[%q] = %v, want token", key, got)
		}
	}
}
