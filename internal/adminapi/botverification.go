package adminapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

// Third-party bot verification over the admin API
// (core.telegram.org/api/bots/verification).
//
// These are the mirror routes of the panel's own /api/botverification endpoints:
// the panel reads straight from PostgreSQL for speed, while an integration holding
// a scoped token reads it here. Every mutation only ever travels this way, so the
// command journal, the status machine and the optimistic lock are enforced in one
// place.
//
// This is NOT the official platform verification surface in verification.go. The
// two mechanisms own separate tables, separate permissions (botverification.* vs
// verification.*) and separate routes, and neither reads the other's state: a
// third-party verifier must never be able to mint a platform checkmark.
//
// Every int64 crosses the JSON boundary as a decimal string. Bot ids, peer ids,
// custom emoji document ids and the optimistic-locking version all exceed the
// range a JSON number holds exactly, and a rounded version would decide the wrong
// revision of a row.

// handleBotVerifiers lists verifier bots.
func (s *Server) handleBotVerifiers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, ok := optionalQueryInt(w, query, "limit")
	if !ok {
		return
	}
	items, err := s.svc.BotVerifiers(r.Context(), queryBool(query.Get("enabled_only")), limit)
	if err != nil {
		writeBotVerificationError(w, err)
		return
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, botVerifierResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// handleVerificationIcons lists the icon catalogue.
func (s *Server) handleVerificationIcons(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, ok := optionalQueryInt(w, query, "limit")
	if !ok {
		return
	}
	items, err := s.svc.VerificationIcons(r.Context(), queryBool(query.Get("active_only")), limit)
	if err != nil {
		writeBotVerificationError(w, err)
		return
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, verificationIconResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// handleCustomVerifications lists granted marks with keyset paging.
func (s *Server) handleCustomVerifications(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	peerType, ok := botVerificationPeerType(w, query.Get("peer_type"))
	if !ok {
		return
	}
	verifierBotID, ok := optionalQueryInt64(w, query, "verifier_bot_id")
	if !ok {
		return
	}
	beforeID, ok := optionalQueryInt64(w, query, "before_id")
	if !ok {
		return
	}
	limit, ok := optionalQueryInt(w, query, "limit")
	if !ok {
		return
	}
	items, err := s.svc.CustomVerifications(r.Context(), domain.CustomVerificationFilter{
		VerifierBotID: verifierBotID,
		PeerType:      peerType,
		Query:         query.Get("q"),
		BeforeID:      beforeID,
		Limit:         limit,
	})
	if err != nil {
		writeBotVerificationError(w, err)
		return
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, customVerificationResponse(item))
	}
	// The page bound is the use-case layer's, so has_more is derived from what came
	// back rather than from the limit the caller asked for.
	hasMore := limit > 0 && len(items) >= limit
	nextBeforeID := ""
	if hasMore && len(items) > 0 {
		nextBeforeID = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":           rows,
		"has_more":       hasMore,
		"next_before_id": nextBeforeID,
	})
}

// handleCustomVerificationRequests is the third-party review queue.
func (s *Server) handleCustomVerificationRequests(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	peerType, ok := botVerificationPeerType(w, query.Get("peer_type"))
	if !ok {
		return
	}
	filter := domain.CustomVerificationRequestFilter{
		PeerType: peerType,
		Query:    query.Get("q"),
	}
	// status accepts a comma-separated list, so a "pending,approved" view is one
	// request rather than two.
	for _, raw := range strings.Split(query.Get("status"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		status := domain.CustomVerificationRequestStatus(raw)
		if !status.Valid() {
			writeCodedError(w, http.StatusBadRequest, admin.CodeCustomVerificationStatusInvalid, "invalid status "+raw)
			return
		}
		filter.Statuses = append(filter.Statuses, status)
	}
	verifierBotID, ok := optionalQueryInt64(w, query, "verifier_bot_id")
	if !ok {
		return
	}
	filter.VerifierBotID = verifierBotID
	beforeID, ok := optionalQueryInt64(w, query, "before_id")
	if !ok {
		return
	}
	filter.BeforeID = beforeID
	limit, ok := optionalQueryInt(w, query, "limit")
	if !ok {
		return
	}
	filter.Limit = limit
	items, err := s.svc.CustomVerificationRequests(r.Context(), filter)
	if err != nil {
		writeBotVerificationError(w, err)
		return
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, customVerificationRequestResponse(item))
	}
	hasMore := limit > 0 && len(items) >= limit
	nextBeforeID := ""
	if hasMore && len(items) > 0 {
		nextBeforeID = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":           rows,
		"has_more":       hasMore,
		"next_before_id": nextBeforeID,
	})
}

