package gen

import (
	"bytes"
	"go/format"
	"regexp"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/gen/semantic"
)

func TestTLProfileSidecarGeneratedMetadata(t *testing.T) {
	set := layerRPCSyntheticSchemaSet(t)
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: layerSparseSyntheticPolicy(t, set)})
	if err != nil {
		t.Fatal(err)
	}
	result := sourceSnapshot{}
	if err := generator.WriteTLProfileSource(result, "tlprofile", Template()); err != nil {
		t.Fatal(err)
	}
	source := result["tl_profile_metadata_gen.go"]
	if len(source) == 0 {
		t.Fatal("tlprofile metadata source is absent")
	}
	if _, err := format.Source(source); err != nil {
		t.Fatalf("format tlprofile metadata: %v", err)
	}
	text := string(source)
	for _, want := range []string{"type Profile int", "func ResolveProfile", "type SemanticID uint64", "func WireID", "func SemanticForWireID"} {
		if !strings.Contains(text, want) {
			t.Errorf("tlprofile metadata is missing %q", want)
		}
	}
	if strings.Contains(text, "LayerProfile") || strings.Contains(text, "LayerSemanticID") {
		t.Fatal("tlprofile metadata retained the old dense public API names")
	}
	scanner := string(result["tl_profile_scan_gen.go"])
	for _, want := range []string{"func tlScanExact", "func tlScanDynamic", "func tlScanWire", "remainingElements"} {
		if !strings.Contains(scanner, want) {
			t.Errorf("tlprofile static scanner is missing %q", want)
		}
	}
	for _, forbidden := range []string{"reflect.", "semantic.TypeRef", "map[uint32]", "LayerTypeRef"} {
		if strings.Contains(scanner, forbidden) {
			t.Errorf("tlprofile static scanner contains runtime catalog machinery %q", forbidden)
		}
	}
	routes := string(result["tl_profile_route_gen.go"])
	for _, want := range []string{"func tlLookupRoute", "func tlNewCanonical", "tlRouteDirect", "tlRouteRetag"} {
		if !strings.Contains(routes, want) {
			t.Errorf("tlprofile sparse routes are missing %q", want)
		}
	}
	if strings.Contains(routes, "map[") || strings.Contains(routes, "reflect.") {
		t.Fatal("tlprofile sparse routes contain a runtime registry or reflection")
	}

	model, err := generator.buildLayerSparseCodecModel("tlprofile")
	if err != nil {
		t.Fatal(err)
	}
	var generated strings.Builder
	for _, source := range result {
		generated.Write(source)
	}
	generatedText := generated.String()
	var directWires int
	for _, wire := range model.Wires {
		if wire.SparseDirect {
			directWires++
			encode := "func " + wire.EncodeBareName + "Body("
			if got := strings.Contains(generatedText, encode); got != wire.SparseEncode {
				t.Errorf("tlprofile sparse direct wire %#08x encode helper presence = %v, want %v", wire.WireID, got, wire.SparseEncode)
			}
			decode := "func " + wire.DecodeName + "("
			if got := strings.Contains(generatedText, decode); got != wire.SparseDecode {
				t.Errorf("tlprofile sparse direct wire %#08x decode helper presence = %v, want %v", wire.WireID, got, wire.SparseDecode)
			}
		}
		var forbidden []string
		if !wire.ProfileOnly {
			forbidden = append(forbidden, "func "+wire.EncodeName+"(")
		}
		if wire.SparseDirect {
			forbidden = append(forbidden, "func "+wire.DecodeBareName+"(")
		} else if !wire.ProfileOnly {
			forbidden = append(forbidden, "func "+wire.EncodeBareName+"(")
		}
		for _, signature := range forbidden {
			if strings.Contains(generatedText, signature) {
				t.Errorf("tlprofile sparse wire %#08x retained unused helper %q", wire.WireID, signature)
			}
		}
	}
	if directWires == 0 {
		t.Fatal("synthetic tlprofile schema has no sparse direct wire")
	}
	for file, expression := range map[string]*regexp.Regexp{
		"tl_profile_scan_gen.go":            regexp.MustCompile(`(?m)^func (tlScanClass[A-Za-z0-9_]+)\(`),
		"tl_profile_sparse_families_gen.go": regexp.MustCompile(`(?m)^func (layer(?:Project|Encode)Family[A-Za-z0-9_]+)\(`),
		"tl_profile_sparse_classes_gen.go":  regexp.MustCompile(`(?m)^func (layer(?:Project|Encode|Decode)Class[A-Za-z0-9_]+)\(`),
	} {
		for _, match := range expression.FindAllStringSubmatch(string(result[file]), -1) {
			if strings.Count(generatedText, match[1]) == 1 {
				t.Errorf("tlprofile sparse source retained unreferenced helper %s in %s", match[1], file)
			}
		}
	}
}

