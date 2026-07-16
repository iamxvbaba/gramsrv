package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

//go:embed web/dist
var webDist embed.FS

type server struct {
	cfg       uiConfig
	read      *readStore
	web       fs.FS
	webServer http.Handler
}

func newServer(cfg uiConfig, read *readStore) (*server, error) {
	web, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		return nil, err
	}
	return &server{
		cfg:       cfg,
		read:      read,
		web:       web,
		webServer: http.FileServer(http.FS(web)),
	}, nil
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.handleAPILogin)
	mux.HandleFunc("POST /api/logout", s.handleAPILogout)
	mux.Handle("GET /api/session", s.requireAuthAPI(http.HandlerFunc(s.handleSession)))
	mux.Handle("GET /api/accounts", s.requireAuthAPI(http.HandlerFunc(s.handleAccountsAPI)))
	mux.Handle("GET /api/accounts/{id}", s.requireAuthAPI(http.HandlerFunc(s.handleAccountDetailAPI)))
	mux.Handle("GET /api/channels", s.requireAuthAPI(http.HandlerFunc(s.handleChannelsAPI)))
	mux.Handle("GET /api/channels/{id}", s.requireAuthAPI(http.HandlerFunc(s.handleChannelDetailAPI)))
	mux.Handle("GET /api/messages", s.requireAuthAPI(http.HandlerFunc(s.handleMessagesAPI)))
	mux.Handle("GET /api/messages/detail", s.requireAuthAPI(http.HandlerFunc(s.handleMessageDetailAPI)))
	mux.Handle("GET /api/messages/groups", s.requireAuthAPI(http.HandlerFunc(s.handleGroupMessagesAPI)))
	mux.Handle("GET /api/messages/groups/detail", s.requireAuthAPI(http.HandlerFunc(s.handleGroupMessageDetailAPI)))
	mux.Handle("GET /api/gifts", s.requireAuthAPI(http.HandlerFunc(s.handleStarGiftsAPI)))
	mux.Handle("GET /api/gifts/{id}/animation", s.requireAuthAPI(http.HandlerFunc(s.handleStarGiftAnimationAPI)))
	mux.Handle("GET /api/gifts/{id}/collectibles", s.requireAuthAPI(http.HandlerFunc(s.handleStarGiftCollectiblesAPI)))
	mux.Handle("GET /api/gifts/{id}/collectibles/{kind}/{attribute_id}/animation", s.requireAuthAPI(http.HandlerFunc(s.handleStarGiftCollectibleAnimationAPI)))
	mux.Handle("POST /api/actions/set-frozen", s.requireAuthAPI(http.HandlerFunc(s.handleSetAccountFrozenAPI)))
	mux.Handle("POST /api/actions/grant-premium", s.requireAuthAPI(http.HandlerFunc(s.handleGrantPremiumAPI)))
	mux.Handle("POST /api/actions/grant-stars", s.requireAuthAPI(http.HandlerFunc(s.handleGrantStarsAPI)))
	mux.Handle("POST /api/actions/set-verified", s.requireAuthAPI(http.HandlerFunc(s.handleSetVerifiedAPI)))
	mux.Handle("POST /api/actions/set-channel-verified", s.requireAuthAPI(http.HandlerFunc(s.handleSetChannelVerifiedAPI)))
	mux.Handle("POST /api/actions/revoke-sessions", s.requireAuthAPI(http.HandlerFunc(s.handleRevokeSessionsAPI)))
	mux.Handle("POST /api/actions/delete-messages", s.requireAuthAPI(http.HandlerFunc(s.handleDeleteMessagesAPI)))
	mux.Handle("POST /api/actions/delete-history", s.requireAuthAPI(http.HandlerFunc(s.handleDeleteHistoryAPI)))
	mux.Handle("POST /api/actions/import-gift", s.requireAuthAPI(http.HandlerFunc(s.handleImportStarGiftAPI)))
	mux.Handle("POST /api/actions/publish-gift-collectibles", s.requireAuthAPI(http.HandlerFunc(s.handlePublishStarGiftCollectiblesAPI)))
	mux.Handle("POST /api/actions/set-gift-enabled", s.requireAuthAPI(http.HandlerFunc(s.handleSetStarGiftEnabledAPI)))
	mux.Handle("POST /api/actions/set-gift-sort-order", s.requireAuthAPI(http.HandlerFunc(s.handleSetStarGiftSortOrderAPI)))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusNotFound, "api route not found")
	})
	mux.HandleFunc("/", s.handleApp)
	return mux
}

