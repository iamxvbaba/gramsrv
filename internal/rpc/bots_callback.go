package rpc

import (
	"bytes"
	"context"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// botCallbackTimeout is the pending cap for getBotCallbackAnswer: if the bot does not call
// setBotCallbackAnswer within the window, BOT_RESPONSE_TIMEOUT is returned (no fast fail, giving
// the bot a window to come online and answer late).
const botCallbackTimeout = 25 * time.Second

func botResponseTimeoutErr() error { return tgerr.New(502, "BOT_RESPONSE_TIMEOUT") }
func dataInvalidErr() error        { return tgerr.New(400, "DATA_INVALID") }

type privateMessageByUIDService interface {
	GetMessageByUID(ctx context.Context, userID, uid int64) (domain.Message, bool, error)
}

// onMessagesGetBotCallbackAnswer handles an inline callback button click: it delivers the same
// callback query to both the online MTProto bot session and the Bot API update_id queue, then
// pends waiting for the bot's setBotCallbackAnswer/answerCallbackQuery, or times out with
// BOT_RESPONSE_TIMEOUT.
func (r *Router) onMessagesGetBotCallbackAnswer(ctx context.Context, req *tg.MessagesGetBotCallbackAnswerRequest) (*tg.MessagesBotCallbackAnswer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	// The game button (getBotCallbackAnswer.game) is not supported in P3: return an empty answer
	// (the client shows nothing), with no pending and no push (avoiding delivering an
	// unhandleable game query to the bot).
	if req.Game {
		return &tg.MessagesBotCallbackAnswer{}, nil
	}
	data, hasData := req.GetData()
	if !hasData {
		return nil, dataInvalidErr()
	}
	if len(data) > domain.MaxCallbackDataLen {
		return nil, dataInvalidErr()
	}
	// Verify the target message exists in the requester's own box and that the peer is exactly this bot.
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	callback, err := r.resolveBotCallbackQuery(ctx, userID, peer, req.MsgID, data)
	if err != nil {
		return nil, err
	}
	callback.ClientSession = clientSessionMetadataFromContext(ctx)
	botUserID := callback.BotUserID

	// Built-in (in-process) service bot branch: a bot like @verifybot has no MTProto session and
	// no Bot API consumer, so going through the "push updateBotCallbackQuery + pend 25s" below
	// would always time out with BOT_RESPONSE_TIMEOUT. So before registerContext / push, ask the
	// responder synchronously: the click itself has already been validated by
	// resolveBotCallbackQuery (the message is in the requester's own box, the peer is exactly this
	// bot, and the data really appears in that message's inline keyboard), and here we only hand
	// the answer to the implementation that owns the bot. The query id is not registered, so an
	// external setBotCallbackAnswer cannot forge/override the built-in bot's response.
	if r.deps.ServiceBotCallbacks != nil && r.deps.ServiceBotCallbacks.HandlesBot(botUserID) {
		ans, handled, err := r.deps.ServiceBotCallbacks.OnCallbackQuery(ctx, callback)
		if err != nil {
			r.log.Warn("service bot callback query",
				zap.Int64("bot_user_id", botUserID), zap.Int64("user_id", userID), zap.Error(err))
			return nil, internalErr()
		}
		if !handled {
			return nil, dataInvalidErr()
		}
		return tgBotCallbackAnswer(ans), nil
	}

	queryID, pending, err := r.callbacks.registerContext(ctx, r.clock.Now(), botUserID, userID, botCallbackTimeout)
	if err != nil {
		r.log.Warn("register shared bot callback query", zap.Int64("bot_user_id", botUserID), zap.Error(err))
		return nil, internalErr()
	}
	defer r.callbacks.deregisterContext(context.Background(), botUserID, queryID)
	callback.ID = queryID

	// Bot API callback_query shares the dedicated durable update_id queue with message and
	// edited_message. The callback answer waiter itself remains ephemeral/process-local.
	if r.deps.BotAPIUpdates != nil {
		if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: botUserID,
			Kind:      domain.BotAPIUpdateCallbackQuery,
			Peer:      callback.Peer,
			MessageID: callback.MessageID,
			Date:      int(r.clock.Now().Unix()),
			Callback:  &callback,
		}); err != nil {
			r.log.Warn("enqueue bot api callback query",
				zap.Int64("bot_user_id", botUserID), zap.Int64("query_id", queryID), zap.Error(err))
			return nil, internalErr()
		} else if created {
			r.notifyBotAPIUpdate(botUserID)
		}
	}

	// updateBotCallbackQuery is ephemeral (no pts/qts, not part of getDifference); the private
	// MessageID has been translated to the bot-perspective box id, while channels use the shared
	// message id.
	var update tg.UpdateClass
	if callback.InlineMessage != nil {
		inline := &tg.UpdateInlineBotCallbackQuery{
			QueryID: queryID, UserID: userID,
			MsgID: tgInputBotInlineMessageID(*callback.InlineMessage), ChatInstance: callback.ChatInstance,
		}
		inline.SetData(data)
		update = inline
	} else {
		direct := &tg.UpdateBotCallbackQuery{
			QueryID: queryID, UserID: userID, Peer: tgPeer(callback.Peer),
			MsgID: callback.MessageID, ChatInstance: callback.ChatInstance,
		}
		direct.SetData(data)
		update = direct
	}
	r.pushUserMessage(ctx, botUserID, "push bot callback query", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(r.clock.Now().Unix()),
	})

	return r.waitBotCallbackAnswer(ctx, botUserID, queryID, pending)
}

