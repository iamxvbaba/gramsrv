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
	"telesrv/internal/identity"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store"
)

// Login errors.
var (
	ErrCodeExpired             = errors.New("phone code expired or not found")
	ErrCodeInvalid             = errors.New("phone code invalid")
	ErrEncryptedMessageInvalid = errors.New("encrypted message invalid")
	ErrExpiresAtInvalid        = errors.New("temporary auth key request expiry invalid")
	ErrTempAuthKeyEmpty        = errors.New("temporary auth key missing or expired")
	ErrTempAuthKeyAlreadyBound = errors.New("temporary auth key already bound")
	ErrAuthKeyPermEmpty        = errors.New("permanent auth key required")
	// ErrLoginCodeDeliveryUnavailable indicates that an existing account's app-code has no available
	// durable message/event/outbox delivery boundary. This is a server misconfiguration and must not
	// degrade into "keep returning sentCode and resend after signIn".
	ErrLoginCodeDeliveryUnavailable = errors.New("login code durable delivery unavailable")
	// ErrLoginCodeDeliveryFailed indicates durable delivery did not succeed. SendCode/ResendCode
	// must also revoke the CodeStore hash just written, so the client does not receive an undeliverable code.
	ErrLoginCodeDeliveryFailed = errors.New("login code durable delivery failed")
	// ErrPhoneNumberInvalid indicates the phone number is empty, non-numeric, or out of range.
	// 0090 changed the users.phone unique constraint to a partial index ignoring empty strings (bot rows
	// have phone=''), so phone validation must be moved up to the auth entry point; otherwise
	// sendCode/signUp could mint unlimited ghost human accounts with phone='' (and, because
	// ByPhone('') short-circuits, they could never log in again).
	ErrPhoneNumberInvalid = errors.New("phone number invalid")
	// ErrSystemUserLoginForbidden indicates a built-in system account was attempted to be bound as a normal business session.
	ErrSystemUserLoginForbidden = errors.New("system user login forbidden")
)

const (
	codeChannelPhone              = store.PhoneCodeChannelPhone
	codeChannelSMS                = store.PhoneCodeChannelSMS
	codeChannelEmailLogin         = store.PhoneCodeChannelEmailLogin
	codeChannelEmailSetupRequired = store.PhoneCodeChannelEmailSetupRequired
	loginCodeRollbackTimeout      = 2 * time.Second
)

// validPhone validates a normalized phone number: 5-32 digits (upper bound aligned with the users.phone column
// width). The core purpose is to reject empty/non-numeric phones (preventing unlimited ghost-account minting
// under the 0090 partial index). The length cap is lenient; exact E.164 digit counts are not required (tests
// often use longer unique phones).
func validPhone(phone string) bool {
	return domain.ValidPhone(phone)
}

func systemUserLoginForbidden(u domain.User) bool {
	// Login-facing callers deliberately use the same non-enumerating error for
	// reserved identities and irreversible tombstones. The durable authorization
	// store repeats the deleted check under the user lock to close the TOCTOU gap.
	return u.Deleted || domain.IsSystemUserID(u.ID)
}

func systemLoginPhoneForbidden(phone string) bool {
	_, ok := domain.SystemUserByPhone(phone)
	return ok
}

// Service implements login/sign-up business logic. By default it keeps the fixed development code; once an
// external provider is configured it generates a random verification code and delivers it via otpdelivery.
// External delivery for existing accounts is an additional channel to the durable 777000 App-code and must
// not replace or weaken the original message fact.
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
	loginCodeTemplate      func() (string, error)
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
	// premiumGrantMonths is the number of Premium months granted by default to newly registered accounts; 0 disables the grant.
	premiumGrantMonths int
	// welcomeMessageIdentity is the shared identity.Store instance; recordWelcomeMessage re-reads it on
	// every call (never cached), so admin panel edits to the login notification template take effect
	// without a restart -- consistent with the identity package's own design contract. When nil this
	// source is skipped and it falls through to welcomeMessage{Phone,Email}Default.
	welcomeMessageIdentity     *identity.Store
	welcomeMessagePhoneDefault string
	welcomeMessageEmailDefault string
	// loginCodeMessageIdentity/loginCodeMessageEnvDefault mirror
	// welcomeMessageIdentity/welcomeMessage{Phone,Email}Default, but for the 777000 login-code delivery
	// message rather than the post-login welcome notification -- see WithLoginCodeMessageTemplate and
	// resolveLoginCodeMessageTemplate. The login-code message is not per-channel, so there is only one
	// env default (rather than one per channel).
	loginCodeMessageIdentity   *identity.Store
	loginCodeMessageEnvDefault string
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

