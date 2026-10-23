package rpc

import (
	"errors"
	"fmt"
	"testing"

	"telesrv/internal/otpdelivery"
)

func TestOTPRecipientRejected(t *testing.T) {
	final := fmt.Errorf("send login SMS code: %w", &otpdelivery.RejectedError{StatusCode: 200, Code: "PHONE_NUMBER_INVALID"})
	if !otpRecipientRejected(errors.Join(final, errors.New("rollback"))) {
		t.Fatal("final rejection not detected")
	}
	if otpRecipientRejected(&otpdelivery.RejectedError{Code: "BUSY", Retryable: true}) {
		t.Fatal("retryable rejection treated as invalid number")
	}
	if otpRecipientRejected(errors.New("timeout")) {
		t.Fatal("transport error treated as invalid number")
	}
}
