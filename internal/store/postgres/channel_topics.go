package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) SetForum(ctx context.Context, userID, channelID int64, enabled, tabs bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("toggle channel forum: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin toggle channel forum: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !channel.Megagroup || channel.Broadcast {
		return domain.Channel{}, domain.ErrChannelNotModified
	}
	if member.Role != domain.ChannelRoleCreator {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if enabled && channel.LinkedChatID != 0 {
		return domain.Channel{}, domain.ErrChatDiscussionUnallowed
	}
	prevForum := channel.Forum
	prevTabs := channel.ForumTabs
	nextTabs := enabled && tabs
	if _, err := tx.Exec(ctx, `
UPDATE channels
SET forum = $2,
    forum_tabs = $3,
    updated_at = now()
WHERE id = $1`, channelID, enabled, nextTabs); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel forum: %w", err)
	}
	channel.Forum = enabled
	channel.ForumTabs = nextTabs
	if err := markUserChannelMemberIndexForumTx(ctx, tx, channelID, enabled); err != nil {
		return domain.Channel{}, err
	}
	if prevForum != channel.Forum || prevTabs != channel.ForumTabs {
		if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      nowUnix(),
			Type:      domain.ChannelAdminLogToggleForum,
			PrevBool:  prevForum,
			NewBool:   enabled,
		}); err != nil {
			return domain.Channel{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit toggle channel forum: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) SetChannelViewForumAsMessages(ctx context.Context, userID, channelID int64, enabled bool) (bool, error) {
	if userID == 0 || channelID == 0 {
		return false, nil
	}
	var changed bool
	if err := s.db.QueryRow(ctx, `
WITH target AS (
    SELECT c.id AS channel_id, c.top_message_id, c.date AS top_message_date
    FROM channels c
    JOIN channel_members m ON m.channel_id = c.id
    WHERE c.id = $2 AND m.user_id = $1 AND m.status = 'active' AND NOT c.deleted
),
ensured AS (
    INSERT INTO channel_dialogs (user_id, channel_id, top_message_id, top_message_date)
    SELECT $1, channel_id, top_message_id, top_message_date FROM target
    ON CONFLICT (user_id, channel_id) DO NOTHING
),
updated_dialog AS (
    UPDATE channel_dialogs d
    SET view_forum_as_messages = $3, updated_at = now()
    WHERE d.user_id = $1 AND d.channel_id = $2
      AND EXISTS (SELECT 1 FROM target)
      AND d.view_forum_as_messages IS DISTINCT FROM $3::boolean
    RETURNING d.user_id
)
SELECT EXISTS (SELECT 1 FROM updated_dialog)::boolean`, userID, channelID, enabled).Scan(&changed); err != nil {
		return false, fmt.Errorf("set channel view forum as messages: %w", err)
	}
	// 连接池写后同步失效本进程 dialog 缓存，保证同实例 read-your-write；
	// 跨实例由 dialog_light NOTIFY 异步兜底。
	if changed && s.dialogCacheActive(s.db) {
		s.dialogCache.delete(userID, channelID)
	}
	return changed, nil
}

func (s *ChannelStore) CreateForumTopic(ctx context.Context, req domain.CreateChannelForumTopicRequest) (domain.CreateChannelForumTopicResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.RandomID == 0 {
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelInvalid
	}
	title := strings.TrimSpace(req.Title)
	if title == "" && !req.TitleMissing {
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelInvalid
	}
	channel, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.CreateChannelForumTopicResult{}, err
	}
	if !channel.Forum || channel.Broadcast || !channel.Megagroup {
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelForumMissing
	}
	if !canSendChannelMessage(channel, member) {
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelWriteForbidden
	}
	selfBoostsApplied := 0
	if channel.Megagroup {
		now := req.Date
		if now <= 0 {
			now = nowUnix()
		}
		selfBoostsApplied, err = s.countActiveUserBoostsForPeer(ctx, s.db, req.UserID, domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}, now)
		if err != nil {
			return domain.CreateChannelForumTopicResult{}, err
		}
	}
	if domain.ChannelBannedRightsBlockManageTopics(channel, member, selfBoostsApplied) {
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelWriteForbidden
	}
	if req.IconColor == 0 {
		req.IconColor = domain.DefaultForumTopicIconColor
	}
	res, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    req.UserID,
		ChannelID: req.ChannelID,
		RandomID:  req.RandomID,
		SendAs:    req.SendAs,
		Action: &domain.ChannelMessageAction{
			Type:         domain.ChannelActionTopicCreate,
			Title:        title,
			IconColor:    req.IconColor,
			IconEmojiID:  req.IconEmojiID,
			TitleMissing: req.TitleMissing,
		},
		Date: req.Date,
	})
	if err != nil {
		return domain.CreateChannelForumTopicResult{}, err
	}
	if res.Message.Action == nil || res.Message.Action.Type != domain.ChannelActionTopicCreate {
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelInvalid
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO channel_forum_topics (
    channel_id, topic_id, creator_user_id, title, icon_color, icon_emoji_id,
    title_missing, date, top_message_id, read_inbox_max_id, read_outbox_max_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $2, $2, $2)
ON CONFLICT (channel_id, topic_id) DO NOTHING`,
		req.ChannelID, res.Message.ID, req.UserID, title, req.IconColor, req.IconEmojiID, req.TitleMissing, res.Message.Date); err != nil {
		return domain.CreateChannelForumTopicResult{}, fmt.Errorf("insert forum topic: %w", err)
	}
	topic, err := s.getForumTopic(ctx, s.db, req.ChannelID, res.Message.ID)
	if err != nil {
		return domain.CreateChannelForumTopicResult{}, err
	}
	return domain.CreateChannelForumTopicResult{
		Channel:    res.Channel,
		Topic:      topic,
		Message:    res.Message,
		Event:      res.Event,
		Recipients: res.Recipients,
		Duplicate:  res.Duplicate,
	}, nil
}

func (s *ChannelStore) EditForumTopic(ctx context.Context, req domain.EditChannelForumTopicRequest) (domain.EditChannelForumTopicResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.TopicID <= 0 {
		return domain.EditChannelForumTopicResult{}, domain.ErrChannelInvalid
	}
	channel, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelForumTopicResult{}, err
	}
	if !channel.Forum {
		return domain.EditChannelForumTopicResult{}, domain.ErrChannelForumMissing
	}
	topic, err := s.getForumTopic(ctx, s.db, req.ChannelID, req.TopicID)
	if err != nil {
		return domain.EditChannelForumTopicResult{}, err
	}
	selfBoostsApplied := 0
	if channel.Megagroup {
		now := req.Date
		if now <= 0 {
			now = nowUnix()
		}
		selfBoostsApplied, err = s.countActiveUserBoostsForPeer(ctx, s.db, req.UserID, domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}, now)
		if err != nil {
			return domain.EditChannelForumTopicResult{}, err
		}
	}
	if !canManageForumTopic(channel, member, topic, req.UserID, selfBoostsApplied) {
		return domain.EditChannelForumTopicResult{}, domain.ErrChannelAdminRequired
	}
	next := topic
	action := domain.ChannelMessageAction{Type: domain.ChannelActionTopicEdit}
	changed := false
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			return domain.EditChannelForumTopicResult{}, domain.ErrChannelInvalid
		}
		if next.Title != title {
			next.Title = title
			action.Title = title
			changed = true
		}
	}
	if req.IconEmojiID != nil && next.IconEmojiID != *req.IconEmojiID {
		next.IconEmojiID = *req.IconEmojiID
		action.IconEmojiID = *req.IconEmojiID
		action.IconEmojiIDSet = true
		changed = true
	}
	if req.Closed != nil && next.Closed != *req.Closed {
		next.Closed = *req.Closed
		action.Closed = boolPtr(*req.Closed)
		changed = true
	}
	if req.Hidden != nil && next.Hidden != *req.Hidden {
		next.Hidden = *req.Hidden
		action.Hidden = boolPtr(*req.Hidden)
		changed = true
	}
	if !changed {
		return domain.EditChannelForumTopicResult{}, domain.ErrChannelNotModified
	}
	res, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    req.UserID,
		ChannelID: req.ChannelID,
		ReplyTo: &domain.MessageReply{
			Peer:         domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID},
			MessageID:    req.TopicID,
			TopMessageID: req.TopicID,
		},
		Action: &action,
		Date:   req.Date,
	})
	if err != nil {
		return domain.EditChannelForumTopicResult{}, err
	}
	if _, err := s.db.Exec(ctx, `
UPDATE channel_forum_topics
SET title = $3,
    icon_emoji_id = $4,
    closed = $5,
    hidden = $6,
    top_message_id = GREATEST(top_message_id, $7),
    updated_at = now()
WHERE channel_id = $1 AND topic_id = $2 AND NOT deleted`,
		req.ChannelID, req.TopicID, next.Title, next.IconEmojiID, next.Closed, next.Hidden, res.Message.ID); err != nil {
		return domain.EditChannelForumTopicResult{}, fmt.Errorf("update forum topic: %w", err)
	}
	topic, err = s.getForumTopic(ctx, s.db, req.ChannelID, req.TopicID)
	if err != nil {
		return domain.EditChannelForumTopicResult{}, err
	}
	return domain.EditChannelForumTopicResult{
		Channel:    res.Channel,
		Topic:      topic,
		Message:    res.Message,
		Event:      res.Event,
		Recipients: res.Recipients,
	}, nil
}

func (s *ChannelStore) UpdatePinnedForumTopic(ctx context.Context, req domain.UpdateChannelForumTopicPinnedRequest) (domain.UpdateChannelForumTopicPinnedResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.TopicID <= 0 {
		return domain.UpdateChannelForumTopicPinnedResult{}, domain.ErrChannelInvalid
	}
	channel, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.UpdateChannelForumTopicPinnedResult{}, err
	}
	if !channel.Forum {
		return domain.UpdateChannelForumTopicPinnedResult{}, domain.ErrChannelForumMissing
	}
	topic, err := s.getForumTopic(ctx, s.db, req.ChannelID, req.TopicID)
	if err != nil {
		return domain.UpdateChannelForumTopicPinnedResult{}, err
	}
	if !canPinChannelMessages(channel, member) {
		return domain.UpdateChannelForumTopicPinnedResult{}, domain.ErrChannelAdminRequired
	}
	if topic.Pinned == req.Pinned {
		return domain.UpdateChannelForumTopicPinnedResult{}, domain.ErrChannelNotModified
	}
	pinnedOrder := 0
	if req.Pinned {
		pinnedOrder = topic.PinnedOrder
		if pinnedOrder == 0 {
			pinnedOrder, err = s.nextForumTopicPinnedOrder(ctx, req.ChannelID)
			if err != nil {
				return domain.UpdateChannelForumTopicPinnedResult{}, err
			}
		}
	}
	if _, err := s.db.Exec(ctx, `
UPDATE channel_forum_topics
SET pinned = $3, pinned_order = $4, updated_at = now()
WHERE channel_id = $1 AND topic_id = $2 AND NOT deleted`,
		req.ChannelID, req.TopicID, req.Pinned, pinnedOrder); err != nil {
		return domain.UpdateChannelForumTopicPinnedResult{}, fmt.Errorf("update pinned forum topic: %w", err)
	}
	topic, err = s.getForumTopic(ctx, s.db, req.ChannelID, req.TopicID)
	if err != nil {
		return domain.UpdateChannelForumTopicPinnedResult{}, err
	}
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return domain.UpdateChannelForumTopicPinnedResult{Channel: channel, Topic: topic, Recipients: recipients}, nil
}

func (s *ChannelStore) ReorderPinnedForumTopics(ctx context.Context, req domain.ReorderChannelPinnedForumTopicsRequest) (domain.ReorderChannelPinnedForumTopicsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || len(req.Order) > domain.MaxChannelForumTopicIDs {
		return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrChannelInvalid
	}
	channel, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReorderChannelPinnedForumTopicsResult{}, err
	}
	if !channel.Forum {
		return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrChannelForumMissing
	}
	if !canPinChannelMessages(channel, member) {
		return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrChannelAdminRequired
	}
	seen := make(map[int]struct{}, len(req.Order))
	order := make([]int, 0, len(req.Order))
	for _, id := range req.Order {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrMessageIDInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		topic, err := s.getForumTopic(ctx, s.db, req.ChannelID, id)
		if err != nil || !topic.Pinned {
			if req.Force {
				continue
			}
			return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrMessageIDInvalid
		}
		seen[id] = struct{}{}
		order = append(order, id)
	}
	for i, id := range order {
		if _, err := s.db.Exec(ctx, `
UPDATE channel_forum_topics
SET pinned_order = $3, updated_at = now()
WHERE channel_id = $1 AND topic_id = $2 AND pinned AND NOT deleted`, req.ChannelID, id, len(order)-i); err != nil {
			return domain.ReorderChannelPinnedForumTopicsResult{}, fmt.Errorf("reorder pinned forum topics: %w", err)
		}
	}
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return domain.ReorderChannelPinnedForumTopicsResult{Channel: channel, Order: order, Recipients: recipients}, nil
}

func (s *ChannelStore) DeleteForumTopicHistory(ctx context.Context, req domain.DeleteChannelForumTopicHistoryRequest) (domain.DeleteChannelHistoryResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.TopicID <= 0 {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("delete forum topic history: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("begin delete forum topic history: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, err
	}
	if !channel.Forum {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelForumMissing
	}
	topic, err := s.getForumTopic(ctx, tx, req.ChannelID, req.TopicID)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, err
	}
	if !canManageForumTopic(channel, member, topic, req.UserID, 0) && !canDeleteAnyChannelMessage(member) {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelAdminRequired
	}
	rows, err := tx.Query(ctx, `
SELECT id
FROM channel_messages
WHERE channel_id = $1 AND NOT deleted AND (id = $2 OR reply_to_top_id = $2)
ORDER BY id DESC
LIMIT $3`, req.ChannelID, req.TopicID, domain.MaxDeleteHistoryBatch)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("list forum topic delete ids: %w", err)
	}
	ids := make([]int, 0, domain.MaxDeleteHistoryBatch)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return domain.DeleteChannelHistoryResult{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.DeleteChannelHistoryResult{}, err
	}
	rows.Close()
	deleted, event, channel, err := s.deleteChannelMessagesTx(ctx, tx, channel, member, ids, req.UserID, req.Date)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, err
	}
	remaining := 0
	if err := tx.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM channel_messages
WHERE channel_id = $1 AND NOT deleted AND (id = $2 OR reply_to_top_id = $2)`, req.ChannelID, req.TopicID).Scan(&remaining); err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("count remaining forum topic messages: %w", err)
	}
	offset := 0
	if remaining > 0 {
		offset = 1
	} else if _, err := tx.Exec(ctx, `
UPDATE channel_forum_topics
SET deleted = true, updated_at = now()
WHERE channel_id = $1 AND topic_id = $2`, req.ChannelID, req.TopicID); err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("mark forum topic deleted: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("commit delete forum topic history: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return domain.DeleteChannelHistoryResult{Channel: channel, Event: event, DeletedIDs: deleted, Recipients: recipients, Offset: offset}, nil
}

