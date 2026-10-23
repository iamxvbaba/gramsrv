package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"telesrv/internal/domain"
)

// PermissionBotShopRead lets the numbers bot read its shop price list.
const PermissionBotShopRead = "botshop.read"

// BotShopStore is the optional bot shop config storage.
type BotShopStore interface {
	BotShopConfig(ctx context.Context) (domain.BotShopConfig, error)
	SetBotShopConfig(ctx context.Context, cfg domain.BotShopConfig, actor string) error
}

func (s *Server) handleGetBotShop(w http.ResponseWriter, r *http.Request) {
	if s.botShop == nil {
		writeError(w, http.StatusNotImplemented, "bot shop is not configured")
		return
	}
	cfg, err := s.botShop.BotShopConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg})
}

func (s *Server) handleSetBotShop(w http.ResponseWriter, r *http.Request) {
	if s.botShop == nil {
		writeError(w, http.StatusNotImplemented, "bot shop is not configured")
		return
	}
	var req struct {
		Actor  string               `json:"actor"`
		Config domain.BotShopConfig `json:"config"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	err := s.botShop.SetBotShopConfig(r.Context(), req.Config, strings.TrimSpace(req.Actor))
	if errors.Is(err, domain.ErrBotShopConfigInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": req.Config})
}
