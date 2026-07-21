package telegramlogin

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"telesrv/internal/domain"
)

const defaultIDTokenTTL = time.Hour

type SigningKeyMaterial struct {
	Algorithm    domain.TelegramLoginSigningAlgorithm
	KeyID        string
	PrivateKey   any
	Active       bool
	PublishUntil time.Time
}

type signingKey struct {
	algorithm    domain.TelegramLoginSigningAlgorithm
	jwaAlgorithm jwa.SignatureAlgorithm
	keyID        string
	private      jwk.Key
	public       jwk.Key
	active       bool
	publishUntil time.Time
}

// SigningKeyRing owns no mutable crypto state. Rotation is performed by
// constructing a new ring containing the new active key and old public keys
// with a PublishUntil at least as long as the maximum ID-token lifetime.
type SigningKeyRing struct {
	keys   []signingKey
	active map[domain.TelegramLoginSigningAlgorithm]signingKey
	now    func() time.Time
}

func NewSigningKeyRing(materials []SigningKeyMaterial, now func() time.Time) (*SigningKeyRing, error) {
	if len(materials) == 0 {
		return nil, errors.New("telegram login signing key ring is empty")
	}
	if now == nil {
		now = time.Now
	}
	ring := &SigningKeyRing{
		keys:   make([]signingKey, 0, len(materials)),
		active: make(map[domain.TelegramLoginSigningAlgorithm]signingKey),
		now:    now,
	}
	seenKeyIDs := make(map[string]struct{}, len(materials))
	for _, material := range materials {
		key, err := importSigningKey(material)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seenKeyIDs[key.keyID]; duplicate {
			return nil, fmt.Errorf("duplicate telegram login signing kid %q", key.keyID)
		}
		seenKeyIDs[key.keyID] = struct{}{}
		if key.active {
			if _, duplicate := ring.active[key.algorithm]; duplicate {
				return nil, fmt.Errorf("multiple active telegram login signing keys for %s", key.algorithm)
			}
			ring.active[key.algorithm] = key
		}
		ring.keys = append(ring.keys, key)
	}
	if len(ring.active) == 0 {
		return nil, errors.New("telegram login signing key ring has no active key")
	}
	return ring, nil
}

func importSigningKey(material SigningKeyMaterial) (signingKey, error) {
	if !material.Algorithm.Valid() || material.PrivateKey == nil {
		return signingKey{}, fmt.Errorf("invalid telegram login signing key material")
	}
	if material.Algorithm == domain.TelegramLoginSigningES256K && !telegramLoginES256KEnabled {
		return signingKey{}, errors.New("telegram login ES256K requires a build with -tags jwx_es256k")
	}
	if err := validateRawSigningKey(material.Algorithm, material.PrivateKey); err != nil {
		return signingKey{}, err
	}
	privateKey, err := jwk.Import(material.PrivateKey)
	if err != nil {
		return signingKey{}, fmt.Errorf("import telegram login %s private key: %w", material.Algorithm, err)
	}
	if err := privateKey.Validate(); err != nil {
		return signingKey{}, fmt.Errorf("validate telegram login %s private JWK: %w", material.Algorithm, err)
	}
	publicKey, err := privateKey.PublicKey()
	if err != nil {
		return signingKey{}, fmt.Errorf("derive telegram login %s public JWK: %w", material.Algorithm, err)
	}
	thumbprint, err := publicKey.Thumbprint(crypto.SHA256)
	if err != nil {
		return signingKey{}, fmt.Errorf("thumbprint telegram login %s public JWK: %w", material.Algorithm, err)
	}
	keyID := strings.TrimSpace(material.KeyID)
	if keyID == "" {
		keyID = base64.RawURLEncoding.EncodeToString(thumbprint)
	}
	if len(keyID) > 128 || strings.IndexFunc(keyID, func(r rune) bool { return r <= 0x20 || r == 0x7f }) >= 0 {
		return signingKey{}, fmt.Errorf("invalid telegram login signing kid")
	}
	jwaAlgorithm, err := telegramLoginJWA(material.Algorithm)
	if err != nil {
		return signingKey{}, err
	}
	for _, key := range []jwk.Key{privateKey, publicKey} {
		if err := key.Set(jwk.KeyIDKey, keyID); err != nil {
			return signingKey{}, fmt.Errorf("set telegram login signing kid: %w", err)
		}
		if err := key.Set(jwk.AlgorithmKey, jwaAlgorithm); err != nil {
			return signingKey{}, fmt.Errorf("set telegram login signing algorithm: %w", err)
		}
		if err := key.Set(jwk.KeyUsageKey, "sig"); err != nil {
			return signingKey{}, fmt.Errorf("set telegram login signing use: %w", err)
		}
	}
	return signingKey{
		algorithm: material.Algorithm, jwaAlgorithm: jwaAlgorithm, keyID: keyID,
		private: privateKey, public: publicKey, active: material.Active,
		publishUntil: material.PublishUntil.UTC(),
	}, nil
}

