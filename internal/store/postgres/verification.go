package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// VerificationStore is the PostgreSQL implementation of the official platform
// verification store: applications filed through @verifybot, the immutable
// per-application history and the applicant-notification outbox.
//
// Three properties are load bearing and every method below exists to keep them:
//
//   - "approved implies target verified". The status change, the history row, the
//     outbox row and the peer flag are written in one transaction. The flag is set
//     by the caller-supplied applyVerified callback, which runs inside that
//     transaction (see VerificationTxFromContext), so a callback failure rolls the
//     decision back and a crash can never leave an approved application whose
//     target is not verified.
//   - Exactly one decision per application. Every mutation is guarded by
//     WHERE id = $1 AND version = $2 on top of a SELECT ... FOR UPDATE, so two
//     reviewers deciding the same application concurrently produce one winner and
//     domain.ErrVerificationVersionConflict for the loser.
//   - Exactly one notification per decision. The outbox unique key is
//     (application_id, kind), and a repeated decision on an already-decided
//     application is a no-op reporting changed=false rather than a second message.
//
// Status transitions are never re-implemented in SQL: they all go through
// domain.CanTransitionVerificationStatus, which the bot dialog validates against
// too. The history table has no UPDATE or DELETE path in this file by design.
type VerificationStore struct {
	db sqlcgen.DBTX
}

// NewVerificationStore builds the store on a pgx pool or transaction.
func NewVerificationStore(db sqlcgen.DBTX) *VerificationStore {
	return &VerificationStore{db: db}
}

var _ store.VerificationStore = (*VerificationStore)(nil)

const (
	defaultVerificationListLimit = 50
	maxVerificationListLimit     = 200
	// maxVerificationUsernameBytes mirrors the octet_length CHECK on
	// target_username. The domain request bounds the title but not the username
	// snapshot, so the store guards the column itself.
	maxVerificationUsernameBytes = 64
	// maxVerificationOutboxErrorBytes mirrors the octet_length CHECK on
	// verification_notification_outbox.last_error.
	maxVerificationOutboxErrorBytes = 1024
)

// Constraint names the store maps onto domain errors. They are the schema's
// invariants, so a race that slips past the pre-checks still reports the same
// error as the check would have.
const (
	verificationActiveTargetConstraint   = "verification_applications_active_target_idx"
	verificationApplicantDraftConstraint = "verification_applications_applicant_draft_idx"
	verificationNotificationConstraint   = "verification_notification_once"
)

// verificationApplicationColumnList is the application projection shared by every
// reader, in scan order.
const verificationApplicationColumnList = `id, applicant_user_id, target_type, target_id,
       target_title, target_username, target_access_hash, category, description,
       official_website, social_links, press_links, additional_note, status,
       reviewer_admin_id, decision_reason, internal_note, correlation_id,
       created_at, updated_at, submitted_at, reviewed_at, version`

// verificationApplicationJoinColumns is the same projection qualified for the
// outbox join, kept in sync by construction rather than by hand.
var verificationApplicationJoinColumns = prefixVerificationColumns("a.")

func prefixVerificationColumns(alias string) string {
	parts := strings.Split(verificationApplicationColumnList, ",")
	for i, part := range parts {
		parts[i] = alias + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}

// verificationTxKey carries the transaction driving a decision into the
// applyVerified / clearVerified callback.
type verificationTxKey struct{}

func verificationTxContext(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, verificationTxKey{}, tx)
}

// VerificationTxFromContext returns the transaction that is currently deciding a
// verification application, if the context came from
// DecideVerificationApplication or RevokeVerification.
//
// This is how "approved implies target verified" is kept atomic across two
// stores: the applyVerified / clearVerified callback must write the peer flag
// through this handle (for example postgres.NewUserStore(tx).SetVerified) instead
// of its own pool connection. A callback that ignores it writes in a separate
// transaction, and the flag would survive a rollback of the decision.
func VerificationTxFromContext(ctx context.Context) (sqlcgen.DBTX, bool) {
	tx, ok := ctx.Value(verificationTxKey{}).(pgx.Tx)
	if !ok || tx == nil {
		return nil, false
	}
	return tx, true
}

