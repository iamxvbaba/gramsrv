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

// The NFT wallet command is a two-step panel action like the rest: a preview is
// a dry run, the execute carries the preview's command id, and both the session
// fence and the gifts.manage right decide before any upstream call happens.
func TestSetNftGiftWalletAPIConfirmationAndFence(t *testing.T) {
	var mu sync.Mutex
	var calls []admin.SetNftGiftWalletRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/gifts/set-nft-wallet" || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var body admin.SetNftGiftWalletRequest
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
	payload := `{"command_id":"dry-nft-wallet-test","reason":"manual bind","ref":"gift-42","wallet_name":"Alice","wallet_address":"EQAAAQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHx2j","host_user_id":"1234","confirm":false}`

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
			body := payload
			if tc.confirm {
				body = strings.Replace(body, `"confirm":false`, `"confirm":true`, 1)
				body = strings.Replace(body, "dry-nft-wallet-test", "exec-nft-wallet-test", 1)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/actions/set-nft-gift-wallet", strings.NewReader(body))
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
		t.Fatalf("upstream calls=%d, want preview and execute", len(calls))
	}
	preview, execute := calls[0], calls[1]
	if !preview.DryRun || execute.DryRun {
		t.Fatalf("preview=%v execute=%v", preview.DryRun, execute.DryRun)
	}
	if preview.Ref != "gift-42" || preview.WalletName != "Alice" || preview.HostUserID != 1234 {
		t.Fatalf("preview=%+v", preview)
	}
	if preview.CommandID == execute.CommandID {
		t.Fatalf("execute reused the dry-run command id %q", execute.CommandID)
	}
	if execute.Reason != "manual bind" {
		t.Fatalf("reason=%q", execute.Reason)
	}
}

// The action sits behind gifts.manage exactly like the other gift commands.
func TestSetNftGiftWalletAPIRequiresGiftsManage(t *testing.T) {
	srv := panelServer(t, permissionGiftsRead)
	cookies, token := signIn(t, srv)
	req := httptest.NewRequest(http.MethodPost, "/api/actions/set-nft-gift-wallet",
		strings.NewReader(`{"command_id":"dry-nft-wallet-fence","reason":"manual bind","ref":"gift-42","confirm":false}`))
	req.Header.Set(csrfHeaderName, token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(req, cookies))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 403 body: %v", err)
	}
	if body["permission"] != permissionGiftsManage {
		t.Fatalf("403 body=%+v, want the missing right named", body)
	}
}
