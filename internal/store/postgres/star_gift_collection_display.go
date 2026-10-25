package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *StarGiftStore) ListCollectionDisplaySettings(ctx context.Context, owner domain.Peer) ([]domain.StarGiftCollectionDisplaySettings, error) {
	if !validPostgresStarGiftOwner(owner) {
		return nil, domain.ErrStarGiftCollectionDisplayInvalid
	}
	return listCollectionDisplaySettings(ctx, s.db, owner)
}

func (s *StarGiftStore) GetCollectionDisplaySettings(ctx context.Context, owner domain.Peer, collectionID int) (domain.StarGiftCollectionDisplaySettings, error) {
	if !validPostgresStarGiftOwner(owner) || collectionID <= 0 {
		return domain.StarGiftCollectionDisplaySettings{}, domain.ErrStarGiftCollectionDisplayInvalid
	}
	settings, err := listCollectionDisplaySettings(ctx, s.db, owner)
	if err != nil {
		return domain.StarGiftCollectionDisplaySettings{}, err
	}
	for _, setting := range settings {
		if setting.CollectionID == collectionID {
			return setting, nil
		}
	}
	return domain.StarGiftCollectionDisplaySettings{}, domain.ErrStarGiftCollectionNotFound
}

func (s *StarGiftStore) UpdateCollectionDisplaySettings(ctx context.Context, owner domain.Peer, collectionID int, patch domain.StarGiftCollectionDisplayPatch) (domain.StarGiftCollectionDisplaySettings, error) {
	if !validPostgresStarGiftOwner(owner) || collectionID <= 0 {
		return domain.StarGiftCollectionDisplaySettings{}, domain.ErrStarGiftCollectionDisplayInvalid
	}
	var result domain.StarGiftCollectionDisplaySettings
	err := withTx(ctx, s.db, "update star gift collection display settings", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		settings, err := listCollectionDisplaySettings(ctx, tx, owner)
		if err != nil {
			return err
		}
		var current domain.StarGiftCollectionDisplaySettings
		found := false
		for _, setting := range settings {
			if setting.CollectionID == collectionID {
				current = setting
				found = true
				break
			}
		}
		if !found {
			return domain.ErrStarGiftCollectionNotFound
		}
		if patch.FilterMask != nil && (*patch.FilterMask < 0 || uint32(*patch.FilterMask)&^domain.StarGiftCollectionDisplayFilterMaskAll != 0) {
			return domain.ErrStarGiftCollectionDisplayInvalid
		}
		next := current
		if patch.Hidden != nil {
			next.Hidden = *patch.Hidden
		}
		if patch.MainTab != nil {
			next.MainTab = *patch.MainTab
		}
		if patch.FilterMask != nil {
			next.FilterMask = *patch.FilterMask
		}
		if patch.ItemsSet {
			items, err := domain.NormalizeStarGiftCollectionDisplayItems(collectionDisplayGiftIDs(current.Items), patch.Items)
			if err != nil {
				return err
			}
			next.Items = items
			if err := updateCollectionDisplayOrder(ctx, tx, collectionID, items); err != nil {
				return err
			}
		}
		if patch.MainTab != nil && *patch.MainTab {
			if _, err := tx.Exec(ctx, `
UPDATE star_gift_collection_display_settings
SET main_tab=false, updated_at=now()
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND collection_id<>$3 AND main_tab`, string(owner.Type), owner.ID, collectionID); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `
INSERT INTO star_gift_collection_display_settings
    (collection_id, owner_peer_type, owner_peer_id, hidden, main_tab, filter_mask)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (collection_id) DO UPDATE SET
    owner_peer_type=EXCLUDED.owner_peer_type,
    owner_peer_id=EXCLUDED.owner_peer_id,
    hidden=EXCLUDED.hidden,
    main_tab=EXCLUDED.main_tab,
    filter_mask=EXCLUDED.filter_mask,
    updated_at=now()
RETURNING updated_at`, collectionID, string(owner.Type), owner.ID, next.Hidden, next.MainTab, next.FilterMask).Scan(&next.UpdatedAt); err != nil {
			return err
		}
		if patch.ItemsSet {
			if _, err := tx.Exec(ctx, `DELETE FROM star_gift_collection_display_items WHERE collection_id=$1`, collectionID); err != nil {
				return err
			}
			for _, item := range next.Items {
				if _, err := tx.Exec(ctx, `INSERT INTO star_gift_collection_display_items(collection_id, saved_gift_id, tile_size) VALUES ($1,$2,$3)`, collectionID, item.GiftID, item.Size); err != nil {
					return err
				}
			}
		}
		result = next
		return nil
	})
	return result, err
}

