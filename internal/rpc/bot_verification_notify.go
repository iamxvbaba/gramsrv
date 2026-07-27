package rpc

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

// NotifyPeerBotVerification is the protocol-edge hook invoked after a third-party
// verification mark has already committed -- from bots.setCustomVerification, or
// from an operator action on the same rows. It makes the new
// user#b1b8cc83 bot_verification_icon:flags2.14 /
// channel#d49f34c6 bot_verification_icon:flags2.13 (and the matching
// userFull/channelFull payload) observable without waiting for a cache TTL:
//
//   - the cached peer projections for the target are dropped, so the next
//     users.getFullUser / channels.getFullChannel rebuilds from the committed row;
//   - online clients that already know the peer are pushed the ordinary, non-PTS
//     refresh update (updateUser / updateChannel) together with the re-projected
//     peer object, which is what makes the badge appear live in an official client.
//
// It is deliberately the same mechanism as NotifyPeerVerified, which is the same
// mechanism the scam/fake moderation flags use: the third-party mark is one more
// fact on the peer's base record, and inventing a verification-specific update
// would only add a second path that can drift. Nothing new is added to TL.
//
// The official verified flag is untouched here. That badge is granted by the
// operator alone (see verification_notify.go) and the two mechanisms never read or
// write each other's state.
//
// Offline sessions are not pushed to and do not need to be: the peer's base read
// model version is bumped by the users/channels triggers, so
// updates.getDifference and any later getUsers / getFullUser answer already carries
// the new mark.
//
// A push failure never invalidates the committed change, so callers log and swallow
// the returned error; this method therefore reports problems instead of panicking,
// and is safe on a nil receiver.
func (r *Router) NotifyPeerBotVerification(ctx context.Context, peer domain.Peer) error {
	if r == nil {
		return nil
	}
	if peer.ID <= 0 {
		return fmt.Errorf("notify peer bot verification: invalid peer id %d", peer.ID)
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		return r.notifyUserBotVerification(ctx, peer.ID)
	case domain.PeerTypeChannel:
		return r.notifyChannelBotVerification(ctx, peer.ID)
	default:
		return fmt.Errorf("notify peer bot verification: unsupported peer type %q for peer %d", peer.Type, peer.ID)
	}
}

// notifyUserBotVerification covers ordinary accounts and bots alike: a marked bot is
// a user#b1b8cc83 with bot_verification_icon:flags2.14, so it takes the same
// audience-wide updateUser fan-out the moderation flags use (the peer itself plus
// every online account that already sees it).
func (r *Router) notifyUserBotVerification(ctx context.Context, userID int64) error {
	// Invalidate first and unconditionally: a committed mark whose projection still
	// says "unmarked" would keep serving the stale badge state even if the push below
	// cannot run.
	r.invalidateRPCProjectionForUser(userID)
	if r.deps.Users == nil {
		return nil
	}
	user, found, err := r.verificationUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("notify peer bot verification: load user %d: %w", userID, err)
	}
	if !found || user.ID == 0 {
		return fmt.Errorf("notify peer bot verification: user %d not found", userID)
	}
	// NotifyUserModerationFlagsChanged re-projects the peer per recipient, so the
	// snapshot handed in only carries identity; the pushed tg.User is always built
	// from a fresh read and therefore picks the icon up from the batch overlay.
	return r.NotifyUserModerationFlagsChanged(ctx, user)
}

// notifyChannelBotVerification reuses the channel state-mutation path, which
// invalidates the channel projections (plus a linked monoforum's) and pushes
// updateChannel with the refreshed chat object to the channel's members.
func (r *Router) notifyChannelBotVerification(ctx context.Context, channelID int64) error {
	r.invalidateRPCProjectionForChannel(channelID)
	// The bot list cached for channelFull carries botInfo.verifier_settings, so a
	// verifier's own status change has to drop it too; the channel projection cache
	// is cleared by the same call.
	r.invalidateChannelFullBotInfoCacheForChannel(channelID)
	if r.deps.Channels == nil {
		return nil
	}
	directory, ok := r.deps.Channels.(verificationChannelDirectory)
	if !ok {
		return fmt.Errorf("notify peer bot verification: channel service does not expose GetChannelByID")
	}
	channel, err := directory.GetChannelByID(ctx, channelID)
	if err != nil {
		return fmt.Errorf("notify peer bot verification: load channel %d: %w", channelID, err)
	}
	if channel.ID == 0 {
		return fmt.Errorf("notify peer bot verification: channel %d not found", channelID)
	}
	// Same hook the admin panel uses for any other channel base fact.
	return r.NotifyChannelChanged(ctx, channel)
}
