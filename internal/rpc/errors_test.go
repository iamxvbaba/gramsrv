package rpc

import (
	"testing"

	"github.com/gotd/td/tgerr"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

func TestPasswordErrMapsOccupiedLoginEmailToNotAllowed(t *testing.T) {
	if err := passwordErr(domain.ErrEmailOccupied); !tgerr.Is(err, "EMAIL_NOT_ALLOWED") {
		t.Fatalf("passwordErr(ErrEmailOccupied) = %v, want EMAIL_NOT_ALLOWED", err)
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
