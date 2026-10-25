package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/admin"
)

// The operator-account and audit-log machinery is a thin layer over Postgres
// (the accounts table, the audit tables), so the session-revocation contract,
// the store writes and the list endpoints can only be proven against the real
// schema. Gated on TELESRV_TEST_POSTGRES_DSN like the other integration tests.
//
// These are deliberately end-to-end through the routed server where the
// behaviour being pinned is the HTTP surface (login signing, per-request
// re-read, list responses), and directly against the store methods where they
// are smaller (guard, uniqueness, audit trail).

func operatorServer(t *testing.T) (*server, *readStore) {
	t.Helper()
	store, _ := verificationReadStore(t)
	srv, err := newServer(uiConfig{
		SessionKey:  []byte(testSessionKey),
		Password:    "letmein",
		Permissions: []string{permissionAll},
	}, store, nil)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return srv, store
}

// namedSignIn is signIn for a database-backed operator: the same routes, the
// same cookie+csrf pairing, just a real account instead of the break-glass one.
func namedSignIn(t *testing.T, srv *server, username, secret string) ([]*http.Cookie, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(
		fmt.Sprintf(`{"username":%q,"secret":%q}`, username, secret)))
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login(%s) status=%d body=%s", username, rec.Code, rec.Body.String())
	}
	var body struct {
		Actor     string `json:"actor"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if body.Actor != username || body.CSRFToken == "" {
		t.Fatalf("login body=%s", rec.Body.String())
	}
	return rec.Result().Cookies(), body.CSRFToken
}

func TestServerSettingsRemainBreakGlassOnly(t *testing.T) {
	srv, store := operatorServer(t)
	ctx := context.Background()
	username := fmt.Sprintf("settings%d", time.Now().UnixNano()%1_000_000_000)
	secret := "test-password"
	user, err := srv.createAdminConsoleUser(ctx, username, secret, []string{permissionDashboardRead}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE id=$1`, user.ID) })
	// Even a manually inserted legacy grant cannot expose .env secrets to a
	// named operator; the route enforces break-glass identity independently.
	if _, err := store.pool.Exec(ctx, `UPDATE admin_console_users SET permissions=ARRAY[$2]::text[] WHERE id=$1`, user.ID, permissionServerManage); err != nil {
		t.Fatal(err)
	}
	cookies, _ := namedSignIn(t, srv, username, secret)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/server/env", nil), cookies))
	// v2 (D5) does not mount the .env editor at all, so the route is absent
	// (404); either way the named operator must not reach .env values.
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("named operator server env status=%d body=%s, want 403 or 404", rec.Code, rec.Body.String())
	}
}

