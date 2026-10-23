// Package branding owns the user-visible telesrv product identity.
//
// Protocol identifiers, client detection tokens and third-party compatibility
// headers do not belong here: callers must only pass text that is rendered to
// an end user.
package branding

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode"

	"telesrv/internal/links"
)

// Config is the deployment-wide, user-visible product identity. It is loaded
// once during process startup; protocol identifiers and client detection
// tokens deliberately remain outside this structure.
type Config struct {
	ProductName     string
	ProductUsername string
	DesktopAppName  string
	AndroidAppName  string
	IOSAppName      string
	MacOSAppName    string
	WebAAppName     string
	WebKAppName     string
	PremiumName     string
	StarsName       string
	PublicBaseURL   string
}

var (
	defaultConfig = Config{
		ProductName:     "ShuzaGram",
		ProductUsername: "shuzagram",
		DesktopAppName:  "ShuzaGram Desktop",
		AndroidAppName:  "ShuzaGram Android",
		IOSAppName:      "ShuzaGram iOS",
		MacOSAppName:    "ShuzaGram macOS",
		WebAAppName:     "ShuzaGram Web A",
		WebKAppName:     "ShuzaGram Web K",
		PremiumName:     "ShuzaGram Premium",
		StarsName:       "ShuzaGram Stars",
		PublicBaseURL:   links.DefaultPublicBaseURL,
	}
	configured atomic.Pointer[Config]
)

// DefaultConfig returns a copy of the default product identity.
func DefaultConfig() Config { return defaultConfig }

// Validate normalizes and validates a product identity without installing it.
func Validate(cfg Config) (Config, error) {
	for _, field := range []struct {
		name  string
		value *string
	}{
		{name: "product name", value: &cfg.ProductName},
		{name: "desktop app name", value: &cfg.DesktopAppName},
		{name: "Android app name", value: &cfg.AndroidAppName},
		{name: "iOS app name", value: &cfg.IOSAppName},
		{name: "macOS app name", value: &cfg.MacOSAppName},
		{name: "Web A app name", value: &cfg.WebAAppName},
		{name: "Web K app name", value: &cfg.WebKAppName},
		{name: "Premium name", value: &cfg.PremiumName},
		{name: "Stars name", value: &cfg.StarsName},
	} {
		normalized, err := validateDisplayName(*field.value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", field.name, err)
		}
		*field.value = normalized
	}
	cfg.ProductUsername = strings.TrimPrefix(strings.TrimSpace(cfg.ProductUsername), "@")
	if !validProductUsername(cfg.ProductUsername) {
		return Config{}, fmt.Errorf("product username must be 5-32 ASCII username characters and start with a letter")
	}
	cfg.ProductUsername = strings.ToLower(cfg.ProductUsername)
	var err error
	cfg.PublicBaseURL, err = links.ValidateBaseURL(cfg.PublicBaseURL)
	if err != nil {
		return Config{}, fmt.Errorf("public base URL: %w", err)
	}
	return cfg, nil
}

// Configure installs the validated process-wide identity before services are
// constructed. Readers only ever observe complete immutable snapshots.
func Configure(cfg Config) error {
	normalized, err := Validate(cfg)
	if err != nil {
		return err
	}
	configured.Store(&normalized)
	return nil
}

// Current returns a copy of the installed product identity.
func Current() Config {
	if cfg := configured.Load(); cfg != nil {
		return *cfg
	}
	return defaultConfig
}

func ProductName() string     { return Current().ProductName }
func ProductUsername() string { return Current().ProductUsername }
func PremiumName() string     { return Current().PremiumName }
func StarsName() string       { return Current().StarsName }
func PublicBaseURL() string   { return Current().PublicBaseURL }

// ClientAppName returns the branded display name for a stored client platform.
// Stored detection tokens remain unchanged; this is only used at presentation
// boundaries such as account.getAuthorizations.
func ClientAppName(platform string) string {
	cfg := Current()
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "android":
		return cfg.AndroidAppName
	case "ios":
		return cfg.IOSAppName
	case "macos":
		return cfg.MacOSAppName
	case "telegram-tt", "weba":
		return cfg.WebAAppName
	case "tweb", "webk":
		return cfg.WebKAppName
	case "tdesktop", "desktop", "windows":
		return cfg.DesktopAppName
	default:
		return cfg.ProductName
	}
}

// UserVisibleClientPlatform hides internal compatibility tokens from the
// authorization UI without changing their durable representation.
func UserVisibleClientPlatform(platform string) string {
	if strings.EqualFold(strings.TrimSpace(platform), "telegram-tt") {
		return "weba"
	}
	return UserVisibleText(platform, "")
}

var (
	officialHTTPHostRE = regexp.MustCompile(`(?i)https?://(?:[a-z0-9-]+\.)*telegram\.(?:org|com)([^a-z0-9]|$)`)
	officialBareHostRE = regexp.MustCompile(`(?i)(?:[a-z0-9-]+\.)*telegram\.(?:org|com)([^a-z0-9]|$)`)
	officialBrandRE    = regexp.MustCompile(`(?i)telegram|телеграм[\p{L}]*|تيليجرام|تلگرام|텔레그램|טלגרם`)
	technicalIDRE      = regexp.MustCompile(`^[A-Za-z0-9-]+(?:[._][A-Za-z0-9-]+)+$`)
)

// UserVisibleText replaces the official product brand and its public hosts in
// text returned to clients. Placeholder syntax, markup and string keys are
// deliberately untouched by callers; only values should pass through here.
func UserVisibleText(value, publicBaseURL string) string {
	if value == "" {
		return ""
	}
	baseURL, publicHost := publicDestination(publicBaseURL)
	value = officialHTTPHostRE.ReplaceAllString(value, baseURL+"${1}")
	value = officialBareHostRE.ReplaceAllString(value, publicHost+"${1}")
	// Some platform packs carry dotted or underscored runtime identifiers as
	// values. They are not copy and changing them can break client navigation.
	if technicalIDRE.MatchString(value) {
		return value
	}
	return officialBrandRE.ReplaceAllString(value, ProductName())
}

func publicDestination(raw string) (string, string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		raw = PublicBaseURL()
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		raw = PublicBaseURL()
		parsed, _ = url.Parse(raw)
	}
	return raw, parsed.Host
}

func validateDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if len([]rune(name)) > 64 {
		return "", fmt.Errorf("must not exceed 64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("must not contain control characters")
		}
	}
	return name, nil
}

func validProductUsername(username string) bool {
	if len(username) < 5 || len(username) > 32 {
		return false
	}
	for i, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_'):
		default:
			return false
		}
	}
	return true
}
