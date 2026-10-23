package rpc

import (
	"errors"

	"telesrv/internal/otpdelivery"
)

// otpRecipientRejected reports a code the SMS provider explicitly refused for
// good (for ShuzaGram: the numbers bot did not issue this number). Clients
// then see PHONE_NUMBER_INVALID instead of an internal error.
func otpRecipientRejected(err error) bool {
	var rejected *otpdelivery.RejectedError
	return errors.As(err, &rejected) && !rejected.Retryable
}
