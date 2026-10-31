package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/links"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store"
)

var defaultSecureRandom = []byte("telesrv-tdesktop-dev-secure-rand")

const (
	passwordResetWait             = 7 * 24 * time.Hour
	passwordResetRetry            = 24 * time.Hour
	loginEmailVerifyChangePrefix  = "login-email-change:"
	loginEmailVerifySetupPrefix   = "login-email-setup:"
	codeChannelEmailSetup         = "email_setup"
	codeChannelEmailChange        = "email_change"
	codeChannelEmailLogin         = "email_login"
	codeChannelEmailSetupRequired = "email_setup_required"
	codeChannelPasswordRecovery   = "password_recovery"
	passwordRecoveryCodePrefix    = "password-recovery:"
	passwordRecoveryCodeTTL       = 15 * time.Minute
	passwordRecoveryCASRetries    = 32
)

// Service provides account security configuration lookups.
type Service struct {
	passwords   store.PasswordStore
	reactions   store.AccountReactionSettingsStore
	settings    store.AccountSettingsStore
	notify      store.NotifySettingsStore
	stickers    store.StickerCollectionStore
	stickerSets store.UserStickerSetStore
	savedMusic  store.SavedMusicStore
	business    store.BusinessAutomationStore
	// users is used only for login-email phone->user resolution (sendCode detection / login-setup / reset go through phone).
	users                     store.UserStore
	userCache                 store.UserCache
	authorizations            store.AuthorizationStore
	phoneChanges              store.PhoneChangeStore
	lifecycle                 store.AccountLifecycleStore
	tmpPasswords              store.TmpPasswordStore
	publicBaseURL             string
	codes                     store.CodeStore
	phoneChangeCode           string
	phoneChangeCodeTTL        time.Duration
	phoneChangeMaxAttempts    int
	loginEmailSender          otpdelivery.Sender
	phoneCodeSender           otpdelivery.Sender
	phoneCodeLength           int
	loginEmailCodeTTL         time.Duration
	loginEmailCodeMaxAttempts int
	loginEmailCodeLength      int
}

// ServiceOption adjusts the account service dependencies.
type ServiceOption func(*Service)

// WithReactionSettings injects account-level reaction settings persistence.
func WithReactionSettings(reactions store.AccountReactionSettingsStore) ServiceOption {
	return func(s *Service) {
		s.reactions = reactions
	}
}

// WithAccountSettings injects account-level singleton settings (global privacy / TTL / sensitive content / sign-up notification) persistence.
func WithAccountSettings(settings store.AccountSettingsStore) ServiceOption {
	return func(s *Service) {
		s.settings = settings
	}
}

// WithNotifySettings injects per-scope notification settings persistence.
func WithNotifySettings(notify store.NotifySettingsStore) ServiceOption {
	return func(s *Service) {
		s.notify = notify
	}
}

// WithStickerCollections injects personal sticker/GIF collection persistence (faved/recent/gif).
func WithStickerCollections(stickers store.StickerCollectionStore) ServiceOption {
	return func(s *Service) {
		s.stickers = stickers
	}
}

// WithUserStickerSets injects account-level installed sticker set state persistence.
func WithUserStickerSets(stickerSets store.UserStickerSetStore) ServiceOption {
	return func(s *Service) {
		s.stickerSets = stickerSets
	}
}

// WithSavedMusic injects account-level profile music list persistence.
func WithSavedMusic(savedMusic store.SavedMusicStore) ServiceOption {
	return func(s *Service) {
		s.savedMusic = savedMusic
	}
}

// WithBusinessAutomation injects account-level Business Profile/Quick Replies/Chat Links persistence.
func WithBusinessAutomation(business store.BusinessAutomationStore) ServiceOption {
	return func(s *Service) {
		s.business = business
	}
}

// WithUsers injects the user read store, used for login-email phone->user resolution.
func WithUsers(users store.UserStore) ServiceOption {
	return func(s *Service) {
		s.users = users
	}
}

