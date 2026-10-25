package rpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// RevokeAuthorizationAuthKey is the domain-only hook used by the internal Admin API
// after the auth service has removed the durable authorization/auth_key rows.
func (r *Router) RevokeAuthorizationAuthKey(ctx context.Context, authKeyID [8]byte, userID int64) error {
	if r == nil || authKeyID == ([8]byte{}) {
		return nil
	}
	revokeErr := r.revokeAuthKeySessionsBounded(ctx, authKeyID)
	clearErr := r.clearAuthKeyState(ctx, authKeyID)
	if userID != 0 {
		r.discardSecretChatsForAuthKey(ctx, businessAuthKeyInt64(authKeyID), userID)
	}
	return errors.Join(revokeErr, clearErr)
}

// InvalidateUserProjection is a process-local cache operation invoked only
// after an admin aggregate transaction commits. It never produces client
// delivery; the corresponding absolute updateUser effects are already durable.
func (r *Router) InvalidateUserProjection(userID int64) {
	if r == nil || userID <= 0 {
		return
	}
	if r.deps.UserProjectionFacts != nil {
		r.deps.UserProjectionFacts.InvalidateCollectiblePhoneFact(userID)
	}
	r.invalidateRPCProjectionForUser(userID)
}

// NotifyChannelChanged is the domain-only hook used by the internal Admin API
// after a channel/supergroup base fact changed.
func (r *Router) NotifyChannelChanged(ctx context.Context, ch domain.Channel) error {
	if r == nil || ch.ID == 0 {
		return nil
	}
	r.channelStateMutationUpdates(ctx, ch.CreatorUserID, ch)
	return nil
}

// StarsBalanceDeliveryEffects encodes the non-PTS absolute balance refresh
// while the Stars aggregate transaction is still open.
func (r *Router) StarsBalanceDeliveryEffects(balance domain.StarsBalance) ([]store.DeliveryEffect, error) {
	return r.starsBalanceDeliveryEffects(balance, [8]byte{}, 0)
}

func (r *Router) starsBalanceDeliveryEffects(balance domain.StarsBalance, excludeAuthKeyID [8]byte, excludeSessionID int64) ([]store.DeliveryEffect, error) {
	if r == nil || balance.UserID <= 0 {
		return nil, fmt.Errorf("stars balance delivery requires a ledger owner")
	}
	if (excludeAuthKeyID == ([8]byte{})) != (excludeSessionID == 0) {
		return nil, fmt.Errorf("stars balance delivery exclusion requires raw auth key and session")
	}
	payload, err := encodeDeliveryUpdate(&tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance.Balance}}},
		Date:    int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, err
	}
	return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
		TargetUserID: balance.UserID, ExcludeAuthKeyID: excludeAuthKeyID, ExcludeSessionID: excludeSessionID, Payload: payload,
		RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	})}, nil
}

// NotifyAccountFreezeChanged invalidates target-scoped projections immediately
// and wakes the durable audience nudge worker. Cross-instance cache invalidation
// is also carried by the committed user_visibility read-model notification.
func (r *Router) NotifyAccountFreezeChanged(_ context.Context, freeze domain.AccountFreeze) error {
	if r == nil || freeze.UserID == 0 {
		return nil
	}
	r.applyFrozenPresence(freeze.UserID, freeze.Frozen)
	if freeze.Frozen {
		r.announceFrozenEmptyStatus(freeze.UserID)
	}
	if r.deps.UserProjectionFacts != nil {
		r.deps.UserProjectionFacts.InvalidateAccountFreezeFact(freeze.UserID)
	}
	r.invalidateRPCProjectionForUser(freeze.UserID)
	if r.accountFreezeWake != nil {
		select {
		case r.accountFreezeWake <- struct{}{}:
		default:
		}
	}
	return nil
}
