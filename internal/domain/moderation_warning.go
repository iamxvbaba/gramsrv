package domain

// This file implements the server-side text form of SCAM/FAKE marks in the profile description (About):
// official clients render a localized badge based on tg.User.scam / tg.Channel.scam, but a bare badge
// is often ignored by the audience of a self-hosted deployment; so the About returned by getFullUser / channels.getFullChannel /
// invite previews injects a platform warning on the first line, followed by the peer's own description.
//
// The warning text comes from TELESRV_SCAM_WARNING / TELESRV_FAKE_WARNING (see internal/config,
// injected at startup by cmd/telesrv/main.go), and an empty value uses the built-in English default — consistent with
// the .env.example note «Empty keeps built-in English defaults».
//
// The stored description is never rewritten: the warning exists only on the read-path projection, and after the owner edits
// the description the warning still reappears on the first line (semantic guard in rpc/moderation_flags_projection_test.go).

import (
	"strings"
	"sync/atomic"
)

// The built-in default text is a full warning sentence (rather than the official client's SCAM/FAKE badge word): the official client only
// draws a two-letter badge next to the name, with no readable explanation in the description; here "why it was flagged" is written directly
// into the first line of the description, consistent with the operator-overridable semantics in .env (empty value → the text below).
// Both use official wording: fake is impersonating a real person/well-known organization, scam is fraud and demanding money.
const (
	defaultScamProfileWarning = "⚠️ Warning: This account has received numerous complaints " +
		"related to fraud or attempts to impersonate an official account. Be cautious, " +
		"especially if you are asked for money."
	defaultFakeProfileWarning = "⚠️ Warning: Many users have reported that this account is " +
		"impersonating a well-known person or organization."
)

// Held in an atomic.Value, the same pattern as startup-time configuration such as officialSystemUserDisplayName:
// the projection path is read-only, avoiding a data race if runtime refresh is added later.
var (
	scamProfileWarning atomic.Value // string
	fakeProfileWarning atomic.Value // string
)

// SetModerationProfileWarning records the operator-configured warning text; called once at startup.
// An empty value (or pure whitespace) means use the built-in English default.
func SetModerationProfileWarning(scam, fake string) {
	scamProfileWarning.Store(strings.TrimSpace(scam))
	fakeProfileWarning.Store(strings.TrimSpace(fake))
}

// moderationProfileWarning returns the currently effective warning text; an unflagged peer returns an empty string.
// admin rejects setting both flags at once (ErrPeerModerationFlagsInvalid), so scam taking priority is enough.
func moderationProfileWarning(scam, fake bool) string {
	if scam {
		if text, ok := scamProfileWarning.Load().(string); ok && text != "" {
			return text
		}
		return defaultScamProfileWarning
	}
	if fake {
		if text, ok := fakeProfileWarning.Load().(string); ok && text != "" {
			return text
		}
		return defaultFakeProfileWarning
	}
	return ""
}

// ModerationProfileWarning prepends the platform warning to the peer's own description:
// when unflagged it returns about verbatim (zero allocation); when flagged it returns "warning\ndescription",
// and when the description is empty it returns only the warning. The separator is a single newline — About renders line by line in the client.
func ModerationProfileWarning(scam, fake bool, about string) string {
	warning := moderationProfileWarning(scam, fake)
	if warning == "" {
		return about
	}
	if about == "" {
		return warning
	}
	return warning + "\n" + about
}
