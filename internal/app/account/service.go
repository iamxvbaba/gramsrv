package account

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var defaultSecureRandom = []byte("telesrv-tdesktop-dev-secure-rand")

const (
	passwordResetWait  = 7 * 24 * time.Hour
	passwordResetRetry = 24 * time.Hour
)

// Service 提供账号安全配置查询。
type Service struct {
	passwords   store.PasswordStore
	reactions   store.AccountReactionSettingsStore
	settings    store.AccountSettingsStore
	notify      store.NotifySettingsStore
	stickers    store.StickerCollectionStore
	stickerSets store.UserStickerSetStore
	savedMusic  store.SavedMusicStore
	business    store.BusinessAutomationStore
	// users 仅用于登录邮箱的 phone→user 解析（sendCode 检测 / login-setup / reset 走 phone）。
	users store.UserStore
}

// ServiceOption 调整 account 服务依赖。
type ServiceOption func(*Service)

// WithReactionSettings 注入账号级 reaction 设置持久化。
func WithReactionSettings(reactions store.AccountReactionSettingsStore) ServiceOption {
	return func(s *Service) {
		s.reactions = reactions
	}
}

// WithAccountSettings 注入账号级单例设置（全局隐私/TTL/敏感内容/注册通知）持久化。
func WithAccountSettings(settings store.AccountSettingsStore) ServiceOption {
	return func(s *Service) {
		s.settings = settings
	}
}

// WithNotifySettings 注入 per-scope 通知设置持久化。
func WithNotifySettings(notify store.NotifySettingsStore) ServiceOption {
	return func(s *Service) {
		s.notify = notify
	}
}

// WithStickerCollections 注入个人贴纸/GIF 集合持久化（faved/recent/gif）。
func WithStickerCollections(stickers store.StickerCollectionStore) ServiceOption {
	return func(s *Service) {
		s.stickers = stickers
	}
}

// WithUserStickerSets 注入账号级 installed sticker set 状态持久化。
func WithUserStickerSets(stickerSets store.UserStickerSetStore) ServiceOption {
	return func(s *Service) {
		s.stickerSets = stickerSets
	}
}

// WithSavedMusic 注入账号级 profile music 列表持久化。
func WithSavedMusic(savedMusic store.SavedMusicStore) ServiceOption {
	return func(s *Service) {
		s.savedMusic = savedMusic
	}
}

// WithBusinessAutomation 注入账号级 Business Profile/Quick Replies/Chat Links 持久化。
func WithBusinessAutomation(business store.BusinessAutomationStore) ServiceOption {
	return func(s *Service) {
		s.business = business
	}
}

// WithUsers 注入用户读存储，供登录邮箱的 phone→user 解析使用。
func WithUsers(users store.UserStore) ServiceOption {
	return func(s *Service) {
		s.users = users
	}
}

