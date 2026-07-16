package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// StarGiftUpgradeStore is the PostgreSQL aggregate coordinator for collectible
// upgrades. It intentionally shares MessageStore's allocator and transaction
// machinery so Stars, issuance, the saved gift and durable updates commit once.
type StarGiftUpgradeStore struct {
	db       sqlcgen.DBTX
	messages *MessageStore
}

func NewStarGiftUpgradeStore(db sqlcgen.DBTX, messages *MessageStore) *StarGiftUpgradeStore {
	return &StarGiftUpgradeStore{db: db, messages: messages}
}

func (s *StarGiftUpgradeStore) UpgradeStarGift(ctx context.Context, req domain.StarGiftUpgradeRequest) (domain.StarGiftUpgradeResult, error) {
	if s == nil || s.db == nil || s.messages == nil || req.UserID <= 0 || !req.Ref.Valid() ||
		req.Ref.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) ||
		req.ChargeStars < 0 || req.Date <= 0 || strings.TrimSpace(req.CommandKey) == "" || len(req.CommandKey) > 256 {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftCollectibleInvalid
	}
	saved, found, err := NewStarGiftStore(s.db).GetByRef(ctx, req.Ref)
	if err != nil {
		return domain.StarGiftUpgradeResult{}, err
	}
	if !found || saved.FromUserID <= 0 {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftNotFound
	}

	commandKey := strings.TrimSpace(req.CommandKey)
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf(
		"telesrv:star-gift-upgrade:v1:%s:%d:%d:%t:%d:%t",
		commandKey, saved.ID, req.ChargeStars, req.RequirePrepaid, req.FormID, req.KeepOriginalDetails,
	)))
	randomID := starGiftUpgradeRandomID(saved.FromUserID, req.UserID, commandKey)
	placeholder := &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind:           domain.MessageServiceActionStarGiftUnique,
			StarGiftUnique: &domain.MessageStarGiftUniqueAction{Upgrade: true, Saved: true},
		},
	}
	messageReq := domain.SendPrivateTextRequest{
		SenderUserID:           saved.FromUserID,
		RecipientUserID:        req.UserID,
		RandomID:               randomID,
		Media:                  placeholder,
		Date:                   req.Date,
		OriginAuthKeyID:        req.OriginAuthKeyID,
		OriginSessionID:        req.OriginSessionID,
		OriginUserID:           req.UserID,
		IdempotencyFingerprint: fingerprint[:],
	}

	var result domain.StarGiftUpgradeResult
	hooks := privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, messageReq *domain.SendPrivateTextRequest) error {
			locked, err := lockSavedStarGiftForUpgrade(ctx, tx, req.Ref)
			if err != nil {
				return err
			}
			if locked.ID != saved.ID || locked.FromUserID != saved.FromUserID {
				return domain.ErrStarGiftCollectibleInvalid
			}
			if locked.Converted {
				return domain.ErrStarGiftAlreadyConverted
			}
			if locked.UniqueGiftID != 0 {
				return domain.ErrStarGiftAlreadyUpgraded
			}

			revision, err := lockActiveCollectibleRevision(ctx, tx, locked.GiftID)
			if err != nil {
				return err
			}
			if revision.Issued >= revision.SupplyTotal {
				return domain.ErrStarGiftCollectibleSoldOut
			}
			if req.RequirePrepaid {
				// Prepayment is an entitlement captured at gift purchase time. A
				// later published revision may change the current price, but must not
				// retroactively invalidate that already-paid entitlement.
				if req.ChargeStars != 0 || locked.PrepaidUpgradeStars <= 0 {
					return domain.ErrStarGiftCollectibleUnavailable
				}
			} else if req.ChargeStars != revision.UpgradeStars {
				return domain.ErrStarGiftCollectibleUnavailable
			}

			balance, err := debitStarGiftUpgrade(ctx, tx, req.UserID, req.ChargeStars, locked.Owner, req.Date)
			if err != nil {
				return err
			}
			modelID, err := chooseCollectibleAttribute(ctx, tx, "star_gift_collectible_models", revision.ID)
			if err != nil {
				return err
			}
			patternID, err := chooseCollectibleAttribute(ctx, tx, "star_gift_collectible_patterns", revision.ID)
			if err != nil {
				return err
			}
			backdropID, err := chooseCollectibleAttribute(ctx, tx, "star_gift_collectible_backdrops", revision.ID)
			if err != nil {
				return err
			}

			num := revision.Issued + 1
			var uniqueID int64
			if err := tx.QueryRow(ctx, `SELECT nextval('unique_star_gift_id_seq')`).Scan(&uniqueID); err != nil {
				return fmt.Errorf("allocate unique star gift id: %w", err)
			}
			var title string
			if err := tx.QueryRow(ctx, `SELECT title FROM star_gift_catalog_revisions WHERE id=$1`, locked.RevisionID).Scan(&title); err != nil {
				return fmt.Errorf("load upgrade gift title: %w", err)
			}
			slug := fmt.Sprintf("%s-%d", revision.SlugPrefix, num)
			if _, err := tx.Exec(ctx, `
INSERT INTO unique_star_gifts
    (id, gift_id, collectible_revision_id, source_saved_gift_id, title, slug, num,
     owner_peer_type, owner_peer_id, model_attribute_id, pattern_attribute_id,
     backdrop_attribute_id, keep_original_details)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
				uniqueID, locked.GiftID, revision.ID, locked.ID, title, slug, num,
				string(locked.Owner.Type), locked.Owner.ID, modelID, patternID, backdropID, req.KeepOriginalDetails); err != nil {
				return fmt.Errorf("insert unique star gift: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE star_gift_collectible_revisions SET issued=issued+1 WHERE id=$1`, revision.ID); err != nil {
				return fmt.Errorf("increment collectible issuance: %w", err)
			}
			if _, err := tx.Exec(ctx, `
UPDATE peer_star_gifts
SET unique_gift_id=$2, prepaid_upgrade_stars=0, convert_stars=0
WHERE id=$1 AND unique_gift_id IS NULL AND NOT converted`, locked.ID, uniqueID); err != nil {
				return fmt.Errorf("upgrade saved star gift: %w", err)
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO star_gift_upgrade_commands
    (user_id, command_key, source_saved_gift_id, form_id, unique_gift_id, balance_after)
VALUES ($1,$2,$3,$4,$5,$6)`, req.UserID, commandKey, locked.ID, req.FormID, uniqueID, balance.Balance); err != nil {
				return fmt.Errorf("insert star gift upgrade command: %w", err)
			}

			unique, found, err := NewStarGiftStore(tx).UniqueByID(ctx, uniqueID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("new unique star gift %d disappeared", uniqueID)
			}
			locked.UniqueGiftID = uniqueID
			locked.PrepaidUpgradeStars = 0
			locked.ConvertStars = 0
			locked.Unique = &unique
			result.Saved, result.Unique, result.Balance = locked, unique, balance
			messageReq.Media = &domain.MessageMedia{
				Kind: domain.MessageMediaKindService,
				ServiceAction: &domain.MessageServiceAction{
					Kind: domain.MessageServiceActionStarGiftUnique,
					StarGiftUnique: &domain.MessageStarGiftUniqueAction{
						Gift: unique, FromUserID: func() int64 {
							if locked.NameHidden {
								return 0
							}
							return locked.FromUserID
						}(), Peer: locked.Owner, Upgrade: true, Saved: !locked.Unsaved,
						PrepaidUpgrade: req.RequirePrepaid,
					},
				},
			}
			return nil
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			ownerMessageID := sent.RecipientMessage.ID
			if saved.FromUserID == req.UserID {
				ownerMessageID = sent.SenderMessage.ID
			}
			if ownerMessageID <= 0 {
				return fmt.Errorf("upgrade service message missing owner box")
			}
			tag, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET upgrade_msg_id=$2 WHERE id=$1 AND unique_gift_id=$3`, result.Saved.ID, ownerMessageID, result.Unique.ID)
			if err != nil {
				return fmt.Errorf("save star gift upgrade message id: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("save star gift upgrade message id lost aggregate row")
			}
			result.Saved.UpgradeMsgID = ownerMessageID
			return nil
		},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		return domain.StarGiftUpgradeResult{}, err
	}
	result.Send = sent
	result.Duplicate = sent.Duplicate
	if sent.Duplicate {
		return s.loadUpgradeReplay(ctx, req, saved, sent)
	}
	return result, nil
}