// WithPhoneChange injects the authorization check, one-time code, atomic user+update write,
// and base user cache invalidation dependencies required for phone changes.
func WithPhoneChange(phoneChanges store.PhoneChangeStore, authorizations store.AuthorizationStore, codes store.CodeStore, cache store.UserCache, fixedCode string, ttl time.Duration, maxAttempts int) ServiceOption {
	return func(s *Service) {
		s.phoneChanges = phoneChanges
		s.authorizations = authorizations
		s.codes = codes
		s.userCache = cache
		s.phoneChangeCode = fixedCode
		if ttl > 0 {
			s.phoneChangeCodeTTL = ttl
		}
		if maxAttempts > 0 {
			s.phoneChangeMaxAttempts = maxAttempts
		}
	}
}

func WithPublicBaseURL(baseURL string) ServiceOption {
	return func(s *Service) {
		s.publicBaseURL = links.NormalizeBaseURL(baseURL)
	}
}

func WithLoginEmailVerification(codes store.CodeStore, sender otpdelivery.Sender, ttl time.Duration, maxAttempts, length int) ServiceOption {
	return func(s *Service) {
		s.codes = codes
		s.loginEmailSender = sender
		if ttl > 0 {
			s.loginEmailCodeTTL = ttl
		}
		if maxAttempts > 0 {
			s.loginEmailCodeMaxAttempts = maxAttempts
		}
		if length > 0 {
			s.loginEmailCodeLength = length
		}
	}
}

// WithPhoneCodeDelivery replaces the fixed development code used by the
// change-phone flow with an externally delivered SMS code.
func WithPhoneCodeDelivery(sender otpdelivery.Sender, length int) ServiceOption {
	return func(s *Service) {
		s.phoneCodeSender = sender
		if length > 0 {
			s.phoneCodeLength = length
		}
	}
}

// WithAccountLifecycle installs the single durable account deletion boundary.
// It shares the already configured phone-code delivery and user cache.
func WithAccountLifecycle(lifecycle store.AccountLifecycleStore) ServiceOption {
	return func(s *Service) {
		s.lifecycle = lifecycle
	}
}

// NewService creates the account service.
func NewService(passwords store.PasswordStore, opts ...ServiceOption) *Service {
	s := &Service{
		passwords:                 passwords,
		publicBaseURL:             links.DefaultPublicBaseURL,
		loginEmailCodeTTL:         5 * time.Minute,
		loginEmailCodeMaxAttempts: 5,
		loginEmailCodeLength:      6,
		phoneChangeCodeTTL:        5 * time.Minute,
		phoneChangeMaxAttempts:    5,
		phoneCodeLength:           5,
	}
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

// GetPassword returns the current account's 2FA configuration. When not logged in or with no record,
// it returns the persisted policy's default no-password configuration.
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
	// login_email_pattern is always derived from the confirmed login email and is decoupled from the
	// 2FA recovery email RecoveryEmail (a historical implementation mistakenly wrote the recovery email
	// mask into this field, causing clients to display the recovery email as the login email).
	settings.LoginEmail = normalizeLoginEmail(settings.LoginEmail)
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
	// Recovery has no development-code fallback. If the server cannot both
	// persist and deliver a fresh code, it must report the flow unavailable.
	if s == nil || s.codes == nil || s.loginEmailSender == nil {
		return "", domain.ErrPasswordRecoveryNA
	}
	code, err := randomDigits(s.loginEmailCodeLength)
	if err != nil {
		return "", err
	}
	deliveryID, err := otpdelivery.NewDeliveryID()
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().Add(passwordRecoveryCodeTTL)
	key := passwordRecoveryCodeKey(userID)
	rec := store.PhoneCode{
		Version:         store.PhoneCodeVersionCurrent,
		UserID:          userID,
		Email:           normalizeLoginEmail(settings.RecoveryEmail),
		Code:            code,
		DeliveryID:      deliveryID,
		Channel:         codeChannelPasswordRecovery,
		MaxAttempts:     s.loginEmailCodeMaxAttempts,
		RecoveryBinding: passwordRecoveryBinding(settings),
	}
	if err := s.codes.Set(ctx, key, rec, passwordRecoveryCodeTTL); err != nil {
		return "", err
	}
	if err := deliverOTP(ctx, s.loginEmailSender, otpdelivery.Request{
		DeliveryID: deliveryID,
		Purpose:    otpdelivery.PurposePasswordRecovery,
		Channel:    otpdelivery.ChannelEmail,
		Recipient:  settings.RecoveryEmail,
		Code:       code,
		ExpiresAt:  expiresAt,
	}); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if cleanupErr := s.deletePasswordRecoveryCode(cleanupCtx, key, deliveryID); cleanupErr != nil {
			return "", fmt.Errorf("deliver password recovery code: %w (cleanup failed: %v)", err, cleanupErr)
		}
		return "", fmt.Errorf("deliver password recovery code: %w", err)
	}
	return emailPattern(settings.RecoveryEmail), nil
}