func TestNamedOperatorLoginAndSessionRevocation(t *testing.T) {
	srv, store := operatorServer(t)
	pool := store.pool
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	username := "alice" + suffix
	secret := "first-secret-" + suffix
	next := "second-secret-" + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM admin_console_users WHERE username = $1`, username)
	})

	user, err := srv.createAdminConsoleUser(ctx, username, secret, []string{permissionAdminsManage, permissionAuditRead}, true)
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	if user.TokenEpoch != 1 {
		t.Fatalf("new operator epoch=%d, want 1", user.TokenEpoch)
	}

	// A fresh account signs in and its session is usable.
	cookies, _ := namedSignIn(t, srv, username, secret)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/session", nil), cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}

	// Changing the password bumps token_epoch, which retires every session that
	// was signed with the old epoch on their very next request.
	if err := srv.setAdminConsoleUserPassword(ctx, user.ID, next); err != nil {
		t.Fatalf("set password: %v", err)
	}
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/session", nil), cookies))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old-password session status=%d, want 401 after password change", rec.Code)
	}

	// The old secret no longer authenticates; the new one does.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(
		fmt.Sprintf(`{"username":%q,"secret":%q}`, username, secret))))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old secret status=%d, want 401", rec.Code)
	}
	cookies2, _ := namedSignIn(t, srv, username, next)

	// Disabling the account signs it out too, even though the cookie itself is
	// still cryptographically valid: the per-request re-read sees the row is not
	// enabled and refuses.
	if _, err := srv.updateAdminConsoleUser(ctx, user.ID, []string{permissionAdminsManage, permissionAuditRead}, false); err != nil {
		t.Fatalf("disable operator: %v", err)
	}
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/session", nil), cookies2))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled session status=%d, want 401", rec.Code)
	}

	// And a disabled account cannot even log in again.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(
		fmt.Sprintf(`{"username":%q,"secret":%q}`, username, next))))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled login status=%d, want 401", rec.Code)
	}
}

func TestDemotionAppliesFromTheNextRequestWithoutSigningOut(t *testing.T) {
	srv, store := operatorServer(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	username := "bob" + suffix
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE username = $1`, username)
	})

	secret := "secret-" + suffix
	user, err := srv.createAdminConsoleUser(ctx, username, secret, []string{permissionAdminsManage, permissionAuditRead}, true)
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	cookies, _ := namedSignIn(t, srv, username, secret)

	// With both rights, the audit log answers.
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/audit-logs", nil), cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("audit read status=%d, want 200 before demotion", rec.Code)
	}

	// Drop audit.read but keep the account enabled. The already-signed-in
	// session is not terminated (the epoch moved for no reason), but its rights
	// are re-read every request, so the audit route now refuses without a
	// re-login.
	if _, err := srv.updateAdminConsoleUser(ctx, user.ID, []string{permissionAdminsManage}, true); err != nil {
		t.Fatalf("demote operator: %v", err)
	}
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/audit-logs", nil), cookies))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("audit read after demotion status=%d, want 403", rec.Code)
	}
	// The session itself is still alive and the operator list still works.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/admin-users", nil), cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin-users after demotion status=%d, want 200", rec.Code)
	}
	var list struct {
		System map[string]any `json:"system"`
		Rows   int            `json:"-"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.System["username"] != breakGlassUsername {
		t.Fatalf("admin-users response omitted the system operator: %s", rec.Body.String())
	}
}

func TestOperatorUniquenessAndLastManagerGuard(t *testing.T) {
	srv, store := operatorServer(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	name := func(prefix string) string { return prefix + suffix }
	users := []string{name("solo"), name("buddy"), name("DupUser"), name("dupuser")}
	for _, u := range users {
		t.Cleanup(func() {
			_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE username = $1`, u)
		})
	}

	pw := "password-" + suffix
	type row struct {
		id   int64
		name string
	}
	var (
		solo   row
		buddy  row
		dupOne row
	)

	u, err := srv.createAdminConsoleUser(ctx, name("solo"), pw, []string{permissionAdminsManage}, true)
	if err != nil {
		t.Fatalf("create solo: %v", err)
	}
	solo.id, solo.name = u.ID, u.Username

	// Removing the only manager's capability, or disabling the only manager, is
	// refused when the acting operator is doing it to their own row -- not
	// something to walk into by accident.
	if err := srv.guardManagerRemoval(ctx, solo.id, []string{}, false, solo.id); !errors.Is(err, errLastManagerStanding) {
		t.Fatalf("last manager guard err=%v, want errLastManagerStanding", err)
	}
	if err := srv.guardManagerRemoval(ctx, solo.id, []string{permissionAccountsRead}, true, solo.id); !errors.Is(err, errLastManagerStanding) {
		t.Fatalf("self-demotion guard err=%v, want errLastManagerStanding", err)
	}
	// But the same demotion is fine when someone else -- the built-in
	// break-glass login, which has no row and holds every right -- runs it:
	// the acting session still manages operators, so the console stays
	// operable. Before this, every edit to a restricted operator failed while
	// the panel was administered from the master login.
	if err := srv.guardManagerRemoval(ctx, solo.id, []string{permissionAccountsRead}, true, 0); err != nil {
		t.Fatalf("break-glass edit of the last named manager err=%v, want nil", err)
	}

	// With a second manager present the same edit is allowed.
	u, err = srv.createAdminConsoleUser(ctx, name("buddy"), pw, []string{permissionAdminsManage}, true)
	if err != nil {
		t.Fatalf("create buddy: %v", err)
	}
	buddy.id, buddy.name = u.ID, u.Username
	if err := srv.guardManagerRemoval(ctx, solo.id, []string{}, false, solo.id); err != nil {
		t.Fatalf("guard with a second manager err=%v, want nil", err)
	}

	// Granting a right to a restricted operator who does not hold admins.manage
	// has always to be possible: nobody is being demoted. A non-self edit must
	// never trip the fence, so the reported "stars.read cannot be set for
	// admins" failure stays fixed.
	u, err = srv.createAdminConsoleUser(ctx, name("staff"), pw, []string{permissionAccountsRead, permissionPremiumManage}, true)
	if err != nil {
		t.Fatalf("create staff: %v", err)
	}
	staffID := u.ID
	if err := srv.guardManagerRemoval(ctx, staffID, []string{permissionAccountsRead, permissionPremiumManage, permissionStarsRead}, true, 0); err != nil {
		t.Fatalf("grant stars.read to restricted operator err=%v, want nil", err)
	}
	if err := srv.guardManagerRemoval(ctx, staffID, []string{permissionAccountsRead}, true, staffID); err != nil {
		t.Fatalf("self-edit that keeps staff a non-manager err=%v, want nil", err)
	}

	// The unique index is on lower(username), so a differently-cased duplicate
	// is refused rather than allowed to shadow the original.
	u, err = srv.createAdminConsoleUser(ctx, name("DupUser"), pw, []string{permissionAdminsManage}, true)
	if err != nil {
		t.Fatalf("create DupUser: %v", err)
	}
	dupOne.id, dupOne.name = u.ID, u.Username
	if _, err := srv.createAdminConsoleUser(ctx, name("dupuser"), pw, []string{permissionAdminsManage}, true); !errors.Is(err, errAdminUsernameTaken) {
		t.Fatalf("cased duplicate err=%v, want errAdminUsernameTaken", err)
	}

	// Leftovers that could trip other runs are removed.
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE id = $1`, dupOne.id)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE id = $1`, buddy.id)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE id = $1`, staffID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE id = $1`, solo.id)
	})
	if buddy.name == "" || solo.name == "" || dupOne.name == "" || staffID == 0 {
		t.Fatal("fixture rows were not created")
	}
}

