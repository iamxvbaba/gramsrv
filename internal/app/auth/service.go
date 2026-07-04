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
	"github.com/gotd/td/bin"
	mtcrypto "github.com/gotd/td/crypto"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 登录错误。
var (
	ErrCodeExpired             = errors.New("phone code expired or not found")
	ErrCodeInvalid             = errors.New("phone code invalid")
	ErrEncryptedMessageInvalid = errors.New("encrypted message invalid")
	// ErrPhoneNumberInvalid 表示手机号为空或非纯数字/长度越界。
	// 0090 把 users.phone 唯一约束改为忽略空串的部分索引（bot 行 phone=''），
	// 因此 phone 校验必须前移到 auth 入口，否则 sendCode/signUp 可无限铸造
	// phone='' 的幽灵人类账号（且因 ByPhone('') 短路永远无法再登录）。
	ErrPhoneNumberInvalid = errors.New("phone number invalid")
	// ErrSystemUserLoginForbidden 表示内置系统账号被尝试绑定为普通业务会话。
	ErrSystemUserLoginForbidden = errors.New("system user login forbidden")
)

// validPhone 校验规范化后的手机号：5-32 位纯数字（上限对齐 users.phone 列宽）。
// 核心目的是拒绝空/非数字 phone（防 0090 partial index 下无限铸造幽灵账号），
// 长度上限从宽，不强求 E.164 精确位数（测试常用更长的唯一 phone）。
func validPhone(phone string) bool {
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

func systemUserLoginForbidden(u domain.User) bool {
	return domain.IsSystemUserID(u.ID)
}

func systemLoginPhoneForbidden(phone string) bool {
	_, ok := domain.SystemUserByPhone(phone)
	return ok
}

// Service 实现登录/注册业务。第一阶段为开发固定验证码（不真实下发短信）。
type Service struct {
	users     store.UserStore
	auths     store.AuthorizationStore
	codes     store.CodeStore
	authKeys  store.AuthKeyStore
	tempKeys  store.TempAuthKeyBindingStore
	passwords store.PasswordStore
	messages  store.MessageStore
	dialogs   store.DialogStore
	bots      store.BotStore
	fixedCode string
	codeTTL   time.Duration
	// premiumGrantMonths 是新注册账号默认赠送的会员月数；0 表示关闭赠送。
	premiumGrantMonths int
}

type authorizationRevoker interface {
	RevokeByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error)
	RevokeByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
}

// Option 调整登录服务的可选依赖。
type Option func(*Service)