// NewService 创建 account 服务。
func NewService(passwords store.PasswordStore, opts ...ServiceOption) *Service {
	s := &Service{passwords: passwords}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SaveMusic adds, removes, or reorders a song in the current user's profile music list.
func (s *Service) SaveMusic(ctx context.Context, userID int64, req domain.SaveMusicRequest) (bool, error) {
	if userID == 0 || req.Document.ID == 0 || !req.Document.IsMusic() {
		return false, domain.ErrDocumentInvalid
	}
	if s == nil || s.savedMusic == nil {
		return true, nil
	}
	req.UserID = userID
	return true, s.savedMusic.SaveMusic(ctx, req)
}

// ListSavedMusicIDs returns the full ordered id list for account.getSavedMusicIds.
func (s *Service) ListSavedMusicIDs(ctx context.Context, userID int64, limit int) ([]int64, error) {
	if s == nil || s.savedMusic == nil || userID == 0 {
		return nil, nil
	}
	return s.savedMusic.ListSavedMusicIDs(ctx, userID, limit)
}

// ListSavedMusic returns an ordered saved/profile music page.
func (s *Service) ListSavedMusic(ctx context.Context, userID int64, offset, limit int) (domain.SavedMusicList, error) {
	if s == nil || s.savedMusic == nil || userID == 0 {
		return domain.SavedMusicList{UserID: userID}, nil
	}
	return s.savedMusic.ListSavedMusic(ctx, userID, offset, limit)
}

// GetSavedMusicByIDs refreshes file references for songs still present in the user's list.
func (s *Service) GetSavedMusicByIDs(ctx context.Context, userID int64, ids []int64) (domain.SavedMusicList, error) {
	if s == nil || s.savedMusic == nil || userID == 0 || len(ids) == 0 {
		return domain.SavedMusicList{UserID: userID}, nil
	}
	return s.savedMusic.GetSavedMusicByIDs(ctx, userID, ids)
}

// GetPassword 返回当前账号 2FA 配置。未登录或无记录时返回持久化策略的默认 no-password 配置。
func (s *Service) GetPassword(ctx context.Context, userID int64) (domain.PasswordSettings, error) {
	if s == nil || s.passwords == nil || userID == 0 {
		return defaultPasswordSettings(), nil
	}
	settings, found, err := s.passwords.GetByUser(ctx, userID)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	if !found {
		return defaultPasswordSettings(), nil
	}
	settings = normalizePasswordSettings(settings)
	if settings.HasPassword {
		secret, b, err := makeSRPChallenge(settings.SRPVerifier)
		if err != nil {
			return domain.PasswordSettings{}, err
		}
		settings.SRPBSecret = secret
		settings.SRPB = b
		if settings.SRPID == 0 {
			settings.SRPID, err = randomInt64()
			if err != nil {
				return domain.PasswordSettings{}, err
			}
		}
		if err := s.passwords.Save(ctx, userID, settings); err != nil {
			return domain.PasswordSettings{}, err
		}
	}
	return settings, nil
}

func defaultPasswordSettings() domain.PasswordSettings {
	return normalizePasswordSettings(domain.PasswordSettings{SecureRandom: append([]byte(nil), defaultSecureRandom...)})
}

func normalizePasswordSettings(settings domain.PasswordSettings) domain.PasswordSettings {
	if len(settings.SecureRandom) == 0 {
		settings.SecureRandom = append([]byte(nil), defaultSecureRandom...)
	}
	if len(settings.NewAlgo.P) == 0 {
		settings.NewAlgo = defaultPasswordAlgo()
	}
	if settings.NewSecureAlgo.Kind == "" {
		settings.NewSecureAlgo = defaultSecureAlgo()
	}
	if settings.HasPassword && settings.CurrentAlgo == nil {
		algo := settings.NewAlgo
		settings.CurrentAlgo = &algo
	}
	if settings.RecoveryEmail != "" {
		settings.HasRecovery = true
	}
	// login_email_pattern 始终从已确认的登录邮箱派生，与 2FA 恢复邮箱 RecoveryEmail
	// 解耦（历史实现曾把恢复邮箱掩码误写进此字段，导致客户端把恢复邮箱当成登录邮箱显示）。
	settings.LoginEmailPattern = emailPattern(settings.LoginEmail)
	return settings
}

// CheckPassword validates the current account password check.
func (s *Service) CheckPassword(ctx context.Context, userID int64, check domain.PasswordCheck) error {
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	return checkSRP(settings, check)
}

// GetPasswordWithoutRefresh returns persisted settings without rotating the SRP challenge.
func (s *Service) GetPasswordWithoutRefresh(ctx context.Context, userID int64) (domain.PasswordSettings, error) {
	if s == nil || s.passwords == nil || userID == 0 {
		return defaultPasswordSettings(), nil
	}
	settings, found, err := s.passwords.GetByUser(ctx, userID)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	if !found {
		return defaultPasswordSettings(), nil
	}
	return normalizePasswordSettings(settings), nil
}

// GetPasswordSettings validates the password and returns private 2FA settings.
func (s *Service) GetPasswordSettings(ctx context.Context, userID int64, check domain.PasswordCheck) (domain.PrivatePasswordSettings, error) {
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return domain.PrivatePasswordSettings{}, err
	}
	if err := checkSRP(settings, check); err != nil {
		return domain.PrivatePasswordSettings{}, err
	}
	return domain.PrivatePasswordSettings{Email: settings.RecoveryEmail}, nil
}

