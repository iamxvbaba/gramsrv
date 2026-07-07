package adminapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/admin"
)

type Config struct {
	Addr  string
	Token string
}

type Service interface {
	SetSendFrozen(ctx context.Context, req admin.SetSendFrozenRequest) (admin.CommandResult, error)
	GrantPremium(ctx context.Context, req admin.GrantPremiumRequest) (admin.CommandResult, error)
	GrantStars(ctx context.Context, req admin.GrantStarsRequest) (admin.CommandResult, error)
	SetVerified(ctx context.Context, req admin.SetVerifiedRequest) (admin.CommandResult, error)
	SetChannelVerified(ctx context.Context, req admin.SetChannelVerifiedRequest) (admin.CommandResult, error)
	RevokeSessions(ctx context.Context, req admin.RevokeSessionsRequest) (admin.CommandResult, error)
	DeletePrivateMessages(ctx context.Context, req admin.DeletePrivateMessagesRequest) (admin.CommandResult, error)
	DeletePrivateHistory(ctx context.Context, req admin.DeletePrivateHistoryRequest) (admin.CommandResult, error)
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
	mux.HandleFunc("POST /v1/accounts/freeze-send", s.authenticated(s.handleFreezeSend))
	mux.HandleFunc("POST /v1/accounts/grant-premium", s.authenticated(s.handleGrantPremium))
	mux.HandleFunc("POST /v1/accounts/grant-stars", s.authenticated(s.handleGrantStars))
	mux.HandleFunc("POST /v1/accounts/set-verified", s.authenticated(s.handleSetVerified))
	mux.HandleFunc("POST /v1/accounts/revoke-sessions", s.authenticated(s.handleRevokeSessions))
	mux.HandleFunc("POST /v1/channels/set-verified", s.authenticated(s.handleSetChannelVerified))
	mux.HandleFunc("POST /v1/messages/delete", s.authenticated(s.handleDeleteMessages))
	mux.HandleFunc("POST /v1/messages/delete-history", s.authenticated(s.handleDeleteHistory))
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

func (s *Server) handleFreezeSend(w http.ResponseWriter, r *http.Request) {
	var req admin.SetSendFrozenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetSendFrozen(r.Context(), req)
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
