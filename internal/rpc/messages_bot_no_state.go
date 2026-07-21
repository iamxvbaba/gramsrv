package rpc

import (
	"context"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesSavePreparedInlineMessage(ctx context.Context, req *tg.MessagesSavePreparedInlineMessageRequest) (*tg.MessagesBotPreparedInlineMessage, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || req.Result == nil {
		return nil, resultIDInvalidErr()
	}
	if r.deps.Users == nil {
		return nil, internalErr()
	}
	currentUserID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	target, found, err := r.userFromInput(ctx, currentUserID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, userIDInvalidErr()
	}
	result, err := r.domainInlineResultFromTG(ctx, botID, req.Result)
	if err != nil {
		return nil, err
	}
	if err := r.prepareTelegramLoginMarkup(ctx, botID, result.ReplyMarkup); err != nil {
		return nil, replyMarkupErr(err)
	}
	peerTypes, err := preparedInlinePeerTypesFromTG(req.PeerTypes)
	if err != nil {
		return nil, err
	}
	id, expireDate := r.inlines.savePreparedInlineContext(ctx, r.clock.Now(), botID, target.ID, result, peerTypes)
	return &tg.MessagesBotPreparedInlineMessage{
		ID:         id,
		ExpireDate: expireDate,
	}, nil
}

func preparedInlinePeerTypesFromTG(in []tg.InlineQueryPeerTypeClass) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, peerType := range in {
		value := storeInlineQueryPeerType(peerType)
		if value == "" {
			return nil, peerIDInvalidErr()
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func tgPreparedInlinePeerTypes(in []string) []tg.InlineQueryPeerTypeClass {
	if len(in) == 0 {
		return []tg.InlineQueryPeerTypeClass{}
	}
	out := make([]tg.InlineQueryPeerTypeClass, 0, len(in))
	for _, peerType := range in {
		if value, ok := tgInlineQueryPeerTypeFromStore(peerType); ok {
			out = append(out, value)
		}
	}
	return out
}

func (r *Router) onMessagesEditInlineBotMessage(ctx context.Context, req *tg.MessagesEditInlineBotMessageRequest) (bool, error) {
	botID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req == nil {
		return false, messageIDInvalidErr()
	}
	if botID == 0 || !r.userIsBot(ctx, botID) || r.deps.Messages == nil {
		return false, messageIDInvalidErr()
	}
	target, found, err := r.privateMessageFromInlineID(ctx, botID, req.ID)
	if err != nil {
		return false, err
	}
	if found {
		return r.editPrivateInlineBotMessage(ctx, botID, target, req)
	}
	_, channelTarget, found, err := r.channelMessageFromInlineID(ctx, botID, req.ID)
	if err != nil {
		return false, err
	}
	if found {
		return r.editChannelInlineBotMessage(ctx, botID, channelTarget, req)
	}
	return false, messageIDInvalidErr()
}

func (r *Router) editPrivateInlineBotMessage(ctx context.Context, botID int64, target domain.Message, req *tg.MessagesEditInlineBotMessageRequest) (bool, error) {
	newMedia, err := r.inlineEditMedia(ctx, target.OwnerUserID, req)
	if err != nil {
		return false, err
	}
	message := target.Body
	entities := append([]domain.MessageEntity(nil), target.Entities...)
	richMessage := target.RichMessage
	setRichMessage := false
	rawRichMessage, hasRichMessage := req.GetRichMessage()
	rawMessage, hasMessage := req.GetMessage()
	if hasMessage && hasRichMessage {
		return false, mediaInvalidErr()
	}
	if hasRichMessage {
		richMessage, err = r.domainRichMessageFromInput(ctx, rawRichMessage)
		if err != nil {
			return false, err
		}
		if richMessage.IsZero() {
			return false, richMessageInvalidErr()
		}
		message, entities, setRichMessage = "", nil, true
	} else if hasMessage {
		if rawMessage == "" && newMedia == nil && target.Media.IsZero() {
			return false, messageEmptyErr()
		}
		if utf8.RuneCountInString(rawMessage) > maxSendMessageTextLength {
			return false, messageTooLongErr()
		}
		rawEntities, _ := req.GetEntities()
		if len(rawEntities) > maxMessageEntityCount {
			return false, entitiesTooLongErr()
		}
		message = rawMessage
		entities = domainMessageEntitiesForViewer(botID, rawEntities)
		richMessage, setRichMessage = nil, true
	} else if req.ReplyMarkup == nil && newMedia == nil {
		return false, messageNotModifiedErr()
	}
	var replyMarkup *domain.MessageReplyMarkup
	setReplyMarkup := false
	if req.ReplyMarkup != nil {
		var err error
		replyMarkup, err = domainReplyMarkupForSender(req.ReplyMarkup, true)
		if err != nil {
			return false, replyMarkupErr(err)
		}
		if _, ok := req.ReplyMarkup.(*tg.ReplyInlineMarkup); ok {
			setReplyMarkup = true
		}
	}
	if setReplyMarkup {
		if err := r.prepareTelegramLoginMarkup(ctx, botID, replyMarkup); err != nil {
			return false, replyMarkupErr(err)
		}
	}
	_, err = r.deps.Messages.EditMessage(ctx, target.OwnerUserID, domain.EditMessageRequest{
		OwnerUserID:     target.OwnerUserID,
		Peer:            target.Peer,
		ID:              target.ID,
		Message:         message,
		Entities:        entities,
		Media:           newMedia,
		EditDate:        int(r.clock.Now().Unix()),
		SetReplyMarkup:  setReplyMarkup,
		ReplyMarkup:     replyMarkup,
		SetRichMessage:  setRichMessage,
		RichMessage:     richMessage,
		ViaBotEditBotID: botID,
	})
	if err != nil {
		return false, messageEditErr(err)
	}
	return true, nil
}

func (r *Router) editChannelInlineBotMessage(ctx context.Context, botID int64, target domain.ChannelMessage, req *tg.MessagesEditInlineBotMessageRequest) (bool, error) {
	if r.deps.Channels == nil {
		return false, messageIDInvalidErr()
	}
	newMedia, err := r.inlineEditMedia(ctx, target.SenderUserID, req)
	if err != nil {
		return false, err
	}
	message := target.Body
	entities := append([]domain.MessageEntity(nil), target.Entities...)
	var mentionUserIDs []int64
	richMessage := target.RichMessage
	setRichMessage := false
	rawRichMessage, hasRichMessage := req.GetRichMessage()
	rawMessage, hasMessage := req.GetMessage()
	if hasMessage && hasRichMessage {
		return false, mediaInvalidErr()
	}
	if hasRichMessage {
		richMessage, err = r.domainRichMessageFromInput(ctx, rawRichMessage)
		if err != nil {
			return false, err
		}
		if richMessage.IsZero() {
			return false, richMessageInvalidErr()
		}
		message, entities, setRichMessage = "", nil, true
	} else if hasMessage {
		if rawMessage == "" && newMedia == nil && target.Media.IsZero() {
			return false, messageEmptyErr()
		}
		if utf8.RuneCountInString(rawMessage) > maxSendMessageTextLength {
			return false, messageTooLongErr()
		}
		rawEntities, _ := req.GetEntities()
		if len(rawEntities) > maxMessageEntityCount {
			return false, entitiesTooLongErr()
		}
		message = rawMessage
		entities = domainMessageEntitiesForViewer(botID, rawEntities)
		richMessage, setRichMessage = nil, true
		var err error
		mentionUserIDs, err = r.mentionedUserIDsFromMessage(ctx, botID, message, rawEntities)
		if err != nil {
			return false, err
		}
	} else {
		if req.ReplyMarkup == nil && newMedia == nil {
			return false, messageNotModifiedErr()
		}
		mentionUserIDs, err = r.mentionedUserIDsFromDomainMessage(ctx, botID, message, entities)
		if err != nil {
			return false, err
		}
	}
	var replyMarkup *domain.MessageReplyMarkup
	setReplyMarkup := false
	if req.ReplyMarkup != nil {
		var err error
		replyMarkup, err = domainReplyMarkupForSender(req.ReplyMarkup, true)
		if err != nil {
			return false, replyMarkupErr(err)
		}
		if _, ok := req.ReplyMarkup.(*tg.ReplyInlineMarkup); ok {
			setReplyMarkup = true
		}
	}
	if setReplyMarkup {
		if err := r.prepareTelegramLoginMarkup(ctx, botID, replyMarkup); err != nil {
			return false, replyMarkupErr(err)
		}
	}
	res, err := r.deps.Channels.EditInlineBotMessage(ctx, botID, domain.EditChannelMessageRequest{
		UserID:          target.SenderUserID,
		ChannelID:       target.ChannelID,
		ID:              target.ID,
		Message:         message,
		Entities:        entities,
		Media:           newMedia,
		MentionUserIDs:  mentionUserIDs,
		EditDate:        int(r.clock.Now().Unix()),
		SetReplyMarkup:  setReplyMarkup,
		ReplyMarkup:     replyMarkup,
		SetRichMessage:  setRichMessage,
		RichMessage:     richMessage,
		ViaBotEditBotID: botID,
	})
	if err != nil {
		return false, channelEditErr(err)
	}
	r.enqueueChannelEditMessageFanout(ctx, target.SenderUserID, res)
	return true, nil
}

func (r *Router) inlineEditMedia(ctx context.Context, userID int64, req *tg.MessagesEditInlineBotMessageRequest) (*domain.MessageMedia, error) {
	input, ok := req.GetMedia()
	if !ok || editMessageMediaCanDegradeToText(input) {
		return nil, nil
	}
	media, err := r.resolveInputMedia(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	if !inlineEditMediaAllowed(media) {
		return nil, mediaInvalidErr()
	}
	return media, nil
}

func inlineEditMediaAllowed(media *domain.MessageMedia) bool {
	if media == nil || media.IsZero() {
		return false
	}
	switch media.Kind {
	case domain.MessageMediaKindPhoto,
		domain.MessageMediaKindDocument,
		domain.MessageMediaKindContact,
		domain.MessageMediaKindGeo,
		domain.MessageMediaKindVenue:
		return true
	default:
		return false
	}
}

func (r *Router) mentionedUserIDsFromDomainMessage(ctx context.Context, currentUserID int64, message string, entities []domain.MessageEntity) ([]int64, error) {
	if r.deps.Users == nil {
		return nil, nil
	}
	identity, _ := r.deps.Users.(UserIdentityService)
	seen := make(map[int64]struct{})
	out := make([]int64, 0)
	add := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, entity := range entities {
		if entity.Type == domain.MessageEntityMentionName {
			add(entity.UserID)
			if len(out) >= domain.MaxChannelMentionRecipients {
				return out, nil
			}
		}
	}
	if identity != nil {
		for _, username := range extractMentionUsernames(message, domain.MaxChannelMentionRecipients-len(out)) {
			user, found, err := identity.ResolveUsername(ctx, currentUserID, username)
			if err != nil {
				return nil, internalErr()
			}
			if found {
				add(user.ID)
			}
			if len(out) >= domain.MaxChannelMentionRecipients {
				return out, nil
			}
		}
	}
	return out, nil
}

func (r *Router) onMessagesSetBotShippingResults(ctx context.Context, req *tg.MessagesSetBotShippingResultsRequest) (bool, error) {
	if _, err := r.callerBotID(ctx); err != nil {
		return false, err
	}
	return false, queryIDInvalidErr()
}

func (r *Router) onMessagesSetBotPrecheckoutResults(ctx context.Context, req *tg.MessagesSetBotPrecheckoutResultsRequest) (bool, error) {
	if _, err := r.callerBotID(ctx); err != nil {
		return false, err
	}
	return false, queryIDInvalidErr()
}
