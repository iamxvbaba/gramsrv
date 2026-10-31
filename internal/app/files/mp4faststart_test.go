package files

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// box builds an MP4 box: [size(4)][type(4)][payload].
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

// stcoBox builds an stco with a single chunk offset: [ver+flags(4)][count(4)=1][offset(4)].
func stcoBox(offset uint32) []byte {
	p := make([]byte, 12)
	binary.BigEndian.PutUint32(p[4:8], 1) // entry_count
	binary.BigEndian.PutUint32(p[8:12], offset)
	return box("stco", p)
}

// buildMoovEndMP4 builds a minimal valid MP4 with moov at the end and stco pointing at an offset
// inside mdat. Returns (mp4, mdatPayloadAbsOffset).
func buildMoovEndMP4(mdatPayload []byte) ([]byte, uint32) {
	ftyp := box("ftyp", []byte("isom\x00\x00\x02\x00"))
	mdat := box("mdat", mdatPayload)
	mdatAbs := uint32(len(ftyp) + 8) // the mdat payload immediately follows the mdat header (8 bytes)
	// moov→trak→mdia→minf→stbl→stco(offset=mdatAbs)
	stbl := box("stbl", stcoBox(mdatAbs))
	minf := box("minf", stbl)
	mdia := box("mdia", minf)
	trak := box("trak", mdia)
	moov := box("moov", trak)
	out := append([]byte(nil), ftyp...)
	out = append(out, mdat...)
	out = append(out, moov...)
	return out, mdatAbs
}

func TestFaststartMP4MovesMoovAndFixesOffsets(t *testing.T) {
	marker := []byte("THE-REAL-CHUNK-DATA-HERE")
	// mdat payload: padding + marker, with stco pointing at the marker's absolute offset.
	pad := bytes.Repeat([]byte{0xAB}, 40)
	mdatPayload := append(append([]byte(nil), pad...), marker...)
	_, mdatAbs := buildMoovEndMP4(mdatPayload)
	markerAbs := mdatAbs + uint32(len(pad)) // the marker's absolute offset in the original file

	// The original stco points at mdatAbs (the mdat payload head). Here stco is changed to point at the marker for assertion.
	in2, _ := buildMoovEndMP4Marker(mdatPayload, markerAbs)
	// Precondition check: markerAbs in the original file really is the marker.
	if !bytes.Equal(in2[markerAbs:markerAbs+uint32(len(marker))], marker) {
		t.Fatalf("setup: markerAbs in the original file is not the marker")
	}

	out, changed := faststartMP4(in2)
	if !changed {
		t.Fatalf("changed = false, want true (moov at the end should be moved)")
	}
	if len(out) != len(in2) {
		t.Fatalf("size not conserved: in=%d out=%d", len(in2), len(out))
	}
	// Output order: ftyp, moov, mdat.
	boxes, ok := parseTopLevelBoxes(out)
	if !ok || len(boxes) != 3 || boxes[0].typ != "ftyp" || boxes[1].typ != "moov" || boxes[2].typ != "mdat" {
		t.Fatalf("wrong output top-level order: %+v ok=%v", boxes, ok)
	}
	// Take the stco offset in the output moov; it should point to where the marker still is in the output file.
	newOff := readSingleStcoOffset(t, out[boxes[1].start:boxes[1].end])
	if int(newOff)+len(marker) > len(out) {
		t.Fatalf("new offset out of range: %d", newOff)
	}
	got := out[newOff : int(newOff)+len(marker)]
	if !bytes.Equal(got, marker) {
		t.Fatalf("new stco offset %d points at %q, want %q (offset correction wrong)", newOff, got, marker)
	}
	// New offset = original offset + moovSize.
	moovSize := boxes[1].end - boxes[1].start
	if int(newOff) != int(markerAbs)+moovSize {
		t.Fatalf("new offset = %d, want original %d + moovSize %d", newOff, markerAbs, moovSize)
	}
}

func TestFaststartMP4NoopWhenAlreadyFaststart(t *testing.T) {
	// moov before mdat → already faststart, should be returned unchanged.
	ftyp := box("ftyp", []byte("isom\x00\x00\x02\x00"))
	stbl := box("stbl", stcoBox(100))
	moov := box("moov", box("trak", box("mdia", box("minf", stbl))))
	mdat := box("mdat", bytes.Repeat([]byte{1}, 64))
	in := append(append(append([]byte(nil), ftyp...), moov...), mdat...)
	out, changed := faststartMP4(in)
	if changed {
		t.Fatalf("already faststart should not be modified")
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("should be returned unchanged")
	}
}