func (s *Service) CheckRecoveryPassword(ctx context.Context, userID int64, code string) error {
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	if !settings.HasPassword || settings.RecoveryEmail == "" {
		return domain.ErrPasswordRecoveryExpired
	}
	return s.verifyPasswordRecoveryCode(ctx, userID, passwordRecoveryBinding(settings), code, false)
}

func (s *Service) RecoverPassword(ctx context.Context, userID int64, code string, input *domain.PasswordInputSettings) error {
	if s == nil || s.passwords == nil || userID == 0 {
		return domain.ErrPasswordRecoveryExpired
	}
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	if !settings.HasPassword || settings.RecoveryEmail == "" {
		return domain.ErrPasswordRecoveryExpired
	}
	binding := passwordRecoveryBinding(settings)
	if input == nil || len(input.NewPasswordHash) == 0 {
		if err := s.verifyPasswordRecoveryCode(ctx, userID, binding, code, true); err != nil {
			return err
		}
		return s.passwords.Save(ctx, userID, defaultPasswordSettings())
	}
	// Reject invalid proofs before doing the comparatively expensive SRP
	// verifier/challenge work. The final consuming check below still decides
	// the single winner if the code changes concurrently.
	if err := s.verifyPasswordRecoveryCode(ctx, userID, binding, code, false); err != nil {
		return err
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
	if err := s.verifyPasswordRecoveryCode(ctx, userID, binding, code, true); err != nil {
		return err
	}
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
	if s != nil && s.codes != nil && userID != 0 {
		if err := s.codes.Del(ctx, passwordRecoveryCodeKey(userID)); err != nil {
			return err
		}
	}
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	settings.EmailUnconfirmedPattern = ""
	if s.passwords != nil {
		return s.passwords.Save(ctx, userID, settings)
	}
	return nil
}

func passwordRecoveryCodeKey(userID int64) string {
	return passwordRecoveryCodePrefix + fmt.Sprint(userID)
}

func passwordRecoveryBinding(settings domain.PasswordSettings) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", settings.SRPID, normalizeLoginEmail(settings.RecoveryEmail))))
	return hex.EncodeToString(sum[:])
}

