package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// onAccountGetTmpPassword issues the temporary password that Layer 232
// payments.getStarGiftWithdrawalUrl and saved payment credentials carry
// instead of an SRP proof. The grant belongs to this permanent auth key; see
// account.Service.IssueTmpPassword for its scope and revocation.
func (r *Router) onAccountGetTmpPassword(ctx context.Context, req *tg.AccountGetTmpPasswordRequest) (*tg.AccountTmpPassword, error) {
	if req == nil || r.deps.Account == nil {
		return nil, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.userIsBot(ctx, userID) {
		return nil, botMethodInvalidErr()
	}
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok || authKeyID == ([8]byte{}) {
		return nil, authKeyUnregisteredErr()
	}
	issued, err := r.deps.Account.IssueTmpPassword(ctx, userID, authKeyID, domainPasswordCheck(req.Password), req.Period, r.clock.Now())
	if err != nil {
		return nil, tmpPasswordIssueErr(err)
	}
	return &tg.AccountTmpPassword{TmpPassword: issued.Secret, ValidUntil: int(issued.ValidUntil.Unix())}, nil
}

func tmpPasswordIssueErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrPasswordMissing):
		return tgerr.New(400, "PASSWORD_MISSING")
	case errors.Is(err, domain.ErrTmpPasswordPeriod):
		// No official error name is known for an out-of-range period; telesrv
		// names it after the parameter, like the other *_INVALID errors.
		return tgerr.New(400, "PERIOD_INVALID")
	case errors.Is(err, store.ErrAuthorizationStateChanged):
		return authKeyUnregisteredErr()
	default:
		return passwordErr(err)
	}
}
