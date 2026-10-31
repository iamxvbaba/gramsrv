package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// An account freeze and the gift market are directly linked: a frozen seller does not trade. Their
// listings are not deleted but marked suspended -- the price and listing time must survive the
// freeze cycle, otherwise unfreezing could no longer return the gift to the storefront. All market
// reads (ListResaleStarGifts, UniqueStarGiftValueInfo, a unique gift's resell_amount, catalog
// projections) filter out suspended entries, and a guard trigger prevents a frozen seller from
// creating a listing.

// syncStarGiftListingsForAccountFreeze toggles the storefront of a frozen account and recomputes
// the affected catalog projections. It is called from the same transaction as the freeze write, so
// "account frozen but listing still on sale" is not observable by any reader.
//
// updated_at is deliberately left untouched: the storefront sort relies on it, and returning to
// sale with the previous updated_at restores the original listing order. The listing's version
// counter, by contrast, is incremented -- the version is part of the payment form id, so a purchase
// with a form issued before the freeze does not go through as a replay of the old price after unfreezing.
func syncStarGiftListingsForAccountFreeze(ctx context.Context, db sqlcgen.DBTX, userID int64, frozen bool) error {
	if db == nil || userID <= 0 {
		return nil
	}
	rows, err := db.Query(ctx, `UPDATE star_gift_listings l SET suspended=$2,version=l.version+1
  FROM unique_star_gifts u
 WHERE l.unique_gift_id=u.id AND l.seller_peer_type='user' AND l.seller_peer_id=$1 AND l.suspended<>$2
RETURNING u.gift_id`, userID, frozen)
	if err != nil {
		return fmt.Errorf("suspend star gift listings: %w", err)
	}
	seen := make(map[int64]struct{})
	var giftIDs []int64
	for rows.Next() {
		var giftID int64
		if err := rows.Scan(&giftID); err != nil {
			rows.Close()
			return fmt.Errorf("scan suspended star gift listing: %w", err)
		}
		if _, duplicate := seen[giftID]; duplicate {
			continue
		}
		seen[giftID] = struct{}{}
		giftIDs = append(giftIDs, giftID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate suspended star gift listings: %w", err)
	}
	rows.Close()
	sort.Slice(giftIDs, func(i, j int) bool { return giftIDs[i] < giftIDs[j] })
	return withTx(ctx, db, "refresh resale projection after listing suspension", func(tx pgx.Tx) error {
		for _, giftID := range giftIDs {
			if err := updateStarGiftResaleProjection(ctx, tx, giftID); err != nil {
				return fmt.Errorf("refresh resale projection for gift %d: %w", giftID, err)
			}
		}
		return nil
	})
}

// rejectFrozenStarGiftSeller blocks listing a gift on the market while the seller's account is
// frozen. FOR SHARE serializes with the freeze write (account_restrictions.user_id is its PK): an
// overlapping freeze resolves before our commit, not after. Accounts that were never frozen have no
// row and pass through.
func rejectFrozenStarGiftSeller(ctx context.Context, db sqlcgen.DBTX, userID int64) error {
	var frozen bool
	err := db.QueryRow(ctx, `SELECT frozen FROM account_restrictions WHERE user_id=$1 FOR SHARE`, userID).Scan(&frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check star gift seller freeze: %w", err)
	}
	if frozen {
		return domain.ErrStarGiftResaleUnavailable
	}
	return nil
}
