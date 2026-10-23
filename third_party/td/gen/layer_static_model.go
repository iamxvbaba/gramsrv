package gen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/gen/semantic"
)

// layerStaticMode is the amount of generated work needed for one family in
// one wire profile. The static model is deliberately an emitter projection:
// it points at the normalized semantic variants but never copies their type
// graph or the complete schema catalog.
type layerStaticMode uint8

const (
	layerStaticDirect layerStaticMode = iota
	layerStaticRetag
	layerStaticRewrite
	layerStaticUnavailable
	layerStaticObligation
)

func (m layerStaticMode) String() string {
	switch m {
	case layerStaticDirect:
		return "direct"
	case layerStaticRetag:
		return "retag"
	case layerStaticRewrite:
		return "rewrite"
	case layerStaticUnavailable:
		return "unavailable"
	case layerStaticObligation:
		return "obligation"
	default:
		return fmt.Sprintf("layerStaticMode(%d)", m)
	}
}

// layerStaticModel contains only families which need layer-aware rendering in
// at least one non-canonical profile. Families which are identical in every
// loaded profile are intentionally absent.
type layerStaticModel struct {
	CanonicalLayer        int
	Profiles              []layerStaticProfile
	FamilyCount           int
	RewriteCount          int
	UnavailableCount      int
	ObligationCount       int
	ResultObligationCount int
}

type layerStaticProfile struct {
	Layer                 int
	Families              []layerStaticFamily
	RewriteCount          int
	UnavailableCount      int
	ObligationCount       int
	ResultObligationCount int
}

// layerStaticFamily selects the canonical and target profile variants used by
// an emitter. CanonicalStruct and Fields are references into the canonical Go
// backend model; no target TypeRef or schema definition is duplicated here.
type layerStaticFamily struct {
	Key             semantic.SemanticKey
	Canonical       *semantic.ProfileVariant
	Target          *semantic.ProfileVariant
	CanonicalStruct *structDef
	Fields          []layerStaticField
	Mode            layerStaticMode
	OwnDirty        bool
	BodyDirty       bool
	NestedDirty     bool
	Result          layerStaticResult
	Obligations     []LayerObligation
	Projection      []LayerObligation
	Obligation      string
}

// layerStaticResult is kept separate from the request/constructor body plan.
// Its TypeRefs remain on Canonical/Target; this scalar projection only tells
// the emitter whether it may use the canonical result context or needs a
// strongly typed compatibility hook.
type layerStaticResult struct {
	Mode        layerStaticMode
	Changed     bool
	Obligations []LayerObligation
	Obligation  string
}

// layerStaticField is the minimum scalar projection needed to render a target
// field. TargetOrdinal resolves back through Target.Definition.Fields; keeping
// the ordinal avoids cloning a recursive semantic.TypeRef into this model.
type layerStaticField struct {
	Name             string
	TargetOrdinal    int
	CanonicalOrdinal int
	Canonical        *fieldDef
	FlagsWord        bool
	Conditional      bool
	ConditionWord    string
	ConditionBit     uint8
	PresenceOnly     bool
}

type layerStaticDirty struct {
	body   bool
	result bool
	own    bool
	nested bool
}

type canonicalStaticBinding struct {
	structure *structDef
	fields    map[string]int
}

func formatLayerStaticObligations(obligations []LayerObligation) string {
	if len(obligations) == 0 {
		return ""
	}
	parts := make([]string, 0, len(obligations))
	for _, obligation := range obligations {
		parts = append(parts, fmt.Sprintf("%s (%s)", obligation.Kind, obligation.Key))
	}
	return strings.Join(parts, "; ")
}

