package domain

import "strings"

// LoginMethod distinguishes the two login methods for which the welcome message template can be customized per channel: phone
// (SMS/app code) and email (email-login account, see SignInMethodLabel). There is no third dimension
// — signup or not, 2FA or not, both make no difference, because recordWelcomeMessage's caller
// carries no more information.
type LoginMethod string

const (
	LoginMethodPhone LoginMethod = "phone"
	LoginMethodEmail LoginMethod = "email"
)

// LoginMethodFromLabel maps the human-readable SignInMethodLabel string back to
// LoginMethod, so callers that already computed the label (the {{...}} template's historical "via %s"
// wording) need not recompute it from User.
func LoginMethodFromLabel(label string) LoginMethod {
	if label == "email" {
		return LoginMethodEmail
	}
	return LoginMethodPhone
}

// DefaultWelcomeMessagePhoneTemplate / DefaultWelcomeMessageEmailTemplate are
// the built-in final fallback text for the login welcome message (sent by the official system account 777000 after each completed login).
// The two messages are deliberately separate (rather than one template with a method-name substitution) so each channel reads naturally.
//
// {{server_name}} is replaced with the server's currently effective display name
// (see ResolveWelcomeMessageTemplate / RenderWelcomeMessageTemplate).
const (
	DefaultWelcomeMessagePhoneTemplate = "👋 Welcome to {{server_name}}!\n\nA new sign-in to your account was just completed using your phone number.\n\nIf this was you, no action is needed. If it wasn't, please revoke this session immediately from Settings → Privacy and Security → Active Sessions."

	DefaultWelcomeMessageEmailTemplate = "👋 Welcome to {{server_name}}!\n\nA new sign-in to your account was just completed using your email address.\n\nIf this was you, no action is needed. If it wasn't, please revoke this session immediately from Settings → Privacy and Security → Active Sessions."
)

// ResolveWelcomeMessageTemplate picks the message template body for the given login method by priority:
// an explicit admin-panel override (panelOverride, stored verbatim in identity.Info, empty means unset)
// → an explicit environment-variable default (envDefault, empty means unset) → that method's compile-time built-in text.
// A pure function, for dependency-free testing of the priority. The caller (internal/app/auth) invokes it
// live on every recordWelcomeMessage (never cached) so admin-panel edits take effect immediately.
func ResolveWelcomeMessageTemplate(method LoginMethod, panelOverride, envDefault string) string {
	if t := strings.TrimSpace(panelOverride); t != "" {
		return panelOverride
	}
	if t := strings.TrimSpace(envDefault); t != "" {
		return envDefault
	}
	if method == LoginMethodEmail {
		return DefaultWelcomeMessageEmailTemplate
	}
	return DefaultWelcomeMessagePhoneTemplate
}

// RenderWelcomeMessageTemplate replaces the {{server_name}} placeholder in template with
// the server's currently effective display name. It is a literal, single-placeholder replacement — no template engine,
// because this is the only replacement needed.
func RenderWelcomeMessageTemplate(template string) string {
	return strings.ReplaceAll(template, "{{server_name}}", officialSystemDisplayName())
}
