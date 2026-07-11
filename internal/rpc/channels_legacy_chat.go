package rpc

import (
	"context"
	"errors"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onMessagesCreateChat(ctx context.Context, req *tg.MessagesCreateChatRequest) (*tg.MessagesInvitedUsers, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if !validChannelTitle(req.Title) || len(req.Users) > domain.MaxChannelInviteUsers {
		return nil, channelInvalidErr(domain.ErrChannelTitleInvalid)
	}
	if req.TTLPeriod < 0 {
		return nil, ttlPeriodInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	memberIDs, err := r.userIDsFromInputUsers(ctx, userID, req.Users)
	if err != nil {
		return nil, err
	}
	memberIDs = createChatInviteMemberIDs(memberIDs, userID)
	date := int(r.clock.Now().Unix())
	r.log.Debug("messages.createChat resolved users",
		zap.Int("input_users", len(req.Users)),
		zap.Int("member_ids", len(memberIDs)),
		zap.Int64s("member_user_ids", memberIDs),
	)
	if len(memberIDs) == 0 {
		return nil, usersTooFewErr()
	}
	createRes, err := r.deps.Channels.CreateMegagroupFromCreateChat(ctx, userID, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         req.Title,
		TTLPeriod:     req.TTLPeriod,
		Date:          date,
	})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	r.addOnlineChannelMemberships(createRes.Channel.ID, channelMemberUserIDs(createRes.Members)...)

	responseRes := createRes
	var inviteRes domain.CreateChannelResult
	if len(memberIDs) > 0 {
		inviteRes, err = r.deps.Channels.InviteToChannel(ctx, userID, createRes.Channel.ID, memberIDs, date)
		if err != nil {
			return nil, channelInviteErr(err)
		}
		r.addOnlineChannelMemberships(inviteRes.Channel.ID, channelMemberUserIDs(inviteRes.Members)...)
		responseRes.Channel = inviteRes.Channel
		responseRes.Members = mergeChannelMembers(createRes.Members, inviteRes.Members)
		responseRes.Recipients = uniqueRecipientIDs(append(append([]int64{}, createRes.Recipients...), inviteRes.Recipients...))
	}

	cache := newViewerPeerCache(r)
	updates := r.channelOperationUpdatesWithPeerCache(ctx, userID, responseRes, cache)
	if createChatNeedsLegacyChat(ctx) {
		updates = r.tdesktopCreateChatUpdatesWithPeerCache(ctx, userID, responseRes, cache)
	}
	if inviteRes.Event.Pts != 0 {
		inviteUpdates := r.channelOperationUpdatesWithPeerCache(ctx, userID, inviteRes, cache)
		if inviteUpdates != nil {
			updates.Updates = append(updates.Updates, inviteUpdates.Updates...)
		}
	}
	if inviteRes.Event.Pts != 0 {
		r.pushChannelExplicitUpdates(ctx, userID, inviteRes.Channel.ID, memberIDs, func(viewerUserID int64) *tg.Updates {
			return r.channelOperationUpdatesWithPeerCache(ctx, viewerUserID, inviteRes, cache)
		})
	}
	return &tg.MessagesInvitedUsers{Updates: updates, MissingInvitees: []tg.MissingInvitee{}}, nil
}

func (r *Router) onMessagesMigrateChat(ctx context.Context, chatID int64) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if chatID <= 0 {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	userID, view, err := r.channelChangeInfoView(ctx, &tg.InputChannel{ChannelID: chatID})
	if err != nil {
		return nil, err
	}
	if !view.Channel.Megagroup {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	return r.channelStateUpdates(userID, view.Channel), nil
}

func (r *Router) onMessagesGetChats(ctx context.Context, ids []int64) (tg.MessagesChatsClass, error) {
	if len(ids) > maxGetMessagesIDs {
		return nil, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	chats := make([]tg.ChatClass, 0, len(ids))
	if r.deps.Channels != nil {
		unique := make([]int64, 0, len(ids))
		seen := make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			if id <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
		views, err := r.deps.Channels.GetChannels(ctx, userID, unique)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		byID := make(map[int64]domain.ChannelView, len(views))
		for _, view := range views {
			if view.Channel.ID != 0 {
				byID[view.Channel.ID] = view
			}
		}
		for _, id := range ids {
			if id <= 0 {
				continue
			}
			if view, ok := byID[id]; ok {
				chats = append(chats, tgChannelChatForView(userID, view))
			}
		}
	}
	return &tg.MessagesChats{Chats: chats}, nil
}

func (r *Router) onMessagesGetFullChat(ctx context.Context, chatID int64) (*tg.MessagesChatFull, error) {
	if chatID <= 0 {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	return r.onChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: chatID})
}

func (r *Router) onMessagesAddChatUser(ctx context.Context, req *tg.MessagesAddChatUserRequest) (*tg.MessagesInvitedUsers, error) {
	if req.ChatID <= 0 || req.UserID == nil {
		return nil, peerIDInvalidErr()
	}
	return r.onChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: req.ChatID},
		Users:   []tg.InputUserClass{req.UserID},
	})
}

