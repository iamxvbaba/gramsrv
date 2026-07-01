package rpc

import (
	"context"
	"telesrv/internal/domain"
)

type captureMessages struct {
	list              domain.MessageList
	filter            domain.MessageFilter
	sendResult        domain.SendPrivateTextResult
	sendUserID        int64
	sendReq           domain.SendPrivateTextRequest
	setThemeUserID    int64
	setThemeReq       domain.SetPrivateChatThemeRequest
	setThemeRes       domain.SetPrivateChatThemeResult
	forwardUserID     int64
	forwardReq        domain.ForwardPrivateMessagesRequest
	forwardRes        domain.ForwardPrivateMessagesResult
	readResult        domain.ReadHistoryResult
	readReq           domain.ReadHistoryRequest
	readPeer          domain.Peer
	readMaxID         int
	readContentsReq   domain.ReadMessageContentsRequest
	readContentsRes   domain.ReadMessageContentsResult
	setReactionReq    domain.SetPrivateMessageReactionsRequest
	setReactionRes    domain.PrivateMessageReactionsResult
	setReactionCalls  int
	getReactionReq    domain.PrivateMessageReactionsRequest
	getReactionRes    domain.PrivateMessageReactionsResult
	getMessagesCalls  int
	getMessagesIDs    [][]int
	getMessagesListed bool
	editReq           domain.EditMessageRequest
	editRes           domain.EditMessageResult
	outboxReadDateReq domain.OutboxReadDateRequest
	outboxReadDate    int
	deleteMessagesReq domain.DeleteMessagesRequest
	deleteMessagesRes domain.DeleteMessagesResult
	deleteHistoryReq  domain.DeleteHistoryRequest
	deleteHistoryRes  domain.DeleteMessagesResult
}

type scheduledCaptureMessages struct {
	*captureMessages
	scheduled          []domain.ScheduledMessage
	scheduleReq        domain.ScheduleMessageRequest
	editScheduledReq   domain.EditScheduledMessageRequest
	claimScheduledReq  domain.ScheduledMessageClaim
	deletedScheduled   domain.ScheduledMessageFilter
	markedScheduledID  int
	markedSentID       int
	releasedScheduled  int
	releasedErrMessage string
}

type ttlCaptureMessages struct {
	*captureMessages
	setTTLUserID         int64
	setTTLPeer           domain.Peer
	setTTLPeriod         int
	defaultTTLUserID     int64
	defaultTTLPeriod     int
	expiredPrivateClaims []domain.DeleteMessagesRequest
}

func (s *ttlCaptureMessages) GetPrivateHistoryTTL(_ context.Context, _ int64, _ domain.Peer) (int, error) {
	return s.setTTLPeriod, nil
}

func (s *ttlCaptureMessages) SetPrivateHistoryTTL(_ context.Context, userID int64, peer domain.Peer, period int) error {
	s.setTTLUserID = userID
	s.setTTLPeer = peer
	s.setTTLPeriod = period
	return nil
}

func (s *ttlCaptureMessages) DefaultHistoryTTL(_ context.Context, userID int64) (int, error) {
	s.defaultTTLUserID = userID
	return s.defaultTTLPeriod, nil
}

func (s *ttlCaptureMessages) SetDefaultHistoryTTL(_ context.Context, userID int64, period int) error {
	s.defaultTTLUserID = userID
	s.defaultTTLPeriod = period
	return nil
}

func (s *ttlCaptureMessages) ClaimExpiredPrivateMessages(context.Context, int, int) ([]domain.DeleteMessagesRequest, error) {
	return append([]domain.DeleteMessagesRequest(nil), s.expiredPrivateClaims...), nil
}

func (s *scheduledCaptureMessages) ScheduleMessage(_ context.Context, userID int64, req domain.ScheduleMessageRequest) (domain.ScheduledMessage, error) {
	s.scheduleReq = req
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	msg := domain.ScheduledMessage{
		OwnerUserID:          req.OwnerUserID,
		ID:                   len(s.scheduled) + 41,
		Peer:                 req.Peer,
		RandomID:             req.RandomID,
		Message:              req.Message,
		Entities:             append([]domain.MessageEntity(nil), req.Entities...),
		Media:                req.Media,
		Silent:               req.Silent,
		NoForwards:           req.NoForwards,
		ReplyTo:              req.ReplyTo,
		Forward:              req.Forward,
		SendAs:               req.SendAs,
		ScheduleDate:         req.ScheduleDate,
		ScheduleRepeatPeriod: req.ScheduleRepeatPeriod,
		CreatedAt:            req.Date,
		UpdatedAt:            req.Date,
		State:                "pending",
	}
	s.scheduled = append(s.scheduled, msg)
	return msg, nil
}

