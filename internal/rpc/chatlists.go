package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

func (r *Router) registerChatlists(d *tlprofile.Dispatcher) {
	registerRPC[*tg.ChatlistsExportChatlistInviteRequest](d, tlprofile.SemanticMethodChatlistsExportChatlistInvite, func(ctx context.Context, layerRequest *tg.ChatlistsExportChatlistInviteRequest) (any, error) {
		return r.onChatlistsExportChatlistInvite(ctx, layerRequest)
	})
	registerRPC[*tg.ChatlistsDeleteExportedInviteRequest](d, tlprofile.SemanticMethodChatlistsDeleteExportedInvite, func(ctx context.Context, layerRequest *tg.ChatlistsDeleteExportedInviteRequest) (any, error) {
		return r.onChatlistsDeleteExportedInvite(ctx, layerRequest)
	})
	registerRPC[*tg.ChatlistsEditExportedInviteRequest](d, tlprofile.SemanticMethodChatlistsEditExportedInvite, func(ctx context.Context, layerRequest *tg.ChatlistsEditExportedInviteRequest) (any, error) {
		return r.onChatlistsEditExportedInvite(ctx, layerRequest)
	})
	registerRPC[*tg.ChatlistsGetExportedInvitesRequest](d, tlprofile.SemanticMethodChatlistsGetExportedInvites, func(ctx context.Context, layerRequest *tg.ChatlistsGetExportedInvitesRequest) (any, error) {
		return r.onChatlistsGetExportedInvites(ctx, layerRequest.
			Chatlist)
	})
	registerRPC[*tg.ChatlistsCheckChatlistInviteRequest](d, tlprofile.SemanticMethodChatlistsCheckChatlistInvite, func(ctx context.Context, layerRequest *tg.ChatlistsCheckChatlistInviteRequest) (any, error) {
		return r.onChatlistsCheckChatlistInvite(ctx, layerRequest.
			Slug)
	})
	registerRPC[*tg.ChatlistsJoinChatlistInviteRequest](d, tlprofile.SemanticMethodChatlistsJoinChatlistInvite, func(ctx context.Context, layerRequest *tg.ChatlistsJoinChatlistInviteRequest) (any, error) {
		return r.onChatlistsJoinChatlistInvite(ctx, layerRequest)
	})
	registerRPC[*tg.ChatlistsGetChatlistUpdatesRequest](d, tlprofile.SemanticMethodChatlistsGetChatlistUpdates, func(ctx context.Context, layerRequest *tg.ChatlistsGetChatlistUpdatesRequest) (any, error) {
		return r.onChatlistsGetChatlistUpdates(ctx, layerRequest.
			Chatlist)
	})
	registerRPC[*tg.ChatlistsJoinChatlistUpdatesRequest](d, tlprofile.SemanticMethodChatlistsJoinChatlistUpdates, func(ctx context.Context, layerRequest *tg.ChatlistsJoinChatlistUpdatesRequest) (any, error) {
		return r.onChatlistsJoinChatlistUpdates(ctx, layerRequest)
	})
	registerRPC[*tg.ChatlistsHideChatlistUpdatesRequest](d, tlprofile.SemanticMethodChatlistsHideChatlistUpdates, func(ctx context.Context, layerRequest *tg.ChatlistsHideChatlistUpdatesRequest) (any, error) {
		return r.onChatlistsHideChatlistUpdates(ctx, layerRequest.
			Chatlist)
	})
	registerRPC[*tg.ChatlistsGetLeaveChatlistSuggestionsRequest](d, tlprofile.SemanticMethodChatlistsGetLeaveChatlistSuggestions, func(ctx context.Context, layerRequest *tg.ChatlistsGetLeaveChatlistSuggestionsRequest) (any, error) {
		return r.onChatlistsGetLeaveChatlistSuggestions(ctx, layerRequest.
			Chatlist)
	})
	registerRPC[*tg.ChatlistsLeaveChatlistRequest](d, tlprofile.SemanticMethodChatlistsLeaveChatlist, func(ctx context.Context, layerRequest *tg.ChatlistsLeaveChatlistRequest) (any, error) {
		return r.onChatlistsLeaveChatlist(ctx, layerRequest)
	})
}

