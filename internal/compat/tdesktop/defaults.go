package tdesktop

import (
	"github.com/gotd/td/tg"

	"telesrv/internal/seed/catalog"
)

const (
	appConfigHash     = 14 // app config 内容变更时必须递增，否则缓存端只会收到 notModified。
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
		// premium_purchase_blocked=false：客户端把 star gift「Send a Gift」入口与
		// premiumCanBuy()=!premium_purchase_blocked 耦合，置 true 会同时隐藏送礼入口
		// （详见 app/help/service.go 主配置注释）。这里是无 HelpService 时的最小回退。
		{Key: "premium_purchase_blocked", Value: &tg.JSONBool{Value: false}},
		// stargifts_blocked=false：DrKLO 缺省 stargiftsBlocked=true 会隐藏 star gift 送礼网格。
		{Key: "stargifts_blocked", Value: &tg.JSONBool{Value: false}},
		{Key: "reactions_user_max_premium", Value: &tg.JSONNumber{Value: 3}},
		// DrKLO 频道自定义 reaction 编辑页用它作为可选 reaction 数量上限。
		{Key: "boosts_channel_level_max", Value: &tg.JSONNumber{Value: 100}},
		// dialog_filters_enabled=true：TDesktop 据此(或已有文件夹)才显示 Settings→Folders 入口。
		{Key: "dialog_filters_enabled", Value: &tg.JSONBool{Value: true}},
		{Key: "stories_stealth_future_period", Value: &tg.JSONNumber{Value: 1500}},
		{Key: "stories_stealth_past_period", Value: &tg.JSONNumber{Value: 300}},
		{Key: "stories_stealth_cooldown_period", Value: &tg.JSONNumber{Value: 10800}},
		{Key: "aicompose_tone_examples_num", Value: &tg.JSONNumber{Value: 3}},
		{Key: "aicompose_tone_title_length_max", Value: &tg.JSONNumber{Value: 12}},
		{Key: "aicompose_tone_prompt_length_max", Value: &tg.JSONNumber{Value: 1024}},
		{Key: "aicompose_tone_saved_limit_default", Value: &tg.JSONNumber{Value: 5}},
		{Key: "aicompose_tone_saved_limit_premium", Value: &tg.JSONNumber{Value: 20}},
		// upload_markup_video=true 即官方默认（emoji/sticker 头像由客户端本地渲染 mp4 后随
		// markup 一起上传）。显式下发是为了把曾收到过 false 的客户端持久化配置洗回默认——
		// 客户端对缺失的 key 会保留本地旧值，仅删除 key 无法恢复。
		{Key: "upload_markup_video", Value: &tg.JSONBool{Value: true}},
		// 单 emoji 消息转 InputMediaDice 的白名单；须与 rpc 层 dice 取值表同步。
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
		// TDesktop 位置选点器（WebView+Mapbox GL）与 business 位置设置的地图 token；
		// 缺失时选点器地图空白（瓦片直连 api.mapbox.com，不经服务端）。
		values = append(values, tg.JSONObjectValue{Key: "tdesktop_config_map", Value: &tg.JSONObject{Value: []tg.JSONObjectValue{
			{Key: "maps", Value: &tg.JSONString{Value: mapboxToken}},
			{Key: "geo", Value: &tg.JSONString{Value: mapboxToken}},
			{Key: "bmaps", Value: &tg.JSONString{Value: mapboxToken}},
			{Key: "bgeo", Value: &tg.JSONString{Value: mapboxToken}},
		}}})
	}
	return &tg.JSONObject{Value: values}
}

// fallbackTimezones 是 catalog 未 seed 时的内置最小时区集。
var fallbackTimezones = []tg.Timezone{
	{ID: "Etc/UTC", Name: "UTC", UtcOffset: 0},
	{ID: "America/New_York", Name: "Eastern Time", UtcOffset: -5 * 60 * 60},
	{ID: "America/Chicago", Name: "Central Time", UtcOffset: -6 * 60 * 60},
	{ID: "America/Denver", Name: "Mountain Time", UtcOffset: -7 * 60 * 60},
	{ID: "America/Los_Angeles", Name: "Pacific Time", UtcOffset: -8 * 60 * 60},
	{ID: "Asia/Shanghai", Name: "China Standard Time", UtcOffset: 8 * 60 * 60},
}

// TimezonesList 返回时区目录:优先用 catalog 固化的官方全量(~419 个),未 seed 时回退内置最小集。
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

// SuggestedDialogFilters 返回新建对话文件夹时的「推荐文件夹」模板(官方固定语义:
// Unread/Personal/Unmuted/Groups/Channels/Bots)。客户端据此一键创建对应规则的文件夹;
// 模板 id 仅占位,用户采纳时客户端会以新 id 调 updateDialogFilter 真正创建。
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

// CountriesList 返回登录页国家区号目录:优先用 catalog 固化的官方全量(~235 国),未 seed
// 时回退内置最小集(US/CN)。生产路径通常经 HelpService.GetCountries → defaultCountries
// 走 catalog;此函数是 HelpService 缺省时的兜底。
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
