package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// 上传分片上限：与 Telegram 客户端约定一致（单片 ≤512KB；分片总数有上限防止 OOM）。
const (
	MaxUploadPartBytes = 524288 // 512KB
	MaxUploadParts     = 8000   // 512KB * 8000 ≈ 4GB 理论上限，足够主路径媒体
)

const (
	DefaultUploadPartTTL          = 24 * time.Hour
	DefaultUploadPartGCInterval   = 30 * time.Minute
	DefaultUploadPartGCBatch      = 10000
	DefaultUploadInFlightMaxBytes = int64(MaxUploadPartBytes) * int64(MaxUploadParts)
	DefaultUploadInFlightMaxParts = MaxUploadParts
	DefaultUploadInFlightMaxFiles = 64
)

// blobMetaCacheCapacity 是 location_key→FileBlob 元数据 LRU 容量（每项约百字节，约 13MB）。
const blobMetaCacheCapacity = 1 << 16

// 小文件热缓存只覆盖 sticker/reaction/thumbnail 一类不可变小 blob；大媒体继续分段读。
const (
	blobBytesCacheMaxEntryBytes = 256 << 10 // 256KB
	blobBytesCacheMaxBytes      = 64 << 20  // 64MB
	// stickerSetNegativeCacheTTL 是未找到贴纸集的负缓存有效期：未 seed 的 short_name 会被客户端
	// 反复 getStickerSet，这里短时缓存 not-found 短路掉 PG。短 TTL 保证运行时新增集合最多滞后这么久。
	stickerSetNegativeCacheTTL = 30 * time.Second
)

// Service 实现 upload 分片累积、blob 落盘、getFile 下载，并把上传文件组装成 Photo / Document。
type Service struct {
	media       store.MediaStore
	blobs       BlobBackend
	uploadParts UploadPartBackend
	dc          int
	log         *zap.Logger
	thumbs      VideoThumbnailer
	thumbsSet   bool
	gifs        GIFTranscoder
	gifsSet     bool
	blobCache   *blobMetaCache
	byteCache   *blobBytesCache
	// blobMetaSF/blobBytesSF 合并对同一热 blob 的并发首次访问：否则每个并发 getFile 都各打
	// 一发 PG GetFileBlob + backend GetRange(热门贴纸/reaction/头像被大量用户同时拉时尤甚)。
	blobMetaSF         singleflight.Group
	blobBytesSF        singleflight.Group
	stickerSetCache    *stickerSetFullCache
	stickerSetNegCache *stickerSetNegativeCache
	uploadQuota        domain.UploadPartQuota
	mapTiles           *mapTileProxy
	externalMedia      *externalMediaFetcher
	webpage            *webpageFetcher
	// effects 是消息发送特效目录(messages.getAvailableEffects)。全局静态,启动 seedEffects
	// 一次写入后只读,故无锁——与各 read-model 缓存一样在服务就绪前完成填充。
	// effectsHash 在 seed 时算一次,handler 直接比对返回 NotModified,无需每次 RPC 重算。
	effects     []domain.AvailableEffect
	effectsHash int
}

// Option 配置 files 服务的可选能力。
type Option func(*Service)

// WithLogger 注入日志器。未注入时使用 no-op logger。
func WithLogger(log *zap.Logger) Option {
	return func(s *Service) {
		if log != nil {
			s.log = log
		}
	}
}

// WithVideoThumbnailer 覆盖视频缩略图生成器。传 nil 可显式关闭服务端抽帧 fallback。
func WithVideoThumbnailer(thumbnailer VideoThumbnailer) Option {
	return func(s *Service) {
		s.thumbs = thumbnailer
		s.thumbsSet = true
	}
}

// WithGIFTranscoder 覆盖真实 GIF→MP4 规范化器。传 nil 可用于测试不可用路径。
func WithGIFTranscoder(transcoder GIFTranscoder) Option {
	return func(s *Service) {
		s.gifs = transcoder
		s.gifsSet = true
	}
}