func lockSavedStarGiftForUpgrade(ctx context.Context, tx pgx.Tx, ref domain.SavedStarGiftRef) (domain.SavedStarGift, error) {
	where, args := savedStarGiftRefWhere(ref)
	row := tx.QueryRow(ctx, `
SELECT p.id, p.owner_peer_type, p.owner_peer_id, p.from_user_id, p.gift_id, p.catalog_revision_id,
       p.msg_id, p.saved_id, p.gift_date, p.name_hidden, p.unsaved, p.converted, p.convert_stars, p.prepaid_upgrade_stars,
       p.message, COALESCE(p.unique_gift_id, 0), p.upgrade_msg_id, p.pinned_order,
       COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order, i.collection_id)
                 FROM star_gift_collection_items i
                 JOIN star_gift_collections c ON c.collection_id=i.collection_id
                 WHERE i.saved_gift_id=p.id), ARRAY[]::integer[])
FROM peer_star_gifts p WHERE `+where+` FOR UPDATE`, args...)
	saved, err := scanSavedStarGift(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SavedStarGift{}, domain.ErrStarGiftNotFound
	}
	return saved, err
}

func lockActiveCollectibleRevision(ctx context.Context, tx pgx.Tx, giftID int64) (domain.StarGiftCollectibleRevision, error) {
	var revision domain.StarGiftCollectibleRevision
	var status string
	err := tx.QueryRow(ctx, `
SELECT r.id, r.gift_id, r.upgrade_stars, r.supply_total, r.issued, r.slug_prefix, r.status
FROM star_gift_catalog c
JOIN star_gift_collectible_revisions r ON r.id=c.collectible_revision_id
WHERE c.gift_id=$1 FOR UPDATE OF r`, giftID).Scan(
		&revision.ID, &revision.GiftID, &revision.UpgradeStars, &revision.SupplyTotal,
		&revision.Issued, &revision.SlugPrefix, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftCollectibleRevision{}, domain.ErrStarGiftCollectibleUnavailable
	}
	if err != nil {
		return domain.StarGiftCollectibleRevision{}, fmt.Errorf("lock active collectible revision: %w", err)
	}
	if status != "published" {
		return domain.StarGiftCollectibleRevision{}, domain.ErrStarGiftCollectibleUnavailable
	}
	return revision, nil
}

