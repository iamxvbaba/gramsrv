package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"time"
)

func (s *ChannelStore) SendChannelMessage(_ context.Context, req domain.SendChannelMessageRequest) (domain.SendChannelMessageResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	if strings.TrimSpace(req.Message) == "" && req.Action == nil && req.Media.IsZero() && req.RichMessage.IsZero() {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	var fingerprint []byte
	var err error
	if req.RandomID != 0 {
		fingerprint, err = store.ChannelSendFingerprint(req)
		if err != nil {
			return domain.SendChannelMessageResult{}, err
		}
		req.IdempotencyFingerprint = fingerprint
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.RandomID != 0 {
		if replay, found, replayErr := s.lookupChannelSendReplayLocked(domain.ChannelSendReplayRequest{
			ChannelID:              req.ChannelID,
			SenderUserID:           req.UserID,
			RandomID:               req.RandomID,
			IdempotencyFingerprint: fingerprint,
		}); replayErr != nil || found {
			return replay, replayErr
		}
	}
	channel, member, err := s.channelAndMemberLocked(req.UserID, req.ChannelID)
	if errors.Is(err, domain.ErrChannelPrivate) {
		if candidate, ok := s.channels[req.ChannelID]; ok && !candidate.Deleted {
			var guest bool
			member, guest, err = s.linkedDiscussionGuestLocked(req.UserID, candidate)
			if guest {
				channel = candidate
			}
		}
	}
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if member.Guest && channel.JoinToSend {
		return domain.SendChannelMessageResult{}, domain.ErrChannelWriteForbidden
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	fromBoostsApplied := 0
	if channel.Megagroup {
		fromBoostsApplied = s.selfBoostsAppliedLocked(req.UserID, req.ChannelID, req.Date)
	}
	if domain.ChannelBannedRightsBlockMessage(req, channel, member, fromBoostsApplied) {
		return domain.SendChannelMessageResult{}, domain.ErrChannelWriteForbidden
	}
	if !canSendChannelMessageWithBoost(channel, member, fromBoostsApplied) {
		return domain.SendChannelMessageResult{}, domain.ErrChannelWriteForbidden
	}
	if wait := channelSlowModeWait(channel, member, req.Date); wait > 0 {
		return domain.SendChannelMessageResult{}, domain.NewSlowModeWaitError(wait)
	}
	replyTo, err := s.resolveChannelReplyLocked(req, member, channel, fromBoostsApplied)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	var sendAs *domain.Peer
	if req.SendAs != nil {
		p := *req.SendAs
		sendAs = &p
	}
	pts := s.nextChannelPtsLocked(req.ChannelID)
	msgID := s.nextChannelMessageIDLocked(req.ChannelID)
	skipDelivery := channelDeliverySkipSet(req.SkipDeliveryUserIDs)
	var discussion *domain.SendChannelDiscussionResult
	var discussionRef *domain.ChannelDiscussionRef
	if channel.Broadcast && channel.LinkedChatID != 0 {
		if linked, ok := s.channels[channel.LinkedChatID]; ok && !linked.Deleted && linked.Megagroup {
			discussionPts := s.nextChannelPtsLocked(linked.ID)
			discussionMsgID := s.nextChannelMessageIDLocked(linked.ID)
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
				RichMessage:  cloneRichMessage(req.RichMessage),
				Forward:      &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}, Date: req.Date, ChannelPost: msgID, SavedFrom: domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}, SavedFromMsgID: msgID},
				ViaBotID:     req.ViaBotID,
				GroupedID:    req.GroupedID,
				ReplyMarkup:  cloneReplyMarkup(req.ReplyMarkup),
				Pts:          discussionPts,
			}
			discussionEvent := domain.ChannelUpdateEvent{
				ChannelID: linked.ID,
				Type:      domain.ChannelUpdateNewMessage,
				Pts:       discussionPts,
				PtsCount:  1,
				Date:      req.Date,
				Message:   cloneChannelMessage(discussionMsg),
			}
			s.messages[linked.ID] = append(s.messages[linked.ID], discussionMsg)
			s.appendChannelEventLocked(discussionEvent)
			linked.TopMessageID = discussionMsgID
			linked.Pts = discussionPts
			s.channels[linked.ID] = linked
			s.addChannelUnreadMentionsLocked(linked.ID, discussionMsg, req.UserID, req.MentionUserIDs)
			for userID, member := range s.members[linked.ID] {
				if member.Status == domain.ChannelMemberActive {
					s.upsertChannelDialogLocked(userID, linked, discussionMsg, false)
				}
			}
			discussion = &domain.SendChannelDiscussionResult{
				Channel:        cloneChannel(linked),
				Message:        cloneChannelMessage(discussionMsg),
				Event:          cloneChannelEvent(discussionEvent),
				Recipients:     s.activeMemberIDsLocked(linked.ID, 0, 0),
				MentionUserIDs: append([]int64(nil), req.MentionUserIDs...),
			}
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
		PostAuthor:        memoryChannelPostAuthor(channel, req.PostAuthor),
		Silent:            req.Silent,
		NoForwards:        req.NoForwards || channel.NoForwards,
		Body:              req.Message,
		Entities:          append([]domain.MessageEntity(nil), req.Entities...),
		Media:             req.Media,
		RichMessage:       cloneRichMessage(req.RichMessage),
		ReplyTo:           replyTo,
		Forward:           cloneMessageForward(req.Forward),
		ViaBotID:          req.ViaBotID,
		GroupedID:         req.GroupedID,
		ReplyMarkup:       cloneReplyMarkup(req.ReplyMarkup),
		SendAs:            sendAs,
		Discussion:        discussionRef,
		Action:            cloneChannelMessageAction(req.Action),
		FromBoostsApplied: fromBoostsApplied,
		Pts:               pts,
	}
	msg.Replies = s.channelMessageRepliesLocked(req.UserID, req.ChannelID, msg)
	var sendSnapshot []byte
	if req.RandomID != 0 {
		sendSnapshot, err = store.EncodeChannelSendSnapshot(msg)
		if err != nil {
			return domain.SendChannelMessageResult{}, err
		}
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		Message:      cloneChannelMessage(msg),
		SenderUserID: req.UserID,
	}
	s.messages[req.ChannelID] = append(s.messages[req.ChannelID], msg)
	s.appendChannelEventLocked(event)
	if !channel.Broadcast || channel.Megagroup {
		mentionTargets := req.MentionUserIDs
		if msg.ReplyTo != nil && msg.ReplyTo.MessageID > 0 {
			if target, ok := s.findMessageLocked(req.ChannelID, msg.ReplyTo.MessageID); ok &&
				target.SenderUserID != 0 && target.SenderUserID != req.UserID {
				mentionTargets = append(append([]int64(nil), mentionTargets...), target.SenderUserID)
			}
		}
		s.addChannelUnreadMentionsLocked(req.ChannelID, msg, req.UserID, mentionTargets)
	}
	s.updateForumTopicTopMessageLocked(req.ChannelID, msg)
	if channel.Broadcast {
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: req.ChannelID,
			UserID:    req.UserID,
			Date:      req.Date,
			Type:      domain.ChannelAdminLogSendMessage,
			Message:   ptrChannelMessage(msg),
			Query:     msg.Body,
		})
	}
	if req.RandomID != 0 {
		key := channelRandomKey{channelID: req.ChannelID, userID: req.UserID, randomID: req.RandomID}
		s.randomToID[key] = msg.ID
		replayKey := channelMessageReplayKey{channelID: req.ChannelID, messageID: msg.ID}
		s.sendSnapshots[replayKey] = sendSnapshot
		s.sendFingerprints[replayKey] = append([]byte(nil), fingerprint...)
	}
	channel.TopMessageID = msg.ID
	channel.Pts = pts
	s.channels[req.ChannelID] = channel
	if !member.Guest {
		member.SlowmodeLastSendDate = req.Date
		s.members[req.ChannelID][req.UserID] = member
	}
	for userID, member := range s.members[req.ChannelID] {
		if member.Status == domain.ChannelMemberActive {
			if _, skip := skipDelivery[userID]; skip && userID != req.UserID {
				member.ReadInboxMaxID = maxInt(member.ReadInboxMaxID, msg.ID)
				member.UnreadMark = false
				s.members[req.ChannelID][userID] = member
				continue
			}
			s.upsertChannelDialogLocked(userID, channel, msg, userID == req.UserID)
		}
	}
	recipients := s.activeMemberIDsLocked(req.ChannelID, 0, 0)
	recipients = filterSkippedChannelRecipients(recipients, skipDelivery)
	return domain.SendChannelMessageResult{
		Channel:             channel,
		Message:             cloneChannelMessage(msg),
		Event:               cloneChannelEvent(event),
		Recipients:          recipients,
		Discussion:          discussion,
		MentionUserIDs:      append([]int64(nil), req.MentionUserIDs...),
		SkipDeliveryUserIDs: append([]int64(nil), req.SkipDeliveryUserIDs...),
	}, nil
}

