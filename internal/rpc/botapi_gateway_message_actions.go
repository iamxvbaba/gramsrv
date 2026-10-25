package rpc

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// BotAPISendChatAction implements Bot API sendChatAction. Like
// messages.setTyping it is transient: nothing is stored and no pts moves.
// A private chat needs an existing user; a channel or supergroup needs the
// bot to be an active member, so a bot cannot spray typing into chats it does
// not belong to.
func (r *Router) BotAPISendChatAction(ctx context.Context, botID, chatID int64, action domain.BotAPIChatAction, topMessageID int) (bool, error) {
	if r == nil || botID == 0 {
		return false, errors.New("BOT_INVALID")
	}
	tgAction, ok := tgBotAPIChatAction(action)
	if !ok {
		return false, errors.New("ACTION_INVALID")
	}
	if topMessageID < 0 || topMessageID > domain.MaxMessageBoxID {
		return false, errors.New("MESSAGE_THREAD_ID_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok || peer.ID == botID {
		return false, errors.New("CHAT_ID_INVALID")
	}
	date := int(r.clock.Now().Unix())
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return false, errors.New("CHAT_ID_INVALID")
		}
		view, err := r.deps.Channels.GetChannel(ctx, botID, peer.ID)
		if err != nil || view.Forbidden || view.Self.Status != domain.ChannelMemberActive {
			return false, errors.New("CHAT_ID_INVALID")
		}
		updates := &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateChannelUserTyping{
				ChannelID: peer.ID,
				FromID:    &tg.PeerUser{UserID: botID},
				TopMsgID:  topMessageID,
				Action:    tgAction,
			}},
			Date: date,
		}
		r.pushTransientChannelViewerUpdates(ctx, botID, peer.ID, nil, func(int64) *tg.Updates {
			return updates
		})
		return true, nil
	}
	if r.deps.Users == nil {
		return false, errors.New("BOT_INVALID")
	}
	if _, found, err := r.deps.Users.ByID(ctx, botID, peer.ID); err != nil {
		return false, err
	} else if !found {
		return false, errors.New("CHAT_ID_INVALID")
	}
	r.pushTypingUpdate(ctx, peer.ID, &tg.UpdateShort{
		Update: &tg.UpdateUserTyping{UserID: botID, TopMsgID: topMessageID, Action: tgAction},
		Date:   date,
	})
	return true, nil
}

// BotAPIEditMessageCaption implements Bot API editMessageCaption: it replaces
// the caption of a photo/document message the bot sent. Text messages have no
// caption, so they are refused instead of having their text rewritten.
func (r *Router) BotAPIEditMessageCaption(ctx context.Context, botID, chatID int64, messageID int, caption string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	return r.botAPIEditSentMessage(ctx, botID, chatID, messageID, false, caption, entities, setReplyMarkup, replyMarkup)
}

// BotAPIEditMessageReplyMarkup implements Bot API editMessageReplyMarkup: only
// the inline keyboard changes (nil removes it); text, caption and media are
// kept from the stored message.
func (r *Router) BotAPIEditMessageReplyMarkup(ctx context.Context, botID, chatID int64, messageID int, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	return r.botAPIEditSentMessage(ctx, botID, chatID, messageID, true, "", nil, true, replyMarkup)
}

