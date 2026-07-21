// Package telegramloginhttp is the public HTTP adapter for Telegram Login and
// OpenID Connect. It contains protocol parsing/rendering only; durable state
// and authorization transitions remain in app/telegramlogin and its store.
package telegramloginhttp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	loginapp "telesrv/internal/app/telegramlogin"
	"telesrv/internal/domain"
)

const (
	maxAuthorizationQueryBytes = 16 << 10
	maxTokenFormBytes          = 16 << 10
	maxStatusFormBytes         = 4 << 10
)

type Config struct {
	Service           *loginapp.Service
	Tokens            *loginapp.IDTokenIssuer
	Limiter           RateLimiter
	AppName           string
	Logger            *zap.Logger
	TrustedProxyCIDRs []string
	AllowHTTP         bool
}

type Handler struct {
	service        *loginapp.Service
	tokens         *loginapp.IDTokenIssuer
	appName        string
	logger         *zap.Logger
	limiter        RateLimiter
	trustedProxies []netip.Prefix
	allowHTTP      bool
	mux            *http.ServeMux
}

type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
}

func NewHandler(cfg Config) (*Handler, error) {
	if cfg.Service == nil || cfg.Tokens == nil || cfg.Tokens.Issuer() == "" {
		return nil, errors.New("telegram login HTTP dependencies are incomplete")
	}
	if strings.TrimSpace(cfg.AppName) == "" {
		cfg.AppName = "Telesrv"
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	trustedProxies := make([]netip.Prefix, 0, len(cfg.TrustedProxyCIDRs))
	for _, raw := range cfg.TrustedProxyCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("telegram login trusted proxy CIDR %q: %w", raw, err)
		}
		trustedProxies = append(trustedProxies, prefix.Masked())
	}
	h := &Handler{service: cfg.Service, tokens: cfg.Tokens, appName: strings.TrimSpace(cfg.AppName), logger: cfg.Logger, limiter: cfg.Limiter, trustedProxies: trustedProxies, allowHTTP: cfg.AllowHTTP}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", h.discovery)
	mux.HandleFunc("GET /.well-known/jwks.json", h.jwks)
	mux.HandleFunc("GET /auth", h.authorize)
	mux.HandleFunc("GET /crossapp", h.crossApp)
	mux.HandleFunc("GET /inapp", h.inApp)
	mux.HandleFunc("POST /auth/status", h.authorizationStatus)
	mux.HandleFunc("POST /token", h.token)
	mux.HandleFunc("GET /telegram-login.js", h.loginJavaScript)
	mux.HandleFunc("GET /js/telegram-login.js", h.loginJavaScript)
	h.mux = mux
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin-allow-popups")
	h.mux.ServeHTTP(w, r)
}

