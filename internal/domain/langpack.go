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
	Catalog           string
	SourceFingerprint string
	Scopes            []string
	Packs             []LangPackSeedEntry
}

// LangPackSeedEntry records one language pack file, its source hash and normalized content hash.
type LangPackSeedEntry struct {
	Pack          LangPack
	SourceHash    string
	ContentHash   string
	StringsCount  int
	ContentLoaded bool
}

// LangPackSeedCatalog is the inventory snapshot from the last successful reconciliation, usable to skip parsing unchanged files.
type LangPackSeedCatalog struct {
	Catalog           string                     `json:"catalog"`
	SourceFingerprint string                     `json:"source_fingerprint,omitempty"`
	Scopes            []string                   `json:"scopes"`
	Packs             []LangPackSeedCatalogEntry `json:"packs"`
}

// LangPackSeedCatalogEntry stores only the bounded metadata needed to tell whether the source file changed.
type LangPackSeedCatalogEntry struct {
	LangPack     string `json:"lang_pack"`
	LangCode     string `json:"lang_code"`
	Version      int    `json:"version"`
	SourceHash   string `json:"source_hash"`
	ContentHash  string `json:"content_hash"`
	StringsCount int    `json:"strings_count"`
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
