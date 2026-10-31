package files

import "encoding/binary"

// faststartMP4 把 MP4 的 moov 原子移到 mdat 之前（faststart），并把 moov 内所有
// stco/co64 chunk 偏移整体加上 moov 的大小（因为 moov 插到 mdat 前会把媒体数据整体后移）。
// 返回 (newData, changed)。非 MP4 / 已 faststart / 任何解析异常时返回 (data, false) 原样，
// 绝不破坏数据——这是上传热路径，宁可不优化也不能把好视频改坏。
//
// 背景：TDesktop 的 story 流式播放路径无法处理 moov 在文件末尾的视频（av_read_frame
// 报 Invalid data），普通 Telegram 客户端上传前会 faststart。telesrv 在落盘前补这一步，
// 让 supports_streaming=true 的承诺对所有客户端成立。不转码、保留原编码（含 HEVC）。
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
	// 必须有 ftyp(且在最前)、moov、mdat；moov 已在 mdat 前则已 faststart。
	if ftypIdx != 0 || moovIdx < 0 || firstMdatIdx < 0 {
		return data, false
	}
	if moovIdx < firstMdatIdx {
		return data, false
	}

	// 拷出 moov（独立底层数组，后续就地改偏移不影响原 data）。
	moov := append([]byte(nil), data[boxes[moovIdx].start:boxes[moovIdx].end]...)
	moovSize := int64(len(moov))
	if !patchChunkOffsets(moov, moovSize) {
		return data, false
	}

	// 重组：ftyp + moov + 其余 box（除 ftyp/moov）按原顺序。
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
		// 长度必须守恒（只搬不改大小）；不守恒说明哪里算错，保守放弃。
		return data, false
	}
	return out, true
}

// mp4Layout 是只读 box 头得出的顶层结构信息，用于在不读取整段媒体的前提下判断是否需要
// faststart 以及如何流式重写。
type mp4Layout struct {
	needsFaststart     bool // moov 在 mdat 之后
	moovIsLast         bool // moov 是最后一个顶层 box（可走流式重写）
	ftypStart, ftypEnd int64
	moovStart, moovEnd int64
}

// inspectMP4Layout 只读顶层 box 头（每个 ≤16 字节，跳过 box 负载）来判定结构，避免为了
// 「检查是否已 faststart」而把整段视频读进内存。readAt(off, n) 读取 [off, off+n) 字节。
// 非 MP4 / 结构异常返回 (·, false)。
func inspectMP4Layout(size int64, readAt func(off, n int64) ([]byte, error)) (mp4Layout, bool) {
	l := mp4Layout{moovStart: -1, moovEnd: -1}
	mdatStart := int64(-1)
	ftypSeen := false
	p := int64(0)
	for boxes := 0; p+8 <= size; boxes++ {
		if boxes > 1024 { // 顶层 box 数量上限，防御异常文件
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
				return mp4Layout{}, false // ftyp 必须在最前
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

// parseTopLevelBoxes 顺序解析顶层 box，要求恰好无缝覆盖整个 data，否则视为非法不处理。
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

// patchChunkOffsets 递归进入 moov 的容器 box，把 stco/co64 的每个偏移 += delta。
func patchChunkOffsets(box []byte, delta int64) bool {
	if len(box) < 8 {
		return false
	}
	p := 8 // 跳过自身 box 头（moov 用 32 位 size，极少 64 位；若 64 位则下面 walk 仍从 8 起会错→由调用方 moov 头守恒保证）
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

// patchStco：stco = [size(4)][type(4)][version+flags(4)][entry_count(4)][offsets 4*count]。
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

// patchCo64：co64 entries 为 8 字节。
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

// mp4TrackKinds 读取 moov box（含 box 头）里各 trak 的 mdia/hdlr handler_type，报告是否存在
// 视频轨（vide）和音频轨（soun）。ok=false 表示结构无法完整解析，调用方应保持客户端给出的分类。
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

// trakHandler 返回 trak/mdia/hdlr 的 handler_type。
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
			// hdlr 负载：version/flags(4) + pre_defined(4) + handler_type(4) + ...
			payload := data[hdlr.start+boxHeaderSize(data, hdlr) : hdlr.end]
			if len(payload) < 12 {
				return "", false
			}
			return string(payload[8:12]), true
		}
	}
	return "", false
}

// childBoxes 解析容器 box 的子 box，偏移相对于 data。
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

// boxHeaderSize 是 box 头长度：largesize（size==1）为 16 字节，否则 8 字节。
func boxHeaderSize(data []byte, b boxRef) int {
	if binary.BigEndian.Uint32(data[b.start:b.start+4]) == 1 {
		return 16
	}
	return 8
}
