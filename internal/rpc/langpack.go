package rpc

import (
	"context"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// registerLangpack 注册 langpack.* RPC handler。
//
// 老客户端（DrKLO）发的是不带 lang_pack 参数的旧构造器，已由 gotdgen client overlay 入站升级为 canonical
// 形态并把 lang_pack 置空；故这里 lang_pack 为空时回退到按 client 信息派生（langPackFromClient），
// 与历史 handleLegacyLangpack* 的行为一致。
func (r *Router) registerLangpack(d *tlprofile.Dispatcher) {
	registerRPC[*tg.LangpackGetLanguagesRequest](d, tlprofile.SemanticMethodLangpackGetLanguages, func(ctx context.Context, layerRequest *tg.LangpackGetLanguagesRequest) (any, error) {
		langPack := layerRequest.
			LangPack

		languages, err := r.langpackLanguages(ctx, langPack)
		if err != nil {
			return nil, internalErr()
		}
		return languages, nil
	})
	registerRPC[*tg.LangpackGetLanguageRequest](d, tlprofile.SemanticMethodLangpackGetLanguage, func(ctx context.Context, req *tg.LangpackGetLanguageRequest) (any, error) {
		if req == nil {
			return nil, inputConstructorInvalidErr()
		}
		lang, err := r.langpackLanguage(ctx, req.LangPack, req.LangCode)
		if err != nil {
			return nil, err
		}
		return &lang, nil
	})
	registerRPC[*tg.LangpackGetLangPackRequest](d, tlprofile.SemanticMethodLangpackGetLangPack, func(ctx context.Context, req *tg.LangpackGetLangPackRequest) (any, error) {
		langPack := langPackOrClient(ctx, req.LangPack)
		_ = langPack
		if r.deps.LangPack == nil {
			return &tg.LangPackDifference{LangCode: req.LangCode}, nil
		}
		pack, err := r.deps.LangPack.GetLangPack(ctx, langPack, req.LangCode)
		if err != nil {
			return nil, internalErr()
		}
		return tgLangPackDifference(pack), nil
	})
	registerRPC[*tg.LangpackGetDifferenceRequest](d, tlprofile.SemanticMethodLangpackGetDifference, func(ctx context.Context, req *tg.LangpackGetDifferenceRequest) (any, error) {
		if r.deps.LangPack == nil {
			return &tg.LangPackDifference{LangCode: req.LangCode, FromVersion: req.FromVersion}, nil
		}
		pack, err := r.deps.LangPack.GetDifference(ctx, langPackOrClient(ctx, req.LangPack), req.LangCode, req.FromVersion)
		if err != nil {
			return nil, internalErr()
		}
		return tgLangPackDifference(pack), nil
	})
	registerRPC[*tg.LangpackGetStringsRequest](d, tlprofile.SemanticMethodLangpackGetStrings, func(ctx context.Context, req *tg.LangpackGetStringsRequest) (any, error) {
		if r.deps.LangPack == nil {
			return []tg.LangPackStringClass{}, nil
		}
		pack, err := r.deps.LangPack.GetStrings(ctx, langPackOrClient(ctx, req.LangPack), req.LangCode, req.Keys)
		if err != nil {
			return nil, internalErr()
		}
		return tgLangPackStrings(pack.Strings), nil
	})

}

// langPackOrClient 返回请求里的 lang_pack；为空（老客户端经生成 overlay 升级而来）时按 client 派生。
func langPackOrClient(ctx context.Context, langPack string) string {
	if langPack != "" {
		return langPack
	}
	return langPackFromClient(ctx)
}

func (r *Router) langpackLanguage(ctx context.Context, langPack, langCode string) (tg.LangPackLanguage, error) {
	if langCode == "" {
		if info, ok := ClientInfoFrom(ctx); ok && info.LangCode != "" {
			langCode = info.LangCode
		} else {
			langCode = "en"
		}
	}
	langCode = normalizeLangpackCode(langCode)
	languages, err := r.langpackLanguages(ctx, langPack)
	if err != nil {
		return tg.LangPackLanguage{}, internalErr()
	}
	for _, lang := range languages {
		if strings.ToLower(lang.LangCode) == langCode {
			return lang, nil
		}
	}
	return tg.LangPackLanguage{}, langCodeNotSupportedErr()
}

func (r *Router) langpackLanguages(ctx context.Context, langPack string) ([]tg.LangPackLanguage, error) {
	if langPack == "" {
		langPack = langPackFromClient(ctx)
	}
	langPack = strings.ToLower(langPack)
	if r.deps.LangPack == nil {
		return []tg.LangPackLanguage{}, nil
	}
	languages, err := r.deps.LangPack.ListLanguages(ctx, langPack)
	if err != nil {
		return nil, err
	}
	return tgLangPackLanguages(languages), nil
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
	case ClientTypeIOS:
		return string(ClientTypeIOS)
	case ClientTypeMacOS:
		return string(ClientTypeMacOS)
	case ClientTypeTWeb:
		return "webk"
	case ClientTypeTelegramTT:
		return "weba"
	}
	client := strings.ToLower(info.DeviceModel + " " + info.SystemVersion + " " + info.AppVersion)
	if strings.Contains(client, "android") {
		return "android"
	}
	return "tdesktop"
}

func normalizeLangpackCode(langCode string) string {
	code := strings.ToLower(strings.TrimSpace(langCode))
	if code == "" {
		return "en"
	}
	code = strings.ReplaceAll(code, "_", "-")
	return strings.TrimSuffix(code, "-raw")
}
