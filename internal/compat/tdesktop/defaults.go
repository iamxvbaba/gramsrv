package tdesktop

import (
	"github.com/iamxvbaba/td/tg"

	compatandroid "telesrv/internal/compat/android"
	"telesrv/internal/domain"
	"telesrv/internal/seed/catalog"
)

const (
	appConfigHash     = 22 // must be incremented when the app config content changes, or caching clients only get notModified.
	countriesListHash = 1
	timezonesListHash = 1
)

// AppConfig returns the fallback TDesktop startup app config used when HelpService is absent.
func AppConfig(hash int) tg.HelpAppConfigClass {
	if hash == appConfigHash {
		return &tg.HelpAppConfigNotModified{}
	}
	return &tg.HelpAppConfig{
		Hash:   appConfigHash,
		Config: readMarkAppConfig(""),
	}
}

func readMarkAppConfig(mapboxToken string) *tg.JSONObject {
	values := []tg.JSONObjectValue{
		{Key: "chat_read_mark_size_threshold", Value: &tg.JSONNumber{Value: 50}},
		{Key: "chat_read_mark_expire_period", Value: &tg.JSONNumber{Value: 604800}},
		{Key: "pm_read_date_expire_period", Value: &tg.JSONNumber{Value: 604800}},
		{Key: "quote_length_max", Value: &tg.JSONNumber{Value: 1024}},
		{Key: "telegram_antispam_group_size_min", Value: &tg.JSONNumber{Value: 200}},
		{Key: "telegram_antispam_user_id", Value: &tg.JSONString{Value: "5434988373"}},
		{Key: "fragment_prefixes", Value: &tg.JSONArray{Value: []tg.JSONValueClass{
			&tg.JSONString{Value: "888"},
		}}},
		// premium_purchase_blocked=false: the client couples the star gift "Send a Gift"
		// entry with premiumCanBuy()=!premium_purchase_blocked, so setting it true would
		// also hide the gifting entry (see the main config comments in app/help/service.go).
		// This is the minimal fallback for when HelpService is absent.
		{Key: "premium_purchase_blocked", Value: &tg.JSONBool{Value: false}},
		{Key: "stars_purchase_blocked", Value: &tg.JSONBool{Value: false}},
		// stargifts_blocked=false: DrKLO's default stargiftsBlocked=true would hide the star gift grid.
		{Key: "stargifts_blocked", Value: &tg.JSONBool{Value: false}},
		// The Stars resale floor enforced on payments.updateStarGiftPrice.
		{Key: "stars_stargift_resale_amount_min", Value: &tg.JSONNumber{Value: float64(domain.MinStarGiftResaleStars)}},
		{Key: "giveaway_gifts_purchase_available", Value: &tg.JSONBool{Value: true}},
		{Key: "giveaway_boosts_per_premium", Value: &tg.JSONNumber{Value: 4}},
		{Key: "giveaway_countries_max", Value: &tg.JSONNumber{Value: 10}},
		{Key: "giveaway_add_peers_max", Value: &tg.JSONNumber{Value: 10}},
		{Key: "giveaway_period_max", Value: &tg.JSONNumber{Value: 604800}},
		{Key: "premium_playmarket_direct_currency_list", Value: tgStringArray(compatandroid.DirectInvoiceCurrencies())},
		{Key: "reactions_user_max_premium", Value: &tg.JSONNumber{Value: 3}},
		// DrKLO's channel custom reaction editor uses this as the maximum number of selectable reactions.
		{Key: "boosts_channel_level_max", Value: &tg.JSONNumber{Value: 100}},
		// TDesktop rich-text editor entry: the official default is disabled, and only an explicit enabled shows/allows entering the editor.
		{Key: "rich_message_posting", Value: &tg.JSONString{Value: "enabled"}},
		{Key: "ephemeral_welcome_messages_max", Value: &tg.JSONNumber{Value: float64(domain.MaxWelcomeMessagesPerPeer)}},
		// dialog_filters_enabled=true: TDesktop shows the Settings→Folders entry based on this (or existing folders).
		{Key: "dialog_filters_enabled", Value: &tg.JSONBool{Value: true}},
		// settings_display_passkeys=true: per core.telegram.org/api/passkeys, the client
		// only shows the Settings→Privacy→Passkeys registration entry when this key is true;
		// with the key absent no one can register.
		{Key: "settings_display_passkeys", Value: &tg.JSONBool{Value: true}},
		// can_edit_factcheck=true: TDesktop/WebK show the "Add Fact-Check" entry based on
		// this; with the key absent canEdit() is always false, leaving the server-implemented
		// fact-check methods without a UI entry.
		{Key: "can_edit_factcheck", Value: &tg.JSONBool{Value: true}},
		{Key: "factcheck_length_limit", Value: &tg.JSONNumber{Value: float64(domain.MaxFactCheckTextLength)}},
		{Key: "chatlist_update_period", Value: &tg.JSONNumber{Value: 3600}},
		{Key: "chatlist_invites_limit_default", Value: &tg.JSONNumber{Value: 3}},
		{Key: "chatlist_invites_limit_premium", Value: &tg.JSONNumber{Value: 20}},
		{Key: "chatlists_joined_limit_default", Value: &tg.JSONNumber{Value: 2}},
		{Key: "chatlists_joined_limit_premium", Value: &tg.JSONNumber{Value: 20}},
		{Key: "stories_stealth_future_period", Value: &tg.JSONNumber{Value: 1500}},
		{Key: "stories_stealth_past_period", Value: &tg.JSONNumber{Value: 300}},
		{Key: "stories_stealth_cooldown_period", Value: &tg.JSONNumber{Value: 10800}},
		{Key: "aicompose_tone_examples_num", Value: &tg.JSONNumber{Value: 3}},
		{Key: "aicompose_tone_title_length_max", Value: &tg.JSONNumber{Value: 12}},
		{Key: "aicompose_tone_prompt_length_max", Value: &tg.JSONNumber{Value: 1024}},
		{Key: "aicompose_tone_saved_limit_default", Value: &tg.JSONNumber{Value: 5}},
		{Key: "aicompose_tone_saved_limit_premium", Value: &tg.JSONNumber{Value: 20}},
		// upload_markup_video=true is the official default (emoji/sticker avatars are
		// rendered locally by the client as mp4 and uploaded together with the markup).
		// Sending it explicitly is meant to wash the persisted config of clients that once
		// received false back to the default — a client keeps the old local value for a
		// missing key, so merely removing the key cannot restore it.
		{Key: "upload_markup_video", Value: &tg.JSONBool{Value: true}},
		// Whitelist of single-emoji messages converted to InputMediaDice; must stay in sync with the rpc layer's dice value table.
		{Key: "emojies_send_dice", Value: &tg.JSONArray{Value: []tg.JSONValueClass{
			&tg.JSONString{Value: "\U0001F3B2"}, // 🎲
			&tg.JSONString{Value: "\U0001F3AF"}, // 🎯
			&tg.JSONString{Value: "\U0001F3C0"}, // 🏀
			&tg.JSONString{Value: "⚽"},
			&tg.JSONString{Value: "⚽️"},
			&tg.JSONString{Value: "\U0001F3B3"}, // 🎳
			&tg.JSONString{Value: "\U0001F3B0"}, // 🎰
		}}},
	}
	if mapboxToken != "" {
		// Map token for the TDesktop location picker (WebView+Mapbox GL) and the business
		// location settings; when absent the picker's map is blank (tiles connect directly
		// to api.mapbox.com, not through the server).
		values = append(values, tg.JSONObjectValue{Key: "tdesktop_config_map", Value: &tg.JSONObject{Value: []tg.JSONObjectValue{
			{Key: "maps", Value: &tg.JSONString{Value: mapboxToken}},
			{Key: "geo", Value: &tg.JSONString{Value: mapboxToken}},
			{Key: "bmaps", Value: &tg.JSONString{Value: mapboxToken}},
			{Key: "bgeo", Value: &tg.JSONString{Value: mapboxToken}},
		}}})
	}
	return &tg.JSONObject{Value: values}
}

