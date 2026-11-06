package updatecdn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogDesktopMapAndResolve(t *testing.T) {
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	if err := os.Mkdir(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageData := []byte("signed-tdesktop-update-package")
	packageName := "tx64upd7007000"
	if err := os.WriteFile(filepath.Join(filesDir, packageName), packageData, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(packageData)
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop: map[string]map[string]DesktopRelease{
			"win64": {"stable": {
				Build: 7007000, Version: "7.0.7", File: packageName,
				SHA256: hex.EncodeToString(hash[:]), Size: int64(len(packageData)),
			}},
		},
		Apps: map[string]map[string]ApplicationRelease{
			"android": {"stable": {
				ID: 77, Version: "12.9.1", URL: "https://updates.example/app.apk",
				URLBySource: map[string]string{"com.example.store": "https://store.example/app"},
				Notes:       map[string]string{"en": "New version", "ru": "Новая версия"},
			}},
		},
	}
	manifestPath := writeTestManifest(t, dir, manifest)
	catalog, err := LoadCatalog(manifestPath, filesDir)
	if err != nil {
		t.Fatal(err)
	}

	entry := catalog.DesktopMap()["win64"]["stable"]
	if entry["released"] != uint64(7007000) || entry["link"] != "/files/tx64upd7007000" {
		t.Fatalf("desktop entry = %#v", entry)
	}
	resolved, err := catalog.Resolve(ResolveRequest{
		Platform: "android", Version: "12.9.0 (500)", Source: "com.example.store", LangCode: "ru-RU",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.ID != 77 || resolved.Text != "Новая версия" || resolved.URL != "https://store.example/app" {
		t.Fatalf("resolved update = %#v", resolved)
	}
	current, err := catalog.Resolve(ResolveRequest{Platform: "android", Version: "12.9.1", LangCode: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if current != nil {
		t.Fatalf("current client got update %#v", current)
	}
}

func TestLoadCatalogRejectsPackageHashMismatch(t *testing.T) {
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	if err := os.Mkdir(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, "tx64upd7007000"), []byte("package"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := writeTestManifest(t, dir, Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop: map[string]map[string]DesktopRelease{
			"win64": {"stable": {Build: 7007000, File: "tx64upd7007000", SHA256: strings.Repeat("0", 64)}},
		},
	})
	if _, err := LoadCatalog(manifestPath, filesDir); err == nil {
		t.Fatal("hash mismatch accepted")
	}
}

func writeTestManifest(t *testing.T, dir string, manifest Manifest) string {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateDesktopChecksReusedPackageSize(t *testing.T) {
	filesDir := t.TempDir()
	data := []byte("package")
	const filename = "shared-update"
	if err := os.WriteFile(filepath.Join(filesDir, filename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	release := DesktopRelease{Build: 7007000, File: filename, SHA256: hex.EncodeToString(hash[:])}
	for _, tc := range []struct {
		name    string
		size    int64
		wantErr bool
	}{
		{name: "size omitted", size: 0},
		{name: "matching size", size: 7},
		{name: "mismatching size", size: 8, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &Catalog{
				manifest: Manifest{Desktop: map[string]map[string]DesktopRelease{
					"win64": {"stable": release},
				}},
				files: make(map[string]fileRecord),
			}
			if err := catalog.validateDesktop(filesDir); err != nil {
				t.Fatal(err)
			}
			// Deterministically exercise a second reference to an already verified
			// file, independent of Go's map iteration order across channels.
			second := release
			second.Size = tc.size
			catalog.manifest.Desktop = map[string]map[string]DesktopRelease{
				"win64": {"beta": second},
			}
			err := catalog.validateDesktop(filesDir)
			if tc.wantErr {
				if err == nil {
					t.Fatal("reused package with incorrect size was accepted")
				}
				if want := `desktop.win64.beta: package "shared-update" size = 7, want 8`; err.Error() != want {
					t.Fatalf("error = %q, want %q", err, want)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(catalog.files) != 1 {
				t.Fatalf("verified files = %d, want 1", len(catalog.files))
			}
		})
	}
}
