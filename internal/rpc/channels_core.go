package rpc

import (
	"context"
	"github.com/gotd/td/tg"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onChannelsCreateChannel(ctx context.Context, req *tg.ChannelsCreateChannelRequest) (tg.UpdatesClass, error) {
	if err := validateChannelsCreateChannelOptions(req); err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if !validChannelTitle(req.Title) || utf8.RuneCountInString(req.About) > maxChannelAboutLength {
		return nil, channelInvalidErr(domain.ErrChannelTitleInvalid)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	res, err := r.deps.Channels.CreateChannel(ctx, userID, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         req.Title,
		About:         req.About,
		Broadcast:     req.Broadcast,
		Megagroup:     req.Megagroup,
		Forum:         req.Forum,
		ForumTabs:     req.Forum,
		TTLPeriod:     req.TTLPeriod,
		Date:          int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
	updates := r.channelOperationUpdates(ctx, userID, res)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOperationUpdates(ctx, viewerUserID, res)
	})
	return updates, nil
}

func validateChannelsCreateChannelOptions(req *tg.ChannelsCreateChannelRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if req.ForImport {
		return chatInvalidErr()
	}
	if req.GeoPoint != nil || req.Address != "" {
		return addressInvalidErr()
	}
	if req.TTLPeriod < 0 {
		return ttlPeriodInvalidErr()
	}
	return nil
}

func (r *Router) onChannelsGetChannels(ctx context.Context, ids []tg.InputChannelClass) (tg.MessagesChatsClass, error) {
	if len(ids) > maxGetMessagesIDs {
		return nil, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	refs := make([]channelInputRef, 0, len(ids))
	channelIDs := make([]int64, 0, len(ids))
	for _, input := range ids {
		ref, ok := inputChannelRef(input)
		if !ok || ref.ID == 0 || r.deps.Channels == nil {
			continue
		}
		refs = append(refs, ref)
		channelIDs = append(channelIDs, ref.ID)
	}
	if len(channelIDs) == 0 || r.deps.Channels == nil {
		return &tg.MessagesChats{}, nil
	}
	views, err := r.deps.Channels.GetChannels(ctx, userID, channelIDs)
	if err != nil {
		return nil, internalErr()
	}
	byID := make(map[int64]domain.ChannelView, len(views))
	for _, view := range views {
		byID[view.Channel.ID] = view
	}
	chats := make([]tg.ChatClass, 0, len(refs))
	for _, ref := range refs {
		view, ok := byID[ref.ID]
		if !ok || !inputChannelAccessHashMatches(ref, view.Channel) {
			continue
		}
		chats = append(chats, tgChannelChatForView(userID, view))
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, userID, nil, chats)
	return &tg.MessagesChats{Chats: chats}, nil
}

func (r *Router) onChannelsGetFullChannel(ctx context.Context, input tg.InputChannelClass) (*tg.MessagesChatFull, error) {
	if r.deps.Channels == nil {
		return &tg.MessagesChatFull{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	ref, ok := inputChannelRef(input)
	if !ok {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	loadEpoch := r.channelFullProjectionCache.LoadEpoch()
	if cached, ok := r.channelFullProjectionCache.Lookup(userID, ref.ID); ok {
		if !inputChannelAccessHashMatches(ref, domain.Channel{ID: ref.ID, AccessHash: cached.accessHash}) {
			return nil, channelInvalidErr(domain.ErrChannelPrivate)
		}
		full := cached.full
		r.applyStarGiftsCountToChannelFull(ctx, ref.ID, &full)
		r.applyStoriesPinnedAvailableToChannelFull(ctx, userID, ref.ID, &full)
		r.applyNotifySettingsToChannelFull(ctx, userID, ref.ID, &full)
		r.applyAndroidChannelReactionEditorCompat(ctx, &full, cached.canChangeInfo)
		chats := append([]tg.ChatClass(nil), cached.chats...)
		chats = r.appendLinkedDiscussionChat(ctx, userID, ref.ID, chats)
		r.trackChannelInterest(ctx, userID, ref.ID)
		r.applyStoryMaxIDsToPeerObjects(ctx, userID, nil, chats)
		return &tg.MessagesChatFull{
			FullChat: &full,
			Chats:    chats,
			Users:    r.tgUsersForIDs(ctx, userID, cached.userIDs),
		}, nil
	}
	view, err := r.channelFullReadView(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	full := tgChannelFull(view, r.cfg.PublicBaseURL)
	r.applyStarGiftsCountToChannelFull(ctx, view.Channel.ID, full)
	userIDs := []int64{view.Channel.CreatorUserID, view.Self.UserID}
	// 注：Bots 过滤实际会返回群内 bot（TestGroupBotRPCShape 覆盖），这里据此富化 full.BotInfo。
	// （此前审计误判为死代码，已由单测纠正——勿删。）
	botInfo := r.channelFullBotInfo(ctx, userID, view.Channel.ID)
	userIDs = append(userIDs, botInfo.userIDs...)
	full.BotInfo = append(full.BotInfo, botInfo.botInfos...)
	if canViewChannelJoinRequests(view.Self) {
		userIDs = r.applyPendingJoinRequestsToFullChannel(ctx, full, view.Channel.ID, userIDs)
	}
	r.trackChannelInterest(ctx, userID, view.Channel.ID)
	chats := []tg.ChatClass{tgChannelChatForView(userID, view)}
	chats = r.appendLinkedDiscussionChat(ctx, userID, view.Channel.ID, chats)
	if mono, ok := r.linkedMonoforumForChannelState(ctx, userID, view.Channel); ok {
		chats = appendUniqueTGChats(chats, tgChannelChat(userID, mono, nil))
	}
	// 当前频道默认已由 tgChannelFull 处理；外部频道默认（以自己拥有的别的频道身份发言）需在此投影并
	// 带上该频道对象，否则客户端拿不到默认 chip。
	r.applyForeignDefaultSendAsToFull(ctx, userID, view, full, &chats)
	canChangeInfo := channelMemberCanChangeInfo(view.Self)
	r.channelFullProjectionCache.StoreIfEpoch(userID, view.Channel.ID, channelFullProjection{
		accessHash:    view.Channel.AccessHash,
		canChangeInfo: canChangeInfo,
		full:          *full,
		chats:         append([]tg.ChatClass(nil), chats...),
		userIDs:       userIDs,
	}, loadEpoch)
	r.applyStoriesPinnedAvailableToChannelFull(ctx, userID, view.Channel.ID, full)
	r.applyNotifySettingsToChannelFull(ctx, userID, view.Channel.ID, full)
	r.applyAndroidChannelReactionEditorCompat(ctx, full, canChangeInfo)
	r.applyStoryMaxIDsToPeerObjects(ctx, userID, nil, chats)
	return &tg.MessagesChatFull{
		FullChat: full,
		Chats:    chats,
		Users:    r.tgUsersForIDs(ctx, userID, userIDs),
	}, nil
}

func (r *Router) applyStarGiftsCountToChannelFull(ctx context.Context, channelID int64, full *tg.ChannelFull) {
	if r.deps.Gifts == nil || channelID == 0 || full == nil {
		return
	}
	n, err := r.deps.Gifts.CountSaved(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	if err == nil && n > 0 {
		full.SetStargiftsCount(n)
	}
}

type channelReadModelResolver interface {
	GetChannelReadModel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
}

func (r *Router) channelFullReadView(ctx context.Context, userID int64, input tg.InputChannelClass) (domain.ChannelView, error) {
	ref, ok := inputChannelRef(input)
	if !ok {
		return domain.ChannelView{}, channelInvalidErr(domain.ErrChannelInvalid)
	}
	var (
		view domain.ChannelView
		err  error
	)
	if cached, ok := r.deps.Channels.(channelReadModelResolver); ok {
		view, err = cached.GetChannelReadModel(ctx, userID, ref.ID)
	} else {
		view, err = r.deps.Channels.GetChannel(ctx, userID, ref.ID)
	}
	if err != nil {
		return domain.ChannelView{}, channelInvalidErr(err)
	}
	if !inputChannelAccessHashMatches(ref, view.Channel) {
		return domain.ChannelView{}, channelInvalidErr(domain.ErrChannelPrivate)
	}
	return view, nil
}

func (r *Router) onChannelsGetSendAs(ctx context.Context, req *tg.ChannelsGetSendAsRequest) (*tg.ChannelsSendAsPeers, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.Peer)
	if !ok || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	chats := []tg.ChatClass(nil)
	peers := []tg.SendAsPeer{{Peer: &tg.PeerUser{UserID: userID}}}
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return &tg.ChannelsSendAsPeers{}, nil
		}
		view, err := r.deps.Channels.ResolveChannel(ctx, userID, peer.ID)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		if ref, ok := inputPeerChannelRef(req.Peer); ok {
			if ref.ID != view.Channel.ID || (ref.CheckAccessHash && !inputChannelAccessHashMatches(ref, view.Channel)) {
				return nil, channelInvalidErr(domain.ErrChannelPrivate)
			}
		}
		chats = []tg.ChatClass{tgChannelChatForView(userID, view)}
		// 以「当前频道/群本身」发言：广播频道自帖、匿名管理员等（canCurrentChannelSendAs 判定）。
		if canCurrentChannelSendAs(view) {
			peers = append(peers, tg.SendAsPeer{Peer: &tg.PeerChannel{ChannelID: view.Channel.ID}})
		}
		// 以「用户自己拥有的其它广播频道」身份在本群发言。非本群关联频道的个人频道需会员
		// （premium_required，对齐官方：仅本群的 linked 讨论频道免会员），客户端据此置灰/引导开会员，
		// 服务端在发送侧用 PremiumActiveAt 兜底门控。
		if owned, err := r.deps.Channels.ListSendAsChannels(ctx, userID); err == nil && len(owned) > 0 {
			extras := make([]domain.Channel, 0, len(owned))
			for _, ch := range owned {
				if ch.ID == 0 || ch.ID == view.Channel.ID {
					continue
				}
				sendAs := tg.SendAsPeer{Peer: &tg.PeerChannel{ChannelID: ch.ID}}
				if ch.ID != view.Channel.LinkedChatID {
					sendAs.PremiumRequired = true
				}
				peers = append(peers, sendAs)
				extras = append(extras, ch)
			}
			chats = append(chats, tgChannels(userID, extras)...)
		}
	}
	return &tg.ChannelsSendAsPeers{
		Peers: peers,
		Chats: chats,
		Users: r.tgUsersForIDs(ctx, userID, []int64{userID}),
	}, nil
}

func (r *Router) applyPendingJoinRequestsToFullChannel(ctx context.Context, full *tg.ChannelFull, channelID int64, userIDs []int64) []int64 {
	if r.deps.Channels == nil || full == nil || channelID == 0 {
		return userIDs
	}
	pending, err := r.deps.Channels.PendingJoinRequests(ctx, channelID, domain.MaxChannelPendingJoinRecentRequesters)
	if err != nil || pending.Count <= 0 {
		return userIDs
	}
	full.SetRequestsPending(pending.Count)
	full.SetRecentRequesters(pending.RecentRequesters)
	return append(userIDs, pending.RecentRequesters...)
}
