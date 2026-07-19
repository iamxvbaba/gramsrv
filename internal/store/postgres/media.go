package postgres

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// MediaStore 用 PostgreSQL 实现 store.MediaStore（媒体元数据 + blob 索引）。
type MediaStore struct {
	db        sqlcgen.DBTX
	q         *sqlcgen.Queries
	documents *documentMetaCache
}

// NewMediaStore 基于 pgx 连接池（或事务）创建 MediaStore。
func NewMediaStore(db sqlcgen.DBTX) *MediaStore {
	return &MediaStore{
		db:        db,
		q:         sqlcgen.New(db),
		documents: newDocumentMetaCache(documentMetaCacheCapacity),
	}
}

// bytesOrEmpty 把 nil []byte 归一为空切片，避免落入 NOT NULL bytea 列时被当作 NULL。
func bytesOrEmpty(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

var _ store.MediaStore = (*MediaStore)(nil)

func validUploadedMediaReceipt(receipt domain.UploadedMediaReceipt) bool {
	if receipt.OwnerUserID == 0 || receipt.FileID == 0 || receipt.MediaID == 0 || len(receipt.IntentHash) != 32 {
		return false
	}
	return receipt.Kind == domain.UploadedMediaPhoto || receipt.Kind == domain.UploadedMediaDocument
}

func (s *MediaStore) GetUploadedMediaReceipt(ctx context.Context, ownerUserID, fileID int64) (domain.UploadedMediaReceipt, bool, error) {
	var receipt domain.UploadedMediaReceipt
	var kind string
	err := s.db.QueryRow(ctx, `
SELECT owner_user_id, file_id, intent_hash, media_kind, media_id, created_at
FROM uploaded_media_receipts
WHERE owner_user_id = $1 AND file_id = $2`, ownerUserID, fileID).Scan(
		&receipt.OwnerUserID,
		&receipt.FileID,
		&receipt.IntentHash,
		&kind,
		&receipt.MediaID,
		&receipt.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UploadedMediaReceipt{}, false, nil
	}
	if err != nil {
		return domain.UploadedMediaReceipt{}, false, fmt.Errorf("get uploaded media receipt: %w", err)
	}
	receipt.Kind = domain.UploadedMediaKind(kind)
	if !validUploadedMediaReceipt(receipt) {
		return domain.UploadedMediaReceipt{}, false, fmt.Errorf(
			"get uploaded media receipt: invalid owner=%d file=%d kind=%q media=%d hash=%d",
			receipt.OwnerUserID, receipt.FileID, receipt.Kind, receipt.MediaID, len(receipt.IntentHash),
		)
	}
	return receipt, true, nil
}

func (s *MediaStore) PutUploadedMediaReceipt(ctx context.Context, receipt domain.UploadedMediaReceipt) (domain.UploadedMediaReceipt, bool, error) {
	if !validUploadedMediaReceipt(receipt) {
		return domain.UploadedMediaReceipt{}, false, fmt.Errorf(
			"put uploaded media receipt: invalid owner=%d file=%d kind=%q media=%d hash=%d",
			receipt.OwnerUserID, receipt.FileID, receipt.Kind, receipt.MediaID, len(receipt.IntentHash),
		)
	}
	tag, err := s.db.Exec(ctx, `
INSERT INTO uploaded_media_receipts (owner_user_id, file_id, intent_hash, media_kind, media_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (owner_user_id, file_id) DO NOTHING`,
		receipt.OwnerUserID, receipt.FileID, receipt.IntentHash, string(receipt.Kind), receipt.MediaID,
	)
	if err != nil {
		return domain.UploadedMediaReceipt{}, false, fmt.Errorf("put uploaded media receipt: %w", err)
	}
	stored, found, err := s.GetUploadedMediaReceipt(ctx, receipt.OwnerUserID, receipt.FileID)
	if err != nil {
		return domain.UploadedMediaReceipt{}, false, err
	}
	if !found {
		return domain.UploadedMediaReceipt{}, false, fmt.Errorf("put uploaded media receipt: row disappeared after insert")
	}
	return stored, tag.RowsAffected() == 1, nil
}

// ---- 上传分片 ----

func (s *MediaStore) SaveFilePart(ctx context.Context, part domain.UploadPart) error {
	backend := string(part.Backend)
	if backend == "" {
		backend = string(domain.MediaBackendLocalFS)
	}
	sha := part.SHA256
	if sha == nil {
		sha = []byte{}
	}
	return s.q.SaveUploadPart(ctx, sqlcgen.SaveUploadPartParams{
		OwnerUserID: part.OwnerUserID,
		FileID:      part.FileID,
		Part:        int32(part.Part),
		TotalParts:  int32(part.TotalParts),
		IsBig:       part.Big,
		Backend:     backend,
		ObjectKey:   part.ObjectKey,
		Size:        part.Size,
		Sha256:      sha,
	})
}

func (s *MediaStore) UploadPartUsage(ctx context.Context, ownerUserID int64) (domain.UploadPartUsage, error) {
	row, err := s.q.GetUploadPartUsage(ctx, ownerUserID)
	if err != nil {
		return domain.UploadPartUsage{}, err
	}
	return domain.UploadPartUsage{
		Bytes: row.Bytes,
		Parts: int(row.Parts),
		Files: int(row.Files),
	}, nil
}

func (s *MediaStore) UploadPartSlot(ctx context.Context, ownerUserID, fileID int64, part int) (domain.UploadPartSlot, error) {
	row, err := s.q.GetUploadPartSlot(ctx, sqlcgen.GetUploadPartSlotParams{
		OwnerUserID: ownerUserID,
		FileID:      fileID,
		Part:        int32(part),
	})
	if err != nil {
		return domain.UploadPartSlot{}, err
	}
	found := row.ExistingBytes >= 0
	existingBytes := row.ExistingBytes
	if !found {
		existingBytes = 0
	}
	return domain.UploadPartSlot{
		ExistingBytes: existingBytes,
		ObjectKey:     row.ObjectKey,
		FileParts:     int(row.FileParts),
		Found:         found,
	}, nil
}

func (s *MediaStore) LoadFileParts(ctx context.Context, ownerUserID, fileID int64) ([]domain.UploadPart, error) {
	rows, err := s.q.ListUploadParts(ctx, sqlcgen.ListUploadPartsParams{OwnerUserID: ownerUserID, FileID: fileID})
	if err != nil {
		return nil, err
	}
	out := make([]domain.UploadPart, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.UploadPart{
			OwnerUserID: ownerUserID,
			FileID:      fileID,
			Part:        int(r.Part),
			TotalParts:  int(r.TotalParts),
			Big:         r.IsBig,
			Backend:     domain.MediaBackend(r.Backend),
			ObjectKey:   r.ObjectKey,
			Size:        r.Size,
			SHA256:      r.Sha256,
		})
	}
	return out, nil
}

