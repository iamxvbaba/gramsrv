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
		return r.botAPISendChannelMessage(ctx, botID, peer.ID, text, entities, nil, nil, replyMarkup, silent, false, reply)
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

// BotAPISendRichMessage sends one durable rich message through the same
// private/channel state machines as messages.sendMessage. The HTTP input is
// parsed into canonical PageBlocks before any message row, pts or outbox entry
// is written.
func (r *Router) BotAPISendRichMessage(ctx context.Context, botID, chatID int64, input domain.BotAPIRichMessageInput, replyMarkup *domain.MessageReplyMarkup, silent, noForwards bool, replyToMessageID int, effectID int64) (domain.Message, error) {
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
	if effectID != 0 && (peer.Type != domain.PeerTypeUser || r.messageEffectInvalid(ctx, effectID)) {
		return domain.Message{}, effectIDInvalidErr()
	}
	wire, err := tgInputRichMessageFromBotAPI(input)
	if err != nil {
		return domain.Message{}, err
	}
	richMessage, err := r.domainRichMessageFromInput(ctx, wire)
	if err != nil {
		return domain.Message{}, err
	}
	if richMessage.IsZero() {
		return domain.Message{}, richMessageInvalidErr()
	}
	var reply *domain.MessageReply
	if replyToMessageID > 0 {
		reply = &domain.MessageReply{Peer: peer, MessageID: replyToMessageID}
	}
	if peer.Type == domain.PeerTypeChannel {
		return r.botAPISendChannelMessage(ctx, botID, peer.ID, "", nil, nil, richMessage, replyMarkup, silent, noForwards, reply)
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
		SenderUserID: botID, RecipientUserID: peer.ID, RandomID: randomNonZeroInt64(),
		RichMessage: richMessage, Silent: silent, NoForwards: noForwards, ReplyTo: reply,
		Date: int(time.Now().Unix()), ReplyMarkup: replyMarkup, Effect: effectID,
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
		return r.botAPISendChannelMessage(ctx, botID, peer.ID, caption, entities, media, nil, replyMarkup, silent, false, reply)
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

func (r *Router) BotAPISendEphemeral(ctx context.Context, input domain.BotAPIEphemeralSendInput) (domain.EphemeralMessage, error) {
	if r == nil || r.deps.Ephemeral == nil || input.BotUserID <= 0 || input.ReceiverUserID <= 0 {
		return domain.EphemeralMessage{}, errors.New("BOT_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(input.ChatID)
	if !ok || peer.Type != domain.PeerTypeChannel {
		return domain.EphemeralMessage{}, errors.New("CHAT_ID_INVALID")
	}
	if err := domain.ValidateReplyMarkup(input.ReplyMarkup); err != nil {
		return domain.EphemeralMessage{}, replyMarkupErr(err)
	}
	if err := r.validateReplyMarkupForPeer(ctx, input.BotUserID, peer, input.ReplyMarkup); err != nil {
		return domain.EphemeralMessage{}, err
	}
	baseContent := domain.EphemeralContent{
		Message: input.Text, Entities: append([]domain.MessageEntity(nil), input.Entities...), ReplyMarkup: input.ReplyMarkup,
	}
	if !utf8.ValidString(baseContent.Message) || utf8.RuneCountInString(baseContent.Message) > domain.MaxMessageTextLength || len(baseContent.Entities) > domain.MaxMessageEntityCount ||
		!validEphemeralEntityBounds(baseContent.Message, baseContent.Entities) {
		return domain.EphemeralMessage{}, errors.New("ENTITY_BOUNDS_INVALID")
	}
	message, _, err := r.deps.Ephemeral.SendFromBotLazy(ctx, domain.SendBotEphemeralRequest{
		BotUserID: input.BotUserID, ReceiverUserID: input.ReceiverUserID, Peer: peer,
		TopMessageID: input.TopMessageID, ReplyToEphemeralID: input.ReplyToEphemeralID,
		ActionMessageID: input.ReplyToEphemeralID, CallbackQueryID: input.CallbackQueryID,
	}, func(buildCtx context.Context) (domain.EphemeralContent, error) {
		content := baseContent
		if input.DirectMedia != nil {
			content.Media = input.DirectMedia
			if content.Media.Geo != nil && content.Media.Geo.AccessHash == 0 {
				content.Media.Geo.AccessHash, _ = randomGeoAccessHash()
			}
			if content.Media.Venue != nil && content.Media.Venue.Geo.AccessHash == 0 {
				content.Media.Venue.Geo.AccessHash, _ = randomGeoAccessHash()
			}
		} else if input.Kind != "message" {
			media, err := r.botAPIEphemeralMedia(buildCtx, input.BotUserID, input.Kind, input.File, input.SecondaryFile)
			if err != nil {
				return domain.EphemeralContent{}, err
			}
			content.Media = media
		}
		return content, nil
	})
	if err != nil {
		return domain.EphemeralMessage{}, ephemeralBotAPIError(err)
	}
	r.publishEphemeralPush(ctx, store.EphemeralPush{
		Kind: store.EphemeralPushNew, TargetUserID: message.ReceiverUserID,
		TargetBusinessAuthKey: message.OriginDevice.BusinessAuthKeyID, Message: message,
	})
	return message, nil
}

func (r *Router) BotAPIEditEphemeral(ctx context.Context, input domain.BotAPIEphemeralEditInput) (bool, error) {
	if r == nil || r.deps.Ephemeral == nil || input.BotUserID <= 0 || input.ReceiverUserID <= 0 || input.MessageID <= 0 {
		return false, errors.New("MESSAGE_ID_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(input.ChatID)
	if !ok || peer.Type != domain.PeerTypeChannel {
		return false, errors.New("CHAT_ID_INVALID")
	}
	fields := input.Fields
	if fields.SetReplyMarkup {
		if err := domain.ValidateReplyMarkup(fields.ReplyMarkup); err != nil {
			return false, replyMarkupErr(err)
		}
		if err := r.validateReplyMarkupForPeer(ctx, input.BotUserID, peer, fields.ReplyMarkup); err != nil {
			return false, err
		}
	}
	if fields.SetMessage && (!utf8.ValidString(fields.Message) || !validEphemeralEntityBounds(fields.Message, fields.Entities) || utf8.RuneCountInString(fields.Message) > domain.MaxMessageTextLength) {
		return false, errors.New("ENTITY_BOUNDS_INVALID")
	}
	message, err := r.deps.Ephemeral.EditFieldsFromBotLazy(ctx, input.BotUserID, input.ReceiverUserID, peer, input.MessageID, input.Mode, func(buildCtx context.Context) (domain.EditEphemeralFields, error) {
		built := fields
		if input.MediaKind != "" {
			media, err := r.botAPIEphemeralMedia(buildCtx, input.BotUserID, input.MediaKind, input.File, input.SecondaryFile)
			if err != nil {
				return domain.EditEphemeralFields{}, err
			}
			built.SetMedia = true
			built.Media = media
		}
		return built, nil
	})
	if err != nil {
		return false, ephemeralBotAPIError(err)
	}
	r.publishEphemeralPush(ctx, store.EphemeralPush{
		Kind: store.EphemeralPushEdit, TargetUserID: message.ReceiverUserID,
		TargetBusinessAuthKey: message.OriginDevice.BusinessAuthKeyID, Message: message,
	})
	return true, nil
}

func (r *Router) BotAPIDeleteEphemeral(ctx context.Context, botUserID, chatID, receiverUserID int64, messageID int) (bool, error) {
	peer, ok := botAPIPeerFromChatID(chatID)
	if r == nil || r.deps.Ephemeral == nil || !ok || peer.Type != domain.PeerTypeChannel {
		return false, errors.New("CHAT_ID_INVALID")
	}
	message, deleted, err := r.deps.Ephemeral.Delete(ctx, botUserID, receiverUserID, peer, messageID)
	if err != nil {
		return false, ephemeralBotAPIError(err)
	}
	if deleted {
		r.publishEphemeralPush(ctx, store.EphemeralPush{
			Kind: store.EphemeralPushDelete, TargetUserID: receiverUserID,
			TargetBusinessAuthKey: message.OriginDevice.BusinessAuthKeyID, Message: message,
		})
	}
	return true, nil
}

func ephemeralBotAPIError(err error) error {
	switch {
	case errors.Is(err, domain.ErrEphemeralNotFound), errors.Is(err, domain.ErrEphemeralExpired), errors.Is(err, domain.ErrEphemeralDeleted):
		return errors.New("EPHEMERAL_MESSAGE_ID_INVALID")
	case errors.Is(err, domain.ErrEphemeralReplyExpired):
		return errors.New("EPHEMERAL_ACTION_EXPIRED")
	case errors.Is(err, domain.ErrEphemeralPeerInvalid):
		return errors.New("CHAT_ID_INVALID")
	case errors.Is(err, domain.ErrEphemeralReceiverInvalid):
		return errors.New("USER_ID_INVALID")
	case errors.Is(err, domain.ErrEphemeralForbidden), errors.Is(err, domain.ErrEphemeralDeviceMismatch):
		return errors.New("CHAT_WRITE_FORBIDDEN")
	case errors.Is(err, domain.ErrEphemeralVersionConflict):
		return errors.New("MESSAGE_NOT_MODIFIED")
	default:
		return err
	}
}

func (r *Router) botAPIEphemeralMedia(ctx context.Context, botID int64, kind string, file, secondary domain.BotAPIFileInput) (*domain.MessageMedia, error) {
	if kind == "live_photo" {
		photo, err := r.botAPIMedia(ctx, botID, "photo", file.LocationKey, file.RemoteURL, file.FileName, file.MimeType, file.Bytes)
		if err != nil {
			return nil, err
		}
		video, err := r.botAPIDocumentMedia(ctx, botID, "video", secondary)
		if err != nil {
			return nil, err
		}
		photo.LivePhotoVideo = video.Document
		return photo, nil
	}
	if kind == "photo" {
		return r.botAPIMedia(ctx, botID, kind, file.LocationKey, file.RemoteURL, file.FileName, file.MimeType, file.Bytes)
	}
	return r.botAPIDocumentMedia(ctx, botID, kind, file)
}

func (r *Router) botAPIDocumentMedia(ctx context.Context, botID int64, kind string, file domain.BotAPIFileInput) (*domain.MessageMedia, error) {
	if r.deps.Files == nil {
		return nil, errors.New("MEDIA_INVALID")
	}
	attrs, forceFile, ok := botAPIDocumentKindAttributes(kind, file)
	if !ok {
		return nil, errors.New("MEDIA_INVALID")
	}
	var document domain.Document
	var err error
	switch {
	case len(file.Bytes) > 0:
		document, err = r.deps.Files.CreateDocumentFromBytes(ctx, file.Bytes, domain.DocumentSpec{MimeType: file.MimeType, Attributes: attrs, ForceFile: forceFile})
	case file.RemoteURL != "":
		document, err = r.deps.Files.CreateDocumentFromURL(ctx, file.RemoteURL)
		document.Attributes = mergeDocumentAttributes(document.Attributes, attrs)
	case file.LocationKey != "":
		id, valid := botAPIDocumentID(file.LocationKey)
		if !valid {
			return nil, errors.New("FILE_ID_INVALID")
		}
		var found bool
		document, found, err = r.deps.Files.GetDocument(ctx, id)
		if err == nil && !found {
			err = errors.New("FILE_ID_INVALID")
		}
	default:
		err = errors.New("FILE_ID_INVALID")
	}
	if err != nil {
		return nil, botAPIMediaErr(err)
	}
	if !botAPIDocumentMatchesKind(document, kind) {
		return nil, errors.New("MEDIA_INVALID")
	}
	return messageMediaFromDocument(document, false, 0), nil
}

func botAPIDocumentKindAttributes(kind string, file domain.BotAPIFileInput) ([]domain.DocumentAttribute, bool, bool) {
	filename := botAPIDocumentAttributes(file.FileName)
	w, h, duration := file.Width, file.Height, file.Duration
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	if duration <= 0 {
		duration = 1
	}
	switch kind {
	case "document":
		return filename, true, true
	case "animation":
		return append(filename,
			domain.DocumentAttribute{Kind: domain.DocAttrAnimated},
			domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: w, H: h, Duration: float64(duration), NoSound: true}), false, true
	case "audio":
		return append(filename, domain.DocumentAttribute{Kind: domain.DocAttrAudio, AudioDuration: duration, Title: file.Title, Performer: file.Performer}), false, true
	case "sticker":
		return append(filename, domain.DocumentAttribute{Kind: domain.DocAttrSticker, W: w, H: h, Alt: file.Emoji}), false, true
	case "video":
		return append(filename, domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: w, H: h, Duration: float64(duration), SupportsStreaming: true}), false, true
	case "video_note":
		return append(filename, domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: w, H: h, Duration: float64(duration), RoundMessage: true, SupportsStreaming: true}), false, true
	case "voice":
		return append(filename, domain.DocumentAttribute{Kind: domain.DocAttrAudio, AudioDuration: duration, Voice: true}), false, true
	default:
		return nil, false, false
	}
}

func mergeDocumentAttributes(base, additional []domain.DocumentAttribute) []domain.DocumentAttribute {
	out := append([]domain.DocumentAttribute(nil), base...)
	seen := make(map[domain.DocumentAttributeKind]struct{}, len(base)+len(additional))
	for _, attribute := range base {
		seen[attribute.Kind] = struct{}{}
	}
	for _, attribute := range additional {
		if _, exists := seen[attribute.Kind]; exists {
			continue
		}
		seen[attribute.Kind] = struct{}{}
		out = append(out, attribute)
	}
	return out
}

func botAPIDocumentMatchesKind(document domain.Document, kind string) bool {
	has := func(target domain.DocumentAttributeKind, predicate func(domain.DocumentAttribute) bool) bool {
		for _, attribute := range document.Attributes {
			if attribute.Kind == target && (predicate == nil || predicate(attribute)) {
				return true
			}
		}
		return false
	}
	switch kind {
	case "document":
		return document.ID > 0
	case "animation":
		return has(domain.DocAttrAnimated, nil)
	case "audio":
		return has(domain.DocAttrAudio, func(a domain.DocumentAttribute) bool { return !a.Voice })
	case "sticker":
		return document.IsSticker()
	case "video":
		return has(domain.DocAttrVideo, func(a domain.DocumentAttribute) bool { return !a.RoundMessage })
	case "video_note":
		return has(domain.DocAttrVideo, func(a domain.DocumentAttribute) bool { return a.RoundMessage })
	case "voice":
		return has(domain.DocAttrAudio, func(a domain.DocumentAttribute) bool { return a.Voice })
	default:
		return false
	}
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

func (r *Router) botAPISendChannelMessage(ctx context.Context, botID, channelID int64, text string, entities []domain.MessageEntity, media *domain.MessageMedia, richMessage *domain.MessageRichMessage, replyMarkup *domain.MessageReplyMarkup, silent, noForwards bool, reply *domain.MessageReply) (domain.Message, error) {
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
		RichMessage:         richMessage,
		MentionUserIDs:      mentionUserIDs,
		SkipRecipientLookup: true,
		PostAuthor:          r.channelPostAuthorName(ctx, botID),
		Silent:              silent,
		NoForwards:          noForwards,
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
	if setReplyMarkup {
		if err := domain.ValidateReplyMarkup(replyMarkup); err != nil {
			return domain.Message{}, replyMarkupErr(err)
		}
		if err := r.validateReplyMarkupForPeer(ctx, botID, peer, replyMarkup); err != nil {
			return domain.Message{}, err
		}
	}
	res, err := r.deps.Messages.EditMessage(ctx, botID, domain.EditMessageRequest{
		OwnerUserID:    botID,
		Peer:           peer,
		ID:             messageID,
		Message:        text,
		Entities:       append([]domain.MessageEntity(nil), entities...),
		EditDate:       int(time.Now().Unix()),
		SetReplyMarkup: setReplyMarkup,
		ReplyMarkup:    replyMarkup,
		// An explicit plain-text edit replaces a previous rich payload. Keeping
		// both would create a state that neither Bot API nor TDesktop permits.
		SetRichMessage: true,
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

// BotAPIEditRichMessage replaces message content with one rich payload while
// preserving the existing durable edit/pts/outbox semantics.
func (r *Router) BotAPIEditRichMessage(ctx context.Context, botID, chatID int64, messageID int, input domain.BotAPIRichMessageInput, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
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
	if err := domain.ValidateReplyMarkup(replyMarkup); err != nil {
		return domain.Message{}, replyMarkupErr(err)
	}
	if err := r.validateReplyMarkupForPeer(ctx, botID, peer, replyMarkup); err != nil {
		return domain.Message{}, err
	}
	wire, err := tgInputRichMessageFromBotAPI(input)
	if err != nil {
		return domain.Message{}, err
	}
	richMessage, err := r.domainRichMessageFromInput(ctx, wire)
	if err != nil {
		return domain.Message{}, err
	}
	if richMessage.IsZero() {
		return domain.Message{}, richMessageInvalidErr()
	}
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return domain.Message{}, errors.New("CHAT_ID_INVALID")
		}
		res, err := r.deps.Channels.EditMessage(ctx, botID, domain.EditChannelMessageRequest{
			UserID: botID, ChannelID: peer.ID, ID: messageID, Message: "",
			SetReplyMarkup: setReplyMarkup, ReplyMarkup: replyMarkup,
			SetRichMessage: true, RichMessage: richMessage, EditDate: int(time.Now().Unix()),
		})
		if err != nil {
			return domain.Message{}, channelEditErr(err)
		}
		r.enqueueChannelEditMessageFanout(ctx, botID, res)
		return botAPIMessageFromChannel(botID, res.Message), nil
	}
	if r.deps.Messages == nil {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	res, err := r.deps.Messages.EditMessage(ctx, botID, domain.EditMessageRequest{
		OwnerUserID: botID, Peer: peer, ID: messageID, Message: "", EditDate: int(time.Now().Unix()),
		SetReplyMarkup: setReplyMarkup, ReplyMarkup: replyMarkup,
		SetRichMessage: true, RichMessage: richMessage,
	})
	if err != nil {
		return domain.Message{}, err
	}
	r.enqueueBotAPIPrivateEditUpdatesAsync(ctx, res)
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
	if setReplyMarkup {
		if err := r.prepareTelegramLoginMarkup(ctx, botID, replyMarkup); err != nil {
			return false, replyMarkupErr(err)
		}
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

func (r *Router) BotAPIEditInlineRichMessage(ctx context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, input domain.BotAPIRichMessageInput, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (bool, error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return false, errors.New("BOT_INVALID")
	}
	if err := domain.ValidateReplyMarkup(replyMarkup); err != nil {
		return false, replyMarkupErr(err)
	}
	if setReplyMarkup {
		if err := r.prepareTelegramLoginMarkup(ctx, botID, replyMarkup); err != nil {
			return false, replyMarkupErr(err)
		}
	}
	wire, err := tgInputRichMessageFromBotAPI(input)
	if err != nil {
		return false, err
	}
	req := &tg.MessagesEditInlineBotMessageRequest{ID: tgInputBotInlineMessageID(inlineMessageID)}
	req.SetRichMessage(wire)
	if setReplyMarkup {
		markup := tgReplyMarkup(replyMarkup)
		if markup == nil {
			markup = &tg.ReplyInlineMarkup{}
		}
		req.SetReplyMarkup(markup)
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
