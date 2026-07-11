package layerwire

import (
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/gotd/td/bin"
)

// ErrMalformed identifies invalid or truncated TL wire data. Callers may use
// errors.Is to distinguish it from an otherwise well-formed request which was
// rejected by a walker resource limit.
var ErrMalformed = errors.New("layerwire: malformed TL")

// ErrResourceLimit identifies structurally valid-looking TL input which would
// exceed a walker resource budget.
var ErrResourceLimit = errors.New("layerwire: resource limit")

const (
	defaultMaxVectorElements = 4096
	defaultMaxWalkDepth      = 32
	defaultMaxWalkUnits      = 131072 // constructors + declared vector elements
	defaultMaxFieldBytes     = 16 << 20
	defaultMaxTotalBytes     = 32 << 20
)

// A very small number of API methods have a documented limit above the
// package-wide default. Keeping overrides keyed by constructor and field makes
// every exception explicit and prevents a large vector in an unrelated method
// from inheriting the larger allowance.
type vectorLimitKey struct {
	owner string
	field string
}

var vectorElementLimitOverrides = map[vectorLimitKey]int{
	{owner: "contacts.editCloseFriends", field: "id"}: 5000,
	{owner: "contacts.setBlocked", field: "id"}:       5000,
}

type walkLimits struct {
	maxVectorElements int
	maxDepth          int
	maxUnits          uint64
	maxFieldBytes     uint64
	maxTotalBytes     uint64
}

var defaultWalkLimits = walkLimits{
	maxVectorElements: defaultMaxVectorElements,
	maxDepth:          defaultMaxWalkDepth,
	maxUnits:          defaultMaxWalkUnits,
	maxFieldBytes:     defaultMaxFieldBytes,
	maxTotalBytes:     defaultMaxTotalBytes,
}

// walkState is deliberately request-scoped. Every branch of one transform
// shares it, so splitting a large value across nested constructors or vectors
// cannot reset the aggregate budgets.
type walkState struct {
	limits walkLimits
	units  uint64
	bytes  uint64
}

func newWalkState() *walkState {
	return &walkState{limits: defaultWalkLimits}
}

func malformedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

func limitf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrResourceLimit, fmt.Sprintf(format, args...))
}

// classifyWalkError makes all public walker/transform failures classifiable,
// including errors returned by the low-level gotd bin decoder.
func classifyWalkError(err error) error {
	if err == nil || errors.Is(err, ErrMalformed) || errors.Is(err, ErrResourceLimit) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrMalformed, err)
}

func (s *walkState) enter(depth int, what string) error {
	if depth <= 0 || depth > s.limits.maxDepth {
		return limitf("%s nesting depth %d exceeds limit %d", what, depth, s.limits.maxDepth)
	}
	return s.addUnits(1, what)
}

func (s *walkState) addUnits(n int, what string) error {
	if n < 0 {
		return malformedf("negative %s count %d", what, n)
	}
	u := uint64(n)
	// Subtraction form avoids overflow even if limits are changed later.
	if s.units > s.limits.maxUnits || u > s.limits.maxUnits-s.units {
		return limitf("constructor/vector element budget exceeds %d at %s", s.limits.maxUnits, what)
	}
	s.units += u
	return nil
}

func (s *walkState) addBytes(n uint64, what string) error {
	if n > s.limits.maxFieldBytes {
		return limitf("%s payload length %d exceeds per-field limit %d", what, n, s.limits.maxFieldBytes)
	}
	if s.bytes > s.limits.maxTotalBytes || n > s.limits.maxTotalBytes-s.bytes {
		return limitf("string/bytes payload budget exceeds %d at %s", s.limits.maxTotalBytes, what)
	}
	s.bytes += n
	return nil
}

func (s *walkState) vectorLimit(owner *ctorLayout, f *fieldLayout) int {
	if owner != nil && f != nil {
		if n := vectorElementLimitOverrides[vectorLimitKey{owner: owner.name, field: f.name}]; n > 0 {
			return n
		}
	}
	return s.limits.maxVectorElements
}

const maxConstructorFlagWords = 8

type constructorFlagWord struct {
	name  string
	value uint32
}

// ValidateCanonicalRequest performs a complete, allocation-free structural
// preflight of one canonical Layer 227 method request. It is intended for the
// router seam immediately before typed dispatch. A successful result means the
// walker consumed exactly one known function constructor and all of its body.
func ValidateCanonicalRequest(body []byte) error {
	b := &bin.Buffer{Buf: body}
	id, err := b.PeekID()
	if err != nil {
		return classifyWalkError(err)
	}
	cl := canonical.byCRC[id]
	if cl == nil {
		return malformedf("unknown canonical request constructor %#08x", id)
	}
	if !cl.isFunc {
		return malformedf("constructor %s (%#08x) is not a method", cl.name, id)
	}
	return validateRequestLayout(canonical, cl, body)
}