// Option adjusts the login service's optional dependencies.
type Option func(*Service)

// WithLoginMessages writes the official system account's first login message and dialog summary after a new
// user successfully registers. An existing account's app verification code must be persisted during the
// auth.sendCode/resendCode stage via WithLoginCodeDelivery, and must not be resent after signIn succeeds.
func WithLoginMessages(messages store.MessageStore, dialogs store.DialogStore) Option {
	return func(s *Service) {
		s.messages = messages
		s.dialogs = dialogs
	}
}

// WithLoginWelcomeMessages configures the resolution chain for the 777000 login notification message template
// body (see domain.ResolveWelcomeMessageTemplate): the store is read in real time on every recordWelcomeMessage
// call (never cached, so admin panel edits take effect without a restart); phoneDefault/emailDefault are the
// environment variable fallbacks provided by config (Config.WelcomeMessage{Phone,Email}Template), used only when
// the panel has no override. A nil store skips this source entirely.
func WithLoginWelcomeMessages(store *identity.Store, phoneDefault, emailDefault string) Option {
	return func(s *Service) {
		s.welcomeMessageIdentity = store
		s.welcomeMessagePhoneDefault = phoneDefault
		s.welcomeMessageEmailDefault = emailDefault
	}
}

// WithLoginCodeMessageTemplate configures the resolution chain for the 777000 login-code delivery message
// template body (see domain.ResolveLoginCodeMessageTemplate): the store is read in real time on every
// deliverLoginCode/recordLoginMessage call (never cached, so admin panel edits take effect without a restart);
// envDefault is the environment variable fallback provided by config (Config.LoginCodeMessageTemplate), used
// only when the panel has no override. A nil store skips this source entirely. Callers usually pass the same
// *identity.Store instance as WithLoginWelcomeMessages; both read and write the same identity.json.
func WithLoginCodeMessageTemplate(store *identity.Store, envDefault string) Option {
	return func(s *Service) {
		s.loginCodeMessageIdentity = store
		s.loginCodeMessageEnvDefault = envDefault
	}
}

// resolveLoginCodeMessageTemplate resolves the 777000 login-code message template in real time on every call
// (never cached), consistent with recordWelcomeMessage's "always read fresh" contract, so admin panel edits
// take effect without a restart. Every call in this service that goes through
// domain.OfficialLoginCodeMessage must go through here rather than hard-coding its own template copy.
func (s *Service) resolveLoginCodeMessageTemplate() string {
	panelOverride := ""
	if s.loginCodeMessageIdentity != nil {
		if info, err := s.loginCodeMessageIdentity.Get(); err == nil {
			panelOverride = info.LoginCodeMessageTemplate
		}
	}
	return domain.ResolveLoginCodeMessageTemplate(panelOverride, s.loginCodeMessageEnvDefault)
}

// WithLoginCodeDelivery injects the durable delivery boundary for an existing account's app-code. The
// implementation must be idempotent on user_id + phone_code_hash and atomically write the 777000
// message/dialog/user update event/dispatch outbox.
func WithLoginCodeDelivery(delivery store.LoginCodeDeliveryStore) Option {
	return func(s *Service) {
		s.loginCodeDelivery = delivery
	}
}

// WithLoginCodeMessageTemplateResolver reads the effective template for each
// delivery, allowing Admin identity changes to take effect without a restart.
func WithLoginCodeMessageTemplateResolver(resolve func() (string, error)) Option {
	return func(s *Service) { s.loginCodeTemplate = resolve }
}

