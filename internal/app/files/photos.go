package files

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/jpeg" // 注册 jpeg DecodeConfig，用于读取上传头像/图片尺寸
	"image/png"
	"io"
	"math"
	"strings"
	"time"

	"telesrv/internal/domain"

	"go.uber.org/zap"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // 注册 webp Decode，用于 custom emoji / sticker 静态缩略图合成
)

// 头像与图片消息共用的尺寸 type：'a' 小图（≤160），'c' 大图，'x' 通用下载尺寸。
// 同一份上传字节在多个 location_key 下建 blob（不做实际缩放，dev 主路径足够）。

// UploadProfilePhoto 把已上传文件组装成头像 Photo，落 blob/photos/profile_photos，并设为当前头像。
func (s *Service) UploadProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID int64, file domain.UploadedFileRef, date int) (domain.Photo, error) {
	return s.UploadProfilePhotoKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile, file, date)
}

// UploadProfilePhotoKind stores a profile or fallback photo and makes it current for that kind.
func (s *Service) UploadProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, file domain.UploadedFileRef, date int) (domain.Photo, error) {
	if date == 0 {
		date = int(time.Now().Unix())
	}
	photo, err := s.CreateAvatarFromUpload(ctx, file)
	if err != nil {
		return domain.Photo{}, err
	}
	if err := s.media.AddProfilePhotoKind(ctx, ownerType, ownerID, kind, photo.ID, date); err != nil {
		return domain.Photo{}, err
	}
	return photo, nil
}

// CreatePhotoFromUpload 把已上传文件组装成 Photo（不绑定 profile_photos），用于频道头像 / 图片消息。
func (s *Service) CreatePhotoFromUpload(ctx context.Context, file domain.UploadedFileRef) (domain.Photo, error) {
	intentHash, err := uploadedMediaIntentHash(domain.UploadedMediaPhoto, file, nil)
	if err != nil {
		return domain.Photo{}, err
	}
	if photo, found, err := s.replayUploadedPhoto(ctx, file, intentHash); err != nil || found {
		return photo, err
	}
	data, err := s.readUploadBytes(ctx, file.OwnerUserID, file.FileID, file.Parts)
	if err != nil {
		return domain.Photo{}, err
	}
	if len(data) == 0 {
		return domain.Photo{}, domain.ErrPhotoInvalid
	}
	photo, err := s.createPhoto(ctx, data, photoSizeSpecsForMessage(data))
	if err != nil {
		return domain.Photo{}, err
	}
	receipt, err := s.commitUploadedMediaReceipt(ctx, file, domain.UploadedMediaPhoto, intentHash, photo.ID)
	if err != nil {
		return domain.Photo{}, err
	}
	if receipt.MediaID != photo.ID {
		winner, found, err := s.media.GetPhoto(ctx, receipt.MediaID)
		if err != nil {
			return domain.Photo{}, err
		}
		if !found {
			return domain.Photo{}, fmt.Errorf("concurrent upload receipt references missing photo %d", receipt.MediaID)
		}
		photo = winner
	}
	s.cleanupMaterializedUpload(ctx, file, "photo materialized")
	return photo, nil
}

// CreatePhotoFromBytes stores already-fetched image bytes as a message Photo.
func (s *Service) CreatePhotoFromBytes(ctx context.Context, data []byte) (domain.Photo, error) {
	if len(data) == 0 {
		return domain.Photo{}, domain.ErrPhotoInvalid
	}
	return s.createPhoto(ctx, data, photoSizeSpecsForMessage(data))
}

// GetPhoto 按 id 返回已存储照片。
func (s *Service) GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error) {
	return s.media.GetPhoto(ctx, id)
}

// GetDocument 按 id 返回已存储文档（贴纸 / 文件）。
func (s *Service) GetDocument(ctx context.Context, id int64) (domain.Document, bool, error) {
	return s.media.GetDocument(ctx, id)
}

// CreateAvatarFromUpload 把已上传文件组装成头像 Photo（'a'/'c' 尺寸，匹配 InputPeerPhotoFileLocation
// big/small 与 channelFull 合成尺寸的下载路径），不绑定 profile_photos。用于频道 editPhoto。
func (s *Service) CreateAvatarFromUpload(ctx context.Context, file domain.UploadedFileRef) (domain.Photo, error) {
	data, err := s.assembleUpload(ctx, file.OwnerUserID, file.FileID, file.Parts)
	if err != nil {
		return domain.Photo{}, err
	}
	if len(data) == 0 {
		return domain.Photo{}, domain.ErrPhotoInvalid
	}
	return s.createPhoto(ctx, data, photoSizeSpecsForAvatar(data))
}

// CreateAvatarVideoFromUpload stores an animated profile video as photo.video_sizes.
func (s *Service) CreateAvatarVideoFromUpload(ctx context.Context, file domain.UploadedFileRef, videoStartTs float64) (domain.Photo, error) {
	return s.createAvatarVideoFromUpload(ctx, file, videoStartTs, nil)
}

// CreateAvatarVideoMarkupFromUpload stores Android-style generated avatar video plus its emoji/sticker markup.
func (s *Service) CreateAvatarVideoMarkupFromUpload(ctx context.Context, file domain.UploadedFileRef, videoStartTs float64, markup domain.PhotoSize) (domain.Photo, error) {
	if err := validateAvatarMarkupSize(markup); err != nil {
		return domain.Photo{}, err
	}
	return s.createAvatarVideoFromUpload(ctx, file, videoStartTs, []domain.PhotoSize{markup})
}

