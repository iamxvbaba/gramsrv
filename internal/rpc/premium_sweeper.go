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
	svc, ok := r.deps.Users.(UserPremiumService)
	if !ok {
		return
	}
	for {
		sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		users, err := svc.SweepExpiredPremium(sweepCtx, r.clock.Now().Unix(), batch)
		cancel()
		if err != nil {
			r.log.Warn("premium sweep failed", zap.Error(err))
			return
		}
		for _, u := range users {
			r.pushPremiumStatusUpdate(ctx, u)
		}
		// 不满一批说明已扫完当前积压；满批则继续，避免长停机后积压跨多个周期。
		if len(users) < batch {
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

// NotifyUserChanged 是 Admin 用例层可调用的 domain-only hook：账号基础事实
// 变更后失效 RPC 投影缓存，并向本人在线 session 推 updateUser。它不把 tg.*
// 泄漏给 admin/domain/app，协议对象只在 rpc 边界内构造。
func (r *Router) NotifyUserChanged(ctx context.Context, u domain.User) error {
	if r == nil || u.ID == 0 {
		return nil
	}
	r.invalidateRPCProjectionForUser(u.ID)
	r.pushPremiumStatusUpdate(ctx, u)
	return nil
}

// pushPremiumStatusUpdate 向用户本人的全部在线 session 推送会员状态变化。
// 授予、到期与 admin 认证变更共用：updateUser 触发客户端用随附的 self user
// 对象刷新 premium/verified 等基础 flag（TDesktop processUser 按 flag 翻转）。
func (r *Router) pushPremiumStatusUpdate(ctx context.Context, u domain.User) {
	if u.ID == 0 {
		return
	}
	pushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r.pushUserUpdates(pushCtx, u.ID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: u.ID}},
		Users:   []tg.UserClass{r.tgSelfUser(u)},
		Date:    int(r.clock.Now().Unix()),
	})
}