// handleCustomVerificationRequest is one application with the verifier behind it
// and whether the mark is on the peer right now.
func (s *Server) handleCustomVerificationRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	req, err := s.svc.CustomVerificationRequest(r.Context(), id)
	if err != nil {
		writeBotVerificationError(w, err)
		return
	}
	body := map[string]any{"request": customVerificationRequestResponse(req)}
	// The verifier row is advisory: it may have been revoked since the application
	// was filed, and that must not turn the audit record into a 500. An absent row
	// is reported as a verifier with only its id, so the reviewer can see which bot
	// it was.
	if settings, err := s.svc.BotVerifier(r.Context(), req.VerifierBotID); err == nil {
		body["verifier"] = botVerifierResponse(settings)
	} else if errors.Is(err, domain.ErrVerifierNotFound) {
		body["verifier"] = botVerifierResponse(domain.BotVerifierSettings{BotID: req.VerifierBotID})
	} else {
		body["verifier"] = botVerifierResponse(domain.BotVerifierSettings{BotID: req.VerifierBotID})
		body["verifier_error"] = err.Error()
	}
	// mark_active tells "approved" apart from "approved and since stripped by the
	// operator", which is the one thing the status alone cannot say.
	if active, err := s.svc.CustomVerificationMarkActive(r.Context(), req.VerifierBotID, req.Peer); err == nil {
		body["mark_active"] = active
	} else {
		body["mark_active"] = false
		body["mark_error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

// handleCustomVerificationCounts is the queue summary.
func (s *Server) handleCustomVerificationCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := s.svc.CustomVerificationRequestCounts(r.Context())
	if err != nil {
		writeBotVerificationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": customVerificationCountsResponse(counts)})
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func (s *Server) handleGrantBotVerifier(w http.ResponseWriter, r *http.Request) {
	var req admin.GrantBotVerifierRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.GrantBotVerifier(r.Context(), req)
	writeBotVerificationCommandResult(w, result, err)
}

func (s *Server) handleSetBotVerifierEnabled(w http.ResponseWriter, r *http.Request) {
	var req admin.SetBotVerifierEnabledRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.SetBotVerifierEnabled(r.Context(), req)
	writeBotVerificationCommandResult(w, result, err)
}

func (s *Server) handleRevokeBotVerifier(w http.ResponseWriter, r *http.Request) {
	var req admin.RevokeBotVerifierRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.RevokeBotVerifier(r.Context(), req)
	writeBotVerificationCommandResult(w, result, err)
}

func (s *Server) handleUpsertVerificationIcon(w http.ResponseWriter, r *http.Request) {
	var req admin.UpsertVerificationIconRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.UpsertVerificationIcon(r.Context(), req)
	writeBotVerificationCommandResult(w, result, err)
}

func (s *Server) handleSetVerificationIconActive(w http.ResponseWriter, r *http.Request) {
	var req admin.SetVerificationIconActiveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.SetVerificationIconActive(r.Context(), req)
	writeBotVerificationCommandResult(w, result, err)
}

func (s *Server) handleRevokeCustomVerification(w http.ResponseWriter, r *http.Request) {
	var req admin.RevokeCustomVerificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.RevokeCustomVerification(r.Context(), req)
	writeBotVerificationCommandResult(w, result, err)
}

func (s *Server) handleApproveBotVerification(w http.ResponseWriter, r *http.Request) {
	id, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	var req admin.ApproveBotVerificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The path is the authority on which application is decided: a body naming a
	// different one would make the URL lie to the audit trail.
	req.RequestID = id
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.ApproveBotVerification(r.Context(), req)
	writeBotVerificationCommandResult(w, result, err)
}

func (s *Server) handleRejectBotVerification(w http.ResponseWriter, r *http.Request) {
	id, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	var req admin.RejectBotVerificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.RequestID = id
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.RejectBotVerification(r.Context(), req)
	writeBotVerificationCommandResult(w, result, err)
}

func (s *Server) handleRevokeBotVerification(w http.ResponseWriter, r *http.Request) {
	id, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	var req admin.RevokeBotVerificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.RequestID = id
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.RevokeBotVerification(r.Context(), req)
	writeBotVerificationCommandResult(w, result, err)
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// botVerifierResponse renders one verifier bot. The keys are the panel's row
// field names, so the same shape reaches the browser whether it came from here or
// from the panel's direct read.
func botVerifierResponse(settings domain.BotVerifierSettings) map[string]any {
	out := map[string]any{
		"BotID":                      strconv.FormatInt(settings.BotID, 10),
		"IconDocumentID":             strconv.FormatInt(settings.IconDocumentID, 10),
		"CompanyName":                settings.CompanyName,
		"DefaultDescription":         settings.DefaultDescription,
		"CanModifyCustomDescription": settings.CanModifyCustomDescription,
		"Enabled":                    settings.Enabled,
		"GrantedBy":                  settings.GrantedBy,
		"GrantReason":                settings.GrantReason,
		"Version":                    strconv.FormatInt(settings.Version, 10),
	}
	if !settings.CreatedAt.IsZero() {
		out["CreatedAt"] = settings.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !settings.UpdatedAt.IsZero() {
		out["UpdatedAt"] = settings.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func verificationIconResponse(icon domain.VerificationIcon) map[string]any {
	out := map[string]any{
		"ID":         strconv.FormatInt(icon.ID, 10),
		"DocumentID": strconv.FormatInt(icon.DocumentID, 10),
		"OwnerBotID": strconv.FormatInt(icon.OwnerBotID, 10),
		"Name":       icon.Name,
		"Active":     icon.Active,
	}
	if !icon.CreatedAt.IsZero() {
		out["CreatedAt"] = icon.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !icon.UpdatedAt.IsZero() {
		out["UpdatedAt"] = icon.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func customVerificationResponse(mark domain.CustomVerification) map[string]any {
	out := map[string]any{
		"ID":             strconv.FormatInt(mark.ID, 10),
		"VerifierBotID":  strconv.FormatInt(mark.VerifierBotID, 10),
		"PeerType":       string(mark.Peer.Type),
		"PeerID":         strconv.FormatInt(mark.Peer.ID, 10),
		"IconDocumentID": strconv.FormatInt(mark.IconDocumentID, 10),
		"Description":    mark.Description,
		"Version":        strconv.FormatInt(mark.Version, 10),
	}
	if !mark.CreatedAt.IsZero() {
		out["CreatedAt"] = mark.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !mark.UpdatedAt.IsZero() {
		out["UpdatedAt"] = mark.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func customVerificationRequestResponse(req domain.CustomVerificationRequest) map[string]any {
	out := map[string]any{
		"ID":                   strconv.FormatInt(req.ID, 10),
		"VerifierBotID":        strconv.FormatInt(req.VerifierBotID, 10),
		"ApplicantUserID":      strconv.FormatInt(req.ApplicantUserID, 10),
		"PeerType":             string(req.Peer.Type),
		"PeerID":               strconv.FormatInt(req.Peer.ID, 10),
		"PeerTitle":            req.PeerTitle,
		"PeerUsername":         req.PeerUsername,
		"Reason":               req.Reason,
		"RequestedDescription": req.RequestedDescription,
		"Status":               string(req.Status),
		"DecidedBy":            req.DecidedBy,
		"DecisionReason":       req.DecisionReason,
		// InternalNote is operator-only. It is exposed here because every caller of
		// this route already holds botverification.review, and it is the reviewer's
		// own handover note; it is never part of the applicant-facing projection.
		"InternalNote":  req.InternalNote,
		"CorrelationID": req.CorrelationID,
		"Version":       strconv.FormatInt(req.Version, 10),
	}
	if !req.CreatedAt.IsZero() {
		out["CreatedAt"] = req.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !req.UpdatedAt.IsZero() {
		out["UpdatedAt"] = req.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !req.ApprovedAt.IsZero() {
		out["ApprovedAt"] = req.ApprovedAt.UTC().Format(time.RFC3339)
	}
	if !req.RejectedAt.IsZero() {
		out["RejectedAt"] = req.RejectedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// customVerificationCountsResponse renders the queue summary with every modelled
// status present, so the panel never has to distinguish "zero" from "absent". The
// values are decimal strings for the same exactness reason as the ids.
func customVerificationCountsResponse(counts map[domain.CustomVerificationRequestStatus]int64) map[string]string {
	out := make(map[string]string, len(customVerificationStatusOrder))
	for _, status := range customVerificationStatusOrder {
		out[string(status)] = strconv.FormatInt(counts[status], 10)
	}
	for status, count := range counts {
		if _, ok := out[string(status)]; !ok {
			out[string(status)] = strconv.FormatInt(count, 10)
		}
	}
	return out
}

// customVerificationStatusOrder is the closed status set, in lifecycle order.
var customVerificationStatusOrder = []domain.CustomVerificationRequestStatus{
	domain.CustomVerificationPending,
	domain.CustomVerificationApproved,
	domain.CustomVerificationRejected,
	domain.CustomVerificationRevoked,
}

// botVerificationPeerType validates the peer filter against the peer kinds a
// third-party mark can sit on. An unmodelled value is a 400 rather than an empty
// result, so a typo is reported instead of silently returning nothing.
func botVerificationPeerType(w http.ResponseWriter, raw string) (domain.PeerType, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	peerType := domain.PeerType(raw)
	if peerType != domain.PeerTypeUser && peerType != domain.PeerTypeChannel {
		writeCodedError(w, http.StatusBadRequest, admin.CodeCustomVerificationTargetInvalid, "invalid peer_type")
		return "", false
	}
	return peerType, true
}

// queryBool reads a boolean flag the way the panel writes it: an absent or empty
// value is false, and "1"/"true"/"yes" are true.
func queryBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// botVerificationErrorStatus maps a third-party verification failure onto its HTTP
// status.
//
// The version conflict is 409, not 400, because nothing about the request was
// wrong -- another operator simply decided first, and the panel has to answer that
// by reloading rather than by correcting input. The per-verifier bound is 409 for
// the same reason: the request was well formed and the state refused it.
func botVerificationErrorStatus(code string) int {
	switch code {
	case admin.CodeBotVerifierNotFound,
		admin.CodeBotVerifierBotNotFound,
		admin.CodeVerificationIconNotFound,
		admin.CodeCustomVerificationNotFound,
		admin.CodeCustomVerificationRequestNotFound:
		return http.StatusNotFound
	case admin.CodeCustomVerificationConflict,
		admin.CodeCustomVerificationLimit,
		admin.CodeCustomVerificationRequestExists:
		return http.StatusConflict
	case admin.CodeCustomVerificationRateLimited:
		return http.StatusTooManyRequests
	case admin.CodeBotVerifierForbidden,
		admin.CodeBotVerifierDescriptionForbidden,
		admin.CodeBotVerifierInvalid,
		admin.CodeVerificationIconInactive,
		admin.CodeVerificationIconInvalid,
		admin.CodeCustomVerificationStatusInvalid,
		admin.CodeCustomVerificationReasonRequired,
		admin.CodeCustomVerificationTargetInvalid,
		admin.CodeCustomVerificationTargetSystem,
		admin.CodeCustomVerificationInvalid:
		// BOTVERIFIER_FORBIDDEN is 400 rather than 403 on purpose: the caller is
		// authorised (403 is reserved for the permission gate), it is the *subject*
		// that may not verify, which the operator fixes by enabling the verifier.
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeBotVerificationError(w http.ResponseWriter, err error) {
	code := admin.BotVerificationErrorCode(err)
	writeCodedError(w, botVerificationErrorStatus(code), code, err.Error())
}

// writeBotVerificationCommandResult answers a command.
//
// The body stays a CommandResult so the panel parses one shape for every operator
// action, but the status is derived from the failure: a lost optimistic-locking
// race must reach the browser as 409, because that is the one failure the panel
// resolves by reloading the row instead of by asking the operator to fix the form.
func writeBotVerificationCommandResult(w http.ResponseWriter, result admin.CommandResult, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	code := admin.BotVerificationErrorCode(err)
	status := botVerificationErrorStatus(code)
	if status == http.StatusInternalServerError {
		// An unmapped command failure is a bad request, as everywhere else in this
		// API, rather than a server fault.
		status = http.StatusBadRequest
	}
	if result.CommandID == "" {
		result = admin.CommandResult{Status: "failed", Message: "command failed", Error: err.Error()}
	}
	if result.Error == "" {
		result.Error = err.Error()
	}
	if code == admin.CodeCustomVerificationConflict {
		result.Message = "another operator changed this row first; reload it and try again"
	}
	writeJSON(w, status, result)
}
