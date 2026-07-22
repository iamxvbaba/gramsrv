package telegramlogin

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestServiceClientCreationAndSecretRotationAreSingleWinner(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0).UTC()
	service, loginStore := newTelegramLoginTestService(t, &now)

	const contenders = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	var created atomic.Int32
	var conflicts atomic.Int32
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.CreateClient(ctx, 9010, domain.TelegramLoginSigningRS256)
			switch {
			case err == nil:
				created.Add(1)
			case errors.Is(err, domain.ErrTelegramLoginRequestConflict):
				conflicts.Add(1)
			default:
				t.Errorf("CreateClient: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if created.Load() != 1 || conflicts.Load() != contenders-1 {
		t.Fatalf("create winners=%d conflicts=%d", created.Load(), conflicts.Load())
	}

	client, found, err := loginStore.GetTelegramLoginClientByBot(ctx, 9010)
	if err != nil || !found {
		t.Fatalf("GetTelegramLoginClientByBot: found=%v err=%v", found, err)
	}
	start = make(chan struct{})
	created.Store(0)
	conflicts.Store(0)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(seed byte) {
			defer wg.Done()
			<-start
			hash := make([]byte, 32)
			hash[0] = seed
			_, err := loginStore.RotateTelegramLoginClientSecret(ctx, client.BotUserID, client.SecretVersion, hash, now.Add(time.Second))
			switch {
			case err == nil:
				created.Add(1)
			case errors.Is(err, domain.ErrTelegramLoginRequestConflict):
				conflicts.Add(1)
			default:
				t.Errorf("RotateTelegramLoginClientSecret: %v", err)
			}
		}(byte(i + 1))
	}
	close(start)
	wg.Wait()
	if created.Load() != 1 || conflicts.Load() != contenders-1 {
		t.Fatalf("rotate winners=%d conflicts=%d", created.Load(), conflicts.Load())
	}
}

func newTelegramLoginTestService(t *testing.T, now *time.Time) (*Service, *memory.TelegramLoginStore) {
	return newTelegramLoginTestServiceWithConfig(t, now, nil, "")
}

func newTelegramLoginTestServiceWithAlgorithms(t *testing.T, now *time.Time, algorithms []domain.TelegramLoginSigningAlgorithm) (*Service, *memory.TelegramLoginStore) {
	return newTelegramLoginTestServiceWithConfig(t, now, algorithms, "")
}

func newTelegramLoginTestServiceWithAppLinkBase(t *testing.T, now *time.Time, appLinkBase string) (*Service, *memory.TelegramLoginStore) {
	return newTelegramLoginTestServiceWithConfig(t, now, nil, appLinkBase)
}

