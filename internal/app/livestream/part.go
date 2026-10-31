package livestream

import "encoding/binary"

// tgcalls broadcast part packing (the consumer is tgcalls VideoStreamingPart.cpp
// consumeVideoStreamInfo). Structure of a part in unified (RTMP) mode:
//
//	int32(LE) signature 0xa12e810d
//	TL-style short string: container name (always "mp4" in this implementation)
//	int32 activeMask (always 1 for the single unified track)
//	int32 eventCount (the consumer reads only the first event, always written as 1)
//	event: int32 offset(=0, relative to the data after the header) + string endpointId("unified")
//	       + int32 rotation(0) + int32 extra(0)
//	Then the container data immediately follows (audio and video share the container;
//	the client demuxes by Video/Audio content type respectively).
const partSignature uint32 = 0xa12e810d

// partContainer must fall within the demuxer whitelist of TDesktop's trimmed ffmpeg
// (Telegram/build/prepare/prepare.py --enable-demuxer=..., which includes mov/mp4 and no
// mpegts). "mp4" matches via the mov demuxer alias; tgcalls AVIO supports seek, so a
// complete mp4 (moov at the end) can be decoded.
const (
	partContainer  = "mp4"
	partEndpointID = "unified"
)

// appendTLString writes a string as the inverse of tgcalls readSerializedString:
// for length <254, one length byte + data, the whole thing (including the length byte)
// padded to 4 bytes; otherwise 0xFE + 3-byte little-endian length + data, with the data
// padded to 4 bytes.
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

// packUnifiedPart packs a self-contained chunk of MPEG-TS data into a tgcalls broadcast part.
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
