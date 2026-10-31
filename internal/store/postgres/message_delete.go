package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
	"time"
)

func (s *MessageStore) DeleteMessages(ctx context.Context, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error) {
	res := domain.DeleteMessagesResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 {
		return res, fmt.Errorf("delete messages: missing owner user id")
	}
	ids := normalizeMessageIDs(req.IDs)
	if len(ids) == 0 {
		return res, nil
	}
	if len(ids) > domain.MaxDeleteMessageIDs {
		return res, fmt.Errorf("delete messages: too many ids: %d > %d", len(ids), domain.MaxDeleteMessageIDs)
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	lockUserIDs := []int64{req.OwnerUserID}
	if req.Revoke {
		peers, err := s.revokeDeleteLockPeers(ctx, req.OwnerUserID, ids)
		if err != nil {
			return res, fmt.Errorf("load delete revoke peers: %w", err)
		}
		lockUserIDs = append(lockUserIDs, peers...)
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return res, fmt.Errorf("delete messages: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin delete messages tx: %w", err)
	}
	qtx := sqlcgen.New(tx)
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()

	// The advisory lock serializes the owner and the private peers affected by revoke; it must be acquired before any row lock.
	if err := lockUsersForUpdate(ctx, tx, lockUserIDs...); err != nil {
		return res, fmt.Errorf("lock delete messages user: %w", err)
	}
	if err := lockDispatchOutboxAppendFences(ctx, tx, lockUserIDs); err != nil {
		return res, fmt.Errorf("lock delete messages dispatch append fences: %w", err)
	}

	rows, err := qtx.DeleteMessageBoxesByIDs(ctx, sqlcgen.DeleteMessageBoxesByIDsParams{
		OwnerUserID: req.OwnerUserID,
		BoxIds:      int32s(ids),
	})
	if err != nil {
		return res, fmt.Errorf("delete message boxes by ids: %w", err)
	}
	deleted := deletedRowsFromIDRows(rows)
	if req.Revoke && len(deleted) > 0 {
		peerRows, err := qtx.DeleteMessageBoxesByPrivateMessages(ctx, privateMessageDeleteParams(deleted))
		if err != nil {
			return res, fmt.Errorf("delete revoked private message boxes: %w", err)
		}
		deleted = append(deleted, deletedRowsFromPrivateRows(peerRows)...)
	}
	res, err = s.finishDeleteMessagesTx(ctx, tx, qtx, req.OwnerUserID, req.OriginAuthKeyID, req.OriginSessionID, req.Date, deleted, nil)
	if err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit delete messages tx: %w", err)
	}
	committed = true
	return res, nil
}

func (s *MessageStore) revokeDeleteLockPeers(ctx context.Context, ownerUserID int64, ids []int) ([]int64, error) {
	rows, err := s.q.GetMessageBoxesByIDs(ctx, sqlcgen.GetMessageBoxesByIDsParams{
		OwnerUserID: ownerUserID,
		BoxIds:      int32s(ids),
	})
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]struct{}, len(rows))
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.PeerType != string(domain.PeerTypeUser) || row.PeerID == 0 || row.PeerID == ownerUserID {
			continue
		}
		if _, ok := seen[row.PeerID]; ok {
			continue
		}
		seen[row.PeerID] = struct{}{}
		out = append(out, row.PeerID)
	}
	return out, nil
}

type deletedBox struct {
	ownerUserID      int64
	boxID            int
	privateMessageID int64
	messageSenderID  int64
	peer             domain.Peer
}

type deletedOwnerPeerKey struct {
	userID int64
	peer   domain.Peer
}

type historyClearAnchor struct {
	userID       int64
	peer         domain.Peer
	boxID        int
	uid          int64
	messageDate  int
	materialized bool
}

