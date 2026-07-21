package memory

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"telesrv/internal/domain"
)

type telegramLoginBotPermissionWriter interface {
	AllowBotSendMessage(ctx context.Context, botUserID, userID int64, fromRequest bool) (bool, error)
}

// TelegramLoginStore is the deterministic in-memory implementation used by
// application and RPC tests. A single mutex makes the same aggregate changes
// atomic; production uses PostgreSQL row locks and one transaction.
type TelegramLoginStore struct {
	mu sync.RWMutex

	permissions   telegramLoginBotPermissionWriter
	nextURLID     int64
	nextAppID     int64
	nextRequestID int64
	nextCodeID    int64

	clientsByID   map[string]domain.TelegramLoginClient
	clientByBot   map[int64]string
	allowedURLs   map[string]domain.TelegramLoginAllowedURL
	nativeApps    map[int64]domain.TelegramLoginNativeApp
	requests      map[int64]domain.TelegramLoginRequest
	requestToken  map[string]int64
	browserToken  map[string]int64
	codes         map[int64]domain.TelegramLoginAuthorizationCode
	codeByHash    map[string]int64
	codeByRequest map[int64]int64
	webAuths      map[int64]domain.TelegramLoginWebAuthorization
}

func NewTelegramLoginStore(permissions telegramLoginBotPermissionWriter) *TelegramLoginStore {
	return &TelegramLoginStore{
		permissions:   permissions,
		clientsByID:   make(map[string]domain.TelegramLoginClient),
		clientByBot:   make(map[int64]string),
		allowedURLs:   make(map[string]domain.TelegramLoginAllowedURL),
		nativeApps:    make(map[int64]domain.TelegramLoginNativeApp),
		requests:      make(map[int64]domain.TelegramLoginRequest),
		requestToken:  make(map[string]int64),
		browserToken:  make(map[string]int64),
		codes:         make(map[int64]domain.TelegramLoginAuthorizationCode),
		codeByHash:    make(map[string]int64),
		codeByRequest: make(map[int64]int64),
		webAuths:      make(map[int64]domain.TelegramLoginWebAuthorization),
	}
}

