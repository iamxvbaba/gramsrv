package gen

import (
	"bytes"
	"testing"

	"github.com/gotd/tl"

	"github.com/iamxvbaba/td/gen/semantic"
)

const layerStaticSyntheticOne = `
---types---
leaf#10000031 flags:# enabled:flags.0?true = Leaf;
inner#10001032 value:long = Inner;
holder#10000033 leaf:Leaf = Holder;
bareHolder#10000034 inner:%inner = BareHolder;
vectorHolder#10000035 leaves:Vector<Leaf> = VectorHolder;
idOnly#10001036 value:int = IDOnly;
exact#10000037 value:int = Exact;
requires#10001038 value:int = Requires;
---functions---
getHolder#10000039 = Leaf;
// LAYER 1
`

const layerStaticSyntheticTwo = `
---types---
leaf#10000031 flags:# enabled:flags.0?true modern:flags.1?true = Leaf;
inner#10000032 value:long = Inner;
holder#10000033 leaf:Leaf = Holder;
bareHolder#10000034 inner:%inner = BareHolder;
vectorHolder#10000035 leaves:Vector<Leaf> = VectorHolder;
idOnly#10000036 value:int = IDOnly;
exact#10000037 value:int = Exact;
requires#10000038 value:int added:int = Requires;
---functions---
getHolder#10000039 = Holder;
// LAYER 2
`

const layerStaticSyntheticThree = `
---types---
leaf#10000031 flags:# enabled:flags.0?true modern:flags.1?true = Leaf;
inner#10000032 value:long = Inner;
holder#10000033 leaf:Leaf = Holder;
bareHolder#10000034 inner:%inner = BareHolder;
vectorHolder#10000035 leaves:Vector<Leaf> = VectorHolder;
idOnly#10000036 value:int = IDOnly;
exact#10000037 value:int = Exact;
requires#10000038 value:int added:int = Requires;
---functions---
getHolder#10000039 = Holder;
// LAYER 3
`

