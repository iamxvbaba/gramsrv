package gen

import (
	"fmt"

	"github.com/iamxvbaba/td/gen/semantic"
)

// layerValueKind is the generated-code strategy for one semantic TypeRef.
// These values exist only in the generator emitter AST; none of them are
// emitted as a runtime schema catalog.
type layerValueKind uint8

const (
	layerValuePrimitive layerValueKind = iota
	layerValueExactBare
	layerValueBoxedConcrete
	layerValueBoxedAbstract
	layerValueVector
	layerValueDynamicGeneric
	layerValueDynamicObject
)

func (k layerValueKind) String() string {
	switch k {
	case layerValuePrimitive:
		return "primitive"
	case layerValueExactBare:
		return "exact-bare"
	case layerValueBoxedConcrete:
		return "boxed-concrete"
	case layerValueBoxedAbstract:
		return "boxed-abstract"
	case layerValueVector:
		return "vector"
	case layerValueDynamicGeneric:
		return "dynamic-generic"
	case layerValueDynamicObject:
		return "dynamic-object"
	default:
		return fmt.Sprintf("layerValueKind(%d)", k)
	}
}

// layerValueVectorMode records whether the vector constructor itself is on
// the wire. Element encoding is described recursively by Element.
type layerValueVectorMode uint8

const (
	layerValueVectorBoxed layerValueVectorMode = iota
	layerValueVectorBare
)

func (m layerValueVectorMode) String() string {
	switch m {
	case layerValueVectorBoxed:
		return "boxed"
	case layerValueVectorBare:
		return "bare"
	default:
		return fmt.Sprintf("layerValueVectorMode(%d)", m)
	}
}

// layerValueConstructor is a reference from the emitter AST back to the
// shared conversion plan and canonical Go backend. A profile-only constructor
// deliberately has a nil Canonical binding; its conversion obligation must be
// resolved by the caller before source emission.
type layerValueConstructor struct {
	Conversion *LayerFamilyConversion
	Canonical  *layerDefinitionBinding
}

// layerValuePlan is a recursive, generation-time emitter AST for one exact
// TypeRef in one exact target profile. Ref points into the semantic IR; the
// plan does not copy schema definitions or retain a runtime walker.
type layerValuePlan struct {
	Kind layerValueKind
	Ref  *semantic.TypeRef

	// Primitive is the semantic primitive spelling (for example int or Bool).
	Primitive string
	// GenericParam is the name bound by a surrounding method, for example X.
	GenericParam string

	VectorMode layerValueVectorMode
	Element    *layerValuePlan

	// CanonicalClass is the existing Go class/interface binding for boxed
	// named values. It is nil when the whole target class is profile-only.
	CanonicalClass *layerClassBinding

	// Constructors contains exactly one member for exact-bare and boxed-
	// concrete plans, and the target profile's complete allowed set for an
	// abstract boxed class. Dynamic plans never populate this slice.
	Constructors []layerValueConstructor
}

// layerValueCompiler compiles TypeRefs against one SchemaSet and its already
// analyzed LayerConversionPlan. It is intentionally reusable so canonical Go
// bindings and plan identity are validated only once per generation.
type layerValueCompiler struct {
	schemaSet   *SchemaSet
	conversions *LayerConversionPlan
	bindings    *layerBindingIndex
	wire        *layerWireModel
	profiles    map[int]*LayerProfileConversion
	families    map[int]map[semantic.SemanticKey]*LayerFamilyConversion
}

// newLayerValueCompiler constructs the static value-plan compiler. Conversion
// analysis is consumed from Generator; this compiler never re-compares schemas
// or invents a second compatibility decision model.
func (g *Generator) newLayerValueCompiler() (*layerValueCompiler, error) {
	wire, err := g.buildLayerWireModel()
	if err != nil {
		return nil, err
	}
	return g.newLayerValueCompilerForWire(wire)
}

