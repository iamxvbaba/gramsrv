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
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const (
	maxAdjustBodyBytes   = 4 << 10
	maxPurchaseBodyBytes = 4 << 10
	// marketplaceListedStatus is the internal collectible status ("vault" --
	// unclaimed, sitting in gramsrv's own vault, matches real Fragment's
	// meaning of the term) that ShuzaFrag's public API surfaces as "listed".
	marketplaceListedStatus = domain.CollectibleUsernameStatusVault
)

// TonLedger is the store surface extbridge's balance endpoints need --
// satisfied by *postgres.StarGiftLifecycleStore.
type TonLedger interface {
	TonBalance(ctx context.Context, userID int64) (int64, error)
	AdjustTonBalance(ctx context.Context, userID, deltaNanotons int64, reason domain.StarsTransactionReason) (int64, error)
}

// Usernames is the store surface the usernames marketplace endpoints need --
// satisfied by *usernamesapp.Service.
type Usernames interface {
	List(ctx context.Context, filter domain.CollectibleUsernameFilter) ([]domain.CollectibleUsername, error)
	Collectible(ctx context.Context, username string) (domain.CollectibleUsername, error)
	Transfer(ctx context.Context, req domain.TransferCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error)
}

// Phones is the store surface the phone-number marketplace endpoints need --
// satisfied by *postgres.CollectiblePhoneStore.
type Phones interface {
	ListCollectiblePhones(ctx context.Context, filter domain.CollectiblePhoneFilter) ([]domain.CollectiblePhone, error)
	CollectiblePhone(ctx context.Context, phone string) (domain.CollectiblePhone, error)
	TransferCollectiblePhone(ctx context.Context, req domain.TransferCollectiblePhoneRequest) (domain.CollectiblePhone, bool, error)
}

// Stars is the store surface the Stars-purchase endpoint needs -- satisfied
// by *postgres.StarsStore.
type Stars interface {
	Credit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) (domain.StarsBalance, error)
}

// Config configures NewHandler.
type Config struct {
	Ledger    TonLedger
	Usernames Usernames
	Phones    Phones
	Stars     Stars
	// SharedSecret is compared (constant-time) against every request's
	// "Authorization: Bearer <secret>" header. Required -- this endpoint sits
	// behind a tunnel, not behind gramsrv's own MTProto auth, so it must
	// authenticate itself.
	SharedSecret string
	Logger       *zap.Logger
}

