package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	themesapp "telesrv/internal/app/themes"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newThemeRouter(t *testing.T, files *fakeFiles) *Router {
	t.Helper()
	return New(Config{}, Deps{
		Files:  files,
		Themes: themesapp.NewService(memory.NewThemeStore()),
	}, zaptest.NewLogger(t), clock.System)
}

func mustEncodeTheme(t *testing.T, th *tg.Theme) {
	t.Helper()
	var b bin.Buffer
	if err := th.Encode(&b); err != nil {
		t.Fatalf("theme encode failed (likely nil base_theme): %v", err)
	}
}

// TestAccountUploadThemeReturnsDocument verifies uploadTheme turns an uploaded file into a
// downloadable Document and returns it, and validates the mime prefix.
func TestAccountUploadThemeReturnsDocument(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000001)
	r := newThemeRouter(t, &fakeFiles{})

	req := &tg.AccountUploadThemeRequest{FileName: "theme.attheme", MimeType: "application/x-tgtheme-android"}
	req.File = &tg.InputFile{ID: 42, Parts: 1, Name: "theme.attheme"}
	got, err := r.onAccountUploadTheme(ctx, req)
	if err != nil {
		t.Fatalf("uploadTheme err = %v", err)
	}
	doc, ok := got.(*tg.Document)
	if !ok {
		t.Fatalf("uploadTheme = %T, want *tg.Document", got)
	}
	if doc.ID == 0 {
		t.Fatalf("uploaded document id = 0, want non-zero downloadable id")
	}

	// A non-theme mime prefix is rejected.
	bad := &tg.AccountUploadThemeRequest{FileName: "x", MimeType: "image/png"}
	bad.File = &tg.InputFile{ID: 7, Parts: 1, Name: "x"}
	if _, err := r.onAccountUploadTheme(ctx, bad); !tgerr.Is(err, "THEME_MIME_INVALID") {
		t.Fatalf("bad mime err = %v, want THEME_MIME_INVALID", err)
	}
}

// TestAccountCreateThemeFullFlow verifies createTheme auto-assigns a slug, sets creator=true,
// and backfills the document; getTheme fetches it back by slug with the document;
// saveTheme/installTheme return true.
func TestAccountCreateThemeFullFlow(t *testing.T) {
	const userID = 1000002
	ctx := WithUserID(context.Background(), userID)
	files := &fakeFiles{docs: map[int64]domain.Document{
		555: {ID: 555, AccessHash: 5, DCID: 2, MimeType: "application/x-tgtheme-android", Size: 1234},
	}}
	r := newThemeRouter(t, files)

	create := &tg.AccountCreateThemeRequest{Slug: "", Title: "My Theme"}
	create.SetDocument(&tg.InputDocument{ID: 555, AccessHash: 5})
	th, err := r.onAccountCreateTheme(ctx, create)
	if err != nil {
		t.Fatalf("createTheme err = %v", err)
	}
	if th.ID == 0 || th.AccessHash == 0 {
		t.Fatalf("created theme id/access_hash = %d/%d, want non-zero", th.ID, th.AccessHash)
	}
	if !th.GetCreator() {
		t.Fatalf("created theme creator = false, want true (so client routes re-upload to updateTheme)")
	}
	if th.Slug == "" {
		t.Fatalf("created theme slug empty, want auto-assigned slug")
	}
	if doc, ok := th.GetDocument(); !ok {
		t.Fatalf("created theme has no document, want document (deep-link addtheme needs it)")
	} else if d, ok := doc.(*tg.Document); !ok || d.ID != 555 {
		t.Fatalf("created theme document = %#v, want *tg.Document id 555", doc)
	}
	mustEncodeTheme(t, th)

	// getTheme by slug fetches it back, still carrying the document.
	get := &tg.AccountGetThemeRequest{Format: "android"}
	get.Theme = &tg.InputThemeSlug{Slug: th.Slug}
	got, err := r.onAccountGetTheme(ctx, get)
	if err != nil {
		t.Fatalf("getTheme by slug err = %v", err)
	}
	if got.ID != th.ID {
		t.Fatalf("getTheme id = %d, want %d", got.ID, th.ID)
	}
	if _, ok := got.GetDocument(); !ok {
		t.Fatalf("getTheme by slug missing document → client would show ThemeNotSupported")
	}
	mustEncodeTheme(t, got)

	// saveTheme + installTheme by id+access_hash。
	save := &tg.AccountSaveThemeRequest{Theme: &tg.InputTheme{ID: th.ID, AccessHash: th.AccessHash}, Unsave: false}
	if ok, err := r.onAccountSaveTheme(ctx, save); err != nil || !ok {
		t.Fatalf("saveTheme = %v/%v, want true/nil", ok, err)
	}
	inst := &tg.AccountInstallThemeRequest{}
	inst.SetTheme(&tg.InputTheme{ID: th.ID, AccessHash: th.AccessHash})
	inst.SetDark(true)
	if ok, err := r.onAccountInstallTheme(ctx, inst); err != nil || !ok {
		t.Fatalf("installTheme = %v/%v, want true/nil", ok, err)
	}

	// Unknown theme reference -> THEME_INVALID.
	bad := &tg.AccountGetThemeRequest{Format: "android", Theme: &tg.InputThemeSlug{Slug: "does-not-exist"}}
	if _, err := r.onAccountGetTheme(ctx, bad); !tgerr.Is(err, "THEME_INVALID") {
		t.Fatalf("getTheme unknown slug err = %v, want THEME_INVALID", err)
	}
}

