package rpc

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func (r *Router) registerEphemeral(d *tlprofile.Dispatcher) {
	registerRPC[*tg.EphemeralSendMessageRequest](d, tlprofile.SemanticMethodEphemeralSendMessage, func(ctx context.Context, request *tg.EphemeralSendMessageRequest) (any, error) {
		return r.onEphemeralSendMessage(ctx, request)
	})
	registerRPC[*tg.EphemeralDeleteMessageRequest](d, tlprofile.SemanticMethodEphemeralDeleteMessage, func(ctx context.Context, request *tg.EphemeralDeleteMessageRequest) (any, error) {
		return r.onEphemeralDeleteMessage(ctx, request)
	})
	registerRPC[*tg.EphemeralReportMessageRequest](d, tlprofile.SemanticMethodEphemeralReportMessage, func(ctx context.Context, request *tg.EphemeralReportMessageRequest) (any, error) {
		return r.onEphemeralReportMessage(ctx, request)
	})
	registerRPC[*tg.EphemeralGetCallbackAnswerRequest](d, tlprofile.SemanticMethodEphemeralGetCallbackAnswer, func(ctx context.Context, request *tg.EphemeralGetCallbackAnswerRequest) (any, error) {
		return r.onEphemeralGetCallbackAnswer(ctx, request)
	})
}

func (r *Router) onEphemeralSendMessage(ctx context.Context, request *tg.EphemeralSendMessageRequest) (tg.UpdatesClass, error) {
	if request == nil || r.deps.Ephemeral == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID <= 0 {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, request.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID <= 0 {
		return nil, peerIDInvalidErr()
	}
	receiver, found, err := r.userFromInput(ctx, userID, request.ReceiverID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || !receiver.Bot {
		return nil, userBotInvalidErr()
	}
	content, err := r.domainEphemeralInputContent(ctx, userID, request)
	if err != nil {
		return nil, err
	}
	topMessageID, replyID, err := ephemeralReplyFromInput(request.ReplyTo)
	if err != nil {
		return nil, err
	}
	queryID, _ := request.GetQueryID()
	authKeyID, authKeyOK := AuthKeyIDFrom(ctx)
	sessionID, sessionOK := SessionIDFrom(ctx)
	if !authKeyOK || authKeyID == ([8]byte{}) || !sessionOK || sessionID == 0 {
		return nil, internalErr()
	}
	message, fresh, err := r.deps.Ephemeral.SendFromClient(ctx, domain.SendClientEphemeralRequest{
		SenderUserID: userID, ReceiverBotID: receiver.ID, Peer: peer,
		QueryID: queryID, RandomID: request.RandomID, TopMessageID: topMessageID,
		ReplyToEphemeralID: replyID, Content: content,
		OriginDevice: domain.EphemeralDevice{UserID: userID, BusinessAuthKeyID: authKeyID, SessionID: sessionID},
	})
	if err != nil {
		return nil, ephemeralRPCError(err)
	}
	if fresh && r.deps.BotAPIUpdates != nil {
		if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: receiver.ID,
			Kind:      domain.BotAPIUpdateMessage,
			Peer:      message.Peer,
			MessageID: message.ID,
			Date:      message.Date,
			Ephemeral: domain.NewBotAPIEphemeralPayload(message),
		}); err != nil {
			r.log.Warn("enqueue bot api ephemeral message", zap.Int64("bot_user_id", receiver.ID), zap.Int("ephemeral_message_id", message.ID), zap.Error(err))
			return nil, internalErr()
		} else if created {
			r.notifyBotAPIUpdate(receiver.ID)
		}
	}
	if fresh {
		// OriginDevice belongs to the human sender and must not constrain the
		// receiving bot's sessions.
		r.publishEphemeralPush(ctx, store.EphemeralPush{
			Kind: store.EphemeralPushNew, TargetUserID: message.ReceiverUserID, Message: message,
		})
	}
	// A lost create response can be retried after the ephemeral message was
	// deleted. The random-id index deliberately returns its tombstone; reflect
	// that final fact instead of projecting an impossible empty new message.
	if message.Deleted {
		return ephemeralDeleteUpdates(message, int(r.clock.Now().Unix())), nil
	}
	return r.ephemeralMessageUpdates(ctx, userID, message, false)
}

