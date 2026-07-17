package domain

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
	Pack          LangPack
	SourceHash    string
	ContentHash   string
	StringsCount  int
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
