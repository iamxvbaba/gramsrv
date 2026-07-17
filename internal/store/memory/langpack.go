package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"telesrv/internal/domain"
)

// LangPackStore 是 store.LangPackStore 的内存实现。
type LangPackStore struct {
	mu         sync.RWMutex
	m          map[string]domain.LangPack
	seedHashes map[string]string
	catalogs   map[string]domain.LangPackSeedCatalog
}

// NewLangPackStore 创建内存 LangPackStore。
func NewLangPackStore() *LangPackStore {
	return &LangPackStore{
		m:          make(map[string]domain.LangPack),
		seedHashes: make(map[string]string),
		catalogs:   make(map[string]domain.LangPackSeedCatalog),
	}
}

func (s *LangPackStore) GetSeedCatalog(_ context.Context, catalog string) (domain.LangPackSeedCatalog, error) {
	if catalog == "" {
		catalog = "default"
	}
	s.mu.RLock()
	state := s.catalogs[catalog]
	s.mu.RUnlock()
	state.Scopes = append([]string(nil), state.Scopes...)
	state.Packs = append([]domain.LangPackSeedCatalogEntry(nil), state.Packs...)
	return state, nil
}

func (s *LangPackStore) GetPack(_ context.Context, langPack, langCode string, fromVersion int) (domain.LangPack, error) {
	s.mu.RLock()
	pack := s.m[langPackKey(langPack, langCode)]
	s.mu.RUnlock()
	if pack.LangPack == "" {
		return domain.LangPack{LangPack: langPack, LangCode: langCode, FromVersion: fromVersion}, nil
	}
	pack.FromVersion = fromVersion
	if pack.Version <= fromVersion {
		pack.Strings = nil
	} else {
		pack.Strings = append([]domain.LangPackString(nil), pack.Strings...)
	}
	return pack, nil
}

func (s *LangPackStore) GetStrings(_ context.Context, langPack, langCode string, keys []string) (domain.LangPack, error) {
	s.mu.RLock()
	pack := s.m[langPackKey(langPack, langCode)]
	s.mu.RUnlock()
	if pack.LangPack == "" {
		return domain.LangPack{LangPack: langPack, LangCode: langCode}, nil
	}
	if len(keys) == 0 {
		pack.Strings = append([]domain.LangPackString(nil), pack.Strings...)
		return pack, nil
	}
	want := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		want[key] = struct{}{}
	}
	out := domain.LangPack{LangPack: pack.LangPack, LangCode: pack.LangCode, Version: pack.Version}
	for _, item := range pack.Strings {
		if _, ok := want[item.Key]; ok {
			out.Strings = append(out.Strings, item)
		}
	}
	return out, nil
}

func (s *LangPackStore) ListLanguages(_ context.Context, langPack string) ([]domain.LangPackLanguage, error) {
	s.mu.RLock()
	packs := make([]domain.LangPack, 0)
	for _, pack := range s.m {
		if pack.LangPack == langPack {
			packs = append(packs, pack)
		}
	}
	s.mu.RUnlock()

	sort.Slice(packs, func(i, j int) bool {
		return packs[i].LangCode < packs[j].LangCode
	})
	out := make([]domain.LangPackLanguage, 0, len(packs))
	for _, pack := range packs {
		codeKey := langPackCodeKey(pack.LangCode)
		lang := domain.LangPackLanguage{
			LangPack:        pack.LangPack,
			LangCode:        pack.LangCode,
			StringsCount:    len(pack.Strings),
			TranslatedCount: len(pack.Strings),
		}
		for _, item := range pack.Strings {
			switch item.Key {
			case "LanguageNameInEnglish", "Localization.EnglishLanguageName":
				if lang.Name == "" {
					lang.Name = item.Value
				}
			case "lng_language_name", "Localization.LanguageName":
				if lang.NativeName == "" {
					lang.NativeName = item.Value
				}
			case "LanguageName":
				if lang.NativeName == "" && pack.LangPack != "android" {
					lang.NativeName = item.Value
				}
			case "TranslateLanguage" + codeKey, "PassportLanguage_" + codeKey:
				if lang.NativeName == "" || pack.LangPack == "android" {
					lang.NativeName = item.Value
				}
			}
		}
		out = append(out, lang)
	}
	return out, nil
}

