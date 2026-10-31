package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *ChannelStore) EditChannelMessage(ctx context.Context, req domain.EditChannelMessageRequest) (domain.EditChannelMessageResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.ID <= 0 {
		return domain.EditChannelMessageResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.EditChannelMessageResult{}, fmt.Errorf("edit channel message: db does not support transactions")
	}
	if req.EditDate == 0 {
		req.EditDate = nowUnix()
	}
	entities, err := encodeMessageEntities(req.Entities)
	if err != nil {
		return domain.EditChannelMessageResult{}, err
	}
	replyMarkupJSON, err := encodeReplyMarkup(req.ReplyMarkup)
	if err != nil {
		return domain.EditChannelMessageResult{}, fmt.Errorf("encode channel edit reply markup: %w", err)
	}
	richMessageJSON, err := encodeRichMessage(req.RichMessage)
	if err != nil {
		return domain.EditChannelMessageResult{}, fmt.Errorf("encode channel edit rich message: %w", err)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.EditChannelMessageResult{}, fmt.Errorf("begin edit channel message: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelMessageResult{}, err
	}
	msg, err := s.getChannelMessage(ctx, tx, req.ChannelID, req.ID)
	if err != nil {
		return domain.EditChannelMessageResult{}, err
	}
	if msg.Deleted || msg.Action != nil {
		return domain.EditChannelMessageResult{}, domain.ErrMessageIDInvalid
	}
	finalMedia := msg.Media
	if req.Media != nil {
		finalMedia = req.Media
	}
	finalRich := msg.RichMessage
	if req.SetRichMessage {
		finalRich = req.RichMessage
	}
	if strings.TrimSpace(req.Message) == "" && finalMedia.IsZero() && finalRich.IsZero() {
		return domain.EditChannelMessageResult{}, domain.ErrChannelInvalid
	}
	if req.WebPageResolve {
		// Channel link preview replacement in place: only swaps media (without touching body/entities/edit_date) + reserves channel pts +
		// a channel_web_page event. Idempotency guard: swap only when the current media is still the pending placeholder with the matching id.
		if req.Media == nil || !domain.IsPendingWebPageMedia(msg.Media, req.ExpectedWebPageID) {
			return domain.EditChannelMessageResult{}, domain.ErrMessageNotModified
		}
		mediaJSON, err := encodeMessageMedia(req.Media)
		if err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("encode channel web page media: %w", err)
		}
		pts, err := s.reserveChannelPts(ctx, tx, req.ChannelID)
		if err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("allocate channel web page pts: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE channel_messages SET media = $3, pts = $4, updated_at = now()
WHERE channel_id = $1 AND id = $2`, req.ChannelID, req.ID, mediaJSON, pts); err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("update channel web page media: %w", err)
		}
		media := *req.Media
		msg.Media = &media
		msg.Pts = pts
		event := domain.ChannelUpdateEvent{
			ChannelID:    req.ChannelID,
			Type:         domain.ChannelUpdateWebPage,
			Pts:          pts,
			PtsCount:     1,
			Date:         msg.Date,
			Message:      msg,
			SenderUserID: req.UserID,
		}
		if err := insertChannelEventTx(ctx, tx, event); err != nil {
			return domain.EditChannelMessageResult{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE channels SET pts = $2, updated_at = now() WHERE id = $1`, req.ChannelID, pts); err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("update channel web page pts: %w", err)
		}
		channel.Pts = pts
		if err := tx.Commit(ctx); err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("commit channel web page resolve: %w", err)
		}
		committed = true
		recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
		return domain.EditChannelMessageResult{Channel: channel, Message: msg, Event: event, Recipients: recipients}, nil
	}
	// Participant todo collaboration (append/toggle) is equivalent to producing new content/a service message in the channel,
	// so it must satisfy the same permissions as normal sending: a member muted by send_messages, or a
	// broadcast subscriber without posting rights, must not bypass the send restriction via OthersCanComplete/Append.
	participantTodoEdit := isChannelTodoParticipantEdit(req, msg) && canSendChannelMessage(channel, member)
	viaBotEditRequested := req.ViaBotEditBotID != 0
	viaBotEdit := viaBotEditRequested && msg.ViaBotID == req.ViaBotEditBotID
	if viaBotEditRequested && !viaBotEdit {
		return domain.EditChannelMessageResult{}, domain.ErrMessageAuthorRequired
	}
	canWriteEdit := viaBotEdit || msg.SenderUserID == req.UserID || canEditChannelMessage(member) || participantTodoEdit
	if !canWriteEdit {
		return domain.EditChannelMessageResult{}, domain.ErrMessageAuthorRequired
	}
	richChanged := req.SetRichMessage && !richMessagesEqual(msg.RichMessage, req.RichMessage)
	if req.Media == nil && !req.SetReplyMarkup && !richChanged && msg.Body == req.Message && sameMessageEntities(msg.Entities, req.Entities) {
		return domain.EditChannelMessageResult{}, domain.ErrMessageNotModified
	}
	ptsCount := 1
	if req.TodoServiceAction != nil {
		ptsCount = 2
	}
	finalPts, err := s.reserveChannelPtsN(ctx, tx, req.ChannelID, ptsCount)
	if err != nil {
		return domain.EditChannelMessageResult{}, fmt.Errorf("allocate channel edit pts: %w", err)
	}
	editPts := finalPts - ptsCount + 1
	prevMsg := msg
	if _, err := tx.Exec(ctx, `
UPDATE channel_messages
SET body = $4,
    entities = $5,
    edit_date = $6,
    pts = $7,
    reply_markup = CASE WHEN $9 THEN $10::jsonb ELSE reply_markup END,
    rich_message = CASE WHEN $11 THEN $12::jsonb ELSE rich_message END,
    updated_at = now()
WHERE channel_id = $1 AND id = $2 AND NOT deleted AND (sender_user_id = $3 OR $8)`,
		req.ChannelID, req.ID, req.UserID, req.Message, entities, req.EditDate, editPts, canWriteEdit, req.SetReplyMarkup, string(replyMarkupJSON), req.SetRichMessage, string(richMessageJSON)); err != nil {
		return domain.EditChannelMessageResult{}, fmt.Errorf("update channel edit: %w", err)
	}
	if req.Media != nil {
		mediaJSON, err := encodeMessageMedia(req.Media)
		if err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("encode channel edit media: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE channel_messages SET media = $3, updated_at = now()
WHERE channel_id = $1 AND id = $2`, req.ChannelID, req.ID, mediaJSON); err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("update channel edit media: %w", err)
		}
		media := *req.Media
		msg.Media = &media
	}
	msg.Body = req.Message
	msg.Entities = append([]domain.MessageEntity(nil), req.Entities...)
	// Shared media index (0118): an edit may swap media and may change link entities in the text → rebuild the index from the post-edit effective media+entities.
	if err := replaceChannelMediaIndexTx(ctx, tx, req.ChannelID, req.ID, msg.Date, msg.Media, msg.Entities); err != nil {
		return domain.EditChannelMessageResult{}, err
	}
	if req.SetReplyMarkup {
		msg.ReplyMarkup, err = decodeReplyMarkup(string(replyMarkupJSON))
		if err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("decode channel edit reply markup: %w", err)
		}
	}
	if req.SetRichMessage {
		msg.RichMessage, err = decodeRichMessage(string(richMessageJSON))
		if err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("decode channel edit rich message: %w", err)
		}
	}
	msg.EditDate = req.EditDate
	msg.Pts = editPts
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdateEditMessage,
		Pts:          editPts,
		PtsCount:     1,
		Date:         req.EditDate,
		Message:      msg,
		SenderUserID: req.UserID,
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.EditChannelMessageResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET pts = $2, updated_at = now() WHERE id = $1`, req.ChannelID, editPts); err != nil {
		return domain.EditChannelMessageResult{}, fmt.Errorf("update channel edit pts: %w", err)
	}
	channel.Pts = editPts
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID:   req.ChannelID,
		UserID:      req.UserID,
		Date:        req.EditDate,
		Type:        domain.ChannelAdminLogEditMessage,
		PrevMessage: &prevMsg,
		NewMessage:  &msg,
		Query:       msg.Body,
	}); err != nil {
		return domain.EditChannelMessageResult{}, err
	}
	// Reconcile the post-edit @ set with the existing unread mentions: add mentions for new targets (still at the "has not read the
	// message" write boundary), delete entity mentions that were removed; the reply implicit mention is kept.
	// Run only when body/entities actually change: media-only edits such as geolive updates, todo append/toggle, poll
	// close keep the body and @ entities unchanged but carry no MentionUserIDs (=nil), and an unconditional
	// reconcile would wrongly treat still-unread @ mentions in the caption as "removed" and delete them, so the unread mention count
	// could not be recomputed from visible messages.
	if prevMsg.Body != req.Message || !sameMessageEntities(prevMsg.Entities, req.Entities) {
		if err := s.reconcileChannelEditMentionsTx(ctx, tx, channel, msg, req.UserID, req.MentionUserIDs); err != nil {
			return domain.EditChannelMessageResult{}, err
		}
	}
	var serviceMsg domain.ChannelMessage
	var serviceEvent domain.ChannelUpdateEvent
	if req.TodoServiceAction != nil {
		action := cloneChannelMessageAction(req.TodoServiceAction)
		if action == nil {
			return domain.EditChannelMessageResult{}, domain.ErrChannelInvalid
		}
		serviceMsgID, err := s.msgIDs.NextChannelMessageID(ctx, req.ChannelID)
		if err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("allocate channel todo service message id: %w", err)
		}
		servicePts := finalPts
		serviceMsg = domain.ChannelMessage{
			ChannelID:    req.ChannelID,
			ID:           serviceMsgID,
			SenderUserID: req.UserID,
			From:         domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID},
			Date:         req.EditDate,
			Post:         channel.Broadcast,
			Silent:       msg.Silent,
			NoForwards:   msg.NoForwards || channel.NoForwards,
			ReplyTo:      channelTodoServiceReply(msg),
			Action:       action,
			Pts:          servicePts,
		}
		serviceEvent = domain.ChannelUpdateEvent{
			ChannelID:    req.ChannelID,
			Type:         domain.ChannelUpdateNewMessage,
			Pts:          servicePts,
			PtsCount:     1,
			Date:         req.EditDate,
			Message:      serviceMsg,
			SenderUserID: req.UserID,
		}
		if err := insertChannelMessageTx(ctx, tx, serviceMsg); err != nil {
			return domain.EditChannelMessageResult{}, err
		}
		if err := insertChannelEventTx(ctx, tx, serviceEvent); err != nil {
			return domain.EditChannelMessageResult{}, err
		}
		if err := updateForumTopicTopMessageTx(ctx, tx, req.ChannelID, serviceMsg); err != nil {
			return domain.EditChannelMessageResult{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE channels SET top_message_id = $2, pts = $3, updated_at = now() WHERE id = $1`, req.ChannelID, serviceMsgID, servicePts); err != nil {
			return domain.EditChannelMessageResult{}, fmt.Errorf("update channel todo service top: %w", err)
		}
		channel.TopMessageID = serviceMsgID
		channel.Pts = servicePts
		if err := upsertChannelDialogsForMessageTx(ctx, tx, channel, serviceMsg, req.UserID); err != nil {
			return domain.EditChannelMessageResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.EditChannelMessageResult{}, fmt.Errorf("commit edit channel message: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return domain.EditChannelMessageResult{Channel: channel, Message: msg, Event: event, ServiceMessage: serviceMsg, ServiceEvent: serviceEvent, Recipients: recipients}, nil
}

func isChannelTodoParticipantEdit(req domain.EditChannelMessageRequest, msg domain.ChannelMessage) bool {
	if !req.AllowTodoParticipantMutation || req.SetReplyMarkup || req.Media == nil || req.Media.Kind != domain.MessageMediaKindTodo || req.Media.Todo == nil {
		return false
	}
	if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindTodo || msg.Media.Todo == nil {
		return false
	}
	if req.TodoServiceAction == nil {
		return false
	}
	switch req.TodoServiceAction.Type {
	case domain.ChannelActionTodoCompletions:
		if !msg.Media.Todo.OthersCanComplete {
			return false
		}
	case domain.ChannelActionTodoAppendTasks:
		if !msg.Media.Todo.OthersCanAppend {
			return false
		}
	default:
		return false
	}
	return msg.Body == req.Message && sameMessageEntities(msg.Entities, req.Entities)
}

func channelTodoServiceReply(msg domain.ChannelMessage) *domain.MessageReply {
	reply := &domain.MessageReply{
		Peer:      domain.Peer{Type: domain.PeerTypeChannel, ID: msg.ChannelID},
		MessageID: msg.ID,
	}
	if msg.ReplyTo != nil {
		reply.TopMessageID = msg.ReplyTo.TopMessageID
		reply.ForumTopic = msg.ReplyTo.ForumTopic
	}
	return reply
}

func (s *ChannelStore) UpdatePinnedMessage(ctx context.Context, req domain.UpdateChannelPinnedMessageRequest) (domain.UpdateChannelPinnedMessageResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("pin channel message: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("begin pin channel message: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, err
	}
	if !canPinChannelMessages(channel, member) {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelAdminRequired
	}
	msg, err := s.getChannelMessage(ctx, tx, req.ChannelID, req.MessageID)
	if err != nil || msg.Deleted {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrMessageIDInvalid
	}
	// Multi-pin model: pin/unpin only flips the target message's own pinned flag and does not affect other
	// pins; consistent with official, no count limit is imposed.
	if msg.Pinned == req.Pinned {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelNotModified
	}
	pts, err := s.reserveChannelPts(ctx, tx, req.ChannelID)
	if err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("allocate channel pin pts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE channel_messages SET pinned = $3
WHERE channel_id = $1 AND id = $2`, req.ChannelID, req.MessageID, req.Pinned); err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("update channel message pinned: %w", err)
	}
	// channels.pinned_message_id is maintained as the latest pinned id (the output semantics of channelFull.pinned_msg_id).
	var latestPinned int
	if err := tx.QueryRow(ctx, `
UPDATE channels
SET pinned_message_id = COALESCE((
        SELECT MAX(id) FROM channel_messages
        WHERE channel_id = $1 AND pinned AND NOT deleted
    ), 0),
    pts = $2, updated_at = now()
WHERE id = $1
RETURNING pinned_message_id`, req.ChannelID, pts).Scan(&latestPinned); err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("update channel pinned message: %w", err)
	}
	channel.PinnedMessageID = latestPinned
	channel.Pts = pts
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdatePinnedMessages,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		MessageIDs:   []int{req.MessageID},
		SenderUserID: req.UserID,
		Pinned:       req.Pinned,
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, err
	}
	logMsg := msg
	logMsg.Pinned = req.Pinned
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID: req.ChannelID,
		UserID:    req.UserID,
		Date:      req.Date,
		Type:      domain.ChannelAdminLogUpdatePinned,
		Message:   &logMsg,
		Query:     msg.Body,
	}); err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("commit pin channel message: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return domain.UpdateChannelPinnedMessageResult{Channel: channel, Event: event, Recipients: recipients}, nil
}