// WithLoginMessages 在登录成功后写入官方系统账号的登录消息与会话摘要。
func WithLoginMessages(messages store.MessageStore, dialogs store.DialogStore) Option {
	return func(s *Service) {
		s.messages = messages
		s.dialogs = dialogs
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

// NewService 创建登录服务。fixedCode 为开发固定验证码。
func NewService(users store.UserStore, auths store.AuthorizationStore, codes store.CodeStore, authKeys store.AuthKeyStore, tempKeys store.TempAuthKeyBindingStore, fixedCode string, opts ...Option) *Service {
	s := &Service{users: users, auths: auths, codes: codes, authKeys: authKeys, tempKeys: tempKeys, fixedCode: fixedCode, codeTTL: 5 * time.Minute}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// BindTempAuthKey 校验并记录 TDesktop PFS temp→perm auth key 绑定。
func (s *Service) BindTempAuthKey(ctx context.Context, sessionID int64, binding domain.TempAuthKeyBinding) error {
	if s.authKeys != nil {
		inner, err := s.validateBindTempAuthKey(ctx, sessionID, binding)
		if err != nil {
			return err
		}
		binding.TempSessionID = inner.TempSessionID
	}
	if s.tempKeys == nil {
		return nil
	}
	return s.tempKeys.Save(ctx, binding)
}

// ResolveAuthKey 将已绑定的 temp auth_key 解析为对应 perm auth_key。
//
// 过期处理是有意的连续性权衡（见 TestResolveAuthKeyAllowsExpiredTempBindingForAuthorizedPermKey）：
// temp 绑定 expires_at 已过时，仅当 perm key 也未授权才拒绝；perm 仍授权则继续解析，
// 避免已登录会话因 temp key 过期而被强制踢下线。严格 PFS 要求过期 temp key 一律失效
// （不以 perm 授权豁免），但收紧前需先核实目标客户端（TDesktop/DrKLO）会在过期前主动
// 轮换 temp key 并优雅处理拒绝，否则会造成在线会话掉线。RetentionWorker 的 DeleteExpired
// 已把残留窗口限制在 expires_at + 宽限（约 24h）内。收紧为显式硬化任务，需客户端验证。
func (s *Service) ResolveAuthKey(ctx context.Context, authKeyID [8]byte) ([8]byte, bool, error) {
	if s == nil || s.tempKeys == nil {
		return [8]byte{}, false, nil
	}
	binding, found, err := s.tempKeys.GetByTemp(ctx, authKeyID)
	if err != nil || !found {
		return [8]byte{}, found, err
	}
	permID := authKeyIDFromInt64(binding.PermAuthKeyID)
	if binding.ExpiresAt <= int(time.Now().Unix()) && !s.permAuthKeyAuthorized(ctx, permID) {
		return [8]byte{}, false, nil
	}
	return permID, true, nil
}

func (s *Service) permAuthKeyAuthorized(ctx context.Context, authKeyID [8]byte) bool {
	if s == nil || s.auths == nil {
		return false
	}
	_, found, err := s.auths.ByAuthKey(ctx, authKeyID)
	return err == nil && found
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

// SendCode 为 phone 生成 phone_code_hash，暂存（开发）固定验证码，返回 hash。
func (s *Service) SendCode(ctx context.Context, phone string) (string, error) {
	phone = normalizePhone(phone)
	if !validPhone(phone) {
		return "", ErrPhoneNumberInvalid
	}
	if systemLoginPhoneForbidden(phone) {
		return "", ErrSystemUserLoginForbidden
	}
	hash, err := randomHex(8)
	if err != nil {
		return "", err
	}
	if err := s.codes.Set(ctx, hash, store.PhoneCode{Phone: phone, Code: s.fixedCode}, s.codeTTL); err != nil {
		return "", fmt.Errorf("store code: %w", err)
	}
	return hash, nil
}

// ResendCode invalidates an existing code hash and sends a fresh code to the same phone.
func (s *Service) ResendCode(ctx context.Context, phone, phoneCodeHash string) (string, error) {
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
	_ = s.codes.Del(ctx, phoneCodeHash)
	return s.SendCode(ctx, phone)
}

// CancelCode invalidates a pending login code hash.
func (s *Service) CancelCode(ctx context.Context, phone, phoneCodeHash string) error {
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
	return s.codes.Del(ctx, phoneCodeHash)
}

// SignIn 校验验证码并尝试登录。
// needSignUp=true 表示验证码正确但用户不存在，调用方应引导注册（此时不删验证码，留给 SignUp）。
func (s *Service) SignIn(ctx context.Context, auth domain.Authorization, phone, phoneCodeHash, code string) (u domain.User, loginMessage domain.Message, needSignUp bool, err error) {
	phone = normalizePhone(phone)
	if systemLoginPhoneForbidden(phone) {
		return domain.User{}, domain.Message{}, false, ErrSystemUserLoginForbidden
	}
	rec, found, err := s.codes.Get(ctx, phoneCodeHash)
	if err != nil {
		return domain.User{}, domain.Message{}, false, err
	}
	if !found {
		return domain.User{}, domain.Message{}, false, ErrCodeExpired
	}
	if rec.Phone != phone || rec.Code != code {
		return domain.User{}, domain.Message{}, false, ErrCodeInvalid
	}

	existing, found, err := s.users.ByPhone(ctx, phone)
	if err != nil {
		return domain.User{}, domain.Message{}, false, err
	}
	if !found {
		return domain.User{}, domain.Message{}, true, nil // 验证码对、但需注册
	}
	return s.finishSignIn(ctx, auth, existing, phoneCodeHash, rec.Code)
}

// SignInWithEmail 处理带 email_verification 的 auth.signIn：账号设置了登录邮箱后，新设备
// 的验证码改投递到邮箱，客户端凭邮箱码（而非短信码）登录。开发环境接受任意非空邮箱码
// （与短信固定码同口径，"随意输入"）；仍校验 phone_code_hash 有效、手机号匹配，并与短信
// 登录共用 2FA 门控——即便走邮箱验证，开启了两步验证的账号同样会停在 SESSION_PASSWORD_NEEDED。
func (s *Service) SignInWithEmail(ctx context.Context, auth domain.Authorization, phone, phoneCodeHash, code string) (domain.User, domain.Message, bool, error) {
	phone = normalizePhone(phone)
	if systemLoginPhoneForbidden(phone) {
		return domain.User{}, domain.Message{}, false, ErrSystemUserLoginForbidden
	}
	rec, found, err := s.codes.Get(ctx, phoneCodeHash)
	if err != nil {
		return domain.User{}, domain.Message{}, false, err
	}
	if !found {
		return domain.User{}, domain.Message{}, false, ErrCodeExpired
	}
	if rec.Phone != phone {
		return domain.User{}, domain.Message{}, false, ErrCodeInvalid
	}
	if strings.TrimSpace(code) == "" {
		return domain.User{}, domain.Message{}, false, ErrCodeInvalid
	}
	existing, found, err := s.users.ByPhone(ctx, phone)
	if err != nil {
		return domain.User{}, domain.Message{}, false, err
	}
	if !found {
		return domain.User{}, domain.Message{}, true, nil
	}
	return s.finishSignIn(ctx, auth, existing, phoneCodeHash, rec.Code)
}

// finishSignIn 是短信/邮箱两条登录路径在「验证码已通过、用户已存在」之后的共用收尾：
// 处理 2FA password_pending 绑定、写登录消息、消费验证码。
func (s *Service) finishSignIn(ctx context.Context, auth domain.Authorization, existing domain.User, phoneCodeHash, loginCode string) (domain.User, domain.Message, bool, error) {
	if systemUserLoginForbidden(existing) {
		_ = s.codes.Del(ctx, phoneCodeHash)
		return domain.User{}, domain.Message{}, false, ErrSystemUserLoginForbidden
	}
	// 开启两步验证的账号：把授权标记为 password_pending 再写入，业务鉴权据此拒绝该 auth_key，
	// 直到 auth.checkPassword 通过。绝不能先以完全授权写入再返回 SESSION_PASSWORD_NEEDED，
	// 否则客户端忽略该错误即可直接调用业务 RPC 绕过两步验证。
	passwordNeeded := s.passwordNeeded(ctx, existing.ID)
	auth.PasswordPending = passwordNeeded
	if err := s.bind(ctx, auth, existing.ID); err != nil {
		return domain.User{}, domain.Message{}, false, err
	}
	if passwordNeeded {
		_ = s.codes.Del(ctx, phoneCodeHash)
		return existing, domain.Message{}, false, domain.ErrSessionPasswordNeeded
	}
	loginMessage, err := s.recordLoginMessage(ctx, existing.ID, loginCode)
	if err != nil {
		return domain.User{}, domain.Message{}, false, err
	}
	_ = s.codes.Del(ctx, phoneCodeHash)
	return existing, loginMessage, false, nil
}

// SignUp 在 SignIn 判定需注册后创建用户并绑定授权。
// signUp 的 TL 请求不带验证码，这里校验 phone_code_hash 仍有效且手机号匹配。
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
	if rec.Phone != phone {
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
	if err := s.bind(ctx, auth, u.ID); err != nil {
		return domain.User{}, domain.Message{}, err
	}
	loginMessage, err := s.recordLoginMessage(ctx, u.ID, rec.Code)
	if err != nil {
		return domain.User{}, domain.Message{}, err
	}
	_ = s.codes.Del(ctx, phoneCodeHash)
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

func (s *Service) UpdateAuthorizationLayer(ctx context.Context, authKeyID [8]byte, layer int) error {
	if s == nil || s.auths == nil || authKeyID == ([8]byte{}) || layer <= 0 {
		return nil
	}
	return s.auths.UpdateLayer(ctx, authKeyID, layer)
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
	auth.UserID = userID
	return s.auths.Bind(ctx, auth)
}

func (s *Service) passwordNeeded(ctx context.Context, userID int64) bool {
	if s.passwords == nil {
		return false
	}
	settings, found, err := s.passwords.GetByUser(ctx, userID)
	return err == nil && found && settings.HasPassword
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

func (s *Service) validateBindTempAuthKey(ctx context.Context, sessionID int64, binding domain.TempAuthKeyBinding) (mtcrypto.BindAuthKeyInner, error) {
	if binding.ExpiresAt <= int(time.Now().Unix()) {
		return mtcrypto.BindAuthKeyInner{}, ErrEncryptedMessageInvalid
	}

	permID := authKeyIDFromInt64(binding.PermAuthKeyID)
	perm, found, err := s.authKeys.Get(ctx, permID)
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, err
	}
	if !found {
		return mtcrypto.BindAuthKeyInner{}, ErrEncryptedMessageInvalid
	}

	inner, err := decryptBindAuthKeyInner(perm, binding.EncryptedMessage)
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, ErrEncryptedMessageInvalid
	}
	if inner.Nonce != binding.Nonce ||
		inner.TempAuthKeyID != authKeyIDInt64(binding.TempAuthKeyID) ||
		inner.PermAuthKeyID != binding.PermAuthKeyID ||
		inner.TempSessionID != sessionID ||
		inner.ExpiresAt != binding.ExpiresAt {
		return mtcrypto.BindAuthKeyInner{}, ErrEncryptedMessageInvalid
	}
	return inner, nil
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

func randomInt64() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("rand: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil
}