// verificationNow is the single clock for the store. Timestamps are truncated to
// the timestamptz resolution so a value written here reads back identically.
func verificationNow() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// CreateVerificationDraft opens a draft for the applicant/target pair.
//
// The applicant's own draft wins over everything else: the bot dialog is a single
// conversation and verification_applications_applicant_draft_idx allows exactly
// one, so an applicant who already has one gets it back with created=false and
// the dialog resumes. Only then is the target checked, and an application already
// occupying it reports domain.ErrVerificationApplicationExists.
func (s *VerificationStore) CreateVerificationDraft(ctx context.Context, req domain.SubmitVerificationApplicationRequest) (domain.VerificationApplication, bool, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, false, fmt.Errorf("verification store is not configured")
	}
	req.TargetTitle = strings.TrimSpace(req.TargetTitle)
	req.TargetUsername = domain.NormalizeUsername(req.TargetUsername)
	req.CorrelationID = strings.TrimSpace(req.CorrelationID)
	req.Draft = req.Draft.Normalize()
	if err := validateVerificationDraftRequest(req); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	var app domain.VerificationApplication
	created := false
	err := withTx(ctx, s.db, "create verification draft", func(tx pgx.Tx) error {
		// Both locks are taken before the pre-checks so two concurrent bot dialogs
		// serialise instead of racing on the partial unique indexes.
		if err := lockVerificationApplicant(ctx, tx, req.ApplicantUserID); err != nil {
			return err
		}
		if err := lockVerificationTarget(ctx, tx, req.TargetType, req.TargetID); err != nil {
			return err
		}
		existing, err := verificationDraftForApplicantTx(ctx, tx, req.ApplicantUserID)
		switch {
		case err == nil:
			app = existing
			return nil
		case errors.Is(err, domain.ErrVerificationApplicationNotFound):
		default:
			return err
		}
		switch _, err := activeVerificationApplicationTx(ctx, tx, req.TargetType, req.TargetID); {
		case err == nil:
			return domain.ErrVerificationApplicationExists
		case errors.Is(err, domain.ErrVerificationApplicationNotFound):
		default:
			return err
		}
		now := verificationNow()
		inserted, err := scanVerificationApplication(tx.QueryRow(ctx, `
INSERT INTO verification_applications (
  applicant_user_id, target_type, target_id, target_title, target_username,
  category, description, official_website, social_links, press_links,
  additional_note, status, correlation_id, created_at, updated_at, version
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'draft',$12,$13,$13,1)
RETURNING `+verificationApplicationColumnList,
			req.ApplicantUserID, string(req.TargetType), req.TargetID, req.TargetTitle,
			req.TargetUsername, req.Draft.Category, req.Draft.Description,
			req.Draft.OfficialWebsite, verificationLinksArg(req.Draft.SocialLinks),
			verificationLinksArg(req.Draft.PressLinks), req.Draft.AdditionalNote,
			req.CorrelationID, now,
		))
		if err != nil {
			if isUniqueConstraint(err, verificationActiveTargetConstraint) ||
				isUniqueConstraint(err, verificationApplicantDraftConstraint) {
				return domain.ErrVerificationApplicationExists
			}
			return fmt.Errorf("insert verification application: %w", err)
		}
		if err := insertVerificationEventTx(ctx, tx, verificationEventInput{
			applicationID: inserted.ID,
			kind:          domain.VerificationEventCreated,
			to:            domain.VerificationStatusDraft,
			correlationID: inserted.CorrelationID,
			createdAt:     now,
		}); err != nil {
			return err
		}
		app = inserted
		created = true
		return nil
	})
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	return app, created, nil
}

// SaveVerificationDraft rewrites the applicant-supplied payload of a draft.
func (s *VerificationStore) SaveVerificationDraft(ctx context.Context, applicationID int64, version int64, draft domain.VerificationDraftInput) (domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, fmt.Errorf("verification store is not configured")
	}
	if applicationID <= 0 || version <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	draft = draft.Normalize()
	if err := draft.ValidateDraft(); err != nil {
		return domain.VerificationApplication{}, err
	}
	var app domain.VerificationApplication
	err := withTx(ctx, s.db, "save verification draft", func(tx pgx.Tx) error {
		current, err := lockVerificationApplicationTx(ctx, tx, applicationID)
		if err != nil {
			return err
		}
		if current.Version != version {
			return domain.ErrVerificationVersionConflict
		}
		if !current.Editable() {
			return domain.ErrVerificationStatusInvalid
		}
		now := verificationNow()
		updated, err := scanVerificationApplication(tx.QueryRow(ctx, `
UPDATE verification_applications
SET category = $3,
    description = $4,
    official_website = $5,
    social_links = $6,
    press_links = $7,
    additional_note = $8,
    version = version + 1,
    updated_at = GREATEST(updated_at, $9)
WHERE id = $1 AND version = $2
RETURNING `+verificationApplicationColumnList,
			applicationID, version, draft.Category, draft.Description,
			draft.OfficialWebsite, verificationLinksArg(draft.SocialLinks),
			verificationLinksArg(draft.PressLinks), draft.AdditionalNote, now,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVerificationVersionConflict
		}
		if err != nil {
			return fmt.Errorf("update verification draft: %w", err)
		}
		if err := insertVerificationEventTx(ctx, tx, verificationEventInput{
			applicationID: applicationID,
			kind:          domain.VerificationEventUpdated,
			from:          current.Status,
			to:            updated.Status,
			correlationID: updated.CorrelationID,
			createdAt:     now,
		}); err != nil {
			return err
		}
		app = updated
		return nil
	})
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	return app, nil
}

// SubmitVerificationApplication moves an application into the review queue and
// stamps submitted_at.
//
// The stored payload is validated against the submission bar here rather than
// only in the bot: an incomplete draft must not be able to reach a reviewer
// through any path. Coming back from in_review (the domain machine allows it)
// releases the claim and keeps the original submitted_at.
func (s *VerificationStore) SubmitVerificationApplication(ctx context.Context, applicationID int64, version int64) (domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, fmt.Errorf("verification store is not configured")
	}
	if applicationID <= 0 || version <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	var app domain.VerificationApplication
	err := withTx(ctx, s.db, "submit verification application", func(tx pgx.Tx) error {
		current, err := lockVerificationApplicationTx(ctx, tx, applicationID)
		if err != nil {
			return err
		}
		if current.Version != version {
			return domain.ErrVerificationVersionConflict
		}
		if !domain.CanTransitionVerificationStatus(current.Status, domain.VerificationStatusSubmitted) {
			return domain.ErrVerificationStatusInvalid
		}
		if err := verificationDraftOf(current).ValidateForSubmission(); err != nil {
			return err
		}
		now := verificationNow()
		updated, err := scanVerificationApplication(tx.QueryRow(ctx, `
UPDATE verification_applications
SET status = 'submitted',
    reviewer_admin_id = '',
    submitted_at = COALESCE(submitted_at, $3),
    version = version + 1,
    updated_at = GREATEST(updated_at, $3)
WHERE id = $1 AND version = $2
RETURNING `+verificationApplicationColumnList,
			applicationID, version, now,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVerificationVersionConflict
		}
		if err != nil {
			return fmt.Errorf("submit verification application: %w", err)
		}
		if err := insertVerificationEventTx(ctx, tx, verificationEventInput{
			applicationID: applicationID,
			kind:          domain.VerificationEventSubmitted,
			from:          current.Status,
			to:            updated.Status,
			correlationID: updated.CorrelationID,
			createdAt:     now,
		}); err != nil {
			return err
		}
		app = updated
		return nil
	})
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	return app, nil
}

