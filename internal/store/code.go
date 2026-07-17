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
// state machine. Version zero is the pre-state-machine shape and deliberately
// fails closed instead of being normalized on read.
const PhoneCodeVersionCurrent = 1

// PhoneCode 是一条验证码记录（与某次 sendCode 的 phone_code_hash 或邮箱验证键关联）。
// Purpose/UserID/AuthKeyID/SessionID 为已登录敏感操作提供作用域；登录验证码保持零值。
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

// PhoneCodeScope 标识已登录敏感操作的一次性验证码作用域。SessionID 故意不在
// 作用域内：同一 perm auth key 等待验证码期间允许重建 MTProto session。
// 登录/注册验证码没有 Purpose/UserID/AuthKeyID，保持非 scoped 行为。
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

// CodeStore 暂存验证码：phone_code_hash → 作用域 + 手机号 + 验证码，带 TTL。
// 实现见 store/memory（测试替身）、store/redisstore。
type CodeStore interface {
	// Set 对 scoped code 必须原子替换同作用域旧 hash，保证单作用域至多一个
	// 活跃验证码；普通登录码仍按 hash 独立保存。
	Set(ctx context.Context, phoneCodeHash string, code PhoneCode, ttl time.Duration) error
	Get(ctx context.Context, phoneCodeHash string) (PhoneCode, bool, error)
	Del(ctx context.Context, phoneCodeHash string) error
	// ConsumeScoped 仅当 hash 仍是 scope 的当前活跃 hash 时原子读取并删除；
	// 并发调用至多一个返回 found=true。
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
