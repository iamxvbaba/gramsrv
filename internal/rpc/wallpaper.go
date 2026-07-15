package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

func domainWallpaperFromSetChatWallPaper(req *tg.MessagesSetChatWallPaperRequest) (*domain.Wallpaper, error) {
	if req == nil || req.Revert {
		return nil, nil
	}
	input, ok := req.GetWallpaper()
	if !ok {
		return nil, nil
	}
	settings, hasSettings := req.GetSettings()
	return domainWallpaperFromInput(input, settings, hasSettings)
}

func domainWallpaperFromInput(input tg.InputWallPaperClass, settings tg.WallPaperSettings, hasSettings bool) (*domain.Wallpaper, error) {
	switch in := input.(type) {
	case *tg.InputWallPaperNoFile:
		wp := &domain.Wallpaper{ID: in.ID, NoFile: true}
		if hasSettings {
			wp.Settings = domainWallpaperSettings(settings)
		}
		return wp, nil
	case *tg.InputWallPaper, *tg.InputWallPaperSlug:
		found, ok := tdesktop.LookupWallPaper(input)
		if !ok {
			return nil, tgerr400("WALLPAPER_INVALID")
		}
		wp := domainWallpaperFromTG(found)
		if wp == nil {
			return nil, tgerr400("WALLPAPER_INVALID")
		}
		if hasSettings {
			wp.Settings = domainWallpaperSettings(settings)
		}
		return wp, nil
	default:
		return nil, inputConstructorInvalidErr()
	}
}

func domainWallpaperFromTG(input tg.WallPaperClass) *domain.Wallpaper {
	switch wp := input.(type) {
	case *tg.WallPaperNoFile:
		out := &domain.Wallpaper{
			ID:       wp.ID,
			NoFile:   true,
			Default:  wp.Default,
			Dark:     wp.Dark,
			Settings: domainWallpaperSettingsFromOptional(wp.GetSettings()),
		}
		return out
	case *tg.WallPaper:
		out := &domain.Wallpaper{
			ID:         wp.ID,
			AccessHash: wp.AccessHash,
			Slug:       wp.Slug,
			Default:    wp.Default,
			Pattern:    wp.Pattern,
			Dark:       wp.Dark,
			Settings:   domainWallpaperSettingsFromOptional(wp.GetSettings()),
		}
		return out
	default:
		return nil
	}
}

func tgWallpaper(wp *domain.Wallpaper) tg.WallPaperClass {
	if wp == nil {
		return nil
	}
	if wp.NoFile {
		out := &tg.WallPaperNoFile{ID: wp.ID}
		out.SetDefault(wp.Default)
		out.SetDark(wp.Dark)
		if !wp.Settings.Empty() {
			out.SetSettings(tgWallpaperSettings(wp.Settings))
		}
		return out
	}
	if wp.ID != 0 && wp.AccessHash != 0 {
		if found, ok := tdesktop.LookupWallPaper(&tg.InputWallPaper{ID: wp.ID, AccessHash: wp.AccessHash}); ok {
			return tgWallpaperWithSettings(found, wp.Settings)
		}
	}
	if wp.Slug != "" {
		if found, ok := tdesktop.LookupWallPaper(&tg.InputWallPaperSlug{Slug: wp.Slug}); ok {
			return tgWallpaperWithSettings(found, wp.Settings)
		}
	}
	out := &tg.WallPaperNoFile{ID: wp.ID}
	out.SetDefault(wp.Default)
	out.SetDark(wp.Dark)
	if !wp.Settings.Empty() {
		out.SetSettings(tgWallpaperSettings(wp.Settings))
	}
	return out
}

func tgWallpaperWithSettings(input tg.WallPaperClass, settings domain.WallpaperSettings) tg.WallPaperClass {
	switch wp := input.(type) {
	case *tg.WallPaperNoFile:
		out := *wp
		if !settings.Empty() {
			out.SetSettings(tgWallpaperSettings(settings))
		}
		return &out
	case *tg.WallPaper:
		out := *wp
		if !settings.Empty() {
			out.SetSettings(tgWallpaperSettings(settings))
		}
		return &out
	default:
		return input
	}
}

