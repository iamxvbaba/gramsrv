package gen

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/gotd/tl"
	"github.com/iamxvbaba/td/gen/semantic"
)

const obligationProfileOne = `
---types---
oldResult#10000001 = OldResult;
oldOnly#10000002 = OldOnly;
shape#10000003 flags:# renamed_old:flags.0?int legacy_tone:flags.3?string required_old:long incompatible:string = Shape;
atomic#10000004 flags:# first:flags.0?int second:flags.0?int = Atomic;
---functions---
getShape#10000005 = OldResult;
// LAYER 1
`

const obligationCanonicalTwo = `
---types---
newResult#20000001 = NewResult;
newOnly#20000002 = NewOnly;
updateModern#20000003 pts:int = Update;
shape#20000004 flags:# renamed_new:flags.0?int modern_tone:flags.3?long required_new:bytes incompatible:int modern:flags.2?true = Shape;
atomic#20000005 flags:# first:flags.0?int second:flags.1?int = Atomic;
---functions---
getShape#20000006 = NewResult;
// LAYER 2
`

func TestAnalyzeLayerObligationsClassifiesSyntheticChanges(t *testing.T) {
	schemaSet := obligationSchemaSet(t)
	first, err := AnalyzeLayerObligations(schemaSet, LayerObligationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := AnalyzeLayerObligations(schemaSet, LayerObligationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("obligation report is not deterministic")
	}

	kinds := make(map[LayerObligationKind]int)
	keys := make(map[LayerObligationKey]struct{})
	for _, obligation := range first.Obligations {
		kinds[obligation.Kind]++
		if obligation.Key == "" || !strings.HasPrefix(string(obligation.Key), "layer-obligation/v1/") {
			t.Fatalf("invalid stable key %q", obligation.Key)
		}
		if _, duplicate := keys[obligation.Key]; duplicate {
			t.Fatalf("duplicate obligation key %q", obligation.Key)
		}
		keys[obligation.Key] = struct{}{}
	}
	for _, kind := range []LayerObligationKind{
		LayerObligationAlias,
		LayerObligationRequired,
		LayerObligationIncompatible,
		LayerObligationResult,
		LayerObligationOldOnly,
		LayerObligationNewOnly,
		LayerObligationAtomicFlagGroup,
		LayerObligationUpdateProjection,
		LayerObligationFieldProjection,
		LayerObligationFieldReplacement,
		LayerObligationDiscard,
		LayerObligationPrivate,
	} {
		if kinds[kind] == 0 {
			t.Errorf("missing %q obligation; counts=%v", kind, kinds)
		}
	}

	alias := findObligation(t, first, LayerObligationAlias, "shape")
	if alias.Field != "renamed_new" || alias.OtherField != "renamed_old" || alias.Direction != LayerDirectionBoth {
		t.Fatalf("alias obligation = %+v", alias)
	}
	projection := findObligation(t, first, LayerObligationUpdateProjection, "updateModern")
	if projection.Direction != LayerDirectionCanonicalToProfile {
		t.Fatalf("update projection = %+v", projection)
	}
	discard := findObligation(t, first, LayerObligationDiscard, "shape")
	if discard.Direction != LayerDirectionProfileToCanonical || discard.Field == "" || discard.OtherField != "" {
		t.Fatalf("discard obligation = %+v", discard)
	}
	fieldProjection := findFieldObligation(t, first, LayerObligationFieldProjection, "shape", "modern")
	if fieldProjection.Direction != LayerDirectionCanonicalToProfile || fieldProjection.Field != "modern" || fieldProjection.OtherField != "" {
		t.Fatalf("field projection obligation = %+v", fieldProjection)
	}
	if fieldProjection.Resolution.Action != LayerResolveDrop {
		t.Fatalf("field projection default = %+v, want drop", fieldProjection.Resolution)
	}
	if err := validateLayerObligationResolution(LayerObligationFieldProjection, LayerObligationResolution{Action: LayerResolveRejectIfPresent}); err != nil {
		t.Fatalf("field projection reject was rejected: %v", err)
	}
	replacement := findFieldObligation(t, first, LayerObligationFieldReplacement, "shape", "modern_tone")
	if replacement.Direction != LayerDirectionBoth || replacement.OtherField != "legacy_tone" || replacement.SourceType != "long" || replacement.TargetType != "string" {
		t.Fatalf("field replacement obligation = %+v", replacement)
	}
	if err := validateLayerObligationResolution(LayerObligationDiscard, LayerObligationResolution{Action: LayerResolveDrop}); err != nil {
		t.Fatalf("explicit discard drop was rejected: %v", err)
	}
	if err := validateLayerObligationResolution(LayerObligationDiscard, LayerObligationResolution{Action: LayerResolveUnavailable}); err == nil {
		t.Fatal("discard accepted an unavailable action")
	}
}

func TestLayerObligationPolicyMatchesExactKeyAndRejectsStale(t *testing.T) {
	schemaSet := obligationSchemaSet(t)
	report, err := AnalyzeLayerObligations(schemaSet, LayerObligationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	alias := findObligation(t, report, LayerObligationAlias, "shape")
	initialUnresolved := len(report.Unresolved())
	entry := LayerObligationPolicyEntry{Key: alias.Key, Resolution: LayerObligationResolution{
		Action: LayerResolveAlias,
		Hook:   "AliasShapeField",
	}}
	resolved, err := AnalyzeLayerObligations(schemaSet, LayerObligationPolicy{Entries: []LayerObligationPolicyEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(resolved.Unresolved()), initialUnresolved-1; got != want {
		t.Fatalf("unresolved = %d, want %d", got, want)
	}
	var gotResolution LayerObligationResolution
	for _, obligation := range resolved.Obligations {
		if obligation.Key == entry.Key {
			gotResolution = obligation.Resolution
		}
	}
	if gotResolution != entry.Resolution {
		t.Fatalf("resolution = %+v, want %+v", gotResolution, entry.Resolution)
	}

	_, err = AnalyzeLayerObligations(schemaSet, LayerObligationPolicy{Entries: []LayerObligationPolicyEntry{{
		Key:        "layer-obligation/v1/required/stale",
		Resolution: LayerObligationResolution{Action: LayerResolveAdapter, Hook: "RequiredField"},
	}}})
	if err == nil || !strings.Contains(err.Error(), "E_STALE_LAYER_POLICY") {
		t.Fatalf("stale policy error = %v", err)
	}

	_, err = AnalyzeLayerObligations(schemaSet, LayerObligationPolicy{Entries: []LayerObligationPolicyEntry{entry, entry}})
	if err == nil || !strings.Contains(err.Error(), "E_DUPLICATE_LAYER_POLICY") {
		t.Fatalf("duplicate policy error = %v", err)
	}

	_, err = AnalyzeLayerObligations(schemaSet, LayerObligationPolicy{Entries: []LayerObligationPolicyEntry{{
		Key: alias.Key,
		Resolution: LayerObligationResolution{
			Action: LayerResolveDrop,
			Note:   "free-form text must not resolve an alias",
		},
	}}})
	if err == nil || !strings.Contains(err.Error(), "E_INVALID_LAYER_POLICY") {
		t.Fatalf("invalid action error = %v", err)
	}

	modifiedCanonical := strings.Replace(
		obligationCanonicalTwo,
		"required_new:bytes incompatible:int modern:flags.2?true = Shape;",
		"required_new:bytes incompatible:int modern:flags.2?true future:flags.4?long = Shape;",
		1,
	)
	modified := obligationSchemaSetWithCanonical(t, modifiedCanonical)
	_, err = AnalyzeLayerObligations(modified, LayerObligationPolicy{Entries: []LayerObligationPolicyEntry{entry}})
	if err == nil || !strings.Contains(err.Error(), "E_STALE_LAYER_POLICY") {
		t.Fatalf("shape-stale policy error = %v", err)
	}
}

func TestOldOnlyAliasRequiresExactCanonicalTarget(t *testing.T) {
	report, err := AnalyzeLayerObligations(obligationSchemaSet(t), LayerObligationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	oldOnly := findObligation(t, report, LayerObligationOldOnly, "oldOnly")
	resolution := LayerObligationResolution{Action: LayerResolveAlias, Hook: "AdaptOldOnly"}
	if _, err := AnalyzeLayerObligations(obligationSchemaSet(t), LayerObligationPolicy{Entries: []LayerObligationPolicyEntry{{
		Key: oldOnly.Key, Resolution: resolution,
	}}}); err == nil || !strings.Contains(err.Error(), "semantic target") {
		t.Fatalf("missing old-only target error = %v", err)
	}
	resolution.Target = "type:newOnly"
	if _, err := AnalyzeLayerObligations(obligationSchemaSet(t), LayerObligationPolicy{Entries: []LayerObligationPolicyEntry{{
		Key: oldOnly.Key, Resolution: resolution,
	}}}); err != nil {
		t.Fatalf("valid old-only target was rejected: %v", err)
	}
}

func TestGeneratorLayerObligationsRequiresSchemaSet(t *testing.T) {
	parsed, err := tl.Parse(bytes.NewBufferString(obligationCanonicalTwo))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := NewGenerator(parsed, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.LayerObligations(LayerObligationPolicy{}); err == nil {
		t.Fatal("single-schema generator unexpectedly analyzed layer obligations")
	}
}

func TestTelegramLayerObligationReport(t *testing.T) {
	schemaSet, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := AnalyzeLayerObligations(schemaSet, LayerObligationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[LayerObligationKind]int)
	for _, obligation := range report.Obligations {
		counts[obligation.Kind]++
	}
	if counts[LayerObligationPrivate] != 0 {
		t.Fatalf("official profiles produced private obligations: %v", counts)
	}
	if got, want := len(report.Unresolved()), 203; got != want {
		t.Fatalf("blocking unresolved obligations = %d, want %d", got, want)
	}
	t.Logf("Telegram Layers 225-229 obligations: total=%d by_kind=%v", len(report.Obligations), counts)
}

func obligationSchemaSet(t *testing.T) *SchemaSet {
	return obligationSchemaSetWithCanonical(t, obligationCanonicalTwo)
}

func obligationSchemaSetWithCanonical(t *testing.T, canonicalSource string) *SchemaSet {
	t.Helper()
	profile := parseObligationProfile(t, obligationProfileOne, semantic.SourceRef{
		Layer:      1,
		Repository: "https://example.invalid/private.git",
		Path:       "api.tl",
	})
	canonical := parseObligationProfile(t, canonicalSource, semantic.SourceRef{
		Layer:      2,
		Repository: "https://example.invalid/official.git",
		Path:       "api.tl",
	})
	schemaSet, err := NewSchemaSet(2, profile, canonical)
	if err != nil {
		t.Fatal(err)
	}
	return schemaSet
}

func parseObligationProfile(t *testing.T, source string, ref semantic.SourceRef) *SchemaProfile {
	t.Helper()
	parsed, err := tl.Parse(bytes.NewBufferString(source))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := semantic.BuildSchema(parsed, ref)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func findObligation(t *testing.T, report LayerObligationReport, kind LayerObligationKind, qname string) LayerObligation {
	t.Helper()
	for _, obligation := range report.Obligations {
		if obligation.Kind == kind && obligation.Semantic.QName == qname {
			return obligation
		}
	}
	t.Fatalf("missing %s obligation for %s", kind, qname)
	return LayerObligation{}
}

func findFieldObligation(t *testing.T, report LayerObligationReport, kind LayerObligationKind, qname, field string) LayerObligation {
	t.Helper()
	for _, obligation := range report.Obligations {
		if obligation.Kind == kind && obligation.Semantic.QName == qname && obligation.Field == field {
			return obligation
		}
	}
	t.Fatalf("missing %s obligation for %s.%s", kind, qname, field)
	return LayerObligation{}
}
