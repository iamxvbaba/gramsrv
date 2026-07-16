package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// StarGiftStore 用 PostgreSQL 实现 store.StarGiftStore（peer 收到的 Star 礼物实例）。
type StarGiftStore struct {
	db sqlcgen.DBTX
}

// NewStarGiftStore 基于 pgx 连接池（或事务）创建 StarGiftStore。
func NewStarGiftStore(db sqlcgen.DBTX) *StarGiftStore {
	return &StarGiftStore{db: db}
}

const starGiftCatalogSelect = `
SELECT c.gift_id, r.id, r.stars, r.convert_stars, r.title,
       COALESCE(cr.upgrade_stars, 0), COALESCE(cr.supply_total, 0), COALESCE(cr.issued, 0),
       d.id, d.access_hash, d.file_reference, d.date, d.mime_type, d.size, d.dc_id,
       d.attributes::text, d.thumbs::text
FROM star_gift_catalog c
JOIN star_gift_catalog_revisions r ON r.id = c.active_revision_id
LEFT JOIN star_gift_collectible_revisions cr ON cr.id = c.collectible_revision_id AND cr.status = 'published'
JOIN documents d ON d.id = r.document_id`

func (s *StarGiftStore) Catalog(ctx context.Context) ([]domain.StarGift, error) {
	rows, err := s.db.Query(ctx, starGiftCatalogSelect+`
WHERE c.enabled
ORDER BY c.sort_order, c.gift_id`)
	if err != nil {
		return nil, fmt.Errorf("list star gift catalog: %w", err)
	}
	defer rows.Close()
	out := make([]domain.StarGift, 0)
	for rows.Next() {
		gift, err := scanCatalogGift(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, gift)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate star gift catalog: %w", err)
	}
	return out, nil
}

