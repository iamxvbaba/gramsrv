package adminapi

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

	"telesrv/internal/admin"
	"telesrv/internal/domain"
	"telesrv/internal/officialgifts"
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
	SetUserFlags(ctx context.Context, req admin.SetUserFlagsRequest) (admin.CommandResult, error)
	SetChannelVerified(ctx context.Context, req admin.SetChannelVerifiedRequest) (admin.CommandResult, error)
	SetChannelFlags(ctx context.Context, req admin.SetChannelFlagsRequest) (admin.CommandResult, error)
	CreateBot(ctx context.Context, req admin.CreateBotRequest) (admin.CommandResult, error)
	DeleteBot(ctx context.Context, req admin.DeleteBotRequest) (admin.CommandResult, error)
	SetSupport(ctx context.Context, req admin.SetSupportRequest) (admin.CommandResult, error)
	SetUsername(ctx context.Context, req admin.SetUsernameRequest) (admin.CommandResult, error)
	SetUserColor(ctx context.Context, req admin.SetUserColorRequest) (admin.CommandResult, error)
	SetUserEmojiStatus(ctx context.Context, req admin.SetUserEmojiStatusRequest) (admin.CommandResult, error)
	SetChannelSettings(ctx context.Context, req admin.SetChannelSettingsRequest) (admin.CommandResult, error)
	SetChannelUsername(ctx context.Context, req admin.SetChannelUsernameRequest) (admin.CommandResult, error)
	SetChannelColor(ctx context.Context, req admin.SetChannelColorRequest) (admin.CommandResult, error)
	SetChannelEmojiStatus(ctx context.Context, req admin.SetChannelEmojiStatusRequest) (admin.CommandResult, error)
	RevokeSessions(ctx context.Context, req admin.RevokeSessionsRequest) (admin.CommandResult, error)
	DeletePrivateMessages(ctx context.Context, req admin.DeletePrivateMessagesRequest) (admin.CommandResult, error)
	DeletePrivateHistory(ctx context.Context, req admin.DeletePrivateHistoryRequest) (admin.CommandResult, error)
	ImportStarGift(ctx context.Context, req admin.ImportStarGiftRequest) (admin.CommandResult, error)
	ImportOfficialStarGift(ctx context.Context, req admin.ImportOfficialStarGiftRequest) (admin.CommandResult, error)
	OfficialStarGifts(ctx context.Context) ([]officialgifts.GiftSummary, error)
	OfficialStarGiftAnimation(ctx context.Context, sourceGiftID string) ([]byte, bool, error)
	PublishStarGiftCollectibles(ctx context.Context, req admin.PublishStarGiftCollectiblesRequest) (admin.CommandResult, error)
	SetStarGiftEnabled(ctx context.Context, req admin.SetStarGiftEnabledRequest) (admin.CommandResult, error)
	SetStarGiftSortOrder(ctx context.Context, req admin.SetStarGiftSortOrderRequest) (admin.CommandResult, error)
	GiveGift(ctx context.Context, req admin.GiveGiftRequest) (admin.CommandResult, error)
	StarGiftAnimation(ctx context.Context, giftID int64) ([]byte, bool, error)
	EmojiAnimation(ctx context.Context, documentID int64) ([]byte, bool, error)
	StarGiftCollectibles(ctx context.Context, giftID int64) (domain.StarGiftUpgradePreview, bool, error)
	StarGiftCollectibleAnimation(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error)
	ModerationCases(ctx context.Context, filter domain.ModerationCaseFilter) ([]domain.ModerationCase, error)
	ModerationCase(ctx context.Context, caseID int64) (domain.ModerationCaseDetail, bool, error)
	ModerationReport(ctx context.Context, reportID int64) (domain.ModerationReport, bool, error)
	ClaimModerationCase(ctx context.Context, caseID, expectedVersion int64, actor string) (domain.ModerationCase, error)
	DecideModerationCase(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error)
	SubmitModerationAppeal(ctx context.Context, caseID, appellantUserID int64, text string) (domain.ModerationAppeal, bool, error)
	ReviewModerationAppeal(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error)
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
	mux.HandleFunc("POST /v1/accounts/set-flags", s.authenticated(s.handleSetUserFlags))
	mux.HandleFunc("POST /v1/accounts/set-support", s.authenticated(s.handleSetSupport))
	mux.HandleFunc("POST /v1/accounts/set-username", s.authenticated(s.handleSetUsername))
	mux.HandleFunc("POST /v1/accounts/set-color", s.authenticated(s.handleSetUserColor))
	mux.HandleFunc("POST /v1/accounts/set-emoji-status", s.authenticated(s.handleSetUserEmojiStatus))
	mux.HandleFunc("POST /v1/accounts/revoke-sessions", s.authenticated(s.handleRevokeSessions))
	mux.HandleFunc("POST /v1/channels/set-verified", s.authenticated(s.handleSetChannelVerified))
	mux.HandleFunc("POST /v1/channels/set-flags", s.authenticated(s.handleSetChannelFlags))
	mux.HandleFunc("POST /v1/channels/set-settings", s.authenticated(s.handleSetChannelSettings))
	mux.HandleFunc("POST /v1/channels/set-username", s.authenticated(s.handleSetChannelUsername))
	mux.HandleFunc("POST /v1/channels/set-color", s.authenticated(s.handleSetChannelColor))
	mux.HandleFunc("POST /v1/channels/set-emoji-status", s.authenticated(s.handleSetChannelEmojiStatus))
	mux.HandleFunc("POST /v1/bots/create", s.authenticated(s.handleCreateBot))
	mux.HandleFunc("POST /v1/bots/delete", s.authenticated(s.handleDeleteBot))
	mux.HandleFunc("POST /v1/messages/delete", s.authenticated(s.handleDeleteMessages))
	mux.HandleFunc("POST /v1/messages/delete-history", s.authenticated(s.handleDeleteHistory))
	mux.HandleFunc("POST /v1/gifts/import", s.authenticated(s.handleImportStarGift))
	mux.HandleFunc("GET /v1/official-gifts", s.authenticated(s.handleOfficialStarGifts))
	mux.HandleFunc("GET /v1/official-gifts/{id}/animation", s.authenticated(s.handleOfficialStarGiftAnimation))
	mux.HandleFunc("POST /v1/official-gifts/import", s.authenticated(s.handleImportOfficialStarGift))
	mux.HandleFunc("POST /v1/gifts/{id}/collectibles/publish", s.authenticated(s.handlePublishStarGiftCollectibles))
	mux.HandleFunc("POST /v1/gifts/set-enabled", s.authenticated(s.handleSetStarGiftEnabled))
	mux.HandleFunc("POST /v1/gifts/set-sort-order", s.authenticated(s.handleSetStarGiftSortOrder))
	mux.HandleFunc("POST /v1/gifts/give", s.authenticated(s.handleGiveGift))
	mux.HandleFunc("GET /v1/gifts/{id}/animation", s.authenticated(s.handleStarGiftAnimation))
	mux.HandleFunc("GET /v1/emoji/{id}/animation", s.authenticated(s.handleEmojiAnimation))
	mux.HandleFunc("GET /v1/gifts/{id}/collectibles", s.authenticated(s.handleStarGiftCollectibles))
	mux.HandleFunc("GET /v1/gifts/{id}/collectibles/{kind}/{attribute_id}/animation", s.authenticated(s.handleStarGiftCollectibleAnimation))
	mux.HandleFunc("GET /v1/moderation/cases", s.authenticated(s.handleModerationCases))
	mux.HandleFunc("GET /v1/moderation/cases/{id}", s.authenticated(s.handleModerationCase))
	mux.HandleFunc("GET /v1/moderation/reports/{id}", s.authenticated(s.handleModerationReport))
	mux.HandleFunc("POST /v1/moderation/cases/{id}/claim", s.authenticated(s.handleClaimModerationCase))
	mux.HandleFunc("POST /v1/moderation/cases/{id}/decide", s.authenticated(s.handleDecideModerationCase))
	mux.HandleFunc("POST /v1/moderation/cases/{id}/appeals", s.authenticated(s.handleSubmitModerationAppeal))
	mux.HandleFunc("POST /v1/moderation/cases/{id}/appeals/{appeal_id}/review", s.authenticated(s.handleReviewModerationAppeal))
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