func (s *ChannelStore) ListForumTopics(ctx context.Context, viewerUserID int64, filter domain.ChannelForumTopicFilter) (domain.ChannelForumTopicList, error) {
	channel, member, err := s.getChannelForMember(ctx, s.db, viewerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	if !channel.Forum {
		return domain.ChannelForumTopicList{}, domain.ErrChannelForumMissing
	}
	limit := filter.Limit
	if limit <= 0 || limit > domain.MaxChannelForumTopicsLimit {
		limit = domain.MaxChannelForumTopicsLimit
	}
	query := strings.TrimSpace(strings.ToLower(filter.Query))
	countArgs := []any{filter.ChannelID, member.AvailableMinID, query}
	countSQL := `
SELECT COUNT(*)::int
FROM channel_forum_topics
WHERE channel_id = $1 AND NOT deleted AND topic_id > $2
  AND ($3 = '' OR POSITION($3 IN LOWER(title)) > 0)`
	var total int
	if err := s.db.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return domain.ChannelForumTopicList{}, fmt.Errorf("count forum topics: %w", err)
	}
	args := []any{filter.ChannelID, member.AvailableMinID, query}
	where := `channel_id = $1 AND NOT deleted AND topic_id > $2 AND ($3 = '' OR POSITION($3 IN LOWER(title)) > 0)`
	offsetID := filter.OffsetTopic
	if offsetID == 0 {
		offsetID = filter.OffsetID
	}
	if filter.OffsetDate != 0 {
		args = append(args, filter.OffsetDate, offsetID)
		where += fmt.Sprintf(" AND (date, topic_id) < ($%d, $%d)", len(args)-1, len(args))
	} else if offsetID != 0 {
		args = append(args, offsetID)
		where += fmt.Sprintf(" AND topic_id < $%d", len(args))
	}
	args = append(args, limit)
	rows, err := s.db.Query(ctx, `
SELECT `+channelForumTopicColumns+`
FROM channel_forum_topics
WHERE `+where+`
ORDER BY pinned DESC, pinned_order DESC, date DESC, topic_id DESC
LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return domain.ChannelForumTopicList{}, fmt.Errorf("list forum topics: %w", err)
	}
	defer rows.Close()
	topics := make([]domain.ChannelForumTopic, 0, limit)
	for rows.Next() {
		topic, err := scanChannelForumTopic(rows)
		if err != nil {
			return domain.ChannelForumTopicList{}, err
		}
		topics = append(topics, topic)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	if err := s.populateForumTopicViewerCounters(ctx, viewerUserID, filter.ChannelID, topics, member.AvailableMinID); err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	messages, err := s.forumTopicRootMessages(ctx, filter.ChannelID, topics, member.AvailableMinID)
	if err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	dialog, _ := s.getChannelDialog(ctx, s.db, viewerUserID, channel)
	return domain.ChannelForumTopicList{Channel: channel, Dialog: dialog, Topics: topics, Messages: messages, Count: total}, nil
}

func (s *ChannelStore) GetForumTopicsByID(ctx context.Context, viewerUserID, channelID int64, ids []int) (domain.ChannelForumTopicList, error) {
	channel, member, err := s.getChannelForMember(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	if !channel.Forum {
		return domain.ChannelForumTopicList{}, domain.ErrChannelForumMissing
	}
	if len(ids) == 0 {
		dialog, _ := s.getChannelDialog(ctx, s.db, viewerUserID, channel)
		return domain.ChannelForumTopicList{Channel: channel, Dialog: dialog}, nil
	}
	id32, _, err := validUniqueChannelMessageIDs(ids)
	if err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	rows, err := s.db.Query(ctx, `
SELECT `+channelForumTopicColumns+`
FROM channel_forum_topics
WHERE channel_id = $1 AND NOT deleted AND topic_id > $2 AND topic_id = ANY($3::int[])
ORDER BY pinned DESC, pinned_order DESC, date DESC, topic_id DESC`, channelID, member.AvailableMinID, id32)
	if err != nil {
		return domain.ChannelForumTopicList{}, fmt.Errorf("get forum topics by id: %w", err)
	}
	defer rows.Close()
	topics := make([]domain.ChannelForumTopic, 0, len(id32))
	for rows.Next() {
		topic, err := scanChannelForumTopic(rows)
		if err != nil {
			return domain.ChannelForumTopicList{}, err
		}
		topics = append(topics, topic)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	if err := s.populateForumTopicViewerCounters(ctx, viewerUserID, channelID, topics, member.AvailableMinID); err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	messages, err := s.forumTopicRootMessages(ctx, channelID, topics, member.AvailableMinID)
	if err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	dialog, _ := s.getChannelDialog(ctx, s.db, viewerUserID, channel)
	return domain.ChannelForumTopicList{Channel: channel, Dialog: dialog, Topics: topics, Messages: messages, Count: len(topics)}, nil
}

func (s *ChannelStore) ListChannelReplies(ctx context.Context, viewerUserID int64, filter domain.ChannelRepliesFilter) (domain.ChannelHistory, error) {
	source, member, err := s.getChannelForMemberOrLinkedGuest(ctx, s.db, viewerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	root, err := s.getChannelMessage(ctx, s.db, filter.ChannelID, filter.RootMessageID)
	if err != nil || root.Deleted || root.ID <= member.AvailableMinID {
		return domain.ChannelHistory{}, domain.ErrMessageIDInvalid
	}
	target := source
	availableMinID := member.AvailableMinID
	extraChannels := []domain.Channel(nil)
	rootID := root.ID
	if source.Broadcast {
		if root.Discussion == nil || root.Discussion.ChannelID == 0 || root.Discussion.MessageID == 0 {
			return domain.ChannelHistory{Channel: source}, nil
		}
		linked, err := getChannelByID(ctx, s.db, root.Discussion.ChannelID)
		if err != nil {
			return domain.ChannelHistory{Channel: source}, nil
		}
		target = linked
		rootID = root.Discussion.MessageID
		availableMinID = 0
		if linkedMember, err := s.getChannelMember(ctx, s.db, linked.ID, viewerUserID); err == nil && validateChannelMemberVisible(linkedMember) == nil {
			availableMinID = linkedMember.AvailableMinID
		}
		extraChannels = append(extraChannels, source)
	}
	targetRoot, err := s.getChannelMessage(ctx, s.db, target.ID, rootID)
	if err != nil || targetRoot.Deleted || targetRoot.ID <= availableMinID {
		return domain.ChannelHistory{Channel: target, Channels: extraChannels}, nil
	}
	limit := filter.Limit
	if limit <= 0 || limit > domain.MaxChannelRepliesLimit {
		limit = domain.MaxChannelRepliesLimit
	}
	filter.AddOffset = domain.ClampMessageHistoryAddOffset(filter.AddOffset)
	count, err := s.countChannelReplies(ctx, target.ID, rootID, availableMinID, filter)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	messages, err := s.queryChannelRepliesPage(ctx, target.ID, rootID, availableMinID, filter, limit)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	if err := s.populateChannelMessageReplies(ctx, s.db, viewerUserID, target, messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	if err := s.populateChannelMessagesReactions(ctx, s.db, viewerUserID, []domain.Channel{target}, messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	topics := []domain.ChannelForumTopic(nil)
	if target.Forum {
		if topic, err := s.getForumTopic(ctx, s.db, target.ID, rootID); err == nil && !topic.Hidden {
			topic = s.topicWithViewerCounters(ctx, viewerUserID, target.ID, topic, availableMinID, availableMinID)
			topics = append(topics, topic)
		} else if err != nil && !errors.Is(err, domain.ErrMessageIDInvalid) {
			return domain.ChannelHistory{}, err
		}
	}
	return domain.ChannelHistory{Channel: target, Channels: extraChannels, Topics: topics, Messages: messages, Count: count}, nil
}

func (s *ChannelStore) getForumTopic(ctx context.Context, db sqlcgen.DBTX, channelID int64, topicID int) (domain.ChannelForumTopic, error) {
	if channelID == 0 || topicID == 0 {
		return domain.ChannelForumTopic{}, domain.ErrMessageIDInvalid
	}
	topic, err := scanChannelForumTopic(db.QueryRow(ctx, `
SELECT `+channelForumTopicColumns+`
FROM channel_forum_topics
WHERE channel_id = $1 AND topic_id = $2 AND NOT deleted`, channelID, topicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelForumTopic{}, domain.ErrMessageIDInvalid
	}
	return topic, err
}

func (s *ChannelStore) forumTopicRootMessages(ctx context.Context, channelID int64, topics []domain.ChannelForumTopic, availableMinID int) ([]domain.ChannelMessage, error) {
	if len(topics) == 0 {
		return nil, nil
	}
	ids := make([]int, 0, len(topics))
	seen := make(map[int]struct{}, len(topics))
	for _, topic := range topics {
		if topic.TopMessageID <= 0 {
			continue
		}
		if _, ok := seen[topic.TopMessageID]; ok {
			continue
		}
		seen[topic.TopMessageID] = struct{}{}
		ids = append(ids, topic.TopMessageID)
	}
	id32, _, err := validUniqueChannelMessageIDs(ids)
	if err != nil {
		return nil, err
	}
	if len(id32) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE channel_id = $1 AND id = ANY($2::int[]) AND id > $3 AND NOT deleted
ORDER BY id DESC`, channelID, id32, availableMinID)
	if err != nil {
		return nil, fmt.Errorf("list forum topic root messages: %w", err)
	}
	defer rows.Close()
	messages := make([]domain.ChannelMessage, 0, len(id32))
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *ChannelStore) nextForumTopicPinnedOrder(ctx context.Context, channelID int64) (int, error) {
	var maxOrder int
	if err := s.db.QueryRow(ctx, `
SELECT COALESCE(MAX(pinned_order), 0)::int
FROM channel_forum_topics
WHERE channel_id = $1 AND pinned AND NOT deleted`, channelID).Scan(&maxOrder); err != nil {
		return 0, fmt.Errorf("next forum topic pinned order: %w", err)
	}
	return maxOrder + 1, nil
}