func (s *Service) createAvatarVideoFromUpload(ctx context.Context, file domain.UploadedFileRef, videoStartTs float64, extraSizes []domain.PhotoSize) (domain.Photo, error) {
	body, err := s.assembleUploadBlob(ctx, file.OwnerUserID, file.FileID, file.Parts)
	if err != nil {
		return domain.Photo{}, err
	}
	if body.Size == 0 {
		return domain.Photo{}, domain.ErrPhotoInvalid
	}
	photoID := randomID()
	blob := domain.FileBlob{
		LocationKey: fmt.Sprintf("photo:%d:u", photoID),
		Backend:     domain.MediaBackend(s.blobs.Name()),
		ObjectKey:   body.ObjectKey,
		Size:        body.Size,
		SHA256:      body.SHA256,
		MimeType:    "video/mp4",
	}
	if err := s.media.PutFileBlob(ctx, blob); err != nil {
		return domain.Photo{}, err
	}
	s.blobCache.put(blob.LocationKey, blob)
	stillBytes := s.avatarVideoStill(ctx, body, extraSizes)
	sizes, err := s.putPhotoStaticSizes(ctx, photoID, stillBytes, photoSizeSpecsForAvatar(stillBytes))
	if err != nil {
		return domain.Photo{}, err
	}
	sizes = append(sizes, domain.PhotoSize{
		Kind:         domain.PhotoSizeKindVideo,
		Type:         "u",
		W:            640,
		H:            640,
		Size:         int(body.Size),
		VideoStartTs: videoStartTs,
	})
	sizes = append(sizes, extraSizes...)
	photo := domain.Photo{
		ID:            photoID,
		AccessHash:    randomID(),
		FileReference: randomFileReference(),
		Date:          int(time.Now().Unix()),
		DCID:          s.dc,
		Sizes:         sizes,
	}
	if err := s.media.PutPhoto(ctx, photo); err != nil {
		return domain.Photo{}, err
	}
	if err := s.cleanupUploadParts(ctx, file.OwnerUserID, file.FileID); err != nil {
		s.log.Warn("cleanup assembled avatar video upload parts failed",
			zap.Int64("owner_user_id", file.OwnerUserID),
			zap.Int64("file_id", file.FileID),
			zap.Int64("photo_id", photoID),
			zap.Error(err))
	}
	return photo, nil
}

// CreateAvatarMarkup stores an emoji/sticker animated profile markup as photo.video_sizes.
func (s *Service) CreateAvatarMarkup(ctx context.Context, size domain.PhotoSize) (domain.Photo, error) {
	if err := validateAvatarMarkupSize(size); err != nil {
		return domain.Photo{}, err
	}
	photoID := randomID()
	stillBytes := s.generatedAvatarStill(ctx, size)
	sizes, err := s.putPhotoStaticSizes(ctx, photoID, stillBytes, photoSizeSpecsForAvatar(stillBytes))
	if err != nil {
		return domain.Photo{}, err
	}
	sizes = append(sizes, size)
	photo := domain.Photo{
		ID:            photoID,
		AccessHash:    randomID(),
		FileReference: randomFileReference(),
		Date:          int(time.Now().Unix()),
		DCID:          s.dc,
		Sizes:         sizes,
	}
	if err := s.media.PutPhoto(ctx, photo); err != nil {
		return domain.Photo{}, err
	}
	return photo, nil
}

func validateAvatarMarkupSize(size domain.PhotoSize) error {
	switch size.Kind {
	case domain.PhotoSizeKindVideoEmojiMarkup:
		if size.EmojiID == 0 || len(size.BackgroundColors) == 0 {
			return domain.ErrPhotoInvalid
		}
	case domain.PhotoSizeKindVideoStickerMarkup:
		if size.StickerID == 0 || len(size.BackgroundColors) == 0 {
			return domain.ErrPhotoInvalid
		}
	default:
		return domain.ErrPhotoInvalid
	}
	return nil
}

