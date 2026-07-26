package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
)

// Third-party bot verification for the operator (core.telegram.org/api/bots/verification).
//
// This is NOT the official platform badge. The official review lives in
// verification.go behind the verification.* permissions and writes
// verification_applications / the peer's `verified` flag; this file drives the
// third-party mechanism, whose state is verification_icons,
// bot_verifier_settings, custom_verifications and custom_verification_requests,
// behind the botverification.* permissions. Neither section reads the other's
// tables, and a third-party verifier must never be able to mint a platform
// checkmark -- so nothing here ever touches domain.User.Verified.
//
// The operator surface has two halves, and they carry different rights:
//
//   - Configuration (botverification.manage): who is a verifier at all, which
//     icons exist, and stripping a granted mark. These are the actions that
//     decide how much a mark is worth, so they are deliberately not implied by
//     the right to work the queue.
//   - Review (botverification.review): deciding the applications filed with a
//     verifier bot.
//
// Every mutation goes through runCommand, so a decision is journalled in
// admin_commands / admin_audit_logs, replayable by command id and rehearsable
// with a dry run. A dry run never mutates and never predicts an outcome stricter
// than the real command's: where a check can only run inside the use-case layer
// (the icon's custom emoji document is resolved against the document store) the
// dry run says so in its details instead of guessing.

// BotVerificationService is the operator-facing slice of the third-party
// verification use cases. It is the exact method set *app/botverification.Service
// exposes for this surface, so the admin layer never reaches into the store.
type BotVerificationService interface {
	// Icon catalogue.
	Icons(ctx context.Context, activeOnly bool, limit int) ([]domain.VerificationIcon, error)
	UpsertIcon(ctx context.Context, icon domain.VerificationIcon) (domain.VerificationIcon, error)
	SetIconActive(ctx context.Context, iconID int64, active bool) (domain.VerificationIcon, error)

	// Verifier status.
	Verifiers(ctx context.Context, enabledOnly bool, limit int) ([]domain.BotVerifierSettings, error)
	VerifierSettings(ctx context.Context, botID int64) (domain.BotVerifierSettings, error)
	GrantVerifier(ctx context.Context, settings domain.BotVerifierSettings) (domain.BotVerifierSettings, error)
	SetVerifierEnabled(ctx context.Context, botID int64, enabled bool) (domain.BotVerifierSettings, error)
	RevokeVerifier(ctx context.Context, botID int64) (bool, error)

	// Granted marks.
	Marks(ctx context.Context, filter domain.CustomVerificationFilter) ([]domain.CustomVerification, error)
	RevokeMark(ctx context.Context, verifierBotID int64, peer domain.Peer) (bool, error)

	// Application queue.
	Requests(ctx context.Context, filter domain.CustomVerificationRequestFilter) ([]domain.CustomVerificationRequest, error)
	Request(ctx context.Context, requestID int64) (domain.CustomVerificationRequest, error)
	RequestCounts(ctx context.Context) (map[domain.CustomVerificationRequestStatus]int64, error)
	Approve(ctx context.Context, requestID, version int64, decidedBy, reason, note string) (domain.CustomVerificationRequest, bool, error)
	Reject(ctx context.Context, requestID, version int64, decidedBy, reason, note string) (domain.CustomVerificationRequest, bool, error)
	RevokeRequest(ctx context.Context, requestID, version int64, decidedBy, reason, note string) (domain.CustomVerificationRequest, bool, error)
}

// botVerificationCatalogueScan bounds the catalogue page a dry run reads to
// pre-check an icon. The catalogue is operator-curated and small; the check is
// advisory precisely because this bound may not cover it (see grantIconPreflight).
const botVerificationCatalogueScan = 200

// ---------------------------------------------------------------------------
// Command payloads
// ---------------------------------------------------------------------------

// GrantBotVerifierRequest grants or reconfigures a bot's verifier status.
//
// Version is the optimistic-locking token: 0 means "this is a new grant" and a
// positive value means "update the row I read", so two operators editing the same
// verifier cannot clobber each other. Enabled is deliberately absent -- the kill
// switch is its own action, so reconfiguring a switched-off verifier does not
// quietly switch it back on.
type GrantBotVerifierRequest struct {
	CommandMeta
	BotID                      int64  `json:"bot_id"`
	IconDocumentID             int64  `json:"icon_document_id"`
	CompanyName                string `json:"company_name"`
	DefaultDescription         string `json:"default_description"`
	CanModifyCustomDescription bool   `json:"can_modify_custom_description"`
	Version                    int64  `json:"version"`
}

// SetBotVerifierEnabledRequest flips the operator kill switch. The verifier keeps
// its row and the marks it granted, so flipping it back restores exactly what was
// there.
type SetBotVerifierEnabledRequest struct {
	CommandMeta
	BotID   int64 `json:"bot_id"`
	Enabled bool  `json:"enabled"`
}

// RevokeBotVerifierRequest removes verifier status entirely. Its marks cascade
// away with it in the store, because a mark whose verifier no longer exists has
// nothing to render.
type RevokeBotVerifierRequest struct {
	CommandMeta
	BotID int64 `json:"bot_id"`
}

