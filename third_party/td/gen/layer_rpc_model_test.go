package gen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gotd/tl"

	"github.com/iamxvbaba/td/gen/semantic"
)

const layerRPCSyntheticOne = `
---types---
pong#21000001 value:int = Pong;
oldJoin#21000002 value:int = OldJoin;
thingOne#21000004 value:int = Thing;
thingTwo#21000005 value:int = Thing;
boolFalse#bc799737 = Bool;
boolTrue#997275b5 = Bool;
messageRange#31000001 min_id:int max_id:int = MessageRange;
messageRangeEmpty#31000003 = MessageRange;
probeMeta#44000001 value:int = ProbeMeta;
---functions---
echo#21000010 legacy_tag:string value:int = Pong;
join#21000011 value:int = OldJoin;
legacy#21000012 value:int = Pong;
confirm#21000030 = Bool;
getThing#21000031 = Thing;
listIDs#21000032 = Vector<int>;
invokeAfterMsg#cb9f372d {X:Type} msg_id:long query:!X = X;
invokeAfterMsgs#365275f2 {X:Type} msg_ids:Vector<long> query:!X = X;
invokeWithMessagesRange#31000002 {X:Type} range:MessageRange query:!X = X;
invokeWithLayer#da9b0d0d {X:Type} layer:int query:!X = X;
futureEnvelope#42000001 {X:Type} query:!X = X;
profileEnvelope#43000001 {X:Type} padding:Vector<int> meta:ProbeMeta query:!X = X;
// LAYER 1
`

const layerRPCSyntheticTwo = `
---types---
pong#21000001 value:int = Pong;
newJoin#21000003 value:int = NewJoin;
thingOne#21000004 value:int = Thing;
thingTwo#21000005 value:int = Thing;
boolFalse#bc799737 = Bool;
boolTrue#997275b5 = Bool;
messageRange#31000001 min_id:int max_id:int = MessageRange;
messageRangeEmpty#31000003 = MessageRange;
probeMeta#44000002 value:int = ProbeMeta;
---functions---
echo#21000020 value:int = Pong;
join#21000021 value:int = NewJoin;
modern#21000022 value:int = Pong;
confirm#21000030 = Bool;
getThing#21000031 = Thing;
listIDs#21000032 = Vector<int>;
invokeAfterMsg#cb9f372d {X:Type} msg_id:long query:!X = X;
invokeAfterMsgs#365275f2 {X:Type} msg_ids:Vector<long> query:!X = X;
invokeWithMessagesRange#31000002 {X:Type} range:MessageRange query:!X = X;
invokeWithLayer#da9b0d0d {X:Type} layer:int query:!X = X;
futureEnvelope#42000002 {X:Type} query:!X = X;
profileEnvelope#43000001 {X:Type} padding:Vector<int> meta:ProbeMeta query:!X = X;
// LAYER 2
`

func TestLayerRPCRequestComposesStaticFieldAdapters(t *testing.T) {
	obligations := []LayerObligation{
		{Resolution: LayerObligationResolution{Action: LayerResolveAdapter, Hook: "adaptFirst"}},
		{Resolution: LayerObligationResolution{Action: LayerResolveAdapter, Hook: "adaptSecond"}},
	}
	if action, hook := classifyLayerRPCObligations(obligations, true); action != layerRPCAdapter || hook != "" {
		t.Fatalf("composable request adapters = (%s, %q)", action, hook)
	}
	if action, _ := classifyLayerRPCObligations(obligations, false); action != layerRPCReject {
		t.Fatalf("single result adapter accepted multiple hooks: %s", action)
	}
}