// CreateDocumentFromUpload 把已上传文件组装成 Document（文件/视频/音频/gif/贴纸消息），落 blob + documents。
func (s *Service) CreateDocumentFromUpload(ctx context.Context, file domain.UploadedFileRef, spec domain.DocumentSpec) (domain.Document, error) {
	intentHash, err := uploadedMediaIntentHash(domain.UploadedMediaDocument, file, &spec)
	if err != nil {
		return domain.Document{}, err
	}
	if doc, found, err := s.replayUploadedDocument(ctx, file, intentHash); err != nil || found {
		return doc, err
	}
	body, err := s.assembleUploadBlob(ctx, file.OwnerUserID, file.FileID, file.Parts)
	if err != nil {
		return domain.Document{}, err
	}
	if body.Size == 0 {
		return domain.Document{}, domain.ErrDocumentInvalid
	}
	body, spec, err = s.normalizeUploadedGIF(ctx, body, spec)
	if err != nil {
		return domain.Document{}, err
	}
	// faststart：MP4 视频若 moov 在末尾，搬到文件头以支持流式播放。普通 Telegram 客户端
	// 上传前会做这步；DrKLO 发 story 视频不转码导致 moov 在末尾，TDesktop 流式播放路径
	// 无法解复用（av_read_frame Invalid data）。不转码、保留原编码（含 HEVC）。
	body = s.maybeFaststartVideoBlob(ctx, spec.MimeType, body)
	docID := randomID()
	if err := s.media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: fmt.Sprintf("doc:%d", docID),
		Backend:     domain.MediaBackend(s.blobs.Name()),
		ObjectKey:   body.ObjectKey,
		Size:        body.Size,
		SHA256:      body.SHA256,
		MimeType:    spec.MimeType,
	}); err != nil {
		return domain.Document{}, err
	}
	doc := domain.Document{
		ID:            docID,
		AccessHash:    randomID(),
		FileReference: randomFileReference(),
		Date:          int(time.Now().Unix()),
		MimeType:      spec.MimeType,
		Size:          body.Size,
		DCID:          s.dc,
		Attributes:    spec.Attributes,
	}
	thumbMaterialized := false
	if spec.Thumb != nil {
		thumbData, err := s.readUploadBytes(ctx, spec.Thumb.OwnerUserID, spec.Thumb.FileID, spec.Thumb.Parts)
		if err == nil && len(thumbData) > 0 {
			if thumb, err := s.putDocumentThumb(ctx, docID, thumbData); err == nil {
				doc.Thumbs = []domain.PhotoSize{thumb}
				thumbMaterialized = true
			}
		}
	}
	if len(doc.Thumbs) == 0 {
		if thumb, ok := s.generateVideoThumbFallbackFromBlob(ctx, docID, body.ObjectKey, body.Size, spec); ok {
			doc.Thumbs = []domain.PhotoSize{thumb}
		}
	}
	if err := s.media.PutDocument(ctx, doc); err != nil {
		return domain.Document{}, err
	}
	receipt, err := s.commitUploadedMediaReceipt(ctx, file, domain.UploadedMediaDocument, intentHash, doc.ID)
	if err != nil {
		return domain.Document{}, err
	}
	if receipt.MediaID != doc.ID {
		winner, found, err := s.media.GetDocument(ctx, receipt.MediaID)
		if err != nil {
			return domain.Document{}, err
		}
		if !found {
			return domain.Document{}, fmt.Errorf("concurrent upload receipt references missing document %d", receipt.MediaID)
		}
		doc = winner
	}
	s.cleanupMaterializedUpload(ctx, file, "document materialized")
	if spec.Thumb != nil && thumbMaterialized {
		s.cleanupMaterializedUpload(ctx, *spec.Thumb, "document thumbnail materialized")
	}
	return doc, nil
}

func (s *Service) normalizeUploadedGIF(ctx context.Context, body assembledUploadBlob, spec domain.DocumentSpec) (assembledUploadBlob, domain.DocumentSpec, error) {
	if !strings.EqualFold(strings.TrimSpace(spec.MimeType), "image/gif") || spec.ForceFile {
		return body, spec, nil
	}
	if s.gifs == nil || body.Size <= 0 || body.Size > gifTranscodeMaxInputBytes {
		return assembledUploadBlob{}, domain.DocumentSpec{}, domain.ErrDocumentInvalid
	}
	data, total, err := s.blobs.GetRange(ctx, body.ObjectKey, 0, gifTranscodeMaxInputBytes+1)
	if err != nil || total != body.Size || int64(len(data)) != body.Size {
		return assembledUploadBlob{}, domain.DocumentSpec{}, domain.ErrDocumentInvalid
	}
	started := time.Now()
	converted, err := s.gifs.Transcode(ctx, data)
	if err != nil || len(converted.Data) == 0 || converted.Width <= 0 || converted.Height <= 0 || converted.Duration <= 0 {
		s.log.Warn("GIF to MP4 conversion failed", zap.Int64("input_bytes", body.Size), zap.Error(err))
		return assembledUploadBlob{}, domain.DocumentSpec{}, domain.ErrDocumentInvalid
	}
	objectKey, err := s.blobs.Put(ctx, converted.Data)
	if err != nil {
		return assembledUploadBlob{}, domain.DocumentSpec{}, err
	}
	sum := sha256.Sum256(converted.Data)
	body = assembledUploadBlob{ObjectKey: objectKey, Size: int64(len(converted.Data)), SHA256: append([]byte(nil), sum[:]...)}
	spec.MimeType = "video/mp4"
	spec.Attributes = canonicalGIFVideoAttributes(spec.Attributes, converted, spec.NosoundVideo)
	s.log.Info("GIF normalized to MP4",
		zap.Int64("input_bytes", total), zap.Int("output_bytes", len(converted.Data)),
		zap.Int("width", converted.Width), zap.Int("height", converted.Height),
		zap.Float64("duration", converted.Duration), zap.Duration("dur", time.Since(started)))
	return body, spec, nil
}

func canonicalGIFVideoAttributes(attrs []domain.DocumentAttribute, video GIFVideo, nosoundVideo bool) []domain.DocumentAttribute {
	out := make([]domain.DocumentAttribute, 0, 3)
	for _, attr := range attrs {
		if attr.Kind == domain.DocAttrFilename {
			out = append(out, attr)
		}
	}
	if !nosoundVideo {
		out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrAnimated})
	}
	out = append(out, domain.DocumentAttribute{
		Kind: domain.DocAttrVideo, W: video.Width, H: video.Height,
		Duration: video.Duration, SupportsStreaming: true, NoSound: true, VideoCodec: "h264",
	})
	return out
}