func validateRequestLayout(m *schemaModel, cl *ctorLayout, body []byte) error {
	b := &bin.Buffer{Buf: body}
	s := newWalkState()
	if err := s.skipObject(m, b, 1); err != nil {
		return classifyWalkError(err)
	}
	if b.Len() != 0 {
		return malformedf("%d trailing bytes after canonical request %s", b.Len(), cl.name)
	}
	return nil
}

// skipObject advances b past one boxed object (CRC + body), resolving the
// constructor from m. This compatibility wrapper creates a fresh budget; all
// production transforms call the stateful variant directly.
func (m *schemaModel) skipObject(b *bin.Buffer) error {
	return classifyWalkError(newWalkState().skipObject(m, b, 1))
}

func (s *walkState) skipObject(m *schemaModel, b *bin.Buffer, depth int) error {
	if err := s.enter(depth, "constructor"); err != nil {
		return err
	}
	id, err := b.PeekID()
	if err != nil {
		return err
	}
	cl, ok := m.byCRC[id]
	if !ok {
		return malformedf("unknown constructor %#08x", id)
	}
	if err := b.ConsumeID(id); err != nil {
		return err
	}
	return s.skipCtorBody(m, b, cl, depth)
}

// skipCtorBody advances b past a constructor body (no leading CRC), evaluating
// flag integers so conditional fields are read iff present. The constructor's
// unit and depth have already been charged by the caller.
func (s *walkState) skipCtorBody(m *schemaModel, b *bin.Buffer, cl *ctorLayout, depth int) error {
	// Layer 227 constructors currently use at most flags + flags2. Keep generous fixed stack
	// storage so the allocation-free preflight remains allocation-free on the hottest flagged
	// methods; the explicit bound also prevents a future malformed/generated layout from turning
	// every request into an attacker-amplified map allocation.
	var flags [maxConstructorFlagWords]constructorFlagWord
	flagCount := 0
	for i := range cl.fields {
		f := &cl.fields[i]
		if f.isFlags {
			v, err := b.Uint32()
			if err != nil {
				return fmt.Errorf("%s.%s: %w", cl.name, f.name, err)
			}
			if flagCount >= len(flags) {
				return limitf("constructor %s has more than %d flags words", cl.name, len(flags))
			}
			flags[flagCount] = constructorFlagWord{name: f.name, value: v}
			flagCount++
			continue
		}
		if f.conditional() {
			var (
				flagValue uint32
				found     bool
			)
			for j := 0; j < flagCount; j++ {
				if flags[j].name == f.flagName {
					flagValue = flags[j].value
					found = true
					break
				}
			}
			if !found {
				return malformedf("constructor %s conditional field %s references missing flags word %s", cl.name, f.name, f.flagName)
			}
			if flagValue&(1<<uint(f.flagBit)) == 0 {
				continue
			}
		}
		if err := s.skipValue(m, b, f, cl, depth); err != nil {
			return fmt.Errorf("%s.%s: %w", cl.name, f.name, err)
		}
	}
	return nil
}

