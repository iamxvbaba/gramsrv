package gen

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/iamxvbaba/td/gen/semantic"
)

// layerBindingIndex is the explicit bridge from the category-qualified
// semantic model to the canonical Go backend. It is built by identity, never
// by assuming that semantic definitions and generated structs have the same
// slice order.
type layerBindingIndex struct {
	Definitions map[semantic.SemanticKey]*layerDefinitionBinding
	Classes     map[string]*layerClassBinding
}

// layerDefinitionBinding joins one canonical schema definition to the Go
// struct and fields which represent it.
type layerDefinitionBinding struct {
	Key         semantic.SemanticKey
	Definition  *semantic.Definition
	Structure   *structDef
	Fields      []layerFieldBinding
	FieldByName map[string]*layerFieldBinding
}

// layerFieldBinding is an ordinal-preserving semantic-to-Go field binding.
type layerFieldBinding struct {
	Ordinal  int
	Semantic *semantic.FieldShape
	Go       *fieldDef
}

// layerClassBinding joins a TL result class to either its singular canonical
// struct or its generated canonical interface and exact constructor set.
type layerClassBinding struct {
	QName        string
	Backend      classBinding
	Interface    *interfaceDef
	Constructors []*layerDefinitionBinding
}

func (i *layerBindingIndex) definition(key semantic.SemanticKey) *layerDefinitionBinding {
	if i == nil {
		return nil
	}
	return i.Definitions[key]
}

func (i *layerBindingIndex) class(qname string) *layerClassBinding {
	if i == nil {
		return nil
	}
	return i.Classes[qname]
}

type layerStructIdentity struct {
	QName  string
	WireID uint32
}

// buildLayerBindings validates and indexes every canonical schema-backed
// struct, field, class and interface. Synthetic Vector<T> result boxes are
// deliberately excluded because they have no semantic definition.
func (g *Generator) buildLayerBindings() (*layerBindingIndex, error) {
	if g == nil || g.schemaSet == nil {
		return nil, fmt.Errorf("gen: layer bindings require a schema-set generator")
	}
	canonical := g.schemaSet.Schemas[g.schemaSet.CanonicalLayer]
	if canonical == nil {
		return nil, fmt.Errorf("gen: canonical semantic layer %d is absent", g.schemaSet.CanonicalLayer)
	}

	structs := make(map[layerStructIdentity][]*structDef, len(canonical.Definitions))
	schemaBackedCount := 0
	for index := range g.structs {
		structure := &g.structs[index]
		if structure.Vector {
			continue
		}
		wireID, err := parseLayerBindingWireID(structure.HexID)
		if err != nil {
			return nil, fmt.Errorf("gen: canonical backend struct %q: %w", structure.Name, err)
		}
		identity := layerStructIdentity{QName: structure.RawName, WireID: wireID}
		structs[identity] = append(structs[identity], structure)
		schemaBackedCount++
	}
	if schemaBackedCount != len(canonical.Definitions) {
		return nil, fmt.Errorf(
			"gen: canonical semantic/backend definition count mismatch: semantic=%d backend=%d (all backend structs=%d)",
			len(canonical.Definitions), schemaBackedCount, len(g.structs),
		)
	}

	index := &layerBindingIndex{
		Definitions: make(map[semantic.SemanticKey]*layerDefinitionBinding, len(canonical.Definitions)),
		Classes:     make(map[string]*layerClassBinding, len(canonical.ConstructorsByClass)),
	}
	usedStructs := make(map[*structDef]semantic.SemanticKey, len(canonical.Definitions))
	qnameOwners := make(map[string]semantic.SemanticKey, len(canonical.Definitions))
	for _, definition := range canonical.Definitions {
		if previous, duplicate := qnameOwners[definition.Key.QName]; duplicate && previous != definition.Key {
			return nil, fmt.Errorf(
				"gen: canonical backend cannot disambiguate category-qualified definitions %s and %s with the same qname",
				previous, definition.Key,
			)
		}
		qnameOwners[definition.Key.QName] = definition.Key

		identity := layerStructIdentity{QName: definition.Key.QName, WireID: definition.WireID}
		candidates := structs[identity]
		if len(candidates) != 1 {
			return nil, fmt.Errorf(
				"gen: canonical definition %s wire %#08x has %d backend struct matches, want exactly one",
				definition.Key, definition.WireID, len(candidates),
			)
		}
		structure := candidates[0]
		if previous, duplicate := usedStructs[structure]; duplicate {
			return nil, fmt.Errorf("gen: canonical backend struct %q binds both %s and %s", structure.Name, previous, definition.Key)
		}
		usedStructs[structure] = definition.Key

		typeInfo, ok := g.types[definition.Key.QName]
		if !ok {
			return nil, fmt.Errorf("gen: canonical definition %s has no Go type binding", definition.Key)
		}
		if structure.Name != typeInfo.Name {
			return nil, fmt.Errorf(
				"gen: canonical definition %s backend name %q does not match binding %q",
				definition.Key, structure.Name, typeInfo.Name,
			)
		}
		if definition.Key.Category == semantic.CategoryFunction && typeInfo.Method == "" {
			return nil, fmt.Errorf("gen: canonical function %s has no method binding", definition.Key)
		}
		if definition.Key.Category == semantic.CategoryType && typeInfo.Method != "" {
			return nil, fmt.Errorf("gen: canonical type %s unexpectedly has method binding %q", definition.Key, typeInfo.Method)
		}

		binding, err := bindLayerDefinition(definition, structure)
		if err != nil {
			return nil, err
		}
		if _, duplicate := index.Definitions[definition.Key]; duplicate {
			return nil, fmt.Errorf("gen: duplicate canonical definition binding for %s", definition.Key)
		}
		index.Definitions[definition.Key] = binding
	}
	if len(usedStructs) != schemaBackedCount {
		return nil, fmt.Errorf(
			"gen: %d canonical backend structs were not bound to a semantic definition",
			schemaBackedCount-len(usedStructs),
		)
	}

	if err := g.bindLayerClasses(canonical, index); err != nil {
		return nil, err
	}
	return index, nil
}

