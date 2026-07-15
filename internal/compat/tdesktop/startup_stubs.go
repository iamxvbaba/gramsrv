package tdesktop

import (
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/seed/appearance"
	"telesrv/internal/seed/catalog"
)

// NotifySettings returns default per-peer notification settings for empty first-phase accounts.
func NotifySettings() *tg.PeerNotifySettings {
	settings := &tg.PeerNotifySettings{}
	settings.SetShowPreviews(true)
	settings.SetSilent(false)
	settings.SetMuteUntil(0)
	settings.SetIosSound(&tg.NotificationSoundDefault{})
	settings.SetAndroidSound(&tg.NotificationSoundDefault{})
	settings.SetOtherSound(&tg.NotificationSoundDefault{})
	settings.SetStoriesMuted(false)
	settings.SetStoriesHideSender(false)
	settings.SetStoriesIosSound(&tg.NotificationSoundDefault{})
	settings.SetStoriesAndroidSound(&tg.NotificationSoundDefault{})
	settings.SetStoriesOtherSound(&tg.NotificationSoundDefault{})
	return settings
}

func PrivacyRules(key tg.InputPrivacyKeyClass) *tg.AccountPrivacyRules {
	var rule tg.PrivacyRuleClass = &tg.PrivacyValueAllowAll{}
	switch key.(type) {
	case *tg.InputPrivacyKeyPhoneNumber:
		rule = &tg.PrivacyValueDisallowAll{}
	case *tg.InputPrivacyKeyBirthday:
		rule = &tg.PrivacyValueAllowContacts{}
	}
	return &tg.AccountPrivacyRules{
		Rules: []tg.PrivacyRuleClass{rule},
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{},
	}
}

func Authorizations() *tg.AccountAuthorizations {
	return &tg.AccountAuthorizations{Authorizations: []tg.Authorization{}}
}

func WebAuthorizations() *tg.AccountWebAuthorizations {
	return &tg.AccountWebAuthorizations{Authorizations: []tg.WebAuthorization{}, Users: []tg.UserClass{}}
}

func Passkeys() *tg.AccountPasskeys {
	return &tg.AccountPasskeys{Passkeys: []tg.Passkey{}}
}

func GlobalPrivacySettings() *tg.GlobalPrivacySettings {
	return &tg.GlobalPrivacySettings{}
}

const chatThemesHash int64 = 2026062501
const uniqueGiftChatThemesHash int64 = 2026061201
const wallPapersHash int64 = 2026062502
const peerColorsHash = 2026061202
const peerProfileColorsHash = 2026062801

// IsChatThemeEmoticon reports whether token is a known official chat theme emoticon.
func IsChatThemeEmoticon(token string) bool {
	for _, ct := range appearance.Default().ChatThemes {
		if ct.Emoticon == token {
			return true
		}
	}
	return false
}

func ChatThemes(hash int64) tg.AccountThemesClass {
	if hash == chatThemesHash {
		return &tg.AccountThemesNotModified{}
	}
	return &tg.AccountThemes{
		Hash:   chatThemesHash,
		Themes: chatThemeList(),
	}
}

// chatThemeList 用官方外观 catalog(appearancefetch 导出)的聊天主题。
func chatThemeList() []tg.Theme {
	return catalogChatThemes(appearance.Default().ChatThemes)
}

