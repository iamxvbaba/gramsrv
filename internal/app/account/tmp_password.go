package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const tmpPasswordSecretSize = 32

var errTmpPasswordUnavailable = errors.New("tmp password issuance is unavailable")

// WithTmpPasswords installs account.getTmpPassword issuance. A grant is bound
// to the requesting authorization, so the authorization store is required
// together with the grant store; the 2FA password version comes from
// WithAccountLifecycle.
func WithTmpPasswords(grants store.TmpPasswordStore, authorizations store.AuthorizationStore) ServiceOption {
	return func(s *Service) {
		s.tmpPasswords = grants
		s.authorizations = authorizations
	}
}

// IssueTmpPassword implements account.getTmpPassword. After a valid SRP proof
// it replaces this authorization's grant with a fresh bearer secret that is
// valid for period seconds, while the same login and the same 2FA password
// remain in place.
func (s *Service) IssueTmpPassword(ctx context.Context, userID int64, authKeyID [8]byte, check domain.PasswordCheck, period int, now time.Time) (domain.TmpPassword, error) {
	if s == nil || s.tmpPasswords == nil || s.authorizations == nil || s.lifecycle == nil {
		return domain.TmpPassword{}, errTmpPasswordUnavailable
	}
	if userID == 0 || authKeyID == ([8]byte{}) {
		return domain.TmpPassword{}, store.ErrAuthorizationStateChanged
	}
	if period < domain.TmpPasswordMinPeriod || period > domain.TmpPasswordMaxPeriod {
		return domain.TmpPassword{}, domain.ErrTmpPasswordPeriod
	}
	// WHY: the password version is read before the SRP proof. A concurrent
	// change can then only bind the grant to the replaced version, which
	// CheckTmpPassword rejects; it can never bind an old proof to a new password.
	state, err := s.RevenueWithdrawalPasswordState(ctx, userID)
	if err != nil {
		return domain.TmpPassword{}, err
	}
	if !state.HasPassword {
		return domain.TmpPassword{}, domain.ErrPasswordMissing
	}
	if state.PasswordChangedAt.IsZero() {
		return domain.TmpPassword{}, fmt.Errorf("tmp password: password version is unavailable")
	}
	authorization, found, err := s.authorizations.ByAuthKey(ctx, authKeyID)
	if err != nil {
		return domain.TmpPassword{}, err
	}
	if !found || authorization.UserID != userID || authorization.PasswordPending || authorization.CreatedAt.IsZero() {
		return domain.TmpPassword{}, store.ErrAuthorizationStateChanged
	}
	if err := s.CheckPassword(ctx, userID, check); err != nil {
		return domain.TmpPassword{}, err
	}
	secret := make([]byte, tmpPasswordSecretSize)
	if _, err := rand.Read(secret); err != nil {
		return domain.TmpPassword{}, err
	}
	// valid_until goes to the client in whole seconds; the grant expires at
	// exactly the second the client was told.
	validUntil := time.Unix(now.Unix()+int64(period), 0).UTC()
	if err := s.tmpPasswords.PutTmpPassword(ctx, domain.TmpPasswordGrant{
		AuthKeyID:              authKeyID,
		UserID:                 userID,
		AuthorizationCreatedAt: authorization.CreatedAt,
		PasswordChangedAt:      state.PasswordChangedAt,
		SecretHash:             sha256.Sum256(secret),
		ValidUntil:             validUntil,
	}); err != nil {
		return domain.TmpPassword{}, err
	}
	return domain.TmpPassword{Secret: secret, ValidUntil: validUntil}, nil
}

// CheckTmpPassword verifies a tmp_password proof for userID on authKeyID. Only
// the grant this authorization holds can match, and only before valid_until,
// on the same login, under the same 2FA password. An empty value is the
// absence of a proof, never the passwordless state, and there is no SRP
// fallback.
func (s *Service) CheckTmpPassword(ctx context.Context, userID int64, authKeyID [8]byte, secret []byte, now time.Time) error {
	if s == nil || s.tmpPasswords == nil || s.authorizations == nil || s.lifecycle == nil {
		return errTmpPasswordUnavailable
	}
	if userID == 0 || authKeyID == ([8]byte{}) || len(secret) != tmpPasswordSecretSize {
		return domain.ErrTmpPasswordInvalid
	}
	grant, found, err := s.tmpPasswords.TmpPasswordByAuthKey(ctx, authKeyID)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(secret)
	if !found || grant.UserID != userID || subtle.ConstantTimeCompare(digest[:], grant.SecretHash[:]) != 1 ||
		now.Unix() >= grant.ValidUntil.Unix() {
		return domain.ErrTmpPasswordInvalid
	}
	authorization, found, err := s.authorizations.ByAuthKey(ctx, authKeyID)
	if err != nil {
		return err
	}
	if !found || authorization.UserID != userID || authorization.PasswordPending ||
		!authorization.CreatedAt.Equal(grant.AuthorizationCreatedAt) {
		return domain.ErrTmpPasswordInvalid
	}
	state, err := s.RevenueWithdrawalPasswordState(ctx, userID)
	if err != nil {
		return err
	}
	if !state.HasPassword || !state.PasswordChangedAt.Equal(grant.PasswordChangedAt) {
		return domain.ErrTmpPasswordInvalid
	}
	return nil
}