// CancelVerificationApplication withdraws an active application on the
// applicant's behalf. The reason is the applicant's, so it lands on the history
// row and never in decision_reason, which stays reviewer-owned.
func (s *VerificationStore) CancelVerificationApplication(ctx context.Context, applicationID int64, version int64, reason string) (domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, fmt.Errorf("verification store is not configured")
	}
	reason = strings.TrimSpace(reason)
	if applicationID <= 0 || version <= 0 ||
		utf8.RuneCountInString(reason) > domain.MaxVerificationReasonLength {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	var app domain.VerificationApplication
	err := withTx(ctx, s.db, "cancel verification application", func(tx pgx.Tx) error {
		current, err := lockVerificationApplicationTx(ctx, tx, applicationID)
		if err != nil {
			return err
		}
		if current.Version != version {
			return domain.ErrVerificationVersionConflict
		}
		if !domain.CanTransitionVerificationStatus(current.Status, domain.VerificationStatusCancelled) {
			return domain.ErrVerificationStatusInvalid
		}
		now := verificationNow()
		// A cancelled draft was never submitted, but the schema requires
		// submitted_at on every non-draft row, so the withdrawal time stands in.
		updated, err := scanVerificationApplication(tx.QueryRow(ctx, `
UPDATE verification_applications
SET status = 'cancelled',
    submitted_at = COALESCE(submitted_at, $3),
    version = version + 1,
    updated_at = GREATEST(updated_at, $3)
WHERE id = $1 AND version = $2
RETURNING `+verificationApplicationColumnList,
			applicationID, version, now,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVerificationVersionConflict
		}
		if err != nil {
			return fmt.Errorf("cancel verification application: %w", err)
		}
		if err := insertVerificationEventTx(ctx, tx, verificationEventInput{
			applicationID: applicationID,
			kind:          domain.VerificationEventCancelled,
			from:          current.Status,
			to:            updated.Status,
			reason:        reason,
			correlationID: updated.CorrelationID,
			createdAt:     now,
		}); err != nil {
			return err
		}
		app = updated
		return nil
	})
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	return app, nil
}

// ClaimVerificationApplication assigns a reviewer and moves the application to
// in_review. reviewed_at stays NULL: a claim is not a decision, and the schema
// pairs reviewed_at with a decided status.
func (s *VerificationStore) ClaimVerificationApplication(ctx context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, fmt.Errorf("verification store is not configured")
	}
	decision = normalizeVerificationDecision(decision)
	if err := decision.Validate(); err != nil {
		return domain.VerificationApplication{}, err
	}
	var app domain.VerificationApplication
	err := withTx(ctx, s.db, "claim verification application", func(tx pgx.Tx) error {
		current, err := lockVerificationApplicationTx(ctx, tx, decision.ApplicationID)
		if err != nil {
			return err
		}
		if current.Version != decision.Version {
			return domain.ErrVerificationVersionConflict
		}
		if !domain.CanTransitionVerificationStatus(current.Status, domain.VerificationStatusInReview) {
			return domain.ErrVerificationStatusInvalid
		}
		now := verificationNow()
		updated, err := scanVerificationApplication(tx.QueryRow(ctx, `
UPDATE verification_applications
SET status = 'in_review',
    reviewer_admin_id = $3,
    version = version + 1,
    updated_at = GREATEST(updated_at, $4)
WHERE id = $1 AND version = $2
RETURNING `+verificationApplicationColumnList,
			decision.ApplicationID, decision.Version, decision.Reviewer, now,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVerificationVersionConflict
		}
		if err != nil {
			return fmt.Errorf("claim verification application: %w", err)
		}
		if err := insertVerificationEventTx(ctx, tx, verificationEventInput{
			applicationID: decision.ApplicationID,
			kind:          domain.VerificationEventClaimed,
			from:          current.Status,
			to:            updated.Status,
			actor:         decision.Reviewer,
			reason:        decision.Reason,
			note:          decision.InternalNote,
			correlationID: decision.CorrelationID,
			createdAt:     now,
		}); err != nil {
			return err
		}
		app = updated
		return nil
	})
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	return app, nil
}

// DecideVerificationApplication records an approval or a rejection.
//
// Order of checks matters and is the deterministic part of two reviewers acting
// at once: the version is compared first, so the loser of the race always gets
// domain.ErrVerificationVersionConflict rather than a silent no-op. A caller that
// re-issues the same decision with the current version (a retry after it already
// committed) sees the decided application with changed=false, no second history
// row and no second outbox row.
//
// On approve the peer flag is written by applyVerified inside this transaction,
// so a callback error rolls the whole decision back and "approved implies target
// verified" holds even across a crash.
func (s *VerificationStore) DecideVerificationApplication(ctx context.Context, decision domain.VerificationDecision, approve bool, applyVerified func(ctx context.Context, app domain.VerificationApplication) error) (domain.VerificationApplication, bool, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, false, fmt.Errorf("verification store is not configured")
	}
	decision = normalizeVerificationDecision(decision)
	target := domain.VerificationStatusRejected
	kind := domain.VerificationEventRejected
	if approve {
		target = domain.VerificationStatusApproved
		kind = domain.VerificationEventApproved
		if err := decision.Validate(); err != nil {
			return domain.VerificationApplication{}, false, err
		}
		if applyVerified == nil {
			// Approving without a way to set the flag is exactly the state this
			// store exists to make impossible.
			return domain.VerificationApplication{}, false, fmt.Errorf("verification approval requires an applyVerified callback")
		}
	} else if err := decision.ValidateWithReason(); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	var app domain.VerificationApplication
	changed := false
	err := withTx(ctx, s.db, "decide verification application", func(tx pgx.Tx) error {
		current, err := lockVerificationApplicationTx(ctx, tx, decision.ApplicationID)
		if err != nil {
			return err
		}
		if current.Version != decision.Version {
			return domain.ErrVerificationVersionConflict
		}
		if current.Status == target {
			// Already decided this way: keep the record and the single
			// notification, report that nothing moved.
			app = current
			return nil
		}
		if !domain.CanTransitionVerificationStatus(current.Status, target) {
			return domain.ErrVerificationStatusInvalid
		}
		now := verificationNow()
		updated, err := scanVerificationApplication(tx.QueryRow(ctx, `
UPDATE verification_applications
SET status = $3,
    reviewer_admin_id = $4,
    reviewed_at = $5,
    decision_reason = $6,
    internal_note = $7,
    submitted_at = COALESCE(submitted_at, $5),
    version = version + 1,
    updated_at = GREATEST(updated_at, $5)
WHERE id = $1 AND version = $2
RETURNING `+verificationApplicationColumnList,
			decision.ApplicationID, decision.Version, string(target),
			decision.Reviewer, now, decision.Reason, decision.InternalNote,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVerificationVersionConflict
		}
		if err != nil {
			return fmt.Errorf("decide verification application: %w", err)
		}
		if err := insertVerificationEventTx(ctx, tx, verificationEventInput{
			applicationID: updated.ID,
			kind:          kind,
			from:          current.Status,
			to:            updated.Status,
			actor:         decision.Reviewer,
			reason:        decision.Reason,
			note:          decision.InternalNote,
			correlationID: decision.CorrelationID,
			createdAt:     now,
		}); err != nil {
			return err
		}
		if err := insertVerificationNotificationTx(ctx, tx, updated.ID,
			updated.ApplicantUserID, string(target), now); err != nil {
			return err
		}
		if approve {
			if err := applyVerified(verificationTxContext(ctx, tx), updated); err != nil {
				return err
			}
		}
		app = updated
		changed = true
		return nil
	})
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	return app, changed, nil
}

// RevokeVerification clears the platform flag of a previously approved target.
//
// The application stays approved: it is the audit record of a decision that did
// happen. The revocation is its own history row plus its own outbox kind, and
// that outbox row is what makes a repeated revocation a no-op with changed=false.
//
// A target with no approved application on file (verified by an earlier
// deployment, or by an operator by hand) is still cleared through the callback,
// because leaving a flag standing is worse than a missing audit row; there is
// then nothing to deduplicate against, so such a call always reports
// changed=true.
func (s *VerificationStore) RevokeVerification(ctx context.Context, req domain.VerificationRevocation, clearVerified func(ctx context.Context, target domain.Peer) error) (domain.VerificationApplication, bool, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, false, fmt.Errorf("verification store is not configured")
	}
	req.Reviewer = strings.TrimSpace(req.Reviewer)
	req.Reason = strings.TrimSpace(req.Reason)
	req.InternalNote = strings.TrimSpace(req.InternalNote)
	req.CorrelationID = strings.TrimSpace(req.CorrelationID)
	if err := req.Validate(); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if len(req.CorrelationID) > domain.MaxVerificationCorrelationLen {
		return domain.VerificationApplication{}, false, domain.ErrVerificationApplicationInvalid
	}
	if clearVerified == nil {
		return domain.VerificationApplication{}, false, fmt.Errorf("verification revocation requires a clearVerified callback")
	}
	var app domain.VerificationApplication
	changed := false
	err := withTx(ctx, s.db, "revoke verification", func(tx pgx.Tx) error {
		// There may be no application row to lock, so the target itself is the
		// serialisation point for concurrent revocations.
		if err := lockVerificationTarget(ctx, tx, req.TargetType, req.TargetID); err != nil {
			return err
		}
		var current domain.VerificationApplication
		switch found, err := newestApprovedVerificationApplicationTx(ctx, tx, req.TargetType, req.TargetID); {
		case err == nil:
			current = found
			done, err := verificationNotificationExistsTx(ctx, tx, current.ID, string(domain.VerificationEventRevoked))
			if err != nil {
				return err
			}
			if done {
				app = current
				return nil
			}
		case errors.Is(err, domain.ErrVerificationApplicationNotFound):
		default:
			return err
		}
		if err := clearVerified(verificationTxContext(ctx, tx),
			domain.Peer{Type: req.TargetType.PeerType(), ID: req.TargetID}); err != nil {
			return err
		}
		if current.ID != 0 {
			now := verificationNow()
			if err := insertVerificationEventTx(ctx, tx, verificationEventInput{
				applicationID: current.ID,
				kind:          domain.VerificationEventRevoked,
				from:          current.Status,
				to:            current.Status,
				actor:         req.Reviewer,
				reason:        req.Reason,
				note:          req.InternalNote,
				correlationID: req.CorrelationID,
				createdAt:     now,
			}); err != nil {
				return err
			}
			if err := insertVerificationNotificationTx(ctx, tx, current.ID,
				current.ApplicantUserID, string(domain.VerificationEventRevoked), now); err != nil {
				return err
			}
		}
		app = current
		changed = true
		return nil
	})
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	return app, changed, nil
}

