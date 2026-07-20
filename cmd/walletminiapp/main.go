// Command walletminiapp is a runnable Mini App demo for telesrv bot developers.
//
// It intentionally behaves like an external bot program: it serves the Mini App
// over HTTP and configures telesrv through the Bot API gateway. It does not write
// directly to telesrv stores, so the demo remains close to a real integration.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultBalanceCents = int64(125000)
	maxMemoLen          = 180
	maxTransferCents    = int64(500000)
)

type config struct {
	listen    string
	publicURL string
	botAPI    string
	token     string
	menuText  string
	register  bool
	initMax   time.Duration
}

type appServer struct {
	cfg    config
	botapi *botAPIClient
	wallet *walletStore
	log    *log.Logger
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.listen, "listen", envOr("TELESRV_WALLET_LISTEN", "127.0.0.1:8091"), "wallet mini app HTTP listen address")
	flag.StringVar(&cfg.publicURL, "public-url", os.Getenv("TELESRV_WALLET_PUBLIC_URL"), "public HTTPS URL used in the Telesrv menu button")
	flag.StringVar(&cfg.botAPI, "bot-api", envOr("TELESRV_BOT_API_URL", "http://127.0.0.1:8081"), "telesrv Bot API base URL")
	flag.StringVar(&cfg.token, "token", os.Getenv("TELESRV_BOT_TOKEN"), "bot token <bot_id>:<secret>")
	flag.StringVar(&cfg.menuText, "menu-text", envOr("TELESRV_WALLET_MENU_TEXT", "Wallet"), "menu button label")
	flag.BoolVar(&cfg.register, "register", true, "call Bot API setChatMenuButton on startup when token and public-url are set")
	flag.DurationVar(&cfg.initMax, "init-max-age", 24*time.Hour, "maximum accepted tgWebAppData age, 0 disables age checks")
	flag.Parse()

	logger := log.New(os.Stdout, "walletminiapp ", log.LstdFlags|log.Lmicroseconds)
	if cfg.publicURL == "" {
		cfg.publicURL = "http://" + cfg.listen + "/"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := &appServer{
		cfg:    cfg,
		botapi: newBotAPIClient(cfg.botAPI, cfg.token),
		wallet: newWalletStore(),
		log:    logger,
	}
	if err := app.configureBotMenu(ctx); err != nil {
		logger.Fatalf("configure bot menu: %v", err)
	}

	srv := &http.Server{
		Addr:              cfg.listen,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Printf("serving wallet mini app on http://%s/", cfg.listen)
	logger.Printf("configured public URL: %s", cfg.publicURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("serve: %v", err)
	}
}

func (s *appServer) configureBotMenu(ctx context.Context) error {
	if !s.cfg.register {
		s.log.Printf("bot menu registration disabled")
		return nil
	}
	if strings.TrimSpace(s.cfg.token) == "" {
		s.log.Printf("bot token missing; serving UI only, Bot API calls disabled")
		return nil
	}
	if !isHTTPSURL(s.cfg.publicURL) {
		s.log.Printf("public-url is not HTTPS; serving UI only, skip setChatMenuButton")
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.botapi.setChatMenuButton(ctx, s.cfg.menuText, s.cfg.publicURL); err != nil {
		return err
	}
	s.log.Printf("registered menu button %q -> %s", s.cfg.menuText, s.cfg.publicURL)
	return nil
}

func (s *appServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/session", s.handleSession)
	mux.HandleFunc("/api/transfer", s.handleTransfer)
	mux.HandleFunc("/api/share-prepared", s.handleSharePrepared)
	mux.HandleFunc("/api/answer-webapp-query", s.handleAnswerWebAppQuery)
	mux.HandleFunc("/api/payment-intent", s.handlePaymentIntent)
	mux.HandleFunc("/", s.handleIndex)
	return withSecurityHeaders(mux)
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *appServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, walletHTML)
}

func (s *appServer) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	raw := firstNonEmpty(r.Header.Get("X-Telegram-Init-Data"), r.URL.Query().Get("init_data"))
	session, err := parseWebAppSession(raw, s.cfg.token, s.cfg.initMax)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "INIT_DATA_INVALID", err.Error())
		return
	}
	snapshot := s.wallet.snapshot(session.User.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"session":  session,
		"wallet":   snapshot,
		"bot_api":  s.botapi.enabled(),
		"demo_url": s.cfg.publicURL,
	})
}

