package main

import (
	"net/http"
	"strconv"

	"telesrv/internal/admin"
)

// handleDonationWalletStatusAPI reports whether a crypto donations wallet
// exists and how many deposit addresses have been assigned. The wallet is
// auto-provisioned at server startup if missing (see docs/donations.md) --
// there is deliberately no admin action to create or regenerate one here.
func (s *server) handleDonationWalletStatusAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	status, err := s.read.donationWalletStatus(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleDonationChainsAPI lists every configured chain, enabled or not, plus
// every configured stablecoin row, for the chain config editor.
func (s *server) handleDonationChainsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	chains, err := s.read.donationChains(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tokens, err := s.read.donationTokens(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chains": chains, "tokens": tokens})
}

// handleDonationDepositsAPI lists deposits across every user, newest first,
// for the transactions/donations history table.
func (s *server) handleDonationDepositsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	query := r.URL.Query()
	beforeID, err := parseInt64(query.Get("before_id"))
	if err != nil || beforeID < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid before_id")
		return
	}
	limit, err := parseInt(query.Get("limit"))
	if err != nil || limit < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid limit")
		return
	}
	rows, hasMore, err := s.read.donationDeposits(r.Context(), beforeID, limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nextBeforeID := ""
	if hasMore && len(rows) > 0 {
		nextBeforeID = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "has_more": hasMore, "next_before_id": nextBeforeID})
}

type updateDonationChainAPIRequest struct {
	CommandID             string `json:"command_id"`
	Reason                string `json:"reason"`
	Confirm               bool   `json:"confirm"`
	ChainKey              string `json:"chain_key"`
	RPCURL                string `json:"rpc_url"`
	WSURL                 string `json:"ws_url"`
	Enabled               bool   `json:"enabled"`
	ConfirmationsRequired int    `json:"confirmations_required"`
	PriceFeedAddress      string `json:"price_feed_address"`
	ManualUSDRateMicros   int64  `json:"manual_usd_rate_micros"`
}

func (s *server) handleUpdateDonationChainAPI(w http.ResponseWriter, r *http.Request) {
	var body updateDonationChainAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.UpdateDonationChainRequest{
		CommandMeta:           s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "donation-chain"),
		ChainKey:              body.ChainKey,
		RPCURL:                body.RPCURL,
		WSURL:                 body.WSURL,
		Enabled:               body.Enabled,
		ConfirmationsRequired: body.ConfirmationsRequired,
		PriceFeedAddress:      body.PriceFeedAddress,
		ManualUSDRateMicros:   body.ManualUSDRateMicros,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/donations/chains/update", req)
	writeCommandResultAPI(w, result, err)
}
