package semantic

import (
	"fmt"
	"sort"
	"testing"
)

func TestTelegramLayers225Through232(t *testing.T) {
	universe, err := LoadUniverse("../../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := universe.Layers(), []int{225, 226, 227, 228, 229, 230, 231, 232}; !equalInts(got, want) {
		t.Fatalf("layers = %v, want %v", got, want)
	}

	signature := map[DefinitionKey]struct{}{}
	sameWire := map[DefinitionKey]struct{}{}
	resultOnly := map[DefinitionKey]struct{}{}
	oldOnly := map[DefinitionKey]struct{}{}
	for _, layer := range universe.Layers() {
		if layer == universe.CanonicalLayer {
			continue
		}
		diff, err := universe.Diff(layer)
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range diff.SignatureChanges() {
			signature[change.Key] = struct{}{}
		}
		for _, change := range diff.SameWireSignatureChanges() {
			sameWire[change.Key] = struct{}{}
		}
		for _, change := range diff.ResultOnlyChanges() {
			resultOnly[change.Key] = struct{}{}
		}
		for _, definition := range diff.OldOnly {
			oldOnly[definition.Key] = struct{}{}
		}
	}

	assertKeyCount(t, "signature changes", signature, 62)
	assertKeyCount(t, "same-ID signature changes", sameWire, 12)
	assertKeyCount(t, "result-only changes", resultOnly, 4)
	assertKeyCount(t, "old-only definitions", oldOnly, 18)

	semanticVariants := make([]string, 0)
	wireConflicts := 0
	for wireID, codec := range universe.WireCodecs {
		shapes := make(map[ShapeDigest]struct{})
		for _, profile := range codec.ProfileVariants {
			if profile.WireCodec != codec || profile.Definition.Key != codec.Key || profile.Definition.WireShape != codec.Shape {
				wireConflicts++
			}
			shapes[profile.SemanticShape] = struct{}{}
		}
		if len(shapes) > 1 {
			semanticVariants = append(semanticVariants, fmt.Sprintf("%08x %s", wireID, codec.Key))
		}
	}
	if wireConflicts != 0 {
		t.Fatalf("cross-profile wire conflicts = %d, want 0", wireConflicts)
	}
	if got, want := len(semanticVariants), 15; got != want {
		sort.Strings(semanticVariants)
		t.Fatalf("same-ID semantic variants = %d, want %d:\n%s", got, want, semanticVariants)
	}

	for _, key := range []DefinitionKey{
		{Category: CategoryType, QName: "chatAdminRights"},
		{Category: CategoryFunction, QName: "contacts.getTopPeers"},
		{Category: CategoryType, QName: "pageBlockPhoto"},
		{Category: CategoryType, QName: "webViewResultUrl"},
	} {
		if _, ok := sameWire[key]; !ok {
			t.Errorf("same-ID changes missing %s", key)
		}
	}
	for _, key := range []DefinitionKey{
		{Category: CategoryFunction, QName: "channels.joinChannel"},
		{Category: CategoryFunction, QName: "messages.importChatInvite"},
		{Category: CategoryFunction, QName: "account.toggleWebBrowserSettingsException"},
		{Category: CategoryFunction, QName: "messages.setBotGuestChatResult"},
	} {
		if _, ok := resultOnly[key]; !ok {
			t.Errorf("result-only changes missing %s", key)
		}
	}
	diff225, err := universe.Diff(225)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(diff225.SignatureChanges()), 53; got != want {
		t.Fatalf("layer 225 signature changes = %d, want %d", got, want)
	}
	if got, want := len(diff225.SameWireSignatureChanges()), 12; got != want {
		t.Fatalf("layer 225 same-ID changes = %d, want %d", got, want)
	}
	if got, want := len(diff225.ResultOnlyChanges()), 3; got != want {
		t.Fatalf("layer 225 result-only changes = %d, want %d", got, want)
	}
}

func assertKeyCount(t *testing.T, name string, values map[DefinitionKey]struct{}, want int) {
	t.Helper()
	if len(values) == want {
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key.String())
	}
	sort.Strings(keys)
	t.Fatalf("%s = %d, want %d:\n%s", name, len(values), want, keys)
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
