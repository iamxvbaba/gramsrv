package domain

import "errors"

// Passkey-related errors.
var (
	ErrPasskeyChallengeInvalid  = errors.New("passkey challenge invalid")
	ErrPasskeyNotFound          = errors.New("passkey not found")
	ErrPasskeyInvalid           = errors.New("passkey invalid")
	ErrPasskeyUserHandleInvalid = errors.New("passkey user handle invalid")
)

// PasskeyChallengePurpose distinguishes a registration challenge from a login challenge.
type PasskeyChallengePurpose string

const (
	PasskeyChallengeLogin    PasskeyChallengePurpose = "login"
	PasskeyChallengeRegister PasskeyChallengePurpose = "register"
)

// PasskeyChallenge is the metadata of a one-time WebAuthn challenge (short TTL, burn after use).
// A registration challenge is bound to the initiating user; a login (discoverable) challenge has UserID=0 and the user is looked up from user_handle.
type PasskeyChallenge struct {
	Purpose   PasskeyChallengePurpose
	UserID    int64
	ExpiresAt int64 // unix seconds
}

// PasskeyCredential is a registered passkey credential (WebAuthn public key).
type PasskeyCredential struct {
	CredentialID []byte // raw credential id bytes (exposed as base64url)
	UserID       int64
	PublicKey    []byte // raw COSE public key bytes
	SignCount    uint32
	AAGUID       []byte
	Name         string
	Transports   []string
	CreatedAt    int64 // unix seconds
	LastUsedAt   int64 // unix seconds; 0 means never used to log in
}

// Clone deep-copies the slice fields so the in-memory store never exposes internal references.
func (c PasskeyCredential) Clone() PasskeyCredential {
	out := c
	out.CredentialID = append([]byte(nil), c.CredentialID...)
	out.PublicKey = append([]byte(nil), c.PublicKey...)
	out.AAGUID = append([]byte(nil), c.AAGUID...)
	out.Transports = append([]string(nil), c.Transports...)
	return out
}
