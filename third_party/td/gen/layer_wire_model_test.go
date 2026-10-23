package gen

import (
	"bytes"
	"testing"

	"github.com/gotd/tl"

	"github.com/iamxvbaba/td/gen/semantic"
)

func TestLayerWireModelDeduplicatesWireIDAndKeepsProfileSemantics(t *testing.T) {
	set := buildLayerStaticSyntheticUniverse(t)
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerWireModel()
	if err != nil {
		t.Fatal(err)
	}
	if model.Bindings == nil || model.CanonicalLayer != 3 {
		t.Fatalf("wire model header = %+v", model)
	}
	if got, want := len(model.Wires), len(set.WireCodecs); got != want {
		t.Fatalf("wire plans = %d, want %d", got, want)
	}
	if got, want := len(model.Families), len(set.Families); got != want {
		t.Fatalf("family plans = %d, want %d", got, want)
	}

	// The same leaf ID has two presence-only semantic bodies: layer 1 lacks
	// modern:flags.1?true, while layers 2 and 3 include it. It remains one wire
	// codec with two exact body variants.
	leafWire := model.wire(0x10000031)
	if leafWire == nil || leafWire.Key != typeStaticKey("leaf") {
		t.Fatalf("leaf wire plan = %+v", leafWire)
	}
	if got, want := len(leafWire.Profiles), 3; got != want {
		t.Fatalf("leaf wire profiles = %d, want %d", got, want)
	}
	if got, want := len(leafWire.BodyVariants), 2; got != want {
		t.Fatalf("leaf body variants = %d, want %d", got, want)
	}
	if leafWire.profile(1).BodyVariant == leafWire.profile(2).BodyVariant {
		t.Fatalf("layer 1 and 2 presence semantics collapsed: %+v", leafWire.Profiles)
	}
	if leafWire.profile(2).BodyVariant != leafWire.profile(3).BodyVariant {
		t.Fatalf("identical layer 2 and 3 semantics were not grouped: %+v", leafWire.Profiles)
	}
	oldInnerWire := model.wire(0x10001032)
	if oldInnerWire == nil || oldInnerWire.profile(1).Kind == layerWireReject || oldInnerWire.profile(2).Kind != layerWireReject {
		t.Fatalf("old inner profile membership actions = %+v", oldInnerWire)
	}

	assertLayerWireFamilyKind(t, model, typeStaticKey("leaf"), 1, layerWirePolicy)
	assertLayerWireFamilyKind(t, model, typeStaticKey("inner"), 1, layerWireRetag)
	assertLayerWireFamilyKind(t, model, typeStaticKey("holder"), 1, layerWireRewrite)
	assertLayerWireFamilyKind(t, model, typeStaticKey("requires"), 1, layerWirePolicy)
	assertLayerWireFamilyKind(t, model, typeStaticKey("exact"), 1, layerWireDirect)
	assertLayerWireFamilyKind(t, model, typeStaticKey("leaf"), 3, layerWireDirect)

	result := model.family(functionStaticKey("getHolder")).profile(1)
	if result == nil || result.Kind != layerWireDirect || !result.ResultChanged || result.Conversion != generator.LayerConversionPlan().Profile(1).Family(functionStaticKey("getHolder")) {
		t.Fatalf("result-only method action = %+v", result)
	}

	leafClass := model.class("Leaf")
	if leafClass == nil || leafClass.Canonical == nil || len(leafClass.Profiles) != 3 {
		t.Fatalf("Leaf class plan = %+v", leafClass)
	}
	for _, profile := range leafClass.Profiles {
		if len(profile.Constructors) != 1 || profile.Constructors[0].Key != typeStaticKey("leaf") {
			t.Fatalf("Leaf layer %d constructors = %+v", profile.Layer, profile.Constructors)
		}
	}
}

