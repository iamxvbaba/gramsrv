package telegramlogin

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"telesrv/internal/domain"
)

var nativeApplicationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,254}$`)

func normalizeNativeApplicationID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !nativeApplicationIDPattern.MatchString(raw) || !strings.Contains(raw, ".") || strings.Contains(raw, "..") {
		return "", domain.ErrTelegramLoginClientInvalid
	}
	return raw, nil
}

func normalizeNativeVerificationID(platform domain.TelegramLoginNativePlatform, raw string) (string, error) {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	switch platform {
	case domain.TelegramLoginNativeIOS:
		if len(raw) != 10 || strings.IndexFunc(raw, func(r rune) bool {
			return !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
		}) >= 0 {
			return "", domain.ErrTelegramLoginClientInvalid
		}
	case domain.TelegramLoginNativeAndroid:
		raw = strings.ReplaceAll(raw, ":", "")
		if len(raw) != 64 || strings.IndexFunc(raw, func(r rune) bool {
			return !((r >= 'A' && r <= 'F') || (r >= '0' && r <= '9'))
		}) >= 0 {
			return "", domain.ErrTelegramLoginClientInvalid
		}
	default:
		return "", domain.ErrTelegramLoginClientInvalid
	}
	return raw, nil
}

// NormalizeNativeCallbackURI accepts the exact HTTPS universal/app link or a
// non-web custom scheme registered for a native application. Query and
// fragment components are forbidden because OAuth response fields are
// appended by the provider and must not collide with application input.
func NormalizeNativeCallbackURI(raw string, allowHTTP bool) (string, error) {
	if raw == "" || len(raw) > maxTelegramLoginURLLength || raw != strings.TrimSpace(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
		normalized, _, err := NormalizeRedirectURI(raw, allowHTTP)
		return normalized, err
	}
	scheme := strings.ToLower(u.Scheme)
	if !validAppScheme(scheme) || scheme == "tg" || scheme == "javascript" || scheme == "data" || scheme == "file" || u.Port() != "" {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || strings.IndexFunc(host, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.')
	}) >= 0 {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	u.Scheme, u.Host = scheme, host
	return u.String(), nil
}

func normalizeNativeDisplayName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 128 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", domain.ErrTelegramLoginClientInvalid
	}
	return raw, nil
}
