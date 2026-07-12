package tdesktop

import (
	"math"
	"testing"

	"github.com/gotd/td/tg"
)

func TestNotifySettingsDefaultIsAudible(t *testing.T) {
	settings := NotifySettings()
	if value, ok := settings.GetShowPreviews(); !ok || !value {
		t.Fatalf("show_previews = %v ok=%v, want true", value, ok)
	}
	if value, ok := settings.GetSilent(); !ok || value {
		t.Fatalf("silent = %v ok=%v, want explicit false", value, ok)
	}
	if value, ok := settings.GetMuteUntil(); !ok || value != 0 {
		t.Fatalf("mute_until = %d ok=%v, want explicit 0", value, ok)
	}
	if value, ok := settings.GetOtherSound(); !ok || value == nil {
		t.Fatalf("other_sound = %#v ok=%v, want default sound", value, ok)
	}
}

func TestTimezonesListIsNonEmptyAndHashable(t *testing.T) {
	got, ok := TimezonesList(0).(*tg.HelpTimezonesList)
	if !ok || got.Hash == 0 || len(got.Timezones) == 0 {
		t.Fatalf("TimezonesList(0) = %#v, want non-empty modified list", got)
	}
	if _, ok := TimezonesList(got.Hash).(*tg.HelpTimezonesListNotModified); !ok {
		t.Fatalf("TimezonesList(hash) = %#v, want notModified", TimezonesList(got.Hash))
	}
}

func TestAppConfigIncludesStoryStealthPeriods(t *testing.T) {
	got, ok := AppConfig(0).(*tg.HelpAppConfig)
	if !ok || got.Hash == 0 {
		t.Fatalf("AppConfig(0) = %#v, want modified config with hash", got)
	}
	values := make(map[string]float64)
	strings := make(map[string]string)
	arrays := make(map[string]*tg.JSONArray)
	if object, ok := got.Config.(*tg.JSONObject); ok && object != nil {
		for _, entry := range object.Value {
			if number, ok := entry.Value.(*tg.JSONNumber); ok {
				values[entry.Key] = number.Value
			}
			if str, ok := entry.Value.(*tg.JSONString); ok {
				strings[entry.Key] = str.Value
			}
			if array, ok := entry.Value.(*tg.JSONArray); ok {
				arrays[entry.Key] = array
			}
		}
	}
	want := map[string]float64{
		"stories_stealth_future_period":   1500,
		"stories_stealth_past_period":     300,
		"stories_stealth_cooldown_period": 10800,
	}
	for key, expected := range want {
		if values[key] != expected {
			t.Fatalf("AppConfig[%q] = %v, want %v", key, values[key], expected)
		}
	}
	if strings["rich_message_posting"] != "enabled" {
		t.Fatalf("AppConfig[rich_message_posting] = %q, want enabled", strings["rich_message_posting"])
	}
	fragmentPrefixes := arrays["fragment_prefixes"]
	if fragmentPrefixes == nil || len(fragmentPrefixes.Value) != 1 {
		t.Fatalf("AppConfig[fragment_prefixes] = %#v, want one-element array", fragmentPrefixes)
	}
	prefix, ok := fragmentPrefixes.Value[0].(*tg.JSONString)
	if !ok || prefix.Value != "888" {
		t.Fatalf("AppConfig[fragment_prefixes][0] = %#v, want \"888\"", fragmentPrefixes.Value[0])
	}
	if _, ok := AppConfig(got.Hash).(*tg.HelpAppConfigNotModified); !ok {
		t.Fatalf("AppConfig(hash) = %#v, want notModified", AppConfig(got.Hash))
	}
	if _, ok := AppConfig(got.Hash - 1).(*tg.HelpAppConfig); !ok {
		t.Fatalf("AppConfig(old hash) = %#v, want refreshed config", AppConfig(got.Hash-1))
	}
}