func TestLayerStaticModelDirtyReachability(t *testing.T) {
	universe := buildLayerStaticSyntheticUniverse(t)
	generator, err := NewSchemaSetGenerator(universe, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerStaticModel()
	if err != nil {
		t.Fatal(err)
	}

	if got, want := model.CanonicalLayer, 3; got != want {
		t.Fatalf("canonical layer = %d, want %d", got, want)
	}
	if got, want := model.FamilyCount, 8; got != want {
		t.Fatalf("projected families = %d, want %d", got, want)
	}
	if model.FamilyCount >= len(universe.Families) {
		t.Fatalf("projection copied the catalog: projected %d of %d families", model.FamilyCount, len(universe.Families))
	}

	layerOne := model.profile(1)
	if layerOne == nil {
		t.Fatal("layer 1 profile is missing")
	}
	assertLayerStaticMode(t, layerOne, typeStaticKey("leaf"), layerStaticObligation)
	assertLayerStaticMode(t, layerOne, typeStaticKey("inner"), layerStaticRetag)
	assertLayerStaticMode(t, layerOne, typeStaticKey("holder"), layerStaticRewrite)
	assertLayerStaticMode(t, layerOne, typeStaticKey("bareHolder"), layerStaticRewrite)
	assertLayerStaticMode(t, layerOne, typeStaticKey("vectorHolder"), layerStaticRewrite)
	assertLayerStaticMode(t, layerOne, typeStaticKey("idOnly"), layerStaticRetag)
	assertLayerStaticMode(t, layerOne, typeStaticKey("requires"), layerStaticObligation)
	assertLayerStaticMode(t, layerOne, functionStaticKey("getHolder"), layerStaticDirect)

	for _, name := range []string{"holder", "bareHolder", "vectorHolder"} {
		family := layerOne.family(typeStaticKey(name))
		if family == nil || !family.NestedDirty || family.OwnDirty {
			t.Fatalf("%s dirty state = own:%v nested:%v, want nested-only", name, family != nil && family.OwnDirty, family != nil && family.NestedDirty)
		}
	}
	if family := layerOne.family(typeStaticKey("inner")); family == nil || !family.OwnDirty || family.NestedDirty {
		t.Fatalf("inner dirty state = %+v, want own-only", family)
	}

	leaf := layerOne.family(typeStaticKey("leaf"))
	if leaf == nil || leaf.CanonicalStruct == nil || leaf.CanonicalStruct.RawName != "leaf" {
		t.Fatalf("leaf canonical structure = %+v", leaf)
	}
	if got, want := len(leaf.Fields), 2; got != want {
		t.Fatalf("leaf target field projection = %d, want %d", got, want)
	}
	if leaf.Fields[1].Canonical == nil || leaf.Fields[1].Canonical.RawName != "enabled" {
		t.Fatalf("leaf enabled canonical mapping = %+v", leaf.Fields[1])
	}
	if leaf.Fields[1].ConditionWord != "flags" || leaf.Fields[1].ConditionBit != 0 || !leaf.Fields[1].PresenceOnly {
		t.Fatalf("leaf enabled condition projection = %+v", leaf.Fields[1])
	}
	if got := layerOne.family(typeStaticKey("requires")).Obligation; got == "" {
		t.Fatal("missing required reverse field did not produce an obligation reason")
	}
	if family := layerOne.family(functionStaticKey("getHolder")); family == nil ||
		family.BodyDirty || !family.OwnDirty ||
		!family.Result.Changed || family.Result.Mode != layerStaticObligation || family.Result.Obligation == "" || family.Obligation != "" {
		t.Fatalf("result-change obligation = %+v", family)
	}

	// Layer 2 is byte-for-byte canonical for every selected family. It is
	// retained because those families need code in layer 1, but all of its
	// entries must choose the zero-work direct path.
	layerTwo := model.profile(2)
	if layerTwo == nil || len(layerTwo.Families) != model.FamilyCount {
		t.Fatalf("layer 2 projection = %+v", layerTwo)
	}
	for _, family := range layerTwo.Families {
		if family.Mode != layerStaticDirect {
			t.Fatalf("layer 2 family %s mode = %s, want direct", family.Key, family.Mode)
		}
		if family.Result.Mode != layerStaticDirect {
			t.Fatalf("layer 2 family %s result mode = %s, want direct", family.Key, family.Result.Mode)
		}
		if len(family.Fields) != 0 {
			t.Fatalf("direct family %s retained field projection", family.Key)
		}
	}

	if exact := layerOne.family(typeStaticKey("exact")); exact != nil {
		t.Fatalf("globally clean family leaked into emitter projection: %+v", exact)
	}
	if got, want := model.RewriteCount, 3; got != want {
		t.Errorf("rewrite variants = %d, want %d", got, want)
	}
}

func TestLayerStaticModelTelegram225Through232(t *testing.T) {
	universe, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSchemaSetGenerator(universe, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := generator.buildLayerStaticModel()
	if err != nil {
		t.Fatal(err)
	}

	if got, want := len(model.Profiles), len(universe.Layers()); got != want {
		t.Fatalf("profiles = %d, want %d", got, want)
	}
	if model.FamilyCount == 0 || model.FamilyCount >= len(universe.Families) {
		t.Fatalf("projected families = %d of %d, want a non-empty sparse projection", model.FamilyCount, len(universe.Families))
	}
	if model.RewriteCount == 0 {
		t.Fatal("real layer set produced no static rewrites")
	}
	if model.ObligationCount == 0 {
		t.Fatal("real layer set produced no explicit obligations")
	}
	if model.ResultObligationCount == 0 {
		t.Fatal("real layer set produced no explicit result obligations")
	}
	if got, want := model.FamilyCount, 714; got != want {
		t.Fatalf("projected families = %d, want locked schema count %d", got, want)
	}
	if got, want := model.RewriteCount, 2538; got != want {
		t.Errorf("rewrite variants = %d, want locked schema count %d", got, want)
	}
	if got, want := model.UnavailableCount, 957; got != want {
		t.Fatalf("unavailable variants = %d, want locked schema count %d", got, want)
	}
	if got, want := model.ObligationCount, 259; got != want {
		t.Errorf("body obligation variants = %d, want locked schema count %d", got, want)
	}
	if got, want := model.ResultObligationCount, 4; got != want {
		t.Fatalf("result obligation variants = %d, want locked schema count %d", got, want)
	}

	retagCount := 0
	directCount := 0
	for _, profile := range model.Profiles {
		for _, family := range profile.Families {
			switch family.Mode {
			case layerStaticDirect:
				directCount++
			case layerStaticRetag:
				retagCount++
			}
			if family.Canonical != nil && family.CanonicalStruct == nil {
				t.Fatalf("layer %d family %s lost canonical backend mapping", profile.Layer, family.Key)
			}
		}
		t.Logf(
			"layer %d: projected=%d rewrite=%d unavailable=%d obligation=%d result_obligation=%d",
			profile.Layer, len(profile.Families), profile.RewriteCount, profile.UnavailableCount, profile.ObligationCount, profile.ResultObligationCount,
		)
	}
	if directCount == 0 {
		t.Fatal("real layer set produced no direct variants")
	}
	canonical := model.profile(model.CanonicalLayer)
	if canonical == nil || canonical.RewriteCount != 0 || canonical.ObligationCount != 0 || canonical.ResultObligationCount != 0 {
		t.Fatalf("canonical profile is not zero-work: %+v", canonical)
	}
	for _, family := range canonical.Families {
		if family.Canonical == nil {
			t.Fatalf("old-only family %s leaked into canonical profile", family.Key)
		}
		if family.Mode != layerStaticDirect || family.Result.Mode != layerStaticDirect {
			t.Fatalf("canonical family %s body/result mode = %s/%s, want direct/direct", family.Key, family.Mode, family.Result.Mode)
		}
	}
	layer225 := model.profile(225)
	for _, name := range []string{"invokeWithLayer", "initConnection", "invokeAfterMsg"} {
		family := layer225.family(functionStaticKey(name))
		if family == nil || family.Mode != layerStaticRewrite || !family.NestedDirty {
			t.Fatalf("dynamic generic wrapper %s was not forced through layer-aware rewrite: %+v", name, family)
		}
	}
	t.Logf(
		"layers 225-232 static projection: families=%d/%d rewrite=%d unavailable=%d obligation=%d result_obligation=%d direct=%d retag=%d",
		model.FamilyCount, len(universe.Families), model.RewriteCount, model.UnavailableCount, model.ObligationCount, model.ResultObligationCount, directCount, retagCount,
	)
}

func TestLayerGenerationExtendsWithNewCanonicalProfile(t *testing.T) {
	base := buildLayerStaticSyntheticUniverseFrom(t, 2, layerStaticSyntheticOne, layerStaticSyntheticTwo)
	extended := buildLayerStaticSyntheticUniverseFrom(t, 3, layerStaticSyntheticOne, layerStaticSyntheticTwo, layerStaticSyntheticThree)

	// Layer 3 is schema-identical to Layer 2. Appending it and advancing the
	// canonical profile must therefore be a pure regeneration: the reviewed
	// policy for the existing semantic differences remains valid, while the
	// generated profile catalog grows automatically.
	policy := layerTestPolicy(t, base)
	baseGenerator, err := NewSchemaSetGenerator(base, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	extendedGenerator, err := NewSchemaSetGenerator(extended, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatalf("append schema-identical canonical profile required a generator or policy edit: %v", err)
	}

	baseMetadata, err := baseGenerator.buildLayerMetadata()
	if err != nil {
		t.Fatal(err)
	}
	extendedMetadata, err := extendedGenerator.buildLayerMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(extendedMetadata.Profiles), len(baseMetadata.Profiles)+1; got != want {
		t.Fatalf("extended profile count = %d, want %d", got, want)
	}
	baseIDs := make(map[string]uint64, len(baseMetadata.Families))
	for _, family := range baseMetadata.Families {
		baseIDs[family.Category+":"+family.QName] = family.ID
	}
	for _, family := range extendedMetadata.Families {
		key := family.Category + ":" + family.QName
		if want, ok := baseIDs[key]; ok && family.ID != want {
			t.Fatalf("semantic ID for %s changed from %#016x to %#016x after appending a profile", key, want, family.ID)
		}
	}

	baseCodec, err := baseGenerator.buildLayerCodecModel("fixture")
	if err != nil {
		t.Fatal(err)
	}
	extendedCodec, err := extendedGenerator.buildLayerCodecModel("fixture")
	if err != nil {
		t.Fatal(err)
	}
	extendedWires := make(map[uint32]layerCodecWire, len(extendedCodec.Wires))
	for _, wire := range extendedCodec.Wires {
		extendedWires[wire.WireID] = wire
	}
	for _, wire := range baseCodec.Wires {
		added, ok := extendedWires[wire.WireID]
		if !ok || added.EncodeName != wire.EncodeName || added.DecodeName != wire.DecodeName {
			t.Fatalf("wire %#08x static codec identity changed after appending profile: before=%s/%s after=%s/%s", wire.WireID, wire.EncodeName, wire.DecodeName, added.EncodeName, added.DecodeName)
		}
	}
}

func buildLayerStaticSyntheticUniverse(t *testing.T) *SchemaSet {
	return buildLayerStaticSyntheticUniverseFrom(t, 3, layerStaticSyntheticOne, layerStaticSyntheticTwo, layerStaticSyntheticThree)
}

func buildLayerStaticSyntheticUniverseFrom(t *testing.T, canonical int, sources ...string) *SchemaSet {
	t.Helper()
	profiles := make([]*semantic.SchemaModel, 0, len(sources))
	for _, source := range sources {
		parsed, err := tl.Parse(bytes.NewBufferString(source))
		if err != nil {
			t.Fatal(err)
		}
		profile, err := semantic.BuildSchema(parsed, semantic.SourceRef{Layer: parsed.Layer})
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, profile)
	}
	universe, err := NewSchemaSet(canonical, profiles...)
	if err != nil {
		t.Fatal(err)
	}
	return universe
}

func assertLayerStaticMode(t *testing.T, profile *layerStaticProfile, key semantic.SemanticKey, want layerStaticMode) {
	t.Helper()
	family := profile.family(key)
	if family == nil {
		t.Fatalf("layer %d family %s is missing", profile.Layer, key)
	}
	if family.Mode != want {
		t.Fatalf("layer %d family %s mode = %s, want %s (obligation: %s)", profile.Layer, key, family.Mode, want, family.Obligation)
	}
}

func typeStaticKey(name string) semantic.SemanticKey {
	return semantic.SemanticKey{Category: semantic.CategoryType, QName: name}
}

func functionStaticKey(name string) semantic.SemanticKey {
	return semantic.SemanticKey{Category: semantic.CategoryFunction, QName: name}
}
