package memory

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

type telegramLoginPermissionRecorder struct {
	mu     sync.Mutex
	grants map[[2]int64]int
}

func (r *telegramLoginPermissionRecorder) AllowBotSendMessage(_ context.Context, botUserID, userID int64, _ bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.grants == nil {
		r.grants = make(map[[2]int64]int)
	}
	key := [2]int64{botUserID, userID}
	created := r.grants[key] == 0
	r.grants[key]++
	return created, nil
}

func telegramLoginTestHash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func seedTelegramLoginRequest(t *testing.T, s *TelegramLoginStore, now time.Time) domain.TelegramLoginRequest {
	t.Helper()
	ctx := context.Background()
	client := domain.TelegramLoginClient{
		BotUserID:        9001,
		ClientID:         "9001",
		SecretHash:       telegramLoginTestHash("client-secret"),
		SecretVersion:    1,
		SigningAlgorithm: domain.TelegramLoginSigningRS256,
		Enabled:          true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if _, err := s.UpsertTelegramLoginClient(ctx, client); err != nil {
		t.Fatalf("UpsertTelegramLoginClient: %v", err)
	}
	if _, err := s.AddTelegramLoginAllowedURL(ctx, domain.TelegramLoginAllowedURL{
		BotUserID: client.BotUserID, Kind: domain.TelegramLoginAllowedRedirectURI,
		NormalizedURL: "https://rp.example/callback", CreatedAt: now,
	}); err != nil {
		t.Fatalf("AddTelegramLoginAllowedURL: %v", err)
	}
	request := domain.TelegramLoginRequest{
		RequestTokenHash:    telegramLoginTestHash("request-token"),
		BrowserTokenHash:    telegramLoginTestHash("browser-token"),
		BotUserID:           client.BotUserID,
		ClientID:            client.ClientID,
		SigningAlgorithm:    client.SigningAlgorithm,
		Source:              domain.TelegramLoginRequestWeb,
		ResponseType:        "code",
		RedirectURI:         "https://rp.example/callback",
		Origin:              "https://rp.example",
		Domain:              "rp.example",
		Scopes:              []domain.TelegramLoginScope{domain.TelegramLoginScopeOpenID, domain.TelegramLoginScopeProfile, domain.TelegramLoginScopePhone, domain.TelegramLoginScopeBotAccess},
		State:               "state",
		Nonce:               "nonce",
		CodeChallenge:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CodeChallengeMethod: "S256",
		Browser:             "Firefox",
		Platform:            "Windows",
		IP:                  "192.0.2.10",
		Region:              "Test Region",
		MatchCodes:          []string{"🟢", "🔵", "🟠"},
		MatchCode:           "🔵",
		MatchCodesFirst:     true,
		Status:              domain.TelegramLoginRequestPending,
		CreatedAt:           now,
		ExpiresAt:           now.Add(5 * time.Minute),
	}
	created, err := s.CreateTelegramLoginRequest(ctx, request)
	if err != nil {
		t.Fatalf("CreateTelegramLoginRequest: %v", err)
	}
	return created
}

func approveTelegramLoginRequest(t *testing.T, s *TelegramLoginStore, request domain.TelegramLoginRequest, now time.Time) (domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization) {
	t.Helper()
	approved, web, err := s.ApproveTelegramLoginRequest(context.Background(), domain.TelegramLoginApproval{
		RequestID: request.ID,
		Identity: domain.TelegramLoginIdentitySnapshot{
			UserID: 42, Name: "Alice Example", GivenName: "Alice", FamilyName: "Example",
			PreferredUsername: "alice", Picture: "https://oauth.example/userpic/42",
		},
		WriteAllowed: true,
		PhoneShared:  false,
		MatchCode:    request.MatchCode,
		ApprovedAt:   now,
	}, 7000+request.ID)
	if err != nil {
		t.Fatalf("ApproveTelegramLoginRequest: %v", err)
	}
	return approved, web
}

func TestTelegramLoginApproveIsAtomicAndShrinksConsent(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	permissions := &telegramLoginPermissionRecorder{}
	s := NewTelegramLoginStore(permissions)
	request := seedTelegramLoginRequest(t, s, now)
	approved, web := approveTelegramLoginRequest(t, s, request, now.Add(time.Second))
	if approved.Status != domain.TelegramLoginRequestApproved || approved.AuthorizedUserID != 42 {
		t.Fatalf("approved request = %#v", approved)
	}
	if web.PhoneShared || web.BotAccessGranted != true {
		t.Fatalf("web consent = %#v", web)
	}
	if len(web.Scopes) != 3 || web.Scopes[0] != domain.TelegramLoginScopeOpenID || web.Scopes[1] != domain.TelegramLoginScopeProfile || web.Scopes[2] != domain.TelegramLoginScopeBotAccess {
		t.Fatalf("granted scopes = %#v", web.Scopes)
	}
	permissions.mu.Lock()
	grants := permissions.grants[[2]int64{9001, 42}]
	permissions.mu.Unlock()
	if grants != 1 {
		t.Fatalf("bot permission grants = %d, want 1", grants)
	}
}

func TestTelegramLoginAcceptDeclineRaceHasOneTerminalState(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	s := NewTelegramLoginStore(nil)
	request := seedTelegramLoginRequest(t, s, now)
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, _, err := s.ApproveTelegramLoginRequest(context.Background(), domain.TelegramLoginApproval{
			RequestID: request.ID,
			Identity:  domain.TelegramLoginIdentitySnapshot{UserID: 42, Name: "Alice", GivenName: "Alice"},
			MatchCode: request.MatchCode, ApprovedAt: now.Add(time.Second),
		}, 7001)
		errs <- err
	}()
	go func() {
		<-start
		_, err := s.DeclineTelegramLoginRequest(context.Background(), request.ID, 42, now.Add(time.Second))
		errs <- err
	}()
	close(start)
	var success, conflict int
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			success++
		case errors.Is(err, domain.ErrTelegramLoginRequestConflict):
			conflict++
		default:
			t.Fatalf("unexpected race error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d, want 1/1", success, conflict)
	}
}

