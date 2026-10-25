package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const (
	defaultScheduledDispatchBatch = 100
	defaultScheduledDispatchLease = 60
	defaultScheduledDispatchTick  = time.Second
	defaultExpiryDispatchBatch    = 100
	defaultExpiryDispatchTick     = time.Second
)

// ScheduledDispatcher promotes due scheduled messages into normal history.
type ScheduledDispatcher struct {
	router       *Router
	log          *zap.Logger
	batch        int
	leaseSeconds int
	interval     time.Duration
	maxIdle      time.Duration
}

func NewScheduledDispatcher(router *Router, log *zap.Logger) *ScheduledDispatcher {
	if log == nil {
		log = zap.NewNop()
	}
	return &ScheduledDispatcher{
		router:       router,
		log:          log,
		batch:        defaultScheduledDispatchBatch,
		leaseSeconds: defaultScheduledDispatchLease,
		interval:     defaultScheduledDispatchTick,
		maxIdle:      defaultIdleDispatchMaxInterval,
	}
}

// Run keeps v2's idle backoff and is woken early by new schedules and by a
// recipient coming online on this Core instance.
func (d *ScheduledDispatcher) Run(ctx context.Context) {
	if d == nil || d.router == nil {
		return
	}
	runIdleBackoffLoopWithWake(ctx, d.interval, d.maxIdle, d.router.scheduledWake, d.DispatchOnce)
}

func (d *ScheduledDispatcher) DispatchOnce(ctx context.Context) bool {
	if d == nil || d.router == nil || d.router.deps.Messages == nil {
		return false
	}
	now := int(d.router.clock.Now().Unix())
	// Each Core instance claims "when online" messages only for recipients its
	// own presence tracker sees online; every instance runs this dispatcher.
	items, err := d.router.deps.Messages.ClaimDueScheduledMessages(ctx, now, d.batch, d.leaseSeconds, d.router.presence.scheduledOnlineUsers(now))
	if err != nil {
		d.log.Warn("claim due scheduled messages", zap.Error(err))
		return false
	}
	for _, item := range items {
		if _, err := d.router.sendClaimedScheduledMessages(ctx, item.OwnerUserID, item.Peer, []domain.ScheduledMessage{item}, now); err != nil {
			d.log.Warn("send scheduled message", zap.Int64("owner_user_id", item.OwnerUserID), zap.Int("scheduled_id", item.ID), zap.Error(err))
		}
	}
	return len(items) > 0
}

func (p *presenceTracker) scheduledOnlineUsers(now int) []int64 {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	ids := make([]int64, 0, len(p.byUser))
	for id, sessions := range p.byUser {
		for _, status := range sessions {
			if status.Kind == domain.UserStatusOnline && status.Expires > now {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids
}

func (r *Router) validateOnlineSchedule(ctx context.Context, owner int64, peer domain.Peer) error {
	if peer.Type != domain.PeerTypeUser || peer.ID == owner || r.deps.Users == nil {
		return scheduleDateInvalidErr()
	}
	u, found, err := r.deps.Users.ByID(ctx, owner, peer.ID)
	if err != nil {
		return internalErr()
	}
	if !found || u.Bot {
		return scheduleDateInvalidErr()
	}
	if r.deps.Privacy == nil {
		return internalErr()
	}
	visible, err := r.deps.Privacy.CanSee(ctx, peer.ID, owner, domain.PrivacyKeyStatusTimestamp)
	if err != nil {
		return internalErr()
	}
	if !visible {
		return tgerr.New(400, "SCHEDULE_STATUS_PRIVATE")
	}
	return nil
}

func (r *Router) validateSchedule(ctx context.Context, owner int64, peer domain.Peer, date, repeat int) error {
	if !domain.ValidScheduleRepeat(repeat) || date <= 0 {
		return scheduleDateInvalidErr()
	}
	if date == domain.ScheduleWhenOnline {
		if repeat != 0 {
			return scheduleDateInvalidErr()
		}
		return r.validateOnlineSchedule(ctx, owner, peer)
	}
	if int64(date) >= domain.ScheduleWhenOnline || int64(date)+int64(repeat) >= domain.ScheduleWhenOnline {
		return scheduleDateInvalidErr()
	}
	if repeat > 0 && !r.userIsPremium(ctx, owner) {
		return premiumAccountRequiredErr()
	}
	return nil
}

func (r *Router) wakeScheduledDelivery() {
	if r == nil {
		return
	}
	select {
	case r.scheduledWake <- struct{}{}:
	default:
	}
}
