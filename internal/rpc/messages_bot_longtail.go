package rpc

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

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
	r.enqueueBotAPIPrivateMessageUpdateAsync(ctx, res)
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
	webAppReqID, fromWebApp := req.GetWebappReqID()
	idempotencyKey := webAppReqID
	var button domain.BotRequestedWebViewButton
	if fromWebApp {
		if webAppReqID == "" {
			return nil, buttonDataInvalidErr()
		}
		var found bool
		button, found, err = r.deps.Bots.GetRequestedWebViewButton(ctx, botUser.ID, userID, webAppReqID)
		if err != nil {
			return nil, internalErr()
		}
		if !found || button.ButtonID != req.ButtonID {
			return nil, buttonDataInvalidErr()
		}
	} else {
		idempotencyKey = "message:" + strconv.Itoa(req.MsgID)
		button, err = r.requestPeerButtonFromMessage(ctx, userID, botUser.ID, req.MsgID, req.ButtonID)
		if err != nil {
			return nil, err
		}
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
		if matches, err := r.requestedPeerMatches(ctx, userID, botUser.ID, button, resolved); err != nil {
			return nil, internalErr()
		} else if !matches {
			return nil, buttonDataInvalidErr()
		}
		peers = append(peers, resolved)
	}
	details, err := r.requestedPeerDetails(ctx, userID, peers, button)
	if err != nil {
		return nil, internalErr()
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, userID, botUser.ID)
	if err != nil {
		return nil, err
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:    userID,
		RecipientUserID: botUser.ID,
		RandomID:        botRequestedPeerServiceMessageRandomID(userID, botUser.ID, idempotencyKey, button.ButtonID, peers),
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionRequestedPeer,
				RequestedPeer: &domain.MessageRequestedPeerAction{
					ButtonID:          button.ButtonID,
					Peers:             peers,
					Details:           details,
					NameRequested:     button.NameRequested,
					UsernameRequested: button.UsernameRequested,
					PhotoRequested:    button.PhotoRequested,
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
	r.enqueueBotAPIPrivateMessageUpdateAsync(ctx, res)
	if fromWebApp {
		_ = r.deps.Bots.DeleteRequestedWebViewButton(ctx, botUser.ID, userID, webAppReqID)
	}
	var users []tg.UserClass
	var chats []tg.ChatClass
	if !res.Duplicate {
		users = r.usersForMessageUpdate(ctx, userID, res.SenderMessage)
		chats = r.chatsForMessageUpdate(ctx, userID, res.SenderMessage)
	}
	return tgPrivateSendResultUpdates(res, res.SenderMessage.RandomID, false, users, chats), nil
}

type requestedPeerPhotoProvider interface {
	GetPhotos(ctx context.Context, ids []int64) ([]domain.Photo, error)
}

func (r *Router) requestedPeerDetails(ctx context.Context, viewerUserID int64, peers []domain.Peer, button domain.BotRequestedWebViewButton) ([]domain.MessageRequestedPeerDetails, error) {
	details := make([]domain.MessageRequestedPeerDetails, len(peers))
	for i, peer := range peers {
		details[i].Peer = peer
	}
	if !button.NameRequested && !button.UsernameRequested && !button.PhotoRequested {
		return details, nil
	}
	userIDs := make(map[int64]struct{})
	channelIDs := make(map[int64]struct{})
	for _, peer := range peers {
		addDomainPeerRef(peer, 0, userIDs, channelIDs)
	}
	cache := newViewerPeerCache(r)
	users := cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs))
	channels := cache.channelsForIDs(ctx, viewerUserID, mapKeys(channelIDs))
	userByID := make(map[int64]domain.User, len(users))
	channelByID := make(map[int64]domain.Channel, len(channels))
	photoIDs := make([]int64, 0, len(peers))
	for _, user := range users {
		userByID[user.ID] = user
		if button.PhotoRequested && user.PhotoID != 0 {
			photoIDs = append(photoIDs, user.PhotoID)
		}
	}
	for _, channel := range channels {
		channelByID[channel.ID] = channel
		if button.PhotoRequested && channel.PhotoID != 0 {
			photoIDs = append(photoIDs, channel.PhotoID)
		}
	}
	photoByID := make(map[int64]domain.Photo, len(photoIDs))
	if len(photoIDs) > 0 {
		provider, ok := r.deps.Files.(requestedPeerPhotoProvider)
		if !ok {
			return nil, fmt.Errorf("requested peer photo provider unavailable")
		}
		photos, err := provider.GetPhotos(ctx, photoIDs)
		if err != nil {
			return nil, err
		}
		for _, photo := range photos {
			photoByID[photo.ID] = photo
		}
	}
	for i, peer := range peers {
		detail := &details[i]
		switch peer.Type {
		case domain.PeerTypeUser:
			user, ok := userByID[peer.ID]
			if !ok {
				return nil, fmt.Errorf("requested user %d not hydrated", peer.ID)
			}
			if button.NameRequested {
				detail.FirstName, detail.LastName = user.FirstName, user.LastName
			}
			if button.UsernameRequested {
				detail.Username = user.Username
			}
			if button.PhotoRequested && user.PhotoID != 0 {
				photo, ok := photoByID[user.PhotoID]
				if !ok {
					return nil, fmt.Errorf("requested user photo %d missing", user.PhotoID)
				}
				detail.Photo = &photo
			}
		case domain.PeerTypeChannel:
			channel, ok := channelByID[peer.ID]
			if !ok {
				return nil, fmt.Errorf("requested channel %d not hydrated", peer.ID)
			}
			if button.NameRequested {
				detail.Title = channel.Title
			}
			if button.UsernameRequested {
				detail.Username = channel.Username
			}
			if button.PhotoRequested && channel.PhotoID != 0 {
				photo, ok := photoByID[channel.PhotoID]
				if !ok {
					return nil, fmt.Errorf("requested channel photo %d missing", channel.PhotoID)
				}
				detail.Photo = &photo
			}
		}
	}
	return details, nil
}

