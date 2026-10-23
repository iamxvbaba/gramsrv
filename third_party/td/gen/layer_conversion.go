package gen

import (
	"fmt"
	"sort"

	"github.com/iamxvbaba/td/gen/semantic"
)

// LayerAvailability is the exact relationship between the canonical schema
// and one concrete wire profile. Availability is intentionally independent
// from body and RPC result conversion.
type LayerAvailability uint8

const (
	LayerAvailabilityPresent LayerAvailability = iota
	LayerAvailabilityCanonicalOnly
	LayerAvailabilityProfileOnly
	LayerAvailabilityAbsent
)

func (a LayerAvailability) String() string {
	switch a {
	case LayerAvailabilityPresent:
		return "present"
	case LayerAvailabilityCanonicalOnly:
		return "canonical-only"
	case LayerAvailabilityProfileOnly:
		return "profile-only"
	case LayerAvailabilityAbsent:
		return "absent"
	default:
		return fmt.Sprintf("LayerAvailability(%d)", a)
	}
}

// LayerFieldMapping joins a target profile field with its canonical field.
// A -1 ordinal is an explicitly absent side, never an inferred alias.
type LayerFieldMapping struct {
	CanonicalOrdinal int
	ProfileOrdinal   int
	CanonicalName    string
	ProfileName      string
}

// LayerFamilyConversion is the single semantic conversion decision for one
// family in one exact profile. Emitters project this model; they must not
// independently re-classify fields, availability, or result types.
type LayerFamilyConversion struct {
	Key           semantic.SemanticKey
	Canonical     *semantic.ProfileVariant
	Profile       *semantic.ProfileVariant
	Availability  LayerAvailability
	BodyChanged   bool
	ResultChanged bool
	Fields        []LayerFieldMapping
	Obligations   []LayerObligation
}

// BodyObligations returns decisions which affect request/constructor bodies.
func (f LayerFamilyConversion) BodyObligations() []LayerObligation {
	result := make([]LayerObligation, 0, len(f.Obligations))
	for _, obligation := range f.Obligations {
		switch obligation.Kind {
		case LayerObligationResult, LayerObligationNewOnly, LayerObligationUpdateProjection:
			continue
		default:
			result = append(result, obligation)
		}
	}
	return result
}

// ResultObligations returns decisions for a method's complete result TypeRef.
func (f LayerFamilyConversion) ResultObligations() []LayerObligation {
	result := make([]LayerObligation, 0, len(f.Obligations))
	for _, obligation := range f.Obligations {
		if obligation.Kind == LayerObligationResult {
			result = append(result, obligation)
		}
	}
	return result
}

// ProjectionObligations returns the projection decision for a constructor
// which is absent from the target profile. Update projections take precedence
// because every canonical-only update also carries the generic new-only
// obligation. Policy may override the mechanical new-only drop with an
// explicit projection, rejection, or unavailable decision.
func (f LayerFamilyConversion) ProjectionObligations() []LayerObligation {
	for _, obligation := range f.Obligations {
		if obligation.Kind == LayerObligationUpdateProjection {
			return []LayerObligation{obligation}
		}
	}
	for _, obligation := range f.Obligations {
		if obligation.Kind == LayerObligationNewOnly &&
			obligation.Resolution.Action != LayerResolveUnavailable {
			return []LayerObligation{obligation}
		}
	}
	return nil
}

// FieldProjectionObligations returns canonical fields which the exact target
// profile cannot carry. Generated encoders evaluate these policies only when
// the corresponding runtime field is present.
func (f LayerFamilyConversion) FieldProjectionObligations() []LayerObligation {
	result := make([]LayerObligation, 0, len(f.Obligations))
	for _, obligation := range f.Obligations {
		if obligation.Kind == LayerObligationFieldProjection {
			result = append(result, obligation)
		}
	}
	return result
}

// LayerProfileConversion contains deterministic family plans for one profile.
type LayerProfileConversion struct {
	Layer       int
	Families    []LayerFamilyConversion
	Obligations []LayerObligation
}

// Family returns the category-qualified family plan, if present.
func (p *LayerProfileConversion) Family(key semantic.SemanticKey) *LayerFamilyConversion {
	if p == nil {
		return nil
	}
	for i := range p.Families {
		if p.Families[i].Key == key {
			return &p.Families[i]
		}
	}
	return nil
}

// LayerConversionPlan is the single conversion analysis shared by obligation
// reporting, static codecs, metadata, and RPC result descriptors.
type LayerConversionPlan struct {
	CanonicalLayer int
	Profiles       []LayerProfileConversion
	Report         LayerObligationReport
}

