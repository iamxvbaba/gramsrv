package domain

import "time"

// Authorization is one device authorization: the binding of auth_key to user plus initConnection device info.
// auth_key is a protocol artifact and the authorization a business artifact, so it is independent of store.AuthKeyData.
type Authorization struct {
	AuthKeyID [8]byte // native protocol auth_key_id; converted to int64 as little-endian at the store boundary
	UserID    int64
	Hash      int64
	// Layer is the last supported protocol profile explicitly observed for this
	// auth key. It is a durable default for a new session, never an instruction
	// to rewrite an already-active session's own profile.
	Layer         int
	DeviceModel   string
	Platform      string
	SystemVersion string
	APIID         int
	AppVersion    string
	IP            string
	// PasswordPending means this auth_key has passed the SMS code but the account has two-step verification enabled and has not yet passed
	// auth.checkPassword. In this state business authorization must treat it as not logged in, allowing only completion of two-step verification.
	PasswordPending bool
	// CreatedAt is the start of the current fully-authorized login session. Bind
	// refreshes it for every login, and completing password_pending refreshes it
	// again so time spent waiting for 2FA never satisfies payout freshness.
	CreatedAt time.Time
	ActiveAt  time.Time
}

// AuthKeyClientInfo is the client negotiation metadata that must be kept even for a not-yet-logged-in auth_key.
// A device authorization after login is still expressed by Authorization. Layer stores the last supported explicit
// wire profile, used after a server restart to seed defaults for a new session on the same auth key; an active
// session still follows its own explicit invokeWithLayer correction.
type AuthKeyClientInfo struct {
	Layer int
	// LayerObservationID is a read-only ordering token owned by the protocol
	// store. Generic client metadata updates must never manufacture or advance
	// it; only ordered invokeWithLayer evidence may do so.
	LayerObservationID int64
	DeviceModel        string
	Platform           string
	SystemVersion      string
	APIID              int
	AppVersion         string
	// IP is the client peer address of the most recent session setup (host-only). It is only merged
	// and refreshed at the metadata level, never treated as identity evidence for login/binding, and never touches created_at.
	IP string
}
