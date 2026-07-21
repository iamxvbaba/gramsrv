package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// markCraftInputMessagesTx makes the chat projection part of the same commit
// as the craft outcome. TDesktop derives the Craft entry directly from the
// messageActionStarGiftUnique snapshot, so changing only peer_star_gifts and
// unique_star_gifts would leave an already-burned input actionable.
func (s *StarGiftLifecycleStore) markCraftInputMessagesTx(
	ctx context.Context,
	tx pgx.Tx,
	req domain.StarGiftCraftRequest,
	savedIDs []int64,
) ([]domain.EditedMessageForUser, []int32, error) {
	edits := make([]domain.EditedMessageForUser, 0, len(savedIDs)*2)
	ownerPTS := make([]int32, 0, len(savedIDs))
	for _, savedID := range savedIDs {
		saved, found, err := savedStarGiftByID(ctx, tx, savedID)
		if err != nil || !found || saved.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) ||
			saved.UniqueGiftID <= 0 || saved.UpgradeMsgID <= 0 {
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, domain.ErrStarGiftCraftUnavailable
		}
		unique, found, err := NewStarGiftStore(tx).UniqueByID(ctx, saved.UniqueGiftID)
		if err != nil || !found {
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, domain.ErrStarGiftCraftUnavailable
		}
		inputEdits, ownerPT, err := s.markCraftInputMessageTx(ctx, tx, req, saved, unique)
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, inputEdits...)
		ownerPTS = append(ownerPTS, int32(ownerPT))
	}
	return edits, ownerPTS, nil
}

func (s *StarGiftLifecycleStore) markCraftInputMessageTx(
	ctx context.Context,
	tx pgx.Tx,
	req domain.StarGiftCraftRequest,
	saved domain.SavedStarGift,
	unique domain.UniqueStarGift,
) ([]domain.EditedMessageForUser, int, error) {
	q := sqlcgen.New(tx)
	target, err := q.GetMessageBoxForEdit(ctx, sqlcgen.GetMessageBoxForEditParams{
		OwnerUserID: req.UserID,
		BoxID:       int32(saved.UpgradeMsgID),
		PeerType:    string(domain.PeerTypeUser),
		PeerID:      saved.FromUserID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, domain.ErrStarGiftCraftUnavailable
		}
		return nil, 0, fmt.Errorf("lock craft input message: %w", err)
	}
	boxes, err := q.ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
		OwnerUserIds:     privateMessageOwnerIDs(req.UserID, saved.FromUserID),
		MessageSenderID:  target.MessageSenderID,
		PrivateMessageID: target.PrivateMessageID,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list craft input message boxes: %w", err)
	}
	if len(boxes) == 0 {
		return nil, 0, domain.ErrStarGiftCraftUnavailable
	}

	edits := make([]domain.EditedMessageForUser, 0, len(boxes))
	ownerPTS := 0
	var privateMediaJSON []byte
	for _, box := range boxes {
		media, err := decodeMessageMedia(box.MediaJson)
		if err != nil {
			return nil, 0, fmt.Errorf("decode craft input message media: %w", err)
		}
		if media == nil || media.Kind != domain.MessageMediaKindService || media.ServiceAction == nil ||
			media.ServiceAction.Kind != domain.MessageServiceActionStarGiftUnique || media.ServiceAction.StarGiftUnique == nil ||
			media.ServiceAction.StarGiftUnique.Gift.ID != unique.ID {
			return nil, 0, fmt.Errorf("craft input message %d has invalid unique gift projection", box.BoxID)
		}
		action := media.ServiceAction.StarGiftUnique
		action.Gift = unique
		action.Saved = saved.LifecycleStatus.Live() && !saved.Unsaved
		action.CanExportAt = saved.CanExportAt
		action.TransferStars = saved.TransferStars
		action.CanTransferAt = saved.CanTransferAt
		action.CanResellAt = saved.CanResellAt
		action.DropOriginalDetailsStars = saved.DropOriginalDetailsStars
		action.CanCraftAt = saved.CanCraftAt

		mediaJSON, err := encodeMessageMedia(media)
		if err != nil {
			return nil, 0, fmt.Errorf("encode craft input message media: %w", err)
		}
		pts, err := s.messages.reservePts(ctx, tx, box.OwnerUserID)
		if err != nil {
			return nil, 0, fmt.Errorf("allocate craft input edit pts: %w", err)
		}
		tag, err := tx.Exec(ctx, `
UPDATE message_boxes SET media=$3,pts=$4
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted`, box.OwnerUserID, box.BoxID, mediaJSON, int32(pts))
		if err != nil {
			return nil, 0, fmt.Errorf("update craft input message box: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil, 0, fmt.Errorf("update craft input message box lost row")
		}
		msg, err := messageFromVisibleBoxRow(box)
		if err != nil {
			return nil, 0, err
		}
		msg.Media = media
		msg.Pts = pts
		if err := replaceMessageBoxMediaIndexTx(ctx, tx, msg.OwnerUserID, msg.Peer.ID, msg.ID, msg.Date, msg.Media, msg.Entities); err != nil {
			return nil, 0, err
		}
		event := domain.UpdateEvent{UserID: msg.OwnerUserID, Type: domain.UpdateEventEditMessage,
			Pts: pts, PtsCount: 1, Date: req.Date, Message: msg}
		if err := appendUserUpdateEvent(ctx, tx, q, msg.OwnerUserID, event); err != nil {
			return nil, 0, fmt.Errorf("append craft input edit event: %w", err)
		}
		dispatchAuthKeyID := [8]byte{}
		dispatchSessionID := int64(0)
		if msg.OwnerUserID == req.UserID {
			dispatchAuthKeyID = req.OriginAuthKeyID
			dispatchSessionID = req.OriginSessionID
			ownerPTS = pts
		}
		if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
			TargetUserID: msg.OwnerUserID, Pts: int32(pts), EventType: string(domain.UpdateEventEditMessage),
			ExcludeAuthKeyID: authKeyIDToInt64(dispatchAuthKeyID), ExcludeSessionID: dispatchSessionID,
		}); err != nil {
			return nil, 0, fmt.Errorf("enqueue craft input edit: %w", err)
		}
		if box.OwnerUserID == box.MessageSenderID || len(privateMediaJSON) == 0 {
			privateMediaJSON, err = encodeSharedPrivateStarGiftMedia(media)
			if err != nil {
				return nil, 0, err
			}
		}
		edits = append(edits, domain.EditedMessageForUser{UserID: msg.OwnerUserID, Message: msg, Event: event})
	}
	if ownerPTS <= 0 || len(privateMediaJSON) == 0 {
		return nil, 0, fmt.Errorf("craft input message missing owner projection")
	}
	if _, err := tx.Exec(ctx, `
UPDATE private_messages SET media=$3
WHERE sender_user_id=$1 AND id=$2`, target.MessageSenderID, target.PrivateMessageID, privateMediaJSON); err != nil {
		return nil, 0, fmt.Errorf("update craft input private message: %w", err)
	}
	return edits, ownerPTS, nil
}

