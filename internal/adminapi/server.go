package adminapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

type Config struct {
	Addr  string
	Token string
}

type Service interface {
	SetAccountFrozen(ctx context.Context, req admin.SetAccountFrozenRequest) (admin.CommandResult, error)
	GrantPremium(ctx context.Context, req admin.GrantPremiumRequest) (admin.CommandResult, error)
	GrantStars(ctx context.Context, req admin.GrantStarsRequest) (admin.CommandResult, error)
	SetVerified(ctx context.Context, req admin.SetVerifiedRequest) (admin.CommandResult, error)
	SetChannelVerified(ctx context.Context, req admin.SetChannelVerifiedRequest) (admin.CommandResult, error)
	RevokeSessions(ctx context.Context, req admin.RevokeSessionsRequest) (admin.CommandResult, error)
	DeletePrivateMessages(ctx context.Context, req admin.DeletePrivateMessagesRequest) (admin.CommandResult, error)
	DeletePrivateHistory(ctx context.Context, req admin.DeletePrivateHistoryRequest) (admin.CommandResult, error)
	ImportStarGift(ctx context.Context, req admin.ImportStarGiftRequest) (admin.CommandResult, error)
	PublishStarGiftCollectibles(ctx context.Context, req admin.PublishStarGiftCollectiblesRequest) (admin.CommandResult, error)
	SetStarGiftEnabled(ctx context.Context, req admin.SetStarGiftEnabledRequest) (admin.CommandResult, error)
	SetStarGiftSortOrder(ctx context.Context, req admin.SetStarGiftSortOrderRequest) (admin.CommandResult, error)
	StarGiftAnimation(ctx context.Context, giftID int64) ([]byte, bool, error)
	StarGiftCollectibles(ctx context.Context, giftID int64) (domain.StarGiftUpgradePreview, bool, error)
	StarGiftCollectibleAnimation(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error)
}

func Start(ctx context.Context, cfg Config, svc Service, log *zap.Logger) (*http.Server, error) {
	cfg.Addr = strings.TrimSpace(cfg.Addr)
	if cfg.Addr == "" {
		return nil, nil
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("TELESRV_ADMIN_API_TOKEN is required when TELESRV_ADMIN_API_ADDR is set")
	}
	if svc == nil {
		return nil, fmt.Errorf("admin api service is nil")
	}
	if log == nil {
		log = zap.NewNop()
	}
	server := &Server{token: cfg.Token, svc: svc, log: log}
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("Admin API 已启用", zap.String("addr", cfg.Addr))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Warn("Admin API 退出", zap.Error(err))
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	return httpServer, nil
}

type Server struct {
	token string
	svc   Service
	log   *zap.Logger
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/accounts/set-frozen", s.authenticated(s.handleSetAccountFrozen))
	mux.HandleFunc("POST /v1/accounts/grant-premium", s.authenticated(s.handleGrantPremium))
	mux.HandleFunc("POST /v1/accounts/grant-stars", s.authenticated(s.handleGrantStars))
	mux.HandleFunc("POST /v1/accounts/set-verified", s.authenticated(s.handleSetVerified))
	mux.HandleFunc("POST /v1/accounts/revoke-sessions", s.authenticated(s.handleRevokeSessions))
	mux.HandleFunc("POST /v1/channels/set-verified", s.authenticated(s.handleSetChannelVerified))
	mux.HandleFunc("POST /v1/messages/delete", s.authenticated(s.handleDeleteMessages))
	mux.HandleFunc("POST /v1/messages/delete-history", s.authenticated(s.handleDeleteHistory))
	mux.HandleFunc("POST /v1/gifts/import", s.authenticated(s.handleImportStarGift))
	mux.HandleFunc("POST /v1/gifts/{id}/collectibles/publish", s.authenticated(s.handlePublishStarGiftCollectibles))
	mux.HandleFunc("POST /v1/gifts/set-enabled", s.authenticated(s.handleSetStarGiftEnabled))
	mux.HandleFunc("POST /v1/gifts/set-sort-order", s.authenticated(s.handleSetStarGiftSortOrder))
	mux.HandleFunc("GET /v1/gifts/{id}/animation", s.authenticated(s.handleStarGiftAnimation))
	mux.HandleFunc("GET /v1/gifts/{id}/collectibles", s.authenticated(s.handleStarGiftCollectibles))
	mux.HandleFunc("GET /v1/gifts/{id}/collectibles/{kind}/{attribute_id}/animation", s.authenticated(s.handleStarGiftCollectibleAnimation))
	return mux
}