type discoveryDocument struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	JWKSURI                       string   `json:"jwks_uri"`
	ScopesSupported               []string `json:"scopes_supported"`
	ResponseTypesSupported        []string `json:"response_types_supported"`
	ResponseModesSupported        []string `json:"response_modes_supported"`
	GrantTypesSupported           []string `json:"grant_types_supported"`
	SubjectTypesSupported         []string `json:"subject_types_supported"`
	IDTokenSigningAlgorithms      []string `json:"id_token_signing_alg_values_supported"`
	TokenEndpointAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	ClaimsSupported               []string `json:"claims_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
}

func (h *Handler) discovery(w http.ResponseWriter, _ *http.Request) {
	issuer := h.tokens.Issuer()
	writeJSON(w, http.StatusOK, discoveryDocument{
		Issuer: issuer, AuthorizationEndpoint: issuer + "/auth", TokenEndpoint: issuer + "/token",
		JWKSURI:                issuer + "/.well-known/jwks.json",
		ScopesSupported:        []string{"openid", "profile", "phone", "telegram:bot_access"},
		ResponseTypesSupported: []string{"code"}, ResponseModesSupported: []string{"query"},
		GrantTypesSupported: []string{"authorization_code"}, SubjectTypesSupported: []string{"public"},
		IDTokenSigningAlgorithms: h.tokens.SupportedAlgorithms(),
		TokenEndpointAuthMethods: []string{"client_secret_basic", "client_secret_post", "none"},
		ClaimsSupported: []string{
			"iss", "aud", "sub", "iat", "exp", "nonce", "id", "name", "given_name",
			"family_name", "preferred_username", "picture", "phone_number", "phone_number_verified",
		},
		CodeChallengeMethodsSupported: []string{"S256"},
	})
}

func (h *Handler) jwks(w http.ResponseWriter, r *http.Request) {
	body, etag, err := h.tokens.JWKS()
	if err != nil {
		h.logger.Error("telegram_login_jwks_failed", zap.Error(err))
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "key service unavailable")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

type authorizationPageData struct {
	AppName      string
	DeepLink     template.URL
	MatchCode    string
	BrowserToken string
	ExpiresAt    string
	CSPNonce     string
	ResponseType string
	TargetOrigin string
}

var authorizationPage = template.Must(template.New("telegram-login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Log in with {{.AppName}}</title><style>
:root{color-scheme:light dark}body{font:16px/1.45 system-ui,sans-serif;margin:0;background:#17212b;color:#fff}.card{max-width:460px;margin:10vh auto;padding:28px;border-radius:18px;background:#202b36;box-shadow:0 16px 48px #0006}h1{margin:.1em 0 .5em}.button{display:block;text-align:center;margin:24px 0;padding:13px 18px;border-radius:11px;background:#2aabee;color:#fff;text-decoration:none;font-weight:700}.match{text-align:center;margin:22px 0;padding:18px;border-radius:14px;background:#17212b}.match p{margin:0 0 8px}.match-code{font-size:44px;line-height:1.2}.muted{color:#a9b5c1;font-size:14px}.error{color:#ff8d8d}</style></head>
<body><main class="card"><h1>Log in with {{.AppName}}</h1><p>Open the {{.AppName}} app and approve this request. Keep this page open.</p><a class="button" href="{{.DeepLink}}">Open {{.AppName}}</a>{{if .MatchCode}}<section class="match" aria-labelledby="match-title"><p id="match-title">When prompted, select this emoji in {{.AppName}}:</p><div id="match-code" class="match-code" role="img" aria-label="Matching emoji">{{.MatchCode}}</div></section>{{end}}<p id="status" class="muted">Waiting for approval…</p><p class="muted">This request expires at {{.ExpiresAt}}.</p></main>
<script nonce="{{.CSPNonce}}">const token={{.BrowserToken}},responseType={{.ResponseType}},targetOrigin={{.TargetOrigin}};const statusNode=document.getElementById('status');function notifyPending(){if(responseType!=='post_message'||!window.opener||!targetOrigin)return;try{window.opener.postMessage({event:'auth_pending',browser_token:token},targetOrigin)}catch(_){}}function deliver(data){if(!window.opener||!targetOrigin){statusNode.className='muted';statusNode.textContent=data.id_token?'Login approved. Return to the original login tab.':'Login finished. Return to the original login tab.';return}const payload=data.id_token?{event:'auth_result',result:data.id_token}:{event:'auth_result',error:data.error||data.status};window.opener.postMessage(payload,targetOrigin);window.close()}async function poll(){try{const body=new URLSearchParams({browser_token:token});const response=await fetch('/auth/status',{method:'POST',headers:{'content-type':'application/x-www-form-urlencoded'},body,cache:'no-store'});const data=await response.json();if(!response.ok){throw new Error(data.error||'request_failed')}if(data.status==='pending'){setTimeout(poll,1000);return}if(responseType==='post_message'){deliver(data);return}if(data.redirect_url){location.replace(data.redirect_url);return}throw new Error('invalid_response')}catch(error){statusNode.className='error';statusNode.textContent='Login status unavailable. Please restart the login flow.'}}notifyPending();poll();</script></body></html>`))

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) {
	// Authorization popups must retain their cross-origin opener long enough to
	// hand the registered RP a short-lived browser token. The default
	// same-origin-allow-popups policy isolates a document that was itself opened
	// cross-origin, so it breaks the exact-origin postMessage flow before the
	// external Telegram app is launched. Other provider endpoints keep the
	// stricter default policy.
	w.Header().Set("Cross-Origin-Opener-Policy", "unsafe-none")
	clientIP := h.requestIP(r)
	if !h.allow(w, r, "authorize", clientIP, 30, time.Minute) {
		return
	}
	if len(r.URL.RawQuery) > maxAuthorizationQueryBytes {
		h.authorizationRequestError(w)
		return
	}
	query := r.URL.Query()
	nativePlatform, nativeMarkerOK := nativeSDKPlatform(query)
	if !nativeMarkerOK {
		h.authorizationRequestError(w)
		return
	}
	values := make(map[string]string, 9)
	for _, key := range []string{"client_id", "redirect_uri", "response_type", "scope", "state", "nonce", "code_challenge", "code_challenge_method", "origin"} {
		value, ok := singleValue(query, key)
		if !ok {
			h.authorizationRequestError(w)
			return
		}
		values[key] = value
	}
	platformLabel := "Web"
	if nativePlatform == domain.TelegramLoginNativeIOS {
		platformLabel = "iOS"
	} else if nativePlatform == domain.TelegramLoginNativeAndroid {
		platformLabel = "Android"
	}
	source := domain.TelegramLoginRequestWeb
	if values["response_type"] == "post_message" {
		source = domain.TelegramLoginRequestJavaScript
	}
	created, err := h.service.CreateAuthorization(r.Context(), loginapp.CreateAuthorizationParams{
		ClientID: values["client_id"], RedirectURI: values["redirect_uri"], ResponseType: values["response_type"],
		Scope: values["scope"], State: values["state"], Nonce: values["nonce"],
		CodeChallenge: values["code_challenge"], CodeChallengeMethod: values["code_challenge_method"],
		Origin: values["origin"],
		Source: source, NativePlatform: nativePlatform,
		Browser:  boundedHeader(r.UserAgent(), "Unknown browser", 255),
		Platform: platformLabel, IP: clientIP, Region: "Unknown region", IncludeMatchCodes: true, MatchCodesFirst: true,
	})
	if err != nil {
		h.logger.Info("telegram_login_authorize_rejected", zap.String("error", errorClass(err)))
		h.authorizationError(w, r, values, err)
		return
	}
	cspNonce, err := loginapp.GenerateOpaqueToken()
	if err != nil {
		h.logger.Error("telegram_login_csp_nonce_failed", zap.Error(err))
		h.authorizationRequestError(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+cspNonce+"'; connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := authorizationPage.Execute(w, authorizationPageData{
		AppName: h.appName, DeepLink: template.URL(created.DeepLink), MatchCode: created.Request.MatchCode, BrowserToken: created.BrowserToken,
		ExpiresAt: created.Request.ExpiresAt.UTC().Format(time.RFC3339), CSPNonce: cspNonce,
		ResponseType: created.Request.ResponseType, TargetOrigin: created.Request.Origin,
	}); err != nil {
		h.logger.Warn("telegram_login_authorize_render_failed", zap.Error(err))
	}
}

