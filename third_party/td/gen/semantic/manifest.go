package semantic

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gotd/tl"
)

// Manifest locks all schema inputs and their upstream provenance.
type Manifest struct {
	CanonicalLayer    int                        `json:"canonical_layer"`
	Repository        string                     `json:"repository"`
	SourcePath        string                     `json:"source_path"`
	Overlays          []ManifestOverlay          `json:"overlays"`
	ClientRPCOverlays []ManifestClientRPCOverlay `json:"client_rpc_overlays,omitempty"`
	Layers            []ManifestLayer            `json:"layers"`
}

// ManifestClientRPCOverlay is one explicitly audited client-private method
// schema. Unlike an ordinary overlay, these definitions are not merged into
// any official Layer profile. gotdgen compiles them into a separate static
// unknown-method adapter which targets the current canonical method by
// semantic name.
type ManifestClientRPCOverlay struct {
	Name       string                             `json:"name"`
	File       string                             `json:"file"`
	SHA256     string                             `json:"sha256"`
	Repository string                             `json:"repository"`
	Commit     string                             `json:"commit"`
	Sources    []ManifestClientRPCOverlaySource   `json:"sources"`
	Methods    map[string]ManifestClientRPCMethod `json:"methods"`
}

// ManifestClientRPCOverlaySource locks one upstream source file from which
// the derived TL declarations were audited.
type ManifestClientRPCOverlaySource struct {
	Path string `json:"path"`
	Blob string `json:"blob"`
}

// ManifestClientRPCMethod declares the non-structural decisions which schema
// comparison cannot infer. Rename keys are canonical target fields and values
// are client-source fields. Converter keys are canonical target fields.
type ManifestClientRPCMethod struct {
	Target     string            `json:"target,omitempty"`
	Renames    map[string]string `json:"renames,omitempty"`
	Converters map[string]string `json:"converters,omitempty"`
	Drops      []string          `json:"drops,omitempty"`
}

