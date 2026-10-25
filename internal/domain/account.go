package domain

import (
	"errors"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/nyaruka/phonenumbers"
)

const (
	// MinLoginPhoneDigits is the shortest accepted login identity in the
	// lenient format policy. It keeps short international numbers such as
	// "+7 123" usable while staying above the empty and single-digit range.
	MinLoginPhoneDigits = 4
	// MaxLoginPhoneDigits bounds a login identity. It matches the users.phone
	// column width and stays inside E.164's 15-digit ceiling for every
	// number the numbering plan can canonicalize.
	MaxLoginPhoneDigits = 32
)

// phoneFormatCheck selects the strict phone-number format policy. The default
// is the lenient policy; ConfigurePhoneFormatCheck installs the deployment's
// choice before any store or RPC service is constructed.
var phoneFormatCheck atomic.Bool

// ConfigurePhoneFormatCheck installs the deployment's phone-number format
// policy. Strict mode requires the number to sit in an assigned range of a
// real country calling code; the default lenient mode also accepts nonzero-leading
// digit identities outside the numbering plan, with or without a leading '+'.
func ConfigurePhoneFormatCheck(strict bool) {
	phoneFormatCheck.Store(strict)
}

// PhoneFormatCheckEnabled reports whether the strict phone-number format
// policy is installed.
func PhoneFormatCheckEnabled() bool {
	return phoneFormatCheck.Load()
}

var (
	ErrPasswordHashInvalid     = errors.New("password hash invalid")
	ErrSRPIDInvalid            = errors.New("srp id invalid")
	ErrSRPPasswordChanged      = errors.New("srp password changed")
	ErrTmpPasswordInvalid      = errors.New("tmp password invalid")
	ErrTmpPasswordPeriod       = errors.New("tmp password period invalid")
	ErrPasswordMissing         = errors.New("password missing")
	ErrNewSettingsInvalid      = errors.New("new password settings invalid")
	ErrNewSaltInvalid          = errors.New("new password salt invalid")
	ErrPasswordRecoveryNA      = errors.New("password recovery not available")
	ErrRecoveryCodeEmpty       = errors.New("recovery code empty")
	ErrRecoveryCodeInvalid     = errors.New("recovery code invalid")
	ErrPasswordRecoveryExpired = errors.New("password recovery expired")
	ErrEmailCodeInvalid        = errors.New("email code invalid")
	ErrEmailInvalid            = errors.New("email invalid")
	ErrEmailNotAllowed         = errors.New("email not allowed")
	ErrEmailOccupied           = errors.New("email occupied")
	ErrSessionPasswordNeeded   = errors.New("session password needed")
	ErrPhoneNumberInvalid      = errors.New("phone number invalid")
	ErrPhoneNumberOccupied     = errors.New("phone number occupied")
	ErrPhoneCodeEmpty          = errors.New("phone code empty")
	ErrPhoneCodeInvalid        = errors.New("phone code invalid")
	ErrPhoneCodeExpired        = errors.New("phone code expired")
	ErrPhoneChangeAuthInvalid  = errors.New("phone change auth invalid")
	ErrPhoneChangeForbidden    = errors.New("phone change forbidden")
)

type AuthCodeDeliveryKind string

const (
	AuthCodeDeliveryPhone              AuthCodeDeliveryKind = "phone"
	AuthCodeDeliverySMS                AuthCodeDeliveryKind = "sms"
	AuthCodeDeliveryEmail              AuthCodeDeliveryKind = "email"
	AuthCodeDeliveryEmailSetupRequired AuthCodeDeliveryKind = "email_setup_required"
)

type AuthCodeDelivery struct {
	Kind         AuthCodeDeliveryKind
	EmailPattern string
	Length       int
}

// PasswordKDFAlgo 是业务层的 SRP KDF 算法描述，不依赖 tg.*。
type PasswordKDFAlgo struct {
	Salt1 []byte
	Salt2 []byte
	G     int
	P     []byte
}

// SecurePasswordKDFAlgo 是 Telegram Passport secure secret 的 KDF 算法描述。
type SecurePasswordKDFAlgo struct {
	Kind string
	Salt []byte
}

// PasswordCheck 是 inputCheckPasswordEmpty/inputCheckPasswordSRP 的业务层表达。
type PasswordCheck struct {
	Empty bool
	SRPID int64
	A     []byte
	M1    []byte
}

// PasswordInputSettings 是 account.passwordInputSettings 的业务层表达。
type PasswordInputSettings struct {
	NewAlgo         *PasswordKDFAlgo
	NewPasswordHash []byte
	Hint            string
	HasHint         bool
	Email           string
	HasEmail        bool
}

