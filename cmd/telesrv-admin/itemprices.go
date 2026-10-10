package main

import (
	"net/http"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

// handleItemPricesAPI lists every known product's effective price (stored
// overrides merged over the catalog defaults) plus the shop-wide stars rate.
func (s *server) handleItemPricesAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	overrides, err := s.read.itemPrices(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rate, err := s.read.starsRate(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":       domain.MergeItemPrices(domain.DefaultItemPrices, overrides, false),
		"stars_rate": rate,
	})
}

type updateItemPriceAPIRequest struct {
	CommandID   string `json:"command_id"`
	Reason      string `json:"reason"`
	Confirm     bool   `json:"confirm"`
	ProductCode string `json:"product_code"`
	StarsPrice  int    `json:"stars_price"`
	Bid         int64  `json:"bid"`
	Enabled     bool   `json:"enabled"`
}

func (s *server) handleUpdateItemPriceAPI(w http.ResponseWriter, r *http.Request) {
	var body updateItemPriceAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.UpdateItemPriceRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "item-price"),
		ProductCode: body.ProductCode,
		StarsPrice:  body.StarsPrice,
		Bid:         body.Bid,
		Enabled:     body.Enabled,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/item-prices/update", req)
	writeCommandResultAPI(w, result, err)
}

type setStarsRateAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	StarsRate int64  `json:"stars_rate"`
}

func (s *server) handleSetStarsRateAPI(w http.ResponseWriter, r *http.Request) {
	var body setStarsRateAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetStarsRateRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "stars-rate"),
		StarsRate:   body.StarsRate,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/item-prices/set-rate", req)
	writeCommandResultAPI(w, result, err)
}