func (h *Handler) crossApp(w http.ResponseWriter, r *http.Request) {
	clientIP := h.requestIP(r)
	if !h.allow(w, r, "crossapp", clientIP, 30, time.Minute) {
		return
	}
	if len(r.URL.RawQuery) > maxAuthorizationQueryBytes {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid native login request")
		return
	}
	query := r.URL.Query()
	platform, ok := nativeSDKPlatform(query)
	if !ok || !platform.Valid() {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "exactly one native SDK marker is required")
		return
	}
	values := make(map[string]string, 8)
	for _, key := range []string{"client_id", "redirect_uri", "response_type", "scope", "state", "nonce", "code_challenge", "code_challenge_method"} {
		value, unique := singleValue(query, key)
		if !unique {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "duplicate parameter")
			return
		}
		values[key] = value
	}
	platformLabel := "iOS"
	if platform == domain.TelegramLoginNativeAndroid {
		platformLabel = "Android"
	}
	created, err := h.service.CreateAuthorization(r.Context(), loginapp.CreateAuthorizationParams{
		ClientID: values["client_id"], RedirectURI: values["redirect_uri"], ResponseType: values["response_type"],
		Scope: values["scope"], State: values["state"], Nonce: values["nonce"],
		CodeChallenge: values["code_challenge"], CodeChallengeMethod: values["code_challenge_method"],
		Source: domain.TelegramLoginRequestNative, NativePlatform: platform,
		Browser: boundedHeader(r.UserAgent(), "Native SDK", 255), Platform: platformLabel,
		IP: clientIP, Region: "Unknown region", IncludeMatchCodes: true, MatchCodesFirst: true,
	})
	if err != nil {
		h.logger.Info("telegram_login_crossapp_rejected", zap.String("error", errorClass(err)))
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "native login request is not registered")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]string{"url": created.DeepLink})
}