// botAPIEditSentMessage runs a caption and/or keyboard edit through the same
// durable private/channel edit paths as editMessageText, so pts, outbox and
// Egress delivery stay identical. preserveText keeps the stored text and entities.
func (r *Router) botAPIEditSentMessage(ctx context.Context, botID, chatID int64, messageID int, preserveText bool, caption string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	if r == nil || botID == 0 {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	if messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return domain.Message{}, errors.New("MESSAGE_ID_INVALID")
	}
	if setReplyMarkup {
		if err := domain.ValidateReplyMarkup(replyMarkup); err != nil {
			return domain.Message{}, replyMarkupErr(err)
		}
		if err := r.validateReplyMarkupForPeer(ctx, botID, peer, replyMarkup); err != nil {
			return domain.Message{}, err
		}
	}
	if !preserveText {
		if utf8.RuneCountInString(caption) > domain.MaxEphemeralCaptionLength {
			return domain.Message{}, errors.New("MESSAGE_TOO_LONG")
		}
		captionable, err := r.botAPIMessageHasCaption(ctx, botID, peer, messageID)
		if err != nil {
			return domain.Message{}, err
		}
		if !captionable {
			return domain.Message{}, errors.New("MESSAGE_HAS_NO_CAPTION")
		}
		entities = r.augmentBotAPIAutoEntities(caption, entities)
	}
	editDate := int(r.clock.Now().Unix())
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return domain.Message{}, errors.New("CHAT_ID_INVALID")
		}
		res, err := r.deps.Channels.EditMessage(ctx, botID, domain.EditChannelMessageRequest{
			UserID: botID, ChannelID: peer.ID, ID: messageID,
			Message: caption, Entities: append([]domain.MessageEntity(nil), entities...), PreserveText: preserveText,
			SetReplyMarkup: setReplyMarkup, ReplyMarkup: replyMarkup, EditDate: editDate,
		})
		if err != nil {
			return domain.Message{}, channelEditErr(err)
		}
		if err := r.enqueueBotAPIChannelEditMessageUpdate(ctx, botID, res); err != nil {
			return domain.Message{}, err
		}
		return botAPIMessageFromChannel(botID, res.Message), nil
	}
	if r.deps.Messages == nil {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	res, err := r.deps.Messages.EditMessage(ctx, botID, domain.EditMessageRequest{
		OwnerUserID: botID, Peer: peer, ID: messageID,
		Message: caption, Entities: append([]domain.MessageEntity(nil), entities...), PreserveText: preserveText,
		SetReplyMarkup: setReplyMarkup, ReplyMarkup: replyMarkup, EditDate: editDate,
	})
	if err != nil {
		return domain.Message{}, messageEditErr(err)
	}
	if err := r.enqueueBotAPIPrivateEditUpdatesAsync(ctx, res); err != nil {
		return domain.Message{}, err
	}
	self := res.Self()
	if self.Message.ID == 0 {
		return domain.Message{}, errors.New("MESSAGE_ID_INVALID")
	}
	return self.Message, nil
}

// botAPIMessageHasCaption reports whether the bot's own copy of the message
// carries photo or document media, the only kinds with a caption. A link
// preview is media too but belongs to a text message.
func (r *Router) botAPIMessageHasCaption(ctx context.Context, botID int64, peer domain.Peer, messageID int) (bool, error) {
	var media *domain.MessageMedia
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return false, errors.New("CHAT_ID_INVALID")
		}
		history, err := r.deps.Channels.GetMessages(ctx, botID, peer.ID, []int{messageID})
		if err != nil {
			return false, channelEditErr(err)
		}
		found := false
		for _, msg := range history.Messages {
			if msg.ID == messageID {
				media, found = msg.Media, true
				break
			}
		}
		if !found {
			return false, errors.New("MESSAGE_ID_INVALID")
		}
	} else {
		if r.deps.Messages == nil {
			return false, errors.New("BOT_INVALID")
		}
		list, err := r.deps.Messages.GetMessages(ctx, botID, []int{messageID})
		if err != nil {
			return false, messageEditErr(err)
		}
		found := false
		for _, msg := range list.Messages {
			if msg.ID == messageID && msg.Peer == peer {
				media, found = msg.Media, true
				break
			}
		}
		if !found {
			return false, errors.New("MESSAGE_ID_INVALID")
		}
	}
	return media != nil && (media.Kind == domain.MessageMediaKindPhoto || media.Kind == domain.MessageMediaKindDocument), nil
}

func tgBotAPIChatAction(action domain.BotAPIChatAction) (tg.SendMessageActionClass, bool) {
	switch action {
	case domain.BotAPIChatActionTyping:
		return &tg.SendMessageTypingAction{}, true
	case domain.BotAPIChatActionUploadPhoto:
		return &tg.SendMessageUploadPhotoAction{}, true
	case domain.BotAPIChatActionRecordVideo:
		return &tg.SendMessageRecordVideoAction{}, true
	case domain.BotAPIChatActionUploadVideo:
		return &tg.SendMessageUploadVideoAction{}, true
	case domain.BotAPIChatActionRecordVoice:
		return &tg.SendMessageRecordAudioAction{}, true
	case domain.BotAPIChatActionUploadVoice:
		return &tg.SendMessageUploadAudioAction{}, true
	case domain.BotAPIChatActionUploadDocument:
		return &tg.SendMessageUploadDocumentAction{}, true
	case domain.BotAPIChatActionChooseSticker:
		return &tg.SendMessageChooseStickerAction{}, true
	case domain.BotAPIChatActionFindLocation:
		return &tg.SendMessageGeoLocationAction{}, true
	case domain.BotAPIChatActionRecordVideoNote:
		return &tg.SendMessageRecordRoundAction{}, true
	case domain.BotAPIChatActionUploadVideoNote:
		return &tg.SendMessageUploadRoundAction{}, true
	default:
		return nil, false
	}
}