func validateRawSigningKey(algorithm domain.TelegramLoginSigningAlgorithm, raw any) error {
	switch algorithm {
	case domain.TelegramLoginSigningRS256:
		key, ok := rsaPrivateKey(raw)
		if !ok || key.N == nil || key.N.BitLen() < 2048 || key.E < 3 {
			return errors.New("telegram login RS256 requires an RSA private key of at least 2048 bits")
		}
		if err := key.Validate(); err != nil {
			return fmt.Errorf("validate telegram login RSA private key: %w", err)
		}
	case domain.TelegramLoginSigningES256:
		key, ok := ecdsaPrivateKey(raw)
		if !ok || key.Curve != elliptic.P256() || key.D == nil || key.X == nil || key.Y == nil {
			return errors.New("telegram login ES256 requires a P-256 ECDSA private key")
		}
	case domain.TelegramLoginSigningEdDSA:
		key, ok := raw.(ed25519.PrivateKey)
		if !ok || len(key) != ed25519.PrivateKeySize {
			return errors.New("telegram login EdDSA requires an Ed25519 private key")
		}
	case domain.TelegramLoginSigningES256K:
		key, ok := ecdsaPrivateKey(raw)
		if !ok || key.Curve == nil || key.Curve.Params() == nil ||
			!strings.EqualFold(key.Curve.Params().Name, "secp256k1") || key.D == nil || key.X == nil || key.Y == nil {
			return errors.New("telegram login ES256K requires a secp256k1 ECDSA private key")
		}
	default:
		return domain.ErrTelegramLoginClientInvalid
	}
	return nil
}

func rsaPrivateKey(raw any) (*rsa.PrivateKey, bool) {
	switch key := raw.(type) {
	case *rsa.PrivateKey:
		return key, key != nil
	case rsa.PrivateKey:
		return &key, true
	default:
		return nil, false
	}
}

func ecdsaPrivateKey(raw any) (*ecdsa.PrivateKey, bool) {
	switch key := raw.(type) {
	case *ecdsa.PrivateKey:
		return key, key != nil
	case ecdsa.PrivateKey:
		return &key, true
	default:
		return nil, false
	}
}

func telegramLoginJWA(algorithm domain.TelegramLoginSigningAlgorithm) (jwa.SignatureAlgorithm, error) {
	switch algorithm {
	case domain.TelegramLoginSigningRS256:
		return jwa.RS256(), nil
	case domain.TelegramLoginSigningES256:
		return jwa.ES256(), nil
	case domain.TelegramLoginSigningEdDSA:
		return jwa.EdDSA(), nil
	case domain.TelegramLoginSigningES256K:
		if telegramLoginES256KEnabled {
			return jwa.ES256K(), nil
		}
		return jwa.EmptySignatureAlgorithm(), errors.New("telegram login ES256K is disabled in this build")
	default:
		return jwa.EmptySignatureAlgorithm(), domain.ErrTelegramLoginClientInvalid
	}
}

func (r *SigningKeyRing) SupportedAlgorithms() []string {
	if r == nil {
		return nil
	}
	ordered := make([]string, 0, len(r.active))
	for _, algorithm := range []domain.TelegramLoginSigningAlgorithm{
		domain.TelegramLoginSigningRS256,
		domain.TelegramLoginSigningES256,
		domain.TelegramLoginSigningEdDSA,
		domain.TelegramLoginSigningES256K,
	} {
		if _, ok := r.active[algorithm]; ok {
			ordered = append(ordered, string(algorithm))
		}
	}
	return ordered
}

// ActiveAlgorithms returns the algorithms that can sign new tokens on this
// instance. Callers use it to prevent durable client configuration from
// selecting an algorithm without an active private key.
func (r *SigningKeyRing) ActiveAlgorithms() []domain.TelegramLoginSigningAlgorithm {
	if r == nil {
		return nil
	}
	ordered := make([]domain.TelegramLoginSigningAlgorithm, 0, len(r.active))
	for _, algorithm := range []domain.TelegramLoginSigningAlgorithm{
		domain.TelegramLoginSigningRS256,
		domain.TelegramLoginSigningES256,
		domain.TelegramLoginSigningEdDSA,
		domain.TelegramLoginSigningES256K,
	} {
		if _, ok := r.active[algorithm]; ok {
			ordered = append(ordered, algorithm)
		}
	}
	return ordered
}

func (r *SigningKeyRing) JWKS() ([]byte, string, error) {
	if r == nil {
		return nil, "", errors.New("telegram login signing key ring is nil")
	}
	now := r.now().UTC()
	set := jwk.NewSet()
	for _, key := range r.keys {
		if !key.active && (key.publishUntil.IsZero() || !now.Before(key.publishUntil)) {
			continue
		}
		clone, err := key.public.Clone()
		if err != nil {
			return nil, "", fmt.Errorf("clone telegram login public JWK: %w", err)
		}
		if err := set.AddKey(clone); err != nil {
			return nil, "", fmt.Errorf("add telegram login public JWK: %w", err)
		}
	}
	body, err := json.Marshal(set)
	if err != nil {
		return nil, "", fmt.Errorf("marshal telegram login JWKS: %w", err)
	}
	sum := sha256.Sum256(body)
	return body, `"` + base64.RawURLEncoding.EncodeToString(sum[:]) + `"`, nil
}