// WithUploadPartQuota 覆盖用户级 in-flight 上传分片配额；字段 <=0 表示该维度不限制。
func WithUploadPartQuota(quota domain.UploadPartQuota) Option {
	return func(s *Service) {
		s.uploadQuota = quota
	}
}

// NewService 创建 files 服务。dc 是本 server 的 DC id，写入新建 document/photo 的 dc_id。
func NewService(media store.MediaStore, blobs BlobBackend, dc int, opts ...Option) *Service {
	s := &Service{
		media:              media,
		blobs:              blobs,
		dc:                 dc,
		log:                zap.NewNop(),
		blobCache:          newBlobMetaCache(blobMetaCacheCapacity),
		byteCache:          newBlobBytesCache(blobBytesCacheMaxBytes),
		stickerSetCache:    newStickerSetFullCache(),
		stickerSetNegCache: newStickerSetNegativeCache(stickerSetNegativeCacheTTL),
		uploadQuota: domain.UploadPartQuota{
			MaxBytes: DefaultUploadInFlightMaxBytes,
			MaxParts: DefaultUploadInFlightMaxParts,
			MaxFiles: DefaultUploadInFlightMaxFiles,
		},
	}
	if partBackend, ok := blobs.(UploadPartBackend); ok {
		s.uploadParts = partBackend
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.mapTiles != nil {
		// 选项应用顺序无关：logger 在全部 Option 跑完后统一注入。
		s.mapTiles.log = s.log
	}
	if !s.thumbsSet {
		thumbnailer, err := NewFFmpegVideoThumbnailer()
		if err != nil {
			s.log.Warn("ffmpeg not found; server-side video thumbnail fallback disabled", zap.Error(err))
		} else {
			s.thumbs = thumbnailer
		}
	}
	if !s.gifsSet {
		transcoder, err := NewFFmpegGIFTranscoder()
		if err != nil {
			s.log.Warn("ffmpeg/ffprobe not found; GIF uploads will be rejected", zap.Error(err))
		} else {
			s.gifs = transcoder
		}
	}
	return s
}

// SaveFilePart 累积一个 small file 分片。
func (s *Service) SaveFilePart(ctx context.Context, ownerUserID, fileID int64, part int, bytes []byte) (bool, error) {
	if err := validatePart(part, len(bytes)); err != nil {
		return false, err
	}
	if err := s.saveFilePart(ctx, domain.UploadPart{
		OwnerUserID: ownerUserID,
		FileID:      fileID,
		Part:        part,
		Size:        int64(len(bytes)),
	}, bytes); err != nil {
		return false, err
	}
	return true, nil
}

// SaveBigFilePart 累积一个 big file 分片（带已知总分片数）。
func (s *Service) SaveBigFilePart(ctx context.Context, ownerUserID, fileID int64, part, totalParts int, bytes []byte) (bool, error) {
	if err := validatePart(part, len(bytes)); err != nil {
		return false, err
	}
	if totalParts <= 0 || totalParts > MaxUploadParts {
		return false, domain.ErrFilePartsInvalid
	}
	if err := s.saveFilePart(ctx, domain.UploadPart{
		OwnerUserID: ownerUserID,
		FileID:      fileID,
		Part:        part,
		TotalParts:  totalParts,
		Big:         true,
		Size:        int64(len(bytes)),
	}, bytes); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) saveFilePart(ctx context.Context, part domain.UploadPart, bytes []byte) error {
	if s.uploadParts == nil {
		return fmt.Errorf("upload part backend not configured")
	}
	slot, err := s.checkUploadPartQuota(ctx, part)
	if err != nil {
		return err
	}
	obj, err := s.uploadParts.PutUploadPart(ctx, part.OwnerUserID, part.FileID, part.Part, bytes)
	if err != nil {
		return err
	}
	part.Backend = obj.Backend
	part.ObjectKey = obj.ObjectKey
	part.Size = obj.Size
	part.SHA256 = obj.SHA256
	if err := s.media.SaveFilePart(ctx, part); err != nil {
		_ = s.uploadParts.DeleteUploadPart(ctx, obj.ObjectKey)
		return err
	}
	if slot.Found && slot.ObjectKey != "" && slot.ObjectKey != obj.ObjectKey {
		if err := s.uploadParts.DeleteUploadPart(ctx, slot.ObjectKey); err != nil {
			s.log.Warn("delete replaced upload part failed", zap.String("object_key", slot.ObjectKey), zap.Error(err))
		}
	}
	return nil
}

func (s *Service) checkUploadPartQuota(ctx context.Context, part domain.UploadPart) (domain.UploadPartSlot, error) {
	slot, err := s.media.UploadPartSlot(ctx, part.OwnerUserID, part.FileID, part.Part)
	if err != nil {
		return domain.UploadPartSlot{}, err
	}
	quota := s.uploadQuota
	if quota.MaxBytes <= 0 && quota.MaxParts <= 0 && quota.MaxFiles <= 0 {
		return slot, nil
	}
	usage, err := s.media.UploadPartUsage(ctx, part.OwnerUserID)
	if err != nil {
		return domain.UploadPartSlot{}, err
	}
	next := usage
	next.Bytes += part.Size - slot.ExistingBytes
	if !slot.Found {
		next.Parts++
	}
	if slot.FileParts == 0 {
		next.Files++
	}
	if quota.MaxBytes > 0 && next.Bytes > quota.MaxBytes {
		return domain.UploadPartSlot{}, domain.ErrUploadQuotaExceeded
	}
	if quota.MaxParts > 0 && next.Parts > quota.MaxParts {
		return domain.UploadPartSlot{}, domain.ErrUploadQuotaExceeded
	}
	if quota.MaxFiles > 0 && next.Files > quota.MaxFiles {
		return domain.UploadPartSlot{}, domain.ErrUploadQuotaExceeded
	}
	return slot, nil
}

// DeleteExpiredUploadParts 清理超过保留期仍未组装的 transient 上传分片。
func (s *Service) DeleteExpiredUploadParts(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	keys, err := s.media.DeleteExpiredUploadParts(ctx, before, limit)
	if err != nil {
		return 0, err
	}
	if err := s.deleteUploadPartObjects(ctx, keys); err != nil {
		return int64(len(keys)), err
	}
	var orphanDeleted int64
	if s.uploadParts != nil {
		n, err := s.uploadParts.DeleteExpiredUploadParts(ctx, before, limit)
		if err != nil {
			return int64(len(keys)), err
		}
		orphanDeleted = n
	}
	return int64(len(keys)) + orphanDeleted, nil
}

// GetFile 按 location_key 取一段 blob 内容。found=false 表示该 location 无对应 blob。
// 元数据走进程内 LRU（消除每 chunk 一次 PG 查）；小 blob 全量字节进 LRU，供 sticker /
// reaction / thumbnail 热路径直接内存切片；大 blob 仍按 offset/limit 段读。
type blobMetaResult struct {
	blob        domain.FileBlob
	found       bool
	cacheHit    bool
	cacheFilled bool
}

type blobBytesResult struct {
	data        []byte
	total       int64
	cacheable   bool
	cacheHit    bool
	cacheFilled bool
}

type getFileCacheLog struct {
	start             time.Time
	metaCacheHit      bool
	metaCacheFilled   bool
	metaSingleflight  bool
	byteCacheEligible bool
	byteCacheHit      bool
	byteCacheFilled   bool
	byteSingleflight  bool
	backendRead       bool
	source            string
}

func (s *Service) GetFile(ctx context.Context, req domain.FileDownloadRequest) (chunk domain.FileChunk, found bool, err error) {
	cacheLog := getFileCacheLog{start: time.Now(), source: "unknown"}
	var blob domain.FileBlob
	defer func() {
		s.logGetFileCache(req, blob, found, chunk, cacheLog, err)
	}()

	blob, ok := s.blobCache.get(req.LocationKey)
	if ok {
		cacheLog.metaCacheHit = true
	} else {
		// 同一 location_key 的并发首访合并成一次 PG GetFileBlob。
		v, err, shared := s.blobMetaSF.Do(req.LocationKey, func() (any, error) {
			if cached, ok := s.blobCache.get(req.LocationKey); ok {
				return blobMetaResult{blob: cached, found: true, cacheHit: true}, nil
			}
			b, found, err := s.media.GetFileBlob(ctx, req.LocationKey)
			if err != nil {
				return blobMetaResult{}, err
			}
			if found {
				s.blobCache.put(req.LocationKey, b)
			}
			return blobMetaResult{blob: b, found: found, cacheFilled: found}, nil
		})
		cacheLog.metaSingleflight = shared
		if err != nil {
			return domain.FileChunk{}, false, err
		}
		res := v.(blobMetaResult)
		cacheLog.metaCacheHit = res.cacheHit
		cacheLog.metaCacheFilled = res.cacheFilled
		if !res.found {
			cacheLog.source = "metadata_miss"
			return domain.FileChunk{}, false, nil
		}
		blob = res.blob
	}
	if blob.Size > 0 && blob.Size <= blobBytesCacheMaxEntryBytes {
		cacheLog.byteCacheEligible = true
		if data, ok := s.byteCache.get(blob.ObjectKey); ok {
			cacheLog.byteCacheHit = true
			cacheLog.source = "byte_cache"
			return domain.FileChunk{
				Bytes:    sliceBlobBytes(data, req.Offset, int64(req.Limit)),
				MimeType: blob.MimeType,
				Total:    int64(len(data)),
			}, true, nil
		}
		// 同一 object_key 的小 blob 并发首访合并成一次 backend 全量读 + 一次 byteCache 填充。
		v, err, shared := s.blobBytesSF.Do(blob.ObjectKey, func() (any, error) {
			if cached, ok := s.byteCache.get(blob.ObjectKey); ok {
				return blobBytesResult{data: cached, total: int64(len(cached)), cacheable: true, cacheHit: true}, nil
			}
			data, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, 0, blobBytesCacheMaxEntryBytes+1)
			if err != nil {
				return blobBytesResult{}, err
			}
			if total <= blobBytesCacheMaxEntryBytes && int64(len(data)) == total {
				s.byteCache.put(blob.ObjectKey, data)
				return blobBytesResult{data: data, total: total, cacheable: true, cacheFilled: true}, nil
			}
			return blobBytesResult{cacheable: false}, nil
		})
		cacheLog.byteSingleflight = shared
		if err != nil {
			return domain.FileChunk{}, false, fmt.Errorf("read blob %q: %w", blob.LocationKey, err)
		}
		// res.data 在并发 caller 间只读共享，sliceBlobBytes 各自拷贝出自己的分片，安全。
		if res := v.(blobBytesResult); res.cacheable {
			cacheLog.byteCacheHit = res.cacheHit
			cacheLog.byteCacheFilled = res.cacheFilled
			cacheLog.backendRead = res.cacheFilled
			if res.cacheHit {
				cacheLog.source = "byte_cache"
			} else {
				cacheLog.source = "backend_fill_byte_cache"
			}
			return domain.FileChunk{
				Bytes:    sliceBlobBytes(res.data, req.Offset, int64(req.Limit)),
				MimeType: blob.MimeType,
				Total:    res.total,
			}, true, nil
		}
		// 大小不符/超限：落到下面的按需 range 读(与原行为一致)。
		cacheLog.source = "backend_range_uncacheable"
	}
	cacheLog.backendRead = true
	if cacheLog.source == "unknown" {
		cacheLog.source = "backend_range"
	}
	data, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, req.Offset, int64(req.Limit))
	if err != nil {
		return domain.FileChunk{}, false, fmt.Errorf("read blob %q: %w", blob.LocationKey, err)
	}
	return domain.FileChunk{
		Bytes:    data,
		MimeType: blob.MimeType,
		Total:    total,
	}, true, nil
}