func (g *Generator) newLayerValueCompilerForWire(wire *layerWireModel) (*layerValueCompiler, error) {
	if g == nil || g.schemaSet == nil {
		return nil, fmt.Errorf("gen: layer value compiler requires a schema-set generator")
	}
	if wire == nil || wire.Bindings == nil {
		return nil, fmt.Errorf("gen: layer value compiler requires the shared wire model")
	}
	conversions := g.LayerConversionPlan()
	if conversions == nil {
		return nil, fmt.Errorf("gen: layer value compiler requires a conversion plan")
	}
	if conversions.CanonicalLayer != g.schemaSet.CanonicalLayer {
		return nil, fmt.Errorf(
			"gen: layer value conversion canonical layer %d does not match schema set %d",
			conversions.CanonicalLayer, g.schemaSet.CanonicalLayer,
		)
	}

	// These maps are generation-time indexes only. Identity validation prevents
	// a conversion plan built from another schema set with the same layer
	// numbers from being reused, while avoiding an O(families^2) emitter pass.
	profiles := make(map[int]*LayerProfileConversion, len(conversions.Profiles))
	families := make(map[int]map[semantic.SemanticKey]*LayerFamilyConversion, len(conversions.Profiles))
	for profileIndex := range conversions.Profiles {
		profile := &conversions.Profiles[profileIndex]
		layer := profile.Layer
		if g.schemaSet.Schemas[layer] == nil {
			return nil, fmt.Errorf("gen: layer value conversion has unexpected profile %d", layer)
		}
		if _, duplicate := profiles[layer]; duplicate {
			return nil, fmt.Errorf("gen: layer value conversion repeats profile %d", layer)
		}
		profiles[layer] = profile
		byKey := make(map[semantic.SemanticKey]*LayerFamilyConversion, len(profile.Families))
		for familyIndex := range profile.Families {
			conversion := &profile.Families[familyIndex]
			key := conversion.Key
			if _, duplicate := byKey[key]; duplicate {
				return nil, fmt.Errorf("gen: layer value conversion profile %d repeats family %s", layer, key)
			}
			family := g.schemaSet.Families[key]
			if family == nil ||
				conversion.Canonical != family.ProfilesByLayer[g.schemaSet.CanonicalLayer] ||
				conversion.Profile != family.ProfilesByLayer[layer] {
				return nil, fmt.Errorf("gen: layer value conversion profile %d family %s belongs to another schema set", layer, key)
			}
			byKey[key] = conversion
		}
		if len(byKey) != len(g.schemaSet.Families) {
			return nil, fmt.Errorf(
				"gen: layer value conversion profile %d has %d families, want %d",
				layer, len(byKey), len(g.schemaSet.Families),
			)
		}
		families[layer] = byKey
	}
	for _, layer := range g.schemaSet.Layers() {
		if profiles[layer] == nil {
			return nil, fmt.Errorf("gen: layer value conversion profile %d is absent", layer)
		}
	}

	return &layerValueCompiler{
		schemaSet:   g.schemaSet,
		conversions: conversions,
		bindings:    wire.Bindings,
		wire:        wire,
		profiles:    profiles,
		families:    families,
	}, nil
}

// Compile builds a value plan for ref in one exact target layer. Unknown
// layers, malformed references and missing target constructors are hard
// generation errors; layers are never clamped and constructors are never
// guessed by name or wire ID.
func (c *layerValueCompiler) Compile(layer int, ref *semantic.TypeRef) (*layerValuePlan, error) {
	if c == nil || c.schemaSet == nil || c.conversions == nil {
		return nil, fmt.Errorf("gen: nil layer value compiler")
	}
	profile := c.profiles[layer]
	schema := c.schemaSet.Schemas[layer]
	if profile == nil || schema == nil {
		return nil, fmt.Errorf("gen: layer value profile %d is not generated", layer)
	}
	if ref == nil {
		return nil, fmt.Errorf("gen: layer value profile %d has nil TypeRef", layer)
	}
	return c.compile(profile, schema, ref, make(map[*semantic.TypeRef]struct{}))
}

