package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/admin"
)

// Third-party bot verification in the panel BFF. The section is separate from the
// official verification one in every dimension that matters here: its own routes,
// its own two permissions, and no overlap with verification.* in either direction.

// botVerificationRoute is one panel route with a body its handler accepts.
type panelBotVerificationRoute struct {
	method string
	path   string
	body   string
}

var panelBotVerificationReadRoutes = []panelBotVerificationRoute{
	{http.MethodGet, "/api/botverification/verifiers", ""},
	{http.MethodGet, "/api/botverification/icons", ""},
	{http.MethodGet, "/api/botverification/marks", ""},
	{http.MethodGet, "/api/botverification/requests", ""},
	{http.MethodGet, "/api/botverification/requests/7", ""},
	{http.MethodGet, "/api/botverification/counts", ""},
	{http.MethodPost, "/api/botverification/requests/7/approve", `{}`},
	{http.MethodPost, "/api/botverification/requests/7/reject", `{}`},
	{http.MethodPost, "/api/botverification/requests/7/revoke", `{}`},
}

var panelBotVerificationManageRoutes = []panelBotVerificationRoute{
	{http.MethodPost, "/api/actions/grant-bot-verifier", `{}`},
	{http.MethodPost, "/api/actions/set-bot-verifier-enabled", `{}`},
	{http.MethodPost, "/api/actions/revoke-bot-verifier", `{}`},
	{http.MethodPost, "/api/actions/upsert-verification-icon", `{}`},
	{http.MethodPost, "/api/actions/set-verification-icon-active", `{}`},
	{http.MethodPost, "/api/actions/revoke-custom-verification", `{}`},
}

func panelBotVerificationRoutes() []panelBotVerificationRoute {
	out := make([]panelBotVerificationRoute, 0,
		len(panelBotVerificationReadRoutes)+len(panelBotVerificationManageRoutes))
	out = append(out, panelBotVerificationReadRoutes...)
	return append(out, panelBotVerificationManageRoutes...)
}

func TestBotVerificationPanelRoutesRequireASession(t *testing.T) {
	srv := panelServer(t, permissionAll)
	for _, item := range panelBotVerificationRoutes() {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, httptest.NewRequest(item.method, item.path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d, want 401", item.method, item.path, rec.Code)
		}
	}
}

// Every mutating route in the section is behind the double-submit CSRF token, like
// every other one in the panel: a cookie-authenticated request forged by another
// origin must not be able to appoint a verifier.
func TestBotVerificationMutationsRequireTheCSRFHeader(t *testing.T) {
	srv := panelServer(t, permissionAll)
	cookies, token := signIn(t, srv)
	for _, item := range panelBotVerificationRoutes() {
		if item.method != http.MethodPost {
			continue
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(
			httptest.NewRequest(item.method, item.path, strings.NewReader(item.body)), cookies))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), csrfHeaderName) {
			t.Fatalf("%s status=%d body=%s, want 403 without a csrf header", item.path, rec.Code, rec.Body.String())
		}
	}
	// A foreign origin is refused even when the token is right.
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/grant-bot-verifier",
		strings.NewReader(`{}`)), cookies)
	req.Header.Set(csrfHeaderName, token)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "origin") {
		t.Fatalf("foreign origin status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}

// Reads do not need the token: they change nothing, and requiring it would break
// the panel without adding protection.
func TestBotVerificationReadsDoNotNeedTheCSRFHeader(t *testing.T) {
	srv := panelServer(t, permissionBotVerificationReview)
	cookies, _ := signIn(t, srv)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(
		httptest.NewRequest(http.MethodGet, "/api/botverification/verifiers", nil), cookies))
	// No read store is wired in this fixture, so the gate passing is what is under
	// test: 503 means the request got past authorisation and CSRF.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status=%d body=%s, want the gate passed without a token", rec.Code, rec.Body.String())
	}
}