func (s *Service) logGetFileCache(req domain.FileDownloadRequest, blob domain.FileBlob, found bool, chunk domain.FileChunk, cacheLog getFileCacheLog, err error) {
	fields := []zap.Field{
		zap.String("location_key", req.LocationKey),
		zap.Int64("offset", req.Offset),
		zap.Int("limit", req.Limit),
		zap.Bool("found", found),
		zap.String("source", cacheLog.source),
		zap.Bool("meta_cache_hit", cacheLog.metaCacheHit),
		zap.Bool("meta_cache_filled", cacheLog.metaCacheFilled),
		zap.Bool("meta_singleflight_shared", cacheLog.metaSingleflight),
		zap.Bool("byte_cache_eligible", cacheLog.byteCacheEligible),
		zap.Bool("byte_cache_hit", cacheLog.byteCacheHit),
		zap.Bool("byte_cache_filled", cacheLog.byteCacheFilled),
		zap.Bool("byte_singleflight_shared", cacheLog.byteSingleflight),
		zap.Bool("backend_read", cacheLog.backendRead),
		zap.Int("returned_bytes", len(chunk.Bytes)),
		zap.Int64("total_bytes", chunk.Total),
		zap.Duration("dur", time.Since(cacheLog.start)),
	}
	if blob.ObjectKey != "" {
		fields = append(fields,
			zap.String("object_key", blob.ObjectKey),
			zap.Int64("blob_size", blob.Size),
			zap.String("mime_type", blob.MimeType),
		)
	}
	if err != nil {
		fields = append(fields, zap.Error(err))
	}
	s.log.Info("upload.getFile cache", fields...)
}