func (r *Router) onEphemeralGetCallbackAnswer(ctx context.Context, request *tg.EphemeralGetCallbackAnswerRequest) (*tg.MessagesBotCallbackAnswer, error) {
	if request == nil || r.deps.Ephemeral == nil || request.ID <= 0 || request.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID <= 0 {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, request.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	device, err := ephemeralDeviceFromContext(ctx, userID)
	if err != nil {
		return nil, err
	}
	data, _ := request.GetData()
	callback, err := r.deps.Ephemeral.Callback(ctx, userID, device, peer, request.ID, data)
	if err != nil {
		return nil, ephemeralRPCError(err)
	}
	queryID, pending, err := r.callbacks.registerContext(ctx, r.clock.Now(), callback.BotUserID, userID, botCallbackTimeout)
	if err != nil {
		r.log.Warn("register shared ephemeral callback query", zap.Int64("bot_user_id", callback.BotUserID), zap.Error(err))
		return nil, internalErr()
	}
	defer r.callbacks.deregisterContext(context.Background(), callback.BotUserID, queryID)
	created, err := r.deps.Ephemeral.PutCallbackAction(ctx, domain.EphemeralCallbackAction{
		QueryID: queryID, BotUserID: callback.BotUserID, UserID: userID, Peer: peer,
		MessageID: request.ID, TopMessageID: callback.Message.TopMessageID, Device: callback.Device, CreatedAt: callback.OccurredAt,
		ExpiresAt: callback.OccurredAt.Add(domain.EphemeralReplyWindow),
	})
	if err != nil || !created {
		return nil, internalErr()
	}

	botCallback := domain.BotCallbackQuery{
		ID: queryID, BotUserID: callback.BotUserID, UserID: userID,
		Peer: peer, MessageID: request.ID, ChatInstance: chatInstanceForPeer(callback.BotUserID, peer),
		Data: append([]byte(nil), data...),
	}
	if r.deps.BotAPIUpdates != nil {
		if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: callback.BotUserID,
			Kind:      domain.BotAPIUpdateCallbackQuery,
			Peer:      peer,
			MessageID: request.ID,
			Date:      int(callback.OccurredAt.Unix()),
			Callback:  &botCallback,
			Ephemeral: domain.NewBotAPIEphemeralPayload(callback.Message),
		}); err != nil {
			r.log.Warn("enqueue bot api ephemeral callback query", zap.Int64("bot_user_id", callback.BotUserID), zap.Int64("query_id", queryID), zap.Error(err))
			return nil, internalErr()
		} else if created {
			r.notifyBotAPIUpdate(callback.BotUserID)
		}
	}

	r.publishEphemeralPush(ctx, store.EphemeralPush{
		Kind: store.EphemeralPushCallback, TargetUserID: callback.BotUserID,
		Message: callback.Message, Callback: &botCallback, Date: int(callback.OccurredAt.Unix()),
	})
	return r.waitBotCallbackAnswer(ctx, callback.BotUserID, queryID, pending)
}

