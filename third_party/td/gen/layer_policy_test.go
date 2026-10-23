package gen

import (
	"strings"
	"testing"
)

func TestReadLayerPolicyStrictVersionedDocument(t *testing.T) {
	policy, err := ReadLayerPolicy(strings.NewReader(`{
  "version": 1,
  "entries": [{
    "key": "layer-obligation/v1/result/example",
    "resolution": {"action": "adapter", "hook": "AdaptResult", "note": "reviewed"}
  }]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(policy.Entries), 1; got != want {
		t.Fatalf("entries = %d, want %d", got, want)
	}
	resolution := policy.Entries[0].Resolution
	if resolution.Action != LayerResolveAdapter || resolution.Hook != "AdaptResult" || resolution.Note != "reviewed" {
		t.Fatalf("resolution = %+v", resolution)
	}
}

func TestReadLayerPolicyRejectsUnknownVersionFieldsAndTrailingData(t *testing.T) {
	for _, input := range []string{
		`{"version":2,"entries":[]}`,
		`{"version":1,"entries":[],"unknown":true}`,
		`{"version":1,"entries":[]} {"version":1,"entries":[]}`,
	} {
		if _, err := ReadLayerPolicy(strings.NewReader(input)); err == nil {
			t.Fatalf("policy %q unexpectedly succeeded", input)
		}
	}
}
