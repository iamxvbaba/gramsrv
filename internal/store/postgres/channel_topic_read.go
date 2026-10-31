package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// topicReadFallbackInbox is the viewer's initial read watermark for a topic with no read row:
// messages before the topic root are treated as read (root-1), and it is not below available_min
// (invisible before the trim line). For General (topic 1), root-1=0, i.e. "unread from the start".
// This way, when the migration goes live it does not turn all existing topics unread in one jump.
func topicReadFallbackInbox(topicID, availableMinID int) int {
	base := topicID - 1
	if availableMinID > base {
		base = availableMinID
	}
	if base < 0 {
		base = 0
	}
	return base
}

// channelTopicMessageCond returns the message-belonging predicate for a topic, with the placeholder carrying topicID.
// General(=1) merges reply_to_top_id IN (placeholder, 0): both new messages inside General (=1) and
// pre-forum historical messages (=0) count as General.
func channelTopicMessageCond(topicID int, placeholder string) string {
	if topicID == domain.ForumGeneralTopicID {
		// General merges reply_to_top_id ∈ {placeholder,0}, but excludes other topics' root
		// service messages (their reply_to_top_id=0, but their id is some topic root, so they
		// are not part of General).
		return "(cm.reply_to_top_id = " + placeholder + " OR cm.reply_to_top_id = 0)" +
			" AND NOT EXISTS (SELECT 1 FROM channel_forum_topics gt WHERE gt.channel_id = cm.channel_id AND gt.topic_id = cm.id AND NOT gt.deleted)"
	}
	return "cm.reply_to_top_id = " + placeholder
}

// channelTopicReadInbox returns the viewer's read watermark for a topic, falling back when there is no row.
func (s *ChannelStore) channelTopicReadInbox(ctx context.Context, db sqlcgen.DBTX, channelID, userID int64, topicID, availableMinID int) (int, error) {
	var v int
	err := db.QueryRow(ctx, `SELECT read_inbox_max_id FROM channel_topic_read WHERE channel_id = $1 AND user_id = $2 AND topic_id = $3`, channelID, userID, topicID).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return topicReadFallbackInbox(topicID, availableMinID), nil
	}
	if err != nil {
		return 0, fmt.Errorf("select channel topic read: %w", err)
	}
	if fb := topicReadFallbackInbox(topicID, availableMinID); v < fb {
		v = fb
	}
	return v, nil
}

// topicReadWater is the viewer's inbox/outbox read watermark for a topic.
type topicReadWater struct {
	Inbox  int
	Outbox int
}