// TestDemotedInFlightOperatorCannotRemoveLastNamedManager proves permission
// revocation and the last-manager fence remain sound across the advisory-lock
// wait. Both requests can pass HTTP authentication before the first transaction
// demotes the second actor; the stale request must not then be confused with
// break-glass and remove the only remaining named manager.
func TestDemotedInFlightOperatorCannotRemoveLastNamedManager(t *testing.T) {
	srv, store := operatorServer(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	commandID := "stale-manager-" + suffix
	password := "password-" + suffix

	a, err := srv.createAdminConsoleUser(ctx, "routea"+suffix, password, []string{permissionAdminsManage}, true)
	if err != nil {
		t.Fatalf("create manager A: %v", err)
	}
	b, err := srv.createAdminConsoleUser(ctx, "routeb"+suffix, password, []string{permissionAdminsManage}, true)
	if err != nil {
		t.Fatalf("create manager B: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, commandID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, commandID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE id = $1 OR id = $2`, a.ID, b.ID)
	})

	bCookies, csrf := namedSignIn(t, srv, b.Username, password)
	winningTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin winning transaction: %v", err)
	}
	defer func() { _ = winningTx.Rollback(ctx) }()
	if _, err := winningTx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, managerGuardAdvisoryKey); err != nil {
		t.Fatalf("lock manager guard: %v", err)
	}

	body, err := json.Marshal(adminUserActionRequest{
		CommandID:   commandID,
		Reason:      "integration",
		Confirm:     true,
		ID:          a.ID,
		Permissions: []string{permissionAuditRead},
	})
	if err != nil {
		t.Fatalf("marshal stale request: %v", err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/actions/set-admin-operator-access", strings.NewReader(string(body)))
		req.Header.Set(csrfHeaderName, csrf)
		srv.routes().ServeHTTP(rec, withCookies(req, bCookies))
		done <- rec
	}()

	// Do not demote B until its request has passed requireAdminsManage and is
	// actually waiting for this transaction's advisory lock.
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := store.pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_locks
  WHERE locktype = 'advisory'
    AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND NOT granted
)`).Scan(&blocked); err != nil {
			t.Fatalf("observe advisory waiter: %v", err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("stale request did not reach the manager advisory lock")
	}

	if _, err := updateAdminConsoleUserOn(ctx, winningTx, b.ID, []string{permissionAuditRead}, true); err != nil {
		t.Fatalf("A demotes B: %v", err)
	}
	if err := winningTx.Commit(ctx); err != nil {
		t.Fatalf("commit A demotion: %v", err)
	}

	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stale request did not finish after lock release")
	}
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), errLastManagerStanding.Error()) {
		t.Fatalf("stale request status=%d body=%s, want guard failure", rec.Code, rec.Body.String())
	}

	var managers int
	if err := store.pool.QueryRow(ctx, `
