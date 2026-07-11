package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func (s *ChannelStore) SendChannelMessage(ctx context.Context, req domain.SendChannelMessageRequest) (domain.SendChannelMessageResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || (strings.TrimSpace(req.Message) == "" && req.Action == nil && req.Media.IsZero() && req.RichMessage.IsZero()) {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	requestFingerprint, err := store.ChannelSendFingerprint(req)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	// Normalize the fallback to an explicit receipt so retries of the internal
	// transaction use exactly the same bytes as the first attempt.
	req.IdempotencyFingerprint = requestFingerprint
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	var lastErr error
	for attempt := 0; attempt < retryableChannelTxAttempts; attempt++ {
		res, err := s.sendChannelMessageOnce(ctx, req, requestFingerprint)
		if err == nil || !isRetryablePostgresTxError(err) || ctx.Err() != nil {
			return res, err
		}
		lastErr = err
	}
	return domain.SendChannelMessageResult{}, lastErr
}

func (s *ChannelStore) sendChannelMessageOnce(ctx context.Context, req domain.SendChannelMessageRequest, requestFingerprint []byte) (domain.SendChannelMessageResult, error) {
	if req.RandomID != 0 && !req.IdempotencyPreflighted {
		if dup, found, err := s.LookupChannelSendReplay(ctx, domain.ChannelSendReplayRequest{
			ChannelID:              req.ChannelID,
			SenderUserID:           req.UserID,
			RandomID:               req.RandomID,
			IdempotencyFingerprint: requestFingerprint,
		}); err != nil {
			return domain.SendChannelMessageResult{}, err
		} else if found {
			return dup, nil
		}
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.SendChannelMessageResult{}, fmt.Errorf("send channel message: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("begin send channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if errors.Is(err, domain.ErrChannelPrivate) {
		if candidate, candidateErr := s.channelByID(ctx, tx, req.ChannelID); candidateErr == nil {
			var guest bool
			member, guest, err = s.getLinkedDiscussionGuest(ctx, tx, req.UserID, candidate)
			if guest {
				channel = candidate
			}
		} else {
			err = candidateErr
		}
	}
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if member.Guest && channel.JoinToSend {
		return domain.SendChannelMessageResult{}, domain.ErrChannelWriteForbidden
	}
	fromBoostsApplied := 0
	if channel.Megagroup {
		fromBoostsApplied, err = countActiveUserBoostsForPeer(ctx, tx, req.UserID, domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}, req.Date)
		if err != nil {
			return domain.SendChannelMessageResult{}, err
		}
	}
	if domain.ChannelBannedRightsBlockMessage(req, channel, member, fromBoostsApplied) {
		return domain.SendChannelMessageResult{}, domain.ErrChannelWriteForbidden
	}
	if !canSendChannelMessageWithBoost(channel, member, fromBoostsApplied) {
		return domain.SendChannelMessageResult{}, domain.ErrChannelWriteForbidden
	}
	replyTo, err := s.resolveChannelReply(ctx, tx, req, member, channel, fromBoostsApplied)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if _, err := messageMetadataParamsFrom(req.Silent, req.NoForwards, replyTo, req.Forward); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if wait := channelSlowModeWait(channel, member, req.Date); wait > 0 {
		return domain.SendChannelMessageResult{}, domain.NewSlowModeWaitError(wait)
	}
	ttlPeriod := req.TTLPeriod
	if ttlPeriod == 0 && req.Action == nil {
		ttlPeriod = channel.TTLPeriod
	}
	expiresAt := 0
	if ttlPeriod > 0 && req.Action == nil {
		expiresAt = req.Date + ttlPeriod
	}
	var sendAs *domain.Peer
	if req.SendAs != nil {
		p := *req.SendAs
		sendAs = &p
	}
	msgID, err := s.msgIDs.NextChannelMessageID(ctx, req.ChannelID)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("allocate channel message id: %w", err)
	}
	pts, err := s.reserveChannelPts(ctx, tx, req.ChannelID)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("allocate channel pts: %w", err)
	}
	var discussion *domain.SendChannelDiscussionResult
	var discussionRef *domain.ChannelDiscussionRef
	if channel.Broadcast && channel.LinkedChatID != 0 {
		linked, err := getChannelByID(ctx, tx, channel.LinkedChatID)
		if err == nil && !linked.Deleted && linked.Megagroup {
			discussionMsgID, err := s.msgIDs.NextChannelMessageID(ctx, linked.ID)
			if err != nil {
				return domain.SendChannelMessageResult{}, fmt.Errorf("allocate discussion message id: %w", err)
			}
			discussionPts, err := s.reserveChannelPts(ctx, tx, linked.ID)
			if err != nil {
				return domain.SendChannelMessageResult{}, fmt.Errorf("allocate discussion pts: %w", err)
			}
			discussionRef = &domain.ChannelDiscussionRef{ChannelID: linked.ID, MessageID: discussionMsgID}
			discussionMsg := domain.ChannelMessage{
				ChannelID:    linked.ID,
				ID:           discussionMsgID,
				SenderUserID: req.UserID,
				From:         domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID},
				Date:         req.Date,
				Silent:       req.Silent,
				NoForwards:   req.NoForwards || channel.NoForwards || linked.NoForwards,
				Body:         req.Message,
				Entities:     append([]domain.MessageEntity(nil), req.Entities...),
				Media:        req.Media,
				RichMessage:  req.RichMessage,
				ViaBotID:     req.ViaBotID,
				GroupedID:    req.GroupedID,
				ReplyMarkup:  req.ReplyMarkup,
				TTLPeriod:    ttlPeriod,
				ExpiresAt:    expiresAt,
				Forward:      &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}, Date: req.Date, ChannelPost: msgID, SavedFrom: domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}, SavedFromMsgID: msgID},
				Pts:          discussionPts,
			}
			discussionEvent := domain.ChannelUpdateEvent{
				ChannelID: linked.ID,
				Type:      domain.ChannelUpdateNewMessage,
				Pts:       discussionPts,
				PtsCount:  1,
				Date:      req.Date,
				Message:   discussionMsg,
			}
			if err := insertChannelMessageTx(ctx, tx, discussionMsg); err != nil {
				return domain.SendChannelMessageResult{}, err
			}
			if err := insertChannelEventTx(ctx, tx, discussionEvent); err != nil {
				return domain.SendChannelMessageResult{}, err
			}
			if err := insertChannelUnreadMentionsTx(ctx, tx, linked.ID, discussionMsg, req.UserID, req.MentionUserIDs); err != nil {
				return domain.SendChannelMessageResult{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE channels SET top_message_id = $2, pts = $3, updated_at = now() WHERE id = $1`, linked.ID, discussionMsgID, discussionPts); err != nil {
				return domain.SendChannelMessageResult{}, fmt.Errorf("update discussion channel top: %w", err)
			}
			linked.TopMessageID = discussionMsgID
			linked.Pts = discussionPts
			if err := upsertChannelDialogsForMessageTx(ctx, tx, linked, discussionMsg, 0); err != nil {
				return domain.SendChannelMessageResult{}, err
			}
			discussion = &domain.SendChannelDiscussionResult{
				Channel:        linked,
				Message:        discussionMsg,
				Event:          discussionEvent,
				MentionUserIDs: append([]int64(nil), req.MentionUserIDs...),
			}
		} else if err != nil && !errors.Is(err, domain.ErrChannelInvalid) {
			return domain.SendChannelMessageResult{}, err
		}
	}
	msg := domain.ChannelMessage{
		ChannelID:         req.ChannelID,
		ID:                msgID,
		RandomID:          req.RandomID,
		SenderUserID:      req.UserID,
		From:              domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID},
		Date:              req.Date,
		Post:              channel.Broadcast,
		PostAuthor:        channelPostAuthor(channel, req.PostAuthor),
		Silent:            req.Silent,
		NoForwards:        req.NoForwards || channel.NoForwards,
		Body:              req.Message,
		Entities:          append([]domain.MessageEntity(nil), req.Entities...),
		Media:             req.Media,
		RichMessage:       req.RichMessage,
		ViaBotID:          req.ViaBotID,
		GroupedID:         req.GroupedID,
		ReplyMarkup:       req.ReplyMarkup,
		TTLPeriod:         ttlPeriod,
		ExpiresAt:         expiresAt,
		ReplyTo:           replyTo,
		Forward:           cloneMessageForward(req.Forward),
		SendAs:            sendAs,
		Discussion:        discussionRef,
		Action:            cloneChannelMessageAction(req.Action),
		FromBoostsApplied: fromBoostsApplied,
		Pts:               pts,
	}
	if discussionRef != nil {
		msg.Replies = &domain.ChannelMessageReplies{Comments: true, ChannelID: discussionRef.ChannelID, RepliesPts: discussion.Event.Pts}
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		Message:      msg,
		SenderUserID: req.UserID,
	}
	if err := insertChannelMessageWithFingerprintTx(ctx, tx, msg, requestFingerprint); err != nil {
		if isUniqueViolation(err) {
			if req.RandomID == 0 {
				return domain.SendChannelMessageResult{}, err
			}
			// A failed statement leaves the transaction aborted while its pool
			// connection remains checked out. Release it before the winner lookup;
			// otherwise a one-connection pool deadlocks waiting on itself.
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				return domain.SendChannelMessageResult{}, fmt.Errorf("rollback channel random_id conflict: %w", rollbackErr)
			}
			committed = true // transaction is finalized by rollback; suppress deferred rollback
			dup, found, dupErr := s.LookupChannelSendReplay(ctx, domain.ChannelSendReplayRequest{
				ChannelID:              req.ChannelID,
				SenderUserID:           req.UserID,
				RandomID:               req.RandomID,
				IdempotencyFingerprint: requestFingerprint,
			})
			if dupErr != nil {
				return domain.SendChannelMessageResult{}, dupErr
			}
			if !found {
				return domain.SendChannelMessageResult{}, fmt.Errorf("channel random_id unique conflict without replay receipt")
			}
			return dup, nil
		}
		return domain.SendChannelMessageResult{}, err
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	mentionTargets := req.MentionUserIDs
	// 回复某人 = 隐式 mention 该消息作者（官方语义：被回复者收到 @ 角标
	// 且通知穿透群静音）。
	if replyTo != nil && replyTo.MessageID > 0 {
		if target, err := s.getChannelMessage(ctx, tx, req.ChannelID, replyTo.MessageID); err == nil &&
			target.SenderUserID != 0 && target.SenderUserID != req.UserID {
			mentionTargets = append(append([]int64(nil), mentionTargets...), target.SenderUserID)
		}
	}
	if channel.Broadcast && !channel.Megagroup {
		// broadcast 没有 @ 角标/readMentions UI，写入只会造成永远清不掉的
		// 提及角标；讨论组联动消息走 megagroup 路径。
		mentionTargets = nil
	}
	if err := insertChannelUnreadMentionsTx(ctx, tx, req.ChannelID, msg, req.UserID, mentionTargets); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if err := updateForumTopicTopMessageTx(ctx, tx, req.ChannelID, msg); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET top_message_id = $2, pts = $3, updated_at = now() WHERE id = $1`, req.ChannelID, msgID, pts); err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("update channel top: %w", err)
	}
	channel.TopMessageID = msgID
	channel.Pts = pts
	if _, err := tx.Exec(ctx, `
UPDATE channel_members
SET slowmode_last_send_date = $3,
    read_inbox_max_id = GREATEST(read_inbox_max_id, $4),
    unread_mark = false,
    updated_at = now()
WHERE channel_id = $1 AND user_id = $2`, req.ChannelID, req.UserID, req.Date, msgID); err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("update channel member slowmode send date: %w", err)
	}
	// channel_dialogs.unread_mark 非 NULL 时会在读取路径遮蔽 members 的值
	// （COALESCE(d.unread_mark, m.unread_mark)），发送清除必须双表同步。
	if _, err := tx.Exec(ctx, `
UPDATE channel_dialogs
SET unread_mark = false, updated_at = now()
WHERE channel_id = $1 AND user_id = $2 AND unread_mark`, req.ChannelID, req.UserID); err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("clear channel dialog unread mark on send: %w", err)
	}
	if err := upsertChannelDialogsForMessageTx(ctx, tx, channel, msg, req.UserID, req.SkipDeliveryUserIDs); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if channel.Broadcast {
		if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
			ChannelID: req.ChannelID,
			UserID:    req.UserID,
			Date:      req.Date,
			Type:      domain.ChannelAdminLogSendMessage,
			Message:   &msg,
			Query:     msg.Body,
		}); err != nil {
			return domain.SendChannelMessageResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("commit send channel: %w", err)
	}
	committed = true
	var recipients []int64
	if !req.SkipRecipientLookup {
		recipients, _ = s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
		recipients = filterSkippedChannelRecipients(recipients, channelDeliverySkipSet(req.SkipDeliveryUserIDs))
		if discussion != nil {
			discussion.Recipients, _ = s.ListActiveChannelMemberIDs(ctx, req.UserID, discussion.Channel.ID, 0)
		}
	}
	return domain.SendChannelMessageResult{Channel: channel, Message: msg, Event: event, Recipients: recipients, Discussion: discussion, MentionUserIDs: append([]int64(nil), req.MentionUserIDs...), SkipDeliveryUserIDs: append([]int64(nil), req.SkipDeliveryUserIDs...)}, nil
}

