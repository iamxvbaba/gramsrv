package rpc

import (
	"context"
	"sort"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// themesListHash 计算一组主题的稳定哈希(服务端权威,客户端原样回传)。对 id 升序折叠,
// 与返回顺序无关;主题集合变化(用户新建/安装/卸载)即变,驱动客户端重取。
func themesListHash(themes []tg.Theme) int64 {
	ids := make([]int64, 0, len(themes))
	for _, t := range themes {
		ids = append(ids, t.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var h uint64 = 0xcbf29ce484222325 // FNV-1a 64 offset basis
	for _, id := range ids {
		h ^= uint64(id)
		h *= 0x100000001b3
	}
	return int64(h & 0x7fffffffffffffff)
}

// themeRefFromInput 把 tg.InputThemeClass(inputTheme / inputThemeSlug)转成 domain.ThemeRef。
func themeRefFromInput(in tg.InputThemeClass) (domain.ThemeRef, bool) {
	switch v := in.(type) {
	case *tg.InputTheme:
		if v.ID == 0 {
			return domain.ThemeRef{}, false
		}
		return domain.ThemeRef{ID: v.ID, AccessHash: v.AccessHash}, true
	case *tg.InputThemeSlug:
		if v.Slug == "" {
			return domain.ThemeRef{}, false
		}
		return domain.ThemeRef{Slug: v.Slug}, true
	default:
		return domain.ThemeRef{}, false
	}
}

func domainBaseThemeFromInput(in tg.BaseThemeClass) domain.ThemeBaseKind {
	switch in.(type) {
	case *tg.BaseThemeClassic:
		return domain.ThemeBaseClassic
	case *tg.BaseThemeDay:
		return domain.ThemeBaseDay
	case *tg.BaseThemeNight:
		return domain.ThemeBaseNight
	case *tg.BaseThemeTinted:
		return domain.ThemeBaseTinted
	case *tg.BaseThemeArctic:
		return domain.ThemeBaseArctic
	default:
		return domain.ThemeBaseClassic
	}
}

func tgBaseTheme(kind domain.ThemeBaseKind) tg.BaseThemeClass {
	switch kind {
	case domain.ThemeBaseDay:
		return &tg.BaseThemeDay{}
	case domain.ThemeBaseNight:
		return &tg.BaseThemeNight{}
	case domain.ThemeBaseTinted:
		return &tg.BaseThemeTinted{}
	case domain.ThemeBaseArctic:
		return &tg.BaseThemeArctic{}
	default:
		return &tg.BaseThemeClassic{}
	}
}

// domainThemeSettingsFromInput 解析 createTheme/updateTheme 传入的 settings 向量。
func domainThemeSettingsFromInput(in []tg.InputThemeSettings) []domain.ThemeSettingsSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.ThemeSettingsSpec, 0, len(in))
	for _, s := range in {
		spec := domain.ThemeSettingsSpec{
			BaseTheme:             domainBaseThemeFromInput(s.BaseTheme),
			AccentColor:           s.GetAccentColor(),
			MessageColorsAnimated: s.GetMessageColorsAnimated(),
		}
		if v, ok := s.GetOutboxAccentColor(); ok {
			spec.OutboxAccentColor = v
			spec.HasOutboxAccent = true
		}
		if v, ok := s.GetMessageColors(); ok {
			spec.MessageColors = append([]int(nil), v...)
		}
		// flag bit1 同时门控 wallpaper 与 wallpaper_settings;渐变色在 wallpaper_settings 里。
		if wp, ok := s.GetWallpaperSettings(); ok {
			spec.Wallpaper = domainThemeWallpaperFromInput(wp)
		}
		out = append(out, spec)
	}
	return out
}

func domainThemeWallpaperFromInput(wp tg.WallPaperSettings) *domain.ThemeWallpaperSpec {
	out := &domain.ThemeWallpaperSpec{Blur: wp.Blur, Motion: wp.Motion}
	colors := make([]int, 0, 4)
	if v, ok := wp.GetBackgroundColor(); ok {
		colors = append(colors, v)
	}
	if v, ok := wp.GetSecondBackgroundColor(); ok {
		colors = append(colors, v)
	}
	if v, ok := wp.GetThirdBackgroundColor(); ok {
		colors = append(colors, v)
	}
	if v, ok := wp.GetFourthBackgroundColor(); ok {
		colors = append(colors, v)
	}
	if len(colors) > 0 {
		out.BackgroundColors = colors
	}
	if v, ok := wp.GetIntensity(); ok {
		out.Intensity = v
	}
	if v, ok := wp.GetRotation(); ok {
		out.Rotation = v
	}
	if v, ok := wp.GetEmoticon(); ok {
		out.Emoticon = v
	}
	return out
}

// tgTheme 把 domain.Theme 投影为 tg.Theme。viewerUserID 决定 creator 标志(决定客户端
// 下次上传走 update 而非 create)。完整自定义主题靠 DocumentID 解析出可下载 Document。
func (r *Router) tgTheme(ctx context.Context, t domain.Theme, viewerUserID int64) *tg.Theme {
	out := &tg.Theme{
		ID:         t.ID,
		AccessHash: t.AccessHash,
		Slug:       t.Slug,
		Title:      t.Title,
	}
	if t.IsCreator(viewerUserID) {
		out.SetCreator(true)
	}
	if t.ForChat {
		out.SetForChat(true)
	}
	if t.Emoticon != "" {
		out.SetEmoticon(t.Emoticon)
	}
	out.SetInstallsCount(t.InstallsCount)
	if len(t.Settings) > 0 {
		out.SetSettings(tgThemeSettingsList(t.Settings))
	}
	if t.DocumentID != 0 && r.deps.Files != nil {
		if doc, ok, err := r.deps.Files.GetDocument(ctx, t.DocumentID); err == nil && ok {
			out.SetDocument(tgDocument(doc))
		}
	}
	return out
}

func tgThemeSettingsList(in []domain.ThemeSettingsSpec) []tg.ThemeSettings {
	out := make([]tg.ThemeSettings, 0, len(in))
	for _, s := range in {
		ts := tg.ThemeSettings{
			BaseTheme:   tgBaseTheme(s.BaseTheme),
			AccentColor: s.AccentColor,
		}
		if s.HasOutboxAccent {
			ts.SetOutboxAccentColor(s.OutboxAccentColor)
		}
		if len(s.MessageColors) > 0 {
			ts.SetMessageColors(append([]int(nil), s.MessageColors...))
		}
		if s.MessageColorsAnimated {
			ts.SetMessageColorsAnimated(true)
		}
		if s.Wallpaper != nil {
			ts.SetWallpaper(tgThemeWallpaper(s.Wallpaper))
		}
		out = append(out, ts)
	}
	return out
}

func tgThemeWallpaper(wp *domain.ThemeWallpaperSpec) tg.WallPaperClass {
	settings := tg.WallPaperSettings{Blur: wp.Blur, Motion: wp.Motion}
	colors := wp.BackgroundColors
	if len(colors) > 0 {
		settings.SetBackgroundColor(colors[0])
	}
	if len(colors) > 1 {
		settings.SetSecondBackgroundColor(colors[1])
	}
	if len(colors) > 2 {
		settings.SetThirdBackgroundColor(colors[2])
	}
	if len(colors) > 3 {
		settings.SetFourthBackgroundColor(colors[3])
	}
	if wp.Intensity != 0 {
		settings.SetIntensity(wp.Intensity)
	}
	if wp.Rotation != 0 {
		settings.SetRotation(wp.Rotation)
	}
	if wp.Emoticon != "" {
		settings.SetEmoticon(wp.Emoticon)
	}
	out := &tg.WallPaperNoFile{}
	if wp.Dark {
		out.SetDark(true)
	}
	out.SetSettings(settings)
	return out
}
