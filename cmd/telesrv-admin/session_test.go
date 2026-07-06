package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
