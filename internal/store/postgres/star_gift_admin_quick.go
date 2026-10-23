package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// Admin quick settings for one catalog gift: supply, anti-scalping limits and
// deletion. Supply and limits edit the active revision in place, without
// minting a new one, so outstanding purchase forms stay valid.

// SetCatalogSupply sets the plain gift's "X of Y" cap. limited=false removes
// the cap. issued is how many copies count as already sold.
func (s *StarGiftStore) SetCatalogSupply(ctx context.Context, giftID int64, limited bool, total, issued int) (bool, error) {
	if giftID <= 0 || total < 0 || issued < 0 || issued > total || (limited && total <= 0) {
		return false, domain.ErrStarGiftInvalid
	}
	if !limited {
		total, issued = 0, 0
	}
	var changed bool
	err := withTx(ctx, s.db, "set star gift supply", func(tx pgx.Tx) error {
		var revisionID int64
		var auction bool
		var deleted bool
		if err := tx.QueryRow(ctx, `
SELECT c.active_revision_id, r.auction, c.deleted_at IS NOT NULL
FROM star_gift_catalog c JOIN star_gift_catalog_revisions r ON r.id=c.active_revision_id
WHERE c.gift_id=$1 FOR UPDATE OF c`, giftID).Scan(&revisionID, &auction, &deleted); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftNotFound
			}
			return fmt.Errorf("lock star gift for supply edit: %w", err)
		}
		if deleted {
			return domain.ErrStarGiftNotFound
		}
		// An auction's supply is the auction's own round plan.
		if auction {
			return domain.ErrStarGiftInvalid
		}
		remains := total - issued
		soldOut := limited && remains <= 0
		revTag, err := tx.Exec(ctx, `
UPDATE star_gift_catalog_revisions SET limited=$2, availability_total=$3, sold_out=$4
WHERE id=$1 AND (limited IS DISTINCT FROM $2 OR availability_total IS DISTINCT FROM $3 OR sold_out IS DISTINCT FROM $4)`,
			revisionID, limited, total, soldOut)
		if err != nil {
			return fmt.Errorf("update star gift revision supply: %w", err)
		}
		catTag, err := tx.Exec(ctx, `
UPDATE star_gift_catalog SET availability_remains=$2, updated_at=now()
WHERE gift_id=$1 AND availability_remains IS DISTINCT FROM $2`, giftID, remains)
		if err != nil {
			return fmt.Errorf("update star gift catalog supply: %w", err)
		}
		changed = revTag.RowsAffected() > 0 || catTag.RowsAffected() > 0
		return nil
	})
	return changed, err
}

