package ephemeral

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type ChannelAccess interface {
	ResolveChannel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
	GetParticipant(ctx context.Context, userID, channelID, participantUserID int64) (domain.ChannelMember, error)
	GetForumTopicsByID(ctx context.Context, userID, channelID int64, ids []int) (domain.ChannelForumTopicList, error)
}

type UserDirectory interface {
	ByID(ctx context.Context, currentUserID, userID int64) (domain.User, bool, error)
}

type BotCommands interface {
	GetBotCommands(ctx context.Context, botUserID int64) ([]domain.BotCommand, error)
}

type Option func(*Service)

func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

func WithIDGenerator(next func() (int, error)) Option {
	return func(s *Service) {
		if next != nil {
			s.nextID = next
		}
	}
}

type Service struct {
	messages store.EphemeralMessageStore
	channels ChannelAccess
	users    UserDirectory
	bots     BotCommands
	now      func() time.Time
	nextID   func() (int, error)
}

func NewService(messages store.EphemeralMessageStore, channels ChannelAccess, users UserDirectory, bots BotCommands, options ...Option) *Service {
	s := &Service{
		messages: messages,
		channels: channels,
		users:    users,
		bots:     bots,
		now:      time.Now,
		nextID:   randomEphemeralID,
	}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	return s
}

func (s *Service) SendFromClient(ctx context.Context, request domain.SendClientEphemeralRequest) (domain.EphemeralMessage, bool, error) {
	if s == nil || s.messages == nil || s.channels == nil || s.users == nil || s.bots == nil {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralInvalid
	}
	if request.SenderUserID <= 0 || request.ReceiverBotID <= 0 || request.SenderUserID == request.ReceiverBotID ||
		request.Peer.Type != domain.PeerTypeChannel || request.Peer.ID <= 0 || request.RandomID == 0 ||
		request.OriginDevice.UserID != request.SenderUserID || request.OriginDevice.BusinessAuthKeyID == ([8]byte{}) ||
		request.OriginDevice.SessionID == 0 || !validContent(request.Content) {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralInvalid
	}
	view, err := s.requireActiveGroupPair(ctx, request.SenderUserID, request.ReceiverBotID, request.Peer.ID)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	receiver, found, err := s.users.ByID(ctx, request.SenderUserID, request.ReceiverBotID)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	if !found || !receiver.Bot || receiver.Deleted {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralReceiverInvalid
	}
	var replyTarget *domain.EphemeralMessage
	if request.ReplyToEphemeralID != 0 {
		target, found, err := s.messages.GetEphemeralMessage(ctx, request.Peer, request.ReplyToEphemeralID, s.now())
		if err != nil {
			return domain.EphemeralMessage{}, false, err
		}
		if !found || target.Deleted || target.SenderUserID != request.ReceiverBotID || target.ReceiverUserID != request.SenderUserID {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralReplyExpired
		}
		if target.OriginDevice.BusinessAuthKeyID != ([8]byte{}) && target.OriginDevice.BusinessAuthKeyID != request.OriginDevice.BusinessAuthKeyID {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralDeviceMismatch
		}
		if request.TopMessageID != 0 && request.TopMessageID != target.TopMessageID {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralPeerInvalid
		}
		request.TopMessageID = target.TopMessageID
		replyTarget = &target
	} else {
		allowed, err := s.isEphemeralCommand(ctx, receiver, request.Content.Message)
		if err != nil {
			return domain.EphemeralMessage{}, false, err
		}
		if !allowed {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralCommandInvalid
		}
	}
	if err := s.validateForumTopic(ctx, request.SenderUserID, view, request.TopMessageID); err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	message, fresh, err := s.create(ctx, domain.EphemeralMessage{
		Peer:               request.Peer,
		SenderUserID:       request.SenderUserID,
		ReceiverUserID:     request.ReceiverBotID,
		RandomID:           request.RandomID,
		TopMessageID:       request.TopMessageID,
		ReplyToEphemeralID: request.ReplyToEphemeralID,
		Content:            request.Content,
		OriginDevice:       request.OriginDevice,
		PayloadHash:        clientPayloadHash(request),
	})
	if err == nil && replyTarget != nil {
		message.BotAPIReply = replyTarget
	}
	return message, fresh, err
}