func (s *scheduledCaptureMessages) EditScheduledMessage(_ context.Context, userID int64, req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
	s.editScheduledReq = req
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	for i := range s.scheduled {
		if s.scheduled[i].OwnerUserID == req.OwnerUserID && s.scheduled[i].Peer == req.Peer && s.scheduled[i].ID == req.ID {
			if req.SetMessage {
				s.scheduled[i].Message = req.Message
				s.scheduled[i].Entities = append([]domain.MessageEntity(nil), req.Entities...)
			}
			s.scheduled[i].ScheduleDate = req.ScheduleDate
			s.scheduled[i].UpdatedAt = req.Date
			return s.scheduled[i], nil
		}
	}
	return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
}

func (s *scheduledCaptureMessages) ListScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	items := s.matchScheduled(filter)
	return domain.ScheduledMessageList{Messages: items, Count: len(items)}, nil
}

func (s *scheduledCaptureMessages) GetScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	items := s.matchScheduled(filter)
	return domain.ScheduledMessageList{Messages: items, Count: len(items)}, nil
}

func (s *scheduledCaptureMessages) DeleteScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter, _ int) ([]domain.ScheduledMessage, error) {
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	s.deletedScheduled = filter
	deleted := s.matchScheduled(filter)
	s.removeScheduled(filter)
	return deleted, nil
}

func (s *scheduledCaptureMessages) ClaimScheduledMessages(_ context.Context, userID int64, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	if claim.OwnerUserID == 0 {
		claim.OwnerUserID = userID
	}
	s.claimScheduledReq = claim
	return s.matchScheduled(domain.ScheduledMessageFilter{OwnerUserID: claim.OwnerUserID, Peer: claim.Peer, IDs: claim.IDs}), nil
}

func (s *scheduledCaptureMessages) ClaimDueScheduledMessages(context.Context, int, int, int) ([]domain.ScheduledMessage, error) {
	return nil, nil
}

func (s *scheduledCaptureMessages) MarkScheduledMessageSent(_ context.Context, _ int64, id, sentMessageID, _ int) error {
	s.markedScheduledID = id
	s.markedSentID = sentMessageID
	s.removeScheduled(domain.ScheduledMessageFilter{OwnerUserID: s.scheduleReq.OwnerUserID, Peer: s.scheduleReq.Peer, IDs: []int{id}})
	return nil
}

func (s *scheduledCaptureMessages) ReleaseScheduledMessage(_ context.Context, _ int64, id int, errText string) error {
	s.releasedScheduled = id
	s.releasedErrMessage = errText
	return nil
}

func (s *scheduledCaptureMessages) HasScheduledMessages(_ context.Context, userID int64, peer domain.Peer) (bool, error) {
	return len(s.matchScheduled(domain.ScheduledMessageFilter{OwnerUserID: userID, Peer: peer})) > 0, nil
}

func (s *scheduledCaptureMessages) matchScheduled(filter domain.ScheduledMessageFilter) []domain.ScheduledMessage {
	idSet := make(map[int]struct{}, len(filter.IDs))
	for _, id := range filter.IDs {
		idSet[id] = struct{}{}
	}
	out := make([]domain.ScheduledMessage, 0, len(s.scheduled))
	for _, msg := range s.scheduled {
		if filter.OwnerUserID != 0 && msg.OwnerUserID != filter.OwnerUserID {
			continue
		}
		if filter.Peer.ID != 0 && msg.Peer != filter.Peer {
			continue
		}
		if len(idSet) > 0 {
			if _, ok := idSet[msg.ID]; !ok {
				continue
			}
		}
		out = append(out, msg)
	}
	return out
}

func (s *scheduledCaptureMessages) removeScheduled(filter domain.ScheduledMessageFilter) {
	idSet := make(map[int]struct{}, len(filter.IDs))
	for _, id := range filter.IDs {
		idSet[id] = struct{}{}
	}
	kept := s.scheduled[:0]
	for _, msg := range s.scheduled {
		if filter.OwnerUserID != 0 && msg.OwnerUserID != filter.OwnerUserID {
			kept = append(kept, msg)
			continue
		}
		if filter.Peer.ID != 0 && msg.Peer != filter.Peer {
			kept = append(kept, msg)
			continue
		}
		if len(idSet) > 0 {
			if _, ok := idSet[msg.ID]; ok {
				continue
			}
		}
		kept = append(kept, msg)
	}
	s.scheduled = kept
}

