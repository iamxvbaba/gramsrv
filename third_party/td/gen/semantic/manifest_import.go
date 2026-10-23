package semantic

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gotd/tl"
)

// LayerArtifact is one locally inspected, immutable TL schema input. Layer is
// read from the TL source; the two digests are computed from Source rather than
// accepted as caller-provided metadata.
type LayerArtifact struct {
	Layer   int
	Source  []byte
	SHA256  string
	GitBlob string
}

// InspectLayerArtifact parses a local TL source and derives every piece of
// content-addressed metadata needed by manifest.json. GitBlob is the SHA-1
// object ID used by the upstream GitHub repository ("blob <size>\x00<body>"); it
// is provenance metadata, not a security digest. SHA256 remains the offline
// integrity lock.
func InspectLayerArtifact(source []byte) (LayerArtifact, error) {
	if len(source) == 0 {
		return LayerArtifact{}, fmt.Errorf("semantic: imported layer schema is empty")
	}
	if err := validateExplicitIDs(source, "imported layer schema"); err != nil {
		return LayerArtifact{}, err
	}
	parsed, err := tl.Parse(bytes.NewReader(source))
	if err != nil {
		return LayerArtifact{}, fmt.Errorf("semantic: parse imported layer schema: %w", err)
	}
	if parsed.Layer <= 0 {
		return LayerArtifact{}, fmt.Errorf("semantic: imported layer schema does not declare a positive // LAYER")
	}

	sha := sha256.Sum256(source)
	// GitHub's repository is SHA-1 based. This exactly reproduces `git
	// hash-object` locally and does not use SHA-1 as an integrity primitive.
	git := sha1.New() // #nosec G505 -- required Git object identity algorithm.
	_, _ = fmt.Fprintf(git, "blob %d%c", len(source), byte(0))
	_, _ = git.Write(source)
	return LayerArtifact{
		Layer:   parsed.Layer,
		Source:  append([]byte(nil), source...),
		SHA256:  hex.EncodeToString(sha[:]),
		GitBlob: hex.EncodeToString(git.Sum(nil)),
	}, nil
}

// UpdateManifestLayer returns a detached manifest containing artifact. An
// identical import is idempotent. Replacing different bytes or provenance for
// an existing layer requires replace=true so a layer cannot drift silently.
func UpdateManifestLayer(manifest Manifest, artifact LayerArtifact, commit, file string, canonical, replace bool) (Manifest, error) {
	inspected, err := InspectLayerArtifact(artifact.Source)
	if err != nil {
		return Manifest{}, err
	}
	if artifact.Layer != inspected.Layer || artifact.SHA256 != inspected.SHA256 || artifact.GitBlob != inspected.GitBlob {
		return Manifest{}, fmt.Errorf("semantic: imported layer artifact metadata is stale")
	}
	if err := validateHexValue("import commit", commit, 20); err != nil {
		return Manifest{}, err
	}
	file = filepath.ToSlash(strings.TrimSpace(file))
	if file == "" || path.Clean(file) != file || path.Base(file) != file || strings.Contains(file, ":") {
		return Manifest{}, fmt.Errorf("semantic: imported layer file %q must be one relative base name", file)
	}

	result := manifest
	result.Overlays = append([]ManifestOverlay(nil), manifest.Overlays...)
	result.Layers = append([]ManifestLayer(nil), manifest.Layers...)
	entry := ManifestLayer{
		Layer:  inspected.Layer,
		File:   file,
		Commit: commit,
		Blob:   inspected.GitBlob,
		SHA256: inspected.SHA256,
	}
	if canonical && entry.Layer < manifest.CanonicalLayer {
		return Manifest{}, fmt.Errorf("semantic: refusing to move canonical layer backwards from %d to %d", manifest.CanonicalLayer, entry.Layer)
	}

	found := -1
	for i, existing := range result.Layers {
		if existing.Layer == entry.Layer {
			found = i
			continue
		}
		if existing.File == entry.File {
			return Manifest{}, fmt.Errorf("semantic: imported file %q is already assigned to layer %d", entry.File, existing.Layer)
		}
	}
	if found >= 0 {
		if result.Layers[found] != entry && !replace {
			return Manifest{}, fmt.Errorf("semantic: layer %d already exists with different provenance; use explicit replace mode", entry.Layer)
		}
		result.Layers[found] = entry
	} else {
		result.Layers = append(result.Layers, entry)
	}
	sort.Slice(result.Layers, func(i, j int) bool { return result.Layers[i].Layer < result.Layers[j].Layer })
	if canonical {
		result.CanonicalLayer = entry.Layer
	}
	if err := validateManifest(result); err != nil {
		return Manifest{}, err
	}
	return result, nil
}

