package telegramlogin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	defaultRequestTTL = 5 * time.Minute
	defaultCodeTTL    = 2 * time.Minute
)

var telegramLoginMatchCodePool = []string{
	"🍏", "🍊", "🍋", "🍇", "🍉", "🍒", "🥝", "🥕",
	"🚗", "🚲", "✈️", "🚀", "⛵", "🏠", "🏰", "⛺",
	"⚽", "🏀", "🎾", "🎲", "🎸", "🎹", "📷", "💡",
}

type Config struct {
	Issuer                     string
	AppScheme                  string
	AllowHTTP                  bool
	ClientSecretPepper         []byte
	SupportedSigningAlgorithms []domain.TelegramLoginSigningAlgorithm
	RequestTTL                 time.Duration
	CodeTTL                    time.Duration
	Now                        func() time.Time
}

type Service struct {
	store               store.TelegramLoginStore
	sealer              *CodeSealer
	issuer              string
	appScheme           string
	allowHTTP           bool
	clientSecretPepper  []byte
	signingAlgorithms   []domain.TelegramLoginSigningAlgorithm
	signingAlgorithmSet map[domain.TelegramLoginSigningAlgorithm]struct{}
	requestTTL          time.Duration
	codeTTL             time.Duration
	now                 func() time.Time
}

func NewService(loginStore store.TelegramLoginStore, sealer *CodeSealer, cfg Config) (*Service, error) {
	if loginStore == nil || sealer == nil || len(cfg.ClientSecretPepper) < 32 {
		return nil, fmt.Errorf("telegram login dependencies are incomplete")
	}
	issuer, err := NormalizeWebOrigin(cfg.Issuer, cfg.AllowHTTP)
	if err != nil {
		return nil, fmt.Errorf("telegram login issuer: %w", err)
	}
	if !validAppScheme(cfg.AppScheme) {
		return nil, fmt.Errorf("telegram login app scheme is invalid")
	}
	if cfg.RequestTTL == 0 {
		cfg.RequestTTL = defaultRequestTTL
	}
	if cfg.CodeTTL == 0 {
		cfg.CodeTTL = defaultCodeTTL
	}
	if cfg.RequestTTL < time.Minute || cfg.RequestTTL > 15*time.Minute || cfg.CodeTTL < 30*time.Second || cfg.CodeTTL > 10*time.Minute {
		return nil, fmt.Errorf("telegram login ttl is outside the bounded range")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	var signingAlgorithmSet map[domain.TelegramLoginSigningAlgorithm]struct{}
	if len(cfg.SupportedSigningAlgorithms) > 0 {
		signingAlgorithmSet = make(map[domain.TelegramLoginSigningAlgorithm]struct{}, len(cfg.SupportedSigningAlgorithms))
		for _, algorithm := range cfg.SupportedSigningAlgorithms {
			if !algorithm.Valid() {
				return nil, fmt.Errorf("telegram login supported signing algorithm is invalid")
			}
			signingAlgorithmSet[algorithm] = struct{}{}
		}
	}
	return &Service{
		store: loginStore, sealer: sealer, issuer: issuer, appScheme: strings.ToLower(cfg.AppScheme),
		allowHTTP:           cfg.AllowHTTP,
		clientSecretPepper:  append([]byte(nil), cfg.ClientSecretPepper...),
		signingAlgorithms:   append([]domain.TelegramLoginSigningAlgorithm(nil), cfg.SupportedSigningAlgorithms...),
		signingAlgorithmSet: signingAlgorithmSet,
		requestTTL:          cfg.RequestTTL, codeTTL: cfg.CodeTTL, now: cfg.Now,
	}, nil
}

func (s *Service) signingAlgorithmSupported(algorithm domain.TelegramLoginSigningAlgorithm) bool {
	if !algorithm.Valid() {
		return false
	}
	if s.signingAlgorithmSet == nil {
		return true
	}
	_, ok := s.signingAlgorithmSet[algorithm]
	return ok
}

func (s *Service) defaultSigningAlgorithm() (domain.TelegramLoginSigningAlgorithm, bool) {
	if s.signingAlgorithmSupported(domain.TelegramLoginSigningRS256) {
		return domain.TelegramLoginSigningRS256, true
	}
	if len(s.signingAlgorithms) > 0 {
		return s.signingAlgorithms[0], true
	}
	return "", false
}

func validAppScheme(value string) bool {
	if value == "" || strings.ToLower(value) != value {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') || (i > 0 && (r == '+' || r == '-' || r == '.')) {
			continue
		}
		return false
	}
	return true
}

type ClientCredentials struct {
	Client domain.TelegramLoginClient
	Secret string
}

type ClientConfiguration struct {
	Client      domain.TelegramLoginClient
	AllowedURLs []domain.TelegramLoginAllowedURL
	NativeApps  []domain.TelegramLoginNativeApp
}

// EnsureClient creates the bot's OIDC client once. Concurrent BotFather
// sessions converge on the durable winner; only the creator receives the
// one-time secret.
func (s *Service) EnsureClient(ctx context.Context, botUserID int64) (ClientCredentials, bool, error) {
	if client, found, err := s.store.GetTelegramLoginClientByBot(ctx, botUserID); err != nil {
		return ClientCredentials{}, false, err
	} else if found {
		return ClientCredentials{Client: client}, false, nil
	}
	algorithm, ok := s.defaultSigningAlgorithm()
	if !ok {
		return ClientCredentials{}, false, domain.ErrTelegramLoginClientInvalid
	}
	created, err := s.CreateClient(ctx, botUserID, algorithm)
	if err == nil {
		return created, true, nil
	}
	if !errors.Is(err, domain.ErrTelegramLoginRequestConflict) {
		return ClientCredentials{}, false, err
	}
	client, found, readErr := s.store.GetTelegramLoginClientByBot(ctx, botUserID)
	if readErr != nil {
		return ClientCredentials{}, false, readErr
	}
	if !found {
		return ClientCredentials{}, false, err
	}
	return ClientCredentials{Client: client}, false, nil
}

func (s *Service) ClientConfiguration(ctx context.Context, botUserID int64) (ClientConfiguration, bool, error) {
	client, found, err := s.store.GetTelegramLoginClientByBot(ctx, botUserID)
	if err != nil || !found {
		return ClientConfiguration{}, found, err
	}
	allowed, err := s.store.ListTelegramLoginAllowedURLs(ctx, botUserID)
	if err != nil {
		return ClientConfiguration{}, false, err
	}
	apps, err := s.store.ListTelegramLoginNativeApps(ctx, botUserID)
	if err != nil {
		return ClientConfiguration{}, false, err
	}
	return ClientConfiguration{Client: client, AllowedURLs: allowed, NativeApps: apps}, true, nil
}