func (s *captureMessages) SendPrivateText(_ context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	s.sendUserID = userID
	s.sendReq = req
	if s.sendResult.SenderMessage.ID == 0 {
		s.sendResult.SenderMessage = domain.Message{
			ID:          1,
			OwnerUserID: req.SenderUserID,
			RandomID:    req.RandomID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
			Date:        req.Date,
			Out:         true,
			Silent:      req.Silent,
			NoForwards:  req.NoForwards,
			Body:        req.Message,
			Entities:    req.Entities,
			Media:       req.Media,
			ReplyTo:     req.ReplyTo,
			Forward:     req.Forward,
			ViaBotID:    req.ViaBotID,
			Pts:         1,
		}
		s.sendResult.SenderEvent = domain.UpdateEvent{
			UserID:   req.SenderUserID,
			Type:     domain.UpdateEventNewMessage,
			Pts:      1,
			PtsCount: 1,
			Date:     req.Date,
			Message:  s.sendResult.SenderMessage,
		}
	}
	return s.sendResult, nil
}

func (s *captureMessages) SetChatTheme(_ context.Context, userID int64, req domain.SetPrivateChatThemeRequest) (domain.SetPrivateChatThemeResult, error) {
	s.setThemeUserID = userID
	s.setThemeReq = req
	if s.setThemeRes.OwnerUserID == 0 {
		s.setThemeRes = domain.SetPrivateChatThemeResult{
			OwnerUserID: userID,
			Peer:        req.Peer,
			Emoticon:    req.Emoticon,
			Changed:     true,
			Send: domain.SendPrivateTextResult{
				SenderMessage: domain.Message{
					ID:          1,
					OwnerUserID: userID,
					Peer:        req.Peer,
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: userID},
					Date:        req.Date,
					Out:         true,
					Media: &domain.MessageMedia{
						Kind: domain.MessageMediaKindService,
						ServiceAction: &domain.MessageServiceAction{
							Kind:              domain.MessageServiceActionSetChatTheme,
							ChatThemeEmoticon: req.Emoticon,
						},
					},
					Pts: 1,
				},
				SenderEvent: domain.UpdateEvent{
					UserID:   userID,
					Type:     domain.UpdateEventNewMessage,
					Pts:      1,
					PtsCount: 1,
					Date:     req.Date,
				},
			},
		}
		s.setThemeRes.Send.SenderEvent.Message = s.setThemeRes.Send.SenderMessage
	}
	return s.setThemeRes, nil
}

func (s *captureMessages) ForwardPrivateMessages(_ context.Context, userID int64, req domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error) {
	s.forwardUserID = userID
	s.forwardReq = req
	if len(s.forwardRes.SenderMessages) == 0 {
		s.forwardRes.OwnerUserID = userID
		s.forwardRes.SenderMessages = make([]domain.Message, 0, len(req.MessageIDs))
		s.forwardRes.SenderEvents = make([]domain.UpdateEvent, 0, len(req.MessageIDs))
		for i := range req.MessageIDs {
			msg := domain.Message{
				ID:          i + 1,
				OwnerUserID: req.OwnerUserID,
				RandomID:    req.RandomIDs[i],
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.ToUserID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID},
				Date:        req.Date,
				Out:         true,
				Silent:      req.Silent,
				NoForwards:  req.NoForwards,
				Body:        "forwarded",
				ReplyTo:     req.ReplyTo,
				Forward:     &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeUser, ID: req.FromPeer.ID}, Date: req.Date - 1},
				Pts:         i + 1,
			}
			event := domain.UpdateEvent{
				UserID:   req.OwnerUserID,
				Type:     domain.UpdateEventNewMessage,
				Pts:      msg.Pts,
				PtsCount: 1,
				Date:     req.Date,
				Message:  msg,
			}
			s.forwardRes.SenderMessages = append(s.forwardRes.SenderMessages, msg)
			s.forwardRes.SenderEvents = append(s.forwardRes.SenderEvents, event)
		}
	}
	return s.forwardRes, nil
}