func sliceBlobBytes(data []byte, offset, limit int64) []byte {
	total := int64(len(data))
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []byte{}
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return append([]byte(nil), data[offset:end]...)
}

// ---- 资源读取（reaction / sticker / document）----

// ListAvailableReactions 返回可用 reaction 目录（带真实文档 id）。
func (s *Service) ListAvailableReactions(ctx context.Context) ([]domain.AvailableReaction, error) {
	return s.media.ListAvailableReactions(ctx)
}

// GetDocuments 按 id 批量加载文档（自定义 emoji / 贴纸）。
func (s *Service) GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error) {
	return s.media.GetDocuments(ctx, ids)
}

// ListStickerSets 列出某类贴纸集（用于 getAllStickers 等）。
func (s *Service) ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	return s.media.ListStickerSets(ctx, kind)
}

// ResolveStickerSet 按 ref 解析贴纸集，并按 DocumentIDs 顺序加载其文档。
func (s *Service) ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	if set, docs, ok := s.stickerSetCache.get(ref); ok {
		return set, docs, true, nil
	}
	// 负缓存：已 seed 集启动即进正缓存（WarmCaches），能走到这里的 miss 多是「未 seed 的 short_name」
	// 被客户端反复请求。TTL 内直接当 not-found 短路，避免每次都打 PG GetStickerSetByShortName。
	if s.stickerSetNegCache != nil && s.stickerSetNegCache.has(ref) {
		return domain.StickerSet{}, nil, false, nil
	}
	var (
		set   domain.StickerSet
		found bool
		err   error
	)
	switch ref.Kind {
	case domain.StickerSetRefByID:
		set, found, err = s.media.GetStickerSetByID(ctx, ref.ID)
	case domain.StickerSetRefByShortName:
		set, found, err = s.media.GetStickerSetByShortName(ctx, ref.ShortName)
	case domain.StickerSetRefBySystem:
		set, found, err = s.media.GetStickerSetBySystemKey(ctx, ref.SystemKey)
	default:
		return domain.StickerSet{}, nil, false, nil
	}
	if err != nil || !found {
		if err == nil && !found && s.stickerSetNegCache != nil {
			s.stickerSetNegCache.put(ref)
		}
		return domain.StickerSet{}, nil, found, err
	}
	docs, err := s.media.GetDocuments(ctx, set.DocumentIDs)
	if err != nil {
		return domain.StickerSet{}, nil, false, err
	}
	ordered := orderDocuments(docs, set.DocumentIDs)
	s.stickerSetCache.put(set, ordered)
	return set, ordered, true, nil
}

