package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

// Third-party bot verification in the panel BFF
// (core.telegram.org/api/bots/verification).
//
// This is NOT the official platform badge (see verification.go): third-party
// verification is an attributed mark granted by a verifier bot, carrying that
// verifier's own custom emoji icon and description. The two mechanisms own
// separate tables (verification_icons / bot_verifier_settings /
// custom_verifications / custom_verification_requests vs
// verification_applications), separate permissions (botverification.* vs
// verification.*) and separate routes, and neither reads the other's state.
//
// Reads come straight from PostgreSQL, like every other table view, so the tables
// page without a hop through the admin API and peers can be resolved by a join.
// Every mutation goes the other way -- always through the admin API, so the command
// journal, the status machine and the optimistic lock are enforced in one place and
// a panel action is indistinguishable from an API one in the audit trail.

// botVerificationRead mounts a route behind a session and botverification.review.
func (s *server) botVerificationRead(handler http.HandlerFunc) http.Handler {
	return s.requireAuthAPI(s.requirePermission(permissionBotVerificationReview, handler))
}

// botVerificationManage mounts a route behind a session and botverification.manage.
//
// The manage right is checked on its own rather than on top of review: appointing
// a verifier and working its queue are different jobs, so an operator may hold
// either without the other.
func (s *server) botVerificationManage(handler http.HandlerFunc) http.Handler {
	return s.requireAuthAPI(s.requirePermission(permissionBotVerificationManage, handler))
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

func (s *server) handleBotVerifiersAPI(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, err := parseInt(query.Get("limit"))
	if err != nil || limit < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid limit")
		return
	}
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	rows, err := s.read.ListBotVerifiers(r.Context(), queryFlag(query.Get("enabled_only")), limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (s *server) handleVerificationIconsAPI(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, err := parseInt(query.Get("limit"))
	if err != nil || limit < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid limit")
		return
	}
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	rows, err := s.read.ListVerificationIcons(r.Context(), queryFlag(query.Get("active_only")), limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// handleCustomVerificationsAPI pages granted marks. The filter is validated before
// the read store is consulted: a malformed query is a 400 whether or not the
// database happens to be reachable.
func (s *server) handleCustomVerificationsAPI(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	peerType := strings.TrimSpace(query.Get("peer_type"))
	if !validMarkablePeerType(peerType) {
		writeAPIError(w, http.StatusBadRequest, "invalid peer_type")
		return
	}
	verifierBotID, err := parseInt64(query.Get("verifier_bot_id"))
	if err != nil || verifierBotID < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid verifier_bot_id")
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
	rows, hasMore, err := s.read.ListCustomVerifications(r.Context(), verifierBotID, peerType, query.Get("q"), beforeID, limit)
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

func (s *server) handleCustomVerificationRequestsAPI(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	status := strings.TrimSpace(query.Get("status"))
	if status != "" && !domain.CustomVerificationRequestStatus(status).Valid() {
		writeAPIError(w, http.StatusBadRequest, "invalid status")
		return
	}
	peerType := strings.TrimSpace(query.Get("peer_type"))
	if !validMarkablePeerType(peerType) {
		writeAPIError(w, http.StatusBadRequest, "invalid peer_type")
		return
	}
	verifierBotID, err := parseInt64(query.Get("verifier_bot_id"))
	if err != nil || verifierBotID < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid verifier_bot_id")
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
	rows, hasMore, err := s.read.ListCustomVerificationRequests(
		r.Context(), status, verifierBotID, peerType, query.Get("q"), beforeID, limit,
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

func (s *server) handleCustomVerificationRequestDetailAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := botVerificationPathID(w, r)
	if !ok {
		return
	}
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	detail, err := s.read.CustomVerificationRequestDetail(r.Context(), id)
	if err != nil {
		if errors.Is(err, errReadNotFound) {
			writeAPIError(w, http.StatusNotFound, "custom verification request not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"request":  detail.Request,
		"verifier": detail.Verifier,
		// mark_active describes the peer as it is now, not as the status implies: a
		// reviewer has to see that an approved mark was since stripped by the
		// operator before deciding anything else about it.
		"mark_active": detail.MarkActive,
	})
}

func (s *server) handleCustomVerificationCountsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	counts, err := s.read.CustomVerificationRequestCounts(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

// ---------------------------------------------------------------------------
// Queue decisions
// ---------------------------------------------------------------------------

// botVerificationDecisionAPIRequest is the decision payload shared by the three
// per-application actions. version is the optimistic-locking token the reviewer
// read; internal_note is operator-only and is not part of what the applicant is
// told. It is optional everywhere, so one panel form can drive all three actions
// without tripping the strict decoder.
type botVerificationDecisionAPIRequest struct {
	CommandID    string    `json:"command_id"`
	Reason       string    `json:"reason"`
	Confirm      bool      `json:"confirm"`
	Version      flexInt64 `json:"version"`
	InternalNote string    `json:"internal_note"`
}

func (s *server) handleApproveBotVerificationAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := botVerificationPathID(w, r)
	if !ok {
		return
	}
	var body botVerificationDecisionAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.ApproveBotVerificationRequest{
		CommandMeta:  s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "approve-bot-verification"),
		RequestID:    id,
		Version:      body.Version.Int64(),
		InternalNote: body.InternalNote,
	}
	result, status, err := s.callAdminCommand(r.Context(), botVerificationDecisionPath(id, "approve"), req)
	writeBotVerificationResultAPI(w, result, status, err)
}

func (s *server) handleRejectBotVerificationAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := botVerificationPathID(w, r)
	if !ok {
		return
	}
	var body botVerificationDecisionAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.RejectBotVerificationRequest{
		CommandMeta:  s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "reject-bot-verification"),
		RequestID:    id,
		Version:      body.Version.Int64(),
		InternalNote: body.InternalNote,
	}
	result, status, err := s.callAdminCommand(r.Context(), botVerificationDecisionPath(id, "reject"), req)
	writeBotVerificationResultAPI(w, result, status, err)
}

func (s *server) handleRevokeBotVerificationAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := botVerificationPathID(w, r)
	if !ok {
		return
	}
	var body botVerificationDecisionAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.RevokeBotVerificationRequest{
		CommandMeta:  s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "revoke-bot-verification"),
		RequestID:    id,
		Version:      body.Version.Int64(),
		InternalNote: body.InternalNote,
	}
	result, status, err := s.callAdminCommand(r.Context(), botVerificationDecisionPath(id, "revoke"), req)
	writeBotVerificationResultAPI(w, result, status, err)
}

// ---------------------------------------------------------------------------
// Operator actions
// ---------------------------------------------------------------------------

// grantBotVerifierAPIRequest appoints a bot as a verifier or reconfigures one.
// version is 0 for a new grant and the token the operator read for an update, so
// two operators editing the same verifier cannot clobber each other. enabled is
// deliberately absent: the kill switch is its own action.
type grantBotVerifierAPIRequest struct {
	CommandID                  string    `json:"command_id"`
	Reason                     string    `json:"reason"`
	Confirm                    bool      `json:"confirm"`
	BotID                      flexInt64 `json:"bot_id"`
	IconDocumentID             flexInt64 `json:"icon_document_id"`
	CompanyName                string    `json:"company_name"`
	DefaultDescription         string    `json:"default_description"`
	CanModifyCustomDescription bool      `json:"can_modify_custom_description"`
	Version                    flexInt64 `json:"version"`
}

func (s *server) handleGrantBotVerifierAPI(w http.ResponseWriter, r *http.Request) {
	var body grantBotVerifierAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	if body.BotID.Int64() <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid bot_id")
		return
	}
	if body.IconDocumentID.Int64() <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid icon_document_id")
		return
	}
	if strings.TrimSpace(body.CompanyName) == "" {
		writeAPIError(w, http.StatusBadRequest, "company_name is required")
		return
	}
	if body.Version.Int64() < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid version")
		return
	}
	req := admin.GrantBotVerifierRequest{
		CommandMeta:                s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "grant-bot-verifier"),
		BotID:                      body.BotID.Int64(),
		IconDocumentID:             body.IconDocumentID.Int64(),
		CompanyName:                body.CompanyName,
		DefaultDescription:         body.DefaultDescription,
		CanModifyCustomDescription: body.CanModifyCustomDescription,
		Version:                    body.Version.Int64(),
	}
	result, status, err := s.callAdminCommand(r.Context(), "/v1/botverification/verifiers/grant", req)
	writeBotVerificationResultAPI(w, result, status, err)
}

