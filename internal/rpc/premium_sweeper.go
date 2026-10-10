package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// RunPremiumSweeper 周期清理到期会员并通知本人在线 session。
//
// premium 下发正确性由 hydration 即时派生（premium_expires_at > now）保证，
// 不依赖本 sweeper；这里只负责两件收尾事：把过期行清 NULL（保持索引/语义
// 干净），以及向该用户全部在线 session 推 updateUser + 最新 self user，让在线
// 客户端立即降级 UI（updateUser 无 pts，不进 update_events；离线设备重连后由
// 任意带 self user 的响应自愈）。
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
		// 不满一批说明已扫完当前积压；满批则继续，避免长停机后积压跨多个周期。
		if len(users) == 0 {
			return
		}
	}
}

// viewerPremium 报告 viewer 当前是否有效会员（限额双档判断用，best-effort：
// 服务未接通时按非会员档处理）。
func (r *Router) viewerPremium(ctx context.Context, userID int64) bool {
	svc, ok := r.deps.Users.(UserPremiumStatusService)
	return ok && svc.PremiumActive(ctx, userID)
}

// viewerSupport 报告 viewer 是否持有官方 support 账号标记（support 专属礼物
// 的购买门槛用，best-effort：用户服务未接通时不放行，绝不向普通买家泄漏）。
func (r *Router) viewerSupport(ctx context.Context, userID int64) bool {
	if r == nil || r.deps.Users == nil {
		return false
	}
	self, err := r.deps.Users.Self(ctx, userID)
	return err == nil && self.Support
}

// NotifyUserChanged 是 Admin 用例层可调用的 domain-only hook：账号基础事实
// 变更后失效 RPC 投影缓存，并向本人在线 session 推 updateUser。它不把 tg.*
// 泄漏给 admin/domain/app，协议对象只在 rpc 边界内构造。
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

// pushPremiumStatusUpdate 向用户本人的全部在线 session 推送会员状态变化。
// 授予、到期与 admin 认证变更共用：updateUser 触发客户端用随附的 self user
// 对象刷新 premium/verified 等基础 flag（TDesktop processUser 按 flag 翻转）。
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