func (s *StarGiftStore) CatalogGift(ctx context.Context, giftID int64) (domain.StarGift, bool, error) {
	if giftID <= 0 {
		return domain.StarGift{}, false, nil
	}
	gift, err := scanCatalogGift(s.db.QueryRow(ctx, starGiftCatalogSelect+`
WHERE c.enabled AND c.gift_id = $1`, giftID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGift{}, false, nil
	}
	if err != nil {
		return domain.StarGift{}, false, err
	}
	return gift, true, nil
}

func (s *StarGiftStore) CatalogRevision(ctx context.Context, revisionID int64) (domain.StarGift, bool, error) {
	if revisionID <= 0 {
		return domain.StarGift{}, false, nil
	}
	gift, err := scanCatalogGift(s.db.QueryRow(ctx, `
SELECT r.gift_id, r.id, r.stars, r.convert_stars, r.title,
       COALESCE(cr.upgrade_stars, 0), COALESCE(cr.supply_total, 0), COALESCE(cr.issued, 0),
       d.id, d.access_hash, d.file_reference, d.date, d.mime_type, d.size, d.dc_id,
       d.attributes::text, d.thumbs::text
FROM star_gift_catalog_revisions r
JOIN star_gift_catalog c ON c.gift_id = r.gift_id
LEFT JOIN star_gift_collectible_revisions cr ON cr.id = c.collectible_revision_id AND cr.status = 'published'
JOIN documents d ON d.id = r.document_id
WHERE r.id = $1`, revisionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGift{}, false, nil
	}
	if err != nil {
		return domain.StarGift{}, false, err
	}
	return gift, true, nil
}

func scanCatalogGift(row rowScanner) (domain.StarGift, error) {
	var gift domain.StarGift
	var attrsJSON, thumbsJSON string
	if err := row.Scan(
		&gift.ID, &gift.RevisionID, &gift.Stars, &gift.ConvertStars, &gift.Title,
		&gift.UpgradeStars, &gift.UpgradeTotal, &gift.UpgradeIssued,
		&gift.Sticker.ID, &gift.Sticker.AccessHash, &gift.Sticker.FileReference, &gift.Sticker.Date,
		&gift.Sticker.MimeType, &gift.Sticker.Size, &gift.Sticker.DCID, &attrsJSON, &thumbsJSON,
	); err != nil {
		return domain.StarGift{}, err
	}
	attrs, err := decodeDocumentAttributes(attrsJSON)
	if err != nil {
		return domain.StarGift{}, fmt.Errorf("decode star gift document attributes: %w", err)
	}
	thumbs, err := decodePhotoSizes(thumbsJSON)
	if err != nil {
		return domain.StarGift{}, fmt.Errorf("decode star gift document thumbs: %w", err)
	}
	gift.Sticker.Attributes = attrs
	gift.Sticker.Thumbs = thumbs
	if !gift.Sticker.IsSticker() || gift.Sticker.MimeType != "application/x-tgsticker" {
		return domain.StarGift{}, fmt.Errorf("invalid star gift revision %d document %d", gift.RevisionID, gift.Sticker.ID)
	}
	return gift, nil
}

func (s *StarGiftStore) CreateCatalogRevision(ctx context.Context, write domain.StarGiftCatalogWrite) (domain.StarGiftCatalogEntry, error) {
	if write.Stars <= 0 || write.ConvertStars < 0 || write.ConvertStars > write.Stars ||
		write.Document.ID <= 0 || !write.Document.IsSticker() || write.Document.MimeType != "application/x-tgsticker" ||
		len(write.Animation.JSON) == 0 || len(write.Animation.SHA256) != 32 {
		return domain.StarGiftCatalogEntry{}, domain.ErrStarGiftInvalid
	}
	var entry domain.StarGiftCatalogEntry
	err := withTx(ctx, s.db, "create star gift catalog revision", func(tx pgx.Tx) error {
		giftID := write.GiftID
		var revisionID int64
		if err := tx.QueryRow(ctx, `SELECT nextval('star_gift_catalog_revision_id_seq')`).Scan(&revisionID); err != nil {
			return fmt.Errorf("allocate star gift revision id: %w", err)
		}
		revision := 1
		if giftID == 0 {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('star_gift_catalog', 0))`); err != nil {
				return fmt.Errorf("lock star gift catalog capacity: %w", err)
			}
			var catalogCount int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_catalog`).Scan(&catalogCount); err != nil {
				return fmt.Errorf("count star gift catalog: %w", err)
			}
			if catalogCount >= domain.MaxStarGiftCatalogSize {
				return domain.ErrStarGiftCatalogFull
			}
			if err := tx.QueryRow(ctx, `SELECT nextval('star_gift_catalog_gift_id_seq')`).Scan(&giftID); err != nil {
				return fmt.Errorf("allocate star gift id: %w", err)
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO star_gift_catalog (gift_id, active_revision_id, enabled, sort_order)
VALUES ($1,$2,$3,$4)`, giftID, revisionID, write.Enabled, write.SortOrder); err != nil {
				return fmt.Errorf("insert star gift catalog: %w", err)
			}
		} else {
			var ignored int64
			if err := tx.QueryRow(ctx, `
SELECT active_revision_id FROM star_gift_catalog WHERE gift_id=$1 FOR UPDATE`, giftID).Scan(&ignored); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return domain.ErrStarGiftNotFound
				}
				return fmt.Errorf("lock star gift catalog: %w", err)
			}
			if err := tx.QueryRow(ctx, `
SELECT COALESCE(MAX(revision), 0) + 1
FROM star_gift_catalog_revisions
WHERE gift_id = $1`, giftID).Scan(&revision); err != nil {
				return fmt.Errorf("lock star gift catalog: %w", err)
			}
		}

		media := NewMediaStore(tx)
		if err := media.PutDocument(ctx, write.Document); err != nil {
			return fmt.Errorf("put star gift document: %w", err)
		}
		if err := media.PutFileBlob(ctx, write.Blob); err != nil {
			return fmt.Errorf("put star gift blob: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO star_gift_catalog_revisions (
    id, gift_id, revision, title, stars, convert_stars, document_id,
    animation_json, animation_sha256, source_name, source_format,
    width, height, frame_rate, in_point, out_point, created_by, command_id
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			revisionID, giftID, revision, write.Title, write.Stars, write.ConvertStars, write.Document.ID,
			string(write.Animation.JSON), write.Animation.SHA256, write.Animation.SourceName, string(write.Animation.SourceFormat),
			write.Animation.Width, write.Animation.Height, write.Animation.FrameRate, write.Animation.InPoint, write.Animation.OutPoint,
			write.Actor, write.CommandID,
		); err != nil {
			return fmt.Errorf("insert star gift revision: %w", err)
		}
		if write.GiftID != 0 {
			if _, err := tx.Exec(ctx, `
UPDATE star_gift_catalog
SET active_revision_id=$2, enabled=$3, sort_order=$4, updated_at=now()
WHERE gift_id=$1`, giftID, revisionID, write.Enabled, write.SortOrder); err != nil {
				return fmt.Errorf("activate star gift revision: %w", err)
			}
		}
		write.GiftID = giftID
		var err error
		entry, err = catalogEntryByID(ctx, tx, giftID)
		return err
	})
	if err != nil {
		return domain.StarGiftCatalogEntry{}, err
	}
	return entry, nil
}

func (s *StarGiftStore) SetCatalogEnabled(ctx context.Context, giftID int64, enabled bool) (bool, error) {
	tag, err := s.db.Exec(ctx, `
UPDATE star_gift_catalog SET enabled=$2, updated_at=now()
WHERE gift_id=$1 AND enabled IS DISTINCT FROM $2`, giftID, enabled)
	if err != nil {
		return false, fmt.Errorf("set star gift enabled: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM star_gift_catalog WHERE gift_id=$1)`, giftID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check star gift enabled target: %w", err)
	}
	if !exists {
		return false, domain.ErrStarGiftNotFound
	}
	return false, nil
}

func (s *StarGiftStore) SetCatalogSortOrder(ctx context.Context, giftID int64, sortOrder int) (bool, error) {
	tag, err := s.db.Exec(ctx, `
UPDATE star_gift_catalog SET sort_order=$2, updated_at=now()
WHERE gift_id=$1 AND sort_order IS DISTINCT FROM $2`, giftID, sortOrder)
	if err != nil {
		return false, fmt.Errorf("set star gift sort order: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM star_gift_catalog WHERE gift_id=$1)`, giftID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check star gift sort target: %w", err)
	}
	if !exists {
		return false, domain.ErrStarGiftNotFound
	}
	return false, nil
}

func (s *StarGiftStore) AnimationJSON(ctx context.Context, giftID int64) ([]byte, bool, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `
SELECT r.animation_json::text
FROM star_gift_catalog c
JOIN star_gift_catalog_revisions r ON r.id = c.active_revision_id
WHERE c.gift_id=$1`, giftID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get star gift animation: %w", err)
	}
	return raw, true, nil
}

func catalogEntryByID(ctx context.Context, db sqlcgen.DBTX, giftID int64) (domain.StarGiftCatalogEntry, error) {
	row := db.QueryRow(ctx, `
SELECT c.gift_id, r.id, r.stars, r.convert_stars, r.title,
       COALESCE(cr.upgrade_stars, 0), COALESCE(cr.supply_total, 0), COALESCE(cr.issued, 0),
       d.id, d.access_hash, d.file_reference, d.date, d.mime_type, d.size, d.dc_id,
       d.attributes::text, d.thumbs::text,
       c.enabled, c.sort_order, r.revision, r.source_name, r.source_format,
       r.animation_sha256, r.width, r.height, r.frame_rate, r.created_by, c.updated_at,
       (SELECT COUNT(*) FROM peer_star_gifts p WHERE p.gift_id=c.gift_id)
FROM star_gift_catalog c
JOIN star_gift_catalog_revisions r ON r.id=c.active_revision_id
LEFT JOIN star_gift_collectible_revisions cr ON cr.id=c.collectible_revision_id AND cr.status='published'
JOIN documents d ON d.id=r.document_id
WHERE c.gift_id=$1`, giftID)
	var entry domain.StarGiftCatalogEntry
	var attrsJSON, thumbsJSON, sourceFormat string
	if err := row.Scan(
		&entry.Gift.ID, &entry.Gift.RevisionID, &entry.Gift.Stars, &entry.Gift.ConvertStars, &entry.Gift.Title,
		&entry.Gift.UpgradeStars, &entry.Gift.UpgradeTotal, &entry.Gift.UpgradeIssued,
		&entry.Gift.Sticker.ID, &entry.Gift.Sticker.AccessHash, &entry.Gift.Sticker.FileReference, &entry.Gift.Sticker.Date,
		&entry.Gift.Sticker.MimeType, &entry.Gift.Sticker.Size, &entry.Gift.Sticker.DCID, &attrsJSON, &thumbsJSON,
		&entry.Enabled, &entry.SortOrder, &entry.Revision, &entry.SourceName, &sourceFormat,
		&entry.AnimationSHA, &entry.Width, &entry.Height, &entry.FrameRate, &entry.CreatedBy, &entry.UpdatedAt,
		&entry.ReceivedCount,
	); err != nil {
		return domain.StarGiftCatalogEntry{}, err
	}
	attrs, err := decodeDocumentAttributes(attrsJSON)
	if err != nil {
		return domain.StarGiftCatalogEntry{}, err
	}
	thumbs, err := decodePhotoSizes(thumbsJSON)
	if err != nil {
		return domain.StarGiftCatalogEntry{}, err
	}
	entry.Gift.Sticker.Attributes = attrs
	entry.Gift.Sticker.Thumbs = thumbs
	entry.SourceFormat = domain.StarGiftAnimationFormat(sourceFormat)
	entry.AnimationSize = entry.Gift.Sticker.Size
	return entry, nil
}

func (s *StarGiftStore) Create(ctx context.Context, gift domain.SavedStarGift) (int64, error) {
	if !validSavedStarGift(gift) {
		return 0, domain.ErrStarGiftInvalid
	}
	var id int64
	err := s.db.QueryRow(ctx, `
WITH next_id AS (
    SELECT nextval(pg_get_serial_sequence('public.peer_star_gifts', 'id'))::bigint AS id
)
INSERT INTO peer_star_gifts (id, owner_peer_type, owner_peer_id, from_user_id, gift_id, catalog_revision_id, msg_id, saved_id, gift_date, name_hidden, unsaved, converted, convert_stars, prepaid_upgrade_stars, message)
SELECT next_id.id, $1,$2,$3,$4,$5,$6,
       CASE WHEN $1 = 'channel' AND $7::bigint = 0 THEN next_id.id ELSE $7::bigint END,
       $8,$9,$10,false,$11,$12,$13
FROM next_id
RETURNING id`,
		string(gift.Owner.Type), gift.Owner.ID, gift.FromUserID, gift.GiftID, gift.RevisionID, gift.MsgID, gift.SavedID, gift.Date,
		gift.NameHidden, gift.Unsaved, gift.ConvertStars, gift.PrepaidUpgradeStars, gift.Message).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create star gift: %w", err)
	}
	return id, nil
}

func (s *StarGiftStore) ListByOwner(ctx context.Context, owner domain.Peer, excludeUnsaved bool, offset string, limit int) (domain.SavedStarGiftPage, error) {
	return s.ListByOwnerFiltered(ctx, domain.SavedStarGiftFilter{
		Owner: owner, ExcludeUnsaved: excludeUnsaved, Offset: offset, Limit: limit,
	})
}

func (s *StarGiftStore) ListByOwnerFiltered(ctx context.Context, filter domain.SavedStarGiftFilter) (domain.SavedStarGiftPage, error) {
	owner, offset, limit := filter.Owner, filter.Offset, filter.Limit
	if !validStarGiftOwner(owner) {
		return domain.SavedStarGiftPage{}, nil
	}
	if limit <= 0 || limit > domain.MaxSavedStarGiftsLimit {
		limit = domain.MaxSavedStarGiftsLimit
	}
	joins := `
JOIN star_gift_catalog c ON c.gift_id = p.gift_id
LEFT JOIN star_gift_collectible_revisions acr
  ON acr.id = c.collectible_revision_id AND acr.status = 'published'`
	conditions := []string{"p.owner_peer_type = $1", "p.owner_peer_id = $2", "NOT p.converted"}
	args := []any{string(owner.Type), owner.ID}
	if filter.ExcludeUnsaved {
		conditions = append(conditions, "NOT p.unsaved")
	}
	if filter.ExcludeSaved {
		conditions = append(conditions, "p.unsaved")
	}
	if filter.ExcludeUnique {
		conditions = append(conditions, "p.unique_gift_id IS NULL")
	}
	// telesrv ordinary catalog gifts are currently unlimited. Unique gifts are
	// collectibles and therefore survive exclude_unlimited.
	if filter.ExcludeUnlimited {
		conditions = append(conditions, "p.unique_gift_id IS NOT NULL")
	}
	upgradable := `(p.unique_gift_id IS NULL AND acr.id IS NOT NULL AND acr.upgrade_stars > 0 AND acr.issued < acr.supply_total)`
	if filter.ExcludeUpgradable {
		conditions = append(conditions, "NOT "+upgradable)
	}
	if filter.ExcludeUnupgradable {
		conditions = append(conditions, upgradable)
	}
	if filter.CollectionID > 0 {
		args = append(args, filter.CollectionID)
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
SELECT 1 FROM star_gift_collection_items ci
JOIN star_gift_collections cc ON cc.collection_id = ci.collection_id
WHERE ci.saved_gift_id = p.id AND ci.collection_id = $%d
  AND cc.owner_peer_type = p.owner_peer_type AND cc.owner_peer_id = p.owner_peer_id)`, len(args)))
	}
	where := strings.Join(conditions, " AND ")
	countQuery := `SELECT COUNT(*) FROM peer_star_gifts p ` + joins + ` WHERE ` + where
	var total int
	if err := s.db.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return domain.SavedStarGiftPage{}, fmt.Errorf("count star gifts: %w", err)
	}
	page := domain.SavedStarGiftPage{Count: total}

	if cursor, ok := domain.DecodeStarGiftCursor(offset); ok {
		args = append(args, cursor)
		where += fmt.Sprintf(" AND p.id < $%d", len(args))
	}
	args = append(args, limit+1)
	limitPlaceholder := len(args)
	rows, err := s.db.Query(ctx, `
SELECT p.id, p.owner_peer_type, p.owner_peer_id, p.from_user_id, p.gift_id, p.catalog_revision_id,
       p.msg_id, p.saved_id, p.gift_date, p.name_hidden, p.unsaved, p.converted, p.convert_stars, p.prepaid_upgrade_stars,
       p.message, COALESCE(p.unique_gift_id, 0), p.upgrade_msg_id, p.pinned_order,
       COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order, i.collection_id)
                 FROM star_gift_collection_items i
                 JOIN star_gift_collections c ON c.collection_id=i.collection_id
                 WHERE i.saved_gift_id=p.id), ARRAY[]::integer[])