// WithPasswords lets sign-in stop at SESSION_PASSWORD_NEEDED for 2FA accounts.
func WithPasswords(passwords store.PasswordStore) Option {
	return func(s *Service) {
		s.passwords = passwords
	}
}

// WithBotLogin enables bot token login for auth.importBotAuthorization.
func WithBotLogin(bots store.BotStore) Option {
	return func(s *Service) {
		s.bots = bots
	}
}

// WithPremiumGrant gives newly registered accounts months of Premium by default (0 = disable the grant).
// The equivalent grant for existing accounts is backfilled once by migration 0094.
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

// NewService creates the login service. fixedCode is the fixed development verification code.
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

// BindTempAuthKey validates and records the TDesktop PFS temp->perm auth key binding.
func (s *Service) BindTempAuthKey(ctx context.Context, sessionID int64, binding domain.TempAuthKeyBinding) (domain.TempAuthKeyBindingResult, error) {
	var validated domain.TempAuthKeyBindingResult
	if s.authKeys != nil {
		inner, protocolExpiresAt, result, err := s.validateBindTempAuthKey(ctx, sessionID, binding)
		if err != nil {
			return domain.TempAuthKeyBindingResult{}, err
		}
		validated = result
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
		return domain.TempAuthKeyBindingResult{}, ErrTempAuthKeyEmpty
	}
	if s.tempKeys == nil {
		return validated, nil
	}
	result, err := s.tempKeys.SaveWithState(ctx, binding)
	if err != nil {
		if errors.Is(err, store.ErrTempAuthKeyAlreadyBound) {
			return domain.TempAuthKeyBindingResult{}, ErrTempAuthKeyAlreadyBound
		}
		if errors.Is(err, store.ErrAuthKeyBindingInvalid) {
			return domain.TempAuthKeyBindingResult{}, s.classifyBindingStoreInvalid(ctx, binding)
		}
		return domain.TempAuthKeyBindingResult{}, err
	}
	return result, nil
}

// ResolveAuthKey resolves a bound temp auth_key to its corresponding perm auth_key.
//
// temp->perm is a protocol identity relationship formed by the handshake/binding, entirely independent of
// whether perm is currently logged in. Even if auth.logOut has deleted the authorization, as long as the
// binding still exists, subsequent login RPCs must continue to land on the same perm key; the raw temp key
// must never be treated as a new business identity. Protocol expiry is handled by mtprotoedge, which returns
// -404 and closes the connection before decryption/RPC; here we no longer guess the key type from the
// authorization state.
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

// UserID returns the user the auth_key is currently bound to. found=false when not logged in or when two-step verification is incomplete.
func (s *Service) UserID(ctx context.Context, authKeyID [8]byte) (int64, bool, error) {
	if s == nil || s.auths == nil || s.users == nil {
		return 0, false, nil
	}
	a, found, err := s.auths.ByAuthKey(ctx, authKeyID)
	if err != nil || !found {
		return 0, found, err
	}
	if a.PasswordPending {
		// Two-step verification incomplete: business authorization is treated as not logged in, and only auth.checkPassword may continue.
		return 0, false, nil
	}
	u, userFound, err := s.users.ByID(ctx, a.UserID)
	if err != nil {
		return 0, false, err
	}
	if !userFound || systemUserLoginForbidden(u) {
		// A stale row can exist only after an interrupted/legacy path. Fail closed
		// before it reaches the Router auth cache and retire it opportunistically.
		_ = s.auths.Delete(ctx, authKeyID)
		return 0, false, nil
	}
	return a.UserID, true, nil
}

// PendingPasswordUserID returns the user for an auth_key in the "awaiting two-step verification" state.
// UserID returns not-logged-in for a password_pending auth_key, so auth.checkPassword can still locate the user awaiting verification.
func (s *Service) PendingPasswordUserID(ctx context.Context, authKeyID [8]byte) (int64, bool, error) {
	if s == nil || s.auths == nil || s.users == nil {
		return 0, false, nil
	}
	a, found, err := s.auths.ByAuthKey(ctx, authKeyID)
	if err != nil || !found || !a.PasswordPending {
		return 0, false, err
	}
	u, userFound, err := s.users.ByID(ctx, a.UserID)
	if err != nil {
		return 0, false, err
	}
	if !userFound || systemUserLoginForbidden(u) {
		_ = s.auths.Delete(ctx, authKeyID)
		return 0, false, nil
	}
	return a.UserID, true, nil
}

