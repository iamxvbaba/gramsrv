// Package passkey implements the business orchestration for passkey (WebAuthn) login and
// management: generating challenge options, verifying registration attestations, verifying
// login assertions, and maintaining credential/challenge persistence. Cryptographic
// verification is delegated to internal/webauthn; auth_key binding is delegated to auth.Service.
package passkey

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/webauthn"
)

const (
	defaultChallengeTTL = 5 * time.Minute
	challengeSize       = 32
)

// Service provides passkey login/registration business logic.
type Service struct {
	creds          store.PasskeyStore
	challenges     store.PasskeyChallengeStore
	rpID           string
	rpName         string
	allowedOrigins []string
	dcID           int
	challengeTTL   time.Duration
	now            func() time.Time
}

// Option tweaks the passkey service's optional settings.
type Option func(*Service)

// WithRPName sets the relying-party display name (shown in the authenticator UI).
func WithRPName(name string) Option { return func(s *Service) { s.rpName = name } }

// WithAllowedOrigins sets the whitelist of allowed WebAuthn origins; an empty value means
// origins are not strictly validated (the server usually cannot know the Android apk-key-hash origin in advance).
func WithAllowedOrigins(origins []string) Option {
	return func(s *Service) { s.allowedOrigins = append([]string(nil), origins...) }
}

// WithChallengeTTL overrides the challenge TTL.
func WithChallengeTTL(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.challengeTTL = d
		}
	}
}

// WithClock injects a clock (for tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// NewService creates the passkey service. rpID is the WebAuthn relying-party id (domain);
// dcID is written into user_handle so the client can re-route DCs (single DC in this
// deployment, so it is only echoed back).
func NewService(creds store.PasskeyStore, challenges store.PasskeyChallengeStore, rpID string, dcID int, opts ...Option) *Service {
	s := &Service{
		creds:        creds,
		challenges:   challenges,
		rpID:         rpID,
		rpName:       branding.ProductName(),
		dcID:         dcID,
		challengeTTL: defaultChallengeTTL,
		now:          time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) randomChallenge() ([]byte, error) {
	b := make([]byte, challengeSize)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Service) userHandle(userID int64) string {
	return fmt.Sprintf("%d:%d", s.dcID, userID)
}

func parseUserHandle(h string) (int64, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, domain.ErrPasskeyUserHandleInvalid
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[i+1:]
	}
	id, err := strconv.ParseInt(h, 10, 64)
	if err != nil || id == 0 {
		return 0, domain.ErrPasskeyUserHandleInvalid
	}
	return id, nil
}

// InitRegistration generates registration options (creation options DataJSON) for a logged-in user.
func (s *Service) InitRegistration(ctx context.Context, userID int64, displayName string) ([]byte, error) {
	if s == nil || s.creds == nil || s.challenges == nil || userID == 0 {
		return nil, domain.ErrPasskeyInvalid
	}
	challenge, err := s.randomChallenge()
	if err != nil {
		return nil, err
	}
	if err := s.challenges.SavePasskeyChallenge(ctx, challenge, domain.PasskeyChallenge{
		Purpose:   domain.PasskeyChallengeRegister,
		UserID:    userID,
		ExpiresAt: s.now().Add(s.challengeTTL).Unix(),
	}, s.challengeTTL); err != nil {
		return nil, err
	}
	existing, err := s.creds.ListPasskeysByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	exclude := make([][]byte, 0, len(existing))
	for _, c := range existing {
		exclude = append(exclude, c.CredentialID)
	}
	if displayName == "" {
		displayName = fmt.Sprintf("user%d", userID)
	}
	return webauthn.BuildRegistrationOptions(webauthn.RegistrationParams{
		RPID:        s.rpID,
		RPName:      s.rpName,
		UserID:      []byte(s.userHandle(userID)),
		UserName:    displayName,
		UserDisplay: displayName,
		Challenge:   challenge,
		ExcludeIDs:  exclude,
	})
}

