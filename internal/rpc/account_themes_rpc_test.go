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

// TestAccountUploadThemeReturnsDocument 验证 uploadTheme 把上传文件落成可下载 Document 返回,
// 且校验 mime 前缀。
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

	// 非主题 mime 前缀拒绝。
	bad := &tg.AccountUploadThemeRequest{FileName: "x", MimeType: "image/png"}
	bad.File = &tg.InputFile{ID: 7, Parts: 1, Name: "x"}
	if _, err := r.onAccountUploadTheme(ctx, bad); !tgerr.Is(err, "THEME_MIME_INVALID") {
		t.Fatalf("bad mime err = %v, want THEME_MIME_INVALID", err)
	}
}

// TestAccountCreateThemeFullFlow 验证 createTheme 自动分配 slug、设 creator=true、回填 document,
// getTheme 按 slug 取回带 document;saveTheme/installTheme 返回 true。
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

	// getTheme by slug 取回,仍带 document。
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

	// 未知主题引用 → THEME_INVALID。
	bad := &tg.AccountGetThemeRequest{Format: "android", Theme: &tg.InputThemeSlug{Slug: "does-not-exist"}}
	if _, err := r.onAccountGetTheme(ctx, bad); !tgerr.Is(err, "THEME_INVALID") {
		t.Fatalf("getTheme unknown slug err = %v, want THEME_INVALID", err)
	}
}

// TestAccountGetThemesIncludesUserThemes 验证 getThemes 跨设备同步:返回内置默认主题
// (is_default=true,emoji 预览条用)+ 当前用户创建的自定义主题(is_default=false,creator=true);
// hash 稳定→NotModified,集合变化→重取。
func TestAccountGetThemesIncludesUserThemes(t *testing.T) {
	const userID = 1000004
	ctx := WithUserID(context.Background(), userID)
	files := &fakeFiles{docs: map[int64]domain.Document{
		321: {ID: 321, AccessHash: 3, DCID: 2, MimeType: "application/x-tgtheme-android", Size: 100},
	}}
	r := newThemeRouter(t, files)

	// 初始:仅默认主题。
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
	// 同 hash 回传 → NotModified。
	if again, _ := r.onAccountGetThemes(ctx, &tg.AccountGetThemesRequest{Hash: base.Hash}); func() bool {
		_, ok := again.(*tg.AccountThemesNotModified)
		return !ok
	}() {
		t.Fatalf("getThemes with matching hash should be NotModified")
	}

	// 创建一个自定义主题。
	create := &tg.AccountCreateThemeRequest{Title: "Synced"}
	create.SetDocument(&tg.InputDocument{ID: 321, AccessHash: 3})
	created, err := r.onAccountCreateTheme(ctx, create)
	if err != nil {
		t.Fatalf("createTheme err = %v", err)
	}

	// 现在 getThemes 必须包含该自定义主题,且 hash 变化。
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

// TestAccountGetThemesTDesktopExcludesDocumentlessDefaults 验证:对 format="tdesktop"
// 不下发无 document 的合成 emoji 默认主题(否则 TDesktop 云主题网格点击会弹
// lng_theme_no_desktop),而 format="android" 仍下发它们供 emoji 预览条使用。
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
		// 空集合(全新账号无自建主题)对应的稳定 hash 与 0 不同,首个 hash=0 不会命中,
		// 故这里若返回 NotModified 说明哈希实现异常。
		t.Fatalf("tdesktop getThemes with hash=0 = NotModified, want themes")
	default:
		t.Fatalf("tdesktop getThemes = %T, want *tg.AccountThemes", desktop)
	}
}

// TestAccountCreateThemeAccentSettingsEncode 验证带 settings 的 accent 主题往返且 base_theme 非空可编码。
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
