package telegramlogin

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/idna"

	"telesrv/internal/domain"
)

const maxTelegramLoginURLLength = 4096

func NormalizeRedirectURI(raw string, allowHTTP bool) (normalized, domainName string, err error) {
	u, err := parseWebURL(raw, allowHTTP)
	if err != nil {
		return "", "", err
	}
	if u.Fragment != "" {
		return "", "", domain.ErrTelegramLoginURLInvalid
	}
	query := u.Query()
	for _, reserved := range []string{"code", "state", "error", "error_description"} {
		if _, exists := query[reserved]; exists {
			return "", "", domain.ErrTelegramLoginURLInvalid
		}
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), u.Hostname(), nil
}

func NormalizeWebOrigin(raw string, allowHTTP bool) (string, error) {
	u, err := parseWebURL(raw, allowHTTP)
	if err != nil {
		return "", err
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	u.Path = ""
	return u.String(), nil
}

func parseWebURL(raw string, allowHTTP bool) (*url.URL, error) {
	if raw == "" || len(raw) > maxTelegramLoginURLLength || raw != strings.TrimSpace(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return nil, domain.ErrTelegramLoginURLInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.User != nil || u.Host == "" {
		return nil, domain.ErrTelegramLoginURLInvalid
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return nil, domain.ErrTelegramLoginURLInvalid
	}
	if ip := net.ParseIP(host); ip == nil {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || host == "" {
			return nil, domain.ErrTelegramLoginURLInvalid
		}
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, domain.ErrTelegramLoginURLInvalid
		}
	}
	switch u.Scheme {
	case "https":
		if port == "443" {
			port = ""
		}
	case "http":
		if !allowHTTP {
			return nil, domain.ErrTelegramLoginURLInvalid
		}
		if port == "80" {
			port = ""
		}
	default:
		return nil, domain.ErrTelegramLoginURLInvalid
	}
	if port == "" {
		if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
			u.Host = "[" + host + "]"
		} else {
			u.Host = host
		}
	} else {
		u.Host = net.JoinHostPort(host, port)
	}
	return u, nil
}

func AppendAuthorizationResult(redirectURI, code, state string) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil || !u.IsAbs() || code == "" {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	q := u.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func AppendAuthorizationError(redirectURI, errorCode, state string) (string, error) {
	switch errorCode {
	case "access_denied", "temporarily_unavailable", "server_error", "invalid_request", "invalid_scope", "unsupported_response_type":
	default:
		return "", domain.ErrTelegramLoginRequestInvalid
	}
	u, err := url.Parse(redirectURI)
	if err != nil || !u.IsAbs() {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	q := u.Query()
	q.Set("error", errorCode)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