func (s *Service) deletePasswordRecoveryCode(ctx context.Context, key, deliveryID string) error {
	for attempt := 0; attempt < passwordRecoveryCASRetries; attempt++ {
		snapshot, found, err := s.codes.GetSnapshot(ctx, key)
		if err != nil || !found {
			return err
		}
		if snapshot.Record.Channel != codeChannelPasswordRecovery || snapshot.Record.DeliveryID != deliveryID {
			return nil
		}
		deleted, err := s.codes.CompareAndDelete(ctx, key, snapshot.Revision)
		if err != nil {
			return err
		}
		if deleted {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return fmt.Errorf("delete password recovery code: concurrent state did not settle")
}

// verifyPasswordRecoveryCode keeps check non-consuming while making the final
// recovery a single-winner CAS. Wrong attempts are counted atomically and the
// code is removed at the configured threshold.
func (s *Service) verifyPasswordRecoveryCode(ctx context.Context, userID int64, binding, code string, consume bool) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return domain.ErrRecoveryCodeEmpty
	}
	if s == nil || s.codes == nil || userID == 0 {
		return domain.ErrPasswordRecoveryExpired
	}
	key := passwordRecoveryCodeKey(userID)
	for attempt := 0; attempt < passwordRecoveryCASRetries; attempt++ {
		snapshot, found, err := s.codes.GetSnapshot(ctx, key)
		if err != nil {
			return err
		}
		if !found {
			return domain.ErrPasswordRecoveryExpired
		}
		rec := snapshot.Record
		if rec.Channel != codeChannelPasswordRecovery || rec.UserID != userID || rec.RecoveryBinding == "" || rec.RecoveryBinding != binding {
			deleted, err := s.codes.CompareAndDelete(ctx, key, snapshot.Revision)
			if err != nil {
				return err
			}
			if deleted {
				return domain.ErrPasswordRecoveryExpired
			}
			continue
		}
		if subtle.ConstantTimeCompare([]byte(rec.Code), []byte(code)) == 1 {
			if !consume {
				return nil
			}
			deleted, err := s.codes.CompareAndDelete(ctx, key, snapshot.Revision)
			if err != nil {
				return err
			}
			if deleted {
				return nil
			}
			continue
		}
		maxAttempts := rec.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = s.loginEmailCodeMaxAttempts
		}
		if maxAttempts <= 0 {
			maxAttempts = 1
		}
		rec.Attempts++
		var applied bool
		if rec.Attempts >= maxAttempts {
			applied, err = s.codes.CompareAndDelete(ctx, key, snapshot.Revision)
		} else {
			applied, err = s.codes.CompareAndUpdate(ctx, key, snapshot.Revision, rec)
		}
		if err != nil {
			return err
		}
		if applied {
			return domain.ErrRecoveryCodeInvalid
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return fmt.Errorf("verify password recovery code: concurrent state did not settle")
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

func randomDigits(n int) (string, error) {
	if n <= 0 {
		n = 6
	}
	var out strings.Builder
	out.Grow(n)
	var buf [32]byte
	for out.Len() < n {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		for _, v := range buf {
			// Reject the top six values so every digit has exactly 25 source
			// byte values instead of inheriting modulo bias from 256 %% 10.
			if v >= 250 {
				continue
			}
			out.WriteByte(byte('0') + v%10)
			if out.Len() == n {
				break
			}
		}
	}
	return out.String(), nil
}

func emailPattern(email string) string {
	return domain.MaskEmail(email)
}

func normalizeLoginEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validLoginEmail is the minimal login-email validation: non-empty and containing '@'. The development environment does not apply stricter RFC validation.
func validLoginEmail(email string) bool {
	email = normalizeLoginEmail(email)
	return email != "" && strings.Contains(email, "@")
}

func (s *Service) SendLoginEmailCode(ctx context.Context, userID int64, phone, phoneCodeHash, email string, setup bool) (string, int, error) {
	email = normalizeLoginEmail(email)
	if !validLoginEmail(email) {
		return "", 0, domain.ErrEmailInvalid
	}
	if s == nil || s.codes == nil || s.loginEmailSender == nil {
		return "", 0, domain.ErrEmailNotAllowed
	}
	key := loginEmailVerifyChangePrefix + fmt.Sprint(userID)
	rec := store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		Code:         "",
		Channel:      codeChannelEmailChange,
		PendingEmail: email,
		MaxAttempts:  s.loginEmailCodeMaxAttempts,
	}
	if setup {
		if s.users == nil {
			return "", 0, domain.ErrEmailNotAllowed
		}
		phone = domain.NormalizePhone(phone)
		phoneRec, found, err := s.codes.Get(ctx, phoneCodeHash)
		if err != nil {
			return "", 0, err
		}
		if !found {
			return "", 0, domain.ErrEmailCodeInvalid
		}
		if phoneRec.Version != store.PhoneCodeVersionCurrent || phoneRec.Purpose != "" || phoneRec.Phone != phone ||
			phoneRec.Channel != codeChannelEmailSetupRequired || phoneRec.SignUpVerified {
			return "", 0, domain.ErrEmailInvalid
		}
		targetUserID := int64(0)
		if existingUserID, found, err := s.userIDByPhone(ctx, phone); err != nil {
			return "", 0, err
		} else if found {
			targetUserID = existingUserID
		}
		if phoneRec.IssuedUserID != targetUserID {
			return "", 0, domain.ErrEmailInvalid
		}
		if err := s.ensureLoginEmailAvailable(ctx, targetUserID, email); err != nil {
			return "", 0, err
		}
		key = loginEmailVerifySetupPrefix + phoneCodeHash
		rec.Phone = phone
		rec.Channel = codeChannelEmailSetup
	} else if userID == 0 {
		return "", 0, domain.ErrEmailInvalid
	} else if err := s.ensureLoginEmailAvailable(ctx, userID, email); err != nil {
		return "", 0, err
	}
	code, err := randomDigits(s.loginEmailCodeLength)
	if err != nil {
		return "", 0, err
	}
	rec.Code = code
	deliveryID, err := otpdelivery.NewDeliveryID()
	if err != nil {
		return "", 0, err
	}
	rec.DeliveryID = deliveryID
	expiresAt := time.Now().Add(s.loginEmailCodeTTL)
	if err := s.codes.Set(ctx, key, rec, s.loginEmailCodeTTL); err != nil {
		return "", 0, err
	}
	snapshot, found, err := s.codes.GetSnapshot(ctx, key)
	if err != nil {
		return "", 0, err
	}
	if !found || snapshot.Record.DeliveryID != deliveryID {
		return "", 0, domain.ErrEmailCodeInvalid
	}
	purpose := otpdelivery.PurposeLoginEmailChange
	if setup {
		purpose = otpdelivery.PurposeLoginEmailSetup
	}
	if err := deliverOTP(ctx, s.loginEmailSender, otpdelivery.Request{
		DeliveryID: deliveryID,
		Purpose:    purpose,
		Channel:    otpdelivery.ChannelEmail,
		Recipient:  email,
		Code:       code,
		ExpiresAt:  expiresAt,
	}); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		deleted, cleanupErr := s.codes.CompareAndDelete(cleanupCtx, key, snapshot.Revision)
		if cleanupErr != nil {
			return "", 0, fmt.Errorf("%w; rollback email code: %v", err, cleanupErr)
		}
		_ = deleted // false means a newer concurrent resend owns the key.
		return "", 0, err
	}
	return emailPattern(email), len(code), nil
}

