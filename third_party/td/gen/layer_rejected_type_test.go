package gen

import (
	"fmt"
	"go/format"
	"strings"
	"testing"
)

// Removing a constructor must work both when its canonical class remains an
// interface and when it collapses to one concrete type (Layer 230 passkeys).
func TestLayerRejectedHistoricalType(t *testing.T) {
	for _, singular := range []bool{false, true} {
		t.Run(fmt.Sprintf("singular=%v", singular), func(t *testing.T) {
			shared := "current#11000001 value:int = Choice;\n"
			if !singular {
				shared += "other#11000002 = Choice;\n"
			}
			method := "---functions---\nsubmit#11000004 value:Choice = Choice;\ninvokeWithLayer#da9b0d0d {X:Type} layer:int query:!X = X;\n"
			set := buildLayerStaticSyntheticUniverseFrom(t, 2,
				"---types---\n"+shared+"removed#11000003 token:string = Choice;\n"+method+"// LAYER 1\n",
				"---types---\n"+shared+method+"// LAYER 2\n")
			unreviewed, err := NewSchemaSetGenerator(set, GeneratorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := unreviewed.buildLayerWireModel(); err == nil {
				t.Fatal("unreviewed removal accepted")
			}
			generator, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: layerTestPolicy(t, set)})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := generator.buildLayerWireModel()
			if err != nil {
				t.Fatal(err)
			}
			if wire.historicalWire(1, 0x11000003) != nil {
				t.Fatal("rejected constructor acquired a canonical adapter")
			}
			codec, err := generator.buildLayerCodecModel("tg")
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range codec.Wires {
				if w.WireID == 0x11000003 {
					t.Fatal("rejected constructor acquired a typed codec")
				}
			}
			result := sourceSnapshot{}
			if err := generator.WriteTLProfileSource(result, "tlprofile", Template()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(result["tl_profile_scan_gen.go"]), "historical constructor rejected by exact-profile policy") {
				t.Fatal("scanner has no explicit rejected-constructor guard")
			}
			for name, source := range result {
				if _, err := format.Source(source); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			}
		})
	}
}
