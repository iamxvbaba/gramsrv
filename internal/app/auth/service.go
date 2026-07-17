package auth

import (
	"context"
	"crypto/aes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/ige"
	"github.com/iamxvbaba/td/bin"
	mtcrypto "github.com/iamxvbaba/td/crypto"

	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store"
)

// 登录错误。
var (
	ErrCodeExpired             = errors.New("phone code expired or not found")
	ErrCodeInvalid             = errors.New("phone code invalid")
	ErrEncryptedMessageInvalid = errors.New("encrypted message invalid")
	ErrExpiresAtInvalid        = errors.New("temporary auth key request expiry invalid")
	ErrTempAuthKeyEmpty        = errors.New("temporary auth key missing or expired")
	ErrTempAuthKeyAlreadyBound = errors.New("temporary auth key already bound")
	ErrAuthKeyPermEmpty        = errors.New("permanent auth key required")
	// ErrLoginCodeDeliveryUnavailable 表示已有账号的 app-code 没有可用的
	// durable message/event/outbox 投递边界。这是服务端配置错误，不能降级成
	// “继续返回 sentCode，等 signIn 后补发”。
	ErrLoginCodeDeliveryUnavailable = errors.New("login code durable delivery unavailable")
	// ErrLoginCodeDeliveryFailed 表示 durable 投递未成功。SendCode/ResendCode
	// 必须同时撤销刚写入的 CodeStore hash，防止客户拿到无法送达的码。
	ErrLoginCodeDeliveryFailed = errors.New("login code durable delivery failed")
	// ErrPhoneNumberInvalid 表示手机号为空或非纯数字/长度越界。
	// 0090 把 users.phone 唯一约束改为忽略空串的部分索引（bot 行 phone=''），
	// 因此 phone 校验必须前移到 auth 入口，否则 sendCode/signUp 可无限铸造
	// phone='' 的幽灵人类账号（且因 ByPhone('') 短路永远无法再登录）。
	ErrPhoneNumberInvalid = errors.New("phone number invalid")
	// ErrSystemUserLoginForbidden 表示内置系统账号被尝试绑定为普通业务会话。
	ErrSystemUserLoginForbidden = errors.New("system user login forbidden")
)

const (
	codeChannelPhone              = store.PhoneCodeChannelPhone
	codeChannelSMS                = store.PhoneCodeChannelSMS
	codeChannelEmailLogin         = store.PhoneCodeChannelEmailLogin
	codeChannelEmailSetupRequired = store.PhoneCodeChannelEmailSetupRequired
	loginCodeRollbackTimeout      = 2 * time.Second
)

// validPhone 校验规范化后的手机号：5-32 位纯数字（上限对齐 users.phone 列宽）。
// 核心目的是拒绝空/非数字 phone（防 0090 partial index 下无限铸造幽灵账号），
// 长度上限从宽，不强求 E.164 精确位数（测试常用更长的唯一 phone）。
func validPhone(phone string) bool {
	return domain.ValidPhone(phone)
}

func systemUserLoginForbidden(u domain.User) bool {
	return domain.IsSystemUserID(u.ID)
}

func systemLoginPhoneForbidden(phone string) bool {
	_, ok := domain.SystemUserByPhone(phone)
	return ok
}

// Service 实现登录/注册业务。默认保留开发固定码；配置外部 provider
// 后生成随机验证码并通过 otpdelivery 投递。已有账号的外部投递是 durable
// 777000 App-code 的附加渠道，不能替换或削弱原有消息事实。
type Service struct {
	users                  store.UserStore
	auths                  store.AuthorizationStore
	codes                  store.CodeStore
	authKeys               store.AuthKeyStore
	tempKeys               store.TempAuthKeyBindingStore
	passwords              store.PasswordStore
	messages               store.MessageStore
	dialogs                store.DialogStore
	loginCodeDelivery      store.LoginCodeDeliveryStore
	bots                   store.BotStore
	fixedCode              string
	codeTTL                time.Duration
	codeMaxAttempts        int
	loginEmails            loginEmailStore
	loginEmailSender       otpdelivery.Sender
	phoneCodeSender        otpdelivery.Sender
	otpDeliveryFailure     func(context.Context, otpdelivery.Request, error)
	phoneCodeLength        int
	loginEmailEnabled      bool
	loginEmailRequireSetup bool
	loginEmailCodeLength   int
	// premiumGrantMonths 是新注册账号默认赠送的会员月数；0 表示关闭赠送。
	premiumGrantMonths int
}

type loginEmailStore interface {
	LoginEmailByPhone(ctx context.Context, phone string) (string, bool, error)
	SetLoginEmail(ctx context.Context, userID int64, email string) error
}

type LoginEmailOptions struct {
	Enabled      bool
	RequireSetup bool
	CodeLength   int
	Store        loginEmailStore
	Sender       otpdelivery.Sender
}

type authorizationRevoker interface {
	RevokeByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error)
	RevokeByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
}

// Option 调整登录服务的可选依赖。
type Option func(*Service)

// WithLoginMessages 在新用户注册成功后写入官方系统账号的首条登录消息与会话摘要。
// 已有账号的 app 验证码必须在 auth.sendCode/resendCode 阶段通过
// WithLoginCodeDelivery 持久化，禁止在 signIn 成功后补发。
func WithLoginMessages(messages store.MessageStore, dialogs store.DialogStore) Option {
	return func(s *Service) {
		s.messages = messages
		s.dialogs = dialogs
	}
}

// WithLoginCodeDelivery 注入已有账号 app-code 的 durable 投递边界。
// 实现必须以 user_id + phone_code_hash 幂等，并原子写入 777000
// message/dialog/user update event/dispatch outbox。
func WithLoginCodeDelivery(delivery store.LoginCodeDeliveryStore) Option {
	return func(s *Service) {
		s.loginCodeDelivery = delivery
	}
}

// WithPasswords lets sign-in stop at SESSION_PASSWORD_NEEDED for 2FA accounts.
func WithPasswords(passwords store.PasswordStore) Option {
	return func(s *Service) {
		s.passwords = passwords
	}
}

// WithBotLogin 启用 auth.importBotAuthorization 的 bot token 登录。
func WithBotLogin(bots store.BotStore) Option {
	return func(s *Service) {
		s.bots = bots
	}
}

// WithPremiumGrant 让新注册账号默认获得 months 个月会员（0 = 关闭赠送）。
// 存量账号的同等赠送由迁移 0094 一次性 backfill。
func WithPremiumGrant(months int) Option {
	return func(s *Service) {
		s.premiumGrantMonths = months
	}
}

func WithCodeTTL(ttl time.Duration) Option {
	return func(s *Service) {
		if ttl > 0 {
			s.codeTTL = ttl
		}
	}
}

func WithCodeMaxAttempts(max int) Option {
	return func(s *Service) {
		if max > 0 {
			s.codeMaxAttempts = max
		}
	}
}

func WithLoginEmail(opts LoginEmailOptions) Option {
	return func(s *Service) {
		s.loginEmailEnabled = opts.Enabled
		s.loginEmailRequireSetup = opts.RequireSetup
		s.loginEmailCodeLength = opts.CodeLength
		if s.loginEmailCodeLength <= 0 {
			s.loginEmailCodeLength = 6
		}
		s.loginEmails = opts.Store
		s.loginEmailSender = opts.Sender
	}
}

// WithPhoneCodeDelivery enables an external SMS delivery provider. Existing
// accounts keep their durable 777000 App-code and receive the same code through
// the provider as an additional channel. A nil sender preserves development
// behavior.
func WithPhoneCodeDelivery(sender otpdelivery.Sender, length int) Option {
	return func(s *Service) {
		s.phoneCodeSender = sender
		if length > 0 {
			s.phoneCodeLength = length
		}
	}
}

// WithOTPDeliveryFailureObserver observes failures of an additional provider
// delivery after an existing account already has a durable 777000 App-code.
// Observers must not log the recipient or code.
func WithOTPDeliveryFailureObserver(observer func(context.Context, otpdelivery.Request, error)) Option {
	return func(s *Service) {
		s.otpDeliveryFailure = observer
	}
}

