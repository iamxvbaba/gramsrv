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

// retireUserStarGiftMessagesTx closes every user-scoped unique-gift action
// emitted for the source ownership epoch. Ownership moves and terminal export
// must not leave an older chat card with Craft/transfer/resale capabilities.
// The aggregate mutation and all message edits share one transaction and each
// visible box receives its own durable pts/event/outbox entry.
func (s *StarGiftLifecycleStore) retireUserStarGiftMessagesTx(
	ctx context.Context,
	tx pgx.Tx,
	source domain.SavedStarGift,
	current domain.UniqueStarGift,
	date int,
) ([]domain.EditedMessageForUser, error) {
	if s == nil || s.messages == nil || source.Owner.Type != domain.PeerTypeUser || source.Owner.ID <= 0 ||
		source.ID <= 0 || source.UniqueGiftID <= 0 || current.ID != source.UniqueGiftID || date <= 0 {
		return nil, domain.ErrStarGiftTransferUnavailable
	}

	messageIDs := map[int]struct{}{}
	if source.MsgID > 0 {
		messageIDs[source.MsgID] = struct{}{}
	}
	if source.UpgradeMsgID > 0 {
		messageIDs[source.UpgradeMsgID] = struct{}{}
	}
	rows, err := tx.Query(ctx, `
SELECT msg_id FROM star_gift_user_message_refs
WHERE owner_user_id=$1 AND saved_gift_id=$2
ORDER BY msg_id`, source.Owner.ID, source.ID)
	if err != nil {
		return nil, fmt.Errorf("list star gift message projections: %w", err)
	}
	for rows.Next() {
		var msgID int
		if err := rows.Scan(&msgID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan star gift message projection: %w", err)
		}
		if msgID > 0 {
			messageIDs[msgID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate star gift message projections: %w", err)
	}
	rows.Close()

	ids := make([]int, 0, len(messageIDs))
	for msgID := range messageIDs {
		ids = append(ids, msgID)
	}
	sort.Ints(ids)

	q := sqlcgen.New(tx)
	edits := make([]domain.EditedMessageForUser, 0, len(ids)*2)
	seenPrivateMessages := make(map[string]struct{}, len(ids))
	for _, msgID := range ids {
		var peerType string
		var peerID int64
		err := tx.QueryRow(ctx, `
SELECT peer_type,peer_id FROM message_boxes
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted
FOR UPDATE`, source.Owner.ID, msgID).Scan(&peerType, &peerID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("lock star gift message projection: %w", err)
		}
		if peerType != string(domain.PeerTypeUser) || peerID <= 0 {
			return nil, fmt.Errorf("star gift message projection %d is not private", msgID)
		}
		target, err := q.GetMessageBoxForEdit(ctx, sqlcgen.GetMessageBoxForEditParams{
			OwnerUserID: source.Owner.ID, BoxID: int32(msgID), PeerType: peerType, PeerID: peerID,
		})
		if err != nil {
			return nil, fmt.Errorf("load star gift message projection: %w", err)
		}
		logicalKey := fmt.Sprintf("%d:%d", target.MessageSenderID, target.PrivateMessageID)
		if _, duplicate := seenPrivateMessages[logicalKey]; duplicate {
			continue
		}
		seenPrivateMessages[logicalKey] = struct{}{}

		boxes, err := q.ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
			OwnerUserIds:    privateMessageOwnerIDs(source.Owner.ID, peerID),
			MessageSenderID: target.MessageSenderID, PrivateMessageID: target.PrivateMessageID,
		})
		if err != nil {
			return nil, fmt.Errorf("list visible star gift message projections: %w", err)
		}
		var privateMediaJSON []byte
		matched := false
		for _, box := range boxes {
			media, err := decodeMessageMedia(box.MediaJson)
			if err != nil {
				return nil, fmt.Errorf("decode star gift message projection: %w", err)
			}
			if media == nil || media.Kind != domain.MessageMediaKindService || media.ServiceAction == nil ||
				media.ServiceAction.Kind != domain.MessageServiceActionStarGiftUnique || media.ServiceAction.StarGiftUnique == nil ||
				media.ServiceAction.StarGiftUnique.Gift.ID != current.ID {
				continue
			}
			matched = true
			action := media.ServiceAction.StarGiftUnique
			retiredGift := current
			retiredGift.CraftChancePermille = 0
			retiredGift.ResellAmount = nil
			action.Gift = retiredGift
			action.Peer = domain.Peer{}
			action.SavedID = 0
			action.Saved = false
			if validLifecyclePeer(current.Owner) && current.Owner != source.Owner {
				action.Transferred = true
			}
			action.CanExportAt = 0
			action.TransferStars = 0
			action.ResaleAmount = nil
			action.CanTransferAt = 0
			action.CanResellAt = 0
			action.DropOriginalDetailsStars = 0
			action.CanCraftAt = 0

			mediaJSON, err := encodeMessageMedia(media)
			if err != nil {
				return nil, fmt.Errorf("encode retired star gift projection: %w", err)
			}
			pts, err := s.messages.reservePts(ctx, tx, box.OwnerUserID)
			if err != nil {
				return nil, fmt.Errorf("allocate retired star gift pts: %w", err)
			}
			tag, err := tx.Exec(ctx, `
UPDATE message_boxes SET media=$3,pts=$4
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted`, box.OwnerUserID, box.BoxID, mediaJSON, int32(pts))
			if err != nil {
				return nil, fmt.Errorf("update retired star gift projection: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return nil, fmt.Errorf("update retired star gift projection lost row")
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
				Pts: pts, PtsCount: 1, Date: date, Message: msg}
			if err := appendUserUpdateEvent(ctx, tx, q, msg.OwnerUserID, event); err != nil {
				return nil, fmt.Errorf("append retired star gift edit event: %w", err)
			}
			if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
				TargetUserID: msg.OwnerUserID, Pts: int32(pts), EventType: string(domain.UpdateEventEditMessage),
				ExcludeAuthKeyID: 0, ExcludeSessionID: 0,
			}); err != nil {
				return nil, fmt.Errorf("enqueue retired star gift edit: %w", err)
			}
			if box.OwnerUserID == box.MessageSenderID || len(privateMediaJSON) == 0 {
				privateMediaJSON, err = encodeSharedPrivateStarGiftMedia(media)
				if err != nil {
					return nil, err
				}
			}
			edits = append(edits, domain.EditedMessageForUser{UserID: msg.OwnerUserID, Message: msg, Event: event})
		}
		if !matched {
			continue
		}
		if len(privateMediaJSON) == 0 {
			return nil, fmt.Errorf("retired star gift projection missing shared media")
		}
		if _, err := tx.Exec(ctx, `
UPDATE private_messages SET media=$3
WHERE sender_user_id=$1 AND id=$2`, target.MessageSenderID, target.PrivateMessageID, privateMediaJSON); err != nil {
			return nil, fmt.Errorf("update retired star gift private media: %w", err)
		}
	}
	return edits, nil
}
