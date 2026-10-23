package gen

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"go/token"
	"hash"
	"sort"
	"strings"

	"github.com/iamxvbaba/td/gen/semantic"
)

// LayerObligationKind is one non-mechanical cross-layer decision exposed by
// schema generation. Values are stable policy identifiers.
type LayerObligationKind string

const (
	LayerObligationAlias            LayerObligationKind = "alias"
	LayerObligationRequired         LayerObligationKind = "required"
	LayerObligationIncompatible     LayerObligationKind = "incompatible"
	LayerObligationResult           LayerObligationKind = "result"
	LayerObligationOldOnly          LayerObligationKind = "old-only"
	LayerObligationNewOnly          LayerObligationKind = "new-only"
	LayerObligationAtomicFlagGroup  LayerObligationKind = "atomic-flag-group"
	LayerObligationUpdateProjection LayerObligationKind = "update-projection"
	LayerObligationFieldProjection  LayerObligationKind = "field-projection"
	LayerObligationFieldReplacement LayerObligationKind = "field-replacement"
	LayerObligationDiscard          LayerObligationKind = "discard"
	LayerObligationPrivate          LayerObligationKind = "private"
)

// LayerObligationDirection identifies the conversion path requiring policy.
type LayerObligationDirection string

const (
	LayerDirectionCanonicalToProfile LayerObligationDirection = "canonical-to-profile"
	LayerDirectionProfileToCanonical LayerObligationDirection = "profile-to-canonical"
	LayerDirectionBoth               LayerObligationDirection = "both"
	LayerDirectionProfile            LayerObligationDirection = "profile"
)

// LayerObligationKey is copied verbatim into policy. It is a domain-separated
// digest of all machine-relevant coordinates, not of diagnostic prose.
type LayerObligationKey string

// LayerResolutionAction is a generated behavior, not free-form approval text.
type LayerResolutionAction string

const (
	LayerResolveUnavailable     LayerResolutionAction = "unavailable"
	LayerResolveReject          LayerResolutionAction = "reject"
	LayerResolveRejectIfPresent LayerResolutionAction = "reject-if-present"
	LayerResolveDrop            LayerResolutionAction = "drop"
	LayerResolveAlias           LayerResolutionAction = "alias"
	LayerResolveDefault         LayerResolutionAction = "default"
	LayerResolveAdapter         LayerResolutionAction = "adapter"
	LayerResolveProject         LayerResolutionAction = "project"
	LayerResolveAllow           LayerResolutionAction = "allow"
)

// LayerObligationResolution is the machine-checked action for one exact key.
// Hook is required for generated adapter/project/alias calls. Note is human
// context only and never resolves an obligation by itself.
type LayerObligationResolution struct {
	Action LayerResolutionAction `json:"action"`
	Hook   string                `json:"hook,omitempty"`
	Target string                `json:"target,omitempty"`
	Note   string                `json:"note,omitempty"`
}

func (r LayerObligationResolution) resolved() bool { return r.Action != "" }

// LayerObligation describes one explicit compatibility decision.
type LayerObligation struct {
	Key       LayerObligationKey
	Kind      LayerObligationKind
	Layer     int
	Direction LayerObligationDirection

	Semantic      semantic.SemanticKey
	OtherSemantic semantic.SemanticKey
	WireID        uint32
	OtherWireID   uint32
	Field         string
	OtherField    string
	FlagWord      string
	FlagBit       uint8
	Fields        []string
	SourceType    string
	TargetType    string
	SourceShape   semantic.ShapeDigest
	TargetShape   semantic.ShapeDigest

	// Resolution is copied from an exact matching policy entry. A zero Action
	// means the obligation is unresolved.
	Resolution LayerObligationResolution
}

// LayerObligationPolicyEntry resolves exactly one generated key.
type LayerObligationPolicyEntry struct {
	Key        LayerObligationKey        `json:"key"`
	Resolution LayerObligationResolution `json:"resolution"`
}

// LayerObligationPolicy is deliberately a slice: duplicate keys are rejected
// instead of being silently overwritten by a map literal or decoder.
type LayerObligationPolicy struct {
	Entries []LayerObligationPolicyEntry
}

// LayerObligationReport is deterministic for a SchemaSet and policy.
type LayerObligationReport struct {
	Obligations []LayerObligation
}