// NewService 创建登录服务。fixedCode 为开发固定验证码。
func NewService(users store.UserStore, auths store.AuthorizationStore, codes store.CodeStore, authKeys store.AuthKeyStore, tempKeys store.TempAuthKeyBindingStore, fixedCode string, opts ...Option) *Service {
	s := &Service{users: users, auths: auths, codes: codes, authKeys: authKeys, tempKeys: tempKeys, fixedCode: fixedCode, codeTTL: 5 * time.Minute, codeMaxAttempts: 5, loginEmailCodeLength: 6, phoneCodeLength: 5}
	if linker, ok := auths.(store.AuthKeyAuthorityLinker); ok && authKeys != nil {
		linker.LinkAuthKeyAuthority(authKeys)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// BindTempAuthKey 校验并记录 TDesktop PFS temp→perm auth key 绑定。
func (s *Service) BindTempAuthKey(ctx context.Context, sessionID int64, binding domain.TempAuthKeyBinding) error {
	if s.authKeys != nil {
		inner, protocolExpiresAt, err := s.validateBindTempAuthKey(ctx, sessionID, binding)
		if err != nil {
			return err
		}
		binding.TempSessionID = inner.TempSessionID
		// The bind request's expires_at is a signed client assertion. TDesktop
		// intentionally adds a small grace interval, while Android derives its
		// value at handshake completion. Retention and edge admission must use the
		// server's p_q_inner_data_temp lifetime, never the client value.
		binding.ExpiresAt = protocolExpiresAt
	}
	if binding.ExpiresAt <= int(time.Now().Unix()) {
		// The edge may admit the frame immediately before the temporary key's
		// absolute boundary and the encrypted proof may cross it. This is a temp-key
		// rotation condition, never a destructive permanent-key proof failure.
		return ErrTempAuthKeyEmpty
	}
	if s.tempKeys == nil {
		return nil
	}
	if err := s.tempKeys.Save(ctx, binding); err != nil {
		if errors.Is(err, store.ErrTempAuthKeyAlreadyBound) {
			return ErrTempAuthKeyAlreadyBound
		}
		if errors.Is(err, store.ErrAuthKeyBindingInvalid) {
			return s.classifyBindingStoreInvalid(ctx, binding)
		}
		return err
	}
	return nil
}

// ResolveAuthKey 将已绑定的 temp auth_key 解析为对应 perm auth_key。
//
// temp→perm 是握手/绑定形成的协议身份关系，与 perm 当前是否登录完全无关。即使
// auth.logOut 已删除 authorization，只要绑定仍存在，后续登录 RPC 也必须继续落到同一
// perm key，绝不能把 raw temp key 当成新的业务身份。协议过期由 mtprotoedge 在解密/RPC
// 之前返回 -404 并关闭连接；这里不再用 authorization 状态猜测 key 类型。
func (s *Service) ResolveAuthKey(ctx context.Context, authKeyID [8]byte) ([8]byte, bool, error) {
	if s == nil || s.tempKeys == nil {
		return [8]byte{}, false, nil
	}
	binding, found, err := s.tempKeys.GetByTemp(ctx, authKeyID)
	if err != nil || !found {
		return [8]byte{}, found, err
	}
	return authKeyIDFromInt64(binding.PermAuthKeyID), true, nil
}

// UserID 返回 auth_key 当前绑定的用户。未登录、或两步验证未完成时 found=false。
func (s *Service) UserID(ctx context.Context, authKeyID [8]byte) (int64, bool, error) {
	if s == nil || s.auths == nil {
		return 0, false, nil
	}
	a, found, err := s.auths.ByAuthKey(ctx, authKeyID)
	if err != nil || !found {
		return 0, found, err
	}
	if a.PasswordPending {
		// 两步验证未完成：业务鉴权视为未登录，仅允许 auth.checkPassword 继续。
		return 0, false, nil
	}
	if domain.IsSystemUserID(a.UserID) {
		_ = s.auths.Delete(ctx, authKeyID)
		return 0, false, nil
	}
	return a.UserID, true, nil
}

// PendingPasswordUserID 返回处于"待两步验证"状态的 auth_key 对应的用户。
// UserID 对 password_pending 的 auth_key 返回未登录，auth.checkPassword 借此仍能定位待验证用户。
func (s *Service) PendingPasswordUserID(ctx context.Context, authKeyID [8]byte) (int64, bool, error) {
	if s == nil || s.auths == nil {
		return 0, false, nil
	}
	a, found, err := s.auths.ByAuthKey(ctx, authKeyID)
	if err != nil || !found || !a.PasswordPending {
		return 0, false, err
	}
	if domain.IsSystemUserID(a.UserID) {
		_ = s.auths.Delete(ctx, authKeyID)
		return 0, false, nil
	}
	return a.UserID, true, nil
}

// CompletePasswordSignIn 在两步验证通过后清除 password_pending，使 auth_key 转为完全授权。
func (s *Service) CompletePasswordSignIn(ctx context.Context, authKeyID [8]byte) error {
	if s == nil || s.auths == nil {
		return nil
	}
	return s.auths.MarkPasswordPassed(ctx, authKeyID)
}

// SendCode 为 phone 生成 phone_code_hash，按配置选择开发 app code、登录邮箱 code
// 或登录邮箱 setup-required 状态，返回 hash。
func (s *Service) SendCode(ctx context.Context, phone string) (string, error) {
	phone = normalizePhone(phone)
	if !validPhone(phone) {
		return "", ErrPhoneNumberInvalid
	}
	if systemLoginPhoneForbidden(phone) {
		return "", ErrSystemUserLoginForbidden
	}
	existing, found, err := s.currentPhoneOwner(ctx, phone)
	if err != nil {
		return "", fmt.Errorf("lookup login-code recipient: %w", err)
	}
	if found && systemUserLoginForbidden(existing) {
		return "", ErrSystemUserLoginForbidden
	}
	issuedUserID := int64(0)
	if found {
		issuedUserID = existing.ID
	}
	if s.loginEmailEnabled && s.loginEmails != nil {
		email, found, err := s.loginEmails.LoginEmailByPhone(ctx, phone)
		if err != nil {
			return "", err
		}
		if found && strings.TrimSpace(email) != "" {
			return s.createEmailLoginCode(ctx, phone, email, issuedUserID)
		}
		if s.loginEmailRequireSetup {
			return s.createSetupRequiredCode(ctx, phone, issuedUserID)
		}
	}
	return s.createPhoneCode(ctx, phone, issuedUserID)
}

func (s *Service) currentPhoneOwner(ctx context.Context, phone string) (domain.User, bool, error) {
	if s == nil || s.users == nil {
		return domain.User{}, false, fmt.Errorf("user store is not configured")
	}
	return s.users.ByPhone(ctx, phone)
}

func (s *Service) issuedOwnerMatches(ctx context.Context, phone string, issuedUserID int64) (bool, error) {
	current, found, err := s.currentPhoneOwner(ctx, phone)
	if err != nil {
		return false, err
	}
	currentUserID := int64(0)
	if found {
		currentUserID = current.ID
	}
	return currentUserID == issuedUserID, nil
}

func (s *Service) ensureIssuedOwnerAfterSet(ctx context.Context, hash string, rec store.PhoneCode) error {
	matches, err := s.issuedOwnerMatches(ctx, rec.Phone, rec.IssuedUserID)
	if err == nil && matches {
		return nil
	}
	cause := err
	if cause == nil {
		cause = ErrCodeInvalid
	}
	return s.rollbackUndeliveredCode(ctx, hash, cause)
}

func (s *Service) invalidateLoginCodeDetached(ctx context.Context, hash, phone string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginCodeRollbackTimeout)
	defer cancel()
	_, _ = s.codes.InvalidateLoginCode(cleanupCtx, hash, phone)
}

func (s *Service) createPhoneCode(ctx context.Context, phone string, existingUserID int64) (string, error) {
	hash, err := randomHex(8)
	if err != nil {
		return "", err
	}
	code := s.fixedCode
	channel := codeChannelPhone
	deliveryID := ""
	if s.phoneCodeSender != nil {
		code, err = randomDigits(s.phoneCodeLength)
		if err != nil {
			return "", err
		}
		deliveryID, err = otpdelivery.NewDeliveryID()
		if err != nil {
			return "", err
		}
		channel = codeChannelSMS
	}
	rec := store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		IssuedUserID: existingUserID,
		Phone:        phone,
		Code:         code,
		DeliveryID:   deliveryID,
		Channel:      channel,
		MaxAttempts:  s.codeMaxAttempts,
	}
	expiresAt := time.Now().Add(s.codeTTL)
	if err := s.codes.Set(ctx, hash, rec, s.codeTTL); err != nil {
		return "", fmt.Errorf("store code: %w", err)
	}
	if err := s.ensureIssuedOwnerAfterSet(ctx, hash, rec); err != nil {
		return "", err
	}
	// Existing accounts always retain the original durable App-code path. Commit
	// it before attempting the external mirror so a provider cannot replace the
	// message fact or leave an externally disclosed code without local state.
	if existingUserID != 0 {
		if err := s.deliverLoginCode(ctx, existingUserID, hash, code); err != nil {
			return "", s.rollbackUndeliveredCode(ctx, hash, err)
		}
		if err := s.ensureIssuedOwnerAfterSet(ctx, hash, rec); err != nil {
			return "", err
		}
	}
	if s.phoneCodeSender != nil {
		request := otpdelivery.Request{
			DeliveryID: deliveryID,
			Purpose:    otpdelivery.PurposeLoginSMS,
			Channel:    otpdelivery.ChannelSMS,
			Recipient:  phone,
			Code:       code,
			ExpiresAt:  expiresAt,
		}
		if existingUserID != 0 {
			s.deliverOTPWithAppFallback(ctx, s.phoneCodeSender, request)
		} else if err := deliverOTP(ctx, s.phoneCodeSender, request); err != nil {
			return "", s.rollbackUndeliveredCode(ctx, hash, fmt.Errorf("send login SMS code: %w", err))
		}
		if err := s.ensureIssuedOwnerAfterSet(ctx, hash, rec); err != nil {
			return "", err
		}
		return hash, nil
	}
	// 新手机号还没有 owner/dialog，不能在签发阶段创建 777000 消息；
	// 已有账号的 App-code 已在上面的 provider 分支之前 durable 提交。
	return hash, nil
}