type setBotVerifierEnabledAPIRequest struct {
	CommandID string    `json:"command_id"`
	Reason    string    `json:"reason"`
	Confirm   bool      `json:"confirm"`
	BotID     flexInt64 `json:"bot_id"`
	Enabled   bool      `json:"enabled"`
}

func (s *server) handleSetBotVerifierEnabledAPI(w http.ResponseWriter, r *http.Request) {
	var body setBotVerifierEnabledAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	if body.BotID.Int64() <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid bot_id")
		return
	}
	req := admin.SetBotVerifierEnabledRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-bot-verifier-enabled"),
		BotID:       body.BotID.Int64(),
		Enabled:     body.Enabled,
	}
	result, status, err := s.callAdminCommand(r.Context(), "/v1/botverification/verifiers/set-enabled", req)
	writeBotVerificationResultAPI(w, result, status, err)
}

type revokeBotVerifierAPIRequest struct {
	CommandID string    `json:"command_id"`
	Reason    string    `json:"reason"`
	Confirm   bool      `json:"confirm"`
	BotID     flexInt64 `json:"bot_id"`
}

func (s *server) handleRevokeBotVerifierAPI(w http.ResponseWriter, r *http.Request) {
	var body revokeBotVerifierAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	if body.BotID.Int64() <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid bot_id")
		return
	}
	req := admin.RevokeBotVerifierRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "revoke-bot-verifier"),
		BotID:       body.BotID.Int64(),
	}
	result, status, err := s.callAdminCommand(r.Context(), "/v1/botverification/verifiers/revoke", req)
	writeBotVerificationResultAPI(w, result, status, err)
}