func (r *Router) onChatlistsExportChatlistInvite(ctx context.Context, req *tg.ChatlistsExportChatlistInviteRequest) (*tg.ChatlistsExportedChatlistInvite, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Chatlists == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peers, err := r.dialogFolderPeersFromInput(ctx, userID, req.Peers)
	if err != nil {
		return nil, err
	}
	folder, invite, err := r.deps.Chatlists.ExportInvite(ctx, userID, chatlistFilterID(req.Chatlist), req.Title, peers, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, chatlistErr(err)
	}
	if err := r.recordChatlistFilterUpdate(ctx, userID, folder.ID, &folder); err != nil {
		return nil, err
	}
	return &tg.ChatlistsExportedChatlistInvite{
		Filter: tgDialogFilter(folder),
		Invite: *r.tgExportedChatlistInvite(invite),
	}, nil
}

func (r *Router) onChatlistsDeleteExportedInvite(ctx context.Context, req *tg.ChatlistsDeleteExportedInviteRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	if r.deps.Chatlists == nil {
		return false, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	folder, changed, err := r.deps.Chatlists.DeleteInvite(ctx, userID, chatlistFilterID(req.Chatlist), req.Slug)
	if err != nil {
		return false, chatlistErr(err)
	}
	if changed {
		if err := r.recordChatlistFilterUpdate(ctx, userID, folder.ID, &folder); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (r *Router) onChatlistsEditExportedInvite(ctx context.Context, req *tg.ChatlistsEditExportedInviteRequest) (*tg.ExportedChatlistInvite, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Chatlists == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	var title *string
	if v, ok := req.GetTitle(); ok {
		title = &v
	}
	var peersPtr *[]domain.DialogFolderPeer
	if v, ok := req.GetPeers(); ok {
		peers, err := r.dialogFolderPeersFromInput(ctx, userID, v)
		if err != nil {
			return nil, err
		}
		peersPtr = &peers
	}
	invite, err := r.deps.Chatlists.EditInvite(ctx, userID, chatlistFilterID(req.Chatlist), req.Slug, title, peersPtr, req.Flags.Has(0))
	if err != nil {
		return nil, chatlistErr(err)
	}
	return r.tgExportedChatlistInvite(invite), nil
}

func (r *Router) onChatlistsGetExportedInvites(ctx context.Context, chatlist tg.InputChatlistDialogFilter) (*tg.ChatlistsExportedInvites, error) {
	if r.deps.Chatlists == nil {
		return &tg.ChatlistsExportedInvites{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	invites, err := r.deps.Chatlists.ListInvites(ctx, userID, chatlistFilterID(chatlist))
	if err != nil {
		return nil, chatlistErr(err)
	}
	out := &tg.ChatlistsExportedInvites{
		Invites: make([]tg.ExportedChatlistInvite, 0, len(invites)),
	}
	allPeers := make([]domain.DialogFolderPeer, 0)
	for _, invite := range invites {
		out.Invites = append(out.Invites, *r.tgExportedChatlistInvite(invite))
		allPeers = append(allPeers, invite.Peers...)
	}
	out.Users, out.Chats = r.tgChatlistPeerEnvelope(ctx, userID, allPeers)
	return out, nil
}

func (r *Router) onChatlistsCheckChatlistInvite(ctx context.Context, slug string) (tg.ChatlistsChatlistInviteClass, error) {
	if r.deps.Chatlists == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	preview, err := r.deps.Chatlists.CheckInvite(ctx, userID, slug)
	if err != nil {
		return nil, chatlistErr(err)
	}
	if preview.LocalFolder != nil {
		filterID := preview.LocalFolder.ID
		if preview.Membership != nil {
			filterID = preview.Membership.LocalFilterID
		}
		users, chats, channels := r.tgChatlistPeerEnvelopeWithChannels(ctx, userID, append(preview.Missing, preview.Already...))
		if preview.Membership != nil {
			r.pushChatlistJoinedChannelNudges(ctx, userID, channels)
		}
		return &tg.ChatlistsChatlistInviteAlready{
			FilterID:     filterID,
			MissingPeers: tgChatlistPeers(preview.Missing),
			AlreadyPeers: tgChatlistPeers(preview.Already),
			Chats:        chats,
			Users:        users,
		}, nil
	}
	users, chats := r.tgChatlistPeerEnvelope(ctx, userID, preview.Missing)
	out := &tg.ChatlistsChatlistInvite{
		TitleNoanimate: preview.OwnerFolder.TitleNoanimate,
		Title: tg.TextWithEntities{
			Text:     preview.OwnerFolder.Title,
			Entities: tgMessageEntities(preview.OwnerFolder.TitleEntities),
		},
		Peers: tgChatlistPeers(preview.Missing),
		Chats: chats,
		Users: users,
	}
	if preview.OwnerFolder.HasEmoticon {
		out.SetEmoticon(preview.OwnerFolder.Emoticon)
	}
	return out, nil
}

func (r *Router) onChatlistsJoinChatlistInvite(ctx context.Context, req *tg.ChatlistsJoinChatlistInviteRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Chatlists == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peers, err := r.dialogFolderPeersFromInput(ctx, userID, req.Peers)
	if err != nil {
		return nil, err
	}
	result, err := r.deps.Chatlists.JoinInvite(ctx, userID, req.Slug, peers, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, chatlistErr(err)
	}
	return r.chatlistOperationUpdates(ctx, userID, result.Folder.ID, &result.Folder, result.ChannelResults, false)
}

func (r *Router) onChatlistsGetChatlistUpdates(ctx context.Context, chatlist tg.InputChatlistDialogFilter) (*tg.ChatlistsChatlistUpdates, error) {
	if r.deps.Chatlists == nil {
		return &tg.ChatlistsChatlistUpdates{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	updates, err := r.deps.Chatlists.GetUpdates(ctx, userID, chatlistFilterID(chatlist))
	if err != nil {
		return nil, chatlistErr(err)
	}
	users, chats := r.tgChatlistPeerEnvelope(ctx, userID, updates.Missing)
	return &tg.ChatlistsChatlistUpdates{
		MissingPeers: tgChatlistPeers(updates.Missing),
		Chats:        chats,
		Users:        users,
	}, nil
}

func (r *Router) onChatlistsJoinChatlistUpdates(ctx context.Context, req *tg.ChatlistsJoinChatlistUpdatesRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Chatlists == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peers, err := r.dialogFolderPeersFromInput(ctx, userID, req.Peers)
	if err != nil {
		return nil, err
	}
	result, err := r.deps.Chatlists.JoinUpdates(ctx, userID, chatlistFilterID(req.Chatlist), peers, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, chatlistErr(err)
	}
	return r.chatlistOperationUpdates(ctx, userID, result.Folder.ID, &result.Folder, result.ChannelResults, false)
}

func (r *Router) onChatlistsHideChatlistUpdates(ctx context.Context, chatlist tg.InputChatlistDialogFilter) (bool, error) {
	if r.deps.Chatlists == nil {
		return false, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if err := r.deps.Chatlists.HideUpdates(ctx, userID, chatlistFilterID(chatlist)); err != nil {
		return false, chatlistErr(err)
	}
	return true, nil
}

func (r *Router) onChatlistsGetLeaveChatlistSuggestions(ctx context.Context, chatlist tg.InputChatlistDialogFilter) ([]tg.PeerClass, error) {
	if r.deps.Chatlists == nil {
		return []tg.PeerClass{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peers, err := r.deps.Chatlists.LeaveSuggestions(ctx, userID, chatlistFilterID(chatlist))
	if err != nil {
		return nil, chatlistErr(err)
	}
	return tgChatlistPeers(peers), nil
}

func (r *Router) onChatlistsLeaveChatlist(ctx context.Context, req *tg.ChatlistsLeaveChatlistRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Chatlists == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peers, err := r.dialogFolderPeersFromInput(ctx, userID, req.Peers)
	if err != nil {
		return nil, err
	}
	result, err := r.deps.Chatlists.Leave(ctx, userID, chatlistFilterID(req.Chatlist), peers, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, chatlistErr(err)
	}
	return r.chatlistOperationUpdates(ctx, userID, result.FilterID, nil, result.ChannelResults, true)
}

func (r *Router) tgExportedChatlistInvite(invite domain.ChatlistInvite) *tg.ExportedChatlistInvite {
	out := &tg.ExportedChatlistInvite{
		Title: invite.Title,
		URL:   r.publicLink("addlist/" + invite.Slug),
		Peers: tgChatlistPeers(invite.Peers),
	}
	if invite.Revoked {
		out.Flags.Set(0)
	}
	return out
}

func (r *Router) tgChatlistPeerEnvelope(ctx context.Context, userID int64, peers []domain.DialogFolderPeer) ([]tg.UserClass, []tg.ChatClass) {
	users, chats, _ := r.tgChatlistPeerEnvelopeWithChannels(ctx, userID, peers)
	return users, chats
}

func (r *Router) tgChatlistPeerEnvelopeWithChannels(ctx context.Context, userID int64, peers []domain.DialogFolderPeer) ([]tg.UserClass, []tg.ChatClass, []domain.Channel) {
	userIDs := make([]int64, 0)
	channelIDs := make([]int64, 0)
	seenUsers := make(map[int64]struct{})
	seenChannels := make(map[int64]struct{})
	for _, item := range peers {
		switch item.Peer.Type {
		case domain.PeerTypeUser:
			if item.Peer.ID == 0 {
				continue
			}
			if _, ok := seenUsers[item.Peer.ID]; ok {
				continue
			}
			seenUsers[item.Peer.ID] = struct{}{}
			userIDs = append(userIDs, item.Peer.ID)
		case domain.PeerTypeChannel:
			if item.Peer.ID == 0 {
				continue
			}
			if _, ok := seenChannels[item.Peer.ID]; ok {
				continue
			}
			seenChannels[item.Peer.ID] = struct{}{}
			channelIDs = append(channelIDs, item.Peer.ID)
		}
	}
	chats := make([]tg.ChatClass, 0, len(channelIDs))
	channels := make([]domain.Channel, 0, len(channelIDs))
	if r.deps.Channels != nil && len(channelIDs) > 0 {
		if views, err := r.deps.Channels.GetChannels(ctx, userID, channelIDs); err == nil {
			for _, view := range views {
				if view.Channel.ID == 0 {
					continue
				}
				channels = append(channels, view.Channel)
				chats = append(chats, tgChannelChatForView(userID, view))
			}
		}
	}
	return r.tgUsersForIDs(ctx, userID, userIDs), chats, channels
}

func tgChatlistPeers(peers []domain.DialogFolderPeer) []tg.PeerClass {
	out := make([]tg.PeerClass, 0, len(peers))
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, item := range peers {
		if _, ok := seen[item.Peer]; ok {
			continue
		}
		seen[item.Peer] = struct{}{}
		if peer := tgPeer(item.Peer); peer != nil {
			out = append(out, peer)
		}
	}
	return out
}

func chatlistFilterID(chatlist tg.InputChatlistDialogFilter) int {
	return chatlist.FilterID
}

func (r *Router) recordChatlistFilterUpdate(ctx context.Context, userID int64, filterID int, folder *domain.DialogFolder) error {
	event := domain.UpdateEvent{
		Type:         domain.UpdateEventDialogFilter,
		FilterID:     filterID,
		DialogFilter: folder,
		Date:         int(r.clock.Now().Unix()),
	}
	var err error
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, _, err = r.deps.Updates.RecordDialogFilter(ctx, authKeyID, userID, filterID, folder, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return internalErr()
		}
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, event)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, tgUpdateForOutboxEvent(event))
	return nil
}

func (r *Router) chatlistFilterUpdates(ctx context.Context, userID int64, filterID int, folder *domain.DialogFolder) (tg.UpdatesClass, error) {
	event := domain.UpdateEvent{
		Type:         domain.UpdateEventDialogFilter,
		FilterID:     filterID,
		DialogFilter: folder,
		Date:         int(r.clock.Now().Unix()),
	}
	var err error
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, _, err = r.deps.Updates.RecordDialogFilter(ctx, authKeyID, userID, filterID, folder, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return nil, internalErr()
		}
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, event)
	out := tgUpdateForOutboxEvent(event)
	if out == nil {
		out = &tg.Updates{Date: event.Date}
	}
	if folder != nil {
		out.Users, out.Chats = r.tgChatlistPeerEnvelope(ctx, userID, append(folder.PinnedPeers, folder.IncludePeers...))
	}
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, out)
	return out, nil
}

func (r *Router) chatlistOperationUpdates(ctx context.Context, userID int64, filterID int, folder *domain.DialogFolder, channelResults []domain.CreateChannelResult, leaving bool) (tg.UpdatesClass, error) {
	base, err := r.chatlistFilterUpdates(ctx, userID, filterID, folder)
	if err != nil {
		return nil, err
	}
	out, ok := base.(*tg.Updates)
	if !ok {
		out = &tg.Updates{Date: int(r.clock.Now().Unix())}
	}
	for _, res := range channelResults {
		if res.Channel.ID == 0 {
			continue
		}
		r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
		if leaving {
			r.removeOnlineChannelMemberships(res.Channel.ID, userID)
			r.recordChannelStateForUser(ctx, userID, res.Channel.ID, true)
		} else {
			r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
		}
		channelUpdates := r.channelOperationUpdates(ctx, userID, res)
		out = mergeUpdates(out, channelUpdates)
		if !leaving {
			if nudge := chatlistJoinedChannelNudge(res.Channel); nudge != nil {
				out.Updates = append(out.Updates, nudge)
			}
		}
		r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
			return r.channelOperationUpdates(ctx, viewerUserID, res)
		})
	}
	return out, nil
}

func chatlistJoinedChannelNudge(channel domain.Channel) tg.UpdateClass {
	if channel.ID == 0 || channel.Pts <= 0 {
		return nil
	}
	update := &tg.UpdateChannelTooLong{ChannelID: channel.ID}
	update.SetPts(channel.Pts)
	return update
}

func (r *Router) pushChatlistJoinedChannelNudges(ctx context.Context, userID int64, channels []domain.Channel) {
	if userID == 0 || len(channels) == 0 {
		return
	}
	updates := make([]tg.UpdateClass, 0, len(channels))
	for _, channel := range channels {
		if nudge := chatlistJoinedChannelNudge(channel); nudge != nil {
			updates = append(updates, nudge)
		}
	}
	if len(updates) == 0 {
		return
	}
	out := &tg.Updates{
		Updates: updates,
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
	if _, ok := SessionIDFrom(ctx); ok {
		r.pushCurrentSessionMessage(ctx, "push chatlist joined channel nudges", out)
		return
	}
	r.pushUserUpdates(ctx, userID, out)
}

func chatlistErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrInviteRequestSent),
		errors.Is(err, domain.ErrUserAlreadyParticipant),
		errors.Is(err, domain.ErrChannelUserBanned),
		errors.Is(err, domain.ErrUserKicked),
		errors.Is(err, domain.ErrBotGroupsBlocked):
		return channelInviteErr(err)
	case errors.Is(err, domain.ErrChannelUserCreator),
		errors.Is(err, domain.ErrUserNotParticipant),
		errors.Is(err, domain.ErrChannelAdminRequired):
		return channelAdminErr(err)
	case errors.Is(err, domain.ErrChannelInvalid),
		errors.Is(err, domain.ErrChannelPrivate):
		return channelInvalidErr(err)
	case errors.Is(err, domain.ErrChatlistInviteInvalid):
		return inviteSlugEmptyErr()
	case errors.Is(err, domain.ErrChatlistInviteExpired):
		return inviteSlugExpiredErr()
	case errors.Is(err, domain.ErrChatlistInvitesTooMuch):
		return invitesTooMuchErr()
	case errors.Is(err, domain.ErrChatlistsTooMuch):
		return chatlistsTooMuchErr()
	case errors.Is(err, domain.ErrChatlistPeersEmpty):
		return peersListEmptyErr()
	case errors.Is(err, domain.ErrChatlistPeersTooMuch):
		return limitInvalidErr()
	case errors.Is(err, domain.ErrChatlistNotShareable):
		return filterNotSupportedErr()
	case errors.Is(err, domain.ErrChatlistInvalid):
		return filterIDInvalidErr()
	default:
		return internalErr()
	}
}