func (h *Handler) inApp(w http.ResponseWriter, r *http.Request) {
	clientIP := h.requestIP(r)
	if !h.allow(w, r, "inapp", clientIP, 60, time.Minute) {
		return
	}
	if len(r.URL.RawQuery) > maxAuthorizationQueryBytes {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid in-app login request")
		return
	}
	query := r.URL.Query()
	if rawCode, exists := query["code"]; exists {
		if len(query) != 1 || len(rawCode) != 1 || rawCode[0] == "" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid in-app token")
			return
		}
		issued, err := h.service.ExchangeInAppTokenAndIssue(r.Context(), rawCode[0], r.Header.Get("Origin"), h.tokens)
		if err != nil {
			h.logger.Info("telegram_login_inapp_exchange_rejected", zap.String("error", errorClass(err)))
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "in-app token is invalid or expired")
			return
		}
		setInAppCORS(w, issued.Request.InAppOrigin)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		writeJSON(w, http.StatusOK, map[string]string{"result": issued.IDToken})
		return
	}
	if len(query) != 4 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid in-app login request")
		return
	}
	values := make(map[string]string, 4)
	for _, key := range []string{"client_id", "scope", "origin", "response_type"} {
		value, unique := singleValue(query, key)
		if !unique || value == "" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid in-app login request")
			return
		}
		values[key] = value
	}
	if values["response_type"] != "id_token" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_response_type", "only id_token is supported")
		return
	}
	origin, err := loginapp.NormalizeWebOrigin(values["origin"], h.allowHTTP)
	if err != nil || r.Header.Get("Origin") != origin {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "in-app origin is invalid")
		return
	}
	setInAppCORS(w, origin)
	created, err := h.service.CreateAuthorization(r.Context(), loginapp.CreateAuthorizationParams{
		ClientID: values["client_id"], RedirectURI: origin + "/", ResponseType: "post_message",
		Scope: values["scope"], Origin: origin, InAppOrigin: origin,
		Source:  domain.TelegramLoginRequestMiniApp,
		Browser: boundedHeader(r.UserAgent(), "Telegram Mini App", 255), Platform: "Telegram Mini App",
		IP: clientIP, Region: "Unknown region", IncludeMatchCodes: true, MatchCodesFirst: true,
	})
	if err != nil {
		h.logger.Info("telegram_login_inapp_rejected", zap.String("error", errorClass(err)))
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "in-app login request is not registered")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]string{"url": created.DeepLink})
}

func setInAppCORS(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
}

func nativeSDKPlatform(values url.Values) (domain.TelegramLoginNativePlatform, bool) {
	ios, iosUnique := singleValue(values, "ios_sdk")
	android, androidUnique := singleValue(values, "android_sdk")
	if !iosUnique || !androidUnique || (ios != "" && ios != "1") || (android != "" && android != "1") || (ios != "" && android != "") {
		return "", false
	}
	if ios == "1" {
		return domain.TelegramLoginNativeIOS, true
	}
	if android == "1" {
		return domain.TelegramLoginNativeAndroid, true
	}
	return "", true
}