SELECT count(*)::int FROM admin_console_users
WHERE enabled AND permissions @> ARRAY[$1]::text[]`, permissionAdminsManage).Scan(&managers); err != nil {
		t.Fatalf("count remaining managers: %v", err)
	}
	if managers != 1 {
		t.Fatalf("remaining named managers=%d, want A preserved as the only manager", managers)
	}
}

// TestOperatorPermissionEditsThroughTheRoutes pins the reported failure end to
// end. All operator routes are gated on admins.manage, and while the console is
// administered from the built-in master login that session holds the right but
// has no admin_console_users row -- so the old last-manager count, which only
// looked at named accounts, made every edit to a restricted operator fail with
// "this would leave no enabled account able to manage operators". Granting
// stars.read to a restricted operator, and every other assignable right, must
// go through and persist.
func TestOperatorPermissionEditsThroughTheRoutes(t *testing.T) {
	srv, store := operatorServer(t)
	pool := store.pool
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	username := "angela" + suffix
	secret := "password-" + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM admin_console_users WHERE username = $1`, username)
	})

	// The restricted operator from the report: catalogue rights, no admins.manage.
	user, err := srv.createAdminConsoleUser(ctx, username, secret, []string{permissionGiftsRead, permissionGiftsManage}, true)
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}

	// The master login runs the console.
	masterCookies, csrf := signIn(t, srv)

	readPerms := func(want ...string) {
		t.Helper()
		var perms []string
		var enabled bool
		if err := pool.QueryRow(ctx, `SELECT permissions, enabled FROM admin_console_users WHERE id = $1`, user.ID).Scan(&perms, &enabled); err != nil {
			t.Fatalf("read back operator: %v", err)
		}
		perms = normalisePermissions(perms)
		if !enabled || !reflect.DeepEqual(perms, want) {
			t.Fatalf("operator permissions=%v enabled=%v, want %v", perms, enabled, want)
		}
	}
	editNo := 0
	edit := func(permissions ...string) {
		t.Helper()
		editNo++
		body, err := json.Marshal(adminUserActionRequest{
			CommandID:   fmt.Sprintf("it-perm-%s-%d", suffix, editNo),
			Reason:      "integration",
			Confirm:     true,
			ID:          user.ID,
			Permissions: permissions,
		})
		if err != nil {
			t.Fatalf("marshal edit: %v", err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/actions/set-admin-operator-access", strings.NewReader(string(body)))
		req.Header.Set(csrfHeaderName, csrf)
		srv.routes().ServeHTTP(rec, withCookies(req, masterCookies))
		if rec.Code != http.StatusOK {
			t.Fatalf("edit %v status=%d body=%s", permissions, rec.Code, rec.Body.String())
		}
		var result struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode edit response: %v", err)
		}
		if result.Status != "ok" && result.Status != "completed" {
			t.Fatalf("edit %v status=%q error=%q", permissions, result.Status, result.Error)
		}
	}

	// The reported bug: grant stars.read to a restricted operator.
	edit(permissionGiftsRead, permissionGiftsManage, permissionStarsRead)
	readPerms(permissionGiftsRead, permissionGiftsManage, permissionStarsRead)

	// The granted right is live, and only that right: an operator holding just
	// stars.read can read the endpoint the panel's Stars page calls, and is
	// refused everywhere else.
	edit(permissionStarsRead)
	readPerms(permissionStarsRead)
	angelaCookies, _ := namedSignIn(t, srv, username, secret)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/stars/top", nil), angelaCookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("stars ledger with stars.read status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/admin-users", nil), angelaCookies))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin-users without admins.manage status=%d, want 403", rec.Code)
	}

	// Every assignable right -- stars.read included -- is individually grantable
	// and persists, so the fence can never lock the panel into refusing edits
	// for a restricted operator again. Granting then dropping admins.manage in
	// the loop iterates the demote/re-grant path every deployment hits.
	for _, p := range assignablePermissions() {
		edit(p)
		readPerms(p)
	}

	// Replacing the whole set (adding rights to an existing list) also works and
	// the operator stays enabled.
	edit(permissionAccountsRead, permissionAccountsManage, permissionAuditRead)
	readPerms(permissionAccountsRead, permissionAccountsManage, permissionAuditRead)
}

