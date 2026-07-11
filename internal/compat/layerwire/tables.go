package layerwire

import (
	"fmt"

	"github.com/gotd/td/bin"
)

// ruleRaw is the generated, compact form of a single changed-constructor
// downgrade (see tables_gen.go). Mechanical rules carry the target CRC plus the
// canonical field names retained at the target layer; structural rules name a
// hand-written transform registered in fallback.go.
type ruleRaw struct {
	target     uint32
	keep       []string
	structural string
}

// layerRaw is the generated downgrade table for one target layer.
type layerRaw struct {
	rules    map[uint32]ruleRaw
	newTypes []uint32 // canonical CRCs that do not exist at this layer
}

// downgradeRule is the runtime form of ruleRaw with keep as a set.
type downgradeRule struct {
	target     uint32
	keep       map[string]bool
	structural string
}

// layerTables is the runtime downgrade model for one target layer.
type layerTables struct {
	rules    map[uint32]*downgradeRule
	newTypes map[uint32]bool
	dirty    map[uint32]bool // ctor CRC needs a deep walk
	dirtyT   map[string]bool // abstract/bare type name reaches a dirty ctor
}

// tables holds the runtime model per supported layer, built lazily.
var tables = func() map[int]*layerTables {
	out := make(map[int]*layerTables, len(generatedTables))
	for layer, raw := range generatedTables {
		out[layer] = buildLayerTables(raw)
	}
	return out
}()

func buildLayerTables(raw layerRaw) *layerTables {
	lt := &layerTables{
		rules:    make(map[uint32]*downgradeRule, len(raw.rules)),
		newTypes: make(map[uint32]bool, len(raw.newTypes)),
	}
	for crc, r := range raw.rules {
		dr := &downgradeRule{target: r.target, structural: r.structural}
		if r.structural == "" {
			dr.keep = make(map[string]bool, len(r.keep))
			for _, n := range r.keep {
				dr.keep[n] = true
			}
		}
		lt.rules[crc] = dr
	}
	for _, crc := range raw.newTypes {
		lt.newTypes[crc] = true
	}
	lt.computeDirty()
	return lt
}

// computeDirty marks every constructor (and abstract/bare type) that can
// transitively contain a changed, structural, or layer-absent constructor, so
// the transcoder can byte-copy the ~86% of the type graph that is unaffected.
func (lt *layerTables) computeDirty() {
	lt.dirty = make(map[uint32]bool)
	lt.dirtyT = make(map[string]bool)
	// Seed: rules + new types are themselves dirty.
	for crc := range lt.rules {
		lt.dirty[crc] = true
	}
	for crc := range lt.newTypes {
		lt.dirty[crc] = true
	}
	markType := func(name string) {
		if name != "" && !lt.dirtyT[name] {
			lt.dirtyT[name] = true
		}
	}
	// Seed dirty types from seeded dirty ctors.
	for crc := range lt.dirty {
		if cl := canonical.byCRC[crc]; cl != nil {
			markType(cl.result)
			markType(cl.name) // bare reference
		}
	}
	// Fixpoint: a ctor is dirty if any field's type is dirty; a type is dirty
	// if any of its constructors is dirty.
	for changed := true; changed; {
		changed = false
		for crc, cl := range canonical.byCRC {
			if lt.dirty[crc] {
				continue
			}
			if lt.ctorHasDirtyField(cl) {
				lt.dirty[crc] = true
				if !lt.dirtyT[cl.result] {
					lt.dirtyT[cl.result] = true
				}
				if !lt.dirtyT[cl.name] {
					lt.dirtyT[cl.name] = true
				}
				changed = true
			}
		}
	}
}

func (lt *layerTables) ctorHasDirtyField(cl *ctorLayout) bool {
	for i := range cl.fields {
		if lt.fieldDirty(&cl.fields[i]) {
			return true
		}
	}
	return false
}

func (lt *layerTables) fieldDirty(f *fieldLayout) bool {
	switch f.kind {
	case kindObject, kindBareObject:
		return lt.dirtyT[f.typeName]
	case kindVector, kindVectorBare:
		return lt.fieldDirty(f.elem)
	default:
		return false
	}
}

// structuralFunc transforms a changed constructor whose downgrade is not a pure
// field drop. The leading CRC has already been consumed from in; the transform
// reads the canonical body from in and writes the target-layer object (whose
// constructor id is target) to out.
type structuralFunc func(cl *ctorLayout, target uint32, in, out *bin.Buffer, layer, depth int, walk *walkState) error