func channelDeliverySkipSet(ids []int64) map[int64]struct{} {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id != 0 {
			out[id] = struct{}{}
		}
	}
	return out
}

func filterSkippedChannelRecipients(recipients []int64, skip map[int64]struct{}) []int64 {
	if len(recipients) == 0 || len(skip) == 0 {
		return recipients
	}
	out := recipients[:0]
	for _, id := range recipients {
		if _, hidden := skip[id]; hidden {
			continue
		}
		out = append(out, id)
	}
	return out
}

// LookupChannelSendReplay reads an immutable random_id receipt without running
// membership, permission, slow-mode, source/media resolution or allocation. A
// zero SavedPeer selects an ordinary channel receipt; monoforum sub-dialogs are
// scoped by the complete saved peer.
func (s *ChannelStore) LookupChannelSendReplay(ctx context.Context, lookup domain.ChannelSendReplayRequest) (domain.SendChannelMessageResult, bool, error) {
	if lookup.ChannelID == 0 || lookup.SenderUserID == 0 || lookup.RandomID == 0 {
		return domain.SendChannelMessageResult{}, false, fmt.Errorf("channel send replay: invalid scope")
	}
	if err := store.ValidateSendFingerprint(lookup.IdempotencyFingerprint, "channel send replay"); err != nil {
		return domain.SendChannelMessageResult{}, false, err
	}
	var row pgx.Row
	if lookup.SavedPeer.ID == 0 {
		if lookup.SavedPeer.Type != "" {
			return domain.SendChannelMessageResult{}, false, fmt.Errorf("channel send replay: incomplete saved peer scope")
		}
		row = s.db.QueryRow(ctx, `SELECT `+channelMessageColumns+` FROM channel_messages
WHERE channel_id = $1 AND sender_user_id = $2 AND saved_peer_type = '' AND saved_peer_id = 0 AND random_id = $3`,
			lookup.ChannelID, lookup.SenderUserID, lookup.RandomID)
	} else {
		if lookup.SavedPeer.Type != domain.PeerTypeUser {
			return domain.SendChannelMessageResult{}, false, fmt.Errorf("channel send replay: invalid saved peer scope")
		}
		row = s.db.QueryRow(ctx, `SELECT `+channelMessageColumns+` FROM channel_messages
WHERE channel_id = $1 AND sender_user_id = $2 AND saved_peer_type = $3 AND saved_peer_id = $4 AND random_id = $5`,
			lookup.ChannelID, lookup.SenderUserID, string(lookup.SavedPeer.Type), lookup.SavedPeer.ID, lookup.RandomID)
	}
	msg, err := scanChannelMessage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SendChannelMessageResult{}, false, nil
	}
	if err != nil {
		return domain.SendChannelMessageResult{}, false, err
	}
	result, err := s.channelDuplicateReplayResult(ctx, msg, lookup.IdempotencyFingerprint)
	if err != nil {
		return domain.SendChannelMessageResult{}, false, err
	}
	result.Duplicate = true
	return result, true, nil
}