func (s *MediaStore) DeleteFileParts(ctx context.Context, ownerUserID, fileID int64) ([]string, error) {
	return s.q.DeleteUploadParts(ctx, sqlcgen.DeleteUploadPartsParams{OwnerUserID: ownerUserID, FileID: fileID})
}

func (s *MediaStore) DeleteExpiredUploadParts(ctx context.Context, before time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	return s.q.DeleteExpiredUploadParts(ctx, sqlcgen.DeleteExpiredUploadPartsParams{
		Before:     pgtype.Timestamptz{Time: before, Valid: true},
		BatchLimit: int32(limit),
	})
}

// ---- blob 索引 ----

func (s *MediaStore) PutFileBlob(ctx context.Context, blob domain.FileBlob) error {
	backend := string(blob.Backend)
	if backend == "" {
		backend = string(domain.MediaBackendLocalFS)
	}
	sha := blob.SHA256
	if sha == nil {
		sha = []byte{} // 列为 NOT NULL；nil []byte 会被 pgx 当作 NULL。
	}
	return s.q.PutFileBlob(ctx, sqlcgen.PutFileBlobParams{
		LocationKey: blob.LocationKey,
		Backend:     backend,
		ObjectKey:   blob.ObjectKey,
		Size:        blob.Size,
		Sha256:      sha,
		MimeType:    blob.MimeType,
	})
}

func (s *MediaStore) GetFileBlob(ctx context.Context, locationKey string) (domain.FileBlob, bool, error) {
	row, err := s.q.GetFileBlob(ctx, locationKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.FileBlob{}, false, nil
		}
		return domain.FileBlob{}, false, err
	}
	return domain.FileBlob{
		LocationKey: row.LocationKey,
		Backend:     domain.MediaBackend(row.Backend),
		ObjectKey:   row.ObjectKey,
		Size:        row.Size,
		SHA256:      row.Sha256,
		MimeType:    row.MimeType,
	}, true, nil
}

