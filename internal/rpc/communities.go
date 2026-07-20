package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

const communitiesLayer = 228

func communityErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrCommunityPrivate):
		return tgerr400("CHANNEL_PRIVATE")
	case errors.Is(err, domain.ErrCommunityAdminRequired):
		return tgerr400("CHAT_ADMIN_REQUIRED")
	case errors.Is(err, domain.ErrCommunityCreatorRequired):
		return tgerr400("CHAT_ADMIN_REQUIRED")
	case errors.Is(err, domain.ErrCommunityPeersTooMuch):
		return tgerr400("COMMUNITY_PEERS_TOO_MUCH")
	case errors.Is(err, domain.ErrCommunityRequestCreated):
		return tgerr400("COMMUNITY_REQUEST_CREATED")
	case errors.Is(err, domain.ErrCommunityRequestMissing):
		return tgerr400("COMMUNITY_REQUEST_MISSING")
	case errors.Is(err, domain.ErrCommunityPeerLinked):
		return tgerr400("COMMUNITY_PEER_ALREADY_LINKED")
	case errors.Is(err, domain.ErrCommunityPeerInvalid), errors.Is(err, domain.ErrCommunityParticipantInvalid):
		return peerIDInvalidErr()
	case errors.Is(err, domain.ErrChannelTitleInvalid):
		return tgerr400("CHAT_TITLE_EMPTY")
	case errors.Is(err, domain.ErrAboutTooLong):
		return aboutTooLongErr()
	case errors.Is(err, domain.ErrCommunityInvalid):
		return channelInvalidErr(err)
	default:
		return internalErr()
	}
}

func (r *Router) communityFromInput(ctx context.Context, userID int64, input tg.InputChannelClass) (domain.CommunityView, error) {
	if r.deps.Communities == nil {
		return domain.CommunityView{}, notImplementedErr()
	}
	ref, ok := inputChannelRef(input)
	if !ok || ref.ID == 0 {
		return domain.CommunityView{}, channelInvalidErr(domain.ErrCommunityInvalid)
	}
	view, err := r.deps.Communities.Get(ctx, userID, ref.ID)
	if err != nil {
		return domain.CommunityView{}, communityErr(err)
	}
	if ref.CheckAccessHash && ref.AccessHash != view.Community.AccessHash {
		return domain.CommunityView{}, communityErr(domain.ErrCommunityPrivate)
	}
	return view, nil
}

// maybeCommunityFromInput distinguishes a Community from an ordinary channel.
// IDs share one allocator, so ErrCommunityInvalid is the only fallthrough case.
func (r *Router) maybeCommunityFromInput(ctx context.Context, userID int64, input tg.InputChannelClass) (domain.CommunityView, bool, error) {
	if r.deps.Communities == nil {
		return domain.CommunityView{}, false, nil
	}
	ref, ok := inputChannelRef(input)
	if !ok || ref.ID == 0 {
		return domain.CommunityView{}, false, nil
	}
	view, err := r.deps.Communities.Get(ctx, userID, ref.ID)
	if errors.Is(err, domain.ErrCommunityInvalid) {
		return domain.CommunityView{}, false, nil
	}
	if err != nil {
		return domain.CommunityView{}, true, communityErr(err)
	}
	if ref.CheckAccessHash && ref.AccessHash != view.Community.AccessHash {
		return domain.CommunityView{}, true, communityErr(domain.ErrCommunityPrivate)
	}
	return view, true, nil
}

func (r *Router) maybeCommunityFromInputPeer(ctx context.Context, userID int64, peer tg.InputPeerClass) (domain.CommunityView, bool, error) {
	ref, ok := inputPeerChannelRef(peer)
	if !ok {
		return domain.CommunityView{}, false, nil
	}
	input := &tg.InputChannel{ChannelID: ref.ID, AccessHash: ref.AccessHash}
	return r.maybeCommunityFromInput(ctx, userID, input)
}

