package semantic

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gotd/tl"
)

func TestMergeSchemaDeduplicatesOnlyExactDeclaration(t *testing.T) {
	target := parseMergeSchema(t, "---types---\nsample#10000001 value:int = Sample;\n// LAYER 1\n")
	overlay := parseMergeSchema(t, "---types---\nsample#10000001 value:int = Sample;\n")
	if err := mergeSchema(target, overlay); err != nil {
		t.Fatal(err)
	}
	if got, want := len(target.Definitions), 1; got != want {
		t.Fatalf("definitions = %d, want %d", got, want)
	}
}

func TestMergeSchemaRejectsWireIDCollision(t *testing.T) {
	for _, test := range []struct {
		name    string
		target  string
		overlay string
	}{
		{
			name:    "QName",
			target:  "---types---\nsample#10000001 value:int = Sample;\n// LAYER 1\n",
			overlay: "---types---\nother#10000001 value:int = Sample;\n",
		},
		{
			name:    "Category",
			target:  "---types---\nsample#10000001 value:int = Sample;\n// LAYER 1\n",
			overlay: "---functions---\nsample#10000001 value:int = Sample;\n",
		},
		{
			name:    "Payload",
			target:  "---types---\nsample#10000001 value:int = Sample;\n// LAYER 1\n",
			overlay: "---types---\nsample#10000001 value:long = Sample;\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := parseMergeSchema(t, test.target)
			overlay := parseMergeSchema(t, test.overlay)
			err := mergeSchema(target, overlay)
			if err == nil || !strings.Contains(err.Error(), "E_OVERLAY_COLLISION") || !strings.Contains(err.Error(), "0x10000001") {
				t.Fatalf("collision error = %v", err)
			}
		})
	}
}

func parseMergeSchema(t *testing.T, source string) *tl.Schema {
	t.Helper()
	schema, err := tl.Parse(bytes.NewBufferString(source))
	if err != nil {
		t.Fatal(err)
	}
	return schema
}
