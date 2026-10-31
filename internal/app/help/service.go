package help

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
	"sync"

	compatandroid "telesrv/internal/compat/android"
	"telesrv/internal/domain"
	"telesrv/internal/seed/catalog"
	"telesrv/internal/store"
)

const tdesktopClient = "tdesktop"

// upload_markup_video=true is the official default (emoji/sticker avatars are rendered
// locally by the client as mp4 and uploaded along with the markup). It is sent explicitly
// to scrub clients that ever received false back to the default — a client keeps its local
// old value for a missing key, so merely deleting the key cannot restore it.
// emojies_send_dice is the whitelist the client uses to turn a "single-emoji message" into
// InputMediaDice; it must stay in sync with the rpc layer's diceValueSides value table
// (⚽ uses the bare code point; the client matches variant forms itself).
// tdesktop_config_map is the Mapbox access token shared by TDesktop's location picker
// (WebView+Mapbox GL) and business location settings: maps/geo=chat attachment picker (map
// tiles/geocoding), bmaps/bgeo=business location. The token must come from runtime config;
// when unset, tdesktop_config_map is not sent, so the public source carries no third-party token.
// premium-related keys (docs/premium-module.md):
//   - premium_purchase_blocked=false must be sent explicitly: DrKLO(ProfileActivity:12059)
//     and TDesktop (window_peer_menu.cpp:1549) couple the "Send a Gift" (star gift) entry
//     with premiumCanBuy()=!premium_purchase_blocked under the same flag — setting true also
//     hides the gifting entry. star gift is implemented (Stars ledger), so it must be false to
//     allow gifting; the side effect is that the premium purchase UI reappears, but premium is
//     auto-granted (0094) and the purchase flow goes through a stub getPaymentForm that fails
//     gracefully, which is acceptable.
//   - stars_purchase_blocked=false must be sent explicitly: DrKLO defaults starsLocked=true,
//     and when the balance is insufficient for gifting and this key is missing it goes through
//     showNoSupportDialog, wrongly popping "cannot purchase stars in your country". This flag
//     only unlocks the client top-up/purchase entry; the actual gift debit is still constrained
//     by the Stars ledger balance and BALANCE_TOO_LOW.
//   - stargifts_blocked=false must be sent explicitly: DrKLO MessagesController:1752 defaults
//     stargiftsBlocked=true (blocked), and GiftSheet:967 hides the entire star gift grid based
//     on it — a missing key leaves the gift picker permanently empty.
//   - reactions_user_max_premium=3 is linked with domain.MaxMessageReactionsPerUserPremium: a
//     premium user can place 3 reactions on the same message, so the server tier must be ≥ that
//     advertised value.
//   - boosts_channel_level_max=100 must be sent explicitly: DrKLO's channel custom-reaction
//     edit page uses it as the local LengthFilter cap on the number of selectable reactions.
//     A missing key keeps the old preference value; in practice, with the old value 4, once the
//     channel has 4 reactions selected, clicking a new emoji is silently blocked locally by the
//     client.
//   - dialog_filters_enabled=true must be sent explicitly: TDesktop settings_main.cpp:394 shows
//     the "Folders" entry in Settings only based on this (or the account already having
//     folders); a missing key → a new account sees no folder management and cannot create
//     folders/adopt getSuggestedDialogFilters templates.
//   - *_limit_default/_premium dual-tier limits align with the official values; about/
//     dialogs_pinned/dialogs_folder_pinned have server-side dual-tier enforcement, while the
//     rest (channels/saved_gifs/stickers_faved/dialog_filters/caption/fileparts, etc.) are
//     either a loose fallback or not enforced server-side, and the client self-limits by its
//     self premium flag. bots_create_limit is deliberately not sent (server-wide 20, see
//     compatibility-matrix todo); chatlist_update_period and the dual-tier chatlist limits
//     align with the minimal real implementation of shared folders. story quota/commercial
//     keys are not sent (the whole feature family is unimplemented; sending them would lure
//     clients into unimplemented paths). stories_stealth_* are time constants used by the
//     client's stealth-mode local UI/optimistic state, consistent with the current bounded
//     stealth update stub.
//   - aicompose_tone_* match the domain/app/ai defaults: TDesktop/DrKLO read these keys
//     directly when creating/previewing a tone, for local input limits and example counts.
//
// WebK directly calls Array.some on fragment_prefixes while rendering user profiles,
// so this compatibility key must always remain an array, even when it is empty.
const tdesktopDefaultAppConfigBase = `{"chat_read_mark_expire_period":604800,"chat_read_mark_size_threshold":50,"pm_read_date_expire_period":604800,"quote_length_max":1024,"telegram_antispam_group_size_min":200,"telegram_antispam_user_id":"5434988373","fragment_prefixes":["888"],"forum_upgrade_participants_min":2,"reactions_default":{"_":"reactionEmoji","emoticon":"👍"},"reactions_uniq_max":11,"reactions_user_max_default":1,"reactions_user_max_premium":3,"reactions_in_chat_max":3,"boosts_channel_level_max":100,"rich_message_posting":"enabled","ephemeral_welcome_messages_max":5,"upload_markup_video":true,"emojies_send_dice":["🎲","🎯","🏀","⚽","⚽️","🎳","🎰"],"premium_purchase_blocked":false,"premium_gift_attach_menu_icon":true,"premium_gift_text_field_icon":true,"gift_text_length_max":128,"stars_purchase_blocked":false,"stargifts_blocked":false,"stargifts_pinned_to_top_limit":6,"giveaway_gifts_purchase_available":true,"giveaway_boosts_per_premium":4,"giveaway_countries_max":10,"giveaway_add_peers_max":10,"giveaway_period_max":604800,"stories_stealth_future_period":1500,"stories_stealth_past_period":300,"stories_stealth_cooldown_period":10800,"quick_replies_limit":100,"quick_reply_messages_limit":20,"business_chat_links_limit":100,"dialog_filters_enabled":true,"chatlist_update_period":3600,"chatlist_invites_limit_default":3,"chatlist_invites_limit_premium":20,"chatlists_joined_limit_default":2,"chatlists_joined_limit_premium":20,"about_length_limit_default":70,"about_length_limit_premium":140,"bot_verification_description_length_limit":128,"caption_length_limit_default":1024,"caption_length_limit_premium":4096,"channels_limit_default":500,"channels_limit_premium":1000,"channels_public_limit_default":10,"channels_public_limit_premium":20,"dialog_filters_limit_default":10,"dialog_filters_limit_premium":20,"dialog_filters_chats_limit_default":100,"dialog_filters_chats_limit_premium":200,"dialogs_pinned_limit_default":5,"dialogs_pinned_limit_premium":10,"dialogs_folder_pinned_limit_default":100,"dialogs_folder_pinned_limit_premium":200,"saved_dialogs_pinned_limit_default":5,"saved_dialogs_pinned_limit_premium":100,"saved_gifs_limit_default":200,"saved_gifs_limit_premium":400,"stickers_faved_limit_default":5,"stickers_faved_limit_premium":10,"recommended_channels_limit_default":10,"recommended_channels_limit_premium":100,"aicompose_tone_examples_num":3,"aicompose_tone_title_length_max":12,"aicompose_tone_prompt_length_max":1024,"aicompose_tone_saved_limit_default":5,"aicompose_tone_saved_limit_premium":20,"upload_max_fileparts_default":4000,"upload_max_fileparts_premium":8000,"settings_display_passkeys":true,"can_edit_factcheck":true,"factcheck_length_limit":1024`
const tdesktopNoForwardsAppConfig = `,"no_forwards_request_expire_period":86400`