func TestAccountDefaultThemeReferencesResolveWithoutCustomPersistence(t *testing.T) {
	const userID = 1000012
	ctx := WithClientInfo(
		WithUserID(context.Background(), userID),
		ClientInfo{Type: ClientTypeAndroid, AppVersion: "12.9.0 (69669)"},
	)
	r := newThemeRouter(t, &fakeFiles{})
	defaults := tdesktop.DefaultThemeList()
	if len(defaults) == 0 {
		t.Fatal("default theme catalog is empty")
	}

	for i, theme := range defaults {
		t.Run(theme.Slug, func(t *testing.T) {
			input := &tg.InputTheme{ID: theme.ID, AccessHash: theme.AccessHash}
			install := &tg.AccountInstallThemeRequest{}
			install.SetDark(i%2 == 0)
			install.SetTheme(input)
			install.SetFormat("android")
			if ok, err := r.onAccountInstallTheme(ctx, install); err != nil || !ok {
				t.Fatalf("install default theme %d = %v/%v, want true/nil", theme.ID, ok, err)
			}

			for _, unsave := range []bool{false, true} {
				if ok, err := r.onAccountSaveTheme(ctx, &tg.AccountSaveThemeRequest{
					Theme:  input,
					Unsave: unsave,
				}); err != nil || !ok {
					t.Fatalf("save default theme %d unsave=%v = %v/%v, want true/nil", theme.ID, unsave, ok, err)
				}
			}

			got, err := r.onAccountGetTheme(ctx, &tg.AccountGetThemeRequest{
				Format: "android",
				Theme:  &tg.InputThemeSlug{Slug: theme.Slug},
			})
			if err != nil || got.ID != theme.ID || got.AccessHash != theme.AccessHash {
				t.Fatalf("get default theme %d = %#v/%v", theme.ID, got, err)
			}

			if ok, err := r.onAccountSaveTheme(ctx, &tg.AccountSaveThemeRequest{
				Theme: &tg.InputThemeSlug{Slug: theme.Slug},
			}); err != nil || !ok {
				t.Fatalf("save default theme slug %q = %v/%v, want true/nil", theme.Slug, ok, err)
			}
		})
	}

	// Reproduce issue #29 through the canonical Layer 228 wire constructor,
	// not only by invoking the typed handler directly.
	canonical := &tg.AccountInstallThemeRequest{}
	canonical.SetDark(true)
	canonical.SetTheme(&tg.InputTheme{ID: defaults[0].ID, AccessHash: defaults[0].AccessHash})
	canonical.SetFormat("android")
	var body bin.Buffer
	if err := canonical.Encode(&body); err != nil {
		t.Fatalf("encode canonical installTheme: %v", err)
	}
	var authKeyID [8]byte
	authKeyID[0] = 2
	encoded, err := r.Dispatch(ctx, authKeyID, 1001, &body)
	if err != nil {
		t.Fatalf("canonical installTheme dispatch: %v", err)
	}
	var result bin.Buffer
	if err := encoded.Encode(&result); err != nil {
		t.Fatalf("encode canonical installTheme result: %v", err)
	}
	if id, err := result.ID(); err != nil || id != tg.BoolTrueTypeID {
		t.Fatalf("canonical installTheme result id = %#x err=%v, want boolTrue", id, err)
	}

	installed, err := r.deps.Themes.ListInstalled(ctx, userID)
	if err != nil {
		t.Fatalf("list custom installs after default signals: %v", err)
	}
	if len(installed) != 0 {
		t.Fatalf("default signals created %d custom installs, want 0", len(installed))
	}

	forged := defaults[0]
	badInstall := &tg.AccountInstallThemeRequest{}
	badInstall.SetTheme(&tg.InputTheme{ID: forged.ID, AccessHash: forged.AccessHash + 1})
	badInstall.SetFormat("android")
	if _, err := r.onAccountInstallTheme(ctx, badInstall); !tgerr.Is(err, "THEME_INVALID") {
		t.Fatalf("install forged default reference err = %v, want THEME_INVALID", err)
	}
}

