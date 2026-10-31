package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// RunPremiumSweeper periodically clears expired Premium and notifies the user's
// own online sessions about it.
//
// The correctness of Premium delivery is guaranteed by hydration deriving it on
// the fly (premium_expires_at > now) and does not depend on this sweeper; the
// sweeper only handles two cleanup duties: nulling out expired rows (keeping the
// index/semantics clean), and pushing updateUser + the latest self user to all of
// that user's online sessions so online clients immediately downgrade their UI
// (updateUser has no pts and does not enter update_events; offline devices heal
// themselves on reconnect through any response that carries the self user).
func (r *Router) RunPremiumSweeper(ctx context.Context, interval time.Duration, batch int) {
	if interval <= 0 {
		interval = time.Minute
	}
	if batch <= 0 {
		batch = 500
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.sweepExpiredPremium(ctx, batch)
	}
}

func (r *Router) sweepExpiredPremium(ctx context.Context, batch int) {
	var sweep func(context.Context, int, int) ([]domain.User, error)
	if r.deps.Premium != nil {
		sweep = r.deps.Premium.SweepExpired
	} else if svc, ok := r.deps.Users.(UserPremiumService); ok {
		sweep = func(ctx context.Context, now, limit int) ([]domain.User, error) {
			return svc.SweepExpiredPremium(ctx, int64(now), limit)
		}
	}
	if sweep == nil {
		return
	}
	for {
		sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		users, err := sweep(sweepCtx, int(r.clock.Now().Unix()), batch)
		cancel()
		if err != nil {
			r.log.Warn("premium sweep failed", zap.Error(err))
			return
		}
		for _, u := range users {
			r.invalidatePremiumUserCaches(ctx, u.ID)
			r.invalidateRPCProjectionForUser(u.ID)
		}
		r.pushPremiumStatusUpdates(ctx, users)
		// A batch that is not full means the current backlog is drained; a full
		// batch means keep going, so a long outage does not leave the backlog
		// spanning multiple periods.
		if len(users) == 0 {
			return
		}
	}
}

// viewerPremium reports whether the viewer currently has active Premium (used for
// the two-tier limit decision, best-effort: when the service is not wired, it is
// treated as the non-Premium tier).
func (r *Router) viewerPremium(ctx context.Context, userID int64) bool {
	svc, ok := r.deps.Users.(UserPremiumStatusService)
	return ok && svc.PremiumActive(ctx, userID)
}

// viewerSupport reports whether the viewer holds the official support account flag
// (used for the purchase threshold of support-exclusive gifts, best-effort: when
// the user service is not wired it does not allow through, never leaking to
// ordinary buyers).
func (r *Router) viewerSupport(ctx context.Context, userID int64) bool {
	if r == nil || r.deps.Users == nil {
		return false
	}
	self, err := r.deps.Users.Self(ctx, userID)
	return err == nil && self.Support
}

// NotifyUserChanged is a domain-only hook callable from the Admin use-case layer:
// after a change to base account facts it invalidates the RPC projection cache and
// pushes updateUser to the user's own online sessions. It does not leak tg.* into
// admin/domain/app; protocol objects are constructed only inside the rpc boundary.
func (r *Router) NotifyUserChanged(ctx context.Context, u domain.User) error {
	if r == nil || u.ID == 0 {
		return nil
	}
	if r.deps.UserProjectionFacts != nil {
		// Admin collectible-number mutations converge through this standard user
		// hook. Conservative invalidation on other low-frequency admin profile
		// writes is harmless and preserves read-your-write without waiting for NOTIFY.
		r.deps.UserProjectionFacts.InvalidateCollectiblePhoneFact(u.ID)
	}
	r.invalidateRPCProjectionForUser(u.ID)
	r.pushPremiumStatusUpdate(ctx, u)
	return nil
}

type moderationUserAudienceService interface {
	ModerationFlagAudience(ctx context.Context, userID int64, limit int) ([]int64, error)
}

type dialogPeerMetadataInvalidator interface {
	InvalidateDialog(userID int64, peer domain.Peer)
}

