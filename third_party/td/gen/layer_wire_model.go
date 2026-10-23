package gen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/gen/semantic"
)

// layerWireActionKind is one complete generated action for a semantic family
// in an exact profile. It is an emitter decision, not a runtime schema table.
type layerWireActionKind uint8

const (
	layerWireDirect layerWireActionKind = iota
	layerWireRetag
	layerWireRewrite
	layerWirePolicy
	layerWireUnavailable
	layerWireProfileOnly
	layerWireAbsent
	layerWireReject
)

func (k layerWireActionKind) String() string {
	switch k {
	case layerWireDirect:
		return "direct"
	case layerWireRetag:
		return "retag"
	case layerWireRewrite:
		return "rewrite"
	case layerWirePolicy:
		return "policy"
	case layerWireUnavailable:
		return "unavailable"
	case layerWireProfileOnly:
		return "profile-only"
	case layerWireAbsent:
		return "absent"
	case layerWireReject:
		return "reject"
	default:
		return fmt.Sprintf("layerWireActionKind(%d)", k)
	}
}

// layerWireModel is the deterministic static emitter projection. Wire codecs
// are unique by wire ID, while family and class views reference them by stable
// slice index. Exact profile variants are retained even when they share an ID.
type layerWireModel struct {
	CanonicalLayer int
	Profiles       []int
	Bindings       *layerBindingIndex
	Wires          []layerWirePlan
	Families       []layerFamilyPlan
	Classes        []layerClassPlan

	// HistoricalConstructors is the one policy-validated bridge from a
	// constructor which exists only in an older profile to the canonical Go
	// constructor used by every generated backend.  Keeping this plan beside
	// the wire/family/class IR is important: metadata, complete TypeRefs and the
	// static codec must not independently rediscover (or disagree about) the
	// effective semantic-to-wire mapping.
	HistoricalConstructors []*layerHistoricalConstructorPlan
	historicalByWire       map[layerHistoricalWireKey]*layerHistoricalConstructorPlan
	historicalByTarget     map[layerHistoricalTargetKey][]*layerHistoricalConstructorPlan
	historicalBySource     map[layerHistoricalSourceKey]*layerHistoricalConstructorPlan
}

// layerWirePlan is exactly one Universe.WireCodec. BodyVariants preserve
// same-ID profile semantics (notably flags.N?true additions) without copying
// a codec for every layer.
type layerWirePlan struct {
	WireID       uint32
	Key          semantic.SemanticKey
	Codec        *semantic.WireCodec
	Canonical    *layerDefinitionBinding
	BodyVariants []layerWireBodyVariantPlan
	Profiles     []layerWireProfileAction
}

type layerWireBodyVariantPlan struct {
	Shape      semantic.ShapeDigest
	Definition *semantic.Definition
	Profiles   []int
}

// layerWireProfileAction is one accepted (profile, wire ID) pair.
type layerWireProfileAction struct {
	Layer       int
	Variant     *semantic.ProfileVariant
	Conversion  *LayerFamilyConversion
	BodyVariant int
	FamilyIndex int
	Kind        layerWireActionKind
}

// layerFamilyPlan is the semantic encoding view. It contains exactly one
// action for every generated profile, including explicit unavailable/absent
// actions, so callers never infer compatibility from missing data.
type layerFamilyPlan struct {
	Key       semantic.SemanticKey
	Canonical *layerDefinitionBinding
	WireIDs   []uint32
	Profiles  []layerFamilyProfileAction
}

type layerFamilyProfileAction struct {
	Layer         int
	Kind          layerWireActionKind
	Conversion    *LayerFamilyConversion
	WireIndex     int
	BodyVariant   int
	OwnDirty      bool
	BodyDirty     bool
	NestedDirty   bool
	ResultChanged bool
}

// layerClassPlan is the exact boxed-constructor membership of one TL class in
// every profile. Canonical is nil only for a class which exists exclusively in
// historical profiles.
type layerClassPlan struct {
	QName     string
	Canonical *layerClassBinding
	Profiles  []layerClassProfilePlan
}

type layerClassProfilePlan struct {
	Layer        int
	Constructors []layerClassConstructorPlan
}

type layerClassConstructorPlan struct {
	Key         semantic.SemanticKey
	WireID      uint32
	FamilyIndex int
	WireIndex   int
	BodyVariant int
	Kind        layerWireActionKind
}

type layerHistoricalWireKey struct {
	Layer  int
	WireID uint32
}

type layerHistoricalTargetKey struct {
	Layer int
	Key   semantic.SemanticKey
}

type layerHistoricalSourceKey struct {
	Layer int
	Key   semantic.SemanticKey
}