func (s *ChannelStore) channelDuplicateReplayResult(ctx context.Context, msg domain.ChannelMessage, expectedFingerprint []byte) (domain.SendChannelMessageResult, error) {
	var storedFingerprint []byte
	var snapshotJSON, deleteIDsJSON string
	var deletePts, deletePtsCount, deleteDate int
	if err := s.db.QueryRow(ctx, `
SELECT request_fingerprint, send_snapshot::text, delete_pts, delete_pts_count, delete_date, delete_message_ids::text
FROM channel_messages
WHERE channel_id = $1 AND id = $2`, msg.ChannelID, msg.ID).Scan(
		&storedFingerprint, &snapshotJSON, &deletePts, &deletePtsCount, &deleteDate, &deleteIDsJSON,
	); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if !store.SameSendFingerprint(storedFingerprint, expectedFingerprint) {
		return domain.SendChannelMessageResult{}, domain.ErrMessageRandomIDDuplicate
	}
	first, err := store.DecodeChannelSendSnapshot([]byte(snapshotJSON))
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("decode duplicate channel message %d snapshot: %w", msg.ID, err)
	}
	if first.ChannelID != msg.ChannelID || first.ID != msg.ID || first.SenderUserID != msg.SenderUserID || first.RandomID != msg.RandomID || first.SavedPeer != msg.SavedPeer {
		return domain.SendChannelMessageResult{}, fmt.Errorf("duplicate channel message %d snapshot disagrees with random_id receipt", msg.ID)
	}
	channel, err := getChannelByID(ctx, s.db, msg.ChannelID)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	replay := msg
	var replayDelete *domain.ChannelUpdateEvent
	if msg.Deleted {
		replay = first
		messageIDs, err := decodeEventMessageIDs(deleteIDsJSON)
		if err != nil {
			return domain.SendChannelMessageResult{}, fmt.Errorf("decode duplicate channel message %d delete ids: %w", msg.ID, err)
		}
		if deletePts <= 0 || deletePtsCount <= 0 || len(messageIDs) == 0 {
			return domain.SendChannelMessageResult{}, fmt.Errorf("duplicate channel message %d is deleted without a durable delete receipt", msg.ID)
		}
		deleteEvent := domain.ChannelUpdateEvent{
			ChannelID:  msg.ChannelID,
			Type:       domain.ChannelUpdateDeleteMessages,
			Pts:        deletePts,
			PtsCount:   deletePtsCount,
			Date:       deleteDate,
			MessageIDs: messageIDs,
		}
		replayDelete = &deleteEvent
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    msg.ChannelID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          first.Pts,
		PtsCount:     1,
		Date:         first.Date,
		Message:      replay,
		SenderUserID: first.SenderUserID,
	}
	return domain.SendChannelMessageResult{Channel: channel, Message: replay, Event: event, Duplicate: true, ReplayDeleteEvent: replayDelete}, nil
}

