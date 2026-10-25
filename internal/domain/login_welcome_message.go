package domain

import (
	"fmt"
	"math"
	"strings"
)

// OfficialWelcomeMessage builds the account-visible incoming message sent
// from the official system account on every completed sign-in (SignUp and
// every subsequent SignIn/SignInWithEmail), regardless of delivery channel.
// Unlike OfficialLoginCodeMessage this never embeds a secret, so it is safe
// to send unconditionally — it exists to give the account owner (and, on a
// self-hosted single-admin server, that's usually also "the admin") a
// visible record of every session start.
//
// body is the already-resolved, already-{{server_name}}-substituted message
// text (see ResolveWelcomeMessageTemplate / RenderWelcomeMessageTemplate in
// login_welcome_template.go) -- resolving it requires the identity store and
// config, both of which live above this package, so callers (internal/app/auth)
// do that and pass the final text in here.
func OfficialWelcomeMessage(userID int64, body string, date int) (Message, error) {
	body = strings.TrimSpace(body)
	if userID <= 0 || IsSystemUserID(userID) || body == "" || date < 0 || date > math.MaxInt32 {
		return Message{}, fmt.Errorf("%w: user=%d date=%d", ErrLoginCodeDeliveryInvalid, userID, date)
	}
	return Message{
		OwnerUserID: userID,
		Peer:        Peer{Type: PeerTypeUser, ID: OfficialSystemUserID},
		From:        Peer{Type: PeerTypeUser, ID: OfficialSystemUserID},
		Date:        date,
		Body:        body,
	}, nil
}

// SignInMethodLabel returns the human-readable method name embedded in
// OfficialWelcomeMessage. Callers pass the account's confirmed login email
// (PasswordSettings.LoginEmail): a non-empty value means the account was
// created by email, otherwise the sign-in used its phone number — which, once
// assigned, is an ordinary-looking short number that carries no information
// about the signup method.
func SignInMethodLabel(loginEmail string) string {
	if strings.TrimSpace(loginEmail) != "" {
		return "email"
	}
	return "phone number"
}