func domainWallpaperSettingsFromOptional(settings tg.WallPaperSettings, ok bool) domain.WallpaperSettings {
	if !ok {
		return domain.WallpaperSettings{}
	}
	return domainWallpaperSettings(settings)
}

func domainWallpaperSettings(in tg.WallPaperSettings) domain.WallpaperSettings {
	out := domain.WallpaperSettings{
		Blur:   in.Blur || in.GetBlur(),
		Motion: in.Motion || in.GetMotion(),
	}
	if v, ok := in.GetBackgroundColor(); ok {
		out.HasBackgroundColor = true
		out.BackgroundColor = v
	} else if in.BackgroundColor != 0 {
		out.HasBackgroundColor = true
		out.BackgroundColor = in.BackgroundColor
	}
	if v, ok := in.GetSecondBackgroundColor(); ok {
		out.HasSecondBackgroundColor = true
		out.SecondBackgroundColor = v
	} else if in.SecondBackgroundColor != 0 {
		out.HasSecondBackgroundColor = true
		out.SecondBackgroundColor = in.SecondBackgroundColor
	}
	if v, ok := in.GetThirdBackgroundColor(); ok {
		out.HasThirdBackgroundColor = true
		out.ThirdBackgroundColor = v
	} else if in.ThirdBackgroundColor != 0 {
		out.HasThirdBackgroundColor = true
		out.ThirdBackgroundColor = in.ThirdBackgroundColor
	}
	if v, ok := in.GetFourthBackgroundColor(); ok {
		out.HasFourthBackgroundColor = true
		out.FourthBackgroundColor = v
	} else if in.FourthBackgroundColor != 0 {
		out.HasFourthBackgroundColor = true
		out.FourthBackgroundColor = in.FourthBackgroundColor
	}
	if v, ok := in.GetIntensity(); ok {
		out.HasIntensity = true
		out.Intensity = v
	} else if in.Intensity != 0 {
		out.HasIntensity = true
		out.Intensity = in.Intensity
	}
	if v, ok := in.GetRotation(); ok {
		out.HasRotation = true
		out.Rotation = v
	} else if in.Rotation != 0 {
		out.HasRotation = true
		out.Rotation = in.Rotation
	}
	if v, ok := in.GetEmoticon(); ok {
		out.HasEmoticon = true
		out.Emoticon = v
	} else if in.Emoticon != "" {
		out.HasEmoticon = true
		out.Emoticon = in.Emoticon
	}
	return out
}

func tgWallpaperSettings(in domain.WallpaperSettings) tg.WallPaperSettings {
	var out tg.WallPaperSettings
	out.SetBlur(in.Blur)
	out.SetMotion(in.Motion)
	if in.HasBackgroundColor {
		out.SetBackgroundColor(in.BackgroundColor)
	}
	if in.HasSecondBackgroundColor {
		out.SetSecondBackgroundColor(in.SecondBackgroundColor)
	}
	if in.HasThirdBackgroundColor {
		out.SetThirdBackgroundColor(in.ThirdBackgroundColor)
	}
	if in.HasFourthBackgroundColor {
		out.SetFourthBackgroundColor(in.FourthBackgroundColor)
	}
	if in.HasIntensity {
		out.SetIntensity(in.Intensity)
	}
	if in.HasRotation {
		out.SetRotation(in.Rotation)
	}
	if in.HasEmoticon {
		out.SetEmoticon(in.Emoticon)
	}
	return out
}

func tgUpdatePeerWallpaper(peer domain.Peer, wallpaper *domain.Wallpaper) *tg.UpdatePeerWallpaper {
	update := &tg.UpdatePeerWallpaper{Peer: tgPeer(peer)}
	if wp := tgWallpaper(wallpaper); wp != nil {
		update.SetWallpaper(wp)
	}
	return update
}