// catalogChatThemes 把官方聊天主题转成 tg.Theme。官方每个主题只带 classic+tinted 两组
// settings,而 DrKLO 选择器丢弃 base-theme 设置 <4 的主题
// (MediaDataController.generateEmojiPreviewThemes),故扩展到 telesrv 宣告的 5 个 base:
// 浅色 base(classic/day/arctic)复用官方浅色设置,深色 base(tinted/night)复用官方深色设置。
func catalogChatThemes(cts []appearance.ChatTheme) []tg.Theme {
	out := make([]tg.Theme, 0, len(cts))
	for i, ct := range cts {
		var light, dark *appearance.ThemeSettings
		for j := range ct.Settings {
			s := &ct.Settings[j]
			if s.BaseTheme == "tinted" || s.BaseTheme == "night" {
				if dark == nil {
					dark = s
				}
			} else if light == nil {
				light = s
			}
		}
		if light == nil {
			light = dark
		}
		if dark == nil {
			dark = light
		}
		if light == nil {
			continue
		}
		theme := tg.Theme{ID: ct.ID, AccessHash: ct.AccessHash, Slug: ct.Slug, Title: ct.Title}
		theme.SetForChat(true)
		if ct.Emoticon != "" {
			theme.SetEmoticon(ct.Emoticon)
		}
		theme.SetInstallsCount(1)
		settings := make([]tg.ThemeSettings, 0, len(chatThemeBaseThemes))
		for _, b := range chatThemeBaseThemes {
			src := light
			if b.dark {
				src = dark
			}
			settings = append(settings, catalogThemeSettings(*src, b.make()))
		}
		theme.SetSettings(settings)
		if i == 0 || ct.Default {
			theme.SetDefault(true)
		}
		out = append(out, theme)
	}
	return out
}

func catalogThemeSettings(s appearance.ThemeSettings, base tg.BaseThemeClass) tg.ThemeSettings {
	ts := tg.ThemeSettings{BaseTheme: base, AccentColor: s.AccentColor}
	if s.OutboxAccentColor != 0 {
		ts.SetOutboxAccentColor(s.OutboxAccentColor)
	}
	if len(s.MessageColors) > 0 {
		ts.SetMessageColors(append([]int(nil), s.MessageColors...))
	}
	if s.Wallpaper.ID != 0 || s.Wallpaper.Document.ID != 0 {
		ts.SetWallpaper(DefaultWallPaper(s.Wallpaper))
	}
	return ts
}

// DefaultThemes serves the built-in chat theme catalog for account.getThemes.
//
// DrKLO's "Color theme" preview strip on the Chat Settings page
// (DefaultThemesPreviewCell) is populated exclusively from account.getThemes
// results flagged is_default=true — it does not consult account.getChatThemes.
// A fresh client has no cached emoji themes and therefore sends getThemes with
// hash 0; returning themesNotModified to that request leaves defaultEmojiThemes
// empty, so the strip stays stuck on its FlickerLoadingView shimmer forever.
// We return the same catalog as ChatThemes with every entry marked default so
// the strip resolves into real preview cards.
func DefaultThemes(hash int64) tg.AccountThemesClass {
	if hash == chatThemesHash {
		return &tg.AccountThemesNotModified{}
	}
	return &tg.AccountThemes{
		Hash:   chatThemesHash,
		Themes: DefaultThemeList(),
	}
}

// DefaultThemeList 返回内置默认主题切片(全标 is_default=true)。account.getThemes
// 把它与用户自定义主题合并(rpc 层),故单独暴露列表。
func DefaultThemeList() []tg.Theme {
	themes := chatThemeList()
	for i := range themes {
		themes[i].SetDefault(true)
	}
	return themes
}

func UniqueGiftChatThemes(hash int64) tg.AccountChatThemesClass {
	if hash == uniqueGiftChatThemesHash {
		return &tg.AccountChatThemesNotModified{}
	}
	return &tg.AccountChatThemes{
		Hash:   uniqueGiftChatThemesHash,
		Themes: []tg.ChatThemeClass{},
		Chats:  []tg.ChatClass{},
		Users:  []tg.UserClass{},
	}
}

// WallPapers returns the read-only default wallpaper catalog. User wallpaper
// upload/save/install remains outside the current TDesktop compatibility scope.
func WallPapers(hash int64) tg.AccountWallPapersClass {
	if hash == wallPapersHash {
		return &tg.AccountWallPapersNotModified{}
	}
	wallpapers := DefaultWallPapers()
	return &tg.AccountWallPapers{
		Hash:       wallPapersHash,
		Wallpapers: wallpapers,
	}
}

