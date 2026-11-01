package domain

import "testing"

func TestNormalizeWebPageURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"lowercase-host-drop-fragment", "https://Example.COM/Path?q=1#frag", "https://example.com/Path?q=1", true},
		{"strip-default-http-port", "http://example.com:80/x", "http://example.com/x", true},
		{"strip-default-https-port", "https://example.com:443", "https://example.com", true},
		{"keep-nondefault-port", "http://example.com:8080/x", "http://example.com:8080/x", true},
		{"keep-query-case", "https://e.com/a?B=C", "https://e.com/a?B=C", true},
		{"reject-scheme", "ftp://example.com/x", "", false},
		{"reject-userinfo", "https://user:pass@example.com/x", "", false},
		{"reject-no-host", "https:///path", "", false},
		{"reject-plain-text", "not a url", "", false},
		{"reject-empty", "   ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NormalizeWebPageURL(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("NormalizeWebPageURL(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestWebPageURLHashStableAndPositive(t *testing.T) {
	const u = "https://example.com/article"
	h1 := WebPageURLHash(u)
	h2 := WebPageURLHash(u)
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %d != %d", h1, h2)
	}
	if h1 < 0 {
		t.Fatalf("hash must be non-negative (used as TL id), got %d", h1)
	}
	if WebPageURLHash("https://other.example/x") == h1 {
		t.Fatalf("distinct URLs collided")
	}
}

// TestNormalizeThenHashDedupes verifies that URLs equivalent after normalization hash identically (a stable dedup key).
func TestNormalizeThenHashDedupes(t *testing.T) {
	a, _ := NormalizeWebPageURL("https://Example.com:443/x")
	b, _ := NormalizeWebPageURL("https://example.com/x")
	if a != b {
		t.Fatalf("normalized forms differ: %q vs %q", a, b)
	}
	if WebPageURLHash(a) != WebPageURLHash(b) {
		t.Fatalf("equivalent URLs hashed differently")
	}
}