func (s *Service) SendFromBot(ctx context.Context, request domain.SendBotEphemeralRequest) (domain.EphemeralMessage, bool, error) {
	return s.sendFromBot(ctx, request, func(context.Context) (domain.EphemeralContent, error) {
		return request.Content, nil
	})
}

// SendFromBotLazy authorizes the bot, receiver, chat and eligible action before
// materializing content. The RPC edge uses it for URL/upload media so an
// unauthorized target cannot consume file storage, network or decoder work.
func (s *Service) SendFromBotLazy(ctx context.Context, request domain.SendBotEphemeralRequest, build func(context.Context) (domain.EphemeralContent, error)) (domain.EphemeralMessage, bool, error) {
	if build == nil {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralInvalid
	}
	return s.sendFromBot(ctx, request, build)
}

func (s *Service) sendFromBot(ctx context.Context, request domain.SendBotEphemeralRequest, build func(context.Context) (domain.EphemeralContent, error)) (domain.EphemeralMessage, bool, error) {
	if s == nil || s.messages == nil || s.channels == nil || s.users == nil {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralInvalid
	}
	if request.BotUserID <= 0 || request.ReceiverUserID <= 0 || request.BotUserID == request.ReceiverUserID ||
		request.Peer.Type != domain.PeerTypeChannel || request.Peer.ID <= 0 {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralInvalid
	}
	view, err := s.requireActiveGroupPair(ctx, request.BotUserID, request.ReceiverUserID, request.Peer.ID)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	bot, found, err := s.users.ByID(ctx, request.BotUserID, request.BotUserID)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	if !found || !bot.Bot || bot.Deleted {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralSenderInvalid
	}
	receiver, found, err := s.users.ByID(ctx, request.BotUserID, request.ReceiverUserID)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	if !found || receiver.Bot || receiver.Deleted {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralReceiverInvalid
	}
	now := s.now()
	var targetDevice domain.EphemeralDevice
	var replyTarget *domain.EphemeralMessage
	if request.ActionMessageID != 0 && request.CallbackQueryID != 0 {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralInvalid
	}
	if request.CallbackQueryID != 0 {
		action, found, err := s.messages.GetEphemeralCallbackAction(ctx, request.BotUserID, request.CallbackQueryID, now)
		if err != nil {
			return domain.EphemeralMessage{}, false, err
		}
		if !found || action.UserID != request.ReceiverUserID || action.Peer != request.Peer || !now.Before(action.ExpiresAt) {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralReplyExpired
		}
		targetDevice = action.Device
		if request.TopMessageID != 0 && request.TopMessageID != action.TopMessageID {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralPeerInvalid
		}
		request.TopMessageID = action.TopMessageID
	} else if request.ActionMessageID != 0 {
		action, found, err := s.messages.GetEphemeralMessage(ctx, request.Peer, request.ActionMessageID, now)
		if err != nil {
			return domain.EphemeralMessage{}, false, err
		}
		if !found || action.Deleted || action.SenderUserID != request.ReceiverUserID || action.ReceiverUserID != request.BotUserID ||
			now.Sub(action.CreatedAt) < 0 || now.Sub(action.CreatedAt) > domain.EphemeralReplyWindow {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralReplyExpired
		}
		targetDevice = action.OriginDevice
		replyTarget = &action
		if request.TopMessageID != 0 && request.TopMessageID != action.TopMessageID {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralPeerInvalid
		}
		request.TopMessageID = action.TopMessageID
		if request.ReplyToEphemeralID == 0 {
			request.ReplyToEphemeralID = action.ID
		}
	} else {
		if view.Self.Role != domain.ChannelRoleCreator && view.Self.Role != domain.ChannelRoleAdmin {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralForbidden
		}
	}
	if request.ReplyToEphemeralID != 0 {
		var reply domain.EphemeralMessage
		found := false
		if replyTarget != nil && replyTarget.ID == request.ReplyToEphemeralID {
			reply, found = *replyTarget, true
		} else {
			var err error
			reply, found, err = s.messages.GetEphemeralMessage(ctx, request.Peer, request.ReplyToEphemeralID, now)
			if err != nil {
				return domain.EphemeralMessage{}, false, err
			}
		}
		if !found || reply.Deleted || !sameEphemeralParticipants(reply, request.BotUserID, request.ReceiverUserID) {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralReplyExpired
		}
		if targetDevice.BusinessAuthKeyID != ([8]byte{}) && reply.OriginDevice.BusinessAuthKeyID != ([8]byte{}) &&
			targetDevice.BusinessAuthKeyID != reply.OriginDevice.BusinessAuthKeyID {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralDeviceMismatch
		}
		if request.TopMessageID != 0 && request.TopMessageID != reply.TopMessageID {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralPeerInvalid
		}
		request.TopMessageID = reply.TopMessageID
		replyTarget = &reply
	}
	if err := s.validateForumTopic(ctx, request.BotUserID, view, request.TopMessageID); err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	content, err := build(ctx)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	if !validContent(content) {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralInvalid
	}
	request.Content = content
	if request.RandomID == 0 {
		request.RandomID, err = randomEphemeralRandomID()
		if err != nil {
			return domain.EphemeralMessage{}, false, err
		}
	}
	message, fresh, err := s.create(ctx, domain.EphemeralMessage{
		Peer:               request.Peer,
		SenderUserID:       request.BotUserID,
		ReceiverUserID:     request.ReceiverUserID,
		RandomID:           request.RandomID,
		TopMessageID:       request.TopMessageID,
		ReplyToEphemeralID: request.ReplyToEphemeralID,
		Content:            request.Content,
		OriginDevice:       targetDevice,
		PayloadHash:        botPayloadHash(request),
	})
	if err == nil && replyTarget != nil {
		message.BotAPIReply = replyTarget
	}
	return message, fresh, err
}