// UpsertVerificationIconRequest adds or updates a catalogue entry, keyed by custom
// emoji document id. OwnerBotID is zero for a shared entry and a bot id when the
// operator reserves the icon for one verifier.
type UpsertVerificationIconRequest struct {
	CommandMeta
	DocumentID int64  `json:"document_id"`
	Name       string `json:"name"`
	OwnerBotID int64  `json:"owner_bot_id,omitempty"`
}

// SetVerificationIconActiveRequest retires or restores a catalogue entry. Marks
// already granted with it keep rendering -- the icon id is denormalised onto the
// mark -- so retiring an entry stops new grants without blanking existing badges.
type SetVerificationIconActiveRequest struct {
	CommandMeta
	IconID int64 `json:"icon_id"`
	Active bool  `json:"active"`
}

// RevokeCustomVerificationRequest strips one verifier's mark from a peer on the
// operator's behalf. It addresses the (verifier, peer) pair rather than an
// application, because the operator may have to strip a mark that no application
// ever produced.
type RevokeCustomVerificationRequest struct {
	CommandMeta
	VerifierBotID int64           `json:"verifier_bot_id"`
	PeerType      domain.PeerType `json:"peer_type"`
	PeerID        int64           `json:"peer_id"`
}

// ApproveBotVerificationRequest grants the mark an application asked for. The
// mark and the approved status commit together in the use-case layer, so an
// approved application without its mark is not a reachable state.
type ApproveBotVerificationRequest struct {
	CommandMeta
	RequestID int64 `json:"request_id"`
	Version   int64 `json:"version"`
	// InternalNote is operator-only: it is journalled and stored on the
	// application, and it is never part of what the applicant is told.
	InternalNote string `json:"internal_note,omitempty"`
}

// RejectBotVerificationRequest closes an application against the applicant.
// Reason is mandatory: it is the text the applicant receives.
type RejectBotVerificationRequest struct {
	CommandMeta
	RequestID    int64  `json:"request_id"`
	Version      int64  `json:"version"`
	InternalNote string `json:"internal_note,omitempty"`
}