func (s *captureMessages) GetMessages(_ context.Context, _ int64, ids []int) (domain.MessageList, error) {
	s.getMessagesCalls++
	s.getMessagesIDs = append(s.getMessagesIDs, append([]int(nil), ids...))
	byID := make(map[int]domain.Message, len(s.list.Messages))
	for _, msg := range s.list.Messages {
		byID[msg.ID] = msg
	}
	out := domain.MessageList{Messages: make([]domain.Message, 0, len(ids)), Users: s.list.Users}
	if s.getMessagesListed {
		for _, msg := range s.list.Messages {
			for _, id := range ids {
				if msg.ID == id {
					out.Messages = append(out.Messages, msg)
					break
				}
			}
		}
		return out, nil
	}
	for _, id := range ids {
		if msg, ok := byID[id]; ok {
			out.Messages = append(out.Messages, msg)
		}
	}
	return out, nil
}

func (s *captureMessages) GetHistory(_ context.Context, _ int64, filter domain.MessageFilter) (domain.MessageList, error) {
	s.filter = filter
	return s.list, nil
}

func (s *captureMessages) Search(_ context.Context, _ int64, filter domain.MessageFilter) (domain.MessageList, error) {
	s.filter = filter
	return s.list, nil
}

func (s *captureMessages) SearchPrivateMedia(_ context.Context, _, _ int64, _ domain.MediaSearchRequest) (domain.MessageList, error) {
	return domain.MessageList{}, nil
}

func (s *captureMessages) CountPrivateMediaCategories(_ context.Context, _, _ int64) (domain.MediaCategoryCounts, error) {
	return domain.MediaCategoryCounts{}, nil
}

func (s *captureMessages) ReadHistory(_ context.Context, _ int64, req domain.ReadHistoryRequest) (domain.ReadHistoryResult, error) {
	s.readReq = req
	s.readPeer = req.Peer
	s.readMaxID = req.MaxID
	if s.readResult.OwnerUserID == 0 {
		s.readResult.OwnerUserID = req.OwnerUserID
	}
	if s.readResult.Peer.ID == 0 {
		s.readResult.Peer = req.Peer
	}
	if s.readResult.MaxID == 0 {
		s.readResult.MaxID = req.MaxID
	}
	return s.readResult, nil
}

func (s *captureMessages) ReadMessageContents(_ context.Context, userID int64, req domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error) {
	s.readContentsReq = req
	if s.readContentsRes.OwnerUserID == 0 {
		s.readContentsRes.OwnerUserID = userID
	}
	return s.readContentsRes, nil
}

func (s *captureMessages) GetOutboxReadDate(_ context.Context, _ int64, req domain.OutboxReadDateRequest) (int, error) {
	s.outboxReadDateReq = req
	if s.outboxReadDate == 0 {
		return 0, domain.ErrMessageNotReadYet
	}
	return s.outboxReadDate, nil
}

func (s *captureMessages) SetMessageReactions(_ context.Context, userID int64, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	s.setReactionCalls++
	s.setReactionReq = req
	if len(s.setReactionRes.Messages) == 0 {
		if len(req.Reactions) == 0 {
			reactions := domain.ChannelMessageReactions{CanSeeList: true, Results: []domain.ChannelMessageReactionCount{}, Recent: []domain.ChannelMessagePeerReaction{}}
			s.setReactionRes = domain.PrivateMessageReactionsResult{
				Messages: []domain.Message{{
					ID:          req.MessageID,
					OwnerUserID: userID,
					Peer:        req.Peer,
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.Peer.ID},
					Date:        req.Date,
					Reactions:   &reactions,
				}},
				Reactions: reactions,
			}
			return s.setReactionRes, nil
		}
		reactions := domain.ChannelMessageReactions{
			CanSeeList: true,
			Results: []domain.ChannelMessageReactionCount{{
				Reaction:    req.Reactions[0],
				Count:       1,
				ChosenOrder: 1,
			}},
			Recent: []domain.ChannelMessagePeerReaction{{
				UserID:      userID,
				Reaction:    req.Reactions[0],
				My:          true,
				Big:         req.Big,
				ChosenOrder: 1,
				Date:        req.Date,
			}},
		}
		s.setReactionRes = domain.PrivateMessageReactionsResult{
			Messages: []domain.Message{{
				ID:          req.MessageID,
				OwnerUserID: userID,
				Peer:        req.Peer,
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.Peer.ID},
				Date:        req.Date,
				Reactions:   &reactions,
			}},
			Reactions: reactions,
		}
	}
	return s.setReactionRes, nil
}

func (s *captureMessages) VoteMessagePoll(_ context.Context, _ int64, _ domain.VotePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	return domain.PrivateMessagePollResult{}, domain.ErrMessageIDInvalid
}

func (s *captureMessages) CloseMessagePoll(_ context.Context, _ int64, _ domain.ClosePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	return domain.PrivateMessagePollResult{}, domain.ErrMessageIDInvalid
}

