package gen

import "testing"

// layerTestPolicy resolves every non-mechanical fixture decision without
// requiring package-local adapter implementations. Semantic adapter behavior
// is covered by dedicated policy/codec tests; source snapshot tests only need
// a complete fail-closed generation policy.
func layerTestPolicy(t *testing.T, set *SchemaSet) LayerObligationPolicy {
	t.Helper()
	initial, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	policy := LayerObligationPolicy{}
	for _, obligation := range initial.LayerConversionPlan().Report.Unresolved() {
		resolution := LayerObligationResolution{Action: LayerResolveReject}
		switch obligation.Kind {
		case LayerObligationDiscard, LayerObligationUpdateProjection:
			resolution.Action = LayerResolveDrop
		case LayerObligationPrivate:
			resolution.Action = LayerResolveAllow
		}
		policy.Entries = append(policy.Entries, LayerObligationPolicyEntry{
			Key:        obligation.Key,
			Resolution: resolution,
		})
	}
	return policy
}
