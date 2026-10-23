package gen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/gen/semantic"
)

func TestMarshalLayerPolicyTemplateFailsClosed(t *testing.T) {
	report := LayerObligationReport{Obligations: []LayerObligation{
		{
			Key:         "z",
			Kind:        LayerObligationRequired,
			Layer:       2,
			Direction:   LayerDirectionCanonicalToProfile,
			Semantic:    semantic.SemanticKey{Category: semantic.CategoryType, QName: "test.value"},
			WireID:      1,
			OtherWireID: 2,
			SourceType:  "int",
			TargetType:  "long",
		},
		{
			Key:        "a",
			Kind:       LayerObligationNewOnly,
			Layer:      1,
			Resolution: LayerObligationResolution{Action: LayerResolveUnavailable},
		},
	}}

	data, err := MarshalLayerPolicyTemplate(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) || !strings.Contains(string(data), `"action": ""`) {
		t.Fatalf("policy template = %s", data)
	}
	policy, err := ReadLayerPolicy(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Entries) != 1 || policy.Entries[0].Key != "z" {
		t.Fatalf("policy entries = %+v", policy.Entries)
	}
	if _, err := applyLayerObligationPolicy([]LayerObligation{report.Obligations[0]}, policy); err == nil || !strings.Contains(err.Error(), "E_INVALID_LAYER_POLICY") {
		t.Fatalf("unedited policy unexpectedly resolved: %v", err)
	}
}

func TestLayerPolicyTemplateTelegram225Through230(t *testing.T) {
	set, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	document := BuildLayerPolicyTemplate(g.LayerConversionPlan().Report)
	if got, want := len(document.Entries), 203; got != want {
		t.Fatalf("unresolved policy entries = %d, want %d", got, want)
	}
	for i := 1; i < len(document.Entries); i++ {
		if document.Entries[i-1].Key >= document.Entries[i].Key {
			t.Fatalf("policy template is not uniquely sorted at %d", i)
		}
	}
}