func deliverOTP(ctx context.Context, sender otpdelivery.Sender, req otpdelivery.Request) error {
	_, err := sender.Deliver(ctx, req)
	if errors.Is(err, otpdelivery.ErrOutcomeUnknown) {
		return nil
	}
	return err
}

func (s *Service) VerifyLoginEmail(ctx context.Context, userID int64, phone, phoneCodeHash, code string, setup bool) (string, error) {
	if s == nil || s.codes == nil {
		return "", domain.ErrEmailNotAllowed
	}
	key := loginEmailVerifyChangePrefix + fmt.Sprint(userID)
	if setup {
		key = loginEmailVerifySetupPrefix + phoneCodeHash
	}
	snapshot, found, err := s.codes.GetSnapshot(ctx, key)
	if err != nil {
		return "", err
	}
	if !found {
		return "", domain.ErrEmailCodeInvalid
	}
	rec := snapshot.Record
	if strings.TrimSpace(code) == "" || subtle.ConstantTimeCompare([]byte(rec.Code), []byte(strings.TrimSpace(code))) != 1 {
		return "", s.rejectEmailCode(ctx, key, snapshot)
	}
	email := normalizeLoginEmail(rec.PendingEmail)
	if !validLoginEmail(email) {
		applied, deleteErr := s.codes.CompareAndDelete(ctx, key, snapshot.Revision)
		if deleteErr != nil {
			return "", deleteErr
		}
		if !applied {
			return "", domain.ErrEmailCodeInvalid
		}
		return "", domain.ErrEmailInvalid
	}
	if setup {
		if s.users == nil || rec.Channel != codeChannelEmailSetup {
			return "", domain.ErrEmailCodeInvalid
		}
		phone = domain.NormalizePhone(phone)
		if rec.Phone != phone {
			return "", domain.ErrEmailCodeInvalid
		}
		phoneSnapshot, found, err := s.codes.GetSnapshot(ctx, phoneCodeHash)
		if err != nil {
			return "", err
		}
		phoneRec := phoneSnapshot.Record
		if !found || phoneRec.Version != store.PhoneCodeVersionCurrent || phoneRec.Purpose != "" ||
			phoneRec.Phone != phone || phoneRec.Channel != codeChannelEmailSetupRequired || phoneRec.SignUpVerified {
			return "", domain.ErrEmailCodeInvalid
		}
		targetUserID := int64(0)
		if existingUserID, found, err := s.userIDByPhone(ctx, phone); err != nil {
			return "", err
		} else if found {
			targetUserID = existingUserID
		}
		if phoneRec.IssuedUserID != targetUserID {
			s.invalidateLoginCode(ctx, phoneCodeHash, phone)
			return "", domain.ErrEmailCodeInvalid
		}
		if err := s.ensureLoginEmailAvailable(ctx, targetUserID, email); err != nil {
			return "", err
		}
		// Claim this exact email-code revision before mutating the phone login
		// state. A concurrent resend rotates the revision, so an old verifier
		// can neither consume the new code nor authorize the phone hash.
		claimed, err := s.codes.CompareAndDelete(ctx, key, snapshot.Revision)
		if err != nil {
			return "", err
		}
		if !claimed {
			return "", domain.ErrEmailCodeInvalid
		}
		phoneRec.Channel = codeChannelEmailLogin
		phoneRec.Code = strings.TrimSpace(code)
		phoneRec.Email = email
		phoneRec.PendingEmail = email
		phoneRec.VerifiedEmail = true
		phoneRec.Attempts = 0
		phoneRec.MaxAttempts = s.loginEmailCodeMaxAttempts
		updated, err := s.codes.CompareAndUpdate(ctx, phoneCodeHash, phoneSnapshot.Revision, phoneRec)
		if err != nil {
			return "", err
		}
		if !updated {
			return "", domain.ErrEmailCodeInvalid
		}
		if targetUserID == 0 {
			verified, err := s.codes.VerifyLogin(ctx, phoneCodeHash, phone, phoneRec.Code, true, s.loginEmailCodeMaxAttempts)
			if err != nil {
				return "", err
			}
			if verified.Status != store.LoginCodeVerifyAccepted || verified.Record.IssuedUserID != 0 || !verified.Record.SignUpVerified {
				return "", domain.ErrEmailCodeInvalid
			}
			phoneRec = verified.Record
		}
		afterUserID := int64(0)
		if existingUserID, found, err := s.userIDByPhone(ctx, phone); err != nil {
			return "", err
		} else if found {
			afterUserID = existingUserID
		}
		if afterUserID != targetUserID || phoneRec.IssuedUserID != afterUserID {
			s.invalidateLoginCode(ctx, phoneCodeHash, phone)
			return "", domain.ErrEmailCodeInvalid
		}
		if targetUserID != 0 {
			// Keep the identity selected before SMTP verification. Re-resolving
			// phone at this write boundary would let an A→B transfer attach A's
			// verified factor to B.
			if err := s.SetLoginEmail(ctx, targetUserID, email); err != nil {
				return "", err
			}
			finalUserID := int64(0)
			if existingUserID, found, err := s.userIDByPhone(ctx, phone); err != nil {
				return "", err
			} else if found {
				finalUserID = existingUserID
			}
			if finalUserID != targetUserID {
				s.invalidateLoginCode(ctx, phoneCodeHash, phone)
				return "", domain.ErrEmailCodeInvalid
			}
		}
		return email, nil
	}
	if err := s.ensureLoginEmailAvailable(ctx, userID, email); err != nil {
		return "", err
	}
	claimed, err := s.codes.CompareAndDelete(ctx, key, snapshot.Revision)
	if err != nil {
		return "", err
	}
	if !claimed {
		return "", domain.ErrEmailCodeInvalid
	}
	if err := s.SetLoginEmail(ctx, userID, email); err != nil {
		return "", err
	}
	return email, nil
}

