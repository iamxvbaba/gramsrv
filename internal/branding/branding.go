// Package branding owns the user-visible telesrv product identity.
//
// Protocol identifiers, client detection tokens and third-party compatibility
// headers do not belong here: callers must only pass text that is rendered to
// an end user.
package branding

import (
	"net/url"
	"regexp"
	"strings"
)

const (
	ProductName      = "Telesrv"
	ProductUsername  = "telesrv"
	DesktopAppName   = "Telesrv Desktop"
	AndroidAppName   = "Telesrv Android"
	IOSAppName       = "Telesrv iOS"
	MacOSAppName     = "Telesrv macOS"
	WebAAppName      = "Telesrv Web A"
	WebKAppName      = "Telesrv Web K"
	PremiumName      = "Telesrv Premium"
	StarsName        = "Telesrv Stars"
	DefaultPublicURL = "https://telesrv.net"
)

// ClientAppName returns the branded display name for a stored client platform.
// Stored detection tokens remain unchanged; this is only used at presentation
// boundaries such as account.getAuthorizations.
func ClientAppName(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "android":
		return AndroidAppName
	case "ios":
		return IOSAppName
	case "macos":
		return MacOSAppName
	case "telegram-tt", "weba":
		return WebAAppName
	case "tweb", "webk":
		return WebKAppName
	case "tdesktop", "desktop", "windows":
		return DesktopAppName
	default:
		return ProductName
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
	officialHTTPHostRE = regexp.MustCompile(`(?i)https?://(?:[a-z0-9-]+\.)*(?:telegram\.(?:org|me|com|dog)|t\.me)([^a-z0-9]|$)`)
	officialBareHostRE = regexp.MustCompile(`(?i)(?:(?:[a-z0-9-]+\.)*telegram\.(?:org|me|com|dog)|\bt\.me)([^a-z0-9]|$)`)
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
	return officialBrandRE.ReplaceAllString(value, ProductName)
}

func publicDestination(raw string) (string, string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		raw = DefaultPublicURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		raw = DefaultPublicURL
		parsed, _ = url.Parse(raw)
	}
	return raw, parsed.Host
}
