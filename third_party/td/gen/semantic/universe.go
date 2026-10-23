package semantic

import (
	"fmt"
	"sort"

	"github.com/gotd/tl"
)

// WireCodec is one reusable payload codec. It contains no profile-specific
// field semantics, so flags.N?true changes do not duplicate codecs.
type WireCodec struct {
	WireID          uint32
	Key             SemanticKey
	Shape           WireShape
	ProfileVariants []*ProfileVariant
}

// ProfileVariant binds one layer's semantic definition to its shared payload
// codec. Result types and presence-only flags remain profile semantics.
type ProfileVariant struct {
	Layer         int
	Definition    *Definition
	WireCodec     *WireCodec
	SemanticShape ShapeDigest
}

// SignatureVariant is the compatibility view that groups identical semantic
// signatures across profiles.
// Layers can be non-contiguous; callers must not infer ranges from endpoints.
type SignatureVariant struct {
	WireID         uint32
	BodyShape      ShapeDigest
	SignatureShape ShapeDigest
	Definition     *Definition
	WireCodec      *WireCodec
	Layers         []int
}

// Variant is kept as a source-compatible name for SignatureVariant.
type Variant = SignatureVariant

// Family groups the same category-qualified definition across layers.
type Family struct {
	Key             SemanticKey
	ByLayer         map[int]*Definition
	ProfilesByLayer map[int]*ProfileVariant
	ProfileVariants []*ProfileVariant
	Variants        []*SignatureVariant
}

// Universe is the validated collection of all supported layer schemas.
type Universe struct {
	CanonicalLayer    int
	Schemas           map[int]*SchemaModel
	Families          map[SemanticKey]*Family
	ByWire            map[int]map[uint32]*Definition
	WireCodecs        map[uint32]*WireCodec
	ClientRPCOverlays []*ClientRPCOverlay
	SemanticDigest    ShapeDigest
}

// NewUniverse validates layer identity and groups definitions by semantic key.
func NewUniverse(canonicalLayer int, schemas ...*SchemaModel) (*Universe, error) {
	if canonicalLayer <= 0 {
		return nil, fmt.Errorf("semantic: invalid canonical layer %d", canonicalLayer)
	}
	u := &Universe{
		CanonicalLayer: canonicalLayer,
		Schemas:        make(map[int]*SchemaModel, len(schemas)),
		Families:       make(map[SemanticKey]*Family),
		ByWire:         make(map[int]map[uint32]*Definition, len(schemas)),
		WireCodecs:     make(map[uint32]*WireCodec),
	}
	for _, schema := range schemas {
		if schema == nil {
			return nil, fmt.Errorf("semantic: nil schema in universe")
		}
		if schema.Layer <= 0 {
			return nil, fmt.Errorf("semantic: invalid schema layer %d", schema.Layer)
		}
		if _, duplicate := u.Schemas[schema.Layer]; duplicate {
			return nil, fmt.Errorf("semantic: duplicate schema layer %d", schema.Layer)
		}
		u.Schemas[schema.Layer] = schema
		u.ByWire[schema.Layer] = schema.ByWire
	}
	if _, ok := u.Schemas[canonicalLayer]; !ok {
		return nil, fmt.Errorf("semantic: canonical layer %d is not loaded", canonicalLayer)
	}

	layers := u.Layers()
	for _, layer := range layers {
		for _, definition := range u.Schemas[layer].Definitions {
			codec := u.WireCodecs[definition.WireID]
			if codec == nil {
				codec = &WireCodec{
					WireID: definition.WireID,
					Key:    definition.Key,
					Shape:  definition.WireShape,
				}
				u.WireCodecs[definition.WireID] = codec
			} else {
				first := codec.ProfileVariants[0]
				if codec.Key != definition.Key {
					return nil, fmt.Errorf(
						"semantic: wire id %#08x maps to %s in layer %d and %s in layer %d",
						definition.WireID, codec.Key, first.Layer, definition.Key, layer,
					)
				}
				if codec.Shape != definition.WireShape {
					return nil, fmt.Errorf(
						"semantic: wire id %#08x for %s has conflicting payload shapes in layers %d (%s) and %d (%s)",
						definition.WireID, definition.Key, first.Layer, codec.Shape, layer, definition.WireShape,
					)
				}
			}

			profile := &ProfileVariant{
				Layer:         layer,
				Definition:    definition,
				WireCodec:     codec,
				SemanticShape: definition.SemanticShape,
			}
			codec.ProfileVariants = append(codec.ProfileVariants, profile)

			family := u.Families[definition.Key]
			if family == nil {
				family = &Family{
					Key:             definition.Key,
					ByLayer:         make(map[int]*Definition),
					ProfilesByLayer: make(map[int]*ProfileVariant),
				}
				u.Families[definition.Key] = family
			}
			family.ByLayer[layer] = definition
			family.ProfilesByLayer[layer] = profile
			family.ProfileVariants = append(family.ProfileVariants, profile)

			var variant *SignatureVariant
			for _, candidate := range family.Variants {
				if candidate.WireID == definition.WireID && candidate.SignatureShape == definition.SignatureShape {
					variant = candidate
					break
				}
			}
			if variant == nil {
				variant = &SignatureVariant{
					WireID:         definition.WireID,
					BodyShape:      definition.BodyShape,
					SignatureShape: definition.SignatureShape,
					Definition:     definition,
					WireCodec:      codec,
				}
				family.Variants = append(family.Variants, variant)
			}
			variant.Layers = append(variant.Layers, layer)
		}
	}
	u.SemanticDigest = universeDigest(u, layers)
	return u, nil
}