// Unresolved returns a detached list of obligations with no policy decision.
func (r LayerObligationReport) Unresolved() []LayerObligation {
	result := make([]LayerObligation, 0, len(r.Obligations))
	for _, obligation := range r.Obligations {
		if !obligation.Resolution.resolved() {
			result = append(result, obligation)
		}
	}
	return result
}

// LayerObligations analyzes the normalized SchemaSet attached to Generator.
func (g *Generator) LayerObligations(policy LayerObligationPolicy) (LayerObligationReport, error) {
	if g == nil || g.schemaSet == nil {
		return LayerObligationReport{}, fmt.Errorf("gen: layer obligations require a schema-set generator")
	}
	return AnalyzeLayerObligations(g.schemaSet, policy)
}

// AnalyzeLayerObligations discovers non-mechanical compatibility decisions and
// applies policy by exact generated key. Unknown policy keys are stale errors.
func AnalyzeLayerObligations(schemaSet *SchemaSet, policy LayerObligationPolicy) (LayerObligationReport, error) {
	plan, err := AnalyzeLayerConversions(schemaSet, policy)
	if err != nil {
		return LayerObligationReport{}, err
	}
	return plan.Report, nil
}

func privateProfile(canonical, profile *semantic.SchemaModel) bool {
	return profile.Source.Repository != canonical.Source.Repository || profile.Source.Path != canonical.Source.Path
}

func isUpdateDefinition(definition *semantic.Definition) bool {
	return definition.Key.Category == semantic.CategoryType &&
		definition.Result.Kind == semantic.TypeNamed &&
		!definition.Result.Bare && definition.Result.QName == "Update"
}

func makeDefinitionObligation(kind LayerObligationKind, layer int, direction LayerObligationDirection, definition *semantic.Definition) LayerObligation {
	return makeLayerObligation(LayerObligation{
		Kind:        kind,
		Layer:       layer,
		Direction:   direction,
		Semantic:    definition.Key,
		WireID:      definition.WireID,
		SourceType:  definition.Result.String(),
		SourceShape: definition.SemanticShape,
	})
}

type fieldAlias struct {
	canonical string
	profile   string
}

// findFieldReplacements recognizes only an unambiguous one-to-one reuse of
// the same flags slot by otherwise unmatched fields. Unlike a rename alias,
// the wire types may differ and therefore always require one bidirectional
// adapter or rejection decision.
func findFieldReplacements(canonical, profile *semantic.Definition, aliases []fieldAlias) []fieldAlias {
	canonicalFields := valueFieldMap(canonical)
	profileFields := valueFieldMap(profile)
	aliasedCanonical := make(map[string]struct{}, len(aliases))
	aliasedProfile := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		aliasedCanonical[alias.canonical] = struct{}{}
		aliasedProfile[alias.profile] = struct{}{}
	}
	type candidates struct {
		canonical []string
		profile   []string
	}
	groups := make(map[flagGroupKey]*candidates)
	for _, field := range canonical.Fields {
		if field.Kind != semantic.FieldValue || field.Condition == nil {
			continue
		}
		if _, exists := profileFields[field.Name]; exists {
			continue
		}
		if _, aliased := aliasedCanonical[field.Name]; aliased {
			continue
		}
		key := flagGroupKey{word: field.Condition.Word, bit: field.Condition.Bit}
		group := groups[key]
		if group == nil {
			group = new(candidates)
			groups[key] = group
		}
		group.canonical = append(group.canonical, field.Name)
	}
	for _, field := range profile.Fields {
		if field.Kind != semantic.FieldValue || field.Condition == nil {
			continue
		}
		if _, exists := canonicalFields[field.Name]; exists {
			continue
		}
		if _, aliased := aliasedProfile[field.Name]; aliased {
			continue
		}
		key := flagGroupKey{word: field.Condition.Word, bit: field.Condition.Bit}
		group := groups[key]
		if group == nil {
			group = new(candidates)
			groups[key] = group
		}
		group.profile = append(group.profile, field.Name)
	}

	var result []fieldAlias
	for _, group := range groups {
		if len(group.canonical) == 1 && len(group.profile) == 1 {
			result = append(result, fieldAlias{canonical: group.canonical[0], profile: group.profile[0]})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].canonical != result[j].canonical {
			return result[i].canonical < result[j].canonical
		}
		return result[i].profile < result[j].profile
	})
	return result
}

