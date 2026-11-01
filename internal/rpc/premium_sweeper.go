package rpc

import (
	"context"
	"fmt"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// RunPremiumSweeper periodically clears expired premium facts. Each batch
// commits the user-row mutation and one durable, non-PTS updateUser effect per
// changed account in the same store transaction; Egress performs delivery.
// Hydration still derives premium from premium_expires_at > now, so offline
// recovery remains an authoritative absolute read rather than synthetic PTS.
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
	if r.deps.Premium == nil {
		r.log.Error("premium sweep delivery dependency is not configured")
		return
	}
	for {
		sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		users, err := r.deps.Premium.SweepExpiredWithDelivery(sweepCtx, int(r.clock.Now().Unix()), batch, r.PremiumUserDeliveryEffects)
		cancel()
		if err != nil {
			r.log.Warn("premium sweep failed", zap.Error(err))
			return
		}
		for _, u := range users {
			if r.deps.UserProjectionFacts != nil {
				r.deps.UserProjectionFacts.InvalidateCollectiblePhoneFact(u.ID)
			}
			r.invalidatePremiumUserCaches(ctx, u.ID)
			r.invalidateRPCProjectionForUser(u.ID)
		}
		// A batch that is not full means the current backlog is drained; a full
		// batch means keep going, so a long outage does not leave the backlog
		// spanning multiple periods.
		if len(users) == 0 {
			return
		}
	}
}

func (r *Router) PremiumUserDeliveryEffects(users []domain.User) ([]store.DeliveryEffect, error) {
	if r == nil || len(users) == 0 {
		return nil, fmt.Errorf("premium expiry delivery requires changed users")
	}
	date := int(r.clock.Now().Unix())
	effects := make([]store.DeliveryEffect, 0, len(users))
	for _, user := range users {
		if user.ID <= 0 {
			return nil, fmt.Errorf("premium expiry delivery contains invalid user")
		}
		payload, err := encodeDeliveryUpdate(&tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: user.ID}},
			Users:   []tg.UserClass{r.tgSelfUser(user)},
			Date:    date,
		})
		if err != nil {
			return nil, err
		}
		effects = append(effects, store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: user.ID, Payload: payload,
			RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		}))
	}
	return effects, nil
}

func (r *Router) PremiumRefundDeliveryEffects(result domain.PremiumPurchaseResult) ([]store.DeliveryEffect, error) {
	userEffects, err := r.PremiumUserDeliveryEffects([]domain.User{result.User})
	if err != nil {
		return nil, err
	}
	starsEffects, err := r.StarsBalanceDeliveryEffects(result.Balance)
	if err != nil {
		return nil, err
	}
	return append(userEffects, starsEffects...), nil
}

func (r *Router) premiumPurchaseDeliveryEffects(excludeAuthKeyID [8]byte, excludeSessionID int64) store.DeliveryEffectsBuilder[domain.PremiumPurchaseResult] {
	return func(result domain.PremiumPurchaseResult) ([]store.DeliveryEffect, error) {
		if r == nil || result.User.ID <= 0 {
			return nil, fmt.Errorf("premium purchase delivery requires recipient user")
		}
		payload, err := encodeDeliveryUpdate(&tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: result.User.ID}},
			Users:   []tg.UserClass{r.tgSelfUser(result.User)},
			Date:    int(r.clock.Now().Unix()),
		})
		if err != nil {
			return nil, err
		}
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: result.User.ID, ExcludeAuthKeyID: excludeAuthKeyID,
			ExcludeSessionID: excludeSessionID, Payload: payload,
			RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}
}

func (r *Router) UserAudienceDeliveryEffects(snapshot store.UserAudienceDeliverySnapshot) ([]store.DeliveryEffect, error) {
	if r == nil || snapshot.User.ID <= 0 || len(snapshot.Audience) == 0 {
		return nil, fmt.Errorf("user audience delivery requires a user and viewers")
	}
	updates := []tg.UpdateClass{&tg.UpdateUser{UserID: snapshot.User.ID}}
	if snapshot.User.Deleted {
		// A tombstone needs the name delivered through its own update as well:
		// clients that only merge the photo out of updateUser (Android) keep the
		// stale name. The explicit empty status stops a cached last-seen from
		// reading as recently online next to the deleted presentation.
		updates = append(updates,
			&tg.UpdateUserName{UserID: snapshot.User.ID, FirstName: deletedAccountDisplayName},
			&tg.UpdateUserStatus{UserID: snapshot.User.ID, Status: &tg.UserStatusEmpty{}})
	}
	payload, err := encodeDeliveryUpdate(&tg.Updates{
		Updates: updates,
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	})
	if err != nil {
		return nil, err
	}
	effects := make([]store.DeliveryEffect, 0, len(snapshot.Audience))
	for _, viewerID := range snapshot.Audience {
		effects = append(effects, store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: viewerID, Payload: payload,
			RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		}))
	}
	return effects, nil
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
