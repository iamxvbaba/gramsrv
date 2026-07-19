package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) ListChannelDifference(ctx context.Context, req domain.ChannelDifferenceRequest) (domain.ChannelDifference, error) {
	channel, member, preview, err := s.getChannelForViewer(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelDifference{}, err
	}
	if req.Pts < 0 || req.Pts > channel.Pts {
		return domain.ChannelDifference{}, domain.ErrPersistentTimestamp
	}
	if !preview && member.AvailableMinPts > req.Pts {
		req.Pts = minInt(member.AvailableMinPts, channel.Pts)
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelDifferenceLimit {
		limit = domain.MaxChannelDifferenceLimit
	}
	if preview && member.Status != domain.ChannelMemberActive {
		return domain.ChannelDifference{
			Channel: channel,
			Self:    member,
			Pts:     channel.Pts,
			Final:   true,
			Timeout: 30,
			Dialog:  previewChannelDialog(req.UserID, channel, member),
		}, nil
	}
	checkpoint, err := getChannelUpdateCheckpoint(ctx, s.db, req.ChannelID)
	if err != nil {
		return domain.ChannelDifference{}, err
	}
	if req.Pts < checkpoint.RetainedThroughPts || channel.Pts-req.Pts > limit {
		args := []any{req.ChannelID}
		where := "channel_id = $1 AND NOT deleted"
		if member.AvailableMinID > 0 {
			args = append(args, member.AvailableMinID)
			where += fmt.Sprintf(" AND id > $%d", len(args))
		}
		if channel.Monoforum && !isChannelAdmin(member) {
			args = append(args, req.UserID)
			where += fmt.Sprintf(" AND saved_peer_type = 'user' AND saved_peer_id = $%d", len(args))
		}
		args = append(args, domain.MaxChannelDifferenceTooLongMessages)
		rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where+`
ORDER BY id DESC
LIMIT $`+fmt.Sprint(len(args)), args...)
		if err != nil {
			return domain.ChannelDifference{}, fmt.Errorf("list channel too long messages: %w", err)
		}
		defer rows.Close()
		diff := domain.ChannelDifference{
			Channel: channel,
			Self:    member,
			Pts:     channel.Pts,
			Final:   true,
			TooLong: true,
			Timeout: 30,
		}
		for rows.Next() {
			msg, err := scanChannelMessage(rows)
			if err != nil {
				return domain.ChannelDifference{}, err
			}
			diff.NewMessages = append(diff.NewMessages, msg)
		}
		if err := rows.Err(); err != nil {
			return domain.ChannelDifference{}, err
		}
		if err := populateChannelMessageUnreadFlags(ctx, s.db, req.UserID, diff.NewMessages); err != nil {
			return domain.ChannelDifference{}, err
		}
		if preview {
			diff.Dialog = previewChannelDialog(req.UserID, channel, member)
		} else {
			dialog, err := s.getChannelDialog(ctx, s.db, req.UserID, channel)
			if err != nil {
				return domain.ChannelDifference{}, err
			}
			diff.Dialog = dialog
		}
		return diff, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id, pts, pts_count, date, event_type, message_id, message_ids::text, sender_user_id, user_ids::text, payload::text
FROM channel_update_events
WHERE channel_id = $1 AND pts > $2
ORDER BY pts ASC
LIMIT $3`, req.ChannelID, req.Pts, limit)
	if err != nil {
		return domain.ChannelDifference{}, fmt.Errorf("list channel difference: %w", err)
	}
	diff := domain.ChannelDifference{Channel: channel, Self: member, Pts: channel.Pts, Final: true, Timeout: 30}
	userRefs := make(map[int64]struct{})
	channelRefs := make(map[int64]struct{})
	lastPts := req.Pts
	type differenceEventRow struct {
		event     domain.ChannelUpdateEvent
		messageID int
	}
	eventRows := make([]differenceEventRow, 0, limit)
	for rows.Next() {
		event, messageID, err := scanChannelEvent(rows)
		if err != nil {
			return domain.ChannelDifference{}, err
		}
		ptsCount := event.PtsCount
		if ptsCount <= 0 {
			ptsCount = 1
		}
		if event.Pts != lastPts+ptsCount {
			s.log.Warn("channel_difference_stopped_at_gap",
				zap.String("scope", "channel"),
				zap.Int64("user_id", req.UserID),
				zap.Int64("channel_id", req.ChannelID),
				zap.Int("request_pts", req.Pts),
				zap.Int("channel_pts", channel.Pts),
				zap.Int("returned_pts", lastPts),
				zap.Int("expected_pts", lastPts+ptsCount),
				zap.Int("got_pts", event.Pts),
				zap.Int("got_pts_count", ptsCount),
				zap.String("event_type", string(event.Type)),
				zap.Int("limit", limit),
			)
			break
		}
		lastPts = event.Pts
		eventRows = append(eventRows, differenceEventRow{event: event, messageID: messageID})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ChannelDifference{}, err
	}
	rows.Close()
	var visibleMonoforumMessageIDs map[int]struct{}
	if channel.Monoforum && !isChannelAdmin(member) {
		messageIDs := make([]int, 0)
		for _, row := range eventRows {
			messageIDs = append(messageIDs, row.event.MessageIDs...)
		}
		visibleMonoforumMessageIDs, err = s.monoforumVisibleMessageIDs(ctx, req.ChannelID, req.UserID, messageIDs)
		if err != nil {
			return domain.ChannelDifference{}, err
		}
	}
	for _, row := range eventRows {
		event := row.event
		messageID := row.messageID
		if messageID != 0 && event.Message.ID == 0 {
			msg, err := s.getChannelMessage(ctx, s.db, req.ChannelID, messageID)
			if err != nil {
				return domain.ChannelDifference{}, err
			}
			event.Message = msg
		}
		visibleEvent, ok := domain.FilterChannelUpdateEventForAvailableMinID(event, member.AvailableMinID)
		if !ok {
			continue
		}
		event = visibleEvent
		if channel.Monoforum && !isChannelAdmin(member) {
			event, ok = filterMonoforumEventForUser(event, req.UserID, visibleMonoforumMessageIDs)
			if !ok {
				continue
			}
		}
		if preview && event.Type == domain.ChannelUpdateParticipant {
			continue
		}
		collectChannelEventRefs(event, req.ChannelID, userRefs, channelRefs)
		diff.Events = append(diff.Events, event)
		diff.Pts = event.Pts
		switch event.Type {
		case domain.ChannelUpdateNewMessage:
			diff.NewMessages = append(diff.NewMessages, event.Message)
		default:
			diff.OtherUpdates = append(diff.OtherUpdates, event)
		}
	}
	if len(diff.Events) == 0 {
		diff.Pts = lastPts
	} else if lastPts > diff.Pts {
		diff.Pts = lastPts
	}
	if err := populateChannelMessageUnreadFlags(ctx, s.db, req.UserID, diff.NewMessages); err != nil {
		return domain.ChannelDifference{}, err
	}
	// OtherUpdates 里带消息的事件未读/提及标记一次批量回填（原来逐事件一条 SQL 的 N+1）。
	otherMsgs := make([]domain.ChannelMessage, 0, len(diff.OtherUpdates))
	otherIdx := make([]int, 0, len(diff.OtherUpdates))
	for i := range diff.OtherUpdates {
		if diff.OtherUpdates[i].Message.ID == 0 {
			continue
		}
		otherMsgs = append(otherMsgs, diff.OtherUpdates[i].Message)
		otherIdx = append(otherIdx, i)
	}
	if len(otherMsgs) > 0 {
		if err := populateChannelMessageUnreadFlags(ctx, s.db, req.UserID, otherMsgs); err != nil {
			return domain.ChannelDifference{}, err
		}
		for j, i := range otherIdx {
			diff.OtherUpdates[i].Message = otherMsgs[j]
		}
	}
	users, err := listUsersByIDs(ctx, s.db, mapKeysInt64(userRefs))
	if err != nil {
		return domain.ChannelDifference{}, err
	}
	channels, err := listChannelsByIDs(ctx, s.db, mapKeysInt64(channelRefs))
	if err != nil {
		return domain.ChannelDifference{}, err
	}
	diff.Users = users
	diff.Channels = channels
	if preview {
		diff.Dialog = previewChannelDialog(req.UserID, channel, member)
	} else {
		dialog, err := s.getChannelDialog(ctx, s.db, req.UserID, channel)
		if err != nil {
			return domain.ChannelDifference{}, err
		}
		diff.Dialog = dialog
	}
	diff.Final = lastPts >= channel.Pts
	return diff, nil
}

func (s *ChannelStore) monoforumVisibleMessageIDs(ctx context.Context, channelID, userID int64, ids []int) (map[int]struct{}, error) {
	visible := make(map[int]struct{})
	if len(ids) == 0 {
		return visible, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT id
FROM channel_messages
WHERE channel_id = $1
  AND id = ANY($2::int[])
  AND saved_peer_type = 'user'
  AND saved_peer_id = $3`, channelID, int32s(ids), userID)
	if err != nil {
		return nil, fmt.Errorf("list visible monoforum message ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		visible[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return visible, nil
}

func filterMonoforumEventForUser(event domain.ChannelUpdateEvent, userID int64, visibleMessageIDs map[int]struct{}) (domain.ChannelUpdateEvent, bool) {
	if event.Message.ID != 0 {
		return event, event.Message.SavedPeer == (domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	}
	if len(event.MessageIDs) == 0 {
		return event, false
	}
	ids := make([]int, 0, len(event.MessageIDs))
	for _, id := range event.MessageIDs {
		if _, ok := visibleMessageIDs[id]; ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return event, false
	}
	event.MessageIDs = ids
	return event, true
}

func (s *ChannelStore) MaxChannelPts(ctx context.Context, channelID int64) (int, error) {
	var pts int
	err := s.db.QueryRow(ctx, `SELECT pts FROM channels WHERE id = $1`, channelID).Scan(&pts)
	return pts, err
}

func (s *ChannelStore) MaxChannelPtsBatch(ctx context.Context, channelIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(channelIDs))
	if len(channelIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id, pts FROM channels WHERE id = ANY($1::bigint[])`, channelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var channelID int64
		var pts int
		if err := rows.Scan(&channelID, &pts); err != nil {
			return nil, err
		}
		out[channelID] = pts
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func transientChannelParticipantEvent(channelID, actorUserID int64, previous, participant domain.ChannelMember, date int) domain.ChannelUpdateEvent {
	return domain.ChannelUpdateEvent{
		ChannelID:    channelID,
		Type:         domain.ChannelUpdateParticipant,
		Date:         date,
		SenderUserID: actorUserID,
		UserIDs:      uniqueNonZeroInt64s(actorUserID, previous.UserID, previous.InviterUserID, participant.UserID, participant.InviterUserID),
		Previous:     previous,
		Participant:  participant,
	}
}

func (s *ChannelStore) reserveChannelPts(ctx context.Context, db sqlcgen.DBTX, channelID int64) (int, error) {
	return s.reserveChannelPtsN(ctx, db, channelID, 1)
}

func (s *ChannelStore) reserveChannelPtsN(ctx context.Context, db sqlcgen.DBTX, channelID int64, count int) (int, error) {
	count = normalizePtsCount(count)
	caller := traceCaller(2)
	pts, err := reserveChannelPts(ctx, db, channelID, count)
	if err != nil {
		s.log.Warn("pts_reserve_failed",
			zap.String("scope", "channel"),
			zap.Int64("channel_id", channelID),
			zap.Int("pts_count", count),
			zap.String("caller", traceCaller(2)),
			zap.Error(err),
			zap.Error(ctx.Err()),
		)
		return 0, err
	}
	s.log.Debug("pts_reserve",
		zap.String("scope", "channel"),
		zap.Int64("channel_id", channelID),
		zap.Int("pts", pts),
		zap.Int("pts_count", maxInt(count, 1)),
		zap.String("caller", caller),
	)
	return pts, nil
}

func reserveChannelPts(ctx context.Context, db sqlcgen.DBTX, channelID int64, count int) (int, error) {
	count = normalizePtsCount(count)
	if channelID == 0 {
		return 0, fmt.Errorf("channel pts: missing channel id")
	}
	var pts int
	if err := db.QueryRow(ctx, `
UPDATE channels
SET pts = pts + $2,
    updated_at = now()
WHERE id = $1
RETURNING pts`, channelID, count).Scan(&pts); err != nil {
		return 0, fmt.Errorf("reserve channel pts: %w", err)
	}
	return pts, nil
}

func insertChannelEventTx(ctx context.Context, tx pgx.Tx, event domain.ChannelUpdateEvent) error {
	ids, err := marshalJSON(event.MessageIDs, "[]")
	if err != nil {
		return err
	}
	userIDs, err := marshalJSON(event.UserIDs, "[]")
	if err != nil {
		return err
	}
	payloadData := map[string]any{
		"message_id": event.Message.ID,
		"pinned":     event.Pinned,
	}
	if event.Message.ID != 0 {
		payloadData["message"] = event.Message
	}
	if event.Previous.UserID != 0 {
		payloadData["previous_participant"] = event.Previous
	}
	if event.Participant.UserID != 0 {
		payloadData["participant"] = event.Participant
	}
	payload, err := marshalJSON(payloadData, "{}")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO channel_update_events (
    channel_id, pts, pts_count, date, event_type, message_id, message_ids, sender_user_id, user_ids, payload
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		event.ChannelID, event.Pts, event.PtsCount, event.Date, string(event.Type), event.Message.ID,
		ids, event.SenderUserID, userIDs, payload); err != nil {
		return fmt.Errorf("insert channel event: %w", err)
	}
	// The checkpoint is updated in the same business transaction as the event row. Retention may
	// later remove the row, but account-level dirty-channel recovery still has the latest date/pts.
	if _, err := tx.Exec(ctx, `
INSERT INTO channel_update_checkpoints (
    channel_id, retained_through_pts, latest_event_date, latest_pts
) VALUES ($1, 0, $2, $3)
ON CONFLICT (channel_id) DO UPDATE SET
    latest_event_date = GREATEST(channel_update_checkpoints.latest_event_date, EXCLUDED.latest_event_date),
    latest_pts = GREATEST(channel_update_checkpoints.latest_pts, EXCLUDED.latest_pts),
    updated_at = now()`, event.ChannelID, event.Date, event.Pts); err != nil {
		return fmt.Errorf("upsert channel update checkpoint: %w", err)
	}
	return nil
}

func scanChannelEvent(row rowScanner) (domain.ChannelUpdateEvent, int, error) {
	var event domain.ChannelUpdateEvent
	var typ string
	var messageID int
	var messageIDs, userIDs, payload string
	if err := row.Scan(
		&event.ChannelID, &event.Pts, &event.PtsCount, &event.Date, &typ, &messageID,
		&messageIDs, &event.SenderUserID, &userIDs, &payload,
	); err != nil {
		return domain.ChannelUpdateEvent{}, 0, err
	}
	event.Type = domain.ChannelUpdateEventType(typ)
	_ = json.Unmarshal([]byte(messageIDs), &event.MessageIDs)
	_ = json.Unmarshal([]byte(userIDs), &event.UserIDs)
	var data struct {
		Pinned              bool                  `json:"pinned"`
		Message             domain.ChannelMessage `json:"message"`
		PreviousParticipant domain.ChannelMember  `json:"previous_participant"`
		Participant         domain.ChannelMember  `json:"participant"`
	}
	_ = json.Unmarshal([]byte(payload), &data)
	event.Pinned = data.Pinned
	if data.Message.ID != 0 {
		event.Message = data.Message
	}
	event.Previous = data.PreviousParticipant
	event.Participant = data.Participant
	return event, messageID, nil
}

func channelInitialAvailableMinPts(channel domain.Channel) int {
	return channel.Pts
}

func adminLogEventTypesForFilter(filter domain.ChannelAdminLogFilter) []string {
	if filter.Empty() {
		return nil
	}
	types := make([]string, 0, 16)
	add := func(enabled bool, typ domain.ChannelAdminLogEventType) {
		if enabled {
			types = append(types, string(typ))
		}
	}
	add(filter.Join, domain.ChannelAdminLogParticipantJoin)
	add(filter.Leave, domain.ChannelAdminLogParticipantLeave)
	add(filter.Invite || filter.Invites, domain.ChannelAdminLogParticipantInvite)
	add(filter.Ban, domain.ChannelAdminLogParticipantBan)
	add(filter.Unban, domain.ChannelAdminLogParticipantUnban)
	add(filter.Kick, domain.ChannelAdminLogParticipantKick)
	add(filter.Unkick, domain.ChannelAdminLogParticipantUnkick)
	add(filter.Promote, domain.ChannelAdminLogParticipantPromote)
	add(filter.Demote, domain.ChannelAdminLogParticipantDemote)
	add(filter.EditRank, domain.ChannelAdminLogParticipantEditRank)
	if filter.Info {
		types = append(types,
			string(domain.ChannelAdminLogChangeTitle),
			string(domain.ChannelAdminLogChangeUsername),
			string(domain.ChannelAdminLogChangeLinkedChat),
			string(domain.ChannelAdminLogToggleSlowMode),
		)
	}
	if filter.Settings {
		types = append(types,
			string(domain.ChannelAdminLogToggleSignatures),
			string(domain.ChannelAdminLogTogglePreHistoryHidden),
			string(domain.ChannelAdminLogToggleAntiSpam),
			string(domain.ChannelAdminLogToggleAutotranslation),
		)
	}
	add(filter.Forums || filter.Settings, domain.ChannelAdminLogToggleForum)
	add(filter.Pinned, domain.ChannelAdminLogUpdatePinned)
	add(filter.Edit, domain.ChannelAdminLogEditMessage)
	add(filter.Delete, domain.ChannelAdminLogDeleteMessage)
	add(filter.Send, domain.ChannelAdminLogSendMessage)
	return types
}

func collectChannelEventRefs(event domain.ChannelUpdateEvent, currentChannelID int64, userRefs, channelRefs map[int64]struct{}) {
	if event.SenderUserID != 0 {
		userRefs[event.SenderUserID] = struct{}{}
	}
	for _, id := range event.UserIDs {
		if id != 0 {
			userRefs[id] = struct{}{}
		}
	}
	for _, member := range []domain.ChannelMember{event.Previous, event.Participant} {
		if member.UserID != 0 {
			userRefs[member.UserID] = struct{}{}
		}
		if member.InviterUserID != 0 {
			userRefs[member.InviterUserID] = struct{}{}
		}
	}
	collectChannelMessageRefs(event.Message, currentChannelID, userRefs, channelRefs)
}