// fallbackFunc replaces a layer-absent (227-only) constructor with an
// equivalent the target layer understands. The leading CRC is NOT yet consumed.
type fallbackFunc func(cl *ctorLayout, in, out *bin.Buffer, layer, depth int, walk *walkState) error

// structuralTransforms and the newType fallback registries are populated in
// fallback.go. newTypeFallbacks is keyed by canonical CRC (specific override);
// newTypeFallbacksByType is keyed by the canonical abstract result type and
// covers every 227-only constructor of that class (e.g. any new MessageAction).
var (
	structuralTransforms   = map[string]structuralFunc{}
	newTypeFallbacks       = map[uint32]fallbackFunc{}
	newTypeFallbacksByType = map[string]fallbackFunc{}
)

// Transcode downgrades a single canonical (Layer 227) boxed object to the wire
// shape of layer. layer >= CanonicalLayer (or unsupported) returns in verbatim.
// On any transform gap it returns an error so the edge can fall back to sending
// the canonical bytes rather than corrupting the stream.
func Transcode(canonicalBytes []byte, layer int) ([]byte, error) {
	if layer >= CanonicalLayer {
		return canonicalBytes, nil
	}
	lt := tables[layer]
	if lt == nil {
		return canonicalBytes, nil // unsupported floor: best-effort passthrough
	}
	// Top-level constructors that are not in the canonical tg schema are MTProto
	// control/error objects (mt.*, e.g. rpc_error) — layer-invariant, so pass
	// them through. A nested unknown id is still a hard error (real gap).
	if id, err := (&bin.Buffer{Buf: canonicalBytes}).PeekID(); err != nil || canonical.byCRC[id] == nil {
		return canonicalBytes, nil
	}
	in := &bin.Buffer{Buf: canonicalBytes}
	out := &bin.Buffer{}
	walk := newWalkState()
	if err := lt.transcodeObject(in, out, layer, 1, walk); err != nil {
		return nil, classifyWalkError(err)
	}
	if in.Len() != 0 {
		return nil, malformedf("%d trailing bytes after transcode to layer %d", in.Len(), layer)
	}
	return out.Buf, nil
}

// UpgradeMethodCRC maps an old client's method constructor id to the canonical
// (227) id when the request body is byte-compatible — i.e. swapping the leading
// 4-byte id yields a valid 227 request. It unifies two sources: generated
// official layer drift (inboundMethodUpgrades) and hand-maintained client
// constructor drift (clientMethodAliases). Returns ok=false for unchanged
// methods and for changes that need a real decode (those stay as rpc handlers).
func UpgradeMethodCRC(oldID uint32) (uint32, bool) {
	if newID, ok := inboundMethodUpgrades[oldID]; ok {
		return newID, true
	}
	newID, ok := clientMethodAliases[oldID]
	return newID, ok
}

func (lt *layerTables) transcodeObject(in, out *bin.Buffer, layer, depth int, walk *walkState) error {
	if err := walk.enter(depth, "constructor"); err != nil {
		return err
	}
	id, err := in.PeekID()
	if err != nil {
		return err
	}
	cl, ok := canonical.byCRC[id]
	if !ok {
		return fmt.Errorf("layerwire: unknown constructor %#08x", id)
	}
	if rule := lt.rules[id]; rule != nil {
		if err := in.ConsumeID(id); err != nil {
			return err
		}
		if rule.structural != "" {
			fn := structuralTransforms[rule.structural]
			if fn == nil {
				return fmt.Errorf("layerwire: no structural transform %q for %s@%d", rule.structural, cl.name, layer)
			}
			return fn(cl, rule.target, in, out, layer, depth, walk)
		}
		out.PutID(rule.target)
		return lt.transcodeBody(in, out, cl, rule.keep, layer, depth, walk)
	}
	if lt.newTypes[id] {
		fn := newTypeFallbacks[id]
		if fn == nil {
			fn = newTypeFallbacksByType[cl.result]
		}
		if fn == nil {
			return fmt.Errorf("layerwire: %s (%#08x) absent at layer %d and no fallback", cl.name, id, layer)
		}
		return fn(cl, in, out, layer, depth, walk)
	}
	if !lt.dirty[id] {
		// Unaffected subtree: byte-for-byte copy.
		pre := in.Buf
		if err := in.ConsumeID(id); err != nil {
			return err
		}
		if err := walk.skipCtorBody(canonical, in, cl, depth); err != nil {
			return err
		}
		out.Put(pre[:len(pre)-len(in.Buf)])
		return nil
	}
	// Unchanged at this level but a descendant is dirty: keep CRC, recurse.
	if err := in.ConsumeID(id); err != nil {
		return err
	}
	out.PutID(id)
	return lt.transcodeBody(in, out, cl, nil, layer, depth, walk)
}