func newTelegramLoginTestServiceWithConfig(t *testing.T, now *time.Time, algorithms []domain.TelegramLoginSigningAlgorithm, appLinkBase string) (*Service, *memory.TelegramLoginStore) {
	t.Helper()
	key := make([]byte, 32)
	key[0] = 7
	sealer, err := NewCodeSealer("test", map[string][]byte{"test": key})
	if err != nil {
		t.Fatal(err)
	}
	loginStore := memory.NewTelegramLoginStore(nil)
	pepper := make([]byte, 32)
	pepper[0] = 9
	service, err := NewService(loginStore, sealer, Config{
		Issuer: "https://oauth.telesrv.test", AppScheme: "telesrv", AppLinkBase: appLinkBase,
		AllowHTTP: true, ClientSecretPepper: pepper,
		SupportedSigningAlgorithms: algorithms,
		Now:                        func() time.Time { return *now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, loginStore
}

func TestServiceAcceptsOfficialClientCanonicalOAuthDeepLinks(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0).UTC()
	service, _ := newTelegramLoginTestServiceWithAppLinkBase(t, &now, "owpg://tenant.example.test")
	credentials, err := service.CreateClient(ctx, 9030, domain.TelegramLoginSigningRS256)
	if err != nil {
		t.Fatal(err)
	}
	const redirectURI = "https://rp.example/callback"
	if _, err := service.AddAllowedURL(ctx, 9030, domain.TelegramLoginAllowedRedirectURI, redirectURI); err != nil {
		t.Fatal(err)
	}
	challenge, err := PKCEChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateAuthorization(ctx, CreateAuthorizationParams{
		ClientID: credentials.Client.ClientID, RedirectURI: redirectURI, ResponseType: "code",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(created.DeepLink)
	if err != nil {
		t.Fatal(err)
	}
	token := parsed.Query().Get("token")
	if got, want := parsed.Scheme+"://"+parsed.Host+parsed.Path, "owpg://tenant.example.test/oauth"; got != want {
		t.Fatalf("generated deep link root = %q, want %q", got, want)
	}
	valid := []string{
		created.DeepLink,
		"telesrv://oauth?token=" + url.QueryEscape(token),
		"telesrv://resolve?domain=oauth&startapp=" + url.QueryEscape(token),
		"tg://oauth?token=" + url.QueryEscape(token),
		"tg://resolve?domain=oauth&startapp=" + url.QueryEscape(token),
		"https://t.me/oauth?startapp=" + url.QueryEscape(token),
	}
	for _, deepLink := range valid {
		request, err := service.RequestByDeepLink(ctx, deepLink)
		if err != nil || request.ID != created.Request.ID {
			t.Fatalf("RequestByDeepLink(%q) request=%#v err=%v", deepLink, request, err)
		}
	}
	invalid := []string{
		"telegram://oauth?token=" + url.QueryEscape(token),
		"owpg://other.example.test/oauth?token=" + url.QueryEscape(token),
		"owpg://tenant.example.test/resolve?domain=oauth&startapp=" + url.QueryEscape(token),
		"owpg://tenant.example.test/oauth/extra?token=" + url.QueryEscape(token),
		"tg://oauth/path?token=" + url.QueryEscape(token),
		"tg://oauth?token=" + url.QueryEscape(token) + "&token=other",
		"tg://resolve?domain=oauth&domain=other&startapp=" + url.QueryEscape(token),
		"tg://resolve?domain=oauth&startapp=" + url.QueryEscape(token) + "&startapp=other",
		"tg://oauth?token=" + url.QueryEscape(token) + "#fragment",
	}
	for _, deepLink := range invalid {
		if _, err := service.RequestByDeepLink(ctx, deepLink); !errors.Is(err, domain.ErrTelegramLoginURLInvalid) {
			t.Fatalf("RequestByDeepLink(%q) error=%v, want URL invalid", deepLink, err)
		}
	}
}

func TestServiceRejectsSigningAlgorithmsWithoutActiveKeys(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0).UTC()
	service, loginStore := newTelegramLoginTestServiceWithAlgorithms(t, &now, []domain.TelegramLoginSigningAlgorithm{
		domain.TelegramLoginSigningES256,
	})
	if _, err := service.CreateClient(ctx, 9020, domain.TelegramLoginSigningRS256); !errors.Is(err, domain.ErrTelegramLoginClientInvalid) {
		t.Fatalf("CreateClient unsupported algorithm error=%v", err)
	}
	credentials, created, err := service.EnsureClient(ctx, 9020)
	if err != nil || !created || credentials.Client.SigningAlgorithm != domain.TelegramLoginSigningES256 {
		t.Fatalf("EnsureClient credentials=%#v created=%v err=%v", credentials, created, err)
	}
	if _, err := service.SetClientSigningAlgorithm(ctx, 9020, domain.TelegramLoginSigningEdDSA); !errors.Is(err, domain.ErrTelegramLoginClientInvalid) {
		t.Fatalf("SetClientSigningAlgorithm unsupported error=%v", err)
	}

	// Simulate configuration drift from a previous deployment. Authorization
	// must fail before a request is persisted instead of failing after consent.
	if _, err := loginStore.SetTelegramLoginClientSigningAlgorithm(ctx, 9020, domain.TelegramLoginSigningRS256, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := service.SetClientEnabled(ctx, 9020, false); err != nil {
		t.Fatal(err)
	}
	if err := service.SetClientEnabled(ctx, 9020, true); !errors.Is(err, domain.ErrTelegramLoginClientInvalid) {
		t.Fatalf("SetClientEnabled unavailable algorithm error=%v", err)
	}
	if _, err := service.AddAllowedURL(ctx, 9020, domain.TelegramLoginAllowedWebOrigin, "https://rp.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAuthorization(ctx, CreateAuthorizationParams{
		ClientID: credentials.Client.ClientID, RedirectURI: "https://rp.example/", ResponseType: "post_message",
		Scope: "openid profile",
	}); !errors.Is(err, domain.ErrTelegramLoginClientDisabled) {
		t.Fatalf("CreateAuthorization unavailable algorithm error=%v", err)
	}
}

func TestServiceAuthorizationCodeFlowAndRevocation(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0).UTC()
	service, _ := newTelegramLoginTestService(t, &now)
	credentials, err := service.CreateClient(ctx, 9001, domain.TelegramLoginSigningRS256)
	if err != nil {
		t.Fatalf("CreateClient: %v", err)
	}
	const redirectURI = "https://rp.example/callback"
	if _, err := service.AddAllowedURL(ctx, 9001, domain.TelegramLoginAllowedRedirectURI, redirectURI); err != nil {
		t.Fatalf("AddAllowedURL redirect: %v", err)
	}
	if _, err := service.AddAllowedURL(ctx, 9001, domain.TelegramLoginAllowedWebOrigin, "https://rp.example"); err != nil {
		t.Fatalf("AddAllowedURL origin: %v", err)
	}
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge, _ := PKCEChallenge(verifier)
	created, err := service.CreateAuthorization(ctx, CreateAuthorizationParams{
		ClientID: credentials.Client.ClientID, RedirectURI: redirectURI,
		ResponseType: "code", Scope: "openid profile phone telegram:bot_access",
		State: "opaque-state", Nonce: "nonce", CodeChallenge: challenge, CodeChallengeMethod: "S256",
		Browser: "Firefox", Platform: "Windows", IP: "192.0.2.10", Region: "Test Region",
		IncludeMatchCodes: true, MatchCodesFirst: true,
	})
	if err != nil {
		t.Fatalf("CreateAuthorization: %v", err)
	}
	if created.DeepLink == "" || created.Request.ID == 0 || len(created.Request.MatchCodes) != 5 {
		t.Fatalf("created authorization = %#v", created)
	}
	if !strings.HasPrefix(created.DeepLink, "telesrv://oauth?token=") {
		t.Fatalf("default deep link = %q, want legacy telesrv:// OAuth form", created.DeepLink)
	}
	if _, err := service.CheckMatchCode(ctx, created.DeepLink, created.Request.MatchCodes[0]); err == nil && created.Request.MatchCodes[0] != created.Request.MatchCode {
		t.Fatal("wrong match code unexpectedly accepted")
	}
	if ok, err := service.CheckMatchCode(ctx, created.DeepLink, created.Request.MatchCode); err != nil || !ok {
		t.Fatalf("CheckMatchCode correct = %v,%v", ok, err)
	}
	now = now.Add(time.Second)
	identity := domain.TelegramLoginIdentitySnapshot{
		UserID: 42, Name: "Alice Example", GivenName: "Alice", FamilyName: "Example",
		PreferredUsername: "alice", Picture: "https://oauth.telesrv.test/userpic/42",
	}
	approved, web, err := service.Approve(ctx, created.DeepLink, identity, true, false, created.Request.MatchCode)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.Status != domain.TelegramLoginRequestApproved || web.PhoneShared || !web.BotAccessGranted {
		t.Fatalf("approved=%#v web=%#v", approved, web)
	}
	if approved.ProfileName != "Alice Example" || approved.PhoneNumber != "" {
		t.Fatalf("identity snapshot = %#v", approved)
	}
	now = now.Add(time.Second)
	finalized, err := service.FinalizeByBrowserToken(ctx, created.BrowserToken)
	if err != nil {
		t.Fatalf("FinalizeByBrowserToken: %v", err)
	}
	redirect, err := url.Parse(finalized.RedirectURL)
	if err != nil || redirect.Query().Get("code") != finalized.Code || redirect.Query().Get("state") != "opaque-state" {
		t.Fatalf("final redirect = %q,%v", finalized.RedirectURL, err)
	}
	if _, err := service.ExchangeAuthorizationCode(ctx, ExchangeAuthorizationCodeParams{
		Code: finalized.Code, ClientID: credentials.Client.ClientID, ClientSecret: credentials.Secret,
		RedirectURI: redirectURI, CodeVerifier: verifier + "x",
	}); !errors.Is(err, domain.ErrTelegramLoginCodeInvalid) {
		t.Fatalf("exchange wrong verifier error = %v, want code invalid", err)
	}
	now = now.Add(time.Second)
	exchanged, err := service.ExchangeAuthorizationCode(ctx, ExchangeAuthorizationCodeParams{
		Code: finalized.Code, ClientID: credentials.Client.ClientID, ClientSecret: credentials.Secret,
		RedirectURI: redirectURI, CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode: %v", err)
	}
	if exchanged.Request.AuthorizedUserID != 42 || exchanged.WebAuthorization.Hash != web.Hash {
		t.Fatalf("exchanged = %#v", exchanged)
	}
	if _, err := service.ExchangeAuthorizationCode(ctx, ExchangeAuthorizationCodeParams{
		Code: finalized.Code, ClientID: credentials.Client.ClientID, ClientSecret: credentials.Secret,
		RedirectURI: redirectURI, CodeVerifier: verifier,
	}); !errors.Is(err, domain.ErrTelegramLoginCodeConsumed) {
		t.Fatalf("replay exchange error = %v, want consumed", err)
	}
	if err := service.RevokeWebAuthorization(ctx, 42, web.Hash); err != nil {
		t.Fatalf("RevokeWebAuthorization: %v", err)
	}
	if list, err := service.ListWebAuthorizations(ctx, 42); err != nil || len(list) != 0 {
		t.Fatalf("ListWebAuthorizations after revoke = %#v,%v", list, err)
	}
	if err := service.RevokeWebAuthorization(ctx, 42, web.Hash); !errors.Is(err, domain.ErrTelegramLoginWebAuthHashInvalid) {
		t.Fatalf("second revoke error = %v, want hash invalid", err)
	}
}

func TestFinalizationRetryRechecksLiveAuthorization(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0).UTC()
	service, _ := newTelegramLoginTestService(t, &now)
	credentials, err := service.CreateClient(ctx, 9010, domain.TelegramLoginSigningRS256)
	if err != nil {
		t.Fatal(err)
	}
	const redirectURI = "https://retry.example/callback"
	const origin = "https://retry.example"
	if _, err := service.AddAllowedURL(ctx, 9010, domain.TelegramLoginAllowedRedirectURI, redirectURI); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddAllowedURL(ctx, 9010, domain.TelegramLoginAllowedWebOrigin, origin); err != nil {
		t.Fatal(err)
	}
	challenge, err := PKCEChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if err != nil {
		t.Fatal(err)
	}
	codeRequest, err := service.CreateAuthorization(ctx, CreateAuthorizationParams{
		ClientID: credentials.Client.ClientID, RedirectURI: redirectURI, ResponseType: "code",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, codeWeb, err := service.Approve(ctx, codeRequest.DeepLink, domain.TelegramLoginIdentitySnapshot{UserID: 51}, false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinalizeByBrowserToken(ctx, codeRequest.BrowserToken); err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeWebAuthorization(ctx, 51, codeWeb.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinalizeByBrowserToken(ctx, codeRequest.BrowserToken); !errors.Is(err, domain.ErrTelegramLoginRequestConflict) {
		t.Fatalf("authorization-code retry after revoke error = %v, want conflict", err)
	}

	miniRequest, err := service.CreateAuthorization(ctx, CreateAuthorizationParams{
		ClientID: credentials.Client.ClientID, RedirectURI: origin + "/", ResponseType: "post_message", Scope: "openid",
		Origin: origin, InAppOrigin: origin, Source: domain.TelegramLoginRequestMiniApp,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, miniWeb, err := service.Approve(ctx, miniRequest.DeepLink, domain.TelegramLoginIdentitySnapshot{UserID: 52}, false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinalizeInAppRedirectByDeepLink(ctx, miniRequest.DeepLink); err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeWebAuthorization(ctx, 52, miniWeb.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinalizeInAppRedirectByDeepLink(ctx, miniRequest.DeepLink); !errors.Is(err, domain.ErrTelegramLoginRequestConflict) {
		t.Fatalf("Mini App token retry after revoke error = %v, want conflict", err)
	}
}

func TestServiceSecretRotationClosesExchangeTOCTOU(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0).UTC()
	service, _ := newTelegramLoginTestService(t, &now)
	oldCredentials, err := service.CreateClient(ctx, 9002, domain.TelegramLoginSigningRS256)
	if err != nil {
		t.Fatal(err)
	}
	const redirectURI = "https://rotate.example/callback"
	if _, err := service.AddAllowedURL(ctx, 9002, domain.TelegramLoginAllowedRedirectURI, redirectURI); err != nil {
		t.Fatal(err)
	}
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge, _ := PKCEChallenge(verifier)
	created, err := service.CreateAuthorization(ctx, CreateAuthorizationParams{
		ClientID: oldCredentials.Client.ClientID, RedirectURI: redirectURI, ResponseType: "code",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, _, err := service.Approve(ctx, created.DeepLink, domain.TelegramLoginIdentitySnapshot{UserID: 43}, false, false, ""); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	finalized, err := service.FinalizeByBrowserToken(ctx, created.BrowserToken)
	if err != nil {
		t.Fatal(err)
	}
	newCredentials, err := service.RotateClientSecret(ctx, 9002)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExchangeAuthorizationCode(ctx, ExchangeAuthorizationCodeParams{
		Code: finalized.Code, ClientID: oldCredentials.Client.ClientID, ClientSecret: oldCredentials.Secret,
		RedirectURI: redirectURI, CodeVerifier: verifier,
	}); !errors.Is(err, domain.ErrTelegramLoginSecretInvalid) {
		t.Fatalf("old secret exchange error = %v", err)
	}
	if _, err := service.ExchangeAuthorizationCode(ctx, ExchangeAuthorizationCodeParams{
		Code: finalized.Code, ClientID: newCredentials.Client.ClientID, ClientSecret: newCredentials.Secret,
		RedirectURI: redirectURI, CodeVerifier: verifier,
	}); err != nil {
		t.Fatalf("new secret exchange: %v", err)
	}
}
