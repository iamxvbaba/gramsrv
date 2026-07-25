package postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"hash"
	"sort"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
	"time"
)

func (s *MessageStore) SetMessageReactions(ctx context.Context, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	if req.UserID == 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	if len(req.Reactions) > domain.MaxChannelMessageReactionsPerUser {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	req.Reactions = domain.TrimMessageReactionsToUserMax(req.Reactions, req.ReactionsPerUserMax)
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	for _, reaction := range req.Reactions {
		if !reaction.Valid() {
			return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
		}
	}
	if req.Peer.ID == req.UserID {
		return s.setSavedMessageTags(ctx, req)
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("set message reactions: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("begin set message reactions tx: %w", err)
	}
	qtx := sqlcgen.New(tx)
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := lockUsersForUpdate(ctx, tx, req.UserID, req.Peer.ID); err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("lock set message reactions users: %w", err)
	}

	var target struct {
		boxID            int32
		privateMessageID int64
		messageSenderID  int64
	}
	if err := tx.QueryRow(ctx, `
SELECT box_id, private_message_id, message_sender_id
FROM message_boxes
WHERE owner_user_id = $1
  AND box_id = $2
  AND peer_type = $3
  AND peer_id = $4
  AND NOT deleted
LIMIT 1
FOR UPDATE`, req.UserID, int32(req.MessageID), string(req.Peer.Type), req.Peer.ID).Scan(&target.boxID, &target.privateMessageID, &target.messageSenderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
		}
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("get message for reactions: %w", err)
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM private_message_reactions
WHERE message_sender_id = $1
  AND private_message_id = $2
  AND user_id = $3`, target.messageSenderID, target.privateMessageID, req.UserID); err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("delete old message reactions: %w", err)
	}
	for i, reaction := range req.Reactions {
		if _, err := tx.Exec(ctx, `
INSERT INTO private_message_reactions (
  message_sender_id,
  private_message_id,
  user_id,
  reaction_type,
  reaction_value,
  big,
  reaction_date,
  chosen_order
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (message_sender_id, private_message_id, user_id, reaction_type, reaction_value)
DO UPDATE SET
  big = EXCLUDED.big,
  reaction_date = EXCLUDED.reaction_date,
  chosen_order = EXCLUDED.chosen_order,
  updated_at = now()`,
			target.messageSenderID,
			target.privateMessageID,
			req.UserID,
			string(reaction.Type),
			reaction.Value(),
			req.Big,
			int32(req.Date),
			int32(i+1),
		); err != nil {
			return domain.PrivateMessageReactionsResult{}, fmt.Errorf("insert message reaction: %w", err)
		}
	}
	if target.messageSenderID != 0 && target.messageSenderID != req.UserID {
		if _, err := tx.Exec(ctx, `
UPDATE message_boxes b
SET reaction_unread = EXISTS (
    SELECT 1
    FROM private_message_reactions r
    WHERE r.message_sender_id = b.message_sender_id
      AND r.private_message_id = b.private_message_id
      AND r.user_id <> b.owner_user_id
)
WHERE b.owner_user_id = $1
  AND b.message_sender_id = $2
  AND b.private_message_id = $3`, target.messageSenderID, target.messageSenderID, target.privateMessageID); err != nil {
			return domain.PrivateMessageReactionsResult{}, fmt.Errorf("update private reaction unread: %w", err)
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
  AND d.peer_id = $3`, target.messageSenderID, string(domain.PeerTypeUser), req.UserID); err != nil {
			return domain.PrivateMessageReactionsResult{}, fmt.Errorf("refresh private reaction unread dialog: %w", err)
		}
	}

	boxes, err := qtx.ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
		OwnerUserIds:     privateMessageOwnerIDs(req.UserID, req.Peer.ID),
		MessageSenderID:  target.messageSenderID,
		PrivateMessageID: target.privateMessageID,
	})
	if err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("list visible reaction boxes: %w", err)
	}
	res := domain.PrivateMessageReactionsResult{Messages: make([]domain.Message, 0, len(boxes))}
	for _, box := range boxes {
		msg, err := messageFromVisibleBoxRow(box)
		if err != nil {
			return domain.PrivateMessageReactionsResult{}, err
		}
		res.Messages = append(res.Messages, msg)
	}
	if err := s.enrichPrivateMessageReactions(ctx, tx, req.UserID, res.Messages); err != nil {
		return domain.PrivateMessageReactionsResult{}, err
	}
	for _, msg := range res.Messages {
		if msg.OwnerUserID == req.UserID && msg.Reactions != nil {
			res.Reactions = *msg.Reactions
			break
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("commit set message reactions tx: %w", err)
	}
	committed = true
	return res, nil
}