func (s *Service) rejectEmailCode(ctx context.Context, key string, snapshot store.PhoneCodeSnapshot) error {
	rec := snapshot.Record
	rec.Attempts++
	max := rec.MaxAttempts
	if max <= 0 {
		max = s.loginEmailCodeMaxAttempts
	}
	if max > 0 && rec.Attempts >= max {
		if _, err := s.codes.CompareAndDelete(ctx, key, snapshot.Revision); err != nil {
			return err
		}
		return domain.ErrEmailCodeInvalid
	}
	if _, err := s.codes.CompareAndUpdate(ctx, key, snapshot.Revision, rec); err != nil {
		return err
	}
	return domain.ErrEmailCodeInvalid
}

func (s *Service) invalidateLoginCode(ctx context.Context, hash, phone string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = s.codes.InvalidateLoginCode(cleanupCtx, hash, phone)
}

// SetLoginEmail writes the login email for an authenticated user (authed's emailVerifyPurposeLoginChange).
// It can be set even without 2FA: an account_passwords row can carry just the login email with has_password=false.
func (s *Service) SetLoginEmail(ctx context.Context, userID int64, email string) error {
	if s == nil || s.passwords == nil || userID == 0 {
		return domain.ErrEmailInvalid
	}
	email = normalizeLoginEmail(email)
	if !validLoginEmail(email) {
		return domain.ErrEmailInvalid
	}
	if err := s.ensureLoginEmailAvailable(ctx, userID, email); err != nil {
		return err
	}
	settings, err := s.GetPasswordWithoutRefresh(ctx, userID)
	if err != nil {
		return err
	}
	settings.LoginEmail = email
	settings.LoginEmailPattern = emailPattern(email)
	return s.passwords.Save(ctx, userID, settings)
}