// chatThemeBaseThemes enumerates the base themes every emoji chat theme ships
// settings for. The client matches a chat theme's settings to whichever base
// theme the user currently runs, and DrKLO's theme picker only surfaces a chat
// theme whose settings cover at least four base themes
// (MediaDataController.generateEmojiPreviewThemes drops anything whose
// items.size() < 4, one item per ThemeSettings). dark selects the bubble/accent
// palette used for that base theme.
var chatThemeBaseThemes = []struct {
	make func() tg.BaseThemeClass
	dark bool
}{
	{func() tg.BaseThemeClass { return &tg.BaseThemeClassic{} }, false},
	{func() tg.BaseThemeClass { return &tg.BaseThemeDay{} }, false},
	{func() tg.BaseThemeClass { return &tg.BaseThemeArctic{} }, false},
	{func() tg.BaseThemeClass { return &tg.BaseThemeTinted{} }, true},
	{func() tg.BaseThemeClass { return &tg.BaseThemeNight{} }, true},
}

func AutoDownloadSettings() *tg.AccountAutoDownloadSettings {
	settings := tg.AutoDownloadSettings{
		PhotoSizeMax:                  10 * 1024 * 1024,
		VideoSizeMax:                  20 * 1024 * 1024,
		FileSizeMax:                   20 * 1024 * 1024,
		VideoUploadMaxbitrate:         1000,
		SmallQueueActiveOperationsMax: 2,
		LargeQueueActiveOperationsMax: 1,
	}
	return &tg.AccountAutoDownloadSettings{
		Low:    settings,
		Medium: settings,
		High:   settings,
	}
}

func DefaultEmojiStatuses() tg.AccountEmojiStatusesClass {
	return &tg.AccountEmojiStatusesNotModified{}
}

func CollectibleEmojiStatuses() tg.AccountEmojiStatusesClass {
	return &tg.AccountEmojiStatuses{Hash: 0, Statuses: []tg.EmojiStatusClass{}}
}

func DefaultGroupPhotoEmojis() tg.EmojiListClass {
	return &tg.EmojiList{Hash: 0, DocumentID: []int64{}}
}

const availableReactionsHash = 20260602
const emptyStickerSetHash = 20260602

// DefaultReactionEmoticon is the account fallback used by config and the
// built-in available-reactions catalog.
const DefaultReactionEmoticon = "\U0001f44d"

type defaultReaction struct {
	emoticon string
	title    string
}

var defaultAvailableReactions = []defaultReaction{
	{emoticon: DefaultReactionEmoticon, title: "Thumbs Up"},
	{emoticon: "\u2764\ufe0f", title: "Red Heart"},
	{emoticon: "\U0001f602", title: "Face With Tears of Joy"},
	{emoticon: "\U0001f62e", title: "Face With Open Mouth"},
	{emoticon: "\U0001f622", title: "Crying Face"},
	{emoticon: "\U0001f64f", title: "Folded Hands"},
}

// DefaultReactionEmoticons returns the TDesktop-compatible emoji reaction catalog order.
func DefaultReactionEmoticons() []string {
	out := make([]string, 0, len(defaultAvailableReactions))
	for _, reaction := range defaultAvailableReactions {
		out = append(out, reaction.emoticon)
	}
	return out
}

func AvailableReactions(hash int) tg.MessagesAvailableReactionsClass {
	if hash == availableReactionsHash {
		return &tg.MessagesAvailableReactionsNotModified{}
	}
	reactions := make([]tg.AvailableReaction, 0, len(defaultAvailableReactions))
	for i, reaction := range defaultAvailableReactions {
		reactions = append(reactions, availableReaction(reaction, i))
	}
	return &tg.MessagesAvailableReactions{
		Hash:      availableReactionsHash,
		Reactions: reactions,
	}
}

