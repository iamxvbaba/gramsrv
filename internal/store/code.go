package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	PhoneCodePurposeChangePhone        = "change_phone"
	PhoneCodePurposeConfirmPhone       = "confirm_phone"
	PhoneCodeChannelPhone              = "phone"
	PhoneCodeChannelSMS                = "sms"
	PhoneCodeChannelEmailLogin         = "email_login"
	PhoneCodeChannelEmailSetupRequired = "email_setup_required"
)

// LoginCodeChannelVerifiable reports whether a code record belongs to the
// auth.signIn/auth.signUp state machine. The TL field carrying the proof
// (phone_code or email_verification) is deliberately not part of this
// decision: official clients use both fields for an email-delivered login
// code, while the record channel remains the server-side delivery fact.
func LoginCodeChannelVerifiable(channel string) bool {
	switch channel {
	case PhoneCodeChannelPhone, PhoneCodeChannelSMS, PhoneCodeChannelEmailLogin:
		return true
	default:
		return false
	}
}

// LoginCodeChannelTakeable includes the setup-required placeholder because
// cancel/resend may replace it, although it is never valid auth.signIn proof.
func LoginCodeChannelTakeable(channel string) bool {
	return LoginCodeChannelVerifiable(channel) || channel == PhoneCodeChannelEmailSetupRequired
}

// PhoneCodeVersionCurrent is the only version accepted by the atomic login
// state machine. Version 2 binds every scope to an E.164 canonical identity;
// version 1 records are invalidated across rollout instead of being normalized
// on read and accidentally authorizing a different phone owner.
const PhoneCodeVersionCurrent = 2

// PhoneCode is one verification code record (associated with a sendCode phone_code_hash
// or an email verification key). Purpose/UserID/AuthKeyID/SessionID scope logged-in
// sensitive operations; login codes keep zero values.
type PhoneCode struct {
	Version int
	// Revision is an opaque store-managed CAS token. Callers must pass it back
	// through PhoneCodeSnapshot and must never synthesize or persist it outside
	// CodeStore.
	Revision string
	// IssuedUserID is encoded as a JSON string so Redis Lua can round-trip the
	// full int64 range without cjson's IEEE-754 number precision loss.
	IssuedUserID   int64 `json:",string"`
	SignUpVerified bool
	Phone          string
	Code           string
	// DeliveryID is the stable, opaque idempotency key used for the outbound
	// provider call that carries this code. It contains no recipient or secret
	// material and is rotated whenever a genuinely new code is issued.
	DeliveryID string
	Channel    string
	Purpose    string
	// UserID is also encoded as a string because scoped verification mutates the
	// record in Redis Lua and must not round an int64 owner through cjson.
	UserID    int64 `json:",string"`
	AuthKeyID [8]byte
	// SessionID is audit metadata but still crosses Redis Lua on wrong attempts;
	// encode it as a string to preserve MTProto's full signed 64-bit value.
	SessionID      int64 `json:",string"`
	Email          string
	PendingEmail   string
	Attempts       int
	MaxAttempts    int
	VerifiedEmail  bool
	RequireSignUp  bool
	LoginEmailHash string
	// RecoveryBinding ties a password-recovery code to the exact 2FA state
	// which existed when the code was issued. A password or recovery-email
	// change makes an older code unusable without relying on cross-store
	// best-effort invalidation.
	RecoveryBinding string
	// AccountDeletionHash is the hex-encoded SHA-256 digest of the validated
	// confirmphone link token. It binds account.confirmPhone to one pending
	// deletion without persisting the raw link credential in the code record.
	AccountDeletionHash string
}

type PhoneCodeSnapshot struct {
	Record   PhoneCode
	Revision string
}

func NewPhoneCodeRevisionToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate phone code revision: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// LoginCodeVerifyStatus separates an expired/consumed hash from a live hash
// whose scope or code did not match. RPC maps these to PHONE_CODE_EXPIRED and
// PHONE_CODE_INVALID respectively.
type LoginCodeVerifyStatus uint8

const (
	LoginCodeVerifyMissing LoginCodeVerifyStatus = iota
	LoginCodeVerifyInvalid
	LoginCodeVerifyAccepted
)