func (s *Service) deliverLoginCode(ctx context.Context, userID int64, phoneCodeHash, code string) error {
	if s.loginCodeDelivery == nil {
		return ErrLoginCodeDeliveryUnavailable
	}
	now := time.Now()
	if _, err := s.loginCodeDelivery.DeliverLoginCodeMessage(ctx, domain.LoginCodeDeliveryRequest{
		UserID:        userID,
		PhoneCodeHash: phoneCodeHash,
		Code:          code,
		Date:          int(now.Unix()),
		ExpiresAt:     now.Add(s.codeTTL).Unix(),
	}); err != nil {
		return errors.Join(ErrLoginCodeDeliveryFailed, err)
	}
	return nil
}

func (s *Service) rollbackUndeliveredCode(ctx context.Context, phoneCodeHash string, cause error) error {
	// lib/pq can report an I/O failure after COMMIT reached PostgreSQL. In that
	// state deleting the code could turn an already delivered 777000 message
	// into an unusable login attempt. Preserve it and let the delivery receipt
	// make the retry idempotent.
	if errors.Is(cause, domain.ErrLoginCodeDeliveryCommitAmbiguous) {
		return cause
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginCodeRollbackTimeout)
	defer cancel()
	if err := s.codes.Del(cleanupCtx, phoneCodeHash); err != nil {
		return errors.Join(cause, fmt.Errorf("rollback undelivered login code: %w", err))
	}
	return cause
}

func (s *Service) createSetupRequiredCode(ctx context.Context, phone string, issuedUserID int64) (string, error) {
	hash, err := randomHex(8)
	if err != nil {
		return "", err
	}
	if err := s.codes.Set(ctx, hash, store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		IssuedUserID: issuedUserID,
		Phone:        phone,
		Channel:      codeChannelEmailSetupRequired,
		MaxAttempts:  s.codeMaxAttempts,
	}, s.codeTTL); err != nil {
		return "", fmt.Errorf("store code: %w", err)
	}
	if err := s.ensureIssuedOwnerAfterSet(ctx, hash, store.PhoneCode{Phone: phone, IssuedUserID: issuedUserID}); err != nil {
		return "", err
	}
	return hash, nil
}

func (s *Service) createEmailLoginCode(ctx context.Context, phone, email string, issuedUserID int64) (string, error) {
	hash, err := randomHex(8)
	if err != nil {
		return "", err
	}
	code, err := randomDigits(s.loginEmailCodeLength)
	if err != nil {
		return "", err
	}
	deliveryID, err := otpdelivery.NewDeliveryID()
	if err != nil {
		return "", err
	}
	rec := store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		IssuedUserID: issuedUserID,
		Phone:        phone,
		Code:         code,
		DeliveryID:   deliveryID,
		Channel:      codeChannelEmailLogin,
		Email:        strings.TrimSpace(email),
		MaxAttempts:  s.codeMaxAttempts,
	}
	expiresAt := time.Now().Add(s.codeTTL)
	if err := s.codes.Set(ctx, hash, rec, s.codeTTL); err != nil {
		return "", fmt.Errorf("store email code: %w", err)
	}
	if err := s.ensureIssuedOwnerAfterSet(ctx, hash, rec); err != nil {
		return "", err
	}
	if issuedUserID != 0 {
		if err := s.deliverLoginCode(ctx, issuedUserID, hash, code); err != nil {
			return "", s.rollbackUndeliveredCode(ctx, hash, err)
		}
		if err := s.ensureIssuedOwnerAfterSet(ctx, hash, rec); err != nil {
			return "", err
		}
	}
	if s.loginEmailSender == nil {
		if issuedUserID != 0 {
			s.reportOTPDeliveryFailure(ctx, otpdelivery.Request{
				DeliveryID: deliveryID,
				Purpose:    otpdelivery.PurposeLoginEmail,
				Channel:    otpdelivery.ChannelEmail,
				Recipient:  rec.Email,
				Code:       code,
				ExpiresAt:  expiresAt,
			}, fmt.Errorf("login email sender is not configured"))
			return hash, nil
		}
		return "", s.rollbackUndeliveredCode(ctx, hash, fmt.Errorf("login email sender is not configured"))
	}
	request := otpdelivery.Request{
		DeliveryID: deliveryID,
		Purpose:    otpdelivery.PurposeLoginEmail,
		Channel:    otpdelivery.ChannelEmail,
		Recipient:  rec.Email,
		Code:       code,
		ExpiresAt:  expiresAt,
	}
	if issuedUserID != 0 {
		s.deliverOTPWithAppFallback(ctx, s.loginEmailSender, request)
	} else if err := deliverOTP(ctx, s.loginEmailSender, request); err != nil {
		return "", s.rollbackUndeliveredCode(ctx, hash, fmt.Errorf("send login email code: %w", err))
	}
	if err := s.ensureIssuedOwnerAfterSet(ctx, hash, rec); err != nil {
		return "", err
	}
	return hash, nil
}

// deliverOTPWithAppFallback performs an additional provider delivery only
// after the same code is durably visible through 777000. A provider failure
// must not invalidate that visible code or fail the RPC; it remains observable
// through the injected failure observer.
func (s *Service) deliverOTPWithAppFallback(ctx context.Context, sender otpdelivery.Sender, req otpdelivery.Request) {
	if _, err := sender.Deliver(ctx, req); err != nil {
		s.reportOTPDeliveryFailure(ctx, req, err)
	}
}

func (s *Service) reportOTPDeliveryFailure(ctx context.Context, req otpdelivery.Request, err error) {
	if s.otpDeliveryFailure != nil && err != nil {
		s.otpDeliveryFailure(ctx, req, err)
	}
}

// deliverOTP treats a transport-level unknown outcome as a successful issue:
// the provider may already have accepted the request, so the code must remain
// usable and the client needs the hash in order to verify or explicitly resend
// it. Only an explicit provider rejection is safe to roll back.
func deliverOTP(ctx context.Context, sender otpdelivery.Sender, req otpdelivery.Request) error {
	_, err := sender.Deliver(ctx, req)
	if errors.Is(err, otpdelivery.ErrOutcomeUnknown) {
		return nil
	}
	return err
}

func (s *Service) CodeDelivery(ctx context.Context, phoneCodeHash string) (domain.AuthCodeDelivery, bool, error) {
	rec, found, err := s.codes.Get(ctx, phoneCodeHash)
	if err != nil || !found {
		return domain.AuthCodeDelivery{}, found, err
	}
	return codeDelivery(rec), true, nil
}

