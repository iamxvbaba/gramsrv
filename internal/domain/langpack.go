package domain

import "errors"

var (
	// ErrLangPackInvalid 表示客户端请求的语言包目录不存在。
	ErrLangPackInvalid = errors.New("lang pack invalid")
	// ErrLangCodeNotSupported 表示语言包目录存在，但其中没有请求的语言码。
	ErrLangCodeNotSupported = errors.New("lang code not supported")
)

// LangPack 是一份客户端语言包的查询结果。
type LangPack struct {
	LangPack    string
	LangCode    string
	FromVersion int
	Version     int
	Strings     []LangPackString
}

// LangPackSeed 是一次启动扫描得到的完整语言包文件清单。
type LangPackSeed struct {
	Catalog string
	Scopes  []string
	Packs   []LangPackSeedEntry
}

// LangPackSeedEntry 记录一个语言包文件、源文件 hash 与规范化内容 hash。
type LangPackSeedEntry struct {
	Pack         LangPack
	SourceHash   string
	ContentHash  string
	StringsCount int
	// Size/ModTime 是源文件字节数与修改时间（Unix 纳秒）。两者及文件名版本号都
	// 未变才复用旧 hash；缺少 stat 元数据的旧清单必须先重新校验文件内容。
	Size          int64
	ModTime       int64
	ContentLoaded bool
}

// LangPackSeedCatalog 是上次成功对账后可用于跳过未变文件解析的清单快照。
type LangPackSeedCatalog struct {
	Catalog string                     `json:"catalog"`
	Scopes  []string                   `json:"scopes"`
	Packs   []LangPackSeedCatalogEntry `json:"packs"`
}

// LangPackSeedCatalogEntry 只保存判断源文件是否变化所需的有界元数据。
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

// CatalogSnapshot 把一次扫描结果压成可持久化的清单快照。
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

// SameSeedCatalogSnapshot 比较两份清单是否逐字段一致（含文件大小和修改时间）。
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

// LangPackLanguage 是 langpack.getLanguages/getLanguage 返回的语言元数据。
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

// LangPackString 是语言包中的一个普通或复数形式字符串。
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
