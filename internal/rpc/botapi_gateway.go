package rpc

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var botAPIAuthKeyID = [8]byte{'B', 'O', 'T', 'A', 'P', 'I', 0, 1}

const botAPIChannelChatIDBase int64 = 1000000000000

// BotAPISelf returns the authenticated bot as a domain user.
func (r *Router) BotAPISelf(ctx context.Context, botID int64) (domain.User, error) {
	if r == nil || r.deps.Users == nil || botID == 0 {
		return domain.User{}, errors.New("BOT_INVALID")
	}
	u, found, err := r.deps.Users.ByID(ctx, botID, botID)
	if err != nil {
		return domain.User{}, err
	}
	if !found || !u.Bot {
		return domain.User{}, errors.New("BOT_INVALID")
	}
	return u, nil
}

// BotAPIUpdates returns durable update_id based events projected for the HTTP
// Bot API. New deployments use the dedicated Bot API queue; the legacy
// user_update_events fallback is kept for tests that have not wired the queue.
func (r *Router) BotAPIUpdates(ctx context.Context, botID int64, offset int64) ([]domain.UpdateEvent, error) {
	if r == nil || botID == 0 {
		return nil, nil
	}
	if r.deps.BotAPIUpdates != nil {
		return r.botAPIQueuedUpdates(ctx, botID, offset)
	}
	if r.deps.Updates == nil {
		return nil, nil
	}
	fromPts := 0
	if offset > 0 {
		fromPts = int(offset - 1)
	} else if st, found, err := r.deps.Updates.ConfirmedState(ctx, botAPIAuthKeyID, botID); err != nil {
		return nil, err
	} else if found {
		fromPts = st.Pts
	}
	diff, err := r.deps.Updates.GetDifference(ctx, botAPIAuthKeyID, botID, domain.UpdateState{Pts: fromPts})
	if err != nil {
		return nil, err
	}
	if len(diff.Events) == 0 {
		return nil, nil
	}
	return r.enrichUpdateEvents(ctx, botID, diff.Events), nil
}

func (r *Router) BotAPISetAllowedUpdates(ctx context.Context, botID int64, allowed []domain.BotAPIUpdateKind) error {
	if r == nil || r.deps.BotAPIUpdates == nil || botID == 0 {
		return nil
	}
	return r.deps.BotAPIUpdates.SetBotAPIAllowedUpdates(ctx, botID, allowed)
}

func (r *Router) BotAPIDropPendingUpdates(ctx context.Context, botID int64) error {
	if r == nil || r.deps.BotAPIUpdates == nil || botID == 0 {
		return nil
	}
	return r.deps.BotAPIUpdates.DropPendingBotAPIUpdates(ctx, botID)
}

func (r *Router) BotAPIPendingUpdateCount(ctx context.Context, botID int64) (int, error) {
	if r == nil || r.deps.BotAPIUpdates == nil || botID == 0 {
		return 0, nil
	}
	return r.deps.BotAPIUpdates.PendingBotAPIUpdateCount(ctx, botID)
}

func (r *Router) AcquireBotAPIPollLease(ctx context.Context, botID int64, owner string, ttl time.Duration) (bool, error) {
	leases, ok := r.deps.BotAPIUpdates.(store.BotAPIPollLeaseStore)
	if !ok || botID <= 0 {
		return true, nil
	}
	return leases.AcquireBotAPIPollLease(ctx, botID, owner, ttl)
}

func (r *Router) ReleaseBotAPIPollLease(ctx context.Context, botID int64, owner string) error {
	leases, ok := r.deps.BotAPIUpdates.(store.BotAPIPollLeaseStore)
	if !ok || botID <= 0 {
		return nil
	}
	return leases.ReleaseBotAPIPollLease(ctx, botID, owner)
}

func (r *Router) BotAPISetWebhook(ctx context.Context, config domain.BotAPIWebhook, dropPending bool) error {
	webhooks, ok := r.deps.BotAPIUpdates.(store.BotAPIWebhookStore)
	if !ok {
		return errors.New("WEBHOOK_UNSUPPORTED")
	}
	return webhooks.SetBotAPIWebhook(ctx, config, dropPending)
}