func (s *ChannelStore) insertServiceMessage(ctx context.Context, tx pgx.Tx, channel domain.Channel, senderUserID int64, date int, action domain.ChannelMessageAction) (domain.ChannelMessage, domain.ChannelUpdateEvent, error) {
	msgID, err := s.msgIDs.NextChannelMessageID(ctx, channel.ID)
	if err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, fmt.Errorf("allocate channel service message id: %w", err)
	}
	action = channelServiceActionForMessage(channel.ID, msgID, action)
	pts, err := s.reserveChannelPts(ctx, tx, channel.ID)
	if err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, fmt.Errorf("allocate channel service pts: %w", err)
	}
	msg := domain.ChannelMessage{
		ChannelID:    channel.ID,
		ID:           msgID,
		SenderUserID: senderUserID,
		From:         domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID},
		Date:         date,
		Post:         channel.Broadcast,
		Action:       &action,
		Pts:          pts,
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    channel.ID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         date,
		Message:      msg,
		SenderUserID: senderUserID,
		UserIDs:      append([]int64(nil), action.UserIDs...),
	}
	if err := insertChannelMessageTx(ctx, tx, msg); err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET top_message_id = $2, pts = $3, updated_at = now() WHERE id = $1`, channel.ID, msgID, pts); err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, fmt.Errorf("update channel service top: %w", err)
	}
	return msg, event, nil
}

func channelServiceActionForMessage(channelID int64, msgID int, action domain.ChannelMessageAction) domain.ChannelMessageAction {
	if action.Type == domain.ChannelActionStarGift && action.StarGift != nil {
		g := *action.StarGift
		if g.PeerChannelID == 0 {
			g.PeerChannelID = channelID
		}
		if g.SavedID == 0 {
			g.SavedID = int64(msgID)
		}
		action.StarGift = &g
	}
	return action
}

func insertChannelMessageTx(ctx context.Context, tx pgx.Tx, msg domain.ChannelMessage) error {
	return insertChannelMessageWithFingerprintTx(ctx, tx, msg, nil)
}

// insertChannelMessageWithFingerprintTx is the only first-send write boundary
// for client-random-id channel messages. Callers that create service/discussion
// rows without random_id use insertChannelMessageTx and persist the legacy-safe
// empty default instead.
func insertChannelMessageWithFingerprintTx(ctx context.Context, tx pgx.Tx, msg domain.ChannelMessage, requestFingerprint []byte) error {
	if msg.RandomID != 0 {
		if err := store.ValidateSendFingerprint(requestFingerprint, "insert channel message"); err != nil {
			return err
		}
	} else if len(requestFingerprint) != 0 {
		if err := store.ValidateSendFingerprint(requestFingerprint, "insert channel message"); err != nil {
			return err
		}
	}
	if requestFingerprint == nil {
		requestFingerprint = []byte{}
	}
	entities, err := encodeMessageEntities(msg.Entities)
	if err != nil {
		return err
	}
	reply, err := marshalJSON(msg.ReplyTo, "{}")
	if err != nil {
		return err
	}
	forward, err := marshalJSON(msg.Forward, "{}")
	if err != nil {
		return err
	}
	action, err := marshalJSON(msg.Action, "{}")
	if err != nil {
		return err
	}
	media, err := encodeMessageMedia(msg.Media)
	if err != nil {
		return err
	}
	replyMarkup, err := encodeReplyMarkup(msg.ReplyMarkup)
	if err != nil {
		return err
	}
	richMessage, err := encodeRichMessage(msg.RichMessage)
	if err != nil {
		return err
	}
	sendSnapshot := []byte("{}")
	if msg.RandomID != 0 {
		sendSnapshot, err = store.EncodeChannelSendSnapshot(msg)
		if err != nil {
			return err
		}
	}
	var sendAsType sql.NullString
	var sendAsID sql.NullInt64
	if msg.SendAs != nil && msg.SendAs.ID != 0 {
		sendAsType = sql.NullString{String: string(msg.SendAs.Type), Valid: true}
		sendAsID = sql.NullInt64{Int64: msg.SendAs.ID, Valid: true}
	}
	if msg.From.Type == "" {
		msg.From = domain.Peer{Type: domain.PeerTypeUser, ID: msg.SenderUserID}
	}
	replyMsgID, replyTopID := 0, 0
	replyPeerType := ""
	replyPeerID := int64(0)
	if msg.ReplyTo != nil {
		replyMsgID = msg.ReplyTo.MessageID
		replyTopID = msg.ReplyTo.TopMessageID
		replyPeerType = string(msg.ReplyTo.Peer.Type)
		replyPeerID = msg.ReplyTo.Peer.ID
	}
	discussionChannelID, discussionMessageID := int64(0), 0
	if msg.Discussion != nil {
		discussionChannelID = msg.Discussion.ChannelID
		discussionMessageID = msg.Discussion.MessageID
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO channel_messages (
    channel_id, id, random_id, sender_user_id, from_peer_type, from_peer_id,
    send_as_peer_type, send_as_peer_id, message_date, edit_date, post, silent, noforwards,
    body, entities, reply_to, reply_to_msg_id, reply_to_peer_type, reply_to_peer_id, reply_to_top_id,
    fwd_from, discussion_channel_id, discussion_message_id, action, pts, deleted, media, reply_markup, rich_message, ttl_period, expires_at, post_author, via_bot_id, from_boosts_applied, grouped_id, saved_peer_type, saved_peer_id, send_snapshot, request_fingerprint
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38::jsonb,$39::bytea)`,
		msg.ChannelID, msg.ID, msg.RandomID, msg.SenderUserID, string(msg.From.Type), msg.From.ID,
		sendAsType, sendAsID, msg.Date, msg.EditDate, msg.Post, msg.Silent, msg.NoForwards,
		msg.Body, entities, reply, replyMsgID, replyPeerType, replyPeerID, replyTopID,
		forward, discussionChannelID, discussionMessageID, action, msg.Pts, msg.Deleted, media, replyMarkup, richMessage, msg.TTLPeriod, msg.ExpiresAt, msg.PostAuthor, msg.ViaBotID, msg.FromBoostsApplied, msg.GroupedID, string(msg.SavedPeer.Type), msg.SavedPeer.ID, sendSnapshot, requestFingerprint); err != nil {
		return fmt.Errorf("insert channel message: %w", err)
	}
	// 共享媒体索引(迁移 0118):创建即按媒体类别建索引行,供 messages.search 媒体标签页。
	if err := insertChannelMediaIndexTx(ctx, tx, msg.ChannelID, msg.ID, msg.Date, msg.Media, msg.Entities); err != nil {
		return err
	}
	return nil
}

// channelPostAuthor 仅在 signatures 开启的 broadcast post 上保留作者签名。
func channelPostAuthor(channel domain.Channel, author string) string {
	if !channel.Broadcast || !channel.Signatures {
		return ""
	}
	return author
}

func canSendChannelMessage(channel domain.Channel, member domain.ChannelMember) bool {
	return canSendChannelMessageWithBoost(channel, member, 0)
}

func canSendChannelMessageWithBoost(channel domain.Channel, member domain.ChannelMember, selfBoostsApplied int) bool {
	if channel.Broadcast {
		return canPostChannel(member)
	}
	if member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin {
		return true
	}
	if member.BannedRights.SendMessages {
		return false
	}
	if !channel.DefaultBannedRights.SendMessages {
		return true
	}
	return channel.BoostsUnrestrict > 0 && selfBoostsApplied >= channel.BoostsUnrestrict
}
