package rpc

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Layer 233 bot web widgets: the widget markup is attached to a message as
// messageMediaWidget, may query its bot (updateBotWidgetQuery +
// messages.setBotWidgetQueryAnswer), and carries editable public/chat data.
const (
	widgetQueryTimeout = 25 * time.Second
	maxWidgetAnswerLen = 4 << 10
)

func widgetDataNotModifiedErr() error { return tgerr.New(400, "WIDGET_DATA_NOT_MODIFIED") }
func widgetIDInvalidErr() error       { return tgerr.New(400, "WIDGET_ID_INVALID") }

// registerWidgets registers the Layer 233 widget and transcription-file methods.
func (r *Router) registerWidgets(d *tlprofile.Dispatcher) {
	registerRPC[*tg.MessagesGetWidgetPublicDataRequest](d, tlprofile.SemanticMethodMessagesGetWidgetPublicData, func(ctx context.Context, req *tg.MessagesGetWidgetPublicDataRequest) (any, error) {
		return r.onMessagesGetWidgetPublicData(ctx, req)
	})
	registerRPC[*tg.MessagesEditMessageWidgetDataRequest](d, tlprofile.SemanticMethodMessagesEditMessageWidgetData, func(ctx context.Context, req *tg.MessagesEditMessageWidgetDataRequest) (any, error) {
		return r.onMessagesEditMessageWidgetData(ctx, req)
	})
	registerRPC[*tg.MessagesEditWidgetDataRequest](d, tlprofile.SemanticMethodMessagesEditWidgetData, func(ctx context.Context, req *tg.MessagesEditWidgetDataRequest) (any, error) {
		return r.onMessagesEditWidgetData(ctx, req)
	})
	registerRPC[*tg.MessagesEditInlineBotMessageWidgetDataRequest](d, tlprofile.SemanticMethodMessagesEditInlineBotMessageWidgetData, func(ctx context.Context, req *tg.MessagesEditInlineBotMessageWidgetDataRequest) (any, error) {
		return r.onMessagesEditInlineBotMessageWidgetData(ctx, req)
	})
	registerRPC[*tg.MessagesGetBotWidgetQueryAnswerRequest](d, tlprofile.SemanticMethodMessagesGetBotWidgetQueryAnswer, func(ctx context.Context, req *tg.MessagesGetBotWidgetQueryAnswerRequest) (any, error) {
		return r.onMessagesGetBotWidgetQueryAnswer(ctx, req)
	})
	registerRPC[*tg.MessagesSetBotWidgetQueryAnswerRequest](d, tlprofile.SemanticMethodMessagesSetBotWidgetQueryAnswer, func(ctx context.Context, req *tg.MessagesSetBotWidgetQueryAnswerRequest) (any, error) {
		return r.onMessagesSetBotWidgetQueryAnswer(ctx, req)
	})
	registerRPC[*tg.MessagesGetTranscriptionLangsRequest](d, tlprofile.SemanticMethodMessagesGetTranscriptionLangs, func(ctx context.Context, req *tg.MessagesGetTranscriptionLangsRequest) (any, error) {
		return r.onMessagesGetTranscriptionLangs(ctx, req)
	})
	registerRPC[*tg.MessagesTranscribeAudioFileRequest](d, tlprofile.SemanticMethodMessagesTranscribeAudioFile, func(ctx context.Context, req *tg.MessagesTranscribeAudioFileRequest) (any, error) {
		return r.onMessagesTranscribeAudioFile(ctx, req)
	})
}

type widgetMessageTarget struct {
	media    *domain.MessageMedia
	body     string
	entities []domain.MessageEntity
	// senderBotID is the bot that owns the widget message (sender of the message).
	senderBotID int64
}

// cloneMessageWidget deep-copies a widget snapshot so a mutation never aliases
// the persisted message media.
func cloneMessageWidget(w *domain.MessageWidget) *domain.MessageWidget {
	if w == nil {
		return nil
	}
	clone := *w
	clone.Payload = append([]byte(nil), w.Payload...)
	return &clone
}