// maxFaststartBytes 限制 faststart 一次性载入内存的视频大小；超过则跳过（流式 faststart
// 复杂度高，超大视频是边角）。与缩略图回退路径同样全量读 blob，内存模式无新增。
const maxFaststartBytes = 200 << 20

// faststartVideoMimes 是会用 moov/mdat 结构、值得尝试 faststart 的容器 mime。
// 其它 video/*（webm 等）结构不同，faststartMP4 也会自检后 no-op，故不在此列以免白读 blob。
var faststartVideoMimes = map[string]bool{
	"video/mp4":       true,
	"video/quicktime": true,
	"video/x-m4v":     true,
}

// maybeFaststartVideoBlob 对 MP4/MOV 视频上传尝试 faststart。性能考量：
//  1. 先只读顶层 box 头（廉价探测）判断是否需要——绝大多数客户端上传的视频本就已 faststart，
//     此时只发生几次 16 字节读，不读整段媒体。
//  2. 仅 moov 在末尾时才重写；且优先走流式（仅 ftyp+moov 进内存，mdat 大块分块流式拼接），
//     不把整段视频 2× 驻留内存。moov 非末尾的罕见排布回退到全量重排。
//
// 任何不适用/失败都返回原 body，绝不让上传失败或损坏数据。
func (s *Service) maybeFaststartVideoBlob(ctx context.Context, mimeType string, body assembledUploadBlob) assembledUploadBlob {
	if !faststartVideoMimes[strings.ToLower(strings.TrimSpace(mimeType))] {
		return body
	}
	if body.Size <= 0 || body.Size > maxFaststartBytes {
		return body
	}
	readAt := func(off, n int64) ([]byte, error) {
		data, _, err := s.blobs.GetRange(ctx, body.ObjectKey, off, n)
		return data, err
	}
	layout, ok := inspectMP4Layout(body.Size, readAt)
	if !ok || !layout.needsFaststart {
		return body // 非 MP4 / 已 faststart —— 未读整段媒体
	}

	var reader io.Reader
	if layout.moovIsLast {
		// 流式重写：只把 ftyp + moov 读进内存并 patch 偏移，mdat 区段分块流式。
		ftyp, e1 := readAt(layout.ftypStart, layout.ftypEnd-layout.ftypStart)
		moov, e2 := readAt(layout.moovStart, layout.moovEnd-layout.moovStart)
		moovSize := layout.moovEnd - layout.moovStart
		if e1 != nil || e2 != nil ||
			int64(len(ftyp)) != layout.ftypEnd-layout.ftypStart ||
			int64(len(moov)) != moovSize ||
			!patchChunkOffsets(moov, moovSize) {
			return body
		}
		mid := &blobRangeReader{ctx: ctx, blobs: s.blobs, key: body.ObjectKey, pos: layout.ftypEnd, end: layout.moovStart}
		reader = io.MultiReader(bytes.NewReader(ftyp), bytes.NewReader(moov), mid)
	} else {
		// 罕见：moov 非末尾。回退到全量读 + 重排（已测函数）。
		data, total, err := s.blobs.GetRange(ctx, body.ObjectKey, 0, body.Size)
		if err != nil || total != body.Size || int64(len(data)) != body.Size {
			return body
		}
		out, changed := faststartMP4(data)
		if !changed {
			return body
		}
		reader = bytes.NewReader(out)
	}

	key, size, sum, err := s.blobs.PutReader(ctx, reader)
	if err != nil {
		s.log.Warn("faststart re-store failed; keeping original blob",
			zap.String("mime", mimeType), zap.Int64("size", body.Size), zap.Error(err))
		return body
	}
	if size != body.Size {
		// faststart 守恒大小；不等说明流式拼接出错，丢弃新 blob 用原 blob 兜底。
		s.log.Warn("faststart size mismatch; keeping original blob",
			zap.Int64("orig", body.Size), zap.Int64("got", size))
		return body
	}
	s.log.Info("faststart applied to uploaded video",
		zap.String("mime", mimeType), zap.Int64("size", size))
	return assembledUploadBlob{ObjectKey: key, Size: size, SHA256: sum}
}

// blobRangeReader 把 blob 的 [pos, end) 区段按 io.Reader 调用方给的缓冲大小分块流式读出，
// 用于 faststart 流式拼接 mdat，避免整段媒体驻留内存。
type blobRangeReader struct {
	ctx   context.Context
	blobs BlobBackend
	key   string
	pos   int64
	end   int64
}

func (r *blobRangeReader) Read(p []byte) (int, error) {
	if r.pos >= r.end {
		return 0, io.EOF
	}
	want := r.end - r.pos
	if want > int64(len(p)) {
		want = int64(len(p))
	}
	data, _, err := r.blobs.GetRange(r.ctx, r.key, r.pos, want)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, io.ErrUnexpectedEOF // 区段内不应读到空，避免静默截断
	}
	n := copy(p, data)
	r.pos += int64(n)
	return n, nil
}

