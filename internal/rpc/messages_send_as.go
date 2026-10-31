package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) forumSendAsPeer(ctx context.Context, peerInput, sendAsInput tg.InputPeerClass) (*domain.Peer, error) {
	peer, err := r.forumTopicPeer(ctx, peerInput)
	if err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	// Reuse the unified send-as validation: self within a group -> nil (post as yourself), the
	// current channel/group -> anonymous/broadcast self-post, another broadcast channel you own ->
	// validated against the target and channel eligibility. Forum topics and ordinary messages use
	// the same eligibility rules.
	return r.sendAsPeerFromInput(ctx, userID, peer, sendAsInput)
}

func (r *Router) onMessagesSaveDefaultSendAs(ctx context.Context, req *tg.MessagesSaveDefaultSendAsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if userID == 0 {
		return false, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return false, peerIDInvalidErr()
	}
	sendAs, err := r.sendAsPeerFromInput(ctx, userID, peer, req.SendAs)
	if err != nil {
		return false, err
	}
	if r.deps.Channels == nil || req.SendAs == nil {
		return false, sendAsPeerInvalidErr()
	}
	// Persist an explicit self selection, distinct from a channel's natural default.
	if sendAs == nil {
		sendAs = &domain.Peer{Type: domain.PeerTypeUser, ID: userID}
	}
	if _, err := r.deps.Channels.SaveDefaultSendAs(ctx, userID, domain.SaveChannelDefaultSendAsRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		SendAs:    sendAs,
	}); err != nil {
		return false, channelInvalidErr(err)
	}
	r.invalidateRPCProjectionForPeer(userID, peer)
	return true, nil
}

func (r *Router) sendAsPeerFromInput(ctx context.Context, userID int64, to domain.Peer, input tg.InputPeerClass) (*domain.Peer, error) {
	if input == nil {
		return nil, nil
	}
	if to.Type != domain.PeerTypeChannel {
		return nil, sendAsPeerInvalidErr()
	}
	sendAs, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return nil, sendAsPeerInvalidErr()
	}
	switch sendAs.Type {
	case domain.PeerTypeUser:
		if sendAs.ID != userID {
			return nil, sendAsPeerInvalidErr()
		}
		if r.deps.Channels == nil {
			return nil, sendAsPeerInvalidErr()
		}
		view, err := r.deps.Channels.ResolveChannel(ctx, userID, to.ID)
		if err != nil {
			return nil, sendAsPeerInvalidErr()
		}
		if view.Channel.Broadcast {
			if !view.Channel.SignatureProfiles || !canSendAsForeignChannel(view) {
				return nil, sendAsPeerInvalidErr()
			}
			return &sendAs, nil
		}
		if view.Self.AdminRights.Anonymous {
			return nil, sendAsPeerInvalidErr()
		}
		return nil, nil
	case domain.PeerTypeChannel:
		if sendAs.ID == to.ID {
			// Post as the current channel/group itself (broadcast self-post, anonymous admin).
			if err := r.validateCurrentChannelSendAs(ctx, userID, to.ID); err != nil {
				return nil, err
			}
		} else {
			// Post as another broadcast channel you own.
			if err := r.validateForeignChannelSendAs(ctx, userID, to.ID, sendAs.ID); err != nil {
				return nil, err
			}
		}
		out := sendAs
		return &out, nil
	default:
		return nil, sendAsPeerInvalidErr()
	}
}

// validateForeignChannelSendAs requires current posting rights in the source.
// Broadcast profiles and linked/verified group identities do not require Premium.
func (r *Router) validateForeignChannelSendAs(ctx context.Context, userID, toChannelID, sendAsChannelID int64) error {
	if r.deps.Channels == nil {
		return sendAsPeerInvalidErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, sendAsChannelID)
	if err != nil || !canSendAsForeignChannel(view) {
		return sendAsPeerInvalidErr()
	}
	target, err := r.deps.Channels.ResolveChannel(ctx, userID, toChannelID)
	if err != nil {
		return sendAsPeerInvalidErr()
	}
	if target.Channel.Broadcast {
		if !target.Channel.SignatureProfiles || !canSendAsForeignChannel(target) {
			return sendAsPeerInvalidErr()
		}
		return nil
	}
	if !view.Channel.Verified && !r.userIsPremium(ctx, userID) && target.Channel.LinkedChatID != sendAsChannelID {
		return sendAsPeerInvalidErr()
	}
	return nil
}

// canSendAsForeignChannel reports whether the channel described by view can be used by the current
// user to "post as" in another group: broadcast channels only, and the user must be the creator or
// an admin holding PostMessages (posting as a megagroup you own is not a real Telegram capability,
// so it is excluded).
func canSendAsForeignChannel(view domain.ChannelView) bool {
	if !view.Channel.Broadcast {
		return false
	}
	if view.Self.Role == domain.ChannelRoleCreator {
		return true
	}
	if view.Self.Role == domain.ChannelRoleAdmin && view.Self.AdminRights.PostMessages {
		return true
	}
	return false
}