type actorKey struct{}

func (s *server) requireAuthAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		claims, ok := verifySession(s.cfg.SessionKey, cookie.Value, time.Now())
		if !ok {
			clearSessionCookie(w)
			writeAPIError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, claims.Actor)))
	})
}

func actorFromContext(ctx context.Context) string {
	if actor, ok := ctx.Value(actorKey{}).(string); ok && actor != "" {
		return actor
	}
	return "admin"
}

func (s *server) handleApp(w http.ResponseWriter, r *http.Request) {
	clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if clean != "." && clean != "" {
		if info, err := fs.Stat(s.web, clean); err == nil && !info.IsDir() {
			s.webServer.ServeHTTP(w, r)
			return
		}
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	s.webServer.ServeHTTP(w, r2)
}

type loginRequest struct {
	Secret string `json:"secret"`
}

func (s *server) handleAPILogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.validSecret(req.Secret) {
		writeAPIError(w, http.StatusUnauthorized, "invalid credential")
		return
	}
	value, err := signSession(s.cfg.SessionKey, sessionClaims{
		Actor: "admin",
		Exp:   time.Now().Add(12 * time.Hour).Unix(),
		Nonce: newCommandID("sess"),
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int((12 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"actor": "admin"})
}

func (s *server) validSecret(secret string) bool {
	if s.cfg.Password != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(s.cfg.Password)) == 1 {
		return true
	}
	if s.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(s.cfg.Token)) == 1 {
		return true
	}
	return false
}

func (s *server) handleAPILogout(w http.ResponseWriter, _ *http.Request) {
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"actor": actorFromContext(r.Context())})
}

func (s *server) handleStarGiftsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	rows, err := s.read.ListStarGifts(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"Gifts": rows})
}

func (s *server) handleStarGiftAnimationAPI(w http.ResponseWriter, r *http.Request) {
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || giftID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid gift id")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fmt.Sprintf("%s/v1/gifts/%d/animation", s.cfg.AdminAPIURL, giftID), nil)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminAPIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		writeAPIError(w, http.StatusBadGateway, "invalid animation response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeAPIError(w, resp.StatusCode, string(raw))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *server) handleStarGiftCollectiblesAPI(w http.ResponseWriter, r *http.Request) {
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || giftID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid gift id")
		return
	}
	s.proxyAdminJSON(w, r, fmt.Sprintf("/v1/gifts/%d/collectibles", giftID), 4<<20)
}

func (s *server) handleStarGiftCollectibleAnimationAPI(w http.ResponseWriter, r *http.Request) {
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	attributeID, attrErr := strconv.ParseInt(r.PathValue("attribute_id"), 10, 64)
	kind := r.PathValue("kind")
	if err != nil || giftID <= 0 || attrErr != nil || attributeID <= 0 || (kind != "model" && kind != "pattern") {
		writeAPIError(w, http.StatusBadRequest, "invalid collectible animation")
		return
	}
	s.proxyAdminJSON(w, r, fmt.Sprintf("/v1/gifts/%d/collectibles/%s/%d/animation", giftID, kind, attributeID), 4<<20)
}