func availableReaction(reaction defaultReaction, index int) tg.AvailableReaction {
	const documentBaseID int64 = 900000000000000000
	doc := func(slot int64) tg.DocumentClass {
		return &tg.DocumentEmpty{ID: documentBaseID + int64(index)*10 + slot}
	}
	return tg.AvailableReaction{
		Reaction:          reaction.emoticon,
		Title:             reaction.title,
		StaticIcon:        doc(1),
		AppearAnimation:   doc(2),
		SelectAnimation:   doc(3),
		ActivateAnimation: doc(4),
		EffectAnimation:   doc(5),
	}
}

// Stickers 返回空的全量 messages.stickers。禁止回 stickersNotModified：DrKLO 对
func StickerSet(req *tg.MessagesGetStickerSetRequest) tg.MessagesStickerSetClass {
	if req != nil && req.Hash == emptyStickerSetHash {
		return &tg.MessagesStickerSetNotModified{}
	}
	title, shortName := "Telesrv Empty Sticker Set", "telesrv_empty"
	if req != nil {
		switch set := req.Stickerset.(type) {
		case *tg.InputStickerSetAnimatedEmoji:
			title, shortName = "Animated Emoji", "AnimatedEmojies"
		case *tg.InputStickerSetAnimatedEmojiAnimations:
			title, shortName = "Emoji Animations", "EmojiAnimations"
		case *tg.InputStickerSetEmojiGenericAnimations:
			title, shortName = "Emoji Generic Animations", "EmojiGenericAnimations"
		case *tg.InputStickerSetDice:
			title, shortName = "Dice Animations", "AnimatedDices"
			if set.Emoticon != "" {
				shortName = "AnimatedDice"
			}
		case *tg.InputStickerSetPremiumGifts:
			title, shortName = "Premium Gifts", "GiftsPremium"
		case *tg.InputStickerSetShortName:
			if set.ShortName != "" {
				title, shortName = set.ShortName, set.ShortName
			}
		}
	}
	return &tg.MessagesStickerSet{
		Set: tg.StickerSet{
			ID:         910000000000000000,
			AccessHash: 910000000000000001,
			Title:      title,
			ShortName:  shortName,
			Count:      0,
			Hash:       emptyStickerSetHash,
		},
		Packs:     []tg.StickerPack{},
		Keywords:  []tg.StickerKeyword{},
		Documents: []tg.DocumentClass{},
	}
}

// EmojiGroups 返回 emoji 分类目录(客户端 emoji 面板顶部的主题快捷标签)。优先用 catalog
// 固化的官方分组,未 seed 时回 NotModified(客户端保留本地/无分组)。icon_emoji_id 指向
// custom-emoji 文档,本地缺该文档时分类图标回退占位,不影响分组与按词搜索。
func EmojiGroups(hash int) tg.MessagesEmojiGroupsClass {
	groups, h := catalog.EmojiGroups()
	if len(groups) == 0 || hash == h {
		return &tg.MessagesEmojiGroupsNotModified{}
	}
	out := make([]tg.EmojiGroupClass, 0, len(groups))
	for _, g := range groups {
		out = append(out, &tg.EmojiGroup{Title: g.Title, IconEmojiID: g.IconEmojiID, Emoticons: g.Emoticons})
	}
	return &tg.MessagesEmojiGroups{Hash: h, Groups: out}
}

func EmojiStatusGroups() tg.MessagesEmojiGroupsClass {
	return &tg.MessagesEmojiGroups{Hash: 0, Groups: []tg.EmojiGroupClass{}}
}

func EmojiProfilePhotoGroups() tg.MessagesEmojiGroupsClass {
	return &tg.MessagesEmojiGroups{Hash: 0, Groups: []tg.EmojiGroupClass{}}
}

func AttachMenuBots() tg.AttachMenuBotsClass {
	return &tg.AttachMenuBots{
		Hash:  0,
		Bots:  []tg.AttachMenuBot{},
		Users: []tg.UserClass{},
	}
}

