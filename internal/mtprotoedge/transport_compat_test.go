package mtprotoedge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/gotd/td/bin"
)

type countWriteBuffer struct {
	bytes.Buffer
	writes int
}

func (w *countWriteBuffer) Write(p []byte) (int, error) {
	w.writes++
	return w.Buffer.Write(p)
}

func TestQuickAckResponseEncoding(t *testing.T) {
	const token = 0x01020304

	abridged := (&quickAckAbridgedCodec{}).quickAckResponse(token)
	if want := []byte{0x81, 0x02, 0x03, 0x04}; !bytes.Equal(abridged[:], want) {
		t.Fatalf("abridged quick ack = %x, want %x", abridged, want)
	}

	intermediate := (&quickAckIntermediateCodec{}).quickAckResponse(token)
	if want := []byte{0x04, 0x03, 0x02, 0x81}; !bytes.Equal(intermediate[:], want) {
		t.Fatalf("intermediate quick ack = %x, want %x", intermediate, want)
	}
}

func TestQuickAckReadFlags(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	abridgedPacket := append([]byte{0x80 | byte(len(payload)/bin.Word)}, payload...)

	var got bin.Buffer
	requested, err := readQuickAckAbridged(bytes.NewReader(abridgedPacket), &got)
	if err != nil {
		t.Fatalf("read abridged: %v", err)
	}
	if !requested {
		t.Fatal("abridged quick ack flag was not detected")
	}
	if !bytes.Equal(got.Raw(), payload) {
		t.Fatalf("abridged payload = %x, want %x", got.Raw(), payload)
	}

	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(payload))|quickAckResponseFlag)
	intermediatePacket := append(header[:], payload...)
	requested, err = readQuickAckIntermediate(bytes.NewReader(intermediatePacket), &got, false)
	if err != nil {
		t.Fatalf("read intermediate: %v", err)
	}
	if !requested {
		t.Fatal("intermediate quick ack flag was not detected")
	}
	if !bytes.Equal(got.Raw(), payload) {
		t.Fatalf("intermediate payload = %x, want %x", got.Raw(), payload)
	}
}

func TestCompatPaddedIntermediateWriteRoundTrip(t *testing.T) {
	codec := &quickAckPaddedIntermediateCodec{}
	// 连写多帧：验证复用写缓冲不串包，且 padding 后仍能被读端正确剥离。
	for i := 0; i < 8; i++ {
		var payload bin.Buffer
		payload.PutInt32(int32(0x11220000 + i))
		payload.PutInt32(int32(0x33440000 + i))

		var out countWriteBuffer
		if err := codec.Write(&out, &payload); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if out.writes < 2 || out.writes > 3 {
			t.Fatalf("write %d: writes = %d, want header/payload[/padding] segments", i, out.writes)
		}
		total := binary.LittleEndian.Uint32(out.Bytes()[:4])
		if int(total) != len(out.Bytes())-4 {
			t.Fatalf("write %d: header length = %d, body = %d", i, total, len(out.Bytes())-4)
		}

		var got bin.Buffer
		requested, err := readQuickAckIntermediate(bytes.NewReader(out.Bytes()), &got, true)
		if err != nil {
			t.Fatalf("read back %d: %v", i, err)
		}
		if requested {
			t.Fatalf("read back %d: unexpected quick ack flag", i)
		}
		if !bytes.Equal(got.Raw(), payload.Raw()) {
			t.Fatalf("read back %d: payload = %x, want %x", i, got.Raw(), payload.Raw())
		}
	}
}

func TestCompatTransportCodecsWriteSegmentedPacketWithoutFullCopy(t *testing.T) {
	var payload bin.Buffer
	payload.PutInt32(0x01020304)
	payload.PutInt32(0x05060708)

	t.Run("abridged", func(t *testing.T) {
		var out countWriteBuffer
		if err := (&quickAckAbridgedCodec{}).Write(&out, &payload); err != nil {
			t.Fatalf("write: %v", err)
		}
		if out.writes != 2 {
			t.Fatalf("generic writer calls = %d, want header+payload; raw TCP uses vectored I/O", out.writes)
		}
		if got, want := out.Bytes()[0], byte(payload.Len()/bin.Word); got != want {
			t.Fatalf("abridged header = %#x, want %#x", got, want)
		}
	})

	t.Run("intermediate", func(t *testing.T) {
		var out countWriteBuffer
		if err := (&quickAckIntermediateCodec{}).Write(&out, &payload); err != nil {
			t.Fatalf("write: %v", err)
		}
		if out.writes != 2 {
			t.Fatalf("generic writer calls = %d, want header+payload; raw TCP uses vectored I/O", out.writes)
		}
		if got, want := binary.LittleEndian.Uint32(out.Bytes()[:4]), uint32(payload.Len()); got != want {
			t.Fatalf("intermediate length = %d, want %d", got, want)
		}
	})
}