// skipValue advances b past one already-known-present field value.
func (s *walkState) skipValue(m *schemaModel, b *bin.Buffer, f *fieldLayout, owner *ctorLayout, depth int) error {
	switch f.kind {
	case kindInt:
		return skipFixed(b, 4)
	case kindLong, kindDouble:
		return skipFixed(b, 8)
	case kindInt128:
		return skipFixed(b, 16)
	case kindInt256:
		return skipFixed(b, 32)
	case kindBytes:
		return s.skipTLBytes(b, "bytes")
	case kindString:
		return s.skipTLBytes(b, "string")
	case kindBool:
		if err := s.addUnits(1, "Bool constructor"); err != nil {
			return err
		}
		id, err := b.Uint32()
		if err != nil {
			return err
		}
		if id != bin.TypeTrue && id != bin.TypeFalse {
			return malformedf("invalid Bool constructor %#08x", id)
		}
		return nil
	case kindTrue:
		return nil
	case kindVector, kindVectorBare:
		vectorDepth := depth + 1
		if vectorDepth <= 0 || vectorDepth > s.limits.maxDepth {
			return limitf("vector nesting depth %d exceeds limit %d", vectorDepth, s.limits.maxDepth)
		}
		if f.kind == kindVector {
			id, err := b.Uint32()
			if err != nil {
				return err
			}
			if id != vectorTypeID {
				return malformedf("expected vector id, got %#08x", id)
			}
		}
		n, err := b.Int()
		if err != nil {
			return err
		}
		if n < 0 {
			return malformedf("negative vector length %d", n)
		}
		if max := s.vectorLimit(owner, f); n > max {
			return limitf("vector %s.%s length %d exceeds limit %d", ownerName(owner), fieldName(f), n, max)
		}
		if err := s.addUnits(n, "vector "+ownerName(owner)+"."+fieldName(f)); err != nil {
			return err
		}
		if width, ok := fixedWireWidth(f.elem); ok {
			total, ok := checkedMulInt(n, width)
			if !ok {
				return malformedf("vector byte length overflow: %d * %d", n, width)
			}
			return skipFixed(b, total)
		}
		for i := 0; i < n; i++ {
			if err := s.skipValue(m, b, f.elem, nil, vectorDepth); err != nil {
				return fmt.Errorf("vector element %d: %w", i, err)
			}
		}
		return nil
	case kindObject:
		return s.skipObject(m, b, depth+1)
	case kindBareObject:
		bareDepth := depth + 1
		if err := s.enter(bareDepth, "bare constructor"); err != nil {
			return err
		}
		cl, ok := m.bareByT[f.typeName]
		if !ok {
			return malformedf("unknown bare type %q", f.typeName)
		}
		return s.skipCtorBody(m, b, cl, bareDepth)
	default:
		return malformedf("bad wire kind %d", f.kind)
	}
}

// skipTLBytes parses TL's 1/4-byte length prefix directly and advances the
// input slice. Unlike bin.Buffer.Bytes it never copies payload data.
func (s *walkState) skipTLBytes(b *bin.Buffer, what string) error {
	if len(b.Buf) == 0 {
		return io.ErrUnexpectedEOF
	}
	var header, payload uint64
	switch b.Buf[0] {
	case 254:
		if len(b.Buf) < 4 {
			return io.ErrUnexpectedEOF
		}
		header = 4
		payload = uint64(b.Buf[1]) | uint64(b.Buf[2])<<8 | uint64(b.Buf[3])<<16
	case 255:
		return malformedf("invalid %s length prefix 255", what)
	default:
		header = 1
		payload = uint64(b.Buf[0])
	}
	if err := s.addBytes(payload, what); err != nil {
		return err
	}
	encoded, ok := checkedAddUint64(header, payload)
	if !ok {
		return malformedf("%s encoded length overflow", what)
	}
	withPadding, ok := checkedAddUint64(encoded, 3)
	if !ok {
		return malformedf("%s padded length overflow", what)
	}
	padded := withPadding &^ uint64(3)
	if padded > uint64(math.MaxInt) {
		return malformedf("%s padded length %d overflows int", what, padded)
	}
	if uint64(len(b.Buf)) < padded {
		return io.ErrUnexpectedEOF
	}
	b.Buf = b.Buf[int(padded):]
	return nil
}

func skipFixed(b *bin.Buffer, n int) error {
	if n < 0 {
		return malformedf("negative fixed-width skip %d", n)
	}
	if len(b.Buf) < n {
		return io.ErrUnexpectedEOF
	}
	b.Buf = b.Buf[n:]
	return nil
}

func fixedWireWidth(f *fieldLayout) (int, bool) {
	if f == nil {
		return 0, false
	}
	switch f.kind {
	case kindInt:
		return 4, true
	case kindLong, kindDouble:
		return 8, true
	case kindInt128:
		return 16, true
	case kindInt256:
		return 32, true
	case kindTrue:
		return 0, true
	default:
		// Bool deliberately stays on the element loop so constructor ids are
		// validated and charged to the aggregate constructor budget.
		return 0, false
	}
}

func checkedMulInt(a, b int) (int, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	if a != 0 && b > math.MaxInt/a {
		return 0, false
	}
	return a * b, true
}

func checkedAddUint64(a, b uint64) (uint64, bool) {
	if b > math.MaxUint64-a {
		return 0, false
	}
	return a + b, true
}

func ownerName(cl *ctorLayout) string {
	if cl == nil || cl.name == "" {
		return "<nested>"
	}
	return cl.name
}

func fieldName(f *fieldLayout) string {
	if f == nil || f.name == "" {
		return "<element>"
	}
	return f.name
}