func (r *Router) waitBotCallbackAnswer(ctx context.Context, botUserID, queryID int64, pending *pendingCallback) (*tg.MessagesBotCallbackAnswer, error) {
	waitCtx, cancel := context.WithTimeout(ctx, botCallbackTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case ans := <-pending.ch:
			return tgBotCallbackAnswer(ans), nil
		case <-ticker.C:
			ans, found, err := r.callbacks.sharedAnswer(waitCtx, botUserID, queryID)
			if err != nil {
				r.log.Warn("read shared bot callback answer", zap.Int64("bot_user_id", botUserID), zap.Int64("query_id", queryID), zap.Error(err))
				continue
			}
			if found {
				return tgBotCallbackAnswer(ans), nil
			}
		case <-waitCtx.Done():
			return nil, botResponseTimeoutErr()
		}
	}
}

// resolveBotCallbackQuery validates the clicked message and resolves the bot-visible message
// identity. Inline-mode via_bot messages require updateInlineBotCallbackQuery + signed inline
// ids and therefore remain an explicit blocked path instead of being misrouted here.
func (r *Router) resolveBotCallbackQuery(ctx context.Context, userID int64, peer domain.Peer, msgID int, data []byte) (domain.BotCallbackQuery, error) {
	if peer.Type == domain.PeerTypeUser {
		msg, found, err := r.lookupOwnerMessage(ctx, userID, msgID)
		if err != nil {
			return domain.BotCallbackQuery{}, internalErr()
		}
		if !found || msg.Peer != peer || msg.ReplyMarkup == nil || msg.ReplyMarkup.Kind() != domain.MessageReplyMarkupInline || msg.ReplyMarkup.IsZero() {
			return domain.BotCallbackQuery{}, messageIDInvalidErr()
		}
		if !replyMarkupContainsCallbackData(msg.ReplyMarkup, data) {
			return domain.BotCallbackQuery{}, dataInvalidErr()
		}
		if msg.ViaBotID != 0 {
			if !r.userIsBot(ctx, msg.ViaBotID) {
				return domain.BotCallbackQuery{}, dataInvalidErr()
			}
			inlineID, ok := r.inputInlineMessageIDForPrivateMessage(msg.ViaBotID, msg).(*tg.InputBotInlineMessageID64)
			if !ok {
				return domain.BotCallbackQuery{}, messageIDInvalidErr()
			}
			return domain.BotCallbackQuery{
				BotUserID: msg.ViaBotID, UserID: userID,
				ChatInstance: chatInstanceFor(msg.ViaBotID, userID), Data: append([]byte(nil), data...),
				InlineMessage: domainInlineMessageID(inlineID),
			}, nil
		}
		if msg.From.Type != domain.PeerTypeUser || msg.From.ID == 0 || !r.userIsBot(ctx, msg.From.ID) {
			return domain.BotCallbackQuery{}, dataInvalidErr()
		}
		provider, ok := r.deps.Messages.(privateMessageByUIDService)
		if !ok || msg.UID == 0 {
			return domain.BotCallbackQuery{}, internalErr()
		}
		botMessage, found, err := provider.GetMessageByUID(ctx, msg.From.ID, msg.UID)
		if err != nil {
			return domain.BotCallbackQuery{}, internalErr()
		}
		if !found || botMessage.ID <= 0 || botMessage.OwnerUserID != msg.From.ID ||
			botMessage.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
			return domain.BotCallbackQuery{}, messageIDInvalidErr()
		}
		return domain.BotCallbackQuery{
			BotUserID:    msg.From.ID,
			UserID:       userID,
			Peer:         botMessage.Peer,
			MessageID:    botMessage.ID,
			ChatInstance: chatInstanceFor(msg.From.ID, userID),
			Data:         append([]byte(nil), data...),
		}, nil
	}
	if peer.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return domain.BotCallbackQuery{}, peerIDInvalidErr()
	}
	history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{msgID})
	if err != nil {
		return domain.BotCallbackQuery{}, channelInvalidErr(err)
	}
	if len(history.Messages) != 1 {
		return domain.BotCallbackQuery{}, messageIDInvalidErr()
	}
	msg := history.Messages[0]
	if msg.ID != msgID || msg.Deleted || msg.ReplyMarkup == nil || msg.ReplyMarkup.Kind() != domain.MessageReplyMarkupInline || msg.ReplyMarkup.IsZero() {
		return domain.BotCallbackQuery{}, messageIDInvalidErr()
	}
	if !replyMarkupContainsCallbackData(msg.ReplyMarkup, data) {
		return domain.BotCallbackQuery{}, dataInvalidErr()
	}
	if msg.ViaBotID != 0 {
		if !r.userIsBot(ctx, msg.ViaBotID) {
			return domain.BotCallbackQuery{}, dataInvalidErr()
		}
		inlineID, ok := r.inputInlineMessageIDForChannelMessage(msg.ViaBotID, msg).(*tg.InputBotInlineMessageID64)
		if !ok {
			return domain.BotCallbackQuery{}, messageIDInvalidErr()
		}
		return domain.BotCallbackQuery{
			BotUserID: msg.ViaBotID, UserID: userID,
			ChatInstance: chatInstanceForPeer(msg.ViaBotID, peer), Data: append([]byte(nil), data...),
			InlineMessage: domainInlineMessageID(inlineID),
		}, nil
	}
	if msg.SenderUserID == 0 || !r.userIsBot(ctx, msg.SenderUserID) {
		return domain.BotCallbackQuery{}, dataInvalidErr()
	}
	return domain.BotCallbackQuery{
		BotUserID:    msg.SenderUserID,
		UserID:       userID,
		Peer:         peer,
		MessageID:    msg.ID,
		ChatInstance: chatInstanceForPeer(msg.SenderUserID, peer),
		Data:         append([]byte(nil), data...),
	}, nil
}

