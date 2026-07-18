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
	db        sqlcgen.DBTX
	messages  *MessageStore
	lifecycle domain.StarGiftLifecyclePolicy
}

type StarGiftUpgradeOption func(*StarGiftUpgradeStore)

func WithStarGiftLifecyclePolicy(policy domain.StarGiftLifecyclePolicy) StarGiftUpgradeOption {
	return func(s *StarGiftUpgradeStore) {
		if policy.Valid() {
			s.lifecycle = policy
		}
	}
}

func NewStarGiftUpgradeStore(db sqlcgen.DBTX, messages *MessageStore, opts ...StarGiftUpgradeOption) *StarGiftUpgradeStore {
	s := &StarGiftUpgradeStore{db: db, messages: messages, lifecycle: domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 250,
	}}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *StarGiftUpgradeStore) UpgradeStarGift(ctx context.Context, req domain.StarGiftUpgradeRequest) (domain.StarGiftUpgradeResult, error) {
	if s == nil || s.db == nil || s.messages == nil || req.UserID <= 0 || !req.Ref.Valid() ||
		(req.Ref.Owner.Type == domain.PeerTypeUser && req.Ref.Owner.ID != req.UserID) ||
		(req.Ref.Owner.Type != domain.PeerTypeUser && req.Ref.Owner.Type != domain.PeerTypeChannel) ||
		req.ChargeStars < 0 || (req.RequirePrepaid && (req.ChargeStars != 0 || req.FormID != 0)) ||
		(!req.RequirePrepaid && (req.ChargeStars <= 0 || req.FormID == 0)) ||
		req.Date <= 0 || strings.TrimSpace(req.CommandKey) == "" || len(req.CommandKey) > 256 {
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
	messageSenderID := saved.FromUserID
	if saved.Owner.Type == domain.PeerTypeChannel {
		messageSenderID = domain.OfficialSystemUserID
	}
	randomID := starGiftUpgradeRandomID(messageSenderID, req.UserID, commandKey)
	placeholder := &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind:           domain.MessageServiceActionStarGiftUnique,
			StarGiftUnique: &domain.MessageStarGiftUniqueAction{Upgrade: true, Saved: true},
		},
	}
	messageReq := domain.SendPrivateTextRequest{
		SenderUserID:           messageSenderID,
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
			var craftable bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM star_gift_collectible_models
WHERE collectible_revision_id=$1 AND crafted
)`, revision.ID).Scan(&craftable); err != nil {
				return fmt.Errorf("load collectible craft capability: %w", err)
			}
			craftChancePermille := 0
			canCraftAt := 0
			if craftable {
				craftChancePermille = s.lifecycle.CraftChancePermille
				canCraftAt = starGiftReadyAt(req.Date, s.lifecycle.CraftDelaySeconds)
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
     backdrop_attribute_id, keep_original_details, original_owner_peer_type, original_owner_peer_id,
     craft_chance_permille, offer_min_stars)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
				uniqueID, locked.GiftID, revision.ID, locked.ID, title, slug, num,
				string(locked.Owner.Type), locked.Owner.ID, modelID, patternID, backdropID, req.KeepOriginalDetails,
				string(locked.Owner.Type), locked.Owner.ID, craftChancePermille, s.lifecycle.OfferMinStars); err != nil {
				return fmt.Errorf("insert unique star gift: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE star_gift_collectible_revisions SET issued=issued+1 WHERE id=$1`, revision.ID); err != nil {
				return fmt.Errorf("increment collectible issuance: %w", err)
			}
			if _, err := tx.Exec(ctx, `
UPDATE peer_star_gifts
SET unique_gift_id=$2, prepaid_upgrade_stars=0, prepaid_upgrade_hash='', convert_stars=0,
    transfer_stars=$3,can_export_at=$4,can_transfer_at=$5,can_resell_at=$6,
    drop_original_details_stars=$7,can_craft_at=$8
WHERE id=$1 AND unique_gift_id IS NULL AND lifecycle_status='active'`, locked.ID, uniqueID,
				s.lifecycle.TransferStars, starGiftReadyAt(req.Date, s.lifecycle.ExportDelaySeconds),
				starGiftReadyAt(req.Date, s.lifecycle.TransferDelaySeconds), starGiftReadyAt(req.Date, s.lifecycle.ResellDelaySeconds),
				s.lifecycle.DropOriginalDetailsStars, canCraftAt); err != nil {
				return fmt.Errorf("upgrade saved star gift: %w", err)
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO star_gift_upgrade_commands
    (user_id, command_key, source_saved_gift_id, form_id, unique_gift_id, balance_after,
     charge_stars, require_prepaid, keep_original_details)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, req.UserID, commandKey, locked.ID, req.FormID, uniqueID, balance.Balance,
				req.ChargeStars, req.RequirePrepaid, req.KeepOriginalDetails); err != nil {
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
			locked.TransferStars = s.lifecycle.TransferStars
			locked.CanExportAt = starGiftReadyAt(req.Date, s.lifecycle.ExportDelaySeconds)
			locked.CanTransferAt = starGiftReadyAt(req.Date, s.lifecycle.TransferDelaySeconds)
			locked.CanResellAt = starGiftReadyAt(req.Date, s.lifecycle.ResellDelaySeconds)
			locked.DropOriginalDetailsStars = s.lifecycle.DropOriginalDetailsStars
			locked.CanCraftAt = canCraftAt
			locked.Unique = &unique
			result.Saved, result.Unique, result.Balance = locked, unique, balance
			action := starGiftUpgradeUniqueAction(locked, unique, req, messageSenderID)
			messageReq.Media = &domain.MessageMedia{
				Kind: domain.MessageMediaKindService,
				ServiceAction: &domain.MessageServiceAction{
					Kind:           domain.MessageServiceActionStarGiftUnique,
					StarGiftUnique: action,
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
			if result.Saved.Owner.Type == domain.PeerTypeUser {
				edits, err := s.markPrivateStarGiftSourceUpgradedTx(ctx, tx, req, result.Saved, sent)
				if err != nil {
					return err
				}
				result.SourceEdits = edits
				ownerEditPts := 0
				for _, edit := range edits {
					if edit.UserID == req.UserID {
						ownerEditPts = edit.Event.Pts
						break
					}
				}
				if ownerEditPts <= 0 {
					return fmt.Errorf("upgrade source edit missing owner event")
				}
				tag, err := tx.Exec(ctx, `
UPDATE star_gift_upgrade_commands SET source_edit_pts=$3
WHERE user_id=$1 AND command_key=$2`, req.UserID, commandKey, ownerEditPts)
				if err != nil {
					return fmt.Errorf("save star gift source edit pts: %w", err)
				}
				if tag.RowsAffected() != 1 {
					return fmt.Errorf("save star gift source edit pts lost command row")
				}
			} else {
				action := starGiftUpgradeUniqueAction(result.Saved, result.Unique, req, messageSenderID)
				if err := NewChannelStore(tx).appendStarGiftAdminLogTx(ctx, tx, result.Saved.Owner.ID,
					req.UserID, result.Saved.SavedID, req.Date, domain.ChannelMessageAction{
						Type: domain.ChannelActionStarGiftUnique, StarGiftUnique: action,
					}); err != nil {
					return fmt.Errorf("append channel star gift upgrade admin log: %w", err)
				}
			}
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

func starGiftUpgradeUniqueAction(saved domain.SavedStarGift, unique domain.UniqueStarGift, req domain.StarGiftUpgradeRequest, messageSenderID int64) *domain.MessageStarGiftUniqueAction {
	fromUserID := saved.FromUserID
	if saved.NameHidden {
		fromUserID = 0
	}
	if saved.Owner.Type == domain.PeerTypeChannel {
		// TDesktop recognizes a channel-owned upgrade from the official service
		// peer plus action.peer=channel and action.saved_id.
		fromUserID = messageSenderID
	}
	savedID := saved.SavedID
	if saved.Owner.Type == domain.PeerTypeUser {
		// For user-owned gifts messageActionStarGiftUnique.saved_id is the
		// stable source gift message id. TDesktop uses this back-reference as
		// inputSavedStarGiftUser.msg_id for crafting and later lifecycle RPCs.
		savedID = int64(saved.MsgID)
	}
	return &domain.MessageStarGiftUniqueAction{
		Gift: unique, FromUserID: fromUserID, Peer: saved.Owner, SavedID: savedID,
		Upgrade: true, Saved: !saved.Unsaved, PrepaidUpgrade: req.RequirePrepaid,
		CanExportAt: saved.CanExportAt, TransferStars: saved.TransferStars,
		CanTransferAt: saved.CanTransferAt, CanResellAt: saved.CanResellAt,
		DropOriginalDetailsStars: saved.DropOriginalDetailsStars, CanCraftAt: saved.CanCraftAt,
	}
}

// markPrivateStarGiftSourceUpgradedTx rewrites both visible copies of the
// original gift service message in the same transaction that creates the
// unique gift message. upgrade_msg_id is box-local, so each owner projection
// must point at that owner's copy of the new service message. Every rewrite is
// a durable edit_message event with its own pts and outbox row.
func (s *StarGiftUpgradeStore) markPrivateStarGiftSourceUpgradedTx(
	ctx context.Context,
	tx pgx.Tx,
	req domain.StarGiftUpgradeRequest,
	saved domain.SavedStarGift,
	sent domain.SendPrivateTextResult,
) ([]domain.EditedMessageForUser, error) {
	if saved.Owner.Type != domain.PeerTypeUser || saved.Owner.ID != req.UserID || saved.MsgID <= 0 {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	q := sqlcgen.New(tx)
	target, err := q.GetMessageBoxForEdit(ctx, sqlcgen.GetMessageBoxForEditParams{
		OwnerUserID: req.UserID,
		BoxID:       int32(saved.MsgID),
		PeerType:    string(domain.PeerTypeUser),
		PeerID:      saved.FromUserID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrStarGiftCollectibleInvalid
		}
		return nil, fmt.Errorf("lock star gift source message: %w", err)
	}
	boxes, err := q.ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
		OwnerUserIds:     privateMessageOwnerIDs(req.UserID, saved.FromUserID),
		MessageSenderID:  target.MessageSenderID,
		PrivateMessageID: target.PrivateMessageID,
	})
	if err != nil {
		return nil, fmt.Errorf("list star gift source message boxes: %w", err)
	}
	if len(boxes) == 0 {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	upgradeMessageIDs := make(map[int64]int, 2)
	if sent.SenderMessage.OwnerUserID > 0 && sent.SenderMessage.ID > 0 {
		upgradeMessageIDs[sent.SenderMessage.OwnerUserID] = sent.SenderMessage.ID
	}
	if sent.RecipientMessage.OwnerUserID > 0 && sent.RecipientMessage.ID > 0 {
		upgradeMessageIDs[sent.RecipientMessage.OwnerUserID] = sent.RecipientMessage.ID
	}
	edits := make([]domain.EditedMessageForUser, 0, len(boxes))
	var privateMediaJSON []byte
	for _, box := range boxes {
		upgradeMessageID := upgradeMessageIDs[box.OwnerUserID]
		if upgradeMessageID <= 0 {
			return nil, fmt.Errorf("upgrade service message missing box for user %d", box.OwnerUserID)
		}
		media, err := decodeMessageMedia(box.MediaJson)
		if err != nil {
			return nil, fmt.Errorf("decode star gift source media: %w", err)
		}
		if media == nil || media.Kind != domain.MessageMediaKindService || media.ServiceAction == nil ||
			media.ServiceAction.Kind != domain.MessageServiceActionStarGift || media.ServiceAction.StarGift == nil {
			return nil, fmt.Errorf("star gift source message %d has invalid media", box.BoxID)
		}
		action := media.ServiceAction.StarGift
		if action.UpgradeMsgID != 0 && action.UpgradeMsgID != upgradeMessageID {
			return nil, fmt.Errorf("star gift source message %d has conflicting upgrade message %d", box.BoxID, action.UpgradeMsgID)
		}
		action.UpgradeMsgID = upgradeMessageID
		action.CanUpgrade = false
		mediaJSON, err := encodeMessageMedia(media)
		if err != nil {
			return nil, fmt.Errorf("encode upgraded star gift source media: %w", err)
		}
		pts, err := s.messages.reservePts(ctx, tx, box.OwnerUserID)
		if err != nil {
			return nil, fmt.Errorf("allocate star gift source edit pts: %w", err)
		}
		tag, err := tx.Exec(ctx, `
UPDATE message_boxes SET media=$3, pts=$4
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted`, box.OwnerUserID, box.BoxID, mediaJSON, int32(pts))
		if err != nil {
			return nil, fmt.Errorf("update star gift source message box: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil, fmt.Errorf("update star gift source message box lost row")
		}
		msg, err := messageFromVisibleBoxRow(box)
		if err != nil {
			return nil, err
		}
		msg.Media = media
		msg.Pts = pts
		if err := replaceMessageBoxMediaIndexTx(ctx, tx, msg.OwnerUserID, msg.Peer.ID, msg.ID, msg.Date, msg.Media, msg.Entities); err != nil {
			return nil, err
		}
		event := domain.UpdateEvent{
			UserID: msg.OwnerUserID, Type: domain.UpdateEventEditMessage,
			Pts: pts, PtsCount: 1, Date: req.Date, Message: msg,
		}
		if err := appendUserUpdateEvent(ctx, tx, q, msg.OwnerUserID, event); err != nil {
			return nil, fmt.Errorf("append star gift source edit event: %w", err)
		}
		dispatchAuthKeyID := [8]byte{}
		dispatchSessionID := int64(0)
		if msg.OwnerUserID == req.UserID {
			dispatchAuthKeyID = req.OriginAuthKeyID
			dispatchSessionID = req.OriginSessionID
		}
		if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
			TargetUserID: msg.OwnerUserID, Pts: int32(pts), EventType: string(domain.UpdateEventEditMessage),
			ExcludeAuthKeyID: authKeyIDToInt64(dispatchAuthKeyID), ExcludeSessionID: dispatchSessionID,
		}); err != nil {
			return nil, fmt.Errorf("enqueue star gift source edit: %w", err)
		}
		if box.OwnerUserID == box.MessageSenderID || len(privateMediaJSON) == 0 {
			privateMediaJSON = mediaJSON
		}
		edits = append(edits, domain.EditedMessageForUser{UserID: msg.OwnerUserID, Message: msg, Event: event})
	}
	if len(privateMediaJSON) == 0 {
		return nil, fmt.Errorf("upgrade source message missing private media projection")
	}
	if _, err := tx.Exec(ctx, `
UPDATE private_messages SET media=$3
WHERE sender_user_id=$1 AND id=$2`, target.MessageSenderID, target.PrivateMessageID, privateMediaJSON); err != nil {
		return nil, fmt.Errorf("update star gift source private message: %w", err)
	}
	return edits, nil
}

func starGiftReadyAt(date, delaySeconds int) int {
	if date <= 0 || delaySeconds <= 0 {
		return 0
	}
	const maxProtocolDate = int(1<<31 - 1)
	if delaySeconds > maxProtocolDate-date {
		return maxProtocolDate
	}
	return date + delaySeconds
}

func lockSavedStarGiftForUpgrade(ctx context.Context, tx pgx.Tx, ref domain.SavedStarGiftRef) (domain.SavedStarGift, error) {
	where, args := savedStarGiftRefWhere(ref)
	return lockSavedStarGiftWhere(ctx, tx, where, args...)
}

func lockSavedStarGiftByID(ctx context.Context, tx pgx.Tx, savedID int64) (domain.SavedStarGift, error) {
	return lockSavedStarGiftWhere(ctx, tx, "p.id = $1", savedID)
}

func lockSavedStarGiftWhere(ctx context.Context, tx pgx.Tx, where string, args ...any) (domain.SavedStarGift, error) {
	row := tx.QueryRow(ctx, `
SELECT p.id, p.owner_peer_type, p.owner_peer_id, p.from_user_id, p.gift_id, p.catalog_revision_id,
       p.msg_id, p.saved_id, p.gift_date, p.name_hidden, p.unsaved, p.converted, p.convert_stars, p.prepaid_upgrade_stars, p.prepaid_upgrade_hash, p.gift_num,
	   p.lifecycle_status, p.transfer_stars, p.can_export_at, p.can_transfer_at, p.can_resell_at,
	   p.drop_original_details_stars, p.can_craft_at,
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
	extra := ""
	if table == "star_gift_collectible_models" {
		extra = " AND NOT crafted"
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT id, rarity_permille FROM %s
WHERE collectible_revision_id=$1 AND rarity_kind='permille' AND rarity_permille > 0%s
ORDER BY sort_order, id`, table, extra), revisionID)
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
	if len(items) == 0 || total <= 0 {
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
	receipt, found, err := s.StarGiftUpgradeReceipt(ctx, req.UserID, req.CommandKey)
	if err != nil {
		return domain.StarGiftUpgradeResult{}, fmt.Errorf("load star gift upgrade replay: %w", err)
	}
	if !found || receipt.UniqueGiftID != unique.ID || receipt.SourceSavedGiftID != saved.ID || saved.ID != original.ID ||
		receipt.FormID != req.FormID || receipt.ChargeStars != req.ChargeStars || receipt.RequirePrepaid != req.RequirePrepaid ||
		receipt.KeepOriginalDetails != req.KeepOriginalDetails {
		return domain.StarGiftUpgradeResult{}, domain.ErrStarGiftCollectibleInvalid
	}
	uniqueCopy := unique
	saved.Unique = &uniqueCopy
	sourceEdits, err := s.loadUpgradeSourceReplay(ctx, req, saved, receipt.SourceEditPts)
	if err != nil {
		return domain.StarGiftUpgradeResult{}, err
	}
	return domain.StarGiftUpgradeResult{
		Saved: saved, Unique: unique, Balance: domain.StarsBalance{UserID: req.UserID, Balance: receipt.BalanceAfter},
		Send: sent, SourceEdits: sourceEdits, Duplicate: true,
	}, nil
}

func (s *StarGiftUpgradeStore) loadUpgradeSourceReplay(ctx context.Context, req domain.StarGiftUpgradeRequest, saved domain.SavedStarGift, pts int) ([]domain.EditedMessageForUser, error) {
	if saved.Owner.Type != domain.PeerTypeUser {
		return nil, nil
	}
	if pts <= 0 || saved.MsgID <= 0 {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	var privateMessageID, messageSenderID int64
	err := s.db.QueryRow(ctx, `
SELECT private_message_id,message_sender_id FROM message_boxes
WHERE owner_user_id=$1 AND box_id=$2 AND peer_type='user' AND peer_id=$3 AND NOT deleted`,
		req.UserID, saved.MsgID, saved.FromUserID).Scan(&privateMessageID, &messageSenderID)
	if errors.Is(err, pgx.ErrNoRows) {
		// A later delete event is authoritative; replaying the old edit here
		// would transiently resurrect the source message.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load star gift source replay message: %w", err)
	}
	boxes, err := sqlcgen.New(s.db).ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
		OwnerUserIds: []int64{req.UserID}, MessageSenderID: messageSenderID, PrivateMessageID: privateMessageID,
	})
	if err != nil {
		return nil, fmt.Errorf("load star gift source replay box: %w", err)
	}
	if len(boxes) != 1 || int(boxes[0].BoxID) != saved.MsgID {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	var eventDate int
	err = s.db.QueryRow(ctx, `
SELECT date FROM user_update_events
WHERE user_id=$1 AND pts=$2 AND event_type='edit_message' AND message_box_id=$3`,
		req.UserID, pts, saved.MsgID).Scan(&eventDate)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrStarGiftCollectibleInvalid
		}
		return nil, fmt.Errorf("load star gift source replay event: %w", err)
	}
	msg, err := messageFromVisibleBoxRow(boxes[0])
	if err != nil {
		return nil, err
	}
	msg.Pts = pts
	event := domain.UpdateEvent{UserID: req.UserID, Type: domain.UpdateEventEditMessage, Pts: pts, PtsCount: 1, Date: eventDate, Message: msg}
	return []domain.EditedMessageForUser{{UserID: req.UserID, Message: msg, Event: event}}, nil
}

