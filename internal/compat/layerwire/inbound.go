package layerwire

import (
	_ "embed"
	"encoding/binary"
	"fmt"

	"github.com/gotd/td/bin"
)

// Canonical ids used to synthesize converted/defaulted values.
const (
	inputUserID                   = 0xf21158c6 // inputUser user_id:long access_hash:long
	inputMessageID                = 0xa676a322 // inputMessageID id:int
	inputChannelEmptyID           = 0xee8c1e86 // inputChannelEmpty
	inputChannelID                = 0xf35aec28 // inputChannel channel_id:long access_hash:long
	inputChannelFromMessageID     = 0x5b934f9d // inputChannelFromMessage peer:InputPeer msg_id:int channel_id:long
	inputPeerEmptyID              = 0x7f3b18ea // inputPeerEmpty
	inputPeerChannelID            = 0x27bcbbfc // inputPeerChannel channel_id:long access_hash:long
	inputPeerChannelFromMessageID = 0xbd2a0840 // inputPeerChannelFromMessage peer:InputPeer msg_id:int channel_id:long
	boolFalseID                   = 0xbc799737 // boolFalse
)

//go:embed schema/client-drift.tl
var clientDriftSchema string

// driftModel holds the declared old-layout of each client-drift constructor.
var driftModel = mustLoadDrift()

func mustLoadDrift() *schemaModel {
	m, err := parseSchemaModel(clientDriftSchema)
	if err != nil {
		panic("layerwire: parse client-drift schema: " + err.Error())
	}
	return m
}

// driftFieldRenames maps a canonical field that was renamed from the client's
// old constructor: key "<qualified method>\x00<canonical field>" -> old field.
// Pure schema diff cannot recover a rename, so it is declared here (data, not a
// transform). It is the only thing a structural rename needs.
var driftFieldRenames = map[string]string{
	"bots.exportBotToken\x00bot":       "bot_id",
	"messages.editChatCreator\x00peer": "channel",
}

// fieldConverter rewrites one field whose wire type changed between the old and
// canonical layout. Keyed by "<oldTypeSig>-><newTypeSig>"; raw is the old field's
// encoded bytes. Reusable across any method with the same type change.
type fieldConverter func(raw []byte, out *bin.Buffer, walk *walkState, owner *ctorLayout, field *fieldLayout) error

var fieldConverters = map[string]fieldConverter{
	// id:Vector<int> -> id:Vector<InputMessage> (wrap each int in inputMessageID).
	"Vector<int>->Vector<InputMessage>": func(raw []byte, out *bin.Buffer, walk *walkState, owner *ctorLayout, field *fieldLayout) error {
		in := &bin.Buffer{Buf: raw}
		n, err := in.VectorHeader()
		if err != nil {
			return err
		}
		if max := walk.vectorLimit(owner, field); n > max {
			return limitf("vector %s.%s length %d exceeds limit %d", ownerName(owner), fieldName(field), n, max)
		}
		out.PutVectorHeader(n)
		for i := 0; i < n; i++ {
			v, err := in.Int()
			if err != nil {
				return err
			}
			out.PutID(inputMessageID)
			out.PutInt(v)
		}
		if in.Len() != 0 {
			return malformedf("%d trailing bytes in Vector<int> converter", in.Len())
		}
		return nil
	},
	// bot_id:long -> bot:InputUser{user_id, access_hash=0}.
	"long->InputUser": func(raw []byte, out *bin.Buffer, walk *walkState, owner *ctorLayout, field *fieldLayout) error {
		in := &bin.Buffer{Buf: raw}
		id, err := in.Long()
		if err != nil {
			return err
		}
		out.PutID(inputUserID)
		out.PutLong(id)
		out.PutLong(0)
		if in.Len() != 0 {
			return malformedf("%d trailing bytes in long converter", in.Len())
		}
		return nil
	},
	// channel:InputChannel -> peer:InputPeer for the old channels.editCreator
	// Android constructor. Concrete layouts are otherwise byte-compatible.
	"InputChannel->InputPeer": func(raw []byte, out *bin.Buffer, walk *walkState, owner *ctorLayout, field *fieldLayout) error {
		in := &bin.Buffer{Buf: raw}
		id, err := in.ID()
		if err != nil {
			return err
		}
		switch id {
		case inputChannelEmptyID:
			out.PutID(inputPeerEmptyID)
		case inputChannelID:
			out.PutID(inputPeerChannelID)
			out.Put(in.Buf)
		case inputChannelFromMessageID:
			out.PutID(inputPeerChannelFromMessageID)
			out.Put(in.Buf)
		default:
			return bin.NewUnexpectedID(id)
		}
		return nil
	},
}