// UpdatePasswordSettings sets, changes, clears, or updates the recovery email for 2FA.
func (s *Service) UpdatePasswordSettings(ctx context.Context, userID int64, check domain.PasswordCheck, input domain.PasswordInputSettings) error {
	if s == nil || s.passwords == nil || userID == 0 {
		return nil
	}
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	if err := checkSRP(settings, check); err != nil {
		return err
	}
	if len(input.NewPasswordHash) == 0 && !input.HasEmail {
		settings = defaultPasswordSettings()
		settings.SecureRandom = randomBytesOrDefault(passwordHashSize, settings.SecureRandom)
		return s.passwords.Save(ctx, userID, settings)
	}
	if len(input.NewPasswordHash) > 0 {
		if err := validateNewPasswordSettings(input); err != nil {
			return err
		}
		srpID, err := randomInt64()
		if err != nil {
			return err
		}
		algo := *input.NewAlgo
		settings.CurrentAlgo = &algo
		settings.NewAlgo = defaultPasswordAlgo()
		settings.SRPVerifier = padToHash(input.NewPasswordHash)
		secret, b, err := makeSRPChallenge(settings.SRPVerifier)
		if err != nil {
			return err
		}
		settings.SRPBSecret = secret
		settings.SRPB = b
		settings.SRPID = srpID
		settings.HasPassword = true
		if input.HasHint {
			settings.Hint = input.Hint
		}
	}
	if input.HasEmail {
		email := strings.TrimSpace(input.Email)
		if email != "" && !strings.Contains(email, "@") {
			return domain.ErrEmailInvalid
		}
		settings.RecoveryEmail = email
		settings.HasRecovery = email != ""
		settings.EmailUnconfirmedPattern = ""
	}
	settings.SecureRandom = randomBytesOrDefault(passwordHashSize, settings.SecureRandom)
	return s.passwords.Save(ctx, userID, normalizePasswordSettings(settings))
}

func (s *Service) RequestPasswordRecovery(ctx context.Context, userID int64) (string, error) {
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return "", err
	}
	if !settings.HasPassword || settings.RecoveryEmail == "" {
		return "", domain.ErrPasswordRecoveryNA
	}
	settings.RecoveryCode = recoveryCode
	settings.RecoveryCodeExpiresAt = time.Now().Unix() + recoveryCodeTTL
	if s.passwords != nil {
		if err := s.passwords.Save(ctx, userID, settings); err != nil {
			return "", err
		}
	}
	return emailPattern(settings.RecoveryEmail), nil
}

func (s *Service) CheckRecoveryPassword(ctx context.Context, userID int64, code string) error {
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	return checkRecoveryCode(settings, code)
}

func (s *Service) RecoverPassword(ctx context.Context, userID int64, code string, input *domain.PasswordInputSettings) error {
	if s == nil || s.passwords == nil || userID == 0 {
		return nil
	}
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	if err := checkRecoveryCode(settings, code); err != nil {
		return err
	}
	if input == nil || len(input.NewPasswordHash) == 0 {
		settings = defaultPasswordSettings()
		return s.passwords.Save(ctx, userID, settings)
	}
	if err := validateNewPasswordSettings(*input); err != nil {
		return err
	}
	settings.CurrentAlgo = input.NewAlgo
	settings.SRPVerifier = padToHash(input.NewPasswordHash)
	settings.SRPID, err = randomInt64()
	if err != nil {
		return err
	}
	settings.SRPBSecret, settings.SRPB, err = makeSRPChallenge(settings.SRPVerifier)
	if err != nil {
		return err
	}
	settings.HasPassword = true
	if input.HasHint {
		settings.Hint = input.Hint
	}
	settings.RecoveryCode = ""
	settings.RecoveryCodeExpiresAt = 0
	return s.passwords.Save(ctx, userID, normalizePasswordSettings(settings))
}