func (r *Router) communityPeerFromInput(ctx context.Context, userID int64, input tg.InputPeerClass) (domain.Peer, error) {
	peer, ok := r.domainPeerFromInputPeer(userID, input)
	if peer.ID == 0 || (peer.Type != domain.PeerTypeChannel && peer.Type != domain.PeerTypeUser) {
		return domain.Peer{}, peerIDInvalidErr()
	}
	if !ok {
		return domain.Peer{}, peerIDInvalidErr()
	}
	if peer.Type == domain.PeerTypeChannel {
		ref, ok := inputPeerChannelRef(input)
		if !ok || r.deps.Channels == nil {
			return domain.Peer{}, peerIDInvalidErr()
		}
		// A Community admin may approve or unlink a channel without being a
		// member of that channel. Resolve the immutable base row for constructor
		// and access_hash validation; aggregate authorization remains in the
		// Community transaction (direct links still require channel admin rights,
		// approvals require an existing validated request).
		if resolver, ok := r.deps.Channels.(interface {
			GetChannelByID(context.Context, int64) (domain.Channel, error)
		}); ok {
			channel, err := resolver.GetChannelByID(ctx, peer.ID)
			if err != nil || channel.ID == 0 || channel.Deleted {
				return domain.Peer{}, peerIDInvalidErr()
			}
			if ref.CheckAccessHash && !inputChannelAccessHashMatches(ref, channel) {
				return domain.Peer{}, channelInvalidErr(domain.ErrChannelPrivate)
			}
		} else if err := r.validateInputPeerChannelAccess(ctx, userID, input, peer.ID); err != nil {
			return domain.Peer{}, err
		}
	}
	return peer, nil
}

func (r *Router) communityUpdates(view domain.CommunityView) *tg.Updates {
	return &tg.Updates{Updates: []tg.UpdateClass{}, Users: tgUsers(view.Users), Chats: tgCommunityHydratedChats(view.Self.UserID, view), Date: int(r.clock.Now().Unix())}
}

func (r *Router) pushCommunityState(ctx context.Context, userID int64, view domain.CommunityView) {
	r.pushUserUpdates(ctx, userID, r.communityUpdates(view))
}

func (r *Router) refreshAndPushCommunityState(ctx context.Context, viewerUserID, communityID int64, fallback domain.Community) {
	if viewerUserID == 0 || r.deps.Communities == nil {
		return
	}
	view, err := r.deps.Communities.Get(ctx, viewerUserID, communityID)
	if err == nil {
		r.pushCommunityState(ctx, viewerUserID, view)
		return
	}
	if errors.Is(err, domain.ErrCommunityPrivate) {
		r.pushCommunityState(ctx, viewerUserID, domain.CommunityView{
			Community: fallback,
			Self:      domain.CommunityMember{CommunityID: communityID, UserID: viewerUserID},
			Forbidden: true,
		})
	}
}

func (r *Router) communityMutationUpdates(ctx context.Context, userID int64, view domain.CommunityView, changed bool) *tg.Updates {
	out := r.communityUpdates(view)
	if changed {
		r.pushCommunityState(ctx, userID, view)
	}
	return out
}

func (r *Router) withCommunityDialogList(ctx context.Context, userID int64, filter domain.DialogFilter, list domain.DialogList) (domain.DialogList, error) {
	if LayerFrom(ctx) < communitiesLayer {
		return list, nil
	}
	return r.withCollapsedCommunityDialogs(ctx, userID, filter, list)
}

// withCollapsedCommunityDialogs applies the account-level Community dialog
// state without a wire-layer visibility decision. Business invariants such as
// the shared pinned limit use this path; RPC response construction must use
// withCommunityDialogList instead.
func (r *Router) withCollapsedCommunityDialogs(ctx context.Context, userID int64, filter domain.DialogFilter, list domain.DialogList) (domain.DialogList, error) {
	if r.deps.Communities == nil || (filter.HasFolderID && filter.FolderID != domain.DialogMainFolderID) {
		return list, nil
	}
	views, err := r.deps.Communities.ListJoined(ctx, userID)
	if err != nil {
		return domain.DialogList{}, err
	}
	for _, view := range views {
		if !view.State.Collapsed || (filter.PinnedOnly && !view.State.Pinned) || (filter.ExcludePinned && view.State.Pinned) {
			continue
		}
		list.Communities = append(list.Communities, view)
		list.Count++
	}
	return list, nil
}

func (r *Router) communityDialogPeerFromInput(ctx context.Context, userID int64, input tg.InputDialogPeerClass) (domain.CommunityView, bool, error) {
	peer, ok := input.(*tg.InputDialogPeerCommunity)
	if !ok || peer == nil || peer.Community == nil {
		return domain.CommunityView{}, false, nil
	}
	view, err := r.communityFromInput(ctx, userID, peer.Community)
	return view, true, err
}