// CreateDocumentFromBytes stores already-fetched bytes as a message Document.
func (s *Service) CreateDocumentFromBytes(ctx context.Context, data []byte, spec domain.DocumentSpec) (domain.Document, error) {
	if len(data) == 0 {
		return domain.Document{}, domain.ErrDocumentInvalid
	}
	if strings.TrimSpace(spec.MimeType) == "" {
		spec.MimeType = "application/octet-stream"
	}
	if strings.EqualFold(strings.TrimSpace(spec.MimeType), "image/gif") && !spec.ForceFile {
		if s.gifs == nil || len(data) > gifTranscodeMaxInputBytes {
			return domain.Document{}, domain.ErrDocumentInvalid
		}
		converted, err := s.gifs.Transcode(ctx, data)
		if err != nil || len(converted.Data) == 0 || converted.Width <= 0 || converted.Height <= 0 || converted.Duration <= 0 {
			return domain.Document{}, domain.ErrDocumentInvalid
		}
		data = converted.Data
		spec.MimeType = "video/mp4"
		spec.Attributes = canonicalGIFVideoAttributes(spec.Attributes, converted, spec.NosoundVideo)
	}
	objectKey, size, sum, err := s.blobs.PutReader(ctx, bytes.NewReader(data))
	if err != nil {
		return domain.Document{}, err
	}
	if size == 0 {
		return domain.Document{}, domain.ErrDocumentInvalid
	}
	docID := randomID()
	if err := s.media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: fmt.Sprintf("doc:%d", docID),
		Backend:     domain.MediaBackend(s.blobs.Name()),
		ObjectKey:   objectKey,
		Size:        size,
		SHA256:      sum,
		MimeType:    spec.MimeType,
	}); err != nil {
		return domain.Document{}, err
	}
	doc := domain.Document{
		ID:            docID,
		AccessHash:    randomID(),
		FileReference: randomFileReference(),
		Date:          int(time.Now().Unix()),
		MimeType:      spec.MimeType,
		Size:          size,
		DCID:          s.dc,
		Attributes:    spec.Attributes,
	}
	if thumb, ok := s.generateVideoThumbFallbackFromBlob(ctx, docID, objectKey, size, spec); ok {
		doc.Thumbs = []domain.PhotoSize{thumb}
	}
	if err := s.media.PutDocument(ctx, doc); err != nil {
		return domain.Document{}, err
	}
	return doc, nil
}

// SetCurrentProfilePhoto 把已存在的 photo 设为当前头像（updateProfilePhoto 选历史头像）。
func (s *Service) SetCurrentProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID, photoID int64, date int) (domain.Photo, bool, error) {
	return s.SetCurrentProfilePhotoKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile, photoID, date)
}

// SetCurrentProfilePhotoKind sets an existing photo as current for profile or fallback history.
func (s *Service) SetCurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoID int64, date int) (domain.Photo, bool, error) {
	photo, ok, err := s.media.GetPhoto(ctx, photoID)
	if err != nil || !ok {
		return domain.Photo{}, ok, err
	}
	if date == 0 {
		date = int(time.Now().Unix())
	}
	if err := s.media.AddProfilePhotoKind(ctx, ownerType, ownerID, kind, photoID, date); err != nil {
		return domain.Photo{}, false, err
	}
	return photo, true, nil
}

// CurrentProfilePhoto 返回某 owner 的当前头像 Photo。
func (s *Service) CurrentProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID int64) (domain.Photo, bool, error) {
	return s.CurrentProfilePhotoKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile)
}

// CurrentProfilePhotoKind returns the current profile/fallback photo.
func (s *Service) CurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (domain.Photo, bool, error) {
	id, ok, err := s.media.CurrentProfilePhotoKind(ctx, ownerType, ownerID, kind)
	if err != nil || !ok {
		return domain.Photo{}, ok, err
	}
	return s.media.GetPhoto(ctx, id)
}

// GetProfilePhotos 返回 owner 的头像历史（最新在前）。
func (s *Service) GetProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, offset, limit int, maxID int64) ([]domain.Photo, int, error) {
	return s.GetProfilePhotosKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile, offset, limit, maxID)
}

// GetProfilePhotosKind returns profile/fallback photo history.
func (s *Service) GetProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) ([]domain.Photo, int, error) {
	return s.media.ListProfilePhotoDetailsKind(ctx, ownerType, ownerID, kind, offset, limit, maxID)
}

// DeleteProfilePhotos 停用指定头像，返回成功停用数量。
func (s *Service) DeleteProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, photoIDs []int64) (int, error) {
	return s.DeleteProfilePhotosKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile, photoIDs)
}

// DeleteProfilePhotosKind disables profile/fallback photos of the selected kind.
func (s *Service) DeleteProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoIDs []int64) (int, error) {
	deleted, err := s.media.DeleteProfilePhotosKind(ctx, ownerType, ownerID, kind, photoIDs)
	if err != nil {
		return 0, err
	}
	return len(deleted), nil
}

// createPhoto 把字节落 blob（每个尺寸一个 location_key，指向同一内容）并写 photos 表。
func (s *Service) createPhoto(ctx context.Context, data []byte, specs []photoSizeSpec) (domain.Photo, error) {
	photoID := randomID()
	sizes, err := s.putPhotoStaticSizes(ctx, photoID, data, specs)
	if err != nil {
		return domain.Photo{}, err
	}
	photo := domain.Photo{
		ID:            photoID,
		AccessHash:    randomID(),
		FileReference: randomFileReference(),
		Date:          int(time.Now().Unix()),
		DCID:          s.dc,
		Sizes:         sizes,
	}
	if err := s.media.PutPhoto(ctx, photo); err != nil {
		return domain.Photo{}, err
	}
	return photo, nil
}

