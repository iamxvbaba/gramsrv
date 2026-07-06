package livestream

import "encoding/binary"

// tgcalls broadcast part 打包（消费方 tgcalls VideoStreamingPart.cpp
// consumeVideoStreamInfo）。unified（RTMP）模式的 part 结构：
//
//	int32(LE) 签名 0xa12e810d
//	TL 风格短字符串：容器名（本实现恒 "mp4"）
//	int32 activeMask（单一 unified 轨恒 1）
//	int32 eventCount（消费方只读第一个 event，恒写 1）
//	event: int32 offset(=0，相对头部之后的数据) + 字符串 endpointId("unified")
//	       + int32 rotation(0) + int32 extra(0)
//	随后紧跟容器数据（音视频同容器，客户端分别按 Video/Audio content type 解）。
const partSignature uint32 = 0xa12e810d

// partContainer 必须落在 TDesktop 裁剪版 ffmpeg 的 demuxer 白名单内
// （Telegram/build/prepare/prepare.py --enable-demuxer=...，含 mov/mp4、无 mpegts）。
// "mp4" 由 mov demuxer 别名匹配；tgcalls AVIO 支持 seek，完整 mp4（moov 在尾）可解。
const (
	partContainer  = "mp4"
	partEndpointID = "unified"
)

// appendTLString 按 tgcalls readSerializedString 的逆操作写入字符串：
// 长度 <254 用 1 字节长度 + 数据，整体（含长度字节）补齐到 4 字节；
// 否则 0xFE + 3 字节小端长度 + 数据，数据补齐到 4 字节。
func appendTLString(dst []byte, s string) []byte {
	n := len(s)
	if n < 254 {
		dst = append(dst, byte(n))
		dst = append(dst, s...)
		for (n+1)%4 != 0 {
			dst = append(dst, 0)
			n++
		}
		return dst
	}
	dst = append(dst, 0xFE, byte(n), byte(n>>8), byte(n>>16))
	dst = append(dst, s...)
	for n%4 != 0 {
		dst = append(dst, 0)
		n++
	}
	return dst
}

func appendUint32(dst []byte, v uint32) []byte {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	return append(dst, buf[:]...)
}

func appendInt32(dst []byte, v int32) []byte {
	return appendUint32(dst, uint32(v))
}

// packUnifiedPart 把一段自包含 MPEG-TS 数据包成 tgcalls broadcast part。
func packUnifiedPart(tsData []byte) []byte {
	out := make([]byte, 0, len(tsData)+48)
	out = appendUint32(out, partSignature)
	out = appendTLString(out, partContainer)
	out = appendInt32(out, 1) // activeMask
	out = appendInt32(out, 1) // eventCount
	out = appendInt32(out, 0) // event.offset
	out = appendTLString(out, partEndpointID)
	out = appendInt32(out, 0) // event.rotation
	out = appendInt32(out, 0) // event.extra
	return append(out, tsData...)
}