func TestAuditTrailRecordsAndListsThroughTheRoutes(t *testing.T) {
	srv, store := operatorServer(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	operator := "carol" + suffix
	password := "password-" + suffix

	// The audit tables are shared across the integration suite, so this test
	// owns a namespace -- its actor -- rather than assuming the tables start
	// empty. Wipe that namespace before asserting exact counts, and again in
	// the cleanup below, so a prior partial run (or a re-run against a reused
	// database) can never decide whether the route works.
	_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE actor = $1`, "audit-tester")
	_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE actor = $1`, "audit-tester")
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE username = $1`, operator)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE actor = $1`, "audit-tester")
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE actor = $1`, "audit-tester")
	})

	_, err := srv.createAdminConsoleUser(ctx, operator, password, []string{permissionAdminsManage, permissionAuditRead}, true)
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}

	mk := func(n int) admin.CommandMeta {
		return admin.CommandMeta{
			CommandID: fmt.Sprintf("test-cmd-%s-%d", suffix, n),
			Actor:     "audit-tester",
			Reason:    "integration",
			DryRun:    n%2 == 0,
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/audit-logs", nil)

	// One completed real run and one failed dry run.
	ok := admin.CommandResult{CommandID: mk(1).CommandID, Action: "set-admin-operator-password", Status: "ok", Message: "done"}
	srv.recordAgentCommand(req, mk(1), "set-admin-operator-password", "completed", &ok, nil)
	srv.recordAgentCommand(req, mk(2), "set-admin-operator-access", "failed", nil, errors.New("boom"))

	// Scoped to this test's actor: unfiltered would also see rows written by
	// every other bar following the same global table, which is theirs to own.
	got, err := store.listAuditLogs(ctx, "audit-tester", "", "", 10)
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("list returned %d rows, want 2", len(got))
	}
	// Newest first by id.
	if got[0].Action != "set-admin-operator-access" || got[0].Status != "failed" || got[0].Error != "boom" {
		t.Fatalf("row[0]=%+v", got[0])
	}
	if got[1].DryRun || got[1].Status != "completed" || !strings.Contains(got[1].Result, `"message": "done"`) {
		t.Fatalf("row[1]=%+v (result=%s)", got[1], got[1].Result)
	}

	// The same records surface through the routed HTTP endpoint with filters,
	// for an operator holding audit.read.
	cookies, _ := namedSignIn(t, srv, operator, password)
	filter := func(query string) []auditLogAPIEntry {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/audit-logs?"+query, nil), cookies))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/audit-logs?%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		var out struct {
			Rows []auditLogAPIEntry `json:"rows"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode audit response: %v", err)
		}
		return out.Rows
	}

	if rows := filter("actor=audit-tester"); len(rows) != 2 {
		t.Fatalf("actor filter rows=%d, want 2", len(rows))
	}
	if rows := filter("actor=audit-tester&status=failed"); len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("status filter rows=%+v", rows)
	}
	if rows := filter("actor=audit-tester&action=set-admin-operator-password"); len(rows) != 1 {
		t.Fatalf("action filter rows=%d, want 1", len(rows))
	}
	// A limit below the row count slices newest-first.
	if rows := filter("actor=audit-tester&limit=1"); len(rows) != 1 || rows[0].Action != "set-admin-operator-access" {
		t.Fatalf("limit filter rows=%+v", rows)
	}
}