// SetDropProtection sets the lifetime per-buyer cap and the sliding-window
// rate limit (rateLimitCount purchases per rateLimitWindowSeconds, 0/0 is
// off). Buyers mid-window are judged by the new rule on their next purchase.
func (s *StarGiftStore) SetDropProtection(ctx context.Context, giftID int64, limitedPerUser bool, perUserTotal, rateLimitCount, rateLimitWindowSeconds int) (bool, error) {
	if giftID <= 0 || perUserTotal < 0 || (limitedPerUser && perUserTotal <= 0) ||
		rateLimitCount < 0 || rateLimitWindowSeconds < 0 ||
		(rateLimitCount > 0) != (rateLimitWindowSeconds > 0) {
		return false, domain.ErrStarGiftInvalid
	}
	if !limitedPerUser {
		perUserTotal = 0
	}
	var changed bool
	err := withTx(ctx, s.db, "set star gift drop protection", func(tx pgx.Tx) error {
		var revisionID int64
		var deleted bool
		if err := tx.QueryRow(ctx, `SELECT active_revision_id, deleted_at IS NOT NULL FROM star_gift_catalog WHERE gift_id=$1 FOR UPDATE`, giftID).
			Scan(&revisionID, &deleted); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftNotFound
			}
			return fmt.Errorf("lock star gift for drop protection edit: %w", err)
		}
		if deleted {
			return domain.ErrStarGiftNotFound
		}
		tag, err := tx.Exec(ctx, `
UPDATE star_gift_catalog_revisions
SET limited_per_user=$2, per_user_total=$3, rate_limit_count=$4, rate_limit_window_seconds=$5
WHERE id=$1 AND (limited_per_user IS DISTINCT FROM $2 OR per_user_total IS DISTINCT FROM $3
  OR rate_limit_count IS DISTINCT FROM $4 OR rate_limit_window_seconds IS DISTINCT FROM $5)`,
			revisionID, limitedPerUser, perUserTotal, rateLimitCount, rateLimitWindowSeconds)
		if err != nil {
			return fmt.Errorf("update star gift revision drop protection: %w", err)
		}
		changed = tag.RowsAffected() > 0
		return nil
	})
	return changed, err
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// starGiftDeleteBlockers counts the state a gift deletion refuses to cascade
// through. See domain.StarGiftDeleteResult. Purchase forms in flight are not
// a blocker: a purchase re-checks that the gift is enabled in its own
// transaction, so a form for a deleted gift can no longer be paid.
func starGiftDeleteBlockers(ctx context.Context, db queryRower, giftID int64) (map[string]int, error) {
	var collectibles, auctions int
	if err := db.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM unique_star_gifts WHERE gift_id=$1),
  (SELECT count(*) FROM star_gift_auctions WHERE gift_id=$1)`,
		giftID).Scan(&collectibles, &auctions); err != nil {
		return nil, fmt.Errorf("check star gift delete blockers: %w", err)
	}
	blockers := map[string]int{}
	if collectibles > 0 {
		blockers["collectibles"] = collectibles
	}
	if auctions > 0 {
		blockers["auctions"] = auctions
	}
	return blockers, nil
}

// starGiftDeletePreview fills title, owners, refunds and blockers.
func starGiftDeletePreview(ctx context.Context, db interface {
	queryRower
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, giftID int64, lock bool) (domain.StarGiftDeleteResult, error) {
	result := domain.StarGiftDeleteResult{GiftID: giftID}
	query := `
SELECT r.title, c.deleted_at IS NOT NULL
FROM star_gift_catalog c JOIN star_gift_catalog_revisions r ON r.id=c.active_revision_id
WHERE c.gift_id=$1`
	if lock {
		query += ` FOR UPDATE OF c`
	}
	var deleted bool
	if err := db.QueryRow(ctx, query, giftID).Scan(&result.Title, &deleted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StarGiftDeleteResult{}, domain.ErrStarGiftNotFound
		}
		return domain.StarGiftDeleteResult{}, fmt.Errorf("get star gift for delete: %w", err)
	}
	if deleted {
		return domain.StarGiftDeleteResult{}, domain.ErrStarGiftNotFound
	}
	blockers, err := starGiftDeleteBlockers(ctx, db, giftID)
	if err != nil {
		return domain.StarGiftDeleteResult{}, err
	}
	result.Blockers = blockers
	rows, err := db.Query(ctx, `
SELECT owner_peer_type, owner_peer_id, convert_stars + prepaid_upgrade_stars
FROM peer_star_gifts
WHERE gift_id=$1 AND lifecycle_status='active' AND unique_gift_id IS NULL
ORDER BY id`, giftID)
	if err != nil {
		return domain.StarGiftDeleteResult{}, fmt.Errorf("list star gift owners for delete: %w", err)
	}
	defer rows.Close()
	refunds := map[int64]int{}
	for rows.Next() {
		var ownerType string
		var ownerID, stars int64
		if err := rows.Scan(&ownerType, &ownerID, &stars); err != nil {
			return domain.StarGiftDeleteResult{}, fmt.Errorf("scan star gift owner for delete: %w", err)
		}
		result.Owners++
		if ownerType != string(domain.PeerTypeUser) || stars <= 0 {
			continue
		}
		if i, ok := refunds[ownerID]; ok {
			result.Refunds[i].Stars += stars
			continue
		}
		refunds[ownerID] = len(result.Refunds)
		result.Refunds = append(result.Refunds, domain.StarGiftOwnerRefund{UserID: ownerID, Stars: stars})
	}
	if err := rows.Err(); err != nil {
		return domain.StarGiftDeleteResult{}, fmt.Errorf("iterate star gift owners for delete: %w", err)
	}
	return result, nil
}

// PreviewStarGiftDelete reports what DeleteStarGift would do.
func (s *StarGiftStore) PreviewStarGiftDelete(ctx context.Context, giftID int64) (domain.StarGiftDeleteResult, error) {
	if giftID <= 0 {
		return domain.StarGiftDeleteResult{}, domain.ErrStarGiftInvalid
	}
	return starGiftDeletePreview(ctx, s.db, giftID, false)
}

// DeleteStarGift retires a gift for good: it leaves the catalog (disabled,
// marked deleted, hidden from the admin list) and every ordinary saved copy
// moves to the 'deleted' lifecycle, so it disappears from its owner's
// profile. Rows are kept as the anchor messages and purchase records point
// at. Refunds are reported, not paid; the caller credits them.
func (s *StarGiftStore) DeleteStarGift(ctx context.Context, giftID int64) (domain.StarGiftDeleteResult, error) {
	if giftID <= 0 {
		return domain.StarGiftDeleteResult{}, domain.ErrStarGiftInvalid
	}
	var result domain.StarGiftDeleteResult
	err := withTx(ctx, s.db, "delete star gift", func(tx pgx.Tx) error {
		var err error
		result, err = starGiftDeletePreview(ctx, tx, giftID, true)
		if err != nil {
			return err
		}
		if len(result.Blockers) > 0 {
			return domain.ErrStarGiftDeleteBlocked
		}
		if _, err := tx.Exec(ctx, `
