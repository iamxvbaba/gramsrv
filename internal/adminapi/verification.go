package adminapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

// Official platform verification review over the admin API.
//
// These are the mirror routes of the panel's own endpoints: the panel reads the
// queue straight from PostgreSQL for speed, while an integration holding a scoped
// token reads it here. Decisions only ever travel this way, so the command
// journal and the status machine are enforced in one place.

// handleVerificationApplications is the review queue.
func (s *Server) handleVerificationApplications(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := domain.VerificationApplicationFilter{
		TargetType: domain.VerificationTargetType(strings.TrimSpace(query.Get("target_type"))),
		Reviewer:   strings.TrimSpace(query.Get("reviewer")),
		Query:      query.Get("q"),
	}
	if filter.TargetType != "" && !filter.TargetType.Valid() {
		writeCodedError(w, http.StatusBadRequest, admin.CodeVerificationTargetInvalid, "invalid target_type")
		return
	}
	// status accepts a comma-separated list, so the queue view ("submitted,
	// in_review") is one request rather than two.
	for _, raw := range strings.Split(query.Get("status"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		status := domain.VerificationStatus(raw)
		if !status.Valid() {
			writeCodedError(w, http.StatusBadRequest, admin.CodeVerificationStatusInvalid, "invalid status "+raw)
			return
		}
		filter.Statuses = append(filter.Statuses, status)
	}
	limit, ok := optionalQueryInt(w, query, "limit")
	if !ok {
		return
	}
	filter.Limit = limit
	beforeID, ok := optionalQueryInt64(w, query, "before_id")
	if !ok {
		return
	}
	filter.BeforeID = beforeID
	items, err := s.svc.VerificationApplications(r.Context(), filter)
	if err != nil {
		writeVerificationError(w, err)
		return
	}
	applications := make([]map[string]any, 0, len(items))
	for _, item := range items {
		applications = append(applications, verificationApplicationResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"applications": applications})
}

// handleVerificationApplication is one application with its history and the
// target as it looks right now.
func (s *Server) handleVerificationApplication(w http.ResponseWriter, r *http.Request) {
	id, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	app, err := s.svc.VerificationApplication(r.Context(), id)
	if err != nil {
		writeVerificationError(w, err)
		return
	}
	limit, ok := optionalQueryInt(w, r.URL.Query(), "limit")
	if !ok {
		return
	}
	events, err := s.svc.VerificationApplicationEvents(r.Context(), app.ID, limit)
	if err != nil {
		writeVerificationError(w, err)
		return
	}
	history := make([]map[string]any, 0, len(events))
	for _, event := range events {
		history = append(history, verificationEventResponse(event))
	}
	body := map[string]any{
		"application": verificationApplicationResponse(app),
		"events":      history,
	}
	// The snapshot is advisory: a target that vanished must not turn the audit
	// record into a 500, so a snapshot failure is reported next to the record
	// instead of replacing it.
	if target, err := s.svc.VerificationTargetSnapshot(r.Context(), app.TargetType, app.TargetID); err == nil {
		body["target"] = verificationTargetResponse(target)
	} else {
		body["target_error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

// handleVerificationCounts is the queue summary.
func (s *Server) handleVerificationCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := s.svc.VerificationCounts(r.Context())
	if err != nil {
		writeVerificationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": verificationCountsResponse(counts)})
}

func (s *Server) handleClaimVerification(w http.ResponseWriter, r *http.Request) {
	id, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	var req admin.ClaimVerificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The path is the authority on which application is decided: a body naming a
	// different one would make the URL lie to the audit trail.
	req.ApplicationID = id
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.ClaimVerification(r.Context(), req)
	writeVerificationCommandResult(w, result, err)
}

func (s *Server) handleApproveVerification(w http.ResponseWriter, r *http.Request) {
	id, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	var req admin.ApproveVerificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.ApplicationID = id
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.ApproveVerification(r.Context(), req)
	writeVerificationCommandResult(w, result, err)
}

func (s *Server) handleRejectVerification(w http.ResponseWriter, r *http.Request) {
	id, ok := moderationPathID(w, r, "id")
	if !ok {
		return
	}
	var req admin.RejectVerificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.ApplicationID = id
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.RejectVerification(r.Context(), req)
	writeVerificationCommandResult(w, result, err)
}

func (s *Server) handleRevokeVerification(w http.ResponseWriter, r *http.Request) {
	var req admin.RevokeVerificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := s.svc.RevokeVerification(r.Context(), req)
	writeVerificationCommandResult(w, result, err)
}

// applyVerificationPrincipal fills in the audit actor for a scoped token that did
// not state one.
//
// A scoped token's configured name *is* its audit identity, so an integration
// does not have to invent an actor string. The master token has no name, so a
// caller using it keeps having to state who is acting -- which is what the panel
// does with the signed-in operator.
func (s *Server) applyVerificationPrincipal(r *http.Request, meta *admin.CommandMeta) {
	if strings.TrimSpace(meta.Actor) != "" {
		return
	}
	if name := principalName(r.Context()); name != "" {
		meta.Actor = name
	}
}

// verificationApplicationResponse renders one application. Every int64 crosses
// the JSON boundary as a decimal string: application ids, peer ids and the
// optimistic-locking version exceed the range a JSON number holds exactly, and a
// rounded id would decide the wrong application.
func verificationApplicationResponse(app domain.VerificationApplication) map[string]any {
	out := map[string]any{
		"id":                strconv.FormatInt(app.ID, 10),
		"applicant_user_id": strconv.FormatInt(app.ApplicantUserID, 10),
		"target_type":       string(app.TargetType),
		"target_id":         strconv.FormatInt(app.TargetID, 10),
		"target_title":      app.TargetTitle,
		"target_username":   app.TargetUsername,
		"category":          app.Category,
		"description":       app.Description,
		"official_website":  app.OfficialWebsite,
		"social_links":      stringList(app.SocialLinks),
		"press_links":       stringList(app.PressLinks),
		"additional_note":   app.AdditionalNote,
		"status":            string(app.Status),
		"reviewer_admin_id": app.ReviewerAdminID,
		"decision_reason":   app.DecisionReason,
		// internal_note is operator-only. It is exposed here because every caller
		// of this route already holds verification.review, and it is the reviewer's
		// own handover note; it is never part of the applicant-facing projection.
		"internal_note":  app.InternalNote,
		"correlation_id": app.CorrelationID,
		"version":        strconv.FormatInt(app.Version, 10),
	}
	if !app.CreatedAt.IsZero() {
		out["created_at"] = app.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !app.UpdatedAt.IsZero() {
		out["updated_at"] = app.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !app.SubmittedAt.IsZero() {
		out["submitted_at"] = app.SubmittedAt.UTC().Format(time.RFC3339)
	}
	if !app.ReviewedAt.IsZero() {
		out["reviewed_at"] = app.ReviewedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func verificationEventResponse(event domain.VerificationApplicationEvent) map[string]any {
	out := map[string]any{
		"id":             strconv.FormatInt(event.ID, 10),
		"application_id": strconv.FormatInt(event.ApplicationID, 10),
		"kind":           string(event.Kind),
		"from_status":    string(event.FromStatus),
		"to_status":      string(event.ToStatus),
		"actor":          event.Actor,
		"reason":         event.Reason,
		"note":           event.Note,
		"correlation_id": event.CorrelationID,
	}
	if !event.CreatedAt.IsZero() {
		out["created_at"] = event.CreatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func verificationTargetResponse(target domain.VerificationTarget) map[string]any {
	return map[string]any{
		"type":     string(target.Type),
		"id":       strconv.FormatInt(target.ID, 10),
		"title":    target.Title,
		"username": target.Username,
		"verified": target.Verified,
		"eligible": target.Eligible,
		"reason":   target.Reason,
	}
}

// verificationCountsResponse renders the queue summary with every modelled status
// present, so the panel never has to distinguish "zero" from "absent". The values
// are decimal strings for the same exactness reason as the ids.
func verificationCountsResponse(counts domain.VerificationStatusCounts) map[string]string {
	out := make(map[string]string, len(verificationStatusOrder))
	for _, status := range verificationStatusOrder {
		out[string(status)] = strconv.FormatInt(counts[status], 10)
	}
	for status, count := range counts {
		if _, ok := out[string(status)]; !ok {
			out[string(status)] = strconv.FormatInt(count, 10)
		}
	}
	return out
}

// verificationStatusOrder is the closed status set, in lifecycle order.
var verificationStatusOrder = []domain.VerificationStatus{
	domain.VerificationStatusDraft,
	domain.VerificationStatusSubmitted,
	domain.VerificationStatusInReview,
	domain.VerificationStatusApproved,
	domain.VerificationStatusRejected,
	domain.VerificationStatusCancelled,
}

// stringList normalises a nil slice to an empty JSON array, so the panel can
// iterate without a null check.
func stringList(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// verificationErrorStatus maps a verification failure onto its HTTP status.
//
// The version conflict is the interesting one: it is 409, not 400, because
// nothing about the request was wrong -- another reviewer simply decided first,
// and the panel has to answer that by reloading rather than by correcting input.
func verificationErrorStatus(code string) int {
	switch code {
	case admin.CodeVerificationNotFound:
		return http.StatusNotFound
	case admin.CodeVerificationConflict,
		admin.CodeVerificationTargetOccupied,
		admin.CodeVerificationTargetVerified:
		return http.StatusConflict
	case admin.CodeVerificationStatusInvalid,
		admin.CodeVerificationReasonRequired,
		admin.CodeVerificationTargetInvalid,
		admin.CodeVerificationTargetNotPublic,
		admin.CodeVerificationTargetRestricted,
		admin.CodeVerificationTargetSystem,
		admin.CodeVerificationNotOwner,
		admin.CodeVerificationUserTargetsDisabled,
		admin.CodeVerificationInvalid:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeVerificationError(w http.ResponseWriter, err error) {
	code := admin.VerificationErrorCode(err)
	writeCodedError(w, verificationErrorStatus(code), code, err.Error())
}

// writeVerificationCommandResult answers a decision.
//
// The body stays a CommandResult so the panel parses one shape for every
// operator action, but the status is derived from the failure: a lost
// optimistic-locking race must reach the browser as 409, because that is the one
// failure the panel resolves by reloading the application instead of by asking
// the operator to fix the form.
func writeVerificationCommandResult(w http.ResponseWriter, result admin.CommandResult, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	code := admin.VerificationErrorCode(err)
	status := verificationErrorStatus(code)
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
	if code == admin.CodeVerificationConflict {
		result.Message = "another reviewer changed this application first; reload it and decide again"
	}
	writeJSON(w, status, result)
}
