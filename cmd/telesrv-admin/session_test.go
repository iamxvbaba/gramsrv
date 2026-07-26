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

func TestStarGiftRowJSONPreservesInt64AsDecimalStrings(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	raw, err := json.Marshal(StarGiftRow{
		GiftID:        maxInt64,
		RevisionID:    maxInt64,
		Stars:         maxInt64,
		ConvertStars:  maxInt64,
		DocumentID:    maxInt64,
		AnimationSize: maxInt64,
		ReceivedCount: maxInt64,
	})
	if err != nil {
		t.Fatalf("marshal star gift row: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal star gift row: %v", err)
	}
	for _, field := range []string{"GiftID", "RevisionID", "Stars", "ConvertStars", "DocumentID", "AnimationSize", "ReceivedCount"} {
		if got[field] != "9223372036854775807" {
			t.Fatalf("%s = %#v, want exact decimal string", field, got[field])
		}
	}
}

func TestStarGiftActionDecimalStringDecodingPreservesInt64(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	req := httptest.NewRequest(http.MethodPost, "/api/actions/import-official-gift", strings.NewReader(`{
		"source_gift_id":"5895603153683874485",
		"gift_id":"9223372036854775807",
		"stars":"9223372036854775807",
		"convert_stars":"9223372036854775807",
		"upgrade_stars":"9223372036854775807"
	}`))
	var got importOfficialStarGiftAPIRequest
	if err := decodeJSON(req, &got); err != nil {
		t.Fatalf("decode gift action: %v", err)
	}
	if got.GiftID != maxInt64 || got.Stars != maxInt64 || got.ConvertStars != maxInt64 || got.UpgradeStars != maxInt64 {
		t.Fatalf("decoded gift action = %+v", got)
	}
}

func TestSetStarGiftEnabledBFFForwardsExactInt64(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	var got admin.SetStarGiftEnabledRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/gifts/set-enabled" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("upstream request path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(admin.CommandResult{CommandID: got.CommandID, Status: "completed", DryRun: got.DryRun})
	}))
	defer upstream.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: upstream.URL, AdminAPIToken: "secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/actions/set-gift-enabled", strings.NewReader(`{
		"reason":"precision regression","confirm":false,
		"gift_id":"9223372036854775807","enabled":false
	}`))
	req = req.WithContext(context.WithValue(req.Context(), actorKey{}, "operator"))
	rec := httptest.NewRecorder()
	srv.handleSetStarGiftEnabledAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got.GiftID != maxInt64 || got.Actor != "operator" || !got.DryRun {
		t.Fatalf("forwarded gift request = %+v", got)
	}
}

func TestMintCollectibleUsernameBFFForwardsActorAndTolerantScalars(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	var got admin.MintCollectibleUsernameRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/collectible-usernames/mint" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("upstream request path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(admin.CommandResult{CommandID: got.CommandID, Status: "completed", DryRun: got.DryRun})
	}))
	defer upstream.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: upstream.URL, AdminAPIToken: "secret"}}
	// The panel sends a picker id as a number, a nanoton amount as a string and an
	// RFC3339 purchase date; all three have to survive the hop unchanged.
	req := httptest.NewRequest(http.MethodPost, "/api/actions/mint-collectible-username", strings.NewReader(`{
		"reason":"fragment import","confirm":false,
		"username":"@Durov","owner_user_id":1001,"currency":"TON",
		"amount":"9223372036854775807","crypto_currency":"TON","crypto_amount":"250000000000",
		"url":"https://fragment.example/durov","purchase_date":"2026-07-26T00:00:00Z"
	}`))
	req = req.WithContext(context.WithValue(req.Context(), actorKey{}, "operator"))
	rec := httptest.NewRecorder()
	srv.handleMintCollectibleUsernameAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got.Actor != "operator" || !got.DryRun || got.CommandID == "" {
		t.Fatalf("forwarded command meta = %+v", got.CommandMeta)
	}
	if got.Username != "@Durov" || got.OwnerUserID != 1001 || got.Amount != maxInt64 ||
		got.CryptoAmount != 250000000000 || got.PurchaseDate != time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("forwarded mint request = %+v", got)
	}
}