func (s *Service) putPhotoStaticSizes(ctx context.Context, photoID int64, data []byte, specs []photoSizeSpec) ([]domain.PhotoSize, error) {
	objectKey, err := s.blobs.Put(ctx, data)
	if err != nil {
		return nil, err
	}
	mimeType := imageMimeType(data)
	sizes := make([]domain.PhotoSize, 0, len(specs))
	for _, spec := range specs {
		blob := domain.FileBlob{
			LocationKey: fmt.Sprintf("photo:%d:%s", photoID, spec.Type),
			Backend:     domain.MediaBackend(s.blobs.Name()),
			ObjectKey:   objectKey,
			Size:        int64(len(data)),
			MimeType:    mimeType,
		}
		if err := s.media.PutFileBlob(ctx, blob); err != nil {
			return nil, err
		}
		s.blobCache.put(blob.LocationKey, blob)
		sizes = append(sizes, domain.PhotoSize{Kind: domain.PhotoSizeKindDefault, Type: spec.Type, W: spec.W, H: spec.H, Size: len(data)})
	}
	s.prewarmSmallBlob(objectKey, data)
	return sizes, nil
}

func (s *Service) putDocumentThumb(ctx context.Context, docID int64, thumbData []byte) (domain.PhotoSize, error) {
	if len(thumbData) == 0 {
		return domain.PhotoSize{}, fmt.Errorf("empty document thumbnail")
	}
	thumbKey, err := s.blobs.Put(ctx, thumbData)
	if err != nil {
		return domain.PhotoSize{}, err
	}
	w, h := imageDimensions(thumbData, 0, 0)
	blob := domain.FileBlob{
		LocationKey: fmt.Sprintf("doc:%d:m", docID),
		Backend:     domain.MediaBackend(s.blobs.Name()),
		ObjectKey:   thumbKey,
		Size:        int64(len(thumbData)),
		MimeType:    "image/jpeg",
	}
	if err := s.media.PutFileBlob(ctx, blob); err != nil {
		return domain.PhotoSize{}, err
	}
	s.blobCache.put(blob.LocationKey, blob)
	s.prewarmSmallBlob(thumbKey, thumbData)
	return domain.PhotoSize{Kind: domain.PhotoSizeKindDefault, Type: "m", W: w, H: h, Size: len(thumbData)}, nil
}

func (s *Service) generateVideoThumbFallback(ctx context.Context, docID int64, data []byte, spec domain.DocumentSpec) (domain.PhotoSize, bool) {
	if s.thumbs == nil || !documentSpecIsVideo(spec) || len(data) > videoThumbnailMaxInputBytes {
		return domain.PhotoSize{}, false
	}
	thumbData, err := s.thumbs.Extract(ctx, data, spec.MimeType)
	if err != nil {
		s.log.Warn("server-side video thumbnail fallback failed",
			zap.Int64("document_id", docID),
			zap.String("mime_type", spec.MimeType),
			zap.Int64("bytes", int64(len(data))),
			zap.Error(err))
		return domain.PhotoSize{}, false
	}
	thumb, err := s.putDocumentThumb(ctx, docID, thumbData)
	if err != nil {
		s.log.Warn("store server-side video thumbnail failed",
			zap.Int64("document_id", docID),
			zap.String("mime_type", spec.MimeType),
			zap.Int64("thumb_bytes", int64(len(thumbData))),
			zap.Error(err))
		return domain.PhotoSize{}, false
	}
	return thumb, true
}

func (s *Service) generateVideoThumbFallbackFromBlob(ctx context.Context, docID int64, objectKey string, size int64, spec domain.DocumentSpec) (domain.PhotoSize, bool) {
	if s.thumbs == nil || !documentSpecIsVideo(spec) || size > videoThumbnailMaxInputBytes {
		return domain.PhotoSize{}, false
	}
	data, total, err := s.blobs.GetRange(ctx, objectKey, 0, size)
	if err != nil {
		s.log.Warn("read video blob for thumbnail fallback failed",
			zap.Int64("document_id", docID),
			zap.String("mime_type", spec.MimeType),
			zap.Int64("bytes", size),
			zap.Error(err))
		return domain.PhotoSize{}, false
	}
	if int64(len(data)) != total || total != size {
		s.log.Warn("video blob size mismatch for thumbnail fallback",
			zap.Int64("document_id", docID),
			zap.Int64("expected_size", size),
			zap.Int64("total_size", total),
			zap.Int("read_bytes", len(data)))
		return domain.PhotoSize{}, false
	}
	return s.generateVideoThumbFallback(ctx, docID, data, spec)
}

func documentSpecIsVideo(spec domain.DocumentSpec) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(spec.MimeType)), "video/") {
		return true
	}
	for _, attr := range spec.Attributes {
		if attr.Kind == domain.DocAttrVideo {
			return true
		}
	}
	return false
}

type photoSizeSpec struct {
	Type string
	W    int
	H    int
}

func photoSizeSpecsForAvatar(data []byte) []photoSizeSpec {
	w, h := imageDimensions(data, 640, 640)
	small := 160
	if w < small {
		small = w
	}
	return []photoSizeSpec{
		{Type: "a", W: small, H: small},
		{Type: "c", W: w, H: h},
	}
}