// LookupChannelSendReplay returns a committed regular-channel or monoforum send receipt without
// evaluating current membership, write permissions, slow mode or any message allocation path.
func (s *ChannelStore) LookupChannelSendReplay(_ context.Context, req domain.ChannelSendReplayRequest) (domain.SendChannelMessageResult, bool, error) {
	if req.ChannelID == 0 || req.SenderUserID == 0 || req.RandomID == 0 {
		return domain.SendChannelMessageResult{}, false, fmt.Errorf("memory channel send replay: invalid scope")
	}
	if req.SavedPeer.ID == 0 {
		if req.SavedPeer.Type != "" {
			return domain.SendChannelMessageResult{}, false, fmt.Errorf("memory channel send replay: incomplete saved peer scope")
		}
	} else if req.SavedPeer.Type != domain.PeerTypeUser {
		return domain.SendChannelMessageResult{}, false, fmt.Errorf("memory channel send replay: invalid saved peer scope")
	}
	if err := store.ValidateSendFingerprint(req.IdempotencyFingerprint, "channel send replay"); err != nil {
		return domain.SendChannelMessageResult{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lookupChannelSendReplayLocked(req)
}

func (s *ChannelStore) lookupChannelSendReplayLocked(req domain.ChannelSendReplayRequest) (domain.SendChannelMessageResult, bool, error) {
	var id int
	if req.SavedPeer.ID == 0 {
		var found bool
		id, found = s.randomToID[channelRandomKey{channelID: req.ChannelID, userID: req.SenderUserID, randomID: req.RandomID}]
		if !found {
			return domain.SendChannelMessageResult{}, false, nil
		}
	} else {
		msg, found := s.findMonoforumDuplicateLocked(req.ChannelID, req.SenderUserID, req.SavedPeer, req.RandomID)
		if !found {
			return domain.SendChannelMessageResult{}, false, nil
		}
		id = msg.ID
	}
	replayKey := channelMessageReplayKey{channelID: req.ChannelID, messageID: id}
	if !store.SameSendFingerprint(s.sendFingerprints[replayKey], req.IdempotencyFingerprint) {
		return domain.SendChannelMessageResult{}, false, domain.ErrMessageRandomIDDuplicate
	}
	first, err := store.DecodeChannelSendSnapshot(s.sendSnapshots[replayKey])
	if err != nil {
		return domain.SendChannelMessageResult{}, false, fmt.Errorf("memory duplicate channel message snapshot: %w", err)
	}
	if first.ID != id || first.ChannelID != req.ChannelID || first.SenderUserID != req.SenderUserID || first.RandomID != req.RandomID || first.SavedPeer != req.SavedPeer {
		return domain.SendChannelMessageResult{}, false, fmt.Errorf("memory duplicate channel message snapshot disagrees with random_id receipt")
	}
	replay := first
	var replayDelete *domain.ChannelUpdateEvent
	if current, found := s.findMessageLocked(req.ChannelID, id); found && !current.Deleted {
		replay = cloneChannelMessage(current)
	} else if receipt := s.deleteReceipts[replayKey]; receipt != nil {
		cloned := cloneChannelEvent(*receipt)
		replayDelete = &cloned
	} else {
		return domain.SendChannelMessageResult{}, false, fmt.Errorf("memory duplicate channel message %d is absent without a durable delete receipt", id)
	}
	channel, ok := s.channels[req.ChannelID]
	if !ok {
		return domain.SendChannelMessageResult{}, false, fmt.Errorf("memory duplicate channel message %d has no channel", id)
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    first.ChannelID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          first.Pts,
		PtsCount:     1,
		Date:         first.Date,
		Message:      cloneChannelMessage(replay),
		SenderUserID: first.SenderUserID,
	}
	return domain.SendChannelMessageResult{
		Channel:           cloneChannel(channel),
		Message:           cloneChannelMessage(replay),
		Event:             event,
		Duplicate:         true,
		ReplayDeleteEvent: replayDelete,
	}, true, nil
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

func (s *ChannelStore) nextChannelMessageIDLocked(channelID int64) int {
	s.msgSeq[channelID]++
	return s.msgSeq[channelID]
}

func (s *ChannelStore) appendChannelServiceMessageLocked(channelID, senderUserID int64, date int, action domain.ChannelMessageAction) (domain.ChannelMessage, domain.ChannelUpdateEvent) {
	channel := s.channels[channelID]
	msgID := s.nextChannelMessageIDLocked(channelID)
	action = channelServiceActionForMessage(channelID, msgID, action)
	pts := s.nextChannelPtsLocked(channelID)
	msg := domain.ChannelMessage{
		ChannelID:    channelID,
		ID:           msgID,
		SenderUserID: senderUserID,
		From:         domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID},
		Date:         date,
		Post:         channel.Broadcast,
		Action:       &action,
		Pts:          pts,
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    channelID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         date,
		Message:      cloneChannelMessage(msg),
		SenderUserID: senderUserID,
		UserIDs:      append([]int64(nil), action.UserIDs...),
	}
	s.messages[channelID] = append(s.messages[channelID], msg)
	s.appendChannelEventLocked(event)
	return msg, event
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

func canSendChannelMessage(channel domain.Channel, member domain.ChannelMember) bool {
	return canSendChannelMessageWithBoost(channel, member, 0)
}

func canSendChannelMessageWithBoost(channel domain.Channel, member domain.ChannelMember, selfBoostsApplied int) bool {
	if channel.Broadcast {
		return canPostToBroadcast(member)
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

// memoryChannelPostAuthor 仅在 signatures 开启的 broadcast post 上保留签名。
func memoryChannelPostAuthor(channel domain.Channel, author string) string {
	if !channel.Broadcast || !channel.Signatures {
		return ""
	}
	return author
}