func TestAdjustAccountRatingBFFForwardsNumericPayload(t *testing.T) {
	var got admin.AdjustAccountRatingRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account-ratings/adjust" {
			t.Fatalf("upstream path=%q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(admin.CommandResult{CommandID: got.CommandID, Status: "completed"})
	}))
	defer upstream.Close()

	srv := &server{cfg: uiConfig{AdminAPIURL: upstream.URL, AdminAPIToken: "secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/actions/adjust-account-rating", strings.NewReader(
		`{"reason":"manual penalty","confirm":true,"user_id":1001,"amount":-2500}`))
	req = req.WithContext(context.WithValue(req.Context(), actorKey{}, "operator"))
	rec := httptest.NewRecorder()
	srv.handleAdjustAccountRatingAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got.Actor != "operator" || got.UserID != 1001 || got.Amount != -2500 || got.DryRun {
		t.Fatalf("forwarded adjust request = %+v", got)
	}
}

func TestRevokeCollectibleUsernameBFFRejectsUnknownFields(t *testing.T) {
	srv := &server{cfg: uiConfig{AdminAPIURL: "http://127.0.0.1:1", AdminAPIToken: "secret"}}
	req := httptest.NewRequest(http.MethodPost, "/api/actions/revoke-collectible-username", strings.NewReader(
		`{"reason":"fraud","confirm":true,"username":"durov","burn":true,"actor":"attacker"}`))
	req = req.WithContext(context.WithValue(req.Context(), actorKey{}, "operator"))
	rec := httptest.NewRecorder()
	srv.handleRevokeCollectibleUsernameAPI(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "actor") {
		t.Fatalf("status=%d body=%s, want 400 rejecting the unknown actor field", rec.Code, rec.Body.String())
	}
}

func TestCollectibleUsernameAndRatingRowsJSONPreserveInt64AsDecimalStrings(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	raw, err := json.Marshal(CollectibleUsernameRow{
		ID: maxInt64, OwnerPeerID: maxInt64, Amount: maxInt64, CryptoAmount: maxInt64,
		OriginalOwnerPeerID: maxInt64, Version: maxInt64,
	})
	if err != nil {
		t.Fatalf("marshal collectible username row: %v", err)
	}
	var asset map[string]any
	if err := json.Unmarshal(raw, &asset); err != nil {
		t.Fatalf("unmarshal collectible username row: %v", err)
	}
	for _, field := range []string{"ID", "OwnerPeerID", "Amount", "CryptoAmount", "OriginalOwnerPeerID", "Version"} {
		if asset[field] != "9223372036854775807" {
			t.Fatalf("asset %s = %#v, want exact decimal string", field, asset[field])
		}
	}

	raw, err = json.Marshal(AccountRatingRow{
		UserID: maxInt64, Stars: maxInt64, CurrentLevelStars: maxInt64, NextLevelStars: maxInt64,
		StarsComponent: maxInt64, ActivityComponent: maxInt64, PenaltyComponent: maxInt64,
		ManualComponent: -maxInt64, PendingStars: maxInt64, Version: maxInt64,
	})
	if err != nil {
		t.Fatalf("marshal account rating row: %v", err)
	}
	var rating map[string]any
	if err := json.Unmarshal(raw, &rating); err != nil {
		t.Fatalf("unmarshal account rating row: %v", err)
	}
	for _, field := range []string{
		"UserID", "Stars", "CurrentLevelStars", "NextLevelStars",
		"StarsComponent", "ActivityComponent", "PenaltyComponent", "PendingStars", "Version",
	} {
		if rating[field] != "9223372036854775807" {
			t.Fatalf("rating %s = %#v, want exact decimal string", field, rating[field])
		}
	}
	if rating["ManualComponent"] != "-9223372036854775807" {
		t.Fatalf("rating ManualComponent = %#v, want signed decimal string", rating["ManualComponent"])
	}

	transfer, err := json.Marshal(CollectibleUsernameTransferRow{
		ID: maxInt64, CollectibleID: maxInt64, FromPeerID: maxInt64, ToPeerID: maxInt64, Amount: maxInt64,
	})
	if err != nil {
		t.Fatalf("marshal transfer row: %v", err)
	}
	var log map[string]any
	if err := json.Unmarshal(transfer, &log); err != nil {
		t.Fatalf("unmarshal transfer row: %v", err)
	}
	for _, field := range []string{"ID", "CollectibleID", "FromPeerID", "ToPeerID", "Amount"} {
		if log[field] != "9223372036854775807" {
			t.Fatalf("transfer %s = %#v, want exact decimal string", field, log[field])
		}
	}
}

