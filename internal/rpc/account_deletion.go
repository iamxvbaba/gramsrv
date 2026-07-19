package rpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
	"telesrv/internal/postresponse"
)

type accountDeletionService interface {
	DeleteAccount(ctx context.Context, userID int64, authKeyID [8]byte, reason string, password *domain.PasswordCheck, now time.Time) (domain.AccountDeleteOutcome, error)
	SendConfirmPhoneCode(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64, hash string) (string, domain.AuthCodeDelivery, error)
	ConfirmPhone(ctx context.Context, userID int64, authKeyID [8]byte, phoneCodeHash, code string, now time.Time) ([]domain.Authorization, error)
	ResendConfirmPhoneCode(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64, phone, oldHash string) (string, domain.AuthCodeDelivery, bool, error)
	CancelConfirmPhoneCode(ctx context.Context, userID int64, authKeyID [8]byte, phone, hash string) (bool, error)
}

func (r *Router) accountDeletionSvc() (accountDeletionService, bool) {
	svc, ok := r.deps.Account.(accountDeletionService)
	return svc, ok
}

func (r *Router) onAccountDeleteAccount(ctx context.Context, req *tg.AccountDeleteAccountRequest) (bool, error) {
	userID, authorized, passwordPending, err := r.currentOrPendingPasswordUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if userID == 0 || (!authorized && !passwordPending) {
		return false, authKeyUnregisteredErr()
	}
	svc, ok := r.accountDeletionSvc()
	if !ok {
		return false, internalErr()
	}
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok || authKeyID == ([8]byte{}) {
		return false, authKeyUnregisteredErr()
	}
	var password *domain.PasswordCheck
	if check, present := req.GetPassword(); present {
		converted := domainPasswordCheck(check)
		password = &converted
	}
	outcome, err := svc.DeleteAccount(ctx, userID, authKeyID, req.Reason, password, time.Now().UTC())
	if err != nil {
		return false, accountDeletionErr(err)
	}
	if outcome.Kind == domain.AccountDeleteDelayed {
		wait := outcome.WaitSeconds
		if wait < 1 {
			wait = 1
		}
		return false, tgerr.New(420, fmt.Sprintf("2FA_CONFIRM_WAIT_%d", wait))
	}
	r.finishDeletedAccountAuthorizations(ctx, userID, outcome.Deletion.RevokedAuthorizations)
	r.invalidateRPCProjectionForUser(userID)
	dispatchNotifications := func() {
		dispatchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r.runAccountLifecycleOnce(dispatchCtx, 500)
	}
	if !postresponse.Register(ctx, dispatchNotifications) {
		go dispatchNotifications()
	}
	return true, nil
}

func (r *Router) onAccountSendConfirmPhoneCode(ctx context.Context, req *tg.AccountSendConfirmPhoneCodeRequest) (tg.AuthSentCodeClass, error) {
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !authorized || userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	svc, ok := r.accountDeletionSvc()
	if !ok {
		return nil, internalErr()
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	hash, delivery, err := svc.SendConfirmPhoneCode(ctx, userID, authKeyID, sessionID, req.Hash)
	if err != nil {
		return nil, accountDeletionErr(err)
	}
	return tgSMSSentCode(hash, delivery.Length), nil
}

func (r *Router) onAccountConfirmPhone(ctx context.Context, req *tg.AccountConfirmPhoneRequest) (bool, error) {
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if !authorized || userID == 0 {
		return false, authKeyUnregisteredErr()
	}
	svc, ok := r.accountDeletionSvc()
	if !ok {
		return false, internalErr()
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	revoked, err := svc.ConfirmPhone(ctx, userID, authKeyID, req.PhoneCodeHash, req.PhoneCode, time.Now().UTC())
	if err != nil {
		return false, accountDeletionErr(err)
	}
	r.finishDeletedAccountAuthorizations(ctx, userID, revoked)
	return true, nil
}

func (r *Router) finishDeletedAccountAuthorizations(ctx context.Context, userID int64, revoked []domain.Authorization) {
	current, _ := AuthKeyIDFrom(ctx)
	for _, authorization := range revoked {
		a := authorization
		finish := func() {
			r.discardSecretChatsForAuthKey(context.Background(), businessAuthKeyInt64(a.AuthKeyID), userID)
			r.revokeAuthKeySessions(a.AuthKeyID)
		}
		if a.AuthKeyID == current {
			if postresponse.Register(ctx, finish) {
				continue
			}
		}
		finish()
	}
}

func accountDeletionErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrPasswordHashInvalid), errors.Is(err, domain.ErrSRPIDInvalid), errors.Is(err, domain.ErrSRPPasswordChanged):
		return passwordErr(err)
	case errors.Is(err, domain.ErrAccountDeletionHashInvalid), errors.Is(err, domain.ErrAccountDeletionNotPending):
		return tgerr.New(400, "HASH_INVALID")
	case errors.Is(err, domain.ErrPhoneCodeEmpty):
		return phoneCodeEmptyErr()
	case errors.Is(err, domain.ErrPhoneCodeInvalid):
		return phoneCodeInvalidErr()
	case errors.Is(err, domain.ErrPhoneCodeExpired):
		return phoneCodeExpiredErr()
	case errors.Is(err, domain.ErrAccountDeletionForbidden):
		return botMethodInvalidErr()
	case errors.Is(err, domain.ErrAccountDeleted):
		return authKeyUnregisteredErr()
	default:
		return internalErr()
	}
}