func compareLayerDefinitions(layer int, canonical, profile *semantic.Definition) []LayerObligation {
	aliases := findFieldAliases(canonical, profile)
	replacements := findFieldReplacements(canonical, profile, aliases)
	result := make([]LayerObligation, 0, len(aliases)+len(replacements)+4)
	for _, alias := range aliases {
		result = append(result, makeLayerObligation(LayerObligation{
			Kind:        LayerObligationAlias,
			Layer:       layer,
			Direction:   LayerDirectionBoth,
			Semantic:    canonical.Key,
			WireID:      canonical.WireID,
			OtherWireID: profile.WireID,
			Field:       alias.canonical,
			OtherField:  alias.profile,
			SourceType:  fieldTypeString(findValueField(canonical, alias.canonical)),
			TargetType:  fieldTypeString(findValueField(profile, alias.profile)),
			SourceShape: canonical.SemanticShape,
			TargetShape: profile.SemanticShape,
		}))
	}
	for _, replacement := range replacements {
		result = append(result, makeLayerObligation(LayerObligation{
			Kind:          LayerObligationFieldReplacement,
			Layer:         layer,
			Direction:     LayerDirectionBoth,
			Semantic:      canonical.Key,
			OtherSemantic: profile.Key,
			WireID:        canonical.WireID,
			OtherWireID:   profile.WireID,
			Field:         replacement.canonical,
			OtherField:    replacement.profile,
			SourceType:    fieldTypeString(findValueField(canonical, replacement.canonical)),
			TargetType:    fieldTypeString(findValueField(profile, replacement.profile)),
			SourceShape:   canonical.SemanticShape,
			TargetShape:   profile.SemanticShape,
		}))
	}
	result = append(result, compareDirection(
		layer, canonical, profile, LayerDirectionCanonicalToProfile, aliasMap(aliases, true), aliasMap(replacements, true),
	)...)
	result = append(result, compareDirection(
		layer, profile, canonical, LayerDirectionProfileToCanonical, aliasMap(aliases, false), aliasMap(replacements, false),
	)...)
	result = append(result, projectedCanonicalFields(
		layer, canonical, profile, aliasMap(aliases, true), aliasMap(replacements, true),
	)...)
	result = append(result, discardedProfileFields(
		layer, profile, canonical, aliasMap(aliases, false), aliasMap(replacements, false),
	)...)
	if canonical.Key.Category == semantic.CategoryFunction && !canonical.Result.Equal(profile.Result) {
		result = append(result, makeLayerObligation(LayerObligation{
			Kind:        LayerObligationResult,
			Layer:       layer,
			Direction:   LayerDirectionCanonicalToProfile,
			Semantic:    canonical.Key,
			WireID:      canonical.WireID,
			OtherWireID: profile.WireID,
			SourceType:  canonical.Result.String(),
			TargetType:  profile.Result.String(),
			SourceShape: canonical.SemanticShape,
			TargetShape: profile.SemanticShape,
		}))
		result = append(result, makeLayerObligation(LayerObligation{
			Kind:        LayerObligationResult,
			Layer:       layer,
			Direction:   LayerDirectionProfileToCanonical,
			Semantic:    canonical.Key,
			WireID:      profile.WireID,
			OtherWireID: canonical.WireID,
			SourceType:  profile.Result.String(),
			TargetType:  canonical.Result.String(),
			SourceShape: profile.SemanticShape,
			TargetShape: canonical.SemanticShape,
		}))
	}
	return result
}

// projectedCanonicalFields exposes canonical data which the exact target
// profile has no field for. The exact encoder mechanically omits those fields:
// they have no flag bit or body slot in the target schema. Policy can still
// replace this default with a whole-value projection when omission would alter
// semantics (for example, a privacy marker).
func projectedCanonicalFields(layer int, canonical, profile *semantic.Definition, aliases, replacements map[string]string) []LayerObligation {
	profileFields := valueFieldMap(profile)
	var result []LayerObligation
	for i := range canonical.Fields {
		field := &canonical.Fields[i]
		if field.Kind != semantic.FieldValue {
			continue
		}
		profileName := field.Name
		if alias, ok := aliases[field.Name]; ok {
			profileName = alias
		}
		if replacement, ok := replacements[field.Name]; ok {
			profileName = replacement
		}
		if _, exists := profileFields[profileName]; exists {
			continue
		}
		obligation := makeFieldObligation(
			LayerObligationFieldProjection,
			layer,
			LayerDirectionCanonicalToProfile,
			canonical,
			profile,
			field,
			nil,
		)
		obligation.Resolution = LayerObligationResolution{Action: LayerResolveDrop}
		result = append(result, obligation)
	}
	return result
}

