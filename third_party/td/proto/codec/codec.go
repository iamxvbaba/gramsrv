package codec

import (
	"encoding/binary"
	"io"

	"github.com/go-faster/errors"

	"github.com/iamxvbaba/td/bin"
)

// inboundReadChunkSize limits memory committed solely on the strength of an
// untrusted transport length prefix. Larger messages grow progressively as
// their bytes arrive.
const inboundReadChunkSize = 32 << 10

// Codec is MTProto transport protocol encoding abstraction.
type Codec interface {
	// WriteHeader sends protocol tag if needed.
	WriteHeader(w io.Writer) error
	// ReadHeader reads protocol tag if needed.
	ReadHeader(r io.Reader) error
	// Write encode to writer message from given buffer.
	Write(w io.Writer, b *bin.Buffer) error
	// Read fills buffer with received message.
	Read(r io.Reader, b *bin.Buffer) error
}

// TaggedCodec is codec with protocol tag.
type TaggedCodec interface {
	Codec
	// ObfuscatedTag returns protocol tag for obfuscation.
	ObfuscatedTag() [4]byte
}

// readLen reads 32-bit integer and validates it as message length.
func readLen(r io.Reader, b *bin.Buffer) (int, error) {
	b.ResetN(bin.Word)
	if _, err := io.ReadFull(r, b.Buf[:bin.Word]); err != nil {
		return 0, errors.Wrap(err, "read length")
	}
	n := int(binary.LittleEndian.Uint32(b.Buf[:bin.Word]))

	if err := checkMessageLength(n); err != nil {
		return 0, err
	}

	return n, nil
}

// readPayload reads exactly n bytes while growing b in bounded increments.
// A peer that sends only a large length prefix therefore cannot make the
// process allocate the entire declared frame before sending its payload.
func readPayload(r io.Reader, b *bin.Buffer, n int) error {
	b.Reset()
	return appendPayload(r, b, n)
}

func appendPayload(r io.Reader, b *bin.Buffer, n int) error {
	if n < 0 {
		return invalidMsgLenErr{n: n}
	}

	target := b.Len() + n
	for remaining := n; remaining > 0; {
		chunk := remaining
		if chunk > inboundReadChunkSize {
			chunk = inboundReadChunkSize
		}

		start := b.Len()
		end := start + chunk
		if cap(b.Buf) < end {
			nextCap := cap(b.Buf) * 2
			if nextCap < end {
				nextCap = end
			}
			if nextCap > target {
				nextCap = target
			}
			grown := make([]byte, start, nextCap)
			copy(grown, b.Buf)
			b.Buf = grown
		}
		b.Buf = b.Buf[:end]
		read, err := io.ReadFull(r, b.Buf[start:start+chunk])
		if read != chunk {
			// Do not expose zero-filled bytes which were never received.
			b.Buf = b.Buf[:start+read]
		}
		if err != nil {
			return err
		}
		remaining -= chunk
	}
	return nil
}