func TestFallbackAppConfigOmitsMapboxToken(t *testing.T) {
	got, ok := AppConfig(0).(*tg.HelpAppConfig)
	if !ok {
		t.Fatalf("AppConfig(0) = %T, want modified config", got)
	}
	if object, ok := got.Config.(*tg.JSONObject); ok && object != nil {
		for _, entry := range object.Value {
			if entry.Key == "tdesktop_config_map" {
				t.Fatal("fallback AppConfig leaked tdesktop_config_map without runtime token")
			}
		}
	}
}

func TestAvailableReactionsCatalogIsNonEmptyAndHashable(t *testing.T) {
	got, ok := AvailableReactions(0).(*tg.MessagesAvailableReactions)
	if !ok {
		t.Fatalf("AvailableReactions(0) = %T, want modified list", got)
	}
	if got.Hash == 0 {
		t.Fatal("AvailableReactions(0).Hash = 0, want stable cache hash")
	}
	if len(got.Reactions) == 0 {
		t.Fatal("AvailableReactions(0).Reactions is empty")
	}
	for i, reaction := range got.Reactions {
		if reaction.Reaction == "" {
			t.Fatalf("reaction[%d].Reaction is empty", i)
		}
		if reaction.Title == "" {
			t.Fatalf("reaction[%d].Title is empty", i)
		}
		if reaction.StaticIcon == nil ||
			reaction.AppearAnimation == nil ||
			reaction.SelectAnimation == nil ||
			reaction.ActivateAnimation == nil ||
			reaction.EffectAnimation == nil {
			t.Fatalf("reaction[%d] has nil required document: %#v", i, reaction)
		}
		if reaction.Inactive || reaction.Premium {
			t.Fatalf("reaction[%d] flags = inactive %v premium %v, want active non-premium", i, reaction.Inactive, reaction.Premium)
		}
	}
	if _, ok := AvailableReactions(got.Hash).(*tg.MessagesAvailableReactionsNotModified); !ok {
		t.Fatalf("AvailableReactions(hash) = %#v, want notModified", AvailableReactions(got.Hash))
	}
}

func TestChatThemesCatalogIsNonEmptyHashableAndHasQRBackgroundColors(t *testing.T) {
	got, ok := ChatThemes(0).(*tg.AccountThemes)
	if !ok {
		t.Fatalf("ChatThemes(0) = %T, want modified list", got)
	}
	if got.Hash == 0 {
		t.Fatal("ChatThemes(0).Hash = 0, want stable cache hash")
	}
	if len(got.Themes) == 0 {
		t.Fatal("ChatThemes(0).Themes is empty")
	}
	for i, theme := range got.Themes {
		if !theme.GetForChat() {
			t.Fatalf("theme[%d].for_chat = false, want true", i)
		}
		if emoticon, ok := theme.GetEmoticon(); !ok || emoticon == "" || !IsChatThemeEmoticon(emoticon) {
			t.Fatalf("theme[%d].emoticon = %q ok=%v, want supported token", i, emoticon, ok)
		}
		settings, ok := theme.GetSettings()
		// DrKLO only surfaces an emoji chat theme in the picker when it ships
		// settings for at least four base themes
		// (MediaDataController.generateEmojiPreviewThemes drops items.size() < 4,
		// one item per ThemeSettings); fewer leaves the strip stuck on its
		// loading shimmer.
		if !ok || len(settings) < 4 {
			t.Fatalf("theme[%d].settings = %#v ok=%v, want >=4 base-theme settings", i, settings, ok)
		}
		var hasDay, hasNight bool
		for j, setting := range settings {
			if setting.BaseTheme == nil {
				t.Fatalf("theme[%d].settings[%d].base_theme missing", i, j)
			}
			switch setting.BaseTheme.(type) {
			case *tg.BaseThemeDay:
				hasDay = true
			case *tg.BaseThemeNight:
				hasNight = true
			}
			// 官方聊天主题用文档型主题背景墙纸(非旧的纯渐变 WallPaperNoFile),
			// 这里只要求每组设置带一个墙纸。
			if wallpaper, ok := setting.GetWallpaper(); !ok || wallpaper == nil {
				t.Fatalf("theme[%d].settings[%d].wallpaper = %#v ok=%v, want wallpaper", i, j, wallpaper, ok)
			}
		}
		if !hasDay || !hasNight {
			t.Fatalf("theme[%d] settings missing day/night coverage (day=%v night=%v)", i, hasDay, hasNight)
		}
	}
	if _, ok := ChatThemes(got.Hash).(*tg.AccountThemesNotModified); !ok {
		t.Fatalf("ChatThemes(hash) = %#v, want notModified", ChatThemes(got.Hash))
	}
}