func QuickReplies() tg.MessagesQuickRepliesClass {
	return &tg.MessagesQuickReplies{
		QuickReplies: []tg.QuickReply{},
		Messages:     []tg.MessageClass{},
		Chats:        []tg.ChatClass{},
		Users:        []tg.UserClass{},
	}
}

func TopPeers() tg.ContactsTopPeersClass {
	return &tg.ContactsTopPeersDisabled{}
}

func BlockedContacts() tg.ContactsBlockedClass {
	return &tg.ContactsBlocked{
		Blocked: []tg.PeerBlocked{},
		Chats:   []tg.ChatClass{},
		Users:   []tg.UserClass{},
	}
}

type defaultPeerColor struct {
	id     int
	light  []int
	dark   []int
	bg     []int
	story  []int
	hidden bool
}

var defaultPeerColors = []defaultPeerColor{
	{id: 0, light: []int{0xcc4d4d}, dark: []int{0xff7a7a}, bg: []int{0xffd6d6, 0xfff0f0}, story: []int{0xff7a7a, 0xf24b4b}},
	{id: 1, light: []int{0xd97829}, dark: []int{0xffa15c}, bg: []int{0xffdfbd, 0xfff2e2}, story: []int{0xffa15c, 0xf07a2b}},
	{id: 2, light: []int{0x8756d9}, dark: []int{0xb38cff}, bg: []int{0xe3d8ff, 0xf4eeff}, story: []int{0xb38cff, 0x8756d9}},
	{id: 3, light: []int{0x3ca66b}, dark: []int{0x74d99b}, bg: []int{0xd5f5df, 0xedfff2}, story: []int{0x74d99b, 0x35a86a}},
	{id: 4, light: []int{0x2d9fc6}, dark: []int{0x6fd8f5}, bg: []int{0xd4f4ff, 0xf0fbff}, story: []int{0x6fd8f5, 0x2d9fc6}},
	{id: 5, light: []int{0x4a86d9}, dark: []int{0x78aef5}, bg: []int{0xd8e8ff, 0xf2f7ff}, story: []int{0x78aef5, 0x4a86d9}},
	{id: 6, light: []int{0xd85b93}, dark: []int{0xff8abb}, bg: []int{0xffd7e8, 0xfff5fb}, story: []int{0xff8abb, 0xd85b93}},
	{id: 7, light: []int{0x5f6a7a, 0x8c96a6}, dark: []int{0x9aa4b4, 0xc0c8d4}, bg: []int{0xe3e7ee, 0xf7f9fc}, story: []int{0x9aa4b4, 0x6f7b8d}},
}

// IsPeerColorID reports whether id is in the TDesktop-compatible peer color palette.
func IsPeerColorID(id int) bool {
	if found, seeded := DefaultPeerColorID(id, false); seeded {
		return found
	}
	for _, color := range defaultPeerColors {
		if color.id == id {
			return true
		}
	}
	return false
}

// IsPeerProfileColorID reports whether id is in the profile background palette.
func IsPeerProfileColorID(id int) bool {
	if found, seeded := DefaultPeerColorID(id, true); seeded {
		return found
	}
	return IsPeerColorID(id)
}

func PeerColors(hash int) tg.HelpPeerColorsClass {
	if hash == peerColorsHash {
		return &tg.HelpPeerColorsNotModified{}
	}
	colors := DefaultPeerColorOptions(false)
	if len(colors) == 0 {
		colors = make([]tg.HelpPeerColorOption, 0, len(defaultPeerColors))
		for _, color := range defaultPeerColors {
			option := tg.HelpPeerColorOption{ColorID: color.id}
			option.SetColors(&tg.HelpPeerColorSet{Colors: append([]int(nil), color.light...)})
			option.SetDarkColors(&tg.HelpPeerColorSet{Colors: append([]int(nil), color.dark...)})
			if color.hidden {
				option.SetHidden(true)
			}
			colors = append(colors, option)
		}
	}
	return &tg.HelpPeerColors{Hash: peerColorsHash, Colors: colors}
}