// The local ledger has no chain top-up, gasless relay or TON Connect service.
// Advertise only the explicitly configured wallet and the server reference rate.
func tonAppConfigFragment() string {
	return `,"ton_usd_rate":` + strconv.FormatFloat(domain.TonUSDRate, 'f', -1, 64)
}
func walletAppConfigFragment(available bool) string {
	return `,"wallet_available":` + strconv.FormatBool(available) + `,"wallet_gasless_daily_transfers":0`
}

// The Stars resale floor the server enforces on payments.updateStarGiftPrice.
// Clients otherwise fall back to their own built-in default for the key.
func starGiftResaleAppConfigFragment() string {
	return `,"stars_stargift_resale_amount_min":` + strconv.FormatInt(domain.MinStarGiftResaleStars, 10)
}

const defaultAppConfigHash = 37 // must be incremented when the default app config content changes, otherwise caching clients only receive notModified.

// Service provides the client startup config and the country dialing-code catalog.
//
// The app config and country codes are reference catalogs that are "basically unchanged
// after startup": there are no runtime writes (UpsertAppConfig/UpsertCountries are used only
// for seed/migration), so each is loaded and cached in memory once, and every subsequent RPC
// stays in memory without hitting the DB (the login page/startup config is a high-frequency
// handshake path). Operators changing the DB must restart for it to take effect. Other
// catalogs such as timezones/emoji go through internal/seed/catalog (parsed once via
// go:embed) and are already in memory.
type Service struct {
	appConfigs              store.AppConfigStore
	countries               store.CountryStore
	accountFreeze           AccountFreezeProvider
	mapboxToken             string
	premiumBot              string
	walletEnabled           bool
	walletInternalTransfers bool

	appConfigOnce  sync.Once
	appConfigCache domain.AppConfig
	countriesOnce  sync.Once
	countriesCache domain.CountriesList
}

