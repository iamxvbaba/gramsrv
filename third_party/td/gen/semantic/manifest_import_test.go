package semantic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/gotd/tl"
)

func TestUpdateManifestLayerAndRenderCanonicalSchema(t *testing.T) {
	layer1 := []byte("---types---\nold#10000001 value:int = Sample;\n// LAYER 1\n")
	layer2 := []byte("---types---\nmodern#10000002 value:int = Sample;\n// LAYER 2\n")
	overlay := []byte("---types---\nlegacy#10000003 = Legacy;\n")
	baseArtifact, err := InspectLayerArtifact(layer1)
	if err != nil {
		t.Fatal(err)
	}
	overlayDigest := sha256.Sum256(overlay)
	manifest := Manifest{
		CanonicalLayer: 1,
		Repository:     "https://github.com/example/schema.git",
		SourcePath:     "api.tl",
		Overlays: []ManifestOverlay{{
			File:       "legacy.tl",
			Repository: "https://github.com/example/overlay.git",
			Commit:     strings.Repeat("a", 40),
			Blob:       strings.Repeat("b", 40),
			Path:       "legacy.tl",
			SHA256:     hex.EncodeToString(overlayDigest[:]),
		}},
		Layers: []ManifestLayer{{
			Layer:  1,
			File:   "layer-1.tl",
			Commit: strings.Repeat("c", 40),
			Blob:   baseArtifact.GitBlob,
			SHA256: baseArtifact.SHA256,
		}},
	}
	artifact, err := InspectLayerArtifact(layer2)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateManifestLayer(manifest, artifact, strings.Repeat("d", 40), "layer-2.tl", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CanonicalLayer != 2 || len(updated.Layers) != 2 || updated.Layers[1].Layer != 2 {
		t.Fatalf("updated manifest = %+v", updated)
	}
	if updated.Layers[1].Blob != artifact.GitBlob || updated.Layers[1].SHA256 != artifact.SHA256 {
		t.Fatalf("imported provenance = %+v, artifact=%+v", updated.Layers[1], artifact)
	}

	// Importing the exact same immutable artifact is deliberately idempotent.
	again, err := UpdateManifestLayer(updated, artifact, strings.Repeat("d", 40), "layer-2.tl", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if data1, _ := MarshalManifest(updated); !bytes.Equal(data1, mustMarshalManifest(t, again)) {
		t.Fatal("idempotent import changed manifest bytes")
	}

	rendered, err := RenderCanonicalSchema(updated, layer2, map[string][]byte{"legacy.tl": overlay})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"// Layer:  2",
		"/blob/" + strings.Repeat("d", 40) + "/api.tl",
		"modern#10000002",
		"legacy#10000003",
	} {
		if !bytes.Contains(rendered, []byte(want)) {
			t.Fatalf("canonical schema is missing %q:\n%s", want, rendered)
		}
	}
	parsed, err := tl.Parse(bytes.NewReader(rendered))
	if err != nil {
		t.Fatalf("parse rendered canonical schema: %v", err)
	}
	if parsed.Layer != 2 || len(parsed.Definitions) != 2 {
		t.Fatalf("rendered canonical schema = layer %d, definitions %d", parsed.Layer, len(parsed.Definitions))
	}
}

func TestUpdateManifestLayerRequiresExplicitReplace(t *testing.T) {
	source := []byte("---types---\nsample#10000001 = Sample;\n// LAYER 1\n")
	artifact, err := InspectLayerArtifact(source)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		CanonicalLayer: 1,
		Repository:     "https://example.invalid/schema.git",
		SourcePath:     "api.tl",
		Layers: []ManifestLayer{{
			Layer:  1,
			File:   "layer-1.tl",
			Commit: strings.Repeat("a", 40),
			Blob:   artifact.GitBlob,
			SHA256: artifact.SHA256,
		}},
	}
	_, err = UpdateManifestLayer(manifest, artifact, strings.Repeat("b", 40), "layer-1.tl", true, false)
	if err == nil || !strings.Contains(err.Error(), "explicit replace mode") {
		t.Fatalf("replacement error = %v", err)
	}
	if _, err := UpdateManifestLayer(manifest, artifact, strings.Repeat("b", 40), "layer-1.tl", true, true); err != nil {
		t.Fatalf("explicit replacement: %v", err)
	}
}

func TestInspectLayerArtifactRejectsImplicitIDAndMissingLayer(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{"ImplicitID", "---types---\nsample = Sample;\n// LAYER 2\n", "E_EXPLICIT_ID_REQUIRED"},
		{"MissingLayer", "---types---\nsample#10000001 = Sample;\n", "positive // LAYER"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := InspectLayerArtifact([]byte(test.source))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("inspect error = %v", err)
			}
		})
	}
}

func mustMarshalManifest(t *testing.T, manifest Manifest) []byte {
	t.Helper()
	data, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