// PrivatePasswordSettings 是 account.passwordSettings 的业务层表达。
type PrivatePasswordSettings struct {
	Email string
}

type PasswordResetKind string

const (
	PasswordResetOK            PasswordResetKind = "ok"
	PasswordResetRequestedWait PasswordResetKind = "requested_wait"
	PasswordResetFailedWait    PasswordResetKind = "failed_wait"
)

type PasswordResetResult struct {
	Kind      PasswordResetKind
	UntilDate int
	RetryDate int
}

// PasswordSettings 是账号 2FA/SRP 配置。默认 HasPassword=false。
type PasswordSettings struct {
	HasRecovery             bool
	HasSecureValues         bool
	HasPassword             bool
	CurrentAlgo             *PasswordKDFAlgo
	SRPB                    []byte
	SRPID                   int64
	Hint                    string
	EmailUnconfirmedPattern string
	RecoveryEmail           string
	// LoginEmail 是已确认的登录邮箱地址（服务端私有，永不直接下发；下发的是掩码后的
	// LoginEmailPattern）。它独立于 2FA 恢复邮箱 RecoveryEmail：账号可只设登录邮箱而无 2FA。
	LoginEmail        string
	LoginEmailPattern string
	NewAlgo           PasswordKDFAlgo
	NewSecureAlgo     SecurePasswordKDFAlgo
	SecureRandom      []byte
	PendingResetDate  int
	PendingChange     *PendingPasswordChange

	// Server-only SRP fields. They are persisted but never exposed to rpc/tg conversion.
	SRPVerifier []byte
	SRPBSecret  []byte
}

// PendingPasswordChange is server-only. Neither proofs nor delivery metadata are projected to clients.
type PendingPasswordChange struct {
	DeliveryID  string
	Email       string
	Input       PasswordInputSettings
	BaseBinding string
	CodeHash    []byte
	ExpiresAt   time.Time
	Attempts    int
	MaxAttempts int
	CodeLength  int
}

type EmailUnconfirmedError struct{ CodeLength int }

func (e *EmailUnconfirmedError) Error() string { return "recovery email confirmation required" }

// RevenueWithdrawalPasswordState carries only the durable 2FA facts required
// by high-risk payout admission. It intentionally excludes password material.
type RevenueWithdrawalPasswordState struct {
	HasPassword       bool
	PasswordChangedAt time.Time
}

// account.getTmpPassword period bounds in seconds, as TDLib's
// createTemporaryPassword documents them.
const (
	TmpPasswordMinPeriod = 60
	TmpPasswordMaxPeriod = 86400
)

// TmpPasswordGrant is the server record of one account.getTmpPassword
// issuance. It keeps only the SHA-256 of the bearer secret. The grant is valid
// for its own authorization only while AuthorizationCreatedAt and
// PasswordChangedAt still match the live authorization and 2FA password.
type TmpPasswordGrant struct {
	AuthKeyID              [8]byte
	UserID                 int64
	AuthorizationCreatedAt time.Time
	PasswordChangedAt      time.Time
	SecretHash             [32]byte
	ValidUntil             time.Time
}

// TmpPassword is an issued bearer secret; it is returned to the client once.
type TmpPassword struct {
	Secret     []byte
	ValidUntil time.Time
}

// ReactionNotifyFrom stores one account-level reaction notification scope.
type ReactionNotifyFrom string

const (
	ReactionNotifyFromNone     ReactionNotifyFrom = "none"
	ReactionNotifyFromContacts ReactionNotifyFrom = "contacts"
	ReactionNotifyFromAll      ReactionNotifyFrom = "all"
)

// ReactionsNotifySettings stores the account reaction notification settings
// consumed by account.get/setReactionsNotifySettings.
type ReactionsNotifySettings struct {
	Sound         NotificationSound
	MessagesFrom  ReactionNotifyFrom
	StoriesFrom   ReactionNotifyFrom
	PollVotesFrom ReactionNotifyFrom
	ShowPreviews  bool
}

// PaidReactionPrivacyKind stores the account default paid reaction privacy.
type PaidReactionPrivacyKind string

const (
	PaidReactionPrivacyDefault   PaidReactionPrivacyKind = "default"
	PaidReactionPrivacyAnonymous PaidReactionPrivacyKind = "anonymous"
	PaidReactionPrivacyPeer      PaidReactionPrivacyKind = "peer"
)

// PaidReactionPrivacy is the domain representation of tg.PaidReactionPrivacy.
type PaidReactionPrivacy struct {
	Kind PaidReactionPrivacyKind
	Peer *Peer
}

