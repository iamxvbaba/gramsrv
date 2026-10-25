package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

func TestPasswordErrMapsOccupiedLoginEmailToNotAllowed(t *testing.T) {
	if err := passwordErr(domain.ErrEmailOccupied); !tgerr.Is(err, "EMAIL_NOT_ALLOWED") {
		t.Fatalf("passwordErr(ErrEmailOccupied) = %v, want EMAIL_NOT_ALLOWED", err)
	}
}

func TestPasswordErrMapsRecoveryState(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{domain.ErrRecoveryCodeEmpty, "CODE_EMPTY"},
		{&domain.EmailUnconfirmedError{CodeLength: 6}, "EMAIL_UNCONFIRMED"},
		{domain.ErrRecoveryCodeInvalid, "CODE_INVALID"},
		{domain.ErrPasswordRecoveryExpired, "PASSWORD_RECOVERY_EXPIRED"},
	}
	for _, tc := range tests {
		if err := passwordErr(tc.err); !tgerr.Is(err, tc.want) {
			t.Fatalf("passwordErr(%v)=%v, want %s", tc.err, err, tc.want)
		}
	}
}

func TestBindTempAuthKeyErrPreservesRecoverableRotationErrors(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: auth.ErrExpiresAtInvalid, want: "EXPIRES_AT_INVALID"},
		{err: auth.ErrTempAuthKeyEmpty, want: "TEMP_AUTH_KEY_EMPTY"},
		{err: auth.ErrEncryptedMessageInvalid, want: "ENCRYPTED_MESSAGE_INVALID"},
		{err: auth.ErrTempAuthKeyAlreadyBound, want: "TEMP_AUTH_KEY_ALREADY_BOUND"},
	}
	for _, test := range tests {
		t.Run(test.want, func(t *testing.T) {
			if err := bindTempAuthKeyErr(test.err); !tgerr.Is(err, test.want) {
				t.Fatalf("bindTempAuthKeyErr(%v) = %v, want %s", test.err, err, test.want)
			}
		})
	}
}