// orderDocuments 把无序的文档按 ids 顺序重排（GetDocuments 用 ANY 查询不保证顺序）。
func orderDocuments(docs []domain.Document, ids []int64) []domain.Document {
	byID := make(map[int64]domain.Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}
	out := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		if d, ok := byID[id]; ok {
			out = append(out, d)
		}
	}
	return out
}

// assembleUpload 把已上传分片按 part 顺序拼成完整字节，并清理分片。
// expectedParts>0 时校验分片连续且齐全。
func (s *Service) assembleUpload(ctx context.Context, ownerUserID, fileID int64, expectedParts int) ([]byte, error) {
	buf, err := s.readUploadBytes(ctx, ownerUserID, fileID, expectedParts)
	if err != nil {
		return nil, err
	}
	if err := s.cleanupUploadParts(ctx, ownerUserID, fileID); err != nil {
		return nil, err
	}
	return buf, nil
}

// readUploadBytes validates and reads all parts without consuming them. Message-media
// materialization persists an upload receipt before cleanup; callers that do not need replayability
// continue to use assembleUpload.
func (s *Service) readUploadBytes(ctx context.Context, ownerUserID, fileID int64, expectedParts int) ([]byte, error) {
	parts, _, err := s.loadAndValidateUploadParts(ctx, ownerUserID, fileID, expectedParts)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, uploadPartsTotalSize(parts))
	for _, p := range parts {
		if s.uploadParts == nil {
			return nil, fmt.Errorf("upload part backend not configured")
		}
		data, err := s.uploadParts.GetUploadPart(ctx, p.ObjectKey)
		if err != nil {
			return nil, fmt.Errorf("read upload part %d: %w", p.Part, err)
		}
		if err := validateUploadPartBytes(p, data); err != nil {
			return nil, err
		}
		buf = append(buf, data...)
	}
	return buf, nil
}

