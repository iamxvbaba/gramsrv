package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxRequestBody = 64 << 10

var errIdempotencyConflict = errors.New("idempotency key was already used with a different payload")

type config struct {
	address string
	secret  string
	maxSkew time.Duration
	logCode bool
}

type deliveryRequest struct {
	Version    string    `json:"version"`
	DeliveryID string    `json:"delivery_id"`
	Purpose    string    `json:"purpose"`
	Channel    string    `json:"channel"`
	Recipient  string    `json:"recipient"`
	Code       string    `json:"code"`
	ExpiresAt  time.Time `json:"expires_at"`
	ExpiresIn  int64     `json:"expires_in"`
	Locale     string    `json:"locale,omitempty"`
}

type deliveryResponse struct {
	Accepted  bool   `json:"accepted"`
	MessageID string `json:"message_id,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Retryable *bool  `json:"retryable,omitempty"`
}

type deliveryFunc func(context.Context, deliveryRequest) (string, error)

type receipt struct {
	fingerprint [sha256.Size]byte
	expiresAt   time.Time
	done        chan struct{}
	messageID   string
	err         error
	completed   bool
}

type application struct {
	secret  []byte
	maxSkew time.Duration
	now     func() time.Time
	deliver deliveryFunc
	logger  *slog.Logger

	mu       sync.Mutex
	receipts map[string]*receipt
}

func main() {
	if err := run(); err != nil {
		slog.Error("OTP webhook example stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	app := newApplication(cfg.secret, cfg.maxSkew, time.Now, exampleDelivery(logger, cfg.logCode), logger)
	server := &http.Server{
		Addr:              cfg.address,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.secret == "" {
		logger.Warn("signature verification is disabled; set TELESRV_OTP_EXAMPLE_SECRET outside local development")
	}
	if cfg.logCode {
		logger.Warn("OTP code logging is enabled for local testing")
	}
	logger.Info("OTP webhook example listening", "address", cfg.address)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		err := <-serverErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func loadConfig() (config, error) {
	cfg := config{
		address: envOrDefault("TELESRV_OTP_EXAMPLE_ADDR", "127.0.0.1:2800"),
		secret:  os.Getenv("TELESRV_OTP_EXAMPLE_SECRET"),
		maxSkew: 5 * time.Minute,
	}
	if raw := strings.TrimSpace(os.Getenv("TELESRV_OTP_EXAMPLE_MAX_SKEW")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return config{}, fmt.Errorf("TELESRV_OTP_EXAMPLE_MAX_SKEW must be a positive duration")
		}
		cfg.maxSkew = parsed
	}
	if raw := strings.TrimSpace(os.Getenv("TELESRV_OTP_EXAMPLE_LOG_CODE")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("TELESRV_OTP_EXAMPLE_LOG_CODE must be a boolean")
		}
		cfg.logCode = parsed
	}
	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func newApplication(
	secret string,
	maxSkew time.Duration,
	now func() time.Time,
	deliver deliveryFunc,
	logger *slog.Logger,
) *application {
	return &application{
		secret:   []byte(secret),
		maxSkew:  maxSkew,
		now:      now,
		deliver:  deliver,
		logger:   logger,
		receipts: make(map[string]*receipt),
	}
}

func (a *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("POST /v1/otp/deliveries", a.handleDelivery)
	return mux
}

func (a *application) handleDelivery(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "CONTENT_TYPE_INVALID", false)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", false)
		return
	}
	if err := a.verifySignature(r.Header, body); err != nil {
		writeError(w, http.StatusUnauthorized, "SIGNATURE_INVALID", false)
		return
	}

	var request deliveryRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "JSON_INVALID", false)
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "JSON_INVALID", false)
		return
	}
	if err := validateRequest(request, r.Header.Get("Idempotency-Key"), a.now()); err != nil {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", false)
		return
	}

	messageID, err := a.deliverOnce(r.Context(), request, body)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", false)
		return
	}
	if err != nil {
		a.logger.Warn("OTP delivery failed", "delivery_id", request.DeliveryID)
		writeError(w, http.StatusBadGateway, "DELIVERY_FAILED", true)
		return
	}
	writeJSON(w, http.StatusOK, deliveryResponse{Accepted: true, MessageID: messageID})
}

func (a *application) verifySignature(header http.Header, body []byte) error {
	if len(a.secret) == 0 {
		return nil
	}

	timestamp := header.Get("X-Telesrv-Timestamp")
	unixSeconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("invalid timestamp")
	}
	delta := a.now().Sub(time.Unix(unixSeconds, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > a.maxSkew {
		return errors.New("timestamp outside allowed skew")
	}

	provided := header.Get("X-Telesrv-Signature")
	expected := signatureFor(a.secret, timestamp, body)
	if !hmac.Equal([]byte(provided), []byte(expected)) {
		return errors.New("signature mismatch")
	}
	return nil
}

func signatureFor(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = io.WriteString(mac, timestamp)
	_, _ = mac.Write([]byte{'.'})
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func validateRequest(request deliveryRequest, idempotencyKey string, now time.Time) error {
	if request.Version != "1" {
		return errors.New("unsupported version")
	}
	if request.DeliveryID == "" || len(request.DeliveryID) > 128 || request.DeliveryID != idempotencyKey {
		return errors.New("invalid delivery ID")
	}
	if len(request.Recipient) == 0 || len(request.Recipient) > 512 {
		return errors.New("invalid recipient")
	}
	if len(request.Code) == 0 || len(request.Code) > 32 {
		return errors.New("invalid code")
	}
	if len(request.Locale) > 64 || request.ExpiresIn < 0 || request.ExpiresAt.IsZero() || !request.ExpiresAt.After(now) {
		return errors.New("invalid expiry or locale")
	}

	expectedChannel, ok := map[string]string{
		"login_email":        "email",
		"login_email_setup":  "email",
		"login_email_change": "email",
		"login_sms":          "sms",
		"change_phone":       "sms",
	}[request.Purpose]
	if !ok || request.Channel != expectedChannel {
		return errors.New("invalid purpose or channel")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (a *application) deliverOnce(
	ctx context.Context,
	request deliveryRequest,
	body []byte,
) (string, error) {
	fingerprint := sha256.Sum256(body)
	now := a.now()

	a.mu.Lock()
	for id, existing := range a.receipts {
		if existing.completed && !existing.expiresAt.After(now) {
			delete(a.receipts, id)
		}
	}
	if existing, ok := a.receipts[request.DeliveryID]; ok {
		if existing.fingerprint != fingerprint {
			a.mu.Unlock()
			return "", errIdempotencyConflict
		}
		done := existing.done
		a.mu.Unlock()

		select {
		case <-done:
			a.mu.Lock()
			messageID, err := existing.messageID, existing.err
			a.mu.Unlock()
			return messageID, err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	current := &receipt{
		fingerprint: fingerprint,
		expiresAt:   request.ExpiresAt,
		done:        make(chan struct{}),
	}
	a.receipts[request.DeliveryID] = current
	a.mu.Unlock()

	messageID, err := a.deliver(ctx, request)

	a.mu.Lock()
	current.messageID = messageID
	current.err = err
	current.completed = true
	close(current.done)
	a.mu.Unlock()
	return messageID, err
}

// exampleDelivery is the extension point for an email/SMS provider. It does
// not send a real message. Replace this function with a provider call before
// real use. Code logging is an explicit local-debug option.
func exampleDelivery(logger *slog.Logger, logCode bool) deliveryFunc {
	return func(_ context.Context, request deliveryRequest) (string, error) {
		recipientHash := sha256.Sum256([]byte(request.Recipient))
		messageHash := sha256.Sum256([]byte(request.DeliveryID))
		attributes := []any{
			"delivery_id", request.DeliveryID,
			"purpose", request.Purpose,
			"channel", request.Channel,
			"recipient_sha256", hex.EncodeToString(recipientHash[:6]),
		}
		if logCode {
			attributes = append(attributes, "code", request.Code)
		}
		logger.Info("OTP delivery accepted by example adapter", attributes...)
		return "example_" + hex.EncodeToString(messageHash[:8]), nil
	}
}

func writeError(w http.ResponseWriter, status int, code string, retryable bool) {
	writeJSON(w, status, deliveryResponse{Accepted: false, ErrorCode: code, Retryable: &retryable})
}

func writeJSON(w http.ResponseWriter, status int, response deliveryResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
