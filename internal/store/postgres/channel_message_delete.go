package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) DeleteChannelMessages(ctx context.Context, req domain.DeleteChannelMessagesRequest) (domain.DeleteChannelMessagesResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || len(req.IDs) == 0 {
		return domain.DeleteChannelMessagesResult{}, domain.ErrChannelInvalid
	}
	if len(req.IDs) > domain.MaxDeleteMessageIDs {
		return domain.DeleteChannelMessagesResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DeleteChannelMessagesResult{}, fmt.Errorf("delete channel messages: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, fmt.Errorf("begin delete channel messages: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, err
	}
	refs, err := s.discussionRefsForMessages(ctx, tx, channel.ID, req.IDs)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, err
	}
	deleted, event, channel, err := s.deleteChannelMessagesTx(ctx, tx, channel, member, req.IDs, req.UserID, req.Date)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, err
	}
	cascades, err := s.cascadeDeleteDiscussionRootsTx(ctx, tx, refs, deleted, req.UserID, req.Date)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DeleteChannelMessagesResult{}, fmt.Errorf("commit delete channel messages: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	for i := range cascades {
		cascades[i].Recipients, _ = s.ListActiveChannelMemberIDs(ctx, 0, cascades[i].Channel.ID, 0)
	}
	return domain.DeleteChannelMessagesResult{Channel: channel, Event: event, DeletedIDs: deleted, Recipients: recipients, DiscussionDeletes: cascades}, nil
}