// ValidLoginEmail exposes the same normalization/shape gate to trusted
// administrative dry-runs without duplicating the address policy.
func (s *Service) ValidLoginEmail(email string) bool {
	return validLoginEmail(normalizeLoginEmail(email))
}

// LoginEmail returns the raw login email of an authenticated user (used by verifyEmail to echo emailVerified.email).
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
	return normalizeLoginEmail(settings.LoginEmail), true, nil
}

// LoginEmailByPhone returns the raw login email by phone number, so auth.sendCode can detect whether to deliver to email instead.
func (s *Service) LoginEmailByPhone(ctx context.Context, phone string) (string, bool, error) {
	userID, found, err := s.userIDByPhone(ctx, phone)
	if err != nil || !found {
		return "", false, err
	}
	return s.LoginEmail(ctx, userID)
}

// ClearLoginEmail clears the factor on the exact account selected by the
// preceding reset-code consume. Authentication factors must never be mutated
// through a second phone→user lookup.
func (s *Service) ClearLoginEmail(ctx context.Context, userID int64) error {
	if s == nil || s.passwords == nil || userID == 0 {
		return domain.ErrEmailInvalid
	}
	settings, found, err := s.passwords.GetByUser(ctx, userID)
	if err != nil || !found {
		return err
	}
	settings.LoginEmail = ""
	settings.LoginEmailPattern = ""
	return s.passwords.Save(ctx, userID, settings)
}

func (s *Service) ensureLoginEmailAvailable(ctx context.Context, userID int64, email string) error {
	if s == nil || s.passwords == nil {
		return domain.ErrEmailInvalid
	}
	email = normalizeLoginEmail(email)
	if !validLoginEmail(email) {
		return domain.ErrEmailInvalid
	}
	ownerUserID, found, err := s.passwords.LoginEmailOwner(ctx, email)
	if err != nil || !found {
		return err
	}
	if ownerUserID != userID {
		return domain.ErrEmailOccupied
	}
	return nil
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

// GetAccountSettings returns the account-level singleton settings (falls back to defaults when not persisted).
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

// GetAccountSettingsBatch is the bounded cold loader behind the RPC read
// model. Missing rows are returned as explicit defaults so they are negative
// cached instead of being queried again.
func (s *Service) GetAccountSettingsBatch(ctx context.Context, userIDs []int64) (map[int64]domain.AccountSettings, error) {
	out := make(map[int64]domain.AccountSettings, len(userIDs))
	for _, userID := range userIDs {
		if userID > 0 {
			out[userID] = domain.DefaultAccountSettings()
		}
	}
	if s == nil || s.settings == nil || len(out) == 0 {
		return out, nil
	}
	if batch, ok := s.settings.(store.AccountSettingsBatchStore); ok {
		loaded, err := batch.GetAccountSettingsBatch(ctx, userIDs)
		if err != nil {
			return nil, err
		}
		for userID, settings := range loaded {
			out[userID] = settings
		}
		return out, nil
	}
	for userID := range out {
		settings, found, err := s.settings.GetAccountSettings(ctx, userID)
		if err != nil {
			return nil, err
		}
		if found {
			out[userID] = settings
		}
	}
	return out, nil
}

// SetGlobalPrivacy persists the account's global privacy switches and returns the merged complete settings.
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

// SetAccountTTL persists the account self-destruct period (clamped >0).
func (s *Service) SetAccountTTL(ctx context.Context, userID int64, days int) (domain.AccountSettings, error) {
	settings, err := s.GetAccountSettings(ctx, userID)
	if err != nil {
		return domain.AccountSettings{}, err
	}
	settings.AccountTTLDays = days
	settings.AccountTTLDays = settings.NormalizedTTLDays()
	return s.saveAccountSettings(ctx, userID, settings)
}

// SetSensitiveContent persists the sensitive-content viewing switch.
func (s *Service) SetSensitiveContent(ctx context.Context, userID int64, enabled bool) (domain.AccountSettings, error) {
	settings, err := s.GetAccountSettings(ctx, userID)
	if err != nil {
		return domain.AccountSettings{}, err
	}
	settings.SensitiveContentEnabled = enabled
	return s.saveAccountSettings(ctx, userID, settings)
}

// SetContactSignUpSilent persists "whether to silently notify on contact sign-up".
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

// ForumTopicNotifySettings loads explicit preferences for a bounded authorized topic page.
func (s *Service) ForumTopicNotifySettings(ctx context.Context, ownerUserID int64, peer domain.Peer, topicIDs []int) (map[int]domain.PeerNotifySettings, error) {
	if s == nil || s.notify == nil || ownerUserID <= 0 {
		return nil, errors.New("forum topic notification store is unavailable")
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID <= 0 || len(topicIDs) > 200 {
		return nil, errors.New("invalid forum topic notification scope")
	}
	for _, id := range topicIDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return nil, errors.New("invalid forum topic identifier")
		}
	}
	return s.notify.GetForumTopicNotifySettings(ctx, ownerUserID, peer, topicIDs)
}