func (s *appServer) handleTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	var payload transferRequest
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON")
		return
	}
	session, err := parseWebAppSession(payload.InitData, s.cfg.token, s.cfg.initMax)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "INIT_DATA_INVALID", err.Error())
		return
	}
	tx, snapshot, err := s.wallet.transfer(session.User.ID, payload.To, payload.AmountCents, payload.Memo)
	if err != nil {
		writeError(w, http.StatusBadRequest, "TRANSFER_INVALID", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": session, "tx": tx, "wallet": snapshot})
}

func (s *appServer) handleSharePrepared(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	if !s.botapi.enabled() {
		writeError(w, http.StatusServiceUnavailable, "BOT_API_DISABLED", "bot token is required")
		return
	}
	var payload shareRequest
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON")
		return
	}
	session, err := parseWebAppSession(payload.InitData, s.cfg.token, s.cfg.initMax)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "INIT_DATA_INVALID", err.Error())
		return
	}
	result := walletInlineResult("wallet-share-"+strconv.FormatInt(time.Now().UnixNano(), 36), "Wallet receipt", receiptText(session, payload), s.publicResultURL())
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	prepared, err := s.botapi.savePreparedInlineMessage(ctx, session.User.ID, result)
	if err != nil {
		writeError(w, http.StatusBadGateway, "BOT_API_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "prepared": prepared})
}

func (s *appServer) handleAnswerWebAppQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	if !s.botapi.enabled() {
		writeError(w, http.StatusServiceUnavailable, "BOT_API_DISABLED", "bot token is required")
		return
	}
	var payload shareRequest
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON")
		return
	}
	session, err := parseWebAppSession(payload.InitData, s.cfg.token, s.cfg.initMax)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "INIT_DATA_INVALID", err.Error())
		return
	}
	if session.QueryID == "" {
		writeError(w, http.StatusBadRequest, "QUERY_ID_INVALID", "current launch has no web_app query_id")
		return
	}
	result := walletInlineResult("wallet-answer-"+strconv.FormatInt(time.Now().UnixNano(), 36), "Wallet receipt", receiptText(session, payload), s.publicResultURL())
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := s.botapi.answerWebAppQuery(ctx, session.QueryID, result)
	if err != nil {
		writeError(w, http.StatusBadGateway, "BOT_API_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": out})
}

func (s *appServer) handlePaymentIntent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	writeError(w, http.StatusNotImplemented, "PAYMENTS_BLOCKED", "durable invoice/payment state is not implemented in telesrv yet")
}

func (s *appServer) publicResultURL() string {
	if isHTTPSURL(s.cfg.publicURL) {
		return s.cfg.publicURL
	}
	return ""
}

type transferRequest struct {
	InitData    string `json:"init_data"`
	To          string `json:"to"`
	AmountCents int64  `json:"amount_cents"`
	Memo        string `json:"memo"`
}

type shareRequest struct {
	InitData    string `json:"init_data"`
	AmountCents int64  `json:"amount_cents"`
	Memo        string `json:"memo"`
}

type webAppSession struct {
	Demo       bool       `json:"demo"`
	Verified   bool       `json:"verified"`
	QueryID    string     `json:"query_id,omitempty"`
	StartParam string     `json:"start_param,omitempty"`
	AuthDate   int64      `json:"auth_date,omitempty"`
	User       webAppUser `json:"user"`
}

type webAppUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