func (s *server) proxyAdminJSON(w http.ResponseWriter, r *http.Request, apiPath string, maxBytes int64) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.AdminAPIURL+apiPath, nil)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminAPIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || int64(len(raw)) > maxBytes {
		writeAPIError(w, http.StatusBadGateway, "invalid admin api response")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeAPIError(w, resp.StatusCode, string(raw))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *server) handleAccountsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query().Get("q")
	beforeID, _ := parseInt64(r.URL.Query().Get("before_id"))
	beforeActiveUS, _ := parseInt64(r.URL.Query().Get("before_active_us"))
	limit, _ := parseInt(r.URL.Query().Get("limit"))
	rows := []AccountRow{}
	hasMore := false
	var err error
	if strings.TrimSpace(q) != "" {
		rows, err = s.read.SearchAccounts(r.Context(), q)
	} else {
		rows, hasMore, err = s.read.ListAccounts(r.Context(), beforeActiveUS, beforeID, limit)
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nextBeforeID := int64(0)
	nextBeforeActiveUS := int64(0)
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextBeforeID = last.ID
		nextBeforeActiveUS = last.LastActiveAt.UnixMicro()
	}
	if limit <= 0 {
		limit = accountListDefaultLimit
	}
	if limit > accountListMaxLimit {
		limit = accountListMaxLimit
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":                 q,
		"limit":                 limit,
		"rows":                  rows,
		"has_more":              hasMore,
		"next_before_id":        nextBeforeID,
		"next_before_active_us": nextBeforeActiveUS,
		"listing":               strings.TrimSpace(q) == "",
	})
}

func (s *server) handleAccountDetailAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	userID, err := parseInt64(r.PathValue("id"))
	if err != nil || userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid id")
		return
	}
	detail, err := s.read.AccountDetail(r.Context(), userID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *server) handleChannelsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query().Get("q")
	beforeID, _ := parseInt64(r.URL.Query().Get("before_id"))
	beforeUpdatedUS, _ := parseInt64(r.URL.Query().Get("before_updated_us"))
	limit, _ := parseInt(r.URL.Query().Get("limit"))
	rows := []ChannelRow{}
	hasMore := false
	var err error
	if strings.TrimSpace(q) != "" {
		rows, err = s.read.SearchChannels(r.Context(), q)
	} else {
		rows, hasMore, err = s.read.ListChannels(r.Context(), beforeUpdatedUS, beforeID, limit)
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nextBeforeID := int64(0)
	nextBeforeUpdatedUS := int64(0)
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextBeforeID = last.ID
		nextBeforeUpdatedUS = last.UpdatedAt.UnixMicro()
	}
	if limit <= 0 {
		limit = channelListDefaultLimit
	}
	if limit > channelListMaxLimit {
		limit = channelListMaxLimit
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":                  q,
		"limit":                  limit,
		"rows":                   rows,
		"has_more":               hasMore,
		"next_before_id":         nextBeforeID,
		"next_before_updated_us": nextBeforeUpdatedUS,
		"listing":                strings.TrimSpace(q) == "",
	})
}

func (s *server) handleChannelDetailAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	channelID, err := parseInt64(r.PathValue("id"))
	if err != nil || channelID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid id")
		return
	}
	detail, err := s.read.ChannelDetail(r.Context(), channelID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *server) handleMessagesAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query()
	owner, _ := parseInt64(q.Get("owner_user_id"))
	peer, _ := parseInt64(q.Get("peer_id"))
	beforeDate, _ := parseInt64(q.Get("before_date"))
	beforeID, _ := parseInt(q.Get("before_id"))
	limit, _ := parseInt(q.Get("limit"))
	rows := []MessageRow{}
	var err error
	if owner > 0 && peer > 0 {
		rows, err = s.read.ListMessages(r.Context(), owner, peer, beforeDate, beforeID, limit)
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"owner_user_id": owner,
		"peer_id":       peer,
		"before_date":   beforeDate,
		"before_id":     beforeID,
		"limit":         limit,
		"rows":          rows,
	})
}