func layerSparseSyntheticPolicy(t *testing.T, set *SchemaSet) LayerObligationPolicy {
	t.Helper()
	initial, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	policy := LayerObligationPolicy{}
	for _, obligation := range initial.LayerConversionPlan().Report.Unresolved() {
		resolution := LayerObligationResolution{Action: LayerResolveReject}
		switch obligation.Kind {
		case LayerObligationRequired:
			resolution.Action = LayerResolveDefault
		case LayerObligationDiscard, LayerObligationUpdateProjection:
			resolution.Action = LayerResolveDrop
		case LayerObligationPrivate:
			resolution.Action = LayerResolveAllow
		case LayerObligationResult:
			if obligation.Semantic.QName == "join" {
				hook := "adaptOldJoinResult"
				if obligation.Direction == LayerDirectionProfileToCanonical {
					hook = "adaptNewJoinResult"
				}
				resolution = LayerObligationResolution{Action: LayerResolveAdapter, Hook: hook}
			}
		}
		policy.Entries = append(policy.Entries, LayerObligationPolicyEntry{Key: obligation.Key, Resolution: resolution})
	}
	return policy
}

func TestLayerExecutionPlanDeduplicatesRoutesByBehavior(t *testing.T) {
	set := layerRPCSyntheticSchemaSet(t)
	initial, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: layerRPCSyntheticPolicy(t, set)})
	if err != nil {
		t.Fatal(err)
	}
	if initial.LayerConversionPlan() == nil {
		t.Fatal("initial conversion analysis is absent")
	}
	model, err := generator.buildLayerExecutionModel()
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Routes) == 0 || len(model.PreflightPlans) == 0 || len(model.ResultPlans) == 0 {
		t.Fatalf("incomplete execution model: routes=%d preflight=%d results=%d", len(model.Routes), len(model.PreflightPlans), len(model.ResultPlans))
	}
	for _, route := range model.Routes {
		if route.Mode != layerExecutionDirect && route.BodyPlan < 0 {
			t.Fatalf("non-direct route has no body plan: %+v", route)
		}
		if route.Key.Category == semantic.CategoryFunction && route.PreflightPlan < 0 {
			t.Fatalf("method route has incomplete plans: %+v", route)
		}
		if route.Key.Category == semantic.CategoryFunction && route.Mode != layerExecutionProfileOnly && route.ResultPlan < 0 {
			t.Fatalf("canonical method route has no result plan: %+v", route)
		}
	}
	if len(model.BodyPlans) >= len(model.Routes) {
		t.Fatalf("body plans were not deduplicated: plans=%d routes=%d", len(model.BodyPlans), len(model.Routes))
	}
}

func TestLayerExecutionAuditDeterministic(t *testing.T) {
	set, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := LoadLayerPolicy("../_schema/layers/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	first, err := generator.MarshalLayerExecutionAudit()
	if err != nil {
		t.Fatal(err)
	}
	second, err := generator.MarshalLayerExecutionAudit()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("layer execution audit is not deterministic")
	}
}