func TestBotVerificationRoutesRefuseASessionWithoutTheRight(t *testing.T) {
	// A session holding only the OFFICIAL verification rights: the two mechanisms
	// are separate, so it must not reach this section at all.
	srv := panelServer(t, permissionVerificationReview, permissionVerificationRevoke)
	cookies, token := signIn(t, srv)

	check := func(item panelBotVerificationRoute, wantPermission string) {
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
		if body["code"] != "FORBIDDEN" || body["permission"] != wantPermission {
			t.Fatalf("%s 403 body=%+v, want %s named", item.path, body, wantPermission)
		}
	}
	for _, item := range panelBotVerificationReadRoutes {
		check(item, permissionBotVerificationReview)
	}
	for _, item := range panelBotVerificationManageRoutes {
		check(item, permissionBotVerificationManage)
	}
}

// The two halves are independent: the review right does not appoint verifiers, and
// the manage right does not decide applications.
func TestBotVerificationReviewAndManageAreIndependent(t *testing.T) {
	reviewOnly := panelServer(t, permissionBotVerificationReview)
	cookies, token := signIn(t, reviewOnly)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/grant-bot-verifier",
		strings.NewReader(`{"reason":"partner","confirm":true,"bot_id":3003,"icon_document_id":900,"company_name":"x"}`)), cookies)
	req.Header.Set(csrfHeaderName, token)
	rec := httptest.NewRecorder()
	reviewOnly.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionBotVerificationManage) {
		t.Fatalf("review-only on grant status=%d body=%s, want 403 naming manage", rec.Code, rec.Body.String())
	}

	manageOnly := panelServer(t, permissionBotVerificationManage)
	cookies, token = signIn(t, manageOnly)
	req = withCookies(httptest.NewRequest(http.MethodPost, "/api/botverification/requests/7/approve",
		strings.NewReader(`{"reason":"verified","confirm":true,"version":3}`)), cookies)
	req.Header.Set(csrfHeaderName, token)
	rec = httptest.NewRecorder()
	manageOnly.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionBotVerificationReview) {
		t.Fatalf("manage-only on approve status=%d body=%s, want 403 naming review", rec.Code, rec.Body.String())
	}
}

// A session holding the third-party rights must not reach the official section
// either: the separation is symmetric.
func TestBotVerificationSessionCannotReachTheOfficialSection(t *testing.T) {
	srv := panelServer(t, permissionBotVerificationReview, permissionBotVerificationManage)
	cookies, _ := signIn(t, srv)
	for _, path := range []string{"/api/verification/applications", "/api/verification/counts"} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, path, nil), cookies))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s, want 403", path, rec.Code, rec.Body.String())
		}
	}
}