func TestDefaultThemesAreDefaultFlaggedForPicker(t *testing.T) {
	// account.getThemes feeds DrKLO's "Color theme" preview strip
	// (DefaultThemesPreviewCell), which only consumes themes flagged
	// is_default=true. Returning themesNotModified to a fresh client (hash 0)
	// leaves the strip stuck on a loading shimmer, so a fresh request must
	// return a populated, default-flagged catalog.
	got, ok := DefaultThemes(0).(*tg.AccountThemes)
	if !ok {
		t.Fatalf("DefaultThemes(0) = %T, want modified list", got)
	}
	if got.Hash == 0 {
		t.Fatal("DefaultThemes(0).Hash = 0, want stable cache hash")
	}
	if len(got.Themes) == 0 {
		t.Fatal("DefaultThemes(0).Themes is empty")
	}
	for i, theme := range got.Themes {
		if !theme.GetDefault() {
			t.Fatalf("theme[%d].is_default = false, want true so the picker shows it", i)
		}
		if !theme.GetForChat() {
			t.Fatalf("theme[%d].for_chat = false, want true", i)
		}
		if settings, ok := theme.GetSettings(); !ok || len(settings) < 4 {
			t.Fatalf("theme[%d].settings len=%d ok=%v, want >=4 for the picker", i, len(settings), ok)
		}
	}
	if _, ok := DefaultThemes(got.Hash).(*tg.AccountThemesNotModified); !ok {
		t.Fatalf("DefaultThemes(hash) = %#v, want notModified", DefaultThemes(got.Hash))
	}
}

func TestUniqueGiftChatThemesIsEmptyHashableStub(t *testing.T) {
	got, ok := UniqueGiftChatThemes(0).(*tg.AccountChatThemes)
	if !ok {
		t.Fatalf("UniqueGiftChatThemes(0) = %T, want modified list", got)
	}
	if got.Hash == 0 {
		t.Fatal("UniqueGiftChatThemes(0).Hash = 0, want stable cache hash")
	}
	if len(got.Themes) != 0 || len(got.Chats) != 0 || len(got.Users) != 0 {
		t.Fatalf("UniqueGiftChatThemes(0) = themes %d chats %d users %d, want empty vectors",
			len(got.Themes), len(got.Chats), len(got.Users))
	}
	if _, ok := UniqueGiftChatThemes(got.Hash).(*tg.AccountChatThemesNotModified); !ok {
		t.Fatalf("UniqueGiftChatThemes(hash) = %#v, want notModified", UniqueGiftChatThemes(got.Hash))
	}
}

func TestWallPapersUsesOrangeFileCatalog(t *testing.T) {
	got, ok := WallPapers(0).(*tg.AccountWallPapers)
	if !ok {
		t.Fatalf("WallPapers(0) = %T, want modified list", got)
	}
	if got.Hash == 0 {
		t.Fatal("WallPapers(0).Hash = 0, want stable cache hash")
	}
	if len(got.Wallpapers) == 0 {
		t.Fatal("WallPapers(0).Wallpapers is empty")
	}
	wallpaper, ok := got.Wallpapers[0].(*tg.WallPaper)
	if !ok {
		t.Fatalf("WallPapers(0).Wallpapers[0] = %T, want *tg.WallPaper", got.Wallpapers[0])
	}
	if wallpaper.ID == 0 || wallpaper.AccessHash == 0 || wallpaper.Slug == "" {
		t.Fatalf("wallpaper identity = id %d hash %d slug %q, want seed ids", wallpaper.ID, wallpaper.AccessHash, wallpaper.Slug)
	}
	doc, ok := wallpaper.Document.(*tg.Document)
	if !ok {
		t.Fatalf("wallpaper document = %T, want *tg.Document", wallpaper.Document)
	}
	if doc.ID == 0 || doc.AccessHash == 0 || doc.Size == 0 || doc.MimeType == "" || doc.DCID != appearanceSeedDCID {
		t.Fatalf("wallpaper document = id %d hash %d size %d mime %q dc %d, want downloadable seed document",
			doc.ID, doc.AccessHash, doc.Size, doc.MimeType, doc.DCID)
	}
	if len(doc.Thumbs) == 0 {
		t.Fatal("wallpaper document has no thumbnails")
	}
	if _, ok := doc.Thumbs[0].(*tg.PhotoSize); !ok {
		t.Fatalf("wallpaper thumb = %T, want *tg.PhotoSize", doc.Thumbs[0])
	}
	if _, ok := WallPapers(got.Hash).(*tg.AccountWallPapersNotModified); !ok {
		t.Fatalf("WallPapers(hash) = %#v, want notModified", WallPapers(got.Hash))
	}
}