func PeerProfileColors(hash int) tg.HelpPeerColorsClass {
	if hash == peerProfileColorsHash {
		return &tg.HelpPeerColorsNotModified{}
	}
	colors := DefaultPeerColorOptions(true)
	if len(colors) == 0 {
		colors = make([]tg.HelpPeerColorOption, 0, len(defaultPeerColors))
		for _, color := range defaultPeerColors {
			option := tg.HelpPeerColorOption{ColorID: color.id}
			option.SetColors(&tg.HelpPeerColorProfileSet{
				PaletteColors: append([]int(nil), color.light...),
				BgColors:      append([]int(nil), color.bg...),
				StoryColors:   append([]int(nil), color.story...),
			})
			option.SetDarkColors(&tg.HelpPeerColorProfileSet{
				PaletteColors: append([]int(nil), color.dark...),
				BgColors:      append([]int(nil), color.bg...),
				StoryColors:   append([]int(nil), color.story...),
			})
			if color.hidden {
				option.SetHidden(true)
			}
			colors = append(colors, option)
		}
	}
	return &tg.HelpPeerColors{Hash: peerProfileColorsHash, Colors: colors}
}

func PromoData(now time.Time) tg.HelpPromoDataClass {
	return &tg.HelpPromoDataEmpty{Expires: int(now.Add(time.Hour).Unix())}
}

func TermsOfServiceUpdate(now time.Time) tg.HelpTermsOfServiceUpdateClass {
	return &tg.HelpTermsOfServiceUpdateEmpty{Expires: int(now.Add(24 * time.Hour).Unix())}
}

func StoriesArchive() *tg.StoriesStories {
	return &tg.StoriesStories{}
}

func PinnedStories() *tg.StoriesStories {
	return &tg.StoriesStories{}
}

func StoryAlbums() tg.StoriesAlbumsClass {
	return &tg.StoriesAlbums{Hash: 0, Albums: []tg.StoryAlbum{}}
}

func StarGiftActiveAuctions() tg.PaymentsStarGiftActiveAuctionsClass {
	return &tg.PaymentsStarGiftActiveAuctionsNotModified{}
}

func StarGifts() tg.PaymentsStarGiftsClass {
	return &tg.PaymentsStarGifts{
		Gifts: []tg.StarGiftClass{},
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{},
	}
}

func SavedStarGifts() *tg.PaymentsSavedStarGifts {
	return &tg.PaymentsSavedStarGifts{
		Gifts: []tg.SavedStarGift{},
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{},
	}
}

func StarGiftCollections() tg.PaymentsStarGiftCollectionsClass {
	return &tg.PaymentsStarGiftCollections{
		Collections: []tg.StarGiftCollection{},
	}
}

func StarsRevenueStats(ton bool) *tg.PaymentsStarsRevenueStats {
	zeroAmount := tg.StarsAmountClass(&tg.StarsAmount{})
	if ton {
		zeroAmount = &tg.StarsTonAmount{}
	}
	return &tg.PaymentsStarsRevenueStats{
		RevenueGraph: &tg.StatsGraphError{Error: "Not enough data to display."},
		Status: tg.StarsRevenueStatus{
			CurrentBalance:    zeroAmount,
			AvailableBalance:  zeroAmount,
			OverallRevenue:    zeroAmount,
			WithdrawalEnabled: false,
		},
		UsdRate: 0.013,
	}
}

func AiComposeTones() tg.AicomposeTonesClass {
	return &tg.AicomposeTonesNotModified{}
}

func WebPage(url string) *tg.MessagesWebPage {
	page := &tg.WebPageEmpty{ID: 0}
	if url != "" {
		page.SetURL(url)
	}
	return &tg.MessagesWebPage{
		Webpage: page,
		Chats:   []tg.ChatClass{},
		Users:   []tg.UserClass{},
	}
}
