package help

import (
	"context"
	"encoding/json"
	"hash/crc32"
	"sync"

	"telesrv/internal/domain"
	"telesrv/internal/seed/catalog"
	"telesrv/internal/store"
)

const tdesktopClient = "tdesktop"

// upload_markup_video=true 即官方默认（emoji/sticker 头像由客户端本地渲染 mp4 后随
// markup 一起上传）。显式下发是为了把曾收到过 false 的客户端持久化配置洗回默认——
// 客户端对缺失的 key 会保留本地旧值，仅删除 key 无法恢复。
// emojies_send_dice 是客户端把「单 emoji 消息」转成 InputMediaDice 的白名单；
// 必须与 rpc 层 diceValueSides 的取值表保持同步（⚽ 用裸码点，客户端会自行匹配变体形态）。
// tdesktop_config_map 是 TDesktop 位置选点器（WebView+Mapbox GL）与 business 位置设置的
// Mapbox access token：maps/geo=聊天附件选点（地图瓦片/地理编码），bmaps/bgeo=business 位置。
// 该 token 必须来自运行期配置；未配置时不下发 tdesktop_config_map，避免公共源码携带第三方 token。
// premium 相关 key（docs/premium-module.md）：
//   - premium_purchase_blocked=false 必须显式下发：DrKLO(ProfileActivity:12059)与 TDesktop
//     (window_peer_menu.cpp:1549)把「Send a Gift」(star gift)入口与 premiumCanBuy()=
//     !premium_purchase_blocked 耦合在同一 flag——置 true 会同时隐藏送礼入口。star gift
//     已实现(Stars 账本)，故必须 false 才能送礼；副作用是 premium 购买 UI 重现，但 premium
//     已自动授予(0094)、购买流走 stub getPaymentForm 优雅报错，可接受。
//   - stargifts_blocked=false 必须显式下发：DrKLO MessagesController:1752 缺省 stargiftsBlocked=
//     true(屏蔽)，GiftSheet:967 据此隐藏整个 star gift 送礼网格——缺 key 则送礼选择器恒空。
//   - reactions_user_max_premium=3 与服务端 domain.MaxMessageReactionsPerUserPremium
//     联动：premium 用户可在同一消息放 3 个 reaction，服务端档位必须 ≥ 该宣告值。
//   - boosts_channel_level_max=100 必须显式下发：DrKLO 的频道自定义 reaction 编辑页
//     用它作为可选 reaction 个数的本地 LengthFilter 上限。缺 key 会保留旧偏好值；实测
//     旧值为 4 时，频道已选 4 个 reaction 后继续点新 emoji 会被客户端本地静默挡掉。
//   - dialog_filters_enabled=true 必须显式下发：TDesktop settings_main.cpp:394 据此(或账号
//     已有文件夹)才在 Settings 显示「Folders」入口，缺 key → 新账号看不到文件夹管理、无法
//     建文件夹/采纳 getSuggestedDialogFilters 模板。
//   - *_limit_default/_premium 双档限额对齐官方值；其中 about/dialogs_pinned/
//     dialogs_folder_pinned 有服务端 enforcement 双档，其余（channels/saved_gifs/
//     stickers_faved/dialog_filters/caption/fileparts 等）服务端为宽兜底或未 enforce，
//     客户端按 self premium flag 自限。bots_create_limit 故意不下发（服务端统一 20，
//     见 compatibility-matrix todo）；chatlists 和 story 配额/商业化 key 不下发
//     （功能全族未实现，下发会诱导客户端走进未实现路径）。stories_stealth_* 是客户端
//     隐身模式本地 UI/乐观状态用的时间常量，与当前 bounded stealth update stub 保持一致。
//   - aicompose_tone_* 与 domain/app/ai 默认值一致：TDesktop/DrKLO 创建/预览 tone 时
//     直接读取这些 key 做本地输入限制和示例数量。
const tdesktopDefaultAppConfigBase = `{"chat_read_mark_expire_period":604800,"chat_read_mark_size_threshold":50,"pm_read_date_expire_period":604800,"quote_length_max":1024,"telegram_antispam_group_size_min":200,"telegram_antispam_user_id":"5434988373","forum_upgrade_participants_min":2,"reactions_default":{"_":"reactionEmoji","emoticon":"👍"},"reactions_uniq_max":11,"reactions_user_max_default":1,"reactions_user_max_premium":3,"reactions_in_chat_max":3,"boosts_channel_level_max":100,"upload_markup_video":true,"emojies_send_dice":["🎲","🎯","🏀","⚽","⚽️","🎳","🎰"],"premium_purchase_blocked":false,"stargifts_blocked":false,"stories_stealth_future_period":1500,"stories_stealth_past_period":300,"stories_stealth_cooldown_period":10800,"quick_replies_limit":100,"quick_reply_messages_limit":20,"business_chat_links_limit":100,"dialog_filters_enabled":true,"about_length_limit_default":70,"about_length_limit_premium":140,"caption_length_limit_default":1024,"caption_length_limit_premium":4096,"channels_limit_default":500,"channels_limit_premium":1000,"channels_public_limit_default":10,"channels_public_limit_premium":20,"dialog_filters_limit_default":10,"dialog_filters_limit_premium":20,"dialog_filters_chats_limit_default":100,"dialog_filters_chats_limit_premium":200,"dialogs_pinned_limit_default":5,"dialogs_pinned_limit_premium":10,"dialogs_folder_pinned_limit_default":100,"dialogs_folder_pinned_limit_premium":200,"saved_dialogs_pinned_limit_default":5,"saved_dialogs_pinned_limit_premium":100,"saved_gifs_limit_default":200,"saved_gifs_limit_premium":400,"stickers_faved_limit_default":5,"stickers_faved_limit_premium":10,"recommended_channels_limit_default":10,"recommended_channels_limit_premium":100,"aicompose_tone_examples_num":3,"aicompose_tone_title_length_max":12,"aicompose_tone_prompt_length_max":1024,"aicompose_tone_saved_limit_default":5,"aicompose_tone_saved_limit_premium":20,"upload_max_fileparts_default":4000,"upload_max_fileparts_premium":8000`

