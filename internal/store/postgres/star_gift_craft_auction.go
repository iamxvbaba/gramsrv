package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

const (
	starGiftAuctionRoundDuration = 3600
	maxStarGiftAuctionAcquired   = 1000
)

func defaultStarGiftCraftDraw(upper int) (int, error) {
	if upper <= 0 {
		return 0, domain.ErrStarGiftCraftUnavailable
	}
	draw, err := rand.Int(rand.Reader, big.NewInt(int64(upper)))
	if err != nil {
		return 0, err
	}
	return int(draw.Int64()), nil
}

// SweepStarGiftLifecycle advances time-driven aggregates without requiring a
// foreground client RPC. All effects remain local PostgreSQL ledger/message
// mutations; this worker never talks to TON, Fragment, wallets or chain nodes.
func (s *StarGiftLifecycleStore) SweepStarGiftLifecycle(ctx context.Context, now, limit int) error {
	if s == nil || s.db == nil || s.messages == nil || now <= 0 || limit <= 0 {
		return domain.ErrStarGiftUnavailable
	}
	if limit > 10000 {
		limit = 10000
	}
	// Payment forms are short-lived intents, not permanent receipts. Committed
	// purchases replay from star_gift_purchase_commands/private-send receipts,
	// so expired form rows can be removed independently in a bounded batch.
	formLimit := limit
	if formLimit > 1000 {
		formLimit = 1000
	}
	if _, err := s.db.Exec(ctx, `WITH stale AS (
SELECT buyer_user_id,form_id FROM star_gift_purchase_forms
WHERE expires_at<$1 ORDER BY expires_at,buyer_user_id,form_id
FOR UPDATE SKIP LOCKED LIMIT $2)
DELETE FROM star_gift_purchase_forms f USING stale
WHERE f.buyer_user_id=stale.buyer_user_id AND f.form_id=stale.form_id`, now, formLimit); err != nil {
		return err
	}
	remaining := limit
	for remaining > 0 {
		batch := remaining
		if batch > 100 {
			batch = 100
		}
		count, err := s.expireStarGiftOffersBatch(ctx, now, batch)
		if err != nil {
			return err
		}
		remaining -= count
		if count < batch {
			break
		}
	}
	for remaining > 0 {
		batch := remaining
		if batch > 100 {
			batch = 100
		}
		count, err := s.dispatchStarGiftOfferResolutions(ctx, batch)
		if err != nil {
			return err
		}
		remaining -= count
		if count < batch {
			break
		}
	}

	auctionLimit := remaining
	if auctionLimit > 100 {
		auctionLimit = 100
	}
	if auctionLimit > 0 {
		rows, err := s.db.Query(ctx, `SELECT gift_id FROM star_gift_auctions
WHERE (status='pending' AND start_date<=$1) OR
      (status='active' AND (next_round_at<=$1 OR end_date<=$1))
ORDER BY next_round_at,gift_id LIMIT $2`, now, auctionLimit)
		if err != nil {
			return err
		}
		giftIDs := make([]int64, 0, auctionLimit)
		for rows.Next() {
			var giftID int64
			if err := rows.Scan(&giftID); err != nil {
				rows.Close()
				return err
			}
			giftIDs = append(giftIDs, giftID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, giftID := range giftIDs {
			if err := s.settleStarGiftAuction(ctx, giftID, now); err != nil {
				return err
			}
			if err := s.dispatchStarGiftAuctionAwards(ctx, giftID); err != nil {
				return err
			}
		}
		remaining -= len(giftIDs)
	}

	// A prior process can commit award rows and stop before delivery. Drain those
	// rows even if their auction clock is no longer due.
	if remaining > 0 {
		rows, err := s.db.Query(ctx, `SELECT DISTINCT gift_id FROM star_gift_auction_acquired
WHERE saved_gift_id IS NULL ORDER BY gift_id LIMIT $1`, minAuctionInt(remaining, 100))
		if err != nil {
			return err
		}
		giftIDs := make([]int64, 0)
		for rows.Next() {
			var giftID int64
			if err := rows.Scan(&giftID); err != nil {
				rows.Close()
				return err
			}
			giftIDs = append(giftIDs, giftID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, giftID := range giftIDs {
			if err := s.dispatchStarGiftAuctionAwards(ctx, giftID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *StarGiftLifecycleStore) ListCraftStarGifts(ctx context.Context, userID, giftID int64, offset string, limit int) (domain.SavedStarGiftPage, error) {
	if s == nil || s.db == nil || userID <= 0 || giftID <= 0 || limit <= 0 || limit > domain.MaxSavedStarGiftsLimit || len(offset) > domain.MaxStarGiftsOffsetBytes {
		return domain.SavedStarGiftPage{}, domain.ErrStarGiftCraftUnavailable
	}
	args := []any{userID, giftID}
	where := `p.owner_peer_type='user' AND p.owner_peer_id=$1 AND p.gift_id=$2
	AND p.lifecycle_status='active' AND p.unique_gift_id IS NOT NULL AND p.can_craft_at<=EXTRACT(EPOCH FROM now())::integer
	AND NOT u.burned AND u.owner_address='' AND u.craft_chance_permille>0
	AND EXISTS (SELECT 1 FROM star_gift_collectible_models m
	            WHERE m.collectible_revision_id=u.collectible_revision_id AND m.crafted)`
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM peer_star_gifts p JOIN unique_star_gifts u ON u.id=p.unique_gift_id WHERE `+where, args...).Scan(&total); err != nil {
		return domain.SavedStarGiftPage{}, fmt.Errorf("count craft star gifts: %w", err)
	}
	if cursor, ok := domain.DecodeStarGiftCursor(offset); ok {
		args = append(args, cursor)
		where += fmt.Sprintf(" AND p.id<$%d", len(args))
	} else if offset != "" {
		return domain.SavedStarGiftPage{}, domain.ErrStarGiftCraftUnavailable
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, `SELECT p.id,p.owner_peer_type,p.owner_peer_id,p.from_user_id,p.gift_id,p.catalog_revision_id,
p.msg_id,p.saved_id,p.gift_date,p.name_hidden,p.unsaved,p.converted,p.convert_stars,p.prepaid_upgrade_stars,p.prepaid_upgrade_hash,p.gift_num,
p.lifecycle_status,p.transfer_stars,p.can_export_at,p.can_transfer_at,p.can_resell_at,p.drop_original_details_stars,p.can_craft_at,
p.message,COALESCE(p.unique_gift_id,0),p.upgrade_msg_id,p.pinned_order,
COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order,i.collection_id) FROM star_gift_collection_items i
JOIN star_gift_collections c ON c.collection_id=i.collection_id WHERE i.saved_gift_id=p.id),ARRAY[]::integer[])
FROM peer_star_gifts p JOIN unique_star_gifts u ON u.id=p.unique_gift_id WHERE `+where+`
ORDER BY p.id DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return domain.SavedStarGiftPage{}, fmt.Errorf("list craft star gifts: %w", err)
	}
	defer rows.Close()
	gifts := make([]domain.SavedStarGift, 0, limit+1)
	uniqueIDs := make([]int64, 0, limit+1)
	for rows.Next() {
		gift, scanErr := scanSavedStarGift(rows)
		if scanErr != nil {
			return domain.SavedStarGiftPage{}, scanErr
		}
		gifts = append(gifts, gift)
		uniqueIDs = append(uniqueIDs, gift.UniqueGiftID)
	}
	if err := rows.Err(); err != nil {
		return domain.SavedStarGiftPage{}, err
	}
	hasMore := len(gifts) > limit
	if hasMore {
		gifts, uniqueIDs = gifts[:limit], uniqueIDs[:limit]
	}
	uniqueByID, err := NewStarGiftStore(s.db).UniqueByIDs(ctx, uniqueIDs)
	if err != nil {
		return domain.SavedStarGiftPage{}, err
	}
	for i := range gifts {
		unique, ok := uniqueByID[gifts[i].UniqueGiftID]
		if !ok {
			return domain.SavedStarGiftPage{}, domain.ErrStarGiftCraftUnavailable
		}
		gifts[i].Unique = &unique
	}
	page := domain.SavedStarGiftPage{Count: total, Gifts: gifts}
	if hasMore && len(gifts) > 0 {
		page.NextOffset = domain.EncodeStarGiftCursor(gifts[len(gifts)-1].ID)
	}
	return page, nil
}

func (s *StarGiftLifecycleStore) CraftStarGift(ctx context.Context, req domain.StarGiftCraftRequest) (domain.StarGiftCraftResult, error) {
	if s == nil || s.db == nil || s.messages == nil || s.craftDraw == nil || req.UserID <= 0 || len(req.Refs) < 1 || len(req.Refs) > 4 || req.Date <= 0 ||
		strings.TrimSpace(req.CommandKey) == "" || len(req.CommandKey) > 256 {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}
	for _, ref := range req.Refs {
		if !ref.Valid() || ref.Owner != owner {
			return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
		}
	}
	// A committed failed craft has already moved every input out of the active
	// lifecycle. Consult the immutable receipt before active-gift resolution so
	// an exact transport retry can still replay the same terminal result.
	if replay, found, err := s.loadCraftReplay(ctx, req); err != nil || found {
		if err != nil || !replay.Success {
			return replay, err
		}
		return s.deliverCraftSuccess(ctx, req, replay)
	}
	savedIDs, err := NewStarGiftStore(s.db).ResolveSavedIDs(ctx, owner, req.Refs)
	if err != nil {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	if len(sortedUniqueInt64(savedIDs)) != len(savedIDs) {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	var result domain.StarGiftCraftResult
	var resultUniqueID int64
	err = withTx(ctx, s.db, "craft star gift", func(tx pgx.Tx) error {
		lockedRows, err := tx.Query(ctx, `SELECT id FROM peer_star_gifts WHERE id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`, sortedUniqueInt64(savedIDs))
		if err != nil {
			return err
		}
		locked := 0
		for lockedRows.Next() {
			locked++
		}
		lockedRows.Close()
		if locked != len(savedIDs) {
			return domain.ErrStarGiftCraftUnavailable
		}

		savedByID := make(map[int64]domain.SavedStarGift, len(savedIDs))
		uniqueIDs := make([]int64, 0, len(savedIDs))
		var giftID, revisionID int64
		chance := 0
		for i := range req.Refs {
			saved, err := lockSavedStarGiftByID(ctx, tx, savedIDs[i])
			if err != nil || !saved.LifecycleStatus.Live() || saved.UniqueGiftID == 0 || saved.CanCraftAt > req.Date {
				return domain.ErrStarGiftCraftUnavailable
			}
			unique, found, err := NewStarGiftStore(tx).UniqueByID(ctx, saved.UniqueGiftID)
			if err != nil || !found || unique.Owner != owner || unique.Burned || unique.OwnerAddress != "" || unique.CraftChancePermille <= 0 {
				return domain.ErrStarGiftCraftUnavailable
			}
			if giftID == 0 {
				giftID, revisionID = unique.GiftID, unique.CollectibleRevisionID
			} else if unique.GiftID != giftID || unique.CollectibleRevisionID != revisionID {
				return domain.ErrStarGiftCraftUnavailable
			}
			savedByID[saved.ID] = saved
			uniqueIDs = append(uniqueIDs, unique.ID)
			chance += unique.CraftChancePermille
		}
		if chance > 1000 {
			chance = 1000
		}
		var craftable bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM star_gift_collectible_models
WHERE collectible_revision_id=$1 AND crafted
)`, revisionID).Scan(&craftable); err != nil {
			return err
		}
		if !craftable {
			return domain.ErrStarGiftCraftUnavailable
		}
		draw, err := s.craftDraw(1000)
		if err != nil {
			return fmt.Errorf("draw star gift craft outcome: %w", err)
		}
		result.Chance = chance
		result.Success = draw < chance

		if _, err := tx.Exec(ctx, `SELECT id FROM unique_star_gifts WHERE id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`, sortedUniqueInt64(uniqueIDs)); err != nil {
			return err
		}
		// TDesktop deliberately keeps Craft available after an owner lists a
		// collectible. Consuming the gift therefore closes every market claim in
		// the same transaction: pending buyers are refunded before their offers
		// are cancelled, listings disappear, and the catalog resale projection is
		// refreshed before any input is crafted or burned.
		for _, uniqueID := range uniqueIDs {
			if err := s.refundPendingStarGiftOffers(ctx, tx, uniqueID, req.Date, "gift crafted"); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM star_gift_listings WHERE unique_gift_id=ANY($1::bigint[])`, uniqueIDs); err != nil {
			return err
		}
		if err := updateStarGiftResaleProjection(ctx, tx, giftID); err != nil {
			return err
		}
		for _, savedID := range savedIDs {
			saved := savedByID[savedID]
			if err := removeSavedGiftFromCollections(ctx, tx, saved.Owner, saved.ID); err != nil {
				return err
			}
		}

		firstSavedID, firstUniqueID := savedIDs[0], uniqueIDs[0]
		if result.Success {
			modelID, err := chooseCraftedModel(ctx, tx, revisionID)
			if err != nil {
				return err
			}
			patternID, err := chooseCollectibleAttribute(ctx, tx, "star_gift_collectible_patterns", revisionID)
			if err != nil {
				return err
			}
			backdropID, err := chooseCollectibleAttribute(ctx, tx, "star_gift_collectible_backdrops", revisionID)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET model_attribute_id=$2,pattern_attribute_id=$3,
backdrop_attribute_id=$4,crafted=true,craft_chance_permille=0,updated_at=now() WHERE id=$1`, firstUniqueID, modelID, patternID, backdropID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET can_craft_at=0 WHERE id=$1`, firstSavedID); err != nil {
				return err
			}
		}
		burnFrom := 0
		if result.Success {
			burnFrom = 1
		}
		if burnFrom < len(uniqueIDs) {
			if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET burned=true,craft_chance_permille=0,
offer_min_stars=0,updated_at=now() WHERE id=ANY($1::bigint[])`, uniqueIDs[burnFrom:]); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET lifecycle_status='burned',unsaved=true,pinned_order=0,
transfer_stars=0,can_export_at=0,can_transfer_at=0,can_resell_at=0,drop_original_details_stars=0,can_craft_at=0
WHERE id=ANY($1::bigint[])`, savedIDs[burnFrom:]); err != nil {
				return err
			}
		}
		sourceEdits, sourceEditPTS, err := s.markCraftInputMessagesTx(ctx, tx, req, savedIDs)
		if err != nil {
			return err
		}
		result.SourceEdits = sourceEdits
		var resultID any
		if result.Success {
			resultID = firstUniqueID
			resultUniqueID = firstUniqueID
		}
		_, err = tx.Exec(ctx, `INSERT INTO star_gift_craft_commands(user_id,command_key,input_unique_gift_ids,gift_id,
success,result_unique_gift_id,chance_permille,created_at,source_edit_pts) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, req.UserID,
			strings.TrimSpace(req.CommandKey), uniqueIDs, giftID, result.Success, resultID, chance, req.Date, sourceEditPTS)
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			if replay, found, replayErr := s.loadCraftReplay(ctx, req); replayErr != nil || found {
				return replay, replayErr
			}
		}
		return domain.StarGiftCraftResult{}, err
	}
	if result.Success {
		gift, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, resultUniqueID)
		if err != nil || !found {
			return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
		}
		result.Gift = &gift
	}
	if result.Success {
		return s.deliverCraftSuccess(ctx, req, result)
	}
	return result, nil
}

func (s *StarGiftLifecycleStore) deliverCraftSuccess(ctx context.Context, req domain.StarGiftCraftRequest, result domain.StarGiftCraftResult) (domain.StarGiftCraftResult, error) {
	if result.Gift == nil || s.messages == nil {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	saved, found, err := savedStarGiftByUniqueID(ctx, s.db, result.Gift.ID)
	if err != nil || !found || saved.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	sent, err := s.messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: req.UserID,
		RecipientUserID: req.UserID, RandomID: lifecycleCommandRandomID("craft", req.UserID, req.CommandKey), Date: req.Date,
		OriginAuthKeyID: req.OriginAuthKeyID, OriginSessionID: req.OriginSessionID, OriginUserID: req.UserID,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftUnique, StarGiftUnique: &domain.MessageStarGiftUniqueAction{
				Gift: *result.Gift, FromUserID: req.UserID, Peer: saved.Owner, Saved: !saved.Unsaved, Craft: true,
				CanExportAt: saved.CanExportAt, TransferStars: saved.TransferStars, CanTransferAt: saved.CanTransferAt,
				CanResellAt: saved.CanResellAt, DropOriginalDetailsStars: saved.DropOriginalDetailsStars,
				CanCraftAt: saved.CanCraftAt}}}})
	if err != nil {
		return domain.StarGiftCraftResult{}, err
	}
	result.Send = sent
	result.Duplicate = result.Duplicate || sent.Duplicate
	return result, nil
}

func (s *StarGiftLifecycleStore) loadCraftReplay(ctx context.Context, req domain.StarGiftCraftRequest) (domain.StarGiftCraftResult, bool, error) {
	var success bool
	var resultID *int64
	var chance int
	var inputUniqueIDs []int64
	var sourceEditPTS []int32
	err := s.db.QueryRow(ctx, `SELECT input_unique_gift_ids,success,result_unique_gift_id,chance_permille,source_edit_pts
FROM star_gift_craft_commands WHERE user_id=$1 AND command_key=$2`,
		req.UserID, strings.TrimSpace(req.CommandKey)).Scan(&inputUniqueIDs, &success, &resultID, &chance, &sourceEditPTS)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftCraftResult{}, false, nil
	}
	if err != nil {
		return domain.StarGiftCraftResult{}, false, err
	}
	if len(req.Refs) != len(inputUniqueIDs) || len(req.Refs) != len(sourceEditPTS) {
		return domain.StarGiftCraftResult{}, false, domain.ErrStarGiftCraftUnavailable
	}
	savedIDs := make([]int64, 0, len(inputUniqueIDs))
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}
	for i, uniqueID := range inputUniqueIDs {
		saved, found, err := savedStarGiftByUniqueID(ctx, s.db, uniqueID)
		if err != nil || !found || saved.Owner != owner || saved.UniqueGiftID != uniqueID {
			return domain.StarGiftCraftResult{}, false, domain.ErrStarGiftCraftUnavailable
		}
		ref := req.Refs[i]
		if ref.Owner != owner || ref.Slug == "" && ref.MsgID != saved.MsgID {
			return domain.StarGiftCraftResult{}, false, domain.ErrStarGiftCraftUnavailable
		}
		if ref.Slug != "" {
			unique, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, uniqueID)
			if err != nil || !found || !strings.EqualFold(ref.Slug, unique.Slug) {
				return domain.StarGiftCraftResult{}, false, domain.ErrStarGiftCraftUnavailable
			}
		}
		savedIDs = append(savedIDs, saved.ID)
	}
	sourceEdits, err := s.loadCraftInputMessageReplays(ctx, req, savedIDs, sourceEditPTS)
	if err != nil {
		return domain.StarGiftCraftResult{}, false, err
	}
	result := domain.StarGiftCraftResult{Success: success, Chance: chance, SourceEdits: sourceEdits, Duplicate: true}
	if resultID != nil {
		gift, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, *resultID)
		if err != nil || !found {
			return domain.StarGiftCraftResult{}, false, domain.ErrStarGiftCraftUnavailable
		}
		result.Gift = &gift
	}
	return result, true, nil
}

func chooseCraftedModel(ctx context.Context, tx pgx.Tx, revisionID int64) (int64, error) {
	var count int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_collectible_models WHERE collectible_revision_id=$1 AND crafted`, revisionID).Scan(&count); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, domain.ErrStarGiftCraftUnavailable
	}
	draw, err := rand.Int(rand.Reader, big.NewInt(count))
	if err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM star_gift_collectible_models WHERE collectible_revision_id=$1 AND crafted ORDER BY sort_order,id OFFSET $2 LIMIT 1`, revisionID, draw.Int64()).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// settleStarGiftAuction lazily advances every elapsed round. Auction rows are
// the clock aggregate; acquired rows are a durable delivery outbox. A winner's
// reserved bid is consumed, while bids that can no longer reach any remaining
// gift are refunded atomically with the state transition.
func (s *StarGiftLifecycleStore) settleStarGiftAuction(ctx context.Context, giftID int64, now int) error {
	if giftID <= 0 || now <= 0 {
		return domain.ErrStarGiftAuctionUnavailable
	}
	return withTx(ctx, s.db, "settle star gift auction", func(tx pgx.Tx) error {
		var startDate, endDate, roundDuration, giftsPerRound, totalRounds, currentRound, nextRoundAt, lastGiftNum, giftsLeft int
		var status string
		if err := tx.QueryRow(ctx, `SELECT start_date,end_date,round_duration,gifts_per_round,total_rounds,current_round,
next_round_at,last_gift_num,gifts_left,status FROM star_gift_auctions WHERE gift_id=$1 FOR UPDATE`, giftID).
			Scan(&startDate, &endDate, &roundDuration, &giftsPerRound, &totalRounds, &currentRound,
				&nextRoundAt, &lastGiftNum, &giftsLeft, &status); err != nil {
			return err
		}
		if status == "cancelled" || status == "completed" {
			return nil
		}
		if now < startDate {
			return nil
		}
		changed := false
		if status == "pending" {
			status = "active"
			changed = true
			if currentRound == 0 {
				currentRound = 1
			}
		}
		awardedCount := 0
		for status == "active" && currentRound <= totalRounds && nextRoundAt <= now {
			awardLimit := giftsPerRound
			if awardLimit > giftsLeft {
				awardLimit = giftsLeft
			}
			type winner struct {
				userID, recipientID, amount int64
				recipientType               string
				bidDate                     int
				hide                        bool
				message                     string
			}
			winners := make([]winner, 0, awardLimit)
			if awardLimit > 0 {
				rows, err := tx.Query(ctx, `SELECT bidder_user_id,recipient_peer_type,recipient_peer_id,amount,bid_date,hide_name,message
FROM star_gift_auction_bids WHERE gift_id=$1 AND active ORDER BY amount DESC,bid_date,bidder_user_id LIMIT $2 FOR UPDATE`, giftID, awardLimit)
				if err != nil {
					return err
				}
				for rows.Next() {
					var winner winner
					if err := rows.Scan(&winner.userID, &winner.recipientType, &winner.recipientID, &winner.amount,
						&winner.bidDate, &winner.hide, &winner.message); err != nil {
						rows.Close()
						return err
					}
					winners = append(winners, winner)
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					return err
				}
				rows.Close()
			}
			if len(winners) == 0 {
				// No active bid can produce an award in any elapsed round. Fast-forward
				// the clock aggregate instead of looping once per (possibly very large)
				// official supply round after a long process outage.
				through := now
				if through > endDate {
					through = endDate
				}
				dueRounds := (through-nextRoundAt)/roundDuration + 1
				remainingRounds := totalRounds - currentRound + 1
				if dueRounds > remainingRounds {
					dueRounds = remainingRounds
				}
				if dueRounds < 1 {
					dueRounds = 1
				}
				currentRound += dueRounds
				nextRoundAt += dueRounds * roundDuration
				changed = true
				if currentRound > totalRounds || nextRoundAt > endDate {
					status = "completed"
				}
				continue
			}
			for pos, winner := range winners {
				giftNum := lastGiftNum + pos + 1
				if _, err := tx.Exec(ctx, `INSERT INTO star_gift_auction_acquired(gift_id,bidder_user_id,recipient_peer_type,
recipient_peer_id,bid_amount,round,pos,gift_num,acquired_at,hide_name,message)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(gift_id,round,pos) DO NOTHING`,
					giftID, winner.userID, winner.recipientType, winner.recipientID, winner.amount, currentRound, pos+1,
					giftNum, nextRoundAt, winner.hide, winner.message); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE star_gift_auction_bids SET active=false,returned=false,
acquired_count=acquired_count+1,version=version+1 WHERE gift_id=$1 AND bidder_user_id=$2 AND active`, giftID, winner.userID); err != nil {
					return err
				}
			}
			lastGiftNum += len(winners)
			giftsLeft -= len(winners)
			awardedCount += len(winners)
			currentRound++
			nextRoundAt += roundDuration
			changed = true
			if currentRound > totalRounds || giftsLeft <= 0 || nextRoundAt > endDate {
				status = "completed"
			}
		}
		if status == "active" && now >= endDate {
			status = "completed"
			changed = true
		}
		// Any active rank beyond all remaining gifts can never win, even after
		// higher bids are consumed in later rounds, and is therefore refundable.
		refundAll := status == "completed" || giftsLeft <= 0
		if err := s.refundUnreachableAuctionBids(ctx, tx, giftID, giftsLeft, refundAll, now); err != nil {
			return err
		}
		if changed {
			if _, err := tx.Exec(ctx, `UPDATE star_gift_auctions SET status=$2,current_round=$3,next_round_at=$4,
last_gift_num=$5,gifts_left=$6,version=version+1,updated_at=now() WHERE gift_id=$1`, giftID, status,
				minAuctionInt(currentRound, totalRounds), minAuctionInt(nextRoundAt, endDate), lastGiftNum, giftsLeft); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE star_gift_catalog SET availability_remains=$2,last_sale_date=CASE WHEN $3>0 THEN $4 ELSE last_sale_date END,
first_sale_date=CASE WHEN first_sale_date=0 AND $3>0 THEN $4 ELSE first_sale_date END,updated_at=now() WHERE gift_id=$1`,
				giftID, giftsLeft, awardedCount, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *StarGiftLifecycleStore) refundUnreachableAuctionBids(ctx context.Context, tx pgx.Tx, giftID int64, giftsLeft int, all bool, date int) error {
	offset := giftsLeft
	if all {
		offset = 0
	}
	rows, err := tx.Query(ctx, `SELECT bidder_user_id,recipient_peer_type,recipient_peer_id,amount FROM star_gift_auction_bids
WHERE gift_id=$1 AND active ORDER BY amount DESC,bid_date,bidder_user_id OFFSET $2 FOR UPDATE`, giftID, offset)
	if err != nil {
		return err
	}
	type refundable struct {
		userID, peerID, amount int64
		peerType               string
	}
	items := make([]refundable, 0)
	for rows.Next() {
		var item refundable
		if err := rows.Scan(&item.userID, &item.peerType, &item.peerID, &item.amount); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if err := s.creditLifecycleAmount(ctx, tx, item.userID,
			domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: item.amount},
			domain.StarsReasonGiftAuction, domain.Peer{Type: domain.PeerType(item.peerType), ID: item.peerID}, date,
			"Star gift auction bid refund"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE star_gift_auction_bids SET active=false,returned=true,version=version+1
WHERE gift_id=$1 AND bidder_user_id=$2 AND active`, giftID, item.userID); err != nil {
			return err
		}
	}
	return nil
}

func minAuctionInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *StarGiftLifecycleStore) dispatchStarGiftAuctionAwards(ctx context.Context, giftID int64) error {
	if s.messages == nil {
		return domain.ErrStarGiftAuctionUnavailable
	}
	gift, found, err := NewStarGiftStore(s.db).CatalogGift(ctx, giftID)
	if err != nil || !found {
		return domain.ErrStarGiftAuctionUnavailable
	}
	dispatched := 0
	for dispatched < maxStarGiftAuctionAcquired {
		rows, err := s.db.Query(ctx, `SELECT id,bidder_user_id,recipient_peer_type,recipient_peer_id,bid_amount,
round,pos,COALESCE(gift_num,0),acquired_at,hide_name,message FROM star_gift_auction_acquired
WHERE gift_id=$1 AND saved_gift_id IS NULL ORDER BY id LIMIT 100`, giftID)
		if err != nil {
			return err
		}
		items := make([]struct {
			id, bidder, recipientID, amount int64
			recipientType                   string
			round, pos, giftNum, date       int
			hide                            bool
			message                         string
		}, 0)
		for rows.Next() {
			var item struct {
				id, bidder, recipientID, amount int64
				recipientType                   string
				round, pos, giftNum, date       int
				hide                            bool
				message                         string
			}
			if err := rows.Scan(&item.id, &item.bidder, &item.recipientType, &item.recipientID, &item.amount,
				&item.round, &item.pos, &item.giftNum, &item.date, &item.hide, &item.message); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			owner := domain.Peer{Type: domain.PeerType(item.recipientType), ID: item.recipientID}
			var msgID int
			if owner.Type == domain.PeerTypeUser {
				sticker := gift.Sticker
				sent, err := s.messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: item.bidder,
					RecipientUserID: owner.ID, RandomID: lifecycleCommandRandomID("auction-award", giftID, item.round, item.pos),
					Date: item.date, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
						Kind: domain.MessageServiceActionStarGift, StarGift: &domain.MessageStarGiftAction{GiftID: gift.ID,
							Stars: gift.Stars, ConvertStars: 0, Title: gift.Title, Sticker: &sticker, Message: item.message,
							FromUserID: item.bidder, PeerUserID: owner.ID, To: owner, NameHidden: item.hide, Saved: true,
							AuctionAcquired: true, GiftNum: item.giftNum}}}})
				if err != nil {
					return err
				}
				msgID = sent.RecipientMessage.ID
				if msgID <= 0 {
					msgID = sent.SenderMessage.ID
				}
			}
			if err := withTx(ctx, s.db, "save star gift auction award", func(tx pgx.Tx) error {
				var savedID *int64
				if err := tx.QueryRow(ctx, `SELECT saved_gift_id FROM star_gift_auction_acquired WHERE id=$1 FOR UPDATE`, item.id).Scan(&savedID); err != nil {
					return err
				}
				if savedID != nil {
					return nil
				}
				id, err := NewStarGiftStore(tx).Create(ctx, domain.SavedStarGift{Owner: owner, FromUserID: item.bidder,
					GiftID: gift.ID, RevisionID: gift.RevisionID, MsgID: msgID, Date: item.date, NameHidden: item.hide,
					ConvertStars: 0, Message: item.message, GiftNum: item.giftNum})
				if err != nil {
					return err
				}
				if owner.Type == domain.PeerTypeChannel {
					sticker := gift.Sticker
					action := domain.ChannelMessageAction{Type: domain.ChannelActionStarGift, StarGift: &domain.MessageStarGiftAction{
						GiftID: gift.ID, Stars: gift.Stars, ConvertStars: 0, Title: gift.Title, Sticker: &sticker,
						Message: item.message, FromUserID: item.bidder, PeerChannelID: owner.ID, SavedID: id,
						NameHidden: item.hide, Saved: true, AuctionAcquired: true, GiftNum: item.giftNum,
					}}
					if err := NewChannelStore(tx).appendStarGiftAdminLogTx(ctx, tx, owner.ID, item.bidder, id, item.date, action); err != nil {
						return err
					}
				}
				_, err = tx.Exec(ctx, `UPDATE star_gift_auction_acquired SET saved_gift_id=$2 WHERE id=$1`, item.id, id)
				return err
			}); err != nil {
				return err
			}
			dispatched++
		}
	}
	return nil
}