func parseWebAppSession(raw, botToken string, maxAge time.Duration) (webAppSession, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return webAppSession{
			Demo: true,
			User: webAppUser{ID: 0, FirstName: "Browser", Username: "preview"},
		}, nil
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return webAppSession{}, err
	}
	if strings.TrimSpace(botToken) != "" {
		hash := values.Get("hash")
		if hash == "" || !validWebAppHash(values, botToken) {
			return webAppSession{}, errors.New("hash mismatch")
		}
	}
	var authDate int64
	if rawDate := values.Get("auth_date"); rawDate != "" {
		authDate, err = strconv.ParseInt(rawDate, 10, 64)
		if err != nil || authDate <= 0 {
			return webAppSession{}, errors.New("auth_date invalid")
		}
		if maxAge > 0 && time.Since(time.Unix(authDate, 0)) > maxAge {
			return webAppSession{}, errors.New("auth_date expired")
		}
	}
	var user webAppUser
	if rawUser := values.Get("user"); rawUser != "" {
		if err := json.Unmarshal([]byte(rawUser), &user); err != nil {
			return webAppSession{}, errors.New("user invalid")
		}
	}
	if user.ID <= 0 {
		return webAppSession{}, errors.New("user missing")
	}
	return webAppSession{
		Verified:   strings.TrimSpace(botToken) != "",
		QueryID:    values.Get("query_id"),
		StartParam: values.Get("start_param"),
		AuthDate:   authDate,
		User:       user,
	}, nil
}

func validWebAppHash(values url.Values, botToken string) bool {
	got := values.Get("hash")
	if got == "" {
		return false
	}
	want := webAppInitDataHash(values, botToken)
	return hmac.Equal([]byte(got), []byte(want))
}

func webAppInitDataHash(values url.Values, botToken string) string {
	check := webAppDataCheckString(values)
	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secretMAC.Write([]byte(botToken))
	secret := secretMAC.Sum(nil)
	dataMAC := hmac.New(sha256.New, secret)
	_, _ = dataMAC.Write([]byte(check))
	return hex.EncodeToString(dataMAC.Sum(nil))
}

func webAppDataCheckString(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, key := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(values.Get(key))
	}
	return b.String()
}

type walletStore struct {
	mu       sync.Mutex
	balances map[int64]int64
	txs      map[int64][]walletTx
	nextID   int64
}

type walletSnapshot struct {
	BalanceCents int64      `json:"balance_cents"`
	Currency     string     `json:"currency"`
	Recent       []walletTx `json:"recent"`
}

type walletTx struct {
	ID          int64  `json:"id"`
	To          string `json:"to"`
	AmountCents int64  `json:"amount_cents"`
	Memo        string `json:"memo"`
	CreatedAt   int64  `json:"created_at"`
}

func newWalletStore() *walletStore {
	return &walletStore{
		balances: map[int64]int64{},
		txs:      map[int64][]walletTx{},
	}
}

func (s *walletStore) snapshot(userID int64) walletSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensure(userID)
	return s.snapshotLocked(userID)
}

func (s *walletStore) transfer(userID int64, to string, amount int64, memo string) (walletTx, walletSnapshot, error) {
	to = strings.TrimSpace(to)
	memo = strings.TrimSpace(memo)
	if to == "" {
		return walletTx{}, walletSnapshot{}, errors.New("recipient is required")
	}
	if amount <= 0 || amount > maxTransferCents {
		return walletTx{}, walletSnapshot{}, errors.New("amount is out of demo bounds")
	}
	if len(memo) > maxMemoLen {
		return walletTx{}, walletSnapshot{}, errors.New("memo is too long")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensure(userID)
	if s.balances[userID] < amount {
		return walletTx{}, walletSnapshot{}, errors.New("insufficient balance")
	}
	s.nextID++
	tx := walletTx{ID: s.nextID, To: to, AmountCents: amount, Memo: memo, CreatedAt: time.Now().Unix()}
	s.balances[userID] -= amount
	s.txs[userID] = append([]walletTx{tx}, s.txs[userID]...)
	if len(s.txs[userID]) > 8 {
		s.txs[userID] = s.txs[userID][:8]
	}
	return tx, s.snapshotLocked(userID), nil
}

func (s *walletStore) ensure(userID int64) {
	if _, ok := s.balances[userID]; !ok {
		s.balances[userID] = defaultBalanceCents
	}
}

func (s *walletStore) snapshotLocked(userID int64) walletSnapshot {
	recent := append([]walletTx(nil), s.txs[userID]...)
	return walletSnapshot{BalanceCents: s.balances[userID], Currency: "TSC", Recent: recent}
}

type botAPIClient struct {
	base  string
	token string
	http  *http.Client
}

func newBotAPIClient(base, token string) *botAPIClient {
	return &botAPIClient{
		base:  strings.TrimRight(base, "/"),
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 8 * time.Second},
	}
}