// CompletePasswordSignIn clears password_pending after two-step verification passes, turning the auth_key into fully authorized.
func (s *Service) CompletePasswordSignIn(ctx context.Context, authKeyID [8]byte, expectedUserID int64) error {
	if s == nil || s.auths == nil {
		return nil
	}
	if expectedUserID == 0 {
		return store.ErrAuthorizationStateChanged
	}
	if err := s.auths.MarkPasswordPassed(ctx, authKeyID, expectedUserID); err != nil {
		return err
	}
	// Revalidate the active account after the CAS promotion before Router caches
	// or binds the session. Account deletion can still linearize immediately
	// after the update and must leave the caller unauthorized.
	if userID, found, err := s.UserID(ctx, authKeyID); err != nil {
		return err
	} else if !found || userID != expectedUserID {
		return ErrSystemUserLoginForbidden
	}
	// This is the finishing step that truly completes login for a 2FA account (finishSignIn deliberately
	// skips the welcome message while password_pending) -- resend the welcome notification.
	if u, found, err := s.users.ByID(ctx, expectedUserID); err == nil && found {
		s.recordWelcomeMessage(ctx, u)
	}
	return nil
}

// SendCode generates a phone_code_hash for phone and, per configuration, selects the development app code,
// the login email code, or the login-email setup-required state, returning the hash.
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
	// A new phone number has no owner/dialog yet, so a 777000 message cannot be created at the issuance
	// stage; an existing account's App-code was already durably committed before the provider branch above.
	return hash, nil
}

