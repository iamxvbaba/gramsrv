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

func (s *MessageStore) ReadMessageContents(ctx context.Context, req domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error) {
	res := domain.ReadMessageContentsResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 {
		return res, fmt.Errorf("read message contents: missing owner user id")
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	if len(req.IDs) > domain.MaxGetMessageIDs {
		return res, domain.ErrMessageIDInvalid
	}
	seen := make(map[int]struct{}, len(req.IDs))
	ids := make([]int32, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return res, domain.ErrMessageIDInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, int32(id))
	}
	if len(ids) == 0 {
		return res, nil
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return res, fmt.Errorf("read message contents: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin read message contents tx: %w", err)
	}
	qtx := sqlcgen.New(tx)
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()
	// First do a read-only pre-check to derive the peer senders involved, so the advisory lock is
	// still acquired uniformly in ascending user_id order, avoiding a crossed lock order with the
	// send/read paths.
	senderRows, err := tx.Query(ctx, `
SELECT DISTINCT message_sender_id
FROM message_boxes
WHERE owner_user_id = $1
  AND box_id = ANY($2::int[])
  AND NOT deleted
  AND media_unread
  AND NOT outgoing
  AND peer_type = 'user'
  AND message_sender_id <> $1`, req.OwnerUserID, ids)
	if err != nil {
		return res, fmt.Errorf("preview read message contents senders: %w", err)
	}
	lockIDs := []int64{req.OwnerUserID}
	for senderRows.Next() {
		var senderID int64
		if err := senderRows.Scan(&senderID); err != nil {
			senderRows.Close()
			return res, fmt.Errorf("scan read message contents sender: %w", err)
		}
		lockIDs = append(lockIDs, senderID)
	}
	if err := senderRows.Err(); err != nil {
		senderRows.Close()
		return res, fmt.Errorf("preview read message contents senders rows: %w", err)
	}
	senderRows.Close()
	if err := lockUsersForUpdate(ctx, tx, lockIDs...); err != nil {
		return res, fmt.Errorf("lock read message contents users: %w", err)
	}
	if err := lockDispatchOutboxAppendFences(ctx, tx, lockIDs); err != nil {
		return res, fmt.Errorf("lock read message contents dispatch append fences: %w", err)
	}
	rows, err := tx.Query(ctx, `
WITH target AS (
  SELECT owner_user_id, box_id, peer_type, peer_id, media_unread, reaction_unread,
         private_message_id, message_sender_id, outgoing
  FROM message_boxes
  WHERE owner_user_id = $1
    AND box_id = ANY($2::int[])
    AND NOT deleted
    AND (media_unread OR reaction_unread)
  FOR UPDATE
),
updated AS (
  UPDATE message_boxes
  SET media_unread = false,
      reaction_unread = false
  FROM target t
  WHERE message_boxes.owner_user_id = t.owner_user_id
    AND message_boxes.box_id = t.box_id
  RETURNING message_boxes.box_id, t.peer_type, t.peer_id, t.media_unread, t.reaction_unread,
            t.private_message_id, t.message_sender_id, t.outgoing
)
SELECT box_id, peer_type, peer_id, media_unread, reaction_unread, private_message_id, message_sender_id, outgoing
FROM updated
ORDER BY box_id`, req.OwnerUserID, ids)
	if err != nil {
		return res, fmt.Errorf("read message contents: %w", err)
	}
	defer rows.Close()
	affectedPeers := make(map[domain.Peer]struct{})
	senderPrivateMessageIDs := make(map[int64][]int64)
	for rows.Next() {
		var id int32
		var peerType string
		var peerID, privateMessageID, messageSenderID int64
		var mediaUnread, reactionUnread, outgoing bool
		if err := rows.Scan(&id, &peerType, &peerID, &mediaUnread, &reactionUnread, &privateMessageID, &messageSenderID, &outgoing); err != nil {
			return res, fmt.Errorf("scan read message contents: %w", err)
		}
		res.MessageIDs = append(res.MessageIDs, int(id))
		// Both reaction and media (voice/round video) unread clears must UPDATE the dialogs row:
		// the dialog_light trigger is attached only to the dialogs table, and flipping
		// message_boxes.media_unread does not bump the version. Without this extra step, the
		// sequence "voice read via history first, then listened to individually" would leave the
		// per-peer cached top message from getPeerDialogs carrying media_unread=true (the blue dot
		// never clears) until the conversation is invalidated by some other write.
		if (reactionUnread || mediaUnread) && peerID != 0 {
			affectedPeers[domain.Peer{Type: domain.PeerType(peerType), ID: peerID}] = struct{}{}
		}
		if mediaUnread && !outgoing && peerType == string(domain.PeerTypeUser) && messageSenderID != 0 && messageSenderID != req.OwnerUserID {
			senderPrivateMessageIDs[messageSenderID] = append(senderPrivateMessageIDs[messageSenderID], privateMessageID)
		}
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("read message contents rows: %w", err)
	}
	if len(res.MessageIDs) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return res, fmt.Errorf("commit read message contents noop: %w", err)
		}
		committed = true
		return res, nil
	}
	for peer := range affectedPeers {
		if peer.Type != domain.PeerTypeUser || peer.ID == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
UPDATE dialogs d
SET unread_reactions_count = (
    SELECT COUNT(*)::int
    FROM message_boxes m
    WHERE m.owner_user_id = d.user_id
      AND m.peer_type = d.peer_type
      AND m.peer_id = d.peer_id
      AND NOT m.deleted
      AND m.reaction_unread
),
updated_at = now()
WHERE d.user_id = $1
  AND d.peer_type = $2
  AND d.peer_id = $3`, req.OwnerUserID, string(peer.Type), peer.ID); err != nil {
			return res, fmt.Errorf("refresh dialog unread reactions after content read: %w", err)
		}
	}
	pts, err := s.reservePtsN(ctx, tx, req.OwnerUserID, len(res.MessageIDs))
	if err != nil {
		return res, fmt.Errorf("allocate read message contents pts: %w", err)
	}
	res.Event = domain.UpdateEvent{
		UserID:     req.OwnerUserID,
		Type:       domain.UpdateEventReadMessageContents,
		Pts:        pts,
		PtsCount:   len(res.MessageIDs),
		Date:       req.Date,
		MessageIDs: append([]int(nil), res.MessageIDs...),
	}
	if err := appendUserUpdateEvent(ctx, tx, qtx, req.OwnerUserID, res.Event); err != nil {
		return res, fmt.Errorf("append read message contents event: %w", err)
	}
	if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
		TargetUserID:     req.OwnerUserID,
		Pts:              int32(pts),
		EventType:        string(domain.UpdateEventReadMessageContents),
		ExcludeAuthKeyID: authKeyIDToInt64(req.OriginAuthKeyID),
		ExcludeSessionID: req.OriginSessionID,
	}); err != nil {
		return res, fmt.Errorf("enqueue read message contents dispatch: %w", err)
	}
	senderIDs := make([]int64, 0, len(senderPrivateMessageIDs))
	for senderID := range senderPrivateMessageIDs {
		senderIDs = append(senderIDs, senderID)
	}
	sort.Slice(senderIDs, func(i, j int) bool { return senderIDs[i] < senderIDs[j] })
	for _, senderID := range senderIDs {
		senderBoxRows, err := tx.Query(ctx, `
UPDATE message_boxes
SET media_unread = false
WHERE owner_user_id = $1
  AND private_message_id = ANY($2::bigint[])
  AND outgoing
  AND NOT deleted
  AND media_unread
RETURNING box_id`, senderID, senderPrivateMessageIDs[senderID])
		if err != nil {
			return res, fmt.Errorf("clear sender media unread: %w", err)
		}
		senderBoxIDs := make([]int, 0, len(senderPrivateMessageIDs[senderID]))
		for senderBoxRows.Next() {
			var boxID int32
			if err := senderBoxRows.Scan(&boxID); err != nil {
				senderBoxRows.Close()
				return res, fmt.Errorf("scan sender media unread box: %w", err)
			}
			senderBoxIDs = append(senderBoxIDs, int(boxID))
		}
		if err := senderBoxRows.Err(); err != nil {
			senderBoxRows.Close()
			return res, fmt.Errorf("sender media unread rows: %w", err)
		}
		senderBoxRows.Close()
		if len(senderBoxIDs) == 0 {
			continue
		}
		sort.Ints(senderBoxIDs)
		senderPts, err := s.reservePtsN(ctx, tx, senderID, len(senderBoxIDs))
		if err != nil {
			return res, fmt.Errorf("allocate sender content read pts: %w", err)
		}
		event := domain.UpdateEvent{
			UserID:     senderID,
			Type:       domain.UpdateEventReadMessageContents,
			Pts:        senderPts,
			PtsCount:   len(senderBoxIDs),
			Date:       req.Date,
			MessageIDs: senderBoxIDs,
		}
		if err := appendUserUpdateEvent(ctx, tx, qtx, senderID, event); err != nil {
			return res, fmt.Errorf("append sender content read event: %w", err)
		}
		if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
			TargetUserID: senderID,
			Pts:          int32(senderPts),
			EventType:    string(domain.UpdateEventReadMessageContents),
		}); err != nil {
			return res, fmt.Errorf("enqueue sender content read dispatch: %w", err)
		}
		res.SenderEvents = append(res.SenderEvents, event)
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit read message contents tx: %w", err)
	}
	committed = true
	return res, nil
}

func (s *MessageStore) GetOutboxReadDate(ctx context.Context, req domain.OutboxReadDateRequest) (int, error) {
	if req.OwnerUserID == 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return 0, domain.ErrMessageIDInvalid
	}
	if _, err := s.q.GetOutboxMessageForReadDate(ctx, sqlcgen.GetOutboxMessageForReadDateParams{
		OwnerUserID: req.OwnerUserID,
		PeerType:    string(req.Peer.Type),
		PeerID:      req.Peer.ID,
		BoxID:       int32(req.ID),
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, domain.ErrMessageIDInvalid
		}
		return 0, fmt.Errorf("get outbox message for read date: %w", err)
	}
	date, err := s.q.GetOutboxReadDate(ctx, sqlcgen.GetOutboxReadDateParams{
		UserID:    req.OwnerUserID,
		PeerType:  string(req.Peer.Type),
		PeerID:    req.Peer.ID,
		MessageID: int32(req.ID),
	})
	if err != nil {
		return 0, fmt.Errorf("get outbox read date: %w", err)
	}
	if date == 0 {
		return 0, domain.ErrMessageNotReadYet
	}
	return int(date), nil
}

type deletedUnreadMessages map[deletedOwnerPeerKey]map[int]struct{}

func loadDeleteUnreadCorrections(ctx context.Context, q *sqlcgen.Queries, deleted deletedUnreadMessages, date int) (map[int64][]domain.UpdateEvent, error) {
	if len(deleted) == 0 {
		return nil, nil
	}
	keys := make([]deletedOwnerPeerKey, 0, len(deleted))
	for key := range deleted {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].userID != keys[j].userID {
			return keys[i].userID < keys[j].userID
		}
		if keys[i].peer.Type != keys[j].peer.Type {
			return keys[i].peer.Type < keys[j].peer.Type
		}
		return keys[i].peer.ID < keys[j].peer.ID
	})
	out := make(map[int64][]domain.UpdateEvent)
	for _, key := range keys {
		deletedIDs := deleted[key]
		if len(deletedIDs) == 0 {
			continue
		}
		correctionMaxID := maxDeletedMessageID(deletedIDs)
		stillUnread := 0
		state, err := q.GetDialogReadStateForUpdate(ctx, sqlcgen.GetDialogReadStateForUpdateParams{
			UserID:   key.userID,
			PeerType: string(key.peer.Type),
			PeerID:   key.peer.ID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// The dialog row was removed along with this deletion (no surviving messages under
				// the peer), so there is no read boundary to advance and a dangling read correction
				// should not be sent for a conversation being discarded.
				continue
			}
			return nil, fmt.Errorf("load dialog read state after delete: %w", err)
		}
		readMax := int(state.ReadInboxMaxID)
		stillUnread = int(state.UnreadCount)
		correctionMaxID = readMax
		for nextID := readMax + 1; ; nextID++ {
			if _, ok := deletedIDs[nextID]; !ok {
				break
			}
			correctionMaxID = nextID
		}
		// TDesktop stores unread as a boundary plus count. A read-history update
		// must never advance the boundary across a still-live unread message, or the
		// client will stop sending a real readHistory for that message while the
		// server still considers it unread. Only deleted unread prefix items are safe
		// to skip over here.
		if correctionMaxID == readMax {
			continue
		}
		out[key.userID] = append(out[key.userID], domain.UpdateEvent{
			UserID:           key.userID,
			Type:             domain.UpdateEventReadHistoryInbox,
			PtsCount:         1,
			Date:             date,
			Peer:             key.peer,
			MaxID:            correctionMaxID,
			StillUnreadCount: stillUnread,
		})
	}
	return out, nil
}

func clampDialogReadInboxToMaxBox(ctx context.Context, tx pgx.Tx, ownerUserID int64, peer domain.Peer, maxID int) error {
	_, err := tx.Exec(ctx, `
UPDATE dialogs
SET
  read_inbox_max_id = $4,
  unread_count = 0,
  unread_mentions_count = 0,
  unread_reactions_count = 0,
  unread_mark = false,
  updated_at = now()
WHERE user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND read_inbox_max_id > $4`, ownerUserID, string(peer.Type), peer.ID, maxID)
	return err
}

// ListUnreadReactionMessages returns the current owner's reaction_unread messages under the
// peer (with the latest reactions aggregate), for messages.getUnreadReactions navigation.
func (s *MessageStore) ListUnreadReactionMessages(ctx context.Context, ownerUserID int64, peer domain.Peer, limit int) ([]domain.Message, error) {
	if ownerUserID == 0 || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > domain.MaxChannelUnreadReactionsLimit {
		limit = domain.MaxChannelUnreadReactionsLimit
	}
	rows, err := s.q.ListUnreadReactionMessageBoxes(ctx, sqlcgen.ListUnreadReactionMessageBoxesParams{
		OwnerUserID: ownerUserID,
		PeerType:    string(peer.Type),
		PeerID:      peer.ID,
		PageLimit:   int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list unread reaction messages: %w", err)
	}
	out := make([]domain.Message, 0, len(rows))
	for _, row := range rows {
		msg, err := messageFromBoxRow(sqlcgen.CreateMessageBoxRow(row))
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if err := s.enrichPrivateMessageReactions(ctx, s.db, ownerUserID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadPeerReactions clears all of the current owner's unread reaction state under the peer,
// corresponding to the private-chat branch of messages.readReactions.
//
// It must run in a single transaction and hold the same key-space (owner, peer) advisory lock as
// SetMessageReactions: otherwise "clear message_boxes.reaction_unread" and "reset the dialogs
// count" are two independent autocommit statements, with both an atomicity gap (stmt1 succeeds,
// stmt2 fails → the underlying state is cleared but the count and cache are still stale) and a
// lost-update when interleaved with a concurrent inbound SetMessageReactions (our hard reset to
// zero clobbers the peer's correctly incremented count, keeping the badge permanently low). The
// count is recomputed from surviving reaction_unread rows rather than hard-set to 0: this both
// preserves stale-counter self-healing of historical bad data (with no surviving rows COUNT is
// naturally 0) and, under concurrent interleaving, yields only a value consistent with the current
// box state.
func (s *MessageStore) ReadPeerReactions(ctx context.Context, ownerUserID int64, peer domain.Peer) (int, error) {
	if ownerUserID == 0 || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return 0, nil
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return 0, fmt.Errorf("read peer reactions: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin read peer reactions tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := lockUsersForUpdate(ctx, tx, ownerUserID, peer.ID); err != nil {
		return 0, fmt.Errorf("lock read peer reactions users: %w", err)
	}
	tag, err := tx.Exec(ctx, `
UPDATE message_boxes
SET reaction_unread = false
WHERE owner_user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND NOT deleted
  AND reaction_unread`, ownerUserID, string(peer.Type), peer.ID)
	if err != nil {
		return 0, fmt.Errorf("read peer reactions: %w", err)
	}
	// The count is always recomputed (and the dialogs row is unconditionally UPDATEed) to bump
	// dialog_light cache invalidation; the COUNT reads the post-clear box state in the same
	// transaction, so with no surviving rows it is 0, preserving stale-counter self-healing.
	if _, err := tx.Exec(ctx, `
UPDATE dialogs d
SET unread_reactions_count = (
    SELECT COUNT(*)::int
    FROM message_boxes m
    WHERE m.owner_user_id = d.user_id
      AND m.peer_type = d.peer_type
      AND m.peer_id = d.peer_id
      AND NOT m.deleted
      AND m.reaction_unread
),
updated_at = now()
WHERE d.user_id = $1 AND d.peer_type = $2 AND d.peer_id = $3`, ownerUserID, string(peer.Type), peer.ID); err != nil {
		return 0, fmt.Errorf("reset dialog unread reactions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit read peer reactions tx: %w", err)
	}
	committed = true
	return int(tag.RowsAffected()), nil
}
