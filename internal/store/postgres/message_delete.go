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

	// advisory lock 串行化 owner 以及 revoke 会影响的私聊对端；必须在任何行锁前获取。
	if err := lockUsersForUpdate(ctx, tx, lockUserIDs...); err != nil {
		return res, fmt.Errorf("lock delete messages user: %w", err)
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
	res, err = s.finishDeleteMessagesTx(ctx, tx, qtx, req.OwnerUserID, req.OriginAuthKeyID, req.OriginSessionID, req.Date, deleted, false)
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

func (s *MessageStore) finishDeleteMessagesTx(ctx context.Context, db sqlcgen.DBTX, q *sqlcgen.Queries, ownerUserID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, date int, rows []deletedBox, preserveEmptyDialogs bool) (domain.DeleteMessagesResult, error) {
	res := domain.DeleteMessagesResult{OwnerUserID: ownerUserID}
	if len(rows) == 0 {
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
	// 按 owner 升序重建 dialog，使两个反向 delete（X 删与 Y 的会话 / Y 删与 X 的会话）以一致顺序
	// 获取 dialog 行锁，配合下方 watermark 的升序推进，彻底避免 delete-delete 之间的 AB-BA 死锁。
	rebuildOwners := make([]int64, 0, len(peersByOwner))
	for userID := range peersByOwner {
		rebuildOwners = append(rebuildOwners, userID)
	}
	sort.Slice(rebuildOwners, func(i, j int) bool { return rebuildOwners[i] < rebuildOwners[j] })
	for _, userID := range rebuildOwners {
		for peer := range peersByOwner[userID] {
			// just_clear（preserveEmptyDialogs）是请求者的本端语义"清空但保留我这侧
			// 空会话"。revoke 反查出的对端并未选择 just_clear，其空会话应按普通删除
			// 处理（无存活消息则移除 dialog），不能也被保留成空会话。
			preserve := preserveEmptyDialogs && userID == ownerUserID
			if err := rebuildDialogAfterMessageDelete(ctx, q, userID, peer, preserve); err != nil {
				return res, err
			}
		}
	}
	readCorrectionsByOwner, err := loadDeleteUnreadCorrections(ctx, q, incomingDeletedByPeer, date)
	if err != nil {
		return res, err
	}

	ownerIDs := make([]int64, 0, len(idsByOwner))
	for userID := range idsByOwner {
		ownerIDs = append(ownerIDs, userID)
	}
	sort.Slice(ownerIDs, func(i, j int) bool { return ownerIDs[i] < ownerIDs[j] })

	res.Deleted = make([]domain.DeletedMessagesForUser, 0, len(ownerIDs))
	for _, userID := range ownerIDs {
		ids := normalizeMessageIDs(idsByOwner[userID])
		if len(ids) == 0 {
			continue
		}
		corrections := readCorrectionsByOwner[userID]
		totalPtsCount := len(ids) + len(corrections)
		pts, err := s.reservePtsN(ctx, db, userID, totalPtsCount)
		if err != nil {
			return res, fmt.Errorf("allocate delete messages pts: %w", err)
		}
		deletePts := pts - len(corrections)
		event := domain.UpdateEvent{
			UserID:     userID,
			Type:       domain.UpdateEventDeleteMessages,
			Pts:        deletePts,
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
		dispatchAuthKeyID := [8]byte{}
		dispatchSessionID := int64(0)
		if userID == ownerUserID {
			dispatchAuthKeyID = excludeAuthKeyID
			dispatchSessionID = excludeSessionID
		}
		if err := q.EnqueueDispatch(ctx, sqlcgen.EnqueueDispatchParams{
			TargetUserID:     userID,
			Pts:              int32(deletePts),
			EventType:        string(domain.UpdateEventDeleteMessages),
			ExcludeAuthKeyID: authKeyIDToInt64(dispatchAuthKeyID),
			ExcludeSessionID: dispatchSessionID,
		}); err != nil {
			return res, fmt.Errorf("enqueue delete messages dispatch: %w", err)
		}
		res.Deleted = append(res.Deleted, domain.DeletedMessagesForUser{
			UserID:     userID,
			MessageIDs: ids,
			Event:      event,
		})
		for i, correction := range corrections {
			correction.Pts = deletePts + i + 1
			if err := appendUserUpdateEvent(ctx, db, q, userID, correction); err != nil {
				return res, fmt.Errorf("append delete unread correction event: %w", err)
			}
			// dialog 快照必须与 update 流宣布的水位一致，否则后续
			// readHistory 会以旧水位为基线重复发已读事件。
			if err := q.AdvanceDialogReadInboxFloor(ctx, sqlcgen.AdvanceDialogReadInboxFloorParams{
				UserID:         userID,
				PeerType:       string(correction.Peer.Type),
				PeerID:         correction.Peer.ID,
				ReadInboxMaxID: int32(correction.MaxID),
			}); err != nil {
				return res, fmt.Errorf("advance dialog read inbox after delete correction: %w", err)
			}
			if err := q.EnqueueDispatch(ctx, sqlcgen.EnqueueDispatchParams{
				TargetUserID: userID,
				Pts:          int32(correction.Pts),
				EventType:    string(domain.UpdateEventReadHistoryInbox),
			}); err != nil {
				return res, fmt.Errorf("enqueue delete unread correction dispatch: %w", err)
			}
		}
	}
	return res, nil
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

func rebuildDialogAfterMessageDelete(ctx context.Context, q *sqlcgen.Queries, userID int64, peer domain.Peer, preserveEmpty bool) error {
	top, err := q.TopVisibleMessageBoxByPeer(ctx, sqlcgen.TopVisibleMessageBoxByPeerParams{
		OwnerUserID: userID,
		PeerType:    string(peer.Type),
		PeerID:      peer.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if preserveEmpty {
			if err := q.ClearDialogAfterHistoryDelete(ctx, sqlcgen.ClearDialogAfterHistoryDeleteParams{
				UserID:   userID,
				PeerType: string(peer.Type),
				PeerID:   peer.ID,
			}); err != nil {
				return fmt.Errorf("clear empty dialog after history delete: %w", err)
			}
			return nil
		}
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
		UserID:          event.UserID,
		Pts:             int32(event.Pts),
		PtsCount:        int32(event.PtsCount),
		Date:            int32(event.Date),
		EventType:       string(domain.UpdateEventDeleteMessages),
		EventPeers:      []byte("[]"),
		PeerSettings:    []byte("{}"),
		MessageIds:      messageIDs,
		DialogFilter:    []byte("{}"),
		FilterOrder:     []byte("[]"),
		FolderPeers:     []byte("[]"),
		StoryPayload:    []byte("{}"),
		ReactionPayload: []byte("{}"),
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
