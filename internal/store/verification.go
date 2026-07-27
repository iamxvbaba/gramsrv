package store

import (
	"context"

	"telesrv/internal/domain"
)

// VerificationStore owns official verification applications, their immutable
// history and the applicant-notification outbox.
//
// Every mutation is expected to be atomic with the history row it produces, and
// every status change is guarded by the caller-supplied version: two reviewers
// deciding the same application concurrently must produce exactly one decision
// and one notification.
type VerificationStore interface {
	// CreateVerificationDraft opens a draft for the applicant/target pair. An
	// active application on the target reports domain.ErrVerificationApplicationExists;
	// an existing draft of the same applicant is returned with created=false so
	// the bot dialog can resume it.
	CreateVerificationDraft(ctx context.Context, req domain.SubmitVerificationApplicationRequest) (app domain.VerificationApplication, created bool, err error)
	// SaveVerificationDraft rewrites the applicant-supplied payload of a draft.
	// The application must be in draft and at the given version.
	SaveVerificationDraft(ctx context.Context, applicationID int64, version int64, draft domain.VerificationDraftInput) (domain.VerificationApplication, error)
	// SubmitVerificationApplication moves a draft to submitted and stamps
	// submitted_at.
	SubmitVerificationApplication(ctx context.Context, applicationID int64, version int64) (domain.VerificationApplication, error)
	// CancelVerificationApplication withdraws an active application on the
	// applicant's behalf.
	CancelVerificationApplication(ctx context.Context, applicationID int64, version int64, reason string) (domain.VerificationApplication, error)
	// ClaimVerificationApplication assigns a reviewer and moves the application to
	// in_review.
	ClaimVerificationApplication(ctx context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, error)
	// DecideVerificationApplication records an approval or a rejection.
	//
	// approve=true is the only path that flips the platform flag, and the store
	// does it in the same transaction as the status change through the supplied
	// applyVerified callback, so the invariant "approved implies target verified"
	// cannot be broken by a crash between two writes. The callback receives the
	// application as it will be stored and must set the flag on the target peer.
	//
	// The notification outbox row is written in the same transaction and is unique
	// per (application, kind), which makes a repeated approve a no-op that returns
	// changed=false instead of notifying twice.
	DecideVerificationApplication(ctx context.Context, decision domain.VerificationDecision, approve bool, applyVerified func(ctx context.Context, app domain.VerificationApplication) error) (app domain.VerificationApplication, changed bool, err error)
	// RevokeVerification clears the platform flag of a previously approved target
	// through the same callback discipline, appends a revoked event to the newest
	// approved application for that target, and enqueues the applicant
	// notification. The application itself stays approved: it is history.
	RevokeVerification(ctx context.Context, req domain.VerificationRevocation, clearVerified func(ctx context.Context, target domain.Peer) error) (app domain.VerificationApplication, changed bool, err error)
	// VerificationApplication reads one application by id.
	VerificationApplication(ctx context.Context, applicationID int64) (domain.VerificationApplication, error)
	// ActiveVerificationApplicationForTarget returns the live application
	// occupying a target, if any.
	ActiveVerificationApplicationForTarget(ctx context.Context, target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error)
	// VerificationDraftForApplicant returns the applicant's open draft, if any.
	VerificationDraftForApplicant(ctx context.Context, applicantUserID int64) (domain.VerificationApplication, error)
	// ListVerificationApplications is the review-queue query with keyset paging.
	ListVerificationApplications(ctx context.Context, filter domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error)
	// VerificationApplicationsForApplicant returns the applicant's own history,
	// newest first, for the bot's /status command.
	VerificationApplicationsForApplicant(ctx context.Context, applicantUserID int64, limit int) ([]domain.VerificationApplication, error)
	// VerificationStatusCounts is the queue summary.
	VerificationStatusCounts(ctx context.Context) (domain.VerificationStatusCounts, error)
	// VerificationApplicationEvents returns the immutable history, newest first.
	VerificationApplicationEvents(ctx context.Context, applicationID int64, limit int) ([]domain.VerificationApplicationEvent, error)
	// LastVerificationRejection returns the newest rejected application for the
	// applicant/target pair, which is what the re-application cooldown is measured
	// from.
	LastVerificationRejection(ctx context.Context, applicantUserID int64, target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error)
	// PendingVerificationNotifications returns undelivered outbox rows, oldest
	// first, for the delivery worker.
	PendingVerificationNotifications(ctx context.Context, limit int) ([]VerificationNotification, error)
	// MarkVerificationNotificationDelivered closes an outbox row.
	MarkVerificationNotificationDelivered(ctx context.Context, id int64) error
	// MarkVerificationNotificationFailed records a delivery attempt so a poisoned
	// row cannot spin forever without a trace.
	MarkVerificationNotificationFailed(ctx context.Context, id int64, reason string) error
}

// VerificationNotification is one queued applicant notification.
type VerificationNotification struct {
	ID              int64
	ApplicationID   int64
	RecipientUserID int64
	Kind            string
	Attempts        int
	Application     domain.VerificationApplication
}
