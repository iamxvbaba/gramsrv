// Package catalog provides "catalog/reference data" seeds fetched from official
// Telegram and frozen: country calling codes (help.getCountriesList), timezones
// (help.getTimezonesList), emoji groups (messages.getEmojiGroups), and emoji
// keywords (messages.getEmojiKeywords).
//
// The data is fetched via the official API by cmd/catalogfetch and written to the
// JSON files in this directory, then embedded into the binary via go:embed.
// Changing the JSON changes the content hash, so a client sending an old hash is
// driven to refetch. Each accessor returns an empty set when data is empty (not
// seeded), leaving it to the caller to decide whether to fall back to built-in
// minimal data.
package catalog

import (
	_ "embed"
	"encoding/json"
	"hash/fnv"
	"sync"

	"telesrv/internal/domain"
)

//go:embed countries.json
var countriesJSON []byte

//go:embed timezones.json
var timezonesJSON []byte

//go:embed emoji_groups.json
var emojiGroupsJSON []byte

//go:embed emoji_keywords_en.json
var emojiKeywordsENJSON []byte

type countriesFile struct {
	Countries []countryJSON `json:"countries"`
}
type countryJSON struct {
	ISO2         string            `json:"iso2"`
	DefaultName  string            `json:"default_name"`
	Name         string            `json:"name"`
	Hidden       bool              `json:"hidden"`
	CountryCodes []countryCodeJSON `json:"country_codes"`
}
type countryCodeJSON struct {
	CountryCode string   `json:"country_code"`
	Prefixes    []string `json:"prefixes"`
	Patterns    []string `json:"patterns"`
}

// Timezone is one timezone from help.getTimezonesList.
type Timezone struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UTCOffset int    `json:"utc_offset"`
}
type timezonesFile struct {
	Timezones []Timezone `json:"timezones"`
}

// EmojiGroup is one category from messages.getEmojiGroups. IconEmojiID points to a
// custom-emoji document (if the document is absent locally, the client's category
// icon falls back to a placeholder, which does not affect the group itself).
type EmojiGroup struct {
	Title       string   `json:"title"`
	IconEmojiID int64    `json:"icon_emoji_id"`
	Emoticons   []string `json:"emoticons"`
}
type emojiGroupsFile struct {
	Groups []EmojiGroup `json:"groups"`
}

// EmojiKeyword is one entry of the emoji keyword dictionary: one keyword -> a set of emoji.
type EmojiKeyword struct {
	Keyword   string   `json:"keyword"`
	Emoticons []string `json:"emoticons"`
}

// EmojiKeywordSet is a whole emoji keyword dictionary for one language.
type EmojiKeywordSet struct {
	LangCode string         `json:"lang_code"`
	Version  int            `json:"version"`
	Keywords []EmojiKeyword `json:"keywords"`
}

var (
	once             sync.Once
	countries        domain.CountriesList
	timezones        []Timezone
	timezonesHash    int
	emojiGroups      []EmojiGroup
	emojiGroupsHash  int
	emojiKeywordSets map[string]EmojiKeywordSet
)

func load() {
	once.Do(func() {
		var cf countriesFile
		_ = json.Unmarshal(countriesJSON, &cf)
		list := make([]domain.Country, 0, len(cf.Countries))
		for _, c := range cf.Countries {
			codes := make([]domain.CountryCode, 0, len(c.CountryCodes))
			for _, cc := range c.CountryCodes {
				codes = append(codes, domain.CountryCode{
					CountryCode: cc.CountryCode,
					Prefixes:    cc.Prefixes,
					Patterns:    cc.Patterns,
				})
			}
			list = append(list, domain.Country{
				ISO2:         c.ISO2,
				DefaultName:  c.DefaultName,
				Name:         c.Name,
				Hidden:       c.Hidden,
				CountryCodes: codes,
			})
		}
		countries = domain.CountriesList{Hash: hashBytes(countriesJSON), Countries: list}

		var tf timezonesFile
		_ = json.Unmarshal(timezonesJSON, &tf)
		timezones = tf.Timezones
		timezonesHash = hashBytes(timezonesJSON)

		var ef emojiGroupsFile
		_ = json.Unmarshal(emojiGroupsJSON, &ef)
		emojiGroups = ef.Groups
		emojiGroupsHash = hashBytes(emojiGroupsJSON)

		emojiKeywordSets = map[string]EmojiKeywordSet{}
		for _, raw := range [][]byte{emojiKeywordsENJSON} {
			var ks EmojiKeywordSet
			if err := json.Unmarshal(raw, &ks); err == nil && ks.LangCode != "" && len(ks.Keywords) > 0 {
				emojiKeywordSets[ks.LangCode] = ks
			}
		}
	})
}

// hashBytes maps content to a stable positive-integer hash (FNV-1a 32) for
// NotModified negotiation. If the content changes the hash necessarily changes,
// so a client sending an old hash misses -> refetch.
func hashBytes(b []byte) int {
	h := fnv.New32a()
	_, _ = h.Write(b)
	return int(h.Sum32() & 0x7fffffff)
}

// Countries returns the frozen country calling code catalog (empty means not seeded; the caller should fall back to the built-in minimal set).
func Countries() domain.CountriesList {
	load()
	return countries
}

// Timezones returns the timezone catalog and its content hash (empty means not seeded).
func Timezones() ([]Timezone, int) {
	load()
	return timezones, timezonesHash
}

// EmojiGroups returns the emoji category catalog and its content hash (empty means not seeded).
func EmojiGroups() ([]EmojiGroup, int) {
	load()
	return emojiGroups, emojiGroupsHash
}

// EmojiKeywords returns the emoji keyword dictionary for the given language; ok=false means that language is not seeded.
func EmojiKeywords(lang string) (EmojiKeywordSet, bool) {
	load()
	ks, ok := emojiKeywordSets[lang]
	return ks, ok
}