func (s *ChannelStore) ModerationDeleteChannelMessages(ctx context.Context, channelID int64, ids []int, date int) (domain.DeleteChannelMessagesResult, error) {
	if channelID <= 0 || len(ids) == 0 || len(ids) > domain.MaxDeleteMessageIDs {
		return domain.DeleteChannelMessagesResult{}, domain.ErrChannelInvalid
	}
	if date == 0 {
		date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DeleteChannelMessagesResult{}, fmt.Errorf("moderation delete channel messages: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, fmt.Errorf("begin moderation delete channel messages: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, err := getChannelByID(ctx, tx, channelID)
	if err != nil || channel.Deleted {
		if err != nil {
			return domain.DeleteChannelMessagesResult{}, err
		}
		return domain.DeleteChannelMessagesResult{}, domain.ErrChannelInvalid
	}
	refs, err := s.discussionRefsForMessages(ctx, tx, channel.ID, ids)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, err
	}
	systemMember := domain.ChannelMember{
		ChannelID: channel.ID, UserID: domain.OfficialSystemUserID,
		Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive,
	}
	deleted, event, channel, err := s.deleteChannelMessagesTx(
		ctx, tx, channel, systemMember, ids, domain.OfficialSystemUserID, date,
	)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, err
	}
	cascades, err := s.cascadeDeleteDiscussionRootsTx(
		ctx, tx, refs, deleted, domain.OfficialSystemUserID, date,
	)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DeleteChannelMessagesResult{}, fmt.Errorf("commit moderation delete channel messages: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, 0, channel.ID, 0)
	for i := range cascades {
		cascades[i].Recipients, _ = s.ListActiveChannelMemberIDs(ctx, 0, cascades[i].Channel.ID, 0)
	}
	return domain.DeleteChannelMessagesResult{
		Channel: channel, Event: event, DeletedIDs: deleted,
		Recipients: recipients, DiscussionDeletes: cascades,
	}, nil
}

// discussionRefsForMessages fetches the discussion-group forward-root references carried by the messages to delete.
func (s *ChannelStore) discussionRefsForMessages(ctx context.Context, tx pgx.Tx, channelID int64, ids []int) (map[int]domain.ChannelDiscussionRef, error) {
	id32, _, err := validUniqueChannelMessageIDs(ids)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
SELECT id, discussion_channel_id, discussion_message_id
FROM channel_messages
WHERE channel_id = $1 AND id = ANY($2::int[]) AND NOT deleted
  AND discussion_channel_id <> 0 AND discussion_message_id <> 0`, channelID, id32)
	if err != nil {
		return nil, fmt.Errorf("list discussion refs for delete: %w", err)
	}
	defer rows.Close()
	out := make(map[int]domain.ChannelDiscussionRef)
	for rows.Next() {
		var id, msgID int
		var discussionChannelID int64
		if err := rows.Scan(&id, &discussionChannelID, &msgID); err != nil {
			return nil, err
		}
		out[id] = domain.ChannelDiscussionRef{ChannelID: discussionChannelID, MessageID: msgID}
	}
	return out, rows.Err()
}

// cascadeDeleteDiscussionRootsTx deletes a broadcast post's forward root in its
// linked discussion group (server-side cascade, as official); the lock order is fixed as post channel → discussion channel,
// and deleting the root on the discussion side does not cascade back, so there is no crossing deadlock path.
func (s *ChannelStore) cascadeDeleteDiscussionRootsTx(ctx context.Context, tx pgx.Tx, refs map[int]domain.ChannelDiscussionRef, deleted []int, actorUserID int64, date int) ([]domain.ChannelCascadeDelete, error) {
	if len(refs) == 0 || len(deleted) == 0 {
		return nil, nil
	}
	byChannel := make(map[int64][]int)
	for _, id := range deleted {
		ref, ok := refs[id]
		if !ok || ref.ChannelID == 0 || ref.MessageID == 0 {
			continue
		}
		byChannel[ref.ChannelID] = append(byChannel[ref.ChannelID], ref.MessageID)
	}
	if len(byChannel) == 0 {
		return nil, nil
	}
	channelIDs := make([]int64, 0, len(byChannel))
	for id := range byChannel {
		channelIDs = append(channelIDs, id)
	}
	sort.Slice(channelIDs, func(i, j int) bool { return channelIDs[i] < channelIDs[j] })
	out := make([]domain.ChannelCascadeDelete, 0, len(channelIDs))
	for _, discussionChannelID := range channelIDs {
		group, err := getChannelByID(ctx, tx, discussionChannelID)
		if err != nil || group.Deleted {
			continue
		}
		// The cascade is a server-side action executed with creator rights (a channel admin is not necessarily a discussion group member).
		systemMember := domain.ChannelMember{ChannelID: discussionChannelID, UserID: actorUserID, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive}
		groupDeleted, groupEvent, group, err := s.deleteChannelMessagesTx(ctx, tx, group, systemMember, byChannel[discussionChannelID], actorUserID, date)
		if err != nil {
			return nil, fmt.Errorf("cascade delete discussion roots: %w", err)
		}
		if len(groupDeleted) == 0 {
			continue
		}
		out = append(out, domain.ChannelCascadeDelete{Channel: group, Event: groupEvent})
	}
	return out, nil
}

func (s *ChannelStore) DeleteChannelHistory(ctx context.Context, req domain.DeleteChannelHistoryRequest) (domain.DeleteChannelHistoryResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("delete channel history: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("begin delete channel history: %w", err)
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
	maxID := req.MaxID
	if maxID <= 0 || maxID > channel.TopMessageID {
		maxID = channel.TopMessageID
	}
	if !req.ForEveryone {
		appliedMinID := maxInt(member.AvailableMinID, maxID)
		changed := appliedMinID > member.AvailableMinID
		anchorID := member.HistoryClearAnchorID
		anchorDate := member.HistoryClearAnchorDate
		if changed {
			anchorID = appliedMinID
			anchorDate = req.Date
			var messageDate int
			err := tx.QueryRow(ctx, `
SELECT message_date
FROM channel_messages
WHERE channel_id = $1 AND id = $2`, req.ChannelID, appliedMinID).Scan(&messageDate)
			switch {
			case err == nil && messageDate > 0:
				anchorDate = messageDate
			case errors.Is(err, pgx.ErrNoRows):
				// max_id may name an already-pruned/hole id. The owner-local
				// marker is still a valid monotonic boundary; retain request
				// time so its dialog projection remains loadable.
			case err != nil:
				return domain.DeleteChannelHistoryResult{}, fmt.Errorf("select channel clear anchor date: %w", err)
			}
			if anchorDate <= 0 {
				anchorDate = channel.Date
			}
		}
		topID, topDate, err := visibleChannelTopAfter(ctx, tx, req.ChannelID, appliedMinID, channel.Date)
		if err != nil {
			return domain.DeleteChannelHistoryResult{}, err
		}
		if topID == 0 && anchorID > 0 && anchorID == appliedMinID {
			topID = anchorID
			topDate = anchorDate
		}
		if _, err := tx.Exec(ctx, `
UPDATE channel_members
SET available_min_id = GREATEST(available_min_id, $3),
    history_clear_anchor_id = CASE WHEN available_min_id < $3 THEN $3 ELSE history_clear_anchor_id END,
    history_clear_anchor_date = CASE WHEN available_min_id < $3 THEN $4 ELSE history_clear_anchor_date END,
    read_inbox_max_id = GREATEST(read_inbox_max_id, $3),
    unread_mark = false,
    updated_at = now()
WHERE channel_id = $1 AND user_id = $2`, req.ChannelID, req.UserID, appliedMinID, anchorDate); err != nil {
			return domain.DeleteChannelHistoryResult{}, fmt.Errorf("update channel local clear member: %w", err)
		}
		if changed {
			member.AvailableMinID = appliedMinID
			member.HistoryClearAnchorID = anchorID
			member.HistoryClearAnchorDate = anchorDate
			if err := upsertUserChannelMemberIndexTx(ctx, tx, channel, member); err != nil {
				return domain.DeleteChannelHistoryResult{}, err
			}
			if _, err := tx.Exec(ctx, `
UPDATE user_channel_member_index
SET available_min_id = $3,
    history_clear_anchor_id = $3,
    history_clear_updated_at = $4,
    updated_at = now()
WHERE user_id = $1 AND channel_id = $2`,
				req.UserID, req.ChannelID, appliedMinID, req.Date); err != nil {
				return domain.DeleteChannelHistoryResult{}, fmt.Errorf("update channel local clear recovery index: %w", err)
			}
		}
		if err := deleteChannelUnreadMentionsUpToTx(ctx, tx, req.UserID, req.ChannelID, appliedMinID); err != nil {
			return domain.DeleteChannelHistoryResult{}, err
		}
		if err := refreshChannelUnreadReactionsCountTx(ctx, tx, req.UserID, req.ChannelID); err != nil {
			return domain.DeleteChannelHistoryResult{}, err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO channel_dialogs (
    user_id, channel_id, top_message_id, top_message_date, read_inbox_max_id, read_outbox_max_id, unread_count, unread_mark
) VALUES ($1,$2,$3,$4,$5,0,0,false)
ON CONFLICT (user_id, channel_id) DO UPDATE SET
    top_message_id = EXCLUDED.top_message_id,
    top_message_date = EXCLUDED.top_message_date,
    read_inbox_max_id = GREATEST(channel_dialogs.read_inbox_max_id, EXCLUDED.read_inbox_max_id),
    unread_count = 0,
    unread_mark = false,
    updated_at = now()`, req.UserID, req.ChannelID, topID, topDate, appliedMinID); err != nil {
			return domain.DeleteChannelHistoryResult{}, fmt.Errorf("upsert channel local clear dialog: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.DeleteChannelHistoryResult{}, fmt.Errorf("commit local clear channel history: %w", err)
		}
		committed = true
		if s.memberCacheActive(s.db) {
			s.memberCache.delete(req.ChannelID, req.UserID)
		}
		if s.dialogCacheActive(s.db) {
			s.dialogCache.delete(req.UserID, req.ChannelID)
		}
		return domain.DeleteChannelHistoryResult{
			Channel:             channel,
			AvailableMinID:      appliedMinID,
			AvailableMinChanged: changed,
		}, nil
	}
	if !canDeleteAnyChannelMessage(member) {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelAdminRequired
	}
	// id=1 is the group-creation service message and must be kept on a full clear: it is the only
	// top message left in the dialog after clearing, and without it the client would treat lastMessage as empty and
	// hide the dialog from the chat list (membership remains, but the dialog entry disappears for everyone).
	rows, err := tx.Query(ctx, `
SELECT id
FROM channel_messages
WHERE channel_id = $1 AND id <= $2 AND id > 1 AND NOT deleted
ORDER BY id DESC
LIMIT $3`, req.ChannelID, maxID, domain.MaxDeleteHistoryBatch)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("list channel history delete ids: %w", err)
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
	if err := tx.Commit(ctx); err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("commit delete channel history: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	offset := 0
	if len(deleted) == domain.MaxDeleteHistoryBatch {
		offset = 1
	}
	return domain.DeleteChannelHistoryResult{Channel: channel, Event: event, DeletedIDs: deleted, Recipients: recipients, Offset: offset}, nil
}

func (s *ChannelStore) DeleteChannelParticipantHistory(ctx context.Context, req domain.DeleteChannelParticipantHistoryRequest) (domain.DeleteChannelHistoryResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.ParticipantUserID == 0 {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("delete participant channel history: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("begin delete participant channel history: %w", err)
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
	if !canDeleteAnyChannelMessage(member) {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelAdminRequired
	}
	// Same as a full clear: the id=1 group-creation service message is not deleted with the sender's (creator's) history,
	// otherwise the dialog would be hidden from everyone's chat list because lastMessage is empty.
	rows, err := tx.Query(ctx, `
SELECT id
FROM channel_messages
WHERE channel_id = $1 AND sender_user_id = $2 AND id > 1 AND NOT deleted
ORDER BY id DESC
LIMIT $3`, req.ChannelID, req.ParticipantUserID, domain.MaxDeleteHistoryBatch)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("list participant channel history delete ids: %w", err)
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
	if err := tx.Commit(ctx); err != nil {
		return domain.DeleteChannelHistoryResult{}, fmt.Errorf("commit delete participant channel history: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	offset := 0
	if len(deleted) == domain.MaxDeleteHistoryBatch {
		offset = 1
	}
	return domain.DeleteChannelHistoryResult{Channel: channel, Event: event, DeletedIDs: deleted, Recipients: recipients, Offset: offset}, nil
}

func (s *ChannelStore) deleteChannelMessagesTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, member domain.ChannelMember, ids []int, actorUserID int64, date int) ([]int, domain.ChannelUpdateEvent, domain.Channel, error) {
	if len(ids) == 0 {
		return nil, domain.ChannelUpdateEvent{}, channel, nil
	}
	id32, ordered, err := validUniqueChannelMessageIDs(ids)
	if err != nil {
		return nil, domain.ChannelUpdateEvent{}, channel, err
	}
	rows, err := tx.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE channel_id = $1 AND id = ANY($2::int[]) AND NOT deleted
ORDER BY id`, channel.ID, id32)
	if err != nil {
		return nil, domain.ChannelUpdateEvent{}, channel, fmt.Errorf("list channel messages for delete: %w", err)
	}
	byID := make(map[int]domain.ChannelMessage, len(ordered))
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			rows.Close()
			return nil, domain.ChannelUpdateEvent{}, channel, err
		}
		byID[msg.ID] = msg
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, domain.ChannelUpdateEvent{}, channel, err
	}
	rows.Close()
	deleted := make([]int, 0, len(ordered))
	for _, id := range ordered {
		msg, ok := byID[id]
		if !ok {
			continue
		}
		if msg.SenderUserID != actorUserID && !canDeleteAnyChannelMessage(member) {
			return nil, domain.ChannelUpdateEvent{}, channel, domain.ErrChannelAdminRequired
		}
		if id <= 1 {
			// The id=1 group-creation service message is the fallback top message left in the dialog after clearing, so every
			// delete entry point silently skips it (official clients disable deletion for it).
			continue
		}
		deleted = append(deleted, id)
		if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
			ChannelID: channel.ID,
			UserID:    actorUserID,
			Date:      date,
			Type:      domain.ChannelAdminLogDeleteMessage,
			Message:   &msg,
			Query:     msg.Body,
		}); err != nil {
			return nil, domain.ChannelUpdateEvent{}, channel, err
		}
	}
	if len(deleted) == 0 {
		return nil, domain.ChannelUpdateEvent{}, channel, nil
	}
	pts, err := s.reserveChannelPtsN(ctx, tx, channel.ID, len(deleted))
	if err != nil {
		return nil, domain.ChannelUpdateEvent{}, channel, fmt.Errorf("allocate channel delete pts: %w", err)
	}
	deleted32 := int32s(deleted)
	if _, err := tx.Exec(ctx, `
UPDATE channel_messages
SET deleted = true,
    pts = $3,
    delete_pts = $3,
    delete_pts_count = $4,
    delete_date = $5,
    delete_message_ids = to_jsonb($2::int[]),
    updated_at = now()
WHERE channel_id = $1 AND id = ANY($2::int[])`, channel.ID, deleted32, pts, len(deleted), date); err != nil {
		return nil, domain.ChannelUpdateEvent{}, channel, fmt.Errorf("soft delete channel messages: %w", err)
	}
	if err := deleteChannelUnreadMentionsTx(ctx, tx, channel.ID, deleted); err != nil {
		return nil, domain.ChannelUpdateEvent{}, channel, err
	}
	if err := refreshChannelUnreadReactionsCountsForMessagesTx(ctx, tx, channel.ID, deleted); err != nil {
		return nil, domain.ChannelUpdateEvent{}, channel, err
	}
	topID, err := topNonDeletedChannelMessageID(ctx, tx, channel.ID)
	if err != nil {
		return nil, domain.ChannelUpdateEvent{}, channel, err
	}
	var latestPinned int
	if err := tx.QueryRow(ctx, `
UPDATE channels
SET top_message_id = $2, pts = $3,
    -- Deletion removes the message from the pinned set (pinned queries filtering NOT deleted are automatically immune),
    -- so recompute the "latest pinned id" cache here in sync to avoid a dangling value.
    pinned_message_id = COALESCE((
        SELECT MAX(id) FROM channel_messages
        WHERE channel_id = $1 AND pinned AND NOT deleted
    ), 0),
    updated_at = now()
WHERE id = $1
RETURNING pinned_message_id`, channel.ID, topID, pts).Scan(&latestPinned); err != nil {
		return nil, domain.ChannelUpdateEvent{}, channel, fmt.Errorf("update channel top after delete: %w", err)
	}
	channel.TopMessageID = topID
	channel.Pts = pts
	channel.PinnedMessageID = latestPinned
	// After deletion, no longer refresh channel_dialogs cache rows for all members (performance audit H4a): unread is derived
	// dynamically at read time from visible incoming messages (deleted ones drop out automatically), and top is provided by channels.top_message_id.
	event := domain.ChannelUpdateEvent{
		ChannelID:    channel.ID,
		Type:         domain.ChannelUpdateDeleteMessages,
		Pts:          pts,
		PtsCount:     len(deleted),
		Date:         date,
		MessageIDs:   append([]int(nil), deleted...),
		SenderUserID: actorUserID,
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return nil, domain.ChannelUpdateEvent{}, channel, err
	}
	return deleted, event, channel, nil
}

func validUniqueChannelMessageIDs(ids []int) ([]int32, []int, error) {
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return nil, nil, domain.ErrMessageIDInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return int32s(out), out, nil
}

func topNonDeletedChannelMessageID(ctx context.Context, db sqlcgen.DBTX, channelID int64) (int, error) {
	var id int
	if err := db.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM channel_messages WHERE channel_id = $1 AND NOT deleted`, channelID).Scan(&id); err != nil {
		return 0, fmt.Errorf("select channel top after delete: %w", err)
	}
	return id, nil
}

func canDeleteAnyChannelMessage(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.DeleteMessages)
}
