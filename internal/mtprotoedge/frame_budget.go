package mtprotoedge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto/codec"
	"github.com/gotd/td/transport"
)

const defaultInboundFrameGlobalMaxBytes int64 = 512 << 20

var (
	// ErrInboundFrameBudgetExceeded means the process-wide wire+plaintext reservation for a
	// newly announced transport frame could not be acquired. The length prefix has been read,
	// but the payload buffer has not been allocated and the connection must be closed.
	ErrInboundFrameBudgetExceeded = errors.New("inbound frame global byte budget exceeded")

	errInboundFrameCodecUnsupported = errors.New("transport codec cannot preflight inbound frame length")
	errInboundFrameNotReserved      = errors.New("transport codec returned a frame without reserving inbound bytes")
)

// InboundFrameBudgetedCodec is the fail-safe extension point for a custom Options.Codec.
// Implementations must parse and validate the frame length, call reserve exactly once before
// allocating or growing the payload buffer, and keep the reservation valid until Read returns.
// Built-in abridged/intermediate/padded-intermediate/full codecs are recognized directly.
type InboundFrameBudgetedCodec interface {
	transport.Codec
	ReadWithInboundFrameBudget(r io.Reader, b *bin.Buffer, reserve func(wireBytes, plaintextBytes int64) error) error
}

// inboundFrameBudget accounts the two per-frame buffers that can coexist while an encrypted
// request is handled: transport/wire bytes and decrypted plaintext. It deliberately charges the
// maximum plaintext size announced by framing even for an unencrypted handshake frame; that
// conservative rule makes admission independent of auth state and prevents allocation before
// auth_key_id can be inspected.
type inboundFrameBudget struct {
	max  int64
	used atomic.Int64
}

func newInboundFrameBudget(max int64) *inboundFrameBudget {
	if max <= 0 {
		max = defaultInboundFrameGlobalMaxBytes
	}
	return &inboundFrameBudget{max: max}
}

func (b *inboundFrameBudget) reserve(wireBytes, plaintextBytes int64) (int64, error) {
	return b.growReservation(0, wireBytes, plaintextBytes)
}

// growReservation atomically raises one connection's existing retained/frame reservation to
// cover a newly announced frame. Keeping the old charge until this transition is what makes a
// reused transport/plaintext backing remain accounted between frames; a small next frame cannot
// release a previously large allocation while still retaining its capacity.
func (b *inboundFrameBudget) growReservation(current, wireBytes, plaintextBytes int64) (int64, error) {
	if current < 0 || wireBytes <= 0 || plaintextBytes < 0 || wireBytes > b.max || plaintextBytes > b.max-wireBytes {
		return 0, fmt.Errorf("%w: wire=%d plaintext=%d limit=%d", ErrInboundFrameBudgetExceeded, wireBytes, plaintextBytes, b.max)
	}
	target := wireBytes + plaintextBytes
	if target <= current {
		return current, nil
	}
	n := target - current

	for {
		used := b.used.Load()
		if n > b.max-used {
			return 0, fmt.Errorf("%w: requested=%d used=%d limit=%d", ErrInboundFrameBudgetExceeded, n, used, b.max)
		}
		if b.used.CompareAndSwap(used, used+n) {
			return target, nil
		}
	}
}

func (b *inboundFrameBudget) release(n int64) {
	if n == 0 {
		return
	}
	used := b.used.Add(-n)
	if used < 0 {
		// This is an internal ownership invariant, not recoverable input. A negative value would
		// silently disable admission for subsequent frames, so fail loudly during development.
		panic("mtprotoedge: inbound frame budget released more than reserved")
	}
}

func (b *inboundFrameBudget) usedBytes() int64 {
	return b.used.Load()
}

type inboundFrameCodecKind uint8

const (
	inboundFrameCodecUnknown inboundFrameCodecKind = iota
	inboundFrameCodecQuickAckAbridged
	inboundFrameCodecAbridged
	inboundFrameCodecIntermediate
	inboundFrameCodecPaddedIntermediate
	inboundFrameCodecFull
	inboundFrameCodecCustom
)