func codeDelivery(rec store.PhoneCode) domain.AuthCodeDelivery {
	if rec.Purpose == store.PhoneCodePurposeChangePhone {
		return domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliverySMS, Length: len(rec.Code)}
	}
	switch rec.Channel {
	case codeChannelSMS:
		return domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliverySMS, Length: len(rec.Code)}
	case codeChannelEmailLogin:
		return domain.AuthCodeDelivery{
			Kind:         domain.AuthCodeDeliveryEmail,
			EmailPattern: domain.MaskEmail(rec.Email),
			Length:       len(rec.Code),
		}
	case codeChannelEmailSetupRequired:
		return domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliveryEmailSetupRequired}
	default:
		return domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliveryPhone, Length: len(rec.Code)}
	}
}

// ResendCode invalidates an existing code hash and sends a fresh code to the same phone.
func (s *Service) ResendCode(ctx context.Context, phone, phoneCodeHash string) (string, error) {
	return s.resendCode(ctx, [8]byte{}, phone, phoneCodeHash)
}

// ResendCodeForAuthKey 对已登录敏感操作额外校验发起 auth key；普通登录码
// 没有 AuthKeyID 作用域，行为与 ResendCode 相同。
func (s *Service) ResendCodeForAuthKey(ctx context.Context, authKeyID [8]byte, phone, phoneCodeHash string) (string, error) {
	return s.resendCode(ctx, authKeyID, phone, phoneCodeHash)
}

func (s *Service) resendCode(ctx context.Context, authKeyID [8]byte, phone, phoneCodeHash string) (string, error) {
	phone = normalizePhone(phone)
	rec, found, err := s.codes.Get(ctx, phoneCodeHash)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrCodeExpired
	}
	if rec.Phone != phone {
		return "", ErrCodeInvalid
	}
	if rec.Purpose == store.PhoneCodePurposeChangePhone {
		if authKeyID == ([8]byte{}) || rec.AuthKeyID != authKeyID {
			return "", ErrCodeInvalid
		}
		consumed, ok, err := s.codes.ConsumeScoped(ctx, phoneCodeHash, rec.Scope())
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrCodeExpired
		}
		return s.recreateChangePhoneCode(ctx, consumed)
	}
	if rec.Version != store.PhoneCodeVersionCurrent {
		_, _, _ = s.codes.TakeLoginCode(ctx, phoneCodeHash, phone)
		return "", ErrCodeExpired
	}
	if matches, err := s.issuedOwnerMatches(ctx, phone, rec.IssuedUserID); err != nil {
		return "", err
	} else if !matches {
		s.invalidateLoginCodeDetached(ctx, phoneCodeHash, phone)
		return "", ErrCodeInvalid
	}
	consumed, ok, err := s.codes.TakeLoginCode(ctx, phoneCodeHash, phone)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrCodeExpired
	}
	rec = consumed
	if matches, err := s.issuedOwnerMatches(ctx, phone, rec.IssuedUserID); err != nil {
		return "", err
	} else if !matches {
		s.invalidateLoginCodeDetached(ctx, phoneCodeHash, phone)
		return "", ErrCodeInvalid
	}
	if rec.Channel == codeChannelEmailLogin && strings.TrimSpace(rec.Email) != "" {
		return s.createEmailLoginCode(ctx, phone, rec.Email, rec.IssuedUserID)
	}
	if rec.Channel == codeChannelEmailSetupRequired {
		return s.createSetupRequiredCode(ctx, phone, rec.IssuedUserID)
	}
	if rec.Channel != codeChannelPhone && rec.Channel != codeChannelSMS {
		return "", ErrCodeInvalid
	}
	return s.createPhoneCode(ctx, phone, rec.IssuedUserID)
}

func (s *Service) recreateChangePhoneCode(ctx context.Context, rec store.PhoneCode) (string, error) {
	hash, err := randomHex(8)
	if err != nil {
		return "", err
	}
	rec.Code = s.fixedCode
	rec.DeliveryID = ""
	rec.Channel = codeChannelPhone
	if s.phoneCodeSender != nil {
		rec.Code, err = randomDigits(s.phoneCodeLength)
		if err != nil {
			return "", err
		}
		rec.DeliveryID, err = otpdelivery.NewDeliveryID()
		if err != nil {
			return "", err
		}
		rec.Channel = codeChannelSMS
	}
	rec.Attempts = 0
	if rec.MaxAttempts <= 0 {
		rec.MaxAttempts = s.codeMaxAttempts
	}
	expiresAt := time.Now().Add(s.codeTTL)
	if err := s.codes.Set(ctx, hash, rec, s.codeTTL); err != nil {
		return "", fmt.Errorf("store resent phone change code: %w", err)
	}
	if s.phoneCodeSender != nil {
		if err := deliverOTP(ctx, s.phoneCodeSender, otpdelivery.Request{
			DeliveryID: rec.DeliveryID,
			Purpose:    otpdelivery.PurposeChangePhone,
			Channel:    otpdelivery.ChannelSMS,
			Recipient:  rec.Phone,
			Code:       rec.Code,
			ExpiresAt:  expiresAt,
		}); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginCodeRollbackTimeout)
			defer cancel()
			if _, _, cleanupErr := s.codes.ConsumeScoped(cleanupCtx, hash, rec.Scope()); cleanupErr != nil {
				return "", errors.Join(err, fmt.Errorf("rollback undelivered phone change code: %w", cleanupErr))
			}
			return "", err
		}
	}
	return hash, nil
}

// CancelCode invalidates a pending login code hash.
func (s *Service) CancelCode(ctx context.Context, phone, phoneCodeHash string) error {
	return s.cancelCode(ctx, [8]byte{}, phone, phoneCodeHash)
}

// CancelCodeForAuthKey 是 ResendCodeForAuthKey 对应的取消路径。
func (s *Service) CancelCodeForAuthKey(ctx context.Context, authKeyID [8]byte, phone, phoneCodeHash string) error {
	return s.cancelCode(ctx, authKeyID, phone, phoneCodeHash)
}

// ConsumeLoginEmailReset authorizes auth.resetLoginEmail with the exact
// email-login hash previously issued for this phone owner. Possession of only
// a phone number is never sufficient to remove an authentication factor.
func (s *Service) ConsumeLoginEmailReset(ctx context.Context, phone, phoneCodeHash string) (int64, error) {
	phone = normalizePhone(phone)
	rec, found, err := s.codes.Get(ctx, phoneCodeHash)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrCodeExpired
	}
	if rec.Version != store.PhoneCodeVersionCurrent {
		_, _, _ = s.codes.TakeLoginCode(ctx, phoneCodeHash, phone)
		return 0, ErrCodeExpired
	}
	if rec.Purpose != "" || rec.Phone != phone || rec.Channel != codeChannelEmailLogin || rec.SignUpVerified {
		return 0, ErrCodeInvalid
	}
	before, beforeFound, err := s.currentPhoneOwner(ctx, phone)
	if err != nil {
		return 0, err
	}
	if !beforeFound || systemUserLoginForbidden(before) || rec.IssuedUserID == 0 || rec.IssuedUserID != before.ID {
		s.invalidateLoginCodeDetached(ctx, phoneCodeHash, phone)
		return 0, ErrCodeInvalid
	}
	consumed, consumedOK, err := s.codes.TakeLoginCode(ctx, phoneCodeHash, phone)
	if err != nil {
		return 0, err
	}
	if !consumedOK {
		return 0, ErrCodeExpired
	}
	if consumed.Channel != codeChannelEmailLogin || consumed.IssuedUserID != before.ID {
		return 0, ErrCodeInvalid
	}
	after, afterFound, err := s.currentPhoneOwner(ctx, phone)
	if err != nil {
		return 0, err
	}
	if !afterFound || after.ID != before.ID {
		return 0, ErrCodeInvalid
	}
	return before.ID, nil
}

// SendPhoneCodeAfterLoginEmailReset issues the replacement app code only for
// the exact user selected by ConsumeLoginEmailReset. It deliberately bypasses
// SendCode's phone→owner reclassification so an A→B transfer cannot send B a
// code and return that hash to A's reset flow.
func (s *Service) SendPhoneCodeAfterLoginEmailReset(ctx context.Context, phone string, expectedUserID int64) (string, error) {
	phone = normalizePhone(phone)
	if !validPhone(phone) {
		return "", ErrPhoneNumberInvalid
	}
	if expectedUserID == 0 || systemLoginPhoneForbidden(phone) {
		return "", ErrCodeInvalid
	}
	owner, found, err := s.currentPhoneOwner(ctx, phone)
	if err != nil {
		return "", err
	}
	if !found || owner.ID != expectedUserID || systemUserLoginForbidden(owner) {
		return "", ErrCodeInvalid
	}
	return s.createPhoneCode(ctx, phone, expectedUserID)
}