// GetFileBlobs 一发 ANY 查询批量取多个 location_key 的 blob 元数据，替代逐个 GetFileBlob 往返
// （启动预热曾对 ~2400 个 blob 各打一次 PG，是启动期 N+1）。缺失的 key 不在返回 map 中。
func (s *MediaStore) GetFileBlobs(ctx context.Context, locationKeys []string) (map[string]domain.FileBlob, error) {
	if len(locationKeys) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT location_key, backend, object_key, size, sha256, mime_type
FROM file_blobs
WHERE location_key = ANY($1::text[])`, locationKeys)
	if err != nil {
		return nil, fmt.Errorf("get file blobs: %w", err)
	}
	defer rows.Close()
	out := make(map[string]domain.FileBlob, len(locationKeys))
	for rows.Next() {
		var (
			blob    domain.FileBlob
			backend string
		)
		if err := rows.Scan(&blob.LocationKey, &backend, &blob.ObjectKey, &blob.Size, &blob.SHA256, &blob.MimeType); err != nil {
			return nil, fmt.Errorf("scan file blob: %w", err)
		}
		blob.Backend = domain.MediaBackend(backend)
		out[blob.LocationKey] = blob
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MediaStore) GetSeedState(ctx context.Context, key string) (string, bool, error) {
	var hash string
	if err := s.db.QueryRow(ctx, `
SELECT content_hash
FROM seed_states
WHERE key = $1`, key).Scan(&hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get seed state: %w", err)
	}
	return hash, true, nil
}

func (s *MediaStore) PutSeedState(ctx context.Context, key, hash string) error {
	_, err := s.db.Exec(ctx, `
INSERT INTO seed_states (key, content_hash)
VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET
  content_hash = EXCLUDED.content_hash,
  updated_at = now()`, key, hash)
	if err != nil {
		return fmt.Errorf("put seed state: %w", err)
	}
	return nil
}

// ---- 文档 ----

func (s *MediaStore) PutDocument(ctx context.Context, doc domain.Document) error {
	params, err := putDocumentParams(doc)
	if err != nil {
		return err
	}
	if err := s.q.PutDocument(ctx, params); err != nil {
		return err
	}
	s.documents.put(doc.ID, doc)
	return nil
}

func putDocumentParams(doc domain.Document) (sqlcgen.PutDocumentParams, error) {
	attrs, err := jsonArrayOrEmpty(doc.Attributes)
	if err != nil {
		return sqlcgen.PutDocumentParams{}, err
	}
	thumbs, err := jsonArrayOrEmpty(doc.Thumbs)
	if err != nil {
		return sqlcgen.PutDocumentParams{}, err
	}
	return sqlcgen.PutDocumentParams{
		ID:             doc.ID,
		AccessHash:     doc.AccessHash,
		FileReference:  bytesOrEmpty(doc.FileReference),
		Date:           int32(doc.Date),
		MimeType:       doc.MimeType,
		Size:           doc.Size,
		DcID:           int32(doc.DCID),
		AttributesJson: attrs,
		ThumbsJson:     thumbs,
	}, nil
}

func (s *MediaStore) GetDocument(ctx context.Context, id int64) (domain.Document, bool, error) {
	if doc, ok := s.documents.get(id); ok {
		return doc, true, nil
	}
	row, err := s.q.GetDocument(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Document{}, false, nil
		}
		return domain.Document{}, false, err
	}
	doc, err := documentFromRow(row)
	if err != nil {
		return domain.Document{}, false, err
	}
	s.documents.put(doc.ID, doc)
	return doc, true, nil
}

func (s *MediaStore) GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	found := make(map[int64]domain.Document, len(ids))
	var missing []int64
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
		if doc, ok := s.documents.get(id); ok {
			found[id] = doc
			continue
		}
		missing = append(missing, id)
	}
	if len(unique) == 0 {
		return nil, nil
	}

	if len(missing) > 0 {
		rows, err := s.q.GetDocuments(ctx, missing)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			doc, err := documentFromRow(sqlcgen.GetDocumentRow(r))
			if err != nil {
				return nil, err
			}
			s.documents.put(doc.ID, doc)
			found[doc.ID] = cloneDocument(doc)
		}
	}

	out := make([]domain.Document, 0, len(found))
	for _, id := range unique {
		doc, ok := found[id]
		if !ok {
			continue
		}
		out = append(out, cloneDocument(doc))
	}
	return out, nil
}

const documentMetaCacheCapacity = 1 << 16

// documentMetaCache keeps immutable document metadata hot for message/media
// hydration. PutDocument refreshes entries synchronously, so same-process reads
// observe newly generated access_hash/file_reference without waiting for TTL.
type documentMetaCache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List
	m   map[int64]*list.Element
}

type documentMetaEntry struct {
	id  int64
	doc domain.Document
}

func newDocumentMetaCache(capacity int) *documentMetaCache {
	if capacity <= 0 {
		capacity = 1
	}
	return &documentMetaCache{
		cap: capacity,
		ll:  list.New(),
		m:   make(map[int64]*list.Element, capacity),
	}
}

func (c *documentMetaCache) get(id int64) (domain.Document, bool) {
	if c == nil || id == 0 {
		return domain.Document{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[id]
	if !ok {
		return domain.Document{}, false
	}
	c.ll.MoveToFront(el)
	return cloneDocument(el.Value.(*documentMetaEntry).doc), true
}

func (c *documentMetaCache) put(id int64, doc domain.Document) {
	if c == nil || id == 0 {
		return
	}
	copied := cloneDocument(doc)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[id]; ok {
		el.Value.(*documentMetaEntry).doc = copied
		c.ll.MoveToFront(el)
		return
	}
	c.m[id] = c.ll.PushFront(&documentMetaEntry{id: id, doc: copied})
	if c.ll.Len() > c.cap {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.m, oldest.Value.(*documentMetaEntry).id)
		}
	}
}

func cloneDocument(doc domain.Document) domain.Document {
	doc.FileReference = append([]byte(nil), doc.FileReference...)
	if len(doc.Attributes) > 0 {
		attrs := make([]domain.DocumentAttribute, len(doc.Attributes))
		copy(attrs, doc.Attributes)
		for i := range attrs {
			attrs[i].Waveform = append([]byte(nil), attrs[i].Waveform...)
		}
		doc.Attributes = attrs
	}
	if len(doc.Thumbs) > 0 {
		thumbs := make([]domain.PhotoSize, len(doc.Thumbs))
		copy(thumbs, doc.Thumbs)
		for i := range thumbs {
			thumbs[i].Bytes = append([]byte(nil), thumbs[i].Bytes...)
			thumbs[i].Sizes = append([]int(nil), thumbs[i].Sizes...)
			thumbs[i].BackgroundColors = append([]int(nil), thumbs[i].BackgroundColors...)
		}
		doc.Thumbs = thumbs
	}
	return doc
}

func documentFromRow(row sqlcgen.GetDocumentRow) (domain.Document, error) {
	attrs, err := decodeDocumentAttributes(row.AttributesJson)
	if err != nil {
		return domain.Document{}, err
	}
	thumbs, err := decodePhotoSizes(row.ThumbsJson)
	if err != nil {
		return domain.Document{}, err
	}
	return domain.Document{
		ID:            row.ID,
		AccessHash:    row.AccessHash,
		FileReference: row.FileReference,
		Date:          int(row.Date),
		MimeType:      row.MimeType,
		Size:          row.Size,
		DCID:          int(row.DcID),
		Attributes:    attrs,
		Thumbs:        thumbs,
	}, nil
}

// ---- 照片 ----

func (s *MediaStore) PutPhoto(ctx context.Context, photo domain.Photo) error {
	sizes, err := jsonArrayOrEmpty(photo.Sizes)
	if err != nil {
		return err
	}
	return s.q.PutPhoto(ctx, sqlcgen.PutPhotoParams{
		ID:            photo.ID,
		AccessHash:    photo.AccessHash,
		FileReference: bytesOrEmpty(photo.FileReference),
		Date:          int32(photo.Date),
		DcID:          int32(photo.DCID),
		HasStickers:   photo.HasStickers,
		SizesJson:     sizes,
	})
}

func (s *MediaStore) GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error) {
	row, err := s.q.GetPhoto(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Photo{}, false, nil
		}
		return domain.Photo{}, false, err
	}
	photo, err := photoFromFields(row.ID, row.AccessHash, row.FileReference, int(row.Date), int(row.DcID), row.HasStickers, row.SizesJson)
	if err != nil {
		return domain.Photo{}, false, err
	}
	return photo, true, nil
}

// GetPhotos resolves a bounded set of immutable photo metadata with one indexed
// ANY query. Missing ids are omitted and the result follows first-seen caller order.
func (s *MediaStore) GetPhotos(ctx context.Context, ids []int64) ([]domain.Photo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT id, access_hash, file_reference, date, dc_id, has_stickers, sizes::text
FROM photos
WHERE id = ANY($1::bigint[])
`, unique)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[int64]domain.Photo, len(unique))
	for rows.Next() {
		photo, err := scanPhotoRow(rows)
		if err != nil {
			return nil, err
		}
		byID[photo.ID] = photo
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.Photo, 0, len(byID))
	for _, id := range unique {
		if photo, ok := byID[id]; ok {
			out = append(out, photo)
		}
	}
	return out, nil
}

