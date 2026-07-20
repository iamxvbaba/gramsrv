package rpc

import (
	"context"
	"errors"
	"time"

	"telesrv/internal/domain"
)

const (
	botAPIGetUpdatesLimit   = 100
	botAPIMaxNegativeOffset = 10000
)

type botAPIChannelBotMemberProvider interface {
	ActiveBotMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error)
}

func (r *Router) botAPIQueuedUpdates(ctx context.Context, botID int64, offset int64) ([]domain.UpdateEvent, error) {
	if r == nil || r.deps.BotAPIUpdates == nil || botID == 0 {
		return nil, nil
	}
	fromID := int64(1)
	var items []domain.BotAPIUpdate
	if offset < 0 {
		if offset < -botAPIMaxNegativeOffset {
			return nil, errors.New("OFFSET_INVALID")
		}
		var err error
		items, err = r.deps.BotAPIUpdates.ListTailBotAPIUpdates(ctx, botID, int(-offset), botAPIGetUpdatesLimit)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 && items[0].ID > 1 {
			if err := r.deps.BotAPIUpdates.ConfirmBotAPIUpdates(ctx, botID, items[0].ID-1); err != nil {
				return nil, err
			}
		}
	} else if offset > 0 {
		if err := r.deps.BotAPIUpdates.ConfirmBotAPIUpdates(ctx, botID, offset-1); err != nil {
			return nil, err
		}
	}
	if offset >= 0 {
		confirmed, found, err := r.deps.BotAPIUpdates.ConfirmedBotAPIUpdateID(ctx, botID)
		if err != nil {
			return nil, err
		}
		if found {
			fromID = confirmed + 1
		}
		items, err = r.deps.BotAPIUpdates.ListBotAPIUpdates(ctx, botID, fromID, botAPIGetUpdatesLimit)
		if err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	events, leadingSkipped := r.botAPIQueuedUpdateEvents(ctx, botID, items, r.clock.Now())
	if leadingSkipped > 0 {
		if err := r.deps.BotAPIUpdates.ConfirmBotAPIUpdates(ctx, botID, leadingSkipped); err != nil {
			return nil, err
		}
	}
	if len(events) == 0 {
		return nil, nil
	}
	return r.enrichUpdateEvents(ctx, botID, events), nil
}

func (r *Router) botAPIQueuedUpdateEvents(ctx context.Context, botID int64, items []domain.BotAPIUpdate, now time.Time) ([]domain.UpdateEvent, int64) {
	privateIDs := make([]int, 0)
	privateSeen := make(map[int]struct{})
	channelIDs := make(map[int64][]int)
	channelSeen := make(map[int64]map[int]struct{})
	for _, item := range items {
		if _, ok := botAPIQueuedUpdateKind(botID, item, now); !ok {
			continue
		}
		if item.Ephemeral != nil {
			continue
		}
		if item.Callback != nil && item.Callback.InlineMessage != nil {
			continue
		}
		switch item.Peer.Type {
		case domain.PeerTypeUser:
			if _, exists := privateSeen[item.MessageID]; !exists {
				privateSeen[item.MessageID] = struct{}{}
				privateIDs = append(privateIDs, item.MessageID)
			}
		case domain.PeerTypeChannel:
			seen := channelSeen[item.Peer.ID]
			if seen == nil {
				seen = make(map[int]struct{})
				channelSeen[item.Peer.ID] = seen
			}
			if _, exists := seen[item.MessageID]; !exists {
				seen[item.MessageID] = struct{}{}
				channelIDs[item.Peer.ID] = append(channelIDs[item.Peer.ID], item.MessageID)
			}
		}
	}

	privateMessages := r.botAPIQueuedPrivateMessages(ctx, botID, privateIDs)
	channelMessages := r.botAPIQueuedChannelMessages(ctx, botID, channelIDs)
	events := make([]domain.UpdateEvent, 0, len(items))
	leadingSkipped := int64(0)
	for _, item := range items {
		event, ok := botAPIQueuedUpdateEventFromMessages(botID, item, privateMessages, channelMessages, now)
		if !ok {
			if len(events) == 0 {
				leadingSkipped = item.ID
			}
			continue
		}
		events = append(events, event)
	}
	return events, leadingSkipped
}

func (r *Router) botAPIQueuedPrivateMessages(ctx context.Context, botID int64, ids []int) map[int]domain.Message {
	if r == nil || r.deps.Messages == nil || len(ids) == 0 {
		return nil
	}
	list, err := r.deps.Messages.GetMessages(ctx, botID, ids)
	if err != nil {
		return nil
	}
	out := make(map[int]domain.Message, len(list.Messages))
	for _, msg := range list.Messages {
		if msg.ID <= 0 || msg.OwnerUserID != botID {
			continue
		}
		out[msg.ID] = msg
	}
	return out
}

func (r *Router) botAPIQueuedChannelMessages(ctx context.Context, botID int64, idsByChannel map[int64][]int) map[int64]map[int]domain.ChannelMessage {
	if r == nil || r.deps.Channels == nil || len(idsByChannel) == 0 {
		return nil
	}
	out := make(map[int64]map[int]domain.ChannelMessage, len(idsByChannel))
	for channelID, ids := range idsByChannel {
		if channelID == 0 || len(ids) == 0 {
			continue
		}
		history, err := r.deps.Channels.GetMessages(ctx, botID, channelID, ids)
		if err != nil {
			continue
		}
		byID := make(map[int]domain.ChannelMessage, len(history.Messages))
		for _, msg := range history.Messages {
			if msg.ID <= 0 || msg.Deleted || msg.Action != nil {
				continue
			}
			byID[msg.ID] = msg
		}
		if len(byID) > 0 {
			out[channelID] = byID
		}
	}
	return out
}

func botAPIQueuedUpdateKind(botID int64, item domain.BotAPIUpdate, now time.Time) (domain.UpdateEventType, bool) {
	if item.ID <= 0 || item.BotUserID != botID {
		return "", false
	}
	eventType, ok := botAPIUpdateEventType(item.Kind)
	if !ok {
		return "", false
	}
	if item.Ephemeral != nil && !botAPIQueuedEphemeralValid(botID, item, now) {
		return "", false
	}
	if item.Kind == domain.BotAPIUpdateCallbackQuery {
		if item.Date <= 0 || !now.Before(time.Unix(int64(item.Date), 0).Add(botCallbackTimeout)) {
			return "", false
		}
		cb := item.Callback
		if cb == nil || cb.ID == 0 || cb.BotUserID != botID || cb.UserID <= 0 ||
			cb.ChatInstance == 0 || len(cb.Data) > domain.MaxCallbackDataLen {
			return "", false
		}
		if cb.InlineMessage != nil {
			inline := cb.InlineMessage
			if item.MessageID != 0 || item.Peer != (domain.Peer{}) || cb.MessageID != 0 || cb.Peer != (domain.Peer{}) ||
				inline.DCID <= 0 || inline.OwnerID == 0 || inline.ID <= 0 || inline.AccessHash == 0 {
				return "", false
			}
			return eventType, true
		}
		if item.MessageID <= 0 || cb.Peer != item.Peer || cb.MessageID != item.MessageID {
			return "", false
		}
	} else if item.MessageID <= 0 {
		return "", false
	}
	switch item.Peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		if item.Peer.ID <= 0 {
			return "", false
		}
	default:
		return "", false
	}
	return eventType, true
}