// Layers returns supported layers in ascending order.
func (u *Universe) Layers() []int {
	layers := make([]int, 0, len(u.Schemas))
	for layer := range u.Schemas {
		layers = append(layers, layer)
	}
	sort.Ints(layers)
	return layers
}

// CanonicalSchema returns a deep copy suitable for the existing Go backend.
// Mutating the returned schema cannot alter the Universe.
func (u *Universe) CanonicalSchema() *tl.Schema {
	return cloneSchema(u.Schemas[u.CanonicalLayer].raw)
}

func universeDigest(u *Universe, layers []int) ShapeDigest {
	w := newShapeWriter("gotd.tl.semantic.universe.v1")
	w.uint64(uint64(u.CanonicalLayer))
	w.uint64(uint64(len(layers)))
	for _, layer := range layers {
		schema := u.Schemas[layer]
		w.uint64(uint64(layer))
		w.digest(schema.Source.SHA256)
		w.uint64(uint64(len(schema.Definitions)))
		for _, definition := range schema.Definitions {
			w.byte(byte(definition.Key.Category))
			w.string(definition.Key.QName)
			w.uint64(uint64(definition.WireID))
			w.digest(definition.SignatureShape)
		}
	}
	return w.sum()
}

// DefinitionChange compares one shared semantic definition with canonical.
type DefinitionChange struct {
	Key              SemanticKey
	Target           *Definition
	Canonical        *Definition
	WireIDChanged    bool
	WireShapeChanged bool
	BodyChanged      bool
	ResultChanged    bool
}

// SignatureChanged reports whether fields or result TypeRef changed. Wire ID
// is intentionally independent: equal IDs do not imply equal signatures.
func (c DefinitionChange) SignatureChanged() bool {
	return c.BodyChanged || c.ResultChanged
}

// ResultOnly reports a method/type whose body is stable but result TypeRef is
// different. Such RPC methods require method-aware result encoding.
func (c DefinitionChange) ResultOnly() bool {
	return !c.BodyChanged && c.ResultChanged
}

// SameIDSemanticChanged reports profile semantics that changed while the
// constructor/method ID remained stable. This is not a payload conflict.
func (c DefinitionChange) SameIDSemanticChanged() bool {
	return !c.WireIDChanged && c.SignatureChanged()
}

// SameWireSignatureChanged is the compatibility name for
// SameIDSemanticChanged.
func (c DefinitionChange) SameWireSignatureChanged() bool {
	return c.SameIDSemanticChanged()
}

// Difference is a target-layer comparison against canonical.
type Difference struct {
	Layer         int
	Changes       []DefinitionChange
	CanonicalOnly []*Definition
	OldOnly       []*Definition
}

// Diff compares a supported target layer against canonical by semantic key.
func (u *Universe) Diff(layer int) (Difference, error) {
	target, ok := u.Schemas[layer]
	if !ok {
		return Difference{}, fmt.Errorf("semantic: layer %d is not loaded", layer)
	}
	canonical := u.Schemas[u.CanonicalLayer]
	result := Difference{Layer: layer}
	for _, current := range canonical.Definitions {
		old, exists := target.ByKey[current.Key]
		if !exists {
			result.CanonicalOnly = append(result.CanonicalOnly, current)
			continue
		}
		change := DefinitionChange{
			Key:              current.Key,
			Target:           old,
			Canonical:        current,
			WireIDChanged:    old.WireID != current.WireID,
			WireShapeChanged: old.WireShape != current.WireShape,
			BodyChanged:      old.BodyShape != current.BodyShape,
			ResultChanged:    !old.Result.Equal(current.Result),
		}
		if change.WireIDChanged || change.SignatureChanged() {
			result.Changes = append(result.Changes, change)
		}
	}
	for _, old := range target.Definitions {
		if _, exists := canonical.ByKey[old.Key]; !exists {
			result.OldOnly = append(result.OldOnly, old)
		}
	}
	return result, nil
}

// SignatureChanges returns changes whose fields and/or result differ.
func (d Difference) SignatureChanges() []DefinitionChange {
	result := make([]DefinitionChange, 0, len(d.Changes))
	for _, change := range d.Changes {
		if change.SignatureChanged() {
			result = append(result, change)
		}
	}
	return result
}

// SameWireSignatureChanges returns all same-ID/different-shape changes.
func (d Difference) SameWireSignatureChanges() []DefinitionChange {
	return d.SameIDSemanticChanges()
}

// SameIDSemanticChanges returns all same-ID semantic changes.
func (d Difference) SameIDSemanticChanges() []DefinitionChange {
	result := make([]DefinitionChange, 0, len(d.Changes))
	for _, change := range d.Changes {
		if change.SameIDSemanticChanged() {
			result = append(result, change)
		}
	}
	return result
}

// ResultOnlyChanges returns all body-stable/result-changing definitions.
func (d Difference) ResultOnlyChanges() []DefinitionChange {
	result := make([]DefinitionChange, 0, len(d.Changes))
	for _, change := range d.Changes {
		if change.ResultOnly() {
			result = append(result, change)
		}
	}
	return result
}
