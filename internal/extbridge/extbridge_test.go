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