func (s *Service) cancelCode(ctx context.Context, authKeyID [8]byte, phone, phoneCodeHash string) error {
	phone = normalizePhone(phone)
	rec, found, err := s.codes.Get(ctx, phoneCodeHash)
	if err != nil {
		return err
	}
	if !found {
		return ErrCodeExpired
	}
	if rec.Phone != phone {
		return ErrCodeInvalid
	}
	if rec.Purpose == store.PhoneCodePurposeChangePhone {
		if authKeyID == ([8]byte{}) || rec.AuthKeyID != authKeyID {
			return ErrCodeInvalid
		}
		_, consumed, err := s.codes.ConsumeScoped(ctx, phoneCodeHash, rec.Scope())
		if err != nil {
			return err
		}
		if !consumed {
			return ErrCodeExpired
		}
		return nil
	}
	if rec.Version != store.PhoneCodeVersionCurrent {
		_, _, _ = s.codes.TakeLoginCode(ctx, phoneCodeHash, phone)
		return ErrCodeExpired
	}
	if matches, err := s.issuedOwnerMatches(ctx, phone, rec.IssuedUserID); err != nil {
		return err
	} else if !matches {
		s.invalidateLoginCodeDetached(ctx, phoneCodeHash, phone)
		return ErrCodeInvalid
	}
	_, consumed, err := s.codes.TakeLoginCode(ctx, phoneCodeHash, phone)
	if err != nil {
		return err
	}
	if !consumed {
		return ErrCodeExpired
	}
	return nil
}

// SignIn 校验验证码并尝试登录。
// needSignUp=true 表示验证码正确但用户不存在，调用方应引导注册（此时不删验证码，留给 SignUp）。
func (s *Service) SignIn(ctx context.Context, auth domain.Authorization, phone, phoneCodeHash, code string) (u domain.User, loginMessage domain.Message, needSignUp bool, err error) {
	phone = normalizePhone(phone)
	if systemLoginPhoneForbidden(phone) {
		return domain.User{}, domain.Message{}, false, ErrSystemUserLoginForbidden
	}
	_, existing, found, err := s.verifyLoginCode(ctx, phone, phoneCodeHash, code)
	if err != nil {
		return domain.User{}, domain.Message{}, false, err
	}
	if !found {
		return domain.User{}, domain.Message{}, true, nil
	}
	return s.finishSignIn(ctx, auth, existing)
}

// SignInWithEmail 处理带 email_verification 的 auth.signIn。它与 SignIn
// 共享同一个登录凭证状态机：TDesktop/Android 把邮箱码放在
// email_verification，WebK 把同一邮箱码放在 phone_code；TL 字段只是 proof
// carrier，服务端签发记录的 channel 才表示实际投递渠道。所有渠道都必须精确
// 匹配签发码，并共用 owner 绑定、原子尝试计数、一次性消费与 2FA 门控。
func (s *Service) SignInWithEmail(ctx context.Context, auth domain.Authorization, phone, phoneCodeHash, code string) (domain.User, domain.Message, bool, error) {
	phone = normalizePhone(phone)
	if systemLoginPhoneForbidden(phone) {
		return domain.User{}, domain.Message{}, false, ErrSystemUserLoginForbidden
	}
	_, existing, found, err := s.verifyLoginCode(ctx, phone, phoneCodeHash, strings.TrimSpace(code))
	if err != nil {
		return domain.User{}, domain.Message{}, false, err
	}
	if !found {
		return domain.User{}, domain.Message{}, true, nil
	}
	return s.finishSignIn(ctx, auth, existing)
}

// verifyLoginCode closes the login-code state transition around one atomic
// CodeStore verification. The phone owner is read both before and after that
// linearization point. A hash issued for an unregistered number therefore can
// never authorize whichever account happens to acquire that number later.
func (s *Service) verifyLoginCode(ctx context.Context, phone, phoneCodeHash, code string) (store.PhoneCode, domain.User, bool, error) {
	rec, found, err := s.codes.Get(ctx, phoneCodeHash)
	if err != nil {
		return store.PhoneCode{}, domain.User{}, false, err
	}
	if !found {
		return store.PhoneCode{}, domain.User{}, false, ErrCodeExpired
	}
	if rec.Version != store.PhoneCodeVersionCurrent {
		_, _, _ = s.codes.TakeLoginCode(ctx, phoneCodeHash, phone)
		return store.PhoneCode{}, domain.User{}, false, ErrCodeExpired
	}
	if rec.Phone != phone || rec.Purpose != "" {
		return store.PhoneCode{}, domain.User{}, false, ErrCodeInvalid
	}
	if !store.LoginCodeChannelVerifiable(rec.Channel) {
		return store.PhoneCode{}, domain.User{}, false, ErrCodeInvalid
	}

	before, beforeFound, err := s.currentPhoneOwner(ctx, phone)
	if err != nil {
		return store.PhoneCode{}, domain.User{}, false, err
	}
	beforeUserID := int64(0)
	if beforeFound {
		if systemUserLoginForbidden(before) {
			return store.PhoneCode{}, domain.User{}, false, ErrSystemUserLoginForbidden
		}
		beforeUserID = before.ID
	}
	if rec.IssuedUserID != beforeUserID {
		s.invalidateLoginCodeDetached(ctx, phoneCodeHash, phone)
		return store.PhoneCode{}, domain.User{}, false, ErrCodeInvalid
	}
	// A verified sign-up marker may precede auth.signIn on the email-setup
	// path, and a normal signIn response can be lost and retried. The marker is
	// already the durable authorization fact; return signUpRequired
	// idempotently without asking CodeStore to verify it a second time.
	if rec.SignUpVerified {
		if beforeFound || rec.IssuedUserID != 0 || subtle.ConstantTimeCompare([]byte(rec.Code), []byte(code)) != 1 {
			return store.PhoneCode{}, domain.User{}, false, ErrCodeInvalid
		}
		after, afterFound, err := s.currentPhoneOwner(ctx, phone)
		if err != nil {
			return store.PhoneCode{}, domain.User{}, false, err
		}
		if afterFound || after.ID != 0 {
			s.invalidateLoginCodeDetached(ctx, phoneCodeHash, phone)
			return store.PhoneCode{}, domain.User{}, false, ErrCodeInvalid
		}
		return rec, domain.User{}, false, nil
	}

	result, err := s.codes.VerifyLogin(ctx, phoneCodeHash, phone, code, !beforeFound, s.codeMaxAttempts)
	if err != nil {
		return store.PhoneCode{}, domain.User{}, false, err
	}
	after, afterFound, ownerErr := s.currentPhoneOwner(ctx, phone)
	if ownerErr != nil {
		return store.PhoneCode{}, domain.User{}, false, ownerErr
	}
	afterUserID := int64(0)
	if afterFound {
		afterUserID = after.ID
	}
	recordOwnerMismatch := result.Status != store.LoginCodeVerifyMissing && result.Record.IssuedUserID != rec.IssuedUserID
	if beforeUserID != afterUserID || recordOwnerMismatch {
		// keepForSignUp may have left a verified marker behind. Remove it on
		// owner drift so a later transfer-back cannot resurrect authorization.
		s.invalidateLoginCodeDetached(ctx, phoneCodeHash, phone)
		return store.PhoneCode{}, domain.User{}, false, ErrCodeInvalid
	}
	switch result.Status {
	case store.LoginCodeVerifyMissing:
		return store.PhoneCode{}, domain.User{}, false, ErrCodeExpired
	case store.LoginCodeVerifyInvalid:
		return store.PhoneCode{}, domain.User{}, false, ErrCodeInvalid
	case store.LoginCodeVerifyAccepted:
		if result.Record.Version != store.PhoneCodeVersionCurrent || result.Record.Phone != phone || result.Record.IssuedUserID != afterUserID {
			return store.PhoneCode{}, domain.User{}, false, ErrCodeInvalid
		}
		if afterFound && systemUserLoginForbidden(after) {
			return store.PhoneCode{}, domain.User{}, false, ErrSystemUserLoginForbidden
		}
		return result.Record, after, afterFound, nil
	default:
		return store.PhoneCode{}, domain.User{}, false, ErrCodeInvalid
	}
}

