package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *StarGiftStore) PublishCollectibleRevision(ctx context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error) {
	write.SlugPrefix = strings.ToLower(strings.TrimSpace(write.SlugPrefix))
	write.Actor = strings.TrimSpace(write.Actor)
	write.CommandID = strings.TrimSpace(write.CommandID)
	if err := domain.ValidateStarGiftCollectibleWrite(write); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	var result domain.StarGiftCollectibleRevision
	err := withTx(ctx, s.db, "publish collectible star gift revision", func(tx pgx.Tx) error {
		var ignored int64
		if err := tx.QueryRow(ctx, `SELECT gift_id FROM star_gift_catalog WHERE gift_id=$1 FOR UPDATE`, write.GiftID).Scan(&ignored); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftNotFound
			}
			return fmt.Errorf("lock collectible catalog gift: %w", err)
		}
		var revision int
		if err := tx.QueryRow(ctx, `
SELECT COALESCE(MAX(revision), 0) + 1 FROM star_gift_collectible_revisions WHERE gift_id=$1`, write.GiftID).Scan(&revision); err != nil {
			return fmt.Errorf("allocate collectible revision: %w", err)
		}
		var revisionID int64
		if err := tx.QueryRow(ctx, `
INSERT INTO star_gift_collectible_revisions
    (gift_id, revision, upgrade_stars, supply_total, slug_prefix, status, created_by, command_id)
VALUES ($1,$2,$3,$4,$5,'draft',$6,$7)
RETURNING id`, write.GiftID, revision, write.UpgradeStars, write.SupplyTotal, write.SlugPrefix, write.Actor, write.CommandID).Scan(&revisionID); err != nil {
			return fmt.Errorf("insert collectible revision: %w", err)
		}
		media := NewMediaStore(tx)
		insertAnimated := func(table string, attributes []domain.StarGiftCollectibleAttribute) error {
			for _, attribute := range attributes {
				if err := media.PutDocument(ctx, *attribute.Document); err != nil {
					return fmt.Errorf("put collectible %s document: %w", attribute.Kind, err)
				}
				if err := media.PutFileBlob(ctx, *attribute.Blob); err != nil {
					return fmt.Errorf("put collectible %s blob: %w", attribute.Kind, err)
				}
				animation := attribute.Animation
				query := fmt.Sprintf(`
INSERT INTO %s
    (collectible_revision_id, name, document_id, animation_json, animation_sha256,
     source_name, source_format, width, height, frame_rate, in_point, out_point,
     rarity_permille, sort_order)
VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, table)
				if _, err := tx.Exec(ctx, query, revisionID, strings.TrimSpace(attribute.Name), attribute.Document.ID,
					string(animation.JSON), animation.SHA256, animation.SourceName, string(animation.SourceFormat),
					animation.Width, animation.Height, animation.FrameRate, animation.InPoint, animation.OutPoint,
					attribute.RarityPermille, attribute.SortOrder); err != nil {
					return fmt.Errorf("insert collectible %s attribute: %w", attribute.Kind, err)
				}
			}
			return nil
		}
		if err := insertAnimated("star_gift_collectible_models", write.Models); err != nil {
			return err
		}
		if err := insertAnimated("star_gift_collectible_patterns", write.Patterns); err != nil {
			return err
		}
		for _, attribute := range write.Backdrops {
			if _, err := tx.Exec(ctx, `
INSERT INTO star_gift_collectible_backdrops
    (collectible_revision_id, name, backdrop_id, center_color, edge_color, pattern_color,
     text_color, rarity_permille, sort_order)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, revisionID, strings.TrimSpace(attribute.Name), attribute.BackdropID,
				attribute.CenterColor, attribute.EdgeColor, attribute.PatternColor, attribute.TextColor,
				attribute.RarityPermille, attribute.SortOrder); err != nil {
				return fmt.Errorf("insert collectible backdrop: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
UPDATE star_gift_collectible_revisions SET status='published', published_at=now() WHERE id=$1`, revisionID); err != nil {
			return fmt.Errorf("publish collectible revision: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE star_gift_catalog SET collectible_revision_id=$2, updated_at=now() WHERE gift_id=$1`, write.GiftID, revisionID); err != nil {
			return fmt.Errorf("activate collectible revision: %w", err)
		}
		var err error
		result, err = collectibleRevisionByID(ctx, tx, revisionID)
		return err
	})
	return result, err
}

func (s *StarGiftStore) ActiveCollectibleRevision(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, bool, error) {
	var revisionID int64
	err := s.db.QueryRow(ctx, `
SELECT collectible_revision_id FROM star_gift_catalog
WHERE gift_id=$1 AND collectible_revision_id IS NOT NULL`, giftID).Scan(&revisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftCollectibleRevision{}, false, nil
	}
	if err != nil {
		return domain.StarGiftCollectibleRevision{}, false, fmt.Errorf("get active collectible revision: %w", err)
	}
	revision, err := collectibleRevisionByID(ctx, s.db, revisionID)
	if err != nil {
		return domain.StarGiftCollectibleRevision{}, false, err
	}
	return revision, true, nil
}

func (s *StarGiftStore) CollectibleAvailability(ctx context.Context, giftIDs []int64) (map[int64]domain.StarGiftCollectibleAvailability, error) {
	out := make(map[int64]domain.StarGiftCollectibleAvailability, len(giftIDs))
	if len(giftIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT c.gift_id, r.upgrade_stars, r.supply_total, r.issued
FROM star_gift_catalog c
JOIN star_gift_collectible_revisions r ON r.id=c.collectible_revision_id
WHERE c.gift_id=ANY($1) AND r.status='published'`, giftIDs)
	if err != nil {
		return nil, fmt.Errorf("list collectible availability: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var giftID int64
		var availability domain.StarGiftCollectibleAvailability
		if err := rows.Scan(&giftID, &availability.UpgradeStars, &availability.SupplyTotal, &availability.Issued); err != nil {
			return nil, fmt.Errorf("scan collectible availability: %w", err)
		}
		out[giftID] = availability
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list collectible availability rows: %w", err)
	}
	return out, nil
}

func collectibleRevisionByID(ctx context.Context, db sqlcgen.DBTX, revisionID int64) (domain.StarGiftCollectibleRevision, error) {
	var revision domain.StarGiftCollectibleRevision
	var status string
	var publishedAt pgtype.Timestamptz
	if err := db.QueryRow(ctx, `
SELECT id, gift_id, revision, upgrade_stars, supply_total, issued, slug_prefix, status,
       created_by, created_at, published_at
FROM star_gift_collectible_revisions WHERE id=$1`, revisionID).Scan(
		&revision.ID, &revision.GiftID, &revision.Revision, &revision.UpgradeStars, &revision.SupplyTotal,
		&revision.Issued, &revision.SlugPrefix, &status, &revision.CreatedBy, &revision.CreatedAt, &publishedAt,
	); err != nil {
		return domain.StarGiftCollectibleRevision{}, fmt.Errorf("get collectible revision: %w", err)
	}
	revision.Published = status == "published"
	if publishedAt.Valid {
		revision.PublishedAt = publishedAt.Time
	}
	var err error
	if revision.Models, err = listAnimatedCollectibleAttributes(ctx, db, revisionID, domain.StarGiftCollectibleModel); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	if revision.Patterns, err = listAnimatedCollectibleAttributes(ctx, db, revisionID, domain.StarGiftCollectiblePattern); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	if revision.Backdrops, err = listCollectibleBackdrops(ctx, db, revisionID); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	return revision, nil
}

func listAnimatedCollectibleAttributes(ctx context.Context, db sqlcgen.DBTX, revisionID int64, kind domain.StarGiftCollectibleAttributeKind) ([]domain.StarGiftCollectibleAttribute, error) {
	table := "star_gift_collectible_models"
	if kind == domain.StarGiftCollectiblePattern {
		table = "star_gift_collectible_patterns"
	} else if kind != domain.StarGiftCollectibleModel {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	rows, err := db.Query(ctx, fmt.Sprintf(`
SELECT a.id, a.collectible_revision_id, a.name, a.rarity_permille, a.sort_order,
       a.animation_json::text, a.animation_sha256, a.source_name, a.source_format,
       a.width, a.height, a.frame_rate, a.in_point, a.out_point,
       d.id, d.access_hash, d.file_reference, d.date, d.mime_type, d.size, d.dc_id,
       d.attributes::text, d.thumbs::text
FROM %s a JOIN documents d ON d.id=a.document_id
WHERE a.collectible_revision_id=$1 ORDER BY a.sort_order, a.id`, table), revisionID)
	if err != nil {
		return nil, fmt.Errorf("list collectible %s attributes: %w", kind, err)
	}
	defer rows.Close()
	out := make([]domain.StarGiftCollectibleAttribute, 0)
	for rows.Next() {
		attribute := domain.StarGiftCollectibleAttribute{Kind: kind, Document: &domain.Document{}, Animation: &domain.StarGiftAnimation{}}
		var attrsJSON, thumbsJSON, sourceFormat string
		if err := rows.Scan(&attribute.ID, &attribute.CollectibleRevisionID, &attribute.Name, &attribute.RarityPermille, &attribute.SortOrder,
			&attribute.Animation.JSON, &attribute.Animation.SHA256, &attribute.Animation.SourceName, &sourceFormat,
			&attribute.Animation.Width, &attribute.Animation.Height, &attribute.Animation.FrameRate, &attribute.Animation.InPoint, &attribute.Animation.OutPoint,
			&attribute.Document.ID, &attribute.Document.AccessHash, &attribute.Document.FileReference, &attribute.Document.Date,
			&attribute.Document.MimeType, &attribute.Document.Size, &attribute.Document.DCID, &attrsJSON, &thumbsJSON); err != nil {
			return nil, err
		}
		attribute.Animation.SourceFormat = domain.StarGiftAnimationFormat(sourceFormat)
		if attribute.Document.Attributes, err = decodeDocumentAttributes(attrsJSON); err != nil {
			return nil, err
		}
		if attribute.Document.Thumbs, err = decodePhotoSizes(thumbsJSON); err != nil {
			return nil, err
		}
		out = append(out, attribute)
	}
	return out, rows.Err()
}

func listCollectibleBackdrops(ctx context.Context, db sqlcgen.DBTX, revisionID int64) ([]domain.StarGiftCollectibleAttribute, error) {
	rows, err := db.Query(ctx, `
SELECT id, collectible_revision_id, name, backdrop_id, center_color, edge_color, pattern_color,
       text_color, rarity_permille, sort_order
FROM star_gift_collectible_backdrops WHERE collectible_revision_id=$1 ORDER BY sort_order, id`, revisionID)
	if err != nil {
		return nil, fmt.Errorf("list collectible backdrops: %w", err)
	}
	defer rows.Close()
	out := make([]domain.StarGiftCollectibleAttribute, 0)
	for rows.Next() {
		attribute := domain.StarGiftCollectibleAttribute{Kind: domain.StarGiftCollectibleBackdrop}
		if err := rows.Scan(&attribute.ID, &attribute.CollectibleRevisionID, &attribute.Name, &attribute.BackdropID,
			&attribute.CenterColor, &attribute.EdgeColor, &attribute.PatternColor, &attribute.TextColor,
			&attribute.RarityPermille, &attribute.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, attribute)
	}
	return out, rows.Err()
}

func (s *StarGiftStore) CollectibleAnimationJSON(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error) {
	table := "star_gift_collectible_models"
	if kind == domain.StarGiftCollectiblePattern {
		table = "star_gift_collectible_patterns"
	} else if kind != domain.StarGiftCollectibleModel {
		return nil, false, nil
	}
	var raw []byte
	err := s.db.QueryRow(ctx, fmt.Sprintf(`
SELECT a.animation_json::text FROM %s a
JOIN star_gift_catalog c ON c.collectible_revision_id=a.collectible_revision_id
WHERE c.gift_id=$1 AND a.id=$2`, table), giftID, attributeID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get collectible animation: %w", err)
	}
	return raw, true, nil
}

func (s *StarGiftStore) UniqueBySlug(ctx context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	return s.uniqueByPredicate(ctx, "u.slug=$1", strings.ToLower(strings.TrimSpace(slug)))
}

func (s *StarGiftStore) UniqueByID(ctx context.Context, uniqueGiftID int64) (domain.UniqueStarGift, bool, error) {
	return s.uniqueByPredicate(ctx, "u.id=$1", uniqueGiftID)
}

func (s *StarGiftStore) UniqueByIDs(ctx context.Context, uniqueGiftIDs []int64) (map[int64]domain.UniqueStarGift, error) {
	out := make(map[int64]domain.UniqueStarGift, len(uniqueGiftIDs))
	if len(uniqueGiftIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, uniqueStarGiftQuery("u.id=ANY($1::bigint[])"), uniqueGiftIDs)
	if err != nil {
		return nil, fmt.Errorf("list unique star gifts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		unique, err := scanUniqueStarGift(rows)
		if err != nil {
			return nil, err
		}
		out[unique.ID] = unique
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unique star gifts: %w", err)
	}
	return out, nil
}

func (s *StarGiftStore) uniqueByPredicate(ctx context.Context, predicate string, value any) (domain.UniqueStarGift, bool, error) {
	row := s.db.QueryRow(ctx, uniqueStarGiftQuery(predicate), value)
	unique, err := scanUniqueStarGift(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.UniqueStarGift{}, false, nil
		}
		return domain.UniqueStarGift{}, false, err
	}
	return unique, true, nil
}

func uniqueStarGiftQuery(predicate string) string {
	return fmt.Sprintf(`
SELECT u.id, u.gift_id, u.collectible_revision_id, u.source_saved_gift_id, u.title, u.slug, u.num,
       u.owner_peer_type, u.owner_peer_id, u.keep_original_details, u.created_at,
       r.issued, r.supply_total, sg.from_user_id, sg.owner_peer_type, sg.owner_peer_id,
       sg.gift_date, sg.message, sg.name_hidden,
       m.id, m.name, m.rarity_permille, md.id, md.access_hash, md.file_reference, md.date,
       md.mime_type, md.size, md.dc_id, md.attributes::text, md.thumbs::text,
       p.id, p.name, p.rarity_permille, pd.id, pd.access_hash, pd.file_reference, pd.date,
       pd.mime_type, pd.size, pd.dc_id, pd.attributes::text, pd.thumbs::text,
       b.id, b.name, b.backdrop_id, b.center_color, b.edge_color, b.pattern_color, b.text_color, b.rarity_permille
FROM unique_star_gifts u
JOIN star_gift_collectible_revisions r ON r.id=u.collectible_revision_id
JOIN star_gift_collectible_models m ON m.id=u.model_attribute_id
JOIN documents md ON md.id=m.document_id
JOIN star_gift_collectible_patterns p ON p.id=u.pattern_attribute_id
JOIN documents pd ON pd.id=p.document_id
JOIN star_gift_collectible_backdrops b ON b.id=u.backdrop_attribute_id
JOIN peer_star_gifts sg ON sg.id=u.source_saved_gift_id
WHERE %s`, predicate)
}

func scanUniqueStarGift(row rowScanner) (domain.UniqueStarGift, error) {
	var unique domain.UniqueStarGift
	var ownerType, originalOwnerType string
	unique.Model.Kind = domain.StarGiftCollectibleModel
	unique.Pattern.Kind = domain.StarGiftCollectiblePattern
	unique.Backdrop.Kind = domain.StarGiftCollectibleBackdrop
	unique.Model.Document = &domain.Document{}
	unique.Pattern.Document = &domain.Document{}
	var modelAttrs, modelThumbs, patternAttrs, patternThumbs string
	if err := row.Scan(&unique.ID, &unique.GiftID, &unique.CollectibleRevisionID, &unique.SourceSavedGiftID,
		&unique.Title, &unique.Slug, &unique.Num, &ownerType, &unique.Owner.ID, &unique.KeepOriginalDetails,
		&unique.CreatedAt, &unique.AvailabilityIssued, &unique.AvailabilityTotal,
		&unique.OriginalFromUserID, &originalOwnerType, &unique.OriginalOwner.ID, &unique.OriginalDate,
		&unique.OriginalMessage, &unique.OriginalNameHidden,
		&unique.Model.ID, &unique.Model.Name, &unique.Model.RarityPermille,
		&unique.Model.Document.ID, &unique.Model.Document.AccessHash, &unique.Model.Document.FileReference,
		&unique.Model.Document.Date, &unique.Model.Document.MimeType, &unique.Model.Document.Size,
		&unique.Model.Document.DCID, &modelAttrs, &modelThumbs,
		&unique.Pattern.ID, &unique.Pattern.Name, &unique.Pattern.RarityPermille,
		&unique.Pattern.Document.ID, &unique.Pattern.Document.AccessHash, &unique.Pattern.Document.FileReference,
		&unique.Pattern.Document.Date, &unique.Pattern.Document.MimeType, &unique.Pattern.Document.Size,
		&unique.Pattern.Document.DCID, &patternAttrs, &patternThumbs,
		&unique.Backdrop.ID, &unique.Backdrop.Name, &unique.Backdrop.BackdropID, &unique.Backdrop.CenterColor,
		&unique.Backdrop.EdgeColor, &unique.Backdrop.PatternColor, &unique.Backdrop.TextColor, &unique.Backdrop.RarityPermille); err != nil {
		return domain.UniqueStarGift{}, fmt.Errorf("get unique star gift: %w", err)
	}
	unique.Owner.Type = domain.PeerType(ownerType)
	unique.OriginalOwner.Type = domain.PeerType(originalOwnerType)
	unique.Model.CollectibleRevisionID = unique.CollectibleRevisionID
	unique.Pattern.CollectibleRevisionID = unique.CollectibleRevisionID
	unique.Backdrop.CollectibleRevisionID = unique.CollectibleRevisionID
	var err error
	if unique.Model.Document.Attributes, err = decodeDocumentAttributes(modelAttrs); err != nil {
		return domain.UniqueStarGift{}, err
	}
	if unique.Model.Document.Thumbs, err = decodePhotoSizes(modelThumbs); err != nil {
		return domain.UniqueStarGift{}, err
	}
	if unique.Pattern.Document.Attributes, err = decodeDocumentAttributes(patternAttrs); err != nil {
		return domain.UniqueStarGift{}, err
	}
	if unique.Pattern.Document.Thumbs, err = decodePhotoSizes(patternThumbs); err != nil {
		return domain.UniqueStarGift{}, err
	}
	return unique, nil
}

func (s *StarGiftStore) ListCollections(ctx context.Context, owner domain.Peer) ([]domain.StarGiftCollection, error) {
	rows, err := s.db.Query(ctx, `
SELECT c.collection_id, c.title, c.hash, c.sort_order, c.created_at, c.updated_at, i.saved_gift_id
FROM star_gift_collections c
LEFT JOIN star_gift_collection_items i ON i.collection_id=c.collection_id
WHERE c.owner_peer_type=$1 AND c.owner_peer_id=$2
ORDER BY c.sort_order, c.collection_id, i.sort_order, i.saved_gift_id`, string(owner.Type), owner.ID)
	if err != nil {
		return nil, fmt.Errorf("list star gift collections: %w", err)
	}
	defer rows.Close()
	out := make([]domain.StarGiftCollection, 0)
	index := make(map[int]int)
	for rows.Next() {
		var collection domain.StarGiftCollection
		var giftID pgtype.Int8
		if err := rows.Scan(&collection.CollectionID, &collection.Title, &collection.Hash, &collection.SortOrder,
			&collection.CreatedAt, &collection.UpdatedAt, &giftID); err != nil {
			return nil, err
		}
		position, ok := index[collection.CollectionID]
		if !ok {
			collection.Owner = owner
			position = len(out)
			index[collection.CollectionID] = position
			out = append(out, collection)
		}
		if giftID.Valid {
			out[position].GiftIDs = append(out[position].GiftIDs, giftID.Int64)
		}
	}
	return out, rows.Err()
}

func (s *StarGiftStore) CreateCollection(ctx context.Context, owner domain.Peer, title string, savedGiftIDs []int64) (domain.StarGiftCollection, error) {
	title = strings.TrimSpace(title)
	if !validPostgresStarGiftOwner(owner) || title == "" || len([]rune(title)) > domain.MaxStarGiftCollectionTitleRunes {
		return domain.StarGiftCollection{}, domain.ErrStarGiftCollectibleInvalid
	}
	var result domain.StarGiftCollection
	err := withTx(ctx, s.db, "create star gift collection", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_collections WHERE owner_peer_type=$1 AND owner_peer_id=$2`, string(owner.Type), owner.ID).Scan(&count); err != nil {
			return err
		}
		if count >= domain.MaxStarGiftCollectionsPerPeer {
			return domain.ErrStarGiftCollectionsFull
		}
		ids, err := validatePostgresCollectionGiftIDs(ctx, tx, owner, savedGiftIDs)
		if err != nil {
			return err
		}
		result = domain.StarGiftCollection{Owner: owner, Title: title, GiftIDs: ids, SortOrder: count}
		result.Hash = domain.StarGiftCollectionHash(title, ids)
		if err := tx.QueryRow(ctx, `
INSERT INTO star_gift_collections(owner_peer_type, owner_peer_id, title, sort_order, hash)
VALUES ($1,$2,$3,$4,$5) RETURNING collection_id, created_at, updated_at`, string(owner.Type), owner.ID,
			title, count, result.Hash).Scan(&result.CollectionID, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return err
		}
		return replaceCollectionItems(ctx, tx, result.CollectionID, ids)
	})
	return result, err
}

