package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"

	"telesrv/internal/app/files/botavatars"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// Upload part limits: consistent with the Telegram client convention (each part ≤512KB; a cap on the total part count prevents OOM).
const (
	MaxUploadPartBytes = 524288 // 512KB
	MaxUploadParts     = 8000   // 512KB * 8000 ≈ a theoretical 4GB limit, enough for main-path media
)

const (
	DefaultUploadPartTTL          = 24 * time.Hour
	DefaultUploadPartGCInterval   = 30 * time.Minute
	DefaultUploadPartGCBatch      = 10000
	DefaultUploadInFlightMaxBytes = int64(MaxUploadPartBytes) * int64(MaxUploadParts)
	DefaultUploadInFlightMaxParts = MaxUploadParts
	DefaultUploadInFlightMaxFiles = 64
)

// blobMetaCacheCapacity is the LRU capacity for location_key→FileBlob metadata (about a hundred bytes each, roughly 13MB).
const blobMetaCacheCapacity = 1 << 16

// The small-file hot cache covers only immutable small blobs like stickers/reactions/thumbnails; large media is still read in ranges.
const (
	blobBytesCacheMaxEntryBytes = 256 << 10 // 256KB
	blobBytesCacheMaxBytes      = 64 << 20  // 64MB
	// stickerSetNegativeCacheTTL is the validity of the negative cache for sticker sets not found: a
	// client repeatedly calls getStickerSet for an unseeded short_name, so the not-found result is
	// cached briefly and short-circuits PG. The short TTL means a set added at runtime lags by at most this long.
	stickerSetNegativeCacheTTL = 30 * time.Second
)

// Service implements upload part accumulation, blob persistence, getFile download, and assembling uploaded files into Photo / Document.
type Service struct {
	media       store.MediaStore
	gifCatalog  store.GifCatalogStore
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
	// blobMetaSF/blobBytesSF merge concurrent first access to the same hot blob: otherwise every
	// concurrent getFile hits its own PG GetFileBlob + backend GetRange (especially when popular
	// stickers/reactions/avatars are pulled by many users at once).
	blobMetaSF         singleflight.Group
	blobBytesSF        singleflight.Group
	blobRangeSF        singleflight.Group
	stickerSetCache    *stickerSetFullCache
	stickerSetNegCache *stickerSetNegativeCache
	uploadQuota        domain.UploadPartQuota
	mapTiles           *mapTileProxy
	externalMedia      *externalMediaFetcher
	webpage            *webpageFetcher
	// effects is the message effect catalog (messages.getAvailableEffects). Globally static, written
	// once by seedEffects at startup and then read-only, hence lock-free — like the read-model caches it
	// is fully populated before the service is ready. effectsHash is computed once at seed time and the
	// handler compares it directly to return NotModified, with no need to recompute per RPC.
	effects     []domain.AvailableEffect
	effectsHash int

	// premiumPromo is populated during startup seed and then read by RPC
	// handlers. Keep a lock so the ownership boundary remains race-safe even
	// when exercised concurrently in tests.
	premiumPromoMu    sync.RWMutex
	premiumPromo      domain.PremiumPromoCatalog
	premiumPromoReady bool
}

// Option configures the optional capabilities of the files service.
type Option func(*Service)

// WithLogger injects the logger. A no-op logger is used when not injected.
func WithLogger(log *zap.Logger) Option {
	return func(s *Service) {
		if log != nil {
			s.log = log
		}
	}
}

// WithVideoThumbnailer overrides the video thumbnail generator. Passing nil explicitly disables the server-side frame extraction fallback.
func WithVideoThumbnailer(thumbnailer VideoThumbnailer) Option {
	return func(s *Service) {
		s.thumbs = thumbnailer
		s.thumbsSet = true
	}
}

// WithGIFTranscoder overrides the real GIF→MP4 normalizer. Passing nil can be used to test the unavailable path.
func WithGIFTranscoder(transcoder GIFTranscoder) Option {
	return func(s *Service) {
		s.gifs = transcoder
		s.gifsSet = true
	}
}

// WithGifCatalog binds the curated catalog. It is independent of BlobBackend:
// imported bytes always use the one backend selected for this Service.
func WithGifCatalog(catalog store.GifCatalogStore) Option {
	return func(s *Service) { s.gifCatalog = catalog }
}

// WithUploadPartQuota overrides the per-user in-flight upload part quota; a field <=0 means that dimension is unlimited.
func WithUploadPartQuota(quota domain.UploadPartQuota) Option {
	return func(s *Service) {
		s.uploadQuota = quota
	}
}

