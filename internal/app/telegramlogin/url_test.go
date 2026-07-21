package telegramlogin

import (
	"errors"
	"net/url"
	"testing"

	"telesrv/internal/domain"
)

func TestNormalizeRedirectURIIsExactAndRejectsOpenRedirectShapes(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		allowHTTP bool
		want      string
		valid     bool
	}{
		{name: "https canonical", raw: "https://EXAMPLE.com:443/callback?tenant=one", want: "https://example.com/callback?tenant=one", valid: true},
		{name: "idna", raw: "https://例子.测试/callback", want: "https://xn--fsqu00a.xn--0zwm56d/callback", valid: true},
		{name: "http hostname enabled", raw: "http://example.com:8080/callback", allowHTTP: true, want: "http://example.com:8080/callback", valid: true},
		{name: "http ipv4 enabled", raw: "http://192.0.2.25:3000/callback", allowHTTP: true, want: "http://192.0.2.25:3000/callback", valid: true},
		{name: "http disabled", raw: "http://example.com/callback"},
		{name: "userinfo", raw: "https://user@example.com/callback"},
		{name: "fragment", raw: "https://example.com/callback#token"},
		{name: "reserved code", raw: "https://example.com/callback?code=attacker"},
		{name: "leading whitespace", raw: " https://example.com/callback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := NormalizeRedirectURI(test.raw, test.allowHTTP)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("NormalizeRedirectURI() = %q,%v, want %q,nil", got, err, test.want)
				}
			} else if !errors.Is(err, domain.ErrTelegramLoginURLInvalid) {
				t.Fatalf("NormalizeRedirectURI() error = %v, want URL invalid", err)
			}
		})
	}
}

func TestAppendAuthorizationErrorPreservesState(t *testing.T) {
	got, err := AppendAuthorizationError("https://example.com/callback?tenant=one", "access_denied", "opaque")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	if u.Query().Get("tenant") != "one" || u.Query().Get("error") != "access_denied" || u.Query().Get("state") != "opaque" {
		t.Fatalf("error redirect = %q", got)
	}
	if _, err := AppendAuthorizationError("https://example.com/callback", "invalid_client", ""); err == nil {
		t.Fatal("unsafe authorization error unexpectedly accepted")
	}
}

func TestNormalizeWebOriginRejectsPathAndQuery(t *testing.T) {
	if got, err := NormalizeWebOrigin("https://Example.com/", false); err != nil || got != "https://example.com" {
		t.Fatalf("NormalizeWebOrigin = %q,%v", got, err)
	}
	for _, raw := range []string{"https://example.com/path", "https://example.com/?x=1", "https://example.com/#x"} {
		if _, err := NormalizeWebOrigin(raw, false); !errors.Is(err, domain.ErrTelegramLoginURLInvalid) {
			t.Fatalf("NormalizeWebOrigin(%q) error = %v, want URL invalid", raw, err)
		}
	}
}

func TestNormalizeHTTPIPv6PreservesURLBrackets(t *testing.T) {
	origin, err := NormalizeWebOrigin("http://[2001:db8::25]:80/", true)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "http://[2001:db8::25]" {
		t.Fatalf("origin=%q", origin)
	}
	redirect, domainName, err := NormalizeRedirectURI("http://[2001:db8::26]:3000/callback", true)
	if err != nil {
		t.Fatal(err)
	}
	if redirect != "http://[2001:db8::26]:3000/callback" || domainName != "2001:db8::26" {
		t.Fatalf("redirect=%q domain=%q", redirect, domainName)
	}
}

func TestPKCERFC7636Vector(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	got, err := PKCEChallenge(verifier)
	if err != nil || got != want {
		t.Fatalf("PKCEChallenge = %q,%v, want %q,nil", got, err, want)
	}
}

func TestCodeSealerUsesAADAndRetiringKeys(t *testing.T) {
	oldKey := make([]byte, 32)
	newKey := make([]byte, 32)
	oldKey[0], newKey[0] = 1, 2
	old, err := NewCodeSealer("old", map[string][]byte{"old": oldKey})
	if err != nil {
		t.Fatal(err)
	}
	sealed, nonce, keyID, err := old.Seal("authorization-code", []byte("request-1"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewCodeSealer("new", map[string][]byte{"old": oldKey, "new": newKey})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rotated.Open(sealed, nonce, keyID, []byte("request-1")); err != nil || got != "authorization-code" {
		t.Fatalf("Open after rotation = %q,%v", got, err)
	}
	if _, err := rotated.Open(sealed, nonce, keyID, []byte("request-2")); !errors.Is(err, domain.ErrTelegramLoginCodeInvalid) {
		t.Fatalf("Open with wrong AAD error = %v", err)
	}
}