func (s *MessageStore) loadHistoryClearAnchor(ctx context.Context, q *sqlcgen.Queries, userID int64, peer domain.Peer) (historyClearAnchor, bool, error) {
	top, err := q.TopVisibleMessageBoxByPeer(ctx, sqlcgen.TopVisibleMessageBoxByPeerParams{
		OwnerUserID: userID,
		PeerType:    string(peer.Type),
		PeerID:      peer.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return historyClearAnchor{}, false, nil
	}
	if err != nil {
		return historyClearAnchor{}, false, fmt.Errorf("load history clear top: %w", err)
	}
	row, err := q.GetMessageBoxForEdit(ctx, sqlcgen.GetMessageBoxForEditParams{
		OwnerUserID: userID,
		BoxID:       top.BoxID,
		PeerType:    string(peer.Type),
		PeerID:      peer.ID,
	})
	if err != nil {
		return historyClearAnchor{}, false, fmt.Errorf("lock history clear top: %w", err)
	}
	media, err := decodeMessageMedia(row.MediaJson)
	if err != nil {
		return historyClearAnchor{}, false, fmt.Errorf("decode history clear top media: %w", err)
	}
	return historyClearAnchor{
		userID:       userID,
		peer:         peer,
		boxID:        int(row.BoxID),
		uid:          row.PrivateMessageID,
		messageDate:  int(row.MessageDate),
		materialized: domain.IsHistoryClearServiceMessage(domain.Message{Media: media}),
	}, true, nil
}

func (s *MessageStore) finishDeleteMessagesTx(ctx context.Context, db sqlcgen.DBTX, q *sqlcgen.Queries, ownerUserID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, date int, rows []deletedBox, anchors map[int64]historyClearAnchor) (domain.DeleteMessagesResult, error) {
	res := domain.DeleteMessagesResult{OwnerUserID: ownerUserID}
	if len(rows) == 0 && len(anchors) == 0 {
		return res, nil
	}
	peersByOwner := make(map[int64]map[domain.Peer]struct{})
	idsByOwner := make(map[int64][]int)
	incomingDeletedByPeer := make(deletedUnreadMessages)
	for _, row := range rows {
		if row.ownerUserID == 0 || row.boxID == 0 {
			continue
		}
		idsByOwner[row.ownerUserID] = append(idsByOwner[row.ownerUserID], row.boxID)
		if row.peer.ID != 0 {
			if peersByOwner[row.ownerUserID] == nil {
				peersByOwner[row.ownerUserID] = make(map[domain.Peer]struct{})
			}
			peersByOwner[row.ownerUserID][row.peer] = struct{}{}
		}
		if row.peer.ID != 0 && row.messageSenderID != 0 && row.messageSenderID != row.ownerUserID {
			key := deletedOwnerPeerKey{userID: row.ownerUserID, peer: row.peer}
			if incomingDeletedByPeer[key] == nil {
				incomingDeletedByPeer[key] = make(map[int]struct{})
			}
			incomingDeletedByPeer[key][row.boxID] = struct{}{}
		}
	}
	for userID, anchor := range anchors {
		if anchor.boxID <= 0 || anchor.peer.ID == 0 {
			continue
		}
		if peersByOwner[userID] == nil {
			peersByOwner[userID] = make(map[domain.Peer]struct{})
		}
		peersByOwner[userID][anchor.peer] = struct{}{}
		if !anchor.materialized {
			// Materializing the anchor for the first time uses a real read update with max_id=anchor
			// that overrides all read corrections for that peer; the incoming prefix deleted in this
			// batch must not advance again.
			delete(incomingDeletedByPeer, deletedOwnerPeerKey{userID: userID, peer: anchor.peer})
		}
	}
	// Rebuild dialogs in ascending owner order, so that two reverse deletes (X deleting the
	// conversation with Y / Y deleting the conversation with X) acquire dialog row locks in a
	// consistent order, and together with the ascending watermark advance below, completely avoid
	// the AB-BA deadlock between delete and delete.
	rebuildOwners := make([]int64, 0, len(peersByOwner))
	for userID := range peersByOwner {
		rebuildOwners = append(rebuildOwners, userID)
	}
	sort.Slice(rebuildOwners, func(i, j int) bool { return rebuildOwners[i] < rebuildOwners[j] })
	for _, userID := range rebuildOwners {
		for peer := range peersByOwner[userID] {
			if anchor, ok := anchors[userID]; ok && anchor.peer == peer && !anchor.materialized {
				// Allocate the edit PTS and convert the anchor in place first, then recompute the
				// dialog from the converted outgoing state; otherwise the incoming anchor would be
				// briefly counted as unread.
				continue
			}
			if err := rebuildDialogAfterMessageDelete(ctx, q, userID, peer); err != nil {
				return res, err
			}
		}
	}
	readCorrectionsByOwner, err := loadDeleteUnreadCorrections(ctx, q, incomingDeletedByPeer, date)
	if err != nil {
		return res, err
	}

	ownerSet := make(map[int64]struct{}, len(idsByOwner)+len(anchors))
	for userID := range idsByOwner {
		ownerSet[userID] = struct{}{}
	}
	for userID, anchor := range anchors {
		if !anchor.materialized {
			ownerSet[userID] = struct{}{}
		}
	}
	ownerIDs := make([]int64, 0, len(ownerSet))
	for userID := range ownerSet {
		ownerIDs = append(ownerIDs, userID)
	}
	sort.Slice(ownerIDs, func(i, j int) bool { return ownerIDs[i] < ownerIDs[j] })

	res.Deleted = make([]domain.DeletedMessagesForUser, 0, len(ownerIDs))
	for _, userID := range ownerIDs {
		ids := normalizeMessageIDs(idsByOwner[userID])
		corrections := readCorrectionsByOwner[userID]
		anchor, hasAnchor := anchors[userID]
		materializeAnchor := hasAnchor && !anchor.materialized
		totalPtsCount := len(ids) + len(corrections)
		if materializeAnchor {
			totalPtsCount += 2 // updateReadHistoryInbox + updateEditMessage
		}
		if totalPtsCount == 0 {
			continue
		}
		pts, err := s.reservePtsN(ctx, db, userID, totalPtsCount)
		if err != nil {
			return res, fmt.Errorf("allocate delete messages pts: %w", err)
		}
		cursor := pts - totalPtsCount
		item := domain.DeletedMessagesForUser{
			UserID:     userID,
			MessageIDs: ids,
			Pts:        pts,
			PtsCount:   totalPtsCount,
			Events:     make([]domain.UpdateEvent, 0, 1+len(corrections)+2),
		}
		dispatchAuthKeyID := [8]byte{}
		dispatchSessionID := int64(0)
		if userID == ownerUserID {
			dispatchAuthKeyID = excludeAuthKeyID
			dispatchSessionID = excludeSessionID
		}
		if len(ids) > 0 {
			cursor += len(ids)
			event := domain.UpdateEvent{
				UserID:     userID,
				Type:       domain.UpdateEventDeleteMessages,
				Pts:        cursor,
				PtsCount:   len(ids),
				Date:       date,
				MessageIDs: ids,
			}
			deleteIDsJSON, err := encodeEventMessageIDs(event.MessageIDs)
			if err != nil {
				return res, fmt.Errorf("encode sender delete receipt ids: %w", err)
			}
			senderPrivateIDs := make([]int64, 0, len(rows))
			for _, row := range rows {
				if row.ownerUserID == userID && row.messageSenderID == userID && row.privateMessageID != 0 {
					senderPrivateIDs = append(senderPrivateIDs, row.privateMessageID)
				}
			}
			if len(senderPrivateIDs) > 0 {
				if _, err := db.Exec(ctx, `
UPDATE private_messages
SET sender_delete_pts = $3,
    sender_delete_pts_count = $4,
    sender_delete_date = $5,
    sender_delete_message_ids = $6::jsonb
WHERE sender_user_id = $1
  AND id = ANY($2::bigint[])
  AND sender_box_id > 0`, userID, senderPrivateIDs, event.Pts, event.PtsCount, event.Date, deleteIDsJSON); err != nil {
					return res, fmt.Errorf("save sender delete replay receipt: %w", err)
				}
			}
			if err := appendDeleteMessagesEvent(ctx, q, event); err != nil {
				return res, err
			}
			if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
				TargetUserID:     userID,
				Pts:              int32(event.Pts),
				EventType:        string(domain.UpdateEventDeleteMessages),
				ExcludeAuthKeyID: authKeyIDToInt64(dispatchAuthKeyID),
				ExcludeSessionID: dispatchSessionID,
			}); err != nil {
				return res, fmt.Errorf("enqueue delete messages dispatch: %w", err)
			}
			item.Event = event
			item.Events = append(item.Events, event)
		}
		for _, correction := range corrections {
			cursor++
			correction.Pts = cursor
			if err := appendUserUpdateEvent(ctx, db, q, userID, correction); err != nil {
				return res, fmt.Errorf("append delete unread correction event: %w", err)
			}
			// The dialog snapshot must match the watermark announced by the update stream;
			// otherwise a later readHistory would use the old watermark as its baseline and re-send
			// read events.
			if err := q.AdvanceDialogReadInboxFloor(ctx, sqlcgen.AdvanceDialogReadInboxFloorParams{
				UserID:         userID,
				PeerType:       string(correction.Peer.Type),
				PeerID:         correction.Peer.ID,
				ReadInboxMaxID: int32(correction.MaxID),
			}); err != nil {
				return res, fmt.Errorf("advance dialog read inbox after delete correction: %w", err)
			}
			if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
				TargetUserID: userID,
				Pts:          int32(correction.Pts),
				EventType:    string(domain.UpdateEventReadHistoryInbox),
			}); err != nil {
				return res, fmt.Errorf("enqueue delete unread correction dispatch: %w", err)
			}
			item.Events = append(item.Events, correction)
		}
		if materializeAnchor {
			readPts := cursor + 1
			editPts := readPts + 1
			msg, err := materializeHistoryClearAnchorTx(ctx, db, anchor, editPts)
			if err != nil {
				return res, err
			}
			if err := rebuildDialogAfterMessageDelete(ctx, q, userID, anchor.peer); err != nil {
				return res, err
			}
			readEvent := domain.UpdateEvent{
				UserID:           userID,
				Type:             domain.UpdateEventReadHistoryInbox,
				Pts:              readPts,
				PtsCount:         1,
				Date:             date,
				Peer:             anchor.peer,
				MaxID:            anchor.boxID,
				StillUnreadCount: 0,
			}
			if err := appendUserUpdateEvent(ctx, db, q, userID, readEvent); err != nil {
				return res, fmt.Errorf("append history clear read event: %w", err)
			}
			if err := q.AdvanceDialogReadInboxFloor(ctx, sqlcgen.AdvanceDialogReadInboxFloorParams{
				UserID:         userID,
				PeerType:       string(anchor.peer.Type),
				PeerID:         anchor.peer.ID,
				ReadInboxMaxID: int32(anchor.boxID),
			}); err != nil {
				return res, fmt.Errorf("advance history clear read inbox: %w", err)
			}
			if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
				TargetUserID:     userID,
				Pts:              int32(readPts),
				EventType:        string(domain.UpdateEventReadHistoryInbox),
				ExcludeAuthKeyID: authKeyIDToInt64(dispatchAuthKeyID),
				ExcludeSessionID: dispatchSessionID,
			}); err != nil {
				return res, fmt.Errorf("enqueue history clear read dispatch: %w", err)
			}
			editEvent := domain.UpdateEvent{
				UserID:   userID,
				Type:     domain.UpdateEventEditMessage,
				Pts:      editPts,
				PtsCount: 1,
				Date:     date,
				Message:  msg,
			}
			if err := appendUserUpdateEvent(ctx, db, q, userID, editEvent); err != nil {
				return res, fmt.Errorf("append history clear edit event: %w", err)
			}
			if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
				TargetUserID:     userID,
				Pts:              int32(editPts),
				EventType:        string(domain.UpdateEventEditMessage),
				ExcludeAuthKeyID: authKeyIDToInt64(dispatchAuthKeyID),
				ExcludeSessionID: dispatchSessionID,
			}); err != nil {
				return res, fmt.Errorf("enqueue history clear edit dispatch: %w", err)
			}
			item.Events = append(item.Events, readEvent, editEvent)
			cursor = editPts
		}
		if cursor != pts {
			return res, fmt.Errorf("delete history pts cursor %d does not reach reserved pts %d", cursor, pts)
		}
		res.Deleted = append(res.Deleted, item)
	}
	return res, nil
}

