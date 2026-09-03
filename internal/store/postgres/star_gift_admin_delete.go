package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// errStarGiftDeletionDryRun forces the tx below to roll back after computing
// a real preview -- every count and compensation figure it returns reflects
// the actual queries a real run would use, just never committed.
var errStarGiftDeletionDryRun = errors.New("stargift: deletion dry run")

// DeleteStarGiftEverywhere is the admin panel's "delete gift" action: the
// catalog entry is disabled (same effect as SetCatalogEnabled(false), so it
// can never be re-enabled by accident through the normal toggle), every
// currently-active instance is removed from its owner's inventory, every
// live listing for it is delisted, and every pending offer against it is
// cancelled with its escrow refunded through the existing offer-refund path.
//
// It never touches: the official gift snapshot (officialgifts source data --
// so the same gift can be re-imported later), or any audit/ledger table
// (stars_transactions, admin_commands, purchase/transfer/upgrade command
// logs) -- those are exactly what compensation below reads from, and a
// financial trail is never deleted.
//
// An already-upgraded (unique_gift_id set) instance is burned, reusing the
// same store-level effect as the existing craft-burn path. A regular
// (never-upgraded) instance is marked 'revoked' (see migration 0208) --
// there is no existing terminal state for that case.
//
// refund, when true, credits Stars to every user who ever paid for this
// gift through a purchase, a transfer, or an upgrade -- one sum per
// transaction kind, per user, read straight from the command tables that
// record what was actually charged (star_gift_purchase_commands,
// star_gift_transfer_commands, star_gift_upgrade_commands). This is
// deliberately by PAID transaction, not by current holder: a user who paid
// to upgrade a copy and later transferred it away still paid that Stars
// cost, and the same gift disappearing under a later holder does not refund
// the earlier payer twice -- each command row is credited exactly once.
//
// dryRun runs every mutation and every compensation query below inside the
// same transaction and then forces a rollback -- the returned result is a
// real preview (actual counts, actual Stars figures), nothing is persisted.
func (s *StarGiftLifecycleStore) DeleteStarGiftEverywhere(ctx context.Context, giftID int64, refund, dryRun bool, date int) (domain.StarGiftDeletionResult, error) {
	result := domain.StarGiftDeletionResult{GiftID: giftID}
	if giftID <= 0 {
		return result, domain.ErrStarGiftNotFound
	}
	err := withTx(ctx, s.db, "delete star gift everywhere", func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM star_gift_catalog WHERE gift_id=$1 FOR UPDATE)`, giftID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.ErrStarGiftNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE star_gift_catalog SET enabled=false, updated_at=now() WHERE gift_id=$1`, giftID); err != nil {
			return err
		}

		// Every unique instance ever minted from this gift (burned or not --
		// listings/offers can still reference an already-burned one, so the
		// cleanup below must not miss it) drives the listing/offer cleanup.
		uniqueRows, err := tx.Query(ctx, `SELECT id FROM unique_star_gifts WHERE gift_id=$1`, giftID)
		if err != nil {
			return err
		}
		uniqueIDs := make([]int64, 0)
		for uniqueRows.Next() {
			var id int64
			if err := uniqueRows.Scan(&id); err != nil {
				uniqueRows.Close()
				return err
			}
			uniqueIDs = append(uniqueIDs, id)
		}
		if err := uniqueRows.Err(); err != nil {
			return err
		}
		uniqueRows.Close()

		if len(uniqueIDs) > 0 {
			ct, err := tx.Exec(ctx, `DELETE FROM star_gift_listings WHERE unique_gift_id = ANY($1::bigint[])`, uniqueIDs)
			if err != nil {
				return err
			}
			result.CancelledListings = int(ct.RowsAffected())
			for _, uniqueID := range uniqueIDs {
				before, err := pendingOfferCount(ctx, tx, uniqueID)
				if err != nil {
					return err
				}
				if before == 0 {
					continue
				}
				if err := s.refundPendingStarGiftOffers(ctx, tx, uniqueID, date, "gift deleted by admin"); err != nil {
					return err
				}
				result.CancelledOffers += before
			}
		}

		// Burn every still-active upgraded instance -- same store-level
		// effect as the existing craft-burn path.
		burnTag, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET burned=true, craft_chance_permille=0,
offer_min_stars=0, updated_at=now() WHERE gift_id=$1 AND NOT burned`, giftID)
		if err != nil {
			return err
		}
		_ = burnTag
		burnedInstances, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET lifecycle_status='burned', unsaved=true, pinned_order=0,
transfer_stars=0, can_export_at=0, can_transfer_at=0, can_resell_at=0, drop_original_details_stars=0, can_craft_at=0
WHERE gift_id=$1 AND lifecycle_status='active' AND unique_gift_id IS NOT NULL`, giftID)
		if err != nil {
			return err
		}
		result.RevokedInstances += int(burnedInstances.RowsAffected())

		// Revoke every still-active regular (never-upgraded) instance.
		revokedInstances, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET lifecycle_status='revoked', unsaved=true, pinned_order=0
