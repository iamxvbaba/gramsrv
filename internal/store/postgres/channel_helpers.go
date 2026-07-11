package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) SaveChannelDefaultSendAs(ctx context.Context, req domain.SaveChannelDefaultSendAsRequest) (domain.ChannelView, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	var sendAsType sql.NullString
	var sendAsID sql.NullInt64
	if req.SendAs != nil {
		if req.SendAs.Type != domain.PeerTypeUser && req.SendAs.Type != domain.PeerTypeChannel {
			return domain.ChannelView{}, domain.ErrChannelInvalid
		}
		sendAsType = sql.NullString{String: string(req.SendAs.Type), Valid: true}
		sendAsID = sql.NullInt64{Int64: req.SendAs.ID, Valid: true}
	}
	channel, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	topMessageID := channel.TopMessageID
	if topMessageID <= member.AvailableMinID {
		topMessageID = 0
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO channel_dialogs (
    user_id, channel_id, top_message_id, top_message_date,
    read_inbox_max_id, read_outbox_max_id,
    default_send_as_peer_type, default_send_as_peer_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id, channel_id) DO UPDATE SET
    default_send_as_peer_type = EXCLUDED.default_send_as_peer_type,
    default_send_as_peer_id = EXCLUDED.default_send_as_peer_id,
    updated_at = now()`,
		req.UserID,
		req.ChannelID,
		topMessageID,
		channel.Date,
		member.ReadInboxMaxID,
		member.ReadOutboxMaxID,
		sendAsType,
		sendAsID,
	); err != nil {
		return domain.ChannelView{}, fmt.Errorf("save channel default send as: %w", err)
	}
	// 连接池写后立即失效本进程缓存，保证下面的 getChannelDialog 读到刚写入的值
	// (dialog_light NOTIFY 仅异步失效其它实例，本实例的 read-your-write 必须同步兜住)。
	if s.dialogCacheActive(s.db) {
		s.dialogCache.delete(req.UserID, req.ChannelID)
	}
	dialog, err := s.getChannelDialog(ctx, s.db, req.UserID, channel)
	if err != nil {
		return domain.ChannelView{}, err
	}
	return domain.ChannelView{Channel: channel, Self: member, Dialog: dialog}, nil
}

func (s *ChannelStore) DeleteChannel(ctx context.Context, req domain.DeleteChannelRequest) (domain.DeleteChannelResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.DeleteChannelResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DeleteChannelResult{}, fmt.Errorf("delete channel: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DeleteChannelResult{}, fmt.Errorf("begin delete channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelResult{}, err
	}
	if member.Role != domain.ChannelRoleCreator {
		return domain.DeleteChannelResult{}, domain.ErrChannelAdminRequired
	}
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	if _, err := tx.Exec(ctx, `UPDATE channels SET deleted = true, username = NULL, updated_at = now() WHERE id = $1`, req.ChannelID); err != nil {
		return domain.DeleteChannelResult{}, fmt.Errorf("mark channel deleted: %w", err)
	}
	if err := deletePeerUsernameTx(ctx, tx, peerUsernameTypeChannel, req.ChannelID); err != nil {
		return domain.DeleteChannelResult{}, err
	}
	if err := markUserChannelMemberIndexDeletedTx(ctx, tx, req.ChannelID, true); err != nil {
		return domain.DeleteChannelResult{}, err
	}
	channel.Deleted = true
	channel.Username = ""
	// 连带软删关联 monoforum(频道私信容器)。仅当 counterpart 本身是 monoforum 时才级联——这样删母广播
	// 频道会清掉其虚拟 mono,而(防御性地)绝不会因删 mono 反向把真实母频道也删掉。不级联会留下
	// monoforum=true 指向已删父频道的孤儿(客户端渲染崩 + DB 垃圾随删除累积)。
	var linkedMono *domain.Channel
	if channel.LinkedMonoforumID != 0 {
		mono, err := getChannelByID(ctx, tx, channel.LinkedMonoforumID)
		switch {
		case errors.Is(err, domain.ErrChannelInvalid):
			// 关联 mono 已不存在/已删,无需级联。
		case err != nil:
			return domain.DeleteChannelResult{}, fmt.Errorf("load linked monoforum: %w", err)
		case mono.Monoforum:
			if _, err := tx.Exec(ctx, `UPDATE channels SET deleted = true, username = NULL, updated_at = now() WHERE id = $1`, mono.ID); err != nil {
				return domain.DeleteChannelResult{}, fmt.Errorf("mark linked monoforum deleted: %w", err)
			}
			if err := deletePeerUsernameTx(ctx, tx, peerUsernameTypeChannel, mono.ID); err != nil {
				return domain.DeleteChannelResult{}, err
			}
			if err := markUserChannelMemberIndexDeletedTx(ctx, tx, mono.ID, true); err != nil {
				return domain.DeleteChannelResult{}, err
			}
			mono.Deleted = true
			mono.Username = ""
			linkedMono = &mono
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DeleteChannelResult{}, fmt.Errorf("commit delete channel: %w", err)
	}
	committed = true
	return domain.DeleteChannelResult{Channel: channel, Recipients: recipients, LinkedMonoforum: linkedMono}, nil
}

func (s *ChannelStore) SearchPublicChannels(ctx context.Context, viewerUserID int64, query string, limit int) (domain.PublicChannelSearchResult, error) {
	if viewerUserID == 0 {
		return domain.PublicChannelSearchResult{}, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxPublicChannelSearchLimit {
		limit = domain.MaxPublicChannelSearchLimit
	}
	queryLower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "@")))
	if queryLower == "" {
		return domain.PublicChannelSearchResult{}, nil
	}
	queryPrefix := escapeLike(queryLower) + "%"
	queryLike := "%" + escapeLike(queryLower) + "%"
	rows, err := s.db.Query(ctx, `
SELECT `+channelColumns+`
FROM channels c
WHERE NOT c.deleted
  AND (c.broadcast OR c.megagroup)
  AND COALESCE(c.username, '') <> ''
  AND NOT EXISTS (
    SELECT 1
    FROM channel_members m
    WHERE m.channel_id = c.id
      AND m.user_id = $1
      AND m.status = 'active'
  )
  AND (
    lower(c.username) = $2
    OR lower(c.username) LIKE $3 ESCAPE '\'
    OR lower(c.title) LIKE $3 ESCAPE '\'
    OR lower(c.username) LIKE $4 ESCAPE '\'
    OR lower(c.title) LIKE $4 ESCAPE '\'
  )
ORDER BY CASE
    WHEN lower(c.username) = $2 THEN 0
    WHEN lower(c.username) LIKE $3 ESCAPE '\' THEN 1
    WHEN lower(c.username) LIKE $4 ESCAPE '\' THEN 2
    WHEN lower(c.title) LIKE $3 ESCAPE '\' THEN 3
    ELSE 4
  END,
  c.participants_count DESC,
  c.date DESC,
  c.id DESC
LIMIT $5`, viewerUserID, queryLower, queryPrefix, queryLike, limit)
	if err != nil {
		return domain.PublicChannelSearchResult{}, fmt.Errorf("search public channels: %w", err)
	}
	defer rows.Close()
	out := domain.PublicChannelSearchResult{
		Results: make([]domain.Channel, 0, limit),
	}
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return domain.PublicChannelSearchResult{}, err
		}
		out.Results = append(out.Results, ch)
	}
	if err := rows.Err(); err != nil {
		return domain.PublicChannelSearchResult{}, err
	}
	return out, nil
}

func (s *ChannelStore) SearchPublicPosts(ctx context.Context, viewerUserID int64, req domain.ChannelSearchPostsRequest) (domain.ChannelHistory, error) {
	query := strings.TrimSpace(req.Query)
	hashtag := strings.TrimSpace(req.Hashtag)
	if (query == "") == (hashtag == "") {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelSearchPostsLimit {
		limit = domain.MaxChannelSearchPostsLimit
	}
	args := []any{}
	where := `NOT deleted
AND body <> ''
AND EXISTS (
  SELECT 1
  FROM channels c
  WHERE c.id = channel_messages.channel_id
    AND NOT c.deleted
    AND COALESCE(c.username, '') <> ''
	)`
	if query != "" {
		args = append(args, "%"+escapeLike(query)+"%")
		where += fmt.Sprintf(" AND body ILIKE $%d ESCAPE '\\'", len(args))
	}
	if hashtag != "" {
		args = append(args, "%#"+escapeLike(hashtag)+"%")
		where += fmt.Sprintf(" AND body ILIKE $%d ESCAPE '\\'", len(args))
	}
	switch {
	case req.OffsetRate > 0 && req.OffsetChannelID > 0 && req.OffsetID > 0:
		args = append(args, req.OffsetRate, req.OffsetChannelID, req.OffsetID)
		n := len(args)
		where += fmt.Sprintf(" AND (message_date < $%d OR (message_date = $%d AND (channel_id < $%d OR (channel_id = $%d AND id < $%d))))", n-2, n-2, n-1, n-1, n)
	case req.OffsetRate > 0:
		args = append(args, req.OffsetRate)
		where += fmt.Sprintf(" AND message_date < $%d", len(args))
	case req.OffsetChannelID > 0 && req.OffsetID > 0:
		args = append(args, req.OffsetChannelID, req.OffsetID)
		n := len(args)
		where += fmt.Sprintf(" AND (channel_id < $%d OR (channel_id = $%d AND id < $%d))", n-1, n-1, n)
	case req.OffsetID > 0:
		args = append(args, req.OffsetID)
		where += fmt.Sprintf(" AND id < $%d", len(args))
	}
	queryLimit := limit + 1
	args = append(args, queryLimit)
	rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where+`
ORDER BY message_date DESC, channel_id DESC, id DESC
LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return domain.ChannelHistory{}, fmt.Errorf("search public channel posts: %w", err)
	}
	defer rows.Close()
	out := domain.ChannelHistory{}
	channelRefs := make(map[int64]struct{})
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return domain.ChannelHistory{}, err
		}
		out.Messages = append(out.Messages, msg)
		channelRefs[msg.ChannelID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelHistory{}, err
	}
	if len(out.Messages) > limit {
		out.Messages = out.Messages[:limit]
		out.Count = limit + 1
		channelRefs = make(map[int64]struct{}, len(out.Messages))
		for _, msg := range out.Messages {
			channelRefs[msg.ChannelID] = struct{}{}
		}
	} else {
		out.Count = len(out.Messages)
	}
	channels, err := listChannelsByIDs(ctx, s.db, mapKeysInt64(channelRefs))
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	out.Channels = channels
	if err := s.populateChannelMessagesReactions(ctx, s.db, viewerUserID, out.Channels, out.Messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	return out, nil
}