func tgStringArray(values []string) *tg.JSONArray {
	result := &tg.JSONArray{Value: make([]tg.JSONValueClass, 0, len(values))}
	for _, value := range values {
		result.Value = append(result.Value, &tg.JSONString{Value: value})
	}
	return result
}

// fallbackTimezones is the built-in minimal timezone set used when the catalog is not seeded.
var fallbackTimezones = []tg.Timezone{
	{ID: "Etc/UTC", Name: "UTC", UtcOffset: 0},
	{ID: "America/New_York", Name: "Eastern Time", UtcOffset: -5 * 60 * 60},
	{ID: "America/Chicago", Name: "Central Time", UtcOffset: -6 * 60 * 60},
	{ID: "America/Denver", Name: "Mountain Time", UtcOffset: -7 * 60 * 60},
	{ID: "America/Los_Angeles", Name: "Pacific Time", UtcOffset: -8 * 60 * 60},
	{ID: "Asia/Shanghai", Name: "China Standard Time", UtcOffset: 8 * 60 * 60},
}

// TimezonesList returns the timezone catalog: it prefers the full official set fixed in the catalog (~419), falling back to the built-in minimal set when not seeded.
func TimezonesList(hash int) tg.HelpTimezonesListClass {
	seeded, h := catalog.Timezones()
	src := seeded
	if len(src) == 0 {
		src, h = nil, timezonesListHash
	}
	if hash == h {
		return &tg.HelpTimezonesListNotModified{}
	}
	items := fallbackTimezones
	if len(src) > 0 {
		items = make([]tg.Timezone, 0, len(src))
		for _, t := range src {
			items = append(items, tg.Timezone{ID: t.ID, Name: t.Name, UtcOffset: t.UTCOffset})
		}
	}
	return &tg.HelpTimezonesList{Hash: h, Timezones: items}
}