func (s *Server) handleSetUserFlags(w http.ResponseWriter, r *http.Request) {
	var req admin.SetUserFlagsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetUserFlags(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetChannelFlags(w http.ResponseWriter, r *http.Request) {
	var req admin.SetChannelFlagsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetChannelFlags(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetSupport(w http.ResponseWriter, r *http.Request) {
	var req admin.SetSupportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetSupport(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetUsername(w http.ResponseWriter, r *http.Request) {
	var req admin.SetUsernameRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetUsername(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetUserColor(w http.ResponseWriter, r *http.Request) {
	var req admin.SetUserColorRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetUserColor(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetUserEmojiStatus(w http.ResponseWriter, r *http.Request) {
	var req admin.SetUserEmojiStatusRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetUserEmojiStatus(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetChannelSettings(w http.ResponseWriter, r *http.Request) {
	var req admin.SetChannelSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetChannelSettings(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetChannelUsername(w http.ResponseWriter, r *http.Request) {
	var req admin.SetChannelUsernameRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetChannelUsername(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetChannelColor(w http.ResponseWriter, r *http.Request) {
	var req admin.SetChannelColorRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetChannelColor(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetChannelEmojiStatus(w http.ResponseWriter, r *http.Request) {
	var req admin.SetChannelEmojiStatusRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetChannelEmojiStatus(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleCreateBot(w http.ResponseWriter, r *http.Request) {
	var req admin.CreateBotRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.CreateBot(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleDeleteBot(w http.ResponseWriter, r *http.Request) {
	var req admin.DeleteBotRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.DeleteBot(r.Context(), req)
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

func (s *Server) handleOfficialStarGifts(w http.ResponseWriter, r *http.Request) {
	items, err := s.svc.OfficialStarGifts(r.Context())
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, officialgifts.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err.Error())
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, officialStarGiftListItem(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"gifts": result})
}

func officialStarGiftListItem(item officialgifts.GiftSummary) map[string]any {
	return map[string]any{
		"source_gift_id": strconv.FormatInt(item.ID, 10), "title": item.Title,
		"stars": strconv.FormatInt(item.Stars, 10), "convert_stars": strconv.FormatInt(item.ConvertStars, 10),
		"upgrade_stars":      strconv.FormatInt(item.UpgradeStars, 10),
		"availability_total": item.AvailabilityTotal, "limited": item.Limited, "sold_out": item.SoldOut,
		"model_count": item.ModelCount, "pattern_count": item.PatternCount, "backdrop_count": item.BackdropCount,
		"crafted_model_count": item.CraftedModelCount, "can_upgrade": item.CanUpgrade(), "can_craft": item.CanCraft(),
		"document_id": strconv.FormatInt(item.DocumentID, 10), "animation_validated": item.AnimationValidated,
	}
}

func (s *Server) handleOfficialStarGiftAnimation(w http.ResponseWriter, r *http.Request) {
	raw, found, err := s.svc.OfficialStarGiftAnimation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "official gift animation not found")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) handleImportOfficialStarGift(w http.ResponseWriter, r *http.Request) {
	var req admin.ImportOfficialStarGiftRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.ImportOfficialStarGift(r.Context(), req)
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

func (s *Server) handleGiveGift(w http.ResponseWriter, r *http.Request) {
	var req admin.GiveGiftRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.GiveGift(r.Context(), req)
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

func (s *Server) handleEmojiAnimation(w http.ResponseWriter, r *http.Request) {
	documentID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || documentID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid document id")
		return
	}
	raw, found, err := s.svc.EmojiAnimation(r.Context(), documentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "emoji animation not found")
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
		writeJSON(w, http.StatusOK, map[string]any{"found": false, "gift_id": strconv.FormatInt(giftID, 10)})
		return
	}
	writeJSON(w, http.StatusOK, collectiblePreviewResponse(preview))
}

func collectiblePreviewResponse(preview domain.StarGiftUpgradePreview) map[string]any {
	attribute := func(value domain.StarGiftCollectibleAttribute) map[string]any {
		result := map[string]any{
			"id": strconv.FormatInt(value.ID, 10), "name": value.Name, "rarity_kind": value.RarityKind,
			"rarity_permille": value.RarityPermille, "crafted": value.Crafted,
			"official_document_id": strconv.FormatInt(value.OfficialDocumentID, 10),
			"sort_order":           value.SortOrder, "kind": value.Kind,
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
		"found": true, "gift_id": strconv.FormatInt(preview.GiftID, 10), "revision": preview.Revision,
		"upgrade_stars": strconv.FormatInt(preview.UpgradeStars, 10),
		"supply_total":  preview.SupplyTotal, "issued": preview.Issued,
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

type moderationClaimRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Actor           string `json:"actor"`
}

type moderationActionRequest struct {
	Kind    domain.ModerationActionKind `json:"kind"`
	Payload json.RawMessage             `json:"payload"`
}

type moderationDecisionRequest struct {
	ExpectedVersion int64                         `json:"expected_version"`
	Actor           string                        `json:"actor"`
	Reason          string                        `json:"reason"`
	CommandID       string                        `json:"command_id"`
	Kind            domain.ModerationDecisionKind `json:"kind"`
	Actions         []moderationActionRequest     `json:"actions"`
}

type moderationAppealRequest struct {
	AppellantUserID int64  `json:"appellant_user_id"`
	Text            string `json:"text"`
}

type moderationAppealReviewRequest struct {
	ExpectedVersion int64                     `json:"expected_version"`
	Actor           string                    `json:"actor"`
	Reason          string                    `json:"reason"`
	CommandID       string                    `json:"command_id"`
	Granted         bool                      `json:"granted"`
	Actions         []moderationActionRequest `json:"actions"`
}

func (s *Server) handleModerationCases(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	filter := domain.ModerationCaseFilter{
		AssignedTo: query.Get("assigned_to"),
		Limit:      limit,
	}
	if raw := query.Get("statuses"); raw != "" {
		for _, status := range strings.Split(raw, ",") {
			if status = strings.TrimSpace(status); status != "" {
				filter.Statuses = append(filter.Statuses, domain.ModerationCaseStatus(status))
			}
		}
	}
	if raw := query.Get("target_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid target id")
			return
		}
		filter.Target = domain.Peer{
			Type: domain.PeerType(query.Get("target_type")), ID: id,
		}
	}
	if raw := query.Get("before_updated_at"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid before_updated_at")
			return
		}
		filter.BeforeUpdate = parsed
		filter.BeforeID, _ = strconv.ParseInt(query.Get("before_id"), 10, 64)
	}
	items, err := s.svc.ModerationCases(r.Context(), filter)
	if err != nil {
		writeModerationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": items})
}

func (s *Server) handleModerationCase(w http.ResponseWriter, r *http.Request) {
	caseID, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	detail, found, err := s.svc.ModerationCase(r.Context(), caseID)
	if err != nil {
		writeModerationError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "moderation case not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleModerationReport(w http.ResponseWriter, r *http.Request) {
	reportID, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	report, found, err := s.svc.ModerationReport(r.Context(), reportID)
	if err != nil {
		writeModerationError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "moderation report not found")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleClaimModerationCase(w http.ResponseWriter, r *http.Request) {
	caseID, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	var request moderationClaimRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := s.svc.ClaimModerationCase(
		r.Context(), caseID, request.ExpectedVersion, request.Actor,
	)
	if err != nil {
		writeModerationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleDecideModerationCase(w http.ResponseWriter, r *http.Request) {
	caseID, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	var request moderationDecisionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	detail, created, err := s.svc.DecideModerationCase(
		r.Context(), moderationDecisionDomain(caseID, 0, request),
	)
	if err != nil {
		writeModerationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created, "case": detail,
	})
}

func (s *Server) handleSubmitModerationAppeal(w http.ResponseWriter, r *http.Request) {
	caseID, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	var request moderationAppealRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	appeal, created, err := s.svc.SubmitModerationAppeal(
		r.Context(), caseID, request.AppellantUserID, request.Text,
	)
	if err != nil {
		writeModerationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created, "appeal": appeal,
	})
}

func (s *Server) handleReviewModerationAppeal(w http.ResponseWriter, r *http.Request) {
	caseID, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	appealID, ok := moderationPathID(w, r, "appeal_id")
	if !ok {
		return
	}
	var request moderationAppealReviewRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	kind := domain.ModerationDecisionAppealDeny
	if request.Granted {
		kind = domain.ModerationDecisionAppealGrant
	}
	decision := moderationDecisionRequest{
		ExpectedVersion: request.ExpectedVersion, Actor: request.Actor,
		Reason: request.Reason, CommandID: request.CommandID,
		Kind: kind, Actions: request.Actions,
	}
	detail, created, err := s.svc.ReviewModerationAppeal(
		r.Context(), moderationDecisionDomain(caseID, appealID, decision),
	)
	if err != nil {
		writeModerationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created, "case": detail,
	})
}

func moderationDecisionDomain(caseID, appealID int64, request moderationDecisionRequest) domain.ModerationDecisionRequest {
	actions := make([]domain.ModerationActionDraft, 0, len(request.Actions))
	for _, action := range request.Actions {
		payload := action.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		actions = append(actions, domain.ModerationActionDraft{
			Kind: action.Kind, Payload: payload,
		})
	}
	return domain.ModerationDecisionRequest{
		CaseID: caseID, AppealID: appealID,
		ExpectedVersion: request.ExpectedVersion, Actor: request.Actor,
		Reason: request.Reason, CommandID: request.CommandID,
		Kind: request.Kind, Actions: actions,
	}
}

func moderationPathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return id, true
}

func writeModerationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrModerationCaseNotFound),
		errors.Is(err, domain.ErrModerationReportNotFound),
		errors.Is(err, domain.ErrModerationEvidenceNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, domain.ErrModerationPermissionDenied):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, domain.ErrModerationCaseConflict),
		errors.Is(err, domain.ErrModerationActionConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, domain.ErrModerationRateLimited):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, domain.ErrModerationCaseInvalid),
		errors.Is(err, domain.ErrModerationActionInvalid),
		errors.Is(err, domain.ErrModerationReportInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
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