func debitStarGiftUpgrade(ctx context.Context, tx pgx.Tx, userID, amount int64, peer domain.Peer, date int) (domain.StarsBalance, error) {
	result := domain.StarsBalance{UserID: userID}
	var balance int64
	err := tx.QueryRow(ctx, `SELECT balance, granted FROM stars_balances WHERE user_id=$1 FOR UPDATE`, userID).Scan(&balance, &result.Granted)
	if amount == 0 && errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && balance < amount) {
		return domain.StarsBalance{}, domain.ErrStarsInsufficient
	}
	if err != nil {
		return domain.StarsBalance{}, fmt.Errorf("lock stars balance for gift upgrade: %w", err)
	}
	if amount == 0 {
		result.Balance = balance
		return result, nil
	}
	if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance=balance-$2, updated_at=now() WHERE user_id=$1 RETURNING balance`, userID, amount).Scan(&result.Balance); err != nil {
		return domain.StarsBalance{}, fmt.Errorf("debit star gift upgrade: %w", err)
	}
	if err := insertStarsTxn(ctx, tx, userID, -amount, domain.StarsReasonGiftUpgrade, peer, date, "Star gift upgrade", ""); err != nil {
		return domain.StarsBalance{}, err
	}
	return result, nil
}

func chooseCollectibleAttribute(ctx context.Context, tx pgx.Tx, table string, revisionID int64) (int64, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT id, rarity_permille FROM %s WHERE collectible_revision_id=$1 ORDER BY sort_order, id`, table), revisionID)
	if err != nil {
		return 0, fmt.Errorf("list collectible attributes for issuance: %w", err)
	}
	defer rows.Close()
	type weightedID struct {
		id     int64
		weight int
	}
	items := make([]weightedID, 0)
	total := 0
	for rows.Next() {
		var item weightedID
		if err := rows.Scan(&item.id, &item.weight); err != nil {
			return 0, err
		}
		items = append(items, item)
		total += item.weight
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(items) == 0 || total != 1000 {
		return 0, domain.ErrStarGiftCollectibleInvalid
	}
	draw, err := rand.Int(rand.Reader, big.NewInt(int64(total)))
	if err != nil {
		return 0, fmt.Errorf("draw collectible attribute: %w", err)
	}
	value := int(draw.Int64())
	for _, item := range items {
		if value < item.weight {
			return item.id, nil
		}
		value -= item.weight
	}
	return 0, domain.ErrStarGiftCollectibleInvalid
}

func starGiftUpgradeRandomID(senderID, ownerID int64, commandKey string) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", senderID, ownerID, commandKey)))
	id := int64(binary.LittleEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	if id == 0 {
		id = 1
	}
	return id
}

func (s *StarGiftUpgradeStore) loadUpgradeReplay(ctx context.Context, req domain.StarGiftUpgradeRequest, original domain.SavedStarGift, sent domain.SendPrivateTextResult) (domain.StarGiftUpgradeResult, error) {
	saved, found, err := NewStarGiftStore(s.db).GetByRef(ctx, req.Ref)
	if err != nil || !found || saved.UniqueGiftID == 0 {
		if err == nil {
			err = domain.ErrStarGiftCollectibleInvalid
		}
		return domain.StarGiftUpgradeResult{}, err
	}
	unique, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, saved.UniqueGiftID)
	if err != nil || !found {
		if err == nil {
			err = domain.ErrStarGiftCollectibleInvalid
		}
		return domain.StarGiftUpgradeResult{}, err
	}
	var commandUniqueID int64
	var balanceAfter int64
	if err := s.db.QueryRow(ctx, `SELECT unique_gift_id, balance_after FROM star_gift_upgrade_commands WHERE user_id=$1 AND command_key=$2`, req.UserID, strings.TrimSpace(req.CommandKey)).Scan(&commandUniqueID, &balanceAfter); err != nil {
		return domain.StarGiftUpgradeResult{}, fmt.Errorf("load star gift upgrade replay: %w", err)
	}
	if commandUniqueID != unique.ID || saved.ID != original.ID {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftCollectibleInvalid
	}
	uniqueCopy := unique
	saved.Unique = &uniqueCopy
	return domain.StarGiftUpgradeResult{
		Saved: saved, Unique: unique, Balance: domain.StarsBalance{UserID: req.UserID, Balance: balanceAfter},
		Send: sent, Duplicate: true,
	}, nil
}

var _ store.StarGiftUpgradeStore = (*StarGiftUpgradeStore)(nil)