// Option configures the help service's runtime default catalogs.
type Option func(*Service)

// AccountFreezeProvider supplies account-specific read-only state without
// exposing protocol types to the help application service.
type AccountFreezeProvider interface {
	AccountFreeze(ctx context.Context, userID int64) (domain.AccountFreeze, bool, error)
}

func WithAccountFreezeProvider(provider AccountFreezeProvider) Option {
	return func(s *Service) {
		s.accountFreeze = provider
	}
}

// WithMapboxToken sets the Mapbox token shared by the TDesktop appConfig and the map thumbnail proxy.
func WithMapboxToken(token string) Option {
	return func(s *Service) {
		s.mapboxToken = token
	}
}

// WithPremiumBotUsername publishes the local Premium storefront to clients.
func WithPremiumBotUsername(username string) Option {
	return func(s *Service) {
		username = strings.TrimPrefix(strings.TrimSpace(username), "@")
		if domain.ValidBotUsername(username) {
			s.premiumBot = username
		}
	}
}

// WithWalletAvailable publishes whether the in-app wallet came up, which is
// what a client checks before offering the wallet entry point.
func WithWalletAvailable(available bool) Option {
	return func(s *Service) {
		s.walletEnabled = available
	}
}

// WithInternalWalletTransfers publishes the explicitly configured internal extension.
// It never enables chain sending or gasless. Stored operator app configs remain authoritative.
func WithInternalWalletTransfers(enabled bool) Option {
	return func(s *Service) { s.walletInternalTransfers = enabled }
}