// Register verifies the registration attestation and persists the credential. credentialID is raw bytes (base64url-decoded by rpc).
func (s *Service) Register(ctx context.Context, userID int64, credentialID, clientDataJSON, attestationObject []byte, name string) (domain.PasskeyCredential, error) {
	if s == nil || s.creds == nil || s.challenges == nil || userID == 0 {
		return domain.PasskeyCredential{}, domain.ErrPasskeyInvalid
	}
	challenge, err := webauthn.ChallengeFromClientData(clientDataJSON)
	if err != nil {
		return domain.PasskeyCredential{}, domain.ErrPasskeyInvalid
	}
	meta, found, err := s.challenges.ConsumePasskeyChallenge(ctx, challenge)
	if err != nil {
		return domain.PasskeyCredential{}, err
	}
	if !found || meta.Purpose != domain.PasskeyChallengeRegister || meta.UserID != userID {
		return domain.PasskeyCredential{}, domain.ErrPasskeyChallengeInvalid
	}
	cred, err := webauthn.VerifyRegistration(webauthn.VerifyRegistrationInput{
		ClientDataJSON:    clientDataJSON,
		AttestationObject: attestationObject,
		RPID:              s.rpID,
		Challenge:         challenge,
		AllowedOrigins:    s.allowedOrigins,
	})
	if err != nil {
		return domain.PasskeyCredential{}, fmt.Errorf("%w: %v", domain.ErrPasskeyInvalid, err)
	}
	// credentialID comes from the TL ID/RawID; the credID inside authData wins (guarding against client inconsistency).
	now := s.now().Unix()
	record := domain.PasskeyCredential{
		CredentialID: cred.ID,
		UserID:       userID,
		PublicKey:    cred.COSEPublicKey,
		SignCount:    cred.SignCount,
		AAGUID:       cred.AAGUID,
		Name:         strings.TrimSpace(name),
		CreatedAt:    now,
	}
	if err := s.creds.InsertPasskey(ctx, record); err != nil {
		return domain.PasskeyCredential{}, err
	}
	return record, nil
}

// InitLogin generates login options (request options DataJSON, discoverable, no allowCredentials).
func (s *Service) InitLogin(ctx context.Context) ([]byte, error) {
	if s == nil || s.challenges == nil {
		return nil, domain.ErrPasskeyInvalid
	}
	challenge, err := s.randomChallenge()
	if err != nil {
		return nil, err
	}
	if err := s.challenges.SavePasskeyChallenge(ctx, challenge, domain.PasskeyChallenge{
		Purpose:   domain.PasskeyChallengeLogin,
		ExpiresAt: s.now().Add(s.challengeTTL).Unix(),
	}, s.challengeTTL); err != nil {
		return nil, err
	}
	return webauthn.BuildLoginOptions(webauthn.LoginParams{RPID: s.rpID, Challenge: challenge})
}

// FinishLogin verifies the login assertion and, on success, returns the user id that owns the passkey (auth_key binding is done by the caller).
func (s *Service) FinishLogin(ctx context.Context, credentialID, clientDataJSON, authenticatorData, signature []byte, userHandle string) (int64, error) {
	if s == nil || s.creds == nil || s.challenges == nil {
		return 0, domain.ErrPasskeyInvalid
	}
	handleUserID, err := parseUserHandle(userHandle)
	if err != nil {
		return 0, err
	}
	cred, found, err := s.creds.GetPasskeyByCredentialID(ctx, credentialID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, domain.ErrPasskeyNotFound
	}
	// Cross-check: the user owning the credential must match user_handle.
	if cred.UserID != handleUserID {
		return 0, domain.ErrPasskeyInvalid
	}
	challenge, err := webauthn.ChallengeFromClientData(clientDataJSON)
	if err != nil {
		return 0, domain.ErrPasskeyInvalid
	}
	meta, found, err := s.challenges.ConsumePasskeyChallenge(ctx, challenge)
	if err != nil {
		return 0, err
	}
	if !found || meta.Purpose != domain.PasskeyChallengeLogin {
		return 0, domain.ErrPasskeyChallengeInvalid
	}
	newCount, err := webauthn.VerifyAssertion(webauthn.VerifyAssertionInput{
		COSEPublicKey:     cred.PublicKey,
		ClientDataJSON:    clientDataJSON,
		AuthenticatorData: authenticatorData,
		Signature:         signature,
		RPID:              s.rpID,
		Challenge:         challenge,
		AllowedOrigins:    s.allowedOrigins,
		StoredSignCount:   cred.SignCount,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: %v", domain.ErrPasskeyInvalid, err)
	}
	if err := s.creds.UpdatePasskeyUsage(ctx, cred.CredentialID, newCount, s.now().Unix()); err != nil {
		return 0, err
	}
	return cred.UserID, nil
}

// List returns all of the user's passkeys (management page).
func (s *Service) List(ctx context.Context, userID int64) ([]domain.PasskeyCredential, error) {
	if s == nil || s.creds == nil || userID == 0 {
		return nil, nil
	}
	return s.creds.ListPasskeysByUser(ctx, userID)
}

// Delete deletes one of the user's passkeys. credentialID is raw bytes.
func (s *Service) Delete(ctx context.Context, userID int64, credentialID []byte) (bool, error) {
	if s == nil || s.creds == nil || userID == 0 {
		return false, nil
	}
	return s.creds.DeletePasskey(ctx, userID, credentialID)
}