func classifyInboundFrameCodec(c transport.Codec) inboundFrameCodecKind {
	switch v := c.(type) {
	case *quickAckAbridgedCodec:
		return inboundFrameCodecQuickAckAbridged
	case codec.Abridged, *codec.Abridged:
		return inboundFrameCodecAbridged
	case *quickAckIntermediateCodec, codec.Intermediate, *codec.Intermediate:
		return inboundFrameCodecIntermediate
	case *quickAckPaddedIntermediateCodec, codec.PaddedIntermediate, *codec.PaddedIntermediate:
		return inboundFrameCodecPaddedIntermediate
	case *codec.Full:
		return inboundFrameCodecFull
	case codec.NoHeader:
		return classifyInboundFrameCodec(v.Codec)
	case *codec.NoHeader:
		if v == nil {
			return inboundFrameCodecUnknown
		}
		return classifyInboundFrameCodec(v.Codec)
	case InboundFrameBudgetedCodec:
		return inboundFrameCodecCustom
	default:
		return inboundFrameCodecUnknown
	}
}

func unwrapInboundFrameBudgetedCodec(c transport.Codec) InboundFrameBudgetedCodec {
	switch v := c.(type) {
	case InboundFrameBudgetedCodec:
		return v
	case codec.NoHeader:
		return unwrapInboundFrameBudgetedCodec(v.Codec)
	case *codec.NoHeader:
		if v != nil {
			return unwrapInboundFrameBudgetedCodec(v.Codec)
		}
	}
	return nil
}

// inboundFramePreflightReader consumes only the framing length prefix, reserves the announced
// wire+plaintext bytes, and only then exposes the final prefix bytes to the codec. Consequently a
// budget error is observed by codec.Read before it can ResetN/Expand the payload buffer.
type inboundFramePreflightReader struct {
	r       io.Reader
	kind    inboundFrameCodecKind
	reserve func(wireBytes, plaintextBytes int64) error

	abridgedFirstDelivered bool
	done                   bool
}

func (r *inboundFramePreflightReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.done {
		return r.r.Read(p)
	}

	switch r.kind {
	case inboundFrameCodecQuickAckAbridged:
		return r.readAbridgedPrefix(p, true)
	case inboundFrameCodecAbridged:
		return r.readAbridgedPrefix(p, false)
	case inboundFrameCodecIntermediate, inboundFrameCodecPaddedIntermediate:
		return r.readWordPrefix(p, false)
	case inboundFrameCodecFull:
		return r.readWordPrefix(p, true)
	default:
		return 0, errInboundFrameCodecUnsupported
	}
}

func (r *inboundFramePreflightReader) readAbridgedPrefix(p []byte, quickAck bool) (int, error) {
	if !r.abridgedFirstDelivered {
		var first [1]byte
		if _, err := io.ReadFull(r.r, first[:]); err != nil {
			return 0, err
		}
		lengthByte := first[0]
		extended := lengthByte >= 0x7f
		if quickAck {
			lengthByte &= 0x7f
			extended = lengthByte == 0x7f
		}
		if !extended {
			n := int64(lengthByte) * bin.Word
			if err := reserveCompatFrame(r.reserve, n, n); err != nil {
				return 0, err
			}
			r.done = true
		}
		r.abridgedFirstDelivered = true
		p[0] = first[0]
		return 1, nil
	}

	var tail [3]byte
	if _, err := io.ReadFull(r.r, tail[:]); err != nil {
		return 0, err
	}
	words := uint32(tail[0]) | uint32(tail[1])<<8 | uint32(tail[2])<<16
	n := int64(words) * bin.Word
	if err := reserveCompatFrame(r.reserve, n, n); err != nil {
		return 0, err
	}
	r.done = true
	return copy(p, tail[:]), nil
}

func (r *inboundFramePreflightReader) readWordPrefix(p []byte, full bool) (int, error) {
	var header [bin.Word]byte
	if _, err := io.ReadFull(r.r, header[:]); err != nil {
		return 0, err
	}
	raw := int64(binary.LittleEndian.Uint32(header[:]))
	var wireBytes, plaintextBytes int64
	if full {
		// Full transport length includes length + sequence + payload + CRC.
		if raw < 3*bin.Word || raw > maxTransportMessageSize {
			return 0, fmt.Errorf("invalid full transport message length %d", raw)
		}
		wireBytes = raw
		plaintextBytes = raw - 3*bin.Word
	} else {
		wireBytes = raw &^ int64(quickAckResponseFlag)
		plaintextBytes = wireBytes
	}
	if err := reserveCompatFrame(r.reserve, wireBytes, plaintextBytes); err != nil {
		return 0, err
	}
	r.done = true
	return copy(p, header[:]), nil
}

func reserveCompatFrame(reserve func(wireBytes, plaintextBytes int64) error, wireBytes, plaintextBytes int64) error {
	if wireBytes <= 0 || wireBytes > maxTransportMessageSize {
		return fmt.Errorf("invalid transport message length %d", wireBytes)
	}
	return reserve(wireBytes, plaintextBytes)
}