// ManifestOverlay is one immutable schema overlay and its exact artifact
// provenance. Git tree provenance is verified by the import/sync workflow;
// offline generation verifies the locked local SHA256 only.
type ManifestOverlay struct {
	File       string `json:"file"`
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Blob       string `json:"blob"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
}

// ManifestLayer is one immutable TDesktop schema source.
type ManifestLayer struct {
	Layer  int    `json:"layer"`
	File   string `json:"file"`
	Commit string `json:"commit"`
	Blob   string `json:"blob"`
	SHA256 string `json:"sha256"`
}

// ReadManifest parses a manifest and rejects unknown or invalid fields.
func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("semantic: read manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("semantic: decode manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Manifest{}, err
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("semantic: decode trailing manifest data: %w", err)
	}
	return fmt.Errorf("semantic: manifest has multiple JSON values")
}

func validateManifest(manifest Manifest) error {
	if manifest.CanonicalLayer <= 0 {
		return fmt.Errorf("semantic: invalid canonical layer %d", manifest.CanonicalLayer)
	}
	if strings.TrimSpace(manifest.Repository) == "" {
		return fmt.Errorf("semantic: manifest repository is empty")
	}
	if strings.TrimSpace(manifest.SourcePath) == "" {
		return fmt.Errorf("semantic: manifest source_path is empty")
	}
	if len(manifest.Layers) == 0 {
		return fmt.Errorf("semantic: manifest has no layers")
	}
	seen := make(map[int]struct{}, len(manifest.Layers))
	canonicalFound := false
	previous := 0
	for i, layer := range manifest.Layers {
		if layer.Layer <= 0 {
			return fmt.Errorf("semantic: manifest layer[%d] has invalid layer %d", i, layer.Layer)
		}
		if i > 0 && layer.Layer <= previous {
			return fmt.Errorf("semantic: manifest layers must be strictly increasing")
		}
		previous = layer.Layer
		if _, duplicate := seen[layer.Layer]; duplicate {
			return fmt.Errorf("semantic: manifest repeats layer %d", layer.Layer)
		}
		seen[layer.Layer] = struct{}{}
		canonicalFound = canonicalFound || layer.Layer == manifest.CanonicalLayer
		if strings.TrimSpace(layer.File) == "" {
			return fmt.Errorf("semantic: layer %d file is empty", layer.Layer)
		}
		if err := validateHex("commit", layer.Layer, layer.Commit, 20); err != nil {
			return err
		}
		if err := validateHex("blob", layer.Layer, layer.Blob, 20); err != nil {
			return err
		}
		if err := validateHex("sha256", layer.Layer, layer.SHA256, sha256.Size); err != nil {
			return err
		}
	}
	if !canonicalFound {
		return fmt.Errorf("semantic: canonical layer %d is absent from manifest", manifest.CanonicalLayer)
	}
	seenOverlays := make(map[string]struct{}, len(manifest.Overlays))
	for i, overlay := range manifest.Overlays {
		if strings.TrimSpace(overlay.File) == "" {
			return fmt.Errorf("semantic: overlay[%d] file is empty", i)
		}
		if _, duplicate := seenOverlays[overlay.File]; duplicate {
			return fmt.Errorf("semantic: overlay[%d] repeats file %q", i, overlay.File)
		}
		seenOverlays[overlay.File] = struct{}{}
		if strings.TrimSpace(overlay.Repository) == "" {
			return fmt.Errorf("semantic: overlay[%d] repository is empty", i)
		}
		if strings.TrimSpace(overlay.Path) == "" {
			return fmt.Errorf("semantic: overlay[%d] path is empty", i)
		}
		if err := validateHexValue(fmt.Sprintf("overlay[%d] commit", i), overlay.Commit, 20); err != nil {
			return err
		}
		if err := validateHexValue(fmt.Sprintf("overlay[%d] blob", i), overlay.Blob, 20); err != nil {
			return err
		}
		if err := validateHexValue(fmt.Sprintf("overlay[%d] sha256", i), overlay.SHA256, sha256.Size); err != nil {
			return err
		}
	}
	seenClientOverlays := make(map[string]struct{}, len(manifest.ClientRPCOverlays))
	seenClientFiles := make(map[string]struct{}, len(manifest.ClientRPCOverlays))
	for i, overlay := range manifest.ClientRPCOverlays {
		prefix := fmt.Sprintf("client_rpc_overlays[%d]", i)
		if strings.TrimSpace(overlay.Name) == "" {
			return fmt.Errorf("semantic: %s name is empty", prefix)
		}
		if _, duplicate := seenClientOverlays[overlay.Name]; duplicate {
			return fmt.Errorf("semantic: %s repeats name %q", prefix, overlay.Name)
		}
		seenClientOverlays[overlay.Name] = struct{}{}
		if strings.TrimSpace(overlay.File) == "" {
			return fmt.Errorf("semantic: %s file is empty", prefix)
		}
		if _, duplicate := seenClientFiles[overlay.File]; duplicate {
			return fmt.Errorf("semantic: %s repeats file %q", prefix, overlay.File)
		}
		seenClientFiles[overlay.File] = struct{}{}
		if strings.TrimSpace(overlay.Repository) == "" {
			return fmt.Errorf("semantic: %s repository is empty", prefix)
		}
		if err := validateHexValue(prefix+" commit", overlay.Commit, 20); err != nil {
			return err
		}
		if err := validateHexValue(prefix+" sha256", overlay.SHA256, sha256.Size); err != nil {
			return err
		}
		if len(overlay.Sources) == 0 {
			return fmt.Errorf("semantic: %s has no upstream sources", prefix)
		}
		seenSources := make(map[string]struct{}, len(overlay.Sources))
		for sourceIndex, source := range overlay.Sources {
			sourcePrefix := fmt.Sprintf("%s sources[%d]", prefix, sourceIndex)
			if strings.TrimSpace(source.Path) == "" {
				return fmt.Errorf("semantic: %s path is empty", sourcePrefix)
			}
			if _, duplicate := seenSources[source.Path]; duplicate {
				return fmt.Errorf("semantic: %s repeats path %q", sourcePrefix, source.Path)
			}
			seenSources[source.Path] = struct{}{}
			if err := validateHexValue(sourcePrefix+" blob", source.Blob, 20); err != nil {
				return err
			}
		}
		if len(overlay.Methods) == 0 {
			return fmt.Errorf("semantic: %s has no audited methods", prefix)
		}
		for method, rule := range overlay.Methods {
			if strings.TrimSpace(method) == "" {
				return fmt.Errorf("semantic: %s contains an empty method name", prefix)
			}
			for target, source := range rule.Renames {
				if strings.TrimSpace(target) == "" || strings.TrimSpace(source) == "" {
					return fmt.Errorf("semantic: %s method %q contains an empty field rename", prefix, method)
				}
			}
			for target, converter := range rule.Converters {
				if strings.TrimSpace(target) == "" || strings.TrimSpace(converter) == "" {
					return fmt.Errorf("semantic: %s method %q contains an empty converter", prefix, method)
				}
			}
			seenDrops := make(map[string]struct{}, len(rule.Drops))
			for _, source := range rule.Drops {
				if strings.TrimSpace(source) == "" {
					return fmt.Errorf("semantic: %s method %q contains an empty dropped field", prefix, method)
				}
				if _, duplicate := seenDrops[source]; duplicate {
					return fmt.Errorf("semantic: %s method %q repeats dropped field %q", prefix, method, source)
				}
				seenDrops[source] = struct{}{}
			}
		}
	}
	return nil
}

func validateHex(field string, layer int, value string, size int) error {
	return validateHexValue(fmt.Sprintf("layer %d %s", layer, field), value, size)
}

func validateHexValue(field, value string, size int) error {
	if value != strings.ToLower(value) {
		return fmt.Errorf("semantic: %s must be lowercase hex", field)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size {
		return fmt.Errorf("semantic: %s must be %d-byte hex", field, size)
	}
	return nil
}

// LoadUniverse reads, hashes, parses, overlays, and validates every schema in
// a manifest. Schema files are resolved relative to the manifest directory.
func LoadUniverse(manifestPath string) (*Universe, error) {
	manifest, err := ReadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	return LoadManifestUniverse(manifest, filepath.Dir(manifestPath), nil)
}

// LoadManifestUniverse validates and loads an in-memory manifest using schema
// files rooted at root. overrides, keyed by manifest File, replace selected
// filesystem inputs. The schema import workflow uses this to prove the complete
// future universe before changing manifest.json or any layer file.
func LoadManifestUniverse(manifest Manifest, root string, overrides map[string][]byte) (*Universe, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	overlays := make([]*tl.Schema, 0, len(manifest.Overlays))
	for _, entry := range manifest.Overlays {
		data, err := readManifestSchemaInput(root, entry.File, overrides)
		if err != nil {
			return nil, fmt.Errorf("semantic: overlay %q read schema: %w", entry.File, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != entry.SHA256 {
			return nil, fmt.Errorf("semantic: overlay %q SHA256 mismatch: got %s, want %s", entry.File, got, entry.SHA256)
		}
		if err := validateExplicitIDs(data, fmt.Sprintf("overlay %q", entry.File)); err != nil {
			return nil, err
		}
		overlay, err := tl.Parse(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("semantic: overlay %q parse schema: %w", entry.File, err)
		}
		overlays = append(overlays, overlay)
	}

	schemas := make([]*SchemaModel, 0, len(manifest.Layers))
	for _, entry := range manifest.Layers {
		data, err := readManifestSchemaInput(root, entry.File, overrides)
		if err != nil {
			return nil, fmt.Errorf("semantic: layer %d read schema: %w", entry.Layer, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != entry.SHA256 {
			return nil, fmt.Errorf("semantic: layer %d SHA256 mismatch: got %s, want %s", entry.Layer, got, entry.SHA256)
		}
		if err := validateExplicitIDs(data, fmt.Sprintf("layer %d file %q", entry.Layer, entry.File)); err != nil {
			return nil, err
		}
		parsed, err := tl.Parse(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("semantic: layer %d parse schema: %w", entry.Layer, err)
		}
		if parsed.Layer != entry.Layer {
			return nil, fmt.Errorf("semantic: layer %d file declares layer %d", entry.Layer, parsed.Layer)
		}
		for i, overlay := range overlays {
			if err := mergeSchema(parsed, overlay); err != nil {
				return nil, fmt.Errorf("semantic: layer %d apply overlay %q: %w", entry.Layer, manifest.Overlays[i].File, err)
			}
		}
		model, err := BuildSchema(parsed, SourceRef{
			Layer:      entry.Layer,
			Repository: manifest.Repository,
			Commit:     entry.Commit,
			Blob:       entry.Blob,
			Path:       manifest.SourcePath,
			File:       entry.File,
			SHA256:     ShapeDigest(digest),
		})
		if err != nil {
			return nil, err
		}
		schemas = append(schemas, model)
	}
	universe, err := NewUniverse(manifest.CanonicalLayer, schemas...)
	if err != nil {
		return nil, err
	}
	universe.ClientRPCOverlays, err = loadClientRPCOverlays(manifest, root, overrides, universe)
	if err != nil {
		return nil, err
	}
	return universe, nil
}

func readManifestSchemaInput(root, file string, overrides map[string][]byte) ([]byte, error) {
	if data, ok := overrides[file]; ok {
		return append([]byte(nil), data...), nil
	}
	return os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
}

// validateExplicitIDs prevents official inputs from silently changing a wire
// ID when a declaration changes. github.com/gotd/tl intentionally exposes
// only the resulting uint32 ID, so this source property must be checked before
// parsing.
func validateExplicitIDs(data []byte, source string) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	line := 0
	for scanner.Scan() {
		line++
		declaration := strings.TrimSpace(scanner.Text())
		if declaration == "" || strings.HasPrefix(declaration, "//") || strings.HasPrefix(declaration, "---") {
			continue
		}
		equals := strings.IndexByte(declaration, '=')
		if equals < 0 {
			// Leave malformed non-declaration input to the TL parser, which can
			// produce the more specific grammar error.
			continue
		}
		left := strings.TrimSpace(declaration[:equals])
		parts := strings.Fields(left)
		if len(parts) == 0 {
			continue
		}
		name, id, explicit := strings.Cut(parts[0], "#")
		if !explicit || name == "" || id == "" {
			return fmt.Errorf("semantic: E_EXPLICIT_ID_REQUIRED: %s line %d definition %q has no explicit wire ID", source, line, name)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("semantic: scan %s for explicit wire IDs: %w", source, err)
	}
	return nil
}

// SortedKeys returns family keys in deterministic category/name order.
func (u *Universe) SortedKeys() []DefinitionKey {
	keys := make([]DefinitionKey, 0, len(u.Families))
	for key := range u.Families {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Category != keys[j].Category {
			return keys[i].Category < keys[j].Category
		}
		return keys[i].QName < keys[j].QName
	})
	return keys
}