const defaultAppConfigHash = 19 // 默认 app config 内容变更时必须递增，否则缓存端只会收到 notModified。

// Service 提供客户端启动配置与国家区号目录。
//
// app config 与国家区号属「启动后基本不变」的参考目录:运行期无写入(UpsertAppConfig/
// UpsertCountries 仅 seed/迁移用),故各加载一次缓存进内存,之后所有 RPC 走内存、不再查库
// (登录页/启动配置是高频握手路径)。运维改库需重启生效。timezones/emoji 等其余目录走
// internal/seed/catalog(go:embed 一次解析),本就在内存。
type Service struct {
	appConfigs  store.AppConfigStore
	countries   store.CountryStore
	mapboxToken string

	appConfigOnce  sync.Once
	appConfigCache domain.AppConfig
	countriesOnce  sync.Once
	countriesCache domain.CountriesList
}

// Option 配置 help 服务运行期默认目录。
type Option func(*Service)

// WithMapboxToken 设置 TDesktop appConfig 与地图缩略图代理共用的 Mapbox token。
func WithMapboxToken(token string) Option {
	return func(s *Service) {
		s.mapboxToken = token
	}
}

// NewService 创建 help 服务。
func NewService(appConfigs store.AppConfigStore, countries store.CountryStore, opts ...Option) *Service {
	s := &Service{appConfigs: appConfigs, countries: countries}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func defaultAppConfig(mapboxToken string) domain.AppConfig {
	jsonBytes := defaultAppConfigJSON(mapboxToken)
	return domain.AppConfig{Client: tdesktopClient, Hash: defaultAppConfigHashFor(mapboxToken), JSON: jsonBytes}
}

func defaultAppConfigJSON(mapboxToken string) []byte {
	if mapboxToken == "" {
		return []byte(tdesktopDefaultAppConfigBase + `}`)
	}
	token, err := json.Marshal(mapboxToken)
	if err != nil {
		return []byte(tdesktopDefaultAppConfigBase + `}`)
	}
	tokenJSON := string(token)
	return []byte(tdesktopDefaultAppConfigBase + `,"tdesktop_config_map":{"maps":` + tokenJSON + `,"geo":` + tokenJSON + `,"bmaps":` + tokenJSON + `,"bgeo":` + tokenJSON + `}}`)
}

func defaultAppConfigHashFor(mapboxToken string) int {
	if mapboxToken == "" {
		return defaultAppConfigHash
	}
	return defaultAppConfigHash + 1 + int(crc32.ChecksumIEEE([]byte(mapboxToken))&0x3fffffff)
}

// GetAppConfig 返回 TDesktop app config，hash 命中时返回 notModified。首次调用加载一次后缓存。
func (s *Service) GetAppConfig(ctx context.Context, hash int) (domain.AppConfig, bool, error) {
	cfg := s.loadAppConfig(ctx)
	return cfg, hash != 0 && hash == cfg.Hash, nil
}

func (s *Service) loadAppConfig(ctx context.Context) domain.AppConfig {
	if s == nil {
		return defaultAppConfig("")
	}
	defaultCfg := defaultAppConfig(s.mapboxToken)
	s.appConfigOnce.Do(func() {
		if s.appConfigs == nil {
			s.appConfigCache = defaultCfg
			return
		}
		cfg, found, err := s.appConfigs.GetAppConfig(ctx, tdesktopClient)
		// DB 行允许运维覆盖，但 hash 落后于代码默认值时视为陈旧（历史 seed 残留），
		// 以默认值为准——否则新增配置 key 永远被旧行遮蔽（曾导致 emojies_send_dice 未下发）。
		// 读失败也回退默认值(默认值恒有效),不让一次瞬时 DB 抖动污染整个进程生命周期的缓存。
		if err != nil || !found || cfg.Hash < defaultAppConfigHash {
			cfg = defaultCfg
		}
		s.appConfigCache = cfg
	})
	return s.appConfigCache
}

// GetCountries 返回国家区号目录，hash 命中时返回 notModified。首次调用加载一次后缓存。
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

// defaultCountries 返回内置国家区号目录:优先用 catalog 固化的官方全量(~235 国),
// 未 seed 时回退最小集(US/CN)。countries 表通常为空,故这就是生产实际下发的目录。
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