func (s *server) handleMessageDetailAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	owner, err1 := parseInt64(r.URL.Query().Get("owner_user_id"))
	msgID, err2 := parseInt(r.URL.Query().Get("msg_id"))
	if err1 != nil || err2 != nil || owner <= 0 || msgID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid owner/msg_id")
		return
	}
	detail, err := s.read.MessageDetail(r.Context(), owner, msgID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *server) handleGroupMessagesAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query()
	channelID, _ := parseInt64(q.Get("channel_id"))
	beforeDate, _ := parseInt64(q.Get("before_date"))
	beforeID, _ := parseInt(q.Get("before_id"))
	limit, _ := parseInt(q.Get("limit"))
	rows := []GroupMessageRow{}
	var err error
	if channelID > 0 {
		rows, err = s.read.ListGroupMessages(r.Context(), channelID, beforeDate, beforeID, limit)
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if limit <= 0 || limit > messagePageLimit {
		limit = messagePageLimit
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channel_id":  channelID,
		"before_date": beforeDate,
		"before_id":   beforeID,
		"limit":       limit,
		"rows":        rows,
	})
}

func (s *server) handleGroupMessageDetailAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	channelID, err1 := parseInt64(r.URL.Query().Get("channel_id"))
	msgID, err2 := parseInt(r.URL.Query().Get("msg_id"))
	if err1 != nil || err2 != nil || channelID <= 0 || msgID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid channel_id/msg_id")
		return
	}
	detail, err := s.read.GroupMessageDetail(r.Context(), channelID, msgID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type setAccountFrozenAPIRequest struct {
	CommandID string    `json:"command_id"`
	Reason    string    `json:"reason"`
	Confirm   bool      `json:"confirm"`
	UserID    int64     `json:"user_id"`
	Frozen    bool      `json:"frozen"`
	Until     time.Time `json:"freeze_until"`
	AppealURL string    `json:"freeze_appeal_url"`
}

func (s *server) handleSetAccountFrozenAPI(w http.ResponseWriter, r *http.Request) {
	var body setAccountFrozenAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetAccountFrozenRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-frozen"),
		UserID:      body.UserID,
		Frozen:      body.Frozen,
		Until:       body.Until,
		AppealURL:   body.AppealURL,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/accounts/set-frozen", req)
	writeCommandResultAPI(w, result, err)
}

type grantPremiumAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	UserID    int64  `json:"user_id"`
	Months    int    `json:"months"`
}

func (s *server) handleGrantPremiumAPI(w http.ResponseWriter, r *http.Request) {
	var body grantPremiumAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.GrantPremiumRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "grant-premium"),
		UserID:      body.UserID,
		Months:      body.Months,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/accounts/grant-premium", req)
	writeCommandResultAPI(w, result, err)
}

type grantStarsAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	UserID    int64  `json:"user_id"`
	Amount    int64  `json:"amount"`
}

func (s *server) handleGrantStarsAPI(w http.ResponseWriter, r *http.Request) {
	var body grantStarsAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.GrantStarsRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "grant-stars"),
		UserID:      body.UserID,
		Amount:      body.Amount,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/accounts/grant-stars", req)
	writeCommandResultAPI(w, result, err)
}

type setVerifiedAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	UserID    int64  `json:"user_id"`
	Verified  bool   `json:"verified"`
}

func (s *server) handleSetVerifiedAPI(w http.ResponseWriter, r *http.Request) {
	var body setVerifiedAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetVerifiedRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-verified"),
		UserID:      body.UserID,
		Verified:    body.Verified,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/accounts/set-verified", req)
	writeCommandResultAPI(w, result, err)
}

type setChannelVerifiedAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	ChannelID int64  `json:"channel_id"`
	Verified  bool   `json:"verified"`
}

func (s *server) handleSetChannelVerifiedAPI(w http.ResponseWriter, r *http.Request) {
	var body setChannelVerifiedAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetChannelVerifiedRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-channel-verified"),
		ChannelID:   body.ChannelID,
		Verified:    body.Verified,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/channels/set-verified", req)
	writeCommandResultAPI(w, result, err)
}

type revokeSessionsAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	UserID    int64  `json:"user_id"`
	Hash      int64  `json:"hash"`
	KeepHash  int64  `json:"keep_hash"`
	RevokeAll bool   `json:"revoke_all"`
}

