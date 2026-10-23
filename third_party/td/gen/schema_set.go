package gen

import "github.com/iamxvbaba/td/gen/semantic"

// SchemaSet is the normalized collection of all supported TL schema profiles.
// It is the single cross-backend IR consumed by both the canonical Go binding
// backend and the layer-aware static wire backend.
type SchemaSet = semantic.Universe

// SchemaProfile is one parsed and validated TL schema at an exact API layer.
type SchemaProfile = semantic.SchemaModel

// SchemaSource identifies the immutable input used to build a profile.
type SchemaSource = semantic.SourceRef

// NewSchemaSet validates and joins profiles around one canonical layer.
func NewSchemaSet(canonicalLayer int, profiles ...*SchemaProfile) (*SchemaSet, error) {
	return semantic.NewUniverse(canonicalLayer, profiles...)
}