DELETE FROM star_gift_collection_items WHERE saved_gift_id IN (
  SELECT id FROM peer_star_gifts WHERE gift_id=$1 AND lifecycle_status='active' AND unique_gift_id IS NULL)`, giftID); err != nil {
			return fmt.Errorf("delete star gift collection items: %w", err)
		}
		type owner struct {
			peerType string
			peerID   int64
		}
		var pinnedOwners []owner
		rows, err := tx.Query(ctx, `
WITH retired AS (
    SELECT id, pinned_order FROM peer_star_gifts
    WHERE gift_id=$1 AND lifecycle_status='active' AND unique_gift_id IS NULL
    FOR UPDATE
)
UPDATE peer_star_gifts p SET lifecycle_status='deleted', pinned_order=0
FROM retired WHERE p.id=retired.id
RETURNING p.owner_peer_type, p.owner_peer_id, retired.pinned_order`, giftID)
		if err != nil {
			return fmt.Errorf("retire saved star gifts: %w", err)
		}
		seen := map[owner]bool{}
		for rows.Next() {
			var o owner
			var oldPinned int
			if err := rows.Scan(&o.peerType, &o.peerID, &oldPinned); err != nil {
				rows.Close()
				return fmt.Errorf("scan retired star gift: %w", err)
			}
			if oldPinned > 0 && !seen[o] {
				seen[o] = true
				pinnedOwners = append(pinnedOwners, o)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("retire saved star gifts: %w", err)
		}
		// Close the gaps the retired pins left. Moving each remaining pin
		// down to its rank, in ascending order, never collides with the
		// unique (owner, pinned_order) index.
		for _, o := range pinnedOwners {
			if err := compactStarGiftPins(ctx, tx, o.peerType, o.peerID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
UPDATE star_gift_catalog SET enabled=false, deleted_at=now(), updated_at=now() WHERE gift_id=$1`, giftID); err != nil {
			return fmt.Errorf("retire star gift catalog entry: %w", err)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func compactStarGiftPins(ctx context.Context, tx pgx.Tx, peerType string, peerID int64) error {
	rows, err := tx.Query(ctx, `
SELECT id, pinned_order FROM peer_star_gifts
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND pinned_order>0
ORDER BY pinned_order`, peerType, peerID)
	if err != nil {
		return fmt.Errorf("list star gift pins: %w", err)
	}
	type pin struct {
		id    int64
		order int
	}
	var pins []pin
	for rows.Next() {
		var p pin
		if err := rows.Scan(&p.id, &p.order); err != nil {
			rows.Close()
			return fmt.Errorf("scan star gift pin: %w", err)
		}
		pins = append(pins, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list star gift pins: %w", err)
	}
	for i, p := range pins {
		if p.order == i+1 {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET pinned_order=$2 WHERE id=$1`, p.id, i+1); err != nil {
			return fmt.Errorf("compact star gift pin: %w", err)
		}
	}
	return nil
}
