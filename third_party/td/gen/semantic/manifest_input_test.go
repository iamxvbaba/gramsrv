package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUniverseLocksOverlaySHA256Independently(t *testing.T) {
	dir := t.TempDir()
	layerSource := []byte("---types---\nsample#10000001 = Sample;\n// LAYER 1\n")
	overlaySource := []byte("---types---\nlegacy#10000002 = Legacy;\n")
	manifestPath, overlayPath := writeLockedSchemaSet(t, dir, layerSource, overlaySource)

	if _, err := LoadUniverse(manifestPath); err != nil {
		t.Fatalf("load locked manifest: %v", err)
	}
	modified := append(append([]byte(nil), overlaySource...), []byte("other#10000003 = Other;\n")...)
	if err := os.WriteFile(overlayPath, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadUniverse(manifestPath)
	if err == nil || !strings.Contains(err.Error(), `overlay "legacy.tl" SHA256 mismatch`) {
		t.Fatalf("modified overlay error = %v", err)
	}
}

func TestLoadUniverseRequiresExplicitWireIDs(t *testing.T) {
	for _, test := range []struct {
		name    string
		layer   []byte
		overlay []byte
		want    string
	}{
		{
			name:    "Profile",
			layer:   []byte("---types---\nsample = Sample;\n// LAYER 1\n"),
			overlay: []byte("---types---\nlegacy#10000002 = Legacy;\n"),
			want:    `layer 1 file "layer-1.tl" line 2 definition "sample"`,
		},
		{
			name:    "Overlay",
			layer:   []byte("---types---\nsample#10000001 = Sample;\n// LAYER 1\n"),
			overlay: []byte("---types---\nlegacy = Legacy;\n"),
			want:    `overlay "legacy.tl" line 2 definition "legacy"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifestPath, _ := writeLockedSchemaSet(t, t.TempDir(), test.layer, test.overlay)
			_, err := LoadUniverse(manifestPath)
			if err == nil || !strings.Contains(err.Error(), "E_EXPLICIT_ID_REQUIRED") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("explicit ID error = %v", err)
			}
		})
	}
}

func TestReadManifestRequiresOverlayProvenance(t *testing.T) {
	dir := t.TempDir()
	manifest := Manifest{
		CanonicalLayer: 1,
		Repository:     "https://example.invalid/schema.git",
		SourcePath:     "api.tl",
		Overlays: []ManifestOverlay{{
			File:   "legacy.tl",
			SHA256: strings.Repeat("a", 64),
		}},
		Layers: []ManifestLayer{{
			Layer:  1,
			File:   "layer-1.tl",
			Commit: strings.Repeat("b", 40),
			Blob:   strings.Repeat("c", 40),
			SHA256: strings.Repeat("d", 64),
		}},
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	writeManifestInput(t, manifestPath, manifest)
	_, err := ReadManifest(manifestPath)
	if err == nil || !strings.Contains(err.Error(), "overlay[0] repository is empty") {
		t.Fatalf("missing overlay provenance error = %v", err)
	}
}

func writeLockedSchemaSet(t *testing.T, dir string, layerSource, overlaySource []byte) (manifestPath, overlayPath string) {
	t.Helper()
	layerPath := filepath.Join(dir, "layer-1.tl")
	overlayPath = filepath.Join(dir, "legacy.tl")
	if err := os.WriteFile(layerPath, layerSource, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlayPath, overlaySource, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		CanonicalLayer: 1,
		Repository:     "https://example.invalid/schema.git",
		SourcePath:     "api.tl",
		Overlays: []ManifestOverlay{{
			File:       "legacy.tl",
			Repository: "https://example.invalid/overlay.git",
			Commit:     strings.Repeat("a", 40),
			Blob:       strings.Repeat("b", 40),
			Path:       "legacy.tl",
			SHA256:     sha256InputHex(overlaySource),
		}},
		Layers: []ManifestLayer{{
			Layer:  1,
			File:   "layer-1.tl",
			Commit: strings.Repeat("c", 40),
			Blob:   strings.Repeat("d", 40),
			SHA256: sha256InputHex(layerSource),
		}},
	}
	manifestPath = filepath.Join(dir, "manifest.json")
	writeManifestInput(t, manifestPath, manifest)
	return manifestPath, overlayPath
}

func writeManifestInput(t *testing.T, path string, manifest Manifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sha256InputHex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
