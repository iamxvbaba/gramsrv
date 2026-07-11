package rpc

import (
	"context"
	"github.com/gotd/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) onChannelsToggleForum(ctx context.Context, req *tg.ChannelsToggleForumRequest) (tg.UpdatesClass, error) {
	return r.applyChannelAdminStateMutation(ctx, req.Channel, func(ctx context.Context, userID, channelID int64) (domain.Channel, error) {
		return r.deps.Channels.SetForum(ctx, userID, channelID, req.Enabled, req.Tabs)
	})
}

func (r *Router) onChannelsToggleViewForumAsMessages(ctx context.Context, req *tg.ChannelsToggleViewForumAsMessagesRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	changed, err := r.deps.Channels.SetViewForumAsMessages(ctx, userID, channelID, req.Enabled)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	if !changed {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	r.invalidateRPCProjectionForPeer(userID, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	event := domain.UpdateEvent{
		Type:     domain.UpdateEventChannelViewForum,
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		Bool:     req.Enabled,
		PtsCount: 1,
		Date:     int(r.clock.Now().Unix()),
	}
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, _, err = r.deps.Updates.RecordChannelViewForumAsMessages(ctx, authKeyID, userID, channelID, req.Enabled, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return nil, internalErr()
		}
	}
	out := tgUpdateForOutboxEvent(event)
	if out == nil {
		out = tgEmptyUpdates(event.Date)
	}
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, out)
	return out, nil
}

func (r *Router) onMessagesUpdatePinnedMessage(ctx context.Context, req *tg.MessagesUpdatePinnedMessageRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if peer.Type == domain.PeerTypeUser && peer.ID != 0 {
		return r.updatePrivatePinnedMessage(ctx, userID, peer, req)
	}
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	res, err := r.deps.Channels.UpdatePinnedMessage(ctx, userID, domain.UpdateChannelPinnedMessageRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		MessageID: req.ID,
		Pinned:    !req.Unpin,
		Silent:    req.Silent,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelAdminErr(err)
	}
	r.invalidateRPCProjectionForChannel(res.Channel.ID)
	updates := r.channelPinnedUpdates(userID, res)
	// pin fan-out 异步化（设计 Phase 0），与已异步的 unpinAll(channels_stubs.go) 对齐。
	// builder 无 Users 数组（仅 pinned update + ChatMin），无需 owner 预热。pin 的真实变更由
	// UpdatePinnedChannelMessages{pts} 承载、可经 getChannelDifference 兜底，bundled 的无 pts
	// UpdateChannel 对 pin 冗余（pts payload 已含变更），丢弃无害——与 unpinAll 取舍一致。
	r.enqueueChannelFanout(ctx, channelFanoutMembers, userID, res.Channel.ID, res.Event.Pts, res.Recipients, func(_ context.Context, viewerUserID int64) *tg.Updates {
		return r.channelPinnedUpdates(viewerUserID, res)
	})
	return updates, nil
}

func (r *Router) channelPinnedUpdates(viewerUserID int64, res domain.UpdateChannelPinnedMessageResult) *tg.Updates {
	updates := []tg.UpdateClass(nil)
	if update := tgChannelUpdate(viewerUserID, res.Event); update != nil {
		updates = append(updates, update)
	}
	updates = append(updates, &tg.UpdateChannel{ChannelID: res.Channel.ID})
	return &tg.Updates{
		Updates: updates,
		Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, res.Channel)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}