func (s *StarGiftLifecycleStore) loadCraftInputMessageReplays(
	ctx context.Context,
	req domain.StarGiftCraftRequest,
	savedIDs []int64,
	ptsValues []int32,
) ([]domain.EditedMessageForUser, error) {
	if len(savedIDs) != len(ptsValues) {
		return nil, domain.ErrStarGiftCraftUnavailable
	}
	edits := make([]domain.EditedMessageForUser, 0, len(savedIDs))
	for i, savedID := range savedIDs {
		saved, found, err := savedStarGiftByID(ctx, s.db, savedID)
		if err != nil || !found || saved.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) ||
			saved.UpgradeMsgID <= 0 || ptsValues[i] <= 0 {
			if err != nil {
				return nil, err
			}
			return nil, domain.ErrStarGiftCraftUnavailable
		}
		var privateMessageID, messageSenderID int64
		err = s.db.QueryRow(ctx, `
SELECT private_message_id,message_sender_id FROM message_boxes
WHERE owner_user_id=$1 AND box_id=$2 AND peer_type='user' AND peer_id=$3 AND NOT deleted`,
			req.UserID, saved.UpgradeMsgID, saved.FromUserID).Scan(&privateMessageID, &messageSenderID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load craft input replay message: %w", err)
		}
		boxes, err := sqlcgen.New(s.db).ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
			OwnerUserIds: []int64{req.UserID}, MessageSenderID: messageSenderID, PrivateMessageID: privateMessageID,
		})
		if err != nil {
			return nil, fmt.Errorf("load craft input replay box: %w", err)
		}
		if len(boxes) != 1 || int(boxes[0].BoxID) != saved.UpgradeMsgID {
			return nil, domain.ErrStarGiftCraftUnavailable
		}
		var eventDate int
		err = s.db.QueryRow(ctx, `
SELECT date FROM user_update_events
WHERE user_id=$1 AND pts=$2 AND event_type='edit_message' AND message_box_id=$3`,
			req.UserID, ptsValues[i], saved.UpgradeMsgID).Scan(&eventDate)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, domain.ErrStarGiftCraftUnavailable
			}
			return nil, fmt.Errorf("load craft input replay event: %w", err)
		}
		msg, err := messageFromVisibleBoxRow(boxes[0])
		if err != nil {
			return nil, err
		}
		msg.Pts = int(ptsValues[i])
		event := domain.UpdateEvent{UserID: req.UserID, Type: domain.UpdateEventEditMessage,
			Pts: int(ptsValues[i]), PtsCount: 1, Date: eventDate, Message: msg}
		edits = append(edits, domain.EditedMessageForUser{UserID: req.UserID, Message: msg, Event: event})
	}
	return edits, nil
}