func TestLookupWallPaperByIDAndSlug(t *testing.T) {
	catalog := WallPapers(0).(*tg.AccountWallPapers)
	first := catalog.Wallpapers[0].(*tg.WallPaper)
	byID, ok := LookupWallPaper(&tg.InputWallPaper{ID: first.ID, AccessHash: first.AccessHash})
	if !ok {
		t.Fatal("LookupWallPaper(inputWallPaper) = false, want true")
	}
	if got := byID.(*tg.WallPaper).Slug; got != first.Slug {
		t.Fatalf("LookupWallPaper(inputWallPaper).Slug = %q, want %q", got, first.Slug)
	}
	bySlug, ok := LookupWallPaper(&tg.InputWallPaperSlug{Slug: first.Slug})
	if !ok {
		t.Fatal("LookupWallPaper(inputWallPaperSlug) = false, want true")
	}
	if got := bySlug.(*tg.WallPaper).ID; got != first.ID {
		t.Fatalf("LookupWallPaper(inputWallPaperSlug).ID = %d, want %d", got, first.ID)
	}
	if _, ok := LookupWallPaper(&tg.InputWallPaper{ID: first.ID, AccessHash: first.AccessHash + 1}); ok {
		t.Fatal("LookupWallPaper(wrong access hash) = true, want false")
	}
	multi, ok := LookupWallPapers([]tg.InputWallPaperClass{
		&tg.InputWallPaper{ID: first.ID, AccessHash: first.AccessHash},
		&tg.InputWallPaperSlug{Slug: catalog.Wallpapers[1].(*tg.WallPaper).Slug},
	})
	if !ok || len(multi) != 2 {
		t.Fatalf("LookupWallPapers = len %d ok %v, want 2 true", len(multi), ok)
	}
}

func TestStarsRevenueStatsIsZeroBalanceCompatStub(t *testing.T) {
	got := StarsRevenueStats(false)
	if got == nil {
		t.Fatal("StarsRevenueStats(false) = nil")
	}
	if _, ok := got.RevenueGraph.(*tg.StatsGraphError); !ok {
		t.Fatalf("RevenueGraph = %T, want *tg.StatsGraphError", got.RevenueGraph)
	}
	if got.Status.WithdrawalEnabled {
		t.Fatal("WithdrawalEnabled = true, want false")
	}
	if _, ok := got.Status.CurrentBalance.(*tg.StarsAmount); !ok {
		t.Fatalf("CurrentBalance = %T, want *tg.StarsAmount", got.Status.CurrentBalance)
	}

	ton := StarsRevenueStats(true)
	if _, ok := ton.Status.CurrentBalance.(*tg.StarsTonAmount); !ok {
		t.Fatalf("TON CurrentBalance = %T, want *tg.StarsTonAmount", ton.Status.CurrentBalance)
	}
}