// TestAccountGetThemesIncludesUserThemes verifies getThemes cross-device sync: it returns the
// built-in default themes (is_default=true, for the emoji preview strip) + the current user's
// self-created custom themes (is_default=false, creator=true); a stable hash -> NotModified, a
// changed set -> re-fetch.
func TestAccountGetThemesIncludesUserThemes(t *testing.T) {
	const userID = 1000004
	ctx := WithUserID(context.Background(), userID)
	files := &fakeFiles{docs: map[int64]domain.Document{
		321: {ID: 321, AccessHash: 3, DCID: 2, MimeType: "application/x-tgtheme-android", Size: 100},
	}}
	r := newThemeRouter(t, files)

	// Initial: only the default themes.
	first, err := r.onAccountGetThemes(ctx, &tg.AccountGetThemesRequest{Format: "android", Hash: 0})
	if err != nil {
		t.Fatalf("getThemes initial err = %v", err)
	}
	base, ok := first.(*tg.AccountThemes)
	if !ok {
		t.Fatalf("getThemes initial = %T, want *tg.AccountThemes", first)
	}
	defaultCount := len(base.Themes)
	if defaultCount == 0 {
		t.Fatalf("getThemes returned no default themes")
	}
	// Same hash echoed back -> NotModified.
	if again, _ := r.onAccountGetThemes(ctx, &tg.AccountGetThemesRequest{Hash: base.Hash}); func() bool {
		_, ok := again.(*tg.AccountThemesNotModified)
		return !ok
	}() {
		t.Fatalf("getThemes with matching hash should be NotModified")
	}

	// Create a custom theme.
	create := &tg.AccountCreateThemeRequest{Title: "Synced"}
	create.SetDocument(&tg.InputDocument{ID: 321, AccessHash: 3})
	created, err := r.onAccountCreateTheme(ctx, create)
	if err != nil {
		t.Fatalf("createTheme err = %v", err)
	}

	// Now getThemes must include this custom theme, and the hash changes.
	second, err := r.onAccountGetThemes(ctx, &tg.AccountGetThemesRequest{Hash: base.Hash})
	if err != nil {
		t.Fatalf("getThemes after create err = %v", err)
	}
	merged, ok := second.(*tg.AccountThemes)
	if !ok {
		t.Fatalf("getThemes after create = %T, want *tg.AccountThemes (hash changed)", second)
	}
	if len(merged.Themes) != defaultCount+1 {
		t.Fatalf("getThemes themes = %d, want %d (defaults + 1 custom)", len(merged.Themes), defaultCount+1)
	}
	var foundCustom bool
	for _, th := range merged.Themes {
		if th.ID == created.ID {
			foundCustom = true
			if th.GetDefault() {
				t.Fatalf("custom theme is_default = true, want false (must not pollute emoji strip)")
			}
			if !th.GetCreator() {
				t.Fatalf("custom theme creator = false, want true for owner")
			}
		}
	}
	if !foundCustom {
		t.Fatalf("getThemes did not include the user's created theme id %d", created.ID)
	}
}