func (s *Service) EditFromBot(ctx context.Context, botUserID int64, peer domain.Peer, id int, content domain.EphemeralContent) (domain.EphemeralMessage, error) {
	now := s.now()
	message, found, err := s.messages.GetEphemeralMessage(ctx, peer, id, now)
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	if !found {
		return domain.EphemeralMessage{}, domain.ErrEphemeralNotFound
	}
	if message.SenderUserID != botUserID {
		return domain.EphemeralMessage{}, domain.ErrEphemeralForbidden
	}
	return s.messages.EditEphemeralMessage(ctx, peer, id, message.Version, content, int(now.Unix()), now)
}

func (s *Service) EditFieldsFromBot(ctx context.Context, botUserID, receiverUserID int64, peer domain.Peer, id int, mode domain.EphemeralEditMode, fields domain.EditEphemeralFields) (domain.EphemeralMessage, error) {
	return s.editFieldsFromBot(ctx, botUserID, receiverUserID, peer, id, mode, func(context.Context) (domain.EditEphemeralFields, error) {
		return fields, nil
	})
}

// EditFieldsFromBotLazy performs the identity/ownership lookup before building
// replacement media. This keeps invalid edit requests off the remote-fetch and
// blob-materialization paths while preserving a single CAS write on success.
func (s *Service) EditFieldsFromBotLazy(ctx context.Context, botUserID, receiverUserID int64, peer domain.Peer, id int, mode domain.EphemeralEditMode, build func(context.Context) (domain.EditEphemeralFields, error)) (domain.EphemeralMessage, error) {
	if build == nil {
		return domain.EphemeralMessage{}, domain.ErrEphemeralInvalid
	}
	return s.editFieldsFromBot(ctx, botUserID, receiverUserID, peer, id, mode, build)
}

