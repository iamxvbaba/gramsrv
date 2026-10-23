package semantic

import (
	"fmt"
	"strings"

	"github.com/gotd/tl"
)

// TypeKind classifies the wire-relevant form of a TL type reference.
type TypeKind uint8

const (
	// TypePrimitive is a TL primitive or built-in value.
	TypePrimitive TypeKind = iota
	// TypeNamed refers to a TL constructor class or an exact bare constructor.
	TypeNamed
	// TypeVector is a boxed Vector<T> or bare vector<T>.
	TypeVector
	// TypeGenericRef refers to a generic parameter, such as !X.
	TypeGenericRef
)

// TypeRef is a recursive, lossless representation of the wire-relevant parts
// of tl.Type. Arg is populated for generic applications, including vectors.
type TypeRef struct {
	Kind    TypeKind
	QName   string
	Bare    bool
	Percent bool
	Arg     *TypeRef
}

var primitiveTypes = map[string]struct{}{
	"int":    {},
	"int32":  {},
	"int53":  {},
	"int64":  {},
	"long":   {},
	"int128": {},
	"int256": {},
	"double": {},
	"string": {},
	"bytes":  {},
	"bool":   {},
	"true":   {},
	"false":  {},

	// Boxed names used as definition or method results.
	"Int":    {},
	"Long":   {},
	"Double": {},
	"String": {},
	"Bytes":  {},
	"Bool":   {},
	"True":   {},
	"Object": {},
}

func qualifiedType(t tl.Type) string {
	if len(t.Namespace) == 0 {
		return t.Name
	}
	return strings.Join(t.Namespace, ".") + "." + t.Name
}

func typeRefFromTL(t tl.Type) (TypeRef, error) {
	qname := qualifiedType(t)
	ref := TypeRef{
		QName:   qname,
		Bare:    t.Bare,
		Percent: t.Percent,
	}

	switch {
	case t.GenericRef:
		ref.Kind = TypeGenericRef
	case t.Name == "Vector" || t.Name == "vector":
		ref.Kind = TypeVector
		if t.GenericArg == nil {
			return TypeRef{}, fmt.Errorf("vector %q has no element type", qname)
		}
	case isPrimitive(qname):
		ref.Kind = TypePrimitive
	default:
		ref.Kind = TypeNamed
	}

	if t.GenericArg != nil {
		arg, err := typeRefFromTL(*t.GenericArg)
		if err != nil {
			return TypeRef{}, err
		}
		ref.Arg = &arg
	}
	return ref, nil
}

func bindGenericRefs(ref *TypeRef, params map[string]struct{}) {
	if ref.Kind == TypeNamed && ref.Arg == nil {
		if _, ok := params[ref.QName]; ok {
			ref.Kind = TypeGenericRef
		}
	}
	if ref.Arg != nil {
		bindGenericRefs(ref.Arg, params)
	}
}

func isPrimitive(name string) bool {
	_, ok := primitiveTypes[name]
	return ok
}

// wireName normalizes TL spellings that use the same payload codec. Semantic
// shape continues to retain the original QName.
func (t TypeRef) wireName() string {
	if t.Kind != TypePrimitive {
		return t.QName
	}
	switch t.QName {
	case "int", "int32", "Int":
		return "int32"
	case "int53", "long", "int64", "Long":
		return "int64"
	case "double", "Double":
		return "float64"
	case "string", "String", "bytes", "Bytes":
		return "tl-bytes"
	case "Bool", "bool", "true", "false", "True":
		return "tl-bool"
	default:
		return t.QName
	}
}

// Equal reports whether two references have identical TL wire semantics.
func (t TypeRef) Equal(other TypeRef) bool {
	if t.Kind != other.Kind || t.QName != other.QName || t.Bare != other.Bare || t.Percent != other.Percent {
		return false
	}
	if t.Arg == nil || other.Arg == nil {
		return t.Arg == nil && other.Arg == nil
	}
	return t.Arg.Equal(*other.Arg)
}

// String renders a compact TL spelling for diagnostics.
func (t TypeRef) String() string {
	var b strings.Builder
	switch {
	case t.Kind == TypeGenericRef:
		b.WriteByte('!')
	case t.Percent:
		b.WriteByte('%')
	}
	b.WriteString(t.QName)
	if t.Arg != nil {
		b.WriteByte('<')
		b.WriteString(t.Arg.String())
		b.WriteByte('>')
	}
	return b.String()
}
