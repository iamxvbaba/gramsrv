package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// Telegram 客户端按 chunk 调 upload.getFile；单次响应限制为 1MiB，
// 防止异常客户端用超大/空 limit 触发整 blob 读回。
const maxUploadGetFileChunkLimit = 1 << 20

// registerUpload 注册 upload.* RPC handler（分片上传 + 文件下载 + 地图 webfile）。
func (r *Router) registerUpload(d *tg.ServerDispatcher) {
	d.OnUploadSaveFilePart(r.onUploadSaveFilePart)
	d.OnUploadSaveBigFilePart(r.onUploadSaveBigFilePart)
	d.OnUploadGetFile(r.onUploadGetFile)
	d.OnUploadGetFileHashes(r.onUploadGetFileHashes)
	r.registerUploadWebFile(d)
}

func (r *Router) onUploadSaveFilePart(ctx context.Context, req *tg.UploadSaveFilePartRequest) (bool, error) {
	if r.deps.Files == nil {
		return false, notImplementedErr()
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if !ok || userID == 0 {
		return false, fileIDInvalidErr()
	}
	if req.FilePart < 0 {
		return false, filePartInvalidErr()
	}
	saved, err := r.deps.Files.SaveFilePart(ctx, userID, req.FileID, req.FilePart, req.Bytes)
	if err != nil {
		return false, fileSaveErr(err)
	}
	return saved, nil
}

func (r *Router) onUploadSaveBigFilePart(ctx context.Context, req *tg.UploadSaveBigFilePartRequest) (bool, error) {
	if r.deps.Files == nil {
		return false, notImplementedErr()
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if !ok || userID == 0 {
		return false, fileIDInvalidErr()
	}
	if req.FilePart < 0 {
		return false, filePartInvalidErr()
	}
	saved, err := r.deps.Files.SaveBigFilePart(ctx, userID, req.FileID, req.FilePart, req.FileTotalParts, req.Bytes)
	if err != nil {
		return false, fileSaveErr(err)
	}
	return saved, nil
}

func (r *Router) onUploadGetFile(ctx context.Context, req *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	if r.deps.Files == nil {
		return nil, notImplementedErr()
	}
	if req.Offset < 0 || req.Limit <= 0 || req.Limit > maxUploadGetFileChunkLimit {
		return nil, limitInvalidErr()
	}
	key, ok := fileLocationKey(req.Location)
	if !ok {
		return nil, locationInvalidErr()
	}
	chunk, found, err := r.deps.Files.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: key,
		Offset:      req.Offset,
		Limit:       req.Limit,
	})
	if err != nil {
		return nil, internalErr()
	}
	if found {
		return &tg.UploadFile{
			Type:  storageFileType(chunk.MimeType, chunk.Bytes),
			Mtime: 0,
			Bytes: chunk.Bytes,
		}, nil
	}
	return nil, locationInvalidErr()
}

// onUploadGetFileHashes 返回空 hash 列表：本阶段不做 CDN/分片完整性校验，客户端据空列表直接信任数据。
func (r *Router) onUploadGetFileHashes(ctx context.Context, req *tg.UploadGetFileHashesRequest) ([]tg.FileHash, error) {
	return []tg.FileHash{}, nil
}

