package rpc

import (
	"context"
	"errors"
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
	// uploadPartBytes / maxTranscriptionFileBytes bound messages.transcribeAudioFile
	// before it materializes a permanent document from the upload.
	uploadPartBytes           = 512 << 10
	maxTranscriptionFileBytes = 25 << 20
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
	// viaBotID is the inline bot when the message was posted via a bot.
	viaBotID int64
	private  *domain.Message
	channel  *domain.ChannelMessage
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
				stored := msg
				return widgetMessageTarget{
					media: msg.Media, body: msg.Body, entities: msg.Entities,
					senderBotID: msg.SenderUserID, viaBotID: msg.ViaBotID, channel: &stored,
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
				stored := msg
				return widgetMessageTarget{
					media: msg.Media, body: msg.Body, entities: msg.Entities,
					senderBotID: senderBotID, viaBotID: msg.ViaBotID, private: &stored,
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

// effectiveWidgetPublicData resolves the effective public data: the per-message
// snapshot takes precedence (so editMessageWidgetData is observable), falling
// back to the shared widget definition when the message carries none.
func (r *Router) effectiveWidgetPublicData(ctx context.Context, widget *domain.MessageWidget) (int64, string, int) {
	if widget == nil {
		return 0, "", 0
	}
	hash, data, period := widget.PublicDataHash, widget.PublicData, 0
	if widget.WidgetID != 0 && r.deps.Widgets != nil {
		def, found, err := r.deps.Widgets.GetWidget(ctx, widget.WidgetID)
		if err == nil && found {
			period = def.CacheTime
			if data == "" {
				data, hash = def.PublicData, def.PublicDataHash
			}
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
	original := widgetMediaOf(target.media)
	widget := cloneMessageWidget(original)
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
	if original == nil || (original.PublicData == widget.PublicData && original.PublicDataHash == widget.PublicDataHash && original.ChatData == widget.ChatData) {
		return nil, widgetDataNotModifiedErr()
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
	patch := &domain.MessageWidgetDataPatch{}
	if hasPublic {
		v := publicData
		patch.PublicData = &v
	}
	if hasChat {
		v := chatData
		patch.ChatData = &v
	}
	res, err := r.deps.Messages.EditMessage(ctx, userID, domain.EditMessageRequest{
		OwnerUserID: userID, Peer: peer, ID: req.ID,
		WidgetData:      patch,
		EditDate:        now,
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx), OriginSessionID: sessionID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrMessageNotModified) {
			return nil, widgetDataNotModifiedErr()
		}
		return nil, messageEditErr(err)
	}
	self := res.Self()
	if self.Event.Pts == 0 || self.Message.ID == 0 {
		return nil, messageIDInvalidErr()
	}
	// The store generated a message_widget_data event (not edit_message): return
	// the schema-native updateMessageWidgetData to the caller.
	update := tgOtherUpdateFromEvent(self.Event)
	if update == nil {
		return nil, messageIDInvalidErr()
	}
	return &tg.Updates{Updates: []tg.UpdateClass{update}, Date: self.Event.Date}, nil
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
	cacheTime := def.CacheTime
	if ct, ok := req.GetCacheTime(); ok {
		cacheTime = ct
	}
	if err := r.deps.Widgets.SetWidgetPublicData(ctx, req.WidgetID, req.PublicData, widgetDataHash(req.PublicData), cacheTime); err != nil {
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
	if r.deps.Messages == nil {
		return false, messageIDInvalidErr()
	}
	if target, found, err := r.privateMessageFromInlineID(ctx, botID, req.ID); err != nil {
		return false, err
	} else if found {
		widget := cloneMessageWidget(widgetMediaOf(target.Media))
		if err := apply(widget); err != nil {
			return false, err
		}
		if _, err := r.deps.Messages.EditMessage(ctx, target.OwnerUserID, domain.EditMessageRequest{
			OwnerUserID: target.OwnerUserID, Peer: target.Peer, ID: target.ID,
			Message: target.Body, Entities: append([]domain.MessageEntity(nil), target.Entities...),
			Media:    &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: widget},
			EditDate: int(r.clock.Now().Unix()), ViaBotEditBotID: botID,
		}); err != nil {
			return false, messageEditErr(err)
		}
		return true, nil
	}
	if channel, msg, found, err := r.channelMessageFromInlineID(ctx, botID, req.ID); err != nil {
		return false, err
	} else if found {
		if r.deps.Channels == nil {
			return false, messageIDInvalidErr()
		}
		widget := cloneMessageWidget(widgetMediaOf(msg.Media))
		if err := apply(widget); err != nil {
			return false, err
		}
		// EditInlineBotMessage authorizes through ViaBotEditBotID with the
		// message sender as the acting user, so it works when the bot is not a
		// member; then fan the edit out to the channel members.
		res, err := r.deps.Channels.EditInlineBotMessage(ctx, botID, domain.EditChannelMessageRequest{
			UserID: msg.SenderUserID, ChannelID: channel.ID, ID: msg.ID,
			Message: msg.Body, Entities: append([]domain.MessageEntity(nil), msg.Entities...),
			Media:    &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: widget},
			EditDate: int(r.clock.Now().Unix()), ViaBotEditBotID: botID,
		})
		if err != nil {
			return false, channelEditErr(err)
		}
		r.enqueueChannelEditMessageFanout(ctx, msg.SenderUserID, res)
		return true, nil
	}
	return false, messageIDInvalidErr()
}

// widgetQueryBotID resolves the bot that owns the queried widget. Every path
// requires the resolved owner to actually be a bot: a non-bot widget owner must
// never receive another user's query.
func (r *Router) widgetQueryBotID(ctx context.Context, target widgetMessageTarget) (int64, error) {
	widget := widgetMediaOf(target.media)
	if widget == nil {
		return 0, dataInvalidErr()
	}
	sharedOwner := func() (int64, bool) {
		if widget.WidgetID == 0 || r.deps.Widgets == nil {
			return 0, false
		}
		def, found, err := r.deps.Widgets.GetWidget(ctx, widget.WidgetID)
		if err != nil || !found || def.BotUserID == 0 || !r.userIsBot(ctx, def.BotUserID) {
			return 0, false
		}
		return def.BotUserID, true
	}
	// A copy belongs to the shared definition's bot; a via_bot message to the
	// inline bot; otherwise to the bot that sent the message.
	if widget.IsCopy {
		if bot, ok := sharedOwner(); ok {
			return bot, nil
		}
	}
	if target.viaBotID != 0 && r.userIsBot(ctx, target.viaBotID) {
		return target.viaBotID, nil
	}
	if bot, ok := sharedOwner(); ok {
		return bot, nil
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
	queryID, pending, err := r.widgets.registerContext(ctx, r.clock.Now(), botID, userID, widgetQueryTimeout)
	if err != nil {
		r.log.Warn("register bot widget query", zap.Error(err))
		return nil, internalErr()
	}
	defer r.widgets.deregisterContext(context.Background(), botID, queryID)

	update, err := r.botWidgetQueryUpdate(ctx, userID, peer, req, target, widget, botID, queryID)
	if err != nil {
		return nil, err
	}
	r.pushUserMessage(ctx, botID, "push bot widget query", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(r.clock.Now().Unix()),
	})
	return r.waitBotWidgetQueryAnswer(ctx, botID, queryID, pending)
}

// botWidgetQueryUpdate builds the query update for the resolved owner:
// updateBotWidgetCopyQuery for an is_copy widget (no peer/msg_id),
// updateInlineBotWidgetQuery routed to the inline bot for a via_bot message,
// and updateBotWidgetQuery for a message the bot itself sent (translated to the
// bot's own view in private chats).
func (r *Router) botWidgetQueryUpdate(
	ctx context.Context,
	userID int64,
	peer domain.Peer,
	req *tg.MessagesGetBotWidgetQueryAnswerRequest,
	target widgetMessageTarget,
	widget *domain.MessageWidget,
	botID int64,
	queryID int64,
) (tg.UpdateClass, error) {
	attachWallet := func(set func(tg.WalletCard)) {
		if !widgetQueryWantsWallet(req.QueryType) {
			return
		}
		if card, ok := r.widgetWalletCard(ctx, userID); ok {
			set(card)
		}
	}
	switch {
	case widget.IsCopy && widget.WidgetID != 0:
		update := &tg.UpdateBotWidgetCopyQuery{
			QueryID: queryID, QueryType: req.QueryType, UserID: userID,
			Data: append([]byte(nil), req.Data...), WidgetID: widget.WidgetID,
		}
		attachWallet(update.SetWalletCard)
		return update, nil
	case target.viaBotID != 0:
		var inlineID tg.InputBotInlineMessageIDClass
		switch {
		case target.channel != nil:
			inlineID = r.inputInlineMessageIDForChannelMessage(botID, *target.channel)
		case target.private != nil:
			inlineID = r.inputInlineMessageIDForPrivateMessage(botID, *target.private)
		}
		if inlineID == nil {
			return nil, dataInvalidErr()
		}
		update := &tg.UpdateInlineBotWidgetQuery{
			QueryID: queryID, QueryType: req.QueryType, UserID: userID,
			InlineMessageID: inlineID, ChatInstance: chatInstanceForPeer(botID, peer),
			ChatType: widgetChatType(peer), Data: append([]byte(nil), req.Data...),
		}
		if widget.WidgetID != 0 {
			update.SetWidgetID(widget.WidgetID)
		}
		attachWallet(update.SetWalletCard)
		return update, nil
	default:
		updatePeer, updateMsgID := peer, req.MsgID
		chatInstance := chatInstanceForPeer(botID, peer)
		if peer.Type == domain.PeerTypeUser {
			chatInstance = chatInstanceFor(botID, userID)
			// Translate to the bot's own view: peer = the querying user, msg_id
			// = the bot's copy box id (mirrors resolveBotCallbackQuery).
			if target.private != nil && target.private.UID != 0 {
				if provider, ok := r.deps.Messages.(privateMessageByUIDService); ok {
					if botMessage, found, err := provider.GetMessageByUID(ctx, botID, target.private.UID); err == nil && found &&
						botMessage.OwnerUserID == botID &&
						botMessage.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
						updatePeer = botMessage.Peer
						updateMsgID = botMessage.ID
					}
				}
			}
		}
		update := &tg.UpdateBotWidgetQuery{
			QueryID: queryID, QueryType: req.QueryType, UserID: userID,
			Peer: tgPeer(updatePeer), MsgID: updateMsgID, ChatInstance: chatInstance,
			ChatType: widgetChatType(peer), Data: append([]byte(nil), req.Data...),
		}
		if widget.WidgetID != 0 {
			update.SetWidgetID(widget.WidgetID)
		}
		attachWallet(update.SetWalletCard)
		return update, nil
	}
}

// widgetQueryWantsWallet reports whether the query type opts into receiving the
// querying user's wallet card. The schema puts the opt-in on the query type, so
// an ordinary data query must not leak the wallet to the bot.
func widgetQueryWantsWallet(queryType tg.WidgetQueryTypeClass) bool {
	switch q := queryType.(type) {
	case *tg.WidgetQueryTypeData:
		return q != nil && (q.WithWallet || q.Flags.Has(0))
	case *tg.WidgetQueryTypeSignData, *tg.WidgetQueryTypeSentTransaction:
		return true
	default:
		return false
	}
}

// widgetWalletCard projects the querying user's wallet into the bot-facing
// updateBotWidgetQuery.wallet_card. A widget without a bound wallet carries no
// card (the query type still conveys the client-side UserWallet for signing).
func (r *Router) widgetWalletCard(ctx context.Context, userID int64) (tg.WalletCard, bool) {
	if r.deps.Wallet == nil || userID == 0 {
		return tg.WalletCard{}, false
	}
	link, balance, ok, err := r.deps.Wallet.State(ctx, userID)
	if err != nil || !ok || link.Address == "" {
		return tg.WalletCard{}, false
	}
	return tg.WalletCard{Address: link.Address, Balance: balance}, true
}

func widgetQueryAnswerResult(ans domain.BotWidgetQueryAnswer) *tg.MessagesBotWidgetQueryAnswer {
	out := &tg.MessagesBotWidgetQueryAnswer{}
	if ans.Result != "" {
		out.SetResult(ans.Result)
	}
	return out
}

func (r *Router) waitBotWidgetQueryAnswer(ctx context.Context, botUserID, queryID int64, pending *pendingWidgetQuery) (*tg.MessagesBotWidgetQueryAnswer, error) {
	waitCtx, cancel := context.WithTimeout(ctx, widgetQueryTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case ans := <-pending.ch:
			return widgetQueryAnswerResult(ans), nil
		case <-ticker.C:
			ans, found, err := r.widgets.sharedAnswer(waitCtx, botUserID, queryID)
			if err != nil {
				r.log.Warn("read shared bot widget query answer",
					zap.Int64("bot_user_id", botUserID), zap.Int64("query_id", queryID), zap.Error(err))
				continue
			}
			if found {
				return widgetQueryAnswerResult(ans), nil
			}
		case <-pending.done:
			return nil, botResponseTimeoutErr()
		case <-waitCtx.Done():
			return nil, botResponseTimeoutErr()
		}
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
	resolved, err := r.widgets.resolveContext(ctx, botID, req.QueryID, domain.BotWidgetQueryAnswer{Result: result})
	if err != nil {
		return false, internalErr()
	}
	if !resolved {
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
	// Reject an oversized upload before CreateDocumentFromUpload persists a
	// permanent document (the ASR job's own 25 MiB check runs too late).
	if ref.Parts <= 0 || int64(ref.Parts)*uploadPartBytes > maxTranscriptionFileBytes {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	document, err := r.deps.Files.CreateDocumentFromUpload(ctx, ref, domain.DocumentSpec{
		MimeType:   "audio/ogg",
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: ref.Name}},
	})
	if err != nil {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	// File transcription caches by document id and pushes updateTranscribedAudioFile
	// to the requester on completion; a failed file job is terminal.
	res, err := r.deps.Transcriptions.TranscribeFile(ctx, userID, document)
	if err != nil {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	return &tg.MessagesTranscribedAudio{
		Pending:         res.Pending,
		TranscriptionID: res.TranscriptionID,
		Text:            res.Text,
	}, nil
}
