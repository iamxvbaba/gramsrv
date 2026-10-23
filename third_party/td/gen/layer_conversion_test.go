package gen

import (
	"testing"

	"github.com/iamxvbaba/td/gen/semantic"
)

func TestLayerConversionPlanIsSingleDecisionSource(t *testing.T) {
	set := obligationSchemaSet(t)
	plan, err := AnalyzeLayerConversions(set, LayerObligationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	profile := plan.Profile(1)
	if profile == nil {
		t.Fatal("profile 1 is missing")
	}

	oldOnly := profile.Family(typeStaticKey("oldOnly"))
	if oldOnly == nil || oldOnly.Availability != LayerAvailabilityProfileOnly || len(oldOnly.BodyObligations()) != 1 {
		t.Fatalf("old-only conversion = %+v", oldOnly)
	}
	newOnly := profile.Family(typeStaticKey("newOnly"))
	if newOnly == nil || newOnly.Availability != LayerAvailabilityCanonicalOnly || len(newOnly.BodyObligations()) != 0 {
		t.Fatalf("new-only conversion = %+v", newOnly)
	}
	update := profile.Family(typeStaticKey("updateModern"))
	if update == nil || update.Availability != LayerAvailabilityCanonicalOnly || len(update.ProjectionObligations()) != 1 {
		t.Fatalf("update projection conversion = %+v", update)
	}
	result := profile.Family(functionStaticKey("getShape"))
	if result == nil || !result.ResultChanged || len(result.ResultObligations()) != 2 {
		t.Fatalf("result conversion = %+v", result)
	}

	shape := profile.Family(typeStaticKey("shape"))
	if shape == nil || shape.Availability != LayerAvailabilityPresent || !shape.BodyChanged {
		t.Fatalf("shape conversion = %+v", shape)
	}
	if got := len(shape.FieldProjectionObligations()); got != 2 {
		t.Fatalf("shape field projections = %d, want 2", got)
	}
	foundAlias := false
	foundReplacement := false
	for _, mapping := range shape.Fields {
		if mapping.ProfileName == "renamed_old" {
			foundAlias = mapping.CanonicalName == "renamed_new" && mapping.CanonicalOrdinal >= 0
		}
		if mapping.ProfileName == "legacy_tone" {
			foundReplacement = mapping.CanonicalName == "modern_tone" && mapping.CanonicalOrdinal >= 0
		}
	}
	if !foundAlias {
		t.Fatalf("shape aliases were not projected into field mappings: %+v", shape.Fields)
	}
	if !foundReplacement {
		t.Fatalf("shape replacements were not projected into field mappings: %+v", shape.Fields)
	}

	canonical := plan.Profile(2)
	if canonical == nil || canonical.Family(typeStaticKey("oldOnly")).Availability != LayerAvailabilityAbsent {
		t.Fatalf("canonical old-only availability = %+v", canonical)
	}
}

func TestProjectionObligationsPreferUpdateOverNewOnly(t *testing.T) {
	update := LayerObligation{
		Kind:       LayerObligationUpdateProjection,
		Resolution: LayerObligationResolution{Action: LayerResolveDrop},
	}
	newOnlyDrop := LayerObligation{
		Kind:       LayerObligationNewOnly,
		Resolution: LayerObligationResolution{Action: LayerResolveDrop},
	}
	newOnlyProjected := LayerObligation{
		Kind: LayerObligationNewOnly,
		Resolution: LayerObligationResolution{
			Action: LayerResolveProject,
			Hook:   "projectNewOnly",
		},
	}

	tests := []struct {
		name        string
		obligations []LayerObligation
		wantKind    LayerObligationKind
		wantCount   int
	}{
		{
			name:        "default new-only drop is active",
			obligations: []LayerObligation{newOnlyDrop},
			wantKind:    LayerObligationNewOnly,
			wantCount:   1,
		},
		{
			name:        "reviewed new-only projection is active",
			obligations: []LayerObligation{newOnlyProjected},
			wantKind:    LayerObligationNewOnly,
			wantCount:   1,
		},
		{
			name:        "update policy wins over generic new-only policy",
			obligations: []LayerObligation{newOnlyProjected, update},
			wantKind:    LayerObligationUpdateProjection,
			wantCount:   1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := (LayerFamilyConversion{Obligations: test.obligations}).ProjectionObligations()
			if len(got) != test.wantCount {
				t.Fatalf("projection obligations = %+v, want %d", got, test.wantCount)
			}
			if test.wantCount != 0 && got[0].Kind != test.wantKind {
				t.Fatalf("projection obligation kind = %s, want %s", got[0].Kind, test.wantKind)
			}
		})
	}
}

func TestLayerConversionPlanTelegram225Through232(t *testing.T) {
	set, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := AnalyzeLayerConversions(set, LayerObligationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[LayerObligationKind]int)
	for _, obligation := range plan.Report.Obligations {
		counts[obligation.Kind]++
	}
	want := map[LayerObligationKind]int{
		LayerObligationAtomicFlagGroup:  0,
		LayerObligationDiscard:          9,
		LayerObligationFieldProjection:  263,
		LayerObligationFieldReplacement: 0,
		LayerObligationIncompatible:     38,
		LayerObligationNewOnly:          957,
		LayerObligationOldOnly:          73,
		LayerObligationRequired:         30,
		LayerObligationResult:           8,
		LayerObligationUpdateProjection: 45,
	}
	for kind, expected := range want {
		if got := counts[kind]; got != expected {
			t.Errorf("%s obligations = %d, want %d", kind, got, expected)
		}
	}
	if got := counts[LayerObligationPrivate]; got != 0 {
		t.Fatalf("private obligations = %d, want 0", got)
	}
}