// MarshalManifest renders the stable checked-in representation used by the
// import workflow.
func MarshalManifest(manifest Manifest) ([]byte, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("semantic: marshal manifest: %w", err)
	}
	return append(data, '\n'), nil
}

// RenderCanonicalSchema renders _schema/telegram.tl from the exact canonical
// manifest input plus every locked overlay. overlaySources is keyed by the
// overlay File value from the manifest. No network input participates.
func RenderCanonicalSchema(manifest Manifest, canonicalSource []byte, overlaySources map[string][]byte) ([]byte, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	var canonical ManifestLayer
	found := false
	for _, layer := range manifest.Layers {
		if layer.Layer == manifest.CanonicalLayer {
			canonical, found = layer, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("semantic: canonical layer %d is absent", manifest.CanonicalLayer)
	}
	artifact, err := InspectLayerArtifact(canonicalSource)
	if err != nil {
		return nil, err
	}
	if artifact.Layer != canonical.Layer || artifact.SHA256 != canonical.SHA256 || artifact.GitBlob != canonical.Blob {
		return nil, fmt.Errorf("semantic: canonical layer %d source disagrees with locked manifest provenance", canonical.Layer)
	}
	schema, err := tl.Parse(bytes.NewReader(canonicalSource))
	if err != nil {
		return nil, fmt.Errorf("semantic: parse canonical schema: %w", err)
	}
	for _, overlay := range manifest.Overlays {
		data, ok := overlaySources[overlay.File]
		if !ok {
			return nil, fmt.Errorf("semantic: canonical render is missing overlay %q", overlay.File)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != overlay.SHA256 {
			return nil, fmt.Errorf("semantic: overlay %q SHA256 mismatch: got %s, want %s", overlay.File, got, overlay.SHA256)
		}
		if err := validateExplicitIDs(data, fmt.Sprintf("overlay %q", overlay.File)); err != nil {
			return nil, err
		}
		parsed, err := tl.Parse(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("semantic: parse overlay %q: %w", overlay.File, err)
		}
		if err := mergeSchema(schema, parsed); err != nil {
			return nil, fmt.Errorf("semantic: apply overlay %q: %w", overlay.File, err)
		}
	}

	var out bytes.Buffer
	fmt.Fprintln(&out, "// Code generated by ./cmd/gotdgen --schema-import, DO NOT EDIT.")
	fmt.Fprintln(&out, "//")
	fmt.Fprintf(&out, "// Source: %s\n", manifestLayerSource(manifest, canonical))
	if len(manifest.Overlays) != 0 {
		paths := make([]string, 0, len(manifest.Overlays))
		for _, overlay := range manifest.Overlays {
			paths = append(paths, overlay.Path)
		}
		fmt.Fprintf(&out, "// Merge:  %s\n", strings.Join(paths, ","))
	}
	fmt.Fprintf(&out, "// Layer:  %d\n", canonical.Layer)
	fmt.Fprintf(&out, "// SHA256: %s\n\n", canonical.SHA256)
	if _, err := schema.WriteTo(&out); err != nil {
		return nil, fmt.Errorf("semantic: render canonical schema: %w", err)
	}
	return out.Bytes(), nil
}

func manifestLayerSource(manifest Manifest, layer ManifestLayer) string {
	repository := strings.TrimSuffix(manifest.Repository, ".git")
	if strings.HasPrefix(repository, "https://github.com/") {
		return repository + "/blob/" + layer.Commit + "/" + strings.TrimPrefix(manifest.SourcePath, "/")
	}
	return fmt.Sprintf("%s@%s:%s", manifest.Repository, layer.Commit, manifest.SourcePath)
}