func (s *Service) editFieldsFromBot(ctx context.Context, botUserID, receiverUserID int64, peer domain.Peer, id int, mode domain.EphemeralEditMode, build func(context.Context) (domain.EditEphemeralFields, error)) (domain.EphemeralMessage, error) {
	now := s.now()
	message, found, err := s.messages.GetEphemeralMessage(ctx, peer, id, now)
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	if !found {
		return domain.EphemeralMessage{}, domain.ErrEphemeralNotFound
	}
	if message.SenderUserID != botUserID || message.ReceiverUserID != receiverUserID {
		return domain.EphemeralMessage{}, domain.ErrEphemeralForbidden
	}
	fields, err := build(ctx)
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	switch mode {
	case domain.EphemeralEditText:
		if message.Content.Media != nil || !message.Content.RichMessage.IsZero() || !fields.SetMessage {
			return domain.EphemeralMessage{}, domain.ErrEphemeralInvalid
		}
	case domain.EphemeralEditCaption:
		if message.Content.Media == nil || !fields.SetMessage {
			return domain.EphemeralMessage{}, domain.ErrEphemeralInvalid
		}
	case domain.EphemeralEditMedia:
		if message.Content.Media == nil || !fields.SetMedia {
			return domain.EphemeralMessage{}, domain.ErrEphemeralInvalid
		}
	case domain.EphemeralEditReplyMarkup:
		if !fields.SetReplyMarkup || fields.SetMessage || fields.SetMedia {
			return domain.EphemeralMessage{}, domain.ErrEphemeralInvalid
		}
	default:
		return domain.EphemeralMessage{}, domain.ErrEphemeralInvalid
	}
	content := message.Content
	if fields.SetMessage {
		content.Message = fields.Message
		content.Entities = append([]domain.MessageEntity(nil), fields.Entities...)
	}
	if fields.SetMedia {
		content.Media = fields.Media
	}
	if fields.SetReplyMarkup {
		content.ReplyMarkup = fields.ReplyMarkup
	}
	if !validContent(content) {
		return domain.EphemeralMessage{}, domain.ErrEphemeralInvalid
	}
	return s.messages.EditEphemeralMessage(ctx, peer, id, message.Version, content, int(now.Unix()), now)
}

func (s *Service) Delete(ctx context.Context, actorUserID, receiverUserID int64, peer domain.Peer, id int) (domain.EphemeralMessage, bool, error) {
	return s.delete(ctx, actorUserID, receiverUserID, nil, peer, id)
}

func (s *Service) DeleteFromDevice(ctx context.Context, actorUserID, receiverUserID int64, device domain.EphemeralDevice, peer domain.Peer, id int) (domain.EphemeralMessage, bool, error) {
	if device.UserID != actorUserID || device.BusinessAuthKeyID == ([8]byte{}) || device.SessionID == 0 {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralForbidden
	}
	return s.delete(ctx, actorUserID, receiverUserID, &device, peer, id)
}

func (s *Service) delete(ctx context.Context, actorUserID, receiverUserID int64, device *domain.EphemeralDevice, peer domain.Peer, id int) (domain.EphemeralMessage, bool, error) {
	now := s.now()
	message, found, err := s.messages.GetEphemeralMessage(ctx, peer, id, now)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	if !found {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralNotFound
	}
	if message.ReceiverUserID != receiverUserID || (actorUserID != message.SenderUserID && actorUserID != message.ReceiverUserID) {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralForbidden
	}
	if device != nil && message.OriginDevice.UserID == actorUserID && message.OriginDevice.BusinessAuthKeyID != ([8]byte{}) &&
		message.OriginDevice.BusinessAuthKeyID != device.BusinessAuthKeyID {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralDeviceMismatch
	}
	return s.messages.DeleteEphemeralMessage(ctx, peer, id, message.Version, now)
}