func parseLayerBindingWireID(value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid hexadecimal wire id %q: %w", value, err)
	}
	return uint32(parsed), nil
}

func bindLayerDefinition(definition *semantic.Definition, structure *structDef) (*layerDefinitionBinding, error) {
	if len(definition.Fields) != len(structure.Fields) {
		return nil, fmt.Errorf(
			"gen: canonical field count mismatch for %s: semantic=%d backend=%d",
			definition.Key, len(definition.Fields), len(structure.Fields),
		)
	}
	binding := &layerDefinitionBinding{
		Key:         definition.Key,
		Definition:  definition,
		Structure:   structure,
		Fields:      make([]layerFieldBinding, 0, len(definition.Fields)),
		FieldByName: make(map[string]*layerFieldBinding, len(definition.Fields)),
	}
	for ordinal := range definition.Fields {
		semanticField := &definition.Fields[ordinal]
		goField := &structure.Fields[ordinal]
		if goField.RawName != semanticField.Name {
			return nil, fmt.Errorf(
				"gen: canonical field %d mismatch for %s: semantic=%q backend=%q",
				ordinal, definition.Key, semanticField.Name, goField.RawName,
			)
		}
		if semanticField.Ordinal != ordinal {
			return nil, fmt.Errorf(
				"gen: canonical field %q of %s has semantic ordinal %d, want %d",
				semanticField.Name, definition.Key, semanticField.Ordinal, ordinal,
			)
		}
		if semanticField.Kind == semantic.FieldFlagsWord {
			if goField.Type != flagsType || goField.Conditional {
				return nil, fmt.Errorf("gen: canonical flags field %q of %s has incompatible Go binding", semanticField.Name, definition.Key)
			}
		} else {
			if goField.Type == flagsType {
				return nil, fmt.Errorf("gen: canonical value field %q of %s is bound as flags", semanticField.Name, definition.Key)
			}
			if got, want := goField.RawType, semanticField.Type.String(); got != want {
				return nil, fmt.Errorf(
					"gen: canonical field %q of %s type mismatch: semantic=%q backend=%q",
					semanticField.Name, definition.Key, want, got,
				)
			}
		}
		if err := validateLayerFieldCondition(definition.Key, semanticField, goField); err != nil {
			return nil, err
		}

		binding.Fields = append(binding.Fields, layerFieldBinding{
			Ordinal:  ordinal,
			Semantic: semanticField,
			Go:       goField,
		})
		fieldBinding := &binding.Fields[len(binding.Fields)-1]
		if _, duplicate := binding.FieldByName[semanticField.Name]; duplicate {
			return nil, fmt.Errorf("gen: duplicate canonical field binding %q for %s", semanticField.Name, definition.Key)
		}
		binding.FieldByName[semanticField.Name] = fieldBinding
	}
	return binding, nil
}

