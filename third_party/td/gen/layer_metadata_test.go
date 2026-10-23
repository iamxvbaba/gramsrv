package gen

import (
	"reflect"
	"testing"

	"github.com/iamxvbaba/td/gen/semantic"
)

func TestLayerMetadataUsesSparseProfileOverrides(t *testing.T) {
	set, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := LoadLayerPolicy("../_schema/layers/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := g.buildLayerMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := metadata.Profiles, []int{225, 226, 227, 228, 229, 230, 231, 232}; !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}
	if got, want := len(metadata.Wires), len(set.WireCodecs); got != want {
		t.Fatalf("wire entries = %d, want %d", got, want)
	}
	if got, want := len(metadata.Families), len(set.Families); got != want {
		t.Fatalf("semantic entries = %d, want %d", got, want)
	}

	constants := make(map[string]struct{}, len(metadata.Families))
	for _, family := range metadata.Families {
		if _, duplicate := constants[family.Constant]; duplicate {
			t.Fatalf("duplicate semantic constant %q", family.Constant)
		}
		constants[family.Constant] = struct{}{}
	}

	canonicalEntries := len(metadata.Families) * (len(metadata.Profiles) - 1)
	overrides := 0
	for _, profile := range metadata.Overrides {
		overrides += len(profile.Entries)
	}
	if overrides == 0 || overrides >= canonicalEntries {
		t.Fatalf("profile overrides = %d, want sparse non-zero set below %d", overrides, canonicalEntries)
	}
}

func TestLayerSemanticStableID(t *testing.T) {
	key := semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "messages.sendMessage"}
	if got, want := layerSemanticStableID(key), uint64(0x78525a6b737529bd); got != want {
		t.Fatalf("stable semantic id = %#016x, want %#016x", got, want)
	}
	if got := layerSemanticStableID(semantic.SemanticKey{Category: semantic.CategoryType, QName: key.QName}); got == layerSemanticStableID(key) {
		t.Fatalf("type/function category collision for %s: %#016x", key.QName, got)
	}
}
