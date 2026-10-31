package files

import "encoding/binary"

// faststartMP4 moves the MP4 moov atom before mdat (faststart) and adds the moov size to every
// stco/co64 chunk offset inside moov (inserting moov before mdat shifts all media data later).
// It returns (newData, changed). For non-MP4 / already-faststart / any parse error it returns
// (data, false) unchanged -- this is the upload hot path, so it is better to skip the optimization
// than to corrupt a good video.
//
// Background: TDesktop's story streaming path cannot handle videos whose moov is at the end of the
// file (av_read_frame reports Invalid data), and ordinary Telegram clients faststart before upload.
// telesrv performs this step before writing to disk so the supports_streaming=true promise holds for
// all clients. No transcoding; the original encoding (including HEVC) is preserved.
func faststartMP4(data []byte) ([]byte, bool) {
	boxes, ok := parseTopLevelBoxes(data)
	if !ok {
		return data, false
	}
	ftypIdx, moovIdx, firstMdatIdx := -1, -1, -1
	for i, b := range boxes {
		switch b.typ {
		case "ftyp":
			if ftypIdx < 0 {
				ftypIdx = i
			}
		case "moov":
			if moovIdx < 0 {
				moovIdx = i
			}
		case "mdat":
			if firstMdatIdx < 0 {
				firstMdatIdx = i
			}
		}
	}
	// Must have ftyp (and first), moov, mdat; if moov is already before mdat it is already faststart.
	if ftypIdx != 0 || moovIdx < 0 || firstMdatIdx < 0 {
		return data, false
	}
	if moovIdx < firstMdatIdx {
		return data, false
	}

	// Copy out moov (independent backing array, so patching offsets in place does not touch the original data).
	moov := append([]byte(nil), data[boxes[moovIdx].start:boxes[moovIdx].end]...)
	moovSize := int64(len(moov))
	if !patchChunkOffsets(moov, moovSize) {
		return data, false
	}

	// Reassemble: ftyp + moov + the remaining boxes (except ftyp/moov) in their original order.
	out := make([]byte, 0, len(data))
	out = append(out, data[boxes[ftypIdx].start:boxes[ftypIdx].end]...)
	out = append(out, moov...)
	for i, b := range boxes {
		if i == ftypIdx || i == moovIdx {
			continue
		}
		out = append(out, data[b.start:b.end]...)
	}
	if len(out) != len(data) {
		// Length must be conserved (we only move, never resize); if it is not, something was miscomputed, so bail out conservatively.
		return data, false
	}
	return out, true
}

// mp4Layout is top-level structure info derived from reading only box headers, used to decide
// whether faststart is needed and how to rewrite the stream without reading the entire media.
type mp4Layout struct {
	needsFaststart     bool // moov comes after mdat
	moovIsLast         bool // moov is the last top-level box (streaming rewrite is possible)
	ftypStart, ftypEnd int64
	moovStart, moovEnd int64
}

// inspectMP4Layout reads only the top-level box headers (each <=16 bytes, skipping box payloads) to
// determine the structure, avoiding reading the whole video into memory just to check whether it is
// already faststart. readAt(off, n) reads bytes [off, off+n). For non-MP4 / malformed structure it
// returns (·, false).
func inspectMP4Layout(size int64, readAt func(off, n int64) ([]byte, error)) (mp4Layout, bool) {
	l := mp4Layout{moovStart: -1, moovEnd: -1}
	mdatStart := int64(-1)
	ftypSeen := false
	p := int64(0)
	for boxes := 0; p+8 <= size; boxes++ {
		if boxes > 1024 { // top-level box count cap, defending against malformed files
			return mp4Layout{}, false
		}
		hdr, err := readAt(p, 16)
		if err != nil || int64(len(hdr)) < 8 {
			return mp4Layout{}, false
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		switch {
		case boxSize == 1:
			if len(hdr) < 16 {
				return mp4Layout{}, false
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
		case boxSize == 0:
			boxSize = size - p
		}
		if boxSize < 8 || p+boxSize > size {
			return mp4Layout{}, false
		}
		switch typ {
		case "ftyp":
			if p != 0 {
				return mp4Layout{}, false // ftyp must come first
			}
			ftypSeen = true
			l.ftypStart, l.ftypEnd = p, p+boxSize
		case "moov":
			if l.moovStart < 0 {
				l.moovStart, l.moovEnd = p, p+boxSize
			}
		case "mdat":
			if mdatStart < 0 {
				mdatStart = p
			}
		}
		p += boxSize
	}
	if p != size || !ftypSeen || l.moovStart < 0 || mdatStart < 0 {
		return mp4Layout{}, false
	}
	l.needsFaststart = l.moovStart > mdatStart
	l.moovIsLast = l.moovEnd == size
	return l, true
}

type boxRef struct {
	typ        string
	start, end int
}

// parseTopLevelBoxes parses top-level boxes in order, requiring them to exactly and seamlessly cover all of data; otherwise it is treated as invalid and left alone.
func parseTopLevelBoxes(data []byte) ([]boxRef, bool) {
	var boxes []boxRef
	p := 0
	for p+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[p : p+4]))
		typ := string(data[p+4 : p+8])
		switch {
		case size == 1:
			if p+16 > len(data) {
				return nil, false
			}
			size64 := binary.BigEndian.Uint64(data[p+8 : p+16])
			size = int(size64)
		case size == 0:
			size = len(data) - p
		}
		if size < 8 || p+size > len(data) {
			return nil, false
		}
		boxes = append(boxes, boxRef{typ: typ, start: p, end: p + size})
		p += size
	}
	if p != len(data) {
		return nil, false
	}
	return boxes, true
}

