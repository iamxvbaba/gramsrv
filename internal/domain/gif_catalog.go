package domain

import (
	"errors"
	"strings"
	"time"
	"unicode"
)

var (
	ErrGifCatalogUnavailable   = errors.New("gif catalog is not configured")
	ErrGifCatalogFileInvalid   = errors.New("gif catalog file invalid")
	ErrGifCatalogEntryInvalid  = errors.New("gif catalog entry invalid")
	ErrGifCatalogEntryNotFound = errors.New("gif catalog entry not found")
	ErrGifCatalogFull          = errors.New("gif catalog is full")
	ErrGifCatalogSourceChanged = errors.New("gif catalog seed source changed")
)

const (
	MaxGifCatalogTitleLen     = 128
	MaxGifCatalogFileNameLen  = 255
	MaxGifCatalogEntries      = MaxBotInlineResults
	MaxGifCatalogUploadSize   = 50 << 20
	MaxGifCatalogDocumentSize = 200 << 20
)

// GifCatalogEntry is one operator-curated GIFV document served by @gif.
// FileName is display/classification metadata. SourceFilename/SourceSHA256 are
// startup-seed identity, not display fields.
type GifCatalogEntry struct {
	ID             int64     `json:"ID,string"`
	Title          string    `json:"Title"`
	FileName       string    `json:"FileName"`
	Category       string    `json:"Category"`
	CategoryManual bool      `json:"CategoryManual"`
	DocumentID     int64     `json:"DocumentID,string"`
	Enabled        bool      `json:"Enabled"`
	SortOrder      int       `json:"SortOrder"`
	CreatedBy      string    `json:"CreatedBy"`
	SourceFilename string    `json:"SourceFilename"`
	SourceSHA256   string    `json:"SourceSHA256"`
	CreatedAt      time.Time `json:"CreatedAt"`
	UpdatedAt      time.Time `json:"UpdatedAt"`
}

const (
	GifCategoryReaction    = "reaction"
	GifCategoryHumor       = "humor"
	GifCategoryAnimals     = "animals"
	GifCategorySports      = "sports"
	GifCategoryCelebration = "celebration"
	GifCategoryOther       = "other"
)

var gifCategoryKeywords = []struct {
	category string
	keywords []string
}{
	{GifCategoryReaction, []string{"reaction", "react", "wow", "yes", "no", "hello", "hi", "bye", "wave", "thanks", "thank", "sorry", "love", "facepalm", "shrug", "applause", "thumbsup", "鼓掌", "表情", "反应", "你好", "再见", "谢谢", "抱歉", "爱你"}},
	{GifCategoryHumor, []string{"meme", "memes", "funny", "joke", "laugh", "lol", "haha", "comedy", "搞笑", "梗", "哈哈", "笑话"}},
	{GifCategoryAnimals, []string{"animal", "animals", "cat", "cats", "kitten", "kittens", "dog", "dogs", "puppy", "puppies", "panda", "bird", "rabbit", "宠物", "动物", "猫", "猫咪", "狗", "狗狗", "熊猫", "鸟", "兔"}},
	{GifCategorySports, []string{"sport", "sports", "football", "soccer", "basketball", "tennis", "baseball", "goal", "goals", "运动", "足球", "篮球", "网球", "棒球"}},
	{GifCategoryCelebration, []string{"celebrate", "celebration", "birthday", "happybirthday", "party", "congrats", "congratulations", "cheers", "holiday", "christmas", "newyear", "生日", "派对", "庆祝", "恭喜", "节日", "圣诞", "新年"}},
}

func ValidGifCategory(category string) bool {
	switch category {
	case GifCategoryReaction, GifCategoryHumor, GifCategoryAnimals, GifCategorySports, GifCategoryCelebration, GifCategoryOther:
		return true
	default:
		return false
	}
}

// ClassifyGifCategory prefers the operator's title over the source filename.
// Latin keywords match whole tokens, so "cat" does not match "catch".
func ClassifyGifCategory(title, filename string) string {
	for _, source := range []string{title, filename} {
		words := gifCategoryWords(source)
		for _, rule := range gifCategoryKeywords {
			for _, keyword := range rule.keywords {
				if gifKeywordMatches(source, words, keyword) {
					return rule.category
				}
			}
		}
	}
	return GifCategoryOther
}

func GifCategoryMatchesQuery(category, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || !ValidGifCategory(category) {
		return false
	}
	if strings.Contains(category, query) {
		return true
	}
	for _, rule := range gifCategoryKeywords {
		if rule.category != category {
			continue
		}
		for _, keyword := range rule.keywords {
			if strings.ToLower(keyword) == query {
				return true
			}
		}
	}
	return false
}

func gifCategoryWords(value string) map[string]bool {
	returnMap := make(map[string]bool)
	var normalized strings.Builder
	var previous rune
	for _, current := range value {
		if unicode.IsUpper(current) && (unicode.IsLower(previous) || unicode.IsDigit(previous)) {
			normalized.WriteByte(' ')
		}
		normalized.WriteRune(unicode.ToLower(current))
		previous = current
	}
	for _, word := range strings.FieldsFunc(normalized.String(), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		returnMap[word] = true
	}
	return returnMap
}

func gifKeywordMatches(value string, words map[string]bool, keyword string) bool {
	for _, r := range keyword {
		if r > unicode.MaxASCII {
			return strings.Contains(value, keyword)
		}
	}
	return words[keyword]
}