func (r *Router) onMessagesDeleteChatUser(ctx context.Context, req *tg.MessagesDeleteChatUserRequest) (tg.UpdatesClass, error) {
	if req.ChatID <= 0 || req.UserID == nil {
		return nil, peerIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	target, found, err := r.userFromInput(ctx, userID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || target.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	if target.ID == userID {
		return r.onChannelsLeaveChannel(ctx, &tg.InputChannel{ChannelID: req.ChatID})
	}
	return r.onChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
		Channel:     &tg.InputChannel{ChannelID: req.ChatID},
		Participant: &tg.InputPeerUser{UserID: target.ID, AccessHash: target.AccessHash},
		BannedRights: tg.ChatBannedRights{
			ViewMessages: true,
			UntilDate:    0,
		},
	})
}

func (r *Router) onMessagesEditChatTitle(ctx context.Context, req *tg.MessagesEditChatTitleRequest) (tg.UpdatesClass, error) {
	if req.ChatID <= 0 {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	return r.onChannelsEditTitle(ctx, &tg.ChannelsEditTitleRequest{
		Channel: &tg.InputChannel{ChannelID: req.ChatID},
		Title:   req.Title,
	})
}

func (r *Router) onMessagesEditChatPhoto(ctx context.Context, req *tg.MessagesEditChatPhotoRequest) (tg.UpdatesClass, error) {
	if req.ChatID <= 0 || req.Photo == nil {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	return r.onChannelsEditPhoto(ctx, &tg.ChannelsEditPhotoRequest{
		Channel: &tg.InputChannel{ChannelID: req.ChatID},
		Photo:   req.Photo,
	})
}

func (r *Router) onMessagesEditChatAdmin(ctx context.Context, req *tg.MessagesEditChatAdminRequest) (bool, error) {
	if req.ChatID <= 0 || req.UserID == nil {
		return false, peerIDInvalidErr()
	}
	rights := tg.ChatAdminRights{}
	if req.IsAdmin {
		rights = legacyBasicGroupAdminRights()
	}
	_, err := r.onChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{
		Channel:     &tg.InputChannel{ChannelID: req.ChatID},
		UserID:      req.UserID,
		AdminRights: rights,
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onMessagesEditChatAbout(ctx context.Context, req *tg.MessagesEditChatAboutRequest) (bool, error) {
	if r.deps.Channels == nil {
		return false, notImplementedErr()
	}
	if utf8.RuneCountInString(req.About) > maxChannelAboutLength {
		return false, channelInvalidErr(domain.ErrChannelInvalid)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	channelID, err := r.channelIDFromLegacyInputPeerChecked(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	channel, err := r.deps.Channels.EditAbout(ctx, userID, domain.EditChannelAboutRequest{
		UserID:    userID,
		ChannelID: channelID,
		About:     req.About,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return false, channelAdminErr(err)
	}
	r.pushChannelStateToMembers(ctx, userID, channel)
	return true, nil
}

func (r *Router) onMessagesEditChatDefaultBannedRights(ctx context.Context, req *tg.MessagesEditChatDefaultBannedRightsRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	channel, err := r.deps.Channels.EditDefaultBannedRights(ctx, userID, domain.EditChannelDefaultBannedRightsRequest{
		UserID:       userID,
		ChannelID:    peer.ID,
		BannedRights: domainChannelBannedRights(req.BannedRights),
		Date:         int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelAdminErr(err)
	}
	return r.channelStateMutationUpdates(ctx, userID, channel), nil
}

func (r *Router) onMessagesEditChatCreator(ctx context.Context, req *tg.MessagesEditChatCreatorRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if req.UserID == nil {
		return nil, peerIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromLegacyInputPeerChecked(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if req.Password == nil {
		return nil, passwordHashInvalidErr()
	}
	if _, ok := req.UserID.(*tg.InputUserEmpty); ok {
		return nil, passwordHashInvalidErr()
	}
	if _, ok := req.Password.(*tg.InputCheckPasswordEmpty); ok {
		return nil, passwordHashInvalidErr()
	}
	target, found, err := r.userFromInput(ctx, userID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || target.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	if target.Bot {
		return nil, userIDInvalidErr()
	}
	if r.deps.Account == nil {
		return nil, passwordHashInvalidErr()
	}
	if err := r.deps.Account.CheckPassword(ctx, userID, domainPasswordCheck(req.Password)); err != nil {
		return nil, passwordErr(err)
	}
	res, err := r.deps.Channels.TransferOwnership(ctx, userID, domain.TransferChannelOwnershipRequest{
		UserID:     userID,
		ChannelID:  channelID,
		NewOwnerID: target.ID,
		Date:       int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelTransferErr(err)
	}
	r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
	r.addOnlineChannelMemberships(res.Channel.ID, res.OldOwner.UserID, res.NewOwner.UserID)
	cache := newViewerPeerCache(r)
	updates := r.channelOwnershipTransferUpdatesWithPeerCache(ctx, userID, userID, res, cache)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOwnershipTransferUpdatesWithPeerCache(ctx, viewerUserID, userID, res, cache)
	})
	return updates, nil
}

func (r *Router) onMessagesGetFutureChatCreatorAfterLeave(ctx context.Context, peer tg.InputPeerClass) (tg.UserClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	resolved, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil {
		return nil, err
	}
	if resolved.Type != domain.PeerTypeChannel || resolved.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	member, err := r.deps.Channels.FutureCreatorAfterLeave(ctx, userID, resolved.ID)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	if r.deps.Users == nil {
		return nil, userIDInvalidErr()
	}
	user, found, err := r.deps.Users.ByID(ctx, userID, member.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || user.Bot {
		return nil, tgerr400("USER_NOT_PARTICIPANT")
	}
	users := r.tgUsersForViewer(userID, []domain.User{user})
	if len(users) == 0 {
		return nil, userIDInvalidErr()
	}
	return users[0], nil
}

func (r *Router) onMessagesEditChatParticipantRank(ctx context.Context, req *tg.MessagesEditChatParticipantRankRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if len(req.Rank) > domain.MaxChannelAdminRankLength {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromLegacyInputPeerChecked(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	participant, ok := r.domainPeerFromInputPeer(userID, req.Participant)
	if !ok || participant.Type != domain.PeerTypeUser || participant.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	res, err := r.deps.Channels.EditMemberRank(ctx, userID, domain.EditChannelMemberRankRequest{
		UserID:    userID,
		ChannelID: channelID,
		MemberID:  participant.ID,
		Rank:      req.Rank,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelAdminErr(err)
	}
	cache := newViewerPeerCache(r)
	updates := r.channelParticipantUpdatesWithPeerCache(ctx, userID, userID, res.Channel, res.Previous, res.Participant, res.Date, cache)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelParticipantUpdatesWithPeerCache(ctx, viewerUserID, userID, res.Channel, res.Previous, res.Participant, res.Date, cache)
	})
	return updates, nil
}

func (r *Router) onMessagesSetChatTheme(ctx context.Context, req *tg.MessagesSetChatThemeRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if channelID, err := r.channelIDFromLegacyInputPeerChecked(ctx, userID, req.Peer); err == nil {
		if r.deps.Channels == nil {
			return nil, notImplementedErr()
		}
		view, err := r.deps.Channels.GetChannel(ctx, userID, channelID)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		return r.channelStateUpdates(userID, view.Channel), nil
	} else if _, ok := channelIDFromLegacyInputPeer(userID, req.Peer); ok {
		return nil, err
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.Peer)
	if !ok || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	emoticon, err := chatThemeEmoticonFromInput(req.Theme)
	if err != nil {
		return nil, err
	}
	if r.deps.Messages == nil {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, userID, peer.ID)
	if err != nil {
		return nil, err
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.SetChatTheme(ctx, userID, domain.SetPrivateChatThemeRequest{
		OwnerUserID:      userID,
		Peer:             peer,
		Emoticon:         emoticon,
		Date:             int(r.clock.Now().Unix()),
		OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:  sessionID,
		RecipientBlocked: recipientBlocked,
	})
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return nil, peerIDInvalidErr()
		}
		return nil, internalErr()
	}
	if res.Changed {
		r.invalidateRPCProjectionForPeer(userID, peer)
		r.invalidateRPCProjectionForPeer(peer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	}
	if !res.Changed || res.Send.SenderMessage.ID == 0 {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	return tgPrivateMessageUpdates(
		res.Send.SenderEvent,
		res.Send.SenderMessage,
		0,
		false,
		r.usersForMessageUpdate(ctx, userID, res.Send.SenderMessage),
		[]tg.ChatClass{},
	), nil
}

func chatThemeEmoticonFromInput(theme tg.InputChatThemeClass) (string, error) {
	switch value := theme.(type) {
	case *tg.InputChatThemeEmpty:
		return "", nil
	case *tg.InputChatTheme:
		if value.Emoticon == "" {
			return "", nil
		}
		if !tdesktop.IsChatThemeEmoticon(value.Emoticon) {
			return "", themeInvalidErr()
		}
		return value.Emoticon, nil
	case *tg.InputChatThemeUniqueGift:
		return "", themeInvalidErr()
	default:
		return "", inputConstructorInvalidErr()
	}
}

func (r *Router) onMessagesSetChatWallPaper(ctx context.Context, req *tg.MessagesSetChatWallPaperRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputConstructorInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	wallpaper, err := domainWallpaperFromSetChatWallPaper(req)
	if err != nil {
		return nil, err
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		if peer.ID == 0 {
			return nil, peerIDInvalidErr()
		}
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil || peer.ID == 0 {
			return nil, peerIDInvalidErr()
		}
		res, err := r.deps.Channels.SetWallpaper(ctx, userID, domain.SetChannelWallpaperRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			Wallpaper: wallpaper,
			Date:      int(r.clock.Now().Unix()),
		})
		if err != nil {
			return nil, channelAdminErr(err)
		}
		r.invalidateRPCProjectionForChannel(res.Channel.ID)
		if res.Changed && res.Event.Pts != 0 {
			r.enqueueChannelWallpaperFanout(ctx, userID, res)
		} else if res.Changed {
			r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
				return r.channelWallpaperUpdatesWithPeerCache(ctx, viewerUserID, res, nil)
			})
		}
		return r.channelWallpaperUpdatesWithPeerCache(ctx, userID, res, nil), nil
	default:
		return nil, peerIDInvalidErr()
	}
}

func (r *Router) channelWallpaperUpdatesWithPeerCache(ctx context.Context, viewerUserID int64, res domain.SetChannelWallpaperResult, cache *viewerPeerCache) *tg.Updates {
	update := tgUpdatePeerWallpaper(domain.Peer{Type: domain.PeerTypeChannel, ID: res.Channel.ID}, res.Channel.Wallpaper)
	if res.Event.Pts != 0 {
		sendRes := domain.SendChannelMessageResult{
			Channel:    res.Channel,
			Message:    res.Message,
			Event:      res.Event,
			Recipients: res.Recipients,
		}
		updates := r.channelMessageUpdatesWithPeerCache(ctx, viewerUserID, sendRes, 0, cache)
		updates.Updates = append(updates.Updates, update)
		return updates
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, res.Channel)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func (r *Router) enqueueChannelWallpaperFanout(ctx context.Context, originUserID int64, res domain.SetChannelWallpaperResult) {
	sendRes := domain.SendChannelMessageResult{
		Channel:    res.Channel,
		Message:    res.Message,
		Event:      res.Event,
		Recipients: res.Recipients,
	}
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelMessageFanoutOwnerIDs(sendRes, nil)
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMembers, originUserID, res.Channel.ID, res.Event.Pts, res.Recipients,
		0,
		func(bgCtx context.Context, viewers []int64) {
			r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs)
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.channelWallpaperUpdatesWithPeerCache(bgCtx, viewerUserID, res, fanoutCache)
		})
}

func (r *Router) onMessagesToggleNoForwards(ctx context.Context, req *tg.MessagesToggleNoForwardsRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromLegacyInputPeerChecked(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	channel, err := r.deps.Channels.SetNoForwards(ctx, userID, channelID, req.Enabled)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	return r.channelStateMutationUpdates(ctx, userID, channel), nil
}

func (r *Router) onMessagesSetChatAvailableReactions(ctx context.Context, req *tg.MessagesSetChatAvailableReactionsRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if req == nil {
		return nil, tgerr400("REACTION_INVALID")
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromLegacyInputPeerChecked(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	current, err := r.deps.Channels.GetChannelForChangeInfo(ctx, userID, channelID)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	_, defaultReactionDocuments := r.availableReactionDocumentMaps(ctx)
	policy, err := domainChannelReactionPolicy(req, current.Channel.ReactionPolicy, defaultReactionDocuments)
	if err != nil {
		return nil, err
	}
	channel, err := r.deps.Channels.SetAvailableReactions(ctx, userID, channelID, policy)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	return r.channelStateMutationUpdates(ctx, userID, channel), nil
}

func legacyBasicGroupAdminRights() tg.ChatAdminRights {
	return tg.ChatAdminRights{
		ChangeInfo:     true,
		DeleteMessages: true,
		BanUsers:       true,
		InviteUsers:    true,
		PinMessages:    true,
		Other:          true,
	}
}
