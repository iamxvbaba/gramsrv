package langpack

import (
	"context"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service 提供客户端语言包查询。
type Service struct {
	packs store.LangPackStore
}

// NewService 创建 langpack 服务。
func NewService(packs store.LangPackStore) *Service {
	return &Service{packs: packs}
}

// GetLangPack 返回完整语言包。
func (s *Service) GetLangPack(ctx context.Context, langPack, langCode string) (domain.LangPack, error) {
	return s.GetDifference(ctx, langPack, langCode, 0)
}

// GetDifference 返回从 fromVersion 到当前版本的语言包差异。
func (s *Service) GetDifference(ctx context.Context, langPack, langCode string, fromVersion int) (domain.LangPack, error) {
	packName := normalizePack(langPack)
	code := normalizeCode(langCode)
	if s == nil || s.packs == nil {
		return domain.LangPack{LangPack: packName, LangCode: code, FromVersion: fromVersion}, nil
	}
	pack, err := s.packs.GetPack(ctx, packName, code, fromVersion)
	if err != nil {
		return domain.LangPack{}, err
	}
	return s.overlayWebAStrings(ctx, pack, packName, code, fromVersion)
}

// GetStrings 返回指定 key 的语言包字符串。
func (s *Service) GetStrings(ctx context.Context, langPack, langCode string, keys []string) (domain.LangPack, error) {
	packName := normalizePack(langPack)
	code := normalizeCode(langCode)
	if s == nil || s.packs == nil {
		return domain.LangPack{LangPack: packName, LangCode: code}, nil
	}
	pack, err := s.packs.GetStrings(ctx, packName, code, keys)
	if err != nil {
		return domain.LangPack{}, err
	}
	if len(keys) == 0 {
		return s.overlayWebAStrings(ctx, pack, packName, code, 0)
	}
	missing := missingLangPackKeys(keys, pack.Strings)
	if len(missing) == 0 || !shouldOverlayWebA(packName) {
		return pack, nil
	}
	overlay, err := s.packs.GetStrings(ctx, "weba", code, missing)
	if err != nil {
		return domain.LangPack{}, err
	}
	return mergeMissingLangPackStrings(pack, overlay), nil
}

func normalizePack(langPack string) string {
	if langPack == "" {
		return "tdesktop"
	}
	return langPack
}

func normalizeCode(langCode string) string {
	code := strings.ToLower(strings.TrimSpace(langCode))
	if code == "" {
		return "en"
	}
	return strings.TrimSuffix(code, "-raw")
}

func shouldOverlayWebA(langPack string) bool {
	switch strings.ToLower(langPack) {
	case "android", "ios", "tdesktop", "macos":
		return true
	default:
		return false
	}
}

func (s *Service) overlayWebAStrings(ctx context.Context, pack domain.LangPack, langPack, langCode string, fromVersion int) (domain.LangPack, error) {
	if fromVersion != 0 || !shouldOverlayWebA(langPack) {
		return pack, nil
	}
	overlay, err := s.packs.GetPack(ctx, "weba", langCode, fromVersion)
	if err != nil {
		return domain.LangPack{}, err
	}
	return mergeMissingLangPackStrings(pack, overlay), nil
}

func mergeMissingLangPackStrings(pack, overlay domain.LangPack) domain.LangPack {
	if len(overlay.Strings) == 0 {
		return pack
	}
	if pack.LangCode == "" {
		pack.LangCode = overlay.LangCode
	}
	if overlay.Version > pack.Version {
		pack.Version = overlay.Version
	}
	seen := make(map[string]struct{}, len(pack.Strings)+len(overlay.Strings))
	for _, item := range pack.Strings {
		seen[item.Key] = struct{}{}
	}
	for _, item := range overlay.Strings {
		if _, ok := seen[item.Key]; ok {
			continue
		}
		pack.Strings = append(pack.Strings, item)
		seen[item.Key] = struct{}{}
	}
	return pack
}

func missingLangPackKeys(keys []string, strings []domain.LangPackString) []string {
	if len(keys) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(strings))
	for _, item := range strings {
		have[item.Key] = struct{}{}
	}
	missing := make([]string, 0)
	seenMissing := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := have[key]; ok {
			continue
		}
		if _, ok := seenMissing[key]; ok {
			continue
		}
		missing = append(missing, key)
		seenMissing[key] = struct{}{}
	}
	return missing
}