// layerHistoricalConstructorPlan is generation-only policy IR.  Functions
// deliberately never enter this plan: an old-only method remains an RPC
// admission decision, while constructors need a bidirectional value codec so
// they can occur recursively in fields, results, vectors, classes and Object.
type layerHistoricalConstructorPlan struct {
	Layer      int
	WireID     uint32
	OldKey     semantic.SemanticKey
	Definition *semantic.Definition
	Conversion *LayerFamilyConversion
	TargetKey  semantic.SemanticKey
	Target     *layerDefinitionBinding
	Resolution LayerObligationResolution
}

func (m *layerWireModel) historicalWire(layer int, wireID uint32) *layerHistoricalConstructorPlan {
	if m == nil {
		return nil
	}
	return m.historicalByWire[layerHistoricalWireKey{Layer: layer, WireID: wireID}]
}

func (m *layerWireModel) historicalTarget(layer int, key semantic.SemanticKey) *layerHistoricalConstructorPlan {
	if m == nil {
		return nil
	}
	plans := m.historicalByTarget[layerHistoricalTargetKey{Layer: layer, Key: key}]
	if len(plans) != 1 {
		return nil
	}
	return plans[0]
}

func (m *layerWireModel) historicalTargets(layer int, key semantic.SemanticKey) []*layerHistoricalConstructorPlan {
	if m == nil {
		return nil
	}
	return m.historicalByTarget[layerHistoricalTargetKey{Layer: layer, Key: key}]
}

func (m *layerWireModel) historicalSource(layer int, key semantic.SemanticKey) *layerHistoricalConstructorPlan {
	if m == nil {
		return nil
	}
	return m.historicalBySource[layerHistoricalSourceKey{Layer: layer, Key: key}]
}

func (m *layerWireModel) historicalSourceTarget(key semantic.SemanticKey) *layerDefinitionBinding {
	if m == nil {
		return nil
	}
	var target *layerDefinitionBinding
	for _, plan := range m.HistoricalConstructors {
		if plan == nil || plan.OldKey != key {
			continue
		}
		if target == nil {
			target = plan.Target
			continue
		}
		if plan.Target == nil || target.Key != plan.Target.Key {
			return nil
		}
	}
	return target
}

func (m *layerWireModel) wire(wireID uint32) *layerWirePlan {
	if m == nil {
		return nil
	}
	for i := range m.Wires {
		if m.Wires[i].WireID == wireID {
			return &m.Wires[i]
		}
	}
	return nil
}

func (m *layerWireModel) family(key semantic.SemanticKey) *layerFamilyPlan {
	if m == nil {
		return nil
	}
	for i := range m.Families {
		if m.Families[i].Key == key {
			return &m.Families[i]
		}
	}
	return nil
}

func (m *layerWireModel) class(qname string) *layerClassPlan {
	if m == nil {
		return nil
	}
	for i := range m.Classes {
		if m.Classes[i].QName == qname {
			return &m.Classes[i]
		}
	}
	return nil
}

func (p *layerFamilyPlan) profile(layer int) *layerFamilyProfileAction {
	if p == nil {
		return nil
	}
	for i := range p.Profiles {
		if p.Profiles[i].Layer == layer {
			return &p.Profiles[i]
		}
	}
	return nil
}

func (p *layerWirePlan) profile(layer int) *layerWireProfileAction {
	if p == nil {
		return nil
	}
	for i := range p.Profiles {
		if p.Profiles[i].Layer == layer {
			return &p.Profiles[i]
		}
	}
	return nil
}

// buildLayerWireModel consumes the one policy-applied conversion plan cached
// on Generator. It never re-runs schema comparison or obligation analysis.
func (g *Generator) buildLayerWireModel() (*layerWireModel, error) {
	if g == nil || g.schemaSet == nil {
		return nil, fmt.Errorf("gen: layer wire model requires a schema-set generator")
	}
	conversions := g.LayerConversionPlan()
	if conversions == nil {
		return nil, fmt.Errorf("gen: layer wire model requires the generator conversion plan")
	}
	if err := validateLayerWireConversionIdentity(g.schemaSet, conversions); err != nil {
		return nil, err
	}
	bindings, err := g.buildLayerBindings()
	if err != nil {
		return nil, fmt.Errorf("gen: layer wire canonical bindings: %w", err)
	}

	model := &layerWireModel{
		CanonicalLayer: g.schemaSet.CanonicalLayer,
		Profiles:       g.schemaSet.Layers(),
		Bindings:       bindings,
	}
	wireIndex, bodyVariant, err := buildLayerWirePlans(g.schemaSet, bindings, model)
	if err != nil {
		return nil, err
	}
	familyIndex, err := buildLayerFamilyPlans(g.schemaSet, conversions, bindings, wireIndex, bodyVariant, model)
	if err != nil {
		return nil, err
	}
	if err := joinLayerWireFamilyActions(model, familyIndex); err != nil {
		return nil, err
	}
	if err := buildLayerHistoricalConstructorPlans(model); err != nil {
		return nil, err
	}
	if err := buildLayerClassPlans(g.schemaSet, bindings, familyIndex, model); err != nil {
		return nil, err
	}
	if err := validateLayerWireCompleteness(g.schemaSet, model); err != nil {
		return nil, err
	}
	return model, nil
}