// VerificationApplication reads one application by id.
func (s *VerificationStore) VerificationApplication(ctx context.Context, applicationID int64) (domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, fmt.Errorf("verification store is not configured")
	}
	if applicationID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return verificationApplicationByID(ctx, s.db, applicationID)
}

// ActiveVerificationApplicationForTarget returns the live application occupying a
// target, if any.
func (s *VerificationStore) ActiveVerificationApplicationForTarget(ctx context.Context, target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, fmt.Errorf("verification store is not configured")
	}
	if !target.Valid() || targetID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationTargetInvalid
	}
	return activeVerificationApplicationTx(ctx, s.db, target, targetID)
}

// VerificationDraftForApplicant returns the applicant's open draft, if any.
func (s *VerificationStore) VerificationDraftForApplicant(ctx context.Context, applicantUserID int64) (domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, fmt.Errorf("verification store is not configured")
	}
	if applicantUserID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return verificationDraftForApplicantTx(ctx, s.db, applicantUserID)
}

// ListVerificationApplications is the review-queue query.
//
// Paging is keyset over (created_at DESC, id DESC), the order
// verification_applications_queue_idx is built for: filter.Until and
// filter.BeforeID carry the last row of the previous page, filter.CreatedAt is
// the inclusive lower bound of the date range. Query matches an application id or
// a peer id when it is numeric and otherwise prefix-matches
// lower(target_username), which is what verification_applications_username_idx
// indexes.
func (s *VerificationStore) ListVerificationApplications(ctx context.Context, filter domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("verification store is not configured")
	}
	statuses, err := verificationFilterStatuses(filter)
	if err != nil {
		return nil, err
	}
	if filter.TargetType != "" && !filter.TargetType.Valid() {
		return nil, domain.ErrVerificationTargetInvalid
	}
	if len(filter.Reviewer) > domain.MaxVerificationReviewerLength {
		return nil, domain.ErrVerificationApplicationInvalid
	}
	limit := verificationLimit(filter.Limit)
	numeric, isNumeric, prefix := parseVerificationQuery(filter.Query)
	var since, until any
	if !filter.CreatedAt.IsZero() {
		since = filter.CreatedAt.UTC()
	}
	if !filter.Until.IsZero() {
		until = filter.Until.UTC()
	}
	rows, err := s.db.Query(ctx, `
SELECT `+verificationApplicationColumnList+`
FROM verification_applications
WHERE (cardinality($1::text[]) = 0 OR status = ANY($1::text[]))
  AND ($2 = '' OR target_type = $2)
  AND ($3 = '' OR reviewer_admin_id = $3)
  AND ($4::timestamptz IS NULL OR created_at >= $4::timestamptz)
  AND ($5::timestamptz IS NULL OR (created_at, id) < ($5::timestamptz, $6))
  AND ($5::timestamptz IS NOT NULL OR $6 = 0 OR id < $6)
  AND (
    NOT $7::boolean
    OR ($8::boolean AND (id = $9::bigint OR target_id = $9::bigint))
    OR (
      NOT $8::boolean
      AND target_username <> ''
      AND lower(target_username) LIKE $10::text || '%'
    )
  )
ORDER BY created_at DESC, id DESC
LIMIT $11`,
		statuses, string(filter.TargetType), filter.Reviewer, since, until,
		filter.BeforeID, prefix != "" || isNumeric, isNumeric, numeric,
		escapeLike(prefix), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list verification applications: %w", err)
	}
	defer rows.Close()
	out := make([]domain.VerificationApplication, 0, limit)
	for rows.Next() {
		app, err := scanVerificationApplication(rows)
		if err != nil {
			return nil, fmt.Errorf("scan verification application: %w", err)
		}
		out = append(out, app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verification applications: %w", err)
	}
	return out, nil
}

