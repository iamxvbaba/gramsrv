package links

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildPreservesEscapedBasePath(t *testing.T) {
	for _, prefix := range []string{"/tenant%2Fone", "/tenant%2fone", "/tenant%2F", "/tenant%252Fone", "/root%20path"} {
		for _, trailingSlash := range []string{"", "/"} {
			base := "https://example.test" + prefix + trailingSlash
			t.Run(prefix+trailingSlash, func(t *testing.T) {
				normalized, err := ValidateBaseURL(base)
				if err != nil {
					t.Fatal(err)
				}
				if want := "https://example.test" + prefix; normalized != want {
					t.Fatalf("ValidateBaseURL(%q) = %q, want %q", base, normalized, want)
				}
				for _, path := range []string{"", "/", "/addlist/abc", "/name with space/100%?#"} {
					query := url.Values{"start": {"a+b"}}
					want := "https://example.test" + prefix
					if suffix := strings.TrimLeft(path, "/"); suffix != "" {
						want += "/" + (&url.URL{Path: suffix}).EscapedPath()
					}
					want += "?start=a%2Bb"
					if got := Build(base, path, query); got != want {
						t.Errorf("Build(%q, %q) = %q, want %q", base, path, got, want)
					}
				}
			})
		}
	}
}