func buildLayerHistoricalConstructorPlans(model *layerWireModel) error {
	if model == nil || model.Bindings == nil {
		return fmt.Errorf("gen: historical constructor plan requires wire bindings")
	}
	model.historicalByWire = make(map[layerHistoricalWireKey]*layerHistoricalConstructorPlan)
	model.historicalByTarget = make(map[layerHistoricalTargetKey][]*layerHistoricalConstructorPlan)
	model.historicalBySource = make(map[layerHistoricalSourceKey]*layerHistoricalConstructorPlan)
	sourceTargets := make(map[semantic.SemanticKey]semantic.SemanticKey)

	for wireIndex := range model.Wires {
		wire := &model.Wires[wireIndex]
		if wire.Canonical != nil || wire.Key.Category != semantic.CategoryType {
			continue
		}
		for actionIndex := range wire.Profiles {
			action := &wire.Profiles[actionIndex]
			if action.Kind == layerWireReject {
				continue
			}
			if action.Conversion == nil || action.Variant == nil || action.Variant.Definition == nil {
				return fmt.Errorf("gen: E_PROFILE_ONLY_TYPE_VARIANT_MISSING: layer %d wire %#08x semantic %s has no exact conversion variant", action.Layer, wire.WireID, wire.Key)
			}
			resolution, err := layerHistoricalConstructorResolution(action.Conversion)
			if err != nil {
				return fmt.Errorf("gen: E_PROFILE_ONLY_TYPE_POLICY: layer %d wire %#08x semantic %s: %w", action.Layer, wire.WireID, wire.Key, err)
			}
			if resolution.Action == LayerResolveReject {
				continue // No canonical value exists for an explicitly rejected constructor.
			}
			targetKey, err := parseLayerPolicySemanticTarget(resolution.Target)
			if err != nil {
				return fmt.Errorf("gen: E_PROFILE_ONLY_TYPE_TARGET_INVALID: layer %d wire %#08x: %w", action.Layer, wire.WireID, err)
			}
			if targetKey.Category != semantic.CategoryType {
				return fmt.Errorf("gen: E_PROFILE_ONLY_TYPE_TARGET_NOT_CONSTRUCTOR: layer %d wire %#08x target %s is not a type constructor", action.Layer, wire.WireID, targetKey)
			}
			target := model.Bindings.definition(targetKey)
			if target == nil || target.Definition == nil || target.Structure == nil {
				return fmt.Errorf("gen: E_PROFILE_ONLY_TYPE_TARGET_NOT_FOUND: layer %d wire %#08x target %s has no canonical constructor binding", action.Layer, wire.WireID, targetKey)
			}
			definition := action.Variant.Definition
			if len(definition.GenericParams) != 0 {
				return fmt.Errorf("gen: E_PROFILE_ONLY_TYPE_GENERIC: layer %d wire %#08x historical constructors with generic parameters are not statically emittable", action.Layer, wire.WireID)
			}
			if previous, ok := sourceTargets[wire.Key]; ok && previous != targetKey {
				return fmt.Errorf("gen: E_PROFILE_ONLY_TYPE_SOURCE_TARGET_DRIFT: historical semantic %s maps to both %s and %s", wire.Key, previous, targetKey)
			}
			sourceTargets[wire.Key] = targetKey
			plan := &layerHistoricalConstructorPlan{
				Layer:      action.Layer,
				WireID:     wire.WireID,
				OldKey:     wire.Key,
				Definition: definition,
				Conversion: action.Conversion,
				TargetKey:  targetKey,
				Target:     target,
				Resolution: resolution,
			}
			wireKey := layerHistoricalWireKey{Layer: action.Layer, WireID: wire.WireID}
			if previous := model.historicalByWire[wireKey]; previous != nil {
				return fmt.Errorf("gen: E_PROFILE_ONLY_TYPE_DUPLICATE_WIRE: layer %d wire %#08x maps to both %s and %s", action.Layer, wire.WireID, previous.TargetKey, targetKey)
			}
			targetMapKey := layerHistoricalTargetKey{Layer: action.Layer, Key: targetKey}
			sourceMapKey := layerHistoricalSourceKey{Layer: action.Layer, Key: wire.Key}
			if previous := model.historicalBySource[sourceMapKey]; previous != nil {
				return fmt.Errorf("gen: E_PROFILE_ONLY_TYPE_DUPLICATE_SOURCE: layer %d historical semantic %s maps to wires %#08x and %#08x", action.Layer, wire.Key, previous.WireID, wire.WireID)
			}
			model.historicalByWire[wireKey] = plan
			model.historicalByTarget[targetMapKey] = append(model.historicalByTarget[targetMapKey], plan)
			model.historicalBySource[sourceMapKey] = plan
			model.HistoricalConstructors = append(model.HistoricalConstructors, plan)
		}
	}
	for key := range model.historicalByTarget {
		sort.Slice(model.historicalByTarget[key], func(i, j int) bool {
			return model.historicalByTarget[key][i].WireID < model.historicalByTarget[key][j].WireID
		})
	}
	return nil
}

