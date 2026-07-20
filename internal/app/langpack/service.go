package langpack

import (
	"context"
	"strings"

	"golang.org/x/sync/singleflight"
	"golang.org/x/text/unicode/bidi"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service 提供客户端语言包查询。
type Service struct {
	packs         store.LangPackStore
	packCache     *langPackCache
	languageCache *languageListCache
	packLoads     singleflight.Group
	languageLoads singleflight.Group
	publicBaseURL string
}

// Option configures user-visible language-pack projection.
type Option func(*Service)

// WithPublicBaseURL replaces official public hosts embedded in upstream
// language-pack values with this deployment's public link root.
func WithPublicBaseURL(value string) Option {
	return func(s *Service) {
		if strings.TrimSpace(value) != "" {
			s.publicBaseURL = value
		}
	}
}

// NewService 创建 langpack 服务。
func NewService(packs store.LangPackStore, opts ...Option) *Service {
	s := newServiceWithCacheLimits(
		packs,
		defaultLangPackCacheMaxBytes,
		defaultLangPackCacheMaxEntries,
		defaultLanguageListCacheMaxEntries,
	)
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func newServiceWithCacheLimits(packs store.LangPackStore, maxBytes int64, maxEntries, languageEntries int) *Service {
	return &Service{
		packs:         packs,
		packCache:     newLangPackCache(maxBytes, maxEntries),
		languageCache: newLanguageListCache(languageEntries),
		publicBaseURL: branding.DefaultPublicURL,
	}
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
	var (
		pack domain.LangPack
		err  error
	)
	if fromVersion == 0 {
		pack, err = s.effectivePack(ctx, packName, code)
	} else {
		pack, err = s.rawPack(ctx, packName, code)
	}
	if err != nil {
		return domain.LangPack{}, err
	}
	pack.FromVersion = fromVersion
	if pack.Version <= fromVersion {
		pack.Strings = nil
	}
	return pack, nil
}

// GetStrings 返回指定 key 的语言包字符串。
func (s *Service) GetStrings(ctx context.Context, langPack, langCode string, keys []string) (domain.LangPack, error) {
	packName := normalizePack(langPack)
	code := normalizeCode(langCode)
	if s == nil || s.packs == nil {
		return domain.LangPack{LangPack: packName, LangCode: code}, nil
	}
	pack, err := s.effectivePack(ctx, packName, code)
	if err != nil {
		return domain.LangPack{}, err
	}
	if len(keys) == 0 {
		return pack, nil
	}
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	selected := pack
	selected.Strings = make([]domain.LangPackString, 0, len(keys))
	for _, item := range pack.Strings {
		if _, ok := wanted[item.Key]; ok {
			selected.Strings = append(selected.Strings, item)
		}
	}
	return selected, nil
}

// ListLanguages 返回已 seed 的语言包语言列表。
func (s *Service) ListLanguages(ctx context.Context, langPack string) ([]domain.LangPackLanguage, error) {
	packName := normalizePack(langPack)
	if s == nil || s.packs == nil {
		return nil, nil
	}
	return s.cachedLanguages(ctx, packName)
}

func normalizePack(langPack string) string {
	pack := strings.ToLower(strings.TrimSpace(langPack))
	if pack == "" {
		return "tdesktop"
	}
	return pack
}

func normalizeCode(langCode string) string {
	code := strings.ToLower(strings.TrimSpace(langCode))
	if code == "" {
		return "en"
	}
	code = strings.ReplaceAll(code, "_", "-")
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

func (s *Service) rawPack(ctx context.Context, langPack, langCode string) (domain.LangPack, error) {
	key := langPackCacheKey{pack: langPack, code: langCode, kind: langPackCacheRaw}
	return s.cachedPack(ctx, key, func() (domain.LangPack, error) {
		pack, err := s.packs.GetPack(ctx, langPack, langCode, 0)
		if err != nil {
			return domain.LangPack{}, err
		}
		return s.brandPack(pack), nil
	})
}

func (s *Service) effectivePack(ctx context.Context, langPack, langCode string) (domain.LangPack, error) {
	if !shouldOverlayWebA(langPack) {
		return s.rawPack(ctx, langPack, langCode)
	}
	key := langPackCacheKey{pack: langPack, code: langCode, kind: langPackCacheEffective}
	return s.cachedPack(ctx, key, func() (domain.LangPack, error) {
		pack, err := s.rawPack(ctx, langPack, langCode)
		if err != nil {
			return domain.LangPack{}, err
		}
		overlay, err := s.rawPack(ctx, "weba", langCode)
		if err != nil {
			return domain.LangPack{}, err
		}
		return mergeMissingLangPackStrings(pack, overlay), nil
	})
}

type cachedPackLoadResult struct {
	pack   domain.LangPack
	stable bool
}

func (s *Service) cachedPack(ctx context.Context, key langPackCacheKey, load func() (domain.LangPack, error)) (domain.LangPack, error) {
	if s.packCache == nil {
		return load()
	}
	for {
		if pack, ok := s.packCache.get(key); ok {
			return pack, nil
		}
		value, err, _ := s.packLoads.Do(key.singleflightKey(), func() (any, error) {
			if pack, ok := s.packCache.get(key); ok {
				return cachedPackLoadResult{pack: pack, stable: true}, nil
			}
			loadEpoch := s.packCache.loadEpoch()
			pack, err := load()
			if err != nil {
				return cachedPackLoadResult{}, err
			}
			return cachedPackLoadResult{
				pack:   pack,
				stable: s.packCache.putIfEpoch(key, pack, loadEpoch),
			}, nil
		})
		if err != nil {
			return domain.LangPack{}, err
		}
		result := value.(cachedPackLoadResult)
		if result.stable {
			return cloneLangPack(result.pack), nil
		}
		if err := ctx.Err(); err != nil {
			return domain.LangPack{}, err
		}
	}
}

type cachedLanguagesLoadResult struct {
	languages []domain.LangPackLanguage
	stable    bool
}

func (s *Service) cachedLanguages(ctx context.Context, langPack string) ([]domain.LangPackLanguage, error) {
	if languages, ok := s.languageCache.get(langPack); ok {
		return languages, nil
	}
	for {
		value, err, _ := s.languageLoads.Do(langPack, func() (any, error) {
			if languages, ok := s.languageCache.get(langPack); ok {
				return cachedLanguagesLoadResult{languages: languages, stable: true}, nil
			}
			loadEpoch := s.languageCache.loadEpoch()
			languages, err := s.packs.ListLanguages(ctx, langPack)
			if err != nil {
				return cachedLanguagesLoadResult{}, err
			}
			for i := range languages {
				languages[i] = completeLanguageMetadata(langPack, languages[i])
				languages[i] = s.brandLanguage(languages[i])
			}
			return cachedLanguagesLoadResult{
				languages: languages,
				stable:    s.languageCache.putIfEpoch(langPack, languages, loadEpoch),
			}, nil
		})
		if err != nil {
			return nil, err
		}
		result := value.(cachedLanguagesLoadResult)
		if result.stable {
			return cloneLanguages(result.languages), nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

func (s *Service) brandPack(pack domain.LangPack) domain.LangPack {
	for i := range pack.Strings {
		item := &pack.Strings[i]
		item.Value = branding.UserVisibleText(item.Value, s.publicBaseURL)
		item.ZeroValue = branding.UserVisibleText(item.ZeroValue, s.publicBaseURL)
		item.OneValue = branding.UserVisibleText(item.OneValue, s.publicBaseURL)
		item.TwoValue = branding.UserVisibleText(item.TwoValue, s.publicBaseURL)
		item.FewValue = branding.UserVisibleText(item.FewValue, s.publicBaseURL)
		item.ManyValue = branding.UserVisibleText(item.ManyValue, s.publicBaseURL)
		item.OtherValue = branding.UserVisibleText(item.OtherValue, s.publicBaseURL)
	}
	return pack
}

func (s *Service) brandLanguage(lang domain.LangPackLanguage) domain.LangPackLanguage {
	lang.Name = branding.UserVisibleText(lang.Name, s.publicBaseURL)
	lang.NativeName = branding.UserVisibleText(lang.NativeName, s.publicBaseURL)
	lang.TranslationsURL = branding.UserVisibleText(lang.TranslationsURL, s.publicBaseURL)
	return lang
}

func (s *Service) flushCaches() {
	if s == nil {
		return
	}
	s.packCache.flush()
	s.languageCache.flush()
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

func completeLanguageMetadata(langPack string, lang domain.LangPackLanguage) domain.LangPackLanguage {
	if lang.LangPack == "" {
		lang.LangPack = langPack
	}
	lang.LangCode = normalizeCode(lang.LangCode)
	if lang.PluralCode == "" {
		lang.PluralCode = pluralCode(lang.LangCode)
	}
	if lang.NativeName == "" {
		lang.NativeName = lang.Name
	}
	if lang.Name == "" {
		lang.Name = lang.NativeName
	}
	if lang.Name == "" {
		lang.Name = lang.LangCode
	}
	if lang.NativeName == "" {
		lang.NativeName = lang.Name
	}
	if lang.StringsCount == 0 {
		lang.StringsCount = lang.TranslatedCount
	}
	if lang.TranslatedCount == 0 {
		lang.TranslatedCount = lang.StringsCount
	}
	lang.Official = true
	lang.Rtl = lang.Rtl || isRTLText(lang.NativeName)
	return lang
}

func pluralCode(langCode string) string {
	if idx := strings.IndexAny(langCode, "-_"); idx > 0 {
		return langCode[:idx]
	}
	return langCode
}

func isRTLText(value string) bool {
	for _, r := range value {
		properties, _ := bidi.LookupRune(r)
		switch properties.Class() {
		case bidi.R, bidi.AL:
			return true
		case bidi.L:
			return false
		}
	}
	return false
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