// channelTopicReadBatch fetches the per-viewer read watermarks for multiple topics in a batch (used by getForumTopics for on-the-fly computing and delivery);
// topics with no row fall back (inbox=root-1, outbox=0). Returns topicID -> watermark.
func (s *ChannelStore) channelTopicReadBatch(ctx context.Context, channelID, userID int64, topicIDs []int32, availableMinID int) (map[int]topicReadWater, error) {
	out := make(map[int]topicReadWater, len(topicIDs))
	for _, t := range topicIDs {
		out[int(t)] = topicReadWater{Inbox: topicReadFallbackInbox(int(t), availableMinID)}
	}
	if len(topicIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT topic_id, read_inbox_max_id, read_outbox_max_id FROM channel_topic_read WHERE channel_id = $1 AND user_id = $2 AND topic_id = ANY($3::int[])`, channelID, userID, topicIDs)
	if err != nil {
		return nil, fmt.Errorf("batch channel topic read: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var topicID, inbox, outbox int
		if err := rows.Scan(&topicID, &inbox, &outbox); err != nil {
			return nil, err
		}
		w := out[topicID]
		if inbox > w.Inbox {
			w.Inbox = inbox
		}
		w.Outbox = outbox
		out[topicID] = w
	}
	return out, rows.Err()
}

// channelTopicTopMessageID returns a topic's current latest visible message id, as the upper bound for advancing readDiscussion.
func (s *ChannelStore) channelTopicTopMessageID(ctx context.Context, channelID int64, topicID, availableMinID int) (int, error) {
	var v int
	err := s.db.QueryRow(ctx, `
SELECT COALESCE(MAX(cm.id), 0)::int
FROM channel_messages cm
WHERE cm.channel_id = $1 AND `+channelTopicMessageCond(topicID, "$2")+` AND cm.id > $3 AND NOT cm.deleted`,
		channelID, topicID, availableMinID).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("channel topic top message: %w", err)
	}
	return v, nil
}

// ReadChannelTopicHistory advances the viewer's per-topic read watermark for a single forum topic (messages.readDiscussion),
// without touching the channel-level channel_members.read_inbox_max_id (eliminating cross-topic read crosstalk), and returns the senders whose outbox needs advancing.
func (s *ChannelStore) ReadChannelTopicHistory(ctx context.Context, req domain.ReadChannelTopicHistoryRequest) (domain.ReadChannelTopicHistoryResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.TopicID <= 0 {
		return domain.ReadChannelTopicHistoryResult{}, domain.ErrChannelInvalid
	}
	channel, member, err := s.getChannelForMemberOrLinkedGuest(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReadChannelTopicHistoryResult{}, err
	}
	if !channel.Forum {
		return domain.ReadChannelTopicHistoryResult{}, domain.ErrChannelForumMissing
	}
	if member.Guest {
		return domain.ReadChannelTopicHistoryResult{}, domain.ErrChannelPrivate
	}
	topMax, err := s.channelTopicTopMessageID(ctx, req.ChannelID, req.TopicID, member.AvailableMinID)
	if err != nil {
		return domain.ReadChannelTopicHistoryResult{}, err
	}
	maxID := req.MaxID
	if maxID <= 0 || maxID > topMax {
		maxID = topMax
	}
	prev, err := s.channelTopicReadInbox(ctx, s.db, req.ChannelID, req.UserID, req.TopicID, member.AvailableMinID)
	if err != nil {
		return domain.ReadChannelTopicHistoryResult{}, err
	}
	if maxID <= prev {
		return domain.ReadChannelTopicHistoryResult{Channel: channel, TopicID: req.TopicID, MaxID: prev, Changed: false, Pts: channel.Pts}, nil
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO channel_topic_read (channel_id, user_id, topic_id, read_inbox_max_id, read_inbox_date, updated_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (channel_id, user_id, topic_id) DO UPDATE SET
    read_inbox_date = CASE WHEN channel_topic_read.read_inbox_max_id < EXCLUDED.read_inbox_max_id THEN EXCLUDED.read_inbox_date ELSE channel_topic_read.read_inbox_date END,
    read_inbox_max_id = GREATEST(channel_topic_read.read_inbox_max_id, EXCLUDED.read_inbox_max_id),
    updated_at = now()`,
		req.ChannelID, req.UserID, req.TopicID, maxID, req.Date); err != nil {
		return domain.ReadChannelTopicHistoryResult{}, fmt.Errorf("upsert channel topic read inbox: %w", err)
	}
	outbox, err := s.advanceChannelTopicReadOutbox(ctx, req.ChannelID, req.UserID, req.TopicID, prev, maxID)
	if err != nil {
		return domain.ReadChannelTopicHistoryResult{}, err
	}
	return domain.ReadChannelTopicHistoryResult{Channel: channel, TopicID: req.TopicID, MaxID: maxID, Changed: true, Pts: channel.Pts, OutboxUpdates: outbox}, nil
}

