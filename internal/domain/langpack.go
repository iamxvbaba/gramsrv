package domain

import "errors"

var (
	// ErrLangPackInvalid means the language pack catalog requested by the client does not exist.
	ErrLangPackInvalid = errors.New("lang pack invalid")
	// ErrLangCodeNotSupported means the language pack catalog exists but contains no entry for the requested language code.
	ErrLangCodeNotSupported = errors.New("lang code not supported")
)

// LangPack is a client language pack query result.
type LangPack struct {
	LangPack    string
	LangCode    string
	FromVersion int
	Version     int
	Strings     []LangPackString
}

// LangPackSeed is the full language pack file inventory obtained from one startup scan.
type LangPackSeed struct {
	Catalog string
	Scopes  []string
	Packs   []LangPackSeedEntry
}

// LangPackSeedEntry records one language pack file, its source hash and normalized content hash.
type LangPackSeedEntry struct {
	Pack         LangPack
	SourceHash   string
	ContentHash  string
	StringsCount int
	// Size/ModTime are the source file's byte count and modification time (Unix nanoseconds). The old hash is
	// reused only when both and the filename version are unchanged; an old inventory lacking stat metadata must re-verify the file content first.
	Size          int64
	ModTime       int64
	ContentLoaded bool
}

// LangPackSeedCatalog is the inventory snapshot from the last successful reconciliation, usable to skip parsing unchanged files.
type LangPackSeedCatalog struct {
	Catalog string                     `json:"catalog"`
	Scopes  []string                   `json:"scopes"`
	Packs   []LangPackSeedCatalogEntry `json:"packs"`
}

// LangPackSeedCatalogEntry stores only the bounded metadata needed to tell whether the source file changed.
type LangPackSeedCatalogEntry struct {
	LangPack     string `json:"lang_pack"`
	LangCode     string `json:"lang_code"`
	Version      int    `json:"version"`
	SourceHash   string `json:"source_hash"`
	ContentHash  string `json:"content_hash"`
	StringsCount int    `json:"strings_count"`
	Size         int64  `json:"size,omitempty"`
	ModTime      int64  `json:"mod_time,omitempty"`
}

// CatalogSnapshot compresses one scan result into a persistable inventory snapshot.
func (seed LangPackSeed) CatalogSnapshot() LangPackSeedCatalog {
	state := LangPackSeedCatalog{
		Catalog: seed.Catalog,
		Scopes:  append([]string(nil), seed.Scopes...),
		Packs:   make([]LangPackSeedCatalogEntry, 0, len(seed.Packs)),
	}
	for _, entry := range seed.Packs {
		state.Packs = append(state.Packs, LangPackSeedCatalogEntry{
			LangPack:     entry.Pack.LangPack,
			LangCode:     entry.Pack.LangCode,
			Version:      entry.Pack.Version,
			SourceHash:   entry.SourceHash,
			ContentHash:  entry.ContentHash,
			StringsCount: entry.StringsCount,
			Size:         entry.Size,
			ModTime:      entry.ModTime,
		})
	}
	return state
}

// SameSeedCatalogSnapshot compares whether two inventories match field by field (including file size and modification time).
func SameSeedCatalogSnapshot(a, b LangPackSeedCatalog) bool {
	if a.Catalog != b.Catalog || len(a.Scopes) != len(b.Scopes) || len(a.Packs) != len(b.Packs) {
		return false
	}
	for i := range a.Scopes {
		if a.Scopes[i] != b.Scopes[i] {
			return false
		}
	}
	for i := range a.Packs {
		if a.Packs[i] != b.Packs[i] {
			return false
		}
	}
	return true
}

// LangPackLanguage is the language metadata returned by langpack.getLanguages/getLanguage.
type LangPackLanguage struct {
	LangPack        string
	LangCode        string
	Name            string
	NativeName      string
	BaseLangCode    string
	PluralCode      string
	Official        bool
	Rtl             bool
	Beta            bool
	StringsCount    int
	TranslatedCount int
	TranslationsURL string
}

// LangPackString is a normal or plural-form string in a language pack.
type LangPackString struct {
	Key        string
	Value      string
	Pluralized bool
	ZeroValue  string
	OneValue   string
	TwoValue   string
	FewValue   string
	ManyValue  string
	OtherValue string
	Deleted    bool
}
