package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"telesrv/internal/admin"
)

const testSessionKey = "01234567890123456789012345678901"

// panelServer builds a BFF whose sessions carry the given permissions.
func panelServer(t *testing.T, permissions ...string) *server {
	t.Helper()
	srv, err := newServer(uiConfig{
		SessionKey:  []byte(testSessionKey),
		Password:    "letmein",
		Permissions: permissions,
	}, nil)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return srv
}

// signIn performs a real login against the routed server and returns the cookies
// plus the CSRF token the panel would echo, so the tests exercise the same pairing
// the browser gets.
func signIn(t *testing.T, srv *server) ([]*http.Cookie, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"secret":"letmein"}`))
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Actor       string   `json:"actor"`
		Permissions []string `json:"permissions"`
		CSRFToken   string   `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if body.CSRFToken == "" {
		t.Fatal("login did not mint a csrf token")
	}
	cookies := rec.Result().Cookies()
	var sawCSRFCookie bool
	for _, cookie := range cookies {
		if cookie.Name != csrfCookieName {
			continue
		}
		sawCSRFCookie = true
		if cookie.HttpOnly {
			t.Fatal("csrf cookie is HttpOnly; the panel could not read it back")
		}
		if cookie.Value != body.CSRFToken || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("csrf cookie=%+v", cookie)
		}
	}
	if !sawCSRFCookie {
		t.Fatal("login did not set the csrf cookie")
	}
	return cookies, body.CSRFToken
}

func withCookies(req *http.Request, cookies []*http.Cookie) *http.Request {
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	return req
}

func TestPanelSessionReportsPermissions(t *testing.T) {
	srv := panelServer(t, permissionVerificationReview)
	cookies, _ := signIn(t, srv)

	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/session", nil), cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Actor       string   `json:"actor"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if body.Actor != "admin" || len(body.Permissions) != 1 || body.Permissions[0] != permissionVerificationReview {
		t.Fatalf("session=%+v, want the granted permissions reported to the panel", body)
	}
}

func TestPanelSessionReportsTheWildcardDefault(t *testing.T) {
	// The shipped default is the wildcard, so an operator upgrading into the
	// permission model keeps every section.
	srv := panelServer(t, permissionAll)
	cookies, _ := signIn(t, srv)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/session", nil), cookies))
	if !strings.Contains(rec.Body.String(), `"*"`) {
		t.Fatalf("session body=%s, want the wildcard reported", rec.Body.String())
	}
}

func TestMutatingRequestsRequireTheCSRFHeader(t *testing.T) {
	srv := panelServer(t, permissionAll)
	cookies, token := signIn(t, srv)
	const path = "/api/actions/set-verified"
	const payload = `{"reason":"official","confirm":false,"user_id":1001,"verified":true}`

	// No header at all.
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload)), cookies))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), csrfHeaderName) {
		t.Fatalf("missing header status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	// A header that does not match the cookie.
	rec = httptest.NewRecorder()
	req := withCookies(httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload)), cookies)
	req.Header.Set(csrfHeaderName, token+"-tampered")
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mismatched header status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	// A matching header from a different session's token: it agrees with the
	// cookie the attacker planted but not with the signed session.
	otherSrv := panelServer(t, permissionAll)
	_, otherToken := signIn(t, otherSrv)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
	for _, cookie := range cookies {
		if cookie.Name == sessionCookieName {
			req.AddCookie(cookie)
		}
	}
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: otherToken})
	req.Header.Set(csrfHeaderName, otherToken)
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not bound to this session") {
		t.Fatalf("foreign token status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	// A session minted before the CSRF token existed is refused rather than left
	// half protected.
	legacy, err := signSession([]byte(testSessionKey), sessionClaims{
		Actor: "admin", Exp: time.Now().Add(time.Hour).Unix(), Nonce: "n",
		Permissions: []string{permissionAll},
	})
	if err != nil {
		t.Fatalf("signSession: %v", err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: legacy})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "anything"})
	req.Header.Set(csrfHeaderName, "anything")
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("pre-CSRF session status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}

func TestCSRFProtectionCoversEveryExistingMutatingRoute(t *testing.T) {
	srv := panelServer(t, permissionAll)
	cookies, _ := signIn(t, srv)
	// A representative slice of the routes that predate CSRF: they must all be
	// closed, not just the new ones.
	for _, path := range []string{
		"/api/logout",
		"/api/actions/set-frozen",
		"/api/actions/grant-stars",
		"/api/actions/delete-bot",
		"/api/actions/revoke-collectible-username",
		"/api/actions/adjust-account-rating",
		"/api/moderation/cases/7/claim",
		"/api/verification/applications/7/approve",
		"/api/actions/revoke-verification",
	} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)), cookies))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s, want 403 without a csrf header", path, rec.Code, rec.Body.String())
		}
	}
}