// discardedProfileFields exposes historical input data which has no
// canonical field. Losing it while admitting a request or decoding a value
// would silently change semantics, so it always requires an explicit drop,
// adapter, or rejection policy.
func discardedProfileFields(layer int, profile, canonical *semantic.Definition, aliases, replacements map[string]string) []LayerObligation {
	canonicalFields := valueFieldMap(canonical)
	var result []LayerObligation
	for i := range profile.Fields {
		field := &profile.Fields[i]
		if field.Kind != semantic.FieldValue {
			continue
		}
		canonicalName := field.Name
		if alias, ok := aliases[field.Name]; ok {
			canonicalName = alias
		}
		if replacement, ok := replacements[field.Name]; ok {
			canonicalName = replacement
		}
		if _, exists := canonicalFields[canonicalName]; exists {
			continue
		}
		result = append(result, makeFieldObligation(
			LayerObligationDiscard,
			layer,
			LayerDirectionProfileToCanonical,
			profile,
			canonical,
			field,
			nil,
		))
	}
	return result
}

func compareDirection(layer int, source, target *semantic.Definition, direction LayerObligationDirection, aliases, replacements map[string]string) []LayerObligation {
	sourceFields := valueFieldMap(source)
	result := make([]LayerObligation, 0)
	for _, targetField := range target.Fields {
		if targetField.Kind != semantic.FieldValue {
			continue
		}
		sourceName := targetField.Name
		paired := false
		for candidate, mapped := range aliases {
			if mapped == targetField.Name {
				sourceName = candidate
				break
			}
		}
		for candidate, mapped := range replacements {
			if mapped == targetField.Name {
				sourceName = candidate
				paired = true
				break
			}
		}
		sourceField, exists := sourceFields[sourceName]
		if !exists {
			if targetField.Condition == nil {
				result = append(result, makeFieldObligation(
					LayerObligationRequired, layer, direction, source, target, nil, &targetField,
				))
			}
			continue
		}
		if paired {
			continue
		}
		if incompatibleFields(sourceField, targetField) {
			result = append(result, makeFieldObligation(
				LayerObligationIncompatible, layer, direction, source, target, &sourceField, &targetField,
			))
			continue
		}
		if sourceField.Condition != nil && targetField.Condition == nil {
			result = append(result, makeFieldObligation(
				LayerObligationRequired, layer, direction, source, target, &sourceField, &targetField,
			))
		}
	}
	combined := make(map[string]string, len(aliases)+len(replacements))
	for sourceName, targetName := range aliases {
		combined[sourceName] = targetName
	}
	for sourceName, targetName := range replacements {
		combined[sourceName] = targetName
	}
	result = append(result, atomicFlagGroupObligations(layer, direction, source, target, combined)...)
	return result
}

func incompatibleFields(source, target semantic.FieldShape) bool {
	if !source.Type.Equal(target.Type) {
		return true
	}
	sourcePresence := source.Condition != nil && source.Condition.PresenceOnly
	targetPresence := target.Condition != nil && target.Condition.PresenceOnly
	return sourcePresence != targetPresence
}

func makeFieldObligation(kind LayerObligationKind, layer int, direction LayerObligationDirection, source, target *semantic.Definition, sourceField, targetField *semantic.FieldShape) LayerObligation {
	obligation := LayerObligation{
		Kind:          kind,
		Layer:         layer,
		Direction:     direction,
		Semantic:      source.Key,
		OtherSemantic: target.Key,
		WireID:        source.WireID,
		OtherWireID:   target.WireID,
		SourceShape:   source.SemanticShape,
		TargetShape:   target.SemanticShape,
	}
	if sourceField != nil {
		obligation.Field = sourceField.Name
		obligation.SourceType = fieldTypeString(sourceField)
	}
	if targetField != nil {
		obligation.OtherField = targetField.Name
		obligation.TargetType = fieldTypeString(targetField)
	}
	return makeLayerObligation(obligation)
}

