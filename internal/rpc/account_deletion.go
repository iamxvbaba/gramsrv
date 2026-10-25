package rpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

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

var errAccountAuthorizationPostResponseUnavailable = errors.New("rpc: account authorization teardown requires post-response delivery")

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
	// v2: the tombstone reaches every contact/dialog viewer through the durable
	// account_deletion_notifications queue, whose worker converts rows into
	// edge_delivery_outbox deliveries; wake it instead of pushing from Core.
	r.wakeAccountDeletionNotifications()
	if err := r.finishDeletedAccountAuthorizations(ctx, userID, outcome.Deletion.RevokedAuthorizations); err != nil {
		return false, internalErr()
	}
	r.invalidateDeletedUserProjectionFacts(userID)
	// A tombstone changes this target for every viewer. Flushing once is bounded
	// and avoids four full-cache predicate scans; the PostgreSQL user_deleted
	// event performs the same coarse invalidation on other instances.
	r.flushRPCProjectionCache()
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
	if err := r.finishDeletedAccountAuthorizations(ctx, userID, revoked); err != nil {
		return false, internalErr()
	}
	return true, nil
}

func (r *Router) finishDeletedAccountAuthorizations(ctx context.Context, userID int64, revoked []domain.Authorization) error {
	current, _ := AuthKeyIDFrom(ctx)
	var deliveryErr error
	for _, authorization := range revoked {
		// Secret-chat state/event convergence is durable store work and must not
		// depend on the non-durable post-response transport commit succeeding.
		r.discardSecretChatsForAuthKey(context.Background(), businessAuthKeyInt64(authorization.AuthKeyID), userID)
		if authorization.AuthKeyID == current {
			r.prepareDeletedAccountAuthorizationRetirement(authorization.AuthKeyID)
			if postresponse.RegisterAction(ctx, postresponse.Action{
				Kind: postresponse.ActionAccountAuthorizationTeardown,
				AccountAuthorizationTeardown: postresponse.AccountAuthorizationTeardownAction{
					UserID:    userID,
					AuthKeyID: authorization.AuthKeyID,
				},
			}) {
				continue
			}
			deliveryErr = errAccountAuthorizationPostResponseUnavailable
			continue
		}
		r.revokeAuthKeySessions(authorization.AuthKeyID)
	}
	return deliveryErr
}

// prepareDeletedAccountAuthorizationRetirement removes positive Core identity
// facts immediately after the durable revocation commits. The authorization
// invalidation broker converges other Core caches but deliberately does not
// close the owning Edge transport; that remains delivery-gated below.
func (r *Router) prepareDeletedAccountAuthorizationRetirement(authKeyID [8]byte) {
	r.invalidateAuthUserCache(authKeyID)
	rawTempAuthKeyIDs := r.invalidateTempAuthKeyCacheForPerm(authKeyID)
	r.publishAuthInvalidation(context.Background(), append([][8]byte{authKeyID}, rawTempAuthKeyIDs...)...)
}

// retireDeletedAccountAuthorization is safe to repeat. The production
// SessionControlFabric closes every Edge session bound to the business key,
// including bound temp-key transports; unbind is the idempotent final cleanup.
func (r *Router) retireDeletedAccountAuthorization(authKeyID [8]byte) {
	if terminator, ok := r.deps.Sessions.(SessionTerminator); ok {
		terminator.CloseSessionsForBusinessAuthKey(authKeyID)
	}
	r.unbindAuthKey(authKeyID)
}

// NotifyModerationAccountDeletion completes the runtime half of the durable
// moderation tombstone. Moderation runs off-request, so every revoked auth key
// can be disconnected immediately; reconnects then observe the missing durable
// authorization and the auth service's tombstone guard.
func (r *Router) NotifyModerationAccountDeletion(ctx context.Context, result domain.AccountDeletionResult) {
	if r == nil || !result.Changed || result.User.ID == 0 {
		return
	}
	if err := r.finishDeletedAccountAuthorizations(ctx, result.User.ID, result.RevokedAuthorizations); err != nil {
		r.log.Error("moderation account deletion authorization teardown unavailable", zap.Error(err))
	}
	r.invalidateDeletedUserProjectionFacts(result.User.ID)
	r.flushRPCProjectionCache()
	r.wakeAccountDeletionNotifications()
}

func (r *Router) invalidateDeletedUserProjectionFacts(userID int64) {
	if r == nil || userID == 0 || r.deps.UserProjectionFacts == nil {
		return
	}
	r.deps.UserProjectionFacts.InvalidateAccountFreezeFact(userID)
	r.deps.UserProjectionFacts.InvalidateCollectiblePhoneFact(userID)
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