// upsertVerificationIconAPIRequest adds or updates a catalogue entry. owner_bot_id
// is optional: absent (or 0) means a shared entry any verifier may use.
type upsertVerificationIconAPIRequest struct {
	CommandID  string    `json:"command_id"`
	Reason     string    `json:"reason"`
	Confirm    bool      `json:"confirm"`
	DocumentID flexInt64 `json:"document_id"`
	Name       string    `json:"name"`
	OwnerBotID flexInt64 `json:"owner_bot_id"`
}

func (s *server) handleUpsertVerificationIconAPI(w http.ResponseWriter, r *http.Request) {
	var body upsertVerificationIconAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	if body.DocumentID.Int64() <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid document_id")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeAPIError(w, http.StatusBadRequest, "name is required")
		return
	}
	if body.OwnerBotID.Int64() < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid owner_bot_id")
		return
	}
	req := admin.UpsertVerificationIconRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "upsert-verification-icon"),
		DocumentID:  body.DocumentID.Int64(),
		Name:        body.Name,
		OwnerBotID:  body.OwnerBotID.Int64(),
	}
	result, status, err := s.callAdminCommand(r.Context(), "/v1/botverification/icons/upsert", req)
	writeBotVerificationResultAPI(w, result, status, err)
}

type setVerificationIconActiveAPIRequest struct {
	CommandID string    `json:"command_id"`
	Reason    string    `json:"reason"`
	Confirm   bool      `json:"confirm"`
	IconID    flexInt64 `json:"icon_id"`
	Active    bool      `json:"active"`
}

func (s *server) handleSetVerificationIconActiveAPI(w http.ResponseWriter, r *http.Request) {
	var body setVerificationIconActiveAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	if body.IconID.Int64() <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid icon_id")
		return
	}
	req := admin.SetVerificationIconActiveRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "set-verification-icon-active"),
		IconID:      body.IconID.Int64(),
		Active:      body.Active,
	}
	result, status, err := s.callAdminCommand(r.Context(), "/v1/botverification/icons/set-active", req)
	writeBotVerificationResultAPI(w, result, status, err)
}

// revokeCustomVerificationAPIRequest strips one verifier's mark from a peer. It
// addresses the (verifier, peer) pair rather than an application, because the
// operator may have to strip a mark no application ever produced.
type revokeCustomVerificationAPIRequest struct {
	CommandID     string    `json:"command_id"`
	Reason        string    `json:"reason"`
	Confirm       bool      `json:"confirm"`
	VerifierBotID flexInt64 `json:"verifier_bot_id"`
	PeerType      string    `json:"peer_type"`
	PeerID        flexInt64 `json:"peer_id"`
}

func (s *server) handleRevokeCustomVerificationAPI(w http.ResponseWriter, r *http.Request) {
	var body revokeCustomVerificationAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	if body.VerifierBotID.Int64() <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid verifier_bot_id")
		return
	}
	peerType := strings.TrimSpace(body.PeerType)
	if peerType == "" || !validMarkablePeerType(peerType) {
		writeAPIError(w, http.StatusBadRequest, "invalid peer_type")
		return
	}
	if body.PeerID.Int64() <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid peer_id")
		return
	}
	req := admin.RevokeCustomVerificationRequest{
		CommandMeta:   s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "revoke-custom-verification"),
		VerifierBotID: body.VerifierBotID.Int64(),
		PeerType:      domain.PeerType(peerType),
		PeerID:        body.PeerID.Int64(),
	}
	result, status, err := s.callAdminCommand(r.Context(), "/v1/botverification/marks/revoke", req)
	writeBotVerificationResultAPI(w, result, status, err)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func botVerificationPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := parseInt64(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func botVerificationDecisionPath(requestID int64, action string) string {
	return "/v1/botverification/requests/" + strconv.FormatInt(requestID, 10) + "/" + action
}

// validMarkablePeerType accepts the peer kinds a third-party mark can sit on, plus
// the empty string for "no filter". An unmodelled value is refused rather than
// silently returning nothing, so a typo is reported.
func validMarkablePeerType(peerType string) bool {
	switch domain.PeerType(peerType) {
	case "", domain.PeerTypeUser, domain.PeerTypeChannel:
		return true
	default:
		return false
	}
}

// queryFlag reads a boolean query flag the way the panel writes it.
func queryFlag(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// writeBotVerificationResultAPI relays the admin API's own status to the browser.
//
// The generic action handlers flatten every upstream failure into 502, which is
// fine when the only failure mode is "bad request". These have more: 409 when
// another operator changed the row first or a verifier hit its mark bound, and 404
// for a row that is gone. Those have to reach the panel intact, because 409 is the
// one failure it resolves by reloading rather than by asking the operator to change
// something.
func writeBotVerificationResultAPI(w http.ResponseWriter, result admin.CommandResult, status int, err error) {
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