// WithUploadPartBackend separates transient upload staging from permanent blob
// storage. S3 mode uses a local implementation here without making localfs a
// permanent read/write fallback.
func WithUploadPartBackend(backend UploadPartBackend) Option {
	return func(s *Service) {
		s.uploadParts = backend
	}
}

// NewService creates the files service. dc is this server's DC id, written as the dc_id of new documents/photos.
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
		// Option application order does not matter: the logger is injected uniformly after all Options run.
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

// txMediaStore is a subset of store.MediaStore that supports transactions.
type txMediaStore interface {
	store.MediaStore
	WithTx(ctx context.Context, fn func(ctx context.Context, txMedia store.MediaStore) error) error
}

// SeedTx implements botavatars.AvatarSetter. It runs fn inside a single
// transaction holding the PostgreSQL advisory lock that serialises bot-avatar
// seeding across instances. If fn returns an error the transaction is rolled
// back (including any media created inside), otherwise it is committed. This
// makes the check+create+bind sequence atomic and orphan-free.
//
// The avatar pipeline needs more than the media store: creating the Premium
// bot's animated avatar stages bytes through uploadParts (SaveFilePart) and
// derives the video still through thumbs. Both, along with every other
// configured dependency, are carried onto the transaction-scoped service;
// only media is swapped for its transactional view. The singleflight groups
// and the premiumPromo mutex are deliberately re-initialised fresh because a
// lock must never be copied after it has been used by the outer service.
func (s *Service) SeedTx(ctx context.Context, fn func(ctx context.Context, tx botavatars.AvatarSetter) error) error {
	txMs, ok := s.media.(txMediaStore)
	if !ok {
		return fmt.Errorf("bot avatar: media store does not support transactions")
	}
	return txMs.WithTx(ctx, func(ctx context.Context, txMedia store.MediaStore) error {
		txService := &Service{
			media:              txMedia,
			gifCatalog:         s.gifCatalog,
			blobs:              s.blobs,
			uploadParts:        s.uploadParts,
			dc:                 s.dc,
			log:                s.log,
			thumbs:             s.thumbs,
			thumbsSet:          s.thumbsSet,
			gifs:               s.gifs,
			gifsSet:            s.gifsSet,
			blobCache:          s.blobCache,
			byteCache:          s.byteCache,
			stickerSetCache:    s.stickerSetCache,
			stickerSetNegCache: s.stickerSetNegCache,
			uploadQuota:        s.uploadQuota,
			mapTiles:           s.mapTiles,
			externalMedia:      s.externalMedia,
			webpage:            s.webpage,
			effects:            s.effects,
			effectsHash:        s.effectsHash,
			premiumPromo:       s.premiumPromo,
			premiumPromoReady:  s.premiumPromoReady,
		}
		return fn(ctx, txService)
	})
}

// SaveFilePart accumulates a small file part.
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

// ValidBigUploadPartCount accepts TDLib's unknown-size generated upload marker.
// The final inputFileBig must still declare the actual positive part count.
func ValidBigUploadPartCount(totalParts int) bool {
	return totalParts == -1 || (totalParts > 0 && totalParts <= MaxUploadParts)
}