// RevokeBotVerificationRequest withdraws a granted mark through the application it
// came from. The application stays as history -- revoked is reachable only from
// approved, so it keeps meaning "was verified once". Reason is mandatory for the
// same reason it is on rejection.
type RevokeBotVerificationRequest struct {
	CommandMeta
	RequestID    int64  `json:"request_id"`
	Version      int64  `json:"version"`
	InternalNote string `json:"internal_note,omitempty"`
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// BotVerifiers lists verifier bots.
func (s *Service) BotVerifiers(ctx context.Context, enabledOnly bool, limit int) ([]domain.BotVerifierSettings, error) {
	if s == nil || s.botVerification == nil {
		return nil, errBotVerificationNotConfigured
	}
	return s.botVerification.Verifiers(ctx, enabledOnly, limit)
}

// BotVerifier resolves one verifier's status, enabled or not: the panel needs the
// disabled row too, to render the kill switch.
func (s *Service) BotVerifier(ctx context.Context, botID int64) (domain.BotVerifierSettings, error) {
	if s == nil || s.botVerification == nil {
		return domain.BotVerifierSettings{}, errBotVerificationNotConfigured
	}
	if botID <= 0 {
		return domain.BotVerifierSettings{}, botVerificationCoded(domain.ErrVerifierNotFound)
	}
	return s.botVerification.VerifierSettings(ctx, botID)
}

// VerificationIcons lists the icon catalogue, newest first.
func (s *Service) VerificationIcons(ctx context.Context, activeOnly bool, limit int) ([]domain.VerificationIcon, error) {
	if s == nil || s.botVerification == nil {
		return nil, errBotVerificationNotConfigured
	}
	return s.botVerification.Icons(ctx, activeOnly, limit)
}

// CustomVerifications lists granted third-party marks with keyset paging.
func (s *Service) CustomVerifications(ctx context.Context, filter domain.CustomVerificationFilter) ([]domain.CustomVerification, error) {
	if s == nil || s.botVerification == nil {
		return nil, errBotVerificationNotConfigured
	}
	return s.botVerification.Marks(ctx, filter)
}

// CustomVerificationRequests is the third-party review queue.
func (s *Service) CustomVerificationRequests(ctx context.Context, filter domain.CustomVerificationRequestFilter) ([]domain.CustomVerificationRequest, error) {
	if s == nil || s.botVerification == nil {
		return nil, errBotVerificationNotConfigured
	}
	return s.botVerification.Requests(ctx, filter)
}

// CustomVerificationRequest resolves one application by identity.
func (s *Service) CustomVerificationRequest(ctx context.Context, requestID int64) (domain.CustomVerificationRequest, error) {
	if s == nil || s.botVerification == nil {
		return domain.CustomVerificationRequest{}, errBotVerificationNotConfigured
	}
	if requestID <= 0 {
		return domain.CustomVerificationRequest{}, botVerificationCoded(domain.ErrCustomVerificationRequestNotFound)
	}
	return s.botVerification.Request(ctx, requestID)
}

// CustomVerificationRequestCounts is the queue summary rendered above the list.
func (s *Service) CustomVerificationRequestCounts(ctx context.Context) (map[domain.CustomVerificationRequestStatus]int64, error) {
	if s == nil || s.botVerification == nil {
		return nil, errBotVerificationNotConfigured
	}
	return s.botVerification.RequestCounts(ctx)
}

// CustomVerificationMarkActive reports whether a (verifier, peer) pair currently
// carries a mark. The request detail view needs it to tell "approved" apart from
// "approved and since stripped by the operator".
func (s *Service) CustomVerificationMarkActive(ctx context.Context, verifierBotID int64, peer domain.Peer) (bool, error) {
	if s == nil || s.botVerification == nil {
		return false, errBotVerificationNotConfigured
	}
	if verifierBotID <= 0 || peer.ID <= 0 {
		return false, nil
	}
	marks, err := s.botVerification.Marks(ctx, domain.CustomVerificationFilter{
		VerifierBotID: verifierBotID,
		PeerType:      peer.Type,
		PeerID:        peer.ID,
		Limit:         1,
	})
	if err != nil {
		return false, botVerificationError(err)
	}
	return len(marks) > 0, nil
}

// ---------------------------------------------------------------------------
// Commands: verifier status
// ---------------------------------------------------------------------------

// GrantBotVerifier grants or reconfigures verifier status.
//
// The dry run predicts everything it can without writing: the payload shape, the
// optimistic lock against the row as it is now, and -- when the icon is visible in
// the catalogue page it reads -- that the icon is active and usable by this bot.
// It deliberately does not fail an icon it cannot see: the use-case layer resolves
// the icon's custom emoji document against the document store, and a dry run that
// refused more than the real command would make a rehearsal useless.
func (s *Service) GrantBotVerifier(ctx context.Context, req GrantBotVerifierRequest) (CommandResult, error) {
	if s == nil || s.botVerification == nil {
		return CommandResult{}, errBotVerificationNotConfigured
	}
	if req.BotID <= 0 {
		return CommandResult{}, botVerificationCoded(domain.ErrVerifierNotFound)
	}
	if req.Version < 0 {
		return CommandResult{}, botVerifierInvalid("version must not be negative")
	}
	settings := domain.BotVerifierSettings{
		BotID:                      req.BotID,
		IconDocumentID:             req.IconDocumentID,
		CompanyName:                strings.TrimSpace(req.CompanyName),
		DefaultDescription:         strings.TrimSpace(req.DefaultDescription),
		CanModifyCustomDescription: req.CanModifyCustomDescription,
		GrantedBy:                  strings.TrimSpace(req.Actor),
		GrantReason:                strings.TrimSpace(req.Reason),
		Enabled:                    true,
		Version:                    req.Version,
	}
	if err := settings.Validate(); err != nil {
		return CommandResult{}, botVerificationError(err)
	}
	target := domain.Peer{Type: domain.PeerTypeUser, ID: req.BotID}
	return s.runCommand(ctx, req.CommandMeta, ActionGrantBotVerifier, req.BotID, target, req, func() (CommandResult, error) {
		details := map[string]any{
			"bot_id":                        strconv.FormatInt(req.BotID, 10),
			"icon_document_id":              strconv.FormatInt(req.IconDocumentID, 10),
			"company_name":                  settings.CompanyName,
			"can_modify_custom_description": req.CanModifyCustomDescription,
			"correlation_id":                strings.TrimSpace(req.CommandID),
		}
		current, exists, err := s.botVerifierState(ctx, req.BotID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["created"] = !exists
		if exists {
			details["previous_version"] = strconv.FormatInt(current.Version, 10)
			details["previous_icon_document_id"] = strconv.FormatInt(current.IconDocumentID, 10)
			details["previous_enabled"] = current.Enabled
			// The kill switch is its own action, so reconfiguring a switched-off
			// verifier must not switch it back on.
			settings.Enabled = current.Enabled
			if req.Version != current.Version {
				return CommandResult{Details: details}, codedError(CodeCustomVerificationConflict, domain.ErrCustomVerificationVersionConflict)
			}
		} else if req.Version != 0 {
			// The operator is editing a row that is no longer there.
			return CommandResult{Details: details}, botVerificationCoded(domain.ErrVerifierNotFound)
		}
		details["enabled"] = settings.Enabled
		if err := s.grantIconPreflight(ctx, details, req.BotID, req.IconDocumentID); err != nil {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			message := "bot verifier grant validated"
			if exists {
				message = "bot verifier update validated"
			}
			return CommandResult{Message: message, Details: details}, nil
		}
		stored, err := s.botVerification.GrantVerifier(ctx, settings)
		if err != nil {
			return CommandResult{Details: details}, botVerificationError(err)
		}
		details["version"] = strconv.FormatInt(stored.Version, 10)
		details["enabled"] = stored.Enabled
		details["changed"] = true
		message := "bot verifier status granted"
		if exists {
			message = "bot verifier status updated"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// SetBotVerifierEnabled flips the operator kill switch.
func (s *Service) SetBotVerifierEnabled(ctx context.Context, req SetBotVerifierEnabledRequest) (CommandResult, error) {
	if s == nil || s.botVerification == nil {
		return CommandResult{}, errBotVerificationNotConfigured
	}
	if req.BotID <= 0 {
		return CommandResult{}, botVerificationCoded(domain.ErrVerifierNotFound)
	}
	target := domain.Peer{Type: domain.PeerTypeUser, ID: req.BotID}
	return s.runCommand(ctx, req.CommandMeta, ActionSetBotVerifierEnabled, req.BotID, target, req, func() (CommandResult, error) {
		details := map[string]any{
			"bot_id":         strconv.FormatInt(req.BotID, 10),
			"enabled":        req.Enabled,
			"correlation_id": strings.TrimSpace(req.CommandID),
		}
		current, exists, err := s.botVerifierState(ctx, req.BotID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if !exists {
			return CommandResult{Details: details}, botVerificationCoded(domain.ErrVerifierNotFound)
		}
		details["previous_enabled"] = current.Enabled
		details["previous_version"] = strconv.FormatInt(current.Version, 10)
		details["company_name"] = current.CompanyName
		// A no-op flip neither burns a version nor pushes an update, so the audit
		// trail does not claim a change that never happened.
		noop := current.Enabled == req.Enabled
		details["changed"] = !noop
		if req.DryRun {
			message := "bot verifier switch validated"
			if noop {
				message = "bot verifier switch validated; already in that state"
			}
			return CommandResult{Message: message, Details: details}, nil
		}
		stored, err := s.botVerification.SetVerifierEnabled(ctx, req.BotID, req.Enabled)
		if err != nil {
			return CommandResult{Details: details}, botVerificationError(err)
		}
		details["version"] = strconv.FormatInt(stored.Version, 10)
		details["enabled"] = stored.Enabled
		message := "bot verifier enabled"
		if !req.Enabled {
			message = "bot verifier disabled"
		}
		if noop {
			message = "bot verifier was already in that state"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// RevokeBotVerifier removes verifier status entirely.
//
// A missing row is not an error: the command answers changed=false, so a panel
// retry after a lost response is harmless and the dry run never refuses what the
// real command accepts.
func (s *Service) RevokeBotVerifier(ctx context.Context, req RevokeBotVerifierRequest) (CommandResult, error) {
	if s == nil || s.botVerification == nil {
		return CommandResult{}, errBotVerificationNotConfigured
	}
	if req.BotID <= 0 {
		return CommandResult{}, botVerificationCoded(domain.ErrVerifierNotFound)
	}
	target := domain.Peer{Type: domain.PeerTypeUser, ID: req.BotID}
	return s.runCommand(ctx, req.CommandMeta, ActionRevokeBotVerifier, req.BotID, target, req, func() (CommandResult, error) {
		details := map[string]any{
			"bot_id":         strconv.FormatInt(req.BotID, 10),
			"correlation_id": strings.TrimSpace(req.CommandID),
		}
		current, exists, err := s.botVerifierState(ctx, req.BotID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["present"] = exists
		if exists {
			details["previous_version"] = strconv.FormatInt(current.Version, 10)
			details["previous_enabled"] = current.Enabled
			details["company_name"] = current.CompanyName
			// The marks this verifier granted cascade away with the row, which is
			// the fact an operator most needs stated before confirming.
			details["mark_count"] = s.botVerifierMarkCount(ctx, req.BotID)
		}
		if req.DryRun {
			message := "bot verifier revoke validated"
			if !exists {
				message = "bot verifier revoke validated; the bot is not a verifier"
			}
			return CommandResult{Message: message, Details: details}, nil
		}
		removed, err := s.botVerification.RevokeVerifier(ctx, req.BotID)
		if err != nil {
			return CommandResult{Details: details}, botVerificationError(err)
		}
		details["changed"] = removed
		message := "bot verifier status revoked"
		if !removed {
			message = "bot was not a verifier"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// ---------------------------------------------------------------------------
// Commands: icon catalogue
// ---------------------------------------------------------------------------

// UpsertVerificationIcon adds or updates a catalogue entry.
//
// The use-case layer resolves the custom emoji document before writing, because an
// entry pointing at a document no client can fetch renders as *nothing*: the peer
// looks unverified while the server insists it is marked. That probe needs the
// document store, so it runs on execution only; the dry run validates the shape.
func (s *Service) UpsertVerificationIcon(ctx context.Context, req UpsertVerificationIconRequest) (CommandResult, error) {
	if s == nil || s.botVerification == nil {
		return CommandResult{}, errBotVerificationNotConfigured
	}
	icon := domain.VerificationIcon{
		DocumentID: req.DocumentID,
		OwnerBotID: req.OwnerBotID,
		Name:       strings.TrimSpace(req.Name),
		Active:     true,
	}
	if err := icon.Validate(); err != nil {
		return CommandResult{}, botVerificationError(err)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionUpsertVerificationIcon, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"document_id":    strconv.FormatInt(req.DocumentID, 10),
			"owner_bot_id":   strconv.FormatInt(req.OwnerBotID, 10),
			"name":           icon.Name,
			"correlation_id": strings.TrimSpace(req.CommandID),
		}
		if req.DryRun {
			return CommandResult{Message: "verification icon validated", Details: details}, nil
		}
		stored, err := s.botVerification.UpsertIcon(ctx, icon)
		if err != nil {
			return CommandResult{Details: details}, botVerificationError(err)
		}
		details["icon_id"] = strconv.FormatInt(stored.ID, 10)
		details["active"] = stored.Active
		details["changed"] = true
		return CommandResult{Message: "verification icon stored", Details: details}, nil
	})
}

// SetVerificationIconActive retires or restores a catalogue entry.
func (s *Service) SetVerificationIconActive(ctx context.Context, req SetVerificationIconActiveRequest) (CommandResult, error) {
	if s == nil || s.botVerification == nil {
		return CommandResult{}, errBotVerificationNotConfigured
	}
	if req.IconID <= 0 {
		return CommandResult{}, botVerificationCoded(domain.ErrVerificationIconNotFound)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetVerificationIconActive, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"icon_id":        strconv.FormatInt(req.IconID, 10),
			"active":         req.Active,
			"correlation_id": strings.TrimSpace(req.CommandID),
		}
		if req.DryRun {
			return CommandResult{Message: "verification icon switch validated", Details: details}, nil
		}
		stored, err := s.botVerification.SetIconActive(ctx, req.IconID, req.Active)
		if err != nil {
			return CommandResult{Details: details}, botVerificationError(err)
		}
		details["document_id"] = strconv.FormatInt(stored.DocumentID, 10)
		details["name"] = stored.Name
		details["active"] = stored.Active
		details["changed"] = true
		message := "verification icon activated"
		if !req.Active {
			// Marks already granted with it keep rendering: the icon id is
			// denormalised onto the mark.
			message = "verification icon retired"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// ---------------------------------------------------------------------------
// Commands: granted marks
// ---------------------------------------------------------------------------

// RevokeCustomVerification strips one verifier's mark from a peer.
//
// The peer is checked for shape only, never for existence: an operator must still
// be able to strip a mark from a peer that has since been deleted or become
// unresolvable, which is exactly when stripping it matters most.
func (s *Service) RevokeCustomVerification(ctx context.Context, req RevokeCustomVerificationRequest) (CommandResult, error) {
	if s == nil || s.botVerification == nil {
		return CommandResult{}, errBotVerificationNotConfigured
	}
	if req.VerifierBotID <= 0 {
		return CommandResult{}, botVerificationCoded(domain.ErrVerifierNotFound)
	}
	peer := domain.Peer{Type: req.PeerType, ID: req.PeerID}
	if !markableAdminPeer(peer) {
		return CommandResult{}, botVerificationCoded(domain.ErrCustomVerificationTargetInvalid)
	}
	targetUserID := int64(0)
	if peer.Type == domain.PeerTypeUser {
		targetUserID = peer.ID
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRevokeCustomVerification, targetUserID, peer, req, func() (CommandResult, error) {
		details := map[string]any{
			"verifier_bot_id": strconv.FormatInt(req.VerifierBotID, 10),
			"peer_type":       string(peer.Type),
			"peer_id":         strconv.FormatInt(peer.ID, 10),
			"correlation_id":  strings.TrimSpace(req.CommandID),
		}
		present, err := s.CustomVerificationMarkActive(ctx, req.VerifierBotID, peer)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["mark_present"] = present
		if req.DryRun {
			message := "custom verification revoke validated"
			if !present {
				message = "custom verification revoke validated; the peer carries no mark from this verifier"
			}
			return CommandResult{Message: message, Details: details}, nil
		}
		removed, err := s.botVerification.RevokeMark(ctx, req.VerifierBotID, peer)
		if err != nil {
			return CommandResult{Details: details}, botVerificationError(err)
		}
		details["changed"] = removed
		message := "custom verification revoked"
		if !removed {
			message = "custom verification was already absent"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// ---------------------------------------------------------------------------
// Commands: application queue
// ---------------------------------------------------------------------------

// ApproveBotVerification grants the mark an application asked for.
func (s *Service) ApproveBotVerification(ctx context.Context, req ApproveBotVerificationRequest) (CommandResult, error) {
	if s == nil || s.botVerification == nil {
		return CommandResult{}, errBotVerificationNotConfigured
	}
	if err := validateBotVerificationDecisionShape(req.RequestID, req.Version, req.InternalNote); err != nil {
		return CommandResult{}, err
	}
	return s.runCommand(ctx, req.CommandMeta, ActionApproveBotVerification, 0, domain.Peer{}, req, func() (CommandResult, error) {
		current, details, err := s.botVerificationSubject(ctx, req.CommandMeta, req.RequestID, domain.CustomVerificationApproved, req.InternalNote)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if err := botVerificationTransition(current, req.Version, domain.CustomVerificationApproved); err != nil {
			return CommandResult{Details: details}, err
		}
		// An application that is already approved is a replay: the use-case layer
		// answers changed=false without re-running the verifier gate, so the dry run
		// must not re-run it either.
		replay := current.Status == domain.CustomVerificationApproved
		if !replay {
			if err := s.mergeVerifierDecisionDetails(ctx, details, current.VerifierBotID, true); err != nil {
				return CommandResult{Details: details}, err
			}
		}
		if req.DryRun {
			return CommandResult{Message: "bot verification approve validated", Details: details}, nil
		}
		stored, changed, err := s.botVerification.Approve(ctx, req.RequestID, req.Version,
			strings.TrimSpace(req.Actor), strings.TrimSpace(req.Reason), strings.TrimSpace(req.InternalNote))
		if err != nil {
			return CommandResult{Details: details}, botVerificationError(err)
		}
		mergeBotVerificationDetails(details, stored, changed)
		message := "bot verification application approved"
		if !changed {
			message = "bot verification application already approved"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// RejectBotVerification closes an application against the applicant. The reason is
// mandatory and is the text the applicant is shown; the internal note stays
// operator-side.
func (s *Service) RejectBotVerification(ctx context.Context, req RejectBotVerificationRequest) (CommandResult, error) {
	if s == nil || s.botVerification == nil {
		return CommandResult{}, errBotVerificationNotConfigured
	}
	if err := validateBotVerificationDecisionShape(req.RequestID, req.Version, req.InternalNote); err != nil {
		return CommandResult{}, err
	}
	// Refused before the journal is touched: the audit trail must never contain a
	// decision nobody can explain.
	if strings.TrimSpace(req.Reason) == "" {
		return CommandResult{}, codedError(CodeCustomVerificationReasonRequired, domain.ErrVerificationReasonRequired)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRejectBotVerification, 0, domain.Peer{}, req, func() (CommandResult, error) {
		current, details, err := s.botVerificationSubject(ctx, req.CommandMeta, req.RequestID, domain.CustomVerificationRejected, req.InternalNote)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if err := botVerificationTransition(current, req.Version, domain.CustomVerificationRejected); err != nil {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			return CommandResult{Message: "bot verification reject validated", Details: details}, nil
		}
		stored, changed, err := s.botVerification.Reject(ctx, req.RequestID, req.Version,
			strings.TrimSpace(req.Actor), strings.TrimSpace(req.Reason), strings.TrimSpace(req.InternalNote))
		if err != nil {
			return CommandResult{Details: details}, botVerificationError(err)
		}
		mergeBotVerificationDetails(details, stored, changed)
		message := "bot verification application rejected"
		if !changed {
			message = "bot verification application already rejected"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// RevokeBotVerification withdraws a granted mark through the application it came
// from. A reason is mandatory for the same reason it is on rejection.
func (s *Service) RevokeBotVerification(ctx context.Context, req RevokeBotVerificationRequest) (CommandResult, error) {
	if s == nil || s.botVerification == nil {
		return CommandResult{}, errBotVerificationNotConfigured
	}
	if err := validateBotVerificationDecisionShape(req.RequestID, req.Version, req.InternalNote); err != nil {
		return CommandResult{}, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return CommandResult{}, codedError(CodeCustomVerificationReasonRequired, domain.ErrVerificationReasonRequired)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRevokeBotVerification, 0, domain.Peer{}, req, func() (CommandResult, error) {
		current, details, err := s.botVerificationSubject(ctx, req.CommandMeta, req.RequestID, domain.CustomVerificationRevoked, req.InternalNote)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if err := botVerificationTransition(current, req.Version, domain.CustomVerificationRevoked); err != nil {
			return CommandResult{Details: details}, err
		}
		// The mark may already be gone -- the operator can strip one directly -- and
		// that is not an error: the application still has to reach "revoked".
		present, err := s.CustomVerificationMarkActive(ctx, current.VerifierBotID, current.Peer)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["mark_present"] = present
		if req.DryRun {
			return CommandResult{Message: "bot verification revoke validated", Details: details}, nil
		}
		stored, changed, err := s.botVerification.RevokeRequest(ctx, req.RequestID, req.Version,
			strings.TrimSpace(req.Actor), strings.TrimSpace(req.Reason), strings.TrimSpace(req.InternalNote))
		if err != nil {
			return CommandResult{Details: details}, botVerificationError(err)
		}
		mergeBotVerificationDetails(details, stored, changed)
		message := "bot verification mark revoked"
		if !changed {
			message = "bot verification application already revoked"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

var errBotVerificationNotConfigured = errors.New("admin bot verification dependency is not configured")

// botVerifierState loads a verifier row and reports whether it exists at all,
// because "not a verifier" is a normal answer for several of these commands
// rather than a failure.
func (s *Service) botVerifierState(ctx context.Context, botID int64) (domain.BotVerifierSettings, bool, error) {
	settings, err := s.botVerification.VerifierSettings(ctx, botID)
	switch {
	case err == nil:
		return settings, settings.BotID == botID, nil
	case errors.Is(err, domain.ErrVerifierNotFound):
		return domain.BotVerifierSettings{}, false, nil
	default:
		return domain.BotVerifierSettings{}, false, botVerificationError(err)
	}
}

// botVerifierMarkCount reports how many marks a verifier holds, bounded by the
// listing page. It is advisory audit detail, so a read failure is reported as an
// unknown count rather than failing the command it is describing.
func (s *Service) botVerifierMarkCount(ctx context.Context, botID int64) any {
	marks, err := s.botVerification.Marks(ctx, domain.CustomVerificationFilter{
		VerifierBotID: botID,
		Limit:         botVerificationCatalogueScan,
	})
	if err != nil {
		return "unknown"
	}
	if len(marks) >= botVerificationCatalogueScan {
		return strconv.Itoa(len(marks)) + "+"
	}
	return strconv.Itoa(len(marks))
}

// grantIconPreflight records what the catalogue says about the icon and refuses
// the definite failures early.
//
// The check is deliberately one-sided. An icon found in the page and unusable is
// a certain failure and is refused with its own code; an icon the page does not
// cover is left to the use-case layer, which reads the entry by document id and
// additionally resolves the custom emoji document. So this can only ever make the
// dry run *more* informative, never stricter than the command it rehearses.
func (s *Service) grantIconPreflight(ctx context.Context, details map[string]any, botID, documentID int64) error {
	icons, err := s.botVerification.Icons(ctx, false, botVerificationCatalogueScan)
	if err != nil {
		details["icon_catalogue_checked"] = false
		return nil
	}
	for _, icon := range icons {
		if icon.DocumentID != documentID {
			continue
		}
		details["icon_catalogue_checked"] = true
		details["icon_id"] = strconv.FormatInt(icon.ID, 10)
		details["icon_name"] = icon.Name
		details["icon_active"] = icon.Active
		details["icon_owner_bot_id"] = strconv.FormatInt(icon.OwnerBotID, 10)
		if !icon.Active {
			return botVerificationCoded(domain.ErrVerificationIconInactive)
		}
		if !icon.UsableBy(botID) {
			// A reserved entry belongs to one verifier; for anybody else it does
			// not exist, which is why this is "not found" and not "forbidden".
			return botVerificationCoded(domain.ErrVerificationIconNotFound)
		}
		return nil
	}
	details["icon_catalogue_checked"] = false
	return nil
}

// botVerificationSubject loads the application under decision and seeds the
// command details with everything the audit entry must state even when the command
// then fails: which application, whose, which verifier, which peer, the status it
// is leaving and the status it was asked to reach.
func (s *Service) botVerificationSubject(
	ctx context.Context,
	meta CommandMeta,
	requestID int64,
	next domain.CustomVerificationRequestStatus,
	internalNote string,
) (domain.CustomVerificationRequest, map[string]any, error) {
	details := map[string]any{
		"request_id":     strconv.FormatInt(requestID, 10),
		"next_status":    string(next),
		"correlation_id": strings.TrimSpace(meta.CommandID),
	}
	if note := strings.TrimSpace(internalNote); note != "" {
		details["internal_note"] = note
	}
	current, err := s.botVerification.Request(ctx, requestID)
	if err != nil {
		return domain.CustomVerificationRequest{}, details, botVerificationError(err)
	}
	if current.ID != requestID {
		return domain.CustomVerificationRequest{}, details, botVerificationCoded(domain.ErrCustomVerificationRequestNotFound)
	}
	details["verifier_bot_id"] = strconv.FormatInt(current.VerifierBotID, 10)
	details["applicant_user_id"] = strconv.FormatInt(current.ApplicantUserID, 10)
	details["peer_type"] = string(current.Peer.Type)
	details["peer_id"] = strconv.FormatInt(current.Peer.ID, 10)
	details["peer_username"] = current.PeerUsername
	details["previous_status"] = string(current.Status)
	details["previous_version"] = strconv.FormatInt(current.Version, 10)
	return current, details, nil
}

// mergeVerifierDecisionDetails records the verifier behind an application and,
// for a decision that would grant a mark, refuses a verifier that may not verify
// right now.
//
// An application can sit in the queue for days: a verifier switched off in the
// meantime must not be able to grant through the review path what the RPC path
// would refuse. "No row" and "switched off" answer the same code on purpose --
// that is the distinction BOTVERIFIER_FORBIDDEN deliberately hides.
func (s *Service) mergeVerifierDecisionDetails(ctx context.Context, details map[string]any, botID int64, requireEnabled bool) error {
	settings, exists, err := s.botVerifierState(ctx, botID)
	if err != nil {
		return err
	}
	details["verifier_present"] = exists
	if exists {
		details["verifier_enabled"] = settings.Enabled
		details["verifier_company_name"] = settings.CompanyName
		details["verifier_icon_document_id"] = strconv.FormatInt(settings.IconDocumentID, 10)
		details["verifier_can_modify_custom_description"] = settings.CanModifyCustomDescription
	}
	if requireEnabled && (!exists || !settings.Enabled) {
		return botVerificationCoded(domain.ErrVerifierForbidden)
	}
	return nil
}

// mergeBotVerificationDetails records the decided state.
func mergeBotVerificationDetails(details map[string]any, req domain.CustomVerificationRequest, changed bool) {
	details["status"] = string(req.Status)
	details["version"] = strconv.FormatInt(req.Version, 10)
	details["changed"] = changed
	details["decided_by"] = req.DecidedBy
	if req.CorrelationID != "" {
		details["correlation_id"] = req.CorrelationID
	}
	if req.DecisionReason != "" {
		details["decision_reason"] = req.DecisionReason
	}
}

// validateBotVerificationDecisionShape rejects a malformed decision before the
// command journal is touched.
func validateBotVerificationDecisionShape(requestID, version int64, internalNote string) error {
	if requestID <= 0 {
		return botVerificationCoded(domain.ErrCustomVerificationRequestNotFound)
	}
	if version <= 0 {
		// Without the version the reviewer never read the row, so the optimistic
		// lock could not protect a concurrent decision.
		return botVerificationInvalid("version is required")
	}
	if utf8.RuneCountInString(internalNote) > domain.MaxCustomVerificationNoteLength {
		return botVerificationInvalid("internal_note is too long")
	}
	return nil
}

// botVerificationTransition checks the status machine and the optimistic lock in
// the order the use-case layer does, so a dry run predicts the real outcome
// exactly.
//
// A request already in the target status is a replay the service answers as a
// no-op, so it passes without consulting the version: re-sending a decision whose
// response was lost must not turn into a spurious conflict.
func botVerificationTransition(req domain.CustomVerificationRequest, version int64, next domain.CustomVerificationRequestStatus) error {
	if req.Status == next {
		return nil
	}
	if !domain.CanTransitionCustomVerificationStatus(req.Status, next) {
		return codedError(CodeCustomVerificationStatusInvalid,
			fmt.Errorf("%w: %s -> %s", domain.ErrCustomVerificationRequestInvalid, req.Status, next))
	}
	if req.Version != version {
		return codedError(CodeCustomVerificationConflict, domain.ErrCustomVerificationVersionConflict)
	}
	return nil
}

// markableAdminPeer mirrors the store's peer_type CHECK: only users (bots
// included) and channels can carry a third-party mark.
func markableAdminPeer(peer domain.Peer) bool {
	switch peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		return peer.ID > 0
	default:
		return false
	}
}

// BotVerificationErrorCode maps a third-party verification failure onto the stable
// code the admin panel switches on. An unmapped error returns "" so the caller can
// report it verbatim instead of inventing a code.
//
// The codes are separate from the official verification ones by design: the two
// mechanisms fail for different reasons and the panel renders them in different
// sections, so sharing a token would make a message land in the wrong place.
func BotVerificationErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, domain.ErrVerifierNotFound):
		return CodeBotVerifierNotFound
	case errors.Is(err, domain.ErrVerifierForbidden):
		return CodeBotVerifierForbidden
	case errors.Is(err, domain.ErrVerifierDescriptionForbidden):
		return CodeBotVerifierDescriptionForbidden
	case errors.Is(err, domain.ErrVerifierSettingsInvalid):
		return CodeBotVerifierInvalid
	case errors.Is(err, domain.ErrVerificationIconNotFound):
		return CodeVerificationIconNotFound
	case errors.Is(err, domain.ErrVerificationIconInactive):
		return CodeVerificationIconInactive
	case errors.Is(err, domain.ErrVerificationIconInvalid):
		return CodeVerificationIconInvalid
	case errors.Is(err, domain.ErrCustomVerificationVersionConflict):
		return CodeCustomVerificationConflict
	case errors.Is(err, domain.ErrCustomVerificationLimit):
		return CodeCustomVerificationLimit
	case errors.Is(err, domain.ErrCustomVerificationNotFound):
		return CodeCustomVerificationNotFound
	case errors.Is(err, domain.ErrCustomVerificationRequestNotFound):
		return CodeCustomVerificationRequestNotFound
	case errors.Is(err, domain.ErrCustomVerificationRequestExists):
		return CodeCustomVerificationRequestExists
	case errors.Is(err, domain.ErrCustomVerificationTargetInvalid):
		return CodeCustomVerificationTargetInvalid
	case errors.Is(err, domain.ErrVerificationTargetSystem):
		return CodeCustomVerificationTargetSystem
	case errors.Is(err, domain.ErrVerificationReasonRequired):
		return CodeCustomVerificationReasonRequired
	case errors.Is(err, domain.ErrVerificationRateLimited):
		return CodeCustomVerificationRateLimited
	case errors.Is(err, domain.ErrBotNotFound):
		return CodeBotVerifierBotNotFound
	case errors.Is(err, domain.ErrCustomVerificationRequestInvalid):
		return CodeCustomVerificationInvalid
	default:
		return ""
	}
}

// botVerificationError prefixes a recognised failure with its stable code, the way
// verificationError does for the official review.
func botVerificationError(err error) error {
	if code := BotVerificationErrorCode(err); code != "" {
		return codedError(code, err)
	}
	return err
}

func botVerificationCoded(err error) error {
	return codedError(BotVerificationErrorCode(err), err)
}

func botVerificationInvalid(message string) error {
	return codedError(CodeCustomVerificationInvalid, fmt.Errorf("%s: %w", message, domain.ErrCustomVerificationRequestInvalid))
}

func botVerifierInvalid(message string) error {
	return codedError(CodeBotVerifierInvalid, fmt.Errorf("%s: %w", message, domain.ErrVerifierSettingsInvalid))
}
