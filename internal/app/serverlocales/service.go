package serverlocales

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
	seed "telesrv/internal/seed/serverlocales"
)

const DefaultLang = "en"

type contextKey struct{}

var requiredMessageKeys = []string{
	"login.code",
	"login.code.title",
	"new_login",
	"new_login.title",
	"new_login.settings",
	"new_login.active_sessions",
	"unknown_device",
	"unknown_location",
	"fallback_name",
	"account.frozen",
}

// Message is a localized server-authored text plus Telegram message entities.
type Message struct {
	Body     string
	Entities []domain.MessageEntity
}

type catalog struct {
	supported map[string]struct{}
	messages  map[string]map[string]string
}

var defaultCatalog = mustLoadCatalog()

// WithLanguage records the user-facing language selected for server-owned text.
func WithLanguage(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, contextKey{}, Normalize(lang))
}

// LanguageFromContext returns the normalized server-locale language from ctx.
func LanguageFromContext(ctx context.Context) string {
	if lang, ok := ctx.Value(contextKey{}).(string); ok && lang != "" {
		return lang
	}
	return DefaultLang
}

// Normalize maps Telegram lang codes to the closest supported server locale.
func Normalize(lang string) string {
	return defaultCatalog.normalize(lang)
}

// SupportedLanguages returns the current language codes advertised by bundled client language packs.
func SupportedLanguages() []string {
	out := make([]string, 0, len(defaultCatalog.supported))
	for lang := range defaultCatalog.supported {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}

// Text returns a localized string with English fallback.
func Text(lang, key string, params map[string]string) string {
	return defaultCatalog.text(lang, key, params)
}

// LoginCodeMessage builds the localized official-account login code message.
func LoginCodeMessage(lang, code string) Message {
	params := map[string]string{
		"code":    code,
		"product": branding.ProductName,
	}
	body := Text(lang, "login.code", params)
	title := Text(lang, "login.code.title", params)
	return Message{
		Body: body,
		Entities: boldTerms(body,
			title,
			code,
		),
	}
}

type NewLoginParams struct {
	Name     string
	When     time.Time
	Device   string
	Location string
}

// NewLoginMessage builds the localized service notification for a new session.
func NewLoginMessage(lang string, p NewLoginParams) Message {
	normalized := Normalize(lang)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = Text(normalized, "fallback_name", nil)
	}
	device := strings.TrimSpace(p.Device)
	if device == "" {
		device = Text(normalized, "unknown_device", nil)
	}
	location := strings.TrimSpace(p.Location)
	if location == "" {
		location = Text(normalized, "unknown_location", nil)
	}
	body := Text(normalized, "new_login", map[string]string{
		"name":     name,
		"time":     formatTime(normalized, p.When),
		"device":   device,
		"location": location,
	})
	return Message{
		Body: body,
		Entities: boldTerms(body,
			Text(normalized, "new_login.title", nil),
			Text(normalized, "new_login.settings", nil),
			Text(normalized, "new_login.active_sessions", nil),
		),
	}
}

func mustLoadCatalog() catalog {
	c, err := loadCatalog()
	if err != nil {
		panic(err)
	}
	return c
}

func loadCatalog() (catalog, error) {
	c := catalog{
		supported: map[string]struct{}{},
		messages:  map[string]map[string]string{},
	}
	rawSupported, err := seed.FS.ReadFile("supported.json")
	if err != nil {
		return catalog{}, err
	}
	var supported struct {
		Languages []string `json:"languages"`
	}
	if err := json.Unmarshal(rawSupported, &supported); err != nil {
		return catalog{}, err
	}
	for _, lang := range supported.Languages {
		if normalized := normalizeCode(lang); normalized != "" {
			c.supported[normalized] = struct{}{}
		}
	}
	entries, err := seed.FS.ReadDir(".")
	if err != nil {
		return catalog{}, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || name == "supported.json" {
			continue
		}
		lang := normalizeCode(strings.TrimSuffix(name, ".json"))
		if lang == "" {
			continue
		}
		raw, err := seed.FS.ReadFile(name)
		if err != nil {
			return catalog{}, err
		}
		values := map[string]string{}
		if err := json.Unmarshal(raw, &values); err != nil {
			return catalog{}, fmt.Errorf("load %s: %w", name, err)
		}
		c.messages[lang] = values
	}
	if _, ok := c.messages[DefaultLang]; !ok {
		return catalog{}, fmt.Errorf("default server locale %q is missing", DefaultLang)
	}
	for lang := range c.supported {
		values, ok := c.messages[lang]
		if !ok {
			return catalog{}, fmt.Errorf("server locale %q is listed as supported but %s.json is missing", lang, lang)
		}
		for _, key := range requiredMessageKeys {
			if values[key] == "" {
				return catalog{}, fmt.Errorf("server locale %q is missing key %q", lang, key)
			}
		}
	}
	return c, nil
}

func (c catalog) normalize(lang string) string {
	code := normalizeCode(lang)
	if code == "" {
		return DefaultLang
	}
	if _, ok := c.supported[code]; ok {
		return code
	}
	base, _, _ := strings.Cut(code, "-")
	if _, ok := c.supported[base]; ok {
		return base
	}
	return DefaultLang
}

func (c catalog) text(lang, key string, params map[string]string) string {
	normalized := c.normalize(lang)
	value := c.messages[normalized][key]
	if value == "" && normalized != DefaultLang {
		value = c.messages[DefaultLang][key]
	}
	if value == "" {
		value = key
	}
	for name, replacement := range params {
		value = strings.ReplaceAll(value, "{{"+name+"}}", replacement)
	}
	return value
}

func normalizeCode(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	lang = strings.ReplaceAll(lang, "_", "-")
	switch lang {
	case "", "default":
		return ""
	case "pt":
		return "pt-br"
	case "zh", "zh-cn", "zh-sg":
		return "zh-hans"
	case "zh-tw", "zh-hk", "zh-mo":
		return "zh-hant"
	default:
		return lang
	}
}

func formatTime(lang string, t time.Time) string {
	utc := t.UTC()
	switch Normalize(lang) {
	case "ru":
		return utc.Format("02/01/2006 в 15:04:05 UTC")
	default:
		return utc.Format(time.RFC1123)
	}
}

func boldTerms(body string, terms ...string) []domain.MessageEntity {
	out := make([]domain.MessageEntity, 0, len(terms))
	seen := map[string]struct{}{}
	for _, term := range terms {
		if term == "" {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		offset := strings.Index(body, term)
		if offset < 0 {
			continue
		}
		out = append(out, domain.MessageEntity{
			Type:   domain.MessageEntityBold,
			Offset: utf16Len(body[:offset]),
			Length: utf16Len(term),
		})
	}
	return out
}

func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}