func (s *Service) Callback(ctx context.Context, userID int64, device domain.EphemeralDevice, peer domain.Peer, id int, data []byte) (domain.EphemeralCallback, error) {
	if len(data) > domain.MaxEphemeralCallbackDataBytes || userID <= 0 || device.UserID != userID ||
		device.BusinessAuthKeyID == ([8]byte{}) || device.SessionID == 0 {
		return domain.EphemeralCallback{}, domain.ErrEphemeralCallbackInvalid
	}
	now := s.now()
	message, found, err := s.messages.GetEphemeralMessage(ctx, peer, id, now)
	if err != nil {
		return domain.EphemeralCallback{}, err
	}
	if !found || message.Deleted || message.ReceiverUserID != userID {
		return domain.EphemeralCallback{}, domain.ErrEphemeralCallbackInvalid
	}
	if !ephemeralMarkupContainsCallback(message.Content.ReplyMarkup, data) {
		return domain.EphemeralCallback{}, domain.ErrEphemeralCallbackInvalid
	}
	if message.OriginDevice.BusinessAuthKeyID != ([8]byte{}) && message.OriginDevice.BusinessAuthKeyID != device.BusinessAuthKeyID {
		return domain.EphemeralCallback{}, domain.ErrEphemeralDeviceMismatch
	}
	return domain.EphemeralCallback{
		Message:    message,
		BotUserID:  message.SenderUserID,
		UserID:     userID,
		Peer:       peer,
		Data:       append([]byte(nil), data...),
		Device:     device,
		OccurredAt: now,
	}, nil
}

func (s *Service) PutCallbackAction(ctx context.Context, action domain.EphemeralCallbackAction) (bool, error) {
	if s == nil || s.messages == nil {
		return false, domain.ErrEphemeralInvalid
	}
	return s.messages.PutEphemeralCallbackAction(ctx, action)
}

func (s *Service) ReportTarget(ctx context.Context, userID int64, device domain.EphemeralDevice, peer domain.Peer, id int) (domain.EphemeralMessage, error) {
	if userID <= 0 || device.UserID != userID || device.BusinessAuthKeyID == ([8]byte{}) || device.SessionID == 0 {
		return domain.EphemeralMessage{}, domain.ErrEphemeralForbidden
	}
	message, found, err := s.messages.GetEphemeralMessage(ctx, peer, id, s.now())
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	if !found || message.Deleted || message.ReceiverUserID != userID {
		return domain.EphemeralMessage{}, domain.ErrEphemeralNotFound
	}
	if message.OriginDevice.BusinessAuthKeyID != ([8]byte{}) && message.OriginDevice.BusinessAuthKeyID != device.BusinessAuthKeyID {
		return domain.EphemeralMessage{}, domain.ErrEphemeralDeviceMismatch
	}
	return message, nil
}

func ephemeralMarkupContainsCallback(markup *domain.MessageReplyMarkup, data []byte) bool {
	if markup == nil || markup.Kind() != domain.MessageReplyMarkupInline {
		return false
	}
	for _, row := range markup.Inline {
		for _, button := range row {
			if button.Type == domain.MarkupButtonCallback && bytes.Equal(button.Data, data) {
				return true
			}
		}
	}
	return false
}

func sameEphemeralParticipants(message domain.EphemeralMessage, first, second int64) bool {
	return (message.SenderUserID == first && message.ReceiverUserID == second) ||
		(message.SenderUserID == second && message.ReceiverUserID == first)
}

func (s *Service) create(ctx context.Context, message domain.EphemeralMessage) (domain.EphemeralMessage, bool, error) {
	now := s.now()
	message.Date = int(now.Unix())
	message.CreatedAt = now
	message.ExpiresAt = now.Add(domain.EphemeralMessageRetention)
	message.Version = 1
	for attempt := 0; attempt < domain.MaxEphemeralCreateAttempts; attempt++ {
		id, err := s.nextID()
		if err != nil {
			return domain.EphemeralMessage{}, false, err
		}
		message.ID = id
		created, fresh, err := s.messages.CreateEphemeralMessage(ctx, message)
		if !errors.Is(err, domain.ErrEphemeralIDCollision) {
			return created, fresh, err
		}
	}
	return domain.EphemeralMessage{}, false, domain.ErrEphemeralIDCollision
}

