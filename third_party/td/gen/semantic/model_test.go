package semantic

import (
	"bytes"
	"testing"

	"github.com/gotd/tl"
)

const syntheticLayerOne = `
---types---
inner#10000001 value:int = Inner;
sample#10000002 flags:# enabled:flags.0?true items:flags.1?Vector<vector<%inner>> = Sample;
---functions---
wrap#10000003 {X:Type} query:!X = X;
getSample#10000004 = Vector<Sample>;
// LAYER 1
`

const syntheticLayerTwo = `
---types---
inner#10000001 value:int = Inner;
sample#10000002 flags:# enabled:flags.0?true modern:flags.2?true items:flags.1?Vector<vector<%inner>> = Sample;
---functions---
wrap#10000003 {X:Type} query:!X = X;
getSample#10000004 = Sample;
// LAYER 2
`

func buildSynthetic(t *testing.T, source string) *SchemaModel {
	t.Helper()
	parsed, err := tl.Parse(bytes.NewBufferString(source))
	if err != nil {
		t.Fatal(err)
	}
	model, err := BuildSchema(parsed, SourceRef{Layer: parsed.Layer})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func TestBuildSchemaPreservesWireShape(t *testing.T) {
	model := buildSynthetic(t, syntheticLayerOne)
	sample := model.ByKey[DefinitionKey{Category: CategoryType, QName: "sample"}]
	if sample == nil {
		t.Fatal("sample definition is missing")
	}
	if got, want := len(sample.Fields), 3; got != want {
		t.Fatalf("fields = %d, want %d", got, want)
	}
	enabled := sample.Fields[1]
	if enabled.Condition == nil || !enabled.Condition.PresenceOnly || enabled.Condition.Word != "flags" || enabled.Condition.Bit != 0 {
		t.Fatalf("enabled condition = %+v", enabled.Condition)
	}
	items := sample.Fields[2].Type
	if got, want := items.String(), "Vector<vector<%inner>>"; got != want {
		t.Fatalf("items type = %q, want %q", got, want)
	}
	if items.Kind != TypeVector || items.Arg == nil || items.Arg.Kind != TypeVector || !items.Arg.Bare {
		t.Fatalf("nested vector was not preserved: %+v", items)
	}
	if items.Arg.Arg == nil || !items.Arg.Arg.Percent || !items.Arg.Arg.Bare || items.Arg.Arg.QName != "inner" {
		t.Fatalf("bare vector element was not preserved: %+v", items.Arg.Arg)
	}

	wrap := model.ByKey[DefinitionKey{Category: CategoryFunction, QName: "wrap"}]
	if wrap == nil || len(wrap.GenericParams) != 1 || wrap.GenericParams[0] != "X" {
		t.Fatalf("generic method = %+v", wrap)
	}
	if len(wrap.Fields) != 1 || wrap.Fields[0].Type.Kind != TypeGenericRef || wrap.Fields[0].Type.QName != "X" {
		t.Fatalf("generic query = %+v", wrap.Fields)
	}
	if wrap.Result.Kind != TypeGenericRef || wrap.Result.QName != "X" {
		t.Fatalf("generic result = %+v", wrap.Result)
	}
}

func TestShapeDigestSeparatesBodyAndResult(t *testing.T) {
	one := buildSynthetic(t, syntheticLayerOne)
	two := buildSynthetic(t, syntheticLayerTwo)

	getKey := DefinitionKey{Category: CategoryFunction, QName: "getSample"}
	getOne, getTwo := one.ByKey[getKey], two.ByKey[getKey]
	if getOne.BodyShape != getTwo.BodyShape {
		t.Fatal("result-only change altered body digest")
	}
	if getOne.SignatureShape == getTwo.SignatureShape {
		t.Fatal("result-only change did not alter signature digest")
	}
	if getOne.WireShape != getTwo.WireShape {
		t.Fatal("result-only change altered request payload wire shape")
	}

	sampleKey := DefinitionKey{Category: CategoryType, QName: "sample"}
	if got, want := one.ByKey[sampleKey].BodyShape.String(), "2d8f9745dbbe1136793864cd0e82902f5f9132027ecb120d967215f7d10511b8"; got != want {
		t.Fatalf("body digest = %s, want stable golden %s", got, want)
	}
	if got, want := one.ByKey[sampleKey].SignatureShape.String(), "ba73b8dd95ec07995b8b8e11b057d51a346838f7861aa188f50a81447a0c6b17"; got != want {
		t.Fatalf("signature digest = %s, want stable golden %s", got, want)
	}
	if one.ByKey[sampleKey].BodyShape == two.ByKey[sampleKey].BodyShape {
		t.Fatal("same-ID field change did not alter body digest")
	}
	if one.ByKey[sampleKey].WireShape != two.ByKey[sampleKey].WireShape {
		t.Fatal("presence-only field duplicated payload wire shape")
	}
	clone := buildSynthetic(t, syntheticLayerOne)
	if got, want := clone.ByKey[sampleKey].BodyShape, one.ByKey[sampleKey].BodyShape; got != want {
		t.Fatalf("body digest is unstable: %s != %s", got, want)
	}
}

func TestUniverseSyntheticDiff(t *testing.T) {
	one := buildSynthetic(t, syntheticLayerOne)
	two := buildSynthetic(t, syntheticLayerTwo)
	universe, err := NewUniverse(2, one, two)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := universe.Diff(1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(diff.SignatureChanges()), 2; got != want {
		t.Fatalf("signature changes = %d, want %d", got, want)
	}
	if got, want := len(diff.SameWireSignatureChanges()), 2; got != want {
		t.Fatalf("same-wire changes = %d, want %d", got, want)
	}
	resultOnly := diff.ResultOnlyChanges()
	if len(resultOnly) != 1 || resultOnly[0].Key.QName != "getSample" {
		t.Fatalf("result-only changes = %+v", resultOnly)
	}
	if got := universe.ByWire[1][0x10000002]; got == nil || got.Key.QName != "sample" {
		t.Fatalf("layer-aware registry lookup = %+v", got)
	}
	sampleKey := SemanticKey{Category: CategoryType, QName: "sample"}
	family := universe.Families[sampleKey]
	oneProfile, twoProfile := family.ProfilesByLayer[1], family.ProfilesByLayer[2]
	if oneProfile == nil || twoProfile == nil {
		t.Fatalf("profile variants are missing: %+v", family.ProfilesByLayer)
	}
	if oneProfile.SemanticShape == twoProfile.SemanticShape {
		t.Fatal("presence-only field was lost from profile semantics")
	}
	if oneProfile.WireCodec != twoProfile.WireCodec {
		t.Fatal("presence-only semantic change duplicated WireCodec")
	}
}

func TestUniverseRejectsWireIDConflicts(t *testing.T) {
	for _, test := range []struct {
		name string
		one  string
		two  string
	}{
		{
			name: "QName",
			one:  "alpha#10000009 value:int = Alpha;\n// LAYER 1",
			two:  "beta#10000009 value:int = Beta;\n// LAYER 2",
		},
		{
			name: "Category",
			one:  "alpha#10000009 value:int = Alpha;\n// LAYER 1",
			two:  "---functions---\nalpha#10000009 value:int = Bool;\n// LAYER 2",
		},
		{
			name: "RequiredPayload",
			one:  "sample#10000009 value:int = Sample;\n// LAYER 1",
			two:  "sample#10000009 value:long = Sample;\n// LAYER 2",
		},
		{
			name: "ConditionalPayloadBit",
			one:  "sample#10000009 flags:# value:flags.0?int = Sample;\n// LAYER 1",
			two:  "sample#10000009 flags:# value:flags.1?int = Sample;\n// LAYER 2",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			one := buildSynthetic(t, test.one)
			two := buildSynthetic(t, test.two)
			if _, err := NewUniverse(2, one, two); err == nil {
				t.Fatal("expected cross-profile wire ID conflict")
			}
		})
	}
}

func TestWireShapeIgnoresSemanticNames(t *testing.T) {
	one := buildSynthetic(t, "sample#10000009 flags:# value:flags.1?int = Sample;\n// LAYER 1")
	two := buildSynthetic(t, "sample#10000009 options:# renamed:options.1?int = Sample;\n// LAYER 2")

	key := SemanticKey{Category: CategoryType, QName: "sample"}
	if one.ByKey[key].BodyShape == two.ByKey[key].BodyShape {
		t.Fatal("semantic names were lost from semantic body shape")
	}
	if one.ByKey[key].WireShape != two.ByKey[key].WireShape {
		t.Fatal("semantic names altered exact payload wire shape")
	}
	universe, err := NewUniverse(2, one, two)
	if err != nil {
		t.Fatal(err)
	}
	if universe.Families[key].ProfilesByLayer[1].WireCodec != universe.Families[key].ProfilesByLayer[2].WireCodec {
		t.Fatal("semantic rename duplicated WireCodec")
	}
}

func TestBuildSchemaValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
	}{
		{
			name: "DuplicateWireID",
			schema: `a#10000001 = A;
b#10000001 = B;
// LAYER 1`,
		},
		{
			name: "MissingFlagsWord",
			schema: `a#10000001 value:flags.0?int = A;
// LAYER 1`,
		},
		{
			name: "FlagsWordAfterValue",
			schema: `a#10000001 value:flags.0?int flags:# = A;
// LAYER 1`,
		},
		{
			name: "UndefinedGeneric",
			schema: `---functions---
a#10000001 value:!X = X;
// LAYER 1`,
		},
		{
			name: "UnknownClass",
			schema: `a#10000001 value:Missing = A;
// LAYER 1`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := tl.Parse(bytes.NewBufferString(test.schema))
			if err != nil {
				return // Parser rejection is also a valid validation boundary.
			}
			if _, err := BuildSchema(parsed, SourceRef{Layer: 1}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCanonicalSchemaIsIndependentClone(t *testing.T) {
	one := buildSynthetic(t, syntheticLayerOne)
	two := buildSynthetic(t, syntheticLayerTwo)
	universe, err := NewUniverse(2, one, two)
	if err != nil {
		t.Fatal(err)
	}
	first := universe.CanonicalSchema()
	first.Definitions[0].Definition.Name = "mutated"
	second := universe.CanonicalSchema()
	if second.Definitions[0].Definition.Name == "mutated" {
		t.Fatal("canonical adapter leaked mutable schema state")
	}
}