func layerHistoricalConstructorResolution(conversion *LayerFamilyConversion) (LayerObligationResolution, error) {
	if conversion == nil {
		return LayerObligationResolution{}, fmt.Errorf("nil conversion")
	}
	var found *LayerObligationResolution
	for index := range conversion.Obligations {
		obligation := &conversion.Obligations[index]
		if obligation.Kind != LayerObligationOldOnly || !layerWireDirectionMatches(obligation.Direction, LayerDirectionProfileToCanonical) {
			continue
		}
		if found != nil {
			return LayerObligationResolution{}, fmt.Errorf("multiple old-only policy resolutions")
		}
		resolution := obligation.Resolution
		found = &resolution
	}
	if found != nil && found.Action == LayerResolveReject {
		return *found, nil
	}
	if found == nil || (found.Action != LayerResolveAlias && found.Action != LayerResolveAdapter) ||
		strings.TrimSpace(found.Hook) == "" || strings.TrimSpace(found.Target) == "" {
		return LayerObligationResolution{}, fmt.Errorf("historical type requires an explicit reject or one alias/adapter with hook and canonical target")
	}
	return *found, nil
}

// layerRejectedHistoricalType is an explicit admission decision, not an absent
// schema variant. Keep its wire identity for diagnostics and exact scanning,
// but never invent a canonical Go value or a reverse conversion for it.
func layerRejectedHistoricalType(conversion *LayerFamilyConversion) bool {
	if conversion == nil || conversion.Canonical != nil || conversion.Key.Category != semantic.CategoryType ||
		conversion.Availability != LayerAvailabilityProfileOnly {
		return false
	}
	resolution, err := layerHistoricalConstructorResolution(conversion)
	return err == nil && resolution.Action == LayerResolveReject
}

func layerWireDirectionMatches(actual, wanted LayerObligationDirection) bool {
	return actual == wanted || actual == LayerDirectionBoth
}

func validateLayerWireConversionIdentity(schemaSet *SchemaSet, conversions *LayerConversionPlan) error {
	if conversions.CanonicalLayer != schemaSet.CanonicalLayer {
		return fmt.Errorf(
			"gen: layer wire conversion canonical layer %d does not match schema set %d",
			conversions.CanonicalLayer, schemaSet.CanonicalLayer,
		)
	}
	layers := schemaSet.Layers()
	if len(conversions.Profiles) != len(layers) {
		return fmt.Errorf("gen: layer wire conversion profile count=%d, want=%d", len(conversions.Profiles), len(layers))
	}
	keys := schemaSet.SortedKeys()
	for profileIndex, layer := range layers {
		profile := &conversions.Profiles[profileIndex]
		if profile.Layer != layer {
			return fmt.Errorf("gen: layer wire conversion profile %d is layer %d, want %d", profileIndex, profile.Layer, layer)
		}
		if len(profile.Families) != len(keys) {
			return fmt.Errorf("gen: layer wire conversion layer %d family count=%d, want=%d", layer, len(profile.Families), len(keys))
		}
		seen := make(map[semantic.SemanticKey]struct{}, len(keys))
		for _, conversion := range profile.Families {
			if _, duplicate := seen[conversion.Key]; duplicate {
				return fmt.Errorf("gen: layer wire conversion layer %d repeats family %s", layer, conversion.Key)
			}
			seen[conversion.Key] = struct{}{}
			family := schemaSet.Families[conversion.Key]
			if family == nil ||
				conversion.Canonical != family.ProfilesByLayer[schemaSet.CanonicalLayer] ||
				conversion.Profile != family.ProfilesByLayer[layer] {
				return fmt.Errorf("gen: layer wire conversion layer %d family %s belongs to another schema set", layer, conversion.Key)
			}
		}
		for _, key := range keys {
			if _, ok := seen[key]; !ok {
				return fmt.Errorf("gen: layer wire conversion layer %d misses family %s", layer, key)
			}
		}
	}
	return nil
}

type layerWireProfileKey struct {
	Layer  int
	WireID uint32
}