func (s *Service) CreateClient(ctx context.Context, botUserID int64, algorithm domain.TelegramLoginSigningAlgorithm) (ClientCredentials, error) {
	if botUserID <= 0 || !s.signingAlgorithmSupported(algorithm) {
		return ClientCredentials{}, domain.ErrTelegramLoginClientInvalid
	}
	now := s.now().UTC()
	return s.createClientWithSecret(ctx, domain.TelegramLoginClient{
		BotUserID: botUserID, ClientID: strconv.FormatInt(botUserID, 10),
		SecretVersion: 1, SigningAlgorithm: algorithm, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Service) RotateClientSecret(ctx context.Context, botUserID int64) (ClientCredentials, error) {
	client, found, err := s.store.GetTelegramLoginClientByBot(ctx, botUserID)
	if err != nil {
		return ClientCredentials{}, err
	}
	if !found {
		return ClientCredentials{}, domain.ErrTelegramLoginClientInvalid
	}
	secret, err := GenerateOpaqueToken()
	if err != nil {
		return ClientCredentials{}, err
	}
	secretHash, err := HashClientSecret(s.clientSecretPepper, secret)
	if err != nil {
		return ClientCredentials{}, err
	}
	client, err = s.store.RotateTelegramLoginClientSecret(ctx, botUserID, client.SecretVersion, secretHash, s.now().UTC())
	if err != nil {
		return ClientCredentials{}, err
	}
	return ClientCredentials{Client: client, Secret: secret}, nil
}

func (s *Service) createClientWithSecret(ctx context.Context, client domain.TelegramLoginClient) (ClientCredentials, error) {
	secret, err := GenerateOpaqueToken()
	if err != nil {
		return ClientCredentials{}, err
	}
	client.SecretHash, err = HashClientSecret(s.clientSecretPepper, secret)
	if err != nil {
		return ClientCredentials{}, err
	}
	client, err = s.store.CreateTelegramLoginClient(ctx, client)
	if err != nil {
		return ClientCredentials{}, err
	}
	return ClientCredentials{Client: client, Secret: secret}, nil
}

func (s *Service) AddAllowedURL(ctx context.Context, botUserID int64, kind domain.TelegramLoginAllowedURLKind, raw string) (domain.TelegramLoginAllowedURL, error) {
	var normalized string
	var err error
	switch kind {
	case domain.TelegramLoginAllowedWebOrigin:
		normalized, err = NormalizeWebOrigin(raw, s.allowHTTP)
	case domain.TelegramLoginAllowedRedirectURI:
		normalized, _, err = NormalizeRedirectURI(raw, s.allowHTTP)
	default:
		err = domain.ErrTelegramLoginURLInvalid
	}
	if err != nil {
		return domain.TelegramLoginAllowedURL{}, err
	}
	return s.store.AddTelegramLoginAllowedURL(ctx, domain.TelegramLoginAllowedURL{
		BotUserID: botUserID, Kind: kind, NormalizedURL: normalized, CreatedAt: s.now().UTC(),
	})
}

func (s *Service) DeleteAllowedURL(ctx context.Context, botUserID int64, kind domain.TelegramLoginAllowedURLKind, raw string) (bool, error) {
	var normalized string
	var err error
	switch kind {
	case domain.TelegramLoginAllowedWebOrigin:
		normalized, err = NormalizeWebOrigin(raw, s.allowHTTP)
	case domain.TelegramLoginAllowedRedirectURI:
		normalized, _, err = NormalizeRedirectURI(raw, s.allowHTTP)
	default:
		err = domain.ErrTelegramLoginURLInvalid
	}
	if err != nil {
		return false, err
	}
	return s.store.DeleteTelegramLoginAllowedURL(ctx, botUserID, kind, normalized)
}

func (s *Service) SetClientEnabled(ctx context.Context, botUserID int64, enabled bool) error {
	if enabled {
		client, found, err := s.store.GetTelegramLoginClientByBot(ctx, botUserID)
		if err != nil {
			return err
		}
		if !found || !s.signingAlgorithmSupported(client.SigningAlgorithm) {
			return domain.ErrTelegramLoginClientInvalid
		}
	}
	return s.store.SetTelegramLoginClientEnabled(ctx, botUserID, enabled, s.now().UTC())
}

func (s *Service) SetClientSigningAlgorithm(ctx context.Context, botUserID int64, algorithm domain.TelegramLoginSigningAlgorithm) (domain.TelegramLoginClient, error) {
	if botUserID <= 0 || !s.signingAlgorithmSupported(algorithm) {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginClientInvalid
	}
	return s.store.SetTelegramLoginClientSigningAlgorithm(ctx, botUserID, algorithm, s.now().UTC())
}

func (s *Service) AddNativeApp(ctx context.Context, botUserID int64, platform domain.TelegramLoginNativePlatform, applicationID, verificationID, callbackURI, displayName string) (domain.TelegramLoginNativeApp, error) {
	if botUserID <= 0 || !platform.Valid() {
		return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginClientInvalid
	}
	applicationID, err := normalizeNativeApplicationID(applicationID)
	if err != nil {
		return domain.TelegramLoginNativeApp{}, err
	}
	verificationID, err = normalizeNativeVerificationID(platform, verificationID)
	if err != nil {
		return domain.TelegramLoginNativeApp{}, err
	}
	callbackURI, err = NormalizeNativeCallbackURI(callbackURI, s.allowHTTP)
	if err != nil {
		return domain.TelegramLoginNativeApp{}, err
	}
	displayName, err = normalizeNativeDisplayName(displayName)
	if err != nil {
		return domain.TelegramLoginNativeApp{}, err
	}
	now := s.now().UTC()
	return s.store.UpsertTelegramLoginNativeApp(ctx, domain.TelegramLoginNativeApp{
		BotUserID: botUserID, Platform: platform, ApplicationID: applicationID,
		VerificationID: verificationID, CallbackURI: callbackURI, VerifiedDisplayName: displayName,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Service) DeleteNativeApp(ctx context.Context, botUserID, appID int64) (bool, error) {
	if botUserID <= 0 || appID <= 0 {
		return false, domain.ErrTelegramLoginClientInvalid
	}
	return s.store.DeleteTelegramLoginNativeApp(ctx, botUserID, appID)
}

func (s *Service) matchNativeApp(ctx context.Context, botUserID int64, platform domain.TelegramLoginNativePlatform, rawCallbackURI string) (domain.TelegramLoginNativeApp, string, bool, error) {
	callbackURI, err := NormalizeNativeCallbackURI(rawCallbackURI, s.allowHTTP)
	if err != nil {
		return domain.TelegramLoginNativeApp{}, "", false, nil
	}
	apps, err := s.store.ListTelegramLoginNativeApps(ctx, botUserID)
	if err != nil {
		return domain.TelegramLoginNativeApp{}, "", false, err
	}
	for _, app := range apps {
		if app.Enabled && app.CallbackURI == callbackURI && (!platform.Valid() || app.Platform == platform) {
			return app, callbackURI, true, nil
		}
	}
	return domain.TelegramLoginNativeApp{}, callbackURI, false, nil
}

// ValidateMessageButton verifies the linked-domain invariant for a legacy
// Bot API login_url button. Bot buttons are origin-bound (not exact callback
// URI-bound): the final signed user fields are appended to the original URL.
func (s *Service) ValidateMessageButton(ctx context.Context, botUserID int64, rawURL string) (normalizedURL, domainName string, err error) {
	client, found, err := s.store.GetTelegramLoginClientByBot(ctx, botUserID)
	if err != nil {
		return "", "", err
	}
	if !found || !client.Enabled {
		return "", "", domain.ErrTelegramLoginClientDisabled
	}
	normalizedURL, domainName, err = NormalizeRedirectURI(rawURL, s.allowHTTP)
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(normalizedURL)
	if err != nil {
		return "", "", domain.ErrTelegramLoginURLInvalid
	}
	origin, err := NormalizeWebOrigin(u.Scheme+"://"+u.Host, s.allowHTTP)
	if err != nil {
		return "", "", err
	}
	allowed, err := s.store.IsTelegramLoginURLAllowed(ctx, botUserID, domain.TelegramLoginAllowedWebOrigin, origin)
	if err != nil {
		return "", "", err
	}
	if !allowed {
		return "", "", domain.ErrTelegramLoginOriginNotAllowed
	}
	return normalizedURL, domainName, nil
}

// AuthorizeMessageButton implements the legacy Seamless Login button path.
// It creates a short-lived internal request and immediately performs the same
// transactional approval used by OIDC, so web_authorizations and optional bot
// write access cannot diverge. The returned URL uses Telegram's documented
// legacy HMAC format and is independent from authorization-code/PKCE tokens.
func (s *Service) AuthorizeMessageButton(ctx context.Context, params domain.TelegramLoginMessageButtonAuthorization) (domain.TelegramLoginMessageButtonResult, error) {
	if params.UserID <= 0 || params.BotUserID <= 0 || params.Identity.UserID != params.UserID ||
		params.MessageID <= 0 || params.ButtonID < 0 || params.Peer.ID <= 0 ||
		(params.Peer.Type != domain.PeerTypeUser && params.Peer.Type != domain.PeerTypeChannel) ||
		params.WriteAllowed && !params.RequestWriteAccess {
		return domain.TelegramLoginMessageButtonResult{}, domain.ErrTelegramLoginRequestInvalid
	}
	tokenBotID, _, ok := domain.ParseBotToken(params.BotToken)
	if !ok || tokenBotID != params.BotUserID {
		return domain.TelegramLoginMessageButtonResult{}, domain.ErrTelegramLoginSecretInvalid
	}
	normalizedURL, domainName, err := s.ValidateMessageButton(ctx, params.BotUserID, params.URL)
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, err
	}
	client, found, err := s.store.GetTelegramLoginClientByBot(ctx, params.BotUserID)
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, err
	}
	if !found || !client.Enabled {
		return domain.TelegramLoginMessageButtonResult{}, domain.ErrTelegramLoginClientDisabled
	}
	u, err := url.Parse(normalizedURL)
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, domain.ErrTelegramLoginURLInvalid
	}
	origin, err := NormalizeWebOrigin(u.Scheme+"://"+u.Host, s.allowHTTP)
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, err
	}
	requestToken, err := GenerateOpaqueToken()
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, err
	}
	browserToken, err := GenerateOpaqueToken()
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, err
	}
	scopes := []domain.TelegramLoginScope{domain.TelegramLoginScopeOpenID, domain.TelegramLoginScopeProfile}
	if params.RequestWriteAccess {
		scopes = append(scopes, domain.TelegramLoginScopeBotAccess)
	}
	now := s.now().UTC()
	request, err := s.store.CreateTelegramLoginRequest(ctx, domain.TelegramLoginRequest{
		RequestTokenHash: HashOpaqueToken(requestToken), BrowserTokenHash: HashOpaqueToken(browserToken),
		BotUserID: params.BotUserID, ClientID: client.ClientID, SigningAlgorithm: client.SigningAlgorithm,
		Source: domain.TelegramLoginRequestMessageButton, ResponseType: "legacy_url",
		RedirectURI: normalizedURL, Origin: origin, Domain: domainName, Scopes: scopes,
		Browser: boundedValue(params.Browser, "Telegram", 255), Platform: boundedValue(params.Platform, "Telegram Client", 255),
		IP: boundedValue(params.IP, "Unknown IP", 128), Region: boundedValue(params.Region, "Unknown region", 255),
		UserIDHint: params.UserID, PeerType: params.Peer.Type, PeerID: params.Peer.ID,
		MessageID: params.MessageID, ButtonID: params.ButtonID,
		Status: domain.TelegramLoginRequestPending, CreatedAt: now, ExpiresAt: now.Add(s.requestTTL),
	})
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, err
	}
	hash, err := randomWebAuthorizationHash()
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, err
	}
	approved, webAuthorization, err := s.store.ApproveTelegramLoginRequest(ctx, domain.TelegramLoginApproval{
		RequestID: request.ID, Identity: params.Identity, WriteAllowed: params.WriteAllowed, ApprovedAt: now,
	}, hash)
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, err
	}
	acceptedURL, err := appendLegacyTelegramLoginResult(normalizedURL, approved, params.BotToken)
	if err != nil {
		return domain.TelegramLoginMessageButtonResult{}, err
	}
	return domain.TelegramLoginMessageButtonResult{URL: acceptedURL, Request: approved, WebAuthorization: webAuthorization}, nil
}