func findFieldAliases(canonical, profile *semantic.Definition) []fieldAlias {
	canonicalFields := valueFieldMap(canonical)
	profileFields := valueFieldMap(profile)
	var canonicalOnly, profileOnly []semantic.FieldShape
	for _, field := range canonical.Fields {
		if field.Kind == semantic.FieldValue {
			if _, exists := profileFields[field.Name]; !exists {
				canonicalOnly = append(canonicalOnly, field)
			}
		}
	}
	for _, field := range profile.Fields {
		if field.Kind == semantic.FieldValue {
			if _, exists := canonicalFields[field.Name]; !exists {
				profileOnly = append(profileOnly, field)
			}
		}
	}

	var result []fieldAlias
	usedProfile := make(map[string]struct{})
	for _, canonicalField := range canonicalOnly {
		var match *semantic.FieldShape
		for i := range profileOnly {
			profileField := &profileOnly[i]
			if _, used := usedProfile[profileField.Name]; used || !aliasCompatible(canonicalField, *profileField) {
				continue
			}
			if match != nil {
				match = nil // Ambiguous candidates must not be guessed.
				break
			}
			match = profileField
		}
		if match != nil {
			usedProfile[match.Name] = struct{}{}
			result = append(result, fieldAlias{canonical: canonicalField.Name, profile: match.Name})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].canonical != result[j].canonical {
			return result[i].canonical < result[j].canonical
		}
		return result[i].profile < result[j].profile
	})
	return result
}

func aliasCompatible(canonical, profile semantic.FieldShape) bool {
	if canonical.Ordinal != profile.Ordinal || !canonical.Type.Equal(profile.Type) {
		return false
	}
	if canonical.Condition == nil || profile.Condition == nil {
		return canonical.Condition == nil && profile.Condition == nil
	}
	return canonical.Condition.PresenceOnly == profile.Condition.PresenceOnly
}

func aliasMap(aliases []fieldAlias, canonicalToProfile bool) map[string]string {
	result := make(map[string]string, len(aliases))
	for _, alias := range aliases {
		if canonicalToProfile {
			result[alias.canonical] = alias.profile
		} else {
			result[alias.profile] = alias.canonical
		}
	}
	return result
}

type flagGroupKey struct {
	word string
	bit  uint8
}

func atomicFlagGroupObligations(layer int, direction LayerObligationDirection, source, target *semantic.Definition, aliases map[string]string) []LayerObligation {
	groups := make(map[flagGroupKey][]semantic.FieldShape)
	for _, field := range target.Fields {
		if field.Kind == semantic.FieldValue && field.Condition != nil {
			key := flagGroupKey{word: field.Condition.Word, bit: field.Condition.Bit}
			groups[key] = append(groups[key], field)
		}
	}
	keys := make([]flagGroupKey, 0, len(groups))
	for key, fields := range groups {
		if len(fields) > 1 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].word != keys[j].word {
			return keys[i].word < keys[j].word
		}
		return keys[i].bit < keys[j].bit
	})

	sourceFields := valueFieldMap(source)
	var result []LayerObligation
	for _, key := range keys {
		fields := groups[key]
		var expected string
		compatible := true
		names := make([]string, 0, len(fields))
		for i, field := range fields {
			names = append(names, field.Name)
			sourceName := field.Name
			for candidate, mapped := range aliases {
				if mapped == field.Name {
					sourceName = candidate
					break
				}
			}
			presence := fieldPresence(sourceFields[sourceName])
			if i == 0 {
				expected = presence
			} else if presence != expected {
				compatible = false
			}
		}
		if compatible {
			continue
		}
		sort.Strings(names)
		result = append(result, makeLayerObligation(LayerObligation{
			Kind:          LayerObligationAtomicFlagGroup,
			Layer:         layer,
			Direction:     direction,
			Semantic:      source.Key,
			OtherSemantic: target.Key,
			WireID:        source.WireID,
			OtherWireID:   target.WireID,
			FlagWord:      key.word,
			FlagBit:       key.bit,
			Fields:        names,
			SourceShape:   source.SemanticShape,
			TargetShape:   target.SemanticShape,
		}))
	}
	return result
}