func TestInspectMP4LayoutDetectsMoovAtEnd(t *testing.T) {
	in, _ := buildMoovEndMP4(bytes.Repeat([]byte{0xCD}, 100))
	readAt := func(off, n int64) ([]byte, error) {
		if off+n > int64(len(in)) {
			n = int64(len(in)) - off
		}
		return in[off : off+n], nil
	}
	l, ok := inspectMP4Layout(int64(len(in)), readAt)
	if !ok {
		t.Fatalf("inspect failed")
	}
	if !l.needsFaststart {
		t.Fatalf("moov at the end should give needsFaststart=true")
	}
	if !l.moovIsLast {
		t.Fatalf("moov is the last box and should give moovIsLast=true")
	}
	if l.ftypStart != 0 || l.moovEnd != int64(len(in)) {
		t.Fatalf("wrong range: %+v (len=%d)", l, len(in))
	}
}

func TestInspectMP4LayoutNoFaststartWhenMoovFirst(t *testing.T) {
	ftyp := box("ftyp", []byte("isom\x00\x00\x02\x00"))
	moov := box("moov", box("trak", box("mdia", box("minf", box("stbl", stcoBox(100))))))
	mdat := box("mdat", bytes.Repeat([]byte{1}, 100))
	in := append(append(append([]byte(nil), ftyp...), moov...), mdat...)
	readAt := func(off, n int64) ([]byte, error) {
		if off+n > int64(len(in)) {
			n = int64(len(in)) - off
		}
		return in[off : off+n], nil
	}
	l, ok := inspectMP4Layout(int64(len(in)), readAt)
	if !ok || l.needsFaststart {
		t.Fatalf("moov first should give needsFaststart=false, got ok=%v %+v", ok, l)
	}
}

// The streaming assembly (ftyp + patched moov + mid section) must match the full faststartMP4 output byte for byte.
func TestStreamingAssemblyMatchesFullRewrite(t *testing.T) {
	in, _ := buildMoovEndMP4(bytes.Repeat([]byte{0xEE}, 256))
	readAt := func(off, n int64) ([]byte, error) { return in[off : off+n], nil }
	l, ok := inspectMP4Layout(int64(len(in)), readAt)
	if !ok || !l.needsFaststart || !l.moovIsLast {
		t.Fatalf("setup: %+v ok=%v", l, ok)
	}
	// Streaming version: ftyp + patched moov + in[ftypEnd:moovStart]
	ftyp := append([]byte(nil), in[l.ftypStart:l.ftypEnd]...)
	moov := append([]byte(nil), in[l.moovStart:l.moovEnd]...)
	if !patchChunkOffsets(moov, l.moovEnd-l.moovStart) {
		t.Fatalf("patch failed")
	}
	var streaming []byte
	streaming = append(streaming, ftyp...)
	streaming = append(streaming, moov...)
	streaming = append(streaming, in[l.ftypEnd:l.moovStart]...)

	full, changed := faststartMP4(in)
	if !changed {
		t.Fatalf("full should be changed")
	}
	if !bytes.Equal(streaming, full) {
		t.Fatalf("streaming assembly differs from full rewrite: streaming=%d full=%d", len(streaming), len(full))
	}
}

func TestFaststartMP4NoopOnNonMP4(t *testing.T) {
	for _, data := range [][]byte{
		nil,
		[]byte("not an mp4 at all"),
		{0, 0, 0, 4}, // size too small
	} {
		if out, changed := faststartMP4(data); changed || !bytes.Equal(out, data) {
			t.Fatalf("non-MP4 should be left unchanged: changed=%v", changed)
		}
	}
}

// buildMoovEndMP4Marker is like buildMoovEndMP4, but stco points at the given absolute offset.
func buildMoovEndMP4Marker(mdatPayload []byte, stcoOffset uint32) ([]byte, uint32) {
	ftyp := box("ftyp", []byte("isom\x00\x00\x02\x00"))
	mdat := box("mdat", mdatPayload)
	mdatAbs := uint32(len(ftyp) + 8)
	stbl := box("stbl", stcoBox(stcoOffset))
	moov := box("moov", box("trak", box("mdia", box("minf", stbl))))
	out := append([]byte(nil), ftyp...)
	out = append(out, mdat...)
	out = append(out, moov...)
	return out, mdatAbs
}

func readSingleStcoOffset(t *testing.T, moov []byte) uint32 {
	t.Helper()
	idx := bytes.Index(moov, []byte("stco"))
	if idx < 0 {
		t.Fatalf("stco not found in moov")
	}
	// After the stco header: type(4) is already at idx; the payload starts at idx+4 = ver+flags(4)+count(4)+offset(4)
	off := idx + 4 + 4 + 4
	return binary.BigEndian.Uint32(moov[off : off+4])
}