// loadWidgetMessage loads the target widget message from the viewer's box.
func (r *Router) loadWidgetMessage(ctx context.Context, userID int64, peer domain.Peer, msgID int) (widgetMessageTarget, bool) {
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return widgetMessageTarget{}, false
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{msgID})
		if err != nil {
			return widgetMessageTarget{}, false
		}
		for _, msg := range history.Messages {
			if msg.ID == msgID && widgetMediaOf(msg.Media) != nil {
				return widgetMessageTarget{
					media: msg.Media, body: msg.Body, entities: msg.Entities,
					senderBotID: msg.SenderUserID,
				}, true
			}
		}
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return widgetMessageTarget{}, false
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, []int{msgID})
		if err != nil {
			return widgetMessageTarget{}, false
		}
		for _, msg := range list.Messages {
			if msg.ID == msgID && msg.Peer == peer && widgetMediaOf(msg.Media) != nil {
				senderBotID := int64(0)
				if msg.From.Type == domain.PeerTypeUser {
					senderBotID = msg.From.ID
				}
				return widgetMessageTarget{
					media: msg.Media, body: msg.Body, entities: msg.Entities,
					senderBotID: senderBotID,
				}, true
			}
		}
	}
	return widgetMessageTarget{}, false
}

func widgetMediaOf(media *domain.MessageMedia) *domain.MessageWidget {
	if media == nil || media.Kind != domain.MessageMediaKindWidget || media.Widget == nil {
		return nil
	}
	return media.Widget
}

// widgetChatType maps a peer to the chat_type carried by updateBotWidgetQuery.
func widgetChatType(peer domain.Peer) string {
	if peer.Type == domain.PeerTypeUser {
		return "private"
	}
	return "channel"
}

// effectiveWidgetPublicData resolves the widget-level public data (shared widget
// definition) over the per-message snapshot.
func (r *Router) effectiveWidgetPublicData(ctx context.Context, widget *domain.MessageWidget) (int64, string, int) {
	if widget == nil {
		return 0, "", 0
	}
	hash, data, period := widget.PublicDataHash, widget.PublicData, 0
	if widget.WidgetID != 0 && r.deps.Widgets != nil {
		def, found, err := r.deps.Widgets.GetWidget(ctx, widget.WidgetID)
		if err == nil && found {
			if def.PublicData != "" {
				data, hash = def.PublicData, def.PublicDataHash
			}
			period = def.CacheTime
		}
	}
	return hash, data, period
}

func (r *Router) onMessagesGetWidgetPublicData(ctx context.Context, req *tg.MessagesGetWidgetPublicDataRequest) (tg.MessagesWidgetPublicDataClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	target, found := r.loadWidgetMessage(ctx, userID, peer, req.MsgID)
	if !found {
		return nil, messageIDInvalidErr()
	}
	hash, data, period := r.effectiveWidgetPublicData(ctx, widgetMediaOf(target.media))
	if req.Hash != 0 && req.Hash == hash {
		return &tg.MessagesWidgetPublicDataNotModified{Period: period}, nil
	}
	return &tg.MessagesWidgetPublicData{Hash: hash, Data: data, Period: period}, nil
}

func (r *Router) onMessagesEditMessageWidgetData(ctx context.Context, req *tg.MessagesEditMessageWidgetDataRequest) (tg.UpdatesClass, error) {
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	publicData, hasPublic := req.GetPublicData()
	chatData, hasChat := req.GetChatData()
	if !hasPublic && !hasChat {
		return nil, widgetDataNotModifiedErr()
	}
	if len(publicData) > maxWidgetDataBytes || len(chatData) > maxWidgetDataBytes {
		return nil, mediaInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	target, found := r.loadWidgetMessage(ctx, userID, peer, req.ID)
	if !found {
		return nil, messageIDInvalidErr()
	}
	widget := cloneMessageWidget(widgetMediaOf(target.media))
	if widget == nil {
		return nil, messageIDInvalidErr()
	}
	if hasPublic {
		widget.PublicData = publicData
		widget.PublicDataHash = widgetDataHash(publicData)
	}
	if hasChat {
		widget.ChatData = chatData
	}
	newMedia := &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: widget}
	now := int(r.clock.Now().Unix())
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return nil, peerIDInvalidErr()
		}
		res, err := r.deps.Channels.EditMessage(ctx, userID, domain.EditChannelMessageRequest{
			UserID: userID, ChannelID: peer.ID, ID: req.ID,
			Message: target.body, Entities: target.entities, Media: newMedia,
			EditDate: now,
		})
		if err != nil {
			return nil, channelEditErr(err)
		}
		updates := r.channelEditMessageUpdates(ctx, userID, res)
		r.enqueueChannelEditMessageFanout(ctx, userID, res)
		return updates, nil
	}
	if peer.Type != domain.PeerTypeUser || r.deps.Messages == nil {
		return nil, peerIDInvalidErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.EditMessage(ctx, userID, domain.EditMessageRequest{
		OwnerUserID: userID, Peer: peer, ID: req.ID,
		Message: target.body, Entities: target.entities, Media: newMedia,
		EditDate:        now,
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionID,
	})
	if err != nil {
		return nil, messageEditErr(err)
	}
	self := res.Self()
	if self.Event.Pts == 0 || self.Message.ID == 0 {
		return nil, messageIDInvalidErr()
	}
	users := r.usersForMessageUpdate(ctx, userID, self.Message)
	chats := r.chatsForMessageUpdate(ctx, userID, self.Message)
	// Emit the schema-native durable updateMessageWidgetData alongside the edit
	// update: the edit re-sends the message (so the client's snapshot refreshes),
	// while this event is the pts-bearing widget-data stream entry (difference,
	// offline replay and proactive delivery).
	if svc, ok := r.deps.Updates.(MessageWidgetDataUpdatesService); ok {
		if _, _, err := svc.RecordMessageWidgetData(ctx, rawAuthKeyIDForOrigin(ctx), userID, self.Message); err != nil {
			r.log.Warn("record message widget data", zap.Error(err))
		}
	}
	return tgEditMessageUpdates(self.Event, self.Message, users, chats), nil
}