type LoginCodeVerifyResult struct {
	Status LoginCodeVerifyStatus
	Record PhoneCode
}

// PhoneCodeScope identifies the one-time code scope of a logged-in sensitive operation.
// SessionID is deliberately not part of the scope: an MTProto session may be rebuilt while
// the same perm auth key waits for a code. Login/sign-up codes have no
// Purpose/UserID/AuthKeyID and keep unscoped behavior.
type PhoneCodeScope struct {
	Purpose   string
	UserID    int64
	AuthKeyID [8]byte
	Phone     string
}

func (c PhoneCode) Scope() PhoneCodeScope {
	return PhoneCodeScope{
		Purpose:   c.Purpose,
		UserID:    c.UserID,
		AuthKeyID: c.AuthKeyID,
		Phone:     c.Phone,
	}
}

func (s PhoneCodeScope) Valid() bool {
	return s.Purpose != "" && s.UserID != 0 && s.AuthKeyID != ([8]byte{}) && s.Phone != ""
}

// CodeStore temporarily stores verification codes: phone_code_hash -> scope + phone number
// + code, with a TTL. See store/memory (test double) and store/redisstore for implementations.
type CodeStore interface {
	// Set must atomically replace the old hash of the same scope for scoped codes, ensuring
	// at most one active code per scope; plain login codes are still stored independently by hash.
	Set(ctx context.Context, phoneCodeHash string, code PhoneCode, ttl time.Duration) error
	Get(ctx context.Context, phoneCodeHash string) (PhoneCode, bool, error)
	Del(ctx context.Context, phoneCodeHash string) error
	// ConsumeScoped atomically reads and deletes only while hash is still the scope's active
	// hash; at most one concurrent call returns found=true.
	ConsumeScoped(ctx context.Context, phoneCodeHash string, scope PhoneCodeScope) (PhoneCode, bool, error)
	// VerifyScoped atomically verifies a current-version code only while hash is
	// still the active value for scope. A wrong code increments Attempts and, at
	// the threshold, removes both the code and scope index. A correct code
	// consumes both keys, so concurrent callers can observe Accepted at most once.
	VerifyScoped(ctx context.Context, phoneCodeHash string, scope PhoneCodeScope, code string, defaultMaxAttempts int) (LoginCodeVerifyResult, error)
	// VerifyLogin atomically validates one current-version, unscoped login code.
	// A correct code is consumed unless keepForSignUp is true, in which case the
	// same TTL is retained and SignUpVerified is set. Wrong-code attempts are
	// incremented in the same linearization point and delete the record at the
	// configured threshold.
	VerifyLogin(ctx context.Context, phoneCodeHash, phone, code string, keepForSignUp bool, defaultMaxAttempts int) (LoginCodeVerifyResult, error)
	// ConsumeSignUpVerified atomically consumes a marker created by VerifyLogin.
	// Concurrent sign-up calls can return found=true at most once.
	ConsumeSignUpVerified(ctx context.Context, phoneCodeHash, phone string) (PhoneCode, bool, error)
	// TakeLoginCode atomically removes a current-version, unscoped login record
	// after matching its phone. Cancel/resend use the returned record to decide
	// what successor to issue; concurrent Verify/Take calls have one winner.
	TakeLoginCode(ctx context.Context, phoneCodeHash, phone string) (PhoneCode, bool, error)
	// InvalidateLoginCode is the server-side cleanup primitive for owner drift
	// or a failed post-verification workflow. Unlike TakeLoginCode it may delete
	// a SignUpVerified marker; user-driven cancel/resend must not call it.
	InvalidateLoginCode(ctx context.Context, phoneCodeHash, phone string) (bool, error)
	// GetSnapshot and CompareAnd* provide optimistic concurrency for unscoped
	// fixed-key verification flows (for example login-email setup/change).
	// CompareAndUpdate preserves the current TTL and rotates the opaque revision.
	GetSnapshot(ctx context.Context, phoneCodeHash string) (PhoneCodeSnapshot, bool, error)
	CompareAndUpdate(ctx context.Context, phoneCodeHash, expectedRevision string, next PhoneCode) (bool, error)
	CompareAndDelete(ctx context.Context, phoneCodeHash, expectedRevision string) (bool, error)
}