func (s *StarGiftStore) UpdateCollection(ctx context.Context, owner domain.Peer, collectionID int, patch domain.StarGiftCollectionPatch) (domain.StarGiftCollection, error) {
	var result domain.StarGiftCollection
	err := withTx(ctx, s.db, "update star gift collection", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
SELECT title, hash, sort_order, created_at, updated_at FROM star_gift_collections
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND collection_id=$3 FOR UPDATE`, string(owner.Type), owner.ID, collectionID).Scan(
			&result.Title, &result.Hash, &result.SortOrder, &result.CreatedAt, &result.UpdatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftCollectionNotFound
			}
			return err
		}
		result.Owner = owner
		result.CollectionID = collectionID
		rows, err := tx.Query(ctx, `SELECT saved_gift_id FROM star_gift_collection_items WHERE collection_id=$1 ORDER BY sort_order, saved_gift_id`, collectionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			result.GiftIDs = append(result.GiftIDs, id)
		}
		rows.Close()
		if patch.Title != nil {
			title := strings.TrimSpace(*patch.Title)
			if title == "" || len([]rune(title)) > domain.MaxStarGiftCollectionTitleRunes {
				return domain.ErrStarGiftCollectibleInvalid
			}
			result.Title = title
		}
		deleted := make(map[int64]struct{}, len(patch.DeleteIDs))
		for _, id := range patch.DeleteIDs {
			deleted[id] = struct{}{}
		}
		next := make([]int64, 0, len(result.GiftIDs)+len(patch.AddIDs))
		for _, id := range result.GiftIDs {
			if _, ok := deleted[id]; !ok {
				next = append(next, id)
			}
		}
		add, err := validatePostgresCollectionGiftIDs(ctx, tx, owner, patch.AddIDs)
		if err != nil {
			return err
		}
		next = appendUniquePostgresIDs(next, add...)
		if patch.Order != nil {
			order, err := validatePostgresCollectionGiftIDs(ctx, tx, owner, patch.Order)
			if err != nil || !samePostgresIDSet(order, next) {
				return domain.ErrStarGiftCollectibleInvalid
			}
			next = order
		}
		if len(next) > domain.MaxStarGiftCollectionItems {
			return domain.ErrStarGiftCollectibleInvalid
		}
		result.GiftIDs = next
		result.Hash = domain.StarGiftCollectionHash(result.Title, result.GiftIDs)
		if err := tx.QueryRow(ctx, `
UPDATE star_gift_collections SET title=$4, hash=$5, updated_at=now()
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND collection_id=$3 RETURNING updated_at`,
			string(owner.Type), owner.ID, collectionID, result.Title, result.Hash).Scan(&result.UpdatedAt); err != nil {
			return err
		}
		return replaceCollectionItems(ctx, tx, collectionID, result.GiftIDs)
	})
	return result, err
}

func (s *StarGiftStore) DeleteCollection(ctx context.Context, owner domain.Peer, collectionID int) (bool, error) {
	var changed bool
	err := withTx(ctx, s.db, "delete star gift collection", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM star_gift_collections WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND collection_id=$3`, string(owner.Type), owner.ID, collectionID)
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() > 0
		if changed {
			_, err = tx.Exec(ctx, `
WITH ordered AS (
    SELECT collection_id, row_number() OVER (ORDER BY sort_order, collection_id) - 1 AS next_order
    FROM star_gift_collections WHERE owner_peer_type=$1 AND owner_peer_id=$2
)
UPDATE star_gift_collections c SET sort_order=o.next_order, updated_at=now()
FROM ordered o WHERE c.collection_id=o.collection_id`, string(owner.Type), owner.ID)
		}
		return err
	})
	return changed, err
}

