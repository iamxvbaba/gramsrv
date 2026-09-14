// Package extbridge is the one deliberately minimal HTTP surface gramsrv
// exposes to third-party Shuza products (today: ShuzaFrag/frag.sgq.me) that
// need to read or adjust something on a real gramsrv account without
// re-implementing gramsrv themselves or getting a direct database
// connection. It binds to loopback only and is reached exclusively over the
// private SSH tunnel each product's own VPS holds open to this host
// (shuzabridge-tunnel.service) -- never a public port, never CORS-enabled.
//
// The balance this exposes is gramsrv's own internal TON ledger
// (internal/store/postgres.StarGiftLifecycleStore's ton_balances/
// ton_transactions -- the same balance a client already shows and the
// star-gift lifecycle already spends from for upgrades/transfers/resale),
// not a Fragment-specific ledger: "the balance in the client app" the
// product exists to reflect.
//
// Every endpoint here is a purpose-built, minimal projection of exactly one
// piece of state; this package must never grow into a general gramsrv API
// client. See the standing principle: third-party features get their own
// repo/DB/process, and only the minimum necessary endpoints land in gramsrv
// itself.
package extbridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const maxAdjustBodyBytes = 4 << 10

// TonLedger is the store surface extbridge's balance endpoints need --
// satisfied by *postgres.StarGiftLifecycleStore.
type TonLedger interface {
	TonBalance(ctx context.Context, userID int64) (int64, error)
	AdjustTonBalance(ctx context.Context, userID, deltaNanotons int64, reason domain.StarsTransactionReason) (int64, error)
}

// Config configures NewHandler.
type Config struct {
	Ledger TonLedger
	// SharedSecret is compared (constant-time) against every request's
	// "Authorization: Bearer <secret>" header. Required -- this endpoint sits
	// behind a tunnel, not behind gramsrv's own MTProto auth, so it must
	// authenticate itself.
	SharedSecret string
	Logger       *zap.Logger
}

type handler struct {
	ledger TonLedger
	secret string
	log    *zap.Logger
}

// NewHandler builds the extbridge HTTP handler. Callers are expected to
// serve it on a loopback-only listener (see cmd/telesrv/main.go), never the
// same mux as gramsrv's public routes.
func NewHandler(cfg Config) (http.Handler, error) {
	if cfg.Ledger == nil {
		return nil, fmt.Errorf("extbridge: Ledger is nil")
	}
	if strings.TrimSpace(cfg.SharedSecret) == "" {
		return nil, fmt.Errorf("extbridge: SharedSecret is empty")
	}
	log := cfg.Logger
	if log == nil {
		log = zap.NewNop()
	}
	h := &handler{ledger: cfg.Ledger, secret: cfg.SharedSecret, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ton-balance/{user_id}", h.getBalance)
	mux.HandleFunc("POST /v1/ton-balance/{user_id}/adjust", h.adjustBalance)
	return h.withAuth(mux), nil
}

func (h *handler) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(h.secret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseUserID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	return id, err == nil && id > 0
}

type balanceResponse struct {
	UserID   int64 `json:"user_id"`
	Nanotons int64 `json:"nanotons"`
}

func (h *handler) getBalance(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseUserID(r)
	if !ok {
		http.Error(w, "invalid user_id", http.StatusBadRequest)
		return
	}
	nanotons, err := h.ledger.TonBalance(r.Context(), userID)
	if err != nil {
		h.writeBalanceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, balanceResponse{UserID: userID, Nanotons: nanotons})
}

type adjustRequest struct {
	// DeltaNanotons is added to the balance; negative debits (e.g. a
	// marketplace purchase). The caller (ShuzaFrag) owns idempotency on its
	// own side -- this endpoint applies whatever delta it's given, exactly
	// once per call.
	DeltaNanotons int64 `json:"delta_nanotons"`
}

func (h *handler) adjustBalance(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseUserID(r)
	if !ok {
		http.Error(w, "invalid user_id", http.StatusBadRequest)
		return
	}
	var req adjustRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxAdjustBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.DeltaNanotons == 0 {
		http.Error(w, "delta_nanotons must be non-zero", http.StatusBadRequest)
		return
	}
	nanotons, err := h.ledger.AdjustTonBalance(r.Context(), userID, req.DeltaNanotons, domain.StarsReasonFragment)
	if err != nil {
		h.writeBalanceErr(w, err)
		return
	}
	h.log.Info("extbridge: ton balance adjusted",
		zap.Int64("user_id", userID), zap.Int64("delta_nanotons", req.DeltaNanotons), zap.Int64("new_balance_nanotons", nanotons))
	writeJSON(w, http.StatusOK, balanceResponse{UserID: userID, Nanotons: nanotons})
}

func (h *handler) writeBalanceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrUserNotFound), errors.Is(err, domain.ErrStarGiftOwnerInvalid):
		http.Error(w, "user not found", http.StatusNotFound)
	case errors.Is(err, domain.ErrStarsInsufficient):
		http.Error(w, "insufficient balance", http.StatusConflict)
	default:
		h.log.Error("extbridge: ton balance store error", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
