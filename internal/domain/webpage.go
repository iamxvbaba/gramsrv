package domain

import (
	"hash/fnv"
	"net/url"
	"strings"
)

// NormalizeWebPageURL normalizes a link in a user message into a stable form for dedup: it accepts only http/https,
// rejects URLs with userinfo (SSRF/impersonation defense), lowercases the scheme and host, drops the default port and fragment,
// and keeps the path and query as-is. It returns the normalized URL and whether it is previewable (false when not previewable).
func NormalizeWebPageURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if u.User != nil {
		return "", false
	}
	host := strings.ToLower(u.Host)
	if host == "" {
		return "", false
	}
	// Drop the default port matching the scheme, so example.com and example.com:80 do not hash differently.
	if h, port, ok := splitHostPort(host); ok {
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			host = h
		}
	}
	out := scheme + "://" + host + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out, true
}

// splitHostPort splits host:port; ok=false when there is no port. It does not use net.SplitHostPort so that a bare IPv6
// literal host (no port) is not treated as an error.
func splitHostPort(host string) (string, string, bool) {
	// An IPv6 literal looks like [::1]:443; the port is only searched for after the trailing ].
	idx := strings.LastIndexByte(host, ':')
	if idx < 0 {
		return host, "", false
	}
	if close := strings.LastIndexByte(host, ']'); close >= 0 && idx < close {
		return host, "", false
	}
	return host[:idx], host[idx+1:], true
}

// IsPendingWebPageMedia reports whether media is a pending link preview placeholder with ID==id (the
// idempotency guard for async parsing: only this state may be replaced in place by a resolved result).
func IsPendingWebPageMedia(m *MessageMedia, id int64) bool {
	return m != nil &&
		m.Kind == MessageMediaKindWebPage &&
		m.WebPage != nil &&
		m.WebPage.State == MessageWebPageStatePending &&
		m.WebPage.ID == id
}

// WebPageURLHash computes a stable 63-bit hash of the normalized URL, used both as the web_pages row primary key and
// as the webPage id (so a pending placeholder and its done resolution carry the same id). FNV-1a/64, made positive.
func WebPageURLHash(normalized string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(normalized))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}
