package domain

import "errors"

// Errors related to custom cloud themes (account.createTheme etc.).
var (
	ErrThemeNotFound      = errors.New("theme not found")
	ErrThemeInvalid       = errors.New("theme invalid")
	ErrThemeSlugTaken     = errors.New("theme slug taken")
	ErrThemeFormatInvalid = errors.New("theme format invalid")
)

// ThemeBaseKind corresponds to the five variants of TL baseTheme (classic/day/night/tinted/arctic).
// The client selects the matching ThemeSettings based on the current base theme.
type ThemeBaseKind string

const (
	ThemeBaseClassic ThemeBaseKind = "classic"
	ThemeBaseDay     ThemeBaseKind = "day"
	ThemeBaseNight   ThemeBaseKind = "night"
	ThemeBaseTinted  ThemeBaseKind = "tinted"
	ThemeBaseArctic  ThemeBaseKind = "arctic"
)

// ThemeWallpaperSpec is a pure-gradient wallpaper within a theme setting (corresponding to wallPaperNoFile + wallPaperSettings).
// Custom cloud themes use only solid-color gradients (no download), consistent with the static catalog.
type ThemeWallpaperSpec struct {
	BackgroundColors []int  `json:"background_colors,omitempty"` // at most 4
	Intensity        int    `json:"intensity,omitempty"`
	Rotation         int    `json:"rotation,omitempty"`
	Blur             bool   `json:"blur,omitempty"`
	Motion           bool   `json:"motion,omitempty"`
	Emoticon         string `json:"emoticon,omitempty"`
	Dark             bool   `json:"dark,omitempty"`
}

// WallpaperSettings is the domain-only settings for a channel/private-chat wallpaper. The Has* fields keep TL
// conditional-flag semantics so that legally valid values like black (0) are not lost on persistence.
type WallpaperSettings struct {
	Blur                     bool   `json:"blur,omitempty"`
	Motion                   bool   `json:"motion,omitempty"`
	HasBackgroundColor       bool   `json:"has_background_color,omitempty"`
	BackgroundColor          int    `json:"background_color,omitempty"`
	HasSecondBackgroundColor bool   `json:"has_second_background_color,omitempty"`
	SecondBackgroundColor    int    `json:"second_background_color,omitempty"`
	HasThirdBackgroundColor  bool   `json:"has_third_background_color,omitempty"`
	ThirdBackgroundColor     int    `json:"third_background_color,omitempty"`
	HasFourthBackgroundColor bool   `json:"has_fourth_background_color,omitempty"`
	FourthBackgroundColor    int    `json:"fourth_background_color,omitempty"`
	HasIntensity             bool   `json:"has_intensity,omitempty"`
	Intensity                int    `json:"intensity,omitempty"`
	HasRotation              bool   `json:"has_rotation,omitempty"`
	Rotation                 int    `json:"rotation,omitempty"`
	HasEmoticon              bool   `json:"has_emoticon,omitempty"`
	Emoticon                 string `json:"emoticon,omitempty"`
}

// Empty reports whether no explicit wallpaper rendering settings are present.
func (s WallpaperSettings) Empty() bool {
	return !s.Blur &&
		!s.Motion &&
		!s.HasBackgroundColor &&
		!s.HasSecondBackgroundColor &&
		!s.HasThirdBackgroundColor &&
		!s.HasFourthBackgroundColor &&
		!s.HasIntensity &&
		!s.HasRotation &&
		!s.HasEmoticon
}

// Wallpaper is the domain-only description of a channel/private-chat's current wallpaper. NoFile=true corresponds to
// wallPaperNoFile; otherwise ID+AccessHash/Slug references a catalog/document wallpaper.
type Wallpaper struct {
	ID         int64             `json:"id,omitempty"`
	AccessHash int64             `json:"access_hash,omitempty"`
	Slug       string            `json:"slug,omitempty"`
	NoFile     bool              `json:"no_file,omitempty"`
	Default    bool              `json:"default,omitempty"`
	Pattern    bool              `json:"pattern,omitempty"`
	Dark       bool              `json:"dark,omitempty"`
	Settings   WallpaperSettings `json:"settings,omitempty"`
}

// CloneWallpaperPtr returns a detached wallpaper pointer.
func CloneWallpaperPtr(in *Wallpaper) *Wallpaper {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// WallpaperEqual compares optional wallpapers by value.
func WallpaperEqual(a, b *Wallpaper) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ThemeSettingsSpec is the color scheme under one base theme (corresponding to themeSettings/inputThemeSettings).
// BaseTheme and AccentColor are required (the TL encoder requires base_theme to be non-empty).
type ThemeSettingsSpec struct {
	BaseTheme             ThemeBaseKind       `json:"base_theme"`
	AccentColor           int                 `json:"accent_color"`
	OutboxAccentColor     int                 `json:"outbox_accent_color,omitempty"`
	HasOutboxAccent       bool                `json:"has_outbox_accent,omitempty"`
	MessageColors         []int               `json:"message_colors,omitempty"`
	MessageColorsAnimated bool                `json:"message_colors_animated,omitempty"`
	Wallpaper             *ThemeWallpaperSpec `json:"wallpaper,omitempty"`
}

// Theme is a persisted custom cloud theme. document_id soft-references documents (no hard foreign key);
// settings is non-empty only for accent themes; a full .attheme theme is downloaded via DocumentID.
type Theme struct {
	ID            int64
	AccessHash    int64
	CreatorUserID int64
	Slug          string
	Title         string
	Emoticon      string
	ForChat       bool
	DocumentID    int64
	Settings      []ThemeSettingsSpec
	InstallsCount int
	CreatedAt     int64 // unix seconds
}

// Clone deep-copies the slice fields so the in-memory store never exposes internal references.
func (t Theme) Clone() Theme {
	out := t
	out.Settings = cloneThemeSettings(t.Settings)
	return out
}

func cloneThemeSettings(in []ThemeSettingsSpec) []ThemeSettingsSpec {
	if in == nil {
		return nil
	}
	out := make([]ThemeSettingsSpec, len(in))
	for i, s := range in {
		out[i] = s
		out[i].MessageColors = append([]int(nil), s.MessageColors...)
		if s.Wallpaper != nil {
			wp := *s.Wallpaper
			wp.BackgroundColors = append([]int(nil), s.Wallpaper.BackgroundColors...)
			out[i].Wallpaper = &wp
		}
	}
	return out
}

// IsCreator reports whether userID created the theme (deciding whether a subsequent client upload uses create or update).
func (t Theme) IsCreator(userID int64) bool {
	return t.CreatorUserID != 0 && t.CreatorUserID == userID
}

// ThemeRef references a theme: by id+access_hash (inputTheme) or slug (inputThemeSlug, a deep link).
type ThemeRef struct {
	ID         int64
	AccessHash int64
	Slug       string
}

// IsZero reports whether the reference is empty (neither id nor slug).
func (r ThemeRef) IsZero() bool { return r.ID == 0 && r.Slug == "" }

// ThemeSpec is the input of createTheme. An empty Slug means the server assigns one automatically.
type ThemeSpec struct {
	CreatorUserID int64
	Slug          string
	Title         string
	Emoticon      string
	ForChat       bool
	DocumentID    int64
	Settings      []ThemeSettingsSpec
}

// ThemeUpdate is a partial update of updateTheme; a nil field means unchanged.
type ThemeUpdate struct {
	Slug       *string
	Title      *string
	DocumentID *int64
	Settings   *[]ThemeSettingsSpec
}
