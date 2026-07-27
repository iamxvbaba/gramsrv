package memory

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Default page sizes applied when a caller leaves the limit unset. The
// PostgreSQL queries page with LIMIT, so an unset limit has to resolve to the
// same finite page in both backends.
const (
	defaultVerificationListLimit = 50
	maxVerificationListLimit     = 200
	// maxVerificationUsernameBytes mirrors the octet_length CHECK on
	// verification_applications.target_username, which the domain request shape
	// does not cover.
	maxVerificationUsernameBytes = 64
	// maxVerificationOutboxErrorBytes mirrors the octet_length CHECK on
	// verification_notification_outbox.last_error.
	maxVerificationOutboxErrorBytes = 1024
)

// VerificationStore is the in-memory implementation of store.VerificationStore.
// The RPC, bot and admin unit tests run against it, so it reproduces every
// invariant migration 0154 encodes as an index, a CHECK or a transaction
// boundary, and returns the same domain errors the PostgreSQL backend maps its
// violations onto:
//
//   - verification_applications_active_target_idx: at most one draft/submitted/
//     in_review application per target; a second one is
//     domain.ErrVerificationApplicationExists.
//   - verification_applications_applicant_draft_idx: at most one draft per
//     applicant, which is why an applicant who already has one gets it back with
//     created=false instead of a second draft.
//   - the reviewed_at/reviewer pairing CHECK: only a decided application carries
//     them, and a claim leaves reviewed_at unset.
//   - "status = 'draft' OR submitted_at IS NOT NULL": a cancelled draft is
//     stamped with its withdrawal time, because the schema has nowhere to record
//     "left before submitting".
//   - verification_notification_once: one outbox row per (application, kind),
//     which is what makes a repeated decision a no-op rather than a second
//     message to the applicant.
//   - the decision transaction: applyVerified / clearVerified run before anything
//     is committed to the maps, so a failing callback leaves the store exactly as
//     it was and "approved implies target verified" holds here too.
//
// Status transitions all go through domain.CanTransitionVerificationStatus; this
// file never re-implements the machine.
type VerificationStore struct {
	mu                sync.Mutex
	nextApplicationID int64
	nextEventID       int64
	nextOutboxID      int64
	applications      map[int64]domain.VerificationApplication
	events            map[int64][]domain.VerificationApplicationEvent
	outbox            map[int64]*memoryVerificationNotification
	outboxByKey       map[memoryVerificationNotificationKey]int64
	// lastNow keeps the store's clock strictly increasing, so keyset paging over
	// (created_at DESC, id DESC) is as deterministic as it is against PostgreSQL
	// even when several applications are created inside one microsecond.
	lastNow time.Time
}

type memoryVerificationNotificationKey struct {
	applicationID int64
	kind          string
}

type memoryVerificationNotification struct {
	id              int64
	applicationID   int64
	recipientUserID int64
	kind            string
	attempts        int
	deliveredAt     time.Time
	lastError       string
	createdAt       time.Time
}

// NewVerificationStore creates an empty store. Ids start at 1 so a zero
// VerificationApplication.ID keeps meaning "no application".
func NewVerificationStore() *VerificationStore {
	return &VerificationStore{
		nextApplicationID: 1,
		nextEventID:       1,
		nextOutboxID:      1,
		applications:      make(map[int64]domain.VerificationApplication),
		events:            make(map[int64][]domain.VerificationApplicationEvent),
		outbox:            make(map[int64]*memoryVerificationNotification),
		outboxByKey:       make(map[memoryVerificationNotificationKey]int64),
	}
}

var _ store.VerificationStore = (*VerificationStore)(nil)