func TestCollectibleEmojiStatusesIsEmptyModifiedList(t *testing.T) {
	got, ok := CollectibleEmojiStatuses().(*tg.AccountEmojiStatuses)
	if !ok {
		t.Fatalf("CollectibleEmojiStatuses() = %T, want empty modified list", got)
	}
	if got.Hash != 0 {
		t.Fatalf("CollectibleEmojiStatuses().Hash = %d, want 0", got.Hash)
	}
	if len(got.Statuses) != 0 {
		t.Fatalf("CollectibleEmojiStatuses().Statuses length = %d, want 0", len(got.Statuses))
	}
}

func TestDefaultGroupPhotoEmojisIsEmptyModifiedList(t *testing.T) {
	got, ok := DefaultGroupPhotoEmojis().(*tg.EmojiList)
	if !ok {
		t.Fatalf("DefaultGroupPhotoEmojis() = %T, want empty modified list", got)
	}
	if got.Hash != 0 {
		t.Fatalf("DefaultGroupPhotoEmojis().Hash = %d, want 0", got.Hash)
	}
	if len(got.DocumentID) != 0 {
		t.Fatalf("DefaultGroupPhotoEmojis().DocumentID length = %d, want 0", len(got.DocumentID))
	}
}

func TestEmojiProfilePhotoGroupsIsEmptyModifiedList(t *testing.T) {
	got, ok := EmojiProfilePhotoGroups().(*tg.MessagesEmojiGroups)
	if !ok {
		t.Fatalf("EmojiProfilePhotoGroups() = %T, want empty modified list", got)
	}
	if got.Hash != 0 {
		t.Fatalf("EmojiProfilePhotoGroups().Hash = %d, want 0", got.Hash)
	}
	if len(got.Groups) != 0 {
		t.Fatalf("EmojiProfilePhotoGroups().Groups length = %d, want 0", len(got.Groups))
	}
}

func TestEmojiStatusGroupsIsEmptyModifiedList(t *testing.T) {
	got, ok := EmojiStatusGroups().(*tg.MessagesEmojiGroups)
	if !ok {
		t.Fatalf("EmojiStatusGroups() = %T, want empty modified list", got)
	}
	if got.Hash != 0 {
		t.Fatalf("EmojiStatusGroups().Hash = %d, want 0", got.Hash)
	}
	if len(got.Groups) != 0 {
		t.Fatalf("EmojiStatusGroups().Groups length = %d, want 0", len(got.Groups))
	}
}

func TestQuickRepliesIsEmptyModifiedList(t *testing.T) {
	got, ok := QuickReplies().(*tg.MessagesQuickReplies)
	if !ok {
		t.Fatalf("QuickReplies() = %T, want empty modified list", got)
	}
	if len(got.QuickReplies) != 0 || len(got.Messages) != 0 || len(got.Chats) != 0 || len(got.Users) != 0 {
		t.Fatalf("QuickReplies() = shortcuts %d messages %d chats %d users %d, want empty vectors",
			len(got.QuickReplies), len(got.Messages), len(got.Chats), len(got.Users))
	}
}

func TestPeerColorsAreNonEmptyHashableAccentSets(t *testing.T) {
	got, ok := PeerColors(0).(*tg.HelpPeerColors)
	if !ok {
		t.Fatalf("PeerColors(0) = %T, want modified colors", got)
	}
	if got.Hash == 0 || len(got.Colors) == 0 {
		t.Fatalf("PeerColors(0) = hash %d colors %d, want non-empty stable list", got.Hash, len(got.Colors))
	}
	if len(got.Colors) != 21 {
		t.Fatalf("PeerColors(0).Colors length = %d, want seed palette count 21", len(got.Colors))
	}
	withExplicitColors := 0
	for i, option := range got.Colors {
		if !IsPeerColorID(option.ColorID) {
			t.Fatalf("PeerColors()[%d].ColorID = %d, want supported id", i, option.ColorID)
		}
		colors, ok := option.GetColors()
		if !ok {
			if option.ColorID > 6 {
				t.Fatalf("PeerColors()[%d].Colors missing for non-default id %d", i, option.ColorID)
			}
			continue
		}
		if _, ok := colors.(*tg.HelpPeerColorSet); !ok {
			t.Fatalf("PeerColors()[%d].Colors = %T, want *tg.HelpPeerColorSet", i, colors)
		}
		withExplicitColors++
	}
	if withExplicitColors == 0 {
		t.Fatal("PeerColors() has no explicit seed color sets")
	}
	if _, ok := PeerColors(got.Hash).(*tg.HelpPeerColorsNotModified); !ok {
		t.Fatalf("PeerColors(hash) = %#v, want notModified", PeerColors(got.Hash))
	}
}