func (s *TelegramLoginStore) CreateTelegramLoginClient(_ context.Context, client domain.TelegramLoginClient) (domain.TelegramLoginClient, error) {
	if err := client.Validate(); err != nil {
		return domain.TelegramLoginClient{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.clientByBot[client.BotUserID]; exists {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginRequestConflict
	}
	if _, exists := s.clientsByID[client.ClientID]; exists {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginRequestConflict
	}
	s.clientsByID[client.ClientID] = client.Clone()
	s.clientByBot[client.BotUserID] = client.ClientID
	return client.Clone(), nil
}

func (s *TelegramLoginStore) UpsertTelegramLoginClient(_ context.Context, client domain.TelegramLoginClient) (domain.TelegramLoginClient, error) {
	if err := client.Validate(); err != nil {
		return domain.TelegramLoginClient{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingID, exists := s.clientByBot[client.BotUserID]; exists && existingID != client.ClientID {
		delete(s.clientsByID, existingID)
	}
	if existing, exists := s.clientsByID[client.ClientID]; exists && existing.BotUserID != client.BotUserID {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginClientInvalid
	}
	s.clientsByID[client.ClientID] = client.Clone()
	s.clientByBot[client.BotUserID] = client.ClientID
	return client.Clone(), nil
}

func (s *TelegramLoginStore) GetTelegramLoginClient(_ context.Context, clientID string) (domain.TelegramLoginClient, bool, error) {
	s.mu.RLock()
	client, ok := s.clientsByID[clientID]
	s.mu.RUnlock()
	return client.Clone(), ok, nil
}

func (s *TelegramLoginStore) GetTelegramLoginClientByBot(_ context.Context, botUserID int64) (domain.TelegramLoginClient, bool, error) {
	s.mu.RLock()
	clientID, ok := s.clientByBot[botUserID]
	client := s.clientsByID[clientID]
	s.mu.RUnlock()
	return client.Clone(), ok, nil
}

func (s *TelegramLoginStore) RotateTelegramLoginClientSecret(_ context.Context, botUserID, expectedVersion int64, secretHash []byte, now time.Time) (domain.TelegramLoginClient, error) {
	if botUserID <= 0 || expectedVersion <= 0 || len(secretHash) != 32 {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginClientInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clientID, ok := s.clientByBot[botUserID]
	if !ok {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginClientInvalid
	}
	client := s.clientsByID[clientID]
	if client.SecretVersion != expectedVersion {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginRequestConflict
	}
	client.SecretVersion++
	client.SecretHash = append([]byte(nil), secretHash...)
	client.UpdatedAt = now
	s.clientsByID[clientID] = client
	return client.Clone(), nil
}

func (s *TelegramLoginStore) SetTelegramLoginClientSigningAlgorithm(_ context.Context, botUserID int64, algorithm domain.TelegramLoginSigningAlgorithm, now time.Time) (domain.TelegramLoginClient, error) {
	if botUserID <= 0 || !algorithm.Valid() {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginClientInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clientID, ok := s.clientByBot[botUserID]
	if !ok {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginClientInvalid
	}
	client := s.clientsByID[clientID]
	client.SigningAlgorithm = algorithm
	client.UpdatedAt = now
	s.clientsByID[clientID] = client
	return client.Clone(), nil
}

func (s *TelegramLoginStore) SetTelegramLoginClientEnabled(_ context.Context, botUserID int64, enabled bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clientID, ok := s.clientByBot[botUserID]
	if !ok {
		return domain.ErrTelegramLoginClientInvalid
	}
	client := s.clientsByID[clientID]
	client.Enabled = enabled
	client.UpdatedAt = now
	s.clientsByID[clientID] = client
	return nil
}

func telegramLoginAllowedURLKey(botUserID int64, kind domain.TelegramLoginAllowedURLKind, value string) string {
	return strconv.FormatInt(botUserID, 10) + "\x00" + string(kind) + "\x00" + value
}

func (s *TelegramLoginStore) AddTelegramLoginAllowedURL(_ context.Context, allowed domain.TelegramLoginAllowedURL) (domain.TelegramLoginAllowedURL, error) {
	if allowed.BotUserID <= 0 || allowed.NormalizedURL == "" || (allowed.Kind != domain.TelegramLoginAllowedWebOrigin && allowed.Kind != domain.TelegramLoginAllowedRedirectURI) {
		return domain.TelegramLoginAllowedURL{}, domain.ErrTelegramLoginURLInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clientByBot[allowed.BotUserID]; !ok {
		return domain.TelegramLoginAllowedURL{}, domain.ErrTelegramLoginClientInvalid
	}
	key := telegramLoginAllowedURLKey(allowed.BotUserID, allowed.Kind, allowed.NormalizedURL)
	if existing, ok := s.allowedURLs[key]; ok {
		return existing, nil
	}
	s.nextURLID++
	allowed.ID = s.nextURLID
	s.allowedURLs[key] = allowed
	return allowed, nil
}

func (s *TelegramLoginStore) DeleteTelegramLoginAllowedURL(_ context.Context, botUserID int64, kind domain.TelegramLoginAllowedURLKind, normalizedURL string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := telegramLoginAllowedURLKey(botUserID, kind, normalizedURL)
	if _, ok := s.allowedURLs[key]; !ok {
		return false, nil
	}
	delete(s.allowedURLs, key)
	return true, nil
}

func (s *TelegramLoginStore) ListTelegramLoginAllowedURLs(_ context.Context, botUserID int64) ([]domain.TelegramLoginAllowedURL, error) {
	s.mu.RLock()
	out := make([]domain.TelegramLoginAllowedURL, 0)
	for _, allowed := range s.allowedURLs {
		if allowed.BotUserID == botUserID {
			out = append(out, allowed)
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *TelegramLoginStore) IsTelegramLoginURLAllowed(_ context.Context, botUserID int64, kind domain.TelegramLoginAllowedURLKind, normalizedURL string) (bool, error) {
	s.mu.RLock()
	_, ok := s.allowedURLs[telegramLoginAllowedURLKey(botUserID, kind, normalizedURL)]
	s.mu.RUnlock()
	return ok, nil
}

func (s *TelegramLoginStore) UpsertTelegramLoginNativeApp(_ context.Context, app domain.TelegramLoginNativeApp) (domain.TelegramLoginNativeApp, error) {
	if err := app.Validate(); err != nil {
		return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginClientInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clientByBot[app.BotUserID]; !ok {
		return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginClientInvalid
	}
	if app.ID == 0 {
		for id, existing := range s.nativeApps {
			if existing.BotUserID == app.BotUserID && existing.Platform == app.Platform && existing.ApplicationID == app.ApplicationID && existing.VerificationID == app.VerificationID {
				app.ID, app.CreatedAt = id, existing.CreatedAt
				s.nativeApps[id] = app
				return app, nil
			}
			if existing.BotUserID == app.BotUserID && existing.CallbackURI == app.CallbackURI {
				return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginRequestConflict
			}
		}
		count := 0
		for _, existing := range s.nativeApps {
			if existing.BotUserID == app.BotUserID {
				count++
			}
		}
		if count >= domain.MaxTelegramLoginNativeApps {
			return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginRequestInvalid
		}
		s.nextAppID++
		app.ID = s.nextAppID
	} else if existing, ok := s.nativeApps[app.ID]; ok && existing.BotUserID != app.BotUserID {
		return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginClientInvalid
	}
	s.nativeApps[app.ID] = app
	return app, nil
}

func (s *TelegramLoginStore) DeleteTelegramLoginNativeApp(_ context.Context, botUserID, appID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.nativeApps[appID]
	if !ok || app.BotUserID != botUserID {
		return false, nil
	}
	delete(s.nativeApps, appID)
	return true, nil
}

func (s *TelegramLoginStore) ListTelegramLoginNativeApps(_ context.Context, botUserID int64) ([]domain.TelegramLoginNativeApp, error) {
	s.mu.RLock()
	out := make([]domain.TelegramLoginNativeApp, 0)
	for _, app := range s.nativeApps {
		if app.BotUserID == botUserID {
			out = append(out, app)
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > domain.MaxTelegramLoginNativeApps {
		out = out[:domain.MaxTelegramLoginNativeApps]
	}
	return out, nil
}

func (s *TelegramLoginStore) CreateTelegramLoginRequest(_ context.Context, request domain.TelegramLoginRequest) (domain.TelegramLoginRequest, error) {
	if err := request.Validate(); err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clientsByID[request.ClientID]
	if !ok || client.BotUserID != request.BotUserID || !client.Enabled || client.SigningAlgorithm != request.SigningAlgorithm {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginClientDisabled
	}
	if _, exists := s.requestToken[string(request.RequestTokenHash)]; exists {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestConflict
	}
	if _, exists := s.browserToken[string(request.BrowserTokenHash)]; exists {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestConflict
	}
	s.nextRequestID++
	request.ID = s.nextRequestID
	s.requests[request.ID] = request.Clone()
	s.requestToken[string(request.RequestTokenHash)] = request.ID
	s.browserToken[string(request.BrowserTokenHash)] = request.ID
	return request.Clone(), nil
}

func (s *TelegramLoginStore) GetTelegramLoginRequest(_ context.Context, requestID int64) (domain.TelegramLoginRequest, bool, error) {
	s.mu.RLock()
	request, ok := s.requests[requestID]
	s.mu.RUnlock()
	return request.Clone(), ok, nil
}

func (s *TelegramLoginStore) GetTelegramLoginRequestByTokenHash(_ context.Context, tokenHash []byte) (domain.TelegramLoginRequest, bool, error) {
	s.mu.RLock()
	id, ok := s.requestToken[string(tokenHash)]
	request := s.requests[id]
	s.mu.RUnlock()
	return request.Clone(), ok, nil
}

func (s *TelegramLoginStore) GetTelegramLoginRequestByBrowserTokenHash(_ context.Context, tokenHash []byte) (domain.TelegramLoginRequest, bool, error) {
	s.mu.RLock()
	id, ok := s.browserToken[string(tokenHash)]
	request := s.requests[id]
	s.mu.RUnlock()
	return request.Clone(), ok, nil
}

func grantedTelegramLoginScopes(request domain.TelegramLoginRequest, approval domain.TelegramLoginApproval) ([]domain.TelegramLoginScope, error) {
	if approval.WriteAllowed && !request.Requests(domain.TelegramLoginScopeBotAccess) {
		return nil, domain.ErrTelegramLoginScopeInvalid
	}
	if approval.PhoneShared && !request.Requests(domain.TelegramLoginScopePhone) {
		return nil, domain.ErrTelegramLoginScopeInvalid
	}
	out := make([]domain.TelegramLoginScope, 0, len(request.Scopes))
	for _, scope := range request.Scopes {
		if scope == domain.TelegramLoginScopePhone && !approval.PhoneShared {
			continue
		}
		if scope == domain.TelegramLoginScopeBotAccess && !approval.WriteAllowed {
			continue
		}
		out = append(out, scope)
	}
	return out, nil
}

func (s *TelegramLoginStore) ApproveTelegramLoginRequest(ctx context.Context, approval domain.TelegramLoginApproval, webAuthorizationHash int64) (domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error) {
	if approval.RequestID <= 0 || approval.Identity.UserID <= 0 || webAuthorizationHash == 0 || approval.ApprovedAt.IsZero() {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[approval.RequestID]
	if !ok {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestInvalid
	}
	if request.Status != domain.TelegramLoginRequestPending {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestConflict
	}
	if !approval.ApprovedAt.Before(request.ExpiresAt) {
		request.Status = domain.TelegramLoginRequestExpired
		s.requests[request.ID] = request
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestExpired
	}
	client, clientExists := s.clientsByID[request.ClientID]
	if !clientExists || !client.Enabled || client.BotUserID != request.BotUserID || client.SigningAlgorithm != request.SigningAlgorithm {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginClientDisabled
	}
	if request.ResponseType == "code" {
		_, webAllowed := s.allowedURLs[telegramLoginAllowedURLKey(request.BotUserID, domain.TelegramLoginAllowedRedirectURI, request.RedirectURI)]
		if !webAllowed && !(request.Source == domain.TelegramLoginRequestNative && s.nativeCallbackAllowedLocked(request.BotUserID, request.RedirectURI)) {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRedirectNotAllowed
		}
	} else if request.ResponseType == "post_message" || request.ResponseType == "legacy_url" {
		if _, ok := s.allowedURLs[telegramLoginAllowedURLKey(request.BotUserID, domain.TelegramLoginAllowedWebOrigin, request.Origin)]; !ok {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginOriginNotAllowed
		}
	} else {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestInvalid
	}
	if request.InAppOrigin != "" {
		if _, ok := s.allowedURLs[telegramLoginAllowedURLKey(request.BotUserID, domain.TelegramLoginAllowedWebOrigin, request.InAppOrigin)]; !ok {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginOriginNotAllowed
		}
	}
	if len(request.MatchCodes) > 0 && approval.MatchCode != request.MatchCode {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginMatchCodeInvalid
	}
	scopes, err := grantedTelegramLoginScopes(request, approval)
	if err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, err
	}
	if _, exists := s.webAuths[webAuthorizationHash]; exists {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestConflict
	}
	identity, err := approval.Identity.Sanitized(request.Requests(domain.TelegramLoginScopeProfile), approval.PhoneShared)
	if err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, err
	}
	activeAuthorizations := 0
	for _, authorization := range s.webAuths {
		if authorization.UserID == identity.UserID && authorization.RevokedAt.IsZero() {
			activeAuthorizations++
		}
	}
	if activeAuthorizations >= domain.MaxTelegramLoginWebAuthorizations {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginAuthorizationsTooMany
	}
	if approval.WriteAllowed && s.permissions != nil {
		if _, err := s.permissions.AllowBotSendMessage(ctx, request.BotUserID, identity.UserID, true); err != nil {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, err
		}
	}
	request.Status = domain.TelegramLoginRequestApproved
	request.AuthorizedUserID = identity.UserID
	request.ProfileName = identity.Name
	request.GivenName = identity.GivenName
	request.FamilyName = identity.FamilyName
	request.PreferredUsername = identity.PreferredUsername
	request.Picture = identity.Picture
	request.PhoneNumber = identity.PhoneNumber
	request.WriteAllowed = approval.WriteAllowed
	request.PhoneShared = approval.PhoneShared
	request.ApprovedAt = approval.ApprovedAt
	s.requests[request.ID] = request.Clone()
	web := domain.TelegramLoginWebAuthorization{
		Hash:             webAuthorizationHash,
		RequestID:        request.ID,
		UserID:           identity.UserID,
		BotUserID:        request.BotUserID,
		Domain:           request.Domain,
		Browser:          request.Browser,
		Platform:         request.Platform,
		IP:               request.IP,
		Region:           request.Region,
		Scopes:           scopes,
		PhoneShared:      approval.PhoneShared,
		BotAccessGranted: approval.WriteAllowed,
		CreatedAt:        approval.ApprovedAt,
		LastActiveAt:     approval.ApprovedAt,
	}
	s.webAuths[web.Hash] = web.Clone()
	return request.Clone(), web.Clone(), nil
}

func (s *TelegramLoginStore) DeclineTelegramLoginRequest(_ context.Context, requestID, userID int64, now time.Time) (domain.TelegramLoginRequest, error) {
	if requestID <= 0 || userID <= 0 || now.IsZero() {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[requestID]
	if !ok {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestInvalid
	}
	if request.Status != domain.TelegramLoginRequestPending {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestConflict
	}
	if !now.Before(request.ExpiresAt) {
		request.Status = domain.TelegramLoginRequestExpired
		s.requests[request.ID] = request
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestExpired
	}
	request.Status = domain.TelegramLoginRequestDeclined
	request.DeclinedAt = now
	s.requests[request.ID] = request.Clone()
	return request.Clone(), nil
}

func (s *TelegramLoginStore) PutTelegramLoginAuthorizationCode(_ context.Context, code domain.TelegramLoginAuthorizationCode) (domain.TelegramLoginAuthorizationCode, error) {
	if code.RequestID <= 0 || len(code.CodeHash) != 32 || len(code.SealedCode) < 32 || len(code.SealNonce) < 12 || code.SealKeyID == "" || code.IssuedAt.IsZero() || !code.ExpiresAt.After(code.IssuedAt) {
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginCodeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[code.RequestID]
	if !ok || request.Status != domain.TelegramLoginRequestApproved {
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRequestConflict
	}
	client, clientExists := s.clientsByID[request.ClientID]
	if !clientExists || !client.Enabled || client.BotUserID != request.BotUserID || client.SigningAlgorithm != request.SigningAlgorithm {
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginClientDisabled
	}
	switch request.ResponseType {
	case "code":
		_, allowed := s.allowedURLs[telegramLoginAllowedURLKey(request.BotUserID, domain.TelegramLoginAllowedRedirectURI, request.RedirectURI)]
		if !allowed && !(request.Source == domain.TelegramLoginRequestNative && s.nativeCallbackAllowedLocked(request.BotUserID, request.RedirectURI)) {
			return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRedirectNotAllowed
		}
	case "post_message":
		if _, allowed := s.allowedURLs[telegramLoginAllowedURLKey(request.BotUserID, domain.TelegramLoginAllowedWebOrigin, request.Origin)]; !allowed {
			return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginOriginNotAllowed
		}
	default:
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRequestConflict
	}
	web, active := s.webAuthByRequestLocked(request.ID)
	if !active || !web.RevokedAt.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRequestConflict
	}
	if id, exists := s.codeByRequest[code.RequestID]; exists {
		return s.codes[id].Clone(), nil
	}
	if _, exists := s.codeByHash[string(code.CodeHash)]; exists {
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRequestConflict
	}
	s.nextCodeID++
	code.ID = s.nextCodeID
	s.codes[code.ID] = code.Clone()
	s.codeByHash[string(code.CodeHash)] = code.ID
	s.codeByRequest[code.RequestID] = code.ID
	return code.Clone(), nil
}

func (s *TelegramLoginStore) GetTelegramLoginAuthorizationCodeByRequest(_ context.Context, requestID int64) (domain.TelegramLoginAuthorizationCode, bool, error) {
	s.mu.RLock()
	id, ok := s.codeByRequest[requestID]
	code := s.codes[id]
	s.mu.RUnlock()
	return code.Clone(), ok, nil
}

func (s *TelegramLoginStore) GetTelegramLoginAuthorizationCodeByHash(_ context.Context, codeHash []byte) (domain.TelegramLoginAuthorizationCode, bool, error) {
	s.mu.RLock()
	id, ok := s.codeByHash[string(codeHash)]
	code := s.codes[id]
	s.mu.RUnlock()
	return code.Clone(), ok, nil
}

func (s *TelegramLoginStore) ConsumeTelegramLoginAuthorizationCode(_ context.Context, exchange domain.TelegramLoginCodeExchange) (domain.TelegramLoginAuthorizationCode, domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error) {
	if len(exchange.CodeHash) != 32 || exchange.ClientID == "" || exchange.ClientSecretVersion <= 0 || exchange.RedirectURI == "" || exchange.CodeChallenge == "" || exchange.Now.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.codeByHash[string(exchange.CodeHash)]
	if !ok {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	code := s.codes[id]
	if !code.ConsumedAt.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeConsumed
	}
	if !exchange.Now.Before(code.ExpiresAt) {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	request := s.requests[code.RequestID]
	client, clientExists := s.clientsByID[exchange.ClientID]
	if !clientExists || !client.Enabled || client.SecretVersion != exchange.ClientSecretVersion || request.ResponseType != "code" || request.ClientID != exchange.ClientID || request.RedirectURI != exchange.RedirectURI || request.CodeChallenge != exchange.CodeChallenge {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	_, webAllowed := s.allowedURLs[telegramLoginAllowedURLKey(request.BotUserID, domain.TelegramLoginAllowedRedirectURI, request.RedirectURI)]
	if !webAllowed && !(request.Source == domain.TelegramLoginRequestNative && s.nativeCallbackAllowedLocked(request.BotUserID, request.RedirectURI)) {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	web, exists := s.webAuthByRequestLocked(code.RequestID)
	if request.Status != domain.TelegramLoginRequestApproved || !exists || !web.RevokedAt.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	code.ConsumedAt = exchange.Now
	web.LastActiveAt = exchange.Now
	s.codes[id] = code.Clone()
	s.webAuths[web.Hash] = web.Clone()
	return code.Clone(), request.Clone(), web.Clone(), nil
}

func (s *TelegramLoginStore) ConsumeTelegramLoginDirectToken(_ context.Context, tokenHash []byte, origin string, now time.Time) (domain.TelegramLoginAuthorizationCode, domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error) {
	if len(tokenHash) != 32 || origin == "" || now.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.codeByHash[string(tokenHash)]
	if !ok {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	code := s.codes[id]
	if !code.ConsumedAt.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeConsumed
	}
	if !now.Before(code.ExpiresAt) {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	request := s.requests[code.RequestID]
	client, clientExists := s.clientsByID[request.ClientID]
	if !clientExists || !client.Enabled || client.BotUserID != request.BotUserID ||
		request.Status != domain.TelegramLoginRequestApproved || request.Source != domain.TelegramLoginRequestMiniApp ||
		request.ResponseType != "post_message" || request.Origin != origin || request.InAppOrigin != origin {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	if _, allowed := s.allowedURLs[telegramLoginAllowedURLKey(request.BotUserID, domain.TelegramLoginAllowedWebOrigin, origin)]; !allowed {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	web, exists := s.webAuthByRequestLocked(code.RequestID)
	if !exists || !web.RevokedAt.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	code.ConsumedAt = now
	web.LastActiveAt = now
	s.codes[id] = code.Clone()
	s.webAuths[web.Hash] = web.Clone()
	return code.Clone(), request.Clone(), web.Clone(), nil
}

func (s *TelegramLoginStore) webAuthByRequestLocked(requestID int64) (domain.TelegramLoginWebAuthorization, bool) {
	for _, web := range s.webAuths {
		if web.RequestID == requestID {
			return web, true
		}
	}
	return domain.TelegramLoginWebAuthorization{}, false
}

func (s *TelegramLoginStore) nativeCallbackAllowedLocked(botUserID int64, callbackURI string) bool {
	for _, app := range s.nativeApps {
		if app.BotUserID == botUserID && app.Enabled && app.CallbackURI == callbackURI {
			return true
		}
	}
	return false
}

func (s *TelegramLoginStore) ListTelegramLoginWebAuthorizations(_ context.Context, userID int64) ([]domain.TelegramLoginWebAuthorization, error) {
	s.mu.RLock()
	out := make([]domain.TelegramLoginWebAuthorization, 0, min(len(s.webAuths), domain.MaxTelegramLoginWebAuthorizations))
	for _, web := range s.webAuths {
		if web.UserID == userID && web.RevokedAt.IsZero() {
			out = append(out, web.Clone())
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActiveAt.Equal(out[j].LastActiveAt) {
			return out[i].Hash > out[j].Hash
		}
		return out[i].LastActiveAt.After(out[j].LastActiveAt)
	})
	if len(out) > domain.MaxTelegramLoginWebAuthorizations {
		out = out[:domain.MaxTelegramLoginWebAuthorizations]
	}
	return out, nil
}

func (s *TelegramLoginStore) RevokeTelegramLoginWebAuthorization(_ context.Context, userID, hash int64, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	web, ok := s.webAuths[hash]
	if !ok || web.UserID != userID || !web.RevokedAt.IsZero() {
		return false, nil
	}
	web.RevokedAt = now
	s.webAuths[hash] = web
	return true, nil
}

func (s *TelegramLoginStore) RevokeAllTelegramLoginWebAuthorizations(_ context.Context, userID int64, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for hash, web := range s.webAuths {
		if web.UserID == userID && web.RevokedAt.IsZero() {
			web.RevokedAt = now
			s.webAuths[hash] = web
			count++
		}
	}
	return count, nil
}

func (s *TelegramLoginStore) DeleteExpiredTelegramLoginArtifacts(_ context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		return 0, domain.ErrTelegramLoginRequestInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for id, code := range s.codes {
		if deleted >= int64(limit) {
			break
		}
		if code.ExpiresAt.Before(before) || (!code.ConsumedAt.IsZero() && code.ConsumedAt.Before(before)) {
			delete(s.codes, id)
			delete(s.codeByHash, string(code.CodeHash))
			delete(s.codeByRequest, code.RequestID)
			deleted++
		}
	}
	for id, request := range s.requests {
		if deleted >= int64(limit) {
			break
		}
		deleteRequest := (request.Status == domain.TelegramLoginRequestPending || request.Status == domain.TelegramLoginRequestDeclined || request.Status == domain.TelegramLoginRequestExpired) && request.ExpiresAt.Before(before)
		var revokedWebHash int64
		if request.Status == domain.TelegramLoginRequestApproved && !request.ApprovedAt.IsZero() && request.ApprovedAt.Before(before) {
			// Approved requests remain the immutable claim snapshot behind an active
			// web authorization. They may only be collected after the grant itself
			// was revoked and every exchange code has left the retention window.
			for hash, web := range s.webAuths {
				if web.RequestID == id && !web.RevokedAt.IsZero() && web.RevokedAt.Before(before) {
					deleteRequest = true
					revokedWebHash = hash
					break
				}
			}
			if _, hasCode := s.codeByRequest[id]; hasCode {
				deleteRequest = false
			}
		}
		if !deleteRequest {
			continue
		}
		delete(s.requests, id)
		delete(s.requestToken, string(request.RequestTokenHash))
		delete(s.browserToken, string(request.BrowserTokenHash))
		if revokedWebHash != 0 {
			delete(s.webAuths, revokedWebHash)
		}
		deleted++
	}
	return deleted, nil
}