// buildLayerStaticModel derives a sparse, static emitter plan from the shared
// schema set and the already-built canonical Go backend. It is intentionally a
// pure projection step: no schema parsing and no generated source mutation.
func (g *Generator) buildLayerStaticModel() (*layerStaticModel, error) {
	if g == nil || g.schemaSet == nil {
		return nil, fmt.Errorf("gen: layer static model requires a schema-set generator")
	}

	bindings, err := g.canonicalStaticBindings()
	if err != nil {
		return nil, err
	}
	conversionPlan := g.layerPlan
	if conversionPlan == nil {
		return nil, fmt.Errorf("gen: static layer conversion plan is absent")
	}
	for _, profile := range conversionPlan.Profiles {
		for _, obligation := range profile.Obligations {
			if obligation.Kind == LayerObligationPrivate && !obligation.Resolution.resolved() {
				return nil, fmt.Errorf("gen: unresolved private schema profile obligation %s", obligation.Key)
			}
		}
	}

	layers := g.schemaSet.Layers()
	dirtyByLayer := make(map[int]map[semantic.SemanticKey]layerStaticDirty, len(layers))
	selected := make(map[semantic.SemanticKey]struct{})
	for _, layer := range layers {
		dirty := analyzeLayerStaticDirty(g.schemaSet, conversionPlan, layer)
		dirtyByLayer[layer] = dirty
		if layer == g.schemaSet.CanonicalLayer {
			continue
		}
		for key, state := range dirty {
			if state.own || state.nested {
				selected[key] = struct{}{}
			}
		}
	}

	keys := make([]semantic.SemanticKey, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sortSemanticKeys(keys)

	model := &layerStaticModel{
		CanonicalLayer: g.schemaSet.CanonicalLayer,
		FamilyCount:    len(keys),
		Profiles:       make([]layerStaticProfile, 0, len(layers)),
	}
	for _, layer := range layers {
		profile := layerStaticProfile{Layer: layer}
		conversionProfile := conversionPlan.Profile(layer)
		if conversionProfile == nil {
			return nil, fmt.Errorf("gen: static layer conversion profile %d is absent", layer)
		}
		for _, key := range keys {
			conversion := conversionProfile.Family(key)
			if conversion == nil {
				return nil, fmt.Errorf("gen: selected layer family %s is absent", key)
			}
			canonical := conversion.Canonical
			target := conversion.Profile
			// A target-only family has nothing to render in a profile where both
			// sides are absent (notably the canonical profile itself).
			if canonical == nil && target == nil {
				continue
			}

			entry, err := buildLayerStaticFamily(
				key,
				canonical,
				target,
				bindings[key],
				dirtyByLayer[layer][key],
				conversion,
			)
			if err != nil {
				return nil, fmt.Errorf("gen: build static layer %d family %s: %w", layer, key, err)
			}
			profile.Families = append(profile.Families, entry)
			switch entry.Mode {
			case layerStaticRewrite:
				profile.RewriteCount++
				model.RewriteCount++
			case layerStaticUnavailable:
				profile.UnavailableCount++
				model.UnavailableCount++
			case layerStaticObligation:
				profile.ObligationCount++
				model.ObligationCount++
			}
			if entry.Result.Mode == layerStaticObligation {
				profile.ResultObligationCount++
				model.ResultObligationCount++
			}
		}
		model.Profiles = append(model.Profiles, profile)
	}
	return model, nil
}

func (g *Generator) canonicalStaticBindings() (map[semantic.SemanticKey]canonicalStaticBinding, error) {
	canonical := g.schemaSet.Schemas[g.schemaSet.CanonicalLayer]
	if canonical == nil {
		return nil, fmt.Errorf("gen: canonical semantic layer %d is absent", g.schemaSet.CanonicalLayer)
	}
	// makeStructures may append synthetic boxes for Vector<T> method results.
	// Those have no TL definition and must not shift canonical definition
	// ordinals. All schema-backed structures have Vector == false.
	structures := make([]*structDef, 0, len(canonical.Definitions))
	for i := range g.structs {
		if !g.structs[i].Vector {
			structures = append(structures, &g.structs[i])
		}
	}
	if len(canonical.Definitions) != len(structures) {
		return nil, fmt.Errorf(
			"gen: canonical semantic/backend definition count mismatch: semantic=%d schema=%d backend=%d (all backend structs=%d)",
			len(canonical.Definitions), len(g.schema.Definitions), len(structures), len(g.structs),
		)
	}

	bindings := make(map[semantic.SemanticKey]canonicalStaticBinding, len(canonical.Definitions))
	for i, definition := range canonical.Definitions {
		structure := structures[i]
		if structure.RawName != definition.Key.QName {
			return nil, fmt.Errorf(
				"gen: canonical definition %d mismatch: semantic %s, backend %q",
				i, definition.Key, structure.RawName,
			)
		}
		if structure.HexID != fmt.Sprintf("%x", definition.WireID) {
			return nil, fmt.Errorf(
				"gen: canonical wire id mismatch for %s: semantic %#08x, backend %s",
				definition.Key, definition.WireID, structure.HexID,
			)
		}
		if len(definition.Fields) != len(structure.Fields) {
			return nil, fmt.Errorf(
				"gen: canonical field count mismatch for %s: semantic %d, backend %d",
				definition.Key, len(definition.Fields), len(structure.Fields),
			)
		}
		fields := make(map[string]int, len(definition.Fields))
		for fieldIndex, field := range definition.Fields {
			backendField := &structure.Fields[fieldIndex]
			if backendField.RawName != field.Name {
				return nil, fmt.Errorf(
					"gen: canonical field %d mismatch for %s: semantic %q, backend %q",
					fieldIndex, definition.Key, field.Name, backendField.RawName,
				)
			}
			fields[field.Name] = fieldIndex
		}
		bindings[definition.Key] = canonicalStaticBinding{
			structure: structure,
			fields:    fields,
		}
	}
	return bindings, nil
}

func buildLayerStaticFamily(
	key semantic.SemanticKey,
	canonical *semantic.ProfileVariant,
	target *semantic.ProfileVariant,
	binding canonicalStaticBinding,
	dirty layerStaticDirty,
	conversion *LayerFamilyConversion,
) (layerStaticFamily, error) {
	bodyObligations := conversion.BodyObligations()
	resultObligations := conversion.ResultObligations()
	projectionObligations := conversion.ProjectionObligations()
	entry := layerStaticFamily{
		Key:         key,
		Canonical:   canonical,
		Target:      target,
		OwnDirty:    dirty.own,
		BodyDirty:   dirty.body,
		NestedDirty: dirty.nested,
		Result: layerStaticResult{
			Changed:     dirty.result,
			Obligations: resultObligations,
		},
		Obligations: bodyObligations,
		Projection:  projectionObligations,
	}
	if binding.structure != nil {
		entry.CanonicalStruct = binding.structure
	}

	switch {
	case canonical == nil:
		entry.Mode = layerStaticObligation
		entry.Obligation = formatLayerStaticObligations(bodyObligations)
		if entry.Obligation == "" {
			entry.Obligation = "family has no canonical representation"
		}
		entry.Result.Mode = layerStaticDirect
		return entry, nil
	case target == nil:
		entry.Mode = layerStaticUnavailable
		entry.Obligation = "family is unavailable in target profile"
		if key.Category == semantic.CategoryFunction {
			entry.Result.Mode = layerStaticUnavailable
		} else {
			entry.Result.Mode = layerStaticDirect
		}
		return entry, nil
	case binding.structure == nil:
		return layerStaticFamily{}, fmt.Errorf("canonical backend binding is absent")
	}

	canonicalDefinition := canonical.Definition
	targetDefinition := target.Definition
	entry.Result.Changed = key.Category == semantic.CategoryFunction &&
		!canonicalDefinition.Result.Equal(targetDefinition.Result)
	if entry.Result.Changed {
		entry.Result.Mode = layerStaticObligation
		entry.Result.Obligation = formatLayerStaticObligations(resultObligations)
		if entry.Result.Obligation == "" {
			return layerStaticFamily{}, fmt.Errorf(
				"result changes from %s to %s without a result obligation",
				canonicalDefinition.Result.String(), targetDefinition.Result.String(),
			)
		}
	} else {
		entry.Result.Mode = layerStaticDirect
	}

	entry.Fields = projectLayerStaticFields(targetDefinition, binding, conversion.Fields)
	if len(bodyObligations) != 0 {
		entry.Mode = layerStaticObligation
		entry.Obligation = formatLayerStaticObligations(bodyObligations)
		return entry, nil
	}

	sameBody := canonicalDefinition.BodyShape == targetDefinition.BodyShape
	sameID := canonicalDefinition.WireID == targetDefinition.WireID
	switch {
	case sameBody && sameID && !dirty.nested:
		entry.Mode = layerStaticDirect
		entry.Fields = nil
	case sameBody && !sameID && !dirty.nested:
		entry.Mode = layerStaticRetag
		entry.Fields = nil
	default:
		entry.Mode = layerStaticRewrite
	}
	return entry, nil
}

func projectLayerStaticFields(target *semantic.Definition, binding canonicalStaticBinding, mappings []LayerFieldMapping) []layerStaticField {
	canonicalOrdinalByTarget := make(map[int]int, len(mappings))
	for _, mapping := range mappings {
		canonicalOrdinalByTarget[mapping.ProfileOrdinal] = mapping.CanonicalOrdinal
	}
	projection := make([]layerStaticField, 0, len(target.Fields))
	for targetOrdinal, targetField := range target.Fields {
		field := layerStaticField{
			Name:             targetField.Name,
			TargetOrdinal:    targetOrdinal,
			CanonicalOrdinal: -1,
			FlagsWord:        targetField.Kind == semantic.FieldFlagsWord,
		}
		if targetField.Condition != nil {
			field.Conditional = true
			field.ConditionWord = targetField.Condition.Word
			field.ConditionBit = targetField.Condition.Bit
			field.PresenceOnly = targetField.Condition.PresenceOnly
		}
		if canonicalOrdinal, ok := canonicalOrdinalByTarget[targetOrdinal]; ok && canonicalOrdinal >= 0 {
			field.CanonicalOrdinal = canonicalOrdinal
			field.Canonical = &binding.structure.Fields[canonicalOrdinal]
		}
		projection = append(projection, field)
	}

	return projection
}

// analyzeLayerStaticDirty computes reverse body reachability for one profile.
// ID/body/availability changes are body seeds. Result-only changes select the
// family but remain in its independent result plan. Any body which can contain
// a body seed becomes nested-dirty, including boxed abstract classes, exact
// bare constructors and recursively nested vectors.
func analyzeLayerStaticDirty(schemaSet *SchemaSet, plan *LayerConversionPlan, layer int) map[semantic.SemanticKey]layerStaticDirty {
	states := make(map[semantic.SemanticKey]layerStaticDirty, len(schemaSet.Families))
	reverse := make(map[semantic.SemanticKey]map[semantic.SemanticKey]struct{})
	dynamicParents := make(map[semantic.SemanticKey]struct{})
	canonicalSchema := schemaSet.Schemas[schemaSet.CanonicalLayer]
	targetSchema := schemaSet.Schemas[layer]
	profilePlan := plan.Profile(layer)

	queue := make([]semantic.SemanticKey, 0)
	for key := range schemaSet.Families {
		conversion := profilePlan.Family(key)
		canonical := conversion.Canonical
		target := conversion.Profile
		bodyDirty := conversion.BodyChanged
		resultDirty := conversion.ResultChanged
		states[key] = layerStaticDirty{
			body:   bodyDirty,
			result: resultDirty,
			own:    bodyDirty || resultDirty,
		}
		if bodyDirty {
			queue = append(queue, key)
		}

		dependencies := make(map[semantic.SemanticKey]struct{})
		dynamic := false
		if canonical != nil {
			dynamic = collectLayerStaticDependencies(schemaSet, canonicalSchema, targetSchema, canonical.Definition, dependencies) || dynamic
		}
		if target != nil {
			dynamic = collectLayerStaticDependencies(schemaSet, canonicalSchema, targetSchema, target.Definition, dependencies) || dynamic
		}
		if dynamic {
			dynamicParents[key] = struct{}{}
		}
		for dependency := range dependencies {
			if dependency == key {
				continue
			}
			parents := reverse[dependency]
			if parents == nil {
				parents = make(map[semantic.SemanticKey]struct{})
				reverse[dependency] = parents
			}
			parents[key] = struct{}{}
		}
	}
	// Generic !X and Object fields can contain any boxed request/type. They do
	// not expand the sparse IR into the complete catalog, but if this profile
	// has any dirty body at all their parent must call the generated profile-
	// aware boxed dispatcher instead of canonical Encode/Decode.
	if len(queue) != 0 {
		for parent := range dynamicParents {
			state := states[parent]
			if state.nested {
				continue
			}
			state.nested = true
			states[parent] = state
			queue = append(queue, parent)
		}
	}

	for head := 0; head < len(queue); head++ {
		dependency := queue[head]
		for parent := range reverse[dependency] {
			state := states[parent]
			if state.nested {
				continue
			}
			state.nested = true
			states[parent] = state
			queue = append(queue, parent)
		}
	}
	return states
}

func collectLayerStaticDependencies(
	schemaSet *SchemaSet,
	canonicalSchema *semantic.SchemaModel,
	targetSchema *semantic.SchemaModel,
	definition *semantic.Definition,
	out map[semantic.SemanticKey]struct{},
) (dynamic bool) {
	for _, field := range definition.Fields {
		if field.Kind == semantic.FieldValue {
			dynamic = collectLayerStaticTypeDependencies(schemaSet, canonicalSchema, targetSchema, field.Type, out) || dynamic
		}
	}
	// Results are not request/constructor body dependencies. A method result
	// has its own TypeRef context and is selected through Result.Changed plus the
	// canonical/target ProfileVariant pair; mixing it into this graph would
	// incorrectly force every method returning a changed abstract class to
	// rewrite its request body.
	return dynamic
}

func collectLayerStaticTypeDependencies(
	schemaSet *SchemaSet,
	canonicalSchema *semantic.SchemaModel,
	targetSchema *semantic.SchemaModel,
	ref semantic.TypeRef,
	out map[semantic.SemanticKey]struct{},
) bool {
	switch ref.Kind {
	case semantic.TypeVector:
		if ref.Arg != nil {
			return collectLayerStaticTypeDependencies(schemaSet, canonicalSchema, targetSchema, *ref.Arg, out)
		}
	case semantic.TypeNamed:
		if ref.Bare || ref.Percent {
			key := semantic.SemanticKey{Category: semantic.CategoryType, QName: ref.QName}
			if _, ok := schemaSet.Families[key]; ok {
				out[key] = struct{}{}
			}
			return false
		}
		found := false
		for _, schema := range []*semantic.SchemaModel{canonicalSchema, targetSchema} {
			if schema == nil {
				continue
			}
			for _, key := range schema.ConstructorsByClass[ref.QName] {
				out[key] = struct{}{}
				found = true
			}
		}
		// Some hand-written schemas use a constructor name where a class name
		// would normally appear. Keep that exact family reachable as a safe
		// fallback without expanding the complete catalog.
		if !found {
			key := semantic.SemanticKey{Category: semantic.CategoryType, QName: ref.QName}
			if _, ok := schemaSet.Families[key]; ok {
				out[key] = struct{}{}
			}
		}
	case semantic.TypeGenericRef:
		return true
	case semantic.TypePrimitive:
		return ref.QName == "Object"
	}
	return false
}

func sortSemanticKeys(keys []semantic.SemanticKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Category != keys[j].Category {
			return keys[i].Category < keys[j].Category
		}
		return keys[i].QName < keys[j].QName
	})
}

func (m *layerStaticModel) profile(layer int) *layerStaticProfile {
	for i := range m.Profiles {
		if m.Profiles[i].Layer == layer {
			return &m.Profiles[i]
		}
	}
	return nil
}

func (p *layerStaticProfile) family(key semantic.SemanticKey) *layerStaticFamily {
	for i := range p.Families {
		if p.Families[i].Key == key {
			return &p.Families[i]
		}
	}
	return nil
}