func fieldPresence(field semantic.FieldShape) string {
	if field.Name == "" {
		return "absent"
	}
	if field.Condition == nil {
		return "required"
	}
	return fmt.Sprintf("conditional:%s:%d", field.Condition.Word, field.Condition.Bit)
}

func valueFieldMap(definition *semantic.Definition) map[string]semantic.FieldShape {
	result := make(map[string]semantic.FieldShape, len(definition.Fields))
	for _, field := range definition.Fields {
		if field.Kind == semantic.FieldValue {
			result[field.Name] = field
		}
	}
	return result
}

func findValueField(definition *semantic.Definition, name string) *semantic.FieldShape {
	for i := range definition.Fields {
		if definition.Fields[i].Kind == semantic.FieldValue && definition.Fields[i].Name == name {
			return &definition.Fields[i]
		}
	}
	return nil
}

func fieldTypeString(field *semantic.FieldShape) string {
	if field == nil {
		return ""
	}
	return field.Type.String()
}

func applyLayerObligationPolicy(obligations []LayerObligation, policy LayerObligationPolicy) (LayerObligationReport, error) {
	index := make(map[LayerObligationKey]int, len(obligations))
	for i := range obligations {
		index[obligations[i].Key] = i
	}
	seen := make(map[LayerObligationKey]struct{}, len(policy.Entries))
	for _, entry := range policy.Entries {
		if _, duplicate := seen[entry.Key]; duplicate {
			return LayerObligationReport{}, fmt.Errorf("gen: E_DUPLICATE_LAYER_POLICY: key %q is repeated", entry.Key)
		}
		seen[entry.Key] = struct{}{}
		obligationIndex, ok := index[entry.Key]
		if !ok {
			return LayerObligationReport{}, fmt.Errorf("gen: E_STALE_LAYER_POLICY: key %q does not match a generated obligation", entry.Key)
		}
		if err := validateLayerObligationResolution(obligations[obligationIndex].Kind, entry.Resolution); err != nil {
			return LayerObligationReport{}, fmt.Errorf("gen: E_INVALID_LAYER_POLICY: key %q: %w", entry.Key, err)
		}
		if obligations[obligationIndex].Kind == LayerObligationOldOnly &&
			(entry.Resolution.Action == LayerResolveAlias || entry.Resolution.Action == LayerResolveAdapter) {
			target, _ := parseLayerPolicySemanticTarget(strings.TrimSpace(entry.Resolution.Target))
			if target.Category != obligations[obligationIndex].Semantic.Category {
				return LayerObligationReport{}, fmt.Errorf(
					"gen: E_INVALID_LAYER_POLICY: key %q: old-only %s target %s has another category",
					entry.Key, obligations[obligationIndex].Semantic, target,
				)
			}
		}
		obligations[obligationIndex].Resolution = entry.Resolution
	}
	return LayerObligationReport{Obligations: obligations}, nil
}