func TestTelegramLoginAuthorizationCodeSingleConsumeAndRevocation(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	s := NewTelegramLoginStore(nil)
	request := seedTelegramLoginRequest(t, s, now)
	approveTelegramLoginRequest(t, s, request, now.Add(time.Second))
	code := domain.TelegramLoginAuthorizationCode{
		RequestID:  request.ID,
		CodeHash:   telegramLoginTestHash("authorization-code"),
		SealedCode: append(make([]byte, 32), 1),
		SealNonce:  make([]byte, 12),
		SealKeyID:  "test-key",
		IssuedAt:   now.Add(2 * time.Second),
		ExpiresAt:  now.Add(time.Minute),
	}
	if _, err := s.PutTelegramLoginAuthorizationCode(context.Background(), code); err != nil {
		t.Fatalf("PutTelegramLoginAuthorizationCode: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			_, _, _, err := s.ConsumeTelegramLoginAuthorizationCode(context.Background(), domain.TelegramLoginCodeExchange{
				CodeHash: code.CodeHash, ClientID: request.ClientID, ClientSecretVersion: 1,
				RedirectURI: request.RedirectURI, CodeChallenge: request.CodeChallenge, Now: now.Add(3 * time.Second),
			})
			errs <- err
		}()
	}
	close(start)
	var success, consumed int
	for range 8 {
		err := <-errs
		switch {
		case err == nil:
			success++
		case errors.Is(err, domain.ErrTelegramLoginCodeConsumed):
			consumed++
		default:
			t.Fatalf("unexpected consume error: %v", err)
		}
	}
	if success != 1 || consumed != 7 {
		t.Fatalf("success=%d consumed=%d, want 1/7", success, consumed)
	}

	request2 := request.Clone()
	request2.ID = 0
	request2.RequestTokenHash = telegramLoginTestHash("request-token-2")
	request2.BrowserTokenHash = telegramLoginTestHash("browser-token-2")
	request2, err := s.CreateTelegramLoginRequest(context.Background(), request2)
	if err != nil {
		t.Fatalf("Create second request: %v", err)
	}
	_, web2 := approveTelegramLoginRequest(t, s, request2, now.Add(4*time.Second))
	code2 := code.Clone()
	code2.ID = 0
	code2.RequestID = request2.ID
	code2.CodeHash = telegramLoginTestHash("authorization-code-2")
	if _, err := s.PutTelegramLoginAuthorizationCode(context.Background(), code2); err != nil {
		t.Fatalf("Put second code: %v", err)
	}
	if revoked, err := s.RevokeTelegramLoginWebAuthorization(context.Background(), web2.UserID, web2.Hash, now.Add(5*time.Second)); err != nil || !revoked {
		t.Fatalf("RevokeTelegramLoginWebAuthorization = %v,%v", revoked, err)
	}
	if _, _, _, err := s.ConsumeTelegramLoginAuthorizationCode(context.Background(), domain.TelegramLoginCodeExchange{
		CodeHash: code2.CodeHash, ClientID: request2.ClientID, ClientSecretVersion: 1,
		RedirectURI: request2.RedirectURI, CodeChallenge: request2.CodeChallenge, Now: now.Add(6 * time.Second),
	}); !errors.Is(err, domain.ErrTelegramLoginCodeInvalid) {
		t.Fatalf("consume after revoke error = %v, want code invalid", err)
	}
}

