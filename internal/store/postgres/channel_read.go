package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func channelDialogDynamicUnreadCountSQL(readInboxExpr, topIDExpr string) string {
	// The LIMIT subquery clamps the dynamic unread COUNT to MaxDialogUnreadCount (P1-v): it
	// bounds the scan work (at most cap rows, avoiding an O(backlog) scan when a
	// broadcast/large group has a backlog) and clamps the delivered badge to the upper bound.
	return fmt.Sprintf(`(
           SELECT COUNT(*)::int
           FROM (
               SELECT 1
               FROM channel_messages cm_unread
               WHERE cm_unread.channel_id = c.id
                 AND cm_unread.id > GREATEST(%s, m.available_min_id)
                 AND cm_unread.id <= %s
                 AND NOT cm_unread.deleted
                 AND cm_unread.sender_user_id <> m.user_id
               LIMIT %d
           ) cm_unread_capped
       )`, readInboxExpr, topIDExpr, domain.MaxDialogUnreadCount)
}

func channelDialogDynamicUnreadExistsSQL(readInboxExpr, topIDExpr string) string {
	return fmt.Sprintf(`EXISTS (
           SELECT 1
           FROM channel_messages cm_unread
           WHERE cm_unread.channel_id = c.id
             AND cm_unread.id > GREATEST(%s, m.available_min_id)
             AND cm_unread.id <= %s
             AND NOT cm_unread.deleted
             AND cm_unread.sender_user_id <> m.user_id
       )`, readInboxExpr, topIDExpr)
}

// channelDialogVisibleUnreadCountSQL always derives dynamically at read time (performance
// audit H4a): broadcast/large groups have derived at read time since P0-1 and are
// production-validated; now that ≤1000-member megagroups are unified onto the same path too,
// the send transaction no longer upserts channel_dialogs for all members, and the
// `channel_dialogs.unread_count` cache column is no longer consumed by any read path
// (the truth = read boundary + visible incoming messages recomputed).
func channelDialogVisibleUnreadCountSQL(readInboxExpr, topIDExpr string) string {
	return channelDialogDynamicUnreadCountSQL(readInboxExpr, topIDExpr)
}

func channelDialogHasUnreadSQL(readInboxExpr, topIDExpr string) string {
	return channelDialogDynamicUnreadExistsSQL(readInboxExpr, topIDExpr)
}

func (s *ChannelStore) SetChannelDialogUnreadMark(ctx context.Context, userID, channelID int64, unread bool) (bool, error) {
	if userID == 0 || channelID == 0 {
		return false, nil
	}
	var changed bool
	if err := s.db.QueryRow(ctx, `
WITH target AS (
    SELECT c.id AS channel_id, c.top_message_id, c.date AS top_message_date,
           m.read_inbox_max_id, m.read_outbox_max_id, m.available_min_id
    FROM channels c
    JOIN channel_members m ON m.channel_id = c.id
    WHERE c.id = $2 AND m.user_id = $1 AND m.status = 'active' AND NOT c.deleted
),
ensured AS (
    -- A lazily created row must carry the member's real watermarks and unread count:
    -- once a zero-valued cache row exists it would mask the truth (small groups read the
    -- trusted cache column).
    INSERT INTO channel_dialogs (user_id, channel_id, top_message_id, top_message_date,
                                 read_inbox_max_id, read_outbox_max_id, unread_count)
    SELECT $1, channel_id, top_message_id, top_message_date,
           read_inbox_max_id, read_outbox_max_id, (
        SELECT COUNT(*)::int
        FROM channel_messages cm
        WHERE cm.channel_id = target.channel_id
          AND cm.id > GREATEST(target.read_inbox_max_id, target.available_min_id)
          AND NOT cm.deleted
          AND cm.sender_user_id <> $1
    )
    FROM target
    ON CONFLICT (user_id, channel_id) DO NOTHING
),
updated_dialog AS (
    UPDATE channel_dialogs d
    SET unread_mark = $3, updated_at = now()
    WHERE d.user_id = $1 AND d.channel_id = $2
      AND EXISTS (SELECT 1 FROM target)
      AND d.unread_mark IS DISTINCT FROM $3::boolean
    RETURNING d.user_id
),
updated_member AS (
    UPDATE channel_members m
    SET unread_mark = $3
    WHERE m.user_id = $1 AND m.channel_id = $2 AND m.status = 'active'
    RETURNING m.user_id
)
SELECT EXISTS (SELECT 1 FROM updated_dialog)::boolean`, userID, channelID, unread).Scan(&changed); err != nil {
		return false, fmt.Errorf("set channel dialog unread mark: %w", err)
	}
	// After the pool write, synchronously invalidate this process's caches (both the
	// dialog's unread_mark and the member's unread_mark changed), guaranteeing same-instance
	// read-your-write; cross-instance is eventually covered asynchronously by dialog_light /
	// channel_member NOTIFY.
	if changed && s.dialogCacheActive(s.db) {
		s.dialogCache.delete(userID, channelID)
		s.memberCache.delete(channelID, userID)
	}
	return changed, nil
}