// TestOperatorCommandReplayConflictAndSingleExecution pins the review's
// idempotency requirement: the same command_id must never re-execute the
// mutation, an identical replay returns the stored outcome with
// already_executed, and a command_id reused for a different request (a
// different password, a different action) is refused with COMMAND_ID_CONFLICT.
func TestOperatorCommandReplayConflictAndSingleExecution(t *testing.T) {
	srv, store := operatorServer(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	operator := "opal" + suffix
	commandID := "op-pwd-" + suffix
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE username = $1`, operator)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, commandID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, commandID)
	})

	user, err := srv.createAdminConsoleUser(ctx, operator, "initial-"+suffix, []string{permissionAdminsManage}, true)
	if err != nil {
		t.Fatalf("create target operator: %v", err)
	}
	if user.TokenEpoch != 1 {
		t.Fatalf("new operator epoch=%d, want 1", user.TokenEpoch)
	}
	targetPW := "target-pw-" + suffix
	meta := admin.CommandMeta{CommandID: commandID, Actor: "audit-tester", Reason: "integration"}
	params := map[string]any{"id": user.ID}
	fn := func(ctx context.Context, tx pgx.Tx) (admin.CommandResult, error) {
		if err := setAdminConsoleUserPasswordOn(ctx, tx, user.ID, targetPW); err != nil {
			return admin.CommandResult{}, err
		}
		return admin.CommandResult{Status: "ok", Message: "changed"}, nil
	}
	readEpoch := func() int32 {
		t.Helper()
		var epoch int32
		if err := store.pool.QueryRow(ctx, `SELECT token_epoch FROM admin_console_users WHERE id = $1`, user.ID).Scan(&epoch); err != nil {
			t.Fatalf("read epoch: %v", err)
		}
		return epoch
	}

	result, err := srv.runOperatorCommand(ctx, meta, "set-admin-operator-password", params, targetPW, fn)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if result.AlreadyExecuted || result.Status != "completed" {
		t.Fatalf("first run result=%+v", result)
	}
	if epoch := readEpoch(); epoch != 2 {
		t.Fatalf("epoch after first password change=%d, want 2", epoch)
	}

	// Replaying the identical request returns the stored outcome and does NOT
	// bump the epoch again (the mutation a replay would re-run is the point of
	// the guard).
	replay, err := srv.runOperatorCommand(ctx, meta, "set-admin-operator-password", params, targetPW, fn)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.AlreadyExecuted || replay.Status != "completed" || replay.Message != "changed" {
		t.Fatalf("replay result=%+v, want stored outcome with already_executed", replay)
	}
	if epoch := readEpoch(); epoch != 2 {
		t.Fatalf("epoch after replay=%d, want 2 (no re-execution)", epoch)
	}

	// The same command_id with a different password disagrees on the fingerprint
	// and must conflict rather than change anything.
	otherSecret := "other-" + suffix
	if _, err := srv.runOperatorCommand(ctx, meta, "set-admin-operator-password", params, otherSecret, fn); err == nil || err.Error() != "COMMAND_ID_CONFLICT" {
		t.Fatalf("conflicting password err=%v, want COMMAND_ID_CONFLICT", err)
	}
	// A different action under the same command_id conflicts too.
	if _, err := srv.runOperatorCommand(ctx, meta, "create-admin-operator", params, otherSecret, fn); err == nil || err.Error() != "COMMAND_ID_CONFLICT" {
		t.Fatalf("conflicting action err=%v, want COMMAND_ID_CONFLICT", err)
	}
	if epoch := readEpoch(); epoch != 2 {
		t.Fatalf("epoch after conflicts=%d, want 2", epoch)
	}

	// Exactly one audit row ties the operation to its command_id once.
	var auditN int
	if err := store.pool.QueryRow(ctx, `SELECT count(*)::int FROM admin_audit_logs WHERE command_id = $1`, commandID).Scan(&auditN); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditN != 1 {
		t.Fatalf("audit rows=%d, want 1", auditN)
	}
}

// TestOperatorCommandFailureLandsInAuditAtomically checks that a failed
// mutation is still recorded (status failed) in both admin_commands and the
// audit trail through the same transaction as the attempt itself.
func TestOperatorCommandFailureLandsInAuditAtomically(t *testing.T) {
	srv, store := operatorServer(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	commandID := "op-fail-" + suffix
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, commandID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, commandID)
	})

	meta := admin.CommandMeta{CommandID: commandID, Actor: "audit-tester", Reason: "integration"}
	fn := func(ctx context.Context, tx pgx.Tx) (admin.CommandResult, error) {
		if err := setAdminConsoleUserPasswordOn(ctx, tx, 999_999_999_999, "pw-"+suffix); err != nil {
			return admin.CommandResult{}, err
		}
		return admin.CommandResult{Status: "ok", Message: "unreachable"}, nil
	}
	if _, err := srv.runOperatorCommand(ctx, meta, "set-admin-operator-password",
		map[string]any{"id": 999_999_999_999}, "pw-"+suffix, fn); !errors.Is(err, errAdminUserNotFound) {
		t.Fatalf("missing-target err=%v, want errAdminUserNotFound", err)
	}
	var inCommands, inAudit string
	if err := store.pool.QueryRow(ctx, `SELECT status FROM admin_commands WHERE command_id = $1`, commandID).Scan(&inCommands); err != nil {
		t.Fatalf("read admin_commands: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT status FROM admin_audit_logs WHERE command_id = $1`, commandID).Scan(&inAudit); err != nil {
		t.Fatalf("read admin_audit_logs: %v", err)
	}
	if inCommands != "failed" || inAudit != "failed" {
		t.Fatalf("failed command recorded as commands=%s audit=%s, want failed/failed", inCommands, inAudit)
	}
}

