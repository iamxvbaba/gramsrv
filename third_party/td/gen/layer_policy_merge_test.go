package gen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/gen/semantic"
)

func TestAuditLayerPolicyPartitionsRetainedStaleAndNew(t *testing.T) {
	report := LayerObligationReport{Obligations: []LayerObligation{
		{
			Key:       "layer-obligation/v1/required/retained",
			Kind:      LayerObligationRequired,
			Layer:     1,
			Direction: LayerDirectionCanonicalToProfile,
			Semantic:  semantic.SemanticKey{Category: semantic.CategoryType, QName: "retained"},
		},
		{
			Key:       "layer-obligation/v1/result/new",
			Kind:      LayerObligationResult,
			Layer:     2,
			Direction: LayerDirectionProfileToCanonical,
			Semantic:  semantic.SemanticKey{Category: semantic.CategoryFunction, QName: "new"},
		},
		{
			Key:        "layer-obligation/v1/new-only/mechanical",
			Kind:       LayerObligationNewOnly,
			Layer:      2,
			Direction:  LayerDirectionCanonicalToProfile,
			Semantic:   semantic.SemanticKey{Category: semantic.CategoryType, QName: "mechanical"},
			Resolution: LayerObligationResolution{Action: LayerResolveDrop},
		},
	}}
	policy := LayerObligationPolicy{Entries: []LayerObligationPolicyEntry{
		{
			Key:        "layer-obligation/v1/stale/old-shape",
			Resolution: LayerObligationResolution{Action: LayerResolveReject},
		},
		{
			Key:        report.Obligations[0].Key,
			Resolution: LayerObligationResolution{Action: LayerResolveDefault, Note: "reviewed"},
		},
	}}

	audit, err := AuditLayerPolicy(report, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Retained) != 1 || audit.Retained[0].Key != report.Obligations[0].Key {
		t.Fatalf("retained = %+v", audit.Retained)
	}
	if len(audit.Stale) != 1 || audit.Stale[0].Key != policy.Entries[0].Key {
		t.Fatalf("stale = %+v", audit.Stale)
	}
	if len(audit.New) != 1 || audit.New[0].Key != report.Obligations[1].Key || audit.New[0].Resolution.Action != "" {
		t.Fatalf("new skeleton = %+v", audit.New)
	}
	if !strings.Contains(audit.New[0].Resolution.Note, "kind=result") {
		t.Fatalf("new skeleton note = %q", audit.New[0].Resolution.Note)
	}

	merged := audit.MergedPolicy()
	if len(merged.Entries) != 2 {
		t.Fatalf("merged entries = %+v", merged.Entries)
	}
	// The unreviewed skeleton is present but cannot accidentally resolve normal
	// generation.
	if _, err := applyLayerObligationPolicy(append([]LayerObligation(nil), report.Obligations...), LayerObligationPolicy{Entries: merged.Entries}); err == nil || !strings.Contains(err.Error(), "E_INVALID_LAYER_POLICY") {
		t.Fatalf("unreviewed merged policy error = %v", err)
	}

	data, err := MarshalLayerPolicyAudit(audit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{[]byte(`"retained"`), []byte(`"stale"`), []byte(`"new"`)} {
		if !bytes.Contains(data, want) {
			t.Fatalf("audit JSON missing %s: %s", want, data)
		}
	}
	if again, err := MarshalLayerPolicyAudit(audit); err != nil || !bytes.Equal(data, again) {
		t.Fatalf("audit JSON is not deterministic: equal=%v err=%v", bytes.Equal(data, again), err)
	}
}

func TestAuditLayerPolicyValidatesRetainedEntry(t *testing.T) {
	report := LayerObligationReport{Obligations: []LayerObligation{{
		Key:  "layer-obligation/v1/required/value",
		Kind: LayerObligationRequired,
	}}}
	_, err := AuditLayerPolicy(report, LayerObligationPolicy{Entries: []LayerObligationPolicyEntry{{
		Key:        report.Obligations[0].Key,
		Resolution: LayerObligationResolution{Action: LayerResolveDrop},
	}}})
	if err == nil || !strings.Contains(err.Error(), "E_INVALID_LAYER_POLICY") {
		t.Fatalf("invalid retained action error = %v", err)
	}
}