func validateLayerFieldCondition(key semantic.SemanticKey, semanticField *semantic.FieldShape, goField *fieldDef) error {
	condition := semanticField.Condition
	if condition == nil {
		if goField.Conditional || goField.ConditionalBool {
			return fmt.Errorf("gen: unconditional field %q of %s has conditional Go binding", semanticField.Name, key)
		}
		return nil
	}
	if !goField.Conditional {
		return fmt.Errorf("gen: conditional field %q of %s has unconditional Go binding", semanticField.Name, key)
	}
	if goField.ConditionalField != pascal(condition.Word) || goField.ConditionalIndex != int(condition.Bit) {
		return fmt.Errorf(
			"gen: condition mismatch for field %q of %s: semantic=%s.%d backend=%s.%d",
			semanticField.Name, key, condition.Word, condition.Bit, goField.ConditionalField, goField.ConditionalIndex,
		)
	}
	if goField.ConditionalBool != condition.PresenceOnly {
		return fmt.Errorf(
			"gen: presence-only mismatch for field %q of %s: semantic=%v backend=%v",
			semanticField.Name, key, condition.PresenceOnly, goField.ConditionalBool,
		)
	}
	return nil
}

func (g *Generator) bindLayerClasses(canonical *semantic.SchemaModel, index *layerBindingIndex) error {
	classNames := make([]string, 0, len(canonical.ConstructorsByClass))
	for qname := range canonical.ConstructorsByClass {
		classNames = append(classNames, qname)
	}
	sort.Strings(classNames)

	interfaces := make(map[string][]*interfaceDef, len(g.interfaces))
	for i := range g.interfaces {
		definition := &g.interfaces[i]
		interfaces[definition.RawType] = append(interfaces[definition.RawType], definition)
	}
	usedInterfaces := make(map[*interfaceDef]string, len(g.interfaces))
	seenBackendClasses := 0
	for _, qname := range classNames {
		backend, ok := g.classes[qname]
		if !ok {
			return fmt.Errorf("gen: canonical class %q has no Go class binding", qname)
		}
		if backend.Vector {
			return fmt.Errorf("gen: canonical schema class %q unexpectedly uses a synthetic vector binding", qname)
		}
		seenBackendClasses++
		keys := append([]semantic.SemanticKey(nil), canonical.ConstructorsByClass[qname]...)
		sortSemanticKeys(keys)
		class := &layerClassBinding{
			QName:        qname,
			Backend:      backend,
			Constructors: make([]*layerDefinitionBinding, 0, len(keys)),
		}
		for _, key := range keys {
			constructor := index.Definitions[key]
			if constructor == nil {
				return fmt.Errorf("gen: canonical class %q constructor %s has no definition binding", qname, key)
			}
			class.Constructors = append(class.Constructors, constructor)
		}
		if len(class.Constructors) == 0 {
			return fmt.Errorf("gen: canonical class %q has no constructors", qname)
		}

		if backend.Singular {
			if len(class.Constructors) != 1 {
				return fmt.Errorf("gen: singular canonical class %q has %d constructors", qname, len(class.Constructors))
			}
			if backend.Name != class.Constructors[0].Structure.Name {
				return fmt.Errorf(
					"gen: singular canonical class %q name %q does not match constructor %q",
					qname, backend.Name, class.Constructors[0].Structure.Name,
				)
			}
			if len(interfaces[qname]) != 0 {
				return fmt.Errorf("gen: singular canonical class %q unexpectedly has a generated interface", qname)
			}
		} else {
			candidates := interfaces[qname]
			if len(candidates) != 1 {
				return fmt.Errorf("gen: canonical class %q has %d interface matches, want exactly one", qname, len(candidates))
			}
			class.Interface = candidates[0]
			usedInterfaces[class.Interface] = qname
			if class.Interface.Name != backend.Name || class.Interface.Func != backend.Func {
				return fmt.Errorf("gen: canonical class %q interface/backend identity mismatch", qname)
			}
			if err := validateLayerClassConstructors(class); err != nil {
				return err
			}
		}
		index.Classes[qname] = class
	}

	for qname, backend := range g.classes {
		if backend.Vector {
			continue
		}
		if _, ok := index.Classes[qname]; !ok {
			return fmt.Errorf("gen: Go class binding %q has no canonical semantic class", qname)
		}
	}
	if seenBackendClasses != len(index.Classes) {
		return fmt.Errorf("gen: canonical class binding count mismatch: backend=%d semantic=%d", seenBackendClasses, len(index.Classes))
	}
	if len(usedInterfaces) != len(g.interfaces) {
		return fmt.Errorf("gen: %d generated interfaces were not bound to canonical semantic classes", len(g.interfaces)-len(usedInterfaces))
	}
	return nil
}

