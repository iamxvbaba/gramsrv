package rpc

import (
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// Shared chunk cap for the independently registered web-file and Bot API
// download paths. The four MTProto FileData RPCs are Edge-only and must never
// be registered in the Core business dispatcher.
const maxUploadGetFileChunkLimit = 1 << 20

// registerUpload registers only the Core-owned web-file RPC. FileData methods
// (saveFilePart, saveBigFilePart, getFile, getFileHashes) are owned exclusively
// by mtprotoedge.FileDataLayerRPC.
func (r *Router) registerUpload(d *tlprofile.Dispatcher) {
	r.registerUploadWebFile(d)
}

// storageFileType maps storage.FileType, trusting the byte magic first to accommodate
// historically mis-typed-mime seed blobs.
func storageFileType(mime string, data []byte) tg.StorageFileTypeClass {
	switch sniffImageType(data) {
	case "jpeg":
		return &tg.StorageFileJpeg{}
	case "png":
		return &tg.StorageFilePng{}
	case "gif":
		return &tg.StorageFileGif{}
	case "webp":
		return &tg.StorageFileWebp{}
	}
	switch {
	case strings.Contains(mime, "webp"):
		return &tg.StorageFileWebp{}
	case strings.Contains(mime, "jpeg"), strings.Contains(mime, "jpg"):
		return &tg.StorageFileJpeg{}
	case strings.Contains(mime, "png"):
		return &tg.StorageFilePng{}
	case strings.Contains(mime, "gif"):
		return &tg.StorageFileGif{}
	case strings.Contains(mime, "mp4"), strings.Contains(mime, "quicktime"), strings.Contains(mime, "video"):
		return &tg.StorageFileMov{}
	}
	return &tg.StorageFileUnknown{}
}

// sniffImageType detects common image types by magic bytes.
func sniffImageType(data []byte) string {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "jpeg"
	}
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "png"
	}
	if len(data) >= 6 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' {
		return "gif"
	}
	if len(data) >= 12 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
		data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "webp"
	}
	return ""
}