// TestAccountGetThemesTDesktopExcludesDocumentlessDefaults verifies: for format="tdesktop" the
// synthesized documentless emoji default themes are not sent (otherwise clicking one in the
// TDesktop cloud-themes grid would show lng_theme_no_desktop), while format="android" still
// sends them for the emoji preview strip.
func TestAccountGetThemesTDesktopExcludesDocumentlessDefaults(t *testing.T) {
	const userID = 1000007
	ctx := WithUserID(context.Background(), userID)
	r := newThemeRouter(t, &fakeFiles{})

	android, err := r.onAccountGetThemes(ctx, &tg.AccountGetThemesRequest{Format: "android", Hash: 0})
	if err != nil {
		t.Fatalf("getThemes android err = %v", err)
	}
	a, ok := android.(*tg.AccountThemes)
	if !ok || len(a.Themes) == 0 {
		t.Fatalf("getThemes android = %#v, want non-empty default themes", android)
	}

	desktop, err := r.onAccountGetThemes(ctx, &tg.AccountGetThemesRequest{Format: "tdesktop", Hash: 0})
	if err != nil {
		t.Fatalf("getThemes tdesktop err = %v", err)
	}
	switch d := desktop.(type) {
	case *tg.AccountThemes:
		for _, th := range d.Themes {
			if _, hasDoc := th.GetDocument(); !hasDoc {
				t.Fatalf("tdesktop getThemes returned documentless theme id=%d emoticon=%q, want only document-backed cloud themes", th.ID, func() string { e, _ := th.GetEmoticon(); return e }())
			}
		}
	case *tg.AccountThemesNotModified:
		// The stable hash for an empty set (a brand-new account with no self-created themes)
		// differs from 0, so the first hash=0 will not hit; returning NotModified here would
		// therefore indicate a broken hash implementation.
		t.Fatalf("tdesktop getThemes with hash=0 = NotModified, want themes")
	default:
		t.Fatalf("tdesktop getThemes = %T, want *tg.AccountThemes", desktop)
	}
}

func TestAccountGetThemesIOSProjectsOpaqueARGBAndRefreshesByContent(t *testing.T) {
	const userID = 1000008
	r := newThemeRouter(t, &fakeFiles{})
	iosCtx := WithClientInfo(
		WithUserID(context.Background(), userID),
		ClientInfo{Type: ClientTypeIOS, AppVersion: "12.9.2 (10000)"},
	)

	first, err := r.onAccountGetThemes(iosCtx, &tg.AccountGetThemesRequest{Format: "ios"})
	if err != nil {
		t.Fatalf("getThemes ios err = %v", err)
	}
	themes, ok := first.(*tg.AccountThemes)
	if !ok || len(themes.Themes) == 0 {
		t.Fatalf("getThemes ios = %#v, want non-empty themes", first)
	}
	assertThemeAccentColorsOpaque(t, themes.Themes)

	source := tdesktop.DefaultThemeList()
	sourceSettings, _ := source[0].GetSettings()
	projectedSettings, _ := themes.Themes[0].GetSettings()
	if got, want := uint32(int32(projectedSettings[0].AccentColor))&0x00ffffff, uint32(sourceSettings[0].AccentColor); got != want {
		t.Fatalf("projected RGB = %#06x, want source RGB %#06x", got, want)
	}
	if uint32(int32(sourceSettings[0].AccentColor))>>24 != 0 {
		t.Fatalf("source Android/TDesktop accent unexpectedly changed to ARGB: %#08x", uint32(int32(sourceSettings[0].AccentColor)))
	}

	again, err := r.onAccountGetThemes(iosCtx, &tg.AccountGetThemesRequest{Format: "ios", Hash: themes.Hash})
	if err != nil {
		t.Fatalf("getThemes ios matching hash err = %v", err)
	}
	if _, ok := again.(*tg.AccountThemesNotModified); !ok {
		t.Fatalf("getThemes ios matching hash = %T, want notModified", again)
	}
}