func buildLayerWirePlans(
	schemaSet *SchemaSet,
	bindings *layerBindingIndex,
	model *layerWireModel,
) (map[uint32]int, map[layerWireProfileKey]int, error) {
	wireIDs := make([]uint32, 0, len(schemaSet.WireCodecs))
	for wireID := range schemaSet.WireCodecs {
		wireIDs = append(wireIDs, wireID)
	}
	sort.Slice(wireIDs, func(i, j int) bool { return wireIDs[i] < wireIDs[j] })

	wireIndex := make(map[uint32]int, len(wireIDs))
	bodyVariantByProfile := make(map[layerWireProfileKey]int)
	model.Wires = make([]layerWirePlan, 0, len(wireIDs))
	for _, wireID := range wireIDs {
		codec := schemaSet.WireCodecs[wireID]
		if codec == nil || codec.WireID != wireID {
			return nil, nil, fmt.Errorf("gen: layer wire codec map entry %#08x is nil or has mismatched ID", wireID)
		}
		family := schemaSet.Families[codec.Key]
		if family == nil {
			return nil, nil, fmt.Errorf("gen: layer wire codec %#08x references missing family %s", wireID, codec.Key)
		}

		plan := layerWirePlan{
			WireID:    wireID,
			Key:       codec.Key,
			Codec:     codec,
			Canonical: bindings.definition(codec.Key),
		}
		variants := append([]*semantic.ProfileVariant(nil), codec.ProfileVariants...)
		sort.Slice(variants, func(i, j int) bool { return variants[i].Layer < variants[j].Layer })
		seenLayers := make(map[int]struct{}, len(variants))
		variantByLayer := make(map[int]*semantic.ProfileVariant, len(variants))
		bodyByShape := make(map[semantic.ShapeDigest]int)
		for _, variant := range variants {
			if variant == nil || variant.Definition == nil || variant.WireCodec != codec {
				return nil, nil, fmt.Errorf("gen: layer wire codec %#08x contains a nil or foreign profile variant", wireID)
			}
			if variant.Definition.WireID != wireID || variant.Definition.Key != codec.Key {
				return nil, nil, fmt.Errorf("gen: layer wire codec %#08x profile %d identity mismatch", wireID, variant.Layer)
			}
			if _, duplicate := seenLayers[variant.Layer]; duplicate {
				return nil, nil, fmt.Errorf("gen: layer wire codec %#08x repeats profile %d", wireID, variant.Layer)
			}
			seenLayers[variant.Layer] = struct{}{}
			variantByLayer[variant.Layer] = variant
			if schemaSet.ByWire[variant.Layer][wireID] != variant.Definition {
				return nil, nil, fmt.Errorf("gen: layer wire codec %#08x profile %d is not the schema ByWire definition", wireID, variant.Layer)
			}

			bodyIndex, ok := bodyByShape[variant.Definition.BodyShape]
			if !ok {
				bodyIndex = len(plan.BodyVariants)
				bodyByShape[variant.Definition.BodyShape] = bodyIndex
				plan.BodyVariants = append(plan.BodyVariants, layerWireBodyVariantPlan{
					Shape:      variant.Definition.BodyShape,
					Definition: variant.Definition,
				})
			}
			plan.BodyVariants[bodyIndex].Profiles = append(plan.BodyVariants[bodyIndex].Profiles, variant.Layer)
			key := layerWireProfileKey{Layer: variant.Layer, WireID: wireID}
			if _, duplicate := bodyVariantByProfile[key]; duplicate {
				return nil, nil, fmt.Errorf("gen: duplicate layer wire action for profile %d ID %#08x", variant.Layer, wireID)
			}
			bodyVariantByProfile[key] = bodyIndex
		}
		if len(variants) == 0 || len(plan.BodyVariants) == 0 {
			return nil, nil, fmt.Errorf("gen: layer wire codec %#08x has no profile actions", wireID)
		}
		// A rejected pair is an explicit action too. This makes the full
		// (profile, wire ID) product complete and prevents runtime code from
		// treating absence as permission to guess or clamp a profile.
		plan.Profiles = make([]layerWireProfileAction, 0, len(model.Profiles))
		for _, layer := range model.Profiles {
			variant := variantByLayer[layer]
			if variant == nil {
				plan.Profiles = append(plan.Profiles, layerWireProfileAction{
					Layer:       layer,
					BodyVariant: -1,
					FamilyIndex: -1,
					Kind:        layerWireReject,
				})
				continue
			}
			bodyIndex, ok := bodyVariantByProfile[layerWireProfileKey{Layer: layer, WireID: wireID}]
			if !ok {
				return nil, nil, fmt.Errorf("gen: layer wire codec %#08x profile %d lost its body variant", wireID, layer)
			}
			plan.Profiles = append(plan.Profiles, layerWireProfileAction{
				Layer:       layer,
				Variant:     variant,
				BodyVariant: bodyIndex,
				FamilyIndex: -1,
			})
		}
		wireIndex[wireID] = len(model.Wires)
		model.Wires = append(model.Wires, plan)
	}
	return wireIndex, bodyVariantByProfile, nil
}