func (r *Router) BotAPIDeleteWebhook(ctx context.Context, botID int64, dropPending bool) error {
	webhooks, ok := r.deps.BotAPIUpdates.(store.BotAPIWebhookStore)
	if !ok {
		return errors.New("WEBHOOK_UNSUPPORTED")
	}
	return webhooks.DeleteBotAPIWebhook(ctx, botID, dropPending)
}

func (r *Router) BotAPIWebhook(ctx context.Context, botID int64) (domain.BotAPIWebhook, bool, error) {
	webhooks, ok := r.deps.BotAPIUpdates.(store.BotAPIWebhookStore)
	if !ok {
		return domain.BotAPIWebhook{}, false, nil
	}
	return webhooks.BotAPIWebhook(ctx, botID)
}

func (r *Router) ListDueBotAPIWebhooks(ctx context.Context, limit int) ([]domain.BotAPIWebhook, error) {
	webhooks, ok := r.deps.BotAPIUpdates.(store.BotAPIWebhookStore)
	if !ok {
		return nil, nil
	}
	return webhooks.ListDueBotAPIWebhooks(ctx, limit)
}

func (r *Router) AcquireBotAPIWebhookLease(ctx context.Context, botID int64, owner string, ttl time.Duration) (bool, error) {
	webhooks, ok := r.deps.BotAPIUpdates.(store.BotAPIWebhookStore)
	if !ok {
		return false, nil
	}
	return webhooks.AcquireBotAPIWebhookLease(ctx, botID, owner, ttl)
}

func (r *Router) ReleaseBotAPIWebhookLease(ctx context.Context, botID int64, owner string) error {
	webhooks, ok := r.deps.BotAPIUpdates.(store.BotAPIWebhookStore)
	if !ok {
		return nil
	}
	return webhooks.ReleaseBotAPIWebhookLease(ctx, botID, owner)
}

func (r *Router) RecordBotAPIWebhookFailure(ctx context.Context, botID int64, owner string, nextAttempt time.Time, message string) error {
	webhooks, ok := r.deps.BotAPIUpdates.(store.BotAPIWebhookStore)
	if !ok {
		return nil
	}
	return webhooks.RecordBotAPIWebhookFailure(ctx, botID, owner, nextAttempt, message)
}

func (r *Router) RecordBotAPIWebhookSuccess(ctx context.Context, botID int64, owner string, nextAttempt time.Time) error {
	webhooks, ok := r.deps.BotAPIUpdates.(store.BotAPIWebhookStore)
	if !ok {
		return nil
	}
	return webhooks.RecordBotAPIWebhookSuccess(ctx, botID, owner, nextAttempt)
}

func (r *Router) ConfirmBotAPIWebhookDelivery(ctx context.Context, botID, updateID int64) error {
	if r == nil || r.deps.BotAPIUpdates == nil || botID <= 0 || updateID <= 0 {
		return nil
	}
	return r.deps.BotAPIUpdates.ConfirmBotAPIUpdates(ctx, botID, updateID)
}