// GetNotifySettings returns one scope's explicit notification preferences.
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

// SaveNotifySettings persists one scope's notification settings.
func (s *Service) SaveNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error {
	if s == nil || s.notify == nil || ownerUserID == 0 {
		return nil
	}
	return s.notify.SaveNotifySettings(ctx, ownerUserID, scope, settings)
}

// ResetNotifySettings clears all of the user's scope notification settings (restoring defaults).
func (s *Service) ResetNotifySettings(ctx context.Context, ownerUserID int64) error {
	if s == nil || s.notify == nil || ownerUserID == 0 {
		return nil
	}
	return s.notify.ResetNotifySettings(ctx, ownerUserID)
}

// PeerNotifySettings returns the whole-peer notification settings for a batch of peers (dialog list projection).
func (s *Service) PeerNotifySettings(ctx context.Context, ownerUserID int64, peers []domain.Peer) (map[domain.Peer]domain.PeerNotifySettings, error) {
	if s == nil || s.notify == nil || ownerUserID == 0 || len(peers) == 0 {
		return nil, nil
	}
	return s.notify.GetPeerNotifySettings(ctx, ownerUserID, peers)
}

// AllPeerNotifySettings returns all of the user's whole-peer notification settings in one call (the load source for the per-user notify cache).
func (s *Service) AllPeerNotifySettings(ctx context.Context, ownerUserID int64) (map[domain.Peer]domain.PeerNotifySettings, error) {
	if s == nil || s.notify == nil || ownerUserID == 0 {
		return nil, nil
	}
	return s.notify.AllPeerNotifySettings(ctx, ownerUserID)
}

// ListNotifyExceptions lists all of the user's per-peer non-default notification settings (getNotifyExceptions).
func (s *Service) ListNotifyExceptions(ctx context.Context, ownerUserID int64) ([]domain.NotifyException, error) {
	if s == nil || s.notify == nil || ownerUserID == 0 {
		return nil, nil
	}
	return s.notify.ListNotifyExceptions(ctx, ownerUserID)
}

// SaveStickerCollectionItem adds or removes an item in the faved/recent/GIF collection (newest on top, truncated by the per-kind upper bound).
func (s *Service) SaveStickerCollectionItem(ctx context.Context, userID int64, kind domain.StickerCollectionKind, documentID int64, unsave bool, now int) error {
	if s == nil || s.stickers == nil || userID == 0 {
		return nil
	}
	return s.stickers.SaveStickerCollectionItem(ctx, userID, kind, documentID, unsave, now, domain.MaxStickerCollectionItems(kind))
}

// ListStickerCollection returns one kind of personal sticker collection (newest first).
func (s *Service) ListStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind, limit int) ([]domain.StickerCollectionItem, error) {
	if s == nil || s.stickers == nil || userID == 0 {
		return nil, nil
	}
	return s.stickers.ListStickerCollection(ctx, userID, kind, limit)
}

// ClearStickerCollection clears one kind of personal sticker collection.
func (s *Service) ClearStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind) error {
	if s == nil || s.stickers == nil || userID == 0 {
		return nil
	}
	return s.stickers.ClearStickerCollection(ctx, userID, kind)
}

// InstallUserStickerSet installs or reactivates a sticker set; the installed state is a per-user fact.
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
