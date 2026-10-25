package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"telesrv/internal/admin"
)

func TestDebitStarsAPIConfirmationAndCSRF(t *testing.T) {
	var mu sync.Mutex
	var calls []admin.DebitStarsRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts/debit-stars" || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var body admin.DebitStarsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		calls = append(calls, body)
		mu.Unlock()
		writeJSON(w, http.StatusOK, admin.CommandResult{CommandID: body.CommandID, DryRun: body.DryRun, Status: "completed"})
	}))
	defer upstream.Close()
	srv := panelServer(t, permissionAll)
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = upstream.URL, "test-admin"
	cookies, csrf := signIn(t, srv)
	for _, tc := range []struct {
		name                         string
		authenticated, csrf, confirm bool
		status                       int
	}{
		{"anonymous", false, false, true, http.StatusUnauthorized},
		{"missing csrf", true, false, true, http.StatusForbidden},
		{"preview", true, true, false, http.StatusOK},
		{"execute", true, true, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"command_id":"dry-debit-test","reason":"manual adjustment","user_id":1001,"amount":20,"confirm":false}`
			if tc.confirm {
				body = strings.Replace(body, `"confirm":false`, `"confirm":true`, 1)
				body = strings.Replace(body, "dry-debit-test", "exec-debit-test", 1)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/actions/debit-stars", strings.NewReader(body))
			if tc.authenticated {
				req = withCookies(req, cookies)
			}
			if tc.csrf {
				req.Header.Set(csrfHeaderName, csrf)
			}
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("upstream calls=%d", len(calls))
	}
	for i, call := range calls {
		wantID := "dry-debit-test"
		if i == 1 {
			wantID = "exec-debit-test"
		}
		if call.UserID != 1001 || call.Amount != 20 || call.CommandID != wantID || call.Actor == "" || call.DryRun != (i == 0) {
			t.Fatalf("forwarded request=%+v", call)
		}
	}
}
