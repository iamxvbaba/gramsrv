package gen

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/gen/semantic"
)

type layerMetadata struct {
	CanonicalLayer int
	Profiles       []int
	Families       []layerMetadataFamily
	Wires          []layerMetadataWire
	Overrides      []layerMetadataOverrides
}

type layerMetadataFamily struct {
	ID              uint64
	Constant        string
	Category        string
	QName           string
	CanonicalWireID uint32
	Canonical       bool
}

type layerMetadataWire struct {
	WireID   uint32
	Constant string
}

type layerMetadataOverrides struct {
	Layer   int
	Entries []layerMetadataOverride
}

type layerMetadataOverride struct {
	Constant string
	WireID   uint32
	Present  bool
}

func (g *Generator) buildLayerMetadata() (*layerMetadata, error) {
	if g.schemaSet == nil {
		return nil, fmt.Errorf("build layer metadata: nil schema set")
	}
	wires, err := g.buildLayerWireModel()
	if err != nil {
		return nil, fmt.Errorf("build layer metadata: shared wire model: %w", err)
	}
	set := g.schemaSet
	canonical := set.Schemas[set.CanonicalLayer]
	if canonical == nil {
		return nil, fmt.Errorf("build layer metadata: canonical layer %d is absent", set.CanonicalLayer)
	}

	keys := set.SortedKeys()
	result := &layerMetadata{
		CanonicalLayer: set.CanonicalLayer,
		Profiles:       set.Layers(),
		Families:       make([]layerMetadataFamily, 0, len(keys)),
		Wires:          make([]layerMetadataWire, 0, len(set.WireCodecs)),
		Overrides:      make([]layerMetadataOverrides, 0, len(set.Schemas)),
	}
	semanticID := make(map[semantic.SemanticKey]uint64, len(keys))
	seenSemanticID := make(map[uint64]semantic.SemanticKey, len(keys))
	for _, key := range keys {
		id := layerSemanticStableID(key)
		if id == 0 {
			return nil, fmt.Errorf("build layer metadata: semantic key %s hashes to reserved zero", key)
		}
		if previous, collision := seenSemanticID[id]; collision {
			return nil, fmt.Errorf("build layer metadata: semantic id %#016x collides for %s and %s", id, previous, key)
		}
		seenSemanticID[id] = key
		semanticID[key] = id
		family := set.Families[key]
		canonicalDefinition := family.ByLayer[set.CanonicalLayer]
		entry := layerMetadataFamily{
			ID:       id,
			Constant: layerSemanticConstant(key),
			Category: key.Category.String(),
			QName:    key.QName,
		}
		if canonicalDefinition != nil {
			entry.Canonical = true
			entry.CanonicalWireID = canonicalDefinition.WireID
		}
		result.Families = append(result.Families, entry)
	}

	for wireID, codec := range set.WireCodecs {
		_, ok := semanticID[codec.Key]
		if !ok {
			return nil, fmt.Errorf("build layer metadata: wire id %#08x has unknown semantic key %s", wireID, codec.Key)
		}
		result.Wires = append(result.Wires, layerMetadataWire{WireID: wireID, Constant: layerSemanticConstant(codec.Key)})
	}
	sort.Slice(result.Wires, func(i, j int) bool { return result.Wires[i].WireID < result.Wires[j].WireID })

	for _, layer := range result.Profiles {
		if layer == set.CanonicalLayer {
			continue
		}
		profile := layerMetadataOverrides{Layer: layer}
		for _, key := range keys {
			family := set.Families[key]
			canonicalDefinition := family.ByLayer[set.CanonicalLayer]
			targetDefinition := family.ByLayer[layer]
			historical := wires.historicalTarget(layer, key)
			if historical == nil && canonicalDefinition != nil && targetDefinition != nil && canonicalDefinition.WireID == targetDefinition.WireID {
				continue
			}
			entry := layerMetadataOverride{Constant: layerSemanticConstant(key)}
			if historical != nil {
				entry.Present = true
				entry.WireID = historical.WireID
			} else if targetDefinition != nil {
				entry.Present = true
				entry.WireID = targetDefinition.WireID
			}
			profile.Entries = append(profile.Entries, entry)
		}
		result.Overrides = append(result.Overrides, profile)
	}
	return result, nil
}

func layerSemanticStableID(key semantic.SemanticKey) uint64 {
	digest := sha256.Sum256([]byte("gotd.tl.layer-semantic.v1\x00" + key.String()))
	return binary.LittleEndian.Uint64(digest[:8])
}

func layerSemanticConstant(key semantic.SemanticKey) string {
	parts := strings.Split(key.QName, ".")
	name := namespacedName(parts[len(parts)-1], parts[:len(parts)-1])
	prefix := "Type"
	if key.Category == semantic.CategoryFunction {
		prefix = "Method"
	}
	return "LayerSemantic" + prefix + name
}