// TestOperatorCreateRouteIdempotentAndConflictAware exercises the routed
// create-operator flow end to end: dry run, confirm, replay of the confirmed
// command_id, and reuse of the same command_id for a different request.
func TestOperatorCreateRouteIdempotentAndConflictAware(t *testing.T) {
	srv, store := operatorServer(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	manager := "mgr" + suffix
	createdUser := "cookie" + suffix
	otherUser := "pretzel" + suffix
	dryID := "dry-create-" + suffix
	execID := "exec-create-" + suffix
	manager, other := manager, otherUser

	u, err := srv.createAdminConsoleUser(ctx, manager, "pw-"+suffix, []string{permissionAdminsManage, permissionAuditRead}, true)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	managerID := u.ID
	cookies, token := namedSignIn(t, srv, manager, "pw-"+suffix)
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE id = $1`, managerID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE username = $1`, createdUser)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE username = $1`, other)
		for _, id := range []string{dryID, execID} {
			_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, id)
			_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, id)
		}
	})

	post := func(commandID, username string, confirm bool) (int, map[string]any) {
		t.Helper()
		enabled := true
		body, err := json.Marshal(adminUserActionRequest{
			CommandID: commandID, Reason: "integration", Confirm: confirm,
			Username: username, Password: "pw-" + suffix,
			Permissions: []string{permissionAdminsManage}, Enabled: &enabled,
		})
		if err != nil {
			t.Fatalf("marshal action: %v", err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/actions/create-admin-operator", strings.NewReader(string(body)))
		req.Header.Set(csrfHeaderName, token)
		srv.routes().ServeHTTP(rec, withCookies(req, cookies))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// Dry run first; a fresh dry command_id. The runner normalises the outcome
	// status to the same completed/failed vocabulary the domain commands use.
	code, dry := post(dryID, createdUser, false)
	if code != http.StatusOK || dry["dry_run"] != true || dry["status"] != "completed" {
		t.Fatalf("dry run code=%d body=%+v", code, dry)
	}

	// Confirmed run under its own command_id executes the creation.
	code, exec := post(execID, createdUser, true)
	if code != http.StatusOK || exec["already_executed"] == true || exec["status"] != "completed" {
		t.Fatalf("confirm code=%d body=%+v", code, exec)
	}

	// Replaying the confirmed command_id returns the stored result and does not
	// try to create the same username again (which would have failed it).
	code, replay := post(execID, createdUser, true)
	if code != http.StatusOK || replay["already_executed"] != true || replay["status"] != "completed" {
		t.Fatalf("replay code=%d body=%+v", code, replay)
	}
	var created int
	if err := store.pool.QueryRow(ctx, `SELECT count(*)::int FROM admin_console_users WHERE username = $1`, createdUser).Scan(&created); err != nil {
		t.Fatalf("count created: %v", err)
	}
	if created != 1 {
		t.Fatalf("created rows=%d, want 1", created)
	}

	// Same command_id, different request: refuse (over 502 by panel convention,
	// the same way the domain service reports command conflicts), and do not
	// create anything.
	code, conflict := post(execID, other, true)
	if code != http.StatusBadGateway || conflict["error"] != "COMMAND_ID_CONFLICT" {
		t.Fatalf("conflict code=%d body=%+v, want 502 COMMAND_ID_CONFLICT", code, conflict)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*)::int FROM admin_console_users WHERE username = $1`, other).Scan(&created); err != nil {
		t.Fatalf("count other: %v", err)
	}
	if created != 0 {
		t.Fatalf("conflicting request created %d rows, want 0", created)
	}

	// Exactly two audit rows exist: the dry run and the executed run.
	var auditN int
	if err := store.pool.QueryRow(ctx, `
SELECT count(*)::int FROM admin_audit_logs WHERE command_id = $1 OR command_id = $2`, dryID, execID).Scan(&auditN); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditN != 2 {
		t.Fatalf("audit rows=%d, want 2 (dry + exec, no replay duplicates)", auditN)
	}
}

