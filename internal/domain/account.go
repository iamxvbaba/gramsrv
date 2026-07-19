package domain

import (
	"errors"
	"strings"
)

var (
	ErrPasswordHashInvalid    = errors.New("password hash invalid")
	ErrSRPIDInvalid           = errors.New("srp id invalid")
	ErrSRPPasswordChanged     = errors.New("srp password changed")
	ErrNewSettingsInvalid     = errors.New("new password settings invalid")
	ErrNewSaltInvalid         = errors.New("new password salt invalid")
	ErrPasswordRecoveryNA     = errors.New("password recovery not available")
	ErrEmailCodeInvalid       = errors.New("email code invalid")
	ErrEmailInvalid           = errors.New("email invalid")
	ErrEmailNotAllowed        = errors.New("email not allowed")
	ErrEmailOccupied          = errors.New("email occupied")
	ErrSessionPasswordNeeded  = errors.New("session password needed")
	ErrPhoneNumberInvalid     = errors.New("phone number invalid")
	ErrPhoneNumberOccupied    = errors.New("phone number occupied")
	ErrPhoneCodeEmpty         = errors.New("phone code empty")
	ErrPhoneCodeInvalid       = errors.New("phone code invalid")
	ErrPhoneCodeExpired       = errors.New("phone code expired")
	ErrPhoneChangeAuthInvalid = errors.New("phone change auth invalid")
	ErrPhoneChangeForbidden   = errors.New("phone change forbidden")
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

	// Server-only SRP fields. They are persisted but never exposed to rpc/tg conversion.
	SRPVerifier []byte
	SRPBSecret  []byte

	RecoveryCode          string
	RecoveryCodeExpiresAt int64
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

// GlobalPrivacy 是 globalPrivacySettings 的业务层表达（账号级隐私开关）。
// DisallowedGifts 依赖礼物资产模型（当前未实现），故不建模、保持默认。
type GlobalPrivacy struct {
	ArchiveAndMuteNewNoncontactPeers bool
	KeepArchivedUnmuted              bool
	KeepArchivedFolders              bool
	HideReadMarks                    bool
	NewNoncontactPeersRequirePremium bool
	DisplayGiftsButton               bool
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

// NormalizePhone 仅保留手机号中的数字（与 users.phone 的存储形态一致）。全部被过滤
// 掉时返回原串，便于上层做 validPhone 拒绝。auth/account 两域共用同一规则避免漂移。
func NormalizePhone(phone string) string {
	var b strings.Builder
	b.Grow(len(phone))
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return phone
	}
	return b.String()
}

// ValidPhone 校验 NormalizePhone 后的持久化形态：5-32 位纯数字。
// 上限与 users.phone 列宽一致；当前开发登录/改号链路不强制精确 E.164 长度，
// 但拒绝空串、非数字和会截断的超长输入。
func ValidPhone(phone string) bool {
	if len(phone) < 5 || len(phone) > 32 {
		return false
	}
	for _, r := range phone {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