// BotAPISendMessage sends a text message as a bot through the normal private
// or channel message state machine. Positive chat_id is a user private chat;
// -1000000000000-channel_id is a supergroup/channel chat.
func (r *Router) BotAPISendMessage(ctx context.Context, botID, chatID int64, text string, entities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview, silent bool, replyToMessageID int) (domain.Message, error) {
	if r == nil || botID == 0 {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	if err := domain.ValidateReplyMarkup(replyMarkup); err != nil {
		return domain.Message{}, replyMarkupErr(err)
	}
	if err := r.validateReplyMarkupForPeer(ctx, botID, peer, replyMarkup); err != nil {
		return domain.Message{}, err
	}
	if text == "" {
		return domain.Message{}, errors.New("MESSAGE_EMPTY")
	}
	if utf8.RuneCountInString(text) > domain.MaxMessageTextLength {
		return domain.Message{}, errors.New("MESSAGE_TOO_LONG")
	}
	var reply *domain.MessageReply
	if replyToMessageID > 0 {
		reply = &domain.MessageReply{Peer: peer, MessageID: replyToMessageID}
	}
	if peer.Type == domain.PeerTypeChannel {
		return r.botAPISendChannelMessage(ctx, botID, peer.ID, text, entities, nil, replyMarkup, silent, reply)
	}
	if r.deps.Messages == nil {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	if r.deps.Users != nil && peer.ID != botID {
		if _, found, err := r.deps.Users.ByID(ctx, botID, peer.ID); err != nil {
			return domain.Message{}, err
		} else if !found {
			return domain.Message{}, errors.New("CHAT_ID_INVALID")
		}
	}
	res, err := r.deps.Messages.SendPrivateText(ctx, botID, domain.SendPrivateTextRequest{
		SenderUserID:    botID,
		RecipientUserID: peer.ID,
		RandomID:        randomNonZeroInt64(),
		Message:         text,
		Entities:        append([]domain.MessageEntity(nil), entities...),
		Silent:          silent,
		ReplyTo:         reply,
		Date:            int(time.Now().Unix()),
		ReplyMarkup:     replyMarkup,
	})
	if err != nil {
		return domain.Message{}, err
	}
	return res.SenderMessage, nil
}

// BotAPISendMedia sends a photo/document message through the same files service
// and private/channel message state machines used by MTProto sendMedia.
func (r *Router) BotAPISendMedia(ctx context.Context, botID, chatID int64, kind, locationKey, remoteURL, fileName, mimeType string, fileBytes []byte, caption string, entities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent bool, replyToMessageID int) (domain.Message, error) {
	if r == nil || r.deps.Files == nil || botID == 0 {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	if err := domain.ValidateReplyMarkup(replyMarkup); err != nil {
		return domain.Message{}, replyMarkupErr(err)
	}
	if err := r.validateReplyMarkupForPeer(ctx, botID, peer, replyMarkup); err != nil {
		return domain.Message{}, err
	}
	if utf8.RuneCountInString(caption) > domain.MaxMessageTextLength {
		return domain.Message{}, errors.New("MESSAGE_TOO_LONG")
	}
	media, err := r.botAPIMedia(ctx, botID, kind, locationKey, remoteURL, fileName, mimeType, fileBytes)
	if err != nil {
		return domain.Message{}, err
	}
	var reply *domain.MessageReply
	if replyToMessageID > 0 {
		reply = &domain.MessageReply{Peer: peer, MessageID: replyToMessageID}
	}
	if peer.Type == domain.PeerTypeChannel {
		return r.botAPISendChannelMessage(ctx, botID, peer.ID, caption, entities, media, replyMarkup, silent, reply)
	}
	if r.deps.Messages == nil {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	if r.deps.Users != nil && peer.ID != botID {
		if _, found, err := r.deps.Users.ByID(ctx, botID, peer.ID); err != nil {
			return domain.Message{}, err
		} else if !found {
			return domain.Message{}, errors.New("CHAT_ID_INVALID")
		}
	}
	res, err := r.deps.Messages.SendPrivateText(ctx, botID, domain.SendPrivateTextRequest{
		SenderUserID:    botID,
		RecipientUserID: peer.ID,
		RandomID:        randomNonZeroInt64(),
		Message:         caption,
		Entities:        append([]domain.MessageEntity(nil), entities...),
		Media:           media,
		Silent:          silent,
		ReplyTo:         reply,
		Date:            int(time.Now().Unix()),
		ReplyMarkup:     replyMarkup,
	})
	if err != nil {
		return domain.Message{}, err
	}
	return res.SenderMessage, nil
}

func botAPIPeerFromChatID(chatID int64) (domain.Peer, bool) {
	switch {
	case chatID > 0:
		return domain.Peer{Type: domain.PeerTypeUser, ID: chatID}, true
	case chatID < -botAPIChannelChatIDBase:
		channelID := -botAPIChannelChatIDBase - chatID
		if channelID > 0 {
			return domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, true
		}
	}
	return domain.Peer{}, false
}

func (r *Router) botAPISendChannelMessage(ctx context.Context, botID, channelID int64, text string, entities []domain.MessageEntity, media *domain.MessageMedia, replyMarkup *domain.MessageReplyMarkup, silent bool, reply *domain.MessageReply) (domain.Message, error) {
	if r.deps.Channels == nil {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	mentionUserIDs := r.mentionUserIDsFromDomain(ctx, botID, text, entities)
	res, err := r.deps.Channels.SendMessage(ctx, botID, domain.SendChannelMessageRequest{
		UserID:              botID,
		ChannelID:           channelID,
		RandomID:            randomNonZeroInt64(),
		Message:             text,
		Entities:            append([]domain.MessageEntity(nil), entities...),
		Media:               media,
		MentionUserIDs:      mentionUserIDs,
		SkipRecipientLookup: true,
		PostAuthor:          r.channelPostAuthorName(ctx, botID),
		Silent:              silent,
		ReplyTo:             reply,
		ReplyMarkup:         replyMarkup,
		Date:                int(time.Now().Unix()),
	})
	if err != nil {
		return domain.Message{}, botAPIChannelSendErr(err)
	}
	if !res.Duplicate {
		r.enqueueChannelMessageFanout(ctx, botID, res, nil)
		r.pushChannelDiscussionUpdate(ctx, botID, res.Discussion)
		r.maybeEnqueueWebPageResolve(botID, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, res.Message.ID, res.Message.Media)
	}
	return botAPIMessageFromChannel(botID, res.Message), nil
}

func botAPIMessageFromChannel(botID int64, msg domain.ChannelMessage) domain.Message {
	from := msg.From
	if from.Type == "" && msg.SenderUserID != 0 {
		from = domain.Peer{Type: domain.PeerTypeUser, ID: msg.SenderUserID}
	}
	return domain.Message{
		ID:          msg.ID,
		RandomID:    msg.RandomID,
		OwnerUserID: botID,
		Peer:        domain.Peer{Type: domain.PeerTypeChannel, ID: msg.ChannelID},
		From:        from,
		Date:        msg.Date,
		EditDate:    msg.EditDate,
		Out:         msg.SenderUserID == botID,
		Silent:      msg.Silent,
		NoForwards:  msg.NoForwards,
		Body:        msg.Body,
		Entities:    append([]domain.MessageEntity(nil), msg.Entities...),
		ReplyTo:     msg.ReplyTo,
		Forward:     msg.Forward,
		Reactions:   msg.Reactions,
		Pts:         msg.Pts,
		TTLPeriod:   msg.TTLPeriod,
		ExpiresAt:   msg.ExpiresAt,
		Media:       msg.Media,
		MediaUnread: msg.MediaUnread,
		ViaBotID:    msg.ViaBotID,
		GroupedID:   msg.GroupedID,
		ReplyMarkup: msg.ReplyMarkup,
		RichMessage: msg.RichMessage,
		Pinned:      msg.Pinned,
	}
}

func botAPIChannelSendErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrChannelInvalid),
		errors.Is(err, domain.ErrChannelPrivate),
		errors.Is(err, domain.ErrChannelUserBanned):
		return errors.New("CHAT_ID_INVALID")
	case errors.Is(err, domain.ErrChannelWriteForbidden):
		return errors.New("CHAT_WRITE_FORBIDDEN")
	case errors.Is(err, domain.ErrChannelAdminRequired):
		return errors.New("CHAT_ADMIN_REQUIRED")
	case errors.Is(err, domain.ErrReplyMessageIDInvalid):
		return errors.New("REPLY_MESSAGE_ID_INVALID")
	default:
		return channelInvalidErr(err)
	}
}

func (r *Router) botAPIMedia(ctx context.Context, botID int64, kind, locationKey, remoteURL, fileName, mimeType string, fileBytes []byte) (*domain.MessageMedia, error) {
	switch kind {
	case "photo":
		var photo domain.Photo
		var err error
		switch {
		case len(fileBytes) > 0:
			photo, err = r.deps.Files.CreatePhotoFromBytes(ctx, fileBytes)
		case remoteURL != "":
			photo, err = r.deps.Files.CreatePhotoFromURL(ctx, remoteURL)
		case locationKey != "":
			id, ok := botAPIPhotoID(locationKey)
			if !ok {
				return nil, errors.New("FILE_ID_INVALID")
			}
			var found bool
			photo, found, err = r.deps.Files.GetPhoto(ctx, id)
			if err == nil && !found {
				err = errors.New("FILE_ID_INVALID")
			}
		default:
			err = errors.New("FILE_ID_INVALID")
		}
		if err != nil {
			return nil, botAPIMediaErr(err)
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &photo}, nil
	case "document":
		var doc domain.Document
		var err error
		switch {
		case len(fileBytes) > 0:
			doc, err = r.deps.Files.CreateDocumentFromBytes(ctx, fileBytes, domain.DocumentSpec{
				MimeType:   mimeType,
				Attributes: botAPIDocumentAttributes(fileName),
				ForceFile:  true,
			})
		case remoteURL != "":
			doc, err = r.deps.Files.CreateDocumentFromURL(ctx, remoteURL)
		case locationKey != "":
			id, ok := botAPIDocumentID(locationKey)
			if !ok {
				return nil, errors.New("FILE_ID_INVALID")
			}
			var found bool
			doc, found, err = r.deps.Files.GetDocument(ctx, id)
			if err == nil && !found {
				err = errors.New("FILE_ID_INVALID")
			}
		default:
			err = errors.New("FILE_ID_INVALID")
		}
		if err != nil {
			return nil, botAPIMediaErr(err)
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &doc}, nil
	default:
		return nil, errors.New("MEDIA_INVALID")
	}
}

func botAPIPhotoID(locationKey string) (int64, bool) {
	if !strings.HasPrefix(locationKey, "photo:") {
		return 0, false
	}
	rest := strings.TrimPrefix(locationKey, "photo:")
	idText, _, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	return id, err == nil && id > 0
}

func botAPIDocumentID(locationKey string) (int64, bool) {
	if !strings.HasPrefix(locationKey, "doc:") {
		return 0, false
	}
	rest := strings.TrimPrefix(locationKey, "doc:")
	idText, _, _ := strings.Cut(rest, ":")
	id, err := strconv.ParseInt(idText, 10, 64)
	return id, err == nil && id > 0
}

func botAPIDocumentAttributes(fileName string) []domain.DocumentAttribute {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return nil
	}
	return []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: fileName}}
}

func botAPIMediaErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToUpper(err.Error()), "FILE_ID_INVALID") {
		return err
	}
	return errors.New("MEDIA_INVALID")
}

// BotAPIEditMessageText edits a bot-owned private text message through the
// normal durable edit state machine. Positive chat_id is a private user chat.
func (r *Router) BotAPIEditMessageText(ctx context.Context, botID, chatID int64, messageID int, text string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview bool) (domain.Message, error) {
	if r == nil || r.deps.Messages == nil || botID == 0 {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	if chatID <= 0 {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	if messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return domain.Message{}, errors.New("MESSAGE_ID_INVALID")
	}
	if text == "" {
		return domain.Message{}, errors.New("MESSAGE_EMPTY")
	}
	if utf8.RuneCountInString(text) > domain.MaxMessageTextLength {
		return domain.Message{}, errors.New("MESSAGE_TOO_LONG")
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: chatID}
	res, err := r.deps.Messages.EditMessage(ctx, botID, domain.EditMessageRequest{
		OwnerUserID:    botID,
		Peer:           peer,
		ID:             messageID,
		Message:        text,
		Entities:       append([]domain.MessageEntity(nil), entities...),
		EditDate:       int(time.Now().Unix()),
		SetReplyMarkup: setReplyMarkup,
		ReplyMarkup:    replyMarkup,
	})
	if err != nil {
		return domain.Message{}, err
	}
	self := res.Self()
	if self.Message.ID == 0 {
		return domain.Message{}, errors.New("MESSAGE_ID_INVALID")
	}
	return self.Message, nil
}

func (r *Router) BotAPIEditInlineMessageText(ctx context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, text string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview bool) (bool, error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return false, errors.New("BOT_INVALID")
	}
	if text == "" {
		return false, errors.New("MESSAGE_EMPTY")
	}
	if utf8.RuneCountInString(text) > domain.MaxMessageTextLength {
		return false, errors.New("MESSAGE_TOO_LONG")
	}
	if err := domain.ValidateReplyMarkup(replyMarkup); err != nil {
		return false, replyMarkupErr(err)
	}
	req := &tg.MessagesEditInlineBotMessageRequest{
		ID:        tgInputBotInlineMessageID(inlineMessageID),
		NoWebpage: disableWebPagePreview,
	}
	req.SetMessage(text)
	if len(entities) > 0 {
		req.SetEntities(tgMessageEntities(entities))
	}
	if setReplyMarkup {
		wire := tgReplyMarkup(replyMarkup)
		if wire == nil {
			wire = &tg.ReplyInlineMarkup{}
		}
		req.SetReplyMarkup(wire)
	}
	return r.onMessagesEditInlineBotMessage(WithUserID(ctx, botID), req)
}