// SuggestedDialogFilters returns the "suggested folders" templates for creating a new
// dialog folder (fixed official semantics: Unread/Personal/Unmuted/Groups/Channels/Bots).
// The client uses them to create folders with the corresponding rules in one click; the
// template ids are placeholders only, and on adoption the client creates the folder for
// real by calling updateDialogFilter with new ids.
func SuggestedDialogFilters() []tg.DialogFilterSuggested {
	mk := func(id int, title, desc string, set func(*tg.DialogFilter)) tg.DialogFilterSuggested {
		df := &tg.DialogFilter{
			ID:           id,
			Title:        tg.TextWithEntities{Text: title, Entities: []tg.MessageEntityClass{}},
			PinnedPeers:  []tg.InputPeerClass{},
			IncludePeers: []tg.InputPeerClass{},
			ExcludePeers: []tg.InputPeerClass{},
		}
		set(df)
		return tg.DialogFilterSuggested{Filter: df, Description: desc}
	}
	allTypes := func(d *tg.DialogFilter) {
		d.Contacts, d.NonContacts, d.Groups, d.Broadcasts, d.Bots = true, true, true, true, true
	}
	return []tg.DialogFilterSuggested{
		mk(2, "Unread", "New messages", func(d *tg.DialogFilter) { allTypes(d); d.ExcludeRead = true }),
		mk(3, "Personal", "Personal chats", func(d *tg.DialogFilter) { d.Contacts, d.NonContacts = true, true }),
		mk(4, "Unmuted", "Unmuted chats", func(d *tg.DialogFilter) { allTypes(d); d.ExcludeMuted = true }),
		mk(5, "Groups", "Group chats", func(d *tg.DialogFilter) { d.Groups = true }),
		mk(6, "Channels", "Channels only", func(d *tg.DialogFilter) { d.Broadcasts = true }),
		mk(7, "Bots", "Bot chats", func(d *tg.DialogFilter) { d.Bots = true }),
	}
}

// CountriesList returns the login-page country-code catalog: it prefers the full official
// set fixed in the catalog (~235 countries), falling back to the built-in minimal set
// (US/CN) when not seeded. The production path usually goes through
// HelpService.GetCountries → defaultCountries to the catalog; this function is the
// fallback for when HelpService is absent.
func CountriesList(hash int) tg.HelpCountriesListClass {
	list := catalog.Countries()
	if len(list.Countries) == 0 {
		if hash == countriesListHash {
			return &tg.HelpCountriesListNotModified{}
		}
		return &tg.HelpCountriesList{
			Hash: countriesListHash,
			Countries: []tg.HelpCountry{
				{ISO2: "US", DefaultName: "United States", CountryCodes: []tg.HelpCountryCode{{CountryCode: "1", Prefixes: []string{""}, Patterns: []string{"XXX XXX XXXX"}}}},
				{ISO2: "CN", DefaultName: "China", CountryCodes: []tg.HelpCountryCode{{CountryCode: "86", Prefixes: []string{""}, Patterns: []string{"XXX XXXX XXXX"}}}},
			},
		}
	}
	if hash == list.Hash {
		return &tg.HelpCountriesListNotModified{}
	}
	out := &tg.HelpCountriesList{Hash: list.Hash, Countries: make([]tg.HelpCountry, 0, len(list.Countries))}
	for _, c := range list.Countries {
		item := tg.HelpCountry{
			Hidden:       c.Hidden,
			ISO2:         c.ISO2,
			DefaultName:  c.DefaultName,
			Name:         c.Name,
			CountryCodes: make([]tg.HelpCountryCode, 0, len(c.CountryCodes)),
		}
		for _, cc := range c.CountryCodes {
			item.CountryCodes = append(item.CountryCodes, tg.HelpCountryCode{CountryCode: cc.CountryCode, Prefixes: cc.Prefixes, Patterns: cc.Patterns})
		}
		out.Countries = append(out.Countries, item)
	}
	return out
}