type photoScanner interface {
	Scan(dest ...any) error
}

func scanPhotoRow(row photoScanner) (domain.Photo, error) {
	var (
		id            int64
		accessHash    int64
		fileReference []byte
		date          int32
		dcID          int32
		hasStickers   bool
		sizesJSON     string
	)
	if err := row.Scan(&id, &accessHash, &fileReference, &date, &dcID, &hasStickers, &sizesJSON); err != nil {
		return domain.Photo{}, err
	}
	return photoFromFields(id, accessHash, fileReference, int(date), int(dcID), hasStickers, sizesJSON)
}

func photoFromFields(id, accessHash int64, fileReference []byte, date, dcID int, hasStickers bool, sizesJSON string) (domain.Photo, error) {
	sizes, err := decodePhotoSizes(sizesJSON)
	if err != nil {
		return domain.Photo{}, err
	}
	return domain.Photo{
		ID:            id,
		AccessHash:    accessHash,
		FileReference: fileReference,
		Date:          date,
		DCID:          dcID,
		HasStickers:   hasStickers,
		Sizes:         sizes,
	}, nil
}

// ---- 贴纸集 ----

func (s *MediaStore) PutStickerSet(ctx context.Context, set domain.StickerSet) error {
	thumbs, err := jsonArrayOrEmpty(set.Thumbs)
	if err != nil {
		return err
	}
	docIDs, err := jsonArrayOrEmpty(set.DocumentIDs)
	if err != nil {
		return err
	}
	packs, err := jsonArrayOrEmpty(set.Packs)
	if err != nil {
		return err
	}
	kind := string(set.Kind)
	if kind == "" {
		kind = string(domain.StickerSetKindStickers)
	}
	return s.q.PutStickerSet(ctx, sqlcgen.PutStickerSetParams{
		ID:              set.ID,
		AccessHash:      set.AccessHash,
		ShortName:       set.ShortName,
		Title:           set.Title,
		Count:           int32(set.Count),
		Hash:            int32(set.Hash),
		SetKind:         kind,
		Official:        set.Official,
		Animated:        set.Animated,
		Videos:          set.Videos,
		Emojis:          set.Emojis,
		Masks:           set.Masks,
		Installed:       set.Installed,
		Archived:        set.Archived,
		InstalledDate:   int32(set.InstalledDate),
		ThumbDocumentID: set.ThumbDocumentID,
		ThumbsJson:      thumbs,
		ThumbDcID:       int32(set.ThumbDCID),
		ThumbVersion:    int32(set.ThumbVersion),
		DocumentIdsJson: docIDs,
		PacksJson:       packs,
		SortOrder:       int32(set.SortOrder),
		SystemKey:       set.SystemKey,
	})
}