// transcodeBody re-encodes a constructor body. keep==nil means retain every
// field (recursing into dirty descendants); otherwise only the named canonical
// fields are written, flag integers are remasked to the retained bits, and
// dropped fields are read-and-discarded.
func (lt *layerTables) transcodeBody(in, out *bin.Buffer, cl *ctorLayout, keep map[string]bool, layer, depth int, walk *walkState) error {
	kept := func(name string) bool { return keep == nil || keep[name] }
	var flags map[string]uint32
	for i := range cl.fields {
		f := &cl.fields[i]
		if f.isFlags {
			v, err := in.Uint32()
			if err != nil {
				return fmt.Errorf("%s.%s: %w", cl.name, f.name, err)
			}
			if flags == nil {
				flags = make(map[string]uint32, 2)
			}
			flags[f.name] = v
			if kept(f.name) {
				out.PutUint32(v & lt.keptMask(cl, f.name, kept))
			}
			continue
		}
		present := !f.conditional() || flags[f.flagName]&(1<<uint(f.flagBit)) != 0
		if !present {
			continue
		}
		if kept(f.name) {
			if err := lt.transcodeValue(in, out, f, cl, layer, depth, walk); err != nil {
				return fmt.Errorf("%s.%s: %w", cl.name, f.name, err)
			}
		} else if err := walk.skipValue(canonical, in, f, cl, depth); err != nil {
			return fmt.Errorf("%s.%s (drop): %w", cl.name, f.name, err)
		}
	}
	return nil
}

// keptMask is the OR of bits for retained conditional fields gated by flagName,
// clearing bits whose fields are dropped at the target layer.
func (lt *layerTables) keptMask(cl *ctorLayout, flagName string, kept func(string) bool) uint32 {
	var mask uint32
	for i := range cl.fields {
		g := &cl.fields[i]
		if g.conditional() && g.flagName == flagName && kept(g.name) {
			mask |= 1 << uint(g.flagBit)
		}
	}
	return mask
}

// transcodeValue writes one present field value, recursing only into dirty
// subtrees and byte-copying everything else.
func (lt *layerTables) transcodeValue(in, out *bin.Buffer, f *fieldLayout, owner *ctorLayout, layer, depth int, walk *walkState) error {
	if !lt.fieldDirty(f) {
		pre := in.Buf
		if err := walk.skipValue(canonical, in, f, owner, depth); err != nil {
			return err
		}
		out.Put(pre[:len(pre)-len(in.Buf)])
		return nil
	}
	switch f.kind {
	case kindVector, kindVectorBare:
		vectorDepth := depth + 1
		if vectorDepth <= 0 || vectorDepth > walk.limits.maxDepth {
			return limitf("vector nesting depth %d exceeds limit %d", vectorDepth, walk.limits.maxDepth)
		}
		if f.kind == kindVector {
			id, err := in.Uint32()
			if err != nil {
				return err
			}
			if id != vectorTypeID {
				return fmt.Errorf("expected vector id, got %#08x", id)
			}
			out.PutUint32(vectorTypeID)
		}
		n, err := in.Int()
		if err != nil {
			return err
		}
		if n < 0 {
			return malformedf("negative vector length %d", n)
		}
		if max := walk.vectorLimit(owner, f); n > max {
			return limitf("vector %s.%s length %d exceeds limit %d", ownerName(owner), fieldName(f), n, max)
		}
		if err := walk.addUnits(n, "vector "+ownerName(owner)+"."+fieldName(f)); err != nil {
			return err
		}
		out.PutInt(n)
		for i := 0; i < n; i++ {
			if err := lt.transcodeValue(in, out, f.elem, nil, layer, vectorDepth, walk); err != nil {
				return err
			}
		}
		return nil
	case kindObject:
		return lt.transcodeObject(in, out, layer, depth+1, walk)
	case kindBareObject:
		bareDepth := depth + 1
		if err := walk.enter(bareDepth, "bare constructor"); err != nil {
			return err
		}
		cl, ok := canonical.bareByT[f.typeName]
		if !ok {
			return fmt.Errorf("unknown bare type %q", f.typeName)
		}
		// Bare objects have no CRC and (within 220..227) no changed bare ctor;
		// recurse all-kept to reach any dirty descendants.
		return lt.transcodeBody(in, out, cl, nil, layer, bareDepth, walk)
	default:
		// Primitive marked dirty should be impossible.
		return fmt.Errorf("unexpected dirty primitive kind %d", f.kind)
	}
}