// VerificationApplicationsForApplicant returns the applicant's own history,
// newest first, for the bot's /status command.
func (s *VerificationStore) VerificationApplicationsForApplicant(ctx context.Context, applicantUserID int64, limit int) ([]domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("verification store is not configured")
	}
	if applicantUserID <= 0 {
		return nil, domain.ErrVerificationApplicationInvalid
	}
	limit = verificationLimit(limit)
	rows, err := s.db.Query(ctx, `
SELECT `+verificationApplicationColumnList+`
FROM verification_applications
WHERE applicant_user_id = $1
ORDER BY id DESC
LIMIT $2`, applicantUserID, limit)
	if err != nil {
		return nil, fmt.Errorf("list applicant verification applications: %w", err)
	}
	defer rows.Close()
	out := make([]domain.VerificationApplication, 0, limit)
	for rows.Next() {
		app, err := scanVerificationApplication(rows)
		if err != nil {
			return nil, fmt.Errorf("scan applicant verification application: %w", err)
		}
		out = append(out, app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applicant verification applications: %w", err)
	}
	return out, nil
}

// VerificationStatusCounts is the queue summary. Statuses nobody is in are absent
// rather than zero, which a map read cannot tell apart anyway.
func (s *VerificationStore) VerificationStatusCounts(ctx context.Context) (domain.VerificationStatusCounts, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("verification store is not configured")
	}
	rows, err := s.db.Query(ctx, `
SELECT status, count(*)
FROM verification_applications
GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count verification applications: %w", err)
	}
	defer rows.Close()
	out := make(domain.VerificationStatusCounts, 6)
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan verification status count: %w", err)
		}
		out[domain.VerificationStatus(status)] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verification status counts: %w", err)
	}
	return out, nil
}

// VerificationApplicationEvents returns the immutable history, newest first.
func (s *VerificationStore) VerificationApplicationEvents(ctx context.Context, applicationID int64, limit int) ([]domain.VerificationApplicationEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("verification store is not configured")
	}
	if applicationID <= 0 {
		return nil, domain.ErrVerificationApplicationInvalid
	}
	limit = verificationLimit(limit)
	rows, err := s.db.Query(ctx, `
SELECT id, application_id, kind, from_status, to_status, actor, reason, note,
       correlation_id, created_at
FROM verification_application_events
WHERE application_id = $1
ORDER BY id DESC
LIMIT $2`, applicationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list verification application events: %w", err)
	}
	defer rows.Close()
	out := make([]domain.VerificationApplicationEvent, 0, limit)
	for rows.Next() {
		var event domain.VerificationApplicationEvent
		var kind, from, to string
		if err := rows.Scan(&event.ID, &event.ApplicationID, &kind, &from, &to,
			&event.Actor, &event.Reason, &event.Note, &event.CorrelationID,
			&event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan verification application event: %w", err)
		}
		event.Kind = domain.VerificationApplicationEventKind(kind)
		event.FromStatus = domain.VerificationStatus(from)
		event.ToStatus = domain.VerificationStatus(to)
		event.CreatedAt = event.CreatedAt.UTC()
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verification application events: %w", err)
	}
	return out, nil
}

// LastVerificationRejection returns the newest rejected application for the
// applicant/target pair, which is what the re-application cooldown is measured
// from.
func (s *VerificationStore) LastVerificationRejection(ctx context.Context, applicantUserID int64, target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error) {
	if s == nil || s.db == nil {
		return domain.VerificationApplication{}, fmt.Errorf("verification store is not configured")
	}
	if applicantUserID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	if !target.Valid() || targetID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationTargetInvalid
	}
	app, err := scanVerificationApplication(s.db.QueryRow(ctx, `
SELECT `+verificationApplicationColumnList+`
FROM verification_applications
WHERE applicant_user_id = $1 AND target_type = $2 AND target_id = $3
  AND status = 'rejected'
ORDER BY reviewed_at DESC NULLS LAST, id DESC
LIMIT 1`, applicantUserID, string(target), targetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if err != nil {
		return domain.VerificationApplication{}, fmt.Errorf("get last verification rejection: %w", err)
	}
	return app, nil
}

// PendingVerificationNotifications returns undelivered outbox rows, oldest first.
// The application travels with the row because the worker renders the message
// text from it and must not need a second round trip per notification.
func (s *VerificationStore) PendingVerificationNotifications(ctx context.Context, limit int) ([]store.VerificationNotification, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("verification store is not configured")
	}
	limit = verificationLimit(limit)
	rows, err := s.db.Query(ctx, `
SELECT o.id, o.application_id, o.recipient_user_id, o.kind, o.attempts,
       `+verificationApplicationJoinColumns+`
FROM verification_notification_outbox o
JOIN verification_applications a ON a.id = o.application_id
WHERE o.delivered_at IS NULL
ORDER BY o.created_at, o.id
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending verification notifications: %w", err)
	}
	defer rows.Close()
	out := make([]store.VerificationNotification, 0, limit)
	for rows.Next() {
		var item store.VerificationNotification
		var row verificationApplicationRow
		dest := append([]any{&item.ID, &item.ApplicationID, &item.RecipientUserID,
			&item.Kind, &item.Attempts}, row.dest()...)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan pending verification notification: %w", err)
		}
		item.Application = row.application()
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending verification notifications: %w", err)
	}
	return out, nil
}