FROM peer_star_gifts p `+joins+`
WHERE `+where+`
ORDER BY p.id DESC
LIMIT $`+fmt.Sprint(limitPlaceholder), args...)
	if err != nil {
		return domain.SavedStarGiftPage{}, fmt.Errorf("list star gifts: %w", err)
	}
	defer rows.Close()
	gifts := make([]domain.SavedStarGift, 0, limit)
	for rows.Next() {
		g, err := scanSavedStarGift(rows)
		if err != nil {
			return domain.SavedStarGiftPage{}, err
		}
		gifts = append(gifts, g)
	}
	if err := rows.Err(); err != nil {
		return domain.SavedStarGiftPage{}, fmt.Errorf("iterate star gifts: %w", err)
	}
	if len(gifts) > limit {
		gifts = gifts[:limit]
		page.NextOffset = domain.EncodeStarGiftCursor(gifts[len(gifts)-1].ID)
	}
	page.Gifts = gifts
	return page, nil
}

func (s *StarGiftStore) ResolveSavedIDs(ctx context.Context, owner domain.Peer, refs []domain.SavedStarGiftRef) ([]int64, error) {
	if !validStarGiftOwner(owner) || len(refs) > domain.MaxStarGiftCollectionItems {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	if len(refs) == 0 {
		return []int64{}, nil
	}
	values := make([]int64, 0, len(refs))
	seenValues := make(map[int64]struct{}, len(refs))
	column := "msg_id"
	for _, ref := range refs {
		if ref.Owner != owner || !ref.Valid() {
			return nil, domain.ErrStarGiftNotFound
		}
		value := int64(ref.MsgID)
		if owner.Type == domain.PeerTypeChannel {
			column = "saved_id"
			value = ref.SavedID
		}
		if _, duplicate := seenValues[value]; duplicate {
			return nil, domain.ErrStarGiftCollectibleInvalid
		}
		seenValues[value] = struct{}{}
		values = append(values, value)
	}
	rows, err := s.db.Query(ctx, `SELECT `+column+`::bigint, id FROM peer_star_gifts
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND NOT converted AND `+column+`::bigint=ANY($3::bigint[])`, string(owner.Type), owner.ID, values)
	if err != nil {
		return nil, fmt.Errorf("resolve saved star gifts: %w", err)
	}
	defer rows.Close()
	resolved := make(map[int64]int64, len(values))
	for rows.Next() {
		var value, id int64
		if err := rows.Scan(&value, &id); err != nil {
			return nil, fmt.Errorf("scan resolved saved star gift: %w", err)
		}
		resolved[value] = id
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resolved saved star gifts: %w", err)
	}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		id := resolved[value]
		if id == 0 {
			return nil, domain.ErrStarGiftNotFound
		}
		out = append(out, id)
	}
	return out, nil
}

func (s *StarGiftStore) GetByRef(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error) {
	if !ref.Valid() {
		return domain.SavedStarGift{}, false, nil
	}
	where, args := savedStarGiftRefWhere(ref)
	row := s.db.QueryRow(ctx, `