func (s *Service) ResetPassword(ctx context.Context, userID int64) (domain.PasswordResetResult, error) {
	if s == nil || s.passwords == nil || userID == 0 {
		return domain.PasswordResetResult{Kind: domain.PasswordResetFailedWait, RetryDate: int(time.Now().Add(passwordResetRetry).Unix())}, nil
	}
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return domain.PasswordResetResult{}, err
	}
	if !settings.HasPassword {
		return domain.PasswordResetResult{Kind: domain.PasswordResetOK}, nil
	}
	if settings.HasRecovery {
		return domain.PasswordResetResult{}, domain.ErrPasswordRecoveryNA
	}
	now := time.Now()
	if settings.PendingResetDate > 0 {
		if now.Unix() >= int64(settings.PendingResetDate) {
			next := defaultPasswordSettings()
			next.SecureRandom = randomBytesOrDefault(passwordHashSize, settings.SecureRandom)
			if err := s.passwords.Save(ctx, userID, next); err != nil {
				return domain.PasswordResetResult{}, err
			}
			return domain.PasswordResetResult{Kind: domain.PasswordResetOK}, nil
		}
		return domain.PasswordResetResult{Kind: domain.PasswordResetRequestedWait, UntilDate: settings.PendingResetDate}, nil
	}
	settings.PendingResetDate = int(now.Add(passwordResetWait).Unix())
	if err := s.passwords.Save(ctx, userID, normalizePasswordSettings(settings)); err != nil {
		return domain.PasswordResetResult{}, err
	}
	return domain.PasswordResetResult{Kind: domain.PasswordResetRequestedWait, UntilDate: settings.PendingResetDate}, nil
}

func (s *Service) DeclinePasswordReset(ctx context.Context, userID int64) error {
	if s == nil || s.passwords == nil || userID == 0 {
		return nil
	}
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	settings.PendingResetDate = 0
	return s.passwords.Save(ctx, userID, normalizePasswordSettings(settings))
}

func (s *Service) ConfirmPasswordEmail(ctx context.Context, userID int64, code string) error {
	return s.CheckRecoveryPassword(ctx, userID, code)
}

func (s *Service) ResendPasswordEmail(ctx context.Context, userID int64) error {
	_, err := s.RequestPasswordRecovery(ctx, userID)
	return err
}

func (s *Service) CancelPasswordEmail(ctx context.Context, userID int64) error {
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	settings.EmailUnconfirmedPattern = ""
	settings.RecoveryCode = ""
	settings.RecoveryCodeExpiresAt = 0
	if s.passwords != nil {
		return s.passwords.Save(ctx, userID, settings)
	}
	return nil
}

func checkRecoveryCode(settings domain.PasswordSettings, code string) error {
	if settings.RecoveryCode == "" {
		if code == recoveryCode {
			return nil
		}
		return domain.ErrPasswordRecoveryNA
	}
	if settings.RecoveryCodeExpiresAt > 0 && time.Now().Unix() > settings.RecoveryCodeExpiresAt {
		return domain.ErrEmailCodeInvalid
	}
	if subtle.ConstantTimeCompare([]byte(settings.RecoveryCode), []byte(code)) != 1 {
		return domain.ErrEmailCodeInvalid
	}
	return nil
}

func randomBytesOrDefault(n int, fallback []byte) []byte {
	out := make([]byte, n)
	if _, err := rand.Read(out); err != nil {
		return append([]byte(nil), fallback...)
	}
	return out
}

func randomInt64() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	out := int64(0)
	for _, v := range b {
		out = (out << 8) | int64(v)
	}
	if out == 0 {
		out = 1
	}
	if out < 0 {
		out = -out
	}
	return out, nil
}

func emailPattern(email string) string {
	return domain.MaskEmail(email)
}

// validLoginEmail 是登录邮箱的最小校验：非空且含 '@'。开发环境不做更严格的 RFC 校验。
func validLoginEmail(email string) bool {
	return email != "" && strings.Contains(email, "@")
}