func (s *ChannelStore) ListChannelUnreadMarked(ctx context.Context, userID int64) ([]domain.Peer, error) {
	if userID == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT d.channel_id
FROM channel_dialogs d
JOIN channel_members m ON m.channel_id = d.channel_id AND m.user_id = d.user_id AND m.status = 'active'
JOIN channels c ON c.id = d.channel_id AND NOT c.deleted
WHERE d.user_id = $1 AND d.unread_mark
ORDER BY d.top_message_date DESC, d.top_message_id DESC, d.channel_id DESC
LIMIT 500`, userID)
	if err != nil {
		return nil, fmt.Errorf("list channel unread marks: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Peer, 0)
	for rows.Next() {
		var channelID int64
		if err := rows.Scan(&channelID); err != nil {
			return nil, err
		}
		out = append(out, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ChannelStore) ReadChannelMessageContents(ctx context.Context, req domain.ReadChannelMessageContentsRequest) (domain.ReadChannelMessageContentsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ReadChannelMessageContentsResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ReadChannelMessageContentsResult{}, fmt.Errorf("read channel message contents: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ReadChannelMessageContentsResult{}, fmt.Errorf("begin read channel message contents: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReadChannelMessageContentsResult{}, err
	}
	if len(req.IDs) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.ReadChannelMessageContentsResult{}, fmt.Errorf("commit read channel message contents: %w", err)
		}
		committed = true
		return domain.ReadChannelMessageContentsResult{Channel: channel}, nil
	}
	if len(req.IDs) > domain.MaxGetMessageIDs {
		return domain.ReadChannelMessageContentsResult{}, domain.ErrChannelInvalid
	}
	id32, _, err := validUniqueChannelMessageIDs(req.IDs)
	if err != nil {
		return domain.ReadChannelMessageContentsResult{}, err
	}
	args := []any{req.ChannelID, id32}
	where := "channel_id = $1 AND id = ANY($2::int[]) AND NOT deleted"
	if member.AvailableMinID > 0 {
		args = append(args, member.AvailableMinID)
		where += fmt.Sprintf(" AND id > $%d", len(args))
	}
	rows, err := tx.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where+`
ORDER BY id DESC`, args...)
	if err != nil {
		return domain.ReadChannelMessageContentsResult{}, fmt.Errorf("read channel messages by ids: %w", err)
	}
	messages := make([]domain.ChannelMessage, 0, len(id32))
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			rows.Close()
			return domain.ReadChannelMessageContentsResult{}, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ReadChannelMessageContentsResult{}, err
	}
	rows.Close()
	visibleIDs := make([]int32, 0, len(messages))
	for _, msg := range messages {
		visibleIDs = append(visibleIDs, int32(msg.ID))
	}
	cleared, err := clearChannelUnreadReactionsForMessageIDsTx(ctx, tx, req.UserID, req.ChannelID, visibleIDs)
	if err != nil {
		return domain.ReadChannelMessageContentsResult{}, err
	}
	clearedMentions, err := readChannelMentionsForMessageIDsTx(ctx, tx, req.UserID, req.ChannelID, visibleIDs)
	if err != nil {
		return domain.ReadChannelMessageContentsResult{}, err
	}
	if err := s.populateChannelMessageReplies(ctx, tx, req.UserID, channel, messages); err != nil {
		return domain.ReadChannelMessageContentsResult{}, err
	}
	if err := s.populateChannelMessagesReactions(ctx, tx, req.UserID, []domain.Channel{channel}, messages); err != nil {
		return domain.ReadChannelMessageContentsResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ReadChannelMessageContentsResult{}, fmt.Errorf("commit read channel message contents: %w", err)
	}
	committed = true
	return domain.ReadChannelMessageContentsResult{
		Channel:                         channel,
		Messages:                        messages,
		ClearedUnreadReactionMessageIDs: cleared,
		ClearedUnreadMentionMessageIDs:  clearedMentions,
	}, nil
}

// readChannelMentionsForMessageIDsTx flips unread mentions on the specified visible messages
// to read and recomputes the dialog count; the main path for readMessageContents (viewport
// contents read).
func readChannelMentionsForMessageIDsTx(ctx context.Context, tx pgx.Tx, userID, channelID int64, ids []int32) ([]int, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
WITH flipped AS (
    UPDATE channel_unread_mentions
    SET unread = false
    WHERE user_id = $1 AND channel_id = $2 AND message_id = ANY($3::int[]) AND unread
    RETURNING message_id
),
updated_dialog AS (
    UPDATE channel_dialogs d
    SET unread_mentions_count = (
        SELECT COUNT(*)::int
        FROM channel_unread_mentions um
        WHERE um.user_id = $1 AND um.channel_id = $2 AND um.unread
          AND NOT EXISTS (SELECT 1 FROM flipped f WHERE f.message_id = um.message_id)
    ),
    updated_at = now()
    WHERE d.user_id = $1 AND d.channel_id = $2
      AND EXISTS (SELECT 1 FROM flipped)
)
SELECT message_id FROM flipped ORDER BY message_id`, userID, channelID, ids)
	if err != nil {
		return nil, fmt.Errorf("read channel mentions by contents: %w", err)
	}
	defer rows.Close()
	out := make([]int, 0, len(ids))
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *ChannelStore) ListChannelUnreadMentions(ctx context.Context, viewerUserID int64, filter domain.ChannelUnreadMentionsFilter) (domain.ChannelHistory, error) {
	channel, member, err := s.getChannelForMember(ctx, s.db, viewerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > domain.MaxChannelUnreadMentionsLimit {
		limit = domain.MaxChannelUnreadMentionsLimit
	}
	filter.AddOffset = domain.ClampMessageHistoryAddOffset(filter.AddOffset)
	count, err := s.countChannelUnreadMentions(ctx, viewerUserID, filter, member.AvailableMinID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	messages, err := s.queryChannelUnreadMentionsPage(ctx, viewerUserID, filter, member.AvailableMinID, limit)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	if err := s.populateChannelMessageReplies(ctx, s.db, viewerUserID, channel, messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	if err := s.populateChannelMessagesReactions(ctx, s.db, viewerUserID, []domain.Channel{channel}, messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	return domain.ChannelHistory{Channel: channel, Messages: messages, Count: count}, nil
}

func (s *ChannelStore) ReadChannelMentions(ctx context.Context, req domain.ReadChannelMentionsRequest) (domain.ReadChannelMentionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ReadChannelMentionsResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ReadChannelMentionsResult{}, fmt.Errorf("read channel mentions: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ReadChannelMentionsResult{}, fmt.Errorf("begin read channel mentions: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, _, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReadChannelMentionsResult{}, err
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelReadMentionsBatch {
		limit = domain.MaxChannelReadMentionsBatch
	}
	cleared, remaining, err := readChannelMentionsTx(ctx, tx, req.UserID, req.ChannelID, req.TopMsgID, limit)
	if err != nil {
		return domain.ReadChannelMentionsResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ReadChannelMentionsResult{}, fmt.Errorf("commit read channel mentions: %w", err)
	}
	committed = true
	offset := 0
	if remaining > 0 {
		offset = 1
	}
	return domain.ReadChannelMentionsResult{
		Channel:    channel,
		Cleared:    cleared,
		Remaining:  remaining,
		Offset:     offset,
		ChannelPts: channel.Pts,
	}, nil
}

func (s *ChannelStore) ReadChannelHistory(ctx context.Context, req domain.ReadChannelHistoryRequest) (domain.ReadChannelHistoryResult, error) {
	var lastErr error
	for attempt := 0; attempt < retryableChannelTxAttempts; attempt++ {
		res, err := s.readChannelHistoryOnce(ctx, req)
		if err == nil || !isRetryablePostgresTxError(err) || ctx.Err() != nil {
			return res, err
		}
		lastErr = err
	}
	return domain.ReadChannelHistoryResult{}, lastErr
}

func advanceChannelReadOutboxTx(ctx context.Context, tx pgx.Tx, channelID, readerUserID int64, previous, maxID int) ([]domain.ChannelReadOutboxUpdate, error) {
	if maxID <= previous {
		return nil, nil
	}
	lowerID := previous
	if maxID-lowerID > domain.MaxChannelReadOutboxScanMessages {
		lowerID = maxID - domain.MaxChannelReadOutboxScanMessages
	}
	rows, err := tx.Query(ctx, `
WITH latest_sender_messages AS MATERIALIZED (
    SELECT sender_user_id, MAX(id) AS max_id
    FROM channel_messages
    WHERE channel_id = $1
      AND id > $2
      AND id <= $3
      AND NOT deleted
      AND sender_user_id <> $4
    GROUP BY sender_user_id
    ORDER BY max_id DESC
    LIMIT $5
), updated AS (
    UPDATE channel_members AS member
    SET read_outbox_max_id = GREATEST(member.read_outbox_max_id, latest.max_id),
        updated_at = now()
    FROM latest_sender_messages AS latest
    WHERE member.channel_id = $1
      AND member.user_id = latest.sender_user_id
      AND member.status = 'active'
      AND member.read_outbox_max_id < latest.max_id
    RETURNING member.user_id, member.read_outbox_max_id
)
SELECT user_id, read_outbox_max_id
FROM updated
ORDER BY user_id ASC`, channelID, lowerID, maxID, readerUserID, domain.MaxChannelReadOutboxFanout)
	if err != nil {
		return nil, fmt.Errorf("advance channel sender read outbox: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ChannelReadOutboxUpdate, 0, domain.MaxChannelReadOutboxFanout)
	for rows.Next() {
		var item domain.ChannelReadOutboxUpdate
		if err := rows.Scan(&item.UserID, &item.MaxID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ChannelStore) ListMessageReadParticipants(ctx context.Context, req domain.ChannelReadParticipantsRequest) (domain.ChannelReadParticipantsResult, error) {
	channel, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelReadParticipantsResult{}, err
	}
	if req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.ChannelReadParticipantsResult{}, domain.ErrMessageIDInvalid
	}
	msg, err := s.getChannelMessage(ctx, s.db, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelReadParticipantsResult{}, err
	}
	if msg.Deleted || msg.ID <= member.AvailableMinID {
		return domain.ChannelReadParticipantsResult{}, domain.ErrMessageIDInvalid
	}
	result := domain.ChannelReadParticipantsResult{Channel: channel, Message: msg}
	if !channel.Megagroup || channel.ParticipantsHidden || channel.ParticipantsCount > domain.MaxChannelReadParticipants {
		return result, nil
	}
	if req.Date > 0 && msg.Date+domain.ChannelReadMarkExpirePeriod <= req.Date {
		return result, nil
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelReadParticipants {
		limit = domain.MaxChannelReadParticipants
	}
	rows, err := s.db.Query(ctx, `
SELECT user_id, read_inbox_date
FROM channel_members
WHERE channel_id = $1
  AND status = 'active'
  AND user_id <> $2
  AND available_min_id < $3
  AND read_inbox_max_id >= $3
  AND read_inbox_date > 0
  AND NOT COALESCE((banned_rights->>'ViewMessages')::boolean, false)
ORDER BY read_inbox_date ASC, user_id ASC
LIMIT $4`, req.ChannelID, req.UserID, req.MessageID, limit)
	if err != nil {
		return domain.ChannelReadParticipantsResult{}, fmt.Errorf("list channel read participants: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.ChannelReadParticipant
		if err := rows.Scan(&item.UserID, &item.Date); err != nil {
			return domain.ChannelReadParticipantsResult{}, err
		}
		result.Participants = append(result.Participants, item)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelReadParticipantsResult{}, err
	}
	return result, nil
}

func populateChannelMessageUnreadFlags(ctx context.Context, db sqlcgen.DBTX, viewerUserID int64, messages []domain.ChannelMessage) error {
	if viewerUserID == 0 || len(messages) == 0 {
		return nil
	}
	indexes := make(map[channelReactionMessageKey][]int)
	idsByChannel := make(map[int64][]int32)
	for i := range messages {
		if messages[i].ChannelID == 0 || messages[i].ID <= 0 {
			continue
		}
		key := channelReactionMessageKey{channelID: messages[i].ChannelID, messageID: messages[i].ID}
		if _, ok := indexes[key]; !ok {
			idsByChannel[messages[i].ChannelID] = append(idsByChannel[messages[i].ChannelID], int32(messages[i].ID))
		}
		indexes[key] = append(indexes[key], i)
	}
	if len(idsByChannel) == 0 {
		return nil
	}
	// Batch-fetch across channels in one go, eliminating the "one SQL per channel" N+1
	// (amplified by multi-channel pages in getDialogs/getChannelDifference).
	pairChannels, pairMessages := channelMessagePairs(idsByChannel)
	{
		rows, err := db.Query(ctx, `
SELECT channel_id, message_id, unread
FROM channel_unread_mentions
WHERE user_id = $1
  AND (channel_id, message_id) IN (SELECT * FROM unnest($2::bigint[], $3::int[]))`, viewerUserID, pairChannels, pairMessages)
		if err != nil {
			return fmt.Errorf("load channel message unread flags: %w", err)
		}
		for rows.Next() {
			var channelID int64
			var messageID int
			var unread bool
			if err := rows.Scan(&channelID, &messageID, &unread); err != nil {
				rows.Close()
				return err
			}
			key := channelReactionMessageKey{channelID: channelID, messageID: messageID}
			for _, idx := range indexes[key] {
				// mentioned is retained permanently (official semantics); the client's
				// unread-mention determination requires mentioned and media_unread to be set
				// together, so media_unread follows the mention's unread state rather than
				// whether the message contains media.
				messages[idx].Mentioned = true
				messages[idx].MediaUnread = unread
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func (s *ChannelStore) channelReadWatermarks(ctx context.Context, channelID, userID int64) (int, int) {
	var inbox, outbox int
	_ = s.db.QueryRow(ctx, `SELECT read_inbox_max_id, read_outbox_max_id FROM channel_members WHERE channel_id = $1 AND user_id = $2`, channelID, userID).Scan(&inbox, &outbox)
	return inbox, outbox
}

func (s *ChannelStore) channelThreadUnreadCount(ctx context.Context, channelID int64, rootID int, viewerUserID int64, readMaxID int) int {
	var count int
	// The LIMIT subquery clamps the thread unread COUNT to the upper bound (P1-v): it bounds
	// the scan work and the delivered badge.
	_ = s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM (
    SELECT 1
    FROM channel_messages
    WHERE channel_id = $1 AND reply_to_top_id = $2 AND id > $3 AND sender_user_id <> $4 AND NOT deleted
    LIMIT $5
) thread_unread_capped`, channelID, rootID, readMaxID, viewerUserID, domain.MaxDialogUnreadCount).Scan(&count)
	return count
}

func countChannelUnreadMessages(ctx context.Context, db sqlcgen.DBTX, userID, channelID int64, readMaxID, topID int) (int, error) {
	if userID == 0 || channelID == 0 || topID <= readMaxID {
		return 0, nil
	}
	var count int
	// The LIMIT subquery clamps the unread COUNT to the upper bound (P1-v): this value is
	// written back to the channel_dialogs.unread_count cache; the clamp bounds both the scan
	// work and the cache column's upper bound, consistent with the dynamic/visible projection
	// semantics.
	if err := db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM (
    SELECT 1
    FROM channel_messages
    WHERE channel_id = $1
      AND id > $2
      AND id <= $3
      AND sender_user_id <> $4
      AND NOT deleted
    LIMIT $5
) unread_capped`, channelID, readMaxID, topID, userID, domain.MaxDialogUnreadCount).Scan(&count); err != nil {
		return 0, fmt.Errorf("count channel unread messages: %w", err)
	}
	return count, nil
}

func (s *ChannelStore) countChannelUnreadMentionsForTop(ctx context.Context, userID, channelID int64, topMsgID int) int {
	var count int
	_ = s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM channel_unread_mentions
WHERE user_id = $1 AND channel_id = $2 AND unread
  AND (top_message_id = $3 OR ($3 = 1 AND top_message_id = 0))`, userID, channelID, topMsgID).Scan(&count)
	return count
}

func (s *ChannelStore) countChannelUnreadMentions(ctx context.Context, userID int64, filter domain.ChannelUnreadMentionsFilter, availableMinID int) (int, error) {
	where, args := channelUnreadMentionBaseWhere(userID, filter, availableMinID)
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM channel_unread_mentions um
JOIN channel_messages cm ON cm.channel_id = um.channel_id AND cm.id = um.message_id
WHERE `+where, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count channel unread mentions: %w", err)
	}
	return count, nil
}

func (s *ChannelStore) queryChannelUnreadMentionsPage(ctx context.Context, userID int64, filter domain.ChannelUnreadMentionsFilter, availableMinID, limit int) ([]domain.ChannelMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	switch messageHistoryLoadType(filter.AddOffset, limit) {
	case messageHistoryLoadForward:
		return s.queryChannelUnreadMentionsForward(ctx, userID, filter, availableMinID, limit)
	case messageHistoryLoadAround:
		forwardLimit := -filter.AddOffset
		if forwardLimit > limit {
			forwardLimit = limit
		}
		backwardLimit := limit + filter.AddOffset
		if backwardLimit < 0 {
			backwardLimit = 0
		}
		forward, err := s.queryChannelUnreadMentionsForward(ctx, userID, filter, availableMinID, forwardLimit)
		if err != nil {
			return nil, err
		}
		backward, err := s.queryChannelUnreadMentionsBackward(ctx, userID, filter, availableMinID, backwardLimit, true)
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
		items, err := s.queryChannelUnreadMentionsBackward(ctx, userID, filter, availableMinID, limit+start, false)
		if err != nil || start >= len(items) {
			return nil, err
		}
		return items[start:], nil
	}
}

func (s *ChannelStore) queryChannelUnreadMentionsBackward(ctx context.Context, userID int64, filter domain.ChannelUnreadMentionsFilter, availableMinID, limit int, includeOffset bool) ([]domain.ChannelMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	where, args := channelUnreadMentionBaseWhere(userID, filter, availableMinID)
	where, args = appendChannelUnreadMentionBackwardOffset(where, args, filter, includeOffset)
	args = append(args, limit)
	return s.queryChannelUnreadMentions(ctx, filter.ChannelID, where, args, "DESC")
}

func (s *ChannelStore) queryChannelUnreadMentionsForward(ctx context.Context, userID int64, filter domain.ChannelUnreadMentionsFilter, availableMinID, limit int) ([]domain.ChannelMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	where, args := channelUnreadMentionBaseWhere(userID, filter, availableMinID)
	where, args = appendChannelUnreadMentionForwardOffset(where, args, filter)
	args = append(args, limit)
	out, err := s.queryChannelUnreadMentions(ctx, filter.ChannelID, where, args, "ASC")
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return channelMessageLess(out[i], out[j]) })
	return out, nil
}

func (s *ChannelStore) queryChannelUnreadMentions(ctx context.Context, channelID int64, where string, args []any, order string) ([]domain.ChannelMessage, error) {
	rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE channel_id = $2
  AND id = ANY(ARRAY(
      SELECT cm.id
      FROM channel_unread_mentions um
      JOIN channel_messages cm ON cm.channel_id = um.channel_id AND cm.id = um.message_id
      WHERE `+where+`
      ORDER BY cm.id `+order+`
      LIMIT $`+fmt.Sprint(len(args))+`
  )::int[])
ORDER BY id `+order, args...)
	_ = channelID
	if err != nil {
		return nil, fmt.Errorf("list channel unread mentions: %w", err)
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

func channelUnreadMentionBaseWhere(userID int64, filter domain.ChannelUnreadMentionsFilter, availableMinID int) (string, []any) {
	args := []any{userID, filter.ChannelID}
	where := "um.user_id = $1 AND um.channel_id = $2 AND um.unread AND NOT cm.deleted"
	if availableMinID > 0 {
		args = append(args, availableMinID)
		where += fmt.Sprintf(" AND cm.id > $%d", len(args))
	}
	if filter.TopMsgID > 0 {
		args = append(args, filter.TopMsgID)
		// Forum General (root=1) stores topic-less messages with top=0.
		where += fmt.Sprintf(" AND (um.top_message_id = $%d OR ($%d = 1 AND um.top_message_id = 0))", len(args), len(args))
	}
	if filter.MaxID > 0 {
		args = append(args, filter.MaxID)
		where += fmt.Sprintf(" AND cm.id < $%d", len(args))
	}
	if filter.MinID > 0 {
		args = append(args, filter.MinID)
		where += fmt.Sprintf(" AND cm.id > $%d", len(args))
	}
	return where, args
}

func appendChannelUnreadMentionBackwardOffset(where string, args []any, filter domain.ChannelUnreadMentionsFilter, include bool) (string, []any) {
	if filter.OffsetDate > 0 {
		args = append(args, filter.OffsetDate)
		if include {
			return where + fmt.Sprintf(" AND cm.message_date <= $%d", len(args)), args
		}
		return where + fmt.Sprintf(" AND cm.message_date < $%d", len(args)), args
	}
	if filter.OffsetID > 0 {
		args = append(args, filter.OffsetID)
		if include {
			return where + fmt.Sprintf(" AND cm.id <= $%d", len(args)), args
		}
		return where + fmt.Sprintf(" AND cm.id < $%d", len(args)), args
	}
	return where, args
}

func appendChannelUnreadMentionForwardOffset(where string, args []any, filter domain.ChannelUnreadMentionsFilter) (string, []any) {
	if filter.OffsetDate > 0 {
		args = append(args, filter.OffsetDate)
		return where + fmt.Sprintf(" AND cm.message_date >= $%d", len(args)), args
	}
	if filter.OffsetID > 0 {
		args = append(args, filter.OffsetID)
		return where + fmt.Sprintf(" AND cm.id > $%d", len(args)), args
	}
	return where, args
}

func insertChannelUnreadMentionsTx(ctx context.Context, tx pgx.Tx, channelID int64, msg domain.ChannelMessage, senderUserID int64, userIDs []int64) error {
	candidates := uniqueChannelUserIDs(userIDs, senderUserID)
	if len(candidates) == 0 || msg.ID == 0 {
		return nil
	}
	if len(candidates) > domain.MaxChannelMentionRecipients {
		candidates = candidates[:domain.MaxChannelMentionRecipients]
	}
	topID := channelMentionTopID(msg)
	mediaUnread := !msg.Media.IsZero()
	if _, err := tx.Exec(ctx, `
WITH input(user_id) AS (
    SELECT DISTINCT unnest($4::bigint[])
),
active AS (
    SELECT i.user_id
    FROM input i
    JOIN channel_members m ON m.channel_id = $1 AND m.user_id = i.user_id
    WHERE m.status = 'active'
      AND NOT COALESCE((m.banned_rights->>'ViewMessages')::boolean, false)
      AND $2 > m.available_min_id
      AND $2 > m.read_inbox_max_id
    LIMIT $6
),
inserted AS (
    INSERT INTO channel_unread_mentions (user_id, channel_id, message_id, top_message_id, media_unread)
    SELECT user_id, $1, $2, $3, $7
    FROM active
    ON CONFLICT DO NOTHING
    RETURNING user_id, channel_id, message_id, created_at
),
indexed AS (
    INSERT INTO channel_unread_mention_index (channel_id, message_id, user_id, created_at)
    SELECT channel_id, message_id, user_id, created_at
    FROM inserted
    ON CONFLICT DO NOTHING
)
INSERT INTO channel_dialogs (
    user_id, channel_id, top_message_id, top_message_date,
    read_inbox_max_id, read_outbox_max_id, unread_count, unread_mentions_count
)
SELECT i.user_id, $1, $2, $5,
       m.read_inbox_max_id, m.read_outbox_max_id,
       (
           SELECT COUNT(*)::int
           FROM channel_messages cm
           WHERE cm.channel_id = $1
             AND cm.id > GREATEST(m.read_inbox_max_id, m.available_min_id)
             AND NOT cm.deleted
             AND cm.sender_user_id <> i.user_id
       ),
       1
FROM inserted i
JOIN channel_members m ON m.channel_id = $1 AND m.user_id = i.user_id
ON CONFLICT (user_id, channel_id) DO UPDATE SET
    top_message_id = GREATEST(channel_dialogs.top_message_id, EXCLUDED.top_message_id),
    top_message_date = GREATEST(channel_dialogs.top_message_date, EXCLUDED.top_message_date),
    unread_mentions_count = channel_dialogs.unread_mentions_count + 1,
    updated_at = now()`, channelID, msg.ID, topID, candidates, msg.Date, domain.MaxChannelMentionRecipients, mediaUnread); err != nil {
		return fmt.Errorf("insert channel unread mentions: %w", err)
	}
	return nil
}

func readChannelMentionsTx(ctx context.Context, tx pgx.Tx, userID, channelID int64, topMsgID, limit int) (int, int, error) {
	var cleared, remaining int
	if err := tx.QueryRow(ctx, `
WITH target AS (
    SELECT user_id, channel_id, message_id
    FROM channel_unread_mentions
    WHERE user_id = $1
      AND channel_id = $2
      AND unread
      AND ($3 = 0 OR top_message_id = $3 OR ($3 = 1 AND top_message_id = 0))
    ORDER BY message_id DESC
    LIMIT $4
),
deleted AS (
    -- Read = flip the flag rather than delete the row: the mentioned highlight is retained
    -- permanently in history replay.
    UPDATE channel_unread_mentions um
    SET unread = false
    FROM target t
    WHERE um.user_id = t.user_id
      AND um.channel_id = t.channel_id
      AND um.message_id = t.message_id
    RETURNING um.user_id, um.channel_id, um.message_id
),
scoped_before AS (
    SELECT COUNT(*)::int AS count
    FROM channel_unread_mentions
    WHERE user_id = $1
      AND channel_id = $2
      AND unread
      AND ($3 = 0 OR top_message_id = $3 OR ($3 = 1 AND top_message_id = 0))
),
all_before AS (
    SELECT COUNT(*)::int AS count
    FROM channel_unread_mentions
    WHERE user_id = $1 AND channel_id = $2 AND unread
),
deleted_count AS (
    SELECT COUNT(*)::int AS count FROM deleted
),
updated_dialog AS (
    UPDATE channel_dialogs
    SET unread_mentions_count = GREATEST((SELECT count FROM all_before) - (SELECT count FROM deleted_count), 0),
        updated_at = now()
    WHERE user_id = $1 AND channel_id = $2
)
SELECT
    (SELECT count FROM deleted_count),
    GREATEST((SELECT count FROM scoped_before) - (SELECT count FROM deleted_count), 0)`, userID, channelID, topMsgID, limit).Scan(&cleared, &remaining); err != nil {
		return 0, 0, fmt.Errorf("read channel mentions: %w", err)
	}
	return cleared, remaining, nil
}

func deleteChannelUnreadMentionsTx(ctx context.Context, tx pgx.Tx, channelID int64, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	affected, err := channelUnreadMentionAffectedUsersTx(ctx, tx, channelID, ids)
	if err != nil {
		return err
	}
	for start := 0; start < len(affected); start += channelUnreadMentionDeleteUserBatch {
		end := start + channelUnreadMentionDeleteUserBatch
		if end > len(affected) {
			end = len(affected)
		}
		if err := deleteChannelUnreadMentionsForUsersTx(ctx, tx, channelID, ids, affected[start:end]); err != nil {
			return err
		}
	}
	return nil
}

const channelUnreadMentionDeleteUserBatch = 1000

func channelUnreadMentionAffectedUsersTx(ctx context.Context, tx pgx.Tx, channelID int64, ids []int) ([]int64, error) {
	rows, err := tx.Query(ctx, `
SELECT DISTINCT user_id
FROM channel_unread_mention_index
WHERE channel_id = $1
  AND message_id = ANY($2::int[])
ORDER BY user_id ASC`, channelID, int32s(ids))
	if err != nil {
		return nil, fmt.Errorf("list channel unread mention affected users: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan channel unread mention affected user: %w", err)
		}
		out = append(out, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read channel unread mention affected users: %w", err)
	}
	return out, nil
}

func deleteChannelUnreadMentionsForUsersTx(ctx context.Context, tx pgx.Tx, channelID int64, ids []int, userIDs []int64) error {
	if len(ids) == 0 || len(userIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
WITH affected AS (
    SELECT DISTINCT unnest($3::bigint[]) AS user_id
),
deleted_mentions AS (
    DELETE FROM channel_unread_mentions um
    USING affected a
    WHERE um.user_id = ANY($3::bigint[])
      AND um.user_id = a.user_id
      AND um.channel_id = $1
      AND um.message_id = ANY($2::int[])
    RETURNING um.user_id, um.channel_id, um.message_id, um.unread
),
deleted_index AS (
    DELETE FROM channel_unread_mention_index i
    USING affected a
    WHERE i.channel_id = $1
      AND i.user_id = a.user_id
      AND i.message_id = ANY($2::int[])
),
counts_before AS (
    SELECT user_id, COUNT(*)::int AS count
    FROM channel_unread_mentions
    WHERE channel_id = $1
      AND user_id = ANY($3::bigint[])
      AND unread
    GROUP BY user_id
),
deleted_counts AS (
    SELECT user_id, COUNT(*)::int AS count
    FROM deleted_mentions
    WHERE unread
    GROUP BY user_id
)
UPDATE channel_dialogs d
SET unread_mentions_count = GREATEST(COALESCE(c.count, 0) - COALESCE(dc.count, 0), 0),
    updated_at = now()
FROM affected a
LEFT JOIN counts_before c ON c.user_id = a.user_id
LEFT JOIN deleted_counts dc ON dc.user_id = a.user_id
WHERE d.user_id = ANY($3::bigint[])
  AND d.channel_id = $1
  AND d.user_id = a.user_id`, channelID, int32s(ids), int64s(userIDs)); err != nil {
		return fmt.Errorf("delete channel unread mentions: %w", err)
	}
	return nil
}

func deleteChannelUnreadMentionsUpToTx(ctx context.Context, tx pgx.Tx, userID, channelID int64, maxID int) error {
	if maxID <= 0 {
		return nil
	}
	var deleted int
	if err := tx.QueryRow(ctx, `
WITH deleted AS (
    DELETE FROM channel_unread_mentions
    WHERE user_id = $1 AND channel_id = $2 AND message_id <= $3
    RETURNING user_id, channel_id, message_id, unread
),
deleted_index AS (
    DELETE FROM channel_unread_mention_index i
    USING deleted d
    WHERE i.channel_id = d.channel_id
      AND i.user_id = d.user_id
      AND i.message_id = d.message_id
),
all_before AS (
    SELECT COUNT(*)::int AS count
    FROM channel_unread_mentions
    WHERE user_id = $1 AND channel_id = $2 AND unread
),
deleted_count AS (
    SELECT COUNT(*)::int AS count FROM deleted WHERE unread
),
updated_dialog AS (
    UPDATE channel_dialogs
    SET unread_mentions_count = GREATEST((SELECT count FROM all_before) - (SELECT count FROM deleted_count), 0),
        updated_at = now()
    WHERE user_id = $1 AND channel_id = $2
)
SELECT count FROM deleted_count`, userID, channelID, maxID).Scan(&deleted); err != nil {
		return fmt.Errorf("delete channel unread mentions up to: %w", err)
	}
	return nil
}

func channelMentionTopID(msg domain.ChannelMessage) int {
	if msg.ReplyTo == nil {
		return 0
	}
	if msg.ReplyTo.TopMessageID > 0 {
		return msg.ReplyTo.TopMessageID
	}
	return msg.ReplyTo.MessageID
}

// clearChannelMentionsForUserTx clears all of a member's mention state for the channel when
// they leave/are kicked, to avoid an @ badge pointing at pre-join messages after rejoining.
func clearChannelMentionsForUserTx(ctx context.Context, tx pgx.Tx, channelID, userID int64) error {
	if channelID == 0 || userID == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
WITH deleted AS (
    DELETE FROM channel_unread_mentions
    WHERE user_id = $2 AND channel_id = $1
    RETURNING message_id
),
deleted_index AS (
    DELETE FROM channel_unread_mention_index i
    USING deleted d
    WHERE i.channel_id = $1 AND i.user_id = $2 AND i.message_id = d.message_id
)
UPDATE channel_dialogs
SET unread_mentions_count = 0, updated_at = now()
WHERE user_id = $2 AND channel_id = $1`, channelID, userID); err != nil {
		return fmt.Errorf("clear channel mentions on leave: %w", err)
	}
	return nil
}
