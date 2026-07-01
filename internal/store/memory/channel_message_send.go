package memory

import (
	"context"
	"strings"
	"telesrv/internal/domain"
	"time"
)

func (s *ChannelStore) SendChannelMessage(_ context.Context, req domain.SendChannelMessageRequest) (domain.SendChannelMessageResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	if strings.TrimSpace(req.Message) == "" && req.Action == nil && req.Media.IsZero() {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	fromBoostsApplied := 0
	if channel.Megagroup {
		fromBoostsApplied = s.selfBoostsAppliedLocked(req.UserID, req.ChannelID, req.Date)
	}
	if blocksPlainChannelMessage(req, channel, member, fromBoostsApplied) {
		return domain.SendChannelMessageResult{}, domain.ErrChannelWriteForbidden
	}
	if !canSendChannelMessageWithBoost(channel, member, fromBoostsApplied) {
		return domain.SendChannelMessageResult{}, domain.ErrChannelWriteForbidden
	}
	if req.RandomID != 0 {
		if id, ok := s.randomToID[channelRandomKey{channelID: req.ChannelID, userID: req.UserID, randomID: req.RandomID}]; ok {
			msg, ok := s.findMessageLocked(req.ChannelID, id)
			if ok {
				event := s.eventForMessageLocked(req.ChannelID, id)
				if event.Message.ID != 0 {
					msg = event.Message
				}
				return domain.SendChannelMessageResult{
					Channel:   channel,
					Message:   cloneChannelMessage(msg),
					Event:     event,
					Duplicate: true,
				}, nil
			}
		}
	}
	if wait := channelSlowModeWait(channel, member, req.Date); wait > 0 {
		return domain.SendChannelMessageResult{}, domain.NewSlowModeWaitError(wait)
	}
	replyTo, err := s.resolveChannelReplyLocked(req, member, channel)
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
			s.events[linked.ID] = append(s.events[linked.ID], discussionEvent)
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
	s.events[req.ChannelID] = append(s.events[req.ChannelID], event)
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
		s.randomToID[channelRandomKey{channelID: req.ChannelID, userID: req.UserID, randomID: req.RandomID}] = msg.ID
	}
	channel.TopMessageID = msg.ID
	channel.Pts = pts
	s.channels[req.ChannelID] = channel
	member.SlowmodeLastSendDate = req.Date
	s.members[req.ChannelID][req.UserID] = member
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
	s.events[channelID] = append(s.events[channelID], event)
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

func blocksPlainChannelMessage(req domain.SendChannelMessageRequest, channel domain.Channel, member domain.ChannelMember, selfBoostsApplied int) bool {
	if strings.TrimSpace(req.Message) == "" || req.Action != nil || !req.Media.IsZero() {
		return false
	}
	if channel.Broadcast || member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin {
		return false
	}
	if member.BannedRights.SendPlain {
		return true
	}
	if !channel.DefaultBannedRights.SendPlain {
		return false
	}
	return channel.BoostsUnrestrict == 0 || selfBoostsApplied < channel.BoostsUnrestrict
}

// memoryChannelPostAuthor 仅在 signatures 开启的 broadcast post 上保留签名。
func memoryChannelPostAuthor(channel domain.Channel, author string) string {
	if !channel.Broadcast || !channel.Signatures {
		return ""
	}
	return author
}