// MarkVerificationNotificationDelivered closes an outbox row and appends the
// 'notified' history entry, so the application timeline records that the
// applicant was actually told. Closing an already-closed row is a no-op: the
// outbox is exactly-once, not at-least-once.
func (s *VerificationStore) MarkVerificationNotificationDelivered(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("verification store is not configured")
	}
	if id <= 0 {
		return domain.ErrVerificationApplicationInvalid
	}
	return withTx(ctx, s.db, "deliver verification notification", func(tx pgx.Tx) error {
		var applicationID int64
		var kind string
		var deliveredAt *time.Time
		err := tx.QueryRow(ctx, `
SELECT application_id, kind, delivered_at
FROM verification_notification_outbox
WHERE id = $1
FOR UPDATE`, id).Scan(&applicationID, &kind, &deliveredAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVerificationApplicationNotFound
		}
		if err != nil {
			return fmt.Errorf("lock verification notification: %w", err)
		}
		if deliveredAt != nil {
			return nil
		}
		now := verificationNow()
		if _, err := tx.Exec(ctx, `
UPDATE verification_notification_outbox
SET delivered_at = $2, last_error = ''
WHERE id = $1`, id, now); err != nil {
			return fmt.Errorf("deliver verification notification: %w", err)
		}
		app, err := verificationApplicationByID(ctx, tx, applicationID)
		if err != nil {
			return err
		}
		// reason carries the notification kind so the timeline says which message
		// the applicant received.
		return insertVerificationEventTx(ctx, tx, verificationEventInput{
			applicationID: applicationID,
			kind:          domain.VerificationEventNotified,
			from:          app.Status,
			to:            app.Status,
			reason:        kind,
			correlationID: app.CorrelationID,
			createdAt:     now,
		})
	})
}

// MarkVerificationNotificationFailed records a delivery attempt. The row stays
// pending, so a poisoned notification keeps its attempt count and last error
// instead of spinning without a trace.
func (s *VerificationStore) MarkVerificationNotificationFailed(ctx context.Context, id int64, reason string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("verification store is not configured")
	}
	if id <= 0 {
		return domain.ErrVerificationApplicationInvalid
	}
	tag, err := s.db.Exec(ctx, `
UPDATE verification_notification_outbox
SET attempts = attempts + 1, last_error = $2
WHERE id = $1 AND delivered_at IS NULL`,
		id, truncateVerificationError(reason))
	if err != nil {
		return fmt.Errorf("fail verification notification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the row is gone or it is already delivered; both mean there is no
		// pending attempt left to record.
		var exists bool
		if err := s.db.QueryRow(ctx, `
SELECT true FROM verification_notification_outbox WHERE id = $1`, id).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVerificationApplicationNotFound
		} else if err != nil {
			return fmt.Errorf("get verification notification: %w", err)
		}
	}
	return nil
}

