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
	// 先做只读预查推导本次涉及的对端 sender，保证 advisory lock 仍按
	// user_id 升序统一获取，避免与 send/read 路径形成交叉锁序。
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
		// reaction 与 media(语音/圆形视频)未读清除都要 UPDATE dialogs 行:
		// dialog_light 触发器仅挂在 dialogs 表，message_boxes.media_unread 翻转不 bump 版本。
		// 不补这一下,「语音先历史已读、之后单独点听」会让 getPeerDialogs 的 per-peer 缓存
		// 顶层消息一直带 media_unread=true(蓝点不消),直到该会话因别的写被失效。
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
				// dialog 行已随本次删除被移除（peer 下已无存活消息），没有读边界
				// 可推进，不应再对一个正在丢弃的会话发悬空的 read 校正。
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

// ListUnreadReactionMessages 返回当前 owner 在该 peer 下 reaction_unread 的
// 消息（含最新 reactions 聚合），供 messages.getUnreadReactions 跳转。
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
		out = append(out, messageFromBoxRow(sqlcgen.CreateMessageBoxRow(row)))
	}
	if err := s.enrichPrivateMessageReactions(ctx, s.db, ownerUserID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadPeerReactions 清理当前 owner 在该 peer 下的全部未读 reaction 状态，
// 对应 messages.readReactions 的私聊分支。
//
// 必须在单事务内、且持与 SetMessageReactions 同键空间(owner, peer)的 advisory 锁:
// 否则「清 message_boxes.reaction_unread」与「重置 dialogs 计数」是两条独立 autocommit
// 语句，既有原子性缺口(stmt1 成功 stmt2 失败 → 底层已清但计数与缓存仍陈旧)，又会与并发
// 入站 SetMessageReactions 交错产生 lost-update(本侧硬归零 clobber 掉对端刚自增的正确计数，
// 角标永久偏低)。计数按存活 reaction_unread 重算而非硬置 0:既保留历史坏数据的 stale-counter
// 自愈(无存活行时 COUNT 自然为 0)，又在并发交错时只得出与当前 box 状态一致的值。
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
	// 计数始终重算(且无条件 UPDATE dialogs 行)以 bump dialog_light 失效缓存；
	// COUNT 在同事务内读到清除后的 box 状态，无存活行即为 0，保留 stale-counter 自愈。
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