func (s *server) handleRevokeSessionsAPI(w http.ResponseWriter, r *http.Request) {
	var body revokeSessionsAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.RevokeSessionsRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "revoke-sessions"),
		UserID:      body.UserID,
		Hash:        body.Hash,
		KeepHash:    body.KeepHash,
		RevokeAll:   body.RevokeAll,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/accounts/revoke-sessions", req)
	writeCommandResultAPI(w, result, err)
}

type deleteMessagesAPIRequest struct {
	CommandID   string `json:"command_id"`
	Reason      string `json:"reason"`
	Confirm     bool   `json:"confirm"`
	OwnerUserID int64  `json:"owner_user_id"`
	PeerID      int64  `json:"peer_id"`
	IDs         []int  `json:"ids"`
	Revoke      bool   `json:"revoke"`
}

func (s *server) handleDeleteMessagesAPI(w http.ResponseWriter, r *http.Request) {
	var body deleteMessagesAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.DeletePrivateMessagesRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "delete-messages"),
		OwnerUserID: body.OwnerUserID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: body.PeerID},
		IDs:         body.IDs,
		Revoke:      body.Revoke,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/messages/delete", req)
	writeCommandResultAPI(w, result, err)
}

type deleteHistoryAPIRequest struct {
	CommandID   string `json:"command_id"`
	Reason      string `json:"reason"`
	Confirm     bool   `json:"confirm"`
	OwnerUserID int64  `json:"owner_user_id"`
	PeerID      int64  `json:"peer_id"`
	MaxID       int    `json:"max_id"`
	MinDate     int    `json:"min_date"`
	MaxDate     int    `json:"max_date"`
	MaxBatches  int    `json:"max_batches"`
	JustClear   bool   `json:"just_clear"`
	Revoke      bool   `json:"revoke"`
}

func (s *server) handleDeleteHistoryAPI(w http.ResponseWriter, r *http.Request) {
	var body deleteHistoryAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.DeletePrivateHistoryRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "delete-history"),
		OwnerUserID: body.OwnerUserID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: body.PeerID},
		MaxID:       body.MaxID,
		MinDate:     body.MinDate,
		MaxDate:     body.MaxDate,
		JustClear:   body.JustClear,
		Revoke:      body.Revoke,
		MaxBatches:  body.MaxBatches,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/messages/delete-history", req)
	writeCommandResultAPI(w, result, err)
}

type importStarGiftAPIRequest struct {
	CommandID    string `json:"command_id"`
	Reason       string `json:"reason"`
	Confirm      bool   `json:"confirm"`
	GiftID       int64  `json:"gift_id"`
	Title        string `json:"title"`
	Stars        int64  `json:"stars"`
	ConvertStars int64  `json:"convert_stars"`
	Enabled      bool   `json:"enabled"`
	SortOrder    int    `json:"sort_order"`
}

func (s *server) handleImportStarGiftAPI(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	var body importStarGiftAPIRequest
	dec := json.NewDecoder(strings.NewReader(r.FormValue("metadata")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid metadata: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "animation file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 4<<20 {
		writeAPIError(w, http.StatusBadRequest, "animation file is empty or too large")
		return
	}
	req := admin.ImportStarGiftRequest{
		CommandMeta:  s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "import-gift"),
		GiftID:       body.GiftID,
		Title:        body.Title,
		Stars:        body.Stars,
		ConvertStars: body.ConvertStars,
		Enabled:      body.Enabled,
		SortOrder:    body.SortOrder,
		FileName:     header.Filename,
	}
	result, err := s.callAdminMultipart(r.Context(), "/v1/gifts/import", req, header.Filename, data)
	writeCommandResultAPI(w, result, err)
}

type publishStarGiftCollectiblesAPIRequest struct {
	CommandID    string                                     `json:"command_id"`
	Reason       string                                     `json:"reason"`
	Confirm      bool                                       `json:"confirm"`
	UpgradeStars int64                                      `json:"upgrade_stars"`
	SupplyTotal  int                                        `json:"supply_total"`
	SlugPrefix   string                                     `json:"slug_prefix"`
	Models       []admin.StarGiftCollectibleAnimationUpload `json:"models"`
	Patterns     []admin.StarGiftCollectibleAnimationUpload `json:"patterns"`
	Backdrops    []admin.StarGiftCollectibleBackdropInput   `json:"backdrops"`
}