// UpgradeInbound converts an old client's inbound request to canonical (227)
// form so the normal gotd dispatcher can handle it. It unifies three data-driven
// sources, all of which require no per-method handler code:
//   - inboundMethodUpgrades (generated from api.tl diff): official layer drift.
//   - clientMethodAliases (client_aliases.go): body-identical client drift.
//   - driftModel (client-drift.tl): body-different client drift, upgraded by the
//     generic engine below.
//
// ok=false means no upgrade applies. On ok=true the returned buffer (canonical
// id + body) is what to dispatch.
func UpgradeInbound(id uint32, in *bin.Buffer) (*bin.Buffer, bool, error) {
	if newID, ok := UpgradeMethodCRC(id); ok {
		target := canonical.byCRC[newID]
		if target == nil || !target.isFunc {
			return nil, true, malformedf("alias %#08x targets unknown canonical method %#08x", id, newID)
		}
		if err := validateAliasedMethod(id, target, in.Buf); err != nil {
			return nil, true, err
		}
		// Copy rather than rewrite in place: never mutate the caller's buffer
		// (matches the body-transform path, which also returns a fresh buffer).
		out := &bin.Buffer{Buf: append([]byte(nil), in.Buf...)}
		binary.LittleEndian.PutUint32(out.Buf[:4], newID)
		return out, true, nil
	}
	if old := driftModel.byCRC[id]; old != nil {
		out, err := upgradeFromDrift(old, in, newWalkState())
		if err != nil {
			return nil, true, classifyWalkError(fmt.Errorf("layerwire: upgrade %s (%#08x): %w", old.name, id, err))
		}
		return out, true, nil
	}
	return nil, false, nil
}

// validateAliasedMethod validates the old-id/canonical-body shape before
// allocating the replacement buffer. The body is walked against the canonical
// target layout while the original constructor id remains untouched.
func validateAliasedMethod(oldID uint32, target *ctorLayout, raw []byte) error {
	walk := newWalkState()
	if err := walk.enter(1, "constructor"); err != nil {
		return err
	}
	b := &bin.Buffer{Buf: raw}
	if err := b.ConsumeID(oldID); err != nil {
		return classifyWalkError(err)
	}
	if err := walk.skipCtorBody(canonical, b, target, 1); err != nil {
		return classifyWalkError(err)
	}
	if b.Len() != 0 {
		return malformedf("%d trailing bytes after aliased method %s", b.Len(), target.name)
	}
	return nil
}

// IsClientDrift reports whether id is a client-private constructor (DrKLO
// constructor drift), as opposed to official layer drift from api.tl.
func IsClientDrift(id uint32) bool {
	if _, ok := clientMethodAliases[id]; ok {
		return true
	}
	return driftModel.byCRC[id] != nil
}

