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

// Official platform verification review.
//
// The review actions live behind the same command journal as every other
// operator write: a decision is auditable, replayable by command id and
// rehearsable with a dry run. The application record itself is the durable audit
// subject, so the details captured here name the application, its target and the
// status it moved between, and they carry the correlation id that ties the
// command journal entry to the immutable application event.

// VerificationService is the operator-facing slice of the official verification
// use cases. It is the exact method set *app/verification.Service exposes for the
// reviewer side, so the admin layer never reaches into the store.
type VerificationService interface {
	List(ctx context.Context, filter domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error)
	Counts(ctx context.Context) (domain.VerificationStatusCounts, error)
	Events(ctx context.Context, applicationID int64, limit int) ([]domain.VerificationApplicationEvent, error)
	Application(ctx context.Context, applicationID int64) (domain.VerificationApplication, error)
	TargetSnapshot(ctx context.Context, targetType domain.VerificationTargetType, targetID int64) (domain.VerificationTarget, error)
	Claim(ctx context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, error)
	Approve(ctx context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, bool, error)
	Reject(ctx context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, bool, error)
	Revoke(ctx context.Context, req domain.VerificationRevocation) (domain.VerificationApplication, bool, error)
}

// ClaimVerificationRequest assigns a reviewer to a submitted application.
type ClaimVerificationRequest struct {
	CommandMeta
	ApplicationID int64 `json:"application_id"`
	// Version is the optimistic-locking token the reviewer read. Two reviewers
	// opening the same row submit the same version and exactly one wins.
	Version int64 `json:"version"`
	// InternalNote is optional on a claim: a reviewer picking a case up may want to
	// record why ("waiting on legal") without deciding it yet.
	InternalNote string `json:"internal_note,omitempty"`
}

// ApproveVerificationRequest grants the platform badge.
type ApproveVerificationRequest struct {
	CommandMeta
	ApplicationID int64 `json:"application_id"`
	Version       int64 `json:"version"`
	// InternalNote is operator-only. It is journalled and appended to the
	// application history, and it is never part of what the applicant is told.
	InternalNote string `json:"internal_note,omitempty"`
}

// RejectVerificationRequest closes an application against the applicant. Reason
// is mandatory: it is the text the applicant receives.
type RejectVerificationRequest struct {
	CommandMeta
	ApplicationID int64  `json:"application_id"`
	Version       int64  `json:"version"`
	InternalNote  string `json:"internal_note,omitempty"`
}