func (s *MediaStore) CreateStickerSet(ctx context.Context, set domain.StickerSet, docs []domain.Document) error {
	err := withTx(ctx, s.db, "create sticker set", func(tx pgx.Tx) error {
		if err := insertStickerSet(ctx, tx, set); err != nil {
			return err
		}
		qtx := s.q.WithTx(tx)
		for _, doc := range docs {
			params, err := putDocumentParams(doc)
			if err != nil {
				return err
			}
			if err := qtx.PutDocument(ctx, params); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if stickerSetShortNameConflict(err) {
			return domain.ErrStickerSetShortNameOccupied
		}
		return err
	}
	for _, doc := range docs {
		s.documents.put(doc.ID, doc)
	}
	return nil
}

func (s *MediaStore) UpdateStickerSet(ctx context.Context, set domain.StickerSet, docs []domain.Document) error {
	err := withTx(ctx, s.db, "update sticker set", func(tx pgx.Tx) error {
		if err := updateStickerSet(ctx, tx, set); err != nil {
			return err
		}
		qtx := s.q.WithTx(tx)
		for _, doc := range docs {
			params, err := putDocumentParams(doc)
			if err != nil {
				return err
			}
			if err := qtx.PutDocument(ctx, params); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, doc := range docs {
		s.documents.put(doc.ID, doc)
	}
	return nil
}

func (s *MediaStore) DeleteStickerSet(ctx context.Context, setID int64, creatorUserID int64) error {
	return withTx(ctx, s.db, "delete sticker set", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
UPDATE sticker_sets
SET deleted = true, updated_at = now()
WHERE id = $1
  AND creator_user_id = $2
  AND deleted = false`, setID, creatorUserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrStickerSetInvalid
		}
		_, err = tx.Exec(ctx, `DELETE FROM user_sticker_sets WHERE sticker_set_id = $1`, setID)
		return err
	})
}

func insertStickerSet(ctx context.Context, db sqlcgen.DBTX, set domain.StickerSet) error {
	thumbs, err := jsonArrayOrEmpty(set.Thumbs)
	if err != nil {
		return err
	}
	docIDs, err := jsonArrayOrEmpty(set.DocumentIDs)
	if err != nil {
		return err
	}
	packs, err := jsonArrayOrEmpty(set.Packs)
	if err != nil {
		return err
	}
	keywords, err := jsonArrayOrEmpty(set.Keywords)
	if err != nil {
		return err
	}
	kind := string(set.Kind)
	if kind == "" {
		kind = string(domain.StickerSetKindStickers)
	}
	_, err = db.Exec(ctx, `
INSERT INTO sticker_sets (
  id, access_hash, short_name, title, count, hash, set_kind,
  official, animated, videos, emojis, masks, text_color, creator_user_id,
  installed, archived, deleted, installed_date,
  thumb_document_id, thumbs, thumb_dc_id, thumb_version,
  document_ids, packs, keywords, sort_order, system_key, software, updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7,
  $8, $9, $10, $11, $12, $13, $14,
  $15, $16, $17, $18,
  $19, $20::jsonb, $21, $22,
  $23::jsonb, $24::jsonb, $25::jsonb, $26, $27, $28, now()
)`,
		set.ID, set.AccessHash, set.ShortName, set.Title, set.Count, set.Hash, kind,
		set.Official, set.Animated, set.Videos, set.Emojis, set.Masks, set.TextColor, set.CreatorUserID,
		set.Installed, set.Archived, set.Deleted, set.InstalledDate,
		set.ThumbDocumentID, thumbs, set.ThumbDCID, set.ThumbVersion,
		docIDs, packs, keywords, set.SortOrder, set.SystemKey, set.Software,
	)
	return err
}

func updateStickerSet(ctx context.Context, db sqlcgen.DBTX, set domain.StickerSet) error {
	thumbs, err := jsonArrayOrEmpty(set.Thumbs)
	if err != nil {
		return err
	}
	docIDs, err := jsonArrayOrEmpty(set.DocumentIDs)
	if err != nil {
		return err
	}
	packs, err := jsonArrayOrEmpty(set.Packs)
	if err != nil {
		return err
	}
	keywords, err := jsonArrayOrEmpty(set.Keywords)
	if err != nil {
		return err
	}
	kind := string(set.Kind)
	if kind == "" {
		kind = string(domain.StickerSetKindStickers)
	}
	tag, err := db.Exec(ctx, `
UPDATE sticker_sets
SET title = $3,
    count = $4,
    hash = $5,
    set_kind = $6,
    official = $7,
    animated = $8,
    videos = $9,
    emojis = $10,
    masks = $11,
    text_color = $12,
    installed = $13,
    archived = $14,
    installed_date = $15,
    thumb_document_id = $16,
    thumbs = $17::jsonb,
    thumb_dc_id = $18,
    thumb_version = $19,
    document_ids = $20::jsonb,
    packs = $21::jsonb,
    keywords = $22::jsonb,
    sort_order = $23,
    software = $24,
    updated_at = now()
WHERE id = $1
  AND access_hash = $2
  AND deleted = false`,
		set.ID, set.AccessHash,
		set.Title, set.Count, set.Hash, kind,
		set.Official, set.Animated, set.Videos, set.Emojis, set.Masks, set.TextColor,
		set.Installed, set.Archived, set.InstalledDate,
		set.ThumbDocumentID, thumbs, set.ThumbDCID, set.ThumbVersion,
		docIDs, packs, keywords, set.SortOrder, set.Software,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrStickerSetInvalid
	}
	return nil
}

func stickerSetShortNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgerrcode.UniqueViolation {
		return false
	}
	switch pgErr.ConstraintName {
	case "sticker_sets_short_name_lower_idx", "sticker_sets_short_name_idx":
		return true
	default:
		return false
	}
}

func (s *MediaStore) GetStickerSetByID(ctx context.Context, id int64) (domain.StickerSet, bool, error) {
	return queryStickerSet(ctx, s.db, "id = $1", id)
}

func (s *MediaStore) GetStickerSetByShortName(ctx context.Context, shortName string) (domain.StickerSet, bool, error) {
	return queryStickerSet(ctx, s.db, "lower(short_name) = lower($1)", shortName)
}

func (s *MediaStore) GetStickerSetBySystemKey(ctx context.Context, systemKey string) (domain.StickerSet, bool, error) {
	return queryStickerSet(ctx, s.db, "system_key = $1", systemKey)
}

func (s *MediaStore) ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	rows, err := s.db.Query(ctx, stickerSetSelectSQL+`
WHERE set_kind = $1 AND deleted = false
ORDER BY sort_order ASC, id ASC`, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.StickerSet{}
	for rows.Next() {
		set, err := scanStickerSet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, set)
	}
	return out, rows.Err()
}

func (s *MediaStore) ListStickerSetsByCreator(ctx context.Context, creatorUserID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error) {
	if limit <= 0 {
		limit = domain.MaxCreatedStickerSets
	}
	if limit > domain.MaxCreatedStickerSets {
		limit = domain.MaxCreatedStickerSets
	}
	var total int
	if err := s.db.QueryRow(ctx, `
SELECT count(*)::int
FROM sticker_sets
WHERE creator_user_id = $1 AND deleted = false`, creatorUserID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, stickerSetSelectSQL+`
WHERE creator_user_id = $1
  AND deleted = false
  AND ($2::bigint = 0 OR id < $2::bigint)
ORDER BY id DESC
LIMIT $3`, creatorUserID, offsetID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.StickerSet{}
	for rows.Next() {
		set, err := scanStickerSet(rows)
		if err != nil {
			return nil, 0, err
		}
		set.Creator = true
		out = append(out, set)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *MediaStore) StickerSetShortNameAvailable(ctx context.Context, shortName string) (bool, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM sticker_sets
  WHERE lower(short_name) = lower($1)
    AND short_name <> ''
    AND deleted = false
)`, shortName).Scan(&exists); err != nil {
		return false, err
	}
	return !exists, nil
}

const stickerSetSelectSQL = `
SELECT
  id, access_hash, short_name, title, count, hash, set_kind,
  official, animated, videos, emojis, masks, installed, archived, installed_date,
  thumb_document_id, thumbs::text AS thumbs_json, thumb_dc_id, thumb_version,
  document_ids::text AS document_ids_json, packs::text AS packs_json, sort_order, system_key,
  creator_user_id, text_color, deleted, software, keywords::text AS keywords_json
FROM sticker_sets
`

type stickerSetScanner interface {
	Scan(dest ...any) error
}

func queryStickerSet(ctx context.Context, db sqlcgen.DBTX, predicate string, args ...any) (domain.StickerSet, bool, error) {
	row := db.QueryRow(ctx, stickerSetSelectSQL+"WHERE "+predicate+" AND deleted = false LIMIT 1", args...)
	set, err := scanStickerSet(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StickerSet{}, false, nil
		}
		return domain.StickerSet{}, false, err
	}
	return set, true, nil
}

func scanStickerSet(row stickerSetScanner) (domain.StickerSet, error) {
	var (
		id              int64
		accessHash      int64
		shortName       string
		title           string
		count           int32
		hash            int32
		kind            string
		official        bool
		animated        bool
		videos          bool
		emojis          bool
		masks           bool
		installed       bool
		archived        bool
		installedDate   int32
		thumbDocumentID int64
		thumbsJSON      string
		thumbDCID       int32
		thumbVersion    int32
		docIDsJSON      string
		packsJSON       string
		sortOrder       int32
		systemKey       string
		creatorUserID   int64
		textColor       bool
		deleted         bool
		software        string
		keywordsJSON    string
	)
	if err := row.Scan(
		&id, &accessHash, &shortName, &title, &count, &hash, &kind,
		&official, &animated, &videos, &emojis, &masks, &installed, &archived, &installedDate,
		&thumbDocumentID, &thumbsJSON, &thumbDCID, &thumbVersion,
		&docIDsJSON, &packsJSON, &sortOrder, &systemKey,
		&creatorUserID, &textColor, &deleted, &software, &keywordsJSON,
	); err != nil {
		return domain.StickerSet{}, err
	}
	thumbs, err := decodePhotoSizes(thumbsJSON)
	if err != nil {
		return domain.StickerSet{}, err
	}
	docIDs, err := decodeInt64Slice(docIDsJSON)
	if err != nil {
		return domain.StickerSet{}, err
	}
	packs, err := decodeStickerPacks(packsJSON)
	if err != nil {
		return domain.StickerSet{}, err
	}
	keywords, err := decodeStickerKeywords(keywordsJSON)
	if err != nil {
		return domain.StickerSet{}, err
	}
	return domain.StickerSet{
		ID:              id,
		AccessHash:      accessHash,
		ShortName:       shortName,
		Title:           title,
		Count:           int(count),
		Hash:            int(hash),
		Kind:            domain.StickerSetKind(kind),
		Official:        official,
		Animated:        animated,
		Videos:          videos,
		Emojis:          emojis,
		Masks:           masks,
		TextColor:       textColor,
		CreatorUserID:   creatorUserID,
		Installed:       installed,
		Archived:        archived,
		Deleted:         deleted,
		InstalledDate:   int(installedDate),
		ThumbDocumentID: thumbDocumentID,
		Thumbs:          thumbs,
		ThumbDCID:       int(thumbDCID),
		ThumbVersion:    int(thumbVersion),
		DocumentIDs:     docIDs,
		Packs:           packs,
		Keywords:        keywords,
		SortOrder:       int(sortOrder),
		SystemKey:       systemKey,
		Software:        software,
	}, nil
}

func (s *MediaStore) CountStickerSets(ctx context.Context) (int, error) {
	n, err := s.q.CountStickerSets(ctx)
	return int(n), err
}

func stickerSetFromRow(row sqlcgen.GetStickerSetByIDRow) (domain.StickerSet, bool, error) {
	thumbs, err := decodePhotoSizes(row.ThumbsJson)
	if err != nil {
		return domain.StickerSet{}, false, err
	}
	docIDs, err := decodeInt64Slice(row.DocumentIdsJson)
	if err != nil {
		return domain.StickerSet{}, false, err
	}
	packs, err := decodeStickerPacks(row.PacksJson)
	if err != nil {
		return domain.StickerSet{}, false, err
	}
	return domain.StickerSet{
		ID:              row.ID,
		AccessHash:      row.AccessHash,
		ShortName:       row.ShortName,
		Title:           row.Title,
		Count:           int(row.Count),
		Hash:            int(row.Hash),
		Kind:            domain.StickerSetKind(row.SetKind),
		Official:        row.Official,
		Animated:        row.Animated,
		Videos:          row.Videos,
		Emojis:          row.Emojis,
		Masks:           row.Masks,
		Installed:       row.Installed,
		Archived:        row.Archived,
		InstalledDate:   int(row.InstalledDate),
		ThumbDocumentID: row.ThumbDocumentID,
		Thumbs:          thumbs,
		ThumbDCID:       int(row.ThumbDcID),
		ThumbVersion:    int(row.ThumbVersion),
		DocumentIDs:     docIDs,
		Packs:           packs,
		SortOrder:       int(row.SortOrder),
		SystemKey:       row.SystemKey,
	}, true, nil
}

// ---- 可用 reaction ----

func (s *MediaStore) PutAvailableReaction(ctx context.Context, r domain.AvailableReaction) error {
	return s.q.PutAvailableReaction(ctx, sqlcgen.PutAvailableReactionParams{
		Reaction:            r.Reaction,
		Title:               r.Title,
		Inactive:            r.Inactive,
		Premium:             r.Premium,
		StaticIconID:        r.StaticIconID,
		AppearAnimationID:   r.AppearAnimationID,
		SelectAnimationID:   r.SelectAnimationID,
		ActivateAnimationID: r.ActivateAnimationID,
		EffectAnimationID:   r.EffectAnimationID,
		AroundAnimationID:   r.AroundAnimationID,
		CenterIconID:        r.CenterIconID,
		SortOrder:           int32(r.Order),
	})
}

func (s *MediaStore) ListAvailableReactions(ctx context.Context) ([]domain.AvailableReaction, error) {
	rows, err := s.q.ListAvailableReactions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AvailableReaction, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.AvailableReaction{
			Reaction:            r.Reaction,
			Title:               r.Title,
			Inactive:            r.Inactive,
			Premium:             r.Premium,
			StaticIconID:        r.StaticIconID,
			AppearAnimationID:   r.AppearAnimationID,
			SelectAnimationID:   r.SelectAnimationID,
			ActivateAnimationID: r.ActivateAnimationID,
			EffectAnimationID:   r.EffectAnimationID,
			AroundAnimationID:   r.AroundAnimationID,
			CenterIconID:        r.CenterIconID,
			Order:               int(r.SortOrder),
		})
	}
	return out, nil
}

func (s *MediaStore) CountAvailableReactions(ctx context.Context) (int, error) {
	n, err := s.q.CountAvailableReactions(ctx)
	return int(n), err
}

// ---- 头像历史 ----

func (s *MediaStore) AddProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoID int64, date int) error {
	kind = normalizeProfilePhotoKind(kind)
	next, err := s.nextProfilePhotoOrder(ctx, ownerType, ownerID, kind)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
INSERT INTO profile_photos (owner_peer_type, owner_peer_id, kind, photo_id, date, active, sort_order)
VALUES ($1, $2, $3, $4, $5, true, $6)
ON CONFLICT (owner_peer_type, owner_peer_id, kind, photo_id) DO UPDATE SET
  date = EXCLUDED.date,
  active = true,
  sort_order = EXCLUDED.sort_order
`, string(ownerType), ownerID, string(kind), photoID, date, next+1)
	return err
}

func (s *MediaStore) CurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (int64, bool, error) {
	kind = normalizeProfilePhotoKind(kind)
	row := s.db.QueryRow(ctx, `
SELECT photo_id
FROM profile_photos
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND kind = $3
  AND active
ORDER BY sort_order DESC
LIMIT 1
`, string(ownerType), ownerID, string(kind))
	var id int64
	err := row.Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return id, true, nil
}

func (s *MediaStore) CurrentProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	return s.CurrentProfilePhotosKind(ctx, ownerType, ownerIDs, domain.ProfilePhotoKindProfile)
}

func (s *MediaStore) CurrentProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error) {
	if len(ownerIDs) == 0 {
		return map[int64]domain.ProfilePhotoRef{}, nil
	}
	kind = normalizeProfilePhotoKind(kind)
	rows, err := s.db.Query(ctx, `
SELECT DISTINCT ON (pp.owner_peer_id)
  pp.owner_peer_id,
  pp.photo_id,
  ph.dc_id,
  ph.sizes::text AS sizes_json
FROM profile_photos pp
JOIN photos ph ON ph.id = pp.photo_id
WHERE pp.owner_peer_type = $1
  AND pp.owner_peer_id = ANY($2::bigint[])
  AND pp.kind = $3
  AND pp.active
ORDER BY pp.owner_peer_id, pp.sort_order DESC
`, string(ownerType), ownerIDs, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]domain.ProfilePhotoRef, len(ownerIDs))
	for rows.Next() {
		var ownerID, photoID int64
		var dcID int32
		var sizesJSON string
		if err := rows.Scan(&ownerID, &photoID, &dcID, &sizesJSON); err != nil {
			return nil, err
		}
		sizes, err := decodePhotoSizes(sizesJSON)
		if err != nil {
			return nil, err
		}
		out[ownerID] = domain.ProfilePhotoRef{
			PhotoID:  photoID,
			DCID:     int(dcID),
			Stripped: domain.StrippedFromSizes(sizes),
			HasVideo: domain.PhotoHasVideo(sizes),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MediaStore) ListProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) ([]int64, int, error) {
	kind = normalizeProfilePhotoKind(kind)
	rows, err := s.db.Query(ctx, `
SELECT photo_id
FROM profile_photos
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND kind = $3
  AND active
  AND ($4::bigint <= 0 OR photo_id < $4::bigint)
ORDER BY sort_order DESC
OFFSET $5
LIMIT $6
`, string(ownerType), ownerID, string(kind), maxID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	err = s.db.QueryRow(ctx, `
SELECT count(*)::int
FROM profile_photos
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND kind = $3
  AND active
`, string(ownerType), ownerID, string(kind)).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	return ids, total, nil
}

func (s *MediaStore) ListProfilePhotoDetailsKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) ([]domain.Photo, int, error) {
	kind = normalizeProfilePhotoKind(kind)
	if limit <= 0 {
		return nil, 0, nil
	}
	var rows pgx.Rows
	var err error
	if offset < 0 && maxID > 0 {
		rows, err = s.db.Query(ctx, `
SELECT ph.id, ph.access_hash, ph.file_reference, ph.date, ph.dc_id, ph.has_stickers, ph.sizes::text AS sizes_json
FROM profile_photos pp
JOIN photos ph ON ph.id = pp.photo_id
WHERE pp.owner_peer_type = $1
  AND pp.owner_peer_id = $2
  AND pp.kind = $3
  AND pp.active
  AND pp.photo_id = $4
ORDER BY pp.sort_order DESC
LIMIT $5
`, string(ownerType), ownerID, string(kind), maxID, limit)
	} else {
		if offset < 0 {
			offset = 0
		}
		rows, err = s.db.Query(ctx, `
SELECT ph.id, ph.access_hash, ph.file_reference, ph.date, ph.dc_id, ph.has_stickers, ph.sizes::text AS sizes_json
FROM profile_photos pp
JOIN photos ph ON ph.id = pp.photo_id
WHERE pp.owner_peer_type = $1
  AND pp.owner_peer_id = $2
  AND pp.kind = $3
  AND pp.active
  AND ($4::bigint <= 0 OR pp.photo_id < $4::bigint)
ORDER BY pp.sort_order DESC
OFFSET $5
LIMIT $6
`, string(ownerType), ownerID, string(kind), maxID, offset, limit)
	}
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	photos := make([]domain.Photo, 0, limit)
	for rows.Next() {
		photo, err := scanPhotoRow(rows)
		if err != nil {
			return nil, 0, err
		}
		photos = append(photos, photo)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	err = s.db.QueryRow(ctx, `
SELECT count(*)::int
FROM profile_photos
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND kind = $3
  AND active
`, string(ownerType), ownerID, string(kind)).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	return photos, total, nil
}

func (s *MediaStore) DeleteProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, photoIDs []int64) ([]int64, error) {
	return s.DeleteProfilePhotosKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile, photoIDs)
}

func (s *MediaStore) DeleteProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoIDs []int64) ([]int64, error) {
	if len(photoIDs) == 0 {
		return nil, nil
	}
	kind = normalizeProfilePhotoKind(kind)
	rows, err := s.db.Query(ctx, `
UPDATE profile_photos
SET active = false
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND kind = $3
  AND photo_id = ANY($4::bigint[])
  AND active
RETURNING photo_id
`, string(ownerType), ownerID, string(kind), photoIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deleted := make([]int64, 0, len(photoIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		deleted = append(deleted, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return deleted, nil
}

func (s *MediaStore) nextProfilePhotoOrder(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (int64, error) {
	var maxOrder int64
	err := s.db.QueryRow(ctx, `
SELECT COALESCE(MAX(sort_order), 0)::bigint
FROM profile_photos
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND kind = $3
`, string(ownerType), ownerID, string(kind)).Scan(&maxOrder)
	return maxOrder, err
}

func normalizeProfilePhotoKind(kind domain.ProfilePhotoKind) domain.ProfilePhotoKind {
	if kind == domain.ProfilePhotoKindFallback {
		return kind
	}
	return domain.ProfilePhotoKindProfile
}