func validateLayerClassConstructors(class *layerClassBinding) error {
	expectedNames := make(map[string]struct{}, len(class.Constructors))
	expectedIDs := make(map[layerStructIdentity]struct{}, len(class.Constructors))
	for _, constructor := range class.Constructors {
		expectedNames[constructor.Structure.Name] = struct{}{}
		expectedIDs[layerStructIdentity{QName: constructor.Key.QName, WireID: constructor.Definition.WireID}] = struct{}{}
	}
	if len(class.Backend.Constructors) != len(expectedNames) {
		return fmt.Errorf(
			"gen: canonical class %q backend constructor count=%d, want=%d",
			class.QName, len(class.Backend.Constructors), len(expectedNames),
		)
	}
	seenNames := make(map[string]struct{}, len(class.Backend.Constructors))
	for _, name := range class.Backend.Constructors {
		if _, duplicate := seenNames[name]; duplicate {
			return fmt.Errorf("gen: canonical class %q repeats backend constructor %q", class.QName, name)
		}
		seenNames[name] = struct{}{}
		if _, ok := expectedNames[name]; !ok {
			return fmt.Errorf("gen: canonical class %q has unexpected backend constructor %q", class.QName, name)
		}
	}

	if len(class.Interface.Constructors) != len(expectedIDs) {
		return fmt.Errorf(
			"gen: canonical class %q interface constructor count=%d, want=%d",
			class.QName, len(class.Interface.Constructors), len(expectedIDs),
		)
	}
	seenIDs := make(map[layerStructIdentity]struct{}, len(class.Interface.Constructors))
	for i := range class.Interface.Constructors {
		constructor := &class.Interface.Constructors[i]
		wireID, err := parseLayerBindingWireID(constructor.HexID)
		if err != nil {
			return fmt.Errorf("gen: canonical class %q interface constructor %q: %w", class.QName, constructor.Name, err)
		}
		identity := layerStructIdentity{QName: constructor.RawName, WireID: wireID}
		if _, duplicate := seenIDs[identity]; duplicate {
			return fmt.Errorf("gen: canonical class %q repeats interface constructor %s#%08x", class.QName, identity.QName, identity.WireID)
		}
		seenIDs[identity] = struct{}{}
		if _, ok := expectedIDs[identity]; !ok {
			return fmt.Errorf("gen: canonical class %q has unexpected interface constructor %s#%08x", class.QName, identity.QName, identity.WireID)
		}
	}
	return nil
}
