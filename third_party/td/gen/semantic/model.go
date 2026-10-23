package semantic

import (
	"fmt"
	"strings"

	"github.com/gotd/tl"
)

// Category separates constructors from RPC methods. The same qualified name
// is allowed to exist in both categories without becoming one semantic family.
type Category uint8

const (
	CategoryType Category = iota
	CategoryFunction
)

func categoryFromTL(c tl.Category) Category {
	if c == tl.CategoryFunction {
		return CategoryFunction
	}
	return CategoryType
}

func (c Category) String() string {
	if c == CategoryFunction {
		return "function"
	}
	return "type"
}

// SemanticKey is the stable semantic identity of a constructor or method.
// Wire IDs are deliberately not part of semantic identity.
type SemanticKey struct {
	Category Category
	QName    string
}

func (k SemanticKey) String() string {
	return k.Category.String() + ":" + k.QName
}

// DefinitionKey is kept as a source-compatible name for SemanticKey.
type DefinitionKey = SemanticKey

// SourceRef records the immutable provenance of one layer schema.
type SourceRef struct {
	Layer      int
	Repository string
	Commit     string
	Blob       string
	Path       string
	File       string
	SHA256     ShapeDigest
}

// FieldKind distinguishes a flags word from an encoded field value.
type FieldKind uint8

const (
	FieldValue FieldKind = iota
	FieldFlagsWord
)

// Condition describes flags.N?T. PresenceOnly is true for flags.N?true,
// where no field body is written.
type Condition struct {
	Word         string
	Bit          uint8
	PresenceOnly bool
}

// FieldShape is one ordered TL parameter.
type FieldShape struct {
	Kind      FieldKind
	Name      string
	Ordinal   int
	Type      TypeRef
	Condition *Condition
}

// Definition is one constructor or method in one concrete layer.
type Definition struct {
	Key            SemanticKey
	WireID         uint32
	GenericParams  []string
	Fields         []FieldShape
	Result         TypeRef
	Base           bool
	WireShape      WireShape
	BodyShape      ShapeDigest
	SemanticShape  ShapeDigest
	SignatureShape ShapeDigest
}

// SchemaModel is a validated semantic view of one TL layer.
type SchemaModel struct {
	Layer               int
	Source              SourceRef
	Definitions         []*Definition
	ByKey               map[SemanticKey]*Definition
	ByWire              map[uint32]*Definition
	ConstructorsByClass map[string][]SemanticKey

	raw *tl.Schema
}

func qualifyDefinition(d tl.Definition) string {
	if len(d.Namespace) == 0 {
		return d.Name
	}
	return strings.Join(d.Namespace, ".") + "." + d.Name
}

// BuildSchema validates and converts a parsed TL schema into semantic IR.
func BuildSchema(schema *tl.Schema, source SourceRef) (*SchemaModel, error) {
	if schema == nil {
		return nil, fmt.Errorf("semantic: nil schema")
	}
	if source.Layer != 0 && schema.Layer != 0 && source.Layer != schema.Layer {
		return nil, fmt.Errorf("semantic: source layer %d does not match schema layer %d", source.Layer, schema.Layer)
	}
	layer := schema.Layer
	if source.Layer != 0 {
		layer = source.Layer
	}
	source.Layer = layer

	model := &SchemaModel{
		Layer:               layer,
		Source:              source,
		ByKey:               make(map[SemanticKey]*Definition, len(schema.Definitions)),
		ByWire:              make(map[uint32]*Definition, len(schema.Definitions)),
		ConstructorsByClass: make(map[string][]SemanticKey),
		raw:                 cloneSchema(schema),
	}

	for _, schemaDef := range schema.Definitions {
		d := schemaDef.Definition
		key := SemanticKey{
			Category: categoryFromTL(schemaDef.Category),
			QName:    qualifyDefinition(d),
		}
		if previous, ok := model.ByKey[key]; ok {
			return nil, fmt.Errorf("semantic: layer %d duplicate definition %s (ids %#08x and %#08x)", layer, key, previous.WireID, d.ID)
		}
		if previous, ok := model.ByWire[d.ID]; ok {
			return nil, fmt.Errorf("semantic: layer %d duplicate wire id %#08x for %s and %s", layer, d.ID, previous.Key, key)
		}

		result, err := typeRefFromTL(d.Type)
		if err != nil {
			return nil, fmt.Errorf("semantic: layer %d %s result: %w", layer, key, err)
		}
		genericParams := make(map[string]struct{}, len(d.GenericParams))
		for _, parameter := range d.GenericParams {
			genericParams[parameter] = struct{}{}
		}
		bindGenericRefs(&result, genericParams)
		def := &Definition{
			Key:           key,
			WireID:        d.ID,
			GenericParams: append([]string(nil), d.GenericParams...),
			Result:        result,
			Base:          d.Base,
		}
		for ordinal, parameter := range d.Params {
			field := FieldShape{
				Kind:    FieldValue,
				Name:    parameter.Name,
				Ordinal: ordinal,
			}
			if parameter.Flags {
				field.Kind = FieldFlagsWord
			} else {
				field.Type, err = typeRefFromTL(parameter.Type)
				if err != nil {
					return nil, fmt.Errorf("semantic: layer %d %s field %q: %w", layer, key, parameter.Name, err)
				}
				bindGenericRefs(&field.Type, genericParams)
			}
			if flag := parameter.Flag; flag != nil {
				if flag.Index < 0 || flag.Index > 31 {
					return nil, fmt.Errorf("semantic: layer %d %s field %q has invalid flag bit %d", layer, key, parameter.Name, flag.Index)
				}
				field.Condition = &Condition{
					Word:         flag.Name,
					Bit:          uint8(flag.Index),
					PresenceOnly: parameter.Type.Name == "true" && len(parameter.Type.Namespace) == 0,
				}
			}
			def.Fields = append(def.Fields, field)
		}

		if err := validateDefinition(def); err != nil {
			return nil, fmt.Errorf("semantic: layer %d %s: %w", layer, key, err)
		}
		def.BodyShape = bodyShape(def)
		def.SignatureShape = signatureShape(def)
		def.SemanticShape = def.SignatureShape
		def.WireShape = payloadWireShape(def)
		model.Definitions = append(model.Definitions, def)
		model.ByKey[key] = def
		model.ByWire[d.ID] = def
		if key.Category == CategoryType {
			model.ConstructorsByClass[result.QName] = append(model.ConstructorsByClass[result.QName], key)
		}
	}
	if err := validateReferences(model); err != nil {
		return nil, fmt.Errorf("semantic: layer %d: %w", layer, err)
	}
	return model, nil
}