// RevokeVerificationRequest clears the badge of a previously approved target. It
// addresses the target rather than an application, because a revocation is not a
// decision on the application: the application stays approved as history.
type RevokeVerificationRequest struct {
	CommandMeta
	TargetType   domain.VerificationTargetType `json:"target_type"`
	TargetID     int64                         `json:"target_id"`
	InternalNote string                        `json:"internal_note,omitempty"`
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// VerificationApplications is the review-queue listing. The filter is passed
// through unchanged: the use-case layer owns normalisation and the page bound.
func (s *Service) VerificationApplications(ctx context.Context, filter domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error) {
	if s == nil || s.verification == nil {
		return nil, errVerificationNotConfigured
	}
	return s.verification.List(ctx, filter)
}

// VerificationCounts is the queue summary rendered above the list.
func (s *Service) VerificationCounts(ctx context.Context) (domain.VerificationStatusCounts, error) {
	if s == nil || s.verification == nil {
		return nil, errVerificationNotConfigured
	}
	return s.verification.Counts(ctx)
}

// VerificationApplication resolves one application by identity.
func (s *Service) VerificationApplication(ctx context.Context, applicationID int64) (domain.VerificationApplication, error) {
	if s == nil || s.verification == nil {
		return domain.VerificationApplication{}, errVerificationNotConfigured
	}
	if applicationID <= 0 {
		return domain.VerificationApplication{}, verificationCoded(domain.ErrVerificationApplicationNotFound)
	}
	return s.verification.Application(ctx, applicationID)
}

// VerificationApplicationEvents returns one application's immutable history.
func (s *Service) VerificationApplicationEvents(ctx context.Context, applicationID int64, limit int) ([]domain.VerificationApplicationEvent, error) {
	if s == nil || s.verification == nil {
		return nil, errVerificationNotConfigured
	}
	return s.verification.Events(ctx, applicationID, limit)
}

// VerificationTargetSnapshot returns the target's state as it is now: title,
// username, badge, and whether it would pass the eligibility checks today.
func (s *Service) VerificationTargetSnapshot(ctx context.Context, targetType domain.VerificationTargetType, targetID int64) (domain.VerificationTarget, error) {
	if s == nil || s.verification == nil {
		return domain.VerificationTarget{}, errVerificationNotConfigured
	}
	return s.verification.TargetSnapshot(ctx, targetType, targetID)
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

// ClaimVerification takes ownership of a submitted application.
//
// Claiming an application that somebody else already claimed is not idempotent:
// the status machine has no in_review -> in_review edge, so the second reviewer
// is told the row is taken instead of silently stealing it.
func (s *Service) ClaimVerification(ctx context.Context, req ClaimVerificationRequest) (CommandResult, error) {
	if s == nil || s.verification == nil {
		return CommandResult{}, errVerificationNotConfigured
	}
	if err := validateVerificationDecisionShape(req.ApplicationID, req.Version, req.InternalNote); err != nil {
		return CommandResult{}, err
	}
	return s.runCommand(ctx, req.CommandMeta, ActionClaimVerification, 0, domain.Peer{}, req, func() (CommandResult, error) {
		app, details, err := s.verificationSubject(ctx, req.CommandMeta, req.ApplicationID, domain.VerificationStatusInReview, req.InternalNote)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if err := verificationTransition(app, req.Version, domain.VerificationStatusInReview, false); err != nil {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			return CommandResult{Message: "verification claim validated", Details: details}, nil
		}
		claimed, err := s.verification.Claim(ctx, verificationDecision(req.CommandMeta, req.ApplicationID, req.Version, req.InternalNote))
		if err != nil {
			return CommandResult{Details: details}, verificationError(err)
		}
		mergeVerificationDetails(details, claimed, true)
		return CommandResult{Message: "verification application claimed", Details: details}, nil
	})
}

// ApproveVerification grants the platform badge.
//
// The dry run reloads the target snapshot and refuses in advance whatever the
// real approval would refuse: the snapshot re-runs every eligibility check
// except the ownership probe, and a missing ownership can only make the real run
// stricter, never more permissive. So a passing dry run never turns into a
// surprise, and a failing one names the reason before anything is written.
func (s *Service) ApproveVerification(ctx context.Context, req ApproveVerificationRequest) (CommandResult, error) {
	if s == nil || s.verification == nil {
		return CommandResult{}, errVerificationNotConfigured
	}
	if err := validateVerificationDecisionShape(req.ApplicationID, req.Version, req.InternalNote); err != nil {
		return CommandResult{}, err
	}
	return s.runCommand(ctx, req.CommandMeta, ActionApproveVerification, 0, domain.Peer{}, req, func() (CommandResult, error) {
		app, details, err := s.verificationSubject(ctx, req.CommandMeta, req.ApplicationID, domain.VerificationStatusApproved, req.InternalNote)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if err := verificationTransition(app, req.Version, domain.VerificationStatusApproved, true); err != nil {
			return CommandResult{Details: details}, err
		}
		// An application that is already approved is a replay: the use-case layer
		// returns it untouched without re-running the target checks, so the target
		// gate must not fire here either -- otherwise a retry would fail a dry run
		// that the real command answers as a no-op.
		replay := app.Status == domain.VerificationStatusApproved
		if err := s.mergeVerificationTargetDetails(ctx, details, app, !replay); err != nil {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			return CommandResult{Message: "verification approve validated", Details: details}, nil
		}
		approved, changed, err := s.verification.Approve(ctx, verificationDecision(req.CommandMeta, req.ApplicationID, req.Version, req.InternalNote))
		if err != nil {
			return CommandResult{Details: details}, verificationError(err)
		}
		mergeVerificationDetails(details, approved, changed)
		message := "verification application approved"
		if !changed {
			message = "verification application already approved"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// RejectVerification closes an application against the applicant. The reason is
// mandatory and is the text the applicant is shown; the internal note stays
// operator-side.
func (s *Service) RejectVerification(ctx context.Context, req RejectVerificationRequest) (CommandResult, error) {
	if s == nil || s.verification == nil {
		return CommandResult{}, errVerificationNotConfigured
	}
	if err := validateVerificationDecisionShape(req.ApplicationID, req.Version, req.InternalNote); err != nil {
		return CommandResult{}, err
	}
	// A rejection without a stated reason is refused before the journal is
	// touched: the audit trail must never contain a decision nobody can explain.
	if strings.TrimSpace(req.Reason) == "" {
		return CommandResult{}, codedError(CodeVerificationReasonRequired, domain.ErrVerificationReasonRequired)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRejectVerification, 0, domain.Peer{}, req, func() (CommandResult, error) {
		app, details, err := s.verificationSubject(ctx, req.CommandMeta, req.ApplicationID, domain.VerificationStatusRejected, req.InternalNote)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if err := verificationTransition(app, req.Version, domain.VerificationStatusRejected, true); err != nil {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			return CommandResult{Message: "verification reject validated", Details: details}, nil
		}
		rejected, changed, err := s.verification.Reject(ctx, verificationDecision(req.CommandMeta, req.ApplicationID, req.Version, req.InternalNote))
		if err != nil {
			return CommandResult{Details: details}, verificationError(err)
		}
		mergeVerificationDetails(details, rejected, changed)
		message := "verification application rejected"
		if !changed {
			message = "verification application already rejected"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// RevokeVerification clears the platform badge from a target.
//
// It addresses the peer, not an application: the approved application stays
// approved as history and the revocation is its own audit event. A reason is
// mandatory for the same reason a rejection needs one.
func (s *Service) RevokeVerification(ctx context.Context, req RevokeVerificationRequest) (CommandResult, error) {
	if s == nil || s.verification == nil {
		return CommandResult{}, errVerificationNotConfigured
	}
	if req.TargetID <= 0 || !req.TargetType.Valid() {
		return CommandResult{}, codedError(CodeVerificationTargetInvalid, domain.ErrVerificationTargetInvalid)
	}
	if utf8.RuneCountInString(req.InternalNote) > domain.MaxVerificationNoteLength {
		return CommandResult{}, verificationInvalid("internal_note is too long")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return CommandResult{}, codedError(CodeVerificationReasonRequired, domain.ErrVerificationReasonRequired)
	}
	targetPeer := domain.Peer{Type: req.TargetType.PeerType(), ID: req.TargetID}
	targetUserID := int64(0)
	if targetPeer.Type == domain.PeerTypeUser {
		targetUserID = req.TargetID
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRevokeVerification, targetUserID, targetPeer, req, func() (CommandResult, error) {
		details := map[string]any{
			"target_type":    string(req.TargetType),
			"target_id":      strconv.FormatInt(req.TargetID, 10),
			"correlation_id": strings.TrimSpace(req.CommandID),
		}
		if note := strings.TrimSpace(req.InternalNote); note != "" {
			details["internal_note"] = note
		}
		// A built-in system account carries its badge by construction, so revoking
		// it would desynchronise the seeded record from domain.SystemUserByID.
		if targetPeer.Type == domain.PeerTypeUser && domain.IsSystemUserID(req.TargetID) {
			return CommandResult{Details: details}, codedError(CodeVerificationTargetSystem, domain.ErrVerificationTargetSystem)
		}
		target, err := s.verification.TargetSnapshot(ctx, req.TargetType, req.TargetID)
		if err != nil {
			return CommandResult{Details: details}, verificationError(err)
		}
		details["target_title"] = target.Title
		details["target_username"] = target.Username
		details["target_verified"] = target.Verified
		if req.DryRun {
			message := "verification revoke validated"
			if !target.Verified {
				message = "verification revoke validated; target carries no badge"
			}
			return CommandResult{Message: message, Details: details}, nil
		}
		app, changed, err := s.verification.Revoke(ctx, domain.VerificationRevocation{
			TargetType:    req.TargetType,
			TargetID:      req.TargetID,
			Reviewer:      strings.TrimSpace(req.Actor),
			Reason:        strings.TrimSpace(req.Reason),
			InternalNote:  strings.TrimSpace(req.InternalNote),
			CorrelationID: strings.TrimSpace(req.CommandID),
		})
		if err != nil {
			return CommandResult{Details: details}, verificationError(err)
		}
		details["changed"] = changed
		details["target_verified"] = false
		if app.ID > 0 {
			// The revoked target usually has an approved application behind it; it
			// stays approved, which is why previous_status and status match here.
			details["application_id"] = strconv.FormatInt(app.ID, 10)
			details["applicant_user_id"] = strconv.FormatInt(app.ApplicantUserID, 10)
			details["previous_status"] = string(app.Status)
			details["status"] = string(app.Status)
			details["version"] = strconv.FormatInt(app.Version, 10)
		}
		message := "verification badge revoked"
		if !changed {
			message = "verification badge was already absent"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

var errVerificationNotConfigured = errors.New("admin verification dependency is not configured")

// verificationSubject loads the application under decision and seeds the command
// details with everything the audit entry must state even when the command then
// fails: which application, whose, which target, the status it is leaving and the
// status it was asked to reach.
func (s *Service) verificationSubject(
	ctx context.Context,
	meta CommandMeta,
	applicationID int64,
	next domain.VerificationStatus,
	internalNote string,
) (domain.VerificationApplication, map[string]any, error) {
	details := map[string]any{
		"application_id": strconv.FormatInt(applicationID, 10),
		"next_status":    string(next),
		"correlation_id": strings.TrimSpace(meta.CommandID),
	}
	if note := strings.TrimSpace(internalNote); note != "" {
		details["internal_note"] = note
	}
	app, err := s.verification.Application(ctx, applicationID)
	if err != nil {
		return domain.VerificationApplication{}, details, verificationError(err)
	}
	details["applicant_user_id"] = strconv.FormatInt(app.ApplicantUserID, 10)
	details["target_type"] = string(app.TargetType)
	details["target_id"] = strconv.FormatInt(app.TargetID, 10)
	details["target_username"] = app.TargetUsername
	details["previous_status"] = string(app.Status)
	details["previous_version"] = strconv.FormatInt(app.Version, 10)
	return app, details, nil
}

// mergeVerificationTargetDetails records the current target snapshot and, for a
// decision that flips the badge, refuses a target the platform may not verify.
func (s *Service) mergeVerificationTargetDetails(ctx context.Context, details map[string]any, app domain.VerificationApplication, requireEligible bool) error {
	target, err := s.verification.TargetSnapshot(ctx, app.TargetType, app.TargetID)
	if err != nil {
		return verificationError(err)
	}
	details["target_title"] = target.Title
	details["target_current_username"] = target.Username
	details["target_verified"] = target.Verified
	details["target_eligible"] = target.Eligible
	if target.Reason != "" {
		details["target_reason"] = target.Reason
	}
	if requireEligible && !target.Eligible {
		return verificationError(verificationTargetReasonError(target.Reason))
	}
	return nil
}

// mergeVerificationDetails records the decided state.
func mergeVerificationDetails(details map[string]any, app domain.VerificationApplication, changed bool) {
	details["status"] = string(app.Status)
	details["version"] = strconv.FormatInt(app.Version, 10)
	details["changed"] = changed
	details["reviewer_admin_id"] = app.ReviewerAdminID
	if app.CorrelationID != "" {
		details["correlation_id"] = app.CorrelationID
	}
	if app.DecisionReason != "" {
		details["decision_reason"] = app.DecisionReason
	}
}

// verificationDecision builds the domain decision.
//
// The admin command id doubles as the correlation id, so one token links the
// command journal entry, the immutable application event and the applicant
// notification. The internal note travels in its own field and never in Reason,
// which is what the applicant is shown.
func verificationDecision(meta CommandMeta, applicationID, version int64, internalNote string) domain.VerificationDecision {
	return domain.VerificationDecision{
		ApplicationID: applicationID,
		Version:       version,
		Reviewer:      strings.TrimSpace(meta.Actor),
		Reason:        strings.TrimSpace(meta.Reason),
		InternalNote:  strings.TrimSpace(internalNote),
		CorrelationID: strings.TrimSpace(meta.CommandID),
	}
}

// validateVerificationDecisionShape rejects a malformed decision before the
// command journal is touched.
func validateVerificationDecisionShape(applicationID, version int64, internalNote string) error {
	if applicationID <= 0 {
		return verificationCoded(domain.ErrVerificationApplicationNotFound)
	}
	if version <= 0 {
		// Without the version the reviewer never read the row, so the optimistic
		// lock could not protect a concurrent decision.
		return verificationInvalid("version is required")
	}
	if utf8.RuneCountInString(internalNote) > domain.MaxVerificationNoteLength {
		return verificationInvalid("internal_note is too long")
	}
	return nil
}

// verificationTransition checks the status machine and the optimistic lock in the
// order the use-case layer does, so a dry run predicts the real outcome exactly.
//
// idempotent marks the decisions the service treats as a no-op on replay
// (approve/reject of an already decided application). A claim is not among them:
// there is no in_review -> in_review edge.
func verificationTransition(app domain.VerificationApplication, version int64, next domain.VerificationStatus, idempotent bool) error {
	if idempotent && app.Status == next {
		return nil
	}
	if !domain.CanTransitionVerificationStatus(app.Status, next) {
		return codedError(CodeVerificationStatusInvalid, fmt.Errorf("%w: %s -> %s", domain.ErrVerificationStatusInvalid, app.Status, next))
	}
	if app.Version != version {
		return codedError(CodeVerificationConflict, domain.ErrVerificationVersionConflict)
	}
	return nil
}

// verificationTargetReasonError maps the snapshot's rendered ineligibility reason
// back onto its domain sentinel. VerificationTarget.Reason is a string by
// design -- it is rendered to applicants by the bot -- so the reverse lookup is
// what lets the admin layer answer with a stable code instead of a bare message.
func verificationTargetReasonError(reason string) error {
	for _, candidate := range []error{
		domain.ErrVerificationTargetAlreadyVerified,
		domain.ErrVerificationTargetRestricted,
		domain.ErrVerificationTargetNotPublic,
		domain.ErrVerificationTargetSystem,
		domain.ErrVerificationUserTargetsDisabled,
		domain.ErrVerificationTargetInvalid,
	} {
		if candidate.Error() == reason {
			return candidate
		}
	}
	return fmt.Errorf("%w: %s", domain.ErrVerificationTargetInvalid, reason)
}

// VerificationErrorCode maps a verification failure onto the stable code the
// admin panel switches on. An unmapped error returns "" so the caller can report
// it verbatim instead of inventing a code.
func VerificationErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, domain.ErrVerificationApplicationNotFound):
		return CodeVerificationNotFound
	case errors.Is(err, domain.ErrVerificationVersionConflict):
		return CodeVerificationConflict
	case errors.Is(err, domain.ErrVerificationApplicationExists):
		return CodeVerificationTargetOccupied
	case errors.Is(err, domain.ErrVerificationStatusInvalid):
		return CodeVerificationStatusInvalid
	case errors.Is(err, domain.ErrVerificationReasonRequired):
		return CodeVerificationReasonRequired
	case errors.Is(err, domain.ErrVerificationTargetAlreadyVerified):
		return CodeVerificationTargetVerified
	case errors.Is(err, domain.ErrVerificationTargetNotPublic):
		return CodeVerificationTargetNotPublic
	case errors.Is(err, domain.ErrVerificationTargetRestricted):
		return CodeVerificationTargetRestricted
	case errors.Is(err, domain.ErrVerificationTargetSystem):
		return CodeVerificationTargetSystem
	case errors.Is(err, domain.ErrVerificationNotOwner):
		return CodeVerificationNotOwner
	case errors.Is(err, domain.ErrVerificationUserTargetsDisabled):
		return CodeVerificationUserTargetsDisabled
	case errors.Is(err, domain.ErrVerificationTargetInvalid):
		return CodeVerificationTargetInvalid
	case errors.Is(err, domain.ErrVerificationURLInvalid),
		errors.Is(err, domain.ErrVerificationApplicationInvalid):
		return CodeVerificationInvalid
	default:
		return ""
	}
}

// verificationError prefixes a recognised verification error with its stable
// code, the way collectibleUsernameError does for the username registry.
func verificationError(err error) error {
	if code := VerificationErrorCode(err); code != "" {
		return codedError(code, err)
	}
	return err
}

func verificationCoded(err error) error {
	return codedError(VerificationErrorCode(err), err)
}

func verificationInvalid(message string) error {
	return codedError(CodeVerificationInvalid, fmt.Errorf("%s: %w", message, domain.ErrVerificationApplicationInvalid))
}
