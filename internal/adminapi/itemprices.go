package adminapi

import (
	"net/http"

	"telesrv/internal/admin"
)

// Shop prices. GET is the one read every consumer shares: the panel, and the
// flashgram-bot, which polls it as the authoritative catalog (products with
// enabled=false disappear from the shop).

func (s *Server) handleItemPrices(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.svc.(itemPricesService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "item prices are not configured")
		return
	}
	rows, err := svc.ItemPrices(r.Context(), queryBool(r.URL.Query().Get("enabled_only")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rate, err := svc.StarsRate(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "stars_rate": rate})
}

func (s *Server) handleUpdateItemPrice(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.svc.(itemPricesService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "item prices are not configured")
		return
	}
	var req admin.UpdateItemPriceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := svc.UpdateItemPrice(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetStarsRate(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.svc.(itemPricesService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "item prices are not configured")
		return
	}
	var req admin.SetStarsRateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := svc.SetStarsRate(r.Context(), req)
	writeCommandResult(w, result, err)
}