type assembledUploadBlob struct {
	ObjectKey string
	Size      int64
	SHA256    []byte
}

// assembleUploadBlob 把上传分片流式写入正式 blob。调用方应在 durable media 元数据
// 成功提交后调用 cleanupUploadParts，避免 metadata 写失败时丢失可重试的上传分片。
func (s *Service) assembleUploadBlob(ctx context.Context, ownerUserID, fileID int64, expectedParts int) (assembledUploadBlob, error) {
	parts, _, err := s.loadAndValidateUploadParts(ctx, ownerUserID, fileID, expectedParts)
	if err != nil {
		return assembledUploadBlob{}, err
	}
	if s.uploadParts == nil {
		return assembledUploadBlob{}, fmt.Errorf("upload part backend not configured")
	}
	reader := &uploadPartsReader{
		ctx:     ctx,
		backend: s.uploadParts,
		parts:   parts,
	}
	defer reader.Close()
	objectKey, size, sum, err := s.blobs.PutReader(ctx, reader)
	if err != nil {
		return assembledUploadBlob{}, err
	}
	return assembledUploadBlob{
		ObjectKey: objectKey,
		Size:      size,
		SHA256:    sum,
	}, nil
}

func (s *Service) loadAndValidateUploadParts(ctx context.Context, ownerUserID, fileID int64, expectedParts int) ([]domain.UploadPart, int64, error) {
	parts, err := s.media.LoadFileParts(ctx, ownerUserID, fileID)
	if err != nil {
		return nil, 0, err
	}
	if len(parts) == 0 {
		return nil, 0, domain.ErrFilePartsInvalid
	}
	if expectedParts > 0 && len(parts) != expectedParts {
		return nil, 0, domain.ErrFilePartsInvalid
	}
	var total int64
	for i, p := range parts {
		if p.Part != i {
			return nil, 0, domain.ErrFilePartsInvalid // 缺片或乱序
		}
		if p.Size <= 0 || p.Size > MaxUploadPartBytes || p.ObjectKey == "" {
			return nil, 0, domain.ErrFilePartsInvalid
		}
		total += p.Size
		if total > DefaultUploadInFlightMaxBytes {
			return nil, 0, domain.ErrFilePartsInvalid
		}
	}
	return parts, total, nil
}