func TestFlexScalarsAcceptNumbersStringsAndBlanks(t *testing.T) {
	var body mintCollectibleUsernameAPIRequest
	req := httptest.NewRequest(http.MethodPost, "/api/actions/mint-collectible-username", strings.NewReader(`{
		"username":"durov","currency":"XTR","amount":"","owner_user_id":null,
		"crypto_amount":"9223372036854775807","purchase_date":"2026-07-26"
	}`))
	if err := decodeJSON(req, &body); err != nil {
		t.Fatalf("decode mint action: %v", err)
	}
	if body.Amount.Int64() != 0 || body.OwnerUserID.Int64() != 0 ||
		body.CryptoAmount.Int64() != 9223372036854775807 ||
		body.PurchaseDate.Unix() != time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("decoded mint action = %+v", body)
	}

	var rating adjustAccountRatingAPIRequest
	numeric := httptest.NewRequest(http.MethodPost, "/api/actions/adjust-account-rating", strings.NewReader(
		`{"user_id":1001,"amount":-2500}`))
	if err := decodeJSON(numeric, &rating); err != nil {
		t.Fatalf("decode adjust action: %v", err)
	}
	if rating.UserID.Int64() != 1001 || rating.Amount.Int64() != -2500 {
		t.Fatalf("decoded adjust action = %+v", rating)
	}

	var broken adjustAccountRatingAPIRequest
	invalid := httptest.NewRequest(http.MethodPost, "/api/actions/adjust-account-rating", strings.NewReader(
		`{"user_id":"not-a-number"}`))
	if err := decodeJSON(invalid, &broken); err == nil {
		t.Fatal("decoded a non-numeric user_id")
	}
}

func TestNewCollectibleAndRatingRoutesRequireSession(t *testing.T) {
	srv, err := newServer(uiConfig{SessionKey: []byte("01234567890123456789012345678901")}, nil)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/collectible-usernames"},
		{http.MethodGet, "/api/collectible-usernames/7"},
		{http.MethodGet, "/api/account-ratings"},
		{http.MethodGet, "/api/account-ratings/7"},
		{http.MethodPost, "/api/actions/mint-collectible-username"},
		{http.MethodPost, "/api/actions/transfer-collectible-username"},
		{http.MethodPost, "/api/actions/revoke-collectible-username"},
		{http.MethodPost, "/api/actions/recompute-account-rating"},
		{http.MethodPost, "/api/actions/adjust-account-rating"},
	}
	for _, item := range cases {
		req := httptest.NewRequest(item.method, item.path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d, want 401", item.method, item.path, rec.Code)
		}
	}
}

func TestEscapeLikePatternKeepsUsernameSearchLiteral(t *testing.T) {
	if got := escapeLikePattern("crypto_king"); got != `crypto\_king` {
		t.Fatalf("escapeLikePattern underscore = %q", got)
	}
	if got := escapeLikePattern(`100%_\x`); got != `100\%\_\\x` {
		t.Fatalf("escapeLikePattern metacharacters = %q", got)
	}
	if got := escapeLikePattern(""); got != "" {
		t.Fatalf("escapeLikePattern empty = %q", got)
	}
}
