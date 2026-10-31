package livestream

import (
	"encoding/binary"
	"testing"
)

// readTLString replicates tgcalls readSerializedString, used to decode the header of packUnifiedPart.
func readTLString(data []byte, off *int) (string, bool) {
	if *off >= len(data) {
		return "", false
	}
	first := int(data[*off])
	*off++
	var length, padding int
	if first == 254 {
		if *off+3 > len(data) {
			return "", false
		}
		length = int(data[*off]) | int(data[*off+1])<<8 | int(data[*off+2])<<16
		*off += 3
		padding = (4 - length%4) % 4
	} else {
		length = first
		padding = (4 - (length+1)%4) % 4
	}
	if *off+length > len(data) {
		return "", false
	}
	s := string(data[*off : *off+length])
	*off += length + padding
	return s, true
}

func readI32(data []byte, off *int) (int32, bool) {
	if *off+4 > len(data) {
		return 0, false
	}
	v := int32(binary.LittleEndian.Uint32(data[*off : *off+4]))
	*off += 4
	return v, true
}

// TestPackUnifiedPartMatchesTgcallsHeader verifies that each field of the packed header can
// be decoded by tgcalls consumeVideoStreamInfo: signature, container name, activeMask, single
// event (endpoint="unified"), and that event.offset=0 corresponds to the TS data that
// immediately follows.
func TestPackUnifiedPartMatchesTgcallsHeader(t *testing.T) {
	ts := []byte{0x47, 0x40, 0x00, 0x10, 0xDE, 0xAD, 0xBE, 0xEF} // fake TS data
	part := packUnifiedPart(ts)

	off := 0
	sig, ok := readI32(part, &off)
	if !ok || uint32(sig) != partSignature {
		t.Fatalf("signature = %#x ok=%v, want %#x", uint32(sig), ok, partSignature)
	}
	container, ok := readTLString(part, &off)
	if !ok || container != partContainer {
		t.Fatalf("container = %q ok=%v, want %q", container, ok, partContainer)
	}
	activeMask, ok := readI32(part, &off)
	if !ok || activeMask != 1 {
		t.Fatalf("activeMask = %d ok=%v, want 1", activeMask, ok)
	}
	eventCount, ok := readI32(part, &off)
	if !ok || eventCount != 1 {
		t.Fatalf("eventCount = %d ok=%v, want 1", eventCount, ok)
	}
	eventOffset, ok := readI32(part, &off)
	if !ok || eventOffset != 0 {
		t.Fatalf("event.offset = %d ok=%v, want 0", eventOffset, ok)
	}
	endpoint, ok := readTLString(part, &off)
	if !ok || endpoint != partEndpointID {
		t.Fatalf("endpoint = %q ok=%v, want %q", endpoint, ok, partEndpointID)
	}
	rotation, _ := readI32(part, &off)
	extra, _ := readI32(part, &off)
	if rotation != 0 || extra != 0 {
		t.Fatalf("rotation/extra = %d/%d, want 0/0", rotation, extra)
	}
	// After the header (from event.offset=0) should be the raw TS data.
	if got := part[off:]; string(got) != string(ts) {
		t.Fatalf("payload = %x, want %x", got, ts)
	}
}

// TestAppendTLStringPadding verifies that both short and long strings are padded to a 4-byte boundary.
func TestAppendTLStringPadding(t *testing.T) {
	for _, s := range []string{"", "a", "ab", "abc", "mpegts", "unified"} {
		out := appendTLString(nil, s)
		if len(out)%4 != 0 {
			t.Fatalf("appendTLString(%q) len=%d not 4-aligned", s, len(out))
		}
		off := 0
		got, ok := readTLString(out, &off)
		if !ok || got != s {
			t.Fatalf("roundtrip %q -> %q ok=%v", s, got, ok)
		}
		if off != len(out) {
			t.Fatalf("roundtrip %q consumed %d of %d bytes", s, off, len(out))
		}
	}
}