func TestAccountGetChatThemesIOSInvalidatesTransparentCatalogCache(t *testing.T) {
	const userID = 1000009
	r := newThemeRouter(t, &fakeFiles{})
	base, ok := tdesktop.ChatThemes(0).(*tg.AccountThemes)
	if !ok {
		t.Fatalf("base ChatThemes = %T, want *tg.AccountThemes", tdesktop.ChatThemes(0))
	}

	iosCtx := WithClientInfo(
		WithUserID(context.Background(), userID),
		ClientInfo{Type: ClientTypeIOS, AppVersion: "12.9.2 (10000)"},
	)
	projected, err := r.onAccountGetChatThemes(iosCtx, base.Hash)
	if err != nil {
		t.Fatalf("getChatThemes ios err = %v", err)
	}
	themes, ok := projected.(*tg.AccountThemes)
	if !ok {
		t.Fatalf("getChatThemes ios with old hash = %T, want refreshed themes", projected)
	}
	if themes.Hash == base.Hash {
		t.Fatalf("iOS projected hash = old catalog hash %d, want cache invalidation", themes.Hash)
	}
	assertThemeAccentColorsOpaque(t, themes.Themes)

	again, err := r.onAccountGetChatThemes(iosCtx, themes.Hash)
	if err != nil {
		t.Fatalf("getChatThemes ios matching hash err = %v", err)
	}
	if _, ok := again.(*tg.AccountThemesNotModified); !ok {
		t.Fatalf("getChatThemes ios matching hash = %T, want notModified", again)
	}

	androidCtx := WithClientInfo(WithUserID(context.Background(), userID), ClientInfo{Type: ClientTypeAndroid})
	android, err := r.onAccountGetChatThemes(androidCtx, base.Hash)
	if err != nil {
		t.Fatalf("getChatThemes android old hash err = %v", err)
	}
	if _, ok := android.(*tg.AccountThemesNotModified); !ok {
		t.Fatalf("getChatThemes android old hash = %T, want unchanged notModified", android)
	}
}

func TestThemesListHashIncludesVisibleContentAndIgnoresOrder(t *testing.T) {
	first := tg.Theme{ID: 1, AccessHash: 11, Slug: "one", Title: "One"}
	first.SetSettings([]tg.ThemeSettings{{
		BaseTheme:   &tg.BaseThemeClassic{},
		AccentColor: 0x29b071,
	}})
	second := tg.Theme{ID: 2, AccessHash: 22, Slug: "two", Title: "Two"}

	hashA, err := themesListHash([]tg.Theme{first, second})
	if err != nil {
		t.Fatalf("themesListHash initial: %v", err)
	}
	hashReordered, err := themesListHash([]tg.Theme{second, first})
	if err != nil {
		t.Fatalf("themesListHash reordered: %v", err)
	}
	if hashA != hashReordered {
		t.Fatalf("hash changed with order: %d != %d", hashA, hashReordered)
	}

	changed := first
	changedSettings, _ := changed.GetSettings()
	changedSettings = append([]tg.ThemeSettings(nil), changedSettings...)
	changedSettings[0].AccentColor = 0x329ed7
	changed.SetSettings(changedSettings)
	hashChanged, err := themesListHash([]tg.Theme{changed, second})
	if err != nil {
		t.Fatalf("themesListHash changed: %v", err)
	}
	if hashChanged == hashA {
		t.Fatalf("hash did not change after accent update: %d", hashA)
	}
}

