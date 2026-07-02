package rpc

import (
	"context"
	"strings"

	"github.com/gotd/td/tg"
)

// registerLangpack 注册 langpack.* RPC handler。
//
// 老客户端（DrKLO）发的是不带 lang_pack 参数的旧构造器，已由 layerwire 入站升级为 227
// 形态并把 lang_pack 置空；故这里 lang_pack 为空时回退到按 client 信息派生（langPackFromClient），
// 与历史 handleLegacyLangpack* 的行为一致。
func (r *Router) registerLangpack(d *tg.ServerDispatcher) {
	d.OnLangpackGetLanguages(func(ctx context.Context, langPack string) ([]tg.LangPackLanguage, error) {
		return r.langpackLanguages(ctx, langPack), nil
	})
	d.OnLangpackGetLanguage(func(ctx context.Context, req *tg.LangpackGetLanguageRequest) (*tg.LangPackLanguage, error) {
		if req == nil {
			return nil, inputConstructorInvalidErr()
		}
		lang := r.langpackLanguage(ctx, req.LangPack, req.LangCode)
		return &lang, nil
	})
	d.OnLangpackGetLangPack(func(ctx context.Context, req *tg.LangpackGetLangPackRequest) (*tg.LangPackDifference, error) {
		langPack := langPackOrClient(ctx, req.LangPack)
		if r.deps.LangPack == nil {
			return &tg.LangPackDifference{LangCode: req.LangCode}, nil
		}
		pack, err := r.deps.LangPack.GetLangPack(ctx, langPack, req.LangCode)
		if err != nil {
			return nil, internalErr()
		}
		return tgLangPackDifference(pack), nil
	})
	d.OnLangpackGetDifference(func(ctx context.Context, req *tg.LangpackGetDifferenceRequest) (*tg.LangPackDifference, error) {
		if r.deps.LangPack == nil {
			return &tg.LangPackDifference{LangCode: req.LangCode, FromVersion: req.FromVersion}, nil
		}
		pack, err := r.deps.LangPack.GetDifference(ctx, langPackOrClient(ctx, req.LangPack), req.LangCode, req.FromVersion)
		if err != nil {
			return nil, internalErr()
		}
		return tgLangPackDifference(pack), nil
	})
	d.OnLangpackGetStrings(func(ctx context.Context, req *tg.LangpackGetStringsRequest) ([]tg.LangPackStringClass, error) {
		if r.deps.LangPack == nil {
			return nil, nil
		}
		pack, err := r.deps.LangPack.GetStrings(ctx, langPackOrClient(ctx, req.LangPack), req.LangCode, req.Keys)
		if err != nil {
			return nil, internalErr()
		}
		return tgLangPackStrings(pack.Strings), nil
	})
}

// langPackOrClient 返回请求里的 lang_pack；为空（老客户端经 layerwire 升级而来）时按 client 派生。
func langPackOrClient(ctx context.Context, langPack string) string {
	if langPack != "" {
		return langPack
	}
	return langPackFromClient(ctx)
}

func (r *Router) langpackLanguage(ctx context.Context, langPack, langCode string) tg.LangPackLanguage {
	if langCode == "" {
		if info, ok := ClientInfoFrom(ctx); ok && info.LangCode != "" {
			langCode = info.LangCode
		} else {
			langCode = "en"
		}
	}
	langCode = strings.ToLower(langCode)
	languages := r.langpackLanguages(ctx, langPack)
	for _, lang := range languages {
		if strings.ToLower(lang.LangCode) == langCode {
			return lang
		}
	}
	for _, lang := range languages {
		if strings.ToLower(lang.PluralCode) == langCode {
			return lang
		}
	}
	return languages[0]
}

func (r *Router) langpackLanguages(ctx context.Context, langPack string) []tg.LangPackLanguage {
	if langPack == "" {
		langPack = langPackFromClient(ctx)
	}
	langPack = strings.ToLower(langPack)
	languages := []tg.LangPackLanguage{
		{
			Official:        true,
			Name:            "English",
			NativeName:      "English",
			LangCode:        "en",
			PluralCode:      "en",
			StringsCount:    0,
			TranslatedCount: 0,
			TranslationsURL: "",
		},
		{
			Official:        true,
			Name:            "Chinese (Simplified)",
			NativeName:      "Chinese (Simplified)",
			LangCode:        "zh-hans",
			PluralCode:      "zh",
			StringsCount:    0,
			TranslatedCount: 0,
			TranslationsURL: "",
		},
	}
	if langPack == "android" {
		languages = append(languages, tg.LangPackLanguage{
			Official:        true,
			Rtl:             true,
			Name:            "Persian",
			NativeName:      "فارسی",
			LangCode:        "fa",
			PluralCode:      "fa",
			StringsCount:    11002,
			TranslatedCount: 11002,
			TranslationsURL: "",
		})
	}
	return languages
}

func langPackFromClient(ctx context.Context) string {
	info, ok := ClientInfoFrom(ctx)
	if !ok {
		return "tdesktop"
	}
	if info.LangPack != "" {
		return info.LangPack
	}
	switch info.ClientType() {
	case ClientTypeAndroid:
		return string(ClientTypeAndroid)
	case ClientTypeTDesktop:
		return string(ClientTypeTDesktop)
	}
	client := strings.ToLower(info.DeviceModel + " " + info.SystemVersion + " " + info.AppVersion)
	if strings.Contains(client, "android") {
		return "android"
	}
	return "tdesktop"
}