func (s *MessageStore) GetMessageReactions(ctx context.Context, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	if req.OwnerUserID == 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || len(req.IDs) > domain.MaxGetMessageIDs {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	if len(req.IDs) == 0 {
		return domain.PrivateMessageReactionsResult{}, nil
	}
	boxIDs := make([]int32, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
		}
		boxIDs = append(boxIDs, int32(id))
	}
	rows, err := s.q.GetMessageBoxesByIDs(ctx, sqlcgen.GetMessageBoxesByIDsParams{
		OwnerUserID: req.OwnerUserID,
		BoxIds:      boxIDs,
	})
	if err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("get message reactions boxes: %w", err)
	}
	res := domain.PrivateMessageReactionsResult{Messages: make([]domain.Message, 0, len(rows))}
	for _, row := range rows {
		if row.PeerType != string(req.Peer.Type) || row.PeerID != req.Peer.ID {
			continue
		}
		msg, err := messageFromIDRow(row)
		if err != nil {
			return domain.PrivateMessageReactionsResult{}, err
		}
		res.Messages = append(res.Messages, msg)
	}
	if err := s.enrichPrivateMessageReactions(ctx, s.db, req.OwnerUserID, res.Messages); err != nil {
		return domain.PrivateMessageReactionsResult{}, err
	}
	for _, msg := range res.Messages {
		if msg.Reactions != nil {
			res.Reactions = *msg.Reactions
			break
		}
	}
	return res, nil
}

type privateMessageReactionRow struct {
	messageSenderID  int64
	privateMessageID int64
	userID           int64
	reaction         domain.MessageReaction
	big              bool
	date             int
	chosenOrder      int
}

type privateMessageReactionKey struct {
	messageSenderID  int64
	privateMessageID int64
}

func (s *MessageStore) enrichPrivateMessageReactions(ctx context.Context, db sqlcgen.DBTX, viewerUserID int64, messages []domain.Message) error {
	if len(messages) == 0 {
		return nil
	}
	// poll enrichment 与 reactions 同点位挂载：所有私聊消息读路径都经过本函数，
	// poll media 的权威 closed/聚合/viewer 门控在此一并填充（见 message_polls.go）。
	if err := s.enrichPrivateMessagePolls(ctx, db, viewerUserID, messages); err != nil {
		return err
	}
	if err := s.enrichSavedMessageTags(ctx, db, messages); err != nil {
		return err
	}
	keySet := make(map[privateMessageReactionKey]struct{}, len(messages))
	senderIDs := make([]int64, 0, len(messages))
	privateIDs := make([]int64, 0, len(messages))
	for _, msg := range messages {
		if msg.OwnerUserID != 0 &&
			msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: msg.OwnerUserID}) {
			continue
		}
		if msg.UID == 0 || msg.From.ID == 0 {
			continue
		}
		key := privateMessageReactionKey{messageSenderID: msg.From.ID, privateMessageID: msg.UID}
		if _, ok := keySet[key]; ok {
			continue
		}
		keySet[key] = struct{}{}
		senderIDs = append(senderIDs, key.messageSenderID)
		privateIDs = append(privateIDs, key.privateMessageID)
	}
	if len(senderIDs) == 0 {
		return nil
	}
	rows, err := db.Query(ctx, `
WITH wanted AS (
  SELECT message_sender_id, private_message_id
  FROM unnest($1::bigint[], $2::bigint[]) AS w(message_sender_id, private_message_id)
)
SELECT r.message_sender_id, r.private_message_id, r.user_id, r.reaction_type, r.reaction_value, r.big, r.reaction_date, r.chosen_order
FROM private_message_reactions r
JOIN wanted w
  ON w.message_sender_id = r.message_sender_id
 AND w.private_message_id = r.private_message_id
ORDER BY r.message_sender_id ASC, r.private_message_id ASC, r.reaction_date DESC, r.user_id DESC, r.reaction_type ASC, r.reaction_value ASC`, senderIDs, privateIDs)
	if err != nil {
		return fmt.Errorf("load private message reactions: %w", err)
	}
	defer rows.Close()
	byMessage := make(map[privateMessageReactionKey][]privateMessageReactionRow)
	for rows.Next() {
		var (
			messageSenderID int64
			uid             int64
			userID          int64
			reactionType    string
			value           string
			big             bool
			date            int32
			chosenOrder     int32
		)
		if err := rows.Scan(&messageSenderID, &uid, &userID, &reactionType, &value, &big, &date, &chosenOrder); err != nil {
			return fmt.Errorf("scan private message reactions: %w", err)
		}
		reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(reactionType), value)
		if !ok {
			continue
		}
		key := privateMessageReactionKey{messageSenderID: messageSenderID, privateMessageID: uid}
		byMessage[key] = append(byMessage[key], privateMessageReactionRow{
			messageSenderID:  messageSenderID,
			privateMessageID: uid,
			userID:           userID,
			reaction:         reaction,
			big:              big,
			date:             int(date),
			chosenOrder:      int(chosenOrder),
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("private message reactions rows: %w", err)
	}
	for i := range messages {
		key := privateMessageReactionKey{messageSenderID: messages[i].From.ID, privateMessageID: messages[i].UID}
		// chosen/My 是 per-viewer 字段，必须按该副本的 box owner 视角解析：
		// SetMessageReactions 会同时返回双方 owner 的副本，若统一用请求者视角，
		// 对端收到的 updateMessageReactions 会把发起者的 reaction 标成"自己选的"
		//（TDesktop 非 min 更新直接以 chosen_order 覆盖本地 my 状态）。
		viewpoint := messages[i].OwnerUserID
		if viewpoint == 0 {
			viewpoint = viewerUserID
		}
		reactions := privateMessageReactionsFromRows(byMessage[key], viewpoint)
		applyPrivateMessageReactionUnread(&reactions, messages[i])
		if len(reactions.Results) == 0 && len(reactions.Recent) == 0 {
			continue
		}
		messages[i].Reactions = &reactions
	}
	return nil
}