// SetLoginEmail 为已登录用户写入登录邮箱（authed 的 emailVerifyPurposeLoginChange）。
// 账号无 2FA 也可设置：account_passwords 行可在 has_password=false 下仅承载登录邮箱。
func (s *Service) SetLoginEmail(ctx context.Context, userID int64, email string) error {
	if s == nil || s.passwords == nil || userID == 0 {
		return domain.ErrEmailInvalid
	}
	email = strings.TrimSpace(email)
	if !validLoginEmail(email) {
		return domain.ErrEmailInvalid
	}
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	settings.LoginEmail = email
	settings.LoginEmailPattern = emailPattern(email)
	return s.passwords.Save(ctx, userID, settings)
}

// SetLoginEmailByPhone 为某手机号对应的账号写入登录邮箱（登录流程中的
// emailVerifyPurposeLoginSetup，此时尚未鉴权，只能凭 phone 定位用户）。
func (s *Service) SetLoginEmailByPhone(ctx context.Context, phone, email string) error {
	userID, found, err := s.userIDByPhone(ctx, phone)
	if err != nil {
		return err
	}
	if !found {
		return domain.ErrEmailInvalid
	}
	return s.SetLoginEmail(ctx, userID, email)
}

// LoginEmail 返回已登录用户的登录邮箱原始地址（用于 verifyEmail 回显 emailVerified.email）。
func (s *Service) LoginEmail(ctx context.Context, userID int64) (string, bool, error) {
	if s == nil || s.passwords == nil || userID == 0 {
		return "", false, nil
	}
	settings, found, err := s.passwords.GetByUser(ctx, userID)
	if err != nil {
		return "", false, err
	}
	if !found || settings.LoginEmail == "" {
		return "", false, nil
	}
	return settings.LoginEmail, true, nil
}

// LoginEmailByPhone 按手机号返回登录邮箱原始地址（供 auth.sendCode 检测是否改投邮箱、
// login-setup 回显、reset 回显使用）。
func (s *Service) LoginEmailByPhone(ctx context.Context, phone string) (string, bool, error) {
	userID, found, err := s.userIDByPhone(ctx, phone)
	if err != nil || !found {
		return "", false, err
	}
	return s.LoginEmail(ctx, userID)
}

// ClearLoginEmailByPhone 清除某手机号账号的登录邮箱（auth.resetLoginEmail）。
func (s *Service) ClearLoginEmailByPhone(ctx context.Context, phone string) error {
	userID, found, err := s.userIDByPhone(ctx, phone)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	settings, found, err := s.passwords.GetByUser(ctx, userID)
	if err != nil || !found {
		return err
	}
	settings.LoginEmail = ""
	settings.LoginEmailPattern = ""
	return s.passwords.Save(ctx, userID, settings)
}

func (s *Service) userIDByPhone(ctx context.Context, phone string) (int64, bool, error) {
	if s == nil || s.users == nil {
		return 0, false, nil
	}
	u, found, err := s.users.ByPhone(ctx, domain.NormalizePhone(phone))
	if err != nil || !found {
		return 0, false, err
	}
	return u.ID, true, nil
}

// GetReactionSettings returns account-level reaction preferences.
func (s *Service) GetReactionSettings(ctx context.Context, userID int64) (domain.AccountReactionSettings, error) {
	if s == nil || s.reactions == nil || userID == 0 {
		return domain.DefaultAccountReactionSettings(), nil
	}
	settings, found, err := s.reactions.GetReactionSettings(ctx, userID)
	if err != nil {
		return domain.AccountReactionSettings{}, err
	}
	if !found {
		return domain.DefaultAccountReactionSettings(), nil
	}
	return normalizeReactionSettings(settings), nil
}

// SetReactionsNotifySettings stores reaction notification preferences.
func (s *Service) SetReactionsNotifySettings(ctx context.Context, userID int64, notify domain.ReactionsNotifySettings) (domain.AccountReactionSettings, error) {
	settings, err := s.GetReactionSettings(ctx, userID)
	if err != nil {
		return domain.AccountReactionSettings{}, err
	}
	settings.Notify = normalizeNotifySettings(notify)
	return s.saveReactionSettings(ctx, userID, settings)
}

