package gen

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/go-faster/errors"

	"github.com/gotd/getdoc"
	"github.com/gotd/tl"
)

func definitionType(d tl.Definition) string {
	if len(d.Namespace) == 0 {
		return d.Name
	}
	return fmt.Sprintf("%s.%s", strings.Join(d.Namespace, "."), d.Name)
}

// Generator generates go types from tl.Schema.
type Generator struct {
	schema *tl.Schema
	// schemaSet is the single normalized multi-schema input shared by the
	// canonical Go backend and the layer-aware wire backend. It is nil for the
	// legacy single-schema entry point.
	schemaSet *SchemaSet
	// layerPlan is the single policy-applied semantic conversion plan consumed
	// by every multi-layer backend projection.
	layerPlan *LayerConversionPlan

	// classes type bindings, key is TL type.
	classes map[string]classBinding
	// types bindings, key is TL type.
	types map[string]typeBinding

	// structs definitions.
	structs []structDef
	// interfaces definitions.
	interfaces []interfaceDef
	// errorChecks definitions.
	errorChecks []errCheckDef

	// constructor mappings.
	mappings map[string][]constructorMapping

	// registry of type ids.
	registry []bindingDef

	// docBase is base url for documentation.
	docBase      *url.URL
	doc          *getdoc.Doc
	docLineLimit int

	generateFlags GenerateFlags
}

// NewGenerator initializes and returns new Generator from tl.Schema.
func NewGenerator(s *tl.Schema, genOpt GeneratorOptions) (*Generator, error) {
	return newGenerator(s, nil, genOpt)
}

// NewSchemaSetGenerator initializes a Generator from a normalized collection
// of layer schemas. The canonical profile drives the existing public Go
// bindings; all profiles remain attached to the Generator for layer-aware
// backends. This keeps parsing, semantic identity and wire variants in one IR
// instead of rebuilding them in a companion generator.
func NewSchemaSetGenerator(s *SchemaSet, genOpt GeneratorOptions) (*Generator, error) {
	if s == nil {
		return nil, errors.New("nil schema set")
	}
	canonical := s.CanonicalSchema()
	if canonical == nil {
		return nil, errors.Errorf("canonical layer %d is absent", s.CanonicalLayer)
	}
	return newGenerator(canonical, s, genOpt)
}

func newGenerator(s *tl.Schema, schemaSet *SchemaSet, genOpt GeneratorOptions) (*Generator, error) {
	if s == nil {
		return nil, errors.New("nil schema")
	}
	genOpt.setDefaults()
	g := &Generator{
		schema:        s,
		schemaSet:     schemaSet,
		classes:       map[string]classBinding{},
		types:         map[string]typeBinding{},
		mappings:      map[string][]constructorMapping{},
		docLineLimit:  genOpt.DocLineLimit,
		generateFlags: genOpt.GenerateFlags,
	}
	if schemaSet != nil {
		plan, err := AnalyzeLayerConversions(schemaSet, genOpt.LayerPolicy)
		if err != nil {
			return nil, errors.Wrap(err, "analyze layer conversions")
		}
		g.layerPlan = plan
	}
	if genOpt.DocBaseURL != "" {
		u, err := url.Parse(genOpt.DocBaseURL)
		if err != nil {
			return nil, errors.Wrap(err, "parse docBase")
		}
		g.docBase = u

		if u.Host == "core.telegram.org" {
			// Using embedded documentation.
			// TODO(ernado): Get actual layer
			doc, err := getdoc.Load(getdoc.LayerLatest)
			if err != nil {
				return nil, errors.Wrap(err, "get documentation")
			}
			g.doc = doc
		}
	}
	if err := g.makeBindings(); err != nil {
		return nil, errors.Wrap(err, "make type bindings")
	}
	if err := g.makeStructures(); err != nil {
		return nil, errors.Wrap(err, "generate go structures")
	}
	g.makeInterfaces()
	g.makeErrors()

	return g, nil
}

// SchemaSet returns the normalized multi-schema input, or nil when Generator
// was created through the legacy single-schema entry point.
func (g *Generator) SchemaSet() *SchemaSet {
	return g.schemaSet
}

// LayerConversionPlan returns the single policy-applied conversion plan, or
// nil for a legacy single-schema Generator. Callers must treat it as immutable.
func (g *Generator) LayerConversionPlan() *LayerConversionPlan {
	return g.layerPlan
}
