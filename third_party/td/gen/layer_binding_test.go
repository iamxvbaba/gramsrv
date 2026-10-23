package gen

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gotd/tl"

	"github.com/iamxvbaba/td/gen/semantic"
)

const layerBindingSchema = `
---types---
choiceLeft#11000001 value:int = Choice;
choiceRight#11000002 flags:# enabled:flags.0?true label:flags.1?string = Choice;
holder#11000003 choice:Choice = Holder;
---functions---
getChoice#11000004 holder:Holder = Choice;
// LAYER 1
`

func TestLayerBindingsUseExplicitIdentityNotBackendOrder(t *testing.T) {
	generator := layerBindingGenerator(t)
	reverseStructDefs(generator.structs)
	reverseInterfaceDefs(generator.interfaces)
	for i := range generator.interfaces {
		reverseStructDefs(generator.interfaces[i].Constructors)
	}

	bindings, err := generator.buildLayerBindings()
	if err != nil {
		t.Fatal(err)
	}
	canonical := generator.schemaSet.Schemas[generator.schemaSet.CanonicalLayer]
	if got, want := len(bindings.Definitions), len(canonical.Definitions); got != want {
		t.Fatalf("definition bindings = %d, want %d", got, want)
	}
	if got, want := len(bindings.Classes), len(canonical.ConstructorsByClass); got != want {
		t.Fatalf("class bindings = %d, want %d", got, want)
	}
	for _, definition := range canonical.Definitions {
		binding := bindings.definition(definition.Key)
		if binding == nil || binding.Definition != definition || binding.Structure == nil {
			t.Fatalf("binding for %s = %+v", definition.Key, binding)
		}
		if binding.Structure.RawName != definition.Key.QName {
			t.Fatalf("binding for %s points to %q", definition.Key, binding.Structure.RawName)
		}
		if got, want := len(binding.Fields), len(definition.Fields); got != want {
			t.Fatalf("field bindings for %s = %d, want %d", definition.Key, got, want)
		}
		for ordinal := range definition.Fields {
			field := &definition.Fields[ordinal]
			bound := binding.FieldByName[field.Name]
			if bound == nil || bound.Ordinal != ordinal || bound.Semantic != field || bound.Go == nil || bound.Go.RawName != field.Name {
				t.Fatalf("field binding %s.%s = %+v", definition.Key, field.Name, bound)
			}
		}
	}

	choice := bindings.class("Choice")
	if choice == nil || choice.Backend.Singular || choice.Interface == nil || len(choice.Constructors) != 2 {
		t.Fatalf("Choice class binding = %+v", choice)
	}
	holder := bindings.class("Holder")
	if holder == nil || !holder.Backend.Singular || holder.Interface != nil || len(holder.Constructors) != 1 {
		t.Fatalf("Holder class binding = %+v", holder)
	}
}

func TestLayerBindingsRejectIncompleteBackend(t *testing.T) {
	t.Run("field", func(t *testing.T) {
		generator := layerBindingGenerator(t)
		for i := range generator.structs {
			if generator.structs[i].RawName == "choiceRight" {
				generator.structs[i].Fields[1].RawName = "corrupted"
				break
			}
		}
		_, err := generator.buildLayerBindings()
		if err == nil || !strings.Contains(err.Error(), "field") {
			t.Fatalf("field corruption error = %v", err)
		}
	})

	t.Run("definition", func(t *testing.T) {
		generator := layerBindingGenerator(t)
		var first, second int
		for i := range generator.structs {
			if generator.structs[i].Vector {
				continue
			}
			if first == 0 {
				first = i + 1
			} else {
				second = i
				break
			}
		}
		generator.structs[second] = generator.structs[first-1]
		_, err := generator.buildLayerBindings()
		if err == nil || !strings.Contains(err.Error(), "backend struct matches") {
			t.Fatalf("definition corruption error = %v", err)
		}
	})

	t.Run("interface", func(t *testing.T) {
		generator := layerBindingGenerator(t)
		for i := range generator.interfaces {
			if generator.interfaces[i].RawType == "Choice" {
				generator.interfaces[i].Constructors = generator.interfaces[i].Constructors[:1]
				break
			}
		}
		_, err := generator.buildLayerBindings()
		if err == nil || !strings.Contains(err.Error(), "interface constructor count") {
			t.Fatalf("interface corruption error = %v", err)
		}
	})
}

func TestLayerBindingsRequireSchemaSet(t *testing.T) {
	parsed, err := tl.Parse(bytes.NewBufferString(layerBindingSchema))
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewGenerator(parsed, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generator.buildLayerBindings(); err == nil {
		t.Fatal("single-schema generator unexpectedly built layer bindings")
	}
}

func layerBindingGenerator(t *testing.T) *Generator {
	t.Helper()
	parsed, err := tl.Parse(bytes.NewBufferString(layerBindingSchema))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := semantic.BuildSchema(parsed, semantic.SourceRef{Layer: 1})
	if err != nil {
		t.Fatal(err)
	}
	set, err := NewSchemaSet(1, profile)
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return generator
}

func reverseStructDefs(values []structDef) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseInterfaceDefs(values []interfaceDef) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
