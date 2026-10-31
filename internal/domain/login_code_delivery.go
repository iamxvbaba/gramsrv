package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// loginCodeTemplateCodePlaceholder marks the position in a login-code message template that is replaced with the real verification code.
// The template body is no longer the old hard-coded "Login code: %s", so the bold MessageEntity
// offset/length of the code must be computed dynamically from the placeholder's actual position (see OfficialLoginCodeMessage).
// Any template entering OfficialLoginCodeMessage must contain this placeholder exactly once
// (see ValidateLoginCodeMessageTemplate): zero occurrences silently lose the code, and two or more
// make it impossible to tell which one is the "real verification code".
const loginCodeTemplateCodePlaceholder = "{{code}}"

// DefaultLoginCodeMessageTemplate is the built-in final fallback text for the 777000 login-code delivery message.
// All login codes (SMS/email) share the same template, which does not vary by delivery channel. It supports the {{server_name}}
// placeholder (see RenderWelcomeMessageTemplate) and must contain {{code}} exactly once
// (see ValidateLoginCodeMessageTemplate).
const DefaultLoginCodeMessageTemplate = `Login code: {{code}}. Do not give this code to anyone, even if they say they are from {{server_name}}!

This code can be used to log in to your {{server_name}} account. We never ask it for anything else.

If you didn't request this code by trying to log in on another device, simply ignore this message.`

// ErrLoginCodeMessageTemplateMissingCode means a candidate login-code template does not contain
// the {{code}} placeholder exactly once. The admin API layer must reject such a save outright rather than accept it silently:
// zero occurrences mean the real verification code would never be delivered to the user.
var ErrLoginCodeMessageTemplateMissingCode = errors.New("login code message template must contain the {{code}} placeholder exactly once")

// ValidateLoginCodeMessageTemplate requires the {{code}} placeholder to appear exactly once.
// Zero is functionally broken (the code itself never reaches the user), and two or more is ambiguous
// (which one is replaced and bolded?) — both are rejected outright, never silently patched.
func ValidateLoginCodeMessageTemplate(template string) error {
	if strings.Count(template, loginCodeTemplateCodePlaceholder) != 1 {
		return ErrLoginCodeMessageTemplateMissingCode
	}
	return nil
}

// ResolveLoginCodeMessageTemplate picks the final template body by priority: an explicit admin-panel override
// (panelOverride, stored verbatim in identity.Info, empty means unset) → an explicit environment-variable default
// (envDefault, empty means unset) → the built-in DefaultLoginCodeMessageTemplate. A pure function,
// for dependency-free testing of its priority without depending on the identity store / config. The caller (internal/app/auth)
// resolves it live before each delivery (never cached), so admin-panel edits take effect immediately — consistent with
// ResolveWelcomeMessageTemplate's semantics. The login-code template does not branch by channel.
//
// This function itself does not validate the {{code}} placeholder — the caller that persists an override (the admin API) must
// call ValidateLoginCodeMessageTemplate before saving. OfficialLoginCodeMessage validates the
// resolved result again, as defense in depth against illegal values arriving by other routes (hand-edited identity.json,
// out-of-range environment variables).
func ResolveLoginCodeMessageTemplate(panelOverride, envDefault string) string {
	if t := strings.TrimSpace(panelOverride); t != "" {
		return panelOverride
	}
	if t := strings.TrimSpace(envDefault); t != "" {
		return envDefault
	}
	return DefaultLoginCodeMessageTemplate
}