func buildLayerFamilyPlans(
	schemaSet *SchemaSet,
	conversions *LayerConversionPlan,
	bindings *layerBindingIndex,
	wireIndex map[uint32]int,
	bodyVariant map[layerWireProfileKey]int,
	model *layerWireModel,
) (map[semantic.SemanticKey]int, error) {
	keys := schemaSet.SortedKeys()
	indices := make(map[semantic.SemanticKey]int, len(keys))
	dirtyByLayer := make(map[int]map[semantic.SemanticKey]layerStaticDirty, len(model.Profiles))
	for _, layer := range model.Profiles {
		dirtyByLayer[layer] = analyzeLayerStaticDirty(schemaSet, conversions, layer)
	}

	model.Families = make([]layerFamilyPlan, 0, len(keys))
	for _, key := range keys {
		family := schemaSet.Families[key]
		if family == nil {
			return nil, fmt.Errorf("gen: layer wire family %s is absent", key)
		}
		wireSet := make(map[uint32]struct{}, len(family.ProfileVariants))
		for _, variant := range family.ProfileVariants {
			wireSet[variant.Definition.WireID] = struct{}{}
		}
		wireIDs := make([]uint32, 0, len(wireSet))
		for wireID := range wireSet {
			wireIDs = append(wireIDs, wireID)
		}
		sort.Slice(wireIDs, func(i, j int) bool { return wireIDs[i] < wireIDs[j] })
		plan := layerFamilyPlan{
			Key:       key,
			Canonical: bindings.definition(key),
			WireIDs:   wireIDs,
			Profiles:  make([]layerFamilyProfileAction, 0, len(model.Profiles)),
		}
		for _, layer := range model.Profiles {
			profile := conversions.Profile(layer)
			conversion := profile.Family(key)
			if conversion == nil {
				return nil, fmt.Errorf("gen: layer wire conversion profile %d misses family %s", layer, key)
			}
			dirty := dirtyByLayer[layer][key]
			action := layerFamilyProfileAction{
				Layer:         layer,
				Conversion:    conversion,
				WireIndex:     -1,
				BodyVariant:   -1,
				OwnDirty:      dirty.own,
				BodyDirty:     dirty.body,
				NestedDirty:   dirty.nested,
				ResultChanged: conversion.ResultChanged,
			}
			kind, err := classifyLayerFamilyAction(conversion, plan.Canonical, dirty)
			if err != nil {
				return nil, fmt.Errorf("gen: classify layer %d family %s: %w", layer, key, err)
			}
			action.Kind = kind
			if conversion.Profile != nil {
				wireID := conversion.Profile.Definition.WireID
				index, ok := wireIndex[wireID]
				if !ok {
					return nil, fmt.Errorf("gen: layer %d family %s references missing wire plan %#08x", layer, key, wireID)
				}
				action.WireIndex = index
				variant, ok := bodyVariant[layerWireProfileKey{Layer: layer, WireID: wireID}]
				if !ok {
					return nil, fmt.Errorf("gen: layer %d family %s references missing body variant %#08x", layer, key, wireID)
				}
				action.BodyVariant = variant
			}
			plan.Profiles = append(plan.Profiles, action)
		}
		indices[key] = len(model.Families)
		model.Families = append(model.Families, plan)
	}
	return indices, nil
}

func classifyLayerFamilyAction(
	conversion *LayerFamilyConversion,
	binding *layerDefinitionBinding,
	dirty layerStaticDirty,
) (layerWireActionKind, error) {
	switch conversion.Availability {
	case LayerAvailabilityAbsent:
		if conversion.Canonical != nil || conversion.Profile != nil {
			return 0, fmt.Errorf("absent action retains a profile variant")
		}
		return layerWireAbsent, nil
	case LayerAvailabilityCanonicalOnly:
		if conversion.Canonical == nil || conversion.Profile != nil || binding == nil {
			return 0, fmt.Errorf("canonical-only action has inconsistent variants or binding")
		}
		return layerWireUnavailable, nil
	case LayerAvailabilityProfileOnly:
		if conversion.Canonical != nil || conversion.Profile == nil || binding != nil {
			return 0, fmt.Errorf("profile-only action has inconsistent variants or binding")
		}
		return layerWireProfileOnly, nil
	case LayerAvailabilityPresent:
		if conversion.Canonical == nil || conversion.Profile == nil || binding == nil {
			return 0, fmt.Errorf("present action misses a profile variant or canonical binding")
		}
	default:
		return 0, fmt.Errorf("unsupported availability %s", conversion.Availability)
	}

	if len(conversion.BodyObligations()) != 0 {
		return layerWirePolicy, nil
	}
	canonical := conversion.Canonical.Definition
	profile := conversion.Profile.Definition
	sameBody := canonical.BodyShape == profile.BodyShape
	sameID := canonical.WireID == profile.WireID
	switch {
	case sameBody && sameID && !dirty.nested:
		return layerWireDirect, nil
	case sameBody && !sameID && !dirty.nested:
		return layerWireRetag, nil
	default:
		return layerWireRewrite, nil
	}
}

type layerFamilyProfileKey struct {
	Layer int
	Key   semantic.SemanticKey
}

