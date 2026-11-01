package rpc

import (
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tgerr"
)

// preCheckoutTimeoutSeconds is the window during which the client waits for the bot's reply. The
// Bot API explicitly requires answering pre_checkout_query within 10 seconds; after the timeout the
// payment fails.
const preCheckoutTimeoutSeconds = 10

// preCheckoutErrorMaxLen is the length limit for error_message, consistent with the Bot API: too
// long is rejected outright rather than silently truncated into a statement whose meaning has
// changed.
const preCheckoutErrorMaxLen = 200

// preCheckoutFailedErr is "the payment gate was not approved": the bot explicitly declined, did not
// answer, or answered too late.
//
// It is a different thing from "payment failed": the former is the seller rejecting the order, the
// latter is a technical failure. PAYMENT_FAILED is exactly the error the Bot API uses when
// pre-checkout does not pass.
func preCheckoutFailedErr() error {
	return tgerr.New(400, "PAYMENT_FAILED")
}

// preCheckoutDeclinedErr carries the reason given by the bot — the Bot API shows this
// error_message to the payer, so the reason must be delivered to the client verbatim.
//
// Here tgerr.Error is hand-written instead of tgerr.New: New reverse-parses Type from Message,
// whereas we need Type fixed to PAYMENT_FAILED and Message to hold the bot's own words. The
// rationale for banning hand-writing in errors.go concerns reverse-parsing parameterized fields
// like FLOOD_WAIT_X, which does not apply here.
func preCheckoutDeclinedErr(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return preCheckoutFailedErr()
	}
	if utf8.RuneCountInString(message) > preCheckoutErrorMaxLen {
		return tgerr.New(400, "MESSAGE_TOO_LONG")
	}
	return &tgerr.Error{
		Code:    400,
		Type:    "PAYMENT_FAILED",
		Message: message,
	}
}
