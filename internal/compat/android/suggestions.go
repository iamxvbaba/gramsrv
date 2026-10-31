// Package android consolidates compatibility decisions for the official DrKLO Android client.
package android

import "strings"

const ValidatePhoneNumberSuggestion = "VALIDATE_PHONE_NUMBER"

// DismissSuggestion returns telesrv's bounded compatibility result for a suggestion
// dismissal. telesrv currently publishes no pending suggestions to config/channelFull,
// so there is no server-side suggestion state to persist; a non-empty dismissal from a
// logged-in client is idempotently acknowledged, and it stops DrKLO from retrying a
// generic 500 indefinitely.
func DismissSuggestion(suggestion string) bool {
	return strings.TrimSpace(suggestion) != ""
}
