package updatecdn

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandlerServesCurrentResolveAndRange(t *testing.T) {
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	if err := os.Mkdir(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageData := []byte("0123456789abcdef")
	packageName := "tx64upd7007000"
	if err := os.WriteFile(filepath.Join(filesDir, packageName), packageData, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(packageData)
	manifestPath := writeTestManifest(t, dir, Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop: map[string]map[string]DesktopRelease{
			"win64": {"stable": {Build: 7007000, File: packageName, SHA256: hex.EncodeToString(hash[:])}},
		},
		Apps: map[string]map[string]ApplicationRelease{
			"ios": {"stable": {ID: 8, Version: "12.9.1", URL: "https://apps.apple.com/app/id1", Notes: map[string]string{"en": "Update available"}}},
		},
	})
	store, err := NewStore(manifestPath, filesDir)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/current4")
	if err != nil {
		t.Fatal(err)
	}
	currentBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(currentBody), `"released":7007000`) {
		t.Fatalf("current4 = %d %s", resp.StatusCode, currentBody)
	}

	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := client.Resolve(t.Context(), ResolveRequest{Platform: "ios", Version: "12.9.0", LangCode: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.ID != 8 || resolved.Version != "12.9.1" {
		t.Fatalf("resolved = %#v", resolved)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/files/"+packageName, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=2-5")
	rangeResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rangeBody, _ := io.ReadAll(rangeResp.Body)
	rangeResp.Body.Close()
	if rangeResp.StatusCode != http.StatusPartialContent || string(rangeBody) != "2345" {
		t.Fatalf("range = %d %q", rangeResp.StatusCode, rangeBody)
	}
}

func TestFileConditionalRequestsValidatePackage(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, tc := range []struct {
			name       string
			fileState  string
			condition  string
			ifMatch    string
			wantStatus int
		}{
			{name: "exact etag", wantStatus: http.StatusNotModified},
			{name: "weak etag", condition: "weak", wantStatus: http.StatusNotModified},
			{name: "etag list", condition: "list", wantStatus: http.StatusNotModified},
			{name: "wildcard", condition: "wildcard", wantStatus: http.StatusNotModified},
			{name: "if-match takes precedence", ifMatch: `"different"`, wantStatus: http.StatusPreconditionFailed},
			{name: "changed file", fileState: "changed", wantStatus: http.StatusServiceUnavailable},
			{name: "missing file", fileState: "missing", wantStatus: http.StatusServiceUnavailable},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				const name = "tx64upd7007000"
				filePath := filepath.Join(dir, name)
				data := []byte("signed-package")
				if err := os.WriteFile(filePath, data, 0o600); err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(data)
				digest := hex.EncodeToString(hash[:])
				manifestPath := writeTestManifest(t, dir, Manifest{
					SchemaVersion: ManifestSchemaVersion,
					Desktop: map[string]map[string]DesktopRelease{
						"win64": {"stable": {Build: 7007000, File: name, SHA256: digest}},
					},
				})
				store, err := NewStore(manifestPath, dir)
				if err != nil {
					t.Fatal(err)
				}
				handler, err := NewHandler(store)
				if err != nil {
					t.Fatal(err)
				}
				switch tc.fileState {
				case "changed":
					err = os.WriteFile(filePath, []byte("truncated"), 0o600)
				case "missing":
					err = os.Remove(filePath)
				}
				if err != nil {
					t.Fatal(err)
				}
				etag := `"sha256-` + digest + `"`
				condition := etag
				switch tc.condition {
				case "weak":
					condition = "W/" + etag
				case "list":
					condition = `"other", ` + etag
				case "wildcard":
					condition = "*"
				}
				req := httptest.NewRequest(method, "/files/"+name, nil)
				req.Header.Set("If-None-Match", condition)
				if tc.ifMatch != "" {
					req.Header.Set("If-Match", tc.ifMatch)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d; body = %q", rec.Code, tc.wantStatus, rec.Body.String())
				}
				if rec.Code == http.StatusNotModified {
					if got := rec.Header().Get("ETag"); got != etag {
						t.Errorf("ETag = %q, want %q", got, etag)
					}
					if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
						t.Errorf("Cache-Control = %q", got)
					}
					if rec.Body.Len() != 0 {
						t.Errorf("304 response has body %q", rec.Body.String())
					}
				}
			})
		}
	}
}