func validateDefinition(def *Definition) error {
	genericParams := make(map[string]struct{}, len(def.GenericParams))
	for _, name := range def.GenericParams {
		if name == "" {
			return fmt.Errorf("empty generic parameter")
		}
		if _, duplicate := genericParams[name]; duplicate {
			return fmt.Errorf("duplicate generic parameter %q", name)
		}
		genericParams[name] = struct{}{}
	}

	fields := make(map[string]struct{}, len(def.Fields))
	flagWords := make(map[string]int)
	for _, field := range def.Fields {
		if field.Name == "" {
			return fmt.Errorf("field at ordinal %d has no name", field.Ordinal)
		}
		if _, duplicate := fields[field.Name]; duplicate {
			return fmt.Errorf("duplicate field %q", field.Name)
		}
		fields[field.Name] = struct{}{}
		if field.Kind == FieldFlagsWord {
			flagWords[field.Name] = field.Ordinal
		}
	}
	for _, field := range def.Fields {
		if field.Condition == nil {
			if err := validateGenericRefs(field.Type, genericParams); field.Kind == FieldValue && err != nil {
				return fmt.Errorf("field %q: %w", field.Name, err)
			}
			continue
		}
		flagsOrdinal, ok := flagWords[field.Condition.Word]
		if !ok {
			return fmt.Errorf("field %q refers to missing flags word %q", field.Name, field.Condition.Word)
		}
		if flagsOrdinal >= field.Ordinal {
			return fmt.Errorf("field %q refers to flags word %q that is not encoded before it", field.Name, field.Condition.Word)
		}
		if err := validateGenericRefs(field.Type, genericParams); err != nil {
			return fmt.Errorf("field %q: %w", field.Name, err)
		}
	}
	if err := validateGenericRefs(def.Result, genericParams); err != nil {
		return fmt.Errorf("result: %w", err)
	}
	return nil
}

func validateGenericRefs(ref TypeRef, params map[string]struct{}) error {
	if ref.Kind == TypeGenericRef {
		if _, ok := params[ref.QName]; !ok {
			return fmt.Errorf("undefined generic parameter %q", ref.QName)
		}
	}
	if ref.Arg != nil {
		return validateGenericRefs(*ref.Arg, params)
	}
	return nil
}

func validateReferences(schema *SchemaModel) error {
	constructors := make(map[string]struct{})
	for key := range schema.ByKey {
		if key.Category == CategoryType {
			constructors[key.QName] = struct{}{}
		}
	}
	for _, def := range schema.Definitions {
		for _, field := range def.Fields {
			if field.Kind == FieldFlagsWord {
				continue
			}
			if err := validateTypeReference(field.Type, schema.ConstructorsByClass, constructors); err != nil {
				return fmt.Errorf("%s field %q: %w", def.Key, field.Name, err)
			}
		}
		if err := validateTypeReference(def.Result, schema.ConstructorsByClass, constructors); err != nil {
			return fmt.Errorf("%s result: %w", def.Key, err)
		}
	}
	return nil
}

func validateTypeReference(ref TypeRef, classes map[string][]SemanticKey, constructors map[string]struct{}) error {
	if ref.Arg != nil {
		if err := validateTypeReference(*ref.Arg, classes, constructors); err != nil {
			return err
		}
	}
	if ref.Kind != TypeNamed {
		return nil
	}
	if ref.Bare || ref.Percent {
		if _, ok := constructors[ref.QName]; !ok {
			return fmt.Errorf("unknown bare constructor %q", ref.QName)
		}
		return nil
	}
	if _, ok := classes[ref.QName]; !ok {
		return fmt.Errorf("unknown constructor class %q", ref.QName)
	}
	return nil
}
