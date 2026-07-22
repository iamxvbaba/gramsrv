package links

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	DefaultPublicBaseURL = "https://telesrv.net"
	DefaultWebBaseURL    = "https://web.telesrv.net"
	DefaultAppScheme     = "telesrv"
	DefaultAppName       = "telesrv"
)
const MaxChatlistSlugBytes = 128

// AppLinkBuilder builds client-visible custom-scheme links. Without an
// explicit base it preserves Telegram's route-as-host shape, for example
// telesrv://oauth?token=... . A configured base uses an exact server host and
// moves the route into the path, for example owpg://example.test/oauth?token=...
// . The legacy scheme remains accepted so in-flight links survive a rollout.
type AppLinkBuilder struct {
	legacyScheme string
	baseScheme   string
	baseHost     string
}

// ValidateAppScheme normalizes the client-visible custom URL scheme used by
// public landing pages. Standard Web schemes and Telegram's official tg scheme
// are deliberately rejected: the latter remains a manual compatibility link
// and must never become the automatic open target.
func ValidateAppScheme(raw string) (string, error) {
	scheme := strings.ToLower(strings.TrimSpace(raw))
	if scheme == "" {
		scheme = DefaultAppScheme
	}
	for i, r := range scheme {
		if (r >= 'a' && r <= 'z') || (i > 0 && ((r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.')) {
			continue
		}
		return "", fmt.Errorf("must match [a-z][a-z0-9+.-]*")
	}
	switch scheme {
	case "http", "https", "tg":
		return "", fmt.Errorf("reserved scheme %q is not allowed", scheme)
	}
	return scheme, nil
}

// ValidateAppLinkBase validates the optional host-based custom app-link root.
// The base is deliberately limited to <custom-scheme>://<host>: routes, query
// parameters, and fragments are owned by the individual link builders.
func ValidateAppLinkBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	if parsed.Opaque != "" {
		return "", fmt.Errorf("opaque URLs are not allowed")
	}
	if parsed.Scheme == "" {
		return "", fmt.Errorf("scheme is required")
	}
	scheme, err := ValidateAppScheme(parsed.Scheme)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("host is required")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("credentials are not allowed")
	}
	if parsed.Port() != "" {
		return "", fmt.Errorf("port is not allowed")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" {
		return "", fmt.Errorf("path is not allowed")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("query parameters are not allowed")
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("fragment is not allowed")
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = ""
	return parsed.String(), nil
}

func NewAppLinkBuilder(legacyScheme, rawBase string) (AppLinkBuilder, error) {
	legacyScheme, err := ValidateAppScheme(legacyScheme)
	if err != nil {
		return AppLinkBuilder{}, fmt.Errorf("legacy scheme: %w", err)
	}
	base, err := ValidateAppLinkBase(rawBase)
	if err != nil {
		return AppLinkBuilder{}, fmt.Errorf("app link base: %w", err)
	}
	builder := AppLinkBuilder{legacyScheme: legacyScheme}
	if base != "" {
		parsed, _ := url.Parse(base)
		builder.baseScheme = parsed.Scheme
		builder.baseHost = parsed.Host
	}
	return builder, nil
}

func (b AppLinkBuilder) Build(route string, query url.Values) string {
	if b.baseHost != "" {
		return (&url.URL{
			Scheme:   b.baseScheme,
			Host:     b.baseHost,
			Path:     "/" + strings.Trim(route, "/"),
			RawQuery: query.Encode(),
		}).String()
	}
	return (&url.URL{Scheme: b.legacyScheme, Host: route, RawQuery: query.Encode()}).String()
}

// BuildUsername preserves the official resolve query in legacy mode while a
// host-based multi-server client receives the public username as the path.
func (b AppLinkBuilder) BuildUsername(username string, query url.Values) string {
	query = cloneValues(query)
	if b.baseHost != "" {
		query.Del("domain")
		return b.Build(username, query)
	}
	query.Set("domain", username)
	return b.Build("resolve", query)
}

// MatchesRoute accepts the exact configured host-path form and the retained
// legacy route-as-host form. Query validation remains the caller's concern.
func (b AppLinkBuilder) MatchesRoute(parsed *url.URL, route string) bool {
	if parsed == nil || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawPath != "" {
		return false
	}
	if b.MatchesLegacyRoute(parsed, route) {
		return true
	}
	return b.baseHost != "" &&
		strings.EqualFold(parsed.Scheme, b.baseScheme) &&
		strings.EqualFold(parsed.Host, b.baseHost) &&
		parsed.Path == "/"+route
}

func (b AppLinkBuilder) MatchesLegacyRoute(parsed *url.URL, route string) bool {
	return parsed != nil && parsed.Opaque == "" && parsed.User == nil && parsed.Fragment == "" && parsed.RawPath == "" &&
		strings.EqualFold(parsed.Scheme, b.legacyScheme) &&
		strings.EqualFold(parsed.Host, route) && parsed.Path == ""
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}
	return cloned
}

func ValidateAppName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if len([]rune(name)) > 64 {
		return "", fmt.Errorf("must not exceed 64 characters")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("must not contain control characters")
		}
	}
	return name, nil
}

func NormalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultPublicBaseURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return strings.TrimRight(raw, "/")
}

// ValidateBaseURL normalizes and validates a client-visible HTTP(S) base URL.
// A path prefix is allowed, but credentials, query parameters, and fragments
// are not part of a stable public-link root.
func ValidateBaseURL(raw string) (string, error) {
	normalized := NormalizeBaseURL(raw)
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	if parsed.Opaque != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("host is required")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("credentials are not allowed")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("query parameters are not allowed")
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("fragment is not allowed")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func Build(baseURL, path string, query url.Values) string {
	baseURL, err := ValidateBaseURL(baseURL)
	if err != nil {
		baseURL = DefaultPublicBaseURL
	}
	parsed, _ := url.Parse(baseURL)
	basePath := strings.TrimRight(parsed.Path, "/")
	path = strings.TrimLeft(path, "/")
	if path != "" {
		parsed.Path = basePath + "/" + path
	} else if basePath != "" {
		parsed.Path = basePath
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func Host(baseURL string) string {
	baseURL, err := ValidateBaseURL(baseURL)
	if err != nil {
		return "telesrv.net"
	}
	parsed, _ := url.Parse(baseURL)
	if host := parsed.Hostname(); host != "" {
		return host
	}
	return parsed.Host
}

func CleanChatlistSlug(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if parsed, err := url.Parse(raw); err == nil {
			if slug := parsed.Query().Get("slug"); slug != "" {
				raw = slug
			} else {
				raw = strings.Trim(parsed.Path, "/")
			}
		}
	}
	raw = strings.TrimPrefix(raw, "addlist/")
	raw = strings.Trim(raw, "/")
	if idx := strings.LastIndex(raw, "/"); idx >= 0 {
		raw = raw[idx+1:]
	}
	if idx := strings.IndexAny(raw, "?#"); idx >= 0 {
		raw = raw[:idx]
	}
	if decoded, err := url.PathUnescape(raw); err == nil {
		raw = decoded
	}
	return raw
}

func ValidChatlistSlug(slug string) bool {
	if slug == "" || len(slug) > MaxChatlistSlugBytes {
		return false
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}