func TestLayerRPCModelSingleSemanticHandlerAndExactResults(t *testing.T) {
	set := layerRPCSyntheticSchemaSet(t)
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerRPCModel()
	if err != nil {
		t.Fatal(err)
	}

	joinKey := semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "join"}
	join := model.method(joinKey)
	if join == nil || !join.Handler || join.Canonical == nil {
		t.Fatalf("join method = %+v", join)
	}
	if got, want := join.WireIDs, []uint32{0x21000011, 0x21000021}; !equalLayerRPCWireIDs(got, want) {
		t.Fatalf("join wire IDs = %#v, want %#v", got, want)
	}
	for _, layer := range []int{1, 2} {
		profile := join.profile(layer)
		if profile == nil || profile.Definition == nil {
			t.Fatalf("join profile %d = %+v", layer, profile)
		}
		route := model.route(layer, profile.WireID)
		if route == nil || route.Method != join || route.Profile != profile {
			t.Fatalf("join route layer %d = %+v", layer, route)
		}
		if profile.Result.CanonicalRef == nil || profile.Result.CanonicalRef.QName != "NewJoin" {
			t.Fatalf("join layer %d canonical result = %+v", layer, profile.Result.CanonicalRef)
		}
	}
	old := join.profile(1)
	if old.Result.WireRef == nil || old.Result.WireRef.QName != "OldJoin" || old.Result.Action != layerRPCReject {
		t.Fatalf("old join result = %+v", old.Result)
	}
	canonical := join.profile(2)
	if canonical.Result.WireRef == nil || canonical.Result.WireRef.QName != "NewJoin" || canonical.Result.Action != layerRPCDirect {
		t.Fatalf("canonical join result = %+v", canonical.Result)
	}
	if old.Result.CanonicalValue == nil || old.Result.WireValue == nil || old.Result.CanonicalValue == old.Result.WireValue {
		t.Fatalf("old join did not retain both complete result plans: %+v", old.Result)
	}
	echo := model.method(semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "echo"}).profile(1)
	if echo.Request != layerRPCReject || len(echo.RequestObligations) != 1 || echo.RequestObligations[0].Kind != LayerObligationDiscard {
		t.Fatalf("unresolved source-only request field was not fail-closed: %+v", echo)
	}

	legacy := model.method(semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "legacy"})
	if legacy == nil || legacy.Canonical != nil || legacy.Handler {
		t.Fatalf("old-only legacy method = %+v", legacy)
	}
	legacyProfile := legacy.profile(1)
	if legacyProfile == nil || legacyProfile.Availability != LayerAvailabilityProfileOnly || legacyProfile.Request != layerRPCReject {
		t.Fatalf("old-only legacy profile = %+v", legacyProfile)
	}
	if model.route(1, 0x21000012) == nil {
		t.Fatal("old-only wire ID was silently omitted instead of receiving an explicit fail-closed route")
	}

	modern := model.method(semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "modern"})
	if modern == nil || modern.profile(1).Request != layerRPCUnavailable || modern.profile(1).Definition != nil {
		t.Fatalf("new-only modern profile = %+v", modern)
	}
	if route := model.route(1, 0x21000022); route != nil {
		t.Fatalf("unavailable canonical-only method unexpectedly has a route: %+v", route)
	}
}

