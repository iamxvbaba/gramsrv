package extbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

type fakeLedger struct {
	balances map[int64]int64
}

func (f *fakeLedger) TonBalance(_ context.Context, userID int64) (int64, error) {
	if _, ok := f.balances[userID]; !ok {
		return 0, domain.ErrUserNotFound
	}
	return f.balances[userID], nil
}

func (f *fakeLedger) AdjustTonBalance(_ context.Context, userID, deltaNanotons int64, _ domain.StarsTransactionReason) (int64, error) {
	balance, ok := f.balances[userID]
	if !ok {
		return 0, domain.ErrUserNotFound
	}
	next := balance + deltaNanotons
	if next < 0 {
		return 0, domain.ErrStarsInsufficient
	}
	f.balances[userID] = next
	return next, nil
}

func newTestHandler(t *testing.T, balances map[int64]int64) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{Ledger: &fakeLedger{balances: balances}, SharedSecret: "test-secret"})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestGetBalanceRequiresAuth(t *testing.T) {
	h := newTestHandler(t, map[int64]int64{100: 5_000_000_000})
	req := httptest.NewRequest(http.MethodGet, "/v1/ton-balance/100", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestGetBalance(t *testing.T) {
	h := newTestHandler(t, map[int64]int64{100: 5_000_000_000})
	req := httptest.NewRequest(http.MethodGet, "/v1/ton-balance/100", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"nanotons":5000000000`) {
		t.Fatalf("body = %s, want nanotons=5000000000", rec.Body.String())
	}
}

func TestGetBalanceUnknownUser(t *testing.T) {
	h := newTestHandler(t, map[int64]int64{})
	req := httptest.NewRequest(http.MethodGet, "/v1/ton-balance/999", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestAdjustBalanceCredit(t *testing.T) {
	h := newTestHandler(t, map[int64]int64{100: 1_000_000_000})
	req := httptest.NewRequest(http.MethodPost, "/v1/ton-balance/100/adjust", strings.NewReader(`{"delta_nanotons": 2000000000}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"nanotons":3000000000`) {
		t.Fatalf("body = %s, want nanotons=3000000000", rec.Body.String())
	}
}

func TestAdjustBalanceDebitInsufficient(t *testing.T) {
	h := newTestHandler(t, map[int64]int64{100: 1_000_000_000})
	req := httptest.NewRequest(http.MethodPost, "/v1/ton-balance/100/adjust", strings.NewReader(`{"delta_nanotons": -2000000000}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdjustBalanceZeroDeltaRejected(t *testing.T) {
	h := newTestHandler(t, map[int64]int64{100: 1_000_000_000})
	req := httptest.NewRequest(http.MethodPost, "/v1/ton-balance/100/adjust", strings.NewReader(`{"delta_nanotons": 0}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestNewHandlerRequiresSharedSecret(t *testing.T) {
	if _, err := NewHandler(Config{Ledger: &fakeLedger{balances: map[int64]int64{}}}); err == nil {
		t.Fatalf("expected error for empty SharedSecret")
	}
}

type fakeUsernames struct {
	byName map[string]domain.CollectibleUsername
}

func (f *fakeUsernames) List(_ context.Context, filter domain.CollectibleUsernameFilter) ([]domain.CollectibleUsername, error) {
	var out []domain.CollectibleUsername
	for _, c := range f.byName {
		if filter.Status == "" || c.Status == filter.Status {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeUsernames) Collectible(_ context.Context, username string) (domain.CollectibleUsername, error) {
	c, ok := f.byName[username]
	if !ok {
		return domain.CollectibleUsername{}, domain.ErrUsernameNotOccupied
	}
	return c, nil
}

func (f *fakeUsernames) Transfer(_ context.Context, req domain.TransferCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	c, ok := f.byName[req.Username]
	if !ok || c.Status != domain.CollectibleUsernameStatusVault {
		return domain.CollectibleUsername{}, false, nil
	}
	c.Status = domain.CollectibleUsernameStatusOwned
	c.Owner = req.To
	f.byName[req.Username] = c
	return c, true, nil
}

type fakePhones struct {
	byPhone map[string]domain.CollectiblePhone
}

func (f *fakePhones) ListCollectiblePhones(_ context.Context, filter domain.CollectiblePhoneFilter) ([]domain.CollectiblePhone, error) {
	var out []domain.CollectiblePhone
	for _, c := range f.byPhone {
		if filter.Status == "" || c.Status == filter.Status {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakePhones) CollectiblePhone(_ context.Context, phone string) (domain.CollectiblePhone, error) {
	c, ok := f.byPhone[phone]
	if !ok {
		return domain.CollectiblePhone{}, domain.ErrCollectiblePhoneNotFound
	}
	return c, nil
}

func (f *fakePhones) TransferCollectiblePhone(_ context.Context, req domain.TransferCollectiblePhoneRequest) (domain.CollectiblePhone, bool, error) {
	c, ok := f.byPhone[req.Phone]
	if !ok || c.Status != domain.CollectibleUsernameStatusVault {
		return domain.CollectiblePhone{}, false, nil
	}
	c.Status = domain.CollectibleUsernameStatusOwned
	c.OwnerUserID = req.ToUserID
	f.byPhone[req.Phone] = c
	return c, true, nil
}

type fakeStars struct {
	balances map[int64]int64
}

func (f *fakeStars) Credit(_ context.Context, userID, amount int64, _ domain.StarsTransactionReason, _ domain.Peer, _ int, _, _ string) (domain.StarsBalance, error) {
	f.balances[userID] += amount
	return domain.StarsBalance{UserID: userID, Balance: f.balances[userID]}, nil
}

func newMarketTestHandler(t *testing.T, balances map[int64]int64, usernames map[string]domain.CollectibleUsername, phones map[string]domain.CollectiblePhone) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{
		Ledger:       &fakeLedger{balances: balances},
		Usernames:    &fakeUsernames{byName: usernames},
		Phones:       &fakePhones{byPhone: phones},
		Stars:        &fakeStars{balances: map[int64]int64{}},
		SharedSecret: "test-secret",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func authed(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer test-secret")
	return req
}

func TestListUsernamesDefaultsToListed(t *testing.T) {
	h := newMarketTestHandler(t, nil, map[string]domain.CollectibleUsername{
		"forsale": {Username: "forsale", Status: domain.CollectibleUsernameStatusVault, Currency: "TON", Amount: 5_000_000_000},
		"taken":   {Username: "taken", Status: domain.CollectibleUsernameStatusOwned},
	}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(httptest.NewRequest(http.MethodGet, "/bridge/v1/usernames", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"username":"forsale"`) || strings.Contains(rec.Body.String(), `"taken"`) {
		t.Fatalf("body = %s, want only the vault-status username listed", rec.Body.String())
	}
}

func TestPurchaseUsernameSuccess(t *testing.T) {
	balances := map[int64]int64{100: 10_000_000_000}
	h := newMarketTestHandler(t, balances, map[string]domain.CollectibleUsername{
		"forsale": {Username: "forsale", Status: domain.CollectibleUsernameStatusVault, Currency: "TON", Amount: 5_000_000_000},
	}, nil)
	rec := httptest.NewRecorder()
	req := authed(httptest.NewRequest(http.MethodPost, "/bridge/v1/usernames/purchase",
		strings.NewReader(`{"username":"forsale","buyer_user_id":"100"}`)))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if balances[100] != 5_000_000_000 {
		t.Fatalf("buyer balance = %d, want 5_000_000_000 after a 5 TON purchase", balances[100])
	}
}

func TestPurchaseUsernameInsufficientBalanceDoesNotTransfer(t *testing.T) {
	balances := map[int64]int64{100: 1_000_000_000}
	usernames := map[string]domain.CollectibleUsername{
		"forsale": {Username: "forsale", Status: domain.CollectibleUsernameStatusVault, Currency: "TON", Amount: 5_000_000_000},
	}
	h := newMarketTestHandler(t, balances, usernames, nil)
	rec := httptest.NewRecorder()
	req := authed(httptest.NewRequest(http.MethodPost, "/bridge/v1/usernames/purchase",
		strings.NewReader(`{"username":"forsale","buyer_user_id":"100"}`)))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
	if usernames["forsale"].Status != domain.CollectibleUsernameStatusVault {
		t.Fatalf("username status = %q, want it to stay vault (untransferred) after a failed purchase", usernames["forsale"].Status)
	}
}

func TestPurchaseUsernameNotListedRejected(t *testing.T) {
	h := newMarketTestHandler(t, map[int64]int64{100: 10_000_000_000}, map[string]domain.CollectibleUsername{
		"taken": {Username: "taken", Status: domain.CollectibleUsernameStatusOwned},
	}, nil)
	rec := httptest.NewRecorder()
	req := authed(httptest.NewRequest(http.MethodPost, "/bridge/v1/usernames/purchase",
		strings.NewReader(`{"username":"taken","buyer_user_id":"100"}`)))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
}

func TestPurchasePhoneSuccess(t *testing.T) {
	balances := map[int64]int64{100: 10_000_000_000}
	h := newMarketTestHandler(t, balances, nil, map[string]domain.CollectiblePhone{
		"+888123": {Phone: "+888123", Status: domain.CollectibleUsernameStatusVault, Currency: "TON", Amount: 3_000_000_000},
	})
	rec := httptest.NewRecorder()
	req := authed(httptest.NewRequest(http.MethodPost, "/bridge/v1/phones/purchase",
		strings.NewReader(`{"phone":"+888123","buyer_user_id":"100"}`)))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if balances[100] != 7_000_000_000 {
		t.Fatalf("buyer balance = %d, want 7_000_000_000 after a 3 TON purchase", balances[100])
	}
}

func TestBuyStarsSuccess(t *testing.T) {
	balances := map[int64]int64{100: 10_000_000_000}
	h := newMarketTestHandler(t, balances, nil, nil)
	rec := httptest.NewRecorder()
	req := authed(httptest.NewRequest(http.MethodPost, "/bridge/v1/stars/buy",
		strings.NewReader(`{"user_id":"100","stars":50,"nanoton_cost":100000000}`)))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if balances[100] != 9_900_000_000 {
		t.Fatalf("ton balance = %d, want 9_900_000_000 after buying 50 stars for 0.1 TON", balances[100])
	}
	if !strings.Contains(rec.Body.String(), `"stars_balance":"50"`) {
		t.Fatalf("body = %s, want stars_balance 50", rec.Body.String())
	}
}

func TestMarketplaceRoutesAbsentWithoutDeps(t *testing.T) {
	h, err := NewHandler(Config{Ledger: &fakeLedger{balances: map[int64]int64{}}, SharedSecret: "test-secret"})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(httptest.NewRequest(http.MethodGet, "/bridge/v1/usernames", nil)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when Usernames dep is nil", rec.Code)
	}
}