// finishSignIn 是短信/邮箱两条登录路径在「验证码已通过、用户已存在」之后的共用收尾：
// 验证码已由 VerifyLogin 原子消费；这里只处理 2FA password_pending 绑定。已有账号的 app-code 消息已在
// SendCode/ResendCode 返回前持久化与入 outbox，这里绝不能再创建或补发；
// 否则未完成登录/2FA 的真实验证码反而不会及时到达旧设备。
func (s *Service) finishSignIn(ctx context.Context, auth domain.Authorization, existing domain.User) (domain.User, domain.Message, bool, error) {
	if systemUserLoginForbidden(existing) {
		return domain.User{}, domain.Message{}, false, ErrSystemUserLoginForbidden
	}
	// 开启两步验证的账号：把授权标记为 password_pending 再写入，业务鉴权据此拒绝该 auth_key，
	// 直到 auth.checkPassword 通过。绝不能先以完全授权写入再返回 SESSION_PASSWORD_NEEDED，
	// 否则客户端忽略该错误即可直接调用业务 RPC 绕过两步验证。
	passwordNeeded, err := s.passwordNeeded(ctx, existing.ID)
	if err != nil {
		// Password state is part of the authentication decision. Treat store
		// failures as fail-closed and leave the auth key entirely unbound.
		return domain.User{}, domain.Message{}, false, err
	}
	auth.PasswordPending = passwordNeeded
	if err := s.bind(ctx, auth, existing.ID); err != nil {
		return domain.User{}, domain.Message{}, false, err
	}
	if passwordNeeded {
		return existing, domain.Message{}, false, domain.ErrSessionPasswordNeeded
	}
	return existing, domain.Message{}, false, nil
}

// SignUp 在 SignIn 判定需注册后创建用户并绑定授权。
// signUp 的 TL 请求不带验证码，因此只消费由正确 SignIn/email setup 原子
// 标记过的 hash。直接 SendCode→SignUp 永远不能创建账号。
func (s *Service) SignUp(ctx context.Context, auth domain.Authorization, phone, phoneCodeHash, firstName, lastName string) (domain.User, domain.Message, error) {
	phone = normalizePhone(phone)
	if !validPhone(phone) {
		return domain.User{}, domain.Message{}, ErrPhoneNumberInvalid
	}
	if systemLoginPhoneForbidden(phone) {
		return domain.User{}, domain.Message{}, ErrSystemUserLoginForbidden
	}
	firstName = strings.TrimSpace(firstName)
	lastName = strings.TrimSpace(lastName)
	if firstName == "" || utf8.RuneCountInString(firstName) > 64 || utf8.RuneCountInString(lastName) > 64 {
		return domain.User{}, domain.Message{}, domain.ErrFirstNameInvalid
	}
	rec, found, err := s.codes.Get(ctx, phoneCodeHash)
	if err != nil {
		return domain.User{}, domain.Message{}, err
	}
	if !found {
		return domain.User{}, domain.Message{}, ErrCodeExpired
	}
	if rec.Version != store.PhoneCodeVersionCurrent {
		_, _, _ = s.codes.ConsumeSignUpVerified(ctx, phoneCodeHash, phone)
		return domain.User{}, domain.Message{}, ErrCodeExpired
	}
	if rec.Phone != phone || rec.Purpose != "" {
		return domain.User{}, domain.Message{}, ErrCodeInvalid
	}
	if !rec.SignUpVerified {
		return domain.User{}, domain.Message{}, ErrCodeInvalid
	}
	if rec.IssuedUserID != 0 {
		s.invalidateLoginCodeDetached(ctx, phoneCodeHash, phone)
		return domain.User{}, domain.Message{}, ErrCodeInvalid
	}
	if !store.LoginCodeChannelVerifiable(rec.Channel) {
		return domain.User{}, domain.Message{}, ErrCodeInvalid
	}
	if s.loginEmailRequireSetup && !rec.VerifiedEmail && strings.TrimSpace(rec.PendingEmail) == "" {
		return domain.User{}, domain.Message{}, ErrCodeInvalid
	}
	if current, currentFound, err := s.currentPhoneOwner(ctx, phone); err != nil {
		return domain.User{}, domain.Message{}, err
	} else if currentFound || current.ID != 0 {
		s.invalidateLoginCodeDetached(ctx, phoneCodeHash, phone)
		return domain.User{}, domain.Message{}, ErrCodeInvalid
	}
	consumed, consumedOK, err := s.codes.ConsumeSignUpVerified(ctx, phoneCodeHash, phone)
	if err != nil {
		return domain.User{}, domain.Message{}, err
	}
	if !consumedOK {
		return domain.User{}, domain.Message{}, ErrCodeExpired
	}
	rec = consumed
	if rec.IssuedUserID != 0 || !rec.SignUpVerified || !store.LoginCodeChannelVerifiable(rec.Channel) {
		return domain.User{}, domain.Message{}, ErrCodeInvalid
	}
	if current, currentFound, err := s.currentPhoneOwner(ctx, phone); err != nil {
		return domain.User{}, domain.Message{}, err
	} else if currentFound || current.ID != 0 {
		return domain.User{}, domain.Message{}, ErrCodeInvalid
	}

	accessHash, err := randomInt64()
	if err != nil {
		return domain.User{}, domain.Message{}, err
	}
	newUser := domain.User{
		AccessHash: accessHash,
		Phone:      phone,
		FirstName:  firstName,
		LastName:   lastName,
	}
	// 新账号默认赠送会员：到期时间 = 注册时刻 + N 个月（与迁移 0094 对存量
	// 账号的 backfill 同一语义）。premium 状态由下发路径按该时间即时派生。
	if s.premiumGrantMonths > 0 {
		newUser.PremiumUntil = int(time.Now().AddDate(0, s.premiumGrantMonths, 0).Unix())
	}
	u, err := s.users.Create(ctx, newUser)
	if err != nil {
		return domain.User{}, domain.Message{}, err
	}
	if rec.VerifiedEmail && strings.TrimSpace(rec.PendingEmail) != "" && s.loginEmails != nil {
		if err := s.loginEmails.SetLoginEmail(ctx, u.ID, rec.PendingEmail); err != nil {
			return domain.User{}, domain.Message{}, err
		}
	}
	if err := s.bind(ctx, auth, u.ID); err != nil {
		return domain.User{}, domain.Message{}, err
	}
	loginMessage := domain.Message{}
	// A new account has no owner/dialog at issuance time. Only the development
	// phone/App registration path creates its bootstrap 777000 message here;
	// external SMS and email setup registration retain only their verified fact.
	if rec.Channel == codeChannelPhone {
		loginMessage, err = s.recordLoginMessage(ctx, u.ID, rec.Code)
		if err != nil {
			return domain.User{}, domain.Message{}, err
		}
	}
	return u, loginMessage, nil
}