func TestPeerProfileColorsAreNonEmptyHashableProfileSets(t *testing.T) {
	got, ok := PeerProfileColors(0).(*tg.HelpPeerColors)
	if !ok {
		t.Fatalf("PeerProfileColors(0) = %T, want modified colors", got)
	}
	if got.Hash == 0 || len(got.Colors) == 0 {
		t.Fatalf("PeerProfileColors(0) = hash %d colors %d, want non-empty stable list", got.Hash, len(got.Colors))
	}
	if len(got.Colors) != 16 {
		t.Fatalf("PeerProfileColors(0).Colors length = %d, want seed profile palette count 16", len(got.Colors))
	}
	for i, option := range got.Colors {
		if !IsPeerProfileColorID(option.ColorID) {
			t.Fatalf("PeerProfileColors()[%d].ColorID = %d, want supported id", i, option.ColorID)
		}
		groupMin, ok := option.GetGroupMinLevel()
		if !ok || groupMin <= 0 || groupMin > maxPeerColorBoostLevel {
			t.Fatalf("PeerProfileColors()[%d].group_min_level = %d ok %v, want bounded positive level", i, groupMin, ok)
		}
		if channelMin, ok := option.GetChannelMinLevel(); ok && (channelMin <= 0 || channelMin > maxPeerColorBoostLevel) {
			t.Fatalf("PeerProfileColors()[%d].channel_min_level = %d, want bounded positive level", i, channelMin)
		}
		colors, ok := option.GetColors()
		if !ok {
			t.Fatalf("PeerProfileColors()[%d].Colors missing", i)
		}
		profile, ok := colors.(*tg.HelpPeerColorProfileSet)
		if !ok {
			t.Fatalf("PeerProfileColors()[%d].Colors = %T, want *tg.HelpPeerColorProfileSet", i, colors)
		}
		if len(profile.PaletteColors) == 0 || len(profile.BgColors) == 0 || len(profile.StoryColors) != 2 {
			t.Fatalf("PeerProfileColors()[%d] = palette %d bg %d story %d, want usable profile palette",
				i, len(profile.PaletteColors), len(profile.BgColors), len(profile.StoryColors))
		}
	}
	if _, ok := PeerProfileColors(got.Hash).(*tg.HelpPeerColorsNotModified); !ok {
		t.Fatalf("PeerProfileColors(hash) = %#v, want notModified", PeerProfileColors(got.Hash))
	}
}

func TestPeerProfileColorsKeepTDesktopBoostFeatureLevelsBounded(t *testing.T) {
	got, ok := PeerProfileColors(0).(*tg.HelpPeerColors)
	if !ok {
		t.Fatalf("PeerProfileColors(0) = %T, want modified colors", got)
	}
	levels := make([]int, 0, len(got.Colors))
	lowestNonZeroLevel := math.MaxInt
	for _, option := range got.Colors {
		level, ok := option.GetGroupMinLevel()
		if !ok {
			level = 0
		}
		levels = append(levels, level)
		if level != 0 && level < lowestNonZeroLevel {
			lowestNonZeroLevel = level
		}
	}
	if lowestNonZeroLevel == math.MaxInt {
		t.Fatal("profile color group levels are all zero; TDesktop would aggregate them at MaxInt in BoostBox")
	}
	maxFeatureLevel := 0
	for _, level := range levels {
		if level < lowestNonZeroLevel {
			level = lowestNonZeroLevel
		}
		if level > maxFeatureLevel {
			maxFeatureLevel = level
		}
	}
	if maxFeatureLevel <= 0 || maxFeatureLevel > maxPeerColorBoostLevel {
		t.Fatalf("TDesktop profile feature max level = %d, want 1..%d", maxFeatureLevel, maxPeerColorBoostLevel)
	}
}

