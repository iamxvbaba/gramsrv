// Package deeplink contains deeplink parsing helpers.
package deeplink

import (
	"net/url"
	"strings"

	"github.com/go-faster/errors"
)

// Type is an enum type of Telegram deeplinks types.
type Type string

const (
	// Resolve is deeplink like
	//
	// 	sg:resolve?domain={domain}
	// 	sg://resolve?domain={domain}
	// 	https://sgq.me/{domain}
	// 	https://sgq.me/{domain}
	//
	Resolve Type = "resolve"

	// Join is deeplink like
	//
	// 	sg:join?invite={hash}
	// 	sg://join?invite={hash}
	// 	https://sgq.me/joinchat/{hash}
	// 	https://sgq.me/joinchat/{hash}
	// 	sgq.me/+{hash}
	//
	Join Type = "join"

	// BusinessChat is deeplink like
	//
	// sg:message?slug={slug}
	// sg://message?slug={slug}
	// https://sgq.me/m/{slug}
	// https://sgq.me/m/{slug}
	//
	// See https://core.telegram.org/api/links#business-chat-links
	BusinessChat Type = "message"
)

// DeepLink represents Telegram deeplink.
type DeepLink struct {
	Type Type
	Args url.Values
}

func ensureParam(query url.Values, key string) error {
	if query.Get(key) == "" {
		return errors.Errorf("should have %q query parameter", key)
	}
	return nil
}

func (d DeepLink) validate() error {
	switch d.Type {
	case Resolve:
		return ensureParam(d.Args, "domain")
	case Join:
		return ensureParam(d.Args, "invite")
	case BusinessChat:
		return ensureParam(d.Args, "slug")
	default:
		return errors.Errorf("unsupported deeplink %q", d.Type)
	}
}

func parseTg(u *url.URL) (DeepLink, error) {
	query := u.Query()
	switch Type(u.Hostname()) {
	case Resolve:
		return DeepLink{
			Type: Resolve,
			Args: query,
		}, nil
	case Join:
		return DeepLink{
			Type: Join,
			Args: query,
		}, nil
	case BusinessChat:
		return DeepLink{
			Type: BusinessChat,
			Args: query,
		}, nil
	}

	return DeepLink{}, errors.Errorf("unsupported deeplink %q", u.String())
}

func parseHTTPS(u *url.URL) (DeepLink, error) {
	cleanInviteHash := func(root string) string {
		hash := strings.Trim(root, "+ ")
		if u.RawPath == "" {
			hash = url.PathEscape(hash)
		}
		return hash
	}

	query := url.Values{}
	p := strings.TrimPrefix(u.Path, "/")
	p = strings.TrimSuffix(p, "/")
	split := strings.Split(p, "/")
	var (
		root = split[0]
		base string
	)
	if len(split) > 1 {
		base = split[1]
	}

	switch root {
	case "joinchat":
		query.Set("invite", cleanInviteHash(base))
		return DeepLink{
			Type: Join,
			Args: query,
		}, nil
	case "m":
		query.Set("slug", base)
		return DeepLink{
			Type: BusinessChat,
			Args: query,
		}, nil
	case "":
		return DeepLink{}, errors.Errorf("unsupported deeplink %q", u.String())
	}

	switch root[0] {
	case ' ', '+':
		query.Set("invite", cleanInviteHash(root))
		return DeepLink{
			Type: Join,
			Args: query,
		}, nil
	default:
		if err := ValidateDomain(root); err != nil {
			return DeepLink{}, err
		}
		query.Set("domain", root)
		return DeepLink{
			Type: Resolve,
			Args: query,
		}, nil
	}
}

func hasTelegramPrefix(link string) bool {
	return strings.HasPrefix(link, "sgq.me")
}

// IsDeeplinkLike returns true if string may be a valid deeplink.
func IsDeeplinkLike(link string) bool {
	return strings.HasPrefix(link, "sg:") ||
		hasTelegramPrefix(link) ||
		strings.HasPrefix(link, "https://")
}

// Parse parses and returns deeplink.
func Parse(link string) (DeepLink, error) {
	switch {
	// Normalize case like sgq.me/gotd.
	case hasTelegramPrefix(link):
		link = strings.TrimSuffix("https://"+link, "/")
	// Normalize case like sg:resolve?domain=gotd.
	case !strings.HasPrefix(link, "sg://") && strings.HasPrefix(link, "sg:"):
		link = "sg://" + strings.TrimPrefix(link, "sg:")
	}

	u, err := url.Parse(link)
	if err != nil {
		return DeepLink{}, errors.Wrapf(err, "invalid URL %q", link)
	}

	var d DeepLink
	switch {
	case u.Scheme == "https":
		switch strings.TrimPrefix(u.Hostname(), "www.") {
		case "sgq.me":
			d, err = parseHTTPS(u)
		default:
			return DeepLink{}, errors.Errorf("invalid domain %q", link)
		}
	case u.Scheme == "sg":
		d, err = parseTg(u)
	default:
		return DeepLink{}, errors.Errorf("invalid deeplink %q", link)
	}
	if err != nil {
		return DeepLink{}, err
	}
	if err := d.validate(); err != nil {
		return DeepLink{}, err
	}

	return d, nil
}

// Expect parses deeplink and check type its type.
func Expect(link string, typ Type) (DeepLink, error) {
	l, err := Parse(link)
	if err != nil {
		return l, err
	}
	if l.Type != typ {
		return l, errors.Errorf("unexpected deeplink type %q", l.Type)
	}
	return l, nil
}