// SignInBot 处理 auth.importBotAuthorization：校验 bot token 并把当前 auth_key
// 绑定到 bot 账号。token 校验必须先于 bind（bind 即授权生效）；bot 无 2FA，
// PasswordPending 恒 false；不写登录消息、不推 signIn 通知（手机登录语义）。
// 任何校验失败统一返回 domain.ErrBotTokenInvalid，不区分原因避免泄漏存在性。
func (s *Service) SignInBot(ctx context.Context, auth domain.Authorization, token string) (domain.User, error) {
	if s == nil || s.bots == nil || s.users == nil {
		return domain.User{}, domain.ErrBotTokenInvalid
	}
	botUserID, secret, ok := domain.ParseBotToken(strings.TrimSpace(token))
	if !ok {
		return domain.User{}, domain.ErrBotTokenInvalid
	}
	profile, found, err := s.bots.GetBot(ctx, botUserID)
	if err != nil {
		return domain.User{}, err
	}
	// 空 secret（内置 BotFather）永不可登录；比较走常数时间。
	if !found || profile.TokenSecret == "" ||
		subtle.ConstantTimeCompare([]byte(profile.TokenSecret), []byte(secret)) != 1 {
		return domain.User{}, domain.ErrBotTokenInvalid
	}
	u, found, err := s.users.ByID(ctx, botUserID)
	if err != nil {
		return domain.User{}, err
	}
	if !found || !u.Bot {
		return domain.User{}, domain.ErrBotTokenInvalid
	}
	if systemUserLoginForbidden(u) {
		return domain.User{}, domain.ErrBotTokenInvalid
	}
	auth.PasswordPending = false
	if err := s.bind(ctx, auth, u.ID); err != nil {
		return domain.User{}, err
	}
	// check-bind-recheck：SignInBot 的「校验 secret → bind」非原子，并发 /revoke
	// 可能在两步之间换 secret 并删除已有 authorization。此处 bind 写入的新行不会被
	// 那次删除覆盖（删除发生在 bind 之前），会逃过 session 撤销。bind 后复核 secret：
	// 若已被换掉，撤销刚写入的授权并拒登，闭合竞态窗口。
	if again, found, err := s.bots.GetBot(ctx, botUserID); err != nil {
		_ = s.auths.Delete(ctx, auth.AuthKeyID)
		return domain.User{}, err
	} else if !found || again.TokenSecret == "" ||
		subtle.ConstantTimeCompare([]byte(again.TokenSecret), []byte(secret)) != 1 {
		_ = s.auths.Delete(ctx, auth.AuthKeyID)
		return domain.User{}, domain.ErrBotTokenInvalid
	}
	return u, nil
}

// BindVerifiedLogin 把当前 auth_key 绑定到一个已由外部强因子(如 passkey)验证过身份的
// 用户,直接完成授权。passkey 是独立强因子,不再叠加 2FA password(PasswordPending=false),
// 与官方"passkey 登录跳过密码步骤"一致。校验已发生在调用方(passkey 断言验证),此处只负责绑定。
func (s *Service) BindVerifiedLogin(ctx context.Context, auth domain.Authorization, userID int64) (domain.User, error) {
	if s == nil || s.users == nil || userID == 0 {
		return domain.User{}, domain.ErrPasskeyInvalid
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, domain.ErrPasskeyNotFound
	}
	if systemUserLoginForbidden(u) {
		return domain.User{}, ErrSystemUserLoginForbidden
	}
	auth.PasswordPending = false
	if err := s.bind(ctx, auth, userID); err != nil {
		return domain.User{}, err
	}
	return u, nil
}

// AcceptLoginToken 把 QR 登录请求方的 auth_key 绑定到扫码确认的 user。
func (s *Service) AcceptLoginToken(ctx context.Context, auth domain.Authorization, userID int64) (domain.Authorization, error) {
	if s == nil || s.auths == nil || userID == 0 || auth.AuthKeyID == ([8]byte{}) {
		return domain.Authorization{}, fmt.Errorf("accept login token: invalid authorization")
	}
	if domain.IsSystemUserID(userID) {
		return domain.Authorization{}, ErrSystemUserLoginForbidden
	}
	auth.PasswordPending = false
	if err := s.bind(ctx, auth, userID); err != nil {
		return domain.Authorization{}, err
	}
	bound, found, err := s.auths.ByAuthKey(ctx, auth.AuthKeyID)
	if err != nil {
		return domain.Authorization{}, err
	}
	if found {
		return bound, nil
	}
	auth.UserID = userID
	return auth, nil
}

// LogOut 解绑当前 auth_key 的授权。
func (s *Service) LogOut(ctx context.Context, authKeyID [8]byte) error {
	return s.auths.Delete(ctx, authKeyID)
}

func (s *Service) Authorization(ctx context.Context, authKeyID [8]byte) (domain.Authorization, bool, error) {
	if s == nil || s.auths == nil || authKeyID == ([8]byte{}) {
		return domain.Authorization{}, false, nil
	}
	return s.auths.ByAuthKey(ctx, authKeyID)
}

func (s *Service) AuthKeyClientInfo(ctx context.Context, authKeyID [8]byte) (domain.AuthKeyClientInfo, bool, error) {
	if s == nil || s.authKeys == nil || authKeyID == ([8]byte{}) {
		return domain.AuthKeyClientInfo{}, false, nil
	}
	key, found, err := s.authKeys.Get(ctx, authKeyID)
	if err != nil || !found {
		return domain.AuthKeyClientInfo{}, found, err
	}
	info := domain.AuthKeyClientInfo{
		Layer:              key.Layer,
		LayerObservationID: key.LayerObservationID,
		DeviceModel:        key.DeviceModel,
		Platform:           key.Platform,
		SystemVersion:      key.SystemVersion,
		APIID:              key.APIID,
		AppVersion:         key.AppVersion,
	}
	if info.Layer == 0 && info.DeviceModel == "" && info.Platform == "" &&
		info.SystemVersion == "" && info.APIID == 0 && info.AppVersion == "" {
		return domain.AuthKeyClientInfo{}, false, nil
	}
	return info, true, nil
}

func (s *Service) UpdateAuthKeyClientInfo(ctx context.Context, authKeyID [8]byte, info domain.AuthKeyClientInfo) error {
	if s == nil || s.authKeys == nil || authKeyID == ([8]byte{}) {
		return nil
	}
	if err := s.authKeys.UpdateClientInfo(ctx, authKeyID, store.AuthKeyClientInfo{
		Layer:         info.Layer,
		DeviceModel:   info.DeviceModel,
		Platform:      info.Platform,
		SystemVersion: info.SystemVersion,
		APIID:         info.APIID,
		AppVersion:    info.AppVersion,
	}); err != nil {
		return err
	}
	if s.auths != nil {
		// Layer is an ordered protocol fact. Its authorization-table mirror is
		// advanced atomically by the durable Layer evidence/bind transactions.
		// A generic metadata update is deliberately two-store and can race such
		// a transaction, so it must never write an older Layer after the primary
		// auth_keys row has already advanced.
		authorizationInfo := info
		authorizationInfo.Layer = 0
		authorizationInfo.LayerObservationID = 0
		return s.auths.UpdateClientInfo(ctx, authKeyID, authorizationInfo)
	}
	return nil
}

func (s *Service) ListAuthorizations(ctx context.Context, userID int64) ([]domain.Authorization, error) {
	if s == nil || s.auths == nil || userID == 0 {
		return nil, nil
	}
	return s.auths.ListByUser(ctx, userID)
}

func (s *Service) ResetAuthorization(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error) {
	if s == nil || s.auths == nil || userID == 0 {
		return domain.Authorization{}, false, nil
	}
	if revoker, ok := s.auths.(authorizationRevoker); ok {
		return revoker.RevokeByHash(ctx, userID, hash)
	}
	target, found, err := s.authorizationByHash(ctx, userID, hash)
	if err != nil || !found {
		return target, found, err
	}
	if err := s.deleteAuthKey(ctx, target.AuthKeyID); err != nil {
		return target, true, err
	}
	deleted, found, err := s.auths.DeleteByHash(ctx, userID, hash)
	if err != nil || !found {
		return deleted, found, err
	}
	return deleted, true, nil
}

func (s *Service) ResetAuthorizations(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error) {
	if s == nil || s.auths == nil || userID == 0 {
		return nil, nil
	}
	if revoker, ok := s.auths.(authorizationRevoker); ok {
		return revoker.RevokeByUserExcept(ctx, userID, keepAuthKeyID)
	}
	targets, err := s.authorizationsByUserExcept(ctx, userID, keepAuthKeyID)
	if err != nil {
		return nil, err
	}
	for _, a := range targets {
		if err := s.deleteAuthKey(ctx, a.AuthKeyID); err != nil {
			return nil, err
		}
	}
	deleted, err := s.auths.DeleteByUserExcept(ctx, userID, keepAuthKeyID)
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

func (s *Service) deleteAuthKey(ctx context.Context, authKeyID [8]byte) error {
	if s == nil || s.authKeys == nil || authKeyID == ([8]byte{}) {
		return nil
	}
	return s.authKeys.Delete(ctx, authKeyID)
}

func (s *Service) authorizationByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error) {
	items, err := s.auths.ListByUser(ctx, userID)
	if err != nil {
		return domain.Authorization{}, false, err
	}
	for _, a := range items {
		if a.Hash == hash {
			return a, true, nil
		}
	}
	return domain.Authorization{}, false, nil
}