// TestOperatorCreateRouteRefusesUntenablePermissions proves the routes enforce
// validatePermissions end to end: granting "*" to a named account, or any name
// outside the assignable vocabulary, fails the dry run itself (verified before
// an operator is asked to confirm) and never reaches the store.
func TestOperatorCreateRouteRefusesUntenablePermissions(t *testing.T) {
	srv, store := operatorServer(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	manager := "mgr" + suffix
	username := "fails" + suffix
	commandID := "bad-perm-" + suffix

	u, err := srv.createAdminConsoleUser(ctx, manager, "pw-"+suffix, []string{permissionAdminsManage, permissionAuditRead}, true)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	managerID := u.ID
	cookies, token := namedSignIn(t, srv, manager, "pw-"+suffix)
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE id = $1`, managerID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_console_users WHERE username = $1`, username)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, commandID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, commandID)
	})

	post := func(permissions []string) (int, map[string]any) {
		t.Helper()
		enabled := true
		body, err := json.Marshal(adminUserActionRequest{
			CommandID: commandID, Reason: "integration", Confirm: false,
			Username: username, Password: "pw-" + suffix,
			Permissions: permissions, Enabled: &enabled,
		})
		if err != nil {
			t.Fatalf("marshal action: %v", err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/actions/create-admin-operator", strings.NewReader(string(body)))
		req.Header.Set(csrfHeaderName, token)
		srv.routes().ServeHTTP(rec, withCookies(req, cookies))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	cases := []string{permissionAll, "acounts.read", "accounts.*"}
	var rejected int
	for _, bad := range cases {
		code, out := post([]string{bad})
		if code != http.StatusBadGateway {
			t.Fatalf("permission %q code=%d body=%+v, want 502", bad, code, out)
		}
		if msg, _ := out["message"].(string); msg == "" {
			t.Fatalf("permission %q rejection without a message body=%+v", bad, out)
		}
		rejected++
	}
	// The dry run rejected every bad permission without creating the user.
	if rejected != len(cases) {
		t.Fatalf("rejected=%d cases=%d", rejected, len(cases))
	}
	var created int
	if err := store.pool.QueryRow(ctx, `SELECT count(*)::int FROM admin_console_users WHERE username = $1`, username).Scan(&created); err != nil {
		t.Fatalf("count created: %v", err)
	}
	if created != 0 {
		t.Fatalf("created rows=%d, want 0", created)
	}
}
