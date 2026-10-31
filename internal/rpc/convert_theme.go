package rpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// themesListHash computes a stable hash of a set of themes' full wire content.
// Sorting after encoding makes the return order irrelevant to the hash; any visible
// change to title, document, settings or colors drives the client to refetch.
func themesListHash(themes []tg.Theme) (int64, error) {
	encoded := make([][]byte, 0, len(themes))
	for i := range themes {
		theme := themes[i]
		if settings, ok := theme.GetSettings(); ok {
			copied := append([]tg.ThemeSettings(nil), settings...)
			for j := range copied {
				if colors, ok := copied[j].GetMessageColors(); ok {
					copied[j].SetMessageColors(append([]int(nil), colors...))
				}
			}
			theme.SetSettings(copied)
		}
		var b bin.Buffer
		if err := theme.Encode(&b); err != nil {
			return 0, fmt.Errorf("encode theme %d for hash: %w", theme.ID, err)
		}
		encoded = append(encoded, b.Copy())
	}
	sort.Slice(encoded, func(i, j int) bool {
		return bytes.Compare(encoded[i], encoded[j]) < 0
	})
	h := fnv.New64a()
	var size [8]byte
	for _, body := range encoded {
		binary.LittleEndian.PutUint64(size[:], uint64(len(body)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(body)
	}
	value := int64(h.Sum64() & 0x7fffffffffffffff)
	if value == 0 {
		value = 1
	}
	return value, nil
}

// themeRefFromInput converts tg.InputThemeClass (inputTheme / inputThemeSlug) into domain.ThemeRef.
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

// domainThemeSettingsFromInput parses the settings vector passed to createTheme/updateTheme.
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
		// flag bit1 gates both wallpaper and wallpaper_settings; the gradient lives in wallpaper_settings.
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

// tgTheme projects domain.Theme into tg.Theme. viewerUserID decides the creator
// flag (which decides whether the client's next upload goes through update instead
// of create). A fully custom theme resolves a downloadable Document from DocumentID.
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
