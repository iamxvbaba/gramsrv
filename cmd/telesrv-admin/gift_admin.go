package main

import (
	"fmt"
	"net/http"
	"strconv"

	"telesrv/internal/admin"
)

func (s *server) handlePendingStarGiftCollectibleAPI(w http.ResponseWriter, r *http.Request) {
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || giftID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid gift id")
		return
	}
	s.proxyAdminJSON(w, r, fmt.Sprintf("/v1/gifts/%d/collectibles/pending", giftID), 1<<20)
}

type releaseCollectiblePoolAPIRequest struct {
	CommandID string    `json:"command_id"`
	Reason    string    `json:"reason"`
	Confirm   bool      `json:"confirm"`
	GiftID    flexInt64 `json:"gift_id"`
}

func (s *server) handleReleaseCollectiblePoolAPI(w http.ResponseWriter, r *http.Request) {
	var body releaseCollectiblePoolAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.ReleaseCollectiblePoolRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "release-collectible-pool"),
		GiftID:      body.GiftID.Int64(),
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/gifts/release-collectible-pool", req)
	writeCommandResultAPI(w, result, err)
}

type setStarGiftSupplyAPIRequest struct {
	CommandID          string    `json:"command_id"`
	Reason             string    `json:"reason"`
	Confirm            bool      `json:"confirm"`
	GiftID             flexInt64 `json:"gift_id"`
	Limited            bool      `json:"limited"`
	AvailabilityTotal  int       `json:"availability_total"`
	AvailabilityIssued int       `json:"availability_issued"`
}

func (s *server) handleSetStarGiftSupplyAPI(w http.ResponseWriter, r *http.Request) {
	var body setStarGiftSupplyAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetStarGiftSupplyRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-gift-supply"),
		GiftID:      body.GiftID.Int64(), Limited: body.Limited,
		AvailabilityTotal: body.AvailabilityTotal, AvailabilityIssued: body.AvailabilityIssued,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/gifts/set-supply", req)
	writeCommandResultAPI(w, result, err)
}

type setStarGiftDropProtectionAPIRequest struct {
	CommandID              string    `json:"command_id"`
	Reason                 string    `json:"reason"`
	Confirm                bool      `json:"confirm"`
	GiftID                 flexInt64 `json:"gift_id"`
	LimitedPerUser         bool      `json:"limited_per_user"`
	PerUserTotal           int       `json:"per_user_total"`
	RateLimitCount         int       `json:"rate_limit_count"`
	RateLimitWindowSeconds int       `json:"rate_limit_window_seconds"`
}

func (s *server) handleSetStarGiftDropProtectionAPI(w http.ResponseWriter, r *http.Request) {
	var body setStarGiftDropProtectionAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.SetStarGiftDropProtectionRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-gift-drop-protection"),
		GiftID:      body.GiftID.Int64(), LimitedPerUser: body.LimitedPerUser, PerUserTotal: body.PerUserTotal,
		RateLimitCount: body.RateLimitCount, RateLimitWindowSeconds: body.RateLimitWindowSeconds,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/gifts/set-drop-protection", req)
	writeCommandResultAPI(w, result, err)
}

type deleteStarGiftAPIRequest struct {
	CommandID   string    `json:"command_id"`
	Reason      string    `json:"reason"`
	Confirm     bool      `json:"confirm"`
	GiftID      flexInt64 `json:"gift_id"`
	RefundStars bool      `json:"refund_stars"`
}

func (s *server) handleDeleteStarGiftAPI(w http.ResponseWriter, r *http.Request) {
	var body deleteStarGiftAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.DeleteStarGiftRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "delete-gift"),
		GiftID:      body.GiftID.Int64(), RefundStars: body.RefundStars,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/gifts/delete", req)
	writeCommandResultAPI(w, result, err)
}
