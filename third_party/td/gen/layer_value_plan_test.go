package gen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gotd/tl"

	"github.com/iamxvbaba/td/gen/semantic"
)

const layerValueSyntheticOne = `
---types---
bareItem#11000001 value:long = BareItem;
single#11000002 value:int = Single;
leafA#11000003 value:int = Leaf;
leafB#11000004 value:string = Leaf;
variantA#11000009 value:int = Variant;
holder#11000005 primitive:int exact:%bareItem concrete:Single abstract:Leaf variant:Variant nested:Vector<vector<%bareItem>> = Holder;
legacy#11000007 value:int = Legacy;
---functions---
wrap#11000006 {X:Type} query:!X = X;
// LAYER 1
`

const layerValueSyntheticTwo = `
---types---
bareItem#11000001 value:long = BareItem;
single#11000002 value:int = Single;
leafA#11000003 value:int = Leaf;
leafB#11000004 value:string = Leaf;
leafC#11000008 value:long = Leaf;
variantA#11000009 value:int = Variant;
variantB#1100000a value:string = Variant;
holder#11000005 primitive:int exact:%bareItem concrete:Single abstract:Leaf variant:Variant nested:Vector<vector<%bareItem>> = Holder;
modernLegacy#1100000b value:int = Legacy;
---functions---
wrap#11000006 {X:Type} query:!X = X;
// LAYER 2
`

func TestLayerValuePlanClassifiesStaticAndDynamicValues(t *testing.T) {
	compiler, schemaSet := newLayerValueSyntheticCompiler(t)
	profile := schemaSet.Schemas[1]
	holder := profile.ByKey[semantic.SemanticKey{Category: semantic.CategoryType, QName: "holder"}]
	if holder == nil {
		t.Fatal("holder definition is missing")
	}

	primitiveRef := layerValueFieldRef(t, holder, "primitive")
	primitive := mustCompileLayerValue(t, compiler, 1, primitiveRef)
	if primitive.Kind != layerValuePrimitive || primitive.Primitive != "int" || primitive.Ref != primitiveRef {
		t.Fatalf("primitive plan = %+v", primitive)
	}

	exactRef := layerValueFieldRef(t, holder, "exact")
	exact := mustCompileLayerValue(t, compiler, 1, exactRef)
	assertLayerValueConstructors(t, exact, layerValueExactBare, "bareItem")
	if exact.Constructors[0].Canonical == nil || exact.Constructors[0].Canonical.Structure.RawName != "bareItem" {
		t.Fatalf("exact canonical binding = %+v", exact.Constructors[0].Canonical)
	}
	bareRef := &semantic.TypeRef{Kind: semantic.TypeNamed, QName: "bareItem", Bare: true}
	assertLayerValueConstructors(t, mustCompileLayerValue(t, compiler, 1, bareRef), layerValueExactBare, "bareItem")

	concrete := mustCompileLayerValue(t, compiler, 1, layerValueFieldRef(t, holder, "concrete"))
	assertLayerValueConstructors(t, concrete, layerValueBoxedConcrete, "single")
	if concrete.CanonicalClass == nil || concrete.CanonicalClass.Interface != nil {
		t.Fatalf("concrete canonical class = %+v", concrete.CanonicalClass)
	}

	abstract := mustCompileLayerValue(t, compiler, 1, layerValueFieldRef(t, holder, "abstract"))
	assertLayerValueConstructors(t, abstract, layerValueBoxedAbstract, "leafA", "leafB")
	if abstract.CanonicalClass == nil || abstract.CanonicalClass.Interface == nil {
		t.Fatalf("abstract canonical class = %+v", abstract.CanonicalClass)
	}
	canonicalLeaf := &semantic.TypeRef{Kind: semantic.TypeNamed, QName: "Leaf"}
	assertLayerValueConstructors(
		t,
		mustCompileLayerValue(t, compiler, 2, canonicalLeaf),
		layerValueBoxedAbstract,
		"leafA", "leafB", "leafC",
	)

	// Layer 1 has one wire constructor, but canonical Layer 2 exposes an
	// interface with two constructors. The plan must retain the canonical Go
	// interface shape instead of misclassifying this profile as concrete.
	variant := mustCompileLayerValue(t, compiler, 1, layerValueFieldRef(t, holder, "variant"))
	assertLayerValueConstructors(t, variant, layerValueBoxedAbstract, "variantA")
	if variant.CanonicalClass == nil || variant.CanonicalClass.Backend.Singular || variant.CanonicalClass.Interface == nil {
		t.Fatalf("shrunk profile lost canonical abstract binding: %+v", variant.CanonicalClass)
	}

	nestedRef := layerValueFieldRef(t, holder, "nested")
	nested := mustCompileLayerValue(t, compiler, 1, nestedRef)
	if nested.Kind != layerValueVector || nested.VectorMode != layerValueVectorBoxed || nested.Ref != nestedRef {
		t.Fatalf("outer vector plan = %+v", nested)
	}
	if nested.Element == nil || nested.Element.Kind != layerValueVector || nested.Element.VectorMode != layerValueVectorBare {
		t.Fatalf("middle bare vector plan = %+v", nested.Element)
	}
	innermost := nested.Element.Element
	assertLayerValueConstructors(t, innermost, layerValueExactBare, "bareItem")
	if innermost.Ref != nestedRef.Arg.Arg {
		t.Fatal("nested value plan copied rather than referenced the semantic TypeRef")
	}

	wrap := profile.ByKey[semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "wrap"}]
	if wrap == nil {
		t.Fatal("wrap definition is missing")
	}
	genericRef := layerValueFieldRef(t, wrap, "query")
	generic := mustCompileLayerValue(t, compiler, 1, genericRef)
	if generic.Kind != layerValueDynamicGeneric || generic.GenericParam != "X" || generic.Ref != genericRef ||
		generic.Element != nil || len(generic.Constructors) != 0 {
		t.Fatalf("generic plan = %+v", generic)
	}

	objectRef := &semantic.TypeRef{Kind: semantic.TypePrimitive, QName: "Object"}
	object := mustCompileLayerValue(t, compiler, 1, objectRef)
	if object.Kind != layerValueDynamicObject || object.Ref != objectRef || object.Element != nil || len(object.Constructors) != 0 {
		t.Fatalf("dynamic Object plan = %+v", object)
	}

	legacyRef := &semantic.TypeRef{Kind: semantic.TypeNamed, QName: "Legacy"}
	legacy := mustCompileLayerValue(t, compiler, 1, legacyRef)
	assertLayerValueConstructors(t, legacy, layerValueBoxedConcrete, "legacy")
	if legacy.CanonicalClass == nil || !legacy.CanonicalClass.Backend.Singular {
		t.Fatalf("historical singular class lost canonical binding: %+v", legacy.CanonicalClass)
	}
	legacyConstructor := legacy.Constructors[0]
	if legacyConstructor.Canonical == nil || legacyConstructor.Canonical.Key.QName != "modernLegacy" ||
		legacyConstructor.Conversion.Availability != LayerAvailabilityProfileOnly {
		t.Fatalf("historical constructor target = %+v", legacyConstructor)
	}
}