// fileLocationKey 把 tg.InputFileLocation 推导为 file_blobs 的 location_key。
// 约定：
//
//	doc:<id>            文档主体
//	doc:<id>:<type>     文档缩略图
//	photo:<id>:<type>   照片某尺寸（头像 big→c / small→a）
func fileLocationKey(location tg.InputFileLocationClass) (string, bool) {
	switch loc := location.(type) {
	case *tg.InputFileLocation:
		return legacyVolumeLocationKey(loc.VolumeID, loc.LocalID)
	case *tg.InputDocumentFileLocation:
		if loc.ID == 0 {
			return "", false
		}
		id := serverDocumentIDFromClientID(loc.ID)
		if loc.ThumbSize == "" {
			return fmt.Sprintf("doc:%d", id), true
		}
		return fmt.Sprintf("doc:%d:%s", id, loc.ThumbSize), true
	case *tg.InputPhotoFileLocation:
		if loc.ID == 0 || loc.ThumbSize == "" {
			return "", false
		}
		return fmt.Sprintf("photo:%d:%s", loc.ID, loc.ThumbSize), true
	case *tg.InputPeerPhotoFileLocation:
		if loc.PhotoID == 0 {
			return "", false
		}
		size := "a"
		if loc.Big {
			size = "c"
		}
		return fmt.Sprintf("photo:%d:%s", loc.PhotoID, size), true
	case *tg.InputPhotoLegacyFileLocation:
		photoID := loc.ID
		if photoID == 0 && loc.VolumeID < 0 {
			photoID = -loc.VolumeID
		}
		if photoID == 0 {
			return "", false
		}
		size, ok := legacyPhotoSizeType(loc.LocalID)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("photo:%d:%s", photoID, size), true
	case *tg.InputPeerPhotoFileLocationLegacy:
		photoID := int64(0)
		if loc.VolumeID < 0 {
			photoID = -loc.VolumeID
		}
		if photoID == 0 {
			return "", false
		}
		size := "a"
		if loc.Big {
			size = "c"
		}
		return fmt.Sprintf("photo:%d:%s", photoID, size), true
	case *tg.InputStickerSetThumb:
		return stickerSetThumbLocationKey(loc.Stickerset)
	case *tg.InputStickerSetThumbLegacy:
		return stickerSetThumbLocationKey(loc.Stickerset)
	case *tg.InputEncryptedFileLocation:
		// 密聊文件（P2）：盲 blob，location_key "enc:<id>"。access_hash 不强校验
		// （沿用现有媒体 dev 姿态，依赖不可枚举 id）。
		if loc.ID == 0 {
			return "", false
		}
		return fmt.Sprintf("enc:%d", loc.ID), true
	default:
		// secure / takeout 等本阶段不生成对应资源。
		return "", false
	}
}

func stickerSetThumbLocationKey(set tg.InputStickerSetClass) (string, bool) {
	switch s := set.(type) {
	case *tg.InputStickerSetID:
		if s.ID == 0 {
			return "", false
		}
		return fmt.Sprintf("sticker-set-thumb:id:%d", s.ID), true
	case *tg.InputStickerSetShortName:
		name := strings.TrimSpace(s.ShortName)
		if name == "" {
			return "", false
		}
		return fmt.Sprintf("sticker-set-thumb:short:%s", name), true
	default:
		return "", false
	}
}

func legacyVolumeLocationKey(volumeID int64, localID int) (string, bool) {
	if volumeID >= 0 || localID <= 0 {
		return "", false
	}
	id := -volumeID
	if size, ok := legacyPhotoSizeType(localID); ok {
		return fmt.Sprintf("photo:%d:%s", id, size), true
	}
	if size, ok := legacyDocumentThumbSizeType(localID); ok {
		return fmt.Sprintf("doc:%d:%s", id, size), true
	}
	return "", false
}

func legacyPhotoSizeType(localID int) (string, bool) {
	if localID < 1 || localID > 127 {
		return "", false
	}
	ch := byte(localID)
	if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
		return string(ch), true
	}
	return "", false
}

func legacyDocumentThumbSizeType(localID int) (string, bool) {
	localID -= 1000
	if localID < 1 || localID > 127 {
		return "", false
	}
	ch := byte(localID)
	if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
		return string(ch), true
	}
	return "", false
}

// storageFileType 映射 storage.FileType，优先信任字节魔数以兼容历史上写错 mime 的 seed blob。
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

// sniffImageType 用魔数探测常见图片类型。
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

// fileSaveErr 把 files 服务的分片错误映射为 rpc_error。
func fileSaveErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrFilePartInvalid):
		return filePartInvalidErr()
	case errors.Is(err, domain.ErrFilePartsInvalid):
		return filePartsInvalidErr()
	case errors.Is(err, domain.ErrFilePartTooBig):
		return filePartTooBigErr()
	case errors.Is(err, domain.ErrUploadQuotaExceeded):
		return floodWaitErr(60)
	default:
		return internalErr()
	}
}
