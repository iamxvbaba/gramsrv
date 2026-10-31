package rpc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
	"telesrv/internal/rpcresult"
)

// Telegram clients call upload.getFile chunk by chunk; a single response is limited to
// 1MiB, preventing an abnormal client from using an oversized/empty limit to trigger a
// full-blob read-back.
const maxUploadGetFileChunkLimit = 1 << 20

type immutableFileRangeReader interface {
	ReadImmutableFileRange(context.Context, domain.ImmutableFileRange) ([]byte, error)
}

type immutableUploadFileSource struct {
	reader immutableFileRangeReader
	range_ domain.ImmutableFileRange
}

func (s *immutableUploadFileSource) Value(ctx context.Context) (any, error) {
	if s == nil || s.reader == nil {
		return nil, fmt.Errorf("immutable upload file source is unavailable")
	}
	data, err := s.reader.ReadImmutableFileRange(ctx, s.range_)
	if err != nil {
		return nil, err
	}
	if len(data) != s.range_.Length || sha256.Sum256(data) != s.range_.RangeSHA256 {
		return nil, fmt.Errorf("immutable upload file source changed")
	}
	return &tg.UploadFile{
		Type:  storageFileType(s.range_.MimeType, data),
		Mtime: 0,
		Bytes: data,
	}, nil
}

func (s *immutableUploadFileSource) RetainedBytes() int {
	if s == nil {
		return 0
	}
	return 384 + len(s.range_.ObjectKey) + len(s.range_.MimeType)
}

// registerUpload registers the upload.* RPC handlers (upload parts + file download +
// map webfile).
func (r *Router) registerUpload(d *tlprofile.Dispatcher) {
	registerRPC[*tg.UploadSaveFilePartRequest](d, tlprofile.SemanticMethodUploadSaveFilePart, func(ctx context.Context, layerRequest *tg.UploadSaveFilePartRequest) (any, error) {
		return r.onUploadSaveFilePart(ctx, layerRequest)
	})
	registerRPC[*tg.UploadSaveBigFilePartRequest](d, tlprofile.SemanticMethodUploadSaveBigFilePart, func(ctx context.Context, layerRequest *tg.UploadSaveBigFilePartRequest) (any, error) {
		return r.onUploadSaveBigFilePart(ctx, layerRequest)
	})
	registerRPC[*tg.UploadGetFileRequest](d, tlprofile.SemanticMethodUploadGetFile, func(ctx context.Context, layerRequest *tg.UploadGetFileRequest) (any, error) {
		return r.onUploadGetFile(ctx, layerRequest)
	})
	registerRPC[*tg.UploadGetFileHashesRequest](d, tlprofile.SemanticMethodUploadGetFileHashes, func(ctx context.Context, layerRequest *tg.UploadGetFileHashesRequest) (any, error) {
		return r.onUploadGetFileHashes(ctx, layerRequest)
	})
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
	// RTMP livestream pull: inputGroupCallStream does not hit file_blobs and does not
	// depend on the Files service, connecting directly to the livestream media plane
	// (must come before the Files nil check).
	if loc, ok := req.Location.(*tg.InputGroupCallStream); ok {
		return r.onUploadGetGroupCallStream(ctx, loc, req.Offset, req.Limit)
	}
	if r.deps.Files == nil {
		return nil, notImplementedErr()
	}
	key, ok, err := r.authorizedFileLocationKey(ctx, req.Location)
	if err != nil {
		return nil, err
	}
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
		result := &tg.UploadFile{
			Type:  storageFileType(chunk.MimeType, chunk.Bytes),
			Mtime: 0,
			Bytes: chunk.Bytes,
		}
		if chunk.ImmutableRange != nil {
			if reader, ok := r.deps.Files.(immutableFileRangeReader); ok {
				rpcresult.Publish(ctx, &immutableUploadFileSource{reader: reader, range_: *chunk.ImmutableRange})
			}
		}
		return result, nil
	}
	return nil, locationInvalidErr()
}