// SaveBigFilePart stores a big-file part, including an in-progress generation.
func (s *Service) SaveBigFilePart(ctx context.Context, ownerUserID, fileID int64, part, totalParts int, bytes []byte) (bool, error) {
	if err := validatePart(part, len(bytes)); err != nil {
		return false, err
	}
	if !ValidBigUploadPartCount(totalParts) || (totalParts > 0 && part >= totalParts) {
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

// DeleteExpiredUploadParts cleans up transient upload parts not yet assembled after the retention period.
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

// GetFile takes a range of blob content by location_key. found=false means no blob corresponds to
// that location. Metadata goes through an in-process LRU (eliminating a PG lookup per chunk); small
// blobs' full bytes enter the LRU so the sticker/reaction/thumbnail hot paths can slice directly from
// memory; large blobs are still read by offset/limit range.
type blobMetaResult struct {
	blob        domain.FileBlob
	found       bool
	cacheHit    bool
	cacheFilled bool
}

type blobBytesResult struct {
	data        []byte
	total       int64
	rangeSHA256 [sha256.Size]byte
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
	rangeSingleflight bool
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
		// Concurrent first access to the same location_key is merged into one PG GetFileBlob.
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
	if blob.Backend != domain.MediaBackend(s.blobs.Name()) {
		return domain.FileChunk{}, false, fmt.Errorf(
			"blob backend mismatch for %q: stored=%q configured=%q",
			blob.LocationKey, blob.Backend, s.blobs.Name(),
		)
	}
	if blob.Size > 0 && blob.Size <= blobBytesCacheMaxEntryBytes {
		cacheLog.byteCacheEligible = true
		if data, ok := s.byteCache.get(blob.ObjectKey); ok {
			cacheLog.byteCacheHit = true
			cacheLog.source = "byte_cache"
			return immutableFileChunk(blob, viewBlobBytes(data, req.Offset, int64(req.Limit)), int64(len(data)), req.Offset), true, nil
		}
		// Concurrent first access to the same object_key's small blob is merged into one full backend read + one byteCache fill.
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
		// A byte cache entry is immutable after publication; the caller only borrows a read-only view of the requested range.
		if res := v.(blobBytesResult); res.cacheable {
			cacheLog.byteCacheHit = res.cacheHit
			cacheLog.byteCacheFilled = res.cacheFilled
			cacheLog.backendRead = res.cacheFilled
			if res.cacheHit {
				cacheLog.source = "byte_cache"
			} else {
				cacheLog.source = "backend_fill_byte_cache"
			}
			return immutableFileChunk(blob, viewBlobBytes(res.data, req.Offset, int64(req.Limit)), res.total, req.Offset), true, nil
		}
		// Size mismatch/over limit: fall through to the on-demand range read below (same as the original behavior).
		cacheLog.source = "backend_range_uncacheable"
	}
	cacheLog.backendRead = true
	if cacheLog.source == "unknown" {
		cacheLog.source = "backend_range"
	}
	rangeKey := fmt.Sprintf("%s:%d:%d", blob.ObjectKey, req.Offset, req.Limit)
	v, err, shared := s.blobRangeSF.Do(rangeKey, func() (any, error) {
		data, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, req.Offset, int64(req.Limit))
		if err != nil {
			return blobBytesResult{}, err
		}
		return blobBytesResult{data: data, total: total, rangeSHA256: sha256.Sum256(data)}, nil
	})
	cacheLog.rangeSingleflight = shared
	if err != nil {
		return domain.FileChunk{}, false, fmt.Errorf("read blob %q: %w", blob.LocationKey, err)
	}
	res := v.(blobBytesResult)
	return immutableFileChunkWithDigest(blob, res.data, res.total, req.Offset, res.rangeSHA256), true, nil
}

func immutableFileChunk(blob domain.FileBlob, data []byte, total, offset int64) domain.FileChunk {
	return immutableFileChunkWithDigest(blob, data, total, offset, sha256.Sum256(data))
}

func immutableFileChunkWithDigest(blob domain.FileBlob, data []byte, total, offset int64, digest [sha256.Size]byte) domain.FileChunk {
	if offset < 0 {
		offset = 0
	}
	return domain.FileChunk{
		Bytes:    data,
		MimeType: blob.MimeType,
		Total:    total,
		ImmutableRange: &domain.ImmutableFileRange{
			Backend:     blob.Backend,
			ObjectKey:   blob.ObjectKey,
			Offset:      offset,
			Length:      len(data),
			Total:       total,
			MimeType:    blob.MimeType,
			RangeSHA256: digest,
		},
	}
}

// ReadImmutableFileRange is the replay-only byte path. It intentionally skips
// location metadata and authorization: those facts were resolved by the fresh
// RPC execution, while this capability names one immutable content-addressed
// object and one digest-protected range.
func (s *Service) ReadImmutableFileRange(ctx context.Context, source domain.ImmutableFileRange) ([]byte, error) {
	if s == nil || s.blobs == nil {
		return nil, fmt.Errorf("immutable blob backend is unavailable")
	}
	if source.ObjectKey == "" || source.Offset < 0 || source.Length < 0 || source.Total < 0 {
		return nil, fmt.Errorf("invalid immutable file range")
	}
	if source.Backend != domain.MediaBackend(s.blobs.Name()) {
		return nil, fmt.Errorf("immutable blob backend mismatch: source=%q configured=%q", source.Backend, s.blobs.Name())
	}
	data, total, err := s.blobs.GetRange(ctx, source.ObjectKey, source.Offset, int64(source.Length))
	if err != nil {
		return nil, fmt.Errorf("read immutable blob range: %w", err)
	}
	if total != source.Total || len(data) != source.Length {
		return nil, fmt.Errorf("immutable blob range changed: total=%d/%d length=%d/%d", total, source.Total, len(data), source.Length)
	}
	if digest := sha256.Sum256(data); digest != source.RangeSHA256 {
		return nil, fmt.Errorf("immutable blob range digest mismatch")
	}
	return data, nil
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
		zap.Bool("range_singleflight_shared", cacheLog.rangeSingleflight),
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
	if err != nil {
		s.log.Warn("upload.getFile cache failed", fields...)
		return
	}
	s.log.Debug("upload.getFile cache", fields...)
}

func viewBlobBytes(data []byte, offset, limit int64) []byte {
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
	// Clip capacity at end so an accidental append cannot overwrite a neighboring
	// range in the immutable cache entry. Element mutation remains forbidden by the
	// FileChunk contract and is covered by ownership tests at every consumer boundary.
	return data[offset:end:end]
}

// ---- Resource reads (reaction / sticker / document) ----

// ListAvailableReactions returns the available reaction catalog (with real document ids).
func (s *Service) ListAvailableReactions(ctx context.Context) ([]domain.AvailableReaction, error) {
	return s.media.ListAvailableReactions(ctx)
}

// GetDocuments batch-loads documents by id (custom emoji / stickers).
func (s *Service) GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error) {
	return s.media.GetDocuments(ctx, ids)
}