// LoginCodeDeliveryRequest describes one durable 777000 login-code delivery.
// PhoneCodeHash is an opaque idempotency token and must never be persisted in
// plaintext; store implementations persist only its SHA-256 digest.
type LoginCodeDeliveryRequest struct {
	UserID        int64
	PhoneCodeHash string
	Code          string
	// Template is the already-resolved login-code message template (see
	// ResolveLoginCodeMessageTemplate) -- resolving it requires the identity
	// store and config, both of which live above internal/store, so callers
	// (internal/app/auth) do that and pass the final template text in here,
	// the same division of responsibility OfficialWelcomeMessage's body
	// parameter uses. Empty falls back to DefaultLoginCodeMessageTemplate
	// (see OfficialLoginCodeMessage).
	Template string
	Date     int
	// ExpiresAt is the unix second after which the compact idempotency receipt
	// may be reclaimed. It must cover the corresponding code's usable lifetime.
	ExpiresAt int64
}

// LoginCodeDeliveryResult returns the immutable first delivery. Created is
// false when the same phone_code_hash was already committed and replayed.
type LoginCodeDeliveryResult struct {
	Message Message
	Created bool
}

// OfficialLoginCodeMessage builds the account-visible incoming message from
// Telegram's official notification account. Persistence assigns ID, UID and
// Pts atomically.
//
// The template is rendered first ({{server_name}} replaced, then {{code}} replaced with the real code),
// and the bold MessageEntity is located dynamically from the actual position of {{code}} after replacement — never assuming a fixed prefix,
// because the template can be edited by an admin (see ValidateLoginCodeMessageTemplate). When the template is empty or
// fails validation it falls back to DefaultLoginCodeMessageTemplate and never delivers a message with no code.
func OfficialLoginCodeMessage(userID int64, template, code string, date int) (Message, error) {
	return OfficialLoginCodeMessageWithTemplate(userID, template, code, date)
}

// SnapshotLoginCodeMessageTemplate resolves the brand and fallback at the first
// delivery. The returned template still contains {{code}}, so a compact
// receipt can reproduce the original body without storing the secret code.
func SnapshotLoginCodeMessageTemplate(template string) string {
	if strings.TrimSpace(template) == "" || ValidateLoginCodeMessageTemplate(template) != nil {
		template = DefaultLoginCodeMessageTemplate
	}
	rendered := RenderWelcomeMessageTemplate(template)
	if ValidateLoginCodeMessageTemplate(rendered) != nil {
		rendered = RenderWelcomeMessageTemplate(DefaultLoginCodeMessageTemplate)
		if ValidateLoginCodeMessageTemplate(rendered) != nil {
			return strings.ReplaceAll(DefaultLoginCodeMessageTemplate, "{{server_name}}", "Telesrv")
		}
	}
	return rendered
}

func OfficialLoginCodeMessageWithTemplate(userID int64, template, code string, date int) (Message, error) {
	if userID <= 0 || IsSystemUserID(userID) || strings.TrimSpace(code) == "" || len(code) > 64 || date < 0 || date > math.MaxInt32 {
		return Message{}, fmt.Errorf("%w: user=%d code_length=%d date=%d", ErrLoginCodeDeliveryInvalid, userID, len(code), date)
	}
	rendered := SnapshotLoginCodeMessageTemplate(template)
	idx := strings.Index(rendered, loginCodeTemplateCodePlaceholder)
	if idx < 0 {
		// Actually unreachable: the template was just validated (or is the built-in default) guaranteeing the placeholder exactly once,
		// and the {{server_name}} replacement never removes or moves an unrelated placeholder. Still defensive,
		// never delivering a message that silently lost the code.
		rendered = DefaultLoginCodeMessageTemplate
		idx = strings.Index(rendered, loginCodeTemplateCodePlaceholder)
	}
	body := rendered[:idx] + code + rendered[idx+len(loginCodeTemplateCodePlaceholder):]
	return Message{
		OwnerUserID: userID,
		Peer:        Peer{Type: PeerTypeUser, ID: OfficialSystemUserID},
		From:        Peer{Type: PeerTypeUser, ID: OfficialSystemUserID},
		Date:        date,
		Body:        body,
		Entities: []MessageEntity{
			{Type: MessageEntityBold, Offset: automaticEntityUTF16Length(rendered[:idx]), Length: automaticEntityUTF16Length(code)},
		},
	}, nil
}