func uploadPartsTotalSize(parts []domain.UploadPart) int {
	var total int64
	for _, p := range parts {
		total += p.Size
	}
	return int(total)
}

func validateUploadPartBytes(part domain.UploadPart, data []byte) error {
	if int64(len(data)) != part.Size {
		return domain.ErrFilePartsInvalid
	}
	if len(part.SHA256) > 0 {
		sum := sha256.Sum256(data)
		if !bytes.Equal(sum[:], part.SHA256) {
			return domain.ErrFilePartsInvalid
		}
	}
	return nil
}

func (s *Service) cleanupUploadParts(ctx context.Context, ownerUserID, fileID int64) error {
	keys, err := s.media.DeleteFileParts(ctx, ownerUserID, fileID)
	if err != nil {
		return err
	}
	if err := s.deleteUploadPartObjects(ctx, keys); err != nil {
		return err
	}
	return nil
}

type uploadPartsReader struct {
	ctx         context.Context
	backend     UploadPartBackend
	parts       []domain.UploadPart
	index       int
	current     io.ReadCloser
	currentRead int64
	currentHash hash.Hash
}

func (r *uploadPartsReader) Read(buf []byte) (int, error) {
	for {
		if r.current == nil {
			if r.index >= len(r.parts) {
				return 0, io.EOF
			}
			select {
			case <-r.ctx.Done():
				return 0, r.ctx.Err()
			default:
			}
			part := r.parts[r.index]
			rc, err := r.backend.OpenUploadPart(r.ctx, part.ObjectKey)
			if err != nil {
				return 0, fmt.Errorf("open upload part %d: %w", part.Part, err)
			}
			r.current = rc
			r.currentRead = 0
			if len(part.SHA256) > 0 {
				r.currentHash = sha256.New()
			} else {
				r.currentHash = nil
			}
		}
		n, err := r.current.Read(buf)
		if n > 0 {
			r.currentRead += int64(n)
			if r.currentHash != nil {
				_, _ = r.currentHash.Write(buf[:n])
			}
			return n, nil
		}
		if err == io.EOF {
			if err := r.finishCurrentPart(); err != nil {
				return 0, err
			}
			continue
		}
		if err != nil {
			_ = r.current.Close()
			part := r.parts[r.index]
			r.current = nil
			return 0, fmt.Errorf("read upload part %d: %w", part.Part, err)
		}
		return 0, nil
	}
}

func (r *uploadPartsReader) finishCurrentPart() error {
	part := r.parts[r.index]
	closeErr := r.current.Close()
	r.current = nil
	if closeErr != nil {
		return fmt.Errorf("close upload part %d: %w", part.Part, closeErr)
	}
	if r.currentRead != part.Size {
		return domain.ErrFilePartsInvalid
	}
	if r.currentHash != nil && !bytes.Equal(r.currentHash.Sum(nil), part.SHA256) {
		return domain.ErrFilePartsInvalid
	}
	r.currentHash = nil
	r.currentRead = 0
	r.index++
	return nil
}

func (r *uploadPartsReader) Close() error {
	if r.current == nil {
		return nil
	}
	err := r.current.Close()
	r.current = nil
	return err
}

func (s *Service) deleteUploadPartObjects(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if s.uploadParts == nil {
		return fmt.Errorf("upload part backend not configured")
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if err := s.uploadParts.DeleteUploadPart(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func validatePart(part, size int) error {
	if part < 0 || part >= MaxUploadParts {
		return domain.ErrFilePartInvalid
	}
	if size == 0 {
		return domain.ErrFilePartInvalid
	}
	if size > MaxUploadPartBytes {
		return domain.ErrFilePartTooBig
	}
	return nil
}
