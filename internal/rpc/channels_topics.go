package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// onChannelsToggleForum applies channels.toggleForum. Layer 233 adds the
// required sidebar layout flag, but the server models only the list and tabs
// layouts and never emits channel.forum_sidebar. Enabling a forum with the
// sidebar layout therefore fails closed before any write instead of silently
// storing another layout. Historical profiles arrive with sidebar=false (the
// td default), and disabling a forum ignores the layout flags, as it does tabs.
func (r *Router) onChannelsToggleForum(ctx context.Context, req *tg.ChannelsToggleForumRequest) (tg.UpdatesClass, error) {
	if req.Enabled && req.Sidebar {
		return nil, notImplementedErr()
	}
	return r.applyChannelAdminStateMutation(ctx, req.Channel, func(ctx context.Context, userID, channelID int64) (domain.Channel, error) {
		return r.deps.Channels.SetForum(ctx, userID, channelID, req.Enabled, req.Tabs)
	})
}

func (r *Router) onChannelsToggleViewForumAsMessages(ctx context.Context, req *tg.ChannelsToggleViewForumAsMessagesRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil || r.deps.Dialogs == nil {
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
	sessionID, _ := SessionIDFrom(ctx)
	date := int(r.clock.Now().Unix())
	snapshot, err := r.deps.Dialogs.MutateAccountDialogs(ctx, store.DialogAccountMutation{
		Kind: store.DialogAccountSetChannelViewForum, UserID: userID, Date: date,
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, Value: req.Enabled,
		ExcludeAuthKeyID: rawAuthKeyIDForOrigin(ctx), ExcludeSessionID: sessionID,
	}, dialogAccountDeliveryEffects)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	if !snapshot.Changed {
		return tgEmptyUpdates(date), nil
	}
	r.invalidateRPCProjectionForPeer(userID, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	event := allocatedDialogEvent(snapshot)
	out := tgUpdateForOutboxEvent(event)
	if out == nil {
		return nil, internalErr()
	}
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