func materializeHistoryClearAnchorTx(ctx context.Context, db sqlcgen.DBTX, anchor historyClearAnchor, pts int) (domain.Message, error) {
	msg := domain.NewHistoryClearMessage(anchor.userID, anchor.peer, anchor.boxID, anchor.uid, anchor.messageDate, pts)
	mediaJSON, err := encodeMessageMedia(msg.Media)
	if err != nil {
		return domain.Message{}, fmt.Errorf("encode history clear media: %w", err)
	}
	tag, err := db.Exec(ctx, `
UPDATE message_boxes
SET from_user_id = $3,
    ttl_period = 0,
    expires_at = 0,
    edit_date = 0,
    hide_edited = false,
    outgoing = true,
    body = '',
    entities = '[]'::jsonb,
    silent = false,
    noforwards = false,
    reply_to_msg_id = 0,
    reply_to_peer_type = '',
    reply_to_peer_id = 0,
    reply_to_top_id = 0,
    reply_to_story_id = 0,
    quote_text = '',
    quote_entities = '[]'::jsonb,
    quote_offset = 0,
    fwd_from_peer_type = '',
    fwd_from_peer_id = 0,
    fwd_from_name = '',
    fwd_date = 0,
    fwd_saved_from_peer_type = '',
    fwd_saved_from_peer_id = 0,
    fwd_saved_from_msg_id = 0,
    saved_peer_type = '',
    saved_peer_id = 0,
    pts = $4,
    media = $5::jsonb,
    media_unread = false,
    reaction_unread = false,
    pinned = false,
    via_bot_id = 0,
    grouped_id = 0,
    effect = 0,
    reply_markup = '{}'::jsonb,
    rich_message = '{}'::jsonb
WHERE owner_user_id = $1
  AND box_id = $2
  AND NOT deleted`, anchor.userID, int32(anchor.boxID), anchor.userID, int32(pts), mediaJSON)
	if err != nil {
		return domain.Message{}, fmt.Errorf("materialize history clear anchor: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.Message{}, fmt.Errorf("materialize history clear anchor: box %d disappeared", anchor.boxID)
	}
	if _, err := db.Exec(ctx, `DELETE FROM message_box_media WHERE owner_user_id = $1 AND box_id = $2`, anchor.userID, int32(anchor.boxID)); err != nil {
		return domain.Message{}, fmt.Errorf("delete history clear media index: %w", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM saved_message_reaction_tags WHERE user_id = $1 AND message_box_id = $2`, anchor.userID, int32(anchor.boxID)); err != nil {
		return domain.Message{}, fmt.Errorf("delete history clear saved tags: %w", err)
	}
	return msg, nil
}

func maxDeletedMessageID(ids map[int]struct{}) int {
	maxID := 0
	for id := range ids {
		if id > maxID {
			maxID = id
		}
	}
	return maxID
}

func rebuildDialogAfterMessageDelete(ctx context.Context, q *sqlcgen.Queries, userID int64, peer domain.Peer) error {
	top, err := q.TopVisibleMessageBoxByPeer(ctx, sqlcgen.TopVisibleMessageBoxByPeerParams{
		OwnerUserID: userID,
		PeerType:    string(peer.Type),
		PeerID:      peer.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if err := q.DeleteDialogByPeer(ctx, sqlcgen.DeleteDialogByPeerParams{
			UserID:   userID,
			PeerType: string(peer.Type),
			PeerID:   peer.ID,
		}); err != nil {
			return fmt.Errorf("delete empty dialog after message delete: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("load top message after delete: %w", err)
	}
	if err := q.RefreshDialogAfterMessageDelete(ctx, sqlcgen.RefreshDialogAfterMessageDeleteParams{
		TopMessageID:   top.BoxID,
		TopMessageDate: top.MessageDate,
		UserID:         userID,
		PeerType:       string(peer.Type),
		PeerID:         peer.ID,
	}); err != nil {
		return fmt.Errorf("refresh dialog after message delete: %w", err)
	}
	return nil
}

func appendDeleteMessagesEvent(ctx context.Context, q *sqlcgen.Queries, event domain.UpdateEvent) error {
	messageIDs, err := encodeEventMessageIDs(event.MessageIDs)
	if err != nil {
		return err
	}
	if event.PtsCount == 0 {
		event.PtsCount = len(event.MessageIDs)
	}
	if event.PtsCount == 0 {
		event.PtsCount = 1
	}
	if err := q.AppendUserUpdateEvent(ctx, sqlcgen.AppendUserUpdateEventParams{
		UserID:             event.UserID,
		Pts:                int32(event.Pts),
		PtsCount:           int32(event.PtsCount),
		Date:               int32(event.Date),
		EventType:          string(domain.UpdateEventDeleteMessages),
		EventPeers:         []byte("[]"),
		PeerSettings:       []byte("{}"),
		MessageIds:         messageIDs,
		DialogFilter:       []byte("{}"),
		FilterOrder:        []byte("[]"),
		FolderPeers:        []byte("[]"),
		StoryPayload:       []byte("{}"),
		ReactionPayload:    []byte("{}"),
		EmojiStatusPayload: []byte("{}"),
	}); err != nil {
		return fmt.Errorf("append delete messages event: %w", err)
	}
	return nil
}

func deletedRowsFromIDRows(rows []sqlcgen.DeleteMessageBoxesByIDsRow) []deletedBox {
	out := make([]deletedBox, 0, len(rows))
	for _, row := range rows {
		out = append(out, deletedBox{
			ownerUserID:      row.OwnerUserID,
			boxID:            int(row.BoxID),
			privateMessageID: row.PrivateMessageID,
			messageSenderID:  row.MessageSenderID,
			peer:             domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		})
	}
	return out
}

func deletedRowsFromPeerRows(rows []sqlcgen.DeleteMessageBoxesByPeerRow) []deletedBox {
	out := make([]deletedBox, 0, len(rows))
	for _, row := range rows {
		out = append(out, deletedBox{
			ownerUserID:      row.OwnerUserID,
			boxID:            int(row.BoxID),
			privateMessageID: row.PrivateMessageID,
			messageSenderID:  row.MessageSenderID,
			peer:             domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		})
	}
	return out
}

func deletedRowsFromPeerBatchRows(rows []sqlcgen.DeleteMessageBoxesByPeerBatchRow) []deletedBox {
	out := make([]deletedBox, 0, len(rows))
	for _, row := range rows {
		out = append(out, deletedBox{
			ownerUserID:      row.OwnerUserID,
			boxID:            int(row.BoxID),
			privateMessageID: row.PrivateMessageID,
			messageSenderID:  row.MessageSenderID,
			peer:             domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		})
	}
	return out
}

func deletedRowsFromPrivateRows(rows []sqlcgen.DeleteMessageBoxesByPrivateMessagesRow) []deletedBox {
	out := make([]deletedBox, 0, len(rows))
	for _, row := range rows {
		out = append(out, deletedBox{
			ownerUserID:      row.OwnerUserID,
			boxID:            int(row.BoxID),
			privateMessageID: row.PrivateMessageID,
			messageSenderID:  row.MessageSenderID,
			peer:             domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		})
	}
	return out
}

func privateMessageDeleteParams(rows []deletedBox) sqlcgen.DeleteMessageBoxesByPrivateMessagesParams {
	senderIDs := make([]int64, 0, len(rows))
	privateIDs := make([]int64, 0, len(rows))
	ownerIDs := make([]int64, 0, len(rows)*2)
	seen := make(map[[3]int64]struct{}, len(rows)*2)
	for _, row := range rows {
		if row.messageSenderID == 0 || row.privateMessageID == 0 {
			continue
		}
		owners := privateMessageOwnerIDs(row.ownerUserID, 0)
		if row.peer.Type == domain.PeerTypeUser {
			owners = privateMessageOwnerIDs(row.ownerUserID, row.peer.ID)
		}
		for _, ownerID := range owners {
			key := [3]int64{row.messageSenderID, row.privateMessageID, ownerID}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			senderIDs = append(senderIDs, row.messageSenderID)
			privateIDs = append(privateIDs, row.privateMessageID)
			ownerIDs = append(ownerIDs, ownerID)
		}
	}
	return sqlcgen.DeleteMessageBoxesByPrivateMessagesParams{
		MessageSenderIds:  senderIDs,
		PrivateMessageIds: privateIDs,
		OwnerUserIds:      ownerIDs,
	}
}

func privateMessageOwnerIDs(first, second int64) []int64 {
	switch {
	case first == 0 && second == 0:
		return nil
	case first == 0 || first == second:
		return []int64{second}
	case second == 0:
		return []int64{first}
	default:
		if first < second {
			return []int64{first, second}
		}
		return []int64{second, first}
	}
}