// ListStickerSets lists sticker sets of a kind (used by getAllStickers etc.).
func (s *Service) ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	return s.media.ListStickerSets(ctx, kind)
}

// stickerSetPublishAttempts bounds how often a lookup re-reads after a
// concurrent write invalidated its snapshot. The cache epoch is shared by every
// set, so a steady stream of unrelated sticker-set writes must not keep a cold
// lookup re-querying PostgreSQL; the last read is still a valid answer for a
// read racing a write, it is only left out of the cache.
const stickerSetPublishAttempts = 3

// ResolveStickerSet resolves a sticker set by ref and loads its documents in DocumentIDs order.
func (s *Service) ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return domain.StickerSet{}, nil, false, err
		}
		epoch := s.stickerSetCache.loadEpoch()
		if set, docs, ok := s.stickerSetCache.get(ref); ok {
			return set, docs, true, nil
		}
		// Negative cache: seeded sets are filled into the positive cache by WarmCaches, so a miss
		// reaching here is mostly an "unseeded short_name" repeatedly requested by the client. Within
		// the TTL it is treated as not-found and short-circuited, avoiding a PG GetStickerSetByShortName each time.
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
		if s.stickerSetCache.putIfEpoch(set, ordered, epoch) || attempt >= stickerSetPublishAttempts {
			return set, ordered, true, nil
		}
		// As with warm-up, an old query must not overwrite a concurrent modification; on retry, prefer the cache just published by the write path.
	}
}

// orderDocuments rearranges unordered documents into ids order (a GetDocuments ANY query does not guarantee order).
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

// assembleUpload assembles uploaded parts into complete bytes in part order and cleans up the parts.
// The final positive part count must match all contiguous stored parts.
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

// assembleUploadBlob streams uploaded parts into a permanent blob. The caller should call
// cleanupUploadParts only after the durable media metadata is successfully committed, so a metadata
// write failure does not lose retryable upload parts.
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
	if expectedParts <= 0 || expectedParts > MaxUploadParts {
		return nil, 0, domain.ErrFilePartsInvalid
	}
	parts, err := s.media.LoadFileParts(ctx, ownerUserID, fileID)
	if err != nil {
		return nil, 0, err
	}
	if len(parts) == 0 {
		return nil, 0, domain.ErrFilePartsInvalid
	}
	if len(parts) != expectedParts {
		return nil, 0, domain.ErrFilePartsInvalid
	}
	var total int64
	for i, p := range parts {
		if p.Part != i {
			return nil, 0, domain.ErrFilePartsInvalid // missing or out-of-order part
		}
		if p.Size <= 0 || p.Size > MaxUploadPartBytes || p.ObjectKey == "" {
			return nil, 0, domain.ErrFilePartsInvalid
		}
		if p.Big != parts[0].Big ||
			(p.Big && (!ValidBigUploadPartCount(p.TotalParts) ||
				(p.TotalParts > 0 && p.TotalParts != expectedParts))) ||
			(!p.Big && p.TotalParts != 0) {
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