func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleSetAccountFrozen(w http.ResponseWriter, r *http.Request) {
	var req admin.SetAccountFrozenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetAccountFrozen(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleGrantPremium(w http.ResponseWriter, r *http.Request) {
	var req admin.GrantPremiumRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.GrantPremium(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleGrantStars(w http.ResponseWriter, r *http.Request) {
	var req admin.GrantStarsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.GrantStars(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetVerified(w http.ResponseWriter, r *http.Request) {
	var req admin.SetVerifiedRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetVerified(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetChannelVerified(w http.ResponseWriter, r *http.Request) {
	var req admin.SetChannelVerifiedRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetChannelVerified(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	var req admin.RevokeSessionsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.RevokeSessions(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleDeleteMessages(w http.ResponseWriter, r *http.Request) {
	var req admin.DeletePrivateMessagesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.DeletePrivateMessages(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleDeleteHistory(w http.ResponseWriter, r *http.Request) {
	var req admin.DeletePrivateHistoryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.DeletePrivateHistory(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleImportStarGift(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	var req admin.ImportStarGiftRequest
	dec := json.NewDecoder(strings.NewReader(r.FormValue("metadata")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid metadata: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "animation file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 4<<20 {
		writeError(w, http.StatusBadRequest, "animation file is empty or too large")
		return
	}
	req.FileName = header.Filename
	req.Data = data
	result, err := s.svc.ImportStarGift(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handlePublishStarGiftCollectibles(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || giftID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid gift id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid collectible multipart form: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	var req admin.PublishStarGiftCollectiblesRequest
	dec := json.NewDecoder(strings.NewReader(r.FormValue("metadata")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid metadata: "+err.Error())
		return
	}
	req.GiftID = giftID
	seen := make(map[string]struct{}, len(req.Models)+len(req.Patterns))
	if len(req.Models)+len(req.Patterns) > 128 {
		writeError(w, http.StatusBadRequest, "too many collectible animation files")
		return
	}
	load := func(upload *admin.StarGiftCollectibleAnimationUpload) error {
		upload.FileKey = strings.TrimSpace(upload.FileKey)
		if upload.FileKey == "" {
			return fmt.Errorf("animation file key is required")
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
	for i := range req.Models {
		if err := load(&req.Models[i]); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	for i := range req.Patterns {
		if err := load(&req.Patterns[i]); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	result, err := s.svc.PublishStarGiftCollectibles(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetStarGiftEnabled(w http.ResponseWriter, r *http.Request) {
	var req admin.SetStarGiftEnabledRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetStarGiftEnabled(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetStarGiftSortOrder(w http.ResponseWriter, r *http.Request) {
	var req admin.SetStarGiftSortOrderRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetStarGiftSortOrder(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleStarGiftAnimation(w http.ResponseWriter, r *http.Request) {
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || giftID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid gift id")
		return
	}
	raw, found, err := s.svc.StarGiftAnimation(r.Context(), giftID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "gift animation not found")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) handleStarGiftCollectibles(w http.ResponseWriter, r *http.Request) {
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || giftID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid gift id")
		return
	}
	preview, found, err := s.svc.StarGiftCollectibles(r.Context(), giftID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"found": false, "gift_id": giftID})
		return
	}
	writeJSON(w, http.StatusOK, collectiblePreviewResponse(preview))
}

func collectiblePreviewResponse(preview domain.StarGiftUpgradePreview) map[string]any {
	attribute := func(value domain.StarGiftCollectibleAttribute) map[string]any {
		result := map[string]any{
			"id": value.ID, "name": value.Name, "rarity_permille": value.RarityPermille,
			"sort_order": value.SortOrder, "kind": value.Kind,
		}
		if value.Animation != nil {
			result["source_name"] = value.Animation.SourceName
			result["source_format"] = value.Animation.SourceFormat
		}
		if value.Kind == domain.StarGiftCollectibleBackdrop {
			result["backdrop_id"] = value.BackdropID
			result["center_color"] = value.CenterColor
			result["edge_color"] = value.EdgeColor
			result["pattern_color"] = value.PatternColor
			result["text_color"] = value.TextColor
		}
		return result
	}
	mapAttributes := func(values []domain.StarGiftCollectibleAttribute) []map[string]any {
		result := make([]map[string]any, 0, len(values))
		for _, value := range values {
			result = append(result, attribute(value))
		}
		return result
	}
	return map[string]any{
		"found": true, "gift_id": preview.GiftID, "revision": preview.Revision, "upgrade_stars": preview.UpgradeStars,
		"supply_total": preview.SupplyTotal, "issued": preview.Issued,
		"slug_prefix": preview.SlugPrefix,
		"models":      mapAttributes(preview.Models), "patterns": mapAttributes(preview.Patterns),
		"backdrops": mapAttributes(preview.Backdrops),
	}
}

func (s *Server) handleStarGiftCollectibleAnimation(w http.ResponseWriter, r *http.Request) {
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	attributeID, attrErr := strconv.ParseInt(r.PathValue("attribute_id"), 10, 64)
	kind := domain.StarGiftCollectibleAttributeKind(r.PathValue("kind"))
	if err != nil || giftID <= 0 || attrErr != nil || attributeID <= 0 ||
		(kind != domain.StarGiftCollectibleModel && kind != domain.StarGiftCollectiblePattern) {
		writeError(w, http.StatusBadRequest, "invalid collectible animation")
		return
	}
	raw, found, err := s.svc.StarGiftCollectibleAnimation(r.Context(), giftID, kind, attributeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "collectible animation not found")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return false
	}
	return true
}

func writeCommandResult(w http.ResponseWriter, result admin.CommandResult, err error) {
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadRequest
		if result.CommandID == "" {
			result = admin.CommandResult{Status: "failed", Message: "command failed", Error: err.Error()}
		}
	}
	writeJSON(w, status, result)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
