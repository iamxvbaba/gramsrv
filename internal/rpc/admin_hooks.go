package rpc

import (
	"context"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// RevokeAuthorizationAuthKey is the domain-only hook used by the internal Admin API
// after the auth service has removed the durable authorization/auth_key rows.
func (r *Router) RevokeAuthorizationAuthKey(ctx context.Context, authKeyID [8]byte, userID int64) error {
	if r == nil || authKeyID == ([8]byte{}) {
		return nil
	}
	r.revokeAuthKeySessions(authKeyID)
	if err := r.clearAuthKeyState(ctx, authKeyID); err != nil {
		return err
	}
	if userID != 0 {
		r.discardSecretChatsForAuthKey(ctx, businessAuthKeyInt64(authKeyID), userID)
	}
	return nil
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

// NotifyStarsBalanceChanged is the domain-only hook used by the internal Admin
// API after the local Stars ledger balance has changed outside a client RPC.
func (r *Router) NotifyStarsBalanceChanged(ctx context.Context, balance domain.StarsBalance) error {
	if r == nil || balance.UserID == 0 {
		return nil
	}
	r.pushUserUpdates(ctx, balance.UserID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance.Balance}}},
		Date:    int(r.clock.Now().Unix()),
	})
	return nil
}
