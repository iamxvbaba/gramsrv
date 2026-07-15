package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"telesrv/internal/admin"
)

func TestSignedSessionRoundTripAndTamper(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	now := time.Unix(1_700_000_000, 0)
	value, err := signSession(key, sessionClaims{Actor: "admin", Exp: now.Add(time.Hour).Unix(), Nonce: "n"})
	if err != nil {
		t.Fatalf("signSession: %v", err)
	}
	claims, ok := verifySession(key, value, now)
	if !ok || claims.Actor != "admin" {
		t.Fatalf("verify ok=%v claims=%+v", ok, claims)
	}
	if _, ok := verifySession(key, value+"x", now); ok {
		t.Fatal("tampered session verified")
	}
	if _, ok := verifySession(key, value, now.Add(2*time.Hour)); ok {
		t.Fatal("expired session verified")
	}
}

func TestSPAFallbackSmoke(t *testing.T) {
	srv, err := newServer(uiConfig{SessionKey: []byte("01234567890123456789012345678901")}, nil)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `<div id="root"></div>`) {
		t.Fatalf("spa body missing root: %s", rec.Body.String())
	}
}

func TestAdminAPIURLDefaultUsesAdminAPIPort(t *testing.T) {
	if got, want := adminAPIURL(""), "http://127.0.0.1:2599"; got != want {
		t.Fatalf("adminAPIURL(empty) = %q, want %q", got, want)
	}
}

func TestSetAccountFrozenBFFForwardsClientVisibleState(t *testing.T) {
	var got admin.SetAccountFrozenRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/accounts/set-frozen" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("upstream request path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(admin.CommandResult{CommandID: got.CommandID, Status: "completed", DryRun: got.DryRun})
	}))
	defer upstream.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: upstream.URL, AdminAPIToken: "secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/actions/set-frozen", strings.NewReader(`{
		"reason":"review","confirm":false,"user_id":1001,"frozen":true,
		"freeze_until":"2030-01-02T00:00:00Z","freeze_appeal_url":"https://appeals.example.test/1001"
	}`))
	req = req.WithContext(context.WithValue(req.Context(), actorKey{}, "operator"))
	rec := httptest.NewRecorder()
	srv.handleSetAccountFrozenAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got.Actor != "operator" || got.UserID != 1001 || !got.Frozen || !got.DryRun ||
		got.Until.IsZero() || got.AppealURL != "https://appeals.example.test/1001" {
		t.Fatalf("forwarded freeze request = %+v", got)
	}
}