// upgradeFromDrift rebuilds a canonical (227) request from an old client-drift
// body, comparing the declared old layout to the canonical layout field by field.
func upgradeFromDrift(old *ctorLayout, in *bin.Buffer, walk *walkState) (*bin.Buffer, error) {
	target := canonical.byName[old.name]
	if target == nil {
		return nil, fmt.Errorf("no canonical method %q", old.name)
	}
	if err := walk.enter(1, "constructor"); err != nil {
		return nil, err
	}
	if err := in.ConsumeID(old.crc); err != nil {
		return nil, err
	}

	// Decode the old body: capture each present field's raw bytes + flag ints.
	vals := make(map[string][]byte, len(old.fields))
	present := make(map[string]bool, len(old.fields))
	oldFlags := make(map[string]uint32, 2)
	oldByName := make(map[string]*fieldLayout, len(old.fields))
	for i := range old.fields {
		f := &old.fields[i]
		oldByName[f.name] = f
		if f.isFlags {
			v, err := in.Uint32()
			if err != nil {
				return nil, err
			}
			oldFlags[f.name] = v
			continue
		}
		if f.conditional() && oldFlags[f.flagName]&(1<<uint(f.flagBit)) == 0 {
			continue
		}
		present[f.name] = true
		if f.kind == kindTrue {
			continue
		}
		pre := in.Buf
		if err := walk.skipValue(canonical, in, f, old, 1); err != nil {
			return nil, fmt.Errorf("decode old field %q: %w", f.name, err)
		}
		vals[f.name] = pre[:len(pre)-len(in.Buf)]
	}
	if in.Len() != 0 {
		return nil, fmt.Errorf("%d trailing bytes after old body", in.Len())
	}

	// Emit the canonical body.
	out := &bin.Buffer{}
	out.PutID(target.crc)
	for i := range target.fields {
		nf := &target.fields[i]
		if nf.isFlags {
			out.PutUint32(oldFlags[nf.name]) // 0 when absent in old (new flags int)
			continue
		}
		oldName := nf.name
		if mapped, ok := driftFieldRenames[old.name+"\x00"+nf.name]; ok {
			oldName = mapped
		}
		if present[oldName] {
			of := oldByName[oldName]
			if of != nil && typeSig(of) != typeSig(nf) {
				conv := fieldConverters[typeSig(of)+"->"+typeSig(nf)]
				if conv == nil {
					return nil, fmt.Errorf("field %q: no converter %s->%s", nf.name, typeSig(of), typeSig(nf))
				}
				if err := conv(vals[oldName], out, walk, old, of); err != nil {
					return nil, fmt.Errorf("field %q convert: %w", nf.name, err)
				}
			} else {
				out.Put(vals[oldName]) // shared field, identical wire (kindTrue => no bytes)
			}
			continue
		}
		// Canonical-only field absent in old.
		if nf.conditional() || nf.kind == kindTrue {
			continue // optional: leave absent (its flag bit is clear)
		}
		if err := writeDefault(nf, out); err != nil {
			return nil, fmt.Errorf("field %q default: %w", nf.name, err)
		}
	}
	return out, nil
}

// writeDefault writes the zero value of a required canonical-only field.
func writeDefault(f *fieldLayout, out *bin.Buffer) error {
	switch f.kind {
	case kindInt:
		out.PutInt(0)
	case kindLong:
		out.PutLong(0)
	case kindDouble:
		out.PutDouble(0)
	case kindInt128:
		out.PutInt128(bin.Int128{})
	case kindInt256:
		out.PutInt256(bin.Int256{})
	case kindBytes:
		out.PutBytes(nil)
	case kindString:
		out.PutString("")
	case kindBool:
		out.PutID(boolFalseID)
	case kindVector:
		out.PutVectorHeader(0)
	case kindVectorBare:
		out.PutInt(0)
	default:
		return fmt.Errorf("cannot default kind %d (boxed object needs a transform)", f.kind)
	}
	return nil
}

// typeSig is a stable wire-type signature for matching/converter lookup.
func typeSig(f *fieldLayout) string {
	switch f.kind {
	case kindInt:
		return "int"
	case kindLong:
		return "long"
	case kindDouble:
		return "double"
	case kindInt128:
		return "int128"
	case kindInt256:
		return "int256"
	case kindBytes:
		return "bytes"
	case kindString:
		return "string"
	case kindBool:
		return "Bool"
	case kindTrue:
		return "true"
	case kindVector:
		return "Vector<" + typeSig(f.elem) + ">"
	case kindVectorBare:
		return "vector<" + typeSig(f.elem) + ">"
	case kindObject, kindBareObject:
		return f.typeName
	default:
		return fmt.Sprintf("kind%d", f.kind)
	}
}