func TestAccountSingleThemeResponsesUseIOSARGBProjection(t *testing.T) {
	const userID = 1000011
	r := newThemeRouter(t, &fakeFiles{})
	androidCtx := WithClientInfo(WithUserID(context.Background(), userID), ClientInfo{Type: ClientTypeAndroid})
	iosCtx := WithClientInfo(WithUserID(context.Background(), userID), ClientInfo{Type: ClientTypeIOS})

	input := tg.InputThemeSettings{
		BaseTheme:   &tg.BaseThemeDay{},
		AccentColor: 0x3997d3,
	}
	input.SetOutboxAccentColor(0x4cb064)
	create := &tg.AccountCreateThemeRequest{Title: "Cross-platform"}
	create.SetSettings([]tg.InputThemeSettings{input})
	created, err := r.onAccountCreateTheme(androidCtx, create)
	if err != nil {
		t.Fatalf("create Android theme: %v", err)
	}
	androidSettings, _ := created.GetSettings()
	if color := uint32(int32(androidSettings[0].AccentColor)); color>>24 != 0 {
		t.Fatalf("Android create response accent = %#08x, want unchanged RGB24", color)
	}

	got, err := r.onAccountGetTheme(iosCtx, &tg.AccountGetThemeRequest{
		Format: "ios",
		Theme:  &tg.InputTheme{ID: created.ID, AccessHash: created.AccessHash},
	})
	if err != nil {
		t.Fatalf("get iOS theme: %v", err)
	}
	assertThemeAccentColorsOpaque(t, []tg.Theme{*got})
	iosSettings, _ := got.GetSettings()
	if color := uint32(int32(iosSettings[0].AccentColor)); color != 0xff3997d3 {
		t.Fatalf("iOS getTheme accent = %#08x, want 0xff3997d3", color)
	}
	if color, ok := iosSettings[0].GetOutboxAccentColor(); !ok || uint32(int32(color)) != 0xff4cb064 {
		t.Fatalf("iOS getTheme outbox accent = %#08x ok=%v, want 0xff4cb064", uint32(int32(color)), ok)
	}

	iosCreate := &tg.AccountCreateThemeRequest{Title: "Created on iOS"}
	iosCreate.SetSettings([]tg.InputThemeSettings{input})
	createdOnIOS, err := r.onAccountCreateTheme(iosCtx, iosCreate)
	if err != nil {
		t.Fatalf("create iOS theme: %v", err)
	}
	assertThemeAccentColorsOpaque(t, []tg.Theme{*createdOnIOS})

	updatedInput := tg.InputThemeSettings{
		BaseTheme:   &tg.BaseThemeTinted{},
		AccentColor: 0x8660ad,
	}
	update := &tg.AccountUpdateThemeRequest{
		Format: "ios",
		Theme:  &tg.InputTheme{ID: created.ID, AccessHash: created.AccessHash},
	}
	update.SetSettings([]tg.InputThemeSettings{updatedInput})
	updated, err := r.onAccountUpdateTheme(iosCtx, update)
	if err != nil {
		t.Fatalf("update iOS theme: %v", err)
	}
	assertThemeAccentColorsOpaque(t, []tg.Theme{*updated})
	updatedSettings, _ := updated.GetSettings()
	if color := uint32(int32(updatedSettings[0].AccentColor)); color != 0xff8660ad {
		t.Fatalf("iOS updateTheme accent = %#08x, want 0xff8660ad", color)
	}
}

func assertThemeAccentColorsOpaque(t *testing.T, themes []tg.Theme) {
	t.Helper()
	for _, theme := range themes {
		settings, ok := theme.GetSettings()
		if !ok {
			continue
		}
		for i := range settings {
			if color := uint32(int32(settings[i].AccentColor)); color>>24 == 0 {
				t.Fatalf("theme %d settings[%d] accent remains transparent: %#08x", theme.ID, i, color)
			}
			if color, ok := settings[i].GetOutboxAccentColor(); ok && uint32(int32(color))>>24 == 0 {
				t.Fatalf("theme %d settings[%d] outbox accent remains transparent: %#08x", theme.ID, i, uint32(int32(color)))
			}
		}
	}
}

// TestAccountCreateThemeAccentSettingsEncode verifies an accent theme with settings round-trips and encodes with a non-empty base_theme.
func TestAccountCreateThemeAccentSettingsEncode(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000003)
	r := newThemeRouter(t, &fakeFiles{})

	settings := tg.InputThemeSettings{BaseTheme: &tg.BaseThemeDay{}, AccentColor: 0x3997d3}
	settings.SetMessageColors([]int{0xd4f1ff, 0xb9e4ff})
	wp := tg.WallPaperSettings{}
	wp.SetBackgroundColor(0xd4f1ff)
	wp.SetSecondBackgroundColor(0xb9e4ff)
	settings.SetWallpaperSettings(wp)
	settings.SetWallpaper(&tg.InputWallPaperNoFile{})

	create := &tg.AccountCreateThemeRequest{Title: "Accent"}
	create.SetSettings([]tg.InputThemeSettings{settings})
	th, err := r.onAccountCreateTheme(ctx, create)
	if err != nil {
		t.Fatalf("createTheme accent err = %v", err)
	}
	out, ok := th.GetSettings()
	if !ok || len(out) != 1 {
		t.Fatalf("created accent theme settings = %#v ok=%v, want 1", out, ok)
	}
	if _, isDay := out[0].BaseTheme.(*tg.BaseThemeDay); !isDay {
		t.Fatalf("settings[0].base_theme = %T, want BaseThemeDay", out[0].BaseTheme)
	}
	if out[0].AccentColor != 0x3997d3 {
		t.Fatalf("accent color = %#x, want 0x3997d3", out[0].AccentColor)
	}
	mustEncodeTheme(t, th)
}