// Profile returns the exact profile plan. Layers are never clamped.
func (p *LayerConversionPlan) Profile(layer int) *LayerProfileConversion {
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

// AnalyzeLayerConversions builds and applies policy to the single normalized
// conversion model. No generated backend is involved in this phase.
func AnalyzeLayerConversions(schemaSet *SchemaSet, policy LayerObligationPolicy) (*LayerConversionPlan, error) {
	if schemaSet == nil || schemaSet.Schemas[schemaSet.CanonicalLayer] == nil {
		return nil, fmt.Errorf("gen: layer conversions require a canonical schema profile")
	}

	canonicalSchema := schemaSet.Schemas[schemaSet.CanonicalLayer]
	plan := &LayerConversionPlan{CanonicalLayer: schemaSet.CanonicalLayer}
	var obligations []LayerObligation
	for _, layer := range schemaSet.Layers() {
		profileSchema := schemaSet.Schemas[layer]
		profile := LayerProfileConversion{Layer: layer}
		if layer != schemaSet.CanonicalLayer && privateProfile(canonicalSchema, profileSchema) {
			obligations = append(obligations, makeLayerObligation(LayerObligation{
				Kind:        LayerObligationPrivate,
				Layer:       layer,
				Direction:   LayerDirectionProfile,
				SourceType:  profileSchema.Source.Repository + "#" + profileSchema.Source.Path,
				TargetType:  canonicalSchema.Source.Repository + "#" + canonicalSchema.Source.Path,
				SourceShape: profileSchema.Source.SHA256,
				TargetShape: canonicalSchema.Source.SHA256,
			}))
		}

		for _, key := range schemaSet.SortedKeys() {
			family := schemaSet.Families[key]
			canonical := family.ProfilesByLayer[schemaSet.CanonicalLayer]
			target := family.ProfilesByLayer[layer]
			conversion := LayerFamilyConversion{
				Key:       key,
				Canonical: canonical,
				Profile:   target,
			}
			switch {
			case canonical == nil && target != nil:
				conversion.Availability = LayerAvailabilityProfileOnly
				conversion.BodyChanged = true
				obligations = append(obligations, makeDefinitionObligation(
					LayerObligationOldOnly, layer, LayerDirectionProfileToCanonical, target.Definition,
				))
			case canonical != nil && target == nil:
				conversion.Availability = LayerAvailabilityCanonicalOnly
				conversion.BodyChanged = true
				unavailable := makeDefinitionObligation(
					LayerObligationNewOnly, layer, LayerDirectionCanonicalToProfile, canonical.Definition,
				)
				// A canonical-only constructor has no wire identity in this exact
				// profile. Typed containers can omit it mechanically; required or
				// top-level positions still surface a projection error because an
				// empty TL value is not encodable.
				unavailable.Resolution = LayerObligationResolution{Action: LayerResolveDrop}
				obligations = append(obligations, unavailable)
				if isUpdateDefinition(canonical.Definition) {
					obligations = append(obligations, makeDefinitionObligation(
						LayerObligationUpdateProjection, layer, LayerDirectionCanonicalToProfile, canonical.Definition,
					))
				}
			case canonical != nil && target != nil:
				conversion.Availability = LayerAvailabilityPresent
				conversion.BodyChanged = canonical.Definition.WireID != target.Definition.WireID ||
					canonical.Definition.BodyShape != target.Definition.BodyShape
				conversion.ResultChanged = key.Category == semantic.CategoryFunction &&
					!canonical.Definition.Result.Equal(target.Definition.Result)
				conversion.Fields = buildLayerFieldMappings(canonical.Definition, target.Definition)
				obligations = append(obligations, compareLayerDefinitions(layer, canonical.Definition, target.Definition)...)
			default:
				// The family is absent on both sides for this profile. It remains in
				// the plan so all consumers observe the same exact availability.
				conversion.Availability = LayerAvailabilityAbsent
			}
			profile.Families = append(profile.Families, conversion)
		}
		plan.Profiles = append(plan.Profiles, profile)
	}

	sort.Slice(obligations, func(i, j int) bool { return obligations[i].Key < obligations[j].Key })
	for i := 1; i < len(obligations); i++ {
		if obligations[i-1].Key == obligations[i].Key {
			return nil, fmt.Errorf("gen: E_DUPLICATE_LAYER_OBLIGATION: generated key %q is repeated", obligations[i].Key)
		}
	}
	report, err := applyLayerObligationPolicy(obligations, policy)
	if err != nil {
		return nil, err
	}
	plan.Report = report
	for _, obligation := range report.Obligations {
		profile := plan.Profile(obligation.Layer)
		if profile == nil {
			return nil, fmt.Errorf("gen: obligation %s references missing layer %d", obligation.Key, obligation.Layer)
		}
		if obligation.Semantic.QName == "" {
			profile.Obligations = append(profile.Obligations, obligation)
			continue
		}
		family := profile.Family(obligation.Semantic)
		if family == nil {
			return nil, fmt.Errorf("gen: obligation %s references missing family %s", obligation.Key, obligation.Semantic)
		}
		family.Obligations = append(family.Obligations, obligation)
	}
	return plan, nil
}

func buildLayerFieldMappings(canonical, profile *semantic.Definition) []LayerFieldMapping {
	aliases := findFieldAliases(canonical, profile)
	profileToCanonical := aliasMap(aliases, false)
	for profileName, canonicalName := range aliasMap(findFieldReplacements(canonical, profile, aliases), false) {
		profileToCanonical[profileName] = canonicalName
	}
	canonicalByName := make(map[string]int, len(canonical.Fields))
	for i, field := range canonical.Fields {
		canonicalByName[field.Name] = i
	}
	result := make([]LayerFieldMapping, 0, len(profile.Fields))
	for profileOrdinal, field := range profile.Fields {
		canonicalName := field.Name
		if alias, ok := profileToCanonical[field.Name]; ok {
			canonicalName = alias
		}
		canonicalOrdinal, ok := canonicalByName[canonicalName]
		if !ok {
			canonicalOrdinal = -1
			canonicalName = ""
		}
		result = append(result, LayerFieldMapping{
			CanonicalOrdinal: canonicalOrdinal,
			ProfileOrdinal:   profileOrdinal,
			CanonicalName:    canonicalName,
			ProfileName:      field.Name,
		})
	}
	return result
}
