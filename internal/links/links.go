package links

import (
	"net/url"
	"strings"
)

const DefaultPublicBaseURL = "https://telesrv.net"
const MaxChatlistSlugBytes = 128

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

func Build(baseURL, path string, query url.Values) string {
	baseURL = NormalizeBaseURL(baseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		baseURL = DefaultPublicBaseURL
		parsed, _ = url.Parse(baseURL)
	}
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
	parsed, err := url.Parse(NormalizeBaseURL(baseURL))
	if err != nil || parsed.Host == "" {
		return "telesrv.net"
	}
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
