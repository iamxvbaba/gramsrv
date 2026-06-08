package rpc

import (
	"context"
	"strings"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

const (
	legacyLangpackGetLangPackTypeID  uint32 = 0x9ab5c58e
	legacyLangpackGetStringsTypeID   uint32 = 0x2e1ee318
	legacyLangpackGetLanguagesTypeID uint32 = 0x800fd57d

	maxLegacyLangpackStringKeys = 512
)

// registerLangpack 注册 langpack.* RPC handler。
func (r *Router) registerLangpack(d *tg.ServerDispatcher) {
	d.OnLangpackGetLanguages(func(ctx context.Context, langPack string) ([]tg.LangPackLanguage, error) {
		return r.langpackLanguages(ctx, langPack), nil
	})
	d.OnLangpackGetLangPack(func(ctx context.Context, req *tg.LangpackGetLangPackRequest) (*tg.LangPackDifference, error) {
		if r.deps.LangPack == nil {
			return &tg.LangPackDifference{LangCode: req.LangCode}, nil
		}
		pack, err := r.deps.LangPack.GetLangPack(ctx, req.LangPack, req.LangCode)
		if err != nil {
			return nil, internalErr()
		}
		return tgLangPackDifference(pack), nil
	})
	d.OnLangpackGetDifference(func(ctx context.Context, req *tg.LangpackGetDifferenceRequest) (*tg.LangPackDifference, error) {
		if r.deps.LangPack == nil {
			return &tg.LangPackDifference{LangCode: req.LangCode, FromVersion: req.FromVersion}, nil
		}
		pack, err := r.deps.LangPack.GetDifference(ctx, req.LangPack, req.LangCode, req.FromVersion)
		if err != nil {
			return nil, internalErr()
		}
		return tgLangPackDifference(pack), nil
	})
	d.OnLangpackGetStrings(func(ctx context.Context, req *tg.LangpackGetStringsRequest) ([]tg.LangPackStringClass, error) {
		if r.deps.LangPack == nil {
			return nil, nil
		}
		pack, err := r.deps.LangPack.GetStrings(ctx, req.LangPack, req.LangCode, req.Keys)
		if err != nil {
			return nil, internalErr()
		}
		return tgLangPackStrings(pack.Strings), nil
	})
}

func (r *Router) handleLegacyLangpackGetLangPack(ctx context.Context, b *bin.Buffer) (bin.Encoder, error) {
	if err := b.ConsumeID(legacyLangpackGetLangPackTypeID); err != nil {
		return nil, err
	}
	langCode, err := b.String()
	if err != nil {
		return nil, err
	}
	langPack := langPackFromClient(ctx)
	if r.deps.LangPack == nil {
		return &tg.LangPackDifference{LangCode: langCode}, nil
	}
	pack, err := r.deps.LangPack.GetLangPack(ctx, langPack, langCode)
	if err != nil {
		return nil, internalErr()
	}
	return tgLangPackDifference(pack), nil
}

func (r *Router) handleLegacyLangpackGetStrings(ctx context.Context, b *bin.Buffer) (bin.Encoder, error) {
	if err := b.ConsumeID(legacyLangpackGetStringsTypeID); err != nil {
		return nil, err
	}
	langCode, err := b.String()
	if err != nil {
		return nil, err
	}
	headerLen, err := b.VectorHeader()
	if err != nil {
		return nil, err
	}
	if headerLen > maxLegacyLangpackStringKeys {
		return nil, limitInvalidErr()
	}
	keys := make([]string, 0, headerLen)
	for i := 0; i < headerLen; i++ {
		key, err := b.String()
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	langPack := langPackFromClient(ctx)
	if r.deps.LangPack == nil {
		return &tg.LangPackStringClassVector{}, nil
	}
	pack, err := r.deps.LangPack.GetStrings(ctx, langPack, langCode, keys)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.LangPackStringClassVector{Elems: tgLangPackStrings(pack.Strings)}, nil
}

func (r *Router) handleLegacyLangpackGetLanguages(ctx context.Context, b *bin.Buffer) (bin.Encoder, error) {
	if err := b.ConsumeID(legacyLangpackGetLanguagesTypeID); err != nil {
		return nil, err
	}
	return &tg.LangPackLanguageVector{Elems: r.langpackLanguages(ctx, "")}, nil
}

func (r *Router) langpackLanguages(ctx context.Context, langPack string) []tg.LangPackLanguage {
	if langPack == "" {
		langPack = langPackFromClient(ctx)
	}
	_ = langPack
	return []tg.LangPackLanguage{
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
}

func langPackFromClient(ctx context.Context) string {
	info, ok := ClientInfoFrom(ctx)
	if !ok {
		return "tdesktop"
	}
	if info.LangPack != "" {
		return info.LangPack
	}
	client := strings.ToLower(info.DeviceModel + " " + info.SystemVersion + " " + info.AppVersion)
	if strings.Contains(client, "android") {
		return "android"
	}
	return "tdesktop"
}
