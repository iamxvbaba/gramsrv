package domain

import "time"

// Push device token types carried by account.registerDevice/unregisterDevice
// (TL `token_type`). See
// https://core.telegram.org/api/push-updates#subscribing-to-notifications.
//
// The list is not exhaustive: clients add types over time and unknown values are
// stored but never pushed. Only the types this server can actually deliver to
// are named here.
const (
	PushTokenTypeAPNs     = 1
	PushTokenTypeFCM      = 2
	PushTokenTypeMPNS     = 3
	PushTokenTypeUbuntu   = 5
	PushTokenTypeAPNsVoIP = 9
	PushTokenTypeWebPush  = 10
)

// PushToken is a device registered to receive push notifications for a user
// (account.registerDevice). A token is globally identified by (TokenType,
// Token) and belongs to exactly one user; re-registering the same token for
// another account reassigns it. Secret is the optional per-token encryption key
// used to seal the push payload (FCM and APNs VoIP).
type PushToken struct {
	// UserID is the account the device is currently bound to.
	UserID int64
	// TokenType is the TL token_type discriminator.
	TokenType int
	// Token is the opaque provider token (FCM registration token, APNs device
	// token, ...).
	Token string
	// Secret is the optional client-provided push encryption key. Empty when the
	// client did not send one.
	Secret []byte
	// AppSandbox marks a sandbox-certificate token (APNs/FCM test builds).
	AppSandbox bool
	// NoMuted asks the server to skip silent/background notifications.
	NoMuted bool
	// OtherUIDs lists other accounts used by the same client (informational only;
	// it never creates additional token rows).
	OtherUIDs []int64
	// UpdatedAt is the last registration time, set by the caller.
	UpdatedAt time.Time
}
