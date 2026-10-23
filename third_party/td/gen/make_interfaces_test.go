package gen

import "testing"

func TestMakeInterfacesIntersectsEveryConstructor(t *testing.T) {
	peer := fieldDef{Name: "Peer", Type: "PeerClass", Interface: "PeerClass"}
	pinned := fieldDef{Name: "Pinned", Type: "bool", Func: "Bool"}
	communityID := fieldDef{Name: "CommunityID", Type: "int64", Func: "Long"}

	g := &Generator{
		classes: map[string]classBinding{
			"Dialog": {Name: "DialogClass", Func: "Dialog", RawType: "Dialog"},
		},
		structs: []structDef{
			{Name: "Dialog", Interface: "DialogClass", Fields: []fieldDef{pinned, peer}},
			{Name: "DialogFolder", Interface: "DialogClass", Fields: []fieldDef{pinned, peer}},
			{Name: "DialogCommunity", Interface: "DialogClass", Fields: []fieldDef{pinned, communityID}},
		},
		mappings: map[string][]constructorMapping{},
	}

	g.makeInterfaces()
	if got, want := len(g.interfaces), 1; got != want {
		t.Fatalf("interfaces = %d, want %d", got, want)
	}
	common := g.interfaces[0].SharedFields["Common"]
	if got, want := fieldNames(common), []string{"Pinned"}; !equalStrings(got, want) {
		t.Fatalf("common fields = %v, want %v", got, want)
	}
	if got, want := fieldNames(g.structs[0].Fields), []string{"Pinned", "Peer"}; !equalStrings(got, want) {
		t.Fatalf("constructor fields mutated = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
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
