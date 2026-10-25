package rpc

import (
	"context"
	"fmt"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type captureMessages struct {
	deliveryOutbox    store.DeliveryOutboxStore
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
	getReactionReq    domain.PrivateMessageReactionsRequest
	getReactionRes    domain.PrivateMessageReactionsResult
	savedTagPeer      domain.Peer
	savedTags         []domain.SavedReactionTag
	updatedSavedTag   domain.SavedReactionTag
	savedTagErr       error
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
	mediaReq          domain.MediaSearchRequest
}

func (*captureMessages) ScheduleMessage(context.Context, int64, domain.ScheduleMessageRequest, store.DeliveryEffectsBuilder[domain.ScheduledMessage]) (domain.ScheduledMessage, error) {
	return domain.ScheduledMessage{}, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) EditScheduledMessage(context.Context, int64, domain.EditScheduledMessageRequest, store.DeliveryEffectsBuilder[domain.ScheduledMessage]) (domain.ScheduledMessage, error) {
	return domain.ScheduledMessage{}, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) ListScheduledMessages(context.Context, int64, domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return domain.ScheduledMessageList{}, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) GetScheduledMessages(context.Context, int64, domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return domain.ScheduledMessageList{}, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) DeleteScheduledMessages(context.Context, int64, domain.ScheduledMessageFilter, int, store.DeliveryEffectsBuilder[[]domain.ScheduledMessage]) ([]domain.ScheduledMessage, error) {
	return nil, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) ClaimScheduledMessages(context.Context, int64, domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	return nil, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) ClaimDueScheduledMessages(context.Context, int, int, int, []int64) ([]domain.ScheduledMessage, error) {
	return nil, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) ReleaseScheduledMessage(context.Context, int64, int, int64, string) error {
	return store.ErrDeliveryOutboxRequired
}

func (*captureMessages) HasScheduledMessages(context.Context, int64, domain.Peer) (bool, error) {
	return false, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) GetPrivateHistoryTTL(context.Context, int64, domain.Peer) (int, error) {
	return 0, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) SetPrivateHistoryTTL(context.Context, int64, domain.Peer, int, store.DeliveryEffectsBuilder[domain.PrivateHistoryTTLResult]) error {
	return store.ErrDeliveryOutboxRequired
}

func (*captureMessages) DefaultHistoryTTL(context.Context, int64) (int, error) {
	return 0, store.ErrDeliveryOutboxRequired
}

func (*captureMessages) SetDefaultHistoryTTL(context.Context, int64, int) error {
	return store.ErrDeliveryOutboxRequired
}

func (*captureMessages) ClaimExpiredPrivateMessages(context.Context, int, int) ([]domain.DeleteMessagesRequest, error) {
	return nil, store.ErrDeliveryOutboxRequired
}

func applyCaptureDeliveryEffects(ctx context.Context, outbox store.DeliveryOutboxStore, effects []store.DeliveryEffect) error {
	for i := range effects {
		if err := effects[i].Validate(); err != nil {
			return fmt.Errorf("capture delivery effect %d: %w", i, err)
		}
	}
	if len(effects) > 0 && outbox == nil {
		return store.ErrDeliveryOutboxRequired
	}
	for i, effect := range effects {
		if effect.Kind != store.DeliveryEffectAbsolute {
			return fmt.Errorf("capture delivery effect %d: unsupported kind %d", i, effect.Kind)
		}
		if _, err := outbox.Enqueue(ctx, store.DeliveryOutboxEnqueue{
			TargetUserID: effect.TargetUserID, ExcludeAuthKeyID: effect.ExcludeAuthKeyID,
			ExcludeSessionID: effect.ExcludeSessionID, Payload: effect.Payload,
			RecoveryPolicy: effect.RecoveryPolicy,
		}); err != nil {
			return err
		}
	}
	return nil
}

func attachCaptureMessagesDeliveryOutbox(messages *captureMessages) *memory.DeliveryOutboxStore {
	outbox := memory.NewDeliveryOutboxStore()
	messages.deliveryOutbox = outbox
	return outbox
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

func (s *ttlCaptureMessages) SetPrivateHistoryTTL(ctx context.Context, userID int64, peer domain.Peer, period int, effects store.DeliveryEffectsBuilder[domain.PrivateHistoryTTLResult]) error {
	if effects == nil {
		return store.ErrDeliveryOutboxRequired
	}
	intents, err := effects(domain.PrivateHistoryTTLResult{OwnerUserID: userID, Peer: peer, Period: period})
	if err != nil {
		return err
	}
	if err := applyCaptureDeliveryEffects(ctx, s.deliveryOutbox, intents); err != nil {
		return err
	}
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

func (s *scheduledCaptureMessages) ScheduleMessage(ctx context.Context, userID int64, req domain.ScheduleMessageRequest, effects store.DeliveryEffectsBuilder[domain.ScheduledMessage]) (domain.ScheduledMessage, error) {
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
	if effects == nil {
		return domain.ScheduledMessage{}, store.ErrDeliveryOutboxRequired
	}
	intents, err := effects(msg)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if err := applyCaptureDeliveryEffects(ctx, s.deliveryOutbox, intents); err != nil {
		return domain.ScheduledMessage{}, err
	}
	s.scheduled = append(s.scheduled, msg)
	return msg, nil
}

func (s *scheduledCaptureMessages) EditScheduledMessage(ctx context.Context, userID int64, req domain.EditScheduledMessageRequest, effects store.DeliveryEffectsBuilder[domain.ScheduledMessage]) (domain.ScheduledMessage, error) {
	s.editScheduledReq = req
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	for i := range s.scheduled {
		if s.scheduled[i].OwnerUserID == req.OwnerUserID && s.scheduled[i].Peer == req.Peer && s.scheduled[i].ID == req.ID {
			candidate := s.scheduled[i]
			if req.SetMessage {
				candidate.Message = req.Message
				candidate.Entities = append([]domain.MessageEntity(nil), req.Entities...)
			}
			candidate.ScheduleDate = req.ScheduleDate
			candidate.UpdatedAt = req.Date
			if effects == nil {
				return domain.ScheduledMessage{}, store.ErrDeliveryOutboxRequired
			}
			intents, err := effects(candidate)
			if err != nil {
				return domain.ScheduledMessage{}, err
			}
			if err := applyCaptureDeliveryEffects(ctx, s.deliveryOutbox, intents); err != nil {
				return domain.ScheduledMessage{}, err
			}
			s.scheduled[i] = candidate
			return candidate, nil
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

func (s *scheduledCaptureMessages) DeleteScheduledMessages(ctx context.Context, userID int64, filter domain.ScheduledMessageFilter, _ int, effects store.DeliveryEffectsBuilder[[]domain.ScheduledMessage]) ([]domain.ScheduledMessage, error) {
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	s.deletedScheduled = filter
	deleted := s.matchScheduled(filter)
	if len(deleted) > 0 {
		if effects == nil {
			return nil, store.ErrDeliveryOutboxRequired
		}
		intents, err := effects(deleted)
		if err != nil {
			return nil, err
		}
		if err := applyCaptureDeliveryEffects(ctx, s.deliveryOutbox, intents); err != nil {
			return nil, err
		}
	}
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

func (s *scheduledCaptureMessages) ClaimDueScheduledMessages(context.Context, int, int, int, []int64) ([]domain.ScheduledMessage, error) {
	return nil, nil
}

// SendPrivateText mirrors the store's scheduled fence: a scheduled send also
// commits the sent transition and its durable completion delivery.
func (s *scheduledCaptureMessages) SendPrivateText(ctx context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	res, err := s.captureMessages.SendPrivateText(ctx, userID, req)
	if err != nil || req.ScheduledDelivery == nil {
		return res, err
	}
	delivery := req.ScheduledDelivery
	if delivery.Payload == nil {
		return domain.SendPrivateTextResult{}, store.ErrDeliveryOutboxRequired
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID}
	if items := s.matchScheduled(domain.ScheduledMessageFilter{OwnerUserID: userID, Peer: peer, IDs: []int{delivery.ID}}); len(items) > 0 {
		sent := items[0]
		sent.State = "sent"
		sent.SentMessageID = res.SenderMessage.ID
		payload, err := delivery.Payload(domain.ScheduledCompletion{Sent: sent})
		if err != nil {
			return domain.SendPrivateTextResult{}, err
		}
		if err := applyCaptureDeliveryEffects(ctx, s.deliveryOutbox, []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: userID, ExcludeAuthKeyID: delivery.ExcludeAuthKeyID, ExcludeSessionID: delivery.ExcludeSessionID,
			Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}); err != nil {
			return domain.SendPrivateTextResult{}, err
		}
	}
	s.markedScheduledID = delivery.ID
	s.markedSentID = res.SenderMessage.ID
	s.removeScheduled(domain.ScheduledMessageFilter{OwnerUserID: userID, Peer: peer, IDs: []int{delivery.ID}})
	return res, nil
}

func (s *scheduledCaptureMessages) ReleaseScheduledMessage(_ context.Context, _ int64, id int, _ int64, errText string) error {
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

func (s *captureMessages) SearchPrivateMedia(_ context.Context, _, _ int64, req domain.MediaSearchRequest) (domain.MessageList, error) {
	s.mediaReq = req
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

func (s *captureMessages) SetMessageReactions(ctx context.Context, userID int64, req domain.SetPrivateMessageReactionsRequest, effects store.DeliveryEffectsBuilder[domain.PrivateMessageReactionsResult]) (domain.PrivateMessageReactionsResult, error) {
	result := s.setReactionRes
	if len(result.Messages) == 0 {
		if len(req.Reactions) == 0 {
			reactions := domain.ChannelMessageReactions{
				CanSeeList: req.Peer.ID != userID,
				AsTags:     req.Peer.ID == userID,
				Results:    []domain.ChannelMessageReactionCount{},
				Recent:     []domain.ChannelMessagePeerReaction{},
			}
			result = domain.PrivateMessageReactionsResult{
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
		} else {
			reactions := domain.ChannelMessageReactions{
				CanSeeList: req.Peer.ID != userID,
				AsTags:     req.Peer.ID == userID,
				Results: []domain.ChannelMessageReactionCount{{
					Reaction:    req.Reactions[0],
					Count:       1,
					ChosenOrder: 1,
				}},
			}
			if req.Peer.ID != userID {
				reactions.Recent = []domain.ChannelMessagePeerReaction{{
					UserID:      userID,
					Reaction:    req.Reactions[0],
					My:          true,
					Big:         req.Big,
					ChosenOrder: 1,
					Date:        req.Date,
				}}
			}
			result = domain.PrivateMessageReactionsResult{
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
	}
	if effects == nil {
		return domain.PrivateMessageReactionsResult{}, store.ErrDeliveryOutboxRequired
	}
	intents, err := effects(result)
	if err != nil {
		return domain.PrivateMessageReactionsResult{}, err
	}
	if err := applyCaptureDeliveryEffects(ctx, s.deliveryOutbox, intents); err != nil {
		return domain.PrivateMessageReactionsResult{}, err
	}
	s.setReactionReq = req
	s.setReactionRes = result
	return result, nil
}

func (s *captureMessages) VoteMessagePoll(_ context.Context, _ int64, _ domain.VotePrivateMessagePollRequest, _ store.DeliveryEffectsBuilder[domain.PrivateMessagePollResult]) (domain.PrivateMessagePollResult, error) {
	return domain.PrivateMessagePollResult{}, domain.ErrMessageIDInvalid
}

func (s *captureMessages) CloseMessagePoll(_ context.Context, _ int64, _ domain.ClosePrivateMessagePollRequest, _ store.DeliveryEffectsBuilder[domain.PrivateMessagePollResult]) (domain.PrivateMessagePollResult, error) {
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

func (s *captureMessages) SavedReactionTags(_ context.Context, _ int64, savedPeer domain.Peer, _ int) ([]domain.SavedReactionTag, error) {
	s.savedTagPeer = savedPeer
	return append([]domain.SavedReactionTag(nil), s.savedTags...), s.savedTagErr
}

func (s *captureMessages) UpdateSavedReactionTag(ctx context.Context, _ int64, tag domain.SavedReactionTag, effects store.DeliveryEffectsBuilder[domain.SavedReactionTag]) error {
	if s.savedTagErr != nil {
		return s.savedTagErr
	}
	if effects == nil {
		return store.ErrDeliveryOutboxRequired
	}
	intents, err := effects(tag)
	if err != nil {
		return err
	}
	if err := applyCaptureDeliveryEffects(ctx, s.deliveryOutbox, intents); err != nil {
		return err
	}
	s.updatedSavedTag = tag
	for i := range s.savedTags {
		if s.savedTags[i].Reaction.Key() == tag.Reaction.Key() {
			s.savedTags[i].Title = tag.Title
			return nil
		}
	}
	return nil
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

func (s *captureMessages) MutateSavedDialogs(_ context.Context, _ int64, _ store.SavedDialogMutation, _ store.DeliveryEffectsBuilder[store.SavedDialogMutationSnapshot]) (store.SavedDialogMutationSnapshot, error) {
	return store.SavedDialogMutationSnapshot{}, nil
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