// SetDefaultReaction stores the account default quick reaction.
func (s *Service) SetDefaultReaction(ctx context.Context, userID int64, reaction domain.MessageReaction) (domain.AccountReactionSettings, error) {
	settings, err := s.GetReactionSettings(ctx, userID)
	if err != nil {
		return domain.AccountReactionSettings{}, err
	}
	if !reaction.Valid() {
		reaction = domain.DefaultAccountReactionSettings().DefaultReaction
	}
	settings.DefaultReaction = reaction
	return s.saveReactionSettings(ctx, userID, settings)
}

// SetPaidReactionPrivacy stores the account default paid reaction privacy.
func (s *Service) SetPaidReactionPrivacy(ctx context.Context, userID int64, privacy domain.PaidReactionPrivacy) (domain.AccountReactionSettings, error) {
	settings, err := s.GetReactionSettings(ctx, userID)
	if err != nil {
		return domain.AccountReactionSettings{}, err
	}
	settings.PaidPrivacy = normalizePaidPrivacy(privacy)
	return s.saveReactionSettings(ctx, userID, settings)
}

func (s *Service) saveReactionSettings(ctx context.Context, userID int64, settings domain.AccountReactionSettings) (domain.AccountReactionSettings, error) {
	settings = normalizeReactionSettings(settings)
	if s == nil || s.reactions == nil || userID == 0 {
		return settings, nil
	}
	return settings, s.reactions.SaveReactionSettings(ctx, userID, settings)
}

// GetAccountSettings 返回账号级单例设置（未持久化时回落默认）。
func (s *Service) GetAccountSettings(ctx context.Context, userID int64) (domain.AccountSettings, error) {
	if s == nil || s.settings == nil || userID == 0 {
		return domain.DefaultAccountSettings(), nil
	}
	settings, found, err := s.settings.GetAccountSettings(ctx, userID)
	if err != nil {
		return domain.AccountSettings{}, err
	}
	if !found {
		return domain.DefaultAccountSettings(), nil
	}
	settings.AccountTTLDays = settings.NormalizedTTLDays()
	return settings, nil
}

// SetGlobalPrivacy 持久化账号全局隐私开关，返回合并后的完整设置。
func (s *Service) SetGlobalPrivacy(ctx context.Context, userID int64, privacy domain.GlobalPrivacy) (domain.AccountSettings, error) {
	settings, err := s.GetAccountSettings(ctx, userID)
	if err != nil {
		return domain.AccountSettings{}, err
	}
	if privacy.NoncontactPeersPaidStars < 0 {
		privacy.NoncontactPeersPaidStars = 0
	}
	settings.GlobalPrivacy = privacy
	return s.saveAccountSettings(ctx, userID, settings)
}

// SetAccountTTL 持久化账号自毁期限（钳制 >0）。
func (s *Service) SetAccountTTL(ctx context.Context, userID int64, days int) (domain.AccountSettings, error) {
	settings, err := s.GetAccountSettings(ctx, userID)
	if err != nil {
		return domain.AccountSettings{}, err
	}
	settings.AccountTTLDays = days
	settings.AccountTTLDays = settings.NormalizedTTLDays()
	return s.saveAccountSettings(ctx, userID, settings)
}

// SetSensitiveContent 持久化敏感内容查看开关。
func (s *Service) SetSensitiveContent(ctx context.Context, userID int64, enabled bool) (domain.AccountSettings, error) {
	settings, err := s.GetAccountSettings(ctx, userID)
	if err != nil {
		return domain.AccountSettings{}, err
	}
	settings.SensitiveContentEnabled = enabled
	return s.saveAccountSettings(ctx, userID, settings)
}