func TestLayerRPCModelGenericSlotsAndRecursiveFreeze(t *testing.T) {
	set := layerRPCSyntheticSchemaSet(t)
	policy := layerRPCSyntheticPolicy(t, set)
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerRPCModel()
	if err != nil {
		t.Fatal(err)
	}
	echo := model.method(semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "echo"}).profile(1)
	if echo.Request != layerRPCAdapter || echo.RequestHook != "" || len(echo.RequestObligations) != 1 ||
		echo.RequestObligations[0].Resolution.Action != LayerResolveDrop {
		t.Fatalf("explicit request discard plan = %+v", echo)
	}

	after := model.method(semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "invokeAfterMsg"})
	withLayer := model.method(semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "invokeWithLayer"})
	if after == nil || withLayer == nil || after.Handler || withLayer.Handler {
		t.Fatalf("wrapper methods: after=%+v withLayer=%+v", after, withLayer)
	}
	afterWrapper := after.profile(2).Wrapper
	withLayerWrapper := withLayer.profile(2).Wrapper
	if afterWrapper == nil || afterWrapper.NestedProfile != layerRPCInheritProfile {
		t.Fatalf("invokeAfterMsg wrapper = %+v", afterWrapper)
	}
	if withLayerWrapper == nil || withLayerWrapper.NestedProfile != layerRPCProfileFromField ||
		withLayerWrapper.ProfileFieldName != "layer" || withLayerWrapper.ProfileField >= withLayerWrapper.QueryFieldOrdinal {
		t.Fatalf("invokeWithLayer wrapper = %+v", withLayerWrapper)
	}
	if *afterWrapper.QuerySlot != *afterWrapper.ResultSlot || *withLayerWrapper.QuerySlot != *withLayerWrapper.ResultSlot {
		t.Fatal("wrapper query/result generic slots are not exactly bound")
	}
	if afterWrapper.QuerySlot.Name != "X" || withLayerWrapper.QuerySlot.Name != "X" ||
		afterWrapper.QuerySlot.Owner == withLayerWrapper.QuerySlot.Owner {
		t.Fatalf("generic slots were keyed by the shared name rather than owner+ordinal: after=%+v withLayer=%+v", afterWrapper.QuerySlot, withLayerWrapper.QuerySlot)
	}
	for _, fallbackCase := range []struct {
		wireID uint32
		layers []int
		method string
	}{
		{wireID: 0xda9b0d0d, layers: []int{1, 2}, method: "invokeWithLayer"},
		{wireID: 0x42000001, layers: []int{1}, method: "futureEnvelope"},
		{wireID: 0x42000002, layers: []int{2}, method: "futureEnvelope"},
		{wireID: 0x43000001, layers: []int{1, 2}, method: "profileEnvelope"},
	} {
		fallback := model.defaultWrapper(fallbackCase.wireID)
		if fallback == nil || fallback.Key.QName != fallbackCase.method || !equalLayerProfiles(fallback.Layers, fallbackCase.layers) {
			t.Fatalf("default wrapper %#08x = %+v, want profiles %v", fallbackCase.wireID, fallback, fallbackCase.layers)
		}
		for _, layer := range fallback.Layers {
			route := model.route(layer, fallbackCase.wireID)
			if route == nil || route.Method == nil || route.Method.Key.QName != fallbackCase.method {
				t.Fatalf("default wrapper %#08x profile %d route = %+v, want method %s", fallbackCase.wireID, layer, route, fallbackCase.method)
			}
		}
	}
	if fallback := model.defaultWrapper(0x21000020); fallback != nil {
		t.Fatalf("ordinary terminal entered default wrapper fallback: %+v", fallback)
	}

	request := &layerRPCRequestNode{
		WireID: 0xcb9f372d,
		Nested: &layerRPCRequestNode{
			WireID:      0xda9b0d0d,
			NestedLayer: 1,
			Nested:      &layerRPCRequestNode{WireID: 0x21000011},
		},
	}
	call, err := model.freezeCall(2, request)
	if err != nil {
		t.Fatal(err)
	}
	if call.Profile != 1 || call.Method.QName != "join" || call.WireID != 0x21000011 {
		t.Fatalf("frozen call = %+v", call)
	}
	if call.Result == nil || call.Result.WireRef == nil || call.Result.WireRef.QName != "OldJoin" || call.Result.Action != layerRPCAdapter {
		t.Fatalf("frozen result = %+v", call.Result)
	}
	if len(call.Bindings) != 2 {
		t.Fatalf("frozen generic bindings = %+v", call.Bindings)
	}
	seenOwners := map[semantic.SemanticKey]struct{}{}
	for _, binding := range call.Bindings {
		if binding.Slot.Name != "X" || binding.Slot.Ordinal != 0 || binding.Result != call.Result {
			t.Fatalf("frozen binding = %+v", binding)
		}
		seenOwners[binding.Slot.Owner] = struct{}{}
	}
	if len(seenOwners) != 2 {
		t.Fatalf("nested X bindings collided: %+v", call.Bindings)
	}
	legacyCall, err := model.freezeCall(1, &layerRPCRequestNode{WireID: 0x21000012})
	if err != nil {
		t.Fatal(err)
	}
	if legacyCall.Adapter != "adaptLegacyRequest" || legacyCall.Result == nil || legacyCall.Result.Action != layerRPCAdapter || legacyCall.Result.WireRef.QName != "Pong" {
		t.Fatalf("old-only adapter call = %+v", legacyCall)
	}

	request.Nested.NestedLayer = 999
	if _, err := model.freezeCall(2, request); err == nil || !strings.Contains(err.Error(), "unsupported exact profile 999") {
		t.Fatalf("unsupported invokeWithLayer profile error = %v", err)
	}
	if _, err := model.freezeCall(999, request); err == nil || !strings.Contains(err.Error(), "exact profile 999") {
		t.Fatalf("unsupported connection profile error = %v", err)
	}
}