func (s *server) handlePublishStarGiftCollectiblesAPI(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	giftID, err := strconv.ParseInt(r.URL.Query().Get("gift_id"), 10, 64)
	if err != nil || giftID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid gift id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid collectible multipart form: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	var body publishStarGiftCollectiblesAPIRequest
	dec := json.NewDecoder(strings.NewReader(r.FormValue("metadata")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid metadata: "+err.Error())
		return
	}
	if len(body.Models)+len(body.Patterns) > 128 {
		writeAPIError(w, http.StatusBadRequest, "too many collectible animation files")
		return
	}
	seen := make(map[string]struct{}, len(body.Models)+len(body.Patterns))
	load := func(upload *admin.StarGiftCollectibleAnimationUpload) error {
		upload.FileKey = strings.TrimSpace(upload.FileKey)
		if upload.FileKey == "" {
			return errors.New("animation file key is required")
		}
		if _, ok := seen[upload.FileKey]; ok {
			return fmt.Errorf("duplicate animation file key %q", upload.FileKey)
		}
		seen[upload.FileKey] = struct{}{}
		file, header, err := r.FormFile(upload.FileKey)
		if err != nil {
			return fmt.Errorf("animation file %q is required", upload.FileKey)
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		if err != nil || len(data) == 0 || len(data) > 4<<20 {
			return fmt.Errorf("animation file %q is empty or too large", upload.FileKey)
		}
		upload.FileName = header.Filename
		upload.Data = data
		return nil
	}
	for i := range body.Models {
		if err := load(&body.Models[i]); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	for i := range body.Patterns {
		if err := load(&body.Patterns[i]); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	req := admin.PublishStarGiftCollectiblesRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "publish-gift-collectibles"),
		GiftID:      giftID, UpgradeStars: body.UpgradeStars, SupplyTotal: body.SupplyTotal,
		SlugPrefix: body.SlugPrefix, Models: body.Models, Patterns: body.Patterns, Backdrops: body.Backdrops,
	}
	result, err := s.callAdminCollectibleMultipart(r.Context(), fmt.Sprintf("/v1/gifts/%d/collectibles/publish", giftID), req)
	writeCommandResultAPI(w, result, err)
}

type setStarGiftEnabledAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	GiftID    int64  `json:"gift_id"`
	Enabled   bool   `json:"enabled"`
}

func (s *server) handleSetStarGiftEnabledAPI(w http.ResponseWriter, r *http.Request) {
	var body setStarGiftEnabledAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetStarGiftEnabledRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-gift-enabled"),
		GiftID:      body.GiftID, Enabled: body.Enabled,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/gifts/set-enabled", req)
	writeCommandResultAPI(w, result, err)
}

type setStarGiftSortOrderAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	GiftID    int64  `json:"gift_id"`
	SortOrder int    `json:"sort_order"`
}

func (s *server) handleSetStarGiftSortOrderAPI(w http.ResponseWriter, r *http.Request) {
	var body setStarGiftSortOrderAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetStarGiftSortOrderRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-gift-sort-order"),
		GiftID:      body.GiftID, SortOrder: body.SortOrder,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/gifts/set-sort-order", req)
	writeCommandResultAPI(w, result, err)
}

func (s *server) commandMetaFromAPI(r *http.Request, commandID, reason string, confirm bool, prefix string) admin.CommandMeta {
	commandID = strings.TrimSpace(commandID)
	if confirm && strings.HasPrefix(commandID, "dry-") {
		commandID = ""
	}
	dryRun := !confirm
	if commandID == "" {
		scope := "dry"
		if !dryRun {
			scope = "exec"
		}
		commandID = newCommandID(scope + "-" + prefix)
	}
	return admin.CommandMeta{
		CommandID: commandID,
		Actor:     actorFromContext(r.Context()),
		Reason:    reason,
		DryRun:    dryRun,
	}
}