func (s *LangPackStore) ReconcileSeed(_ context.Context, seed domain.LangPackSeed) (int, error) {
	catalog := seed.Catalog
	if catalog == "" {
		catalog = "default"
	}
	scopes := make(map[string]struct{}, len(seed.Scopes))
	for _, scope := range seed.Scopes {
		scopes[scope] = struct{}{}
	}
	wanted := make(map[string]domain.LangPackSeedEntry, len(seed.Packs))
	for _, entry := range seed.Packs {
		if _, scoped := scopes[entry.Pack.LangPack]; !scoped {
			return 0, fmt.Errorf("seeded langpack %s/%s is outside reconciliation scopes", entry.Pack.LangPack, entry.Pack.LangCode)
		}
		key := langPackKey(entry.Pack.LangPack, entry.Pack.LangCode)
		if _, exists := wanted[key]; exists {
			return 0, fmt.Errorf("duplicate seeded langpack %s/%s", entry.Pack.LangPack, entry.Pack.LangCode)
		}
		wanted[key] = entry
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, scope := range s.catalogs[catalog].Scopes {
		scopes[scope] = struct{}{}
	}
	for key, entry := range wanted {
		if err := validateSeedEntry(entry); err != nil {
			return 0, err
		}
		current, exists := s.m[key]
		if exists {
			if entry.Pack.Version < current.Version {
				return 0, fmt.Errorf("langpack version rollback %s/%s: %d < %d", entry.Pack.LangPack, entry.Pack.LangCode, entry.Pack.Version, current.Version)
			}
			if oldHash := s.seedHashes[key]; oldHash != "" && entry.Pack.Version == current.Version && oldHash != entry.ContentHash {
				return 0, fmt.Errorf("langpack %s/%s v%d content changed without version bump", entry.Pack.LangPack, entry.Pack.LangCode, entry.Pack.Version)
			}
		}
		unchanged := exists && current.Version == entry.Pack.Version && len(current.Strings) == entry.StringsCount && s.seedHashes[key] == entry.ContentHash
		if !unchanged && !entry.ContentLoaded {
			return 0, fmt.Errorf("langpack %s/%s content is required but was not loaded", entry.Pack.LangPack, entry.Pack.LangCode)
		}
	}

	for key, pack := range s.m {
		if _, scoped := scopes[pack.LangPack]; !scoped {
			continue
		}
		if _, exists := wanted[key]; !exists {
			delete(s.m, key)
			delete(s.seedHashes, key)
		}
	}
	written := 0
	for key, entry := range wanted {
		current, exists := s.m[key]
		if exists && current.Version == entry.Pack.Version && len(current.Strings) == entry.StringsCount && s.seedHashes[key] == entry.ContentHash {
			continue
		}
		pack := entry.Pack
		pack.Strings = append([]domain.LangPackString(nil), pack.Strings...)
		s.m[key] = pack
		s.seedHashes[key] = entry.ContentHash
		written += len(pack.Strings)
	}
	s.catalogs[catalog] = seedCatalogSnapshot(seed)
	return written, nil
}

func (s *LangPackStore) UpsertPack(_ context.Context, pack domain.LangPack) error {
	pack.Strings = append([]domain.LangPackString(nil), pack.Strings...)
	s.mu.Lock()
	key := langPackKey(pack.LangPack, pack.LangCode)
	s.m[key] = pack
	delete(s.seedHashes, key)
	clear(s.catalogs)
	s.mu.Unlock()
	return nil
}

func validateSeedEntry(entry domain.LangPackSeedEntry) error {
	if entry.SourceHash == "" || entry.ContentHash == "" || entry.StringsCount <= 0 {
		return fmt.Errorf("langpack %s/%s has incomplete seed metadata", entry.Pack.LangPack, entry.Pack.LangCode)
	}
	if entry.ContentLoaded && len(entry.Pack.Strings) != entry.StringsCount {
		return fmt.Errorf("langpack %s/%s loaded %d strings, want %d", entry.Pack.LangPack, entry.Pack.LangCode, len(entry.Pack.Strings), entry.StringsCount)
	}
	return nil
}

func seedCatalogSnapshot(seed domain.LangPackSeed) domain.LangPackSeedCatalog {
	state := domain.LangPackSeedCatalog{
		Catalog: seed.Catalog,
		Scopes:  append([]string(nil), seed.Scopes...),
		Packs:   make([]domain.LangPackSeedCatalogEntry, 0, len(seed.Packs)),
	}
	for _, entry := range seed.Packs {
		state.Packs = append(state.Packs, domain.LangPackSeedCatalogEntry{
			LangPack:     entry.Pack.LangPack,
			LangCode:     entry.Pack.LangCode,
			Version:      entry.Pack.Version,
			SourceHash:   entry.SourceHash,
			ContentHash:  entry.ContentHash,
			StringsCount: entry.StringsCount,
		})
	}
	return state
}

func langPackKey(langPack, langCode string) string {
	return langPack + "\x00" + langCode
}

func langPackCodeKey(langCode string) string {
	base := strings.ToUpper(strings.TrimSpace(langCode))
	if idx := strings.IndexAny(base, "-_"); idx >= 0 {
		base = base[:idx]
	}
	return base
}