func (c *layerValueCompiler) compile(
	profile *LayerProfileConversion,
	schema *semantic.SchemaModel,
	ref *semantic.TypeRef,
	stack map[*semantic.TypeRef]struct{},
) (*layerValuePlan, error) {
	if _, cyclic := stack[ref]; cyclic {
		return nil, fmt.Errorf(
			"gen: layer value profile %d has cyclic TypeRef at kind=%d qname=%q",
			profile.Layer, ref.Kind, ref.QName,
		)
	}
	stack[ref] = struct{}{}
	defer delete(stack, ref)

	plan := &layerValuePlan{Ref: ref}
	switch ref.Kind {
	case semantic.TypePrimitive:
		if ref.QName == "" || ref.Arg != nil {
			return nil, fmt.Errorf("gen: layer value profile %d has malformed primitive %s", profile.Layer, ref.String())
		}
		if ref.QName == "Object" {
			if ref.Bare || ref.Percent {
				return nil, fmt.Errorf("gen: layer value profile %d has malformed dynamic Object %s", profile.Layer, ref.String())
			}
			plan.Kind = layerValueDynamicObject
			return plan, nil
		}
		plan.Kind = layerValuePrimitive
		plan.Primitive = ref.QName
		return plan, nil

	case semantic.TypeGenericRef:
		if ref.QName == "" || ref.Arg != nil || ref.Bare || ref.Percent {
			return nil, fmt.Errorf("gen: layer value profile %d has malformed generic reference %s", profile.Layer, ref.String())
		}
		plan.Kind = layerValueDynamicGeneric
		plan.GenericParam = ref.QName
		return plan, nil

	case semantic.TypeVector:
		if ref.Arg == nil {
			return nil, fmt.Errorf("gen: layer value profile %d vector %s has no element TypeRef", profile.Layer, ref.String())
		}
		if ref.QName != "Vector" && ref.QName != "vector" {
			return nil, fmt.Errorf("gen: layer value profile %d has unsupported vector spelling %s", profile.Layer, ref.String())
		}
		if ref.QName == "Vector" && !ref.Bare && !ref.Percent {
			plan.VectorMode = layerValueVectorBoxed
		} else {
			plan.VectorMode = layerValueVectorBare
		}
		plan.Kind = layerValueVector
		element, err := c.compile(profile, schema, ref.Arg, stack)
		if err != nil {
			return nil, fmt.Errorf(
				"gen: layer value profile %d vector kind=%d qname=%q element: %w",
				profile.Layer, ref.Kind, ref.QName, err,
			)
		}
		plan.Element = element
		return plan, nil

	case semantic.TypeNamed:
		if ref.QName == "" || ref.Arg != nil {
			return nil, fmt.Errorf("gen: layer value profile %d has malformed named reference %s", profile.Layer, ref.String())
		}
		if ref.Bare || ref.Percent {
			constructor, err := c.constructor(profile, schema, semantic.SemanticKey{
				Category: semantic.CategoryType,
				QName:    ref.QName,
			})
			if err != nil {
				return nil, fmt.Errorf("gen: layer value profile %d exact bare %s: %w", profile.Layer, ref.String(), err)
			}
			plan.Kind = layerValueExactBare
			plan.Constructors = []layerValueConstructor{constructor}
			return plan, nil
		}

		keys := append([]semantic.SemanticKey(nil), schema.ConstructorsByClass[ref.QName]...)
		if len(keys) == 0 {
			return nil, fmt.Errorf("gen: layer value profile %d boxed class %q has no constructors", profile.Layer, ref.QName)
		}
		sortSemanticKeys(keys)
		canonicalSchema := c.schemaSet.Schemas[c.schemaSet.CanonicalLayer]
		if canonicalSchema != nil && len(canonicalSchema.ConstructorsByClass[ref.QName]) != 0 {
			plan.CanonicalClass = c.bindings.class(ref.QName)
			if plan.CanonicalClass == nil {
				return nil, fmt.Errorf("gen: layer value profile %d boxed class %q has no canonical Go class binding", profile.Layer, ref.QName)
			}
		}
		plan.Constructors = make([]layerValueConstructor, 0, len(keys))
		for _, key := range keys {
			constructor, err := c.constructor(profile, schema, key)
			if err != nil {
				return nil, fmt.Errorf("gen: layer value profile %d boxed class %q constructor %s: %w", profile.Layer, ref.QName, key, err)
			}
			if plan.CanonicalClass != nil && constructor.Canonical != nil && constructor.Canonical.Definition != nil &&
				constructor.Canonical.Definition.Result.String() != plan.CanonicalClass.QName {
				// Contextual class splits are decoded by the reviewed incompatible
				// field adapter that names the new target class. They are not values
				// of the canonical form of this historical class.
				continue
			}
			if layerRejectedHistoricalType(constructor.Conversion) {
				continue
			}
			plan.Constructors = append(plan.Constructors, constructor)
		}
		// The value's Go shape is owned by the canonical schema, not by how
		// many constructors happen to remain in this wire profile. A lower
		// layer may contain one Update constructor while the canonical binding
		// is UpdateClass; treating that profile as concrete would generate
		// impossible *UpdateClass -> *Concrete conversions. Keep the canonical
		// interface strategy and let its exact-profile constructor switch
		// enforce the smaller wire set.
		if plan.CanonicalClass != nil && !plan.CanonicalClass.Backend.Singular {
			plan.Kind = layerValueBoxedAbstract
		} else if plan.CanonicalClass != nil && plan.CanonicalClass.Backend.Singular {
			if len(plan.CanonicalClass.Constructors) != 1 {
				return nil, fmt.Errorf("gen: canonical singular class %q has %d constructors", ref.QName, len(plan.CanonicalClass.Constructors))
			}
			canonicalKey := plan.CanonicalClass.Constructors[0].Key
			var canonicalConstructor *layerValueConstructor
			for index := range plan.Constructors {
				candidate := &plan.Constructors[index]
				if candidate.Canonical != nil && candidate.Canonical.Key == canonicalKey {
					if canonicalConstructor == nil {
						canonicalConstructor = candidate
					}
				}
			}
			if canonicalConstructor == nil {
				return nil, fmt.Errorf("gen: layer value profile %d singular canonical constructor %s is unavailable in class %q", profile.Layer, canonicalKey, ref.QName)
			}
			for _, candidate := range plan.Constructors {
				if candidate.Canonical == nil || candidate.Canonical.Key != canonicalKey {
					return nil, fmt.Errorf("gen: layer value profile %d singular class %q maps to multiple canonical constructors", profile.Layer, ref.QName)
				}
			}
			plan.Kind = layerValueBoxedConcrete
		} else if len(plan.Constructors) == 1 {
			plan.Kind = layerValueBoxedConcrete
		} else {
			plan.Kind = layerValueBoxedAbstract
		}
		return plan, nil

	default:
		return nil, fmt.Errorf("gen: layer value profile %d has unsupported TypeRef kind %d", profile.Layer, ref.Kind)
	}
}