func (s *StarGiftUpgradeStore) StarGiftUpgradeReceipt(ctx context.Context, userID int64, commandKey string) (domain.StarGiftUpgradeReceipt, bool, error) {
	commandKey = strings.TrimSpace(commandKey)
	if s == nil || s.db == nil || userID <= 0 || commandKey == "" || len(commandKey) > 256 {
		return domain.StarGiftUpgradeReceipt{}, false, nil
	}
	receipt := domain.StarGiftUpgradeReceipt{UserID: userID}
	err := s.db.QueryRow(ctx, `
SELECT source_saved_gift_id,form_id,unique_gift_id,charge_stars,balance_after,source_edit_pts,require_prepaid,keep_original_details
FROM star_gift_upgrade_commands WHERE user_id=$1 AND command_key=$2`, userID, commandKey).Scan(
		&receipt.SourceSavedGiftID, &receipt.FormID, &receipt.UniqueGiftID, &receipt.ChargeStars,
		&receipt.BalanceAfter, &receipt.SourceEditPts, &receipt.RequirePrepaid, &receipt.KeepOriginalDetails)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftUpgradeReceipt{}, false, nil
	}
	if err != nil {
		return domain.StarGiftUpgradeReceipt{}, false, err
	}
	return receipt, true, nil
}

var _ store.StarGiftUpgradeStore = (*StarGiftUpgradeStore)(nil)
