package telegramloginhttp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	loginapp "telesrv/internal/app/telegramlogin"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestRequestIPTrustsForwardingHeadersOnlyFromConfiguredProxies(t *testing.T) {
	h := &Handler{trustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")}}
	req := httptest.NewRequest(http.MethodGet, "https://oauth.test/auth", nil)
	req.RemoteAddr = "127.0.0.1:44321"
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.4")
	if got := h.requestIP(req); got != "198.51.100.7" {
		t.Fatalf("trusted proxy client IP = %q", got)
	}
	req.RemoteAddr = "203.0.113.9:44321"
	req.Header.Set("X-Forwarded-For", "198.51.100.8")
	if got := h.requestIP(req); got != "203.0.113.9" {
		t.Fatalf("untrusted spoofed client IP = %q", got)
	}
}

func TestBoundedHeaderPreservesValidUTF8AtByteLimit(t *testing.T) {
	got := boundedHeader(strings.Repeat("界", 100)+string([]byte{0xff}), "fallback", 255)
	if !utf8.ValidString(got) || len(got) > 255 || got == "" {
		t.Fatalf("bounded header len=%d valid=%v value=%q", len(got), utf8.ValidString(got), got)
	}
}

type telegramLoginHTTPFixture struct {
	handler     *Handler
	service     *loginapp.Service
	credentials loginapp.ClientCredentials
	redirectURI string
	verifier    string
	challenge   string
	now         *time.Time
}

type telegramLoginHTTPDenyLimiter struct{}

func (telegramLoginHTTPDenyLimiter) Allow(context.Context, string, int, time.Duration) (bool, int, error) {
	return false, 17, nil
}

func newTelegramLoginHTTPFixture(t *testing.T) telegramLoginHTTPFixture {
	t.Helper()
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	sealKey := make([]byte, 32)
	sealKey[0] = 1
	sealer, err := loginapp.NewCodeSealer("test", map[string][]byte{"test": sealKey})
	if err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	pepper[0] = 2
	service, err := loginapp.NewService(memory.NewTelegramLoginStore(nil), sealer, loginapp.Config{
		Issuer: "https://oauth.telesrv.test", AppScheme: "telesrv", ClientSecretPepper: pepper,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := service.CreateClient(context.Background(), 9001, domain.TelegramLoginSigningRS256)
	if err != nil {
		t.Fatal(err)
	}
	const redirectURI = "https://rp.example/callback"
	if _, err := service.AddAllowedURL(context.Background(), 9001, domain.TelegramLoginAllowedRedirectURI, redirectURI); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddAllowedURL(context.Background(), 9001, domain.TelegramLoginAllowedWebOrigin, "https://rp.example"); err != nil {
		t.Fatal(err)
	}
	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ring, err := loginapp.NewSigningKeyRing([]loginapp.SigningKeyMaterial{{
		Algorithm: domain.TelegramLoginSigningRS256, KeyID: "rsa-test", PrivateKey: signingKey, Active: true,
	}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := loginapp.NewIDTokenIssuer(ring, loginapp.IDTokenIssuerConfig{
		Issuer: "https://oauth.telesrv.test", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(Config{Service: service, Tokens: tokens, AppName: "Telesrv", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge, err := loginapp.PKCEChallenge(verifier)
	if err != nil {
		t.Fatal(err)
	}
	return telegramLoginHTTPFixture{
		handler: handler, service: service, credentials: credentials, redirectURI: redirectURI,
		verifier: verifier, challenge: challenge, now: &now,
	}
}

func (f telegramLoginHTTPFixture) authorize(t *testing.T) (browserToken, deepLink string) {
	t.Helper()
	query := url.Values{
		"client_id": {f.credentials.Client.ClientID}, "redirect_uri": {f.redirectURI},
		"response_type": {"code"}, "scope": {"openid profile phone telegram:bot_access"},
		"state": {"state-value"}, "nonce": {"nonce-value"}, "code_challenge": {f.challenge},
		"code_challenge_method": {"S256"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/auth?"+query.Encode(), nil)
	request.RemoteAddr = "192.0.2.10:4242"
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorize status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	tokenMatch := regexp.MustCompile(`const token=("[^"]+")`).FindStringSubmatch(body)
	if len(tokenMatch) != 2 || json.Unmarshal([]byte(tokenMatch[1]), &browserToken) != nil {
		t.Fatalf("browser token not found in page: %s", body)
	}
	deepLinkMatch := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(body)
	if len(deepLinkMatch) != 2 {
		t.Fatalf("deep link not found in page: %s", body)
	}
	deepLink = html.UnescapeString(deepLinkMatch[1])
	pending, err := f.service.RequestByDeepLink(context.Background(), deepLink)
	if err != nil {
		t.Fatalf("resolve authorization page deep link: %v", err)
	}
	if pending.MatchCode == "" || !strings.Contains(body, `id="match-code"`) || !strings.Contains(body, pending.MatchCode) {
		t.Fatalf("matching emoji missing from authorization page: match=%q body=%s", pending.MatchCode, body)
	}
	if !strings.Contains(body, "poll();</script>") {
		t.Fatalf("authorization status polling is not started: %s", body)
	}
	return browserToken, deepLink
}

func TestAuthorizationErrorsUseOnlyPreRegisteredTargets(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	base := url.Values{
		"client_id": {f.credentials.Client.ClientID}, "redirect_uri": {f.redirectURI},
		"response_type": {"code"}, "scope": {"openid unsupported"}, "state": {"safe-state"},
		"code_challenge": {f.challenge}, "code_challenge_method": {"S256"},
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth?"+base.Encode(), nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("valid redirect error status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil || location.Scheme+"://"+location.Host+location.Path != f.redirectURI || location.Query().Get("error") != "invalid_scope" || location.Query().Get("state") != "safe-state" {
		t.Fatalf("error redirect=%q err=%v", recorder.Header().Get("Location"), err)
	}

	forged := cloneURLValues(base)
	forged.Set("redirect_uri", "https://attacker.example/callback")
	recorder = httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth?"+forged.Encode(), nil))
	if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Location") != "" {
		t.Fatalf("forged redirect status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}

	post := cloneURLValues(base)
	post.Set("redirect_uri", "https://rp.example/")
	post.Set("response_type", "post_message")
	post.Set("origin", "https://rp.example")
	recorder = httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth?"+post.Encode(), nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `postMessage`) || !strings.Contains(recorder.Body.String(), `https://rp.example`) {
		t.Fatalf("post_message error status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	post.Set("redirect_uri", "https://attacker.example/")
	recorder = httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth?"+post.Encode(), nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("cross-origin post_message status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func cloneURLValues(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func (f telegramLoginHTTPFixture) approveAndFinalize(t *testing.T) (code string) {
	t.Helper()
	browserToken, deepLink := f.authorize(t)
	*f.now = f.now.Add(time.Second)
	pending, err := f.service.RequestByDeepLink(context.Background(), deepLink)
	if err != nil {
		t.Fatalf("RequestByDeepLink(%q): %v", deepLink, err)
	}
	_, _, err = f.service.Approve(context.Background(), deepLink, domain.TelegramLoginIdentitySnapshot{
		UserID: 42, Name: "Alice Example", GivenName: "Alice", FamilyName: "Example",
		PreferredUsername: "alice", Picture: "https://oauth.telesrv.test/userpic/42", PhoneNumber: "+1 555 123 4567",
	}, true, true, pending.MatchCode)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"browser_token": {browserToken}}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/status", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Status      string `json:"status"`
		RedirectURL string `json:"redirect_url"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Status != "approved" {
		t.Fatalf("status response=%+v err=%v body=%s", response, err, recorder.Body.String())
	}
	redirect, err := url.Parse(response.RedirectURL)
	if err != nil || redirect.Query().Get("state") != "state-value" {
		t.Fatalf("redirect=%q err=%v", response.RedirectURL, err)
	}
	return redirect.Query().Get("code")
}

func TestDiscoveryAndJWKS(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	for _, path := range []string{"/.well-known/openid-configuration", "/.well-known/jwks.json"} {
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET %s status=%d headers=%v body=%s", path, recorder.Code, recorder.Header(), recorder.Body.String())
		}
	}
}

func TestAuthorizationCodeHTTPFlowAndReplay(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	code := f.approveAndFinalize(t)
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {f.redirectURI},
		"client_id": {f.credentials.Client.ClientID}, "code_verifier": {f.verifier},
	}
	exchange := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(f.credentials.Client.ClientID, f.credentials.Secret)
		f.handler.ServeHTTP(recorder, req)
		return recorder
	}
	recorder := exchange()
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var response struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.AccessToken == "" || response.IDToken == "" || response.TokenType != "Bearer" || response.ExpiresIn != 3600 {
		t.Fatalf("token response=%+v err=%v body=%s", response, err, recorder.Body.String())
	}
	jwksRecorder := httptest.NewRecorder()
	f.handler.ServeHTTP(jwksRecorder, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	set, err := jwk.Parse(jwksRecorder.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Parse([]byte(response.IDToken), jwt.WithKeySet(set), jwt.WithValidate(false))
	if err != nil || !token.Has("phone_number") || !token.Has("preferred_username") {
		t.Fatalf("verified ID token=%v err=%v", token, err)
	}
	replay := exchange()
	if replay.Code != http.StatusBadRequest || !strings.Contains(replay.Body.String(), "invalid_grant") {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}

func TestNativeSDKCrossAppAndPublicPKCEExchange(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	const callbackURI = "bedolaga://telegram-login"
	if _, err := f.service.AddNativeApp(context.Background(), 9001, domain.TelegramLoginNativeIOS,
		"dev.bedolaga.demo", "ABCDE12345", callbackURI, "Bedolaga Demo"); err != nil {
		t.Fatalf("AddNativeApp: %v", err)
	}
	query := url.Values{
		"client_id": {f.credentials.Client.ClientID}, "redirect_uri": {callbackURI},
		"response_type": {"code"}, "scope": {"profile"}, "ios_sdk": {"1"},
		"code_challenge": {f.challenge}, "code_challenge_method": {"S256"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/crossapp?"+query.Encode(), nil)
	request.Header.Set("User-Agent", "TelegramLogin/iOS")
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("crossapp status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var crossApp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &crossApp); err != nil || crossApp.URL == "" {
		t.Fatalf("crossapp response=%+v err=%v", crossApp, err)
	}
	pending, err := f.service.RequestByDeepLink(context.Background(), crossApp.URL)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Source != domain.TelegramLoginRequestNative || !pending.IsApp || pending.VerifiedAppName != "Bedolaga Demo" ||
		len(pending.Scopes) != 2 || pending.Scopes[0] != domain.TelegramLoginScopeOpenID {
		t.Fatalf("native request=%#v", pending)
	}
	*f.now = f.now.Add(time.Second)
	if _, _, err := f.service.Approve(context.Background(), crossApp.URL, domain.TelegramLoginIdentitySnapshot{
		UserID: 42, Name: "Alice", GivenName: "Alice",
	}, false, false, pending.MatchCode); err != nil {
		t.Fatal(err)
	}
	redirectURL, err := f.service.FinalizeRedirectByDeepLink(context.Background(), crossApp.URL)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(redirectURL)
	if err != nil || callback.Scheme != "bedolaga" || callback.Host != "telegram-login" || callback.Query().Get("code") == "" {
		t.Fatalf("native callback=%q err=%v", redirectURL, err)
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {f.credentials.Client.ClientID},
		"code": {callback.Query().Get("code")}, "redirect_uri": {callbackURI}, "code_verifier": {f.verifier},
	}
	tokenRecorder := httptest.NewRecorder()
	tokenRequest := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.handler.ServeHTTP(tokenRecorder, tokenRequest)
	if tokenRecorder.Code != http.StatusOK || !strings.Contains(tokenRecorder.Body.String(), `"id_token"`) {
		t.Fatalf("native token status=%d body=%s", tokenRecorder.Code, tokenRecorder.Body.String())
	}

	query.Set("android_sdk", "1")
	recorder = httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/crossapp?"+query.Encode(), nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("dual SDK marker status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicTokenAuthenticationCannotExchangeWebCode(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	code := f.approveAndFinalize(t)
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {f.credentials.Client.ClientID},
		"code": {code}, "redirect_uri": {f.redirectURI}, "code_verifier": {f.verifier},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "invalid_client") {
		t.Fatalf("public web exchange status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// Authentication failure happens before the one-time consume, so the same
	// code remains usable by the confidential web client.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(f.credentials.Client.ClientID, f.credentials.Secret)
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authenticated retry status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestConcurrentTokenExchangeHasOneSuccess(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	code := f.approveAndFinalize(t)
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {f.redirectURI},
		"client_id": {f.credentials.Client.ClientID}, "code_verifier": {f.verifier},
	}.Encode()
	const workers = 8
	statuses := make(chan int, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetBasicAuth(f.credentials.Client.ClientID, f.credentials.Secret)
			f.handler.ServeHTTP(recorder, req)
			statuses <- recorder.Code
		}()
	}
	wg.Wait()
	close(statuses)
	success := 0
	for status := range statuses {
		if status == http.StatusOK {
			success++
		} else if status != http.StatusBadRequest {
			t.Fatalf("unexpected concurrent exchange status %d", status)
		}
	}
	if success != 1 {
		t.Fatalf("successful exchanges=%d, want 1", success)
	}
}

func TestTokenEndpointSupportsBodySecretAndRejectsMixedAuthentication(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	code := f.approveAndFinalize(t)
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {f.credentials.Client.ClientID},
		"client_secret": {f.credentials.Secret}, "code": {code}, "redirect_uri": {f.redirectURI},
		"code_verifier": {f.verifier},
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id_token"`) {
		t.Fatalf("client_secret_post status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	secondCode := f.approveAndFinalize(t)
	form.Set("code", secondCode)
	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(f.credentials.Client.ClientID, f.credentials.Secret)
	f.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_request") {
		t.Fatalf("mixed authentication status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	f.handler.ServeHTTP(recorder, req)
	_, _ = io.Copy(io.Discard, recorder.Result().Body)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("JSON token content status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHTTPRateLimitFailsBeforeAuthorizationCreation(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	f.handler.limiter = telegramLoginHTTPDenyLimiter{}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth", nil))
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "17" {
		t.Fatalf("rate limit status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestJavaScriptPostMessageFlowReturnsStableDirectIDToken(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	query := url.Values{
		"client_id": {f.credentials.Client.ClientID}, "redirect_uri": {"https://rp.example/"},
		"response_type": {"post_message"}, "scope": {"openid profile phone"},
		"nonce": {"js-nonce"},
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/auth?"+query.Encode(), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("JS authorize status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cross-Origin-Opener-Policy"); got != "unsafe-none" {
		t.Fatalf("JS authorize COOP=%q, want unsafe-none for cross-origin opener handoff", got)
	}
	body := recorder.Body.String()
	tokenMatch := regexp.MustCompile(`const token=("[^"]+")`).FindStringSubmatch(body)
	deepLinkMatch := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(body)
	if len(tokenMatch) != 2 || len(deepLinkMatch) != 2 {
		t.Fatalf("JS authorize artifacts missing: %s", body)
	}
	if !strings.Contains(body, "auth_pending") || !strings.Contains(body, "browser_token") {
		t.Fatalf("JS authorize page does not hand parent polling off before the app round-trip: %s", body)
	}
	var browserToken string
	if err := json.Unmarshal([]byte(tokenMatch[1]), &browserToken); err != nil {
		t.Fatal(err)
	}
	requestStatus := func(origin string) *httptest.ResponseRecorder {
		t.Helper()
		statusRecorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/auth/status", strings.NewReader(url.Values{"browser_token": {browserToken}}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		f.handler.ServeHTTP(statusRecorder, request)
		return statusRecorder
	}
	attackerStatus := requestStatus("https://attacker.example")
	if attackerStatus.Code != http.StatusForbidden || attackerStatus.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("attacker parent poll status=%d headers=%v body=%s", attackerStatus.Code, attackerStatus.Header(), attackerStatus.Body.String())
	}
	for _, origin := range []string{"https://rp.example", "https://oauth.telesrv.test"} {
		pendingStatus := requestStatus(origin)
		if pendingStatus.Code != http.StatusOK || !strings.Contains(pendingStatus.Body.String(), `"status":"pending"`) {
			t.Fatalf("pending parent poll origin=%q status=%d body=%s", origin, pendingStatus.Code, pendingStatus.Body.String())
		}
		allowedOrigin := pendingStatus.Header().Get("Access-Control-Allow-Origin")
		if origin == "https://rp.example" {
			if allowedOrigin != origin || !strings.Contains(pendingStatus.Header().Get("Vary"), "Origin") {
				t.Fatalf("RP parent poll origin=%q headers=%v", origin, pendingStatus.Header())
			}
		} else if allowedOrigin != "" {
			t.Fatalf("same-origin popup unexpectedly received CORS header: %v", pendingStatus.Header())
		}
	}
	deepLink := html.UnescapeString(deepLinkMatch[1])
	pending, err := f.service.RequestByDeepLink(context.Background(), deepLink)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Source != domain.TelegramLoginRequestJavaScript || pending.Origin != "https://rp.example" || pending.CodeChallenge != "" {
		t.Fatalf("official JavaScript request = %#v", pending)
	}
	*f.now = f.now.Add(time.Second)
	if _, _, err := f.service.Approve(context.Background(), deepLink, domain.TelegramLoginIdentitySnapshot{
		UserID: 42, Name: "Alice Example", GivenName: "Alice", PhoneNumber: "15551234567",
	}, false, true, pending.MatchCode); err != nil {
		t.Fatal(err)
	}
	poll := func() map[string]string {
		t.Helper()
		statusRecorder := requestStatus("https://rp.example")
		if statusRecorder.Code != http.StatusOK {
			t.Fatalf("JS status=%d body=%s", statusRecorder.Code, statusRecorder.Body.String())
		}
		if statusRecorder.Header().Get("Access-Control-Allow-Origin") != "https://rp.example" {
			t.Fatalf("JS status CORS headers=%v", statusRecorder.Header())
		}
		var result map[string]string
		if err := json.Unmarshal(statusRecorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first, second := poll(), poll()
	if first["status"] != "approved" || first["id_token"] == "" || second["id_token"] != first["id_token"] {
		t.Fatalf("direct token first=%v second=%v", first, second)
	}
	web, err := f.service.ListWebAuthorizations(context.Background(), 42)
	if err != nil || len(web) != 1 {
		t.Fatalf("direct web authorization=%#v err=%v", web, err)
	}
	if err := f.service.RevokeWebAuthorization(context.Background(), 42, web[0].Hash); err != nil {
		t.Fatal(err)
	}
	revokedRecorder := httptest.NewRecorder()
	revokedRequest := httptest.NewRequest(http.MethodPost, "/auth/status", strings.NewReader(url.Values{"browser_token": {browserToken}}.Encode()))
	revokedRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.handler.ServeHTTP(revokedRecorder, revokedRequest)
	if revokedRecorder.Code != http.StatusInternalServerError || !strings.Contains(revokedRecorder.Body.String(), "server_error") {
		t.Fatalf("revoked direct delivery status=%d body=%s", revokedRecorder.Code, revokedRecorder.Body.String())
	}

	exchangeForm := url.Values{
		"grant_type": {"authorization_code"}, "code": {first["id_token"]}, "redirect_uri": {"https://rp.example/"},
		"client_id": {f.credentials.Client.ClientID}, "code_verifier": {f.verifier},
	}
	exchangeRecorder := httptest.NewRecorder()
	exchangeRequest := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(exchangeForm.Encode()))
	exchangeRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	exchangeRequest.SetBasicAuth(f.credentials.Client.ClientID, f.credentials.Secret)
	f.handler.ServeHTTP(exchangeRecorder, exchangeRequest)
	if exchangeRecorder.Code != http.StatusBadRequest || !strings.Contains(exchangeRecorder.Body.String(), "invalid_grant") {
		t.Fatalf("direct token exchange status=%d body=%s", exchangeRecorder.Code, exchangeRecorder.Body.String())
	}
}

func TestMiniAppOfficialInAppFlowIsOriginBoundAndOneTime(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	const origin = "https://rp.example"
	query := url.Values{
		"client_id": {f.credentials.Client.ClientID}, "scope": {"openid profile phone telegram:bot_access"},
		"origin": {origin}, "response_type": {"id_token"},
	}
	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/inapp?"+query.Encode(), nil)
	request.Header.Set("Origin", origin)
	f.handler.ServeHTTP(create, request)
	if create.Code != http.StatusOK || create.Header().Get("Access-Control-Allow-Origin") != origin || create.Header().Get("Vary") != "Origin" {
		t.Fatalf("in-app create status=%d headers=%v body=%s", create.Code, create.Header(), create.Body.String())
	}
	var created struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.URL == "" {
		t.Fatalf("in-app create response=%+v err=%v", created, err)
	}
	pending, err := f.service.RequestByDeepLinkForOrigin(context.Background(), created.URL, origin)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Source != domain.TelegramLoginRequestMiniApp || pending.InAppOrigin != origin || pending.ResponseType != "post_message" {
		t.Fatalf("in-app request=%#v", pending)
	}
	*f.now = f.now.Add(time.Second)
	if _, _, err := f.service.Approve(context.Background(), created.URL, domain.TelegramLoginIdentitySnapshot{
		UserID: 42, Name: "Alice Example", GivenName: "Alice", PhoneNumber: "15551234567",
	}, true, true, pending.MatchCode); err != nil {
		t.Fatal(err)
	}
	resultURL, err := f.service.FinalizeInAppRedirectByDeepLink(context.Background(), created.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(resultURL)
	if err != nil || parsed.Scheme+"://"+parsed.Host != "https://oauth.telesrv.test" || parsed.Path != "/inapp" || parsed.Query().Get("token") == "" {
		t.Fatalf("in-app result URL=%q err=%v", resultURL, err)
	}
	token := parsed.Query().Get("token")

	wrongOrigin := httptest.NewRecorder()
	wrongRequest := httptest.NewRequest(http.MethodGet, "/inapp?code="+url.QueryEscape(token), nil)
	wrongRequest.Header.Set("Origin", "https://attacker.example")
	f.handler.ServeHTTP(wrongOrigin, wrongRequest)
	if wrongOrigin.Code != http.StatusBadRequest {
		t.Fatalf("wrong-origin exchange status=%d body=%s", wrongOrigin.Code, wrongOrigin.Body.String())
	}

	const workers = 8
	statuses := make(chan int, workers)
	results := make(chan string, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/inapp?code="+url.QueryEscape(token), nil)
			req.Header.Set("Origin", origin)
			f.handler.ServeHTTP(recorder, req)
			statuses <- recorder.Code
			results <- recorder.Body.String()
		}()
	}
	wg.Wait()
	close(statuses)
	close(results)
	successes := 0
	responses := make([]string, 0, workers)
	for status := range statuses {
		if status == http.StatusOK {
			successes++
		} else if status != http.StatusBadRequest {
			t.Fatalf("unexpected in-app exchange status=%d", status)
		}
	}
	for response := range results {
		responses = append(responses, response)
	}
	if successes != 1 {
		t.Fatalf("in-app exchange successes=%d, want 1; responses=%v", successes, responses)
	}
}

func TestTelegramLoginJavaScriptIsCacheableAndConditional(t *testing.T) {
	f := newTelegramLoginHTTPFixture(t)
	first := httptest.NewRecorder()
	f.handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/js/telegram-login.js", nil))
	if first.Code != http.StatusOK || first.Header().Get("ETag") == "" ||
		!strings.Contains(first.Body.String(), "Telegram.Login") ||
		!strings.Contains(first.Body.String(), "auth_result") ||
		!strings.Contains(first.Body.String(), "auth_pending") ||
		!strings.Contains(first.Body.String(), "pollFromParent") ||
		!strings.Contains(first.Body.String(), "/auth/status") ||
		!strings.Contains(first.Body.String(), "oauth_supported") ||
		!strings.Contains(first.Body.String(), "/inapp?") ||
		!strings.Contains(first.Body.String(), "data-client-id") {
		t.Fatalf("SDK status=%d headers=%v body=%s", first.Code, first.Header(), first.Body.String())
	}
	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/js/telegram-login.js", nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	f.handler.ServeHTTP(second, request)
	if second.Code != http.StatusNotModified {
		t.Fatalf("conditional SDK status=%d", second.Code)
	}
}