var authorizationErrorPage = template.Must(template.New("telegram-login-error").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Telegram Login</title></head><body><p>Login could not be started.</p><script nonce="{{.Nonce}}">if(window.opener){window.opener.postMessage({event:'auth_result',error:{{.Error}}},{{.Origin}});window.close();}</script></body></html>`))

func (h *Handler) authorizationError(w http.ResponseWriter, r *http.Request, values map[string]string, cause error) {
	target, safe, err := h.service.ResolveAuthorizationErrorTarget(r.Context(), values["client_id"], values["response_type"], values["redirect_uri"], values["origin"])
	if err != nil || !safe {
		if err != nil {
			h.logger.Error("telegram_login_authorize_error_target_failed", zap.String("error", errorClass(err)))
		}
		h.authorizationRequestError(w)
		return
	}
	code := "invalid_request"
	switch {
	case errors.Is(cause, domain.ErrTelegramLoginScopeInvalid):
		code = "invalid_scope"
	case values["response_type"] != "code" && values["response_type"] != "post_message":
		code = "unsupported_response_type"
	default:
		// Known validation failures use invalid_request. Unknown failures are
		// reported as server_error without exposing their details.
		if strings.HasPrefix(errorClass(cause), "internal_") {
			code = "server_error"
		}
	}
	state := values["state"]
	if len(state) > 2048 {
		state = ""
	}
	if target.ResponseType == "code" {
		redirectURL, err := loginapp.AppendAuthorizationError(target.RedirectURI, code, state)
		if err != nil {
			h.authorizationRequestError(w)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}
	nonce, err := loginapp.GenerateOpaqueToken()
	if err != nil {
		h.authorizationRequestError(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = authorizationErrorPage.Execute(w, struct {
		Nonce  string
		Error  string
		Origin string
	}{Nonce: nonce, Error: code, Origin: target.Origin})
}

func (h *Handler) authorizationRequestError(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.Error(w, "Invalid Telegram Login request.", http.StatusBadRequest)
}

func (h *Handler) authorizationStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if !h.allow(w, r, "status", h.requestIP(r), 300, time.Minute) {
		return
	}
	form, ok := parseBoundedForm(w, r, maxStatusFormBytes)
	if !ok {
		return
	}
	browserToken, unique := singleValue(form, "browser_token")
	if !unique || browserToken == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid browser request")
		return
	}
	request, err := h.service.RequestByBrowserToken(r.Context(), browserToken)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid browser request")
		return
	}
	if !h.authorizeStatusOrigin(w, r, request) {
		return
	}
	switch request.Status {
	case domain.TelegramLoginRequestPending:
		writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
	case domain.TelegramLoginRequestApproved:
		if request.ResponseType == "post_message" {
			finalized, err := h.service.FinalizeDirectByBrowserToken(r.Context(), browserToken, h.tokens)
			if err != nil {
				h.logger.Error("telegram_login_direct_finalize_failed", zap.Int64("request_id", request.ID), zap.Error(err))
				writeOAuthError(w, http.StatusInternalServerError, "server_error", "authorization could not be finalized")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "id_token": finalized.IDToken})
			return
		}
		finalized, err := h.service.FinalizeByBrowserToken(r.Context(), browserToken)
		if err != nil {
			h.logger.Error("telegram_login_finalize_failed", zap.Int64("request_id", request.ID), zap.Error(err))
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "authorization could not be finalized")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "redirect_url": finalized.RedirectURL})
	case domain.TelegramLoginRequestDeclined, domain.TelegramLoginRequestExpired:
		errorCode := "access_denied"
		status := "declined"
		if request.Status == domain.TelegramLoginRequestExpired {
			errorCode, status = "temporarily_unavailable", "expired"
		}
		if request.ResponseType == "post_message" {
			writeJSON(w, http.StatusOK, map[string]string{"status": status, "error": errorCode})
			return
		}
		redirectURL, err := loginapp.AppendAuthorizationError(request.RedirectURI, errorCode, request.State)
		if err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "authorization could not be finalized")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": status, "redirect_url": redirectURL})
	default:
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid browser request")
	}
}

// authorizeStatusOrigin keeps the original same-origin popup poll working and
// permits the registered relying-party origin to take over polling when a
// mobile browser drops window.opener during an external app round-trip. The
// short-lived browser token remains a bearer credential, but browser-readable
// responses are exposed only to the exact origin persisted on the request.
func (h *Handler) authorizeStatusOrigin(w http.ResponseWriter, r *http.Request, request domain.TelegramLoginRequest) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	issuerOrigin, err := loginapp.NormalizeWebOrigin(h.tokens.Issuer(), h.allowHTTP)
	if err == nil && origin == issuerOrigin {
		return true
	}
	if request.ResponseType == "post_message" && origin == request.Origin {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
		return true
	}
	writeOAuthError(w, http.StatusForbidden, "access_denied", "browser origin is not authorized")
	return false
}

func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, "token-ip", h.requestIP(r), 60, time.Minute) {
		return
	}
	form, ok := parseBoundedForm(w, r, maxTokenFormBytes)
	if !ok {
		return
	}
	authorizationHeaders := r.Header.Values("Authorization")
	var clientID, clientSecret string
	var publicNativeClient bool
	switch len(authorizationHeaders) {
	case 0:
		var unique bool
		clientID, unique = requiredSingleValue(form, "client_id", 64)
		if !unique {
			h.invalidClient(w)
			return
		}
		clientSecret, unique = optionalSingleValue(form, "client_secret")
		if !unique || len(clientSecret) > 1024 {
			h.invalidClient(w)
			return
		}
		publicNativeClient = clientSecret == ""
	case 1:
		var ok bool
		clientID, clientSecret, ok = r.BasicAuth()
		if !ok || clientID == "" || clientSecret == "" || len(clientID) > 64 || len(clientSecret) > 1024 {
			h.invalidClient(w)
			return
		}
		if _, supplied := form["client_secret"]; supplied {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "multiple client authentication methods are not allowed")
			return
		}
	default:
		h.invalidClient(w)
		return
	}
	if !h.allow(w, r, "token-client", clientID, 60, time.Minute) {
		return
	}
	formClientID, unique := optionalSingleValue(form, "client_id")
	if !unique || (formClientID != "" && formClientID != clientID) {
		h.invalidClient(w)
		return
	}
	grantType, okGrant := requiredSingleValue(form, "grant_type", 64)
	if !okGrant {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "grant_type is required")
		return
	}
	if grantType != "authorization_code" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	code, okCode := requiredSingleValue(form, "code", 1024)
	redirectURI, okRedirect := requiredSingleValue(form, "redirect_uri", 4096)
	codeVerifier, okVerifier := requiredSingleValue(form, "code_verifier", 128)
	if !okCode || !okRedirect || !okVerifier {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code, redirect_uri and code_verifier are required")
		return
	}
	// Generate every response artifact before consuming the one-time code. A
	// transient entropy failure must leave the grant retryable.
	accessToken, err := loginapp.GenerateOpaqueToken()
	if err != nil {
		h.logger.Error("telegram_login_access_token_failed", zap.Error(err))
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "token service unavailable")
		return
	}
	issued, err := h.service.ExchangeAuthorizationCodeAndIssue(r.Context(), loginapp.ExchangeAuthorizationCodeParams{
		Code: code, ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI, CodeVerifier: codeVerifier,
		PublicNativeClient: publicNativeClient,
	}, h.tokens)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrTelegramLoginSecretInvalid), errors.Is(err, domain.ErrTelegramLoginClientDisabled):
			h.invalidClient(w)
		case errors.Is(err, domain.ErrTelegramLoginCodeInvalid), errors.Is(err, domain.ErrTelegramLoginCodeConsumed),
			errors.Is(err, domain.ErrTelegramLoginPKCEInvalid), errors.Is(err, domain.ErrTelegramLoginURLInvalid):
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid or expired")
		default:
			h.logger.Error("telegram_login_token_exchange_failed", zap.String("error", errorClass(err)))
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "token service unavailable")
		}
		return
	}
	_ = issued.WebAuthorization // durable authorization is intentionally not encoded into the opaque access token.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": accessToken, "token_type": "Bearer", "expires_in": int64(h.tokens.TTL().Seconds()),
		"id_token": issued.IDToken,
	})
}

func (h *Handler) invalidClient(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="telegram-login-token", charset="UTF-8"`)
	writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
}