SELECT p.id, p.owner_peer_type, p.owner_peer_id, p.from_user_id, p.gift_id, p.catalog_revision_id,
       p.msg_id, p.saved_id, p.gift_date, p.name_hidden, p.unsaved, p.converted, p.convert_stars, p.prepaid_upgrade_stars,
       p.message, COALESCE(p.unique_gift_id, 0), p.upgrade_msg_id, p.pinned_order,
       COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order, i.collection_id)
                 FROM star_gift_collection_items i
                 JOIN star_gift_collections c ON c.collection_id=i.collection_id
                 WHERE i.saved_gift_id=p.id), ARRAY[]::integer[])
FROM peer_star_gifts p
WHERE `+where, args...)
	g, err := scanSavedStarGift(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.SavedStarGift{}, false, nil
		}
		return domain.SavedStarGift{}, false, err
	}
	return g, true, nil
}

func (s *StarGiftStore) CountByOwner(ctx context.Context, owner domain.Peer) (int, error) {
	if !validStarGiftOwner(owner) {
		return 0, nil
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM peer_star_gifts WHERE owner_peer_type = $1 AND owner_peer_id = $2 AND NOT converted AND NOT unsaved`, string(owner.Type), owner.ID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count star gifts: %w", err)
	}
	return n, nil
}

