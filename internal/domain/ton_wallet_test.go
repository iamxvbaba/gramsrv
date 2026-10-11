package domain

import (
	"strings"
	"testing"
)

func TestTONDNSNameAcceptsTonNames(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"rayo.ton", "rayo.ton"},
		{"RAYO.TON", "rayo.ton"},
		{"@rayo.ton", "rayo.ton"},
		{"rayo", "rayo.ton"},
		{"@rayo", "rayo.ton"},
		{" Rayo ", "rayo.ton"},
		{"sub.rayo.ton", "sub.rayo.ton"},
	} {
		got, ok := TONDNSName(tc.raw)
		if !ok || got != tc.want {
			t.Errorf("TONDNSName(%q) = %q,%v want %q,true", tc.raw, got, ok, tc.want)
		}
	}
}

func TestTONDNSNameRejectsTelegramIdentities(t *testing.T) {
	for _, raw := range []string{
		"rayo.t.me",
		"@rayo.t.me",
		"t.me/rayo",
		"https://rayo.t.me",
		"",
		"rayo..ton",
		"-rayo.ton",
		strings.Repeat("a", 129),
	} {
		if got, ok := TONDNSName(raw); ok {
			t.Errorf("TONDNSName(%q) = %q,true, want reject", raw, got)
		}
	}
}

func TestCanonicalTONAddressRejectsUsernameForms(t *testing.T) {
	for _, raw := range []string{"rayo", "@rayo", "rayo.t.me", "t.me/rayo", "rayo.ton"} {
		if got, err := CanonicalTONAddress(raw); err == nil {
			t.Errorf("CanonicalTONAddress(%q) = %q,nil, want error", raw, got)
		}
	}
}

func TestCanonicalTONAddressCanonicalizesRawForm(t *testing.T) {
	got, err := CanonicalTONAddress("EQAAAQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHx2j")
	if err != nil {
		t.Fatalf("CanonicalTONAddress: %v", err)
	}
	if want := "0:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"; got != want {
		t.Errorf("CanonicalTONAddress = %q want %q", got, want)
	}
}

func TestNormalizeUsernameLinkForms(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"rayo", "rayo"},
		{"@rayo", "rayo"},
		{"t.me/rayo", "rayo"},
		{"https://t.me/rayo", "rayo"},
		{"https://t.me/@rayo", "rayo"},
		{"rayo.t.me", "rayo"},
		{"https://rayo.t.me", "rayo"},
		{"@rayo.t.me", "rayo"},
		{"  @Rayo  ", "Rayo"},
	} {
		if got := NormalizeUsername(tc.raw); got != tc.want {
			t.Errorf("NormalizeUsername(%q) = %q want %q", tc.raw, got, tc.want)
		}
	}
}