func validateLayerObligationResolution(kind LayerObligationKind, resolution LayerObligationResolution) error {
	allowed := false
	switch kind {
	case LayerObligationAlias:
		allowed = resolution.Action == LayerResolveAlias || resolution.Action == LayerResolveAdapter || resolution.Action == LayerResolveReject
	case LayerObligationRequired:
		allowed = resolution.Action == LayerResolveDefault || resolution.Action == LayerResolveAdapter || resolution.Action == LayerResolveReject
	case LayerObligationIncompatible, LayerObligationAtomicFlagGroup, LayerObligationResult:
		allowed = resolution.Action == LayerResolveAdapter || resolution.Action == LayerResolveReject
	case LayerObligationOldOnly:
		allowed = resolution.Action == LayerResolveAlias || resolution.Action == LayerResolveAdapter || resolution.Action == LayerResolveReject
	case LayerObligationNewOnly:
		allowed = resolution.Action == LayerResolveUnavailable || resolution.Action == LayerResolveDrop ||
			resolution.Action == LayerResolveProject || resolution.Action == LayerResolveReject
	case LayerObligationUpdateProjection:
		allowed = resolution.Action == LayerResolveDrop || resolution.Action == LayerResolveProject || resolution.Action == LayerResolveReject
	case LayerObligationFieldProjection:
		allowed = resolution.Action == LayerResolveDrop || resolution.Action == LayerResolveAdapter ||
			resolution.Action == LayerResolveProject || resolution.Action == LayerResolveRejectIfPresent
	case LayerObligationFieldReplacement:
		allowed = resolution.Action == LayerResolveAdapter || resolution.Action == LayerResolveReject
	case LayerObligationDiscard:
		allowed = resolution.Action == LayerResolveDrop || resolution.Action == LayerResolveAdapter || resolution.Action == LayerResolveReject
	case LayerObligationPrivate:
		allowed = resolution.Action == LayerResolveAllow || resolution.Action == LayerResolveReject
	}
	if !allowed {
		return fmt.Errorf("action %q is not valid for %s", resolution.Action, kind)
	}
	requiresHook := resolution.Action == LayerResolveAdapter || resolution.Action == LayerResolveProject || resolution.Action == LayerResolveAlias
	hook := strings.TrimSpace(resolution.Hook)
	if requiresHook {
		if !token.IsIdentifier(hook) || token.Lookup(hook).IsKeyword() {
			return fmt.Errorf("action %q requires a non-keyword Go hook identifier", resolution.Action)
		}
	} else if hook != "" {
		return fmt.Errorf("action %q does not accept hook %q", resolution.Action, hook)
	}
	target := strings.TrimSpace(resolution.Target)
	if kind == LayerObligationOldOnly && (resolution.Action == LayerResolveAlias || resolution.Action == LayerResolveAdapter) {
		_, err := parseLayerPolicySemanticTarget(target)
		if err != nil {
			return err
		}
	} else if target != "" {
		return fmt.Errorf("action %q for %s does not accept target %q", resolution.Action, kind, target)
	}
	return nil
}

func parseLayerPolicySemanticTarget(value string) (semantic.SemanticKey, error) {
	category, qname, ok := strings.Cut(value, ":")
	if !ok || qname == "" || strings.TrimSpace(qname) != qname {
		return semantic.SemanticKey{}, fmt.Errorf("invalid semantic target %q", value)
	}
	key := semantic.SemanticKey{QName: qname}
	switch category {
	case "function":
		key.Category = semantic.CategoryFunction
	case "type":
		key.Category = semantic.CategoryType
	default:
		return semantic.SemanticKey{}, fmt.Errorf("invalid semantic target category %q", category)
	}
	return key, nil
}

func makeLayerObligation(obligation LayerObligation) LayerObligation {
	obligation.Key = obligationDigestKey(obligation)
	return obligation
}

func obligationDigestKey(obligation LayerObligation) LayerObligationKey {
	w := &obligationHashWriter{h: sha256.New()}
	w.string("gotd.tl.layer-obligation.v1")
	w.string(string(obligation.Kind))
	w.integer(obligation.Layer)
	w.string(string(obligation.Direction))
	w.string(obligation.Semantic.String())
	w.string(obligation.OtherSemantic.String())
	w.uint32(obligation.WireID)
	w.uint32(obligation.OtherWireID)
	w.string(obligation.Field)
	w.string(obligation.OtherField)
	w.string(obligation.FlagWord)
	w.uint32(uint32(obligation.FlagBit))
	w.integer(len(obligation.Fields))
	for _, field := range obligation.Fields {
		w.string(field)
	}
	w.string(obligation.SourceType)
	w.string(obligation.TargetType)
	w.string(obligation.SourceShape.String())
	w.string(obligation.TargetShape.String())
	return LayerObligationKey(fmt.Sprintf("layer-obligation/v1/%s/%s", obligation.Kind, hex.EncodeToString(w.h.Sum(nil))))
}

type obligationHashWriter struct {
	h hash.Hash
}

func (w *obligationHashWriter) uint64(value uint64) {
	var data [8]byte
	binary.LittleEndian.PutUint64(data[:], value)
	_, _ = w.h.Write(data[:])
}

func (w *obligationHashWriter) uint32(value uint32) {
	var data [4]byte
	binary.LittleEndian.PutUint32(data[:], value)
	_, _ = w.h.Write(data[:])
}

func (w *obligationHashWriter) integer(value int) {
	w.uint64(uint64(value))
}

func (w *obligationHashWriter) string(value string) {
	w.uint64(uint64(len(value)))
	_, _ = w.h.Write([]byte(value))
}
