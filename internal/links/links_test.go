package links

import (
	"net/url"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "default", raw: "", want: "https://telesrv.net"},
		{name: "host only", raw: "telesrv.net/", want: "https://telesrv.net"},
		{name: "local http", raw: "http://127.0.0.1:2401/", want: "http://127.0.0.1:2401"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeBaseURL(tt.raw); got != tt.want {
				t.Fatalf("NormalizeBaseURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestBuildPreservesBasePathAndQuery(t *testing.T) {
	got := Build("http://127.0.0.1:2401/root/", "/call/abc", url.Values{"slug": []string{"abc"}})
	if want := "http://127.0.0.1:2401/root/call/abc?slug=abc"; got != want {
		t.Fatalf("Build = %q, want %q", got, want)
	}
}

func TestBuildDoesNotDoubleEscapeBasePath(t *testing.T) {
	got := Build("http://127.0.0.1:2401/root%20path/", "/addlist/slug", nil)
	if want := "http://127.0.0.1:2401/root%20path/addlist/slug"; got != want {
		t.Fatalf("Build encoded path = %q, want %q", got, want)
	}
}

func TestHostDropsPort(t *testing.T) {
	if got, want := Host("http://127.0.0.1:2401"), "127.0.0.1"; got != want {
		t.Fatalf("Host = %q, want %q", got, want)
	}
}

func TestCleanAndValidateChatlistSlug(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		clean string
		valid bool
	}{
		{name: "raw", raw: "abc.DEF-12", clean: "abc.DEF-12", valid: true},
		{name: "public url", raw: "http://127.0.0.1:2401/addlist/abc-12?x=1", clean: "abc-12", valid: true},
		{name: "app url", raw: "telesrv://addlist?slug=abc_12", clean: "abc_12", valid: true},
		{name: "bad char", raw: "abc/../bad!", clean: "bad!", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanChatlistSlug(tt.raw)
			if got != tt.clean {
				t.Fatalf("CleanChatlistSlug(%q) = %q, want %q", tt.raw, got, tt.clean)
			}
			if valid := ValidChatlistSlug(got); valid != tt.valid {
				t.Fatalf("ValidChatlistSlug(%q) = %v, want %v", got, valid, tt.valid)
			}
		})
	}
}