func TestPeerProfileColorsReturnsIndependentOptions(t *testing.T) {
	first, ok := PeerProfileColors(0).(*tg.HelpPeerColors)
	if !ok {
		t.Fatalf("PeerProfileColors(0) first = %T, want modified colors", first)
	}
	second, ok := PeerProfileColors(0).(*tg.HelpPeerColors)
	if !ok {
		t.Fatalf("PeerProfileColors(0) second = %T, want modified colors", second)
	}
	if len(first.Colors) == 0 || len(second.Colors) == 0 {
		t.Fatal("PeerProfileColors returned empty colors")
	}
	first.Colors[0].SetGroupMinLevel(maxPeerColorBoostLevel + 1)
	if groupMin, ok := second.Colors[0].GetGroupMinLevel(); !ok || groupMin > maxPeerColorBoostLevel {
		t.Fatalf("second PeerProfileColors group_min_level = %d ok %v, want independent bounded copy", groupMin, ok)
	}

	firstSet, ok := first.Colors[0].Colors.(*tg.HelpPeerColorProfileSet)
	if !ok || len(firstSet.PaletteColors) == 0 {
		t.Fatalf("first profile colors = %T %+v, want profile set with palette", first.Colors[0].Colors, first.Colors[0].Colors)
	}
	secondSet, ok := second.Colors[0].Colors.(*tg.HelpPeerColorProfileSet)
	if !ok || len(secondSet.PaletteColors) == 0 {
		t.Fatalf("second profile colors = %T %+v, want profile set with palette", second.Colors[0].Colors, second.Colors[0].Colors)
	}
	original := secondSet.PaletteColors[0]
	firstSet.PaletteColors[0] = original + 1
	if secondSet.PaletteColors[0] != original {
		t.Fatalf("second profile palette mutated through first response: got %d want %d", secondSet.PaletteColors[0], original)
	}
}

func TestStoryAlbumsIsEmptyModifiedList(t *testing.T) {
	got, ok := StoryAlbums().(*tg.StoriesAlbums)
	if !ok {
		t.Fatalf("StoryAlbums() = %T, want empty modified list", got)
	}
	if got.Hash != 0 {
		t.Fatalf("StoryAlbums().Hash = %d, want 0", got.Hash)
	}
	if len(got.Albums) != 0 {
		t.Fatalf("StoryAlbums().Albums length = %d, want 0", len(got.Albums))
	}
}

func TestStickerSetReturnsEmptyModifiedSetForColdRequest(t *testing.T) {
	got, ok := StickerSet(&tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetEmojiGenericAnimations{},
		Hash:       0,
	}).(*tg.MessagesStickerSet)
	if !ok {
		t.Fatalf("StickerSet(hash=0) = %T, want modified empty set", got)
	}
	if got.Set.Hash == 0 {
		t.Fatal("StickerSet(hash=0).Set.Hash = 0, want stable cache hash")
	}
	if got.Set.ShortName != "EmojiGenericAnimations" {
		t.Fatalf("StickerSet(hash=0).Set.ShortName = %q, want EmojiGenericAnimations", got.Set.ShortName)
	}
	if len(got.Packs) != 0 || len(got.Keywords) != 0 || len(got.Documents) != 0 {
		t.Fatalf("StickerSet(hash=0) = packs %d keywords %d documents %d, want empty vectors", len(got.Packs), len(got.Keywords), len(got.Documents))
	}
	if _, ok := StickerSet(&tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetEmojiGenericAnimations{},
		Hash:       got.Set.Hash,
	}).(*tg.MessagesStickerSetNotModified); !ok {
		t.Fatalf("StickerSet(hash) = %#v, want notModified", StickerSet(&tg.MessagesGetStickerSetRequest{
			Stickerset: &tg.InputStickerSetEmojiGenericAnimations{},
			Hash:       got.Set.Hash,
		}))
	}
}