func (s *Service) requireActiveGroupPair(ctx context.Context, viewerUserID, otherUserID, channelID int64) (domain.ChannelView, error) {
	view, err := s.channels.ResolveChannel(ctx, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if view.Channel.Deleted || view.Channel.Broadcast || view.Channel.Monoforum || view.Self.Status != domain.ChannelMemberActive {
		return domain.ChannelView{}, domain.ErrEphemeralPeerInvalid
	}
	other, err := s.channels.GetParticipant(ctx, viewerUserID, channelID, otherUserID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if other.Status != domain.ChannelMemberActive {
		return domain.ChannelView{}, domain.ErrEphemeralReceiverInvalid
	}
	return view, nil
}

func (s *Service) validateForumTopic(ctx context.Context, userID int64, view domain.ChannelView, topMessageID int) error {
	if topMessageID == 0 {
		return nil
	}
	if !view.Channel.Forum || topMessageID < 0 || topMessageID > domain.MaxMessageBoxID {
		return domain.ErrEphemeralPeerInvalid
	}
	topics, err := s.channels.GetForumTopicsByID(ctx, userID, view.Channel.ID, []int{topMessageID})
	if err != nil {
		return err
	}
	if len(topics.Topics) != 1 || topics.Topics[0].TopicID != topMessageID || topics.Topics[0].Hidden {
		return domain.ErrEphemeralPeerInvalid
	}
	if topics.Topics[0].Closed && view.Self.Role != domain.ChannelRoleAdmin && view.Self.Role != domain.ChannelRoleCreator {
		return domain.ErrEphemeralForbidden
	}
	return nil
}

func (s *Service) isEphemeralCommand(ctx context.Context, bot domain.User, message string) (bool, error) {
	command, username, ok := parseCommand(message)
	if !ok || (username != "" && !strings.EqualFold(username, bot.Username)) {
		return false, nil
	}
	commands, err := s.bots.GetBotCommands(ctx, bot.ID)
	if err != nil {
		return false, err
	}
	for _, candidate := range commands {
		if candidate.Ephemeral && strings.EqualFold(candidate.Command, command) {
			return true, nil
		}
	}
	return false, nil
}

func parseCommand(message string) (command, username string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(message))
	if len(fields) == 0 || len(fields[0]) < 2 || fields[0][0] != '/' {
		return "", "", false
	}
	parts := strings.SplitN(fields[0][1:], "@", 2)
	command = strings.ToLower(parts[0])
	if command == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		username = strings.TrimPrefix(strings.ToLower(parts[1]), "@")
		if username == "" {
			return "", "", false
		}
	}
	return command, username, true
}

func validContent(content domain.EphemeralContent) bool {
	return domain.ValidateEphemeralContent(content) == nil
}

func clientPayloadHash(request domain.SendClientEphemeralRequest) [32]byte {
	return payloadHash(struct {
		SenderUserID, ReceiverBotID int64
		Peer                        domain.Peer
		QueryID, RandomID           int64
		TopMessageID, ReplyID       int
		Content                     domain.EphemeralContent
		Device                      domain.EphemeralDevice
	}{request.SenderUserID, request.ReceiverBotID, request.Peer, request.QueryID, request.RandomID,
		request.TopMessageID, request.ReplyToEphemeralID, request.Content, request.OriginDevice})
}

func botPayloadHash(request domain.SendBotEphemeralRequest) [32]byte {
	return payloadHash(request)
}

func payloadHash(value any) [32]byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return sha256.Sum256([]byte("invalid-ephemeral-payload"))
	}
	return sha256.Sum256(raw)
}

func randomEphemeralID() (int, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	value := binary.LittleEndian.Uint32(raw[:]) & 0x7fffffff
	if value == 0 {
		value = 1
	}
	return int(value), nil
}

func randomEphemeralRandomID() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	value := int64(binary.LittleEndian.Uint64(raw[:]))
	if value == 0 {
		value = 1
	}
	return value, nil
}