func (s *ChannelStore) ListActiveChannelIDsForUser(ctx context.Context, userID, afterChannelID int64, limit int) ([]int64, error) {
	if userID == 0 || afterChannelID < 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'active'
  AND NOT deleted
  AND channel_id > $2
ORDER BY channel_id
LIMIT $3`, userID, afterChannelID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active channel ids for user: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0, limit)
	for rows.Next() {
		var channelID int64
		if err := rows.Scan(&channelID); err != nil {
			return nil, err
		}
		out = append(out, channelID)
	}
	return out, rows.Err()
}

func (s *ChannelStore) ListDirtyActiveChannelsForUser(ctx context.Context, userID int64, sinceDate int, afterChannelID int64, limit int) ([]domain.DirtyChannel, error) {
	if userID == 0 || sinceDate <= 0 || afterChannelID < 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxChannelDifferenceLimit {
		limit = domain.MaxChannelDifferenceLimit
	}
	rows, err := s.db.Query(ctx, `
SELECT i.channel_id, c.pts
FROM user_channel_member_index i
JOIN channels c ON c.id = i.channel_id AND NOT c.deleted
JOIN channel_update_checkpoints cp ON cp.channel_id = i.channel_id
WHERE i.user_id = $1
  AND i.status = 'active'
  AND NOT i.deleted
  AND i.channel_id > $3
  AND cp.latest_event_date > $2
ORDER BY i.channel_id ASC
LIMIT $4`, userID, sinceDate, afterChannelID, limit)
	if err != nil {
		return nil, fmt.Errorf("list dirty active channels for user: %w", err)
	}
	defer rows.Close()
	out := make([]domain.DirtyChannel, 0, limit)
	for rows.Next() {
		var item domain.DirtyChannel
		if err := rows.Scan(&item.ChannelID, &item.Pts); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *ChannelStore) getChannelForViewer(ctx context.Context, db sqlcgen.DBTX, viewerUserID, channelID int64) (domain.Channel, domain.ChannelMember, bool, error) {
	ch, member, err := s.getChannelForMember(ctx, db, viewerUserID, channelID)
	if err == nil {
		return ch, member, false, nil
	}
	if !errors.Is(err, domain.ErrChannelPrivate) {
		return domain.Channel{}, domain.ChannelMember{}, false, err
	}
	ch, err = s.channelByID(ctx, db, channelID)
	if err != nil {
		return domain.Channel{}, domain.ChannelMember{}, false, err
	}
	if guest, ok, guestErr := s.getLinkedDiscussionGuest(ctx, db, viewerUserID, ch); guestErr != nil {
		return domain.Channel{}, domain.ChannelMember{}, false, guestErr
	} else if ok {
		return ch, guest, true, nil
	}
	if member, _, ok, err := s.monoforumAdminPreview(ctx, db, viewerUserID, ch); err != nil {
		return domain.Channel{}, domain.ChannelMember{}, false, err
	} else if ok {
		return ch, member, true, nil
	}
	if !publicPreviewableChannel(ch) {
		return domain.Channel{}, domain.ChannelMember{}, false, domain.ErrChannelPrivate
	}
	member, err = s.getPublicPreviewMember(ctx, db, viewerUserID, ch)
	if err != nil {
		return domain.Channel{}, domain.ChannelMember{}, false, err
	}
	return ch, member, true, nil
}

func getChannelByID(ctx context.Context, db sqlcgen.DBTX, channelID int64) (domain.Channel, error) {
	ch, err := scanChannel(db.QueryRow(ctx, `SELECT `+channelColumns+` FROM channels c WHERE c.id = $1 AND NOT c.deleted`, channelID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return ch, err
}

// channelByID 是 getChannelByID 的缓存读穿版本，仅在连接池句柄上消费缓存(事务内绕过)。
// 命中即返回缓存的频道行；未命中查 PG 并回填。失效由 ReadModelChangeListener 实时驱动。
func (s *ChannelStore) channelByID(ctx context.Context, db sqlcgen.DBTX, channelID int64) (domain.Channel, error) {
	if s.cacheActive(db) {
		return s.rowCache.getOrLoad(ctx, channelID, func() (domain.Channel, error) {
			return getChannelByID(ctx, db, channelID)
		})
	}
	return getChannelByID(ctx, db, channelID)
}

func listChannelsByIDs(ctx context.Context, db sqlcgen.DBTX, ids []int64) ([]domain.Channel, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, `SELECT `+channelColumns+` FROM channels c WHERE c.id = ANY($1::bigint[]) AND NOT c.deleted ORDER BY c.id ASC`, ids)
	if err != nil {
		return nil, fmt.Errorf("list channels by ids: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Channel, 0, len(ids))
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func listChannelsByIDsInOrder(ctx context.Context, db sqlcgen.DBTX, ids []int64) ([]domain.Channel, error) {
	channels, err := listChannelsByIDs(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]domain.Channel, len(channels))
	for _, channel := range channels {
		byID[channel.ID] = channel
	}
	out := make([]domain.Channel, 0, len(ids))
	for _, id := range ids {
		if channel, ok := byID[id]; ok {
			out = append(out, channel)
		}
	}
	return out, nil
}

func listUsersByIDs(ctx context.Context, db sqlcgen.DBTX, ids []int64) ([]domain.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, `
SELECT id, access_hash, phone, first_name, last_name, username, country_code, verified, support, is_bot, bot_info_version,
       COALESCE(EXTRACT(EPOCH FROM premium_expires_at), 0)::bigint AS premium_until,
       emoji_status_document_id,
       emoji_status_until
FROM users
WHERE id = ANY($1::bigint[])
ORDER BY id ASC`, ids)
	if err != nil {
		return nil, fmt.Errorf("list users by ids: %w", err)
	}
	defer rows.Close()
	out := make([]domain.User, 0, len(ids))
	for rows.Next() {
		var u domain.User
		var premiumUntil, emojiStatusUntil int64
		if err := rows.Scan(&u.ID, &u.AccessHash, &u.Phone, &u.FirstName, &u.LastName, &u.Username, &u.CountryCode, &u.Verified, &u.Support, &u.Bot, &u.BotInfoVersion, &premiumUntil, &u.EmojiStatusDocumentID, &emojiStatusUntil); err != nil {
			return nil, err
		}
		u.PremiumUntil = int(premiumUntil)
		u.EmojiStatusUntil = int(emojiStatusUntil)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type channelReplyStatKey struct {
	channelID int64
	rootID    int
}

func (s *ChannelStore) topicWithViewerCounters(ctx context.Context, viewerUserID, channelID int64, topic domain.ChannelForumTopic, readMaxID, availableMinID int) domain.ChannelForumTopic {
	topic.UnreadCount = s.channelThreadUnreadCount(ctx, channelID, topic.TopicID, viewerUserID, readMaxID)
	topic.UnreadMentionsCount = s.countChannelUnreadMentionsForTop(ctx, viewerUserID, channelID, topic.TopicID)
	topic.UnreadReactionsCount = s.countChannelUnreadReactionsForTop(ctx, viewerUserID, channelID, topic.TopicID, availableMinID)
	return topic
}

func (s *ChannelStore) populateForumTopicViewerCounters(ctx context.Context, viewerUserID, channelID int64, topics []domain.ChannelForumTopic, availableMinID int) error {
	if len(topics) == 0 || viewerUserID == 0 || channelID == 0 {
		return nil
	}
	roots := make([]int32, 0, len(topics))
	seen := make(map[int]struct{}, len(topics))
	indexes := make(map[int][]int, len(topics))
	for i := range topics {
		topics[i].UnreadCount = 0
		topics[i].UnreadMentionsCount = 0
		topics[i].UnreadReactionsCount = 0
		rootID := topics[i].TopicID
		indexes[rootID] = append(indexes[rootID], i)
		if rootID <= 0 {
			continue
		}
		if _, ok := seen[rootID]; ok {
			continue
		}
		seen[rootID] = struct{}{}
		roots = append(roots, int32(rootID))
	}
	if len(roots) == 0 {
		return nil
	}
	// per-topic 已读水位：每个话题用各自的 read_inbox_max_id 现算未读，并下发真实已读位（消除死列），
	// 消除频道级单一水位串扰。
	waters, err := s.channelTopicReadBatch(ctx, channelID, viewerUserID, roots, availableMinID)
	if err != nil {
		return err
	}
	inbox := make(map[int]int, len(waters))
	for i := range topics {
		w := waters[topics[i].TopicID]
		inbox[topics[i].TopicID] = w.Inbox
		topics[i].ReadInboxMaxID = w.Inbox
		topics[i].ReadOutboxMaxID = w.Outbox
	}
	if err := s.populateForumTopicUnreadCounts(ctx, channelID, roots, viewerUserID, inbox, indexes, topics); err != nil {
		return err
	}
	if err := s.populateForumTopicUnreadMentionCounts(ctx, viewerUserID, channelID, roots, indexes, topics); err != nil {
		return err
	}
	if err := s.populateForumTopicUnreadReactionCounts(ctx, viewerUserID, channelID, roots, availableMinID, indexes, topics); err != nil {
		return err
	}
	return nil
}

func (s *ChannelStore) populateForumTopicUnreadCounts(ctx context.Context, channelID int64, roots []int32, viewerUserID int64, waters map[int]int, indexes map[int][]int, topics []domain.ChannelForumTopic) error {
	// 每个 root 配对各自的 per-topic 已读水位，unnest 配对后按 topic 各自门槛 COUNT。
	readMaxes := make([]int32, len(roots))
	for i, r := range roots {
		readMaxes[i] = int32(waters[int(r)])
	}
	rows, err := s.db.Query(ctx, `
SELECT cm.reply_to_top_id, COUNT(*)::int
FROM channel_messages cm
JOIN unnest($2::int[], $3::int[]) AS w(topic_id, read_max) ON cm.reply_to_top_id = w.topic_id
WHERE cm.channel_id = $1
  AND cm.id > w.read_max
  AND cm.sender_user_id <> $4
  AND NOT cm.deleted
GROUP BY cm.reply_to_top_id`, channelID, roots, readMaxes, viewerUserID)
	if err != nil {
		return fmt.Errorf("count forum topic unread messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rootID, count int
		if err := rows.Scan(&rootID, &count); err != nil {
			return err
		}
		for _, idx := range indexes[rootID] {
			topics[idx].UnreadCount = count
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func (s *ChannelStore) populateForumTopicUnreadMentionCounts(ctx context.Context, userID, channelID int64, roots []int32, indexes map[int][]int, topics []domain.ChannelForumTopic) error {
	rows, err := s.db.Query(ctx, `
SELECT top_message_id, COUNT(*)::int
FROM channel_unread_mentions
WHERE user_id = $1
  AND channel_id = $2
  AND top_message_id = ANY($3::int[])
  AND unread
GROUP BY top_message_id`, userID, channelID, roots)
	if err != nil {
		return fmt.Errorf("count forum topic unread mentions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rootID, count int
		if err := rows.Scan(&rootID, &count); err != nil {
			return err
		}
		for _, idx := range indexes[rootID] {
			topics[idx].UnreadMentionsCount = count
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func (s *ChannelStore) populateForumTopicUnreadReactionCounts(ctx context.Context, userID, channelID int64, roots []int32, availableMinID int, indexes map[int][]int, topics []domain.ChannelForumTopic) error {
	rows, err := s.db.Query(ctx, `
WITH reaction_messages AS (
  SELECT
    r.message_id,
    CASE
      WHEN cm.id = ANY($3::int[]) THEN cm.id
      ELSE COALESCE(NULLIF(cm.reply_to_top_id, 0), NULLIF(cm.reply_to_msg_id, 0), 0)
    END AS topic_id
  FROM channel_message_reactions r
  JOIN channel_messages cm ON cm.channel_id = r.channel_id AND cm.id = r.message_id
  WHERE r.sender_user_id = $1
    AND r.channel_id = $2
    AND r.unread
    AND r.reacted_user_id <> $1
    AND cm.id > $4
    AND NOT cm.deleted
    AND (
      cm.id = ANY($3::int[])
      OR COALESCE(NULLIF(cm.reply_to_top_id, 0), NULLIF(cm.reply_to_msg_id, 0), 0) = ANY($3::int[])
    )
)
SELECT topic_id, COUNT(DISTINCT message_id)::int
FROM reaction_messages
WHERE topic_id <> 0
GROUP BY topic_id`, userID, channelID, roots, availableMinID)
	if err != nil {
		return fmt.Errorf("count forum topic unread reactions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rootID, count int
		if err := rows.Scan(&rootID, &count); err != nil {
			return err
		}
		for _, idx := range indexes[rootID] {
			topics[idx].UnreadReactionsCount = count
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func (s *ChannelStore) resolveChannelReply(ctx context.Context, db sqlcgen.DBTX, req domain.SendChannelMessageRequest, member domain.ChannelMember, channel domain.Channel, selfBoostsApplied int) (*domain.MessageReply, error) {
	if req.ReplyTo == nil {
		return nil, nil
	}
	if err := domain.ValidateMessageReplyBounds(req.ReplyTo); err != nil {
		return nil, err
	}
	peer := req.ReplyTo.Peer
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	if peer.ID == 0 {
		peer = channelPeer
	}
	if peer != channelPeer {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	if req.ReplyTo.MessageID == 0 {
		if req.ReplyTo.TopMessageID <= 0 || !channel.Forum {
			return nil, domain.ErrReplyMessageIDInvalid
		}
		topic, err := s.getForumTopic(ctx, db, req.ChannelID, req.ReplyTo.TopMessageID)
		if err != nil {
			return nil, domain.ErrReplyMessageIDInvalid
		}
		if topic.Hidden {
			return nil, domain.ErrReplyMessageIDInvalid
		}
		if topic.Closed && !canManageForumTopic(channel, member, topic, req.UserID, selfBoostsApplied) {
			return nil, domain.ErrChannelWriteForbidden
		}
		reply := cloneMessageReply(req.ReplyTo)
		reply.MessageID = 0
		reply.Peer = channelPeer
		reply.TopMessageID = topic.TopicID
		reply.ForumTopic = true
		return reply, nil
	}
	target, err := s.getChannelMessage(ctx, db, req.ChannelID, req.ReplyTo.MessageID)
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) || errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrReplyMessageIDInvalid
		}
		return nil, err
	}
	if target.Deleted || target.ID <= member.AvailableMinID {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	reply := cloneMessageReply(req.ReplyTo)
	reply.MessageID = target.ID
	reply.Peer = channelPeer
	reply.TopMessageID = target.ID
	if target.ReplyTo != nil && target.ReplyTo.TopMessageID > 0 {
		reply.TopMessageID = target.ReplyTo.TopMessageID
	}
	if req.ReplyTo.TopMessageID > 0 && req.ReplyTo.TopMessageID != reply.TopMessageID {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	if channel.Forum && reply.TopMessageID > 0 {
		if topic, err := s.getForumTopic(ctx, db, req.ChannelID, reply.TopMessageID); err == nil && !topic.Hidden {
			if topic.Closed && !canManageForumTopic(channel, member, topic, req.UserID, selfBoostsApplied) {
				return nil, domain.ErrChannelWriteForbidden
			}
			reply.ForumTopic = true
		} else if err != nil && !errors.Is(err, domain.ErrMessageIDInvalid) {
			return nil, err
		}
	}
	return reply, nil
}

func visibleChannelTopAfter(ctx context.Context, db sqlcgen.DBTX, channelID int64, availableMinID int, fallbackDate int) (int, int, error) {
	var id, date int
	err := db.QueryRow(ctx, `
SELECT id, message_date
FROM channel_messages
WHERE channel_id = $1 AND id > $2 AND NOT deleted
ORDER BY id DESC
LIMIT 1`, channelID, availableMinID).Scan(&id, &date)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fallbackDate, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("select visible channel top: %w", err)
	}
	return id, date, nil
}

func discussionGroupUpdateResult(changed map[int64]domain.Channel) domain.DiscussionGroupUpdateResult {
	ids := make([]int64, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})
	out := domain.DiscussionGroupUpdateResult{Channels: make([]domain.Channel, 0, len(ids))}
	for _, id := range ids {
		out.Channels = append(out.Channels, changed[id])
	}
	return out
}

func canChangeChannelInfo(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.ChangeInfo)
}

func boolPtr(v bool) *bool {
	return &v
}

func channelInitialAvailableMinID(channel domain.Channel) int {
	if channel.PreHistoryHidden {
		return channel.TopMessageID
	}
	return 0
}

func adminRightsSubset(want, have domain.ChannelAdminRights) bool {
	return (!want.ChangeInfo || have.ChangeInfo) &&
		(!want.PostMessages || have.PostMessages) &&
		(!want.EditMessages || have.EditMessages) &&
		(!want.DeleteMessages || have.DeleteMessages) &&
		(!want.PostStories || have.PostStories) &&
		(!want.EditStories || have.EditStories) &&
		(!want.DeleteStories || have.DeleteStories) &&
		(!want.BanUsers || have.BanUsers) &&
		(!want.InviteUsers || have.InviteUsers) &&
		(!want.PinMessages || have.PinMessages) &&
		(!want.AddAdmins || have.AddAdmins) &&
		(!want.ManageCall || have.ManageCall) &&
		(!want.Anonymous || have.Anonymous) &&
		(!want.ManageRanks || have.ManageRanks) &&
		(!want.ManageDirectMessages || have.ManageDirectMessages)
}

// checkEditMemberRank validates a rank-only (member tag) edit: creator edits
// anyone but no one else edits the creator; admins always edit their own tag,
// and with ManageRanks edit plain members plus admins they promoted; plain
// members edit only their own tag and only while neither the channel default
// nor their personal banned rights set edit_rank. Member tags exist only in
// megagroups: broadcast participants must keep an empty rank so the admins
// participant filter stays a pure admin list there.
func checkEditMemberRank(channel domain.Channel, actor, target domain.ChannelMember) error {
	if !channel.Megagroup {
		return domain.ErrMegagroupIDInvalid
	}
	if actor.UserID == target.UserID {
		if actor.Role == domain.ChannelRoleCreator || actor.Role == domain.ChannelRoleAdmin {
			return nil
		}
		if channel.DefaultBannedRights.EditRank || actor.BannedRights.EditRank {
			return domain.ErrChannelRightForbidden
		}
		return nil
	}
	if target.Role == domain.ChannelRoleCreator {
		return domain.ErrChannelUserCreator
	}
	if actor.Role == domain.ChannelRoleCreator {
		return nil
	}
	if actor.Role != domain.ChannelRoleAdmin || !actor.AdminRights.ManageRanks {
		return domain.ErrChannelAdminRequired
	}
	if target.Role == domain.ChannelRoleAdmin && target.InviterUserID != actor.UserID {
		return domain.ErrChannelRightForbidden
	}
	return nil
}

func adminLogBanType(previous, next domain.ChannelMember) domain.ChannelAdminLogEventType {
	if next.Status == domain.ChannelMemberKicked || next.BannedRights.ViewMessages {
		return domain.ChannelAdminLogParticipantKick
	}
	if previous.Status == domain.ChannelMemberKicked || previous.BannedRights.ViewMessages {
		return domain.ChannelAdminLogParticipantUnkick
	}
	if !zeroChannelBannedRights(next.BannedRights) {
		return domain.ChannelAdminLogParticipantBan
	}
	return domain.ChannelAdminLogParticipantUnban
}

func adminLogSearchText(event domain.ChannelAdminLogEvent) string {
	parts := []string{
		event.Query,
		event.PrevString,
		event.NewString,
	}
	for _, msg := range []*domain.ChannelMessage{event.Message, event.PrevMessage, event.NewMessage} {
		if msg != nil {
			parts = append(parts, msg.Body)
		}
	}
	return strings.ToLower(strings.TrimSpace(strings.Join(parts, " ")))
}

func adminLogLikePattern(query string) string {
	query = strings.ReplaceAll(query, `\`, `\\`)
	query = strings.ReplaceAll(query, `%`, `\%`)
	query = strings.ReplaceAll(query, `_`, `\_`)
	return "%" + query + "%"
}

func (a pgChannelIDAllocator) NextChannelID(ctx context.Context) (int64, error) {
	current, err := a.CurrentChannelID(ctx)
	if err != nil {
		return 0, err
	}
	return current + 1, nil
}

func (a pgChannelIDAllocator) CurrentChannelID(ctx context.Context) (int64, error) {
	var id int64
	err := a.db.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM channels`).Scan(&id)
	return id, err
}
