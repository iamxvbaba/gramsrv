package rpc

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

// verificationUserDirectory is the optional viewer-independent account reader.
// users.Service satisfies it; it is preferred over UsersService.ByID because
// "does this peer exist" must not depend on a viewer projection.
type verificationUserDirectory interface {
	AdminUser(ctx context.Context, userID int64) (domain.User, bool, error)
}

// verificationChannelDirectory is the optional non-personalized channel base-row
// reader. channels.Service satisfies it (see the same assertion in communities.go);
// the badge hook has no viewer of its own, so it cannot use GetChannel.
type verificationChannelDirectory interface {
	GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error)
}

// NotifyPeerVerified is the protocol-edge hook the official verification service
// invokes after an approve/reject/revoke decision has already committed. It makes
// the new user#b1b8cc83 verified:flags.17 / channel#d49f34c6 verified:flags.7 bit
// observable without waiting for a cache TTL:
//
//   - the cached peer projections for the target are dropped, so the next
//     users.getFullUser / channels.getFullChannel rebuilds from the committed row;
//   - online clients that already know the peer are pushed the ordinary, non-PTS
//     refresh update (updateUser / updateChannel) together with the re-projected
//     peer object, which is what flips the badge in an official client live.
//
// It deliberately reuses the paths the scam/fake moderation flags already use
// rather than inventing a verification-specific update: the badge is one more
// boolean on the same peer record, and a second mechanism could only drift.
//
// Offline sessions are not pushed to and do not need to be: verified is part of
// the peer's base read model, whose version is bumped by the users/channels
// triggers in 0001_init, so updates.getDifference and any later getFullUser /
// getUsers answer already carries the new flag.
//
// A push failure never invalidates the committed decision, so the caller logs and
// swallows the returned error; this method therefore reports problems instead of
// panicking, and is safe on a nil receiver.
func (r *Router) NotifyPeerVerified(ctx context.Context, peer domain.Peer) error {
	if r == nil {
		return nil
	}
	if peer.ID <= 0 {
		return fmt.Errorf("notify peer verified: invalid peer id %d", peer.ID)
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		return r.notifyUserVerified(ctx, peer.ID)
	case domain.PeerTypeChannel:
		return r.notifyChannelVerified(ctx, peer.ID)
	default:
		return fmt.Errorf("notify peer verified: unsupported peer type %q for peer %d", peer.Type, peer.ID)
	}
}

// notifyUserVerified covers ordinary accounts and bots alike: a verified bot is a
// user#b1b8cc83 with verified:flags.17, so it takes the same audience-wide
// updateUser fan-out the moderation flags use (owner plus every online account
// that already sees the peer).
func (r *Router) notifyUserVerified(ctx context.Context, userID int64) error {
	// Invalidate first and unconditionally: a decided application whose projection
	// still says "not verified" would keep serving the stale badge state even if the
	// push below cannot run.
	r.invalidateRPCProjectionForUser(userID)
	if r.deps.Users == nil {
		return nil
	}
	user, found, err := r.verificationUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("notify peer verified: load user %d: %w", userID, err)
	}
	if !found || user.ID == 0 {
		return fmt.Errorf("notify peer verified: user %d not found", userID)
	}
	// NotifyUserModerationFlagsChanged re-projects the peer per recipient, so the
	// snapshot handed in only carries identity; the pushed tg.User is always built
	// from a fresh read.
	return r.NotifyUserModerationFlagsChanged(ctx, user)
}

func (r *Router) verificationUser(ctx context.Context, userID int64) (domain.User, bool, error) {
	if directory, ok := r.deps.Users.(verificationUserDirectory); ok {
		return directory.AdminUser(ctx, userID)
	}
	return r.deps.Users.ByID(ctx, userID, userID)
}

// notifyChannelVerified reuses the channel state-mutation path, which invalidates
// the channel projections (plus a linked monoforum's) and pushes updateChannel
// with the refreshed chat object to the channel's members.
func (r *Router) notifyChannelVerified(ctx context.Context, channelID int64) error {
	r.invalidateRPCProjectionForChannel(channelID)
	if r.deps.Channels == nil {
		return nil
	}
	directory, ok := r.deps.Channels.(verificationChannelDirectory)
	if !ok {
		return fmt.Errorf("notify peer verified: channel service does not expose GetChannelByID")
	}
	channel, err := directory.GetChannelByID(ctx, channelID)
	if err != nil {
		return fmt.Errorf("notify peer verified: load channel %d: %w", channelID, err)
	}
	if channel.ID == 0 {
		return fmt.Errorf("notify peer verified: channel %d not found", channelID)
	}
	// Same hook the admin panel uses for any other channel base fact.
	return r.NotifyChannelChanged(ctx, channel)
}