// NotifyUserModerationFlagsChanged sends the standard, non-PTS updateUser
// shape to online accounts that already know the peer. Offline accounts
// converge when their next authoritative peer/dialog read carries the updated
// User flags; no synthetic message-box event is created.
func (r *Router) NotifyUserModerationFlagsChanged(ctx context.Context, u domain.User) error {
	if r == nil || u.ID == 0 {
		return nil
	}
	r.invalidateRPCProjectionForUser(u.ID)
	if r.deps.Users == nil {
		return nil
	}
	audience := []int64{u.ID}
	if service, ok := r.deps.Users.(moderationUserAudienceService); ok {
		viewers, err := service.ModerationFlagAudience(ctx, u.ID, 4096)
		if err != nil {
			r.log.Warn("list moderation user update audience",
				zap.Int64("target_user_id", u.ID),
				zap.Error(err))
		} else if len(viewers) != 0 {
			audience = viewers
		}
	}

	pushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	botVerificationIcon := r.peerBotVerificationIcon(pushCtx, domain.Peer{Type: domain.PeerTypeUser, ID: u.ID})
	usernames := r.usernameRegistryMap(pushCtx, []domain.Peer{{Type: domain.PeerTypeUser, ID: u.ID}})
	seen := make(map[int64]struct{}, len(audience))
	dialogs, _ := r.deps.Dialogs.(dialogPeerMetadataInvalidator)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}
	for _, viewerUserID := range audience {
		if viewerUserID == 0 {
			continue
		}
		if _, ok := seen[viewerUserID]; ok {
			continue
		}
		seen[viewerUserID] = struct{}{}
		if dialogs != nil {
			// The getDialogs list hash intentionally describes dialog ordering, not
			// embedded User flags. Clearing the viewer's cached hash forces exactly
			// one full response with the refreshed peer and prevents a stale badge
			// from reappearing when the dialog list next refreshes.
			dialogs.InvalidateDialog(viewerUserID, peer)
		}
		if online, ok := r.deps.Sessions.(OnlineUserProvider); ok && !online.IsUserOnline(viewerUserID) {
			continue
		}
		users, err := r.deps.Users.ByIDs(pushCtx, viewerUserID, []int64{u.ID})
		if err != nil || len(users) == 0 {
			r.log.Warn("project moderation user update",
				zap.Int64("viewer_user_id", viewerUserID),
				zap.Int64("target_user_id", u.ID),
				zap.Error(err))
			continue
		}
		projected := tgUsersForViewer(viewerUserID, users)
		// The pushed peer object has to match what users.getUsers would answer, or the
		// client refreshes the peer straight back into the stale shape. The third-party
		// verification icon (user#b1b8cc83 bot_verification_icon:flags2.14) lives in a
		// read model rather than on domain.User, so it is stamped on here -- from the
		// single read taken before the loop, since it is the same peer for every
		// recipient. Zero leaves flags2.14 unset, which is the pre-feature shape.
		applyBotVerificationIconToUsers(projected, u.ID, botVerificationIcon)
		applyUsernamesFromRegistry(projected, nil, usernames)
		// A tombstone needs the name delivered through its own update as well.
		// updateUser alone is not enough: clients that only merge the photo out
		// of it (Android) keep the stale name, so the peer would stay a stranger
		// with an empty avatar instead of reading as Deleted Account.
		updates := []tg.UpdateClass{&tg.UpdateUser{UserID: u.ID}}
		if u.Deleted {
			updates = append(updates,
				&tg.UpdateUserName{
					UserID:    u.ID,
					FirstName: deletedAccountDisplayName,
				},
				// Explicit empty status: a missing field leaves the cached
				// last-seen in place and the account keeps reading as
				// recently online next to its own deleted presentation.
				&tg.UpdateUserStatus{
					UserID: u.ID,
					Status: &tg.UserStatusEmpty{},
				})
		}
		r.pushUserUpdates(pushCtx, viewerUserID, &tg.Updates{
			Updates: updates,
			Users:   projected,
			Date:    int(r.clock.Now().Unix()),
			Seq:     0,
		})
	}
	return nil
}

// pushPremiumStatusUpdate pushes the Premium status change to all of the user's
// own online sessions. Shared by grant, expiry and admin verification changes:
// updateUser makes the client refresh base flags such as premium/verified from the
// attached self user object (TDesktop processUser flips by flag).
func (r *Router) pushPremiumStatusUpdate(ctx context.Context, u domain.User) {
	r.pushPremiumStatusUpdates(ctx, []domain.User{u})
}

// pushPremiumStatusUpdates projects one username-registry snapshot over the
// whole online subset. Premium expiry is swept in batches of up to 500 users;
// reading each peer separately here would turn one maintenance batch into a
// serial registry N+1 even though offline users need no immediate push.
func (r *Router) pushPremiumStatusUpdates(ctx context.Context, candidates []domain.User) {
	if len(candidates) == 0 || r.deps.Sessions == nil {
		return
	}
	online, hasOnlineIndex := r.deps.Sessions.(OnlineUserProvider)
	users := make([]domain.User, 0, len(candidates))
	peers := make([]domain.Peer, 0, len(candidates))
	seen := make(map[int64]struct{}, len(candidates))
	for _, u := range candidates {
		if u.ID == 0 {
			continue
		}
		if _, ok := seen[u.ID]; ok {
			continue
		}
		seen[u.ID] = struct{}{}
		if hasOnlineIndex && !online.IsUserOnline(u.ID) {
			continue
		}
		users = append(users, u)
		peers = append(peers, domain.Peer{Type: domain.PeerTypeUser, ID: u.ID})
	}
	if len(users) == 0 {
		return
	}
	pushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	usernames := r.usernameRegistryMap(pushCtx, peers)
	date := int(r.clock.Now().Unix())
	for _, u := range users {
		projected := r.tgSelfUser(u)
		projectedUsers := []tg.UserClass{projected}
		applyUsernamesFromRegistry(projectedUsers, nil, usernames)
		r.pushUserUpdates(pushCtx, u.ID, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: u.ID}},
			Users:   projectedUsers,
			Date:    date,
		})
	}
}