func (s *StarGiftStore) ReorderCollections(ctx context.Context, owner domain.Peer, collectionIDs []int) error {
	return withTx(ctx, s.db, "reorder star gift collections", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT collection_id FROM star_gift_collections WHERE owner_peer_type=$1 AND owner_peer_id=$2 FOR UPDATE`, string(owner.Type), owner.ID)
		if err != nil {
			return err
		}
		existing := make([]int, 0)
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			existing = append(existing, id)
		}
		rows.Close()
		if !samePostgresIntSet(existing, collectionIDs) {
			return domain.ErrStarGiftCollectibleInvalid
		}
		for order, id := range collectionIDs {
			if _, err := tx.Exec(ctx, `UPDATE star_gift_collections SET sort_order=$4, updated_at=now() WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND collection_id=$3`, string(owner.Type), owner.ID, id, order); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *StarGiftStore) SetPinned(ctx context.Context, owner domain.Peer, savedGiftIDs []int64) error {
	return withTx(ctx, s.db, "set pinned star gifts", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		ids, err := validatePostgresCollectionGiftIDs(ctx, tx, owner, savedGiftIDs)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET pinned_order=0 WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND pinned_order<>0`, string(owner.Type), owner.ID); err != nil {
			return err
		}
		for order, id := range ids {
			if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET pinned_order=$2 WHERE id=$1`, id, order+1); err != nil {
				return err
			}
		}
		return nil
	})
}

func validatePostgresCollectionGiftIDs(ctx context.Context, db sqlcgen.DBTX, owner domain.Peer, ids []int64) ([]int64, error) {
	ids = dedupePostgresIDs(ids)
	if len(ids) > domain.MaxStarGiftCollectionItems {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	if len(ids) == 0 {
		return []int64{}, nil
	}
	rows, err := db.Query(ctx, `
SELECT id FROM peer_star_gifts
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND NOT converted AND id=ANY($3::bigint[])
FOR UPDATE`, string(owner.Type), owner.ID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[int64]struct{}, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found[id] = struct{}{}
	}
	if len(found) != len(ids) {
		return nil, domain.ErrStarGiftNotFound
	}
	return ids, rows.Err()
}

// removeSavedGiftFromCollections runs under the owner advisory lock. It removes
// terminal gifts and updates every affected collection hash in bounded batches,
// so getStarGiftCollections cannot return NotModified for changed membership.
func removeSavedGiftFromCollections(ctx context.Context, tx pgx.Tx, owner domain.Peer, savedGiftID int64) error {
	rows, err := tx.Query(ctx, `
SELECT c.collection_id, c.title
FROM star_gift_collections c
JOIN star_gift_collection_items i ON i.collection_id=c.collection_id
WHERE c.owner_peer_type=$1 AND c.owner_peer_id=$2 AND i.saved_gift_id=$3
ORDER BY c.collection_id
FOR UPDATE OF c`, string(owner.Type), owner.ID, savedGiftID)
	if err != nil {
		return fmt.Errorf("lock converted gift collections: %w", err)
	}
	titles := make(map[int]string)
	ids := make([]int, 0)
	for rows.Next() {
		var id int
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
		titles[id] = title
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM star_gift_collection_items WHERE saved_gift_id=$1`, savedGiftID); err != nil {
		return fmt.Errorf("remove converted gift collection memberships: %w", err)
	}

	memberships := make(map[int][]int64, len(ids))
	itemRows, err := tx.Query(ctx, `
SELECT collection_id, saved_gift_id
FROM star_gift_collection_items
WHERE collection_id=ANY($1::integer[])
ORDER BY collection_id, sort_order, saved_gift_id`, ids)
	if err != nil {
		return fmt.Errorf("list remaining collection memberships: %w", err)
	}
	for itemRows.Next() {
		var collectionID int
		var giftID int64
		if err := itemRows.Scan(&collectionID, &giftID); err != nil {
			itemRows.Close()
			return err
		}
		memberships[collectionID] = append(memberships[collectionID], giftID)
	}
	if err := itemRows.Err(); err != nil {
		itemRows.Close()
		return err
	}
	itemRows.Close()

	hashes := make([]int64, len(ids))
	for i, collectionID := range ids {
		hashes[i] = domain.StarGiftCollectionHash(titles[collectionID], memberships[collectionID])
	}
	if _, err := tx.Exec(ctx, `
UPDATE star_gift_collections c SET hash=x.hash, updated_at=now()
FROM unnest($1::integer[], $2::bigint[]) AS x(collection_id, hash)
WHERE c.collection_id=x.collection_id`, ids, hashes); err != nil {
		return fmt.Errorf("refresh converted gift collection hashes: %w", err)
	}
	return nil
}