func (s *StarGiftLifecycleStore) StarGiftAuctionState(ctx context.Context, userID int64, giftID int64, slug string, now int) (domain.StarGiftAuction, error) {
	if s == nil || s.db == nil || userID <= 0 || now <= 0 || giftID <= 0 && strings.TrimSpace(slug) == "" {
		return domain.StarGiftAuction{}, domain.ErrStarGiftAuctionUnavailable
	}
	resolvedGiftID, err := s.ensureStarGiftAuction(ctx, giftID, strings.TrimSpace(slug), now)
	if err != nil {
		return domain.StarGiftAuction{}, err
	}
	if err := s.settleStarGiftAuction(ctx, resolvedGiftID, now); err != nil {
		return domain.StarGiftAuction{}, err
	}
	if err := s.dispatchStarGiftAuctionAwards(ctx, resolvedGiftID); err != nil {
		return domain.StarGiftAuction{}, err
	}
	return s.loadStarGiftAuction(ctx, userID, resolvedGiftID)
}

func (s *StarGiftLifecycleStore) ActiveStarGiftAuctions(ctx context.Context, userID int64, now int) ([]domain.StarGiftAuction, error) {
	if userID <= 0 || now <= 0 {
		return nil, domain.ErrStarGiftAuctionUnavailable
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT a.gift_id FROM star_gift_auctions a
JOIN star_gift_auction_bids b ON b.gift_id=a.gift_id
WHERE b.bidder_user_id=$1 AND a.end_date>$2 AND a.status<>'cancelled' ORDER BY a.gift_id`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	giftIDs := make([]int64, 0)
	for rows.Next() {
		var giftID int64
		if err := rows.Scan(&giftID); err != nil {
			return nil, err
		}
		giftIDs = append(giftIDs, giftID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.StarGiftAuction, 0)
	for _, giftID := range giftIDs {
		state, err := s.StarGiftAuctionState(ctx, userID, giftID, "", now)
		if err != nil {
			return nil, err
		}
		if !state.Finished && state.UserState.BidDate > 0 {
			out = append(out, state)
		}
	}
	return out, nil
}

func (s *StarGiftLifecycleStore) StarGiftAuctionAcquired(ctx context.Context, userID, giftID int64) ([]domain.StarGiftAuctionAcquired, error) {
	if userID <= 0 || giftID <= 0 {
		return nil, domain.ErrStarGiftAuctionUnavailable
	}
	if err := s.dispatchStarGiftAuctionAwards(ctx, giftID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT recipient_peer_type,recipient_peer_id,acquired_at,bid_amount,round,pos,message,
COALESCE(gift_num,0),hide_name FROM star_gift_auction_acquired WHERE bidder_user_id=$1 AND gift_id=$2 ORDER BY id DESC LIMIT $3`,
		userID, giftID, maxStarGiftAuctionAcquired)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.StarGiftAuctionAcquired, 0)
	for rows.Next() {
		var item domain.StarGiftAuctionAcquired
		var peerType string
		if err := rows.Scan(&peerType, &item.Peer.ID, &item.Date, &item.BidAmount, &item.Round, &item.Pos,
			&item.Message, &item.GiftNum, &item.NameHidden); err != nil {
			return nil, err
		}
		item.Peer.Type = domain.PeerType(peerType)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *StarGiftLifecycleStore) BidStarGiftAuction(ctx context.Context, req domain.StarGiftAuctionBidRequest) (domain.StarGiftAuction, domain.StarsBalance, error) {
	if s == nil || s.db == nil || req.UserID <= 0 || req.GiftID <= 0 || !validLifecyclePeer(req.Peer) ||
		req.BidAmount <= 0 || req.FormID == 0 || req.Date <= 0 || len([]rune(req.Message)) > 128 {
		return domain.StarGiftAuction{}, domain.StarsBalance{}, domain.ErrStarGiftAuctionUnavailable
	}
	if _, err := s.ensureStarGiftAuction(ctx, req.GiftID, "", req.Date); err != nil {
		return domain.StarGiftAuction{}, domain.StarsBalance{}, err
	}
	if err := s.settleStarGiftAuction(ctx, req.GiftID, req.Date); err != nil {
		return domain.StarGiftAuction{}, domain.StarsBalance{}, err
	}
	if balance, found, err := s.loadAuctionBidReplay(ctx, req.UserID, req.FormID, req.GiftID); err != nil || found {
		if err != nil {
			return domain.StarGiftAuction{}, domain.StarsBalance{}, err
		}
		state, stateErr := s.loadStarGiftAuction(ctx, req.UserID, req.GiftID)
		return state, balance, stateErr
	}
	var balance domain.StarsBalance
	err := withTx(ctx, s.db, "bid star gift auction", func(tx pgx.Tx) error {
		var startDate, endDate int
		var minimum int64
		var status string
		if err := tx.QueryRow(ctx, `SELECT start_date,end_date,min_bid_amount,status FROM star_gift_auctions WHERE gift_id=$1 FOR UPDATE`, req.GiftID).
			Scan(&startDate, &endDate, &minimum, &status); err != nil {
			return err
		}
		if status != "active" || req.Date < startDate || req.Date >= endDate || req.BidAmount < minimum {
			return domain.ErrStarGiftAuctionUnavailable
		}
		var oldAmount int64
		var oldActive bool
		err := tx.QueryRow(ctx, `SELECT amount,active FROM star_gift_auction_bids WHERE gift_id=$1 AND bidder_user_id=$2 FOR UPDATE`, req.GiftID, req.UserID).Scan(&oldAmount, &oldActive)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if oldActive && (!req.UpdateBid || req.BidAmount <= oldAmount) || !oldActive && req.UpdateBid {
			return domain.ErrStarGiftAuctionUnavailable
		}
		reserved := int64(0)
		if oldActive {
			reserved = oldAmount
		}
		delta := req.BidAmount - reserved
		balance, err = s.debitLifecycleAmount(ctx, tx, req.UserID,
			domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: delta}, domain.StarsReasonGiftAuction,
			req.Peer, req.Date, "Star gift auction bid")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_auction_bids(gift_id,bidder_user_id,recipient_peer_type,recipient_peer_id,
amount,bid_date,hide_name,message) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(gift_id,bidder_user_id) DO UPDATE SET
recipient_peer_type=CASE WHEN star_gift_auction_bids.active THEN star_gift_auction_bids.recipient_peer_type ELSE EXCLUDED.recipient_peer_type END,
recipient_peer_id=CASE WHEN star_gift_auction_bids.active THEN star_gift_auction_bids.recipient_peer_id ELSE EXCLUDED.recipient_peer_id END,
amount=EXCLUDED.amount,bid_date=EXCLUDED.bid_date,
hide_name=CASE WHEN star_gift_auction_bids.active THEN star_gift_auction_bids.hide_name ELSE EXCLUDED.hide_name END,
message=CASE WHEN star_gift_auction_bids.active THEN star_gift_auction_bids.message ELSE EXCLUDED.message END,
returned=false,active=true,version=star_gift_auction_bids.version+1`,
			req.GiftID, req.UserID, string(req.Peer.Type), req.Peer.ID, req.BidAmount, req.Date, req.HideName, req.Message); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_auction_bid_payments(user_id,form_id,gift_id,bid_amount,balance_after,created_at)
VALUES($1,$2,$3,$4,$5,$6)`, req.UserID, req.FormID, req.GiftID, req.BidAmount, balance.Balance, req.Date); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE star_gift_auctions SET version=version+1,updated_at=now() WHERE gift_id=$1`, req.GiftID)
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			if replayBalance, found, replayErr := s.loadAuctionBidReplay(ctx, req.UserID, req.FormID, req.GiftID); replayErr != nil || found {
				state, stateErr := s.loadStarGiftAuction(ctx, req.UserID, req.GiftID)
				if replayErr != nil {
					return domain.StarGiftAuction{}, domain.StarsBalance{}, replayErr
				}
				return state, replayBalance, stateErr
			}
		}
		return domain.StarGiftAuction{}, domain.StarsBalance{}, err
	}
	state, err := s.loadStarGiftAuction(ctx, req.UserID, req.GiftID)
	return state, balance, err
}

func (s *StarGiftLifecycleStore) ensureStarGiftAuction(ctx context.Context, giftID int64, slug string, now int) (int64, error) {
	if giftID == 0 {
		if err := s.db.QueryRow(ctx, `SELECT gift_id FROM star_gift_catalog_revisions WHERE auction AND auction_slug=$1 ORDER BY id DESC LIMIT 1`, slug).Scan(&giftID); err != nil {
			return 0, domain.ErrStarGiftAuctionUnavailable
		}
	}
	gift, found, err := NewStarGiftStore(s.db).CatalogGift(ctx, giftID)
	if err != nil || !found || !gift.Auction || gift.GiftsPerRound <= 0 || gift.AuctionSlug == "" {
		return 0, domain.ErrStarGiftAuctionUnavailable
	}
	if slug != "" && slug != gift.AuctionSlug {
		return 0, domain.ErrStarGiftAuctionUnavailable
	}
	supply := gift.AvailabilityTotal
	if supply <= 0 {
		supply = gift.UpgradeTotal
	}
	if supply <= 0 {
		return 0, domain.ErrStarGiftAuctionUnavailable
	}
	start := gift.AuctionStartDate
	if start <= 0 {
		start = now
	}
	totalRounds := (supply + gift.GiftsPerRound - 1) / gift.GiftsPerRound
	end := start + totalRounds*starGiftAuctionRoundDuration
	status := "pending"
	currentRound := 0
	if now >= start {
		status, currentRound = "active", 1
	}
	_, err = s.db.Exec(ctx, `INSERT INTO star_gift_auctions(gift_id,slug,start_date,end_date,round_duration,gifts_per_round,
total_rounds,current_round,next_round_at,gifts_left,min_bid_amount,status)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(gift_id) DO NOTHING`, gift.ID, gift.AuctionSlug,
		start, end, starGiftAuctionRoundDuration, gift.GiftsPerRound, totalRounds, currentRound,
		start+starGiftAuctionRoundDuration, supply, maxInt64(1, gift.Stars), status)
	if err != nil {
		return 0, err
	}
	return gift.ID, nil
}

func (s *StarGiftLifecycleStore) loadStarGiftAuction(ctx context.Context, userID, giftID int64) (domain.StarGiftAuction, error) {
	gift, found, err := NewStarGiftStore(s.db).CatalogGift(ctx, giftID)
	if err != nil || !found {
		return domain.StarGiftAuction{}, domain.ErrStarGiftAuctionUnavailable
	}
	out := domain.StarGiftAuction{Gift: gift}
	var status string
	if err := s.db.QueryRow(ctx, `SELECT version,start_date,end_date,min_bid_amount,next_round_at,last_gift_num,gifts_left,
current_round,total_rounds,round_duration,status FROM star_gift_auctions WHERE gift_id=$1`, giftID).Scan(&out.Version, &out.StartDate,
		&out.EndDate, &out.MinBidAmount, &out.NextRoundAt, &out.LastGiftNum, &out.GiftsLeft, &out.CurrentRound,
		&out.TotalRounds, &out.RoundDuration, &status); err != nil {
		return domain.StarGiftAuction{}, err
	}
	out.Finished = status == "completed" || status == "cancelled"
	if out.Finished {
		if err := s.db.QueryRow(ctx, `SELECT COALESCE(AVG(bid_amount)::bigint,0) FROM star_gift_auction_acquired WHERE gift_id=$1`, giftID).
			Scan(&out.AveragePrice); err != nil {
			return domain.StarGiftAuction{}, err
		}
		if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_listings l JOIN unique_star_gifts u ON u.id=l.unique_gift_id WHERE u.gift_id=$1`, giftID).
			Scan(&out.ListedCount); err != nil {
			return domain.StarGiftAuction{}, err
		}
	}
	rows, err := s.db.Query(ctx, `SELECT amount,bid_date FROM star_gift_auction_bids WHERE gift_id=$1 AND active
ORDER BY amount DESC,bid_date,bidder_user_id LIMIT 20`, giftID)
	if err != nil {
		return domain.StarGiftAuction{}, err
	}
	for rows.Next() {
		var level domain.StarGiftAuctionBidLevel
		level.Pos = len(out.BidLevels) + 1
		if err := rows.Scan(&level.Amount, &level.Date); err != nil {
			rows.Close()
			return domain.StarGiftAuction{}, err
		}
		out.BidLevels = append(out.BidLevels, level)
	}
	rows.Close()
	topRows, err := s.db.Query(ctx, `SELECT bidder_user_id FROM star_gift_auction_bids WHERE gift_id=$1 AND active
ORDER BY amount DESC,bid_date,bidder_user_id LIMIT 3`, giftID)
	if err != nil {
		return domain.StarGiftAuction{}, err
	}
	for topRows.Next() {
		var id int64
		if err := topRows.Scan(&id); err != nil {
			topRows.Close()
			return domain.StarGiftAuction{}, err
		}
		out.TopBidders = append(out.TopBidders, id)
	}
	topRows.Close()
	var peerType string
	var active bool
	err = s.db.QueryRow(ctx, `SELECT returned,active,amount,bid_date,recipient_peer_type,recipient_peer_id,acquired_count
FROM star_gift_auction_bids WHERE gift_id=$1 AND bidder_user_id=$2`, giftID, userID).Scan(&out.UserState.Returned,
		&active, &out.UserState.BidAmount, &out.UserState.BidDate, &peerType, &out.UserState.BidPeer.ID, &out.UserState.AcquiredCount)
	if err == nil {
		if active || out.UserState.Returned {
			out.UserState.BidPeer.Type = domain.PeerType(peerType)
			out.UserState.MinBidAmount = out.UserState.BidAmount + 1
		} else {
			out.UserState.BidAmount, out.UserState.BidDate, out.UserState.BidPeer = 0, 0, domain.Peer{}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftAuction{}, err
	}
	return out, nil
}

func (s *StarGiftLifecycleStore) loadAuctionBidReplay(ctx context.Context, userID, formID, giftID int64) (domain.StarsBalance, bool, error) {
	var storedGiftID, balance int64
	err := s.db.QueryRow(ctx, `SELECT gift_id,balance_after FROM star_gift_auction_bid_payments WHERE user_id=$1 AND form_id=$2`, userID, formID).
		Scan(&storedGiftID, &balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarsBalance{}, false, nil
	}
	if err != nil {
		return domain.StarsBalance{}, false, err
	}
	if storedGiftID != giftID {
		return domain.StarsBalance{}, false, domain.ErrStarGiftAuctionUnavailable
	}
	return domain.StarsBalance{UserID: userID, Balance: balance}, true, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