func joinLayerWireFamilyActions(model *layerWireModel, familyIndex map[semantic.SemanticKey]int) error {
	actions := make(map[layerFamilyProfileKey]*layerFamilyProfileAction, len(model.Families)*len(model.Profiles))
	for familyIndexValue := range model.Families {
		family := &model.Families[familyIndexValue]
		for actionIndex := range family.Profiles {
			action := &family.Profiles[actionIndex]
			key := layerFamilyProfileKey{Layer: action.Layer, Key: family.Key}
			if _, duplicate := actions[key]; duplicate {
				return fmt.Errorf("gen: layer wire model repeats semantic action for profile %d family %s", action.Layer, family.Key)
			}
			actions[key] = action
		}
	}
	for wireIndex := range model.Wires {
		wire := &model.Wires[wireIndex]
		index, ok := familyIndex[wire.Key]
		if !ok {
			return fmt.Errorf("gen: layer wire plan %#08x references missing family %s", wire.WireID, wire.Key)
		}
		for actionIndex := range wire.Profiles {
			action := &wire.Profiles[actionIndex]
			if action.Kind == layerWireReject {
				continue
			}
			familyAction := actions[layerFamilyProfileKey{Layer: action.Layer, Key: wire.Key}]
			if familyAction == nil || familyAction.WireIndex != wireIndex || familyAction.BodyVariant != action.BodyVariant {
				return fmt.Errorf("gen: profile %d wire %#08x does not match its semantic action", action.Layer, wire.WireID)
			}
			action.FamilyIndex = index
			action.Kind = familyAction.Kind
			action.Conversion = familyAction.Conversion
		}
	}
	return nil
}

func buildLayerClassPlans(
	schemaSet *SchemaSet,
	bindings *layerBindingIndex,
	familyIndex map[semantic.SemanticKey]int,
	model *layerWireModel,
) error {
	classSet := make(map[string]struct{})
	for _, layer := range model.Profiles {
		for qname := range schemaSet.Schemas[layer].ConstructorsByClass {
			classSet[qname] = struct{}{}
		}
	}
	classNames := make([]string, 0, len(classSet))
	for qname := range classSet {
		classNames = append(classNames, qname)
	}
	sort.Strings(classNames)

	model.Classes = make([]layerClassPlan, 0, len(classNames))
	for _, qname := range classNames {
		class := layerClassPlan{
			QName:     qname,
			Canonical: bindings.class(qname),
			Profiles:  make([]layerClassProfilePlan, 0, len(model.Profiles)),
		}
		for _, layer := range model.Profiles {
			schema := schemaSet.Schemas[layer]
			keys := append([]semantic.SemanticKey(nil), schema.ConstructorsByClass[qname]...)
			sort.Slice(keys, func(i, j int) bool {
				left := schema.ByKey[keys[i]]
				right := schema.ByKey[keys[j]]
				if left.WireID != right.WireID {
					return left.WireID < right.WireID
				}
				return keys[i].QName < keys[j].QName
			})
			profile := layerClassProfilePlan{Layer: layer, Constructors: make([]layerClassConstructorPlan, 0, len(keys))}
			seen := make(map[semantic.SemanticKey]struct{}, len(keys))
			for _, key := range keys {
				if key.Category != semantic.CategoryType {
					return fmt.Errorf("gen: layer %d class %q contains non-type constructor %s", layer, qname, key)
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("gen: layer %d class %q repeats constructor %s", layer, qname, key)
				}
				seen[key] = struct{}{}
				definition := schema.ByKey[key]
				if definition == nil {
					return fmt.Errorf("gen: layer %d class %q constructor %s is absent from schema", layer, qname, key)
				}
				index, ok := familyIndex[key]
				if !ok {
					return fmt.Errorf("gen: layer %d class %q constructor %s has no family plan", layer, qname, key)
				}
				action := model.Families[index].profile(layer)
				if action == nil || action.WireIndex < 0 || action.BodyVariant < 0 {
					return fmt.Errorf("gen: layer %d class %q constructor %s has no accepted wire action", layer, qname, key)
				}
				profile.Constructors = append(profile.Constructors, layerClassConstructorPlan{
					Key:         key,
					WireID:      definition.WireID,
					FamilyIndex: index,
					WireIndex:   action.WireIndex,
					BodyVariant: action.BodyVariant,
					Kind:        action.Kind,
				})
			}
			class.Profiles = append(class.Profiles, profile)
		}
		model.Classes = append(model.Classes, class)
	}
	return nil
}

