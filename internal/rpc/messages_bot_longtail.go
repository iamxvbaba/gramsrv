package rpc

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesSendWebViewData(ctx context.Context, req *tg.MessagesSendWebViewDataRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, botInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Messages == nil {
		return nil, internalErr()
	}
	if req.RandomID == 0 ||
		strings.TrimSpace(req.ButtonText) == "" ||
		utf8.RuneCountInString(req.ButtonText) > domain.MaxWebViewDataButtonTextLen ||
		len(req.Data) > domain.MaxWebViewDataPayloadLen {
		return nil, buttonDataInvalidErr()
	}
	bot, err := r.botUserFromInput(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	if bot.ID == userID {
		return nil, botInvalidErr()
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, userID, bot.ID)
	if err != nil {
		return nil, err
	}
	idempotencyFingerprint, err := rpcRequestFingerprint(req)
	if err != nil {
		return nil, internalErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:           userID,
		RecipientUserID:        bot.ID,
		RandomID:               req.RandomID,
		IdempotencyFingerprint: idempotencyFingerprint,
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionWebViewDataSent,
				WebViewData: &domain.MessageWebViewDataAction{
					ButtonText: req.ButtonText,
					Data:       req.Data,
				},
			},
		},
		Date:             int(r.clock.Now().Unix()),
		OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:  sessionID,
		RecipientBlocked: recipientBlocked,
	})
	if err != nil {
		return nil, messageSendErr(err)
	}
	var users []tg.UserClass
	var chats []tg.ChatClass
	if !res.Duplicate {
		users = r.usersForMessageUpdate(ctx, userID, res.SenderMessage)
		chats = r.chatsForMessageUpdate(ctx, userID, res.SenderMessage)
	}
	return tgPrivateSendResultUpdates(res, req.RandomID, false, users, chats), nil
}

func (r *Router) onMessagesSendBotRequestedPeer(ctx context.Context, req *tg.MessagesSendBotRequestedPeerRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, buttonDataInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Bots == nil || r.deps.Messages == nil || r.deps.Users == nil {
		return nil, internalErr()
	}
	botPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if botPeer.Type != domain.PeerTypeUser {
		return nil, botInvalidErr()
	}
	botUser, found, err := r.deps.Users.ByID(ctx, userID, botPeer.ID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || !botUser.Bot {
		return nil, botInvalidErr()
	}
	webAppReqID, ok := req.GetWebappReqID()
	if !ok || webAppReqID == "" {
		return nil, buttonDataInvalidErr()
	}
	button, found, err := r.deps.Bots.GetRequestedWebViewButton(ctx, botUser.ID, userID, webAppReqID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || button.ButtonID != req.ButtonID {
		return nil, buttonDataInvalidErr()
	}
	if len(req.RequestedPeers) == 0 || len(req.RequestedPeers) > button.MaxQuantity {
		return nil, buttonDataInvalidErr()
	}
	peers := make([]domain.Peer, 0, len(req.RequestedPeers))
	for _, peer := range req.RequestedPeers {
		resolved, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
		if err != nil {
			return nil, err
		}
		if !requestedPeerTypeMatches(button.PeerType, resolved) {
			return nil, buttonDataInvalidErr()
		}
		peers = append(peers, resolved)
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, userID, botUser.ID)
	if err != nil {
		return nil, err
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:    userID,
		RecipientUserID: botUser.ID,
		RandomID:        botRequestedPeerServiceMessageRandomID(userID, botUser.ID, webAppReqID, button.ButtonID, peers),
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionRequestedPeer,
				RequestedPeer: &domain.MessageRequestedPeerAction{
					ButtonID: button.ButtonID,
					Peers:    peers,
				},
			},
		},
		Date:             int(r.clock.Now().Unix()),
		OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:  sessionID,
		RecipientBlocked: recipientBlocked,
	})
	if err != nil {
		return nil, internalErr()
	}
	_ = r.deps.Bots.DeleteRequestedWebViewButton(ctx, botUser.ID, userID, webAppReqID)
	var users []tg.UserClass
	var chats []tg.ChatClass
	if !res.Duplicate {
		users = r.usersForMessageUpdate(ctx, userID, res.SenderMessage)
		chats = r.chatsForMessageUpdate(ctx, userID, res.SenderMessage)
	}
	return tgPrivateSendResultUpdates(res, res.SenderMessage.RandomID, false, users, chats), nil
}

func requestedPeerTypeMatches(kind string, peer domain.Peer) bool {
	switch kind {
	case "user", "":
		return peer.Type == domain.PeerTypeUser
	case "chat", "broadcast":
		return peer.Type == domain.PeerTypeChannel
	default:
		return false
	}
}

func botRequestedPeerServiceMessageRandomID(userID, botUserID int64, reqID string, buttonID int, peers []domain.Peer) int64 {
	parts := []string{"bot-requested-peer", strconv.FormatInt(userID, 10), strconv.FormatInt(botUserID, 10), reqID, strconv.Itoa(buttonID)}
	for _, peer := range peers {
		parts = append(parts, string(peer.Type), strconv.FormatInt(peer.ID, 10))
	}
	return -stableBotAppInt64(parts...)
}

func (r *Router) onMessagesGetPreparedInlineMessage(ctx context.Context, req *tg.MessagesGetPreparedInlineMessageRequest) (*tg.MessagesPreparedInlineMessage, error) {
	if req == nil {
		return nil, botInvalidErr()
	}
	if req.ID == "" {
		return nil, resultIDEmptyErr()
	}
	if len(req.ID) > domain.MaxBotPreparedInlineIDLen {
		return nil, resultIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	bot, err := r.botUserFromInput(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	results, ok := r.inlines.preparedInlineContext(ctx, r.clock.Now(), userID, bot.ID, req.ID)
	if !ok || len(results.Results) != 1 {
		return nil, resultIDInvalidErr()
	}
	out := &tg.MessagesPreparedInlineMessage{
		QueryID:   results.QueryID,
		Result:    tgBotInlineResult(results.Results[0]),
		PeerTypes: tgPreparedInlinePeerTypes(results.PeerTypes),
		CacheTime: results.CacheTime,
		Users:     []tg.UserClass{},
	}
	if r.deps.Users != nil {
		if u, found, err := r.deps.Users.ByID(ctx, userID, bot.ID); err == nil && found {
			out.Users = append(out.Users, r.tgUser(u))
		}
	}
	return out, nil
}

func (r *Router) botUserFromInput(ctx context.Context, userID int64, bot tg.InputUserClass) (domain.User, error) {
	u, found, err := r.userFromInput(ctx, userID, bot)
	if err != nil {
		return domain.User{}, internalErr()
	}
	if !found || !u.Bot {
		return domain.User{}, botInvalidErr()
	}
	return u, nil
}