func (r *SigningKeyRing) sign(algorithm domain.TelegramLoginSigningAlgorithm, token jwt.Token) (string, error) {
	if r == nil || token == nil {
		return "", errors.New("telegram login ID token signer is unavailable")
	}
	key, ok := r.active[algorithm]
	if !ok {
		return "", fmt.Errorf("no active telegram login signing key for %s", algorithm)
	}
	signed, err := jwt.Sign(token, jwt.WithKey(key.jwaAlgorithm, key.private))
	if err != nil {
		return "", fmt.Errorf("sign telegram login ID token with %s: %w", algorithm, err)
	}
	return string(signed), nil
}

type IDTokenIssuerConfig struct {
	Issuer    string
	TTL       time.Duration
	Now       func() time.Time
	AllowHTTP bool
}

type IDTokenIssuer struct {
	issuer string
	ttl    time.Duration
	now    func() time.Time
	keys   *SigningKeyRing
}

func (i *IDTokenIssuer) Issuer() string {
	if i == nil {
		return ""
	}
	return i.issuer
}

func (i *IDTokenIssuer) TTL() time.Duration {
	if i == nil {
		return 0
	}
	return i.ttl
}

func (i *IDTokenIssuer) SupportedAlgorithms() []string {
	if i == nil {
		return nil
	}
	return i.keys.SupportedAlgorithms()
}

func (i *IDTokenIssuer) JWKS() ([]byte, string, error) {
	if i == nil {
		return nil, "", errors.New("telegram login ID token issuer is nil")
	}
	return i.keys.JWKS()
}

func NewIDTokenIssuer(keys *SigningKeyRing, cfg IDTokenIssuerConfig) (*IDTokenIssuer, error) {
	if keys == nil {
		return nil, errors.New("telegram login signing key ring is required")
	}
	issuer, err := NormalizeWebOrigin(cfg.Issuer, cfg.AllowHTTP)
	if err != nil {
		return nil, fmt.Errorf("telegram login ID token issuer: %w", err)
	}
	if cfg.TTL == 0 {
		cfg.TTL = defaultIDTokenTTL
	}
	if cfg.TTL < time.Minute || cfg.TTL > 24*time.Hour {
		return nil, errors.New("telegram login ID token TTL is outside the bounded range")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &IDTokenIssuer{issuer: issuer, ttl: cfg.TTL, now: cfg.Now, keys: keys}, nil
}

func (i *IDTokenIssuer) Issue(request domain.TelegramLoginRequest) (string, error) {
	if i == nil || request.Status != domain.TelegramLoginRequestApproved || request.AuthorizedUserID <= 0 ||
		request.ClientID == "" || request.ApprovedAt.IsZero() {
		return "", domain.ErrTelegramLoginRequestInvalid
	}
	if err := domain.ValidateTelegramLoginScopes(request.Scopes, request.SigningAlgorithm); err != nil {
		return "", err
	}
	identity := domain.TelegramLoginIdentitySnapshot{
		UserID: request.AuthorizedUserID, Name: request.ProfileName, GivenName: request.GivenName,
		FamilyName: request.FamilyName, PreferredUsername: request.PreferredUsername,
		Picture: request.Picture, PhoneNumber: request.PhoneNumber,
	}
	identity, err := identity.Sanitized(request.Requests(domain.TelegramLoginScopeProfile), request.PhoneShared)
	if err != nil {
		return "", err
	}
	now := i.now().UTC()
	builder := jwt.NewBuilder().
		Issuer(i.issuer).
		Audience([]string{request.ClientID}).
		Subject(fmt.Sprintf("%d", identity.UserID)).
		IssuedAt(now).
		Expiration(now.Add(i.ttl))
	if request.Nonce != "" {
		builder.Claim("nonce", request.Nonce)
	}
	if request.Requests(domain.TelegramLoginScopeProfile) {
		builder.Claim("id", identity.UserID).
			Claim("name", identity.Name).
			Claim("given_name", identity.GivenName)
		if identity.FamilyName != "" {
			builder.Claim("family_name", identity.FamilyName)
		}
		if identity.PreferredUsername != "" {
			builder.Claim("preferred_username", identity.PreferredUsername)
		}
		if identity.Picture != "" {
			builder.Claim("picture", identity.Picture)
		}
	}
	if request.Requests(domain.TelegramLoginScopePhone) && request.PhoneShared {
		builder.Claim("phone_number", identity.PhoneNumber).
			Claim("phone_number_verified", true)
	}
	token, err := builder.Build()
	if err != nil {
		return "", fmt.Errorf("build telegram login ID token: %w", err)
	}
	return i.keys.sign(request.SigningAlgorithm, token)
}
