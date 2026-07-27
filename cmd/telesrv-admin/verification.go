package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

// Official platform verification in the panel BFF.
//
// Reads come straight from PostgreSQL, like every other table view, so the queue
// pages without a hop through the admin API and the applicant can be resolved by
// a join. Decisions go the other way -- always through the admin API, so the
// command journal, the status machine and the optimistic lock are enforced in one
// place and a panel action is indistinguishable from an API one in the audit
// trail.

// verificationRead mounts a route behind a session and the verification.review
// right.
func (s *server) verificationRead(handler http.HandlerFunc) http.Handler {
	return s.requireAuthAPI(s.requirePermission(permissionVerificationReview, handler))
}

// handleVerificationApplicationsAPI pages the review queue. The filter is
// validated before the read store is consulted: a malformed query is a 400
// whether or not the database happens to be reachable.
func (s *server) handleVerificationApplicationsAPI(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	status := strings.TrimSpace(query.Get("status"))
	if status != "" && !domain.VerificationStatus(status).Valid() {
		writeAPIError(w, http.StatusBadRequest, "invalid status")
		return
	}
	targetType := strings.TrimSpace(query.Get("target_type"))
	if targetType != "" && !domain.VerificationTargetType(targetType).Valid() {
		writeAPIError(w, http.StatusBadRequest, "invalid target_type")
		return
	}
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
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	rows, hasMore, err := s.read.ListVerificationApplications(
		r.Context(), status, targetType, strings.TrimSpace(query.Get("reviewer")), query.Get("q"), beforeID, limit,
	)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nextBeforeID := ""
	if hasMore && len(rows) > 0 {
		nextBeforeID = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":           rows,
		"has_more":       hasMore,
		"next_before_id": nextBeforeID,
	})
}

func (s *server) handleVerificationApplicationDetailAPI(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt64(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	detail, err := s.read.VerificationApplicationDetail(r.Context(), id)
	if err != nil {
		if errors.Is(err, errReadNotFound) {
			writeAPIError(w, http.StatusNotFound, "verification application not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"application": detail.Application,
		"events":      detail.Events,
		// Both flags describe the target as it is now, not as it was at
		// submission: a reviewer has to see that the applicant lost control of the
		// peer, or that the badge is already on, before deciding.
		"applicant_controls_target": detail.ApplicantControlsTarget,
		"target_verified":           detail.Application.TargetVerified,
	})
}

func (s *server) handleVerificationCountsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	counts, err := s.read.VerificationStatusCounts(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

// verificationDecisionAPIRequest is the decision payload shared by all three
// per-application actions. version is the optimistic-locking token the reviewer
// read; internal_note is operator-only and is not part of what the applicant is
// told. It is optional everywhere, including on a claim, so one panel form can
// drive all three actions without tripping the strict decoder.
type verificationDecisionAPIRequest struct {
	CommandID    string    `json:"command_id"`
	Reason       string    `json:"reason"`
	Confirm      bool      `json:"confirm"`
	Version      flexInt64 `json:"version"`
	InternalNote string    `json:"internal_note"`
}

func (s *server) handleClaimVerificationAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := verificationPathID(w, r)
	if !ok {
		return
	}
	var body verificationDecisionAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.ClaimVerificationRequest{
		CommandMeta:   s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "claim-verification"),
		ApplicationID: id,
		Version:       body.Version.Int64(),
		InternalNote:  body.InternalNote,
	}
	result, status, err := s.callAdminCommand(r.Context(), verificationDecisionPath(id, "claim"), req)
	writeVerificationResultAPI(w, result, status, err)
}

func (s *server) handleApproveVerificationAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := verificationPathID(w, r)
	if !ok {
		return
	}
	var body verificationDecisionAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.ApproveVerificationRequest{
		CommandMeta:   s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "approve-verification"),
		ApplicationID: id,
		Version:       body.Version.Int64(),
		InternalNote:  body.InternalNote,
	}
	result, status, err := s.callAdminCommand(r.Context(), verificationDecisionPath(id, "approve"), req)
	writeVerificationResultAPI(w, result, status, err)
}

func (s *server) handleRejectVerificationAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := verificationPathID(w, r)
	if !ok {
		return
	}
	var body verificationDecisionAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.RejectVerificationRequest{
		CommandMeta:   s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "reject-verification"),
		ApplicationID: id,
		Version:       body.Version.Int64(),
		InternalNote:  body.InternalNote,
	}
	result, status, err := s.callAdminCommand(r.Context(), verificationDecisionPath(id, "reject"), req)
	writeVerificationResultAPI(w, result, status, err)
}

// revokeVerificationAPIRequest clears a badge. It addresses the target, not an
// application: the approved application stays approved as history.
type revokeVerificationAPIRequest struct {
	CommandID    string    `json:"command_id"`
	Reason       string    `json:"reason"`
	Confirm      bool      `json:"confirm"`
	TargetType   string    `json:"target_type"`
	TargetID     flexInt64 `json:"target_id"`
	InternalNote string    `json:"internal_note"`
}

func (s *server) handleRevokeVerificationAPI(w http.ResponseWriter, r *http.Request) {
	var body revokeVerificationAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	targetType := domain.VerificationTargetType(strings.TrimSpace(body.TargetType))
	if !targetType.Valid() {
		writeAPIError(w, http.StatusBadRequest, "invalid target_type")
		return
	}
	if body.TargetID.Int64() <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid target_id")
		return
	}
	req := admin.RevokeVerificationRequest{
		CommandMeta:  s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "revoke-verification"),
		TargetType:   targetType,
		TargetID:     body.TargetID.Int64(),
		InternalNote: body.InternalNote,
	}
	result, status, err := s.callAdminCommand(r.Context(), "/v1/verification/revoke", req)
	writeVerificationResultAPI(w, result, status, err)
}

func verificationPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := parseInt64(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func verificationDecisionPath(applicationID int64, action string) string {
	return "/v1/verification/applications/" + strconv.FormatInt(applicationID, 10) + "/" + action
}

// writeVerificationResultAPI relays the admin API's own status to the browser.
//
// The other action handlers flatten every upstream failure into 502, which is
// fine when the only failure mode is "bad request". A verification decision has
// one more: 409 when another reviewer decided first. That has to reach the panel
// as 409, because it is the single case the panel resolves by reloading the
// application rather than by asking the operator to change something.
func writeVerificationResultAPI(w http.ResponseWriter, result admin.CommandResult, status int, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	if result.Status == "" {
		result.Status = "failed"
	}
	if result.Message == "" {
		result.Message = "command failed"
	}
	if result.Error == "" {
		result.Error = err.Error()
	}
	if status < 400 {
		// No HTTP answer at all: the admin API was unreachable or unparsable.
		status = http.StatusBadGateway
	}
	writeJSON(w, status, result)
}