func domainInlineMessageID(id *tg.InputBotInlineMessageID64) *domain.BotInlineMessageID {
	if id == nil {
		return nil
	}
	return &domain.BotInlineMessageID{DCID: id.DCID, OwnerID: id.OwnerID, ID: id.ID, AccessHash: id.AccessHash}
}

func tgInputBotInlineMessageID(id domain.BotInlineMessageID) tg.InputBotInlineMessageIDClass {
	return &tg.InputBotInlineMessageID64{DCID: id.DCID, OwnerID: id.OwnerID, ID: id.ID, AccessHash: id.AccessHash}
}

func replyMarkupContainsCallbackData(markup *domain.MessageReplyMarkup, data []byte) bool {
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

// onMessagesSetBotCallbackAnswer is the bot's response to a callback query: it unblocks a pending
// getBotCallbackAnswer. Only the owning bot may unblock (callerBotID==pending.botUserID, I6).
func (r *Router) onMessagesSetBotCallbackAnswer(ctx context.Context, req *tg.MessagesSetBotCallbackAnswerRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	ans := domain.BotCallbackAnswer{Alert: req.Alert, CacheTime: req.CacheTime}
	if msg, ok := req.GetMessage(); ok {
		if utf8.RuneCountInString(msg) > domain.MaxBotCallbackAnswerLen {
			return false, messageTooLongErr()
		}
		ans.Message = msg
	}
	if url, ok := req.GetURL(); ok {
		ans.URL = url
	}
	// resolve returns whether delivery succeeded; unregistered/expired/non-owner are all false. To
	// the bot it does not matter whether the waiter receives the answer (the official API always
	// returns true), but a non-owner must be refused delivery (to prevent phishing popups).
	if _, err := r.callbacks.resolveContext(ctx, botID, req.QueryID, ans); err != nil {
		return false, internalErr()
	}
	return true, nil
}

func tgBotCallbackAnswer(ans domain.BotCallbackAnswer) *tg.MessagesBotCallbackAnswer {
	out := &tg.MessagesBotCallbackAnswer{Alert: ans.Alert, CacheTime: ans.CacheTime}
	if ans.Message != "" {
		out.SetMessage(ans.Message)
	}
	if ans.URL != "" {
		out.SetURL(ans.URL)
		out.HasURL = true
	}
	return out
}