func TestLayerRPCModelNeverFallsBackAcrossRejectedBoundary(t *testing.T) {
	set := layerRPCSyntheticSchemaSet(t)
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerRPCModel()
	if err != nil {
		t.Fatal(err)
	}
	request := &layerRPCRequestNode{
		WireID:      0xda9b0d0d,
		NestedLayer: 1,
		Nested:      &layerRPCRequestNode{WireID: 0x21000011},
	}
	if _, err := model.freezeCall(2, request); err == nil || !strings.Contains(err.Error(), "result") || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("unresolved result conversion error = %v", err)
	}
	if _, err := model.freezeCall(1, &layerRPCRequestNode{WireID: 0x21000012}); err == nil || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("old-only request error = %v", err)
	}
}

func TestLayerRPCModelTelegram225Through229(t *testing.T) {
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
	model, err := generator.buildLayerRPCModel()
	if err != nil {
		t.Fatal(err)
	}

	joinKey := semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "channels.joinChannel"}
	join := model.method(joinKey)
	if join == nil || !join.Handler || join.Canonical == nil || len(join.WireIDs) != 2 {
		t.Fatalf("channels.joinChannel plan = %+v", join)
	}
	for _, layer := range set.Layers() {
		profile := join.profile(layer)
		if profile == nil || profile.Result.CanonicalRef == nil || profile.Result.CanonicalRef.QName != "messages.ChatInviteJoinResult" {
			t.Fatalf("channels.joinChannel canonical result layer %d = %+v", layer, profile)
		}
		wantWire := "Updates"
		wantAction := layerRPCAdapter
		if layer >= 226 {
			wantWire = "messages.ChatInviteJoinResult"
			wantAction = layerRPCDirect
		}
		if profile.Result.WireRef == nil || profile.Result.WireRef.QName != wantWire || profile.Result.Action != wantAction {
			t.Fatalf("channels.joinChannel layer %d result = %+v, want wire=%s action=%s", layer, profile.Result, wantWire, wantAction)
		}
		route := model.route(layer, profile.WireID)
		if route == nil || route.Method != join {
			t.Fatalf("channels.joinChannel layer %d route = %+v", layer, route)
		}
	}
	for _, wrapperCase := range []struct {
		name string
		mode layerRPCNestedProfile
	}{
		{name: "invokeWithLayer", mode: layerRPCProfileFromField},
		{name: "initConnection", mode: layerRPCInheritProfile},
		{name: "invokeAfterMsg", mode: layerRPCInheritProfile},
	} {
		method := model.method(semantic.SemanticKey{Category: semantic.CategoryFunction, QName: wrapperCase.name})
		if method == nil {
			t.Fatalf("real wrapper %s is missing", wrapperCase.name)
		}
		for _, layer := range set.Layers() {
			profile := method.profile(layer)
			if profile == nil || profile.Wrapper == nil || profile.Wrapper.NestedProfile != wrapperCase.mode {
				t.Fatalf("real wrapper %s layer %d = %+v, want mode=%s", wrapperCase.name, layer, profile, wrapperCase.mode)
			}
		}
	}

	wrappers := 0
	handlers := 0
	wantRoutes := 0
	for _, layer := range set.Layers() {
		for _, definition := range set.Schemas[layer].Definitions {
			if definition.Key.Category != semantic.CategoryFunction {
				continue
			}
			wantRoutes++
			route := model.route(layer, definition.WireID)
			if route == nil || route.Method.Key != definition.Key || route.Profile.Definition != definition {
				t.Fatalf("real route layer %d wire %#08x = %+v, want %s", layer, definition.WireID, route, definition.Key)
			}
		}
	}
	if len(model.Routes) != wantRoutes {
		t.Fatalf("real RPC routes=%d, want every exact function appearance=%d", len(model.Routes), wantRoutes)
	}
	for methodIndex := range model.Methods {
		method := &model.Methods[methodIndex]
		if method.Handler {
			handlers++
		}
		canonical := method.profile(set.CanonicalLayer)
		if canonical != nil && canonical.Wrapper != nil {
			wrappers++
			if canonical.Wrapper.QuerySlot.Owner != method.Key || canonical.Wrapper.QuerySlot.Ordinal != canonical.Wrapper.ResultSlot.Ordinal {
				t.Fatalf("wrapper %s generic binding = %+v", method.Key, canonical.Wrapper)
			}
		}
	}
	if handlers == 0 || wrappers == 0 || len(model.Routes) == 0 {
		t.Fatalf("real RPC model counts: handlers=%d wrappers=%d routes=%d", handlers, wrappers, len(model.Routes))
	}
	t.Logf("Telegram Layers 225-229 RPC model: semantic_methods=%d handlers=%d wrappers=%d exact_routes=%d", len(model.Methods), handlers, wrappers, len(model.Routes))
}

