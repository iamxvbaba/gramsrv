package rpc

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

// NotifyPeerUsernamesChanged is the domain-only edge hook invoked after a
// collectible username registry mutation commits. It invalidates the cached
// peer projection and pushes the ordinary non-PTS updateUser/updateChannel
// refresh to online viewers. The shared projection paths preload the registry
// once, so fan-out cannot turn the change into an N+1 query.
func (r *Router) NotifyPeerUsernamesChanged(ctx context.Context, peer domain.Peer) error {
	if r == nil {
		return nil
	}
	if peer.ID <= 0 {
		return fmt.Errorf("notify peer usernames changed: invalid peer id %d", peer.ID)
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		return r.notifyUserUsernamesChanged(ctx, peer.ID)
	case domain.PeerTypeChannel:
		return r.notifyChannelUsernamesChanged(ctx, peer.ID)
	default:
		return fmt.Errorf("notify peer usernames changed: unsupported peer type %q for peer %d", peer.Type, peer.ID)
	}
}

func (r *Router) notifyUserUsernamesChanged(ctx context.Context, userID int64) error {
	r.invalidateRPCProjectionForUser(userID)
	if r.deps.Users == nil {
		return nil
	}
	user, found, err := r.verificationUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("notify peer usernames changed: load user %d: %w", userID, err)
	}
	if !found || user.ID == 0 {
		return fmt.Errorf("notify peer usernames changed: user %d not found", userID)
	}
	return r.NotifyUserModerationFlagsChanged(ctx, user)
}

func (r *Router) notifyChannelUsernamesChanged(ctx context.Context, channelID int64) error {
	r.invalidateRPCProjectionForChannel(channelID)
	if r.deps.Channels == nil {
		return nil
	}
	directory, ok := r.deps.Channels.(verificationChannelDirectory)
	if !ok {
		return fmt.Errorf("notify peer usernames changed: channel service does not expose GetChannelByID")
	}
	channel, err := directory.GetChannelByID(ctx, channelID)
	if err != nil {
		return fmt.Errorf("notify peer usernames changed: load channel %d: %w", channelID, err)
	}
	if channel.ID == 0 {
		return fmt.Errorf("notify peer usernames changed: channel %d not found", channelID)
	}
	return r.NotifyChannelChanged(ctx, channel)
}