// SetContactSignUpSilent 持久化“联系人注册时是否静音通知”。
func (s *Service) SetContactSignUpSilent(ctx context.Context, userID int64, silent bool) (domain.AccountSettings, error) {
	settings, err := s.GetAccountSettings(ctx, userID)
	if err != nil {
		return domain.AccountSettings{}, err
	}
	settings.ContactSignUpSilent = silent
	return s.saveAccountSettings(ctx, userID, settings)
}

func (s *Service) saveAccountSettings(ctx context.Context, userID int64, settings domain.AccountSettings) (domain.AccountSettings, error) {
	settings.AccountTTLDays = settings.NormalizedTTLDays()
	if settings.GlobalPrivacy.NoncontactPeersPaidStars < 0 {
		settings.GlobalPrivacy.NoncontactPeersPaidStars = 0
	}
	if s == nil || s.settings == nil || userID == 0 {
		return settings, nil
	}
	return settings, s.settings.SaveAccountSettings(ctx, userID, settings)
}

// GetNotifySettings 返回某作用域的通知设置（未配置返回零值=继承默认）。
func (s *Service) GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, error) {
	if s == nil || s.notify == nil || ownerUserID == 0 {
		return domain.PeerNotifySettings{}, nil
	}
	settings, _, err := s.notify.GetNotifySettings(ctx, ownerUserID, scope)
	if err != nil {
		return domain.PeerNotifySettings{}, err
	}
	return settings, nil
}

// SaveNotifySettings 持久化某作用域的通知设置。
func (s *Service) SaveNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error {
	if s == nil || s.notify == nil || ownerUserID == 0 {
		return nil
	}
	return s.notify.SaveNotifySettings(ctx, ownerUserID, scope, settings)
}

// ResetNotifySettings 清空该用户全部作用域的通知设置（恢复默认）。
func (s *Service) ResetNotifySettings(ctx context.Context, ownerUserID int64) error {
	if s == nil || s.notify == nil || ownerUserID == 0 {
		return nil
	}
	return s.notify.ResetNotifySettings(ctx, ownerUserID)
}

// PeerNotifySettings 批量取一组 peer 的整-peer 通知设置（dialog 列表投影）。
func (s *Service) PeerNotifySettings(ctx context.Context, ownerUserID int64, peers []domain.Peer) (map[domain.Peer]domain.PeerNotifySettings, error) {
	if s == nil || s.notify == nil || ownerUserID == 0 || len(peers) == 0 {
		return nil, nil
	}
	return s.notify.GetPeerNotifySettings(ctx, ownerUserID, peers)
}

// AllPeerNotifySettings 一次取该用户全部整-peer 通知设置（per-user notify 缓存的加载源）。
func (s *Service) AllPeerNotifySettings(ctx context.Context, ownerUserID int64) (map[domain.Peer]domain.PeerNotifySettings, error) {
	if s == nil || s.notify == nil || ownerUserID == 0 {
		return nil, nil
	}
	return s.notify.AllPeerNotifySettings(ctx, ownerUserID)
}

// ListNotifyExceptions 列出该用户全部 per-peer 非默认通知设置（getNotifyExceptions）。
func (s *Service) ListNotifyExceptions(ctx context.Context, ownerUserID int64) ([]domain.NotifyException, error) {
	if s == nil || s.notify == nil || ownerUserID == 0 {
		return nil, nil
	}
	return s.notify.ListNotifyExceptions(ctx, ownerUserID)
}

// SaveStickerCollectionItem 收藏/最近/GIF 集合的加入或移除（最新置顶、按类别上界截断）。
func (s *Service) SaveStickerCollectionItem(ctx context.Context, userID int64, kind domain.StickerCollectionKind, documentID int64, unsave bool, now int) error {
	if s == nil || s.stickers == nil || userID == 0 {
		return nil
	}
	return s.stickers.SaveStickerCollectionItem(ctx, userID, kind, documentID, unsave, now, domain.MaxStickerCollectionItems(kind))
}

// ListStickerCollection 取某类个人贴纸集合（最新在前）。
func (s *Service) ListStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind, limit int) ([]domain.StickerCollectionItem, error) {
	if s == nil || s.stickers == nil || userID == 0 {
		return nil, nil
	}
	return s.stickers.ListStickerCollection(ctx, userID, kind, limit)
}