type handler struct {
	ledger    TonLedger
	usernames Usernames
	phones    Phones
	stars     Stars
	secret    string
	log       *zap.Logger
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
	h := &handler{
		ledger: cfg.Ledger, usernames: cfg.Usernames, phones: cfg.Phones, stars: cfg.Stars,
		secret: cfg.SharedSecret, log: log,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ton-balance/{user_id}", h.getBalance)
	mux.HandleFunc("POST /v1/ton-balance/{user_id}/adjust", h.adjustBalance)
	if cfg.Usernames != nil {
		mux.HandleFunc("GET /bridge/v1/usernames", h.listUsernames)
		mux.HandleFunc("POST /bridge/v1/usernames/purchase", h.purchaseUsername)
	}
	if cfg.Phones != nil {
		mux.HandleFunc("GET /bridge/v1/phones", h.listPhones)
		mux.HandleFunc("POST /bridge/v1/phones/purchase", h.purchasePhone)
	}
	if cfg.Stars != nil {
		mux.HandleFunc("POST /bridge/v1/stars/buy", h.buyStars)
	}
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

// usernameResponse and phoneResponse are the wire shapes bridgeclient.go
// already expects (Username/Phone structs there) -- keep these fields
// exactly in sync with that client.

type usernameResponse struct {
	Username string `json:"username"`
	Status   string `json:"status"`
	Currency string `json:"currency,omitempty"`
	Amount   string `json:"amount,omitempty"`
	URL      string `json:"url,omitempty"`
}

func toUsernameResponse(c domain.CollectibleUsername) usernameResponse {
	resp := usernameResponse{Username: c.Username, Status: string(c.Status), URL: c.URL}
	if c.Status == marketplaceListedStatus {
		resp.Status = "listed"
	}
	if c.Amount > 0 {
		resp.Currency = c.Currency
		resp.Amount = strconv.FormatInt(c.Amount, 10)
	}
	return resp
}

func (h *handler) listUsernames(w http.ResponseWriter, r *http.Request) {
	filter := domain.CollectibleUsernameFilter{Query: r.URL.Query().Get("q"), Limit: 200}
	if status := r.URL.Query().Get("status"); status == "listed" || status == "" {
		filter.Status = marketplaceListedStatus
	}
	items, err := h.usernames.List(r.Context(), filter)
	if err != nil {
		h.log.Error("extbridge: list usernames failed", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]usernameResponse, len(items))
	for i, c := range items {
		out[i] = toUsernameResponse(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"usernames": out})
}

type purchaseUsernameRequest struct {
	Username    string `json:"username"`
	BuyerUserID string `json:"buyer_user_id"`
	Actor       string `json:"actor"`
	Reason      string `json:"reason"`
	CommandKey  string `json:"command_key"`
}

func (h *handler) purchaseUsername(w http.ResponseWriter, r *http.Request) {
	var req purchaseUsernameRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPurchaseBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	buyerID, err := strconv.ParseInt(req.BuyerUserID, 10, 64)
	if err != nil || buyerID <= 0 {
		http.Error(w, "invalid buyer_user_id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	asset, err := h.usernames.Collectible(ctx, req.Username)
	if err != nil {
		http.Error(w, "username not found", http.StatusNotFound)
		return
	}
	if asset.Status != marketplaceListedStatus {
		http.Error(w, "username is not listed for sale", http.StatusConflict)
		return
	}
	// Listings this marketplace surfaces must be TON-priced -- there is no
	// defined conversion from other currencies (XTR, USD, ...) to nanotons.
	// Mint/reprice a listing with Currency="TON" for it to be purchasable here.
	if asset.Amount > 0 && asset.Currency != "TON" {
		h.log.Error("extbridge: username listing has non-TON currency", zap.String("username", req.Username), zap.String("currency", asset.Currency))
		http.Error(w, "listing is not TON-priced", http.StatusConflict)
		return
	}
	if asset.Amount > 0 {
		if _, err := h.ledger.AdjustTonBalance(ctx, buyerID, -asset.Amount, domain.StarsReasonFragment); err != nil {
			h.writeBalanceErr(w, err)
			return
		}
	}
	transferred, found, err := h.usernames.Transfer(ctx, domain.TransferCollectibleUsernameRequest{
		Username: req.Username, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyerID},
		Actor: req.Actor, Reason: req.Reason, CommandKey: req.CommandKey,
	})
	if err != nil || !found {
		if asset.Amount > 0 {
			if _, refundErr := h.ledger.AdjustTonBalance(ctx, buyerID, asset.Amount, domain.StarsReasonFragment); refundErr != nil {
				h.log.Error("extbridge: refund after failed username transfer also failed",
					zap.Int64("buyer_user_id", buyerID), zap.String("username", req.Username), zap.Error(refundErr))
			}
		}
		h.log.Error("extbridge: username transfer failed", zap.String("username", req.Username), zap.Error(err))
		http.Error(w, "purchase failed", http.StatusBadGateway)
		return
	}
	h.log.Info("extbridge: username purchased", zap.String("username", req.Username), zap.Int64("buyer_user_id", buyerID), zap.Int64("amount_nanoton", asset.Amount))
	writeJSON(w, http.StatusOK, map[string]any{"username": toUsernameResponse(transferred)})
}

type phoneResponse struct {
	Phone    string `json:"phone"`
	Tier     string `json:"tier"`
	Status   string `json:"status"`
	Currency string `json:"currency,omitempty"`
	Amount   string `json:"amount,omitempty"`
}

func toPhoneResponse(c domain.CollectiblePhone) phoneResponse {
	resp := phoneResponse{Phone: c.Phone, Tier: string(c.Tier), Status: string(c.Status)}
	if c.Status == marketplaceListedStatus {
		resp.Status = "listed"
	}
	if c.Amount > 0 {
		resp.Currency = c.Currency
		resp.Amount = strconv.FormatInt(c.Amount, 10)
	}
	return resp
}

func (h *handler) listPhones(w http.ResponseWriter, r *http.Request) {
	filter := domain.CollectiblePhoneFilter{Limit: 200}
	if status := r.URL.Query().Get("status"); status == "listed" || status == "" {
		filter.Status = marketplaceListedStatus
	}
	items, err := h.phones.ListCollectiblePhones(r.Context(), filter)
	if err != nil {
		h.log.Error("extbridge: list phones failed", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]phoneResponse, len(items))
	for i, c := range items {
		out[i] = toPhoneResponse(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"phones": out})
}

type purchasePhoneRequest struct {
	Phone       string `json:"phone"`
	BuyerUserID string `json:"buyer_user_id"`
	Actor       string `json:"actor"`
	Reason      string `json:"reason"`
	CommandKey  string `json:"command_key"`
}

func (h *handler) purchasePhone(w http.ResponseWriter, r *http.Request) {
	var req purchasePhoneRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPurchaseBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	buyerID, err := strconv.ParseInt(req.BuyerUserID, 10, 64)
	if err != nil || buyerID <= 0 {
		http.Error(w, "invalid buyer_user_id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	asset, err := h.phones.CollectiblePhone(ctx, req.Phone)
	if err != nil {
		http.Error(w, "phone not found", http.StatusNotFound)
		return
	}
	if asset.Status != marketplaceListedStatus {
		http.Error(w, "phone is not listed for sale", http.StatusConflict)
		return
	}
	if asset.Amount > 0 && asset.Currency != "TON" {
		h.log.Error("extbridge: phone listing has non-TON currency", zap.String("phone", req.Phone), zap.String("currency", asset.Currency))
		http.Error(w, "listing is not TON-priced", http.StatusConflict)
		return
	}
	if asset.Amount > 0 {
		if _, err := h.ledger.AdjustTonBalance(ctx, buyerID, -asset.Amount, domain.StarsReasonFragment); err != nil {
			h.writeBalanceErr(w, err)
			return
		}
	}
	transferred, found, err := h.phones.TransferCollectiblePhone(ctx, domain.TransferCollectiblePhoneRequest{
		Phone: req.Phone, ToUserID: buyerID, Actor: req.Actor, Reason: req.Reason, CommandKey: req.CommandKey,
	})
	if err != nil || !found {
		if asset.Amount > 0 {
			if _, refundErr := h.ledger.AdjustTonBalance(ctx, buyerID, asset.Amount, domain.StarsReasonFragment); refundErr != nil {
				h.log.Error("extbridge: refund after failed phone transfer also failed",
					zap.Int64("buyer_user_id", buyerID), zap.String("phone", req.Phone), zap.Error(refundErr))
			}
		}
		h.log.Error("extbridge: phone transfer failed", zap.String("phone", req.Phone), zap.Error(err))
		http.Error(w, "purchase failed", http.StatusBadGateway)
		return
	}
	h.log.Info("extbridge: phone purchased", zap.String("phone", req.Phone), zap.Int64("buyer_user_id", buyerID), zap.Int64("amount_nanoton", asset.Amount))
	writeJSON(w, http.StatusOK, map[string]any{"phone": toPhoneResponse(transferred)})
}

type buyStarsRequest struct {
	UserID      string `json:"user_id"`
	Stars       int64  `json:"stars"`
	NanotonCost int64  `json:"nanoton_cost"`
}

func (h *handler) buyStars(w http.ResponseWriter, r *http.Request) {
	var req buyStarsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPurchaseBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	userID, err := strconv.ParseInt(req.UserID, 10, 64)
	if err != nil || userID <= 0 || req.Stars <= 0 || req.NanotonCost <= 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	tonBalance, err := h.ledger.AdjustTonBalance(ctx, userID, -req.NanotonCost, domain.StarsReasonFragment)
	if err != nil {
		h.writeBalanceErr(w, err)
		return
	}
	starsBalance, err := h.stars.Credit(ctx, userID, req.Stars, domain.StarsReasonFragment, domain.Peer{}, int(time.Now().Unix()), "ShuzaFrag", "")
	if err != nil {
		if _, refundErr := h.ledger.AdjustTonBalance(ctx, userID, req.NanotonCost, domain.StarsReasonFragment); refundErr != nil {
			h.log.Error("extbridge: refund after failed stars credit also failed", zap.Int64("user_id", userID), zap.Error(refundErr))
		}
		h.log.Error("extbridge: stars credit failed", zap.Int64("user_id", userID), zap.Error(err))
		http.Error(w, "purchase failed", http.StatusBadGateway)
		return
	}
	h.log.Info("extbridge: stars purchased", zap.Int64("user_id", userID), zap.Int64("stars", req.Stars), zap.Int64("nanoton_cost", req.NanotonCost))
	writeJSON(w, http.StatusOK, map[string]any{
		"ton_balance_nanoton": strconv.FormatInt(tonBalance, 10),
		"stars_balance":       strconv.FormatInt(starsBalance.Balance, 10),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