func TestLayerWireModelRequiresCachedConversionPlan(t *testing.T) {
	parsed, err := tl.Parse(bytes.NewBufferString(layerBindingSchema))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := NewGenerator(parsed, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.buildLayerWireModel(); err == nil {
		t.Fatal("single-schema generator unexpectedly built a layer wire model")
	}

	set := buildLayerStaticSyntheticUniverse(t)
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	generator.layerPlan = nil
	if _, err := generator.buildLayerWireModel(); err == nil {
		t.Fatal("schema-set generator without its cached conversion plan unexpectedly succeeded")
	}
}

func TestLayerWireModelTelegram225Through229(t *testing.T) {
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
	model, err := generator.buildLayerWireModel()
	if err != nil {
		t.Fatal(err)
	}

	wireActions := 0
	acceptedWireActions := 0
	rejectedWireActions := 0
	bodyVariants := 0
	multiBodyWires := 0
	profileOnlyWires := 0
	for _, wire := range model.Wires {
		wireActions += len(wire.Profiles)
		for _, action := range wire.Profiles {
			if action.Kind == layerWireReject {
				rejectedWireActions++
			} else {
				acceptedWireActions++
			}
		}
		bodyVariants += len(wire.BodyVariants)
		if len(wire.BodyVariants) > 1 {
			multiBodyWires++
		}
		if wire.Canonical == nil {
			profileOnlyWires++
		}
	}
	wantWireActions := 0
	for _, layer := range set.Layers() {
		wantWireActions += len(set.ByWire[layer])
	}
	if acceptedWireActions != wantWireActions {
		t.Fatalf("accepted (profile,wire) actions = %d, want %d", acceptedWireActions, wantWireActions)
	}
	if got, want := wireActions, len(model.Wires)*len(model.Profiles); got != want {
		t.Fatalf("all (profile,wire) actions = %d, want %d", got, want)
	}
	if rejectedWireActions == 0 || acceptedWireActions+rejectedWireActions != wireActions {
		t.Fatalf("wire membership actions accepted=%d rejected=%d total=%d", acceptedWireActions, rejectedWireActions, wireActions)
	}
	if multiBodyWires == 0 {
		t.Fatal("real schemas lost all same-ID profile body variants")
	}
	pageBlockPhoto := model.wire(0x1759c560)
	if pageBlockPhoto == nil || len(pageBlockPhoto.BodyVariants) != 2 {
		t.Fatalf("pageBlockPhoto same-ID presence variants = %+v", pageBlockPhoto)
	}

	actionCounts := make(map[layerWireActionKind]int)
	semanticActions := 0
	resultChanges := 0
	for _, family := range model.Families {
		semanticActions += len(family.Profiles)
		for _, action := range family.Profiles {
			actionCounts[action.Kind]++
			if action.ResultChanged {
				resultChanges++
			}
		}
	}
	if got, want := semanticActions, len(set.Families)*len(set.Layers()); got != want {
		t.Fatalf("(profile,semantic) actions = %d, want %d", got, want)
	}
	if resultChanges != 4 {
		t.Fatalf("result-changing actions = %d, want 4", resultChanges)
	}
	if actionCounts[layerWireUnavailable] == 0 || actionCounts[layerWirePolicy] == 0 ||
		actionCounts[layerWireProfileOnly] == 0 || actionCounts[layerWireAbsent] == 0 {
		t.Fatalf("real action kinds are incomplete: %v", actionCounts)
	}

	classConstructors := 0
	for _, class := range model.Classes {
		if len(class.Profiles) != len(set.Layers()) {
			t.Fatalf("class %q profiles = %d, want %d", class.QName, len(class.Profiles), len(set.Layers()))
		}
		for _, profile := range class.Profiles {
			classConstructors += len(profile.Constructors)
		}
	}

	t.Logf(
		"Telegram Layers 225-229 wire model: profiles=%d families=%d wires=%d wire_actions=%d accepted_wire_actions=%d rejected_wire_actions=%d body_variants=%d multi_body_wires=%d classes=%d class_constructor_actions=%d profile_only_wires=%d semantic_actions=%d action_kinds=%v result_changes=%d",
		len(model.Profiles), len(model.Families), len(model.Wires), wireActions, acceptedWireActions, rejectedWireActions, bodyVariants, multiBodyWires,
		len(model.Classes), classConstructors, profileOnlyWires, semanticActions, actionCounts, resultChanges,
	)
}

func assertLayerWireFamilyKind(
	t *testing.T,
	model *layerWireModel,
	key semantic.SemanticKey,
	layer int,
	want layerWireActionKind,
) {
	t.Helper()
	family := model.family(key)
	if family == nil {
		t.Fatalf("family %s is absent", key)
	}
	action := family.profile(layer)
	if action == nil || action.Kind != want {
		t.Fatalf("layer %d family %s action = %+v, want %s", layer, key, action, want)
	}
	if action.WireIndex >= 0 {
		wire := &model.Wires[action.WireIndex]
		wireAction := wire.profile(layer)
		if wireAction == nil || wireAction.Kind != want || wireAction.FamilyIndex < 0 {
			t.Fatalf("layer %d family %s wire action = %+v", layer, key, wireAction)
		}
	}
}
