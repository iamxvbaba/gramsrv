package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/store"
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
	return r.communityUpdatesAt(view, int(r.clock.Now().Unix()))
}

func (r *Router) communityUpdatesAt(view domain.CommunityView, date int) *tg.Updates {
	// updateChannel makes clients refetch the community (e.g. a new shared
	// avatar); it rides both the RPC result and the durable fanout.
	updates := []tg.UpdateClass{}
	if view.Community.ID != 0 {
		updates = append(updates, &tg.UpdateChannel{ChannelID: view.Community.ID})
	}
	return &tg.Updates{Updates: updates, Users: tgUsers(view.Users), Chats: tgCommunityHydratedChats(view.Self.UserID, view), Date: date}
}

func (r *Router) communityDeliveryEffects(ctx context.Context, requestUserID int64, date int) store.DeliveryEffectsBuilder[store.CommunityDeliverySnapshot] {
	excludeAuthKeyID, excludeSessionID := deliveryExclusionFromContext(ctx)
	return func(snapshot store.CommunityDeliverySnapshot) ([]store.DeliveryEffect, error) {
		effects := make([]store.DeliveryEffect, 0, len(snapshot.Targets))
		for _, target := range snapshot.Targets {
			payload, err := encodeDeliveryUpdate(r.communityUpdatesAt(target.View, date))
			if err != nil {
				return nil, err
			}
			authKeyID, sessionID := [8]byte{}, int64(0)
			if target.TargetUserID == requestUserID {
				authKeyID, sessionID = excludeAuthKeyID, excludeSessionID
			}
			effects = append(effects, store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
				TargetUserID: target.TargetUserID, ExcludeAuthKeyID: authKeyID,
				ExcludeSessionID: sessionID, Payload: payload,
				RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
			}))
		}
		return effects, nil
	}
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
		if err := r.enqueueBotAPIChannelMessageUpdate(ctx, userID, serviceMessage); err != nil {
			return nil, internalErr()
		}
	}
	return r.communityUpdates(view), nil
}

func (r *Router) enqueueBotAPICommunityLinkService(ctx context.Context, actorUserID int64, result domain.CommunityTogglePeerLinkResult) error {
	if result.ServiceMessage == nil {
		return nil
	}
	return r.enqueueBotAPIChannelMessageUpdate(ctx, actorUserID, *result.ServiceMessage)
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
	date := int(r.clock.Now().Unix())
	result, err := r.deps.Communities.TogglePeerLink(ctx, userID, domain.CommunityTogglePeerLinkRequest{CommunityID: view.Community.ID, Peer: peer, Visibility: visibility, Deleted: req.Deleted, Date: date}, r.communityDeliveryEffects(ctx, userID, date))
	if err != nil {
		return false, communityErr(err)
	}
	if result.RequestCreated {
		return false, tgerr400("COMMUNITY_REQUEST_CREATED")
	}
	if err := r.enqueueBotAPICommunityLinkService(ctx, userID, result); err != nil {
		return false, internalErr()
	}
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
	date := int(r.clock.Now().Unix())
	view, changed, err := r.deps.Communities.SetCollapsed(ctx, userID, view.Community.ID, req.Collapsed, r.communityDeliveryEffects(ctx, userID, date))
	if err != nil {
		return nil, communityErr(err)
	}
	out := r.communityUpdates(view)
	if changed && !req.Collapsed && wasPinned {
		out.Updates = append(out.Updates, &tg.UpdateDialogPinned{Peer: &tg.DialogPeerCommunity{CommunityID: view.Community.ID}})
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
	date := int(r.clock.Now().Unix())
	result, err := r.deps.Communities.DecidePeerLinkRequest(ctx, userID, view.Community.ID, peer, req.Reject, date, r.communityDeliveryEffects(ctx, userID, date))
	if err != nil {
		return false, communityErr(err)
	}
	if !req.Reject {
		if err := r.enqueueBotAPICommunityLinkService(ctx, userID, result); err != nil {
			return false, internalErr()
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
	date := int(r.clock.Now().Unix())
	results, err := r.deps.Communities.DecideAllPeerLinkRequests(ctx, userID, view.Community.ID, req.Reject, date, r.communityDeliveryEffects(ctx, userID, date))
	if err != nil {
		return false, communityErr(err)
	}
	if !req.Reject {
		for _, result := range results {
			if err := r.enqueueBotAPICommunityLinkService(ctx, userID, result); err != nil {
				return false, internalErr()
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
	date := int(r.clock.Now().Unix())
	result, err := r.deps.Communities.ToggleParticipantBanned(ctx, userID, view.Community.ID, peer.ID, req.Unban, date, r.communityDeliveryEffects(ctx, userID, date))
	if err != nil {
		return false, communityErr(err)
	}
	for _, removed := range result.RemovedLinks {
		if err := r.enqueueBotAPICommunityLinkService(ctx, userID, removed); err != nil {
			return false, internalErr()
		}
	}
	for _, ban := range result.ChannelBans {
		r.invalidateChannelFullBotInfoCacheForChannel(ban.Channel.ID)
		r.removeOnlineChannelMemberships(ban.Channel.ID, peer.ID)
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