func TestLayerValuePlanSupportsArbitraryVectorDepth(t *testing.T) {
	compiler, _ := newLayerValueSyntheticCompiler(t)
	root := &semantic.TypeRef{Kind: semantic.TypePrimitive, QName: "int"}
	const depth = 96
	for i := 0; i < depth; i++ {
		if i%2 == 0 {
			root = &semantic.TypeRef{Kind: semantic.TypeVector, QName: "Vector", Arg: root}
		} else {
			root = &semantic.TypeRef{Kind: semantic.TypeVector, QName: "vector", Bare: true, Arg: root}
		}
	}

	plan := mustCompileLayerValue(t, compiler, 1, root)
	for i := 0; i < depth; i++ {
		if plan == nil || plan.Kind != layerValueVector || plan.Element == nil {
			t.Fatalf("vector depth %d plan = %+v", i, plan)
		}
		wantMode := layerValueVectorBare
		// The root is the last vector appended. With an even depth it is bare.
		if (depth-1-i)%2 == 0 {
			wantMode = layerValueVectorBoxed
		}
		if plan.VectorMode != wantMode {
			t.Fatalf("vector depth %d mode = %s, want %s", i, plan.VectorMode, wantMode)
		}
		plan = plan.Element
	}
	if plan.Kind != layerValuePrimitive || plan.Primitive != "int" {
		t.Fatalf("deep vector leaf = %+v", plan)
	}
}