func TestTelegramLoginRetentionPreservesActiveAndReferencedApprovals(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)
	before := now.Add(24 * time.Hour)
	s := NewTelegramLoginStore(nil)

	active := seedTelegramLoginRequest(t, s, now)
	_, activeWeb := approveTelegramLoginRequest(t, s, active, now.Add(time.Second))

	revoked := active.Clone()
	revoked.ID = 0
	revoked.RequestTokenHash = telegramLoginTestHash("retention-revoked-request")
	revoked.BrowserTokenHash = telegramLoginTestHash("retention-revoked-browser")
	revoked.Status = domain.TelegramLoginRequestPending
	revoked.AuthorizedUserID = 0
	revoked.ProfileName, revoked.GivenName, revoked.FamilyName = "", "", ""
	revoked.PreferredUsername, revoked.Picture, revoked.PhoneNumber = "", "", ""
	revoked.WriteAllowed, revoked.PhoneShared = false, false
	revoked.ApprovedAt = time.Time{}
	revoked, err := s.CreateTelegramLoginRequest(ctx, revoked)
	if err != nil {
		t.Fatalf("create revoked request: %v", err)
	}
	_, revokedWeb := approveTelegramLoginRequest(t, s, revoked, now.Add(2*time.Second))
	if ok, err := s.RevokeTelegramLoginWebAuthorization(ctx, revokedWeb.UserID, revokedWeb.Hash, now.Add(3*time.Second)); err != nil || !ok {
		t.Fatalf("revoke old authorization = %v,%v", ok, err)
	}

	referenced := revoked.Clone()
	referenced.ID = 0
	referenced.RequestTokenHash = telegramLoginTestHash("retention-referenced-request")
	referenced.BrowserTokenHash = telegramLoginTestHash("retention-referenced-browser")
	referenced.Status = domain.TelegramLoginRequestPending
	referenced.AuthorizedUserID = 0
	referenced.ProfileName, referenced.GivenName, referenced.FamilyName = "", "", ""
	referenced.PreferredUsername, referenced.Picture, referenced.PhoneNumber = "", "", ""
	referenced.WriteAllowed, referenced.PhoneShared = false, false
	referenced.ApprovedAt = time.Time{}
	referenced, err = s.CreateTelegramLoginRequest(ctx, referenced)
	if err != nil {
		t.Fatalf("create referenced request: %v", err)
	}
	_, referencedWeb := approveTelegramLoginRequest(t, s, referenced, now.Add(4*time.Second))
	if _, err := s.PutTelegramLoginAuthorizationCode(ctx, domain.TelegramLoginAuthorizationCode{
		RequestID: referenced.ID, CodeHash: telegramLoginTestHash("retention-live-code"),
		SealedCode: append(make([]byte, 32), 1), SealNonce: make([]byte, 12), SealKeyID: "test-key",
		IssuedAt: before.Add(time.Hour), ExpiresAt: before.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("put retained code: %v", err)
	}
	if ok, err := s.RevokeTelegramLoginWebAuthorization(ctx, referencedWeb.UserID, referencedWeb.Hash, now.Add(5*time.Second)); err != nil || !ok {
		t.Fatalf("revoke referenced authorization = %v,%v", ok, err)
	}

	deleted, err := s.DeleteExpiredTelegramLoginArtifacts(ctx, before, 100)
	if err != nil {
		t.Fatalf("delete expired artifacts: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want revoked request only", deleted)
	}
	if _, found, _ := s.GetTelegramLoginRequest(ctx, active.ID); !found {
		t.Fatal("active authorization request was deleted")
	}
	if _, found, _ := s.GetTelegramLoginRequest(ctx, referenced.ID); !found {
		t.Fatal("request with retained code was deleted")
	}
	if _, found, _ := s.GetTelegramLoginRequest(ctx, revoked.ID); found {
		t.Fatal("old revoked authorization request was retained")
	}
	listed, err := s.ListTelegramLoginWebAuthorizations(ctx, activeWeb.UserID)
	if err != nil || len(listed) != 1 || listed[0].Hash != activeWeb.Hash {
		t.Fatalf("active authorizations after retention = %#v, %v", listed, err)
	}
}