// UnpinAllChannelMessages clears every pinned message in the channel with one
// channel-pts event carrying all cleared ids.
func (s *ChannelStore) UnpinAllChannelMessages(ctx context.Context, req domain.UnpinAllChannelMessagesRequest) (domain.UpdateChannelPinnedMessageResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("unpin all channel messages: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("begin unpin all channel messages: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, err
	}
	if !canPinChannelMessages(channel, member) {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelAdminRequired
	}
	rows, err := tx.Query(ctx, `
UPDATE channel_messages SET pinned = false
WHERE channel_id = $1 AND pinned AND NOT deleted
RETURNING id`, req.ChannelID)
	if err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("unpin all channel messages: %w", err)
	}
	cleared := []int(nil)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return domain.UpdateChannelPinnedMessageResult{}, err
		}
		cleared = append(cleared, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, err
	}
	if len(cleared) == 0 {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelNotModified
	}
	sort.Ints(cleared)
	pts, err := s.reserveChannelPts(ctx, tx, req.ChannelID)
	if err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("allocate channel unpin-all pts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE channels SET pinned_message_id = 0, pts = $2, updated_at = now()
WHERE id = $1`, req.ChannelID, pts); err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("clear channel pinned message: %w", err)
	}
	channel.PinnedMessageID = 0
	channel.Pts = pts
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdatePinnedMessages,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		MessageIDs:   cleared,
		SenderUserID: req.UserID,
		Pinned:       false,
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, fmt.Errorf("commit unpin all channel messages: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return domain.UpdateChannelPinnedMessageResult{Channel: channel, Event: event, Recipients: recipients}, nil
}

func canPinChannelMessages(channel domain.Channel, member domain.ChannelMember) bool {
	if member.Role == domain.ChannelRoleCreator {
		return true
	}
	if member.Role == domain.ChannelRoleAdmin {
		// A broadcast channel admin is judged by edit_messages (the channel's rights edit UI
		// has no pin_messages bit); a megagroup admin is judged by pin_messages.
		if channel.Broadcast && !channel.Megagroup {
			return member.AdminRights.EditMessages
		}
		return member.AdminRights.PinMessages
	}
	// An ordinary member of a public megagroup cannot pin; a private megagroup follows the default/per-member restriction bits.
	if !channel.Megagroup || channel.Username != "" {
		return false
	}
	return !channel.DefaultBannedRights.PinMessages && !member.BannedRights.PinMessages
}

func canEditChannelMessage(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.EditMessages)
}

// ClearDanglingPinnedMessage zeroes a pinned value that points at a deleted message; it consumes no channel
// pts and emits no event (officially, deletion removes it from the pinned set with no corresponding update).
func (s *ChannelStore) ClearDanglingPinnedMessage(ctx context.Context, channelID int64, messageID int) error {
	if channelID == 0 || messageID <= 0 {
		return domain.ErrChannelInvalid
	}
	_, err := s.db.Exec(ctx, `
UPDATE channels
SET pinned_message_id = 0, updated_at = now()
WHERE id = $1 AND pinned_message_id = $2`, channelID, messageID)
	if err != nil {
		return fmt.Errorf("clear dangling pinned message: %w", err)
	}
	return nil
}

// reconcileChannelEditMentionsTx maintains channel_unread_mentions after a text edit:
// newly appearing @ targets get rows at the send-time write boundary; users no longer @-ed have their rows deleted and the
// dialog count recomputed. The implicit mention of the replied-to message author is not in the entity set and is explicitly excluded.
func (s *ChannelStore) reconcileChannelEditMentionsTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, msg domain.ChannelMessage, actorUserID int64, newTargets []int64) error {
	if channel.Broadcast && !channel.Megagroup {
		return nil
	}
	keep := make(map[int64]struct{}, len(newTargets)+1)
	for _, id := range newTargets {
		if id != 0 {
			keep[id] = struct{}{}
		}
	}
	if msg.ReplyTo != nil && msg.ReplyTo.MessageID > 0 {
		if target, err := s.getChannelMessage(ctx, tx, channel.ID, msg.ReplyTo.MessageID); err == nil && target.SenderUserID != 0 {
			keep[target.SenderUserID] = struct{}{}
		}
	}
	rows, err := tx.Query(ctx, `
SELECT user_id FROM channel_unread_mention_index
WHERE channel_id = $1 AND message_id = $2`, channel.ID, msg.ID)
	if err != nil {
		return fmt.Errorf("list edit mention owners: %w", err)
	}
	existing := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existing[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	removed := make([]int64, 0)
	for id := range existing {
		if _, ok := keep[id]; !ok {
			removed = append(removed, id)
		}
	}
	added := make([]int64, 0, len(newTargets))
	for _, id := range newTargets {
		if id == 0 {
			continue
		}
		if _, ok := existing[id]; !ok {
			added = append(added, id)
		}
	}
	if len(removed) > 0 {
		if err := deleteChannelUnreadMentionsForUsersTx(ctx, tx, channel.ID, []int{msg.ID}, removed); err != nil {
			return err
		}
	}
	if len(added) > 0 {
		if err := insertChannelUnreadMentionsTx(ctx, tx, channel.ID, msg, actorUserID, added); err != nil {
			return err
		}
	}
	return nil
}
