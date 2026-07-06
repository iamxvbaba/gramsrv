package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"

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
	if req.Offset < 0 || req.Limit <= 0 || req.Limit > maxUploadGetFileChunkLimit {
		return nil, limitInvalidErr()
	}
	// RTMP 直播拉流：inputGroupCallStream 不落 file_blobs、不依赖 Files 服务，
	// 直连 livestream 媒体面（须先于 Files nil 检查）。
	if loc, ok := req.Location.(*tg.InputGroupCallStream); ok {
		return r.onUploadGetGroupCallStream(ctx, loc, req.Offset, req.Limit)
	}
	if r.deps.Files == nil {
		return nil, notImplementedErr()
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

// onUploadGetGroupCallStream 处理 RTMP 直播观众拉流：按 time_ms/scale 取一段打包好的
// tgcalls broadcast part，再按 offset/limit 切片返回。错误语义对齐 TDesktop 消费点
// （calls_group_call.cpp broadcastPartStart）：
//   - 段未就绪（时间轴还没走到）→ TIME_TOO_BIG（客户端 100ms 后原样重试）
//   - 段已过期/无流/未加入 → GROUPCALL_JOIN_MISSING（触发客户端 rejoin 重新对时）
func (r *Router) onUploadGetGroupCallStream(ctx context.Context, loc *tg.InputGroupCallStream, offset int64, limit int) (tg.UploadFileClass, error) {
	if r.deps.LiveStreams == nil {
		return nil, notImplementedErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, loc.Call)
	if err != nil {
		return nil, err
	}
	if !scope.call.RtmpStream || scope.channel.ID == 0 {
		return nil, groupCallInvalidErr()
	}
	// RTMP 观众在 stream 模式不发 checkGroupCall 心跳、也无 SFU 媒体面活性，
	// 拉流请求（~1/s/观众）就是它的保活信号——不刷会被 sweeper 置 left，
	// 客户端每 ~50s 报 "Rejoin after got 'left' with my ssrc" 循环重进。
	if _, _, err := r.deps.GroupCalls.Touch(ctx, scope.call.ID, scope.userID, int(r.clock.Now().Unix())); err != nil {
		r.log.Debug("live stream viewer touch", zap.Int64("call_id", scope.call.ID), zap.Error(err))
	}
	part, err := r.deps.LiveStreams.StreamPart(scope.channel.ID, loc.TimeMs, loc.Scale)
	switch {
	case errors.Is(err, domain.ErrLiveStreamPartNotReady):
		// 时间轴尚未走到该段：客户端 100ms 后原样重试（Status::NotReady）。
		return nil, tgerr400("TIME_TOO_BIG")
	case errors.Is(err, domain.ErrLiveStreamPartExpired), errors.Is(err, domain.ErrLiveStreamNoStream):
		// 段已淘汰/无流：客户端重新对时后 resync（Status::ResyncNeeded）。
		return nil, tgerr400("STREAM_TIMESTAMP_EXPIRED")
	case err != nil:
		return nil, internalErr()
	}
	// offset 越界返回空 bytes（客户端已读完该段即停止续读，limit=128KiB 单次到底）。
	if offset >= int64(len(part)) {
		return &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: []byte{}}, nil
	}
	end := offset + int64(limit)
	if end > int64(len(part)) {
		end = int64(len(part))
	}
	return &tg.UploadFile{
		Type:  &tg.StorageFileUnknown{},
		Mtime: 0,
		Bytes: part[offset:end],
	}, nil
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
		if loc.ThumbSize == "" {
			return fmt.Sprintf("doc:%d", loc.ID), true
		}
		return fmt.Sprintf("doc:%d:%s", loc.ID, loc.ThumbSize), true
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
	case *tg.InputEncryptedFileLocation:
		// 密聊文件（P2）：盲 blob，location_key "enc:<id>"。access_hash 不强校验
		// （沿用现有媒体 dev 姿态，依赖不可枚举 id）。
		if loc.ID == 0 {
			return "", false
		}
		return fmt.Sprintf("enc:%d", loc.ID), true
	default:
		// InputStickerSetThumb / secure / takeout 等本阶段不生成对应资源。
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