// ---- helpers ----------------------------------------------------------------

// verificationApplicationRow adapts the projection onto the domain type: the
// enum columns arrive as text and submitted_at / reviewed_at are nullable.
type verificationApplicationRow struct {
	app         domain.VerificationApplication
	targetType  string
	status      string
	submittedAt *time.Time
	reviewedAt  *time.Time
}

func (r *verificationApplicationRow) dest() []any {
	return []any{
		&r.app.ID, &r.app.ApplicantUserID, &r.targetType, &r.app.TargetID,
		&r.app.TargetTitle, &r.app.TargetUsername, &r.app.TargetAccessHash,
		&r.app.Category, &r.app.Description, &r.app.OfficialWebsite,
		&r.app.SocialLinks, &r.app.PressLinks, &r.app.AdditionalNote, &r.status,
		&r.app.ReviewerAdminID, &r.app.DecisionReason, &r.app.InternalNote,
		&r.app.CorrelationID, &r.app.CreatedAt, &r.app.UpdatedAt,
		&r.submittedAt, &r.reviewedAt, &r.app.Version,
	}
}

func (r *verificationApplicationRow) application() domain.VerificationApplication {
	app := r.app
	app.TargetType = domain.VerificationTargetType(r.targetType)
	app.Status = domain.VerificationStatus(r.status)
	app.CreatedAt = app.CreatedAt.UTC()
	app.UpdatedAt = app.UpdatedAt.UTC()
	if r.submittedAt != nil {
		app.SubmittedAt = r.submittedAt.UTC()
	}
	if r.reviewedAt != nil {
		app.ReviewedAt = r.reviewedAt.UTC()
	}
	// An empty text[] and a never-filled list must not read back differently
	// between the two backends.
	if len(app.SocialLinks) == 0 {
		app.SocialLinks = nil
	}
	if len(app.PressLinks) == 0 {
		app.PressLinks = nil
	}
	return app
}

func scanVerificationApplication(row pgx.Row) (domain.VerificationApplication, error) {
	var r verificationApplicationRow
	if err := row.Scan(r.dest()...); err != nil {
		return domain.VerificationApplication{}, err
	}
	return r.application(), nil
}