// ClearStickerCollection 清空某类个人贴纸集合。
func (s *Service) ClearStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind) error {
	if s == nil || s.stickers == nil || userID == 0 {
		return nil
	}
	return s.stickers.ClearStickerCollection(ctx, userID, kind)
}

// InstallUserStickerSet 安装或重新激活一个贴纸集，安装态是 per-user 事实。
func (s *Service) InstallUserStickerSet(ctx context.Context, userID int64, setID int64, kind domain.StickerSetKind, archived bool, installedDate int) error {
	if s == nil || s.stickerSets == nil || userID == 0 || setID == 0 {
		return nil
	}
	return s.stickerSets.InstallUserStickerSet(ctx, userID, setID, kind, archived, installedDate)
}

func (s *Service) UninstallUserStickerSet(ctx context.Context, userID int64, setID int64) error {
	if s == nil || s.stickerSets == nil || userID == 0 || setID == 0 {
		return nil
	}
	return s.stickerSets.UninstallUserStickerSet(ctx, userID, setID)
}

func (s *Service) SetUserStickerSetArchived(ctx context.Context, userID int64, setID int64, archived bool, now int) error {
	if s == nil || s.stickerSets == nil || userID == 0 || setID == 0 {
		return nil
	}
	return s.stickerSets.SetUserStickerSetArchived(ctx, userID, setID, archived, now)
}

func (s *Service) ReorderUserStickerSets(ctx context.Context, userID int64, kind domain.StickerSetKind, order []int64, now int) error {
	if s == nil || s.stickerSets == nil || userID == 0 || len(order) == 0 {
		return nil
	}
	return s.stickerSets.ReorderUserStickerSets(ctx, userID, kind, order, now)
}

func (s *Service) ListUserStickerSets(ctx context.Context, userID int64, kind domain.StickerSetKind, archived *bool, offsetID int64, limit int) ([]domain.UserStickerSet, int, error) {
	if s == nil || s.stickerSets == nil || userID == 0 {
		return nil, 0, nil
	}
	return s.stickerSets.ListUserStickerSets(ctx, userID, kind, archived, offsetID, limit)
}

func normalizeReactionSettings(settings domain.AccountReactionSettings) domain.AccountReactionSettings {
	defaults := domain.DefaultAccountReactionSettings()
	settings.Notify = normalizeNotifySettings(settings.Notify)
	if !settings.DefaultReaction.Valid() {
		settings.DefaultReaction = defaults.DefaultReaction
	}
	settings.PaidPrivacy = normalizePaidPrivacy(settings.PaidPrivacy)
	return settings
}

func normalizeNotifySettings(settings domain.ReactionsNotifySettings) domain.ReactionsNotifySettings {
	if !validNotifyFrom(settings.MessagesFrom) {
		settings.MessagesFrom = domain.ReactionNotifyFromContacts
	}
	if !validNotifyFrom(settings.StoriesFrom) {
		settings.StoriesFrom = domain.ReactionNotifyFromContacts
	}
	if !validNotifyFrom(settings.PollVotesFrom) {
		settings.PollVotesFrom = domain.ReactionNotifyFromContacts
	}
	return settings
}

func validNotifyFrom(value domain.ReactionNotifyFrom) bool {
	switch value {
	case domain.ReactionNotifyFromNone, domain.ReactionNotifyFromContacts, domain.ReactionNotifyFromAll:
		return true
	default:
		return false
	}
}

func normalizePaidPrivacy(privacy domain.PaidReactionPrivacy) domain.PaidReactionPrivacy {
	switch privacy.Kind {
	case domain.PaidReactionPrivacyAnonymous:
		return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyAnonymous}
	case domain.PaidReactionPrivacyPeer:
		if privacy.Peer != nil && privacy.Peer.ID != 0 {
			peer := *privacy.Peer
			return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &peer}
		}
	}
	return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyDefault}
}