func TestLayerValuePlanRejectsMalformedOrUnknownReferences(t *testing.T) {
	compiler, _ := newLayerValueSyntheticCompiler(t)
	primitive := &semantic.TypeRef{Kind: semantic.TypePrimitive, QName: "int"}
	cycle := &semantic.TypeRef{Kind: semantic.TypeVector, QName: "Vector"}
	cycle.Arg = cycle

	tests := []struct {
		name string
		ref  *semantic.TypeRef
		want string
	}{
		{name: "nil", want: "nil TypeRef"},
		{name: "primitive argument", ref: &semantic.TypeRef{Kind: semantic.TypePrimitive, QName: "int", Arg: primitive}, want: "malformed primitive"},
		{name: "object argument", ref: &semantic.TypeRef{Kind: semantic.TypePrimitive, QName: "Object", Arg: primitive}, want: "malformed primitive"},
		{name: "bare object", ref: &semantic.TypeRef{Kind: semantic.TypePrimitive, QName: "Object", Bare: true}, want: "malformed dynamic Object"},
		{name: "generic argument", ref: &semantic.TypeRef{Kind: semantic.TypeGenericRef, QName: "X", Arg: primitive}, want: "malformed generic"},
		{name: "vector element", ref: &semantic.TypeRef{Kind: semantic.TypeVector, QName: "Vector"}, want: "no element"},
		{name: "vector spelling", ref: &semantic.TypeRef{Kind: semantic.TypeVector, QName: "Sequence", Arg: primitive}, want: "unsupported vector spelling"},
		{name: "unknown bare", ref: &semantic.TypeRef{Kind: semantic.TypeNamed, QName: "missing", Bare: true}, want: "target constructor"},
		{name: "unknown class", ref: &semantic.TypeRef{Kind: semantic.TypeNamed, QName: "Missing"}, want: "has no constructors"},
		{name: "named argument", ref: &semantic.TypeRef{Kind: semantic.TypeNamed, QName: "Leaf", Arg: primitive}, want: "malformed named"},
		{name: "cycle", ref: cycle, want: "cyclic TypeRef"},
		{name: "kind", ref: &semantic.TypeRef{Kind: semantic.TypeKind(255), QName: "invalid"}, want: "unsupported TypeRef kind"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := compiler.Compile(1, test.ref)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
	if _, err := compiler.Compile(999, primitive); err == nil || !strings.Contains(err.Error(), "is not generated") {
		t.Fatalf("unknown profile error = %v", err)
	}
}

func TestLayerValueCompilerConsumesGeneratorConversionPlan(t *testing.T) {
	schemaSet := layerValueSyntheticSchemaSet(t)
	generator := newLayerValueSyntheticGenerator(t, schemaSet)
	if generator.LayerConversionPlan() == nil {
		t.Fatal("schema-set generator did not cache its conversion plan")
	}
	compiler, err := generator.newLayerValueCompiler()
	if err != nil {
		t.Fatal(err)
	}
	if compiler.conversions != generator.LayerConversionPlan() {
		t.Fatal("value compiler rebuilt or copied the generator conversion plan")
	}

	legacy, err := NewGenerator(schemaSet.CanonicalSchema(), GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.newLayerValueCompiler(); err == nil || !strings.Contains(err.Error(), "schema-set generator") {
		t.Fatalf("legacy generator error = %v", err)
	}
}

func TestLayerValuePlanTelegram225Through229(t *testing.T) {
	schemaSet, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := LoadLayerPolicy("../_schema/layers/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSchemaSetGenerator(schemaSet, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := generator.newLayerValueCompiler()
	if err != nil {
		t.Fatal(err)
	}

	counts := make(map[layerValueKind]int)
	profileOnly := 0
	compiled := 0
	for _, layer := range schemaSet.Layers() {
		schema := schemaSet.Schemas[layer]
		for _, definition := range schema.Definitions {
			for fieldIndex := range definition.Fields {
				field := &definition.Fields[fieldIndex]
				if field.Kind == semantic.FieldFlagsWord {
					continue
				}
				plan := mustCompileLayerValue(t, compiler, layer, &field.Type)
				countLayerValuePlan(plan, counts, &profileOnly)
				compiled++
			}
			plan := mustCompileLayerValue(t, compiler, layer, &definition.Result)
			countLayerValuePlan(plan, counts, &profileOnly)
			compiled++
		}
	}
	if compiled == 0 {
		t.Fatal("real schema set produced no value plans")
	}
	for _, kind := range []layerValueKind{
		layerValuePrimitive,
		layerValueBoxedConcrete,
		layerValueBoxedAbstract,
		layerValueVector,
		layerValueDynamicGeneric,
	} {
		if counts[kind] == 0 {
			t.Errorf("real schema set produced no %s value plans; counts=%v", kind, counts)
		}
	}
	t.Logf("Telegram Layers 225-229 value plans: roots=%d nodes=%v profile_only_constructors=%d", compiled, counts, profileOnly)
}

func newLayerValueSyntheticCompiler(t *testing.T) (*layerValueCompiler, *SchemaSet) {
	t.Helper()
	schemaSet := layerValueSyntheticSchemaSet(t)
	generator := newLayerValueSyntheticGenerator(t, schemaSet)
	compiler, err := generator.newLayerValueCompiler()
	if err != nil {
		t.Fatal(err)
	}
	return compiler, schemaSet
}

func newLayerValueSyntheticGenerator(t *testing.T, schemaSet *SchemaSet) *Generator {
	t.Helper()
	initial, err := NewSchemaSetGenerator(schemaSet, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	policy := LayerObligationPolicy{}
	for _, obligation := range initial.LayerConversionPlan().Report.Unresolved() {
		if obligation.Kind != LayerObligationOldOnly || obligation.Semantic.QName != "legacy" {
			continue
		}
		policy.Entries = append(policy.Entries, LayerObligationPolicyEntry{
			Key: obligation.Key,
			Resolution: LayerObligationResolution{
				Action: LayerResolveAdapter,
				Hook:   "layerSyntheticLegacy",
				Target: "type:modernLegacy",
			},
		})
	}
	generator, err := NewSchemaSetGenerator(schemaSet, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	return generator
}

func layerValueSyntheticSchemaSet(t *testing.T) *SchemaSet {
	t.Helper()
	profiles := make([]*semantic.SchemaModel, 0, 2)
	for _, source := range []string{layerValueSyntheticOne, layerValueSyntheticTwo} {
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
	schemaSet, err := NewSchemaSet(2, profiles...)
	if err != nil {
		t.Fatal(err)
	}
	return schemaSet
}

func layerValueFieldRef(t *testing.T, definition *semantic.Definition, name string) *semantic.TypeRef {
	t.Helper()
	for index := range definition.Fields {
		field := &definition.Fields[index]
		if field.Kind == semantic.FieldValue && field.Name == name {
			return &field.Type
		}
	}
	t.Fatalf("field %q is missing from %s", name, definition.Key)
	return nil
}

func mustCompileLayerValue(t *testing.T, compiler *layerValueCompiler, layer int, ref *semantic.TypeRef) *layerValuePlan {
	t.Helper()
	plan, err := compiler.Compile(layer, ref)
	if err != nil {
		t.Fatalf("compile layer %d TypeRef %s: %v", layer, safeLayerValueTypeRef(ref), err)
	}
	return plan
}

func safeLayerValueTypeRef(ref *semantic.TypeRef) string {
	if ref == nil {
		return "<nil>"
	}
	return ref.String()
}

func assertLayerValueConstructors(t *testing.T, plan *layerValuePlan, kind layerValueKind, qnames ...string) {
	t.Helper()
	if plan == nil || plan.Kind != kind || len(plan.Constructors) != len(qnames) {
		t.Fatalf("constructor plan = %+v, want kind=%s constructors=%v", plan, kind, qnames)
	}
	for index, qname := range qnames {
		constructor := plan.Constructors[index]
		if constructor.Conversion == nil || constructor.Conversion.Key.QName != qname {
			t.Fatalf("constructor %d = %+v, want %q", index, constructor, qname)
		}
	}
}

func countLayerValuePlan(plan *layerValuePlan, counts map[layerValueKind]int, profileOnly *int) {
	if plan == nil {
		return
	}
	counts[plan.Kind]++
	for _, constructor := range plan.Constructors {
		if constructor.Conversion.Availability == LayerAvailabilityProfileOnly {
			(*profileOnly)++
		}
	}
	countLayerValuePlan(plan.Element, counts, profileOnly)
}