// photoSizeSpecsForMessage 给图片消息生成下载尺寸（'m' 缩略 + 'x'/'y' 大图）。
func photoSizeSpecsForMessage(data []byte) []photoSizeSpec {
	w, h := imageDimensions(data, 1280, 1280)
	thumbW, thumbH := scaleDown(w, h, 320)
	return []photoSizeSpec{
		{Type: "m", W: thumbW, H: thumbH},
		{Type: "x", W: w, H: h},
	}
}

func imageDimensions(data []byte, defW, defH int) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return defW, defH
	}
	return cfg.Width, cfg.Height
}

func scaleDown(w, h, max int) (int, int) {
	if w <= max && h <= max {
		return w, h
	}
	if w >= h {
		return max, max * h / w
	}
	return max * w / h, max
}

const (
	avatarStillSize            = 640
	avatarMarkupScale          = 0.70
	avatarMarkupMaxSourceBytes = 2 << 20 // emoji/sticker thumb 小对象保护线。
)

// avatarVideoStill 生成动画头像的静态尺寸字节：优先抽取上传视频首帧——动画头像
// （emoji/sticker 构造器或自选视频）的首帧就是用户在客户端看到的真实画面（彩色
// emoji、圆角、布局都一致）；抽帧不可用时回退到按 markup 服务端合成。
func (s *Service) avatarVideoStill(ctx context.Context, body assembledUploadBlob, extraSizes []domain.PhotoSize) []byte {
	if s.thumbs != nil && body.Size > 0 && body.Size <= videoThumbnailMaxInputBytes {
		data, total, err := s.blobs.GetRange(ctx, body.ObjectKey, 0, body.Size)
		if err == nil && int64(len(data)) == total && total == body.Size {
			if thumb, err := s.thumbs.Extract(ctx, data, "video/mp4"); err == nil && len(thumb) > 0 {
				return thumb
			} else if err != nil {
				s.log.Debug("extract avatar video first frame failed, falling back to composed still",
					zap.String("object_key", body.ObjectKey),
					zap.Int64("bytes", body.Size),
					zap.Error(err))
			}
		} else if err != nil {
			s.log.Warn("read avatar video blob for still failed",
				zap.String("object_key", body.ObjectKey),
				zap.Int64("bytes", body.Size),
				zap.Error(err))
		}
	}
	return s.generatedAvatarStill(ctx, avatarStillMarkup(extraSizes))
}

func (s *Service) generatedAvatarStill(ctx context.Context, markup domain.PhotoSize) []byte {
	img := generatedAvatarBackground(markup.BackgroundColors)
	if overlay, tintWhite, ok := s.avatarMarkupOverlay(ctx, markup); ok {
		drawAvatarMarkup(img, overlay, tintWhite)
	}
	return encodeAvatarPNG(img)
}

func generatedAvatarBackground(colors []int) *image.RGBA {
	if len(colors) == 0 {
		colors = []int{0x5b8def, 0x53c6a4}
	}
	first := rgbColor(colors[0])
	last := first
	if len(colors) > 1 {
		last = rgbColor(colors[len(colors)-1])
	}
	img := image.NewRGBA(image.Rect(0, 0, avatarStillSize, avatarStillSize))
	for y := 0; y < avatarStillSize; y++ {
		t := float64(y) / float64(avatarStillSize-1)
		row := lerpColor(first, last, t)
		for x := 0; x < avatarStillSize; x++ {
			img.SetRGBA(x, y, row)
		}
	}
	return img
}

func encodeAvatarPNG(img image.Image) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// avatarMarkupOverlay 加载 markup 引用文档的静态缩略图作为合成贴图。第二个返回值
// 表示是否染白：仅 text_color 的 custom emoji（单色、由客户端按文字色适配渲染，
// 头像背景上的约定呈现是白色剪影）需要染白，普通彩色 emoji / sticker 保留原色。
func (s *Service) avatarMarkupOverlay(ctx context.Context, markup domain.PhotoSize) (image.Image, bool, bool) {
	if s == nil || s.media == nil {
		return nil, false, false
	}
	docID := int64(0)
	switch markup.Kind {
	case domain.PhotoSizeKindVideoEmojiMarkup:
		docID = markup.EmojiID
	case domain.PhotoSizeKindVideoStickerMarkup:
		docID = markup.StickerID
	default:
		return nil, false, false
	}
	if docID == 0 {
		return nil, false, false
	}
	doc, found, err := s.media.GetDocument(ctx, docID)
	if err != nil {
		s.log.Warn("load avatar markup document failed", zap.Int64("document_id", docID), zap.Error(err))
		return nil, false, false
	}
	if !found {
		return nil, false, false
	}
	data, ok := s.avatarMarkupBytes(ctx, doc)
	if !ok {
		return nil, false, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		s.log.Debug("decode avatar markup thumbnail failed",
			zap.Int64("document_id", doc.ID),
			zap.String("mime_type", doc.MimeType),
			zap.Error(err))
		return nil, false, false
	}
	return img, documentIsTextColorEmoji(doc), true
}

// documentIsTextColorEmoji 判断文档是否为声明 text_color 的 custom emoji。
func documentIsTextColorEmoji(doc domain.Document) bool {
	for _, attr := range doc.Attributes {
		if attr.Kind == domain.DocAttrCustomEmoji {
			return attr.TextColor
		}
	}
	return false
}