func (s *StarGiftStore) SetUnsaved(ctx context.Context, ref domain.SavedStarGiftRef, unsaved bool) (bool, error) {
	if !ref.Valid() {
		return false, domain.ErrStarGiftNotFound
	}
	where, args := savedStarGiftRefWhere(ref)
	args = append(args, unsaved)
	tag, err := s.db.Exec(ctx, `
UPDATE peer_star_gifts SET unsaved = $4
WHERE `+where+` AND NOT converted`, args...)
	if err != nil {
		return false, fmt.Errorf("set star gift unsaved: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *StarGiftStore) MarkConverted(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, error) {
	if !ref.Valid() {
		return domain.SavedStarGift{}, domain.ErrStarGiftNotFound
	}
	out := domain.SavedStarGift{}
	err := withTx(ctx, s.db, "convert star gift", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(ref.Owner)); err != nil {
			return fmt.Errorf("lock star gift owner collections: %w", err)
		}
		where, args := savedStarGiftRefWhere(ref)
		row := tx.QueryRow(ctx, `
SELECT p.id, p.owner_peer_type, p.owner_peer_id, p.from_user_id, p.gift_id, p.catalog_revision_id,
       p.msg_id, p.saved_id, p.gift_date, p.name_hidden, p.unsaved, p.converted, p.convert_stars, p.prepaid_upgrade_stars,
       p.message, COALESCE(p.unique_gift_id, 0), p.upgrade_msg_id, p.pinned_order,
       COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order, i.collection_id)
                 FROM star_gift_collection_items i
                 JOIN star_gift_collections c ON c.collection_id=i.collection_id
                 WHERE i.saved_gift_id=p.id), ARRAY[]::integer[])
FROM peer_star_gifts p
WHERE `+where+` FOR UPDATE`, args...)
		g, err := scanSavedStarGift(row)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftNotFound
			}
			return err
		}
		if g.Converted {
			return domain.ErrStarGiftAlreadyConverted
		}
		if g.UniqueGiftID != 0 {
			return domain.ErrStarGiftAlreadyUpgraded
		}
		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET converted = true, unsaved = true, pinned_order = 0 WHERE id = $1`, g.ID); err != nil {
			return fmt.Errorf("mark star gift converted: %w", err)
		}
		if err := removeSavedGiftFromCollections(ctx, tx, g.Owner, g.ID); err != nil {
			return err
		}
		g.Converted = true
		g.Unsaved = true
		g.PinnedOrder = 0
		g.CollectionIDs = nil
		out = g
		return nil
	})
	if err != nil {
		return domain.SavedStarGift{}, err
	}
	return out, nil
}

func scanSavedStarGift(row rowScanner) (domain.SavedStarGift, error) {
	var g domain.SavedStarGift
	var ownerType string
	if err := row.Scan(&g.ID, &ownerType, &g.Owner.ID, &g.FromUserID, &g.GiftID, &g.RevisionID, &g.MsgID, &g.SavedID, &g.Date,
		&g.NameHidden, &g.Unsaved, &g.Converted, &g.ConvertStars, &g.PrepaidUpgradeStars, &g.Message, &g.UniqueGiftID,
		&g.UpgradeMsgID, &g.PinnedOrder, &g.CollectionIDs); err != nil {
		return domain.SavedStarGift{}, err
	}
	g.Owner.Type = domain.PeerType(ownerType)
	return g, nil
}

func savedStarGiftRefWhere(ref domain.SavedStarGiftRef) (string, []any) {
	args := []any{string(ref.Owner.Type), ref.Owner.ID}
	switch ref.Owner.Type {
	case domain.PeerTypeChannel:
		args = append(args, ref.SavedID)
		return "owner_peer_type = $1 AND owner_peer_id = $2 AND saved_id = $3", args
	default:
		args = append(args, ref.MsgID)
		return "owner_peer_type = $1 AND owner_peer_id = $2 AND msg_id = $3", args
	}
}

func validSavedStarGift(g domain.SavedStarGift) bool {
	if g.GiftID == 0 || g.RevisionID == 0 || !validStarGiftOwner(g.Owner) {
		return false
	}
	switch g.Owner.Type {
	case domain.PeerTypeUser:
		return g.MsgID > 0 && g.SavedID == 0
	case domain.PeerTypeChannel:
		return g.MsgID == 0 && g.SavedID >= 0
	default:
		return false
	}
}

func validStarGiftOwner(owner domain.Peer) bool {
	return owner.ID != 0 && (owner.Type == domain.PeerTypeUser || owner.Type == domain.PeerTypeChannel)
}
