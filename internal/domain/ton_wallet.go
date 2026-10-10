package domain

import (
	"errors"
	"strings"

	"github.com/xssnick/tonutils-go/address"
)

// CanonicalTONAddress rejects anything but a mainnet basechain address.
func CanonicalTONAddress(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	var (
		a   *address.Address
		err error
	)
	if strings.Contains(trimmed, ":") {
		a, err = address.ParseRawAddr(trimmed)
	} else {
		a, err = address.ParseAddr(trimmed)
	}
	if err != nil || a == nil || a.IsAddrNone() || a.Workchain() != 0 || a.IsTestnetOnly() {
		return "", errors.New("invalid mainnet address")
	}
	return a.StringRaw(), nil
}

// TelegramWalletInput recognizes what an operator typed as a Telegram identity
// instead of an address: rayo, @rayo, rayo.t.me, t.me/rayo. It returns the bare
// username; the wallet itself always comes from a verified record, never from
// the name.
func TelegramWalletInput(raw string) (string, bool) {
	name := strings.ToLower(NormalizeUsername(raw))
	if len(name) < 3 || len(name) > 32 {
		return "", false
	}
	if name[0] < 'a' || name[0] > 'z' {
		return "", false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return "", false
		}
	}
	return name, true
}

// TONDNSName recognizes what an operator typed instead of an address: a .ton
// name, optionally written as a bare collectible username or with a leading @.
// The result is the normalized name whose wallet record must be looked up.
// WHY: a t.me link is a Telegram identity, never a wallet, so only names that
// end in the official .ton zone are accepted here and the address still comes
// from the on-chain wallet record, never from the name itself.
func TONDNSName(raw string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.TrimPrefix(name, "@")
	if name == "" || len(name) > 128 {
		return "", false
	}
	if !strings.Contains(name, ".") {
		name += ".ton"
	}
	if !strings.HasSuffix(name, ".ton") {
		return "", false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return "", false
		}
		for i, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			case r == '-' && i > 0 && i < len(label)-1:
			default:
				return "", false
			}
		}
	}
	return name, true
}