func (c *layerValueCompiler) constructor(
	profile *LayerProfileConversion,
	schema *semantic.SchemaModel,
	key semantic.SemanticKey,
) (layerValueConstructor, error) {
	if key.Category != semantic.CategoryType {
		return layerValueConstructor{}, fmt.Errorf("constructor key %s is not a type", key)
	}
	definition := schema.ByKey[key]
	conversion := c.families[profile.Layer][key]
	historical := c.wire.historicalSource(profile.Layer, key)
	if definition == nil {
		// A complete canonical TypeRef names the canonical target, even when an
		// older profile spells that constructor with a deleted semantic name.
		// Resolve that reverse edge through the shared historical wire plan.
		historical = c.wire.historicalTarget(profile.Layer, key)
		if historical == nil {
			return layerValueConstructor{}, fmt.Errorf("target constructor %s is absent", key)
		}
		definition = historical.Definition
		conversion = historical.Conversion
	}
	if conversion == nil || conversion.Profile == nil || conversion.Profile.Definition != definition {
		return layerValueConstructor{}, fmt.Errorf("conversion for target constructor %s is absent or stale", key)
	}
	if conversion.Availability != LayerAvailabilityPresent && conversion.Availability != LayerAvailabilityProfileOnly {
		return layerValueConstructor{}, fmt.Errorf(
			"target constructor %s has impossible availability %s",
			key, conversion.Availability,
		)
	}

	result := layerValueConstructor{Conversion: conversion}
	if conversion.Canonical == nil {
		if conversion.Availability != LayerAvailabilityProfileOnly {
			return layerValueConstructor{}, fmt.Errorf("target constructor %s has no canonical variant but availability is %s", key, conversion.Availability)
		}
		if historical != nil {
			if historical.Target == nil || historical.Target.Structure == nil {
				return layerValueConstructor{}, fmt.Errorf("historical constructor %s has no canonical target binding", key)
			}
			result.Canonical = historical.Target
		}
		return result, nil
	}
	binding := c.bindings.definition(key)
	if binding == nil || binding.Structure == nil {
		return layerValueConstructor{}, fmt.Errorf("target constructor %s has no canonical Go binding", key)
	}
	result.Canonical = binding
	return result, nil
}
