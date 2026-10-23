package gen

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLayerCodecOperationGroups(t *testing.T) {
	wire := layerCodecWire{ProfileGroups: []layerCodecProfileBody{
		{Layers: []int{225, 226}, Preflight: "scanA", Encode: "encodeA", Decode: "decodeA"},
		{Layers: []int{227}, Preflight: "scanB", Encode: "encodeA", Decode: "decodeB"},
		{Layers: []int{228}, Preflight: "scanA", Encode: "encodeB", Decode: "decodeA"},
		{Layers: []int{229}, EncodeReject: "rejected A", DecodeReject: "rejected A"},
		{Layers: []int{230}, EncodeReject: "rejected B", DecodeReject: "rejected B"},
	}}
	for _, test := range []struct {
		name   string
		groups []layerCodecProfileBody
		want   [][]int
	}{
		{"encode", wire.EncodeGroups(), [][]int{{225, 226, 227}, {228}, {229}, {230}}},
		{"decode", wire.DecodeGroups(), [][]int{{225, 226, 228}, {227}, {229}, {230}}},
		{"preflight", wire.PreflightGroups(), [][]int{{225, 226, 228}, {227}, {229}, {230}}},
		{"encode checks", wire.EncodeChecks(), [][]int{{225, 226, 227, 228}, {229}, {230}}},
		{"decode checks", wire.DecodeChecks(), [][]int{{225, 226, 227, 228}, {229}, {230}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var layers [][]int
			for _, group := range test.groups {
				layers = append(layers, group.Layers)
			}
			if !reflect.DeepEqual(layers, test.want) {
				t.Fatalf("groups = %v, want %v", layers, test.want)
			}
			test.groups[0].Layers[0] = 999
			if wire.ProfileGroups[0].Layers[0] != 225 {
				t.Fatal("grouping mutated the source profile universe")
			}
		})
	}
}

func TestLayerCodecSourceKeyPreservesSemantics(t *testing.T) {
	first := "// profile 225 policy\nb.PutID(0x1234)\nreturn value.EncodeBare(b)\n"
	second := "// profile 230 policy\nb.PutID(0x1234) // same wire\nreturn value.EncodeBare(b)\n"
	if layerCodecSourceKey(first) != layerCodecSourceKey(second) {
		t.Fatal("policy comments prevented body sharing")
	}
	for _, changed := range []string{
		strings.Replace(first, "0x1234", "0x5678", 1),
		strings.Replace(first, "EncodeBare", "DecodeBare", 1),
		"return fmt.Errorf(\"profile 225\")\n",
		"return fmt.Errorf(\"profile 230\")\n",
	} {
		if layerCodecSourceKey(first) == layerCodecSourceKey(changed) {
			t.Fatal("different wire operation was merged")
		}
	}
	if layerCodecSourceKey("return errors.New(\"reason A\")") == layerCodecSourceKey("return errors.New(\"reason B\")") {
		t.Fatal("error reasons were merged")
	}
}

func TestLayerCodecClassEncodersIgnoreOnlyWireIDs(t *testing.T) {
	profiles := []layerCodecClassProfileGroup{
		{Layers: []int{225}, Constructors: []layerCodecClassConstructorCase{{GoType: "Value", WireID: 1}, {GoType: "Value", WireID: 2}}},
		{Layers: []int{226}, Constructors: []layerCodecClassConstructorCase{{GoType: "Value", WireID: 3}}},
		{Layers: []int{227}, Constructors: []layerCodecClassConstructorCase{{GoType: "Other", WireID: 4}, {GoType: "Value", WireID: 3}}},
	}
	groups := groupLayerCodecClassEncoders(profiles)
	if len(groups) != 2 || !reflect.DeepEqual(groups[0].Layers, []int{225, 226}) || !reflect.DeepEqual(groups[1].Layers, []int{227}) {
		t.Fatalf("class groups = %+v", groups)
	}
	if profiles[0].Constructors[0].WireID != 1 || profiles[1].Constructors[0].WireID != 3 || len(profiles[0].Layers) != 1 {
		t.Fatal("encoding groups changed exact decoder routes")
	}
}

func TestGeneratedCodecProfileBranchesAreShared(t *testing.T) {
	files, err := filepath.Glob("../tlprofile/tl_profile_sparse_*_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if !strings.Contains(name, "_wire_") && !strings.Contains(name, "_families_") && !strings.Contains(name, "_classes_") {
			continue
		}
		fs := token.NewFileSet()
		f, err := parser.ParseFile(fs, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			id, ok := sw.Tag.(*ast.Ident)
			if !ok || id.Name != "profile" {
				return true
			}
			seen := map[string]token.Pos{}
			for _, statement := range sw.Body.List {
				c := statement.(*ast.CaseClause)
				if c.List == nil {
					continue
				}
				var body bytes.Buffer
				if err := format.Node(&body, token.NewFileSet(), c.Body); err != nil {
					t.Fatal(err)
				}
				key := body.String()
				if previous, ok := seen[key]; ok {
					t.Errorf("duplicate profile case bodies at %s and %s", fs.Position(previous), fs.Position(c.Pos()))
				}
				seen[key] = c.Pos()
			}
			return true
		})
	}
}