func (s *server) callAdminAPI(ctx context.Context, apiPath string, payload any) (admin.CommandResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return admin.CommandResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.AdminAPIURL+apiPath, bytes.NewReader(body))
	if err != nil {
		return admin.CommandResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminAPIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return admin.CommandResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result admin.CommandResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("admin api %s: status=%d body=%s", apiPath, resp.StatusCode, string(raw))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if result.Error == "" {
			result.Error = resp.Status
		}
		return result, errors.New(result.Error)
	}
	return result, nil
}

func (s *server) callAdminMultipart(ctx context.Context, apiPath string, metadata any, fileName string, data []byte) (admin.CommandResult, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	meta, err := json.Marshal(metadata)
	if err != nil {
		return admin.CommandResult{}, err
	}
	if err := writer.WriteField("metadata", string(meta)); err != nil {
		return admin.CommandResult{}, err
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return admin.CommandResult{}, err
	}
	if _, err := part.Write(data); err != nil {
		return admin.CommandResult{}, err
	}
	if err := writer.Close(); err != nil {
		return admin.CommandResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.AdminAPIURL+apiPath, &body)
	if err != nil {
		return admin.CommandResult{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminAPIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return admin.CommandResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result admin.CommandResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("admin api %s: status=%d body=%s", apiPath, resp.StatusCode, string(raw))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if result.Error == "" {
			result.Error = resp.Status
		}
		return result, errors.New(result.Error)
	}
	return result, nil
}

func (s *server) callAdminCollectibleMultipart(ctx context.Context, apiPath string, payload admin.PublishStarGiftCollectiblesRequest) (admin.CommandResult, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	meta, err := json.Marshal(payload)
	if err != nil {
		return admin.CommandResult{}, err
	}
	if err := writer.WriteField("metadata", string(meta)); err != nil {
		return admin.CommandResult{}, err
	}
	writeUploads := func(uploads []admin.StarGiftCollectibleAnimationUpload) error {
		for _, upload := range uploads {
			part, err := writer.CreateFormFile(upload.FileKey, upload.FileName)
			if err != nil {
				return err
			}
			if _, err := part.Write(upload.Data); err != nil {
				return err
			}
		}
		return nil
	}
	if err := writeUploads(payload.Models); err != nil {
		return admin.CommandResult{}, err
	}
	if err := writeUploads(payload.Patterns); err != nil {
		return admin.CommandResult{}, err
	}
	if err := writer.Close(); err != nil {
		return admin.CommandResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.AdminAPIURL+apiPath, &body)
	if err != nil {
		return admin.CommandResult{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminAPIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return admin.CommandResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result admin.CommandResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("admin api %s: status=%d body=%s", apiPath, resp.StatusCode, string(raw))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if result.Error == "" {
			result.Error = resp.Status
		}
		return result, errors.New(result.Error)
	}
	return result, nil
}

func decodeAction(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := decodeJSON(r, dst); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func writeCommandResultAPI(w http.ResponseWriter, result admin.CommandResult, err error) {
	if err != nil {
		if result.Status == "" {
			result.Status = "failed"
		}
		if result.Message == "" {
			result.Message = "command failed"
		}
		if result.Error == "" {
			result.Error = err.Error()
		}
		writeJSON(w, http.StatusBadGateway, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func parseInt64(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	return strconv.ParseInt(v, 10, 64)
}

func parseInt(v string) (int, error) {
	n, err := parseInt64(v)
	return int(n), err
}

func boolValue(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func displayPhone(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "+") {
		return v
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return v
		}
	}
	return "+" + v
}

func channelKind(ch ChannelRow) string {
	if ch.Broadcast && !ch.Megagroup {
		return "频道"
	}
	if ch.Megagroup {
		if ch.Forum {
			return "超级群/论坛"
		}
		return "超级群"
	}
	return "频道/群"
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