func botAPIQueuedEphemeralValid(botID int64, item domain.BotAPIUpdate, now time.Time) bool {
	if item.Ephemeral == nil {
		return true
	}
	message := item.Ephemeral.Message
	if item.Ephemeral.Validate() != nil || item.SourcePts != 0 || item.Peer.Type != domain.PeerTypeChannel || item.Peer.ID <= 0 ||
		message.ID != item.MessageID || message.Peer != item.Peer || message.Expired(now) ||
		message.SenderUserID <= 0 || message.ReceiverUserID <= 0 {
		return false
	}
	if item.Kind == domain.BotAPIUpdateCallbackQuery {
		return message.SenderUserID == botID
	}
	return message.ReceiverUserID == botID
}

func botAPIQueuedUpdateEventFromMessages(botID int64, item domain.BotAPIUpdate, privateMessages map[int]domain.Message, channelMessages map[int64]map[int]domain.ChannelMessage, now time.Time) (domain.UpdateEvent, bool) {
	eventType, ok := botAPIQueuedUpdateKind(botID, item, now)
	if !ok {
		return domain.UpdateEvent{}, false
	}
	if item.Ephemeral != nil {
		message := item.Ephemeral.EphemeralMessage()
		event := domain.UpdateEvent{
			UserID: botID, Type: eventType, Date: item.Date, Peer: item.Peer,
			BotAPIUpdateID: item.ID, EphemeralMessage: &message,
		}
		if eventType == domain.UpdateEventBotCallbackQuery {
			callback := *item.Callback
			callback.Data = append([]byte(nil), item.Callback.Data...)
			event.BotCallbackQuery = &callback
		}
		return event, true
	}
	if eventType == domain.UpdateEventBotCallbackQuery && item.Callback.InlineMessage != nil {
		callback := *item.Callback
		callback.Data = append([]byte(nil), item.Callback.Data...)
		inline := *item.Callback.InlineMessage
		callback.InlineMessage = &inline
		return domain.UpdateEvent{
			UserID:           botID,
			Type:             eventType,
			Pts:              int(item.ID),
			PtsCount:         1,
			BotAPIUpdateID:   item.ID,
			Date:             item.Date,
			BotCallbackQuery: &callback,
		}, true
	}
	switch item.Peer.Type {
	case domain.PeerTypeUser:
		msg, found := privateMessages[item.MessageID]
		if !found {
			return domain.UpdateEvent{}, false
		}
		if eventType == domain.UpdateEventBotCallbackQuery {
			callback := *item.Callback
			callback.Data = append([]byte(nil), item.Callback.Data...)
			return domain.UpdateEvent{
				UserID:           botID,
				Type:             eventType,
				Pts:              int(item.ID),
				PtsCount:         1,
				BotAPIUpdateID:   item.ID,
				Date:             item.Date,
				Peer:             item.Peer,
				Message:          msg,
				BotCallbackQuery: &callback,
			}, true
		}
		if msg.Out || !botAPIMessageProjectable(msg) {
			return domain.UpdateEvent{}, false
		}
		msg.Pts = int(item.ID)
		return domain.UpdateEvent{
			UserID:         botID,
			Type:           eventType,
			Pts:            int(item.ID),
			PtsCount:       1,
			BotAPIUpdateID: item.ID,
			Date:           item.Date,
			Peer:           msg.Peer,
			Message:        msg,
		}, true
	case domain.PeerTypeChannel:
		msg, found := channelMessages[item.Peer.ID][item.MessageID]
		if !found {
			return domain.UpdateEvent{}, false
		}
		projected := botAPIMessageFromChannel(botID, msg)
		if eventType == domain.UpdateEventBotCallbackQuery {
			callback := *item.Callback
			callback.Data = append([]byte(nil), item.Callback.Data...)
			return domain.UpdateEvent{
				UserID:           botID,
				Type:             eventType,
				Pts:              int(item.ID),
				PtsCount:         1,
				BotAPIUpdateID:   item.ID,
				Date:             item.Date,
				Peer:             item.Peer,
				Message:          projected,
				BotCallbackQuery: &callback,
			}, true
		}
		if projected.Out || !botAPIMessageProjectable(projected) {
			return domain.UpdateEvent{}, false
		}
		projected.Pts = int(item.ID)
		return domain.UpdateEvent{
			UserID:         botID,
			Type:           eventType,
			Pts:            int(item.ID),
			PtsCount:       1,
			BotAPIUpdateID: item.ID,
			Date:           item.Date,
			Peer:           projected.Peer,
			Message:        projected,
		}, true
	default:
		return domain.UpdateEvent{}, false
	}
}