// patchChunkOffsets recurses into moov's container boxes and adds delta to every stco/co64 offset.
func patchChunkOffsets(box []byte, delta int64) bool {
	if len(box) < 8 {
		return false
	}
	p := 8 // skip its own box header (moov uses a 32-bit size, rarely 64-bit; a 64-bit size would make the walk below starting at 8 wrong -> guaranteed by the caller's moov header being conserved)
	for p+8 <= len(box) {
		size := int(binary.BigEndian.Uint32(box[p : p+4]))
		typ := string(box[p+4 : p+8])
		hdr := 8
		switch {
		case size == 1:
			if p+16 > len(box) {
				return false
			}
			size = int(binary.BigEndian.Uint64(box[p+8 : p+16]))
			hdr = 16
		case size == 0:
			size = len(box) - p
		}
		if size < hdr || p+size > len(box) {
			return false
		}
		child := box[p : p+size]
		switch typ {
		case "stco":
			if !patchStco(child, delta) {
				return false
			}
		case "co64":
			if !patchCo64(child, delta) {
				return false
			}
		case "trak", "mdia", "minf", "stbl", "edts":
			if !patchChunkOffsets(child, delta) {
				return false
			}
		}
		p += size
	}
	return true
}

// patchStco: stco = [size(4)][type(4)][version+flags(4)][entry_count(4)][offsets 4*count].
func patchStco(box []byte, delta int64) bool {
	if len(box) < 16 {
		return false
	}
	count := binary.BigEndian.Uint32(box[12:16])
	off := 16
	if int64(off)+int64(count)*4 > int64(len(box)) {
		return false
	}
	for i := uint32(0); i < count; i++ {
		v := binary.BigEndian.Uint32(box[off : off+4])
		binary.BigEndian.PutUint32(box[off:off+4], uint32(int64(v)+delta))
		off += 4
	}
	return true
}

// patchCo64: co64 entries are 8 bytes.
func patchCo64(box []byte, delta int64) bool {
	if len(box) < 16 {
		return false
	}
	count := binary.BigEndian.Uint32(box[12:16])
	off := 16
	if int64(off)+int64(count)*8 > int64(len(box)) {
		return false
	}
	for i := uint32(0); i < count; i++ {
		v := binary.BigEndian.Uint64(box[off : off+8])
		binary.BigEndian.PutUint64(box[off:off+8], v+uint64(delta))
		off += 8
	}
	return true
}

// mp4TrackKinds reads the mdia/hdlr handler_type of each trak inside a moov box (including the box
// header) and reports whether a video track (vide) and audio track (soun) exist. ok=false means the
// structure could not be fully parsed and the caller should keep the classification from the client.
func mp4TrackKinds(moov []byte) (hasVideo, hasAudio, ok bool) {
	boxes, ok := parseTopLevelBoxes(moov)
	if !ok || len(boxes) != 1 || boxes[0].typ != "moov" {
		return false, false, false
	}
	children, ok := childBoxes(moov, boxes[0])
	if !ok {
		return false, false, false
	}
	for _, trak := range children {
		if trak.typ != "trak" {
			continue
		}
		handler, ok := trakHandler(moov, trak)
		if !ok {
			return false, false, false
		}
		switch handler {
		case "vide":
			hasVideo = true
		case "soun":
			hasAudio = true
		}
	}
	return hasVideo, hasAudio, true
}

// trakHandler returns the handler_type of trak/mdia/hdlr.
func trakHandler(data []byte, trak boxRef) (string, bool) {
	children, ok := childBoxes(data, trak)
	if !ok {
		return "", false
	}
	for _, mdia := range children {
		if mdia.typ != "mdia" {
			continue
		}
		parts, ok := childBoxes(data, mdia)
		if !ok {
			return "", false
		}
		for _, hdlr := range parts {
			if hdlr.typ != "hdlr" {
				continue
			}
			// hdlr payload: version/flags(4) + pre_defined(4) + handler_type(4) + ...
			payload := data[hdlr.start+boxHeaderSize(data, hdlr) : hdlr.end]
			if len(payload) < 12 {
				return "", false
			}
			return string(payload[8:12]), true
		}
	}
	return "", false
}

// childBoxes parses the child boxes of a container box, with offsets relative to data.
func childBoxes(data []byte, parent boxRef) ([]boxRef, bool) {
	start := parent.start + boxHeaderSize(data, parent)
	if start > parent.end {
		return nil, false
	}
	children, ok := parseTopLevelBoxes(data[start:parent.end])
	if !ok {
		return nil, false
	}
	for i := range children {
		children[i].start += start
		children[i].end += start
	}
	return children, true
}

// boxHeaderSize is the box header length: 16 bytes for largesize (size==1), otherwise 8 bytes.
func boxHeaderSize(data []byte, b boxRef) int {
	if binary.BigEndian.Uint32(data[b.start:b.start+4]) == 1 {
		return 16
	}
	return 8
}