func (s *Service) deliverLoginCode(ctx context.Context, userID int64, phoneCodeHash, code string) error {
	if s.loginCodeDelivery == nil {
		return ErrLoginCodeDeliveryUnavailable
	}
	var template string
	if s.loginCodeTemplate != nil {
		resolved, err := s.loginCodeTemplate()
		if err != nil {
			return errors.Join(ErrLoginCodeDeliveryFailed, fmt.Errorf("resolve login code message template: %w", err))
		}
		template = resolved
	} else {
		template = s.resolveLoginCodeMessageTemplate()
	}
	now := time.Now()
	if _, err := s.loginCodeDelivery.DeliverLoginCodeMessage(ctx, domain.LoginCodeDeliveryRequest{
		UserID:        userID,
		PhoneCodeHash: phoneCodeHash,
		Code:          code,
		Template:      template,
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

// ResendCodeForAuthKey additionally validates the initiating auth key for sensitive operations by a
// logged-in user; an ordinary login code has no AuthKeyID scope and behaves the same as ResendCode.
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

// CancelCodeForAuthKey is the cancellation path corresponding to ResendCodeForAuthKey.
func (s *Service) CancelCodeForAuthKey(ctx context.Context, authKeyID [8]byte, phone, phoneCodeHash string) error {
	return s.cancelCode(ctx, authKeyID, phone, phoneCodeHash)
}

// LoginEmailResetAvailable reports whether this deployment can complete the
// SMS fallback promised by auth.resetLoginEmail. Fixed development codes are
// deliberately not a recovery channel.
func (s *Service) LoginEmailResetAvailable() bool {
	return s != nil && s.phoneCodeSender != nil && s.codes != nil && s.users != nil
}

// ConsumeLoginEmailReset authorizes auth.resetLoginEmail with the exact
// email-login hash previously issued for this phone owner. Possession of only
// a phone number is never sufficient to remove an authentication factor.
func (s *Service) ConsumeLoginEmailReset(ctx context.Context, phone, phoneCodeHash string) (int64, error) {
	// Refuse before consuming the email proof or clearing any account state. If
	// there is no real SMS sender, the successor code would be the public
	// development code and could strip the login-email factor.
	if !s.LoginEmailResetAvailable() || s.codes == nil {
		return 0, ErrCodeInvalid
	}
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

// SignIn validates the verification code and attempts to log in.
// needSignUp=true means the code is correct but the user does not exist; the caller should prompt
// registration (the code is not deleted in this case, left for SignUp).
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

// SignInWithEmail handles auth.signIn carrying email_verification. It shares the same login credential
// state machine as SignIn: TDesktop/Android put the email code in email_verification, WebK puts the same
// email code in phone_code; the TL field is only the proof carrier, and the channel recorded at issuance
// indicates the actual delivery channel. All channels must exactly match the issued code and share the
// owner binding, atomic attempt counting, one-time consumption, and 2FA gating.
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

// finishSignIn is the shared finish for both the SMS and email login paths after "code passed, user exists":
// the code has already been atomically consumed by VerifyLogin; here we only handle the 2FA password_pending
// binding. An existing account's app-code message was already persisted and enqueued to the outbox before
// SendCode/ResendCode returned, so it must never be created or resent here; otherwise the real verification
// code for an unfinished login/2FA would fail to reach the old device in time.
func (s *Service) finishSignIn(ctx context.Context, auth domain.Authorization, existing domain.User) (domain.User, domain.Message, bool, error) {
	if systemUserLoginForbidden(existing) {
		return domain.User{}, domain.Message{}, false, ErrSystemUserLoginForbidden
	}
	// For an account with two-step verification: mark the authorization as password_pending before
	// writing, so business authorization rejects that auth_key until auth.checkPassword passes. Never
	// write it as fully authorized first and then return SESSION_PASSWORD_NEEDED; otherwise the client
	// could ignore the error and call business RPCs directly to bypass two-step verification.
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
	// A 2FA account truly completes at CompletePasswordSignIn (when checkPassword passes); sending the
	// welcome message here too would also notify attempts that never actually passed the password check.
	s.recordWelcomeMessage(ctx, existing)
	return existing, domain.Message{}, false, nil
}

// SignUp creates the user and binds the authorization after SignIn determines registration is needed.
// The signUp TL request carries no verification code, so it only consumes a hash atomically marked by a
// correct SignIn/email setup. A direct SendCode->SignUp can never create an account.
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
	// New accounts are granted Premium by default: expiry = registration time + N months (same semantics
	// as migration 0094's backfill for existing accounts). The premium state is derived on the fly by the
	// delivery path from that time.
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
	s.recordWelcomeMessage(ctx, u)
	return u, loginMessage, nil
}

// SignInBot handles auth.importBotAuthorization: validates the bot token and binds the current auth_key to
// the bot account. Token validation must precede bind (bind makes the authorization effective); bots have
// no 2FA, so PasswordPending is always false; no login message is written and no signIn notification is
// pushed (phone-login semantics). Any validation failure uniformly returns domain.ErrBotTokenInvalid,
// without distinguishing the reason, to avoid leaking existence.
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
	// An empty secret (the built-in BotFather) can never log in; the comparison is constant-time.
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
	// check-bind-recheck: SignInBot's "validate secret -> bind" is not atomic, and a concurrent /revoke
	// could swap the secret between the two steps and delete the existing authorization. A new row written
	// by this bind would not be overwritten by that deletion (the deletion happened before the bind) and
	// would escape session revocation. Re-check the secret after bind: if it was swapped, revoke the
	// authorization just written and deny the login, closing the race window.
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

// BindVerifiedLogin binds the current auth_key to a user whose identity has already been verified by an
// external strong factor (such as a passkey), completing authorization directly. A passkey is an
// independent strong factor, so no 2FA password is layered on top (PasswordPending=false), consistent with
// the official "passkey login skips the password step". Verification already happened in the caller
// (passkey assertion verification); here we only bind.
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
	s.recordWelcomeMessage(ctx, u)
	return u, nil
}

// AcceptLoginToken binds the QR login requester's auth_key to the user confirmed by scanning.
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

// LogOut unbinds the current auth_key's authorization.
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
	// Client metadata is a read-only projection. The physical connection's
	// first-frame Get and active-key heartbeat already own the durable orphan
	// lease, so this path must not turn every init/profile read into another
	// last_used_at write.
	key, found, err := s.authKeys.Revalidate(ctx, authKeyID)
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
	return s.auths.DeleteByHash(ctx, userID, hash)
}

func (s *Service) ResetAuthorizations(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error) {
	if s == nil || s.auths == nil || userID == 0 {
		return nil, nil
	}
	if revoker, ok := s.auths.(authorizationRevoker); ok {
		return revoker.RevokeByUserExcept(ctx, userID, keepAuthKeyID)
	}
	return s.auths.DeleteByUserExcept(ctx, userID, keepAuthKeyID)
}

func (s *Service) bind(ctx context.Context, auth domain.Authorization, userID int64) error {
	if s == nil || s.users == nil || s.auths == nil || userID == 0 {
		return ErrSystemUserLoginForbidden
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	if !found || systemUserLoginForbidden(u) {
		return ErrSystemUserLoginForbidden
	}
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
	// Bind is the persistence state boundary for authorization switching: the production store first clears
	// the same auth key's old user update state, then atomically establishes the new user baseline. The RPC
	// layer must not clear the entire key after a successful Bind, otherwise it would also delete the
	// just-established retained-floor checkpoint.
	if err := s.auths.Bind(ctx, auth); err != nil {
		if errors.Is(err, store.ErrAuthKeyNotPermanent) {
			return ErrAuthKeyPermEmpty
		}
		if errors.Is(err, domain.ErrAccountDeleted) || errors.Is(err, domain.ErrUserNotFound) {
			return ErrSystemUserLoginForbidden
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

// recordLoginMessage writes the 777000 login-code message for the "new phone-number channel account"
// bootstrap path (the rec.Channel == codeChannelPhone branch of SignUp) -- that path has no owner/dialog
// yet and cannot use WithLoginCodeDelivery's durable idempotent delivery. Like deliverLoginCode, it
// constructs the message via domain.OfficialLoginCodeMessage and the freshly resolved template (see
// resolveLoginCodeMessageTemplate), rather than each maintaining its own copy of the template/entity
// logic, so admin panel template edits and {{code}} placeholder entity offset fixes take effect here too.
func (s *Service) recordLoginMessage(ctx context.Context, userID int64, code string) (domain.Message, error) {
	if s.messages == nil || s.dialogs == nil {
		return domain.Message{}, nil
	}
	base, err := domain.OfficialLoginCodeMessage(userID, s.resolveLoginCodeMessageTemplate(), code, int(time.Now().Unix()))
	if err != nil {
		return domain.Message{}, err
	}
	msg, err := s.messages.Create(ctx, base)
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

// recordWelcomeMessage writes an unconditional "welcome back" 777000 message on every successful login
// (SignUp and every subsequent SignIn/SignInWithEmail/2FA completion/passkey binding), regardless of
// channel. Best-effort: a failure must never fail the login itself -- unlike recordLoginMessage, it
// carries no secret the caller needs. The template body is resolved in real time on every call (see
// ResolveWelcomeMessageTemplate), so admin panel edits take effect without a restart. The login method is
// determined by the account's confirmed login email (see SignInMethodLabel): email account -> email
// template, otherwise -> phone template.
func (s *Service) recordWelcomeMessage(ctx context.Context, u domain.User) {
	if s == nil || s.messages == nil || s.dialogs == nil {
		return
	}
	loginEmail := ""
	if s.passwords != nil {
		if settings, found, err := s.passwords.GetByUser(ctx, u.ID); err == nil && found {
			loginEmail = settings.LoginEmail
		}
	}
	method := domain.LoginMethodFromLabel(domain.SignInMethodLabel(loginEmail))
	envDefault := s.welcomeMessagePhoneDefault
	if method == domain.LoginMethodEmail {
		envDefault = s.welcomeMessageEmailDefault
	}
	panelOverride := ""
	if s.welcomeMessageIdentity != nil {
		if info, err := s.welcomeMessageIdentity.Get(); err == nil {
			if method == domain.LoginMethodEmail {
				panelOverride = info.WelcomeMessageEmailTemplate
			} else {
				panelOverride = info.WelcomeMessagePhoneTemplate
			}
		}
	}
	template := domain.ResolveWelcomeMessageTemplate(method, panelOverride, envDefault)
	body := domain.RenderWelcomeMessageTemplate(template)
	msg, err := domain.OfficialWelcomeMessage(u.ID, body, int(time.Now().Unix()))
	if err != nil {
		return
	}
	created, err := s.messages.Create(ctx, msg)
	if err != nil {
		return
	}
	_ = s.dialogs.UpsertInbox(ctx, u.ID, domain.Dialog{
		Peer:           created.Peer,
		TopMessage:     created.ID,
		TopMessageDate: created.Date,
	})
}

func (s *Service) validateBindTempAuthKey(ctx context.Context, sessionID int64, binding domain.TempAuthKeyBinding) (mtcrypto.BindAuthKeyInner, int, domain.TempAuthKeyBindingResult, error) {
	if binding.ExpiresAt <= int(time.Now().Unix()) {
		return mtcrypto.BindAuthKeyInner{}, 0, domain.TempAuthKeyBindingResult{}, ErrExpiresAtInvalid
	}
	permID := authKeyIDFromInt64(binding.PermAuthKeyID)
	pair, err := s.authKeys.LoadBindingKeys(ctx, binding.TempAuthKeyID, permID)
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, 0, domain.TempAuthKeyBindingResult{}, err
	}
	// expires_at in auth.bindTempAuthKey is client-supplied and must only attest
	// to a still-live binding. It may never create or reclassify a protocol key;
	// the caller normalizes durable retention to this handshake-authoritative
	// temp.ExpiresAt instead of trusting the client value.
	if !pair.TemporaryFound || pair.Temporary.ExpiresAt <= int(time.Now().Unix()) {
		return mtcrypto.BindAuthKeyInner{}, 0, domain.TempAuthKeyBindingResult{}, ErrTempAuthKeyEmpty
	}

	if !pair.PermanentFound || pair.Permanent.ExpiresAt != 0 {
		return mtcrypto.BindAuthKeyInner{}, 0, domain.TempAuthKeyBindingResult{}, ErrEncryptedMessageInvalid
	}

	inner, err := decryptBindAuthKeyInner(pair.Permanent, binding.EncryptedMessage)
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, 0, domain.TempAuthKeyBindingResult{}, ErrEncryptedMessageInvalid
	}
	if inner.Nonce != binding.Nonce ||
		inner.TempAuthKeyID != authKeyIDInt64(binding.TempAuthKeyID) ||
		inner.PermAuthKeyID != binding.PermAuthKeyID ||
		inner.TempSessionID != sessionID ||
		inner.ExpiresAt != binding.ExpiresAt {
		return mtcrypto.BindAuthKeyInner{}, 0, domain.TempAuthKeyBindingResult{}, ErrEncryptedMessageInvalid
	}
	if pair.Temporary.ExpiresAt <= int(time.Now().Unix()) {
		return mtcrypto.BindAuthKeyInner{}, 0, domain.TempAuthKeyBindingResult{}, ErrTempAuthKeyEmpty
	}
	layer, observationID, err := store.MergeAuthKeyLayerObservations(
		pair.Temporary.Layer, pair.Temporary.LayerObservationID,
		pair.Permanent.Layer, pair.Permanent.LayerObservationID,
	)
	if err != nil {
		return mtcrypto.BindAuthKeyInner{}, 0, domain.TempAuthKeyBindingResult{}, err
	}
	return inner, pair.Temporary.ExpiresAt, domain.TempAuthKeyBindingResult{
		Layer: layer, LayerObservationID: observationID,
	}, nil
}

func (s *Service) classifyBindingStoreInvalid(ctx context.Context, binding domain.TempAuthKeyBinding) error {
	if s == nil || s.authKeys == nil {
		return ErrEncryptedMessageInvalid
	}
	temp, found, err := s.authKeys.Revalidate(ctx, binding.TempAuthKeyID)
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