// authorizedFileLocationKey applies the authorization/capability checks which cannot be
// expressed by a plain location-key conversion. Secret-chat blobs are addressed internally by
// id, but the wire capability is the pair (id, access_hash); accepting id alone would let any
// authenticated caller who learned or guessed an id download ciphertext which was never
// delivered to that caller.
func (r *Router) authorizedFileLocationKey(ctx context.Context, location tg.InputFileLocationClass) (string, bool, error) {
	loc, encrypted := location.(*tg.InputEncryptedFileLocation)
	if !encrypted {
		key, ok := fileLocationKey(location)
		return key, ok, nil
	}
	if loc.ID == 0 || loc.AccessHash == 0 || r.deps.SecretChats == nil {
		return "", false, nil
	}
	if _, err := r.secretChatRequireUser(ctx); err != nil {
		return "", false, err
	}
	ref, found, err := r.deps.SecretChats.GetEncryptedFile(ctx, loc.ID, loc.AccessHash)
	if err != nil {
		return "", false, internalErr()
	}
	if !found || ref.ID != loc.ID || ref.AccessHash != loc.AccessHash {
		return "", false, nil
	}
	return fmt.Sprintf("enc:%d", loc.ID), true, nil
}

// onUploadGetGroupCallStream handles RTMP livestream viewer pull: fetches a packaged
// tgcalls broadcast part by time_ms/scale, then slices it by offset/limit. The error
// semantics match TDesktop's consumption point (calls_group_call.cpp
// broadcastPartStart):
//   - part not ready (the timeline has not reached it) -> TIME_TOO_BIG (the client
//     retries as-is after 100ms)
//   - part expired / no stream / not joined -> GROUPCALL_JOIN_MISSING (triggers the
//     client to rejoin and re-sync its clock)
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
	// An RTMP viewer in stream mode does not send a checkGroupCall heartbeat and has no
	// SFU media-plane liveness, so its pull requests (~1/s/viewer) are its keepalive
	// signal - without them the sweeper sets it to left, and the client loops back in
	// every ~50s reporting "Rejoin after got 'left' with my ssrc".
	if _, _, err := r.deps.GroupCalls.Touch(ctx, scope.call.ID, scope.userID, int(r.clock.Now().Unix())); err != nil {
		r.log.Debug("live stream viewer touch", zap.Int64("call_id", scope.call.ID), zap.Error(err))
	}
	part, err := r.deps.LiveStreams.StreamPart(scope.channel.ID, loc.TimeMs, loc.Scale)
	switch {
	case errors.Is(err, domain.ErrLiveStreamPartNotReady):
		// The timeline has not reached this part yet: the client retries as-is after
		// 100ms (Status::NotReady).
		return nil, tgerr400("TIME_TOO_BIG")
	case errors.Is(err, domain.ErrLiveStreamPartExpired), errors.Is(err, domain.ErrLiveStreamNoStream):
		// Part evicted / no stream: the client resyncs after re-alignment
		// (Status::ResyncNeeded).
		return nil, tgerr400("STREAM_TIMESTAMP_EXPIRED")
	case err != nil:
		return nil, internalErr()
	}
	// An out-of-range offset returns empty bytes (the client stops reading once it has
	// exhausted the part, reading to the end in one 128KiB request).
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

// onUploadGetFileHashes returns an empty hash list: this stage does no CDN/part
// integrity verification, and the client trusts the data directly based on the empty
// list.
func (r *Router) onUploadGetFileHashes(ctx context.Context, req *tg.UploadGetFileHashesRequest) ([]tg.FileHash, error) {
	return []tg.FileHash{}, nil
}

// fileLocationKey derives a file_blobs location_key from tg.InputFileLocation.
// Convention:
//
//	doc:<id>            document body
//	doc:<id>:<type>     document thumbnail
//	photo:<id>:<type>   a photo size (avatar big->c / small->a)
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
	default:
		// InputStickerSetThumb / secure / takeout, etc. do not generate a resource at
		// this stage.
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

// fileSaveErr maps the files service's part errors to rpc_error.
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
	case errors.Is(err, domain.ErrStorageFull):
		return storageFullErr()
	default:
		return internalErr()
	}
}