func (c *botAPIClient) enabled() bool {
	return c != nil && c.base != "" && c.token != ""
}

func (c *botAPIClient) setChatMenuButton(ctx context.Context, text, webURL string) error {
	payload := map[string]any{
		"menu_button": map[string]any{
			"type": "web_app",
			"text": text,
			"web_app": map[string]any{
				"url": webURL,
			},
		},
	}
	var out botAPIResponse[bool]
	return c.post(ctx, "setChatMenuButton", payload, &out)
}

func (c *botAPIClient) answerWebAppQuery(ctx context.Context, queryID string, result map[string]any) (map[string]any, error) {
	payload := map[string]any{"web_app_query_id": queryID, "result": result}
	var out botAPIResponse[map[string]any]
	if err := c.post(ctx, "answerWebAppQuery", payload, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

func (c *botAPIClient) savePreparedInlineMessage(ctx context.Context, userID int64, result map[string]any) (map[string]any, error) {
	payload := map[string]any{
		"user_id":             userID,
		"result":              result,
		"allow_user_chats":    true,
		"allow_group_chats":   true,
		"allow_channel_chats": true,
	}
	var out botAPIResponse[map[string]any]
	if err := c.post(ctx, "savePreparedInlineMessage", payload, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

func (c *botAPIClient) post(ctx context.Context, method string, payload any, out any) error {
	if !c.enabled() {
		return errors.New("bot api disabled")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+url.PathEscape(c.token)+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s failed with HTTP %d", method, resp.StatusCode)
	}
	return nil
}

type botAPIResponse[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      T      `json:"result"`
}

func walletInlineResult(id, title, message, link string) map[string]any {
	result := map[string]any{
		"type":        "article",
		"id":          id,
		"title":       title,
		"description": "Telesrv wallet demo receipt",
		"input_message_content": map[string]any{
			"message_text":             message,
			"disable_web_page_preview": true,
		},
	}
	if link != "" {
		result["url"] = link
		result["reply_markup"] = map[string]any{
			"inline_keyboard": [][]map[string]any{{
				{"text": "Open Wallet", "url": link},
			}},
		}
	}
	return result
}

func receiptText(session webAppSession, payload shareRequest) string {
	name := session.User.FirstName
	if name == "" {
		name = "Wallet user"
	}
	memo := strings.TrimSpace(payload.Memo)
	if memo == "" {
		memo = "wallet demo transfer"
	}
	return fmt.Sprintf("%s shared a wallet receipt: %s TSC - %s", name, formatCents(payload.AmountCents), memo)
}

func decodeJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": code, "message": message})
}

func isHTTPSURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func formatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

const walletHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Telesrv Wallet</title>
  <script src="https://telegram.org/js/telegram-web-app.js"></script>
  <style>
    :root {
      color-scheme: light dark;
      --bg: #f5f7f9;
      --surface: #ffffff;
      --surface-2: #eef3f7;
      --text: #13202b;
      --muted: #637381;
      --line: #d8e0e7;
      --accent: #0f8b8d;
      --accent-2: #22356f;
      --danger: #bf3f3f;
      --ok: #237a57;
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --bg: #101418;
        --surface: #171e25;
        --surface-2: #202a33;
        --text: #edf2f7;
        --muted: #9aabba;
        --line: #2d3944;
      }
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-width: 320px;
      background: var(--bg);
      color: var(--text);
      font: 15px/1.45 system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      letter-spacing: 0;
    }
    main {
      width: min(760px, 100%);
      margin: 0 auto;
      padding: 18px;
    }
    header {
      display: flex;
      justify-content: space-between;
      gap: 16px;
      align-items: flex-start;
      padding: 14px 0 18px;
    }
    h1 { margin: 0; font-size: 24px; font-weight: 760; }
    .user { color: var(--muted); margin-top: 4px; }
    .pill {
      border: 1px solid var(--line);
      border-radius: 999px;
      padding: 6px 10px;
      color: var(--muted);
      background: var(--surface);
      white-space: nowrap;
    }
    .balance {
      background: var(--surface);
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 18px;
      margin-bottom: 14px;
    }
    .balance label, .field label { color: var(--muted); display: block; margin-bottom: 6px; }
    .amount { font-size: 40px; font-weight: 780; line-height: 1.05; }
    .grid {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 14px;
    }
    section {
      background: var(--surface);
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 16px;
    }
    h2 { margin: 0 0 14px; font-size: 16px; }
    .field { margin-bottom: 12px; }
    input, textarea {
      width: 100%;
      border: 1px solid var(--line);
      background: var(--surface-2);
      color: var(--text);
      border-radius: 7px;
      padding: 10px 11px;
      font: inherit;
    }
    textarea { min-height: 76px; resize: vertical; }
    .actions { display: flex; gap: 10px; flex-wrap: wrap; }
    button {
      border: 0;
      border-radius: 7px;
      background: var(--accent);
      color: white;
      font: inherit;
      font-weight: 680;
      min-height: 40px;
      padding: 10px 13px;
      cursor: pointer;
    }
    button.secondary { background: var(--accent-2); }
    button.neutral {
      background: var(--surface-2);
      color: var(--text);
      border: 1px solid var(--line);
    }
    button.danger { background: var(--danger); }
    button:disabled { opacity: .55; cursor: default; }
    .activity { list-style: none; padding: 0; margin: 0; display: grid; gap: 9px; }
    .activity li {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      padding: 10px 0;
      border-top: 1px solid var(--line);
    }
    .activity li:first-child { border-top: 0; }
    .muted { color: var(--muted); }
    .status { margin-top: 12px; min-height: 22px; color: var(--muted); }
    .status.ok { color: var(--ok); }
    .status.err { color: var(--danger); }
    @media (max-width: 680px) {
      main { padding: 14px; }
      header { align-items: stretch; flex-direction: column; }
      .grid { grid-template-columns: 1fr; }
      .amount { font-size: 34px; }
      .pill { width: fit-content; }
    }
  </style>
</head>
<body>
<main>
  <header>
    <div>
      <h1>Telesrv Wallet</h1>
      <div class="user" id="userLine">Loading session...</div>
    </div>
    <div class="pill" id="sessionPill">Mini App</div>
  </header>

  <div class="balance">
    <label>Available balance</label>
    <div class="amount" id="balance">0.00 TSC</div>
  </div>

  <div class="grid">
    <section>
      <h2>Transfer</h2>
      <div class="field">
        <label for="to">Recipient</label>
        <input id="to" autocomplete="off" value="@alice" maxlength="64">
      </div>
      <div class="field">
        <label for="amount">Amount</label>
        <input id="amount" type="number" min="0.01" step="0.01" value="12.50">
      </div>
      <div class="field">
        <label for="memo">Memo</label>
        <textarea id="memo" maxlength="180">Coffee settlement</textarea>
      </div>
      <div class="actions">
        <button id="sendBtn">Send</button>
        <button class="secondary" id="shareBtn">Share</button>
      </div>
      <div class="status" id="formStatus"></div>
    </section>

    <section>
      <h2>Recent activity</h2>
      <ul class="activity" id="activity"></ul>
      <div class="actions" style="margin-top:14px">
        <button class="neutral" id="receiptBtn">Send receipt</button>
        <button class="danger" id="payBtn">Pay invoice</button>
      </div>
      <div class="status" id="sideStatus"></div>
    </section>
  </div>
</main>

<script>
const tg = window.Telegram && window.Telegram.WebApp ? window.Telegram.WebApp : null;
if (tg) {
  tg.ready();
  tg.expand();
}
const initData = tg ? tg.initData : "";
let currentSession = null;
const apiURL = (path) => {
  const base = location.pathname.endsWith("/") ? location.href : location.href.replace(/[^/]*$/, "");
  return new URL(path.replace(/^\//, ""), base);
};
const money = (cents) => (Number(cents || 0) / 100).toFixed(2) + " TSC";
const status = (id, text, kind) => {
  const el = document.getElementById(id);
  el.textContent = text || "";
  el.className = "status " + (kind || "");
};
async function request(path, options) {
  const res = await fetch(apiURL(path), {
    ...options,
    headers: { "Content-Type": "application/json", ...(options && options.headers || {}) }
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok || body.ok === false) throw new Error(body.message || body.error || res.statusText);
  return body;
}
function amountCents() {
  return Math.round(Number(document.getElementById("amount").value || "0") * 100);
}
function currentPayload() {
  return {
    init_data: initData,
    to: document.getElementById("to").value,
    amount_cents: amountCents(),
    memo: document.getElementById("memo").value
  };
}
function render(data) {
  const session = data.session || currentSession || {};
  if (data.session) currentSession = data.session;
  const wallet = data.wallet || {};
  const user = session.user || {};
  document.getElementById("userLine").textContent = user.username ? "@" + user.username : (user.first_name || "Browser preview");
  document.getElementById("sessionPill").textContent = session.demo ? "Preview" : (session.verified ? "Verified" : "Unsigned");
  document.getElementById("balance").textContent = money(wallet.balance_cents);
  const activity = document.getElementById("activity");
  activity.innerHTML = "";
  const recent = wallet.recent || [];
  if (!recent.length) {
    const li = document.createElement("li");
    li.innerHTML = '<span class="muted">No transfers yet</span><span></span>';
    activity.appendChild(li);
    return;
  }
  for (const tx of recent) {
    const li = document.createElement("li");
    li.innerHTML = '<span><strong>' + escapeHTML(tx.to) + '</strong><br><span class="muted">' + escapeHTML(tx.memo || "") + '</span></span><span>-' + money(tx.amount_cents) + '</span>';
    activity.appendChild(li);
  }
}
function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
}
async function load() {
  const body = await request("api/session", { headers: { "X-Telegram-Init-Data": initData } });
  render(body);
}
document.getElementById("sendBtn").addEventListener("click", async () => {
  status("formStatus", "Sending...", "");
  try {
    const body = await request("api/transfer", { method: "POST", body: JSON.stringify(currentPayload()) });
    render(body);
    status("formStatus", "Transfer recorded.", "ok");
    if (tg && tg.HapticFeedback) tg.HapticFeedback.notificationOccurred("success");
  } catch (err) {
    status("formStatus", err.message, "err");
  }
});
document.getElementById("shareBtn").addEventListener("click", async () => {
  status("formStatus", "Preparing share...", "");
  try {
    const body = await request("api/share-prepared", { method: "POST", body: JSON.stringify(currentPayload()) });
    const prepared = body.prepared || {};
    if (tg && typeof tg.sendPreparedMessage === "function" && prepared.id) {
      tg.sendPreparedMessage(prepared.id);
      status("formStatus", "Share sheet opened.", "ok");
    } else {
      status("formStatus", "Prepared id: " + (prepared.id || "n/a"), "ok");
    }
  } catch (err) {
    status("formStatus", err.message, "err");
  }
});
document.getElementById("receiptBtn").addEventListener("click", async () => {
  status("sideStatus", "Sending receipt...", "");
  try {
    await request("api/answer-webapp-query", { method: "POST", body: JSON.stringify(currentPayload()) });
    status("sideStatus", "Receipt sent to the source chat.", "ok");
  } catch (err) {
    status("sideStatus", err.message, "err");
  }
});
document.getElementById("payBtn").addEventListener("click", async () => {
  status("sideStatus", "Opening invoice...", "");
  try {
    await request("api/payment-intent", { method: "POST", body: JSON.stringify(currentPayload()) });
  } catch (err) {
    status("sideStatus", err.message, "err");
  }
});
load().catch(err => status("formStatus", err.message, "err"));
</script>
</body>
</html>
`