func (s *captureMessages) GetMessageReactions(_ context.Context, userID int64, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	s.getReactionReq = req
	if len(s.getReactionRes.Messages) == 0 && len(req.IDs) > 0 {
		reactions := domain.ChannelMessageReactions{CanSeeList: true, Results: []domain.ChannelMessageReactionCount{}, Recent: []domain.ChannelMessagePeerReaction{}}
		s.getReactionRes = domain.PrivateMessageReactionsResult{
			Messages: []domain.Message{{
				ID:          req.IDs[0],
				OwnerUserID: userID,
				Peer:        req.Peer,
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.Peer.ID},
				Reactions:   &reactions,
			}},
			Reactions: reactions,
		}
	}
	return s.getReactionRes, nil
}

func (s *captureMessages) EditMessage(_ context.Context, userID int64, req domain.EditMessageRequest) (domain.EditMessageResult, error) {
	s.editReq = req
	if s.editRes.OwnerUserID == 0 {
		s.editRes.OwnerUserID = userID
	}
	if len(s.editRes.Edited) == 0 {
		msg := domain.Message{
			ID:          req.ID,
			OwnerUserID: req.OwnerUserID,
			Peer:        req.Peer,
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID},
			Date:        req.EditDate - 10,
			EditDate:    req.EditDate,
			Out:         true,
			Body:        req.Message,
			Entities:    append([]domain.MessageEntity(nil), req.Entities...),
			Pts:         7,
		}
		s.editRes.Edited = []domain.EditedMessageForUser{{
			UserID:  req.OwnerUserID,
			Message: msg,
			Event: domain.UpdateEvent{
				UserID:   req.OwnerUserID,
				Type:     domain.UpdateEventEditMessage,
				Pts:      7,
				PtsCount: 1,
				Date:     req.EditDate,
				Message:  msg,
			},
		}}
	}
	return s.editRes, nil
}

func (s *captureMessages) DeleteMessages(_ context.Context, userID int64, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error) {
	s.deleteMessagesReq = req
	if s.deleteMessagesRes.OwnerUserID == 0 {
		s.deleteMessagesRes.OwnerUserID = userID
	}
	return s.deleteMessagesRes, nil
}

func (s *captureMessages) GetSavedDialogs(_ context.Context, _ int64, _ domain.SavedDialogsFilter) (domain.SavedDialogList, error) {
	return domain.SavedDialogList{}, nil
}

func (s *captureMessages) GetPinnedSavedDialogs(_ context.Context, _ int64) (domain.SavedDialogList, error) {
	return domain.SavedDialogList{}, nil
}

func (s *captureMessages) GetSavedDialogsByPeers(_ context.Context, _ int64, _ []domain.Peer) (domain.SavedDialogList, error) {
	return domain.SavedDialogList{}, nil
}

func (s *captureMessages) ToggleSavedDialogPin(_ context.Context, _ int64, _ domain.Peer, _ bool) (bool, error) {
	return false, nil
}

func (s *captureMessages) ReorderPinnedSavedDialogs(_ context.Context, _ int64, _ []domain.Peer, _ bool) error {
	return nil
}

func (s *captureMessages) DeleteSavedHistory(_ context.Context, _ int64, _ domain.DeleteSavedHistoryRequest) (domain.DeleteSavedHistoryResult, error) {
	return domain.DeleteSavedHistoryResult{}, nil
}

func (s *captureMessages) DeleteHistory(_ context.Context, userID int64, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error) {
	s.deleteHistoryReq = req
	if s.deleteHistoryRes.OwnerUserID == 0 {
		s.deleteHistoryRes.OwnerUserID = userID
	}
	return s.deleteHistoryRes, nil
}

func (s *captureMessages) PinPrivateMessage(_ context.Context, userID int64, req domain.PinPrivateMessageRequest) (domain.PinPrivateMessageResult, error) {
	return domain.PinPrivateMessageResult{OwnerUserID: userID}, nil
}

func (s *captureMessages) UnpinAllPrivateMessages(_ context.Context, userID int64, req domain.UnpinAllPrivateMessagesRequest) (domain.PinPrivateMessageResult, error) {
	return domain.PinPrivateMessageResult{OwnerUserID: userID}, nil
}

func (s *captureMessages) ListUnreadReactionMessages(_ context.Context, _ int64, _ domain.Peer, _ int) ([]domain.Message, error) {
	return nil, nil
}

func (s *captureMessages) ReadPeerReactions(_ context.Context, _ int64, _ domain.Peer) (int, error) {
	return 0, nil
}