// AccountReactionSettings groups account-level reaction preferences.
type AccountReactionSettings struct {
	Notify          ReactionsNotifySettings
	DefaultReaction MessageReaction
	PaidPrivacy     PaidReactionPrivacy
}

func DefaultAccountReactionSettings() AccountReactionSettings {
	return AccountReactionSettings{
		Notify: ReactionsNotifySettings{
			MessagesFrom:  ReactionNotifyFromContacts,
			StoriesFrom:   ReactionNotifyFromContacts,
			PollVotesFrom: ReactionNotifyFromContacts,
			ShowPreviews:  true,
		},
		DefaultReaction: MessageReaction{Type: MessageReactionEmoji, Emoticon: "👍"},
		PaidPrivacy:     PaidReactionPrivacy{Kind: PaidReactionPrivacyDefault},
	}
}

// DefaultAccountTTLDays 是账号自毁默认期限（无显式设置时）。与历史固定回显一致。
const (
	DefaultAccountTTLDays = 365
	// MaxAccountTTLDays prevents an untrusted int32 TL value from producing an
	// out-of-range PostgreSQL interval/timestamp during deadline maintenance.
	MaxAccountTTLDays = 3650
)

// DisallowedGifts stores the Layer 228 global gift-reception switches.
type DisallowedGifts struct {
	UnlimitedStargifts   bool
	LimitedStargifts     bool
	UniqueStargifts      bool
	PremiumGifts         bool
	StargiftsFromChannel bool
}

func (g DisallowedGifts) Zero() bool {
	return !g.UnlimitedStargifts && !g.LimitedStargifts && !g.UniqueStargifts &&
		!g.PremiumGifts && !g.StargiftsFromChannel
}

// GlobalPrivacy 是 globalPrivacySettings 的业务层表达（账号级隐私开关）。
type GlobalPrivacy struct {
	ArchiveAndMuteNewNoncontactPeers bool
	KeepArchivedUnmuted              bool
	KeepArchivedFolders              bool
	HideReadMarks                    bool
	NewNoncontactPeersRequirePremium bool
	DisplayGiftsButton               bool
	DisallowedGifts                  DisallowedGifts
	// NoncontactPeersPaidStars：非联系人给本人发消息所需 Stars 数。Stars 账本尚未实现，
	// 此处仅做忠实持久化（往返不丢值），不参与计费逻辑。
	NoncontactPeersPaidStars int64
}

// AccountSettings 聚合账号级单例设置（每用户一行）：全局隐私、账号自毁期限、
// 敏感内容开关、联系人注册通知静音。对应 account.get/set GlobalPrivacySettings、
// get/set AccountTTL、get/set ContentSettings、get/set ContactSignUpNotification。
type AccountSettings struct {
	GlobalPrivacy           GlobalPrivacy
	AccountTTLDays          int
	SensitiveContentEnabled bool
	// ContactSignUpSilent 对应 account.setContactSignUpNotification 的 silent 形参：
	// true=联系人注册时不通知本人。getContactSignUpNotification 直接返回该值。
	ContactSignUpSilent bool
}

// DefaultAccountSettings 是未持久化时的账号设置默认值（与历史回显 stub 行为一致：
// 全局隐私全关、TTL 365 天、敏感内容关、联系人注册通知开启）。
func DefaultAccountSettings() AccountSettings {
	return AccountSettings{
		AccountTTLDays: DefaultAccountTTLDays,
	}
}

// NormalizedTTLDays 返回钳制后的账号自毁期限（0/越界回落默认）。
func (s AccountSettings) NormalizedTTLDays() int {
	if s.AccountTTLDays <= 0 || s.AccountTTLDays > MaxAccountTTLDays {
		return DefaultAccountTTLDays
	}
	return s.AccountTTLDays
}

// MaskEmail 把邮箱地址按 Telegram pattern 习惯掩码（首尾各保留一位本地名，如
// a***z@x.com），用于 account.password.login_email_pattern / auth.sentCodeTypeEmailCode
// 等只能暴露掩码的下发点。空串返回空串。
func MaskEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.Index(email, "@")
	if at <= 1 {
		return email
	}
	name := email[:at]
	return name[:1] + "***" + name[len(name)-1:] + email[at:]
}