func (s *Service) avatarMarkupBytes(ctx context.Context, doc domain.Document) ([]byte, bool) {
	var best []byte
	bestScore := -1
	for _, thumb := range doc.Thumbs {
		score := avatarThumbScore(thumb)
		if score <= bestScore {
			continue
		}
		switch thumb.Kind {
		case domain.PhotoSizeKindCached:
			if len(thumb.Bytes) == 0 || len(thumb.Bytes) > avatarMarkupMaxSourceBytes {
				continue
			}
			best = append([]byte(nil), thumb.Bytes...)
			bestScore = score
		case domain.PhotoSizeKindDefault, domain.PhotoSizeKindProgressive:
			if thumb.Type == "" || thumb.Size <= 0 || thumb.Size > avatarMarkupMaxSourceBytes {
				continue
			}
			data, ok := s.readSmallBlob(ctx, fmt.Sprintf("doc:%d:%s", doc.ID, thumb.Type), int64(thumb.Size))
			if !ok {
				continue
			}
			best = data
			bestScore = score
		}
	}
	if len(best) > 0 {
		return best, true
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(doc.MimeType)), "image/") &&
		doc.Size > 0 && doc.Size <= avatarMarkupMaxSourceBytes {
		return s.readSmallBlob(ctx, fmt.Sprintf("doc:%d", doc.ID), doc.Size)
	}
	return nil, false
}

func (s *Service) readSmallBlob(ctx context.Context, locationKey string, expectedSize int64) ([]byte, bool) {
	if expectedSize <= 0 || expectedSize > avatarMarkupMaxSourceBytes {
		return nil, false
	}
	blob, found, err := s.media.GetFileBlob(ctx, locationKey)
	if err != nil {
		s.log.Warn("load avatar markup blob metadata failed",
			zap.String("location_key", locationKey),
			zap.Error(err))
		return nil, false
	}
	if !found || blob.Size <= 0 || blob.Size > avatarMarkupMaxSourceBytes {
		return nil, false
	}
	data, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, 0, blob.Size)
	if err != nil {
		s.log.Warn("read avatar markup blob failed",
			zap.String("location_key", locationKey),
			zap.String("object_key", blob.ObjectKey),
			zap.Error(err))
		return nil, false
	}
	if int64(len(data)) != total || total != blob.Size {
		return nil, false
	}
	return data, true
}

func avatarThumbScore(size domain.PhotoSize) int {
	if size.W > 0 && size.H > 0 {
		return size.W * size.H
	}
	if size.Size > 0 {
		return size.Size
	}
	return len(size.Bytes)
}

// drawAvatarMarkup 把贴图缩放后居中画到背景上。默认保留贴图原色（彩色 emoji /
// sticker 头像），仅 tintWhite 时染成白色剪影。
func drawAvatarMarkup(dst *image.RGBA, src image.Image, tintWhite bool) {
	srcBounds := src.Bounds()
	sw, sh := srcBounds.Dx(), srcBounds.Dy()
	if sw <= 0 || sh <= 0 {
		return
	}
	max := int(math.Round(float64(dst.Bounds().Dx()) * avatarMarkupScale))
	if max <= 0 {
		return
	}
	scale := math.Min(float64(max)/float64(sw), float64(max)/float64(sh))
	w := maxInt(1, int(math.Round(float64(sw)*scale)))
	h := maxInt(1, int(math.Round(float64(sh)*scale)))
	rect := image.Rect(
		(dst.Bounds().Dx()-w)/2,
		(dst.Bounds().Dy()-h)/2,
		(dst.Bounds().Dx()+w)/2,
		(dst.Bounds().Dy()+h)/2,
	)
	scaled := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), src, srcBounds, xdraw.Src, nil)
	if tintWhite {
		tintWhitePremultiplied(scaled)
	}
	stddraw.Draw(dst, rect, scaled, image.Point{}, stddraw.Over)
}

// tintWhitePremultiplied 把贴图就地染成白色剪影（保留 alpha 形状）。image.RGBA 是
// alpha 预乘存储，分量必须满足 R,G,B ≤ A：若写入 R=G=B=255 而 A<255 的非法值，
// draw.Over 合成会算术溢出回绕，凡 alpha 不恰为 255 的像素整片输出近黑色。
func tintWhitePremultiplied(img *image.RGBA) {
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			v := uint8(a >> 8)
			img.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: v})
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func rgbColor(v int) color.RGBA {
	u := uint32(v)
	return color.RGBA{R: uint8(u >> 16), G: uint8(u >> 8), B: uint8(u), A: 255}
}

func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	lerp := func(x, y uint8) uint8 {
		return uint8(float64(x)*(1-t) + float64(y)*t)
	}
	return color.RGBA{R: lerp(a.R, b.R), G: lerp(a.G, b.G), B: lerp(a.B, b.B), A: 255}
}

func imageMimeType(data []byte) string {
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	return "application/octet-stream"
}

func avatarStillMarkup(sizes []domain.PhotoSize) domain.PhotoSize {
	for _, size := range sizes {
		if size.Kind == domain.PhotoSizeKindVideoEmojiMarkup || size.Kind == domain.PhotoSizeKindVideoStickerMarkup {
			return size
		}
		if len(size.BackgroundColors) > 0 {
			return domain.PhotoSize{BackgroundColors: append([]int(nil), size.BackgroundColors...)}
		}
	}
	return domain.PhotoSize{}
}

func randomID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	v := int64(binary.BigEndian.Uint64(b[:]) >> 1)
	if v == 0 {
		v = 1
	}
	return v
}

func randomFileReference() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}
