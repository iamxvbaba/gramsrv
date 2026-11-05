package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The live review console serves message media on the panel origin. The admin
// API already sniffs and allowlists the type, but the panel must not depend on
// that: it sets its own safety headers on whatever comes back.
func TestLiveMediaProxyKeepsItsOwnSafetyHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Like the real admin API, refuse a request without the bearer token.
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("key") {
		case "doc:1":
			// An upstream regression that serves HTML as HTML.
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<script>fetch('/api/actions/x',{method:'POST'})</script>`))
		case "doc:2":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", "attachment")
			_, _ = w.Write([]byte("%PDF-1.4"))
		case "doc:3":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte(`{"error":"media is larger than 16 MiB"}`))
		default:
			http.Error(w, "files: backend mismatch for object 7f/3a", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(upstream.Close)

	srv := panelServer(t, permissionMessagesRead)
	srv.cfg.AdminAPIURL = upstream.URL
	srv.cfg.AdminAPIToken = "secret"
	cookies, _ := signIn(t, srv)
	get := func(key string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/live/media?key="+key, nil), cookies))
		return rec
	}
	checkSafety := func(key string, rec *httptest.ResponseRecorder) {
		t.Helper()
		h := rec.Header()
		if h.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") ||
			h.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s safety headers = %v", key, h)
		}
	}

	rec := get("doc:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("html status=%d body=%s", rec.Code, rec.Body.String())
	}
	checkSafety("html", rec)

	rec = get("doc:2")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/octet-stream" ||
		rec.Header().Get("Content-Disposition") != "attachment" {
		t.Fatalf("download status=%d headers=%v", rec.Code, rec.Header())
	}
	checkSafety("download", rec)

	rec = get("doc:3")
	if rec.Code != http.StatusRequestEntityTooLarge ||
		rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		rec.Body.String() != "This file is larger than 16 MiB and can't be opened from the review console." {
		t.Fatalf("too large status=%d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
	}
	checkSafety("too large", rec)

	rec = get("doc:4")
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "backend mismatch") {
		t.Fatalf("upstream failure status=%d body=%q, want a bare 404", rec.Code, rec.Body.String())
	}
}

// Live review reads private conversations, so every /api/live route needs
// messages.read; neighbouring read rights (content, moderation) are not enough.
func TestLiveRoutesRefuseASessionWithoutMessagesRead(t *testing.T) {
	paths := []string{"/api/live/chats?user_id=1", "/api/live/peers?users=1", "/api/live/media?key=doc:1"}
	for _, permission := range []string{permissionContentRead, permissionModerationReview} {
		srv := panelServer(t, permission)
		cookies, _ := signIn(t, srv)
		for _, path := range paths {
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, path, nil), cookies))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s with %s status=%d body=%s, want 403", path, permission, rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode 403 body: %v", err)
			}
			if body["code"] != "FORBIDDEN" || body["permission"] != permissionMessagesRead {
				t.Fatalf("%s 403 body=%+v, want %s named", path, body, permissionMessagesRead)
			}
		}
	}

	// The same routes do let a messages.read session through the gate.
	srv := panelServer(t, permissionMessagesRead)
	cookies, _ := signIn(t, srv)
	for _, path := range paths {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, path, nil), cookies))
		if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Fatalf("%s with messages.read status=%d body=%s, want past the permission gate", path, rec.Code, rec.Body.String())
		}
	}
}
