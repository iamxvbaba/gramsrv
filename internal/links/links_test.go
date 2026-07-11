package links

import (
	"net/url"
	"strings"
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

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "default", raw: "", want: "https://telesrv.net"},
		{name: "host and path", raw: "links.example.test/root/", want: "https://links.example.test/root"},
		{name: "local HTTP", raw: "http://127.0.0.1:2401/", want: "http://127.0.0.1:2401"},
		{name: "missing host", raw: "https://", wantErr: true},
		{name: "unsupported scheme", raw: "ftp://links.example.test", wantErr: true},
		{name: "credentials", raw: "https://user:pass@links.example.test", wantErr: true},
		{name: "query", raw: "https://links.example.test/root?tenant=one", wantErr: true},
		{name: "fragment", raw: "https://links.example.test/root#links", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateBaseURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateBaseURL(%q) succeeded with %q", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateBaseURL(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ValidateBaseURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestValidateAppScheme(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "default", raw: "", want: "telesrv"},
		{name: "normalized", raw: "  My-App+Dev  ", want: "my-app+dev"},
		{name: "starts with digit", raw: "1app", wantErr: true},
		{name: "colon", raw: "myapp:", wantErr: true},
		{name: "official tg", raw: "tg", wantErr: true},
		{name: "http", raw: "http", wantErr: true},
		{name: "https", raw: "https", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateAppScheme(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateAppScheme(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ValidateAppScheme(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestValidateAppName(t *testing.T) {
	if got, err := ValidateAppName("  Example Chat  "); err != nil || got != "Example Chat" {
		t.Fatalf("ValidateAppName valid = %q, %v", got, err)
	}
	for _, raw := range []string{"", "   ", "bad\nname", strings.Repeat("x", 65)} {
		if got, err := ValidateAppName(raw); err == nil {
			t.Fatalf("ValidateAppName(%q) = %q, want error", raw, got)
		}
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