func validateLayerWireCompleteness(schemaSet *SchemaSet, model *layerWireModel) error {
	if len(model.Wires) != len(schemaSet.WireCodecs) {
		return fmt.Errorf("gen: layer wire plan count=%d, want=%d", len(model.Wires), len(schemaSet.WireCodecs))
	}
	if len(model.Families) != len(schemaSet.Families) {
		return fmt.Errorf("gen: layer family plan count=%d, want=%d", len(model.Families), len(schemaSet.Families))
	}

	wirePairs := make(map[layerWireProfileKey]layerWireActionKind, len(model.Wires)*len(model.Profiles))
	for wireIndex := range model.Wires {
		wire := &model.Wires[wireIndex]
		if wireIndex > 0 && model.Wires[wireIndex-1].WireID >= wire.WireID {
			return fmt.Errorf("gen: layer wire plans are not uniquely sorted at %#08x", wire.WireID)
		}
		for _, action := range wire.Profiles {
			key := layerWireProfileKey{Layer: action.Layer, WireID: wire.WireID}
			if _, duplicate := wirePairs[key]; duplicate {
				return fmt.Errorf("gen: duplicate accepted wire action for profile %d ID %#08x", action.Layer, wire.WireID)
			}
			wirePairs[key] = action.Kind
			if action.Kind == layerWireReject {
				if action.Variant != nil || action.Conversion != nil || action.FamilyIndex >= 0 || action.BodyVariant >= 0 {
					return fmt.Errorf("gen: rejected wire action for profile %d ID %#08x retains an accepted target", action.Layer, wire.WireID)
				}
				continue
			}
			if action.Variant == nil || action.Conversion == nil || action.FamilyIndex < 0 || action.BodyVariant < 0 || action.BodyVariant >= len(wire.BodyVariants) {
				return fmt.Errorf("gen: incomplete accepted wire action for profile %d ID %#08x", action.Layer, wire.WireID)
			}
			if action.Conversion.Profile != action.Variant || action.Conversion.Key != wire.Key {
				return fmt.Errorf("gen: profile %d wire %#08x conversion identity mismatch", action.Layer, wire.WireID)
			}
		}
	}
	for _, layer := range model.Profiles {
		for _, wire := range model.Wires {
			kind, ok := wirePairs[layerWireProfileKey{Layer: layer, WireID: wire.WireID}]
			if !ok {
				return fmt.Errorf("gen: profile %d wire %#08x has no explicit action", layer, wire.WireID)
			}
			definition := schemaSet.ByWire[layer][wire.WireID]
			if definition == nil && kind != layerWireReject {
				return fmt.Errorf("gen: profile %d wire %#08x is absent but action is %s", layer, wire.WireID, kind)
			}
			if definition != nil && kind == layerWireReject {
				return fmt.Errorf("gen: profile %d wire %#08x (%s) is present but rejected", layer, wire.WireID, definition.Key)
			}
		}
		for wireID, definition := range schemaSet.ByWire[layer] {
			if _, ok := wirePairs[layerWireProfileKey{Layer: layer, WireID: wireID}]; !ok {
				return fmt.Errorf("gen: profile %d wire %#08x (%s) has no accepted action", layer, wireID, definition.Key)
			}
		}
	}
	if got, want := len(wirePairs), len(model.Wires)*len(model.Profiles); got != want {
		return fmt.Errorf("gen: wire action pairs=%d, want=%d", got, want)
	}

	semanticPairs := make(map[layerFamilyProfileKey]struct{}, len(model.Families)*len(model.Profiles))
	for familyIndex := range model.Families {
		family := &model.Families[familyIndex]
		if familyIndex > 0 {
			previous := model.Families[familyIndex-1].Key
			if previous.Category > family.Key.Category ||
				(previous.Category == family.Key.Category && previous.QName >= family.Key.QName) {
				return fmt.Errorf("gen: layer family plans are not uniquely sorted at %s", family.Key)
			}
		}
		if len(family.Profiles) != len(model.Profiles) {
			return fmt.Errorf("gen: family %s action count=%d, want=%d", family.Key, len(family.Profiles), len(model.Profiles))
		}
		for _, action := range family.Profiles {
			key := layerFamilyProfileKey{Layer: action.Layer, Key: family.Key}
			if _, duplicate := semanticPairs[key]; duplicate {
				return fmt.Errorf("gen: duplicate semantic action for profile %d family %s", action.Layer, family.Key)
			}
			semanticPairs[key] = struct{}{}
			hasWire := action.WireIndex >= 0 && action.BodyVariant >= 0
			switch action.Kind {
			case layerWireDirect, layerWireRetag, layerWireRewrite, layerWirePolicy, layerWireProfileOnly:
				if !hasWire {
					return fmt.Errorf("gen: profile %d family %s action %s has no wire", action.Layer, family.Key, action.Kind)
				}
			case layerWireUnavailable, layerWireAbsent:
				if hasWire {
					return fmt.Errorf("gen: profile %d family %s action %s unexpectedly has a wire", action.Layer, family.Key, action.Kind)
				}
			default:
				return fmt.Errorf("gen: profile %d family %s has unknown action %s", action.Layer, family.Key, action.Kind)
			}
		}
	}
	if got, want := len(semanticPairs), len(model.Families)*len(model.Profiles); got != want {
		return fmt.Errorf("gen: semantic action pairs=%d, want=%d", got, want)
	}
	return nil
}