func TestApproveBotVerificationBFFForwardsActorVersionAndNote(t *testing.T) {
	upstream := &verificationUpstream{body: admin.CommandResult{CommandID: "c1", Status: "completed"}}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/botverification/requests/88/approve", strings.NewReader(`{
		"reason":"the outlet checks out","confirm":true,"version":"9223372036854775807",
		"internal_note":"contact came through the press office"
	}`))
	req.SetPathValue("id", "88")
	req = requestWithActor(req, "operator")
	rec := httptest.NewRecorder()
	srv.handleApproveBotVerificationAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if upstream.path != "/v1/botverification/requests/88/approve" {
		t.Fatalf("upstream path=%q", upstream.path)
	}
	var got admin.ApproveBotVerificationRequest
	if err := json.Unmarshal(upstream.raw, &got); err != nil {
		t.Fatalf("decode forwarded approval: %v (%s)", err, upstream.raw)
	}
	if got.Actor != "operator" {
		t.Fatalf("actor=%q, want the signed-in operator", got.Actor)
	}
	// The version arrives as a decimal string from the browser and must survive
	// exactly: a rounded version would decide the wrong revision of the row.
	if got.RequestID != 88 || got.Version != 9223372036854775807 {
		t.Fatalf("forwarded approval=%+v, want the exact int64 version", got)
	}
	if got.InternalNote != "contact came through the press office" || got.DryRun {
		t.Fatalf("forwarded approval=%+v", got)
	}
	if got.CommandID == "" {
		t.Fatal("no command id was minted for the idempotency key")
	}
}

// confirm=false is a rehearsal: nothing may be written until the operator confirms.
func TestBotVerificationBFFDefaultsToADryRun(t *testing.T) {
	upstream := &verificationUpstream{body: admin.CommandResult{CommandID: "c1", Status: "completed", DryRun: true}}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/botverification/requests/88/reject", strings.NewReader(
		`{"reason":"not an outlet","confirm":false,"version":3}`))
	req.SetPathValue("id", "88")
	req = requestWithActor(req, "operator")
	rec := httptest.NewRecorder()
	srv.handleRejectBotVerificationAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got admin.RejectBotVerificationRequest
	if err := json.Unmarshal(upstream.raw, &got); err != nil {
		t.Fatalf("decode forwarded rejection: %v", err)
	}
	if !got.DryRun || got.Version != 3 || got.RequestID != 88 {
		t.Fatalf("forwarded rejection=%+v", got)
	}

	// The same on an operator action.
	req = requestWithActor(httptest.NewRequest(http.MethodPost, "/api/actions/revoke-bot-verifier", strings.NewReader(
		`{"reason":"programme ended","confirm":false,"bot_id":3003}`)), "operator")
	rec = httptest.NewRecorder()
	srv.handleRevokeBotVerifierAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	var revoke admin.RevokeBotVerifierRequest
	if err := json.Unmarshal(upstream.raw, &revoke); err != nil {
		t.Fatalf("decode forwarded revocation: %v", err)
	}
	if !revoke.DryRun || revoke.BotID != 3003 {
		t.Fatalf("forwarded revocation=%+v", revoke)
	}
}

func TestGrantBotVerifierBFFForwardsThePayloadAndRejectsBadShapes(t *testing.T) {
	upstream := &verificationUpstream{body: admin.CommandResult{CommandID: "c1", Status: "completed"}}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}
	req := requestWithActor(httptest.NewRequest(http.MethodPost, "/api/actions/grant-bot-verifier", strings.NewReader(`{
		"reason":"partner programme","confirm":true,
		"bot_id":"9223372036854775807","icon_document_id":"9223372036854775806",
		"company_name":"Example Trust","default_description":"verified by Example Trust",
		"can_modify_custom_description":true,"version":"4"
	}`)), "operator")
	rec := httptest.NewRecorder()
	srv.handleGrantBotVerifierAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if upstream.path != "/v1/botverification/verifiers/grant" {
		t.Fatalf("upstream path=%q", upstream.path)
	}
	var got admin.GrantBotVerifierRequest
	if err := json.Unmarshal(upstream.raw, &got); err != nil {
		t.Fatalf("decode forwarded grant: %v", err)
	}
	if got.BotID != 9223372036854775807 || got.IconDocumentID != 9223372036854775806 ||
		got.Version != 4 || got.Actor != "operator" || got.DryRun {
		t.Fatalf("forwarded grant=%+v, want the exact int64s", got)
	}
	if got.CompanyName != "Example Trust" || !got.CanModifyCustomDescription {
		t.Fatalf("forwarded grant=%+v", got)
	}

	for _, payload := range []string{
		`{"reason":"x","confirm":true,"bot_id":0,"icon_document_id":900,"company_name":"y"}`,
		`{"reason":"x","confirm":true,"bot_id":3003,"icon_document_id":0,"company_name":"y"}`,
		`{"reason":"x","confirm":true,"bot_id":3003,"icon_document_id":900,"company_name":"   "}`,
		`{"reason":"x","confirm":true,"bot_id":3003,"icon_document_id":900,"company_name":"y","version":-1}`,
	} {
		rec := httptest.NewRecorder()
		srv.handleGrantBotVerifierAPI(rec, requestWithActor(
			httptest.NewRequest(http.MethodPost, "/api/actions/grant-bot-verifier", strings.NewReader(payload)), "operator"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("payload %s status=%d body=%s, want 400", payload, rec.Code, rec.Body.String())
		}
	}
}

func TestBotVerificationOperatorActionsForwardTheirPayloads(t *testing.T) {
	upstream := &verificationUpstream{body: admin.CommandResult{CommandID: "c1", Status: "completed"}}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()
	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}

	rec := httptest.NewRecorder()
	srv.handleSetBotVerifierEnabledAPI(rec, requestWithActor(httptest.NewRequest(
		http.MethodPost, "/api/actions/set-bot-verifier-enabled", strings.NewReader(
			`{"reason":"abuse report","confirm":true,"bot_id":"3003","enabled":false}`)), "operator"))
	if rec.Code != http.StatusOK || upstream.path != "/v1/botverification/verifiers/set-enabled" {
		t.Fatalf("set-enabled status=%d path=%q body=%s", rec.Code, upstream.path, rec.Body.String())
	}
	var setEnabled admin.SetBotVerifierEnabledRequest
	if err := json.Unmarshal(upstream.raw, &setEnabled); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if setEnabled.BotID != 3003 || setEnabled.Enabled || setEnabled.Actor != "operator" {
		t.Fatalf("forwarded=%+v", setEnabled)
	}

	rec = httptest.NewRecorder()
	srv.handleUpsertVerificationIconAPI(rec, requestWithActor(httptest.NewRequest(
		http.MethodPost, "/api/actions/upsert-verification-icon", strings.NewReader(
			`{"reason":"new icon","confirm":true,"document_id":"9223372036854775807","name":"blue check","owner_bot_id":"3003"}`)), "operator"))
	if rec.Code != http.StatusOK || upstream.path != "/v1/botverification/icons/upsert" {
		t.Fatalf("upsert-icon status=%d path=%q body=%s", rec.Code, upstream.path, rec.Body.String())
	}
	var icon admin.UpsertVerificationIconRequest
	if err := json.Unmarshal(upstream.raw, &icon); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if icon.DocumentID != 9223372036854775807 || icon.Name != "blue check" || icon.OwnerBotID != 3003 {
		t.Fatalf("forwarded=%+v", icon)
	}
	// owner_bot_id is optional: absent means a shared catalogue entry.
	rec = httptest.NewRecorder()
	srv.handleUpsertVerificationIconAPI(rec, requestWithActor(httptest.NewRequest(
		http.MethodPost, "/api/actions/upsert-verification-icon", strings.NewReader(
			`{"reason":"new icon","confirm":true,"document_id":900,"name":"shared"}`)), "operator"))
	if rec.Code != http.StatusOK {
		t.Fatalf("shared icon status=%d body=%s", rec.Code, rec.Body.String())
	}
	// A fresh target: owner_bot_id is omitted when zero, so decoding into the
	// previous value would silently keep the reserved owner.
	var shared admin.UpsertVerificationIconRequest
	if err := json.Unmarshal(upstream.raw, &shared); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if shared.OwnerBotID != 0 || shared.Name != "shared" {
		t.Fatalf("forwarded=%+v, want a shared entry", shared)
	}

	rec = httptest.NewRecorder()
	srv.handleSetVerificationIconActiveAPI(rec, requestWithActor(httptest.NewRequest(
		http.MethodPost, "/api/actions/set-verification-icon-active", strings.NewReader(
			`{"reason":"retired","confirm":true,"icon_id":"501","active":false}`)), "operator"))
	if rec.Code != http.StatusOK || upstream.path != "/v1/botverification/icons/set-active" {
		t.Fatalf("set-icon-active status=%d path=%q", rec.Code, upstream.path)
	}
	var iconActive admin.SetVerificationIconActiveRequest
	if err := json.Unmarshal(upstream.raw, &iconActive); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if iconActive.IconID != 501 || iconActive.Active {
		t.Fatalf("forwarded=%+v", iconActive)
	}

	rec = httptest.NewRecorder()
	srv.handleRevokeCustomVerificationAPI(rec, requestWithActor(httptest.NewRequest(
		http.MethodPost, "/api/actions/revoke-custom-verification", strings.NewReader(
			`{"reason":"impersonation","confirm":true,"verifier_bot_id":"3003","peer_type":"channel","peer_id":"9223372036854775807"}`)), "operator"))
	if rec.Code != http.StatusOK || upstream.path != "/v1/botverification/marks/revoke" {
		t.Fatalf("revoke-mark status=%d path=%q body=%s", rec.Code, upstream.path, rec.Body.String())
	}
	var mark admin.RevokeCustomVerificationRequest
	if err := json.Unmarshal(upstream.raw, &mark); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mark.VerifierBotID != 3003 || mark.PeerType != "channel" || mark.PeerID != 9223372036854775807 {
		t.Fatalf("forwarded=%+v", mark)
	}

	for _, payload := range []string{
		`{"reason":"x","confirm":true,"verifier_bot_id":0,"peer_type":"channel","peer_id":5}`,
		`{"reason":"x","confirm":true,"verifier_bot_id":3003,"peer_type":"chat","peer_id":5}`,
		`{"reason":"x","confirm":true,"verifier_bot_id":3003,"peer_type":"","peer_id":5}`,
		`{"reason":"x","confirm":true,"verifier_bot_id":3003,"peer_type":"channel","peer_id":0}`,
	} {
		rec := httptest.NewRecorder()
		srv.handleRevokeCustomVerificationAPI(rec, requestWithActor(httptest.NewRequest(
			http.MethodPost, "/api/actions/revoke-custom-verification", strings.NewReader(payload)), "operator"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("payload %s status=%d body=%s, want 400", payload, rec.Code, rec.Body.String())
		}
	}
}

// A body may not smuggle in an actor: the signed-in operator is the audit identity,
// and the strict decoder is what enforces it.
func TestBotVerificationRequestsRejectUnknownFields(t *testing.T) {
	srv := &server{cfg: uiConfig{AdminAPIURL: "http://127.0.0.1:1", AdminAPIToken: "api-secret"}}

	req := httptest.NewRequest(http.MethodPost, "/api/botverification/requests/88/approve", strings.NewReader(
		`{"reason":"ok","confirm":true,"version":3,"actor":"attacker"}`))
	req.SetPathValue("id", "88")
	rec := httptest.NewRecorder()
	srv.handleApproveBotVerificationAPI(rec, requestWithActor(req, "operator"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "actor") {
		t.Fatalf("status=%d body=%s, want 400 rejecting the injected actor", rec.Code, rec.Body.String())
	}

	// enabled is not part of the grant form: the kill switch is its own action, and
	// a silently ignored field would hide that from the operator.
	rec = httptest.NewRecorder()
	srv.handleGrantBotVerifierAPI(rec, requestWithActor(httptest.NewRequest(
		http.MethodPost, "/api/actions/grant-bot-verifier", strings.NewReader(
			`{"reason":"x","confirm":true,"bot_id":3003,"icon_document_id":900,"company_name":"y","enabled":true}`)), "operator"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "enabled") {
		t.Fatalf("status=%d body=%s, want 400 naming the unknown field", rec.Code, rec.Body.String())
	}
}

func TestBotVerificationPathIDIsValidated(t *testing.T) {
	srv := &server{cfg: uiConfig{AdminAPIURL: "http://127.0.0.1:1", AdminAPIToken: "api-secret"}}
	for _, id := range []string{"", "0", "-1", "abc"} {
		req := httptest.NewRequest(http.MethodPost, "/api/botverification/requests/x/approve", strings.NewReader(
			`{"reason":"ok","confirm":true,"version":3}`))
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		srv.handleApproveBotVerificationAPI(rec, requestWithActor(req, "operator"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("id=%q status=%d, want 400", id, rec.Code)
		}
	}
}

// A flattened 502 would hide the one failure the panel resolves by reloading.
func TestBotVerificationConflictReachesThePanelAs409(t *testing.T) {
	upstream := &verificationUpstream{
		status: http.StatusConflict,
		body: admin.CommandResult{
			CommandID: "c1", Status: "failed",
			Error:   admin.CodeCustomVerificationConflict + ": custom verification changed concurrently",
			Message: "another operator changed this row first; reload it and try again",
		},
	}
	api := httptest.NewServer(upstream.handler(t))
	defer api.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/botverification/requests/88/approve", strings.NewReader(
		`{"reason":"ok","confirm":true,"version":3}`))
	req.SetPathValue("id", "88")
	rec := httptest.NewRecorder()
	srv.handleApproveBotVerificationAPI(rec, requestWithActor(req, "operator"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	var result admin.CommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if !strings.Contains(result.Error, admin.CodeCustomVerificationConflict) ||
		!strings.Contains(result.Message, "reload") {
		t.Fatalf("result=%+v", result)
	}

	// The manage half too: two operators can race one verifier row.
	rec = httptest.NewRecorder()
	srv.handleGrantBotVerifierAPI(rec, requestWithActor(httptest.NewRequest(
		http.MethodPost, "/api/actions/grant-bot-verifier", strings.NewReader(
			`{"reason":"x","confirm":true,"bot_id":3003,"icon_document_id":900,"company_name":"y","version":3}`)), "operator"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("grant status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}

	// A 404 from upstream is preserved as well, so a decision on a row that is gone
	// is not reported as an upstream outage.
	upstream.status = http.StatusNotFound
	upstream.body = admin.CommandResult{CommandID: "c2", Status: "failed",
		Error: admin.CodeCustomVerificationRequestNotFound + ": custom verification request not found"}
	req = httptest.NewRequest(http.MethodPost, "/api/botverification/requests/88/reject", strings.NewReader(
		`{"reason":"ok","confirm":true,"version":3}`))
	req.SetPathValue("id", "88")
	rec = httptest.NewRecorder()
	srv.handleRejectBotVerificationAPI(rec, requestWithActor(req, "operator"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}

// An unreachable admin API is the one case with no upstream status at all.
func TestBotVerificationUnreachableUpstreamIs502(t *testing.T) {
	srv := &server{cfg: uiConfig{AdminAPIURL: "http://127.0.0.1:1", AdminAPIToken: "api-secret"}}
	rec := httptest.NewRecorder()
	srv.handleRevokeBotVerifierAPI(rec, requestWithActor(httptest.NewRequest(
		http.MethodPost, "/api/actions/revoke-bot-verifier", strings.NewReader(
			`{"reason":"x","confirm":true,"bot_id":3003}`)), "operator"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s, want 502", rec.Code, rec.Body.String())
	}
}

func TestBotVerificationReadFiltersAreValidatedBeforeTheStore(t *testing.T) {
	// No read store: a malformed query still has to be a 400, so the panel is told
	// what it got wrong whether or not the database is reachable.
	srv := &server{}
	cases := []struct {
		handler http.HandlerFunc
		path    string
	}{
		{srv.handleCustomVerificationsAPI, "/api/botverification/marks?peer_type=chat"},
		{srv.handleCustomVerificationsAPI, "/api/botverification/marks?verifier_bot_id=abc"},
		{srv.handleCustomVerificationsAPI, "/api/botverification/marks?before_id=-1"},
		{srv.handleCustomVerificationsAPI, "/api/botverification/marks?limit=abc"},
		{srv.handleCustomVerificationRequestsAPI, "/api/botverification/requests?status=in_review"},
		{srv.handleCustomVerificationRequestsAPI, "/api/botverification/requests?peer_type=chat"},
		{srv.handleCustomVerificationRequestsAPI, "/api/botverification/requests?limit=-1"},
		{srv.handleBotVerifiersAPI, "/api/botverification/verifiers?limit=abc"},
		{srv.handleVerificationIconsAPI, "/api/botverification/icons?limit=-2"},
	}
	for _, item := range cases {
		rec := httptest.NewRecorder()
		item.handler(rec, httptest.NewRequest(http.MethodGet, item.path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s, want 400", item.path, rec.Code, rec.Body.String())
		}
	}
	// A well-formed query with no store wired reports the store, not the query.
	rec := httptest.NewRecorder()
	srv.handleCustomVerificationRequestsAPI(rec, httptest.NewRequest(
		http.MethodGet, "/api/botverification/requests?status=pending&peer_type=channel&limit=10", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", rec.Code, rec.Body.String())
	}
}

func TestQueryFlagReadsThePanelsBooleans(t *testing.T) {
	for _, raw := range []string{"1", "true", "TRUE", " yes ", "on"} {
		if !queryFlag(raw) {
			t.Fatalf("queryFlag(%q) = false", raw)
		}
	}
	for _, raw := range []string{"", "0", "false", "no", "maybe"} {
		if queryFlag(raw) {
			t.Fatalf("queryFlag(%q) = true", raw)
		}
	}
}