func botAPIUpdateEventType(kind domain.BotAPIUpdateKind) (domain.UpdateEventType, bool) {
	switch kind {
	case domain.BotAPIUpdateMessage:
		return domain.UpdateEventNewMessage, true
	case domain.BotAPIUpdateEditedMessage:
		return domain.UpdateEventEditMessage, true
	case domain.BotAPIUpdateCallbackQuery:
		return domain.UpdateEventBotCallbackQuery, true
	default:
		return "", false
	}
}

// enqueueBotAPIPrivateMessageUpdateAsync 把私聊消息的 Bot API 队列写入投给后台
// dispatcher（性能审计 H2）：发送者 RPC 不再为 bot 判定 miss / INSERT 多等 PG 往返。
// dispatcher 未启动（测试/未装配）时同步执行，行为不变。
func (r *Router) enqueueBotAPIPrivateMessageUpdateAsync(ctx context.Context, res domain.SendPrivateTextResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || res.Duplicate || res.RecipientMessage.ID <= 0 {
		return
	}
	r.botAPIEnqueueQueue.Enqueue(ctx, func(jobCtx context.Context) {
		r.enqueueBotAPIPrivateMessageUpdate(jobCtx, res)
	})
}

// enqueueBotAPIPrivateEditUpdatesAsync 同上，覆盖私聊编辑的 edited_message 队列写入。
func (r *Router) enqueueBotAPIPrivateEditUpdatesAsync(ctx context.Context, res domain.EditMessageResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || len(res.Edited) == 0 {
		return
	}
	r.botAPIEnqueueQueue.Enqueue(ctx, func(jobCtx context.Context) {
		r.enqueueBotAPIPrivateEditUpdates(jobCtx, res)
	})
}