// userIsPremium reports whether the user is currently an active subscriber (derived from
// PremiumActiveAt on the fly).
func (r *Router) userIsPremium(ctx context.Context, userID int64) bool {
	if r.deps.Users == nil || userID == 0 {
		return false
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return false
	}
	return u.PremiumActiveAt(r.clock.Now().Unix())
}

func (r *Router) resolveSendAsPeer(ctx context.Context, userID int64, to domain.Peer, input tg.InputPeerClass) (*domain.Peer, error) {
	if input != nil {
		return r.sendAsPeerFromInput(ctx, userID, to, input)
	}
	if to.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return nil, nil
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, to.ID)
	if err != nil {
		return nil, nil
	}
	return r.effectiveDefaultSendAs(ctx, userID, view), nil
}

// effectiveDefaultSendAs resolves a saved identity against current permissions.
// Natural broadcast/anonymous defaults must not expose the acting administrator.
func (r *Router) effectiveDefaultSendAs(ctx context.Context, userID int64, view domain.ChannelView) *domain.Peer {
	def := view.Dialog.DefaultSendAs
	if def != nil && def.Type == domain.PeerTypeChannel && def.ID != view.Channel.ID {
		if err := r.validateForeignChannelSendAs(ctx, userID, view.Channel.ID, def.ID); err == nil {
			out := *def
			return &out
		}
	}
	peer := validDefaultSendAsPeer(view)
	if peer != nil && peer.Type == domain.PeerTypeUser && !view.Channel.Broadcast {
		return nil
	}
	return peer
}

// applyForeignDefaultSendAsToFull includes the selected foreign channel object.
// Revalidate on every full-info response, including a cached projection hit.
func (r *Router) applyForeignDefaultSendAsToFull(ctx context.Context, userID int64, view domain.ChannelView, full *tg.ChannelFull, chats *[]tg.ChatClass) {
	def := view.Dialog.DefaultSendAs
	if full.DefaultSendAs == nil || def == nil || def.Type != domain.PeerTypeChannel || def.ID == 0 || def.ID == view.Channel.ID {
		return
	}
	if err := r.validateForeignChannelSendAs(ctx, userID, view.Channel.ID, def.ID); err != nil {
		return
	}
	sendAsView, err := r.deps.Channels.ResolveChannel(ctx, userID, def.ID)
	if err != nil {
		return
	}
	full.SetDefaultSendAs(&tg.PeerChannel{ChannelID: def.ID})
	*chats = appendUniqueTGChats(*chats, tgChannels(userID, []domain.Channel{sendAsView.Channel})...)
}

// The full-info field is an effective default, not merely an optional saved preference.
func validDefaultSendAsPeer(view domain.ChannelView) *domain.Peer {
	ch, member := view.Channel, view.Self
	if ch.ID == 0 || ch.Deleted || ch.Monoforum || member.UserID == 0 || member.Status != domain.ChannelMemberActive || member.BannedRights.ViewMessages {
		return nil
	}
	admin := member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin
	if ch.Broadcast {
		if !canSendAsForeignChannel(view) {
			return nil
		}
	} else if !admin && (member.BannedRights.SendMessages || ch.DefaultBannedRights.SendMessages && !(ch.BoostsUnrestrict > 0 && view.SelfBoostsApplied >= ch.BoostsUnrestrict)) {
		return nil
	}
	self := domain.Peer{Type: domain.PeerTypeUser, ID: member.UserID}
	current := domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}
	if def := view.Dialog.DefaultSendAs; def != nil {
		if def.Type == domain.PeerTypeUser && def.ID == member.UserID && (!ch.Broadcast || ch.SignatureProfiles) && (!member.AdminRights.Anonymous || ch.Broadcast) {
			return &self
		}
		if def.Type == domain.PeerTypeChannel && def.ID == ch.ID && canCurrentChannelSendAs(view) {
			return &current
		}
	}
	if ch.Broadcast || admin && member.AdminRights.Anonymous {
		return &current
	}
	return &self
}

func (r *Router) validateCurrentChannelSendAs(ctx context.Context, userID, channelID int64) error {
	if r.deps.Channels == nil {
		return sendAsPeerInvalidErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, channelID)
	if err != nil {
		return sendAsPeerInvalidErr()
	}
	if canCurrentChannelSendAs(view) {
		return nil
	}
	return sendAsPeerInvalidErr()
}

func canCurrentChannelSendAs(view domain.ChannelView) bool {
	if view.Self.Role == domain.ChannelRoleCreator {
		return true
	}
	if view.Channel.Broadcast && view.Self.Role == domain.ChannelRoleAdmin && view.Self.AdminRights.PostMessages {
		return true
	}
	if !view.Channel.Broadcast && view.Self.Role == domain.ChannelRoleAdmin && view.Self.AdminRights.Anonymous {
		return true
	}
	return false
}