func replaceCollectionItems(ctx context.Context, tx pgx.Tx, collectionID int, ids []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM star_gift_collection_items WHERE collection_id=$1`, collectionID); err != nil {
		return err
	}
	for order, id := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_collection_items(collection_id, saved_gift_id, sort_order) VALUES ($1,$2,$3)`, collectionID, id, order); err != nil {
			return err
		}
	}
	return nil
}

func validPostgresStarGiftOwner(owner domain.Peer) bool {
	return owner.ID > 0 && (owner.Type == domain.PeerTypeUser || owner.Type == domain.PeerTypeChannel)
}

func starGiftCollectionLockKey(owner domain.Peer) string {
	return fmt.Sprintf("star_gift_collection:%s:%d", owner.Type, owner.ID)
}

func dedupePostgresIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func appendUniquePostgresIDs(dst []int64, values ...int64) []int64 {
	seen := make(map[int64]struct{}, len(dst)+len(values))
	for _, id := range dst {
		seen[id] = struct{}{}
	}
	for _, id := range values {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			dst = append(dst, id)
		}
	}
	return dst
}

func samePostgresIDSet(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]int64(nil), a...)
	b = append([]int64(nil), b...)
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func samePostgresIntSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int]struct{}, len(a))
	for _, id := range a {
		seen[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := seen[id]; !ok {
			return false
		}
		delete(seen, id)
	}
	return len(seen) == 0
}