func parseBoundedForm(w http.ResponseWriter, r *http.Request, maxBytes int64) (url.Values, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		writeOAuthError(w, http.StatusUnsupportedMediaType, "invalid_request", "form content type is required")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return nil, false
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return nil, false
	}
	return form, true
}

func singleValue(values url.Values, key string) (string, bool) {
	items, exists := values[key]
	if !exists {
		return "", true
	}
	if len(items) != 1 {
		return "", false
	}
	return items[0], true
}

func optionalSingleValue(values url.Values, key string) (string, bool) {
	value, ok := singleValue(values, key)
	return value, ok
}

func requiredSingleValue(values url.Values, key string, max int) (string, bool) {
	value, ok := singleValue(values, key)
	return value, ok && value != "" && len(value) <= max
}

func boundedHeader(value, fallback string, max int) string {
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

func (h *Handler) requestIP(r *http.Request) string {
	remote, ok := parseRequestIP(r.RemoteAddr)
	if !ok {
		return "Unknown IP"
	}
	if !prefixContains(h.trustedProxies, remote) {
		return remote.String()
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(forwarded) - 1; i >= 0; i-- {
		candidate, ok := parseRequestIP(strings.TrimSpace(forwarded[i]))
		if !ok {
			continue
		}
		remote = candidate
		if !prefixContains(h.trustedProxies, candidate) {
			return candidate.String()
		}
	}
	if candidate, ok := parseRequestIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ok {
		return candidate.String()
	}
	return remote.String()
}

func parseRequestIP(raw string) (netip.Addr, bool) {
	if addrPort, err := netip.ParseAddrPort(raw); err == nil {
		return addrPort.Addr().Unmap(), true
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	addr, err := netip.ParseAddr(strings.Trim(raw, "[]"))
	return addr.Unmap(), err == nil
}

func prefixContains(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, bucket, subject string, limit int, window time.Duration) bool {
	if h.limiter == nil {
		return true
	}
	sum := sha256.Sum256([]byte(subject))
	key := "telegram-login-http:" + bucket + ":" + base64.RawURLEncoding.EncodeToString(sum[:])
	allowed, retryAfter, err := h.limiter.Allow(r.Context(), key, limit, window)
	if err != nil {
		h.logger.Error("telegram_login_rate_limit_failed", zap.String("bucket", bucket), zap.Error(err))
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "login service temporarily unavailable")
		return false
	}
	if allowed {
		return true
	}
	if retryAfter <= 0 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
	writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many login requests")
	return false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func errorClass(err error) string {
	for _, candidate := range []struct {
		target error
		name   string
	}{
		{domain.ErrTelegramLoginClientInvalid, "client_invalid"},
		{domain.ErrTelegramLoginClientDisabled, "client_disabled"},
		{domain.ErrTelegramLoginRedirectNotAllowed, "redirect_not_allowed"},
		{domain.ErrTelegramLoginOriginNotAllowed, "origin_not_allowed"},
		{domain.ErrTelegramLoginScopeInvalid, "scope_invalid"},
		{domain.ErrTelegramLoginPKCEInvalid, "pkce_invalid"},
		{domain.ErrTelegramLoginRequestInvalid, "request_invalid"},
	} {
		if errors.Is(err, candidate.target) {
			return candidate.name
		}
	}
	return fmt.Sprintf("internal_%T", err)
}