// NewService creates the help service.
func NewService(appConfigs store.AppConfigStore, countries store.CountryStore, opts ...Option) *Service {
	s := &Service{
		appConfigs: appConfigs,
		countries:  countries,
		premiumBot: domain.PremiumBotUser().Username,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func defaultAppConfig(mapboxToken string) domain.AppConfig {
	return defaultAppConfigWithPremiumBot(mapboxToken, domain.PremiumBotUser().Username, false)
}

func defaultAppConfigJSON(mapboxToken string) []byte {
	return defaultAppConfigJSONWithPremiumBot(mapboxToken, domain.PremiumBotUser().Username, false)
}

func defaultAppConfigWithPremiumBot(mapboxToken, premiumBotUsername string, walletAvailable bool) domain.AppConfig {
	jsonBytes := defaultAppConfigJSONWithPremiumBot(mapboxToken, premiumBotUsername, walletAvailable)
	return domain.AppConfig{
		Client: tdesktopClient,
		Hash:   defaultAppConfigHashForPremiumBot(mapboxToken, premiumBotUsername, walletAvailable),
		JSON:   jsonBytes,
	}
}

func defaultAppConfigJSONWithPremiumBot(mapboxToken, premiumBotUsername string, walletAvailable bool) []byte {
	if !domain.ValidBotUsername(premiumBotUsername) {
		premiumBotUsername = domain.PremiumBotUser().Username
	}
	username, _ := json.Marshal(premiumBotUsername)
	premiumFragment := `,"premium_bot_username":` + string(username)
	androidInvoiceBilling := `,"premium_playmarket_direct_currency_list":` + compatandroid.DirectInvoiceCurrenciesJSON()
	wallet := tonAppConfigFragment() + walletAppConfigFragment(walletAvailable)
	resale := starGiftResaleAppConfigFragment()
	if mapboxToken == "" {
		return []byte(tdesktopDefaultAppConfigBase + tdesktopNoForwardsAppConfig + premiumFragment + androidInvoiceBilling + wallet + resale + `}`)
	}
	token, err := json.Marshal(mapboxToken)
	if err != nil {
		return []byte(tdesktopDefaultAppConfigBase + tdesktopNoForwardsAppConfig + premiumFragment + androidInvoiceBilling + wallet + resale + `}`)
	}
	tokenJSON := string(token)
	return []byte(tdesktopDefaultAppConfigBase + tdesktopNoForwardsAppConfig + premiumFragment + androidInvoiceBilling + wallet + resale + `,"tdesktop_config_map":{"maps":` + tokenJSON + `,"geo":` + tokenJSON + `,"bmaps":` + tokenJSON + `,"bgeo":` + tokenJSON + `}}`)
}

func defaultAppConfigHashFor(mapboxToken string) int {
	return defaultAppConfigHashForPremiumBot(mapboxToken, domain.PremiumBotUser().Username, false)
}

func defaultAppConfigHashForPremiumBot(mapboxToken, premiumBotUsername string, walletAvailable bool) int {
	if !domain.ValidBotUsername(premiumBotUsername) {
		premiumBotUsername = domain.PremiumBotUser().Username
	}
	hash := defaultAppConfigHash
	if premiumBotUsername != domain.PremiumBotUser().Username {
		hash += 1 + int(crc32.ChecksumIEEE([]byte(premiumBotUsername))&0x1fffffff)
	}
	// The JSON gains wallet_available, so a client holding the old hash has to be
	// told to refetch instead of receiving appConfigNotModified.
	if walletAvailable {
		hash += 3
	}
	if mapboxToken == "" {
		return hash
	}
	return hash + 1 + int(crc32.ChecksumIEEE([]byte(mapboxToken))&0x1fffffff)
}

// GetAppConfig returns the cached global app config plus an authenticated,
// per-account freeze overlay. Only active freezes add account fields; an
// inactive account receives the field-free base config. The overlay owns its
// own deterministic hash so a FROZEN_METHOD_INVALID-triggered refresh and a
// later unfreeze can never be answered notModified against the other state.
func (s *Service) GetAppConfig(ctx context.Context, userID int64, hash int) (domain.AppConfig, bool, error) {
	cfg := s.loadAppConfig(ctx)
	var err error
	cfg, err = s.accountAppConfig(ctx, userID, cfg)
	if err != nil {
		return domain.AppConfig{}, false, err
	}
	return cfg, hash != 0 && hash == cfg.Hash, nil
}

func (s *Service) accountAppConfig(ctx context.Context, userID int64, base domain.AppConfig) (domain.AppConfig, error) {
	values := make(map[string]json.RawMessage)
	if err := json.Unmarshal(base.JSON, &values); err != nil {
		return domain.AppConfig{}, fmt.Errorf("decode base app config: %w", err)
	}
	changed := false
	for _, key := range []string{"freeze_since_date", "freeze_until_date", "freeze_appeal_url"} {
		if _, exists := values[key]; exists {
			delete(values, key)
			changed = true
		}
	}
	if userID > 0 {
		if s != nil && s.accountFreeze != nil {
			freeze, found, err := s.accountFreeze.AccountFreeze(ctx, userID)
			if err != nil {
				return domain.AppConfig{}, fmt.Errorf("load account freeze: %w", err)
			}
			if found && freeze.Frozen {
				values["freeze_since_date"] = json.RawMessage(strconv.FormatInt(freeze.Since.Unix(), 10))
				values["freeze_until_date"] = json.RawMessage(strconv.FormatInt(freeze.Until.Unix(), 10))
				appeal, _ := json.Marshal(freeze.AppealURL)
				values["freeze_appeal_url"] = appeal
				changed = true
			}
		}
	}
	if !changed {
		return base, nil
	}
	body, err := json.Marshal(values)
	if err != nil {
		return domain.AppConfig{}, fmt.Errorf("encode account app config: %w", err)
	}
	hashInput := append([]byte(strconv.Itoa(base.Hash)+"\x00"), body...)
	overlayHash := int(crc32.ChecksumIEEE(hashInput) & 0x7fffffff)
	if overlayHash == 0 || overlayHash == base.Hash {
		overlayHash = base.Hash + 1
	}
	return domain.AppConfig{Client: base.Client, Hash: overlayHash, JSON: body}, nil
}

func (s *Service) loadAppConfig(ctx context.Context) domain.AppConfig {
	if s == nil {
		return defaultAppConfig("")
	}
	defaultCfg := defaultAppConfigWithPremiumBot(s.mapboxToken, s.premiumBot, s.walletEnabled)
	if s.walletEnabled && s.walletInternalTransfers {
		var values map[string]json.RawMessage
		if json.Unmarshal(defaultCfg.JSON, &values) == nil {
			values["wallet_internal_gram"] = json.RawMessage("true")
			values["wallet_internal_gram_transfer_version"] = json.RawMessage("1")
			values["wallet_transfer_requires_password"] = json.RawMessage("true")
			values["wallet_transfer_min_nanos"] = json.RawMessage("1")
			defaultCfg.JSON, _ = json.Marshal(values)
			defaultCfg.Hash = int(crc32.ChecksumIEEE(defaultCfg.JSON) & 0x7fffffff)
		}
	}
	s.appConfigOnce.Do(func() {
		if s.appConfigs == nil {
			s.appConfigCache = defaultCfg
			return
		}
		cfg, found, err := s.appConfigs.GetAppConfig(ctx, tdesktopClient)
		// The DB row may be overridden by operators, but a hash behind the code default is
		// treated as stale (leftover from an old seed) and the default wins — otherwise newly
		// added config keys are forever shadowed by the old row (this once caused
		// emojies_send_dice not to be sent). A read failure also falls back to the default
		// (the default is always valid), so a single transient DB blip never poisons the cache
		// for the whole process lifetime.
		if err != nil || !found || cfg.Hash < defaultAppConfigHash {
			cfg = defaultCfg
		}
		s.appConfigCache = cfg
	})
	return s.appConfigCache
}

// GetCountries returns the country dialing-code catalog, or notModified on a hash hit. It is loaded once and cached on first call.
func (s *Service) GetCountries(ctx context.Context, langCode string, hash int) (domain.CountriesList, bool, error) {
	list := s.loadCountries(ctx)
	return list, hash != 0 && hash == list.Hash, nil
}

func (s *Service) loadCountries(ctx context.Context) domain.CountriesList {
	if s == nil || s.countries == nil {
		return defaultCountries()
	}
	s.countriesOnce.Do(func() {
		list, err := s.countries.ListCountries(ctx, "")
		if err != nil || len(list.Countries) == 0 {
			list = defaultCountries()
		}
		s.countriesCache = list
	})
	return s.countriesCache
}

// defaultCountries returns the built-in country dialing-code catalog: it prefers the
// official full set (~235 countries) baked into catalog, falling back to a minimal set
// (US/CN) when unseeded. The countries table is usually empty, so this is the catalog
// actually served in production.
func defaultCountries() domain.CountriesList {
	if c := catalog.Countries(); len(c.Countries) > 0 {
		return c
	}
	return domain.CountriesList{
		Hash: 1,
		Countries: []domain.Country{
			{
				ISO2:        "US",
				DefaultName: "United States",
				CountryCodes: []domain.CountryCode{
					{CountryCode: "1", Prefixes: []string{""}, Patterns: []string{"XXX XXX XXXX"}},
				},
			},
			{
				ISO2:        "CN",
				DefaultName: "China",
				CountryCodes: []domain.CountryCode{
					{CountryCode: "86", Prefixes: []string{""}, Patterns: []string{"XXX XXXX XXXX"}},
				},
			},
		},
	}
}