func (r *Router) onEphemeralDeleteMessage(ctx context.Context, request *tg.EphemeralDeleteMessageRequest) (bool, error) {
	if request == nil || r.deps.Ephemeral == nil || request.ID <= 0 || request.ID > domain.MaxMessageBoxID {
		return false, messageIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID <= 0 {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, request.Peer)
	if err != nil {
		return false, err
	}
	receiver, found, err := r.userFromInput(ctx, userID, request.ReceiverID)
	if err != nil {
		return false, internalErr()
	}
	if !found {
		return false, userIDInvalidErr()
	}
	device, err := ephemeralDeviceFromContext(ctx, userID)
	if err != nil {
		return false, err
	}
	message, deleted, err := r.deps.Ephemeral.DeleteFromDevice(ctx, userID, receiver.ID, device, peer, request.ID)
	if err != nil {
		return false, ephemeralRPCError(err)
	}
	if deleted {
		for _, targetUserID := range []int64{message.SenderUserID, message.ReceiverUserID} {
			var targetAuthKey [8]byte
			if message.OriginDevice.UserID == targetUserID {
				targetAuthKey = message.OriginDevice.BusinessAuthKeyID
			}
			r.publishEphemeralPush(ctx, store.EphemeralPush{
				Kind: store.EphemeralPushDelete, TargetUserID: targetUserID,
				TargetBusinessAuthKey: targetAuthKey, Message: message,
			})
		}
	}
	return true, nil
}

func (r *Router) onEphemeralReportMessage(ctx context.Context, request *tg.EphemeralReportMessageRequest) (tg.ReportResultClass, error) {
	if request == nil || r.deps.Ephemeral == nil || request.ID <= 0 || request.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID <= 0 {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, request.Peer)
	if err != nil {
		return nil, err
	}
	device, err := ephemeralDeviceFromContext(ctx, userID)
	if err != nil {
		return nil, err
	}
	target, err := r.deps.Ephemeral.ReportTarget(ctx, userID, device, peer, request.ID)
	if err != nil {
		return nil, ephemeralRPCError(err)
	}
	if utf8.RuneCountInString(request.Message) > 1024 {
		return nil, messageTooLongErr()
	}
	result, err := reportResultForOption(string(request.Option))
	if err != nil {
		return nil, err
	}
	if _, final := result.(*tg.ReportResultReported); !final {
		return result, nil
	}
	reason, ok := moderationReasonForReportOption(string(request.Option))
	if !ok {
		return nil, tgerr.New(400, "OPTION_INVALID")
	}
	if r.deps.Moderation == nil {
		return nil, internalErr()
	}
	if _, _, err := r.deps.Moderation.ReportEphemeral(
		ctx, userID, target, reason, string(request.Option), request.Message, r.clock.Now(),
	); err != nil {
		r.log.Warn("persist ephemeral abuse report", zap.Int64("reporter_user_id", userID), zap.Int64("channel_id", peer.ID), zap.Int("ephemeral_message_id", request.ID), zap.Error(err))
		return nil, moderationReportError(err)
	}
	return result, nil
}

func (r *Router) domainEphemeralInputContent(ctx context.Context, userID int64, request *tg.EphemeralSendMessageRequest) (domain.EphemeralContent, error) {
	if !utf8.ValidString(request.Message) || utf8.RuneCountInString(request.Message) > domain.MaxMessageTextLength || len(request.Entities) > domain.MaxMessageEntityCount {
		return domain.EphemeralContent{}, messageTooLongErr()
	}
	entities := domainMessageEntitiesForViewer(userID, request.Entities)
	if len(entities) != len(request.Entities) || !validEphemeralEntityBounds(request.Message, entities) {
		return domain.EphemeralContent{}, tgerr.New(400, "ENTITY_BOUNDS_INVALID")
	}
	var media *domain.MessageMedia
	if request.Media != nil {
		resolved, err := r.resolveInputMedia(ctx, userID, request.Media)
		if err != nil {
			return domain.EphemeralContent{}, err
		}
		if !ephemeralMediaAllowed(resolved) {
			return domain.EphemeralContent{}, mediaTypeInvalidErr()
		}
		media = resolved
	}
	var markup *domain.MessageReplyMarkup
	if request.ReplyMarkup != nil {
		var err error
		markup, err = domainReplyMarkupForSender(request.ReplyMarkup, false)
		if err != nil {
			return domain.EphemeralContent{}, replyMarkupErr(err)
		}
	}
	// Layer 228 exposes f_rich_message on the request but its
	// ephemeralMessage result has no field capable of carrying that content.
	// Official TDesktop always sends an empty InputRichMessage here. Reject the
	// otherwise lossy shape instead of acknowledging content the receiver could
	// never reconstruct.
	if request.RichMessage != nil {
		return domain.EphemeralContent{}, inputConstructorInvalidErr()
	}
	if request.Message == "" && media == nil {
		return domain.EphemeralContent{}, messageEmptyErr()
	}
	content := domain.EphemeralContent{Message: request.Message, Entities: entities, Media: media, ReplyMarkup: markup}
	if domain.ValidateEphemeralContent(content) != nil {
		return domain.EphemeralContent{}, inputRequestInvalidErr()
	}
	return content, nil
}

func ephemeralReplyFromInput(reply tg.InputReplyToClass) (topMessageID, ephemeralID int, err error) {
	switch value := reply.(type) {
	case nil:
		return 0, 0, nil
	case *tg.InputReplyToEphemeralMessage:
		if value.ID <= 0 || value.ID > domain.MaxMessageBoxID {
			return 0, 0, messageIDInvalidErr()
		}
		return 0, value.ID, nil
	case *tg.InputReplyToMessage:
		topMessageID = value.ReplyToMsgID
		if explicit, ok := value.GetTopMsgID(); ok {
			topMessageID = explicit
		}
		if topMessageID <= 0 || topMessageID > domain.MaxMessageBoxID {
			return 0, 0, messageIDInvalidErr()
		}
		if value.ReplyToPeerID != nil || value.QuoteText != "" || len(value.QuoteEntities) != 0 || value.QuoteOffset != 0 ||
			value.MonoforumPeerID != nil || value.TodoItemID != 0 || len(value.PollOption) != 0 {
			return 0, 0, inputConstructorInvalidErr()
		}
		return topMessageID, 0, nil
	default:
		return 0, 0, inputConstructorInvalidErr()
	}
}

func validEphemeralEntityBounds(message string, entities []domain.MessageEntity) bool {
	utf16Length := 0
	for _, runeValue := range message {
		utf16Length++
		if runeValue > 0xffff {
			utf16Length++
		}
	}
	for _, entity := range entities {
		if entity.Offset < 0 || entity.Length <= 0 || entity.Offset > utf16Length || entity.Length > utf16Length-entity.Offset {
			return false
		}
	}
	return true
}

func ephemeralMediaAllowed(media *domain.MessageMedia) bool {
	if media == nil || media.IsZero() {
		return false
	}
	switch media.Kind {
	case domain.MessageMediaKindPhoto, domain.MessageMediaKindDocument, domain.MessageMediaKindContact,
		domain.MessageMediaKindGeo, domain.MessageMediaKindVenue:
		return true
	default:
		return false
	}
}

func (r *Router) ephemeralMessageUpdates(ctx context.Context, viewerUserID int64, message domain.EphemeralMessage, edited bool) (*tg.Updates, error) {
	if r.deps.Users == nil || r.deps.Channels == nil {
		return nil, internalErr()
	}
	users, err := r.deps.Users.ByIDs(ctx, viewerUserID, []int64{message.SenderUserID, message.ReceiverUserID})
	if err != nil {
		return nil, internalErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, viewerUserID, message.Peer.ID)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	wire := tgEphemeralMessage(viewerUserID, message)
	var update tg.UpdateClass = &tg.UpdateNewEphemeralMessage{Message: wire}
	if edited {
		update = &tg.UpdateEditEphemeralMessage{Message: wire}
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   tgUsersForViewer(viewerUserID, users),
		Chats:   []tg.ChatClass{tgChannelChatForView(viewerUserID, view)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}, nil
}

func ephemeralDeleteUpdates(message domain.EphemeralMessage, date int) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateDeleteEphemeralMessages{
			Peer: tgPeer(message.Peer), IDs: []int{message.ID},
		}},
		Date: date,
		Seq:  0,
	}
}