// BotAPIDeleteMessage deletes a bot-owned private message with revoke=true so
// the target user's MTProto clients observe the normal delete update.
func (r *Router) BotAPIDeleteMessage(ctx context.Context, botID, chatID int64, messageID int) (bool, error) {
	if r == nil || r.deps.Messages == nil || botID == 0 {
		return false, errors.New("BOT_INVALID")
	}
	if chatID <= 0 {
		return false, errors.New("CHAT_ID_INVALID")
	}
	if messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return false, errors.New("MESSAGE_ID_INVALID")
	}
	_, err := r.deps.Messages.DeleteMessages(ctx, botID, domain.DeleteMessagesRequest{
		OwnerUserID: botID,
		IDs:         []int{messageID},
		Revoke:      true,
		Date:        int(time.Now().Unix()),
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// BotAPIAnswerCallbackQuery bridges Bot API answerCallbackQuery to the same
// process-local callback registry used by messages.setBotCallbackAnswer.
func (r *Router) BotAPIAnswerCallbackQuery(ctx context.Context, botID int64, callbackQueryID, text, url string, showAlert bool, cacheTime int) (bool, error) {
	if r == nil || r.callbacks == nil || botID == 0 {
		return false, errors.New("BOT_INVALID")
	}
	queryID, err := strconv.ParseInt(callbackQueryID, 10, 64)
	if err != nil || queryID == 0 {
		return false, errors.New("QUERY_ID_INVALID")
	}
	if utf8.RuneCountInString(text) > domain.MaxBotCallbackAnswerLen {
		return false, errors.New("MESSAGE_TOO_LONG")
	}
	if cacheTime < 0 {
		cacheTime = 0
	}
	resolved, resolveErr := r.callbacks.resolveContext(ctx, botID, queryID, domain.BotCallbackAnswer{
		Alert:     showAlert,
		Message:   text,
		URL:       url,
		CacheTime: cacheTime,
	})
	if resolveErr != nil {
		return false, resolveErr
	}
	if !resolved {
		return false, errors.New("QUERY_ID_INVALID")
	}
	return true, nil
}

// BotAPIGetFile exposes the existing upload.getFile blob location space to the
// HTTP file endpoint after the bot token has authenticated the request.
func (r *Router) BotAPIGetFile(ctx context.Context, botID int64, locationKey string, offset int64, limit int) (domain.FileChunk, bool, error) {
	if r == nil || r.deps.Files == nil || botID == 0 {
		return domain.FileChunk{}, false, nil
	}
	if limit <= 0 || limit > maxUploadGetFileChunkLimit {
		limit = maxUploadGetFileChunkLimit
	}
	if offset < 0 {
		offset = 0
	}
	return r.deps.Files.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: locationKey,
		Offset:      offset,
		Limit:       limit,
	})
}
