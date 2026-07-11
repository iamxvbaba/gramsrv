package layerwire

import "github.com/gotd/td/bin"

// Hand-written transforms for the message subtree: structural changes that are
// not pure field drops, and 227-only constructors that older clients cannot
// decode. By-abstract-type fallbacks auto-cover future variants of the same
// class (e.g. a new MessageAction added in a later layer still degrades to
// messageActionEmpty). See docs/layer-compat-220-227-design.md §5.2.

const (
	messageActionEmptyID   = 0xb6aef7b0 // messageActionEmpty = MessageAction
	messageEntityUnknownID = 0xbb92ba95 // messageEntityUnknown offset:int length:int = MessageEntity
	pageBlockUnsupportedID = 0x13567e8a // pageBlockUnsupported = PageBlock
	textEmptyID            = 0xdc3d824f // textEmpty = RichText
)

func init() {
	structuralTransforms["pollAnswerVoters"] = transcodePollAnswerVoters

	// 227-only constructors degrade to a class member every supported layer has.
	// Each target carries no body, so the replacement is a bare id (a new
	// variant inside a Vector keeps its slot — no element drop needed).
	newTypeFallbacksByType["MessageAction"] = replaceWithBare(messageActionEmptyID)
	newTypeFallbacksByType["PageBlock"] = replaceWithBare(pageBlockUnsupportedID)
	newTypeFallbacksByType["RichText"] = replaceWithBare(textEmptyID)
	newTypeFallbacksByType["MessageEntity"] = fallbackMessageEntity
}

// replaceWithBare consumes the canonical (227-only) object and emits a
// bodyless constructor id the target layer understands.
func replaceWithBare(id uint32) fallbackFunc {
	return func(cl *ctorLayout, in, out *bin.Buffer, layer, depth int, walk *walkState) error {
		if err := in.ConsumeID(cl.crc); err != nil {
			return err
		}
		if err := walk.skipCtorBody(canonical, in, cl, depth); err != nil {
			return err
		}
		out.PutID(id)
		return nil
	}
}

// peerVectorField is a synthetic Vector<Peer> layout used to consume the
// canonical recent_voters field.
var peerVectorField = fieldLayout{
	kind:    kindVector,
	flagBit: -1,
	elem:    &fieldLayout{kind: kindObject, typeName: "Peer", flagBit: -1},
}

var pollOptionBytesField = fieldLayout{kind: kindBytes, flagBit: -1}

// transcodePollAnswerVoters downgrades pollAnswerVoters: canonical (227) made
// voters conditional (flags.2?int) and added recent_voters (flags.2?Vector<Peer>);
// older layers carry voters as a plain int. The leading CRC is already consumed.
//
//	227: flags:# chosen:flags.0?true correct:flags.1?true option:bytes
//	     voters:flags.2?int recent_voters:flags.2?Vector<Peer>
//	<=226: flags:# chosen:flags.0?true correct:flags.1?true option:bytes voters:int
func transcodePollAnswerVoters(cl *ctorLayout, target uint32, in, out *bin.Buffer, layer, depth int, walk *walkState) error {
	flags, err := in.Uint32()
	if err != nil {
		return err
	}
	optionStart := in.Buf
	if err := walk.skipValue(canonical, in, &pollOptionBytesField, cl, depth); err != nil {
		return err
	}
	optionRaw := optionStart[:len(optionStart)-len(in.Buf)]
	var voters int
	if flags&(1<<2) != 0 {
		if voters, err = in.Int(); err != nil {
			return err
		}
		if err := walk.skipValue(canonical, in, &peerVectorField, cl, depth); err != nil {
			return err
		}
	}
	out.PutID(target)
	out.PutUint32(flags & 0b11) // retain chosen/correct, clear the moved bit 2
	out.Put(optionRaw)
	out.PutInt(voters)
	return nil
}

// fallbackMessageEntity replaces any 227-only MessageEntity with
// messageEntityUnknown, preserving offset/length so text positions stay valid.
func fallbackMessageEntity(cl *ctorLayout, in, out *bin.Buffer, layer, depth int, walk *walkState) error {
	id, err := in.PeekID()
	if err != nil {
		return err
	}
	if err := in.ConsumeID(id); err != nil {
		return err
	}
	offset, length, err := canonical.decodeOffsetLength(in, cl, depth, walk)
	if err != nil {
		return err
	}
	out.PutID(messageEntityUnknownID)
	out.PutInt(offset)
	out.PutInt(length)
	return nil
}

// decodeOffsetLength walks a constructor body (no leading CRC) per the canonical
// layout, returning its offset/length int fields and discarding the rest.
func (m *schemaModel) decodeOffsetLength(in *bin.Buffer, cl *ctorLayout, depth int, walk *walkState) (offset, length int, err error) {
	var flags map[string]uint32
	for i := range cl.fields {
		f := &cl.fields[i]
		if f.isFlags {
			v, e := in.Uint32()
			if e != nil {
				return 0, 0, e
			}
			if flags == nil {
				flags = make(map[string]uint32, 2)
			}
			flags[f.name] = v
			continue
		}
		if f.conditional() && flags[f.flagName]&(1<<uint(f.flagBit)) == 0 {
			continue
		}
		switch {
		case f.kind == kindInt && f.name == "offset":
			if offset, err = in.Int(); err != nil {
				return
			}
		case f.kind == kindInt && f.name == "length":
			if length, err = in.Int(); err != nil {
				return
			}
		default:
			if err = walk.skipValue(m, in, f, cl, depth); err != nil {
				return
			}
		}
	}
	return
}
