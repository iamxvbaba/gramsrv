package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// DefaultAndroidAppPackage is the applicationId of the ShuzaGram Android client.
const DefaultAndroidAppPackage = "com.shuzagram.messenger"

var (
	androidPackageRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)
	certSHA256RE     = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){31}$`)
)

// ValidateAndroidAppLinks normalizes the Android App Links identity served at
// /.well-known/assetlinks.json. Fingerprints are the SHA-256 of the APK signing
// certificate, e.g. the output of `keytool -list -v` or `apksigner verify --print-certs`.
func ValidateAndroidAppLinks(pkg string, fingerprints []string) (string, []string, error) {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		pkg = DefaultAndroidAppPackage
	}
	if !androidPackageRE.MatchString(pkg) {
		return "", nil, fmt.Errorf("Android package %q is not a valid applicationId", pkg)
	}
	out := make([]string, 0, len(fingerprints))
	seen := map[string]bool{}
	for _, raw := range fingerprints {
		fp := strings.ToUpper(strings.TrimSpace(raw))
		if fp == "" {
			continue
		}
		if !strings.Contains(fp, ":") && len(fp) == 64 {
			var b strings.Builder
			for i := 0; i < len(fp); i += 2 {
				if i > 0 {
					b.WriteByte(':')
				}
				b.WriteString(fp[i : i+2])
			}
			fp = b.String()
		}
		if !certSHA256RE.MatchString(fp) {
			return "", nil, fmt.Errorf("certificate fingerprint %q must be 32 hex bytes (AA:BB:...)", raw)
		}
		if !seen[fp] {
			seen[fp] = true
			out = append(out, fp)
		}
	}
	return pkg, out, nil
}

// assetLinksHandler lets Android verify that https://<public host>/ links
// belong to the ShuzaGram app, so they open in the app without a chooser.
func assetLinksHandler(pkg string, fingerprints []string) http.HandlerFunc {
	body, _ := json.Marshal([]map[string]any{{
		"relation": []string{"delegate_permission/common.handle_all_urls"},
		"target": map[string]any{
			"namespace":                "android_app",
			"package_name":             pkg,
			"sha256_cert_fingerprints": fingerprints,
		},
	}})
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	}
}