func (r *Router) requestPeerButtonFromMessage(ctx context.Context, userID, botUserID int64, messageID, buttonID int) (domain.BotRequestedWebViewButton, error) {
	if messageID <= 0 || messageID > domain.MaxMessageBoxID || buttonID == 0 {
		return domain.BotRequestedWebViewButton{}, buttonDataInvalidErr()
	}
	message, found, err := r.lookupOwnerMessage(ctx, userID, messageID)
	if err != nil {
		return domain.BotRequestedWebViewButton{}, internalErr()
	}
	if !found || message.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: botUserID}) ||
		message.From != (domain.Peer{Type: domain.PeerTypeUser, ID: botUserID}) || message.ReplyMarkup == nil ||
		message.ReplyMarkup.Kind() != domain.MessageReplyMarkupKeyboard {
		return domain.BotRequestedWebViewButton{}, buttonDataInvalidErr()
	}
	for _, row := range message.ReplyMarkup.Keyboard {
		for _, item := range row {
			if item.Type != domain.MarkupButtonRequestPeer || item.ButtonID != buttonID {
				continue
			}
			return domain.BotRequestedWebViewButton{
				BotUserID: botUserID, UserID: userID, ButtonID: item.ButtonID,
				PeerType: item.RequestPeerType, MaxQuantity: item.MaxQuantity, PeerFilter: item.RequestPeerFilter,
				NameRequested: item.NameRequested, UsernameRequested: item.UsernameRequested,
				PhotoRequested: item.PhotoRequested,
			}, nil
		}
	}
	return domain.BotRequestedWebViewButton{}, buttonDataInvalidErr()
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

func (r *Router) requestedPeerMatches(ctx context.Context, userID, botUserID int64, button domain.BotRequestedWebViewButton, peer domain.Peer) (bool, error) {
	if !requestedPeerTypeMatches(button.PeerType, peer) {
		return false, nil
	}
	filter := button.PeerFilter
	if filter == nil {
		return true, nil
	}
	if peer.Type == domain.PeerTypeUser {
		if r.deps.Users == nil {
			return false, nil
		}
		user, found, err := r.deps.Users.ByID(ctx, userID, peer.ID)
		if err != nil || !found {
			return false, err
		}
		if filter.UserIsBotSet && user.Bot != filter.UserIsBot {
			return false, nil
		}
		if filter.UserIsPremiumSet && user.PremiumActiveAt(r.clock.Now().Unix()) != filter.UserIsPremium {
			return false, nil
		}
		return true, nil
	}
	if r.deps.Channels == nil {
		return false, nil
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, peer.ID)
	if err != nil {
		return false, err
	}
	channel := view.Channel
	if button.PeerType == "chat" && (!channel.Megagroup || channel.Broadcast) {
		return false, nil
	}
	if button.PeerType == "broadcast" && !channel.Broadcast {
		return false, nil
	}
	if filter.ChatHasUsernameSet && (channel.Username != "") != filter.ChatHasUsername {
		return false, nil
	}
	if filter.ChatIsForumSet && channel.Forum != filter.ChatIsForum {
		return false, nil
	}
	if filter.ChatIsCreated && view.Self.Role != domain.ChannelRoleCreator {
		return false, nil
	}
	if filter.UserAdminRights != nil && !channelMemberHasRequestRights(view.Self, *filter.UserAdminRights) {
		return false, nil
	}
	if filter.BotIsMember || filter.BotAdminRights != nil {
		botMember, err := r.deps.Channels.GetParticipant(ctx, userID, peer.ID, botUserID)
		if err != nil {
			return false, err
		}
		if botMember.Status != domain.ChannelMemberActive {
			return false, nil
		}
		if filter.BotAdminRights != nil && !channelMemberHasRequestRights(botMember, *filter.BotAdminRights) {
			return false, nil
		}
	}
	return true, nil
}

func channelMemberHasRequestRights(member domain.ChannelMember, required domain.BotRequestAdminRights) bool {
	if member.Role == domain.ChannelRoleCreator {
		return true
	}
	if member.Role != domain.ChannelRoleAdmin {
		return false
	}
	rights := member.AdminRights
	return (!required.Anonymous || rights.Anonymous) &&
		(!required.ManageChat || rights.ManageChat) &&
		(!required.DeleteMessages || rights.DeleteMessages) &&
		(!required.ManageVideoChats || rights.ManageCall) &&
		(!required.RestrictMembers || rights.BanUsers) &&
		(!required.PromoteMembers || rights.AddAdmins) &&
		(!required.ChangeInfo || rights.ChangeInfo) &&
		(!required.InviteUsers || rights.InviteUsers) &&
		(!required.PostStories || rights.PostStories) &&
		(!required.EditStories || rights.EditStories) &&
		(!required.DeleteStories || rights.DeleteStories) &&
		(!required.PostMessages || rights.PostMessages) &&
		(!required.EditMessages || rights.EditMessages) &&
		(!required.PinMessages || rights.PinMessages) &&
		(!required.ManageTopics || rights.ManageTopics) &&
		(!required.ManageDirectMessages || rights.ManageDirectMessages)
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