func (r *Router) onMessagesEditWidgetData(ctx context.Context, req *tg.MessagesEditWidgetDataRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req.WidgetID == 0 {
		return false, widgetIDInvalidErr()
	}
	if len(req.PublicData) > maxWidgetDataBytes {
		return false, mediaInvalidErr()
	}
	if r.deps.Widgets == nil {
		return false, notImplementedErr()
	}
	def, found, err := r.deps.Widgets.GetWidget(ctx, req.WidgetID)
	if err != nil {
		return false, internalErr()
	}
	if !found {
		return false, widgetIDInvalidErr()
	}
	if def.BotUserID != userID {
		return false, widgetIDInvalidErr()
	}
	if err := r.deps.Widgets.SetWidgetPublicData(ctx, req.WidgetID, req.PublicData, widgetDataHash(req.PublicData), req.CacheTime); err != nil {
		if err == domain.ErrWidgetNotFound {
			return false, widgetIDInvalidErr()
		}
		return false, internalErr()
	}
	return true, nil
}

func (r *Router) onMessagesEditInlineBotMessageWidgetData(ctx context.Context, req *tg.MessagesEditInlineBotMessageWidgetDataRequest) (bool, error) {
	botID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if botID == 0 || !r.userIsBot(ctx, botID) {
		return false, messageIDInvalidErr()
	}
	publicData, hasPublic := req.GetPublicData()
	chatData, hasChat := req.GetChatData()
	if !hasPublic && !hasChat {
		return false, widgetDataNotModifiedErr()
	}
	if len(publicData) > maxWidgetDataBytes || len(chatData) > maxWidgetDataBytes {
		return false, mediaInvalidErr()
	}
	apply := func(widget *domain.MessageWidget) error {
		if widget == nil {
			return messageIDInvalidErr()
		}
		if hasPublic {
			widget.PublicData = publicData
			widget.PublicDataHash = widgetDataHash(publicData)
		}
		if hasChat {
			widget.ChatData = chatData
		}
		return nil
	}
	if target, found, err := r.privateMessageFromInlineID(ctx, botID, req.ID); err != nil {
		return false, internalErr()
	} else if found {
		widget := cloneMessageWidget(widgetMediaOf(target.Media))
		if err := apply(widget); err != nil {
			return false, err
		}
		if _, err := r.deps.Messages.EditMessage(ctx, target.OwnerUserID, domain.EditMessageRequest{
			OwnerUserID: target.OwnerUserID, Peer: target.Peer, ID: target.ID,
			Message: target.Body, Entities: append([]domain.MessageEntity(nil), target.Entities...),
			Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: widget},
			EditDate: int(r.clock.Now().Unix()), ViaBotEditBotID: botID,
		}); err != nil {
			return false, messageEditErr(err)
		}
		return true, nil
	}
	if channel, msg, found, err := r.channelMessageFromInlineID(ctx, botID, req.ID); err != nil {
		return false, internalErr()
	} else if found {
		if r.deps.Channels == nil {
			return false, messageIDInvalidErr()
		}
		widget := cloneMessageWidget(widgetMediaOf(msg.Media))
		if err := apply(widget); err != nil {
			return false, err
		}
		if _, err := r.deps.Channels.EditMessage(ctx, botID, domain.EditChannelMessageRequest{
			UserID: botID, ChannelID: channel.ID, ID: msg.ID,
			Message: msg.Body, Entities: append([]domain.MessageEntity(nil), msg.Entities...),
			Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: widget},
			EditDate: int(r.clock.Now().Unix()), ViaBotEditBotID: botID,
		}); err != nil {
			return false, channelEditErr(err)
		}
		return true, nil
	}
	return false, messageIDInvalidErr()
}

