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
