package layerwire

import (
	_ "embed"
	"fmt"

	"github.com/gotd/td/bin"
)

const maxOpaqueRequestBytes = 16 << 20

//go:embed schema/routable-compat.tl
var routableCompatSchema string

// routable combines the canonical Layer 227 model with the small set of
// explicitly declared compatibility-only methods.  Nested objects in those
// methods are canonical Input* constructors, so one combined graph is needed
// for the same depth/vector/bytes walker to validate the complete request.
var routable = mustLoadRoutable()

func mustLoadRoutable() *schemaModel {
	compat, err := parseSchemaModel(routableCompatSchema)
	if err != nil {
		panic("layerwire: parse routable compat schema: " + err.Error())
	}
	m := &schemaModel{
		byCRC:    make(map[uint32]*ctorLayout, len(canonical.byCRC)+len(compat.byCRC)),
		byName:   make(map[string]*ctorLayout, len(canonical.byName)+len(compat.byName)),
		bareByT:  make(map[string]*ctorLayout, len(canonical.bareByT)),
		ctorsOfT: make(map[string][]*ctorLayout, len(canonical.ctorsOfT)),
	}
	for id, cl := range canonical.byCRC {
		m.byCRC[id] = cl
	}
	for name, cl := range canonical.byName {
		m.byName[name] = cl
	}
	for name, cl := range canonical.bareByT {
		m.bareByT[name] = cl
	}
	for name, ctors := range canonical.ctorsOfT {
		m.ctorsOfT[name] = ctors
	}
	for id, cl := range compat.byCRC {
		if existing := m.byCRC[id]; existing != nil {
			panic(fmt.Sprintf("layerwire: routable compat crc %#08x collides with %s", id, existing.name))
		}
		m.byCRC[id] = cl
		m.byName[cl.name] = cl
	}
	return m
}

// ValidateRoutableRequest validates every request shape the router knows how to
// decode, including compatibility-only fallback methods.  known=false denotes
// a genuinely unknown top-level constructor.  Such a request is never decoded:
// it is treated as opaque, word-aligned TL data, bounded by both this total-size
// cap and mtprotoedge's transport/RPC budgets, and must continue to the router's
// compatibility trace rather than being mislabeled as malformed input.
func ValidateRoutableRequest(body []byte) (known bool, err error) {
	b := &bin.Buffer{Buf: body}
	id, err := b.PeekID()
	if err != nil {
		return false, classifyWalkError(err)
	}
	cl := routable.byCRC[id]
	if cl == nil {
		if len(body) > maxOpaqueRequestBytes {
			return false, limitf("opaque request length %d exceeds limit %d", len(body), maxOpaqueRequestBytes)
		}
		if len(body)%bin.Word != 0 {
			return false, malformedf("opaque request length %d is not word aligned", len(body))
		}
		return false, nil
	}
	if !cl.isFunc {
		return true, malformedf("constructor %s (%#08x) is not a method", cl.name, id)
	}
	return true, validateRequestLayout(routable, cl, body)
}