func tgEphemeralMessage(viewerUserID int64, message domain.EphemeralMessage) tg.EphemeralMessage {
	out := tg.EphemeralMessage{
		Out:        viewerUserID == message.SenderUserID,
		ID:         message.ID,
		FromID:     &tg.PeerUser{UserID: message.SenderUserID},
		PeerID:     tgPeer(message.Peer),
		ReceiverID: message.ReceiverUserID,
		Date:       message.Date,
		Message:    message.Content.Message,
	}
	if message.TopMessageID > 0 {
		out.SetTopMsgID(message.TopMessageID)
	}
	if len(message.Content.Entities) != 0 {
		out.SetEntities(tgMessageEntities(message.Content.Entities))
	}
	if message.Content.Media != nil && !message.Content.Media.IsZero() {
		out.SetMedia(tgMessageMedia(message.Content.Media))
	}
	if message.Content.ReplyMarkup != nil && !message.Content.ReplyMarkup.IsZero() {
		out.SetReplyMarkup(tgReplyMarkup(message.Content.ReplyMarkup))
	}
	if message.ReplyToEphemeralID > 0 {
		reply := &tg.MessageReplyHeader{ReplyToEphemeral: true}
		reply.SetReplyToMsgID(message.ReplyToEphemeralID)
		if message.TopMessageID > 0 {
			reply.ForumTopic = true
			reply.SetReplyToTopID(message.TopMessageID)
		}
		out.SetReplyTo(reply)
	}
	return out
}

func ephemeralDeviceFromContext(ctx context.Context, userID int64) (domain.EphemeralDevice, error) {
	authKeyID, authOK := AuthKeyIDFrom(ctx)
	sessionID, sessionOK := SessionIDFrom(ctx)
	if !authOK || authKeyID == ([8]byte{}) || !sessionOK || sessionID == 0 {
		return domain.EphemeralDevice{}, internalErr()
	}
	return domain.EphemeralDevice{UserID: userID, BusinessAuthKeyID: authKeyID, SessionID: sessionID}, nil
}

func ephemeralRPCError(err error) error {
	switch {
	case errors.Is(err, domain.ErrEphemeralNotFound), errors.Is(err, domain.ErrEphemeralExpired),
		errors.Is(err, domain.ErrEphemeralDeleted), errors.Is(err, domain.ErrEphemeralReplyExpired):
		return messageIDInvalidErr()
	case errors.Is(err, domain.ErrEphemeralPeerInvalid):
		return peerIDInvalidErr()
	case errors.Is(err, domain.ErrEphemeralSenderInvalid), errors.Is(err, domain.ErrEphemeralReceiverInvalid):
		return userIDInvalidErr()
	case errors.Is(err, domain.ErrEphemeralCommandInvalid):
		return tgerr.New(400, "BOT_COMMAND_INVALID")
	case errors.Is(err, domain.ErrEphemeralForbidden), errors.Is(err, domain.ErrEphemeralDeviceMismatch):
		return tgerr.New(403, "CHAT_WRITE_FORBIDDEN")
	case errors.Is(err, domain.ErrEphemeralCallbackInvalid):
		return dataInvalidErr()
	case errors.Is(err, domain.ErrEphemeralInvalid), errors.Is(err, domain.ErrEphemeralRandomIDConflict),
		errors.Is(err, domain.ErrEphemeralVersionConflict):
		return inputRequestInvalidErr()
	default:
		return internalErr()
	}
}