// PhoneDigits removes presentation punctuation from a phone number. It is
// intentionally not an identity canonicalizer: callers that select accounts,
// issue codes, or persist users must use NormalizePhone and ValidPhone.
func PhoneDigits(phone string) string {
	var b strings.Builder
	b.Grow(len(phone))
	seenDigit := false
	seenPlus := false
	for _, r := range phone {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			seenDigit = true
		case r == '+':
			if seenPlus || seenDigit {
				return ""
			}
			seenPlus = true
		case unicode.IsSpace(r), r == '-', r == '(', r == ')', r == '.', r == '/':
			// Presentation separators accepted by official clients and contact UIs.
		default:
			return ""
		}
	}
	return b.String()
}

// NormalizePhone returns the one persisted login identity. Virtual +888
// identities are independent of the collectible-phone registry and accept
// 7-15 canonical digits. Ordinary international numbers use E.164 digits
// without the leading '+'. Their parsing is deliberately country-aware so a
// national trunk prefix is removed only where the numbering plan says it is a
// prefix. For example, both +98 0998 167 9461 and +98 998 167 9461 become
// 989981679461, while Italy's significant leading zero in +39 02 ... is retained.
//
// Two format policies exist:
//
//   - The default (lenient) policy keeps IsPossibleNumber as the structural
//     gate: a country calling code plus a length that matches that country's
//     numbering plan is enough. A number the plan cannot describe is still
//     accepted when it is short enough and carries no national trunk prefix,
//     so ranges such as "+7 123" or "+1488" remain usable login identities.
//     The digits are then stored verbatim.
//   - The strict policy (ConfigurePhoneFormatCheck) additionally requires
//     IsValidNumber, i.e. the number must sit in an assigned range of a real
//     country calling code. Short and unassigned ranges are rejected.
//
// Neither policy accepts a national number that omits its country calling
// code: "09981679461" and "00989981679461" carry a leading trunk zero and
// are refused in both modes.
func NormalizePhone(phone string) string {
	digits := PhoneDigits(phone)
	if digits == "" {
		return ""
	}
	// Every syntactically valid +888 virtual number is an independent login
	// identity; minting or owning the same collectible-phone value is not a
	// prerequisite. users.phone therefore takes lookup precedence over the
	// optional collectible alias registry.
	if len(digits) >= MinCollectiblePhoneLength &&
		len(digits) <= MaxCollectiblePhoneLength &&
		strings.HasPrefix(digits, "888") {
		return digits
	}
	// 42777 is the reserved, non-login phone of the built-in service identity.
	// It predates the ordinary E.164 user invariant and remains resolvable only
	// so auth can reject it as a system account instead of treating it as free.
	if digits == OfficialSystemPhone {
		return digits
	}
	number, err := phonenumbers.Parse("+"+digits, phonenumbers.UNKNOWN_REGION)
	if err == nil {
		if phoneFormatCheck.Load() {
			if !phonenumbers.IsValidNumber(number) {
				return ""
			}
			canonical := strings.TrimPrefix(phonenumbers.Format(number, phonenumbers.E164), "+")
			if canonical == "" || len(canonical) > MaxLoginPhoneDigits {
				return ""
			}
			return canonical
		}
		if phonenumbers.IsPossibleNumber(number) {
			canonical := strings.TrimPrefix(phonenumbers.Format(number, phonenumbers.E164), "+")
			if canonical == "" || len(canonical) > MaxLoginPhoneDigits {
				return ""
			}
			return canonical
		}
		// The numbering plan cannot describe these digits. The lenient policy
		// still keeps an explicitly international number usable.
	} else if phoneFormatCheck.Load() {
		return ""
	}
	// The lenient policy accepts a number the plan cannot describe. The digits
	// are stored verbatim, so the identity stays exactly what the caller
	// typed and stays canonical on the next normalization pass.
	if phoneFormatCheck.Load() {
		return ""
	}
	if len(digits) < MinLoginPhoneDigits || len(digits) > MaxLoginPhoneDigits {
		return ""
	}
	// A leading zero marks the national/trunk form of a number whose country
	// calling code was omitted ("07400123456", "00989981679461"). Such a
	// value is ambiguous between a national number and a calling code, so it
	// never becomes a login identity.
	if strings.HasPrefix(digits, "0") {
		return ""
	}
	// The +888 space has its own identity rule above: a number outside the
	// 7-15 digit collectible range is not a login identity at all.
	if strings.HasPrefix(digits, "888") {
		return ""
	}
	return digits
}

// ValidPhone reports whether phone is already in the persisted canonical form.
// Callers accepting user input normalize first, then validate, so equivalent
// international spellings converge before lookup, rate limiting, OTP delivery,
// and uniqueness checks.
func ValidPhone(phone string) bool {
	canonical := NormalizePhone(phone)
	return canonical != "" && canonical == phone
}