// advanceChannelTopicReadOutbox advances the per-topic read_outbox watermark of the senders
// covered by this read within the topic, and returns those senders for online delivery of
// updateReadChannelDiscussionOutbox (read receipt ✓✓). The receipt UPSERT is idempotent and does
// not need to be strongly atomic with the inbox.
func (s *ChannelStore) advanceChannelTopicReadOutbox(ctx context.Context, channelID, readerUserID int64, topicID, prev, maxID int) ([]domain.ChannelReadOutboxUpdate, error) {
	rows, err := s.db.Query(ctx, `
SELECT DISTINCT cm.sender_user_id
FROM channel_messages cm
WHERE cm.channel_id = $1 AND `+channelTopicMessageCond(topicID, "$2")+`
  AND cm.id > $3 AND cm.id <= $4 AND cm.sender_user_id <> $5 AND cm.sender_user_id <> 0 AND NOT cm.deleted`,
		channelID, topicID, prev, maxID, readerUserID)
	if err != nil {
		return nil, fmt.Errorf("scan channel topic outbox senders: %w", err)
	}
	senders := make([]int64, 0, 8)
	for rows.Next() {
		var sender int64
		if err := rows.Scan(&sender); err != nil {
			rows.Close()
			return nil, err
		}
		senders = append(senders, sender)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	updates := make([]domain.ChannelReadOutboxUpdate, 0, len(senders))
	for _, sender := range senders {
		if _, err := s.db.Exec(ctx, `
INSERT INTO channel_topic_read (channel_id, user_id, topic_id, read_outbox_max_id, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (channel_id, user_id, topic_id) DO UPDATE SET
    read_outbox_max_id = GREATEST(channel_topic_read.read_outbox_max_id, EXCLUDED.read_outbox_max_id),
    updated_at = now()`,
			channelID, sender, topicID, maxID); err != nil {
			return nil, fmt.Errorf("upsert channel topic read outbox: %w", err)
		}
		updates = append(updates, domain.ChannelReadOutboxUpdate{UserID: sender, MaxID: maxID})
	}
	return updates, nil
}

// GeneralForumTopic computes on the fly the state of the General topic (id=1) for the viewer: it belongs to reply_to_top_id ∈ {0,1} and
// excludes other topics' root service messages; unread uses General's own per-topic watermark (not crosstalked by ordinary topic reads).
func (s *ChannelStore) GeneralForumTopic(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelForumTopic, error) {
	if viewerUserID == 0 || channelID == 0 {
		return domain.ChannelForumTopic{}, domain.ErrChannelInvalid
	}
	channel, member, err := s.getChannelForMember(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelForumTopic{}, err
	}
	if !channel.Forum {
		return domain.ChannelForumTopic{}, domain.ErrChannelForumMissing
	}
	gid := domain.ForumGeneralTopicID
	waters, err := s.channelTopicReadBatch(ctx, channelID, viewerUserID, []int32{int32(gid)}, member.AvailableMinID)
	if err != nil {
		return domain.ChannelForumTopic{}, err
	}
	water := waters[gid]
	cond := channelTopicMessageCond(gid, "$2")
	var topMsg int
	if err := s.db.QueryRow(ctx, `
SELECT COALESCE(MAX(cm.id), 0)::int FROM channel_messages cm
WHERE cm.channel_id = $1 AND `+cond+` AND cm.id > $3 AND NOT cm.deleted`,
		channelID, gid, member.AvailableMinID).Scan(&topMsg); err != nil {
		return domain.ChannelForumTopic{}, fmt.Errorf("general top message: %w", err)
	}
	var unread int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int FROM (
  SELECT 1 FROM channel_messages cm
  WHERE cm.channel_id = $1 AND `+cond+` AND cm.id > $3 AND cm.sender_user_id <> $4 AND NOT cm.deleted
  LIMIT $5
) g`, channelID, gid, water.Inbox, viewerUserID, domain.MaxDialogUnreadCount).Scan(&unread); err != nil {
		return domain.ChannelForumTopic{}, fmt.Errorf("general unread: %w", err)
	}
	return domain.ChannelForumTopic{
		ChannelID:            channelID,
		TopicID:              gid,
		Title:                "General",
		CreatorUserID:        channel.CreatorUserID,
		Date:                 channel.Date,
		TopMessageID:         topMsg,
		ReadInboxMaxID:       water.Inbox,
		ReadOutboxMaxID:      water.Outbox,
		UnreadCount:          unread,
		UnreadMentionsCount:  s.countChannelUnreadMentionsForTop(ctx, viewerUserID, channelID, gid),
		UnreadReactionsCount: s.countChannelUnreadReactionsForTop(ctx, viewerUserID, channelID, gid, member.AvailableMinID),
	}, nil
}