func appendLegacyTelegramLoginResult(rawURL string, request domain.TelegramLoginRequest, botToken string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !u.IsAbs() || request.Status != domain.TelegramLoginRequestApproved || request.AuthorizedUserID <= 0 || request.ApprovedAt.IsZero() {
		return "", domain.ErrTelegramLoginRequestInvalid
	}
	values := map[string]string{
		"auth_date":  strconv.FormatInt(request.ApprovedAt.Unix(), 10),
		"first_name": request.GivenName,
		"id":         strconv.FormatInt(request.AuthorizedUserID, 10),
	}
	if request.FamilyName != "" {
		values["last_name"] = request.FamilyName
	}
	if request.PreferredUsername != "" {
		values["username"] = request.PreferredUsername
	}
	if request.Picture != "" {
		values["photo_url"] = request.Picture
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	check := make([]string, 0, len(keys))
	for _, key := range keys {
		check = append(check, key+"="+values[key])
	}
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write([]byte(strings.Join(check, "\n")))
	query := u.Query()
	for key, value := range values {
		query.Set(key, value)
	}
	query.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

type CreateAuthorizationParams struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string
	Scope               string
	State               string
	Nonce               string
	CodeChallenge       string
	CodeChallengeMethod string
	Origin              string
	InAppOrigin         string
	Source              domain.TelegramLoginRequestSource
	Browser             string
	Platform            string
	IP                  string
	Region              string
	UserIDHint          int64
	NativePlatform      domain.TelegramLoginNativePlatform
	IncludeMatchCodes   bool
	MatchCodesFirst     bool
}

type CreatedAuthorization struct {
	Request      domain.TelegramLoginRequest
	RequestToken string
	BrowserToken string
	DeepLink     string
}

func (s *Service) CreateAuthorization(ctx context.Context, params CreateAuthorizationParams) (CreatedAuthorization, error) {
	client, found, err := s.store.GetTelegramLoginClient(ctx, params.ClientID)
	if err != nil {
		return CreatedAuthorization{}, err
	}
	if !found || !client.Enabled {
		return CreatedAuthorization{}, domain.ErrTelegramLoginClientDisabled
	}
	if !s.signingAlgorithmSupported(client.SigningAlgorithm) {
		return CreatedAuthorization{}, domain.ErrTelegramLoginClientDisabled
	}
	if params.ResponseType != "code" && params.ResponseType != "post_message" {
		return CreatedAuthorization{}, domain.ErrTelegramLoginRequestInvalid
	}
	source := params.Source
	if source == "" {
		source = domain.TelegramLoginRequestWeb
	}
	var allowed, isApp bool
	var nativeApp domain.TelegramLoginNativeApp
	redirectURI, domainName, redirectErr := NormalizeRedirectURI(params.RedirectURI, s.allowHTTP)
	if params.ResponseType == "code" && redirectErr == nil && !params.NativePlatform.Valid() {
		allowed, err = s.store.IsTelegramLoginURLAllowed(ctx, client.BotUserID, domain.TelegramLoginAllowedRedirectURI, redirectURI)
		if err != nil {
			return CreatedAuthorization{}, err
		}
	}
	if params.ResponseType == "code" && (!allowed || params.NativePlatform.Valid()) {
		nativeApp, redirectURI, isApp, err = s.matchNativeApp(ctx, client.BotUserID, params.NativePlatform, params.RedirectURI)
		if err != nil {
			return CreatedAuthorization{}, err
		}
		if isApp {
			source, domainName = domain.TelegramLoginRequestNative, nativeApp.ApplicationID
		}
	}
	if params.ResponseType == "code" && !allowed && !isApp {
		if redirectErr != nil {
			return CreatedAuthorization{}, redirectErr
		}
		return CreatedAuthorization{}, domain.ErrTelegramLoginRedirectNotAllowed
	}
	if params.ResponseType == "post_message" && redirectErr != nil {
		return CreatedAuthorization{}, redirectErr
	}
	if params.NativePlatform.Valid() && !isApp || source == domain.TelegramLoginRequestNative && !isApp {
		return CreatedAuthorization{}, domain.ErrTelegramLoginRedirectNotAllowed
	}
	scopeValue := params.Scope
	if isApp && !slices.Contains(strings.Fields(scopeValue), string(domain.TelegramLoginScopeOpenID)) {
		scopeValue = string(domain.TelegramLoginScopeOpenID) + " " + scopeValue
	}
	scopes, err := ParseScopes(scopeValue, client.SigningAlgorithm)
	if err != nil {
		return CreatedAuthorization{}, err
	}
	if params.ResponseType == "code" || params.CodeChallenge != "" || params.CodeChallengeMethod != "" {
		if err := ValidatePKCEChallenge(params.CodeChallenge, params.CodeChallengeMethod); err != nil {
			return CreatedAuthorization{}, err
		}
	}
	if len(params.State) > 2048 || len(params.Nonce) > 1024 || params.UserIDHint < 0 {
		return CreatedAuthorization{}, domain.ErrTelegramLoginRequestInvalid
	}
	origin := ""
	if !isApp {
		origin = params.Origin
		if origin == "" {
			u, _ := url.Parse(redirectURI)
			origin = u.Scheme + "://" + u.Host
		}
		origin, err = NormalizeWebOrigin(origin, s.allowHTTP)
		if err != nil {
			return CreatedAuthorization{}, err
		}
	}
	if params.ResponseType == "post_message" {
		redirectURL, _ := url.Parse(redirectURI)
		redirectOrigin, redirectOriginErr := NormalizeWebOrigin(redirectURL.Scheme+"://"+redirectURL.Host, s.allowHTTP)
		if redirectOriginErr != nil || redirectOrigin != origin {
			return CreatedAuthorization{}, domain.ErrTelegramLoginOriginNotAllowed
		}
		allowed, err = s.store.IsTelegramLoginURLAllowed(ctx, client.BotUserID, domain.TelegramLoginAllowedWebOrigin, origin)
		if err != nil {
			return CreatedAuthorization{}, err
		}
		if !allowed {
			return CreatedAuthorization{}, domain.ErrTelegramLoginOriginNotAllowed
		}
	}
	inAppOrigin := ""
	if params.InAppOrigin != "" {
		if isApp {
			return CreatedAuthorization{}, domain.ErrTelegramLoginOriginNotAllowed
		}
		inAppOrigin, err = NormalizeWebOrigin(params.InAppOrigin, s.allowHTTP)
		if err != nil {
			return CreatedAuthorization{}, err
		}
		allowed, err = s.store.IsTelegramLoginURLAllowed(ctx, client.BotUserID, domain.TelegramLoginAllowedWebOrigin, inAppOrigin)
		if err != nil {
			return CreatedAuthorization{}, err
		}
		if !allowed {
			return CreatedAuthorization{}, domain.ErrTelegramLoginOriginNotAllowed
		}
	}
	requestToken, err := GenerateOpaqueToken()
	if err != nil {
		return CreatedAuthorization{}, err
	}
	browserToken, err := GenerateOpaqueToken()
	if err != nil {
		return CreatedAuthorization{}, err
	}
	matchCodes, matchCode, err := generateMatchCodes(params.IncludeMatchCodes)
	if err != nil {
		return CreatedAuthorization{}, err
	}
	now := s.now().UTC()
	request := domain.TelegramLoginRequest{
		RequestTokenHash: HashOpaqueToken(requestToken), BrowserTokenHash: HashOpaqueToken(browserToken),
		BotUserID: client.BotUserID, ClientID: client.ClientID, SigningAlgorithm: client.SigningAlgorithm,
		Source: source, ResponseType: params.ResponseType, RedirectURI: redirectURI, Origin: origin, Domain: domainName,
		Scopes: scopes, State: params.State, Nonce: params.Nonce,
		CodeChallenge: params.CodeChallenge, CodeChallengeMethod: params.CodeChallengeMethod,
		Browser: boundedValue(params.Browser, "Unknown browser", 255), Platform: boundedValue(params.Platform, "Unknown platform", 255),
		IP: boundedValue(params.IP, "Unknown IP", 128), Region: boundedValue(params.Region, "Unknown region", 255),
		InAppOrigin: inAppOrigin, IsApp: isApp, VerifiedAppName: nativeApp.VerifiedDisplayName,
		MatchCodes: matchCodes, MatchCode: matchCode, MatchCodesFirst: params.MatchCodesFirst && len(matchCodes) > 0,
		UserIDHint: params.UserIDHint, Status: domain.TelegramLoginRequestPending,
		CreatedAt: now, ExpiresAt: now.Add(s.requestTTL),
	}
	request, err = s.store.CreateTelegramLoginRequest(ctx, request)
	if err != nil {
		return CreatedAuthorization{}, err
	}
	deepLink := s.appScheme + "://oauth?token=" + url.QueryEscape(requestToken)
	return CreatedAuthorization{Request: request, RequestToken: requestToken, BrowserToken: browserToken, DeepLink: deepLink}, nil
}

type AuthorizationErrorTarget struct {
	ResponseType string
	RedirectURI  string
	Origin       string
}

// ResolveAuthorizationErrorTarget validates an OAuth error destination
// independently of the invalid parameter that caused the request to fail. It
// prevents redirect and postMessage error handling from becoming an open
// redirect/origin oracle.
func (s *Service) ResolveAuthorizationErrorTarget(ctx context.Context, clientID, responseType, rawRedirectURI, rawOrigin string) (AuthorizationErrorTarget, bool, error) {
	client, found, err := s.store.GetTelegramLoginClient(ctx, clientID)
	if err != nil || !found || !client.Enabled {
		return AuthorizationErrorTarget{}, false, err
	}
	switch responseType {
	case "code":
		redirectURI, safe, err := s.safeCodeRedirect(ctx, client.BotUserID, rawRedirectURI)
		return AuthorizationErrorTarget{ResponseType: responseType, RedirectURI: redirectURI}, safe, err
	case "post_message":
		redirectURI, _, err := NormalizeRedirectURI(rawRedirectURI, s.allowHTTP)
		if err != nil {
			return AuthorizationErrorTarget{}, false, nil
		}
		origin := rawOrigin
		if origin == "" {
			redirect, _ := url.Parse(redirectURI)
			origin = redirect.Scheme + "://" + redirect.Host
		}
		origin, err = NormalizeWebOrigin(origin, s.allowHTTP)
		if err != nil {
			return AuthorizationErrorTarget{}, false, nil
		}
		redirect, _ := url.Parse(redirectURI)
		redirectOrigin, err := NormalizeWebOrigin(redirect.Scheme+"://"+redirect.Host, s.allowHTTP)
		if err != nil || redirectOrigin != origin {
			return AuthorizationErrorTarget{}, false, nil
		}
		allowed, err := s.store.IsTelegramLoginURLAllowed(ctx, client.BotUserID, domain.TelegramLoginAllowedWebOrigin, origin)
		if err != nil || !allowed {
			return AuthorizationErrorTarget{}, false, err
		}
		return AuthorizationErrorTarget{ResponseType: responseType, RedirectURI: redirectURI, Origin: origin}, true, nil
	default:
		// An unsupported response_type may still report the error through a
		// pre-registered redirect URI, per OAuth 2.0.
		redirectURI, safe, err := s.safeCodeRedirect(ctx, client.BotUserID, rawRedirectURI)
		return AuthorizationErrorTarget{ResponseType: "code", RedirectURI: redirectURI}, safe, err
	}
}

func (s *Service) safeCodeRedirect(ctx context.Context, botUserID int64, raw string) (string, bool, error) {
	if redirectURI, _, err := NormalizeRedirectURI(raw, s.allowHTTP); err == nil {
		allowed, err := s.store.IsTelegramLoginURLAllowed(ctx, botUserID, domain.TelegramLoginAllowedRedirectURI, redirectURI)
		if err != nil || allowed {
			return redirectURI, allowed, err
		}
	}
	_, redirectURI, allowed, err := s.matchNativeApp(ctx, botUserID, "", raw)
	return redirectURI, allowed, err
}

func ParseScopes(raw string, algorithm domain.TelegramLoginSigningAlgorithm) ([]domain.TelegramLoginScope, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 || len(fields) > 4 {
		return nil, domain.ErrTelegramLoginScopeInvalid
	}
	set := make(map[domain.TelegramLoginScope]struct{}, len(fields))
	for _, field := range fields {
		if field == "write" {
			field = string(domain.TelegramLoginScopeBotAccess)
		}
		scope := domain.TelegramLoginScope(field)
		if !scope.Valid() {
			return nil, domain.ErrTelegramLoginScopeInvalid
		}
		if _, duplicate := set[scope]; duplicate {
			return nil, domain.ErrTelegramLoginScopeInvalid
		}
		set[scope] = struct{}{}
	}
	ordered := make([]domain.TelegramLoginScope, 0, len(set))
	for _, scope := range []domain.TelegramLoginScope{
		domain.TelegramLoginScopeOpenID,
		domain.TelegramLoginScopeProfile,
		domain.TelegramLoginScopePhone,
		domain.TelegramLoginScopeBotAccess,
	} {
		if _, ok := set[scope]; ok {
			ordered = append(ordered, scope)
		}
	}
	if err := domain.ValidateTelegramLoginScopes(ordered, algorithm); err != nil {
		return nil, err
	}
	return ordered, nil
}

func boundedValue(value, fallback string, max int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if value == "" {
		value = fallback
	}
	for len(value) > max {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

func generateMatchCodes(enabled bool) ([]string, string, error) {
	if !enabled {
		return []string{}, "", nil
	}
	pool := append([]string(nil), telegramLoginMatchCodePool...)
	for i := len(pool) - 1; i > 0; i-- {
		n, err := cryptoRandInt(i + 1)
		if err != nil {
			return nil, "", err
		}
		pool[i], pool[n] = pool[n], pool[i]
	}
	codes := append([]string(nil), pool[:5]...)
	selected, err := cryptoRandInt(len(codes))
	if err != nil {
		return nil, "", err
	}
	return codes, codes[selected], nil
}

func cryptoRandInt(max int) (int, error) {
	if max <= 0 {
		return 0, fmt.Errorf("invalid crypto random bound")
	}
	var raw [8]byte
	limit := ^uint64(0) - (^uint64(0) % uint64(max))
	for {
		if _, err := rand.Read(raw[:]); err != nil {
			return 0, fmt.Errorf("crypto random integer: %w", err)
		}
		value := binary.LittleEndian.Uint64(raw[:])
		if value < limit {
			return int(value % uint64(max)), nil
		}
	}
}

func (s *Service) RequestByDeepLink(ctx context.Context, rawURL string) (domain.TelegramLoginRequest, error) {
	token, err := s.deepLinkToken(rawURL)
	if err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	request, found, err := s.store.GetTelegramLoginRequestByTokenHash(ctx, HashOpaqueToken(token))
	if err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	if !found {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestInvalid
	}
	if !s.now().Before(request.ExpiresAt) {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestExpired
	}
	return request, nil
}

// RequestByDeepLinkForOrigin resolves an OAuth deep link and proves the
// immutable Mini App origin that was bound when /inapp created the request.
// A copied deep link cannot be approved from another origin, and a normal web
// request can never be mutated into a Mini App request by a later MTProto call.
func (s *Service) RequestByDeepLinkForOrigin(ctx context.Context, rawURL, rawOrigin string) (domain.TelegramLoginRequest, error) {
	request, err := s.RequestByDeepLink(ctx, rawURL)
	if err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	if request.Source != domain.TelegramLoginRequestMiniApp {
		if rawOrigin != "" || request.InAppOrigin != "" {
			return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginOriginNotAllowed
		}
		return request, nil
	}
	if rawOrigin == "" || request.InAppOrigin == "" {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginOriginNotAllowed
	}
	origin, err := NormalizeWebOrigin(rawOrigin, s.allowHTTP)
	if err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	allowed, err := s.store.IsTelegramLoginURLAllowed(ctx, request.BotUserID, domain.TelegramLoginAllowedWebOrigin, origin)
	if err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	if !allowed {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginOriginNotAllowed
	}
	if request.InAppOrigin != origin {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginOriginNotAllowed
	}
	return request, nil
}

func (s *Service) deepLinkToken(rawURL string) (string, error) {
	if rawURL == "" || len(rawURL) > maxTelegramLoginURLLength || rawURL != strings.TrimSpace(rawURL) {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Fragment != "" || u.User != nil {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	customOrCanonicalScheme := strings.EqualFold(u.Scheme, s.appScheme) || strings.EqualFold(u.Scheme, "tg")
	var token string
	switch {
	case customOrCanonicalScheme && strings.EqualFold(u.Host, "oauth") && u.Path == "":
		token, _ = singleQueryValue(query, "token")
	case customOrCanonicalScheme && strings.EqualFold(u.Host, "resolve") && u.Path == "":
		domainValue, domainOK := singleQueryValue(query, "domain")
		startApp, startAppOK := singleQueryValue(query, "startapp")
		if domainOK && startAppOK && strings.EqualFold(domainValue, "oauth") {
			token = startApp
		}
	case strings.EqualFold(u.Scheme, "https") && (strings.EqualFold(u.Hostname(), "t.me") || strings.EqualFold(u.Hostname(), "telegram.me")) && strings.Trim(u.Path, "/") == "oauth":
		token, _ = singleQueryValue(query, "startapp")
	default:
		return "", domain.ErrTelegramLoginURLInvalid
	}
	if len(token) < 16 || len(token) > 1024 || strings.IndexFunc(token, func(r rune) bool { return r <= 0x20 || r == 0x7f }) >= 0 {
		return "", domain.ErrTelegramLoginURLInvalid
	}
	return token, nil
}

func singleQueryValue(query url.Values, name string) (string, bool) {
	values, ok := query[name]
	if !ok || len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func (s *Service) CheckMatchCode(ctx context.Context, deepLink, selected string) (bool, error) {
	request, err := s.RequestByDeepLink(ctx, deepLink)
	if err != nil {
		return false, err
	}
	if !request.MatchCodesFirst || len(request.MatchCodes) == 0 || !slices.Contains(request.MatchCodes, selected) {
		return false, domain.ErrTelegramLoginMatchCodeInvalid
	}
	if subtle.ConstantTimeCompare([]byte(selected), []byte(request.MatchCode)) != 1 {
		return false, domain.ErrTelegramLoginMatchCodeInvalid
	}
	return true, nil
}

func (s *Service) Approve(ctx context.Context, deepLink string, identity domain.TelegramLoginIdentitySnapshot, writeAllowed, phoneShared bool, matchCode string) (domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error) {
	request, err := s.RequestByDeepLink(ctx, deepLink)
	if err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, err
	}
	hash, err := randomWebAuthorizationHash()
	if err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, err
	}
	return s.store.ApproveTelegramLoginRequest(ctx, domain.TelegramLoginApproval{
		RequestID: request.ID, Identity: identity, WriteAllowed: writeAllowed,
		PhoneShared: phoneShared, MatchCode: matchCode, ApprovedAt: s.now().UTC(),
	}, hash)
}

func randomWebAuthorizationHash() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, fmt.Errorf("generate web authorization hash: %w", err)
	}
	value := int64(binary.LittleEndian.Uint64(raw[:]) & uint64(^uint64(0)>>1))
	if value == 0 {
		value = 1
	}
	return value, nil
}

func (s *Service) Decline(ctx context.Context, deepLink string, userID int64) (domain.TelegramLoginRequest, error) {
	request, err := s.RequestByDeepLink(ctx, deepLink)
	if err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	return s.store.DeclineTelegramLoginRequest(ctx, request.ID, userID, s.now().UTC())
}

type FinalizedAuthorization struct {
	Request     domain.TelegramLoginRequest
	Code        string
	RedirectURL string
}

func (s *Service) FinalizeByBrowserToken(ctx context.Context, browserToken string) (FinalizedAuthorization, error) {
	request, found, err := s.store.GetTelegramLoginRequestByBrowserTokenHash(ctx, HashOpaqueToken(browserToken))
	if err != nil {
		return FinalizedAuthorization{}, err
	}
	if !found || request.Status != domain.TelegramLoginRequestApproved || request.ResponseType != "code" {
		return FinalizedAuthorization{}, domain.ErrTelegramLoginRequestConflict
	}
	return s.finalizeAuthorizationRequest(ctx, request)
}

// FinalizeRedirectByDeepLink is used by native SDK cross-app flows: there is
// no browser poller, so messages.acceptUrlAuth must return the exact registered
// callback URI with a one-time authorization code.
func (s *Service) FinalizeRedirectByDeepLink(ctx context.Context, deepLink string) (string, error) {
	token, err := s.deepLinkToken(deepLink)
	if err != nil {
		return "", err
	}
	request, found, err := s.store.GetTelegramLoginRequestByTokenHash(ctx, HashOpaqueToken(token))
	if err != nil {
		return "", err
	}
	if !found || request.Status != domain.TelegramLoginRequestApproved || request.ResponseType != "code" || request.Source != domain.TelegramLoginRequestNative || !request.IsApp {
		return "", domain.ErrTelegramLoginRequestConflict
	}
	finalized, err := s.finalizeAuthorizationRequest(ctx, request)
	return finalized.RedirectURL, err
}

// FinalizeInAppRedirectByDeepLink returns the result_url consumed by the
// official JavaScript SDK after the Telegram client emits
// oauth_result_confirmed. The URL contains only a short-lived one-time token;
// the ID token is signed and returned by /inapp after the webview proves the
// immutable requesting origin again.
func (s *Service) FinalizeInAppRedirectByDeepLink(ctx context.Context, deepLink string) (string, error) {
	token, err := s.deepLinkToken(deepLink)
	if err != nil {
		return "", err
	}
	request, found, err := s.store.GetTelegramLoginRequestByTokenHash(ctx, HashOpaqueToken(token))
	if err != nil {
		return "", err
	}
	if !found || request.Status != domain.TelegramLoginRequestApproved ||
		request.Source != domain.TelegramLoginRequestMiniApp || request.ResponseType != "post_message" || request.InAppOrigin == "" {
		return "", domain.ErrTelegramLoginRequestConflict
	}
	directToken, err := s.finalizeInAppToken(ctx, request)
	if err != nil {
		return "", err
	}
	return s.issuer + "/inapp?token=" + url.QueryEscape(directToken), nil
}

func (s *Service) finalizeInAppToken(ctx context.Context, request domain.TelegramLoginRequest) (string, error) {
	if existing, found, err := s.store.GetTelegramLoginAuthorizationCodeByRequest(ctx, request.ID); err != nil {
		return "", err
	} else if found {
		if !existing.ConsumedAt.IsZero() {
			return "", domain.ErrTelegramLoginCodeConsumed
		}
		if !s.now().Before(existing.ExpiresAt) {
			return "", domain.ErrTelegramLoginCodeInvalid
		}
		existing, err = s.store.PutTelegramLoginAuthorizationCode(ctx, existing)
		if err != nil {
			return "", err
		}
		return s.openCode(request, existing)
	}
	token, err := GenerateOpaqueToken()
	if err != nil {
		return "", err
	}
	sealed, nonce, keyID, err := s.sealer.Seal(token, codeAAD(request))
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	stored, err := s.store.PutTelegramLoginAuthorizationCode(ctx, domain.TelegramLoginAuthorizationCode{
		RequestID: request.ID, CodeHash: HashOpaqueToken(token), SealedCode: sealed,
		SealNonce: nonce, SealKeyID: keyID, IssuedAt: now, ExpiresAt: now.Add(s.codeTTL),
	})
	if err != nil {
		return "", err
	}
	return s.openCode(request, stored)
}

// ExchangeInAppTokenAndIssue performs the second official /inapp step. The
// direct token is one-time across all instances and remains bound to the
// original registered webview origin and active Web authorization.
func (s *Service) ExchangeInAppTokenAndIssue(ctx context.Context, token, rawOrigin string, issuer *IDTokenIssuer) (IssuedAuthorization, error) {
	if issuer == nil || len(token) < 16 || len(token) > 1024 || strings.IndexFunc(token, func(r rune) bool { return r <= 0x20 || r == 0x7f }) >= 0 {
		return IssuedAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	origin, err := NormalizeWebOrigin(rawOrigin, s.allowHTTP)
	if err != nil {
		return IssuedAuthorization{}, domain.ErrTelegramLoginOriginNotAllowed
	}
	hash := HashOpaqueToken(token)
	stored, found, err := s.store.GetTelegramLoginAuthorizationCodeByHash(ctx, hash)
	if err != nil {
		return IssuedAuthorization{}, err
	}
	if !found {
		return IssuedAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	request, found, err := s.store.GetTelegramLoginRequest(ctx, stored.RequestID)
	if err != nil {
		return IssuedAuthorization{}, err
	}
	if !found || request.Source != domain.TelegramLoginRequestMiniApp || request.ResponseType != "post_message" || request.InAppOrigin != origin {
		return IssuedAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	opened, err := s.openCode(request, stored)
	if err != nil || subtle.ConstantTimeCompare([]byte(opened), []byte(token)) != 1 {
		return IssuedAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	idToken, err := issuer.Issue(request)
	if err != nil {
		return IssuedAuthorization{}, err
	}
	_, consumed, web, err := s.store.ConsumeTelegramLoginDirectToken(ctx, hash, origin, s.now().UTC())
	if err != nil {
		return IssuedAuthorization{}, err
	}
	return IssuedAuthorization{
		ExchangedAuthorization: ExchangedAuthorization{Request: consumed, WebAuthorization: web},
		IDToken:                idToken,
	}, nil
}

func (s *Service) finalizeAuthorizationRequest(ctx context.Context, request domain.TelegramLoginRequest) (FinalizedAuthorization, error) {
	if existing, found, err := s.store.GetTelegramLoginAuthorizationCodeByRequest(ctx, request.ID); err != nil {
		return FinalizedAuthorization{}, err
	} else if found {
		if !existing.ConsumedAt.IsZero() {
			return FinalizedAuthorization{}, domain.ErrTelegramLoginCodeConsumed
		}
		if !s.now().Before(existing.ExpiresAt) {
			return FinalizedAuthorization{}, domain.ErrTelegramLoginCodeInvalid
		}
		existing, err = s.store.PutTelegramLoginAuthorizationCode(ctx, existing)
		if err != nil {
			return FinalizedAuthorization{}, err
		}
		code, err := s.openCode(request, existing)
		if err != nil {
			return FinalizedAuthorization{}, err
		}
		redirectURL, err := AppendAuthorizationResult(request.RedirectURI, code, request.State)
		return FinalizedAuthorization{Request: request, Code: code, RedirectURL: redirectURL}, err
	}
	code, err := GenerateOpaqueToken()
	if err != nil {
		return FinalizedAuthorization{}, err
	}
	sealed, nonce, keyID, err := s.sealer.Seal(code, codeAAD(request))
	if err != nil {
		return FinalizedAuthorization{}, err
	}
	now := s.now().UTC()
	stored, err := s.store.PutTelegramLoginAuthorizationCode(ctx, domain.TelegramLoginAuthorizationCode{
		RequestID: request.ID, CodeHash: HashOpaqueToken(code), SealedCode: sealed,
		SealNonce: nonce, SealKeyID: keyID, IssuedAt: now, ExpiresAt: now.Add(s.codeTTL),
	})
	if err != nil {
		return FinalizedAuthorization{}, err
	}
	code, err = s.openCode(request, stored)
	if err != nil {
		return FinalizedAuthorization{}, err
	}
	redirectURL, err := AppendAuthorizationResult(request.RedirectURI, code, request.State)
	return FinalizedAuthorization{Request: request, Code: code, RedirectURL: redirectURL}, err
}

type FinalizedDirectAuthorization struct {
	Request domain.TelegramLoginRequest
	IDToken string
}

// FinalizeDirectByBrowserToken implements the JS SDK post_message response.
// The signed token is sealed in the durable artifact table so a lost HTTP
// response can retry and receive byte-identical output. Code consumption
// rejects post_message requests, so the artifact cannot be exchanged as an
// authorization code.
func (s *Service) FinalizeDirectByBrowserToken(ctx context.Context, browserToken string, issuer *IDTokenIssuer) (FinalizedDirectAuthorization, error) {
	if issuer == nil {
		return FinalizedDirectAuthorization{}, fmt.Errorf("telegram login ID token issuer is required")
	}
	request, err := s.RequestByBrowserToken(ctx, browserToken)
	if err != nil {
		return FinalizedDirectAuthorization{}, err
	}
	if request.Status != domain.TelegramLoginRequestApproved || request.ResponseType != "post_message" {
		return FinalizedDirectAuthorization{}, domain.ErrTelegramLoginRequestConflict
	}
	if existing, found, err := s.store.GetTelegramLoginAuthorizationCodeByRequest(ctx, request.ID); err != nil {
		return FinalizedDirectAuthorization{}, err
	} else if found {
		if !s.now().Before(existing.ExpiresAt) {
			return FinalizedDirectAuthorization{}, domain.ErrTelegramLoginCodeInvalid
		}
		existing, err = s.store.PutTelegramLoginAuthorizationCode(ctx, existing)
		if err != nil {
			return FinalizedDirectAuthorization{}, err
		}
		idToken, err := s.openCode(request, existing)
		return FinalizedDirectAuthorization{Request: request, IDToken: idToken}, err
	}
	idToken, err := issuer.Issue(request)
	if err != nil {
		return FinalizedDirectAuthorization{}, err
	}
	sealed, nonce, keyID, err := s.sealer.Seal(idToken, codeAAD(request))
	if err != nil {
		return FinalizedDirectAuthorization{}, err
	}
	now := s.now().UTC()
	stored, err := s.store.PutTelegramLoginAuthorizationCode(ctx, domain.TelegramLoginAuthorizationCode{
		RequestID: request.ID, CodeHash: HashOpaqueToken(idToken), SealedCode: sealed,
		SealNonce: nonce, SealKeyID: keyID, IssuedAt: now, ExpiresAt: now.Add(s.codeTTL),
	})
	if err != nil {
		return FinalizedDirectAuthorization{}, err
	}
	idToken, err = s.openCode(request, stored)
	return FinalizedDirectAuthorization{Request: request, IDToken: idToken}, err
}

func codeAAD(request domain.TelegramLoginRequest) []byte {
	return []byte(fmt.Sprintf("telesrv-telegram-login-code\x00%d\x00%s\x00%s", request.ID, request.ClientID, request.RedirectURI))
}

func (s *Service) openCode(request domain.TelegramLoginRequest, stored domain.TelegramLoginAuthorizationCode) (string, error) {
	code, err := s.sealer.Open(stored.SealedCode, stored.SealNonce, stored.SealKeyID, codeAAD(request))
	if err != nil || subtle.ConstantTimeCompare(HashOpaqueToken(code), stored.CodeHash) != 1 {
		return "", domain.ErrTelegramLoginCodeInvalid
	}
	return code, nil
}

type ExchangeAuthorizationCodeParams struct {
	Code               string
	ClientID           string
	ClientSecret       string
	RedirectURI        string
	CodeVerifier       string
	PublicNativeClient bool
}

type ExchangedAuthorization struct {
	Request          domain.TelegramLoginRequest
	WebAuthorization domain.TelegramLoginWebAuthorization
}

func (s *Service) ExchangeAuthorizationCode(ctx context.Context, params ExchangeAuthorizationCodeParams) (ExchangedAuthorization, error) {
	exchanged, _, err := s.exchangeAuthorizationCode(ctx, params, nil)
	return exchanged, err
}

type IssuedAuthorization struct {
	ExchangedAuthorization
	IDToken string
}

// ExchangeAuthorizationCodeAndIssue signs the immutable approval snapshot
// before the one-time code is consumed, but does not expose the signed token
// unless the locked consume succeeds. This keeps a transient signing/key error
// retryable while preserving exactly-once code exchange under concurrency.
func (s *Service) ExchangeAuthorizationCodeAndIssue(ctx context.Context, params ExchangeAuthorizationCodeParams, issuer *IDTokenIssuer) (IssuedAuthorization, error) {
	if issuer == nil {
		return IssuedAuthorization{}, fmt.Errorf("telegram login ID token issuer is required")
	}
	exchanged, idToken, err := s.exchangeAuthorizationCode(ctx, params, issuer.Issue)
	if err != nil {
		return IssuedAuthorization{}, err
	}
	return IssuedAuthorization{ExchangedAuthorization: exchanged, IDToken: idToken}, nil
}

func (s *Service) exchangeAuthorizationCode(ctx context.Context, params ExchangeAuthorizationCodeParams, issue func(domain.TelegramLoginRequest) (string, error)) (ExchangedAuthorization, string, error) {
	client, found, err := s.store.GetTelegramLoginClient(ctx, params.ClientID)
	if err != nil {
		return ExchangedAuthorization{}, "", err
	}
	if !found || !client.Enabled {
		return ExchangedAuthorization{}, "", domain.ErrTelegramLoginSecretInvalid
	}
	challenge, err := PKCEChallenge(params.CodeVerifier)
	if err != nil {
		return ExchangedAuthorization{}, "", err
	}
	codeHash := HashOpaqueToken(params.Code)
	stored, found, err := s.store.GetTelegramLoginAuthorizationCodeByHash(ctx, codeHash)
	if err != nil {
		return ExchangedAuthorization{}, "", err
	}
	if !found {
		return ExchangedAuthorization{}, "", domain.ErrTelegramLoginCodeInvalid
	}
	// The request is recovered by the durable consume operation. Opening the
	// sealed code first prevents storage corruption/key loss from consuming it.
	requestByBrowser, found, err := s.store.GetTelegramLoginRequest(ctx, stored.RequestID)
	if err != nil || !found {
		if err != nil {
			return ExchangedAuthorization{}, "", err
		}
		return ExchangedAuthorization{}, "", domain.ErrTelegramLoginCodeInvalid
	}
	var redirectURI string
	if requestByBrowser.Source == domain.TelegramLoginRequestNative {
		var allowed bool
		_, redirectURI, allowed, err = s.matchNativeApp(ctx, client.BotUserID, "", params.RedirectURI)
		if err != nil {
			return ExchangedAuthorization{}, "", err
		}
		if !allowed {
			return ExchangedAuthorization{}, "", domain.ErrTelegramLoginCodeInvalid
		}
	} else {
		redirectURI, _, err = NormalizeRedirectURI(params.RedirectURI, s.allowHTTP)
		if err != nil {
			return ExchangedAuthorization{}, "", err
		}
	}
	if params.PublicNativeClient {
		if requestByBrowser.Source != domain.TelegramLoginRequestNative || params.ClientSecret != "" {
			return ExchangedAuthorization{}, "", domain.ErrTelegramLoginSecretInvalid
		}
	} else if !VerifyClientSecret(s.clientSecretPepper, params.ClientSecret, client.SecretHash) {
		return ExchangedAuthorization{}, "", domain.ErrTelegramLoginSecretInvalid
	}
	opened, err := s.openCode(requestByBrowser, stored)
	if err != nil || subtle.ConstantTimeCompare([]byte(opened), []byte(params.Code)) != 1 {
		return ExchangedAuthorization{}, "", domain.ErrTelegramLoginCodeInvalid
	}
	var idToken string
	if issue != nil {
		idToken, err = issue(requestByBrowser)
		if err != nil {
			return ExchangedAuthorization{}, "", err
		}
	}
	_, consumedRequest, web, err := s.store.ConsumeTelegramLoginAuthorizationCode(ctx, domain.TelegramLoginCodeExchange{
		CodeHash: codeHash, ClientID: params.ClientID, ClientSecretVersion: client.SecretVersion,
		RedirectURI: redirectURI, CodeChallenge: challenge, Now: s.now().UTC(),
	})
	if err != nil {
		return ExchangedAuthorization{}, "", err
	}
	return ExchangedAuthorization{Request: consumedRequest, WebAuthorization: web}, idToken, nil
}

func (s *Service) RequestByBrowserToken(ctx context.Context, browserToken string) (domain.TelegramLoginRequest, error) {
	if len(browserToken) < 16 || len(browserToken) > 1024 || strings.IndexFunc(browserToken, func(r rune) bool { return r <= 0x20 || r == 0x7f }) >= 0 {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestInvalid
	}
	request, found, err := s.store.GetTelegramLoginRequestByBrowserTokenHash(ctx, HashOpaqueToken(browserToken))
	if err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	if !found {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestInvalid
	}
	if request.Status == domain.TelegramLoginRequestPending && !s.now().Before(request.ExpiresAt) {
		request.Status = domain.TelegramLoginRequestExpired
	}
	return request, nil
}

func (s *Service) ListWebAuthorizations(ctx context.Context, userID int64) ([]domain.TelegramLoginWebAuthorization, error) {
	return s.store.ListTelegramLoginWebAuthorizations(ctx, userID)
}

func (s *Service) RevokeWebAuthorization(ctx context.Context, userID, hash int64) error {
	revoked, err := s.store.RevokeTelegramLoginWebAuthorization(ctx, userID, hash, s.now().UTC())
	if err != nil {
		return err
	}
	if !revoked {
		return domain.ErrTelegramLoginWebAuthHashInvalid
	}
	return nil
}

func (s *Service) RevokeAllWebAuthorizations(ctx context.Context, userID int64) (int64, error) {
	return s.store.RevokeAllTelegramLoginWebAuthorizations(ctx, userID, s.now().UTC())
}

// DeleteExpiredArtifacts removes only terminal login artifacts older than the
// configured retention boundary. Active web authorizations remain durable and
// keep their immutable approved request/claim snapshot reachable.
func (s *Service) DeleteExpiredArtifacts(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() || limit <= 0 || limit > 1000 {
		return 0, domain.ErrTelegramLoginRequestInvalid
	}
	return s.store.DeleteExpiredTelegramLoginArtifacts(ctx, before.UTC(), limit)
}
