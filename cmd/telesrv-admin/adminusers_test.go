package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The operator-account and audit-log routes are all gated the same way as the
// rest of the panel: no session at all is 401, a session without the matching
// right is a 403 that names the right, and only then does the handler run. The
// handlers themselves need a read store, so this file proves the fence around
// them -- the DB-backed behaviour lives in admin_operators_integration_test.go.
func TestAdminUserRoutesRequireASession(t *testing.T) {
	srv := panelServer(t, permissionAll)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/admin-users"},
		{http.MethodPost, "/api/actions/create-admin-operator"},
		{http.MethodPost, "/api/actions/set-admin-operator-access"},
		{http.MethodPost, "/api/actions/set-admin-operator-password"},
		{http.MethodGet, "/api/audit-logs"},
	}
	for _, item := range cases {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, httptest.NewRequest(item.method, item.path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d, want 401", item.method, item.path, rec.Code)
		}
	}
}

func TestAdminUserRoutesRefuseASessionWithoutTheRight(t *testing.T) {
	srv := panelServer(t, "gifts.import")
	cookies, token := signIn(t, srv)
	cases := []struct {
		method     string
		path       string
		permission string
	}{
		{http.MethodGet, "/api/admin-users", permissionAdminsManage},
		{http.MethodPost, "/api/actions/create-admin-operator", permissionAdminsManage},
		{http.MethodPost, "/api/actions/set-admin-operator-access", permissionAdminsManage},
		{http.MethodPost, "/api/actions/set-admin-operator-password", permissionAdminsManage},
		{http.MethodGet, "/api/audit-logs", permissionAuditRead},
	}
	for _, item := range cases {
		req := httptest.NewRequest(item.method, item.path, strings.NewReader(`{}`))
		req.Header.Set(csrfHeaderName, token)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(req, cookies))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s, want 403", item.method, item.path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode 403 body: %v", err)
		}
		if body["code"] != "FORBIDDEN" || body["permission"] != item.permission {
			t.Fatalf("%s 403 body=%+v, want the missing right named", item.path, body)
		}
	}
}

// The create-operator entry point must reject the reserved break-glass name and
// malformed usernames before it ever needs a database, and reject unusable
// passwords before it spends a bcrypt budget. All of these are validation
// paths, so they run against a server with no read store at all.
func TestCreateAdminConsoleUserValidatesBeforeTheStore(t *testing.T) {
	srv := &server{}
	ctx := context.Background()

	// The break-glass name is refused outright: a row by that name could never
	// be logged into, because authenticateLogin resolves it to the environment
	// credential first.
	if _, err := srv.createAdminConsoleUser(ctx, "admin", "whatever-password", nil, true); err != errUsernameReserved {
		t.Fatalf("reserved name err=%v, want errUsernameReserved", err)
	}
	if _, err := srv.createAdminConsoleUser(ctx, "ADMIN", "whatever-password", nil, true); err != errUsernameReserved {
		t.Fatalf("reserved name (cased) err=%v, want errUsernameReserved", err)
	}

	for _, username := range []string{"a", "ab", "with space", "user@example", "ünïcode", ""} {
		if _, err := srv.createAdminConsoleUser(ctx, username, "password", nil, true); err == nil {
			t.Fatalf("username %q accepted, want validation error", username)
		}
	}

	if _, err := srv.createAdminConsoleUser(ctx, "validuser", "   ", nil, true); err != errPasswordBlank {
		t.Fatalf("blank password err=%v, want errPasswordBlank", err)
	}
	// Even an otherwise valid create with an oversized password must stop at
	// validation (bcrypt silently truncates past 72 bytes).
	if _, err := srv.createAdminConsoleUser(ctx, "validuser", strings.Repeat("x", 73), nil, true); err != errPasswordTooLong {
		t.Fatalf("long password err=%v, want errPasswordTooLong", err)
	}
}

func TestParseBoundLimitDefaultsCapsAndRejects(t *testing.T) {
	if got, err := parseBoundedLimit("", 200); err != nil || got != 200 {
		t.Fatalf("default got=%d err=%v, want 200", got, err)
	}
	if got, err := parseBoundedLimit("  ", 200); err != nil || got != 200 {
		t.Fatalf("blank got=%d err=%v, want 200", got, err)
	}
	if got, err := parseBoundedLimit("1", 200); err != nil || got != 1 {
		t.Fatalf("min got=%d err=%v, want 1", got, err)
	}
	if got, err := parseBoundedLimit("9999", 200); err != nil || got != 200 {
		t.Fatalf("cap got=%d err=%v, want 200", got, err)
	}
	for _, raw := range []string{"0", "-5", "abc", "1.5"} {
		if _, err := parseBoundedLimit(raw, 200); err == nil {
			t.Fatalf("limit %q accepted, want rejection", raw)
		}
	}
}

// normalisePermissions deliberately keeps the wildcard collapse for legacy rows
// and the break-glass path, so the fence that keeps "*" off a named account
// lives in validatePermissions -- the handler calls it before anything is
// stored.
func TestValidatePermissionsRejectsTheWildcardAndUnknown(t *testing.T) {
	// The wildcard is refused for a named account even though normalise
	// recognises it.
	if err := validatePermissions([]string{permissionAll}); err == nil {
		t.Fatal("wildcard accepted, want rejection")
	}
	if err := validatePermissions([]string{"accounts.read", permissionAll, "audit.read"}); err == nil {
		t.Fatal("wildcard in a mixed list accepted, want rejection")
	}
	// Unknown names are refused, typos included.
	if err := validatePermissions([]string{"acounts.read"}); err == nil {
		t.Fatal("typo accepted, want rejection")
	}
	if err := validatePermissions([]string{"accounts.*"}); err == nil {
		t.Fatal("pattern accepted, want rejection")
	}
	if err := validatePermissions([]string{permissionServerManage}); err == nil {
		t.Fatal("server.manage is reserved for break-glass")
	}
	// Every assignable name passes, and an empty list is a valid grant.
	if err := validatePermissions(assignablePermissions()); err != nil {
		t.Fatalf("assignable list rejected: %v", err)
	}
	if err := validatePermissions([]string{"accounts.read", "audit.read"}); err != nil {
		t.Fatalf("known list rejected: %v", err)
	}
	if err := validatePermissions(nil); err != nil {
		t.Fatalf("nil rejected: %v", err)
	}
}

func TestNormalisePermissionsCollapsesToTheWildcard(t *testing.T) {
	got := normalisePermissions([]string{"accounts.read", "accounts.read", "audit.read"})
	if len(got) != 2 || got[0] != "accounts.read" || got[1] != "audit.read" {
		t.Fatalf("dedupe got=%v", got)
	}
	if got := normalisePermissions([]string{"accounts.read", "   ", permissionAll, "audit.read"}); len(got) != 1 || got[0] != permissionAll {
		t.Fatalf("wildcard collapse got=%v", got)
	}
	if got := normalisePermissions([]string{"", " ", "accounts.manage"}); len(got) != 1 || got[0] != "accounts.manage" {
		t.Fatalf("blank filter got=%v", got)
	}
	if got := normalisePermissions(nil); len(got) != 0 {
		t.Fatalf("nil got=%v, want empty", got)
	}
	if got := normalisePermissions([]string{" accounts.read "}); len(got) != 1 || got[0] != "accounts.read" {
		t.Fatalf("trim got=%v", got)
	}
}