WHERE gift_id=$1 AND lifecycle_status='active' AND unique_gift_id IS NULL`, giftID)
		if err != nil {
			return err
		}
		result.RevokedInstances += int(revokedInstances.RowsAffected())

		// Always compute the compensation breakdown -- shown in the preview
		// regardless of the refund toggle, so an operator can see the cost
		// before deciding to turn it on. Only actually credited when refund
		// is true.
		compensation, err := starGiftDeletionCompensation(ctx, tx, giftID)
		if err != nil {
			return err
		}
		result.Compensation = compensation
		if refund {
			for _, c := range compensation {
				total := c.Total()
				if total <= 0 {
					continue
				}
				if !dryRun {
					if err := s.creditLifecycleAmount(ctx, tx, c.UserID, domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: total},
						domain.StarsReasonAdjust, domain.Peer{}, date, "Gift removed from catalog: refund"); err != nil {
						return err
					}
				}
				result.TotalCompensation += total
			}
			result.Refunded = true
		}
		if dryRun {
			return errStarGiftDeletionDryRun
		}
		return nil
	})
	if dryRun && errors.Is(err, errStarGiftDeletionDryRun) {
		err = nil
	}
	return result, err
}

func pendingOfferCount(ctx context.Context, tx pgx.Tx, uniqueID int64) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM star_gift_offers WHERE unique_gift_id=$1 AND status='pending'`, uniqueID).Scan(&n)
	return n, err
}

// starGiftDeletionCompensation sums, per user, exactly what was ever charged
// for this gift across the three command kinds the operator asked to cover:
// a catalog purchase, a paid transfer, and a paid upgrade. Reading straight
// from the command tables (not stars_transactions, which carries no gift_id)
// is what scopes this to the one gift being deleted instead of every gift
// transaction the user ever made.
func starGiftDeletionCompensation(ctx context.Context, tx pgx.Tx, giftID int64) ([]domain.StarGiftDeletionCompensation, error) {
	byUser := make(map[int64]*domain.StarGiftDeletionCompensation)
	get := func(userID int64) *domain.StarGiftDeletionCompensation {
		c, ok := byUser[userID]
		if !ok {
			c = &domain.StarGiftDeletionCompensation{UserID: userID}
			byUser[userID] = c
		}
		return c
	}

	purchaseRows, err := tx.Query(ctx, `SELECT buyer_user_id, sum(charge_stars) FROM star_gift_purchase_commands
WHERE gift_id=$1 GROUP BY buyer_user_id`, giftID)
	if err != nil {
		return nil, err
	}
	for purchaseRows.Next() {
		var userID, amount int64
		if err := purchaseRows.Scan(&userID, &amount); err != nil {
			purchaseRows.Close()
			return nil, err
		}
		get(userID).Purchase = amount
	}
	if err := purchaseRows.Err(); err != nil {
		return nil, err
	}
	purchaseRows.Close()

	transferRows, err := tx.Query(ctx, `SELECT t.actor_user_id, sum(t.charge_stars) FROM star_gift_transfer_commands t
JOIN unique_star_gifts u ON u.id = t.unique_gift_id
WHERE u.gift_id=$1 AND t.charge_stars > 0 GROUP BY t.actor_user_id`, giftID)
	if err != nil {
		return nil, err
	}
	for transferRows.Next() {
		var userID, amount int64
		if err := transferRows.Scan(&userID, &amount); err != nil {
			transferRows.Close()
			return nil, err
		}
		get(userID).Transfer = amount
	}
	if err := transferRows.Err(); err != nil {
		return nil, err
	}
	transferRows.Close()

	upgradeRows, err := tx.Query(ctx, `SELECT g.user_id, sum(g.charge_stars) FROM star_gift_upgrade_commands g
JOIN unique_star_gifts u ON u.id = g.unique_gift_id
WHERE u.gift_id=$1 AND g.charge_stars > 0 GROUP BY g.user_id`, giftID)
	if err != nil {
		return nil, err
	}
	for upgradeRows.Next() {
		var userID, amount int64
		if err := upgradeRows.Scan(&userID, &amount); err != nil {
			upgradeRows.Close()
			return nil, err
		}
		get(userID).Upgrade = amount
	}
	if err := upgradeRows.Err(); err != nil {
		return nil, err
	}
	upgradeRows.Close()

	out := make([]domain.StarGiftDeletionCompensation, 0, len(byUser))
	for _, c := range byUser {
		out = append(out, *c)
	}
	return out, nil
}