func (s *ChannelStore) populateChannelMessageReplies(ctx context.Context, db sqlcgen.DBTX, viewerUserID int64, channel domain.Channel, messages []domain.ChannelMessage) error {
	if len(messages) == 0 || channel.ID == 0 {
		return nil
	}
	indexes := make(map[channelReplyStatKey][]int)
	rootsByChannel := make(map[int64][]int32)
	readMaxByChannel := make(map[int64]int)
	for i := range messages {
		targetChannelID := channel.ID
		rootID := messages[i].ID
		replies := &domain.ChannelMessageReplies{}
		if messages[i].Discussion != nil && messages[i].Discussion.ChannelID != 0 && messages[i].Discussion.MessageID != 0 {
			targetChannelID = messages[i].Discussion.ChannelID
			rootID = messages[i].Discussion.MessageID
			replies.Comments = true
			replies.ChannelID = messages[i].Discussion.ChannelID
		} else if channel.Broadcast && channel.LinkedChatID != 0 && messages[i].Post {
			replies.Comments = true
			replies.ChannelID = channel.LinkedChatID
		}
		if _, ok := readMaxByChannel[targetChannelID]; !ok {
			readInbox, _ := s.channelReadWatermarks(ctx, targetChannelID, viewerUserID)
			readMaxByChannel[targetChannelID] = readInbox
		}
		replies.ReadMaxID = readMaxByChannel[targetChannelID]
		key := channelReplyStatKey{channelID: targetChannelID, rootID: rootID}
		if _, ok := indexes[key]; !ok {
			rootsByChannel[targetChannelID] = append(rootsByChannel[targetChannelID], int32(rootID))
		}
		indexes[key] = append(indexes[key], i)
		if replies.Comments {
			messages[i].Replies = replies
		}
	}
	for channelID, roots := range rootsByChannel {
		rows, err := db.Query(ctx, `
SELECT reply_to_top_id, COUNT(*)::int, COALESCE(MAX(id), 0)::int, COALESCE((array_agg(pts ORDER BY id DESC))[1], 0)::int
FROM channel_messages
WHERE channel_id = $1 AND reply_to_top_id = ANY($2::int[]) AND NOT deleted
GROUP BY reply_to_top_id`, channelID, roots)
		if err != nil {
			return fmt.Errorf("load channel reply stats: %w", err)
		}
		// 只为「确有回复」的 root 再查最近回复者:普通频道/群历史页里绝大多数消息没有 thread
		// 回复,rootsWithReplies 为空则跳过第二条查询,热读路径(getHistory/getDialogs)零额外往返。
		var rootsWithReplies []int32
		for rows.Next() {
			var rootID, count, maxID, repliesPts int
			if err := rows.Scan(&rootID, &count, &maxID, &repliesPts); err != nil {
				rows.Close()
				return err
			}
			if count > 0 {
				rootsWithReplies = append(rootsWithReplies, int32(rootID))
			}
			for _, idx := range indexes[channelReplyStatKey{channelID: channelID, rootID: rootID}] {
				replies := messages[idx].Replies
				if replies == nil {
					replies = &domain.ChannelMessageReplies{ReadMaxID: readMaxByChannel[channelID]}
				}
				replies.Replies = count
				replies.MaxID = maxID
				replies.RepliesPts = repliesPts
				messages[idx].Replies = replies
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		// 与 memory store 的 channelMessageRepliesLocked 对齐:补齐每个 root 的最近回复者。
		// 此前只算了 count/max/pts,RecentRepliers 恒为 nil,导致频道帖评论入口不显示最近回复者头像。
		repliers, err := s.channelRecentRepliers(ctx, db, channelID, rootsWithReplies)
		if err != nil {
			return err
		}
		for rootID, peers := range repliers {
			for _, idx := range indexes[channelReplyStatKey{channelID: channelID, rootID: rootID}] {
				replies := messages[idx].Replies
				if replies == nil {
					replies = &domain.ChannelMessageReplies{ReadMaxID: readMaxByChannel[channelID]}
				}
				replies.RecentRepliers = append([]domain.Peer(nil), peers...)
				messages[idx].Replies = replies
			}
		}
	}
	return nil
}

// channelRecentRepliers 返回每个 root 最近 3 个去重回复者(newest-first),与 memory store 的
// channelMessageRepliesLocked 一致:发送者优先取 from_peer(send-as/匿名管理员),为空时回退
// sender_user_id;按各回复者最新一条回复的 id 倒序取前 3 个。
func (s *ChannelStore) channelRecentRepliers(ctx context.Context, db sqlcgen.DBTX, channelID int64, roots []int32) (map[int][]domain.Peer, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, `
SELECT reply_to_top_id, peer_type, peer_id
FROM (
    SELECT reply_to_top_id, peer_type, peer_id,
           ROW_NUMBER() OVER (PARTITION BY reply_to_top_id ORDER BY last_id DESC) AS rn
    FROM (
        SELECT reply_to_top_id,
               CASE WHEN from_peer_id <> 0 THEN from_peer_type ELSE 'user' END AS peer_type,
               CASE WHEN from_peer_id <> 0 THEN from_peer_id ELSE sender_user_id END AS peer_id,
               MAX(id) AS last_id
        FROM channel_messages
        WHERE channel_id = $1 AND reply_to_top_id = ANY($2::int[]) AND NOT deleted
        GROUP BY reply_to_top_id, peer_type, peer_id
    ) grouped
    WHERE peer_id <> 0
) ranked
WHERE rn <= 3
ORDER BY reply_to_top_id, rn`, channelID, roots)
	if err != nil {
		return nil, fmt.Errorf("load channel recent repliers: %w", err)
	}
	defer rows.Close()
	out := make(map[int][]domain.Peer)
	for rows.Next() {
		var rootID int
		var peerType string
		var peerID int64
		if err := rows.Scan(&rootID, &peerType, &peerID); err != nil {
			return nil, err
		}
		out[rootID] = append(out[rootID], domain.Peer{Type: domain.PeerType(peerType), ID: peerID})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ChannelStore) countChannelReplies(ctx context.Context, channelID int64, rootID, availableMinID int, filter domain.ChannelRepliesFilter) (int, error) {
	where, args := channelRepliesBaseWhere(channelID, rootID, availableMinID, filter)
	var count int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM channel_messages WHERE `+where, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count channel replies: %w", err)
	}
	return count, nil
}

func (s *ChannelStore) queryChannelRepliesPage(ctx context.Context, channelID int64, rootID, availableMinID int, filter domain.ChannelRepliesFilter, limit int) ([]domain.ChannelMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	switch messageHistoryLoadType(filter.AddOffset, limit) {
	case messageHistoryLoadForward:
		return s.queryChannelRepliesForward(ctx, channelID, rootID, availableMinID, filter, limit)
	case messageHistoryLoadAround:
		forwardLimit := -filter.AddOffset
		if forwardLimit > limit {
			forwardLimit = limit
		}
		backwardLimit := limit + filter.AddOffset
		if backwardLimit < 0 {
			backwardLimit = 0
		}
		forward, err := s.queryChannelRepliesForward(ctx, channelID, rootID, availableMinID, filter, forwardLimit)
		if err != nil {
			return nil, err
		}
		backward, err := s.queryChannelRepliesBackward(ctx, channelID, rootID, availableMinID, filter, backwardLimit, true)
		if err != nil {
			return nil, err
		}
		out := append(forward, backward...)
		sort.SliceStable(out, func(i, j int) bool { return channelMessageLess(out[i], out[j]) })
		return out, nil
	default:
		start := filter.AddOffset
		if start < 0 {
			start = 0
		}
		items, err := s.queryChannelRepliesBackward(ctx, channelID, rootID, availableMinID, filter, limit+start, false)
		if err != nil || start >= len(items) {
			return nil, err
		}
		return items[start:], nil
	}
}

func (s *ChannelStore) queryChannelRepliesBackward(ctx context.Context, channelID int64, rootID, availableMinID int, filter domain.ChannelRepliesFilter, limit int, includeOffset bool) ([]domain.ChannelMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	where, args := channelRepliesBaseWhere(channelID, rootID, availableMinID, filter)
	where, args = appendChannelRepliesBackwardOffset(where, args, filter, includeOffset)
	args = append(args, limit)
	return s.queryChannelReplies(ctx, where, args, "DESC")
}

func (s *ChannelStore) queryChannelRepliesForward(ctx context.Context, channelID int64, rootID, availableMinID int, filter domain.ChannelRepliesFilter, limit int) ([]domain.ChannelMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	where, args := channelRepliesBaseWhere(channelID, rootID, availableMinID, filter)
	where, args = appendChannelRepliesForwardOffset(where, args, filter)
	args = append(args, limit)
	out, err := s.queryChannelReplies(ctx, where, args, "ASC")
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return channelMessageLess(out[i], out[j]) })
	return out, nil
}

func (s *ChannelStore) queryChannelReplies(ctx context.Context, where string, args []any, order string) ([]domain.ChannelMessage, error) {
	rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where+`
ORDER BY id `+order+`
LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list channel replies: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ChannelMessage, 0)
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func channelRepliesBaseWhere(channelID int64, rootID, availableMinID int, filter domain.ChannelRepliesFilter) (string, []any) {
	args := []any{channelID, rootID}
	where := "channel_id = $1 AND reply_to_top_id = $2 AND NOT deleted"
	if availableMinID > 0 {
		args = append(args, availableMinID)
		where += fmt.Sprintf(" AND id > $%d", len(args))
	}
	if filter.MaxID > 0 {
		args = append(args, filter.MaxID)
		where += fmt.Sprintf(" AND id < $%d", len(args))
	}
	if filter.MinID > 0 {
		args = append(args, filter.MinID)
		where += fmt.Sprintf(" AND id > $%d", len(args))
	}
	return where, args
}

func appendChannelRepliesBackwardOffset(where string, args []any, filter domain.ChannelRepliesFilter, include bool) (string, []any) {
	if filter.OffsetDate > 0 {
		args = append(args, filter.OffsetDate)
		if include {
			return where + fmt.Sprintf(" AND message_date <= $%d", len(args)), args
		}
		return where + fmt.Sprintf(" AND message_date < $%d", len(args)), args
	}
	if filter.OffsetID > 0 {
		args = append(args, filter.OffsetID)
		if include {
			return where + fmt.Sprintf(" AND id <= $%d", len(args)), args
		}
		return where + fmt.Sprintf(" AND id < $%d", len(args)), args
	}
	return where, args
}

func appendChannelRepliesForwardOffset(where string, args []any, filter domain.ChannelRepliesFilter) (string, []any) {
	if filter.OffsetDate > 0 {
		args = append(args, filter.OffsetDate)
		return where + fmt.Sprintf(" AND message_date >= $%d", len(args)), args
	}
	if filter.OffsetID > 0 {
		args = append(args, filter.OffsetID)
		return where + fmt.Sprintf(" AND id > $%d", len(args)), args
	}
	return where + " AND false", args
}

func updateForumTopicTopMessageTx(ctx context.Context, tx pgx.Tx, channelID int64, msg domain.ChannelMessage) error {
	if msg.ReplyTo == nil || !msg.ReplyTo.ForumTopic || msg.ReplyTo.TopMessageID <= 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
UPDATE channel_forum_topics
SET top_message_id = $3,
    date = $4,
    updated_at = now()
WHERE channel_id = $1 AND topic_id = $2 AND NOT deleted`,
		channelID, msg.ReplyTo.TopMessageID, msg.ID, msg.Date); err != nil {
		return fmt.Errorf("update forum topic top message: %w", err)
	}
	return nil
}

func scanChannelForumTopic(row rowScanner) (domain.ChannelForumTopic, error) {
	var topic domain.ChannelForumTopic
	if err := row.Scan(
		&topic.ChannelID,
		&topic.TopicID,
		&topic.CreatorUserID,
		&topic.Title,
		&topic.IconColor,
		&topic.IconEmojiID,
		&topic.TitleMissing,
		&topic.Closed,
		&topic.Hidden,
		&topic.Pinned,
		&topic.PinnedOrder,
		&topic.Date,
		&topic.TopMessageID,
		&topic.ReadInboxMaxID,
		&topic.ReadOutboxMaxID,
		&topic.UnreadCount,
		&topic.UnreadMentionsCount,
		&topic.UnreadReactionsCount,
		&topic.UnreadPollVotesCount,
	); err != nil {
		return domain.ChannelForumTopic{}, err
	}
	return topic, nil
}

func canManageForumTopic(channel domain.Channel, member domain.ChannelMember, topic domain.ChannelForumTopic, userID int64, selfBoostsApplied int) bool {
	if domain.ChannelBannedRightsBlockManageTopics(channel, member, selfBoostsApplied) {
		return false
	}
	if topic.CreatorUserID == userID {
		return true
	}
	return canPinChannelMessages(channel, member)
}