func TestReadRequestsDoNotNeedTheCSRFHeader(t *testing.T) {
	srv := panelServer(t, permissionAll)
	cookies, _ := signIn(t, srv)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/session", nil), cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s, want a token-free read", rec.Code, rec.Body.String())
	}
}

func TestForeignOriginIsRefusedEvenWithAValidToken(t *testing.T) {
	srv := panelServer(t, permissionAll)
	cookies, token := signIn(t, srv)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-verified", strings.NewReader(
		`{"reason":"official","confirm":false,"user_id":1001,"verified":true}`)), cookies)
	req.Header.Set(csrfHeaderName, token)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "origin") {
		t.Fatalf("foreign origin status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	// The panel's own origin is accepted.
	if !sameOriginRequest(originRequest("https://panel.example", "panel.example")) {
		t.Fatal("same origin refused")
	}
	// A missing Origin is accepted: browsers omit it and non-browser callers never
	// send it, and the token check still applies.
	if !sameOriginRequest(originRequest("", "panel.example")) {
		t.Fatal("absent origin refused")
	}
	// An opaque origin is not this host.
	if sameOriginRequest(originRequest("null", "panel.example")) {
		t.Fatal("opaque origin accepted")
	}
	if sameOriginRequest(originRequest("not a url", "panel.example")) {
		t.Fatal("unparsable origin accepted")
	}
}

func originRequest(origin, host string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/actions/set-verified", nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func TestLoginRefusesAForeignOrigin(t *testing.T) {
	srv := panelServer(t, permissionAll)
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"secret":"letmein"}`))
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}

func TestVerificationRoutesRefuseASessionWithoutTheReviewRight(t *testing.T) {
	srv := panelServer(t, "gifts.import")
	cookies, token := signIn(t, srv)
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/verification/applications", ""},
		{http.MethodGet, "/api/verification/applications/7", ""},
		{http.MethodGet, "/api/verification/counts", ""},
		{http.MethodPost, "/api/verification/applications/7/claim", `{}`},
		{http.MethodPost, "/api/verification/applications/7/approve", `{}`},
		{http.MethodPost, "/api/verification/applications/7/reject", `{}`},
		{http.MethodPost, "/api/actions/revoke-verification", `{}`},
	}
	for _, item := range cases {
		var req *http.Request
		if item.body == "" {
			req = httptest.NewRequest(item.method, item.path, nil)
		} else {
			req = httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
			req.Header.Set(csrfHeaderName, token)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(req, cookies))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s, want 403", item.method, item.path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode 403 body: %v", err)
		}
		if body["code"] != "FORBIDDEN" || body["permission"] != permissionVerificationReview {
			t.Fatalf("%s 403 body=%+v, want the missing permission named", item.path, body)
		}
	}
}

func TestRevokeVerificationNeedsTheRevokeRightOnTopOfReview(t *testing.T) {
	srv := panelServer(t, permissionVerificationReview)
	cookies, token := signIn(t, srv)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/revoke-verification", strings.NewReader(
		`{"reason":"impersonation","confirm":true,"target_type":"channel","target_id":5005}`)), cookies)
	req.Header.Set(csrfHeaderName, token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 403 body: %v", err)
	}
	if body["permission"] != permissionVerificationRevoke {
		t.Fatalf("403 body=%+v, want verification.revoke named", body)
	}
}

func TestVerificationRoutesRequireASession(t *testing.T) {
	srv := panelServer(t, permissionAll)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/verification/applications"},
		{http.MethodGet, "/api/verification/applications/7"},
		{http.MethodGet, "/api/verification/counts"},
		{http.MethodPost, "/api/verification/applications/7/claim"},
		{http.MethodPost, "/api/verification/applications/7/approve"},
		{http.MethodPost, "/api/verification/applications/7/reject"},
		{http.MethodPost, "/api/actions/revoke-verification"},
	}
	for _, item := range cases {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, httptest.NewRequest(item.method, item.path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d, want 401", item.method, item.path, rec.Code)
		}
	}
}

// verificationUpstream stands in for the admin API and records what the BFF sent.
type verificationUpstream struct {
	path   string
	raw    []byte
	status int
	body   any
}

func (u *verificationUpstream) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api-secret" {
			t.Fatalf("upstream authorization=%q", r.Header.Get("Authorization"))
		}
		u.path = r.URL.Path
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		u.raw = raw
		status := u.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(u.body)
	}
}

// requestWithActor stands in for the session middleware, which is what puts the
// signed-in operator into the request context.
func requestWithActor(r *http.Request, actor string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), actorKey{}, actor))
}

func TestApproveVerificationBFFForwardsActorVersionAndNote(t *testing.T) {
	upstream := &verificationUpstream{body: admin.CommandResult{CommandID: "c1", Status: "completed"}}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/verification/applications/77/approve", strings.NewReader(`{
		"reason":"press coverage verified","confirm":true,"version":"9223372036854775807",
		"internal_note":"contact came through the press office"
	}`))
	req.SetPathValue("id", "77")
	req = requestWithActor(req, "operator")
	rec := httptest.NewRecorder()
	srv.handleApproveVerificationAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if upstream.path != "/v1/verification/applications/77/approve" {
		t.Fatalf("upstream path=%q", upstream.path)
	}
	var got admin.ApproveVerificationRequest
	if err := json.Unmarshal(upstream.raw, &got); err != nil {
		t.Fatalf("decode forwarded approval: %v (%s)", err, upstream.raw)
	}
	if got.Actor != "operator" {
		t.Fatalf("actor=%q, want the signed-in operator", got.Actor)
	}
	if got.ApplicationID != 77 || got.Version != 9223372036854775807 {
		t.Fatalf("forwarded approval=%+v, want the exact int64 version", got)
	}
	if got.InternalNote != "contact came through the press office" || got.DryRun {
		t.Fatalf("forwarded approval=%+v", got)
	}
	if got.CommandID == "" {
		t.Fatal("no command id was minted for the idempotency key")
	}
}

func TestClaimVerificationBFFDefaultsToADryRun(t *testing.T) {
	upstream := &verificationUpstream{body: admin.CommandResult{CommandID: "c1", Status: "completed", DryRun: true}}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/verification/applications/77/claim", strings.NewReader(
		`{"reason":"queue sweep","confirm":false,"version":3}`))
	req.SetPathValue("id", "77")
	req = requestWithActor(req, "operator")
	rec := httptest.NewRecorder()
	srv.handleClaimVerificationAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got admin.ClaimVerificationRequest
	if err := json.Unmarshal(upstream.raw, &got); err != nil {
		t.Fatalf("decode forwarded claim: %v", err)
	}
	// confirm=false is a rehearsal: nothing may be written until the operator
	// confirms.
	if !got.DryRun || got.Version != 3 || got.ApplicationID != 77 {
		t.Fatalf("forwarded claim=%+v", got)
	}
}

func TestRevokeVerificationBFFForwardsTargetAndRejectsBadShapes(t *testing.T) {
	upstream := &verificationUpstream{body: admin.CommandResult{CommandID: "c1", Status: "completed"}}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}
	req := requestWithActor(httptest.NewRequest(http.MethodPost, "/api/actions/revoke-verification", strings.NewReader(`{
		"reason":"impersonation confirmed","confirm":true,"target_type":"channel",
		"target_id":"9223372036854775807","internal_note":"legal asked for it"
	}`)), "operator")
	rec := httptest.NewRecorder()
	srv.handleRevokeVerificationAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if upstream.path != "/v1/verification/revoke" {
		t.Fatalf("upstream path=%q", upstream.path)
	}
	var got admin.RevokeVerificationRequest
	if err := json.Unmarshal(upstream.raw, &got); err != nil {
		t.Fatalf("decode forwarded revocation: %v", err)
	}
	if got.TargetID != 9223372036854775807 || got.TargetType != "channel" ||
		got.Actor != "operator" || got.InternalNote != "legal asked for it" || got.DryRun {
		t.Fatalf("forwarded revocation=%+v", got)
	}

	for _, payload := range []string{
		`{"reason":"x","confirm":true,"target_type":"group","target_id":5}`,
		`{"reason":"x","confirm":true,"target_type":"channel","target_id":0}`,
	} {
		rec := httptest.NewRecorder()
		srv.handleRevokeVerificationAPI(rec, requestWithActor(
			httptest.NewRequest(http.MethodPost, "/api/actions/revoke-verification", strings.NewReader(payload)), "operator"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("payload %s status=%d body=%s, want 400", payload, rec.Code, rec.Body.String())
		}
	}
}

func TestVerificationDecisionRejectsUnknownFields(t *testing.T) {
	srv := &server{cfg: uiConfig{AdminAPIURL: "http://127.0.0.1:1", AdminAPIToken: "api-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/verification/applications/77/approve", strings.NewReader(
		`{"reason":"ok","confirm":true,"version":3,"actor":"attacker"}`))
	req.SetPathValue("id", "77")
	req = requestWithActor(req, "operator")
	rec := httptest.NewRecorder()
	srv.handleApproveVerificationAPI(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "actor") {
		t.Fatalf("status=%d body=%s, want 400 rejecting the injected actor", rec.Code, rec.Body.String())
	}
}

func TestVerificationVersionConflictReachesThePanelAs409(t *testing.T) {
	upstream := &verificationUpstream{
		status: http.StatusConflict,
		body: admin.CommandResult{
			CommandID: "c1", Status: "failed",
			Error:   admin.CodeVerificationConflict + ": verification application changed concurrently",
			Message: "another reviewer changed this application first; reload it and decide again",
		},
	}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/verification/applications/77/approve", strings.NewReader(
		`{"reason":"ok","confirm":true,"version":3}`))
	req.SetPathValue("id", "77")
	req = requestWithActor(req, "operator")
	rec := httptest.NewRecorder()
	srv.handleApproveVerificationAPI(rec, req)
	// A flattened 502 would hide the one failure the panel resolves by reloading.
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	var result admin.CommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if !strings.Contains(result.Error, admin.CodeVerificationConflict) || !strings.Contains(result.Message, "reload") {
		t.Fatalf("relayed result=%+v", result)
	}
}

func TestVerificationUnreachableAdminAPIIsABadGateway(t *testing.T) {
	srv := &server{cfg: uiConfig{AdminAPIURL: "http://127.0.0.1:1", AdminAPIToken: "api-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/verification/applications/77/reject", strings.NewReader(
		`{"reason":"press links are self-published","confirm":true,"version":3}`))
	req.SetPathValue("id", "77")
	req = requestWithActor(req, "operator")
	rec := httptest.NewRecorder()
	srv.handleRejectVerificationAPI(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s, want 502", rec.Code, rec.Body.String())
	}
}

func TestVerificationRowsJSONPreserveInt64AsDecimalStrings(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	raw, err := json.Marshal(VerificationApplicationRow{
		ID: maxInt64, ApplicantUserID: maxInt64, TargetID: maxInt64, Version: maxInt64,
	})
	if err != nil {
		t.Fatalf("marshal verification row: %v", err)
	}
	var application map[string]any
	if err := json.Unmarshal(raw, &application); err != nil {
		t.Fatalf("unmarshal verification row: %v", err)
	}
	for _, field := range []string{"ID", "ApplicantUserID", "TargetID", "Version"} {
		if application[field] != "9223372036854775807" {
			t.Fatalf("application %s = %#v, want an exact decimal string", field, application[field])
		}
	}

	raw, err = json.Marshal(VerificationEventRow{ID: maxInt64})
	if err != nil {
		t.Fatalf("marshal verification event row: %v", err)
	}
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("unmarshal verification event row: %v", err)
	}
	if event["ID"] != "9223372036854775807" {
		t.Fatalf("event ID = %#v, want an exact decimal string", event["ID"])
	}
}

func TestVerificationQueryValidationRejectsUnmodelledFilters(t *testing.T) {
	srv := panelServer(t, permissionVerificationReview)
	cookies, _ := signIn(t, srv)
	for _, query := range []string{"?status=pending", "?target_type=group", "?before_id=-1", "?limit=abc"} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(
			httptest.NewRequest(http.MethodGet, "/api/verification/applications"+query, nil), cookies))
		// The read store is absent in this fixture, so a rejected filter is a 400
		// and an accepted one would be a 503: either way the validation is proven.
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s, want 400", query, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(
		httptest.NewRequest(http.MethodGet, "/api/verification/applications?status=submitted&target_type=channel", nil), cookies))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("valid filter status=%d body=%s, want the read store to be reached", rec.Code, rec.Body.String())
	}
}

func TestPanelPermissionsWildcardAndMembership(t *testing.T) {
	all := newPanelPermissions([]string{permissionAll})
	if !all.Has(permissionVerificationReview) || !all.Has(permissionVerificationRevoke) {
		t.Fatal("wildcard session refused a permission")
	}
	bounded := newPanelPermissions([]string{" verification.review ", "", "verification.review"})
	if !bounded.Has(permissionVerificationReview) || bounded.Has(permissionVerificationRevoke) {
		t.Fatalf("bounded session = %+v", bounded.List())
	}
	if len(bounded.List()) != 1 {
		t.Fatalf("bounded list=%+v, want the duplicate collapsed", bounded.List())
	}
	if got := newPanelPermissions(nil).List(); got == nil || len(got) != 0 {
		t.Fatalf("empty list=%#v, want an empty array rather than null", got)
	}
}

// The CSRF gate must let a correctly-tokened request through -- including on the
// routes that predate it -- or the panel is simply broken rather than protected.
func TestExistingMutatingRoutesStillWorkWithAValidToken(t *testing.T) {
	upstream := &verificationUpstream{body: admin.CommandResult{CommandID: "c1", Status: "completed", DryRun: true}}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := panelServer(t, permissionAll)
	srv.cfg.AdminAPIURL = api.URL
	srv.cfg.AdminAPIToken = "api-secret"
	cookies, token := signIn(t, srv)

	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-verified", strings.NewReader(
		`{"reason":"official","confirm":false,"user_id":1001,"verified":true}`)), cookies)
	req.Header.Set(csrfHeaderName, token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tokened legacy action status=%d body=%s", rec.Code, rec.Body.String())
	}
	if upstream.path != "/v1/accounts/set-verified" {
		t.Fatalf("upstream path=%q", upstream.path)
	}

	// And logout, which is now behind the same gate.
	req = withCookies(httptest.NewRequest(http.MethodPost, "/api/logout", nil), cookies)
	req.Header.Set(csrfHeaderName, token)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tokened logout status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Both cookies are cleared, so the browser cannot keep replaying either half.
	cleared := map[string]bool{}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.MaxAge < 0 {
			cleared[cookie.Name] = true
		}
	}
	if !cleared[sessionCookieName] || !cleared[csrfCookieName] {
		t.Fatalf("logout cleared=%+v, want both cookies expired", cleared)
	}
}

// The panel drives claim, approve and reject from one form, so a claim carrying an
// internal note must not be rejected by the strict decoder.
func TestClaimVerificationAcceptsAnOptionalInternalNote(t *testing.T) {
	upstream := &verificationUpstream{body: admin.CommandResult{CommandID: "c1", Status: "completed"}}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/verification/applications/77/claim", strings.NewReader(
		`{"reason":"queue sweep","confirm":true,"version":3,"internal_note":"waiting on legal"}`))
	req.SetPathValue("id", "77")
	req = requestWithActor(req, "operator")
	rec := httptest.NewRecorder()
	srv.handleClaimVerificationAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got admin.ClaimVerificationRequest
	if err := json.Unmarshal(upstream.raw, &got); err != nil {
		t.Fatalf("decode forwarded claim: %v", err)
	}
	if got.InternalNote != "waiting on legal" {
		t.Fatalf("forwarded claim=%+v", got)
	}
}

func TestMutatingMethodClassification(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, "get"} {
		if mutatingMethod(method) {
			t.Fatalf("%s classified as mutating", method)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !mutatingMethod(method) {
			t.Fatalf("%s classified as safe", method)
		}
	}
}
