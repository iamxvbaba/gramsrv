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
			// Keep the durable Craft entitlement attached to the collectible across
			// user/channel ownership moves. The RPC projection suppresses the
			// readiness marker for channel owners until channel Craft execution is
			// implemented, without destroying the official gift property.
			if craftable {
				craftChancePermille = s.lifecycle.CraftChancePermille
				if craftChancePermille > 0 {
					canCraftAt = starGiftCraftReadyAt(req.Date, s.lifecycle.CraftDelaySeconds)
				}
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
			modelID, err := resolveCollectibleAttribute(ctx, tx, "star_gift_collectible_models", revision.ID, req.ModelAttributeID)
			if err != nil {
				return err
			}
			patternID, err := resolveCollectibleAttribute(ctx, tx, "star_gift_collectible_patterns", revision.ID, req.PatternAttributeID)
			if err != nil {
				return err
			}
			backdropID, err := resolveCollectibleAttribute(ctx, tx, "star_gift_collectible_backdrops", revision.ID, req.BackdropAttributeID)
			if err != nil {
				return err
			}

			num := revision.Issued + 1
			if req.Num > 0 {
				if req.Num > revision.SupplyTotal {
					return domain.ErrStarGiftCollectibleInvalid
				}
				var numTaken bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM unique_star_gifts WHERE gift_id=$1 AND num=$2)`, locked.GiftID, req.Num).Scan(&numTaken); err != nil {
					return fmt.Errorf("check collectible number availability: %w", err)
				}
				if numTaken {
					return domain.ErrStarGiftCollectibleNumberTaken
				}
				num = req.Num
			}
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
			if result.Saved.Owner.Type == domain.PeerTypeUser {
				if err := registerUserStarGiftMessageRef(ctx, tx, result.Saved.Owner.ID, ownerMessageID,
					result.Saved.ID, result.Unique.ID); err != nil {
					return err
				}
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
	peer := saved.Owner
	savedID := saved.SavedID
	canCraftAt := saved.CanCraftAt
	if saved.Owner.Type == domain.PeerTypeUser {
		// peer and saved_id share one TL flag and are defined for channel gifts.
		// For user gifts both must be absent; official clients use the emitted
		// service-message id (registered owner-locally by the send transaction).
		peer = domain.Peer{}
		savedID = 0
	} else {
		// The current Craft state machine is user-owned only. Android treats a
		// positive can_craft_at as the channel Craft entry marker, so do not
		// advertise a write path that the server cannot execute yet.
		canCraftAt = 0
	}
	return &domain.MessageStarGiftUniqueAction{
		Gift: unique, FromUserID: fromUserID, Peer: peer, SavedID: savedID,
		Upgrade: true, Saved: !saved.Unsaved, PrepaidUpgrade: req.RequirePrepaid,
		CanExportAt: saved.CanExportAt, TransferStars: saved.TransferStars,
		CanTransferAt: saved.CanTransferAt, CanResellAt: saved.CanResellAt,
		DropOriginalDetailsStars: saved.DropOriginalDetailsStars, CanCraftAt: canCraftAt,
	}
}

func userStarGiftSourceMessageIDs(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, saved domain.SavedStarGift) ([]int, error) {
	if saved.Owner.Type != domain.PeerTypeUser || saved.Owner.ID <= 0 || saved.ID <= 0 || saved.MsgID <= 0 {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	messageIDs := []int{saved.MsgID}
	rows, err := db.Query(ctx, `
SELECT msg_id FROM star_gift_user_message_refs
WHERE owner_user_id=$1 AND saved_gift_id=$2 AND msg_id<>$3
ORDER BY msg_id`, saved.Owner.ID, saved.ID, saved.MsgID)
	if err != nil {
		return nil, fmt.Errorf("list star gift source message refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var msgID int
		if err := rows.Scan(&msgID); err != nil {
			return nil, fmt.Errorf("scan star gift source message ref: %w", err)
		}
		if msgID <= 0 {
			return nil, fmt.Errorf("star gift source message ref has invalid id")
		}
		messageIDs = append(messageIDs, msgID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate star gift source message refs: %w", err)
	}
	return messageIDs, nil
}

// markPrivateStarGiftSourceUpgradedTx rewrites every ordinary gift projection
// owned by the source aggregate: the original gift message and each separately
// prepaid-upgrade notification. The two visible boxes of every logical private
// message are updated together. upgrade_msg_id is box-local and is set only
// when that viewer owns a box for the emitted unique-gift message; a third-party
// payer sees the prepayment become non-actionable without receiving an invalid
// owner-local link. Every rewrite is a durable edit_message event with its own
// pts and outbox row.
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
	messageIDs, err := userStarGiftSourceMessageIDs(ctx, tx, saved)
	if err != nil {
		return nil, err
	}
	q := sqlcgen.New(tx)
	upgradeMessageIDs := make(map[int64]int, 2)
	if sent.SenderMessage.OwnerUserID > 0 && sent.SenderMessage.ID > 0 {
		upgradeMessageIDs[sent.SenderMessage.OwnerUserID] = sent.SenderMessage.ID
	}
	if sent.RecipientMessage.OwnerUserID > 0 && sent.RecipientMessage.ID > 0 {
		upgradeMessageIDs[sent.RecipientMessage.OwnerUserID] = sent.RecipientMessage.ID
	}
	edits := make([]domain.EditedMessageForUser, 0, len(messageIDs)*2)
	seenPrivateMessages := make(map[string]struct{}, len(messageIDs))
	primaryRewritten := false
	for _, sourceMessageID := range messageIDs {
		var peerType string
		var peerID int64
		err := tx.QueryRow(ctx, `
SELECT peer_type,peer_id FROM message_boxes
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted
FOR UPDATE`, req.UserID, sourceMessageID).Scan(&peerType, &peerID)
		if errors.Is(err, pgx.ErrNoRows) {
			if sourceMessageID == saved.MsgID {
				return nil, domain.ErrStarGiftCollectibleInvalid
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("lock star gift source ref %d: %w", sourceMessageID, err)
		}
		if peerType != string(domain.PeerTypeUser) || peerID <= 0 {
			return nil, fmt.Errorf("star gift source ref %d is not private", sourceMessageID)
		}
		target, err := q.GetMessageBoxForEdit(ctx, sqlcgen.GetMessageBoxForEditParams{
			OwnerUserID: req.UserID, BoxID: int32(sourceMessageID), PeerType: peerType, PeerID: peerID,
		})
		if err != nil {
			return nil, fmt.Errorf("load star gift source ref %d: %w", sourceMessageID, err)
		}
		ownerMedia, err := decodeMessageMedia(target.MediaJson)
		if err != nil {
			return nil, fmt.Errorf("decode star gift source ref %d: %w", sourceMessageID, err)
		}
		ownerAction := privateStarGiftAction(ownerMedia)
		if ownerAction == nil {
			// The newly emitted unique action is registered before source edits in
			// the same transaction and belongs to the same aggregate, but is not a
			// source projection to rewrite.
			if privateStarGiftUniqueAction(ownerMedia) != nil {
				continue
			}
			return nil, fmt.Errorf("star gift source ref %d has invalid media", sourceMessageID)
		}
		if ownerAction.GiftID != saved.GiftID {
			return nil, fmt.Errorf("star gift source ref %d points to gift %d", sourceMessageID, ownerAction.GiftID)
		}
		if sourceMessageID != saved.MsgID && (!ownerAction.UpgradeSeparate || !ownerAction.PrepaidUpgrade || ownerAction.GiftMsgID != saved.MsgID) {
			return nil, fmt.Errorf("star gift source ref %d is not a prepaid notification for message %d", sourceMessageID, saved.MsgID)
		}
		logicalKey := fmt.Sprintf("%d:%d", target.MessageSenderID, target.PrivateMessageID)
		if _, duplicate := seenPrivateMessages[logicalKey]; duplicate {
			continue
		}
		seenPrivateMessages[logicalKey] = struct{}{}
		boxes, err := q.ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
			OwnerUserIds: privateMessageOwnerIDs(req.UserID, peerID), MessageSenderID: target.MessageSenderID,
			PrivateMessageID: target.PrivateMessageID,
		})
		if err != nil {
			return nil, fmt.Errorf("list star gift source ref %d boxes: %w", sourceMessageID, err)
		}
		if len(boxes) == 0 {
			return nil, domain.ErrStarGiftCollectibleInvalid
		}
		var privateMediaJSON []byte
		for _, box := range boxes {
			media, err := decodeMessageMedia(box.MediaJson)
			if err != nil {
				return nil, fmt.Errorf("decode star gift source media: %w", err)
			}
			action := privateStarGiftAction(media)
			if action == nil || action.GiftID != saved.GiftID {
				return nil, fmt.Errorf("star gift source message %d has invalid media", box.BoxID)
			}
			upgradeMessageID := upgradeMessageIDs[box.OwnerUserID]
			if action.UpgradeMsgID != 0 && upgradeMessageID > 0 && action.UpgradeMsgID != upgradeMessageID {
				return nil, fmt.Errorf("star gift source message %d has conflicting upgrade message %d", box.BoxID, action.UpgradeMsgID)
			}
			if upgradeMessageID > 0 {
				action.UpgradeMsgID = upgradeMessageID
			} else {
				if box.OwnerUserID == req.UserID {
					return nil, fmt.Errorf("upgrade service message missing owner box")
				}
				action.UpgradeMsgID = 0
			}
			action.CanUpgrade = false
			action.PrepaidUpgradeHash = ""
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
			event := domain.UpdateEvent{UserID: msg.OwnerUserID, Type: domain.UpdateEventEditMessage,
				Pts: pts, PtsCount: 1, Date: req.Date, Message: msg}
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
				privateMediaJSON, err = encodeSharedPrivateStarGiftMedia(media)
				if err != nil {
					return nil, err
				}
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
		if sourceMessageID == saved.MsgID {
			primaryRewritten = true
		}
	}
	if !primaryRewritten {
		return nil, domain.ErrStarGiftCollectibleInvalid
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

// starGiftCraftReadyAt differs intentionally from the other lifecycle delay
// fields. Official Android clients use a positive can_craft_at both as the
// capability marker and as the readiness boundary, so an immediately
// craftable gift must carry its upgrade date instead of omitting the field.
func starGiftCraftReadyAt(date, delaySeconds int) int {
	if date <= 0 || delaySeconds < 0 {
		return 0
	}
	const maxProtocolDate = int(1<<31 - 1)
	if date >= maxProtocolDate {
		return maxProtocolDate
	}
	if delaySeconds == 0 {
		return date
	}
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

// resolveCollectibleAttribute returns explicitID when it names a renderable
// attribute belonging to revisionID (admin-pinned choice), otherwise it falls
// back to the weighted random draw. Models excluded from the random pool
// (crafted) are also rejected for explicit selection to preserve invariants.
func resolveCollectibleAttribute(ctx context.Context, tx pgx.Tx, table string, revisionID, explicitID int64) (int64, error) {
	if explicitID <= 0 {
		return chooseCollectibleAttribute(ctx, tx, table, revisionID)
	}
	extra := ""
	if table == "star_gift_collectible_models" {
		extra = " AND NOT crafted"
	}
	var ok bool
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s
WHERE id=$1 AND collectible_revision_id=$2 AND rarity_kind='permille' AND rarity_permille > 0%s)`, table, extra),
		explicitID, revisionID).Scan(&ok); err != nil {
		return 0, fmt.Errorf("validate collectible attribute: %w", err)
	}
	if !ok {
		return 0, domain.ErrStarGiftCollectibleInvalid
	}
	return explicitID, nil
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
	messageIDs, err := userStarGiftSourceMessageIDs(ctx, s.db, saved)
	if err != nil {
		return nil, err
	}
	edits := make([]domain.EditedMessageForUser, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		var privateMessageID, messageSenderID int64
		err := s.db.QueryRow(ctx, `
SELECT private_message_id,message_sender_id FROM message_boxes
WHERE owner_user_id=$1 AND box_id=$2 AND peer_type='user' AND NOT deleted`,
			req.UserID, messageID).Scan(&privateMessageID, &messageSenderID)
		if errors.Is(err, pgx.ErrNoRows) {
			// A later delete event is authoritative; replaying the old edit here
			// would transiently resurrect that source projection.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load star gift source replay message %d: %w", messageID, err)
		}
		boxes, err := sqlcgen.New(s.db).ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
			OwnerUserIds: []int64{req.UserID}, MessageSenderID: messageSenderID, PrivateMessageID: privateMessageID,
		})
		if err != nil {
			return nil, fmt.Errorf("load star gift source replay box %d: %w", messageID, err)
		}
		if len(boxes) != 1 || int(boxes[0].BoxID) != messageID {
			return nil, domain.ErrStarGiftCollectibleInvalid
		}
		media, err := decodeMessageMedia(boxes[0].MediaJson)
		if err != nil {
			return nil, fmt.Errorf("decode star gift source replay box %d: %w", messageID, err)
		}
		action := privateStarGiftAction(media)
		if action == nil {
			if privateStarGiftUniqueAction(media) != nil {
				continue
			}
			return nil, fmt.Errorf("star gift source replay box %d has invalid media", messageID)
		}
		if action.GiftID != saved.GiftID || action.CanUpgrade || action.UpgradeMsgID != saved.UpgradeMsgID {
			return nil, domain.ErrStarGiftCollectibleInvalid
		}
		var eventPts, eventDate int
		if messageID == saved.MsgID {
			eventPts = pts
			err = s.db.QueryRow(ctx, `
SELECT date FROM user_update_events
WHERE user_id=$1 AND pts=$2 AND event_type='edit_message' AND message_box_id=$3`,
				req.UserID, eventPts, messageID).Scan(&eventDate)
		} else {
			err = s.db.QueryRow(ctx, `
SELECT pts,date FROM user_update_events
WHERE user_id=$1 AND pts>=$2 AND event_type='edit_message' AND message_box_id=$3
ORDER BY pts LIMIT 1`, req.UserID, pts, messageID).Scan(&eventPts, &eventDate)
		}
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, domain.ErrStarGiftCollectibleInvalid
			}
			return nil, fmt.Errorf("load star gift source replay event %d: %w", messageID, err)
		}
		msg, err := messageFromVisibleBoxRow(boxes[0])
		if err != nil {
			return nil, err
		}
		msg.Pts = eventPts
		event := domain.UpdateEvent{UserID: req.UserID, Type: domain.UpdateEventEditMessage,
			Pts: eventPts, PtsCount: 1, Date: eventDate, Message: msg}
		edits = append(edits, domain.EditedMessageForUser{UserID: req.UserID, Message: msg, Event: event})
	}
	return edits, nil
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