func (s *Service) authorizationsByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error) {
	items, err := s.auths.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Authorization, 0, len(items))
	for _, a := range items {
		if a.AuthKeyID != keepAuthKeyID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *Service) bind(ctx context.Context, auth domain.Authorization, userID int64) error {
	if s.authKeys != nil {
		key, found, err := s.authKeys.Get(ctx, auth.AuthKeyID)
		if err != nil {
			return err
		}
		// Defense in depth: Router normally converts a bound temp key to its perm
		// identity and edge rejects expired temp keys. Never let an unbound/sticky
		// temp key create authorization even if either outer boundary regresses.
		if !found || key.ExpiresAt != 0 {
			return ErrAuthKeyPermEmpty
		}
	}
	auth.UserID = userID
	// Bind 是授权切换的持久化状态边界：生产 store 会先清同 auth key 的旧用户
	// update state，再原子建立新用户 baseline。RPC 层不得在 Bind 成功后清整个 key，
	// 否则会把刚建立的 retained-floor checkpoint 一并删除。
	if err := s.auths.Bind(ctx, auth); err != nil {
		if errors.Is(err, store.ErrAuthKeyNotPermanent) {
			return ErrAuthKeyPermEmpty
		}
		return err
	}
	return nil
}

func (s *Service) passwordNeeded(ctx context.Context, userID int64) (bool, error) {
	if s.passwords == nil {
		return false, nil
	}
	settings, found, err := s.passwords.GetByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	return found && settings.HasPassword, nil
}

const loginMessageTpl = `Login code: %s. Do not give this code to anyone, even if they say they are from Telegram!

This code can be used to log in to your Telegram account. We never ask it for anything else.

If you didn't request this code by trying to log in on another device, simply ignore this message.`

func (s *Service) recordLoginMessage(ctx context.Context, userID int64, code string) (domain.Message, error) {
	if s.messages == nil || s.dialogs == nil {
		return domain.Message{}, nil
	}
	body := fmt.Sprintf(loginMessageTpl, code)
	codeOffset := len("Login code: ")
	msg, err := s.messages.Create(ctx, domain.Message{
		OwnerUserID: userID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        int(time.Now().Unix()),
		Body:        body,
		Entities: []domain.MessageEntity{
			{Type: domain.MessageEntityBold, Offset: 0, Length: len("Login code:")},
			{Type: domain.MessageEntityBold, Offset: codeOffset, Length: len(code)},
		},
	})
	if err != nil {
		return domain.Message{}, err
	}
	if err := s.dialogs.UpsertInbox(ctx, userID, domain.Dialog{
		Peer:           msg.Peer,
		TopMessage:     msg.ID,
		TopMessageDate: msg.Date,
	}); err != nil {
		return domain.Message{}, err
	}
	return msg, nil
}

func (s *Service) validateBindTempAuthKey(ctx context.Context, sessionID int64, binding domain.TempAuthKeyBinding) (mtcrypto.BindAuthKeyInner, int, error) {
	if binding.ExpiresAt <= int(time.Now().Unix()) {
		return mtcrypto.BindAuthKeyInner{}, 0, ErrExpiresAtInvalid
	}
	temp, found, err := s.authKeys.Get(ctx, binding.TempAuthKeyID)
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, 0, err
	}
	// expires_at in auth.bindTempAuthKey is client-supplied and must only attest
	// to a still-live binding. It may never create or reclassify a protocol key;
	// the caller normalizes durable retention to this handshake-authoritative
	// temp.ExpiresAt instead of trusting the client value.
	if !found || temp.ExpiresAt <= int(time.Now().Unix()) {
		return mtcrypto.BindAuthKeyInner{}, 0, ErrTempAuthKeyEmpty
	}

	permID := authKeyIDFromInt64(binding.PermAuthKeyID)
	perm, found, err := s.authKeys.Get(ctx, permID)
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, 0, err
	}
	if !found || perm.ExpiresAt != 0 {
		return mtcrypto.BindAuthKeyInner{}, 0, ErrEncryptedMessageInvalid
	}

	inner, err := decryptBindAuthKeyInner(perm, binding.EncryptedMessage)
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, 0, ErrEncryptedMessageInvalid
	}
	if inner.Nonce != binding.Nonce ||
		inner.TempAuthKeyID != authKeyIDInt64(binding.TempAuthKeyID) ||
		inner.PermAuthKeyID != binding.PermAuthKeyID ||
		inner.TempSessionID != sessionID ||
		inner.ExpiresAt != binding.ExpiresAt {
		return mtcrypto.BindAuthKeyInner{}, 0, ErrEncryptedMessageInvalid
	}
	if temp.ExpiresAt <= int(time.Now().Unix()) {
		return mtcrypto.BindAuthKeyInner{}, 0, ErrTempAuthKeyEmpty
	}
	return inner, temp.ExpiresAt, nil
}

func (s *Service) classifyBindingStoreInvalid(ctx context.Context, binding domain.TempAuthKeyBinding) error {
	if s == nil || s.authKeys == nil {
		return ErrEncryptedMessageInvalid
	}
	temp, found, err := s.authKeys.Get(ctx, binding.TempAuthKeyID)
	if err != nil {
		return err
	}
	if !found || temp.ExpiresAt <= int(time.Now().Unix()) {
		return ErrTempAuthKeyEmpty
	}
	return ErrEncryptedMessageInvalid
}

func decryptBindAuthKeyInner(perm store.AuthKeyData, encrypted []byte) (mtcrypto.BindAuthKeyInner, error) {
	var msg mtcrypto.EncryptedMessage
	if err := msg.Decode(&bin.Buffer{Buf: encrypted}); err != nil {
		return mtcrypto.BindAuthKeyInner{}, err
	}
	if msg.AuthKeyID != perm.ID || len(msg.EncryptedData) == 0 || len(msg.EncryptedData)%aes.BlockSize != 0 {
		return mtcrypto.BindAuthKeyInner{}, ErrEncryptedMessageInvalid
	}

	key, iv := mtcrypto.KeysV1(mtcrypto.Key(perm.Value), msg.MsgKey)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, err
	}
	plaintext := make([]byte, len(msg.EncryptedData))
	ige.DecryptBlocks(block, iv[:], plaintext, msg.EncryptedData)

	const headerLen = 16 + 8 + 4 + 4
	if len(plaintext) < headerLen {
		return mtcrypto.BindAuthKeyInner{}, ErrEncryptedMessageInvalid
	}
	b := &bin.Buffer{Buf: plaintext}
	randomPrefix := make([]byte, 16)
	if err := b.ConsumeN(randomPrefix, len(randomPrefix)); err != nil {
		return mtcrypto.BindAuthKeyInner{}, err
	}
	if _, err := b.Long(); err != nil {
		return mtcrypto.BindAuthKeyInner{}, err
	}
	if _, err := b.Int32(); err != nil {
		return mtcrypto.BindAuthKeyInner{}, err
	}
	msgLen, err := b.Int32()
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, err
	}
	if msgLen <= 0 {
		return mtcrypto.BindAuthKeyInner{}, ErrEncryptedMessageInvalid
	}
	bodyEnd := headerLen + int(msgLen)
	if bodyEnd > len(plaintext) {
		return mtcrypto.BindAuthKeyInner{}, ErrEncryptedMessageInvalid
	}
	if msg.MsgKey != mtcrypto.MessageKeyV1(plaintext[:bodyEnd]) {
		return mtcrypto.BindAuthKeyInner{}, ErrEncryptedMessageInvalid
	}

	body := plaintext[headerLen:bodyEnd]
	var inner mtcrypto.BindAuthKeyInner
	if err := inner.Decode(&bin.Buffer{Buf: body}); err != nil {
		return mtcrypto.BindAuthKeyInner{}, err
	}
	return inner, nil
}

func authKeyIDFromInt64(v int64) [8]byte {
	var id [8]byte
	binary.LittleEndian.PutUint64(id[:], uint64(v))
	return id
}

func authKeyIDInt64(id [8]byte) int64 {
	return int64(binary.LittleEndian.Uint64(id[:]))
}

func normalizePhone(phone string) string {
	return domain.NormalizePhone(phone)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func randomDigits(n int) (string, error) {
	if n <= 0 {
		n = 6
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	var out strings.Builder
	out.Grow(n)
	for _, v := range b {
		out.WriteByte(byte('0') + v%10)
	}
	return out.String(), nil
}

func randomInt64() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("rand: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil
}