// CreateVerificationDraft opens a draft for the applicant/target pair. The
// applicant's own draft wins over everything else: the bot dialog is a single
// conversation, so an applicant who already has one gets it back with
// created=false and the dialog resumes.
func (s *VerificationStore) CreateVerificationDraft(_ context.Context, req domain.SubmitVerificationApplicationRequest) (domain.VerificationApplication, bool, error) {
	req.TargetTitle = strings.TrimSpace(req.TargetTitle)
	req.TargetUsername = domain.NormalizeUsername(req.TargetUsername)
	req.CorrelationID = strings.TrimSpace(req.CorrelationID)
	req.Draft = req.Draft.Normalize()
	if err := validateVerificationDraftRequest(req); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, found := s.draftForApplicantLocked(req.ApplicantUserID); found {
		return copyVerificationApplication(existing), false, nil
	}
	if _, found := s.activeForTargetLocked(req.TargetType, req.TargetID); found {
		return domain.VerificationApplication{}, false, domain.ErrVerificationApplicationExists
	}
	now := s.nowLocked()
	app := domain.VerificationApplication{
		ID:              s.nextApplicationID,
		ApplicantUserID: req.ApplicantUserID,
		TargetType:      req.TargetType,
		TargetID:        req.TargetID,
		TargetTitle:     req.TargetTitle,
		TargetUsername:  req.TargetUsername,
		Category:        req.Draft.Category,
		Description:     req.Draft.Description,
		OfficialWebsite: req.Draft.OfficialWebsite,
		SocialLinks:     copyVerificationLinks(req.Draft.SocialLinks),
		PressLinks:      copyVerificationLinks(req.Draft.PressLinks),
		AdditionalNote:  req.Draft.AdditionalNote,
		Status:          domain.VerificationStatusDraft,
		CorrelationID:   req.CorrelationID,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	s.nextApplicationID++
	s.applications[app.ID] = app
	s.appendEventLocked(verificationEventInput{
		applicationID: app.ID,
		kind:          domain.VerificationEventCreated,
		to:            domain.VerificationStatusDraft,
		correlationID: app.CorrelationID,
		createdAt:     now,
	})
	return copyVerificationApplication(app), true, nil
}

// SaveVerificationDraft rewrites the applicant-supplied payload of a draft.
func (s *VerificationStore) SaveVerificationDraft(_ context.Context, applicationID int64, version int64, draft domain.VerificationDraftInput) (domain.VerificationApplication, error) {
	if applicationID <= 0 || version <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	draft = draft.Normalize()
	if err := draft.ValidateDraft(); err != nil {
		return domain.VerificationApplication{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.applicationAtVersionLocked(applicationID, version)
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if !current.Editable() {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	now := s.nowLocked()
	updated := current
	updated.Category = draft.Category
	updated.Description = draft.Description
	updated.OfficialWebsite = draft.OfficialWebsite
	updated.SocialLinks = copyVerificationLinks(draft.SocialLinks)
	updated.PressLinks = copyVerificationLinks(draft.PressLinks)
	updated.AdditionalNote = draft.AdditionalNote
	updated.Version = current.Version + 1
	updated.UpdatedAt = laterVerificationTime(current.UpdatedAt, now)
	s.applications[applicationID] = updated
	s.appendEventLocked(verificationEventInput{
		applicationID: applicationID,
		kind:          domain.VerificationEventUpdated,
		from:          current.Status,
		to:            updated.Status,
		correlationID: updated.CorrelationID,
		createdAt:     now,
	})
	return copyVerificationApplication(updated), nil
}

// SubmitVerificationApplication moves an application into the review queue and
// stamps submitted_at. The stored payload is validated against the submission bar
// here, so an incomplete draft cannot reach a reviewer through any path. Coming
// back from in_review releases the claim and keeps the original submitted_at.
func (s *VerificationStore) SubmitVerificationApplication(_ context.Context, applicationID int64, version int64) (domain.VerificationApplication, error) {
	if applicationID <= 0 || version <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.applicationAtVersionLocked(applicationID, version)
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if !domain.CanTransitionVerificationStatus(current.Status, domain.VerificationStatusSubmitted) {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	if err := verificationDraftOf(current).ValidateForSubmission(); err != nil {
		return domain.VerificationApplication{}, err
	}
	now := s.nowLocked()
	updated := current
	updated.Status = domain.VerificationStatusSubmitted
	updated.ReviewerAdminID = ""
	if updated.SubmittedAt.IsZero() {
		updated.SubmittedAt = now
	}
	updated.Version = current.Version + 1
	updated.UpdatedAt = laterVerificationTime(current.UpdatedAt, now)
	s.applications[applicationID] = updated
	s.appendEventLocked(verificationEventInput{
		applicationID: applicationID,
		kind:          domain.VerificationEventSubmitted,
		from:          current.Status,
		to:            updated.Status,
		correlationID: updated.CorrelationID,
		createdAt:     now,
	})
	return copyVerificationApplication(updated), nil
}

// CancelVerificationApplication withdraws an active application on the
// applicant's behalf. The reason is the applicant's, so it lands on the history
// row and never in DecisionReason, which stays reviewer-owned.
func (s *VerificationStore) CancelVerificationApplication(_ context.Context, applicationID int64, version int64, reason string) (domain.VerificationApplication, error) {
	reason = strings.TrimSpace(reason)
	if applicationID <= 0 || version <= 0 ||
		utf8.RuneCountInString(reason) > domain.MaxVerificationReasonLength {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.applicationAtVersionLocked(applicationID, version)
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if !domain.CanTransitionVerificationStatus(current.Status, domain.VerificationStatusCancelled) {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	now := s.nowLocked()
	updated := current
	updated.Status = domain.VerificationStatusCancelled
	// A cancelled draft was never submitted, but the schema requires
	// submitted_at on every non-draft row, so the withdrawal time stands in.
	if updated.SubmittedAt.IsZero() {
		updated.SubmittedAt = now
	}
	updated.Version = current.Version + 1
	updated.UpdatedAt = laterVerificationTime(current.UpdatedAt, now)
	s.applications[applicationID] = updated
	s.appendEventLocked(verificationEventInput{
		applicationID: applicationID,
		kind:          domain.VerificationEventCancelled,
		from:          current.Status,
		to:            updated.Status,
		reason:        reason,
		correlationID: updated.CorrelationID,
		createdAt:     now,
	})
	return copyVerificationApplication(updated), nil
}

// ClaimVerificationApplication assigns a reviewer and moves the application to
// in_review. ReviewedAt stays unset: a claim is not a decision, and the schema
// pairs reviewed_at with a decided status.
func (s *VerificationStore) ClaimVerificationApplication(_ context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, error) {
	decision = normalizeVerificationDecision(decision)
	if err := decision.Validate(); err != nil {
		return domain.VerificationApplication{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.applicationAtVersionLocked(decision.ApplicationID, decision.Version)
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if !domain.CanTransitionVerificationStatus(current.Status, domain.VerificationStatusInReview) {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	now := s.nowLocked()
	updated := current
	updated.Status = domain.VerificationStatusInReview
	updated.ReviewerAdminID = decision.Reviewer
	updated.Version = current.Version + 1
	updated.UpdatedAt = laterVerificationTime(current.UpdatedAt, now)
	s.applications[decision.ApplicationID] = updated
	s.appendEventLocked(verificationEventInput{
		applicationID: decision.ApplicationID,
		kind:          domain.VerificationEventClaimed,
		from:          current.Status,
		to:            updated.Status,
		actor:         decision.Reviewer,
		reason:        decision.Reason,
		note:          decision.InternalNote,
		correlationID: decision.CorrelationID,
		createdAt:     now,
	})
	return copyVerificationApplication(updated), nil
}

// DecideVerificationApplication records an approval or a rejection.
//
// The order of checks is the deterministic part of two reviewers acting at once:
// the version is compared first, so the loser of the race always gets
// domain.ErrVerificationVersionConflict rather than a silent no-op. A caller that
// re-issues the same decision with the current version sees the decided
// application with changed=false, no second history row and no second outbox row.
//
// On approve nothing is written until applyVerified has succeeded, which is the
// in-memory equivalent of the PostgreSQL transaction: a failing callback leaves
// the application undecided and the outbox empty.
func (s *VerificationStore) DecideVerificationApplication(ctx context.Context, decision domain.VerificationDecision, approve bool, applyVerified func(ctx context.Context, app domain.VerificationApplication) error) (domain.VerificationApplication, bool, error) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.applicationAtVersionLocked(decision.ApplicationID, decision.Version)
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if current.Status == target {
		// Already decided this way: keep the record and the single notification,
		// report that nothing moved.
		return copyVerificationApplication(current), false, nil
	}
	if !domain.CanTransitionVerificationStatus(current.Status, target) {
		return domain.VerificationApplication{}, false, domain.ErrVerificationStatusInvalid
	}
	now := s.nowLocked()
	updated := current
	updated.Status = target
	updated.ReviewerAdminID = decision.Reviewer
	updated.ReviewedAt = now
	updated.DecisionReason = decision.Reason
	updated.InternalNote = decision.InternalNote
	if updated.SubmittedAt.IsZero() {
		updated.SubmittedAt = now
	}
	updated.Version = current.Version + 1
	updated.UpdatedAt = laterVerificationTime(current.UpdatedAt, now)
	if approve {
		if err := applyVerified(ctx, copyVerificationApplication(updated)); err != nil {
			return domain.VerificationApplication{}, false, err
		}
	}
	s.applications[decision.ApplicationID] = updated
	s.appendEventLocked(verificationEventInput{
		applicationID: updated.ID,
		kind:          kind,
		from:          current.Status,
		to:            updated.Status,
		actor:         decision.Reviewer,
		reason:        decision.Reason,
		note:          decision.InternalNote,
		correlationID: decision.CorrelationID,
		createdAt:     now,
	})
	s.enqueueNotificationLocked(updated.ID, updated.ApplicantUserID, string(target), now)
	return copyVerificationApplication(updated), true, nil
}

// RevokeVerification clears the platform flag of a previously approved target.
//
// The application stays approved: it is the audit record of a decision that did
// happen. The revocation is its own history row plus its own outbox kind, and
// that outbox row is what makes a repeated revocation a no-op with changed=false.
//
// A target with no approved application on file is still cleared through the
// callback, because leaving a flag standing is worse than a missing audit row;
// there is then nothing to deduplicate against, so such a call always reports
// changed=true.
func (s *VerificationStore) RevokeVerification(ctx context.Context, req domain.VerificationRevocation, clearVerified func(ctx context.Context, target domain.Peer) error) (domain.VerificationApplication, bool, error) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.newestApprovedForTargetLocked(req.TargetType, req.TargetID)
	if found {
		key := memoryVerificationNotificationKey{
			applicationID: current.ID,
			kind:          string(domain.VerificationEventRevoked),
		}
		if _, done := s.outboxByKey[key]; done {
			return copyVerificationApplication(current), false, nil
		}
	}
	if err := clearVerified(ctx, domain.Peer{Type: req.TargetType.PeerType(), ID: req.TargetID}); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if found {
		now := s.nowLocked()
		s.appendEventLocked(verificationEventInput{
			applicationID: current.ID,
			kind:          domain.VerificationEventRevoked,
			from:          current.Status,
			to:            current.Status,
			actor:         req.Reviewer,
			reason:        req.Reason,
			note:          req.InternalNote,
			correlationID: req.CorrelationID,
			createdAt:     now,
		})
		s.enqueueNotificationLocked(current.ID, current.ApplicantUserID,
			string(domain.VerificationEventRevoked), now)
	}
	return copyVerificationApplication(current), true, nil
}

// VerificationApplication reads one application by id.
func (s *VerificationStore) VerificationApplication(_ context.Context, applicationID int64) (domain.VerificationApplication, error) {
	if applicationID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.applications[applicationID]
	if !ok {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return copyVerificationApplication(app), nil
}

// ActiveVerificationApplicationForTarget returns the live application occupying a
// target, if any.
func (s *VerificationStore) ActiveVerificationApplicationForTarget(_ context.Context, target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error) {
	if !target.Valid() || targetID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationTargetInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, found := s.activeForTargetLocked(target, targetID)
	if !found {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return copyVerificationApplication(app), nil
}

// VerificationDraftForApplicant returns the applicant's open draft, if any.
func (s *VerificationStore) VerificationDraftForApplicant(_ context.Context, applicantUserID int64) (domain.VerificationApplication, error) {
	if applicantUserID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, found := s.draftForApplicantLocked(applicantUserID)
	if !found {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return copyVerificationApplication(app), nil
}

// ListVerificationApplications is the review-queue query.
//
// Paging is keyset over (CreatedAt DESC, ID DESC): filter.Until and
// filter.BeforeID carry the last row of the previous page, filter.CreatedAt is
// the inclusive lower bound of the date range. Query matches an application id or
// a peer id when it is numeric and otherwise prefix-matches the lowercased
// username snapshot.
func (s *VerificationStore) ListVerificationApplications(_ context.Context, filter domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error) {
	for _, status := range filter.Statuses {
		if !status.Valid() {
			return nil, domain.ErrVerificationApplicationInvalid
		}
	}
	if filter.TargetType != "" && !filter.TargetType.Valid() {
		return nil, domain.ErrVerificationTargetInvalid
	}
	if len(filter.Reviewer) > domain.MaxVerificationReviewerLength {
		return nil, domain.ErrVerificationApplicationInvalid
	}
	limit := verificationLimit(filter.Limit)
	numeric, isNumeric, prefix := parseVerificationQuery(filter.Query)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.VerificationApplication, 0, limit)
	for _, app := range s.applicationsByQueueOrderLocked() {
		if len(filter.Statuses) > 0 && !containsVerificationStatus(filter.Statuses, app.Status) {
			continue
		}
		if filter.TargetType != "" && app.TargetType != filter.TargetType {
			continue
		}
		if filter.Reviewer != "" && app.ReviewerAdminID != filter.Reviewer {
			continue
		}
		if !filter.CreatedAt.IsZero() && app.CreatedAt.Before(filter.CreatedAt) {
			continue
		}
		if !filter.Until.IsZero() {
			if !beforeVerificationCursor(app, filter.Until, filter.BeforeID) {
				continue
			}
		} else if filter.BeforeID != 0 && app.ID >= filter.BeforeID {
			continue
		}
		if isNumeric && app.ID != numeric && app.TargetID != numeric {
			continue
		}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(app.TargetUsername), prefix) {
			continue
		}
		out = append(out, copyVerificationApplication(app))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// VerificationApplicationsForApplicant returns the applicant's own history,
// newest first, for the bot's /status command.
func (s *VerificationStore) VerificationApplicationsForApplicant(_ context.Context, applicantUserID int64, limit int) ([]domain.VerificationApplication, error) {
	if applicantUserID <= 0 {
		return nil, domain.ErrVerificationApplicationInvalid
	}
	limit = verificationLimit(limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int64, 0, len(s.applications))
	for id, app := range s.applications {
		if app.ApplicantUserID == applicantUserID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	out := make([]domain.VerificationApplication, 0, limit)
	for _, id := range ids {
		if len(out) == limit {
			break
		}
		out = append(out, copyVerificationApplication(s.applications[id]))
	}
	return out, nil
}

// VerificationStatusCounts is the queue summary. Statuses nobody is in are absent
// rather than zero, which a map read cannot tell apart anyway.
func (s *VerificationStore) VerificationStatusCounts(_ context.Context) (domain.VerificationStatusCounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(domain.VerificationStatusCounts, 6)
	for _, app := range s.applications {
		out[app.Status]++
	}
	return out, nil
}

// VerificationApplicationEvents returns the immutable history, newest first.
func (s *VerificationStore) VerificationApplicationEvents(_ context.Context, applicationID int64, limit int) ([]domain.VerificationApplicationEvent, error) {
	if applicationID <= 0 {
		return nil, domain.ErrVerificationApplicationInvalid
	}
	limit = verificationLimit(limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.events[applicationID]
	out := make([]domain.VerificationApplicationEvent, 0, limit)
	for i := len(history) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, history[i])
	}
	return out, nil
}

// LastVerificationRejection returns the newest rejected application for the
// applicant/target pair, which is what the re-application cooldown is measured
// from.
func (s *VerificationStore) LastVerificationRejection(_ context.Context, applicantUserID int64, target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error) {
	if applicantUserID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	if !target.Valid() || targetID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationTargetInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var best domain.VerificationApplication
	found := false
	for _, app := range s.applications {
		if app.ApplicantUserID != applicantUserID || app.TargetType != target ||
			app.TargetID != targetID || app.Status != domain.VerificationStatusRejected {
			continue
		}
		if !found || newerVerificationDecision(app, best) {
			best = app
			found = true
		}
	}
	if !found {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return copyVerificationApplication(best), nil
}

// PendingVerificationNotifications returns undelivered outbox rows, oldest first.
// The application travels with the row because the worker renders the message
// text from it.
func (s *VerificationStore) PendingVerificationNotifications(_ context.Context, limit int) ([]store.VerificationNotification, error) {
	limit = verificationLimit(limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := make([]*memoryVerificationNotification, 0, len(s.outbox))
	for _, item := range s.outbox {
		if item.deliveredAt.IsZero() {
			pending = append(pending, item)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if !pending[i].createdAt.Equal(pending[j].createdAt) {
			return pending[i].createdAt.Before(pending[j].createdAt)
		}
		return pending[i].id < pending[j].id
	})
	out := make([]store.VerificationNotification, 0, limit)
	for _, item := range pending {
		if len(out) == limit {
			break
		}
		out = append(out, store.VerificationNotification{
			ID:              item.id,
			ApplicationID:   item.applicationID,
			RecipientUserID: item.recipientUserID,
			Kind:            item.kind,
			Attempts:        item.attempts,
			Application:     copyVerificationApplication(s.applications[item.applicationID]),
		})
	}
	return out, nil
}

// MarkVerificationNotificationDelivered closes an outbox row and appends the
// 'notified' history entry, so the application timeline records that the
// applicant was actually told. Closing an already-closed row is a no-op: the
// outbox is exactly-once, not at-least-once.
func (s *VerificationStore) MarkVerificationNotificationDelivered(_ context.Context, id int64) error {
	if id <= 0 {
		return domain.ErrVerificationApplicationInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.outbox[id]
	if !ok {
		return domain.ErrVerificationApplicationNotFound
	}
	if !item.deliveredAt.IsZero() {
		return nil
	}
	now := s.nowLocked()
	item.deliveredAt = now
	item.lastError = ""
	app := s.applications[item.applicationID]
	// reason carries the notification kind so the timeline says which message the
	// applicant received.
	s.appendEventLocked(verificationEventInput{
		applicationID: item.applicationID,
		kind:          domain.VerificationEventNotified,
		from:          app.Status,
		to:            app.Status,
		reason:        item.kind,
		correlationID: app.CorrelationID,
		createdAt:     now,
	})
	return nil
}

// MarkVerificationNotificationFailed records a delivery attempt. The row stays
// pending, so a poisoned notification keeps its attempt count and last error
// instead of spinning without a trace.
func (s *VerificationStore) MarkVerificationNotificationFailed(_ context.Context, id int64, reason string) error {
	if id <= 0 {
		return domain.ErrVerificationApplicationInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.outbox[id]
	if !ok {
		return domain.ErrVerificationApplicationNotFound
	}
	if !item.deliveredAt.IsZero() {
		// Already delivered: there is no pending attempt left to record.
		return nil
	}
	item.attempts++
	item.lastError = truncateVerificationError(reason)
	return nil
}

// ---- locked helpers ---------------------------------------------------------

// applicationAtVersionLocked resolves the mutation target and applies the
// optimistic-locking check every mutation shares.
func (s *VerificationStore) applicationAtVersionLocked(applicationID, version int64) (domain.VerificationApplication, error) {
	app, ok := s.applications[applicationID]
	if !ok {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if app.Version != version {
		return domain.VerificationApplication{}, domain.ErrVerificationVersionConflict
	}
	return app, nil
}

func (s *VerificationStore) draftForApplicantLocked(applicantUserID int64) (domain.VerificationApplication, bool) {
	for _, id := range s.sortedApplicationIDsLocked() {
		app := s.applications[id]
		if app.ApplicantUserID == applicantUserID && app.Status == domain.VerificationStatusDraft {
			return app, true
		}
	}
	return domain.VerificationApplication{}, false
}

func (s *VerificationStore) activeForTargetLocked(target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, bool) {
	for _, id := range s.sortedApplicationIDsLocked() {
		app := s.applications[id]
		if app.TargetType == target && app.TargetID == targetID && app.Status.Active() {
			return app, true
		}
	}
	return domain.VerificationApplication{}, false
}

func (s *VerificationStore) newestApprovedForTargetLocked(target domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, bool) {
	var best domain.VerificationApplication
	found := false
	for _, app := range s.applications {
		if app.TargetType != target || app.TargetID != targetID ||
			app.Status != domain.VerificationStatusApproved {
			continue
		}
		if !found || newerVerificationDecision(app, best) {
			best = app
			found = true
		}
	}
	return best, found
}

func (s *VerificationStore) sortedApplicationIDsLocked() []int64 {
	ids := make([]int64, 0, len(s.applications))
	for id := range s.applications {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// applicationsByQueueOrderLocked is the (created_at DESC, id DESC) projection the
// review queue pages through.
func (s *VerificationStore) applicationsByQueueOrderLocked() []domain.VerificationApplication {
	out := make([]domain.VerificationApplication, 0, len(s.applications))
	for _, app := range s.applications {
		out = append(out, app)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// appendEventLocked appends one history row. There is deliberately no update or
// delete counterpart: the timeline is the audit trail.
func (s *VerificationStore) appendEventLocked(in verificationEventInput) {
	event := domain.VerificationApplicationEvent{
		ID:            s.nextEventID,
		ApplicationID: in.applicationID,
		Kind:          in.kind,
		FromStatus:    in.from,
		ToStatus:      in.to,
		Actor:         in.actor,
		Reason:        in.reason,
		Note:          in.note,
		CorrelationID: in.correlationID,
		CreatedAt:     in.createdAt,
	}
	s.nextEventID++
	s.events[in.applicationID] = append(s.events[in.applicationID], event)
}

// enqueueNotificationLocked enqueues the applicant notification. The unique key
// is the decision, not the attempt, so a retry that reaches this far cannot
// produce a second message.
func (s *VerificationStore) enqueueNotificationLocked(applicationID, recipientUserID int64, kind string, now time.Time) {
	key := memoryVerificationNotificationKey{applicationID: applicationID, kind: kind}
	if _, exists := s.outboxByKey[key]; exists {
		return
	}
	item := &memoryVerificationNotification{
		id:              s.nextOutboxID,
		applicationID:   applicationID,
		recipientUserID: recipientUserID,
		kind:            kind,
		createdAt:       now,
	}
	s.nextOutboxID++
	s.outbox[item.id] = item
	s.outboxByKey[key] = item.id
}

// nowLocked hands out strictly increasing timestamps at timestamptz resolution.
func (s *VerificationStore) nowLocked() time.Time {
	now := time.Now().UTC().Truncate(time.Microsecond)
	if !now.After(s.lastNow) {
		now = s.lastNow.Add(time.Microsecond)
	}
	s.lastNow = now
	return now
}

// ---- helpers ----------------------------------------------------------------

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

// copyVerificationApplication deep-copies the link slices so stored state cannot
// be mutated through a returned application.
func copyVerificationApplication(app domain.VerificationApplication) domain.VerificationApplication {
	app.SocialLinks = copyVerificationLinks(app.SocialLinks)
	app.PressLinks = copyVerificationLinks(app.PressLinks)
	return app
}

// copyVerificationLinks normalises an empty list to nil, matching what the
// PostgreSQL backend reads back out of an empty text[].
func copyVerificationLinks(links []string) []string {
	if len(links) == 0 {
		return nil
	}
	out := make([]string, len(links))
	copy(out, links)
	return out
}

func containsVerificationStatus(statuses []domain.VerificationStatus, status domain.VerificationStatus) bool {
	for _, item := range statuses {
		if item == status {
			return true
		}
	}
	return false
}

// beforeVerificationCursor is the (created_at, id) < (until, beforeID) keyset
// comparison the SQL row-value predicate performs.
func beforeVerificationCursor(app domain.VerificationApplication, until time.Time, beforeID int64) bool {
	if app.CreatedAt.Before(until) {
		return true
	}
	return app.CreatedAt.Equal(until) && app.ID < beforeID
}

// newerVerificationDecision orders decided applications the way
// "reviewed_at DESC NULLS LAST, id DESC" does.
func newerVerificationDecision(candidate, best domain.VerificationApplication) bool {
	if !candidate.ReviewedAt.Equal(best.ReviewedAt) {
		if candidate.ReviewedAt.IsZero() {
			return false
		}
		if best.ReviewedAt.IsZero() {
			return true
		}
		return candidate.ReviewedAt.After(best.ReviewedAt)
	}
	return candidate.ID > best.ID
}

func laterVerificationTime(current, now time.Time) time.Time {
	if now.After(current) {
		return now
	}
	return current
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

// truncateVerificationError bounds the outbox error text at the column CHECK the
// PostgreSQL backend has to satisfy, on a rune boundary.
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
