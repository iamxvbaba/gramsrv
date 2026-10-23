package adminapi

import (
	"net/http"
	"strconv"

	"telesrv/internal/admin"
)

func (s *Server) handlePendingStarGiftCollectible(w http.ResponseWriter, r *http.Request) {
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || giftID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid gift id")
		return
	}
	revision, found, err := s.svc.PendingGiftCollectible(r.Context(), giftID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"found": false, "gift_id": strconv.FormatInt(giftID, 10)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"found": true, "gift_id": strconv.FormatInt(giftID, 10), "revision": revision.Revision,
		"upgrade_stars": strconv.FormatInt(revision.UpgradeStars, 10), "supply_total": revision.SupplyTotal,
		"slug_prefix": revision.SlugPrefix, "scheduled_publish_at": revision.ScheduledPublishAt,
		"models": len(revision.Models), "patterns": len(revision.Patterns), "backdrops": len(revision.Backdrops),
	})
}

func (s *Server) handleReleaseCollectiblePool(w http.ResponseWriter, r *http.Request) {
	var req admin.ReleaseCollectiblePoolRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.ReleaseCollectiblePool(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetStarGiftSupply(w http.ResponseWriter, r *http.Request) {
	var req admin.SetStarGiftSupplyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetStarGiftSupply(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleSetStarGiftDropProtection(w http.ResponseWriter, r *http.Request) {
	var req admin.SetStarGiftDropProtectionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.SetStarGiftDropProtection(r.Context(), req)
	writeCommandResult(w, result, err)
}

func (s *Server) handleDeleteStarGift(w http.ResponseWriter, r *http.Request) {
	var req admin.DeleteStarGiftRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.svc.DeleteStarGift(r.Context(), req)
	writeCommandResult(w, result, err)
}