func verificationApplicationByID(ctx context.Context, db sqlcgen.DBTX, applicationID int64) (domain.VerificationApplication, error) {
	app, err := scanVerificationApplication(db.QueryRow(ctx, `
SELECT `+verificationApplicationColumnList+`
FROM verification_applications
WHERE id = $1`, applicationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if err != nil {
		return domain.VerificationApplication{}, fmt.Errorf("get verification application: %w", err)
	}
	return app, nil
}

// lockVerificationApplicationTx reads the application for mutation. FOR UPDATE
// plus the version guard on the following UPDATE is what serialises two
// reviewers: the second one blocks here and then sees the bumped version.
func lockVerificationApplicationTx(ctx context.Context, tx pgx.Tx, applicationID int64) (domain.VerificationApplication, error) {
	app, err := scanVerificationApplication(tx.QueryRow(ctx, `
SELECT `+verificationApplicationColumnList+`
FROM verification_applications
WHERE id = $1
FOR UPDATE`, applicationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if err != nil {
		return domain.VerificationApplication{}, fmt.Errorf("lock verification application: %w", err)
	}
	return app, nil
}

// activeVerificationApplicationTx resolves the application occupying a target.
// verification_applications_active_target_idx guarantees there is at most one, so
// no ordering is needed to pick it.
func activeVerificationApplicationTx(ctx context.Context, db sqlcgen.DBTX, target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error) {
	app, err := scanVerificationApplication(db.QueryRow(ctx, `
SELECT `+verificationApplicationColumnList+`
FROM verification_applications
WHERE target_type = $1 AND target_id = $2
  AND status IN ('draft', 'submitted', 'in_review')
LIMIT 1`, string(target), targetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if err != nil {
		return domain.VerificationApplication{}, fmt.Errorf("get active verification application: %w", err)
	}
	return app, nil
}

func verificationDraftForApplicantTx(ctx context.Context, db sqlcgen.DBTX, applicantUserID int64) (domain.VerificationApplication, error) {
	app, err := scanVerificationApplication(db.QueryRow(ctx, `
SELECT `+verificationApplicationColumnList+`
FROM verification_applications
WHERE applicant_user_id = $1 AND status = 'draft'
LIMIT 1`, applicantUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if err != nil {
		return domain.VerificationApplication{}, fmt.Errorf("get verification draft: %w", err)
	}
	return app, nil
}

func newestApprovedVerificationApplicationTx(ctx context.Context, tx pgx.Tx, target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error) {
	app, err := scanVerificationApplication(tx.QueryRow(ctx, `
SELECT `+verificationApplicationColumnList+`
FROM verification_applications
WHERE target_type = $1 AND target_id = $2 AND status = 'approved'
ORDER BY reviewed_at DESC NULLS LAST, id DESC
LIMIT 1
FOR UPDATE`, string(target), targetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if err != nil {
		return domain.VerificationApplication{}, fmt.Errorf("lock approved verification application: %w", err)
	}
	return app, nil
}

type verificationEventInput struct {
	applicationID int64
	kind          domain.VerificationApplicationEventKind
	from          domain.VerificationStatus
	to            domain.VerificationStatus
	actor         string
	reason        string
	note          string
	correlationID string
	createdAt     time.Time
}

// insertVerificationEventTx appends one history row. There is deliberately no
// update or delete counterpart: the timeline is the audit trail.
func insertVerificationEventTx(ctx context.Context, tx pgx.Tx, in verificationEventInput) error {
	if !in.kind.Valid() {
		return domain.ErrVerificationApplicationInvalid
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO verification_application_events (
  application_id, kind, from_status, to_status, actor, reason, note,
  correlation_id, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		in.applicationID, string(in.kind), string(in.from), string(in.to),
		in.actor, in.reason, in.note, in.correlationID, in.createdAt,
	); err != nil {
		return fmt.Errorf("insert verification application event: %w", err)
	}
	return nil
}

// insertVerificationNotificationTx enqueues the applicant notification. The
// unique key is the decision, not the attempt, so a retry that reaches this far
// cannot produce a second message.
func insertVerificationNotificationTx(ctx context.Context, tx pgx.Tx, applicationID, recipientUserID int64, kind string, now time.Time) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO verification_notification_outbox (
  application_id, recipient_user_id, kind, created_at
) VALUES ($1,$2,$3,$4)
ON CONFLICT ON CONSTRAINT `+verificationNotificationConstraint+` DO NOTHING`,
		applicationID, recipientUserID, kind, now,
	); err != nil {
		return fmt.Errorf("enqueue verification notification: %w", err)
	}
	return nil
}

func verificationNotificationExistsTx(ctx context.Context, tx pgx.Tx, applicationID int64, kind string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
SELECT true FROM verification_notification_outbox
WHERE application_id = $1 AND kind = $2`, applicationID, kind).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get verification notification: %w", err)
	}
	return exists, nil
}

func lockVerificationApplicant(ctx context.Context, tx pgx.Tx, applicantUserID int64) error {
	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("verification-applicant:%d", applicantUserID)); err != nil {
		return fmt.Errorf("lock verification applicant: %w", err)
	}
	return nil
}

func lockVerificationTarget(ctx context.Context, tx pgx.Tx, target domain.VerificationTargetType, targetID int64) error {
	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("verification-target:%s:%d", target, targetID)); err != nil {
		return fmt.Errorf("lock verification target: %w", err)
	}
	return nil
}

// validateVerificationDraftRequest checks a draft-creation request.
//
// It deliberately does not run SubmitVerificationApplicationRequest.Validate: a
// draft is opened before the bot has collected anything, so only the fields
// already present must be well formed. The submission bar is enforced by
// SubmitVerificationApplication.
func validateVerificationDraftRequest(req domain.SubmitVerificationApplicationRequest) error {
	if req.ApplicantUserID <= 0 || req.TargetID <= 0 || !req.TargetType.Valid() {
		return domain.ErrVerificationApplicationInvalid
	}
	if utf8.RuneCountInString(req.TargetTitle) > domain.MaxVerificationTitleLength {
		return domain.ErrVerificationApplicationInvalid
	}
	if len(req.TargetUsername) > maxVerificationUsernameBytes {
		return domain.ErrVerificationApplicationInvalid
	}
	if len(req.CorrelationID) > domain.MaxVerificationCorrelationLen {
		return domain.ErrVerificationApplicationInvalid
	}
	return req.Draft.ValidateDraft()
}

// verificationDraftOf projects the stored payload back onto the applicant input,
// so submission is validated against the same domain rules the bot dialog used.
func verificationDraftOf(app domain.VerificationApplication) domain.VerificationDraftInput {
	return domain.VerificationDraftInput{
		Category:        app.Category,
		Description:     app.Description,
		OfficialWebsite: app.OfficialWebsite,
		SocialLinks:     app.SocialLinks,
		PressLinks:      app.PressLinks,
		AdditionalNote:  app.AdditionalNote,
	}
}

func normalizeVerificationDecision(decision domain.VerificationDecision) domain.VerificationDecision {
	decision.Reviewer = strings.TrimSpace(decision.Reviewer)
	decision.Reason = strings.TrimSpace(decision.Reason)
	decision.InternalNote = strings.TrimSpace(decision.InternalNote)
	decision.CorrelationID = strings.TrimSpace(decision.CorrelationID)
	return decision
}

// verificationLinksArg keeps a NOT NULL text[] out of NULL: pgx encodes a nil
// slice as NULL, which the column rejects.
func verificationLinksArg(links []string) []string {
	if links == nil {
		return []string{}
	}
	return links
}

func verificationLimit(limit int) int {
	if limit <= 0 {
		return defaultVerificationListLimit
	}
	if limit > maxVerificationListLimit {
		return maxVerificationListLimit
	}
	return limit
}

func verificationFilterStatuses(filter domain.VerificationApplicationFilter) ([]string, error) {
	statuses := make([]string, 0, len(filter.Statuses))
	for _, status := range filter.Statuses {
		if !status.Valid() {
			return nil, domain.ErrVerificationApplicationInvalid
		}
		statuses = append(statuses, string(status))
	}
	return statuses, nil
}

// parseVerificationQuery splits the review-queue search term into its two
// shapes: a number addresses an application id or a peer id, anything else is a
// username prefix. Telegram usernames never start with a digit, so the two
// shapes cannot collide.
func parseVerificationQuery(query string) (numeric int64, isNumeric bool, prefix string) {
	query = strings.TrimSpace(query)
	query = strings.TrimPrefix(query, "@")
	query = strings.TrimSpace(query)
	if query == "" {
		return 0, false, ""
	}
	if id, err := strconv.ParseInt(query, 10, 64); err == nil && id > 0 {
		return id, true, ""
	}
	return 0, false, strings.ToLower(query)
}

// truncateVerificationError bounds the outbox error text at the column CHECK,
// on a rune boundary, because a worker hands over whatever the transport said.
func truncateVerificationError(reason string) string {
	if len(reason) <= maxVerificationOutboxErrorBytes {
		return reason
	}
	cut := maxVerificationOutboxErrorBytes
	for cut > 0 && !utf8.ValidString(reason[:cut]) {
		cut--
	}
	return reason[:cut]
}