func TestLayerExecutionPlanRealSchemaIsSparse(t *testing.T) {
	set, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := LoadLayerPolicy("../_schema/layers/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerExecutionModel()
	if err != nil {
		t.Fatal(err)
	}
	var direct, retag, rewrite, policyCount, profileOnly int
	for _, route := range model.Routes {
		switch route.Mode {
		case layerExecutionDirect:
			direct++
		case layerExecutionRetag:
			retag++
		case layerExecutionRewrite:
			rewrite++
		case layerExecutionPolicy:
			policyCount++
		case layerExecutionProfileOnly:
			profileOnly++
		}
	}
	t.Logf("routes=%d direct=%d retag=%d rewrite=%d policy=%d profile_only=%d body_plans=%d preflight_plans=%d result_plans=%d",
		len(model.Routes), direct, retag, rewrite, policyCount, profileOnly,
		len(model.BodyPlans), len(model.PreflightPlans), len(model.ResultPlans))
	if direct*2 <= len(model.Routes) {
		t.Fatalf("canonical-direct routes are not the majority: direct=%d routes=%d", direct, len(model.Routes))
	}
	if len(model.BodyPlans)*4 >= len(model.Routes) {
		t.Fatalf("sparse body-plan ratio regressed: plans=%d routes=%d", len(model.BodyPlans), len(model.Routes))
	}
	// Layer 230 adds a sixth profile and 85 new definitions, with 1626/1085/858
	// deduplicated plans. Layer 232 makes 231 a seventh exact profile and adds
	// 24 definitions: 1899/1148/969 plans. Byte/line caps remain unchanged in
	// the source gate.
	if len(model.BodyPlans) > 2000 || len(model.PreflightPlans) > 1200 || len(model.ResultPlans) > 1020 {
		t.Fatalf("sparse plan budget regressed: body=%d preflight=%d result=%d", len(model.BodyPlans), len(model.PreflightPlans), len(model.ResultPlans))
	}
}

func TestLayerScanModelRealSchemaStaysCompact(t *testing.T) {
	set, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := LoadLayerPolicy("../_schema/layers/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerScanModel()
	if err != nil {
		t.Fatal(err)
	}
	var wires int
	for _, bucket := range model.WireBuckets {
		wires += len(bucket.Wires)
	}
	t.Logf("static scanner bodies=%d wires=%d classes=%d bare=%d", len(model.Bodies), wires, len(model.Classes), len(model.Bares))
	if wires < 2000 {
		t.Fatalf("scanner wire universe is incomplete: %d", wires)
	}
	if len(model.Bodies) > 1500 {
		t.Fatalf("scanner body deduplication regressed: %d", len(model.Bodies))
	}
}

func TestLayerSparseCodecModelRealSchemaStaysCompact(t *testing.T) {
	set, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := LoadLayerPolicy("../_schema/layers/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerSparseCodecModel("tlprofile")
	if err != nil {
		t.Fatal(err)
	}
	var wires int
	for _, bucket := range model.WireBuckets {
		wires += len(bucket.Wires)
	}
	t.Logf("sparse typed codec wires=%d families=%d classes=%d result_plans=%d",
		wires, len(model.FamilyDeclarations), len(model.ClassDeclarations), len(model.SparseResultPlans))
	// Layer 231 measures 1501 wires after URL-auth and wallet-proof closure;
	// Layer 232 measures 1527 with the seventh profile and withdrawal adapter.
	// Keep the independent source-byte/line and execution-plan ratio gates.
	if wires > 1540 || len(model.FamilyDeclarations) > 4000 || len(model.ClassDeclarations) > 1000 || len(model.SparseResultPlans) > 350 {
		t.Fatalf("sparse typed codec budget regressed: wires=%d families=%d classes=%d result_plans=%d",
			wires, len(model.FamilyDeclarations), len(model.ClassDeclarations), len(model.SparseResultPlans))
	}
}

func TestLayerWrapperModelRealSchemaStaysCompact(t *testing.T) {
	set, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := LoadLayerPolicy("../_schema/layers/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerWrapperModel()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sparse wrappers bodies=%d unprofiled_invariants=%d known_rpc_ids=%d", len(model.Bodies), len(model.UnprofiledInvariants), len(model.KnownRPCWireIDs))
	if len(model.Bodies) != 11 || len(model.UnprofiledInvariants) != 399 || len(model.KnownRPCWireIDs) < 800 {
		t.Fatalf("sparse wrapper/invariant surface drifted: bodies=%d invariants=%d known=%d", len(model.Bodies), len(model.UnprofiledInvariants), len(model.KnownRPCWireIDs))
	}
	vectorGuard := false
	objectGuard := false
	for _, body := range model.Bodies {
		if strings.Contains(body.Source, "SemanticMethodInvokeAfterMsgs") {
			vectorGuard = strings.Contains(body.Source, "tlWrapperVectorLength(profile, b, scanState, 8)") &&
				strings.Contains(body.Source, "scanState.leave()")
		}
		if strings.Contains(body.Source, "SemanticMethodInitConnection") {
			objectGuard = strings.Contains(body.Source, "tlDecodeObjectPrefixValidated(profile, b, limits, scanState, tlScanClass")
		}
	}
	if !vectorGuard {
		t.Fatal("invokeAfterMsgs wrapper parser does not use generated vector limits")
	}
	if !objectGuard {
		t.Fatal("initConnection wrapper parser does not statically validate Object metadata")
	}
}