// widgetQueryBotID resolves the bot that owns the queried widget.
func (r *Router) widgetQueryBotID(ctx context.Context, target widgetMessageTarget) (int64, error) {
	widget := widgetMediaOf(target.media)
	if widget != nil && widget.WidgetID != 0 && r.deps.Widgets != nil {
		if def, found, err := r.deps.Widgets.GetWidget(ctx, widget.WidgetID); err == nil && found && def.BotUserID != 0 {
			return def.BotUserID, nil
		}
	}
	if target.senderBotID != 0 && r.userIsBot(ctx, target.senderBotID) {
		return target.senderBotID, nil
	}
	return 0, dataInvalidErr()
}

func (r *Router) onMessagesGetBotWidgetQueryAnswer(ctx context.Context, req *tg.MessagesGetBotWidgetQueryAnswerRequest) (*tg.MessagesBotWidgetQueryAnswer, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if len(req.Data) > domain.MaxCallbackDataLen {
		return nil, dataInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	target, found := r.loadWidgetMessage(ctx, userID, peer, req.MsgID)
	if !found {
		return nil, messageIDInvalidErr()
	}
	widget := widgetMediaOf(target.media)
	if widget == nil || !widget.CanQuery {
		return nil, dataInvalidErr()
	}
	botID, err := r.widgetQueryBotID(ctx, target)
	if err != nil {
		return nil, err
	}
	queryID, pending, err := r.widgets.register(botID, userID)
	if err != nil {
		r.log.Warn("register bot widget query", zap.Error(err))
		return nil, internalErr()
	}
	defer r.widgets.deregister(queryID)

	update := &tg.UpdateBotWidgetQuery{
		QueryID: queryID, QueryType: req.QueryType, UserID: userID,
		Peer: tgPeer(peer), MsgID: req.MsgID, ChatInstance: chatInstanceForPeer(botID, peer),
		ChatType: widgetChatType(peer), Data: append([]byte(nil), req.Data...),
	}
	if widget.WidgetID != 0 {
		update.SetWidgetID(widget.WidgetID)
	}
	r.pushUserMessage(ctx, botID, "push bot widget query", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(r.clock.Now().Unix()),
	})
	return r.waitBotWidgetQueryAnswer(ctx, pending)
}

func (r *Router) waitBotWidgetQueryAnswer(ctx context.Context, pending *pendingWidgetQuery) (*tg.MessagesBotWidgetQueryAnswer, error) {
	waitCtx, cancel := context.WithTimeout(ctx, widgetQueryTimeout)
	defer cancel()
	select {
	case ans := <-pending.ch:
		out := &tg.MessagesBotWidgetQueryAnswer{}
		if ans.Result != "" {
			out.SetResult(ans.Result)
		}
		return out, nil
	case <-pending.done:
		return nil, botResponseTimeoutErr()
	case <-waitCtx.Done():
		return nil, botResponseTimeoutErr()
	}
}

func (r *Router) onMessagesSetBotWidgetQueryAnswer(ctx context.Context, req *tg.MessagesSetBotWidgetQueryAnswerRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	result, _ := req.GetResult()
	if utf8.RuneCountInString(result) > maxWidgetAnswerLen {
		return false, messageTooLongErr()
	}
	if !r.widgets.resolve(botID, req.QueryID, domain.BotWidgetQueryAnswer{Result: result}) {
		return false, queryIDInvalidErr()
	}
	return true, nil
}

func (r *Router) onMessagesGetTranscriptionLangs(ctx context.Context, req *tg.MessagesGetTranscriptionLangsRequest) ([]string, error) {
	// The ASR backend exposes no fixed language list; an empty vector tells the
	// client that message transcription is the only supported path.
	return []string{}, nil
}

func (r *Router) onMessagesTranscribeAudioFile(ctx context.Context, req *tg.MessagesTranscribeAudioFileRequest) (*tg.MessagesTranscribedAudio, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Transcriptions == nil || r.deps.Files == nil {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	ref, ok := uploadedFileRef(userID, req.File)
	if !ok {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	document, err := r.deps.Files.CreateDocumentFromUpload(ctx, ref, domain.DocumentSpec{
		MimeType:   "audio/ogg",
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: ref.Name}},
	})
	if err != nil {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	// File transcription has no message to push updateTranscribedAudio against;
	// it caches by document id (peer/msg_id stay zero) and the client re-calls
	// until Text is populated.
	res, err := r.deps.Transcriptions.Transcribe(ctx, userID, domain.Peer{}, 0, document)
	if err != nil {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	return &tg.MessagesTranscribedAudio{
		Pending:         res.Pending,
		TranscriptionID: res.TranscriptionID,
		Text:            res.Text,
	}, nil
}