func layerRPCSyntheticSchemaSet(t *testing.T) *SchemaSet {
	t.Helper()
	canonical := strings.Replace(
		layerRPCSyntheticTwo,
		"newJoin#21000003 value:int = NewJoin;",
		"oldJoin#21000002 value:int = OldJoin;\nnewJoin#21000003 value:int = NewJoin;",
		1,
	)
	profiles := make([]*semantic.SchemaModel, 0, 2)
	for _, source := range []string{layerRPCSyntheticOne, canonical} {
		parsed, err := tl.Parse(bytes.NewBufferString(source))
		if err != nil {
			t.Fatal(err)
		}
		profile, err := semantic.BuildSchema(parsed, semantic.SourceRef{
			Layer:      parsed.Layer,
			Repository: "https://example.invalid/official.git",
			Path:       "api.tl",
		})
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, profile)
	}
	set, err := NewSchemaSet(2, profiles...)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func layerRPCSyntheticPolicy(t *testing.T, set *SchemaSet) LayerObligationPolicy {
	t.Helper()
	plan, err := AnalyzeLayerConversions(set, LayerObligationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	policy := LayerObligationPolicy{}
	for _, obligation := range plan.Report.Obligations {
		if obligation.Layer != 1 || obligation.Semantic.Category != semantic.CategoryFunction {
			continue
		}
		entry := LayerObligationPolicyEntry{Key: obligation.Key}
		switch {
		case obligation.Kind == LayerObligationResult && obligation.Semantic.QName == "join":
			hook := "adaptOldJoinResult"
			if obligation.Direction == LayerDirectionProfileToCanonical {
				hook = "adaptNewJoinResult"
			}
			entry.Resolution = LayerObligationResolution{Action: LayerResolveAdapter, Hook: hook}
		case obligation.Kind == LayerObligationOldOnly && obligation.Semantic.QName == "legacy":
			entry.Resolution = LayerObligationResolution{Action: LayerResolveAdapter, Hook: "adaptLegacyRequest", Target: "function:modern"}
		case obligation.Kind == LayerObligationDiscard && obligation.Semantic.QName == "echo":
			entry.Resolution = LayerObligationResolution{Action: LayerResolveDrop}
		default:
			continue
		}
		policy.Entries = append(policy.Entries, entry)
	}
	if len(policy.Entries) != 4 {
		t.Fatalf("synthetic RPC policy entries = %+v", policy.Entries)
	}
	return policy
}

func equalLayerRPCWireIDs(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