func applyPrivateMessageReactionUnread(reactions *domain.ChannelMessageReactions, msg domain.Message) {
	if reactions == nil || len(reactions.Recent) == 0 || msg.From.ID == 0 {
		return
	}
	for i := range reactions.Recent {
		reactions.Recent[i].SenderUserID = msg.From.ID
		if msg.ReactionUnread && msg.From.ID == msg.OwnerUserID && reactions.Recent[i].UserID != msg.OwnerUserID {
			reactions.Recent[i].Unread = true
		}
	}
}

func privateMessageReactionsFromRows(rows []privateMessageReactionRow, viewerUserID int64) domain.ChannelMessageReactions {
	out := domain.ChannelMessageReactions{
		CanSeeList: true,
		Results:    []domain.ChannelMessageReactionCount{},
		Recent:     []domain.ChannelMessagePeerReaction{},
	}
	if len(rows) == 0 {
		return out
	}
	type aggregate struct {
		reaction    domain.MessageReaction
		count       int
		chosenOrder int
		latestDate  int
	}
	aggregates := make(map[string]*aggregate)
	recent := make([]domain.ChannelMessagePeerReaction, 0, len(rows))
	for _, row := range rows {
		key := row.reaction.Key()
		item := aggregates[key]
		if item == nil {
			item = &aggregate{reaction: row.reaction}
			aggregates[key] = item
		}
		item.count++
		if row.userID == viewerUserID && row.chosenOrder > 0 && (item.chosenOrder == 0 || row.chosenOrder < item.chosenOrder) {
			item.chosenOrder = row.chosenOrder
		}
		if row.date > item.latestDate {
			item.latestDate = row.date
		}
		recent = append(recent, domain.ChannelMessagePeerReaction{
			UserID:      row.userID,
			Reaction:    row.reaction,
			Big:         row.big,
			My:          row.userID == viewerUserID,
			ChosenOrder: row.chosenOrder,
			Date:        row.date,
		})
	}
	items := make([]aggregate, 0, len(aggregates))
	for _, item := range aggregates {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		if items[i].latestDate != items[j].latestDate {
			return items[i].latestDate > items[j].latestDate
		}
		return items[i].reaction.Key() < items[j].reaction.Key()
	})
	for _, item := range items {
		out.Results = append(out.Results, domain.ChannelMessageReactionCount{
			Reaction:    item.reaction,
			Count:       item.count,
			ChosenOrder: item.chosenOrder,
		})
	}
	sort.Slice(recent, func(i, j int) bool {
		if recent[i].Date != recent[j].Date {
			return recent[i].Date > recent[j].Date
		}
		if recent[i].UserID != recent[j].UserID {
			return recent[i].UserID > recent[j].UserID
		}
		return recent[i].Reaction.Key() < recent[j].Reaction.Key()
	})
	if len(recent) > domain.MaxChannelMessageReactionRecent {
		recent = recent[:domain.MaxChannelMessageReactionRecent]
	}
	out.Recent = recent
	return out
}

func writeMessageReactionsHash(h hash.Hash64, reactions *domain.ChannelMessageReactions) {
	if reactions == nil {
		_, _ = h.Write([]byte{0})
		return
	}
	var buf [16]byte
	if reactions.AsTags {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
	for _, item := range reactions.Results {
		_, _ = h.Write([]byte(item.Reaction.Type))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(item.Reaction.Value()))
		_, _ = h.Write([]byte{0})
		binary.LittleEndian.PutUint32(buf[:4], uint32(item.Count))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(item.ChosenOrder))
		_, _ = h.Write(buf[:8])
	}
	_, _ = h.Write([]byte{0xfe})
	for _, item := range reactions.Recent {
		_, _ = h.Write([]byte(item.Reaction.Type))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(item.Reaction.Value()))
		_, _ = h.Write([]byte{0})
		binary.LittleEndian.PutUint64(buf[:8], uint64(item.UserID))
		binary.LittleEndian.PutUint32(buf[8:12], uint32(item.Date))
		binary.LittleEndian.PutUint32(buf[12:16], uint32(item.ChosenOrder))
		_, _ = h.Write(buf[:])
	}
}
