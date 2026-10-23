package semantic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/gotd/tl"
)

// ClientRPCOverlay is an audited client-private method schema compiled next
// to, but never merged into, the official Layer universe.
type ClientRPCOverlay struct {
	Name       string
	Repository string
	Commit     string
	File       string
	SHA256     ShapeDigest
	Sources    []ClientRPCOverlaySource
	Methods    []*ClientRPCMethod
}

type ClientRPCOverlaySource struct {
	Path string
	Blob string
}

// ClientRPCMethod joins one private wire definition to its current canonical
// semantic target and the explicit non-structural conversion rules.
type ClientRPCMethod struct {
	Definition *Definition
	Target     SemanticKey
	Renames    map[string]string
	Converters map[string]string
	Drops      []string
}

func loadClientRPCOverlays(manifest Manifest, root string, overrides map[string][]byte, universe *Universe) ([]*ClientRPCOverlay, error) {
	result := make([]*ClientRPCOverlay, 0, len(manifest.ClientRPCOverlays))
	for _, entry := range manifest.ClientRPCOverlays {
		data, err := readManifestSchemaInput(root, entry.File, overrides)
		if err != nil {
			return nil, fmt.Errorf("semantic: client RPC overlay %q read schema: %w", entry.Name, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != entry.SHA256 {
			return nil, fmt.Errorf("semantic: client RPC overlay %q SHA256 mismatch: got %s, want %s", entry.Name, got, entry.SHA256)
		}
		if err := validateExplicitIDs(data, fmt.Sprintf("client RPC overlay %q", entry.Name)); err != nil {
			return nil, err
		}
		parsed, err := tl.Parse(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("semantic: client RPC overlay %q parse schema: %w", entry.Name, err)
		}
		overlay, err := buildClientRPCOverlay(entry, ShapeDigest(digest), parsed, universe)
		if err != nil {
			return nil, fmt.Errorf("semantic: client RPC overlay %q: %w", entry.Name, err)
		}
		result = append(result, overlay)
	}
	return result, nil
}

func buildClientRPCOverlay(entry ManifestClientRPCOverlay, digest ShapeDigest, parsed *tl.Schema, universe *Universe) (*ClientRPCOverlay, error) {
	if universe == nil || universe.Schemas[universe.CanonicalLayer] == nil || parsed == nil {
		return nil, fmt.Errorf("canonical schema or parsed overlay is unavailable")
	}
	canonical := universe.Schemas[universe.CanonicalLayer]
	if len(parsed.Definitions) == 0 {
		return nil, fmt.Errorf("schema has no definitions")
	}

	sourceNames := make(map[string]struct{}, len(parsed.Definitions))
	for _, schemaDef := range parsed.Definitions {
		if schemaDef.Category != tl.CategoryFunction {
			return nil, fmt.Errorf("%s:%s is not an RPC function", schemaDef.Category, qualifyDefinition(schemaDef.Definition))
		}
		name := qualifyDefinition(schemaDef.Definition)
		if _, duplicate := sourceNames[name]; duplicate {
			return nil, fmt.Errorf("schema repeats method %q", name)
		}
		sourceNames[name] = struct{}{}
		if _, ok := entry.Methods[name]; !ok {
			return nil, fmt.Errorf("method %q has no explicit audit rule", name)
		}
	}
	for name := range entry.Methods {
		if _, ok := sourceNames[name]; !ok {
			return nil, fmt.Errorf("audit rule %q has no schema definition", name)
		}
	}

	// BuildSchema supplies the same TypeRef/reference validation used by every
	// official profile. Replace same-name canonical functions only inside this
	// temporary generation-time schema; the official universe remains frozen.
	merged := universe.CanonicalSchema()
	kept := merged.Definitions[:0]
	for _, schemaDef := range merged.Definitions {
		name := qualifyDefinition(schemaDef.Definition)
		if schemaDef.Category == tl.CategoryFunction {
			if _, replaced := sourceNames[name]; replaced {
				continue
			}
		}
		kept = append(kept, schemaDef)
	}
	merged.Definitions = kept
	for _, schemaDef := range parsed.Definitions {
		merged.Definitions = append(merged.Definitions, cloneSchemaDefinition(schemaDef))
	}
	model, err := BuildSchema(merged, SourceRef{
		Layer:      universe.CanonicalLayer,
		Repository: entry.Repository,
		Commit:     entry.Commit,
		File:       entry.File,
		SHA256:     digest,
	})
	if err != nil {
		return nil, err
	}

	overlay := &ClientRPCOverlay{
		Name:       entry.Name,
		Repository: entry.Repository,
		Commit:     entry.Commit,
		File:       entry.File,
		SHA256:     digest,
		Sources:    make([]ClientRPCOverlaySource, len(entry.Sources)),
	}
	for i, source := range entry.Sources {
		overlay.Sources[i] = ClientRPCOverlaySource{Path: source.Path, Blob: source.Blob}
	}
	for _, schemaDef := range parsed.Definitions {
		name := qualifyDefinition(schemaDef.Definition)
		definition := model.ByKey[SemanticKey{Category: CategoryFunction, QName: name}]
		if definition == nil || definition.WireID != schemaDef.Definition.ID {
			return nil, fmt.Errorf("method %q did not bind to its private wire definition", name)
		}
		rule := entry.Methods[name]
		targetName := strings.TrimSpace(rule.Target)
		if targetName == "" {
			targetName = name
		}
		target := SemanticKey{Category: CategoryFunction, QName: targetName}
		canonicalDefinition := canonical.ByKey[target]
		if canonicalDefinition == nil {
			return nil, fmt.Errorf("method %q targets missing canonical function %q", name, targetName)
		}
		if !definition.Result.Equal(canonicalDefinition.Result) {
			return nil, fmt.Errorf("method %q result %s differs from canonical target %s result %s", name, definition.Result.String(), targetName, canonicalDefinition.Result.String())
		}
		overlay.Methods = append(overlay.Methods, &ClientRPCMethod{
			Definition: definition,
			Target:     target,
			Renames:    cloneStringMap(rule.Renames),
			Converters: cloneStringMap(rule.Converters),
			Drops:      append([]string(nil), rule.Drops...),
		})
	}
	sort.Slice(overlay.Methods, func(i, j int) bool {
		return overlay.Methods[i].Definition.WireID < overlay.Methods[j].Definition.WireID
	})
	return overlay, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