func (r *Router) enqueueBotAPIPrivateMessageUpdate(ctx context.Context, res domain.SendPrivateTextResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || res.Duplicate || res.RecipientMessage.ID <= 0 || res.RecipientMessage.Out {
		return
	}
	botID := res.RecipientMessage.OwnerUserID
	if botID == 0 || !botAPIMessageProjectable(res.RecipientMessage) {
		return
	}
	isBot, err := r.botAPIKnownBot(ctx, botID)
	if err != nil || !isBot {
		return
	}
	if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
		BotUserID: botID,
		Kind:      domain.BotAPIUpdateMessage,
		Peer:      res.RecipientMessage.Peer,
		MessageID: res.RecipientMessage.ID,
		SourcePts: res.RecipientEvent.Pts,
		Date:      res.RecipientMessage.Date,
	}); err == nil && created {
		r.notifyBotAPIUpdate(botID)
	}
}

func (r *Router) enqueueBotAPIPrivateEditUpdates(ctx context.Context, res domain.EditMessageResult) {
	if r == nil || r.deps.BotAPIUpdates == nil {
		return
	}
	for _, item := range res.Edited {
		if item.UserID == 0 || item.Message.ID <= 0 || item.Message.Out || !botAPIMessageProjectable(item.Message) {
			continue
		}
		isBot, err := r.botAPIKnownBot(ctx, item.UserID)
		if err != nil || !isBot {
			continue
		}
		if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: item.UserID,
			Kind:      domain.BotAPIUpdateEditedMessage,
			Peer:      item.Message.Peer,
			MessageID: item.Message.ID,
			SourcePts: item.Event.Pts,
			Date:      item.Message.EditDate,
		}); err == nil && created {
			r.notifyBotAPIUpdate(item.UserID)
		}
	}
}

func (r *Router) enqueueBotAPIChannelMessageUpdate(ctx context.Context, originUserID int64, res domain.SendChannelMessageResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || r.deps.Channels == nil || res.Duplicate || res.Message.ID <= 0 || res.Message.ChannelID == 0 {
		return
	}
	botIDs, err := r.botAPIChannelBotCandidates(ctx, originUserID, res.Message.ChannelID)
	if err != nil {
		return
	}
	r.enqueueBotAPIChannelMessageUpdateForBots(ctx, res, botIDs)
}

func (r *Router) enqueueBotAPIChannelMessageUpdateForBots(ctx context.Context, res domain.SendChannelMessageResult, botIDs []int64) {
	if r == nil || r.deps.BotAPIUpdates == nil || res.Duplicate || res.Message.ID <= 0 || res.Message.ChannelID == 0 || len(botIDs) == 0 {
		return
	}
	skip := skipDeliverySet(res.SkipDeliveryUserIDs)
	for _, botID := range botIDs {
		if botID == 0 || botID == res.Message.SenderUserID {
			continue
		}
		if _, skipped := skip[botID]; skipped {
			continue
		}
		if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: botID,
			Kind:      domain.BotAPIUpdateMessage,
			Peer:      domain.Peer{Type: domain.PeerTypeChannel, ID: res.Message.ChannelID},
			MessageID: res.Message.ID,
			SourcePts: res.Event.Pts,
			Date:      res.Message.Date,
		}); err == nil && created {
			r.notifyBotAPIUpdate(botID)
		}
	}
}

func (r *Router) enqueueBotAPIChannelMessagesUpdate(ctx context.Context, originUserID int64, results []domain.SendChannelMessageResult) {
	candidates := make(map[int64][]int64)
	for _, res := range results {
		if r == nil || r.deps.BotAPIUpdates == nil || r.deps.Channels == nil || res.Duplicate || res.Message.ID <= 0 || res.Message.ChannelID == 0 {
			continue
		}
		botIDs, ok := candidates[res.Message.ChannelID]
		if !ok {
			loaded, err := r.botAPIChannelBotCandidates(ctx, originUserID, res.Message.ChannelID)
			if err != nil {
				candidates[res.Message.ChannelID] = nil
				continue
			}
			botIDs = loaded
			candidates[res.Message.ChannelID] = botIDs
		}
		r.enqueueBotAPIChannelMessageUpdateForBots(ctx, res, botIDs)
	}
}