func listCollectionDisplaySettings(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, owner domain.Peer) ([]domain.StarGiftCollectionDisplaySettings, error) {
	rows, err := db.Query(ctx, `
SELECT c.collection_id,
       COALESCE(s.hidden, false), COALESCE(s.main_tab, false),
       COALESCE(s.filter_mask, 0), COALESCE(s.updated_at, c.updated_at)
FROM star_gift_collections c
LEFT JOIN star_gift_collection_display_settings s ON s.collection_id=c.collection_id
WHERE c.owner_peer_type=$1 AND c.owner_peer_id=$2
ORDER BY c.sort_order, c.collection_id`, string(owner.Type), owner.ID)
	if err != nil {
		return nil, fmt.Errorf("list star gift collection display settings: %w", err)
	}
	defer rows.Close()
	settings := make([]domain.StarGiftCollectionDisplaySettings, 0)
	byID := make(map[int]int)
	for rows.Next() {
		var setting domain.StarGiftCollectionDisplaySettings
		if err := rows.Scan(&setting.CollectionID, &setting.Hidden, &setting.MainTab, &setting.FilterMask, &setting.UpdatedAt); err != nil {
			return nil, err
		}
		setting.Owner = owner
		byID[setting.CollectionID] = len(settings)
		settings = append(settings, setting)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items, err := db.Query(ctx, `
SELECT i.collection_id, i.saved_gift_id, i.sort_order, COALESCE(d.tile_size, 0)
FROM star_gift_collection_items i
JOIN star_gift_collections c ON c.collection_id=i.collection_id
LEFT JOIN star_gift_collection_display_items d
  ON d.collection_id=i.collection_id AND d.saved_gift_id=i.saved_gift_id
WHERE c.owner_peer_type=$1 AND c.owner_peer_id=$2
ORDER BY i.collection_id, i.sort_order, i.saved_gift_id`, string(owner.Type), owner.ID)
	if err != nil {
		return nil, fmt.Errorf("list star gift collection display items: %w", err)
	}
	defer items.Close()
	for items.Next() {
		var collectionID int
		var item domain.StarGiftCollectionDisplayItem
		if err := items.Scan(&collectionID, &item.GiftID, &item.Order, &item.Size); err != nil {
			return nil, err
		}
		position, ok := byID[collectionID]
		if ok {
			settings[position].Items = append(settings[position].Items, item)
		}
	}
	if err := items.Err(); err != nil {
		return nil, err
	}
	return settings, nil
}

func collectionDisplayGiftIDs(items []domain.StarGiftCollectionDisplayItem) []int64 {
	ids := make([]int64, len(items))
	for i, item := range items {
		ids[i] = item.GiftID
	}
	return ids
}

func updateCollectionDisplayOrder(ctx context.Context, tx pgx.Tx, collectionID int, items []domain.StarGiftCollectionDisplayItem) error {
	ids := make([]int64, len(items))
	for i, item := range items {
		ids[i] = item.GiftID
		if _, err := tx.Exec(ctx, `
UPDATE star_gift_collection_items SET sort_order=$3
WHERE collection_id=$1 AND saved_gift_id=$2`, collectionID, item.GiftID, i); err != nil {
			return err
		}
	}
	var title string
	if err := tx.QueryRow(ctx, `SELECT title FROM star_gift_collections WHERE collection_id=$1 FOR UPDATE`, collectionID).Scan(&title); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE star_gift_collections SET hash=$2, updated_at=now() WHERE collection_id=$1`, collectionID, domain.StarGiftCollectionHash(title, ids))
	return err
}