func (r *Router) onCommunitiesCreate(ctx context.Context, req *tg.CommunitiesCreateRequest) (tg.UpdatesClass, error) {
	if req == nil || r.deps.Communities == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.communityPeerFromInput(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	visibility := domain.CommunityPeerVisible
	if req.Hidden {
		visibility = domain.CommunityPeerHidden
	}
	view, err := r.deps.Communities.Create(ctx, userID, domain.CreateCommunityRequest{Title: req.Title, About: req.About, InitialPeer: peer, Visibility: visibility, Date: int(r.clock.Now().Unix())})
	if err != nil {
		return nil, communityErr(err)
	}
	for _, serviceMessage := range view.ServiceMessages {
		r.enqueueChannelMessageFanout(ctx, userID, serviceMessage, nil)
	}
	return r.communityUpdates(view), nil
}

func (r *Router) emitCommunityLinkService(ctx context.Context, actorUserID int64, result domain.CommunityTogglePeerLinkResult) {
	if result.ServiceMessage == nil {
		return
	}
	r.enqueueChannelMessageFanout(ctx, actorUserID, *result.ServiceMessage, nil)
}

func (r *Router) onCommunitiesTogglePeerLink(ctx context.Context, req *tg.CommunitiesTogglePeerLinkRequest) (bool, error) {
	if req == nil || r.deps.Communities == nil {
		return false, notImplementedErr()
	}
	actions := 0
	if req.Visible {
		actions++
	}
	if req.Hidden {
		actions++
	}
	if req.Deleted {
		actions++
	}
	if actions != 1 {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	view, err := r.communityFromInput(ctx, userID, req.Community)
	if err != nil {
		return false, err
	}
	peer, err := r.communityPeerFromInput(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	visibility := domain.CommunityPeerVisible
	if req.Hidden {
		visibility = domain.CommunityPeerHidden
	}
	result, err := r.deps.Communities.TogglePeerLink(ctx, userID, domain.CommunityTogglePeerLinkRequest{CommunityID: view.Community.ID, Peer: peer, Visibility: visibility, Deleted: req.Deleted, Date: int(r.clock.Now().Unix())})
	if err != nil {
		return false, communityErr(err)
	}
	if result.RequestCreated {
		return false, tgerr400("COMMUNITY_REQUEST_CREATED")
	}
	r.emitCommunityLinkService(ctx, userID, result)
	r.refreshAndPushCommunityState(ctx, userID, view.Community.ID, result.Community)
	return true, nil
}

func (r *Router) onCommunitiesGetJoined(ctx context.Context) (tg.MessagesChatsClass, error) {
	if r.deps.Communities == nil {
		return &tg.MessagesChats{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	views, err := r.deps.Communities.ListJoined(ctx, userID)
	if err != nil {
		return nil, communityErr(err)
	}
	return &tg.MessagesChats{Chats: tgCommunityChats(views)}, nil
}

func (r *Router) onCommunitiesToggleCollapsed(ctx context.Context, req *tg.CommunitiesToggleCommunityCollapsedInDialogsRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	view, err := r.communityFromInput(ctx, userID, req.Community)
	if err != nil {
		return nil, err
	}
	wasPinned := view.State.Pinned
	view, changed, err := r.deps.Communities.SetCollapsed(ctx, userID, view.Community.ID, req.Collapsed)
	if err != nil {
		return nil, communityErr(err)
	}
	out := r.communityUpdates(view)
	if changed && !req.Collapsed && wasPinned {
		out.Updates = append(out.Updates, &tg.UpdateDialogPinned{Peer: &tg.DialogPeerCommunity{CommunityID: view.Community.ID}})
	}
	if changed {
		r.pushCommunityState(ctx, userID, view)
	}
	return out, nil
}

func (r *Router) onCommunitiesGetPeerLinkRequests(ctx context.Context, req *tg.CommunitiesGetPeerLinkRequestsRequest) (*tg.CommunitiesPeerLinkRequests, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	view, err := r.communityFromInput(ctx, userID, req.Community)
	if err != nil {
		return nil, err
	}
	page, err := r.deps.Communities.ListPeerLinkRequests(ctx, userID, view.Community.ID, req.Offset, req.Limit)
	if err != nil {
		return nil, communityErr(err)
	}
	requests := make([]tg.CommunityPeerRequest, 0, len(page.Requests))
	for _, item := range page.Requests {
		requests = append(requests, tg.CommunityPeerRequest{Visible: item.Visibility == domain.CommunityPeerVisible, Peer: tgPeer(item.Peer), RequestedBy: item.RequestedBy, Date: item.Date})
	}
	out := &tg.CommunitiesPeerLinkRequests{TotalCount: page.TotalCount, Requests: requests, Chats: tgChannels(userID, page.Channels), Users: tgUsers(page.Users)}
	if page.NextOffset != "" {
		out.SetNextOffset(page.NextOffset)
	}
	return out, nil
}

func (r *Router) onCommunitiesTogglePeerLinkRequestApproval(ctx context.Context, req *tg.CommunitiesTogglePeerLinkRequestApprovalRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	view, err := r.communityFromInput(ctx, userID, req.Community)
	if err != nil {
		return false, err
	}
	peer, err := r.communityPeerFromInput(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	result, err := r.deps.Communities.DecidePeerLinkRequest(ctx, userID, view.Community.ID, peer, req.Reject, int(r.clock.Now().Unix()))
	if err != nil {
		return false, communityErr(err)
	}
	if !req.Reject {
		r.emitCommunityLinkService(ctx, userID, result)
		r.refreshAndPushCommunityState(ctx, userID, view.Community.ID, result.Community)
		if result.RequestedBy != userID {
			r.refreshAndPushCommunityState(ctx, result.RequestedBy, view.Community.ID, result.Community)
		}
	}
	return true, nil
}

func (r *Router) onCommunitiesToggleAllPeerLinkRequestApproval(ctx context.Context, req *tg.CommunitiesToggleAllPeerLinkRequestApprovalRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	view, err := r.communityFromInput(ctx, userID, req.Community)
	if err != nil {
		return false, err
	}
	results, err := r.deps.Communities.DecideAllPeerLinkRequests(ctx, userID, view.Community.ID, req.Reject, int(r.clock.Now().Unix()))
	if err != nil {
		return false, communityErr(err)
	}
	if !req.Reject {
		requesters := map[int64]struct{}{}
		for _, result := range results {
			r.emitCommunityLinkService(ctx, userID, result)
			if result.RequestedBy != 0 && result.RequestedBy != userID {
				requesters[result.RequestedBy] = struct{}{}
			}
		}
		if len(results) > 0 {
			r.refreshAndPushCommunityState(ctx, userID, view.Community.ID, results[0].Community)
			for requester := range requesters {
				r.refreshAndPushCommunityState(ctx, requester, view.Community.ID, results[0].Community)
			}
		}
	}
	return true, nil
}

func (r *Router) onCommunitiesToggleParticipantBanned(ctx context.Context, req *tg.CommunitiesToggleParticipantBannedRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	view, err := r.communityFromInput(ctx, userID, req.Community)
	if err != nil {
		return false, err
	}
	peer, err := r.communityPeerFromInput(ctx, userID, req.Participant)
	if err != nil || peer.Type != domain.PeerTypeUser {
		return false, peerIDInvalidErr()
	}
	result, err := r.deps.Communities.ToggleParticipantBanned(ctx, userID, view.Community.ID, peer.ID, req.Unban, int(r.clock.Now().Unix()))
	if err != nil {
		return false, communityErr(err)
	}
	for _, removed := range result.RemovedLinks {
		r.emitCommunityLinkService(ctx, userID, removed)
	}
	for _, ban := range result.ChannelBans {
		r.invalidateChannelFullBotInfoCacheForChannel(ban.Channel.ID)
		r.removeOnlineChannelMemberships(ban.Channel.ID, peer.ID)
		r.recordChannelStateForUser(ctx, peer.ID, ban.Channel.ID, false)
		cache := newViewerPeerCache(r)
		build := func(viewerUserID int64) *tg.Updates {
			updates := r.channelParticipantUpdatesWithPeerCache(ctx, viewerUserID, userID, ban.Channel, ban.Previous, ban.Participant, ban.Date, cache)
			if updates != nil && ban.ServiceEvent.Pts != 0 {
				if update := tgChannelUpdate(viewerUserID, ban.ServiceEvent); update != nil {
					updates.Updates = append([]tg.UpdateClass{update}, updates.Updates...)
				}
			}
			return updates
		}
		r.pushChannelUpdates(ctx, userID, ban.Channel.ID, ban.Recipients, build)
	}
	if result.Changed && !req.Unban {
		forbidden := domain.CommunityView{Community: view.Community, Forbidden: true, Self: domain.CommunityMember{UserID: peer.ID}}
		r.pushUserUpdates(ctx, peer.ID, r.communityUpdates(forbidden))
	}
	return true, nil
}

func (r *Router) onCommunitiesGetParticipantJoinedChats(ctx context.Context, req *tg.CommunitiesGetParticipantJoinedChatsRequest) (*tg.CommunitiesParticipantJoinedChats, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	view, err := r.communityFromInput(ctx, userID, req.Community)
	if err != nil {
		return nil, err
	}
	peer, err := r.communityPeerFromInput(ctx, userID, req.Participant)
	if err != nil || peer.Type != domain.PeerTypeUser {
		return nil, peerIDInvalidErr()
	}
	joined, err := r.deps.Communities.ParticipantJoinedChats(ctx, userID, view.Community.ID, peer.ID)
	if err != nil {
		return nil, communityErr(err)
	}
	return &tg.CommunitiesParticipantJoinedChats{CreatorChatIDs: joined.CreatorChatIDs, JoinedChatIDs: joined.JoinedChatIDs, Chats: tgChannels(userID, joined.Channels), Users: tgUsers(joined.Users)}, nil
}