func (r *Router) enqueueBotAPIChannelEditMessageUpdate(ctx context.Context, originUserID int64, res domain.EditChannelMessageResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || r.deps.Channels == nil || res.Message.ID <= 0 || res.Message.ChannelID == 0 || res.Event.Pts == 0 {
		return
	}
	botIDs, err := r.botAPIChannelBotCandidates(ctx, originUserID, res.Message.ChannelID)
	if err != nil {
		return
	}
	date := res.Message.EditDate
	if date == 0 {
		date = res.Message.Date
	}
	for _, botID := range botIDs {
		if botID == 0 || botID == res.Message.SenderUserID {
			continue
		}
		if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: botID,
			Kind:      domain.BotAPIUpdateEditedMessage,
			Peer:      domain.Peer{Type: domain.PeerTypeChannel, ID: res.Message.ChannelID},
			MessageID: res.Message.ID,
			SourcePts: res.Event.Pts,
			Date:      date,
		}); err == nil && created {
			r.notifyBotAPIUpdate(botID)
		}
	}
}

func (r *Router) botAPIChannelBotCandidates(ctx context.Context, viewerUserID, channelID int64) ([]int64, error) {
	if r == nil || r.deps.Channels == nil || channelID == 0 {
		return nil, nil
	}
	provider, ok := r.deps.Channels.(botAPIChannelBotMemberProvider)
	if !ok {
		return nil, nil
	}
	return provider.ActiveBotMemberIDs(ctx, viewerUserID, channelID, domain.MaxSynchronousChannelDialogFanout)
}

func (r *Router) botAPIKnownBot(ctx context.Context, botID int64) (bool, error) {
	if botID == 0 {
		return false, nil
	}
	if r.deps.Bots != nil {
		if _, found, err := r.deps.Bots.BotInfo(ctx, botID); err != nil || found {
			return found, err
		}
	}
	return r.userIsBot(ctx, botID), nil
}

func botAPIMessageProjectable(msg domain.Message) bool {
	if msg.ID <= 0 || msg.Out {
		return false
	}
	if msg.Body != "" {
		return true
	}
	return botAPIMessageMediaProjectable(msg.Media)
}

func botAPIMessageMediaProjectable(media *domain.MessageMedia) bool {
	if media.IsZero() {
		return false
	}
	switch media.Kind {
	case domain.MessageMediaKindPhoto:
		return media.Photo != nil
	case domain.MessageMediaKindDocument:
		return media.Document != nil
	case domain.MessageMediaKindContact:
		return media.Contact != nil
	case domain.MessageMediaKindGeo:
		return media.Geo != nil
	case domain.MessageMediaKindVenue:
		return media.Venue != nil
	case domain.MessageMediaKindPoll:
		return media.Poll != nil
	case domain.MessageMediaKindGeoLive:
		return media.GeoLive != nil
	case domain.MessageMediaKindService:
		if media.ServiceAction == nil {
			return false
		}
		switch media.ServiceAction.Kind {
		case domain.MessageServiceActionWebViewDataSent:
			return media.ServiceAction.WebViewData != nil
		case domain.MessageServiceActionRequestedPeer:
			return botAPIRequestedPeerProjectable(media.ServiceAction.RequestedPeer)
		default:
			return false
		}
	default:
		return false
	}
}

func botAPIRequestedPeerProjectable(action *domain.MessageRequestedPeerAction) bool {
	if action == nil || action.ButtonID == 0 || len(action.Peers) == 0 || len(action.Peers) > domain.MaxBotRequestedPeerQuantity {
		return false
	}
	details := make(map[domain.Peer]struct{}, len(action.Details))
	for _, detail := range action.Details {
		if detail.Peer.ID == 0 || (detail.Peer.Type != domain.PeerTypeUser && detail.Peer.Type != domain.PeerTypeChannel) {
			return false
		}
		details[detail.Peer] = struct{}{}
	}
	requiresDetails := action.NameRequested || action.UsernameRequested || action.PhotoRequested
	allUsers := true
	for _, peer := range action.Peers {
		if peer.ID == 0 || (peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel) {
			return false
		}
		if requiresDetails {
			if _, ok := details[peer]; !ok {
				return false
			}
		}
		allUsers = allUsers && peer.Type == domain.PeerTypeUser
	}
	return allUsers || (len(action.Peers) == 1 && action.Peers[0].Type == domain.PeerTypeChannel)
}
