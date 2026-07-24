package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *ModerationReportStore) ListModerationCases(ctx context.Context, filter domain.ModerationCaseFilter) ([]domain.ModerationCase, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("moderation case store is not configured")
	}
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	statuses := make([]string, len(filter.Statuses))
	for i, status := range filter.Statuses {
		statuses[i] = string(status)
	}
	var before any
	if !filter.BeforeUpdate.IsZero() {
		before = filter.BeforeUpdate
	}
	rows, err := s.db.Query(ctx, `
SELECT id, target_peer_type, target_peer_id, status, severity,
       assigned_to, version, report_count, distinct_reporter_count,
       first_report_at, last_report_at, created_at, updated_at
FROM moderation_cases
WHERE (cardinality($1::text[]) = 0 OR status = ANY($1::text[]))
  AND ($2 = '' OR assigned_to = $2)
  AND ($3 = '' OR target_peer_type = $3)
  AND ($4 = 0 OR target_peer_id = $4)
  AND (
    $5::timestamptz IS NULL
    OR (updated_at, id) < ($5::timestamptz, $6)
  )
ORDER BY updated_at DESC, id DESC
LIMIT $7`,
		statuses, filter.AssignedTo, string(filter.Target.Type),
		filter.Target.ID, before, filter.BeforeID, filter.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list moderation cases: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ModerationCase, 0, filter.Limit)
	for rows.Next() {
		item, err := scanModerationCase(rows)
		if err != nil {
			return nil, fmt.Errorf("scan moderation case: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate moderation cases: %w", err)
	}
	return out, nil
}

func (s *ModerationReportStore) GetModerationCase(ctx context.Context, caseID int64) (domain.ModerationCaseDetail, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation case store is not configured")
	}
	if caseID <= 0 {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseInvalid
	}
	return getModerationCase(ctx, s.db, caseID)
}

func (s *ModerationReportStore) ClaimModerationCase(ctx context.Context, caseID, expectedVersion int64, actor string, now time.Time) (domain.ModerationCase, error) {
	if s == nil || s.db == nil {
		return domain.ModerationCase{}, fmt.Errorf("moderation case store is not configured")
	}
	if caseID <= 0 || expectedVersion <= 0 || actor == "" ||
		len(actor) > domain.MaxModerationActorBytes || now.IsZero() {
		return domain.ModerationCase{}, domain.ErrModerationCaseInvalid
	}
	row := s.db.QueryRow(ctx, `
UPDATE moderation_cases
SET status = CASE
      WHEN status = 'appeal_review' THEN status
      ELSE 'in_review'
    END,
    assigned_to = $3,
    version = version + 1,
    updated_at = greatest(updated_at, $4)
WHERE id = $1
  AND version = $2
  AND status IN ('open', 'in_review', 'appeal_review')
  AND (assigned_to = '' OR assigned_to = $3)
RETURNING id, target_peer_type, target_peer_id, status, severity,
          assigned_to, version, report_count, distinct_reporter_count,
          first_report_at, last_report_at, created_at, updated_at`,
		caseID, expectedVersion, actor, now,
	)
	item, err := scanModerationCase(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationCase{}, domain.ErrModerationCaseConflict
	}
	if err != nil {
		return domain.ModerationCase{}, fmt.Errorf("claim moderation case: %w", err)
	}
	return item, nil
}

func (s *ModerationReportStore) DecideModerationCase(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation case store is not configured")
	}
	if err := request.Validate(); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if request.AppealID != 0 {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation case store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("begin moderation decision: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingCaseID int64
	var existingFingerprint []byte
	err = tx.QueryRow(ctx, `
SELECT case_id, fingerprint
FROM moderation_decisions
WHERE command_id = $1`, request.CommandID).Scan(&existingCaseID, &existingFingerprint)
	if err == nil {
		if existingCaseID != request.CaseID ||
			len(existingFingerprint) != len(request.Fingerprint) ||
			!bytes.Equal(existingFingerprint, request.Fingerprint[:]) {
			return domain.ModerationCaseDetail{}, false, domain.ErrModerationActionConflict
		}
		detail, found, err := getModerationCase(ctx, tx, existingCaseID)
		return detail, false, requireModerationCase(found, err)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("lookup moderation decision command: %w", err)
	}

	current, err := scanModerationCase(tx.QueryRow(ctx, `
SELECT id, target_peer_type, target_peer_id, status, severity,
       assigned_to, version, report_count, distinct_reporter_count,
       first_report_at, last_report_at, created_at, updated_at
FROM moderation_cases
WHERE id = $1
FOR UPDATE`, request.CaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseNotFound
	}
	if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("lock moderation case: %w", err)
	}
	if err := lockModerationTarget(ctx, tx, current.Target); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if current.Version != request.ExpectedVersion ||
		(current.Status != domain.ModerationCaseInReview &&
			!(current.Status == domain.ModerationCaseActionFailed &&
				request.Kind == domain.ModerationDecisionViolation)) ||
		current.AssignedTo != request.Actor {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseConflict
	}
	if err := validateModerationActionsForTarget(current.Target, request.Actions); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if _, err := insertModerationDecision(ctx, tx, request); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	nextStatus := domain.ModerationCaseDismissed
	if request.Kind == domain.ModerationDecisionViolation {
		nextStatus = domain.ModerationCaseActionPending
	}
	if _, err := tx.Exec(ctx, `
UPDATE moderation_cases
SET status = $2,
    assigned_to = CASE WHEN $2 = 'action_pending' THEN assigned_to ELSE '' END,
    version = version + 1,
    updated_at = greatest(updated_at, $3)
WHERE id = $1`, request.CaseID, string(nextStatus), request.CreatedAt); err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("finish moderation decision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("commit moderation decision: %w", err)
	}
	detail, found, err := getModerationCase(ctx, s.db, request.CaseID)
	return detail, true, requireModerationCase(found, err)
}

func (s *ModerationReportStore) ReviewModerationAppeal(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation case store is not configured")
	}
	if err := request.Validate(); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if request.AppealID <= 0 ||
		(request.Kind != domain.ModerationDecisionAppealGrant &&
			request.Kind != domain.ModerationDecisionAppealDeny) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation case store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("begin moderation appeal review: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingCaseID int64
	var existingFingerprint []byte
	err = tx.QueryRow(ctx, `
SELECT case_id, fingerprint
FROM moderation_decisions
WHERE command_id = $1`, request.CommandID).Scan(&existingCaseID, &existingFingerprint)
	if err == nil {
		if existingCaseID != request.CaseID ||
			len(existingFingerprint) != len(request.Fingerprint) ||
			!bytes.Equal(existingFingerprint, request.Fingerprint[:]) {
			return domain.ModerationCaseDetail{}, false, domain.ErrModerationActionConflict
		}
		detail, found, err := getModerationCase(ctx, tx, existingCaseID)
		return detail, false, requireModerationCase(found, err)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("lookup moderation appeal command: %w", err)
	}

	current, err := scanModerationCase(tx.QueryRow(ctx, `
SELECT id, target_peer_type, target_peer_id, status, severity,
       assigned_to, version, report_count, distinct_reporter_count,
       first_report_at, last_report_at, created_at, updated_at
FROM moderation_cases
WHERE id = $1
FOR UPDATE`, request.CaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseNotFound
	}
	if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("lock moderation appeal case: %w", err)
	}
	if err := lockModerationTarget(ctx, tx, current.Target); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if current.Version != request.ExpectedVersion ||
		current.Status != domain.ModerationCaseAppealReview ||
		current.AssignedTo != request.Actor {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseConflict
	}
	var appealStatus, previousStatus string
	if err := tx.QueryRow(ctx, `
SELECT status, previous_case_status
FROM moderation_appeals
WHERE id = $1 AND case_id = $2
FOR UPDATE`, request.AppealID, request.CaseID).Scan(&appealStatus, &previousStatus); errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseNotFound
	} else if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("lock moderation appeal: %w", err)
	}
	if appealStatus != string(domain.ModerationAppealPending) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseConflict
	}
	if err := validateModerationActionsForTarget(current.Target, request.Actions); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if request.Kind == domain.ModerationDecisionAppealGrant {
		if err := validateAppealSanctionOwnership(ctx, tx, current, request.Actions); err != nil {
			return domain.ModerationCaseDetail{}, false, err
		}
	}
	if _, err := insertModerationDecision(ctx, tx, request); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	reviewStatus := domain.ModerationAppealRejected
	nextCaseStatus := domain.ModerationCaseStatus(previousStatus)
	if request.Kind == domain.ModerationDecisionAppealGrant {
		reviewStatus = domain.ModerationAppealGranted
		nextCaseStatus = domain.ModerationCaseDismissed
		if len(request.Actions) > 0 {
			nextCaseStatus = domain.ModerationCaseActionPending
		}
	}
	if _, err := tx.Exec(ctx, `
UPDATE moderation_appeals
SET status = $2, reviewer = $3, review_reason = $4, reviewed_at = $5
WHERE id = $1`,
		request.AppealID, string(reviewStatus), request.Actor,
		request.Reason, request.CreatedAt,
	); err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("finish moderation appeal: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE moderation_cases
SET status = $2,
    assigned_to = CASE WHEN $2 = 'action_pending' THEN assigned_to ELSE '' END,
    version = version + 1,
    updated_at = greatest(updated_at, $3)
WHERE id = $1`, request.CaseID, string(nextCaseStatus), request.CreatedAt); err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("finish moderation appeal case: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("commit moderation appeal review: %w", err)
	}
	detail, found, err := getModerationCase(ctx, s.db, request.CaseID)
	return detail, true, requireModerationCase(found, err)
}

func insertModerationDecision(ctx context.Context, tx pgx.Tx, request domain.ModerationDecisionRequest) (int64, error) {
	var decisionID int64
	if err := tx.QueryRow(ctx, `
INSERT INTO moderation_decisions (
  case_id, appeal_id, kind, actor, reason, command_id, fingerprint,
  created_at
) VALUES ($1,nullif($2,0),$3,$4,$5,$6,$7,$8)
RETURNING id`,
		request.CaseID, request.AppealID, string(request.Kind),
		request.Actor, request.Reason, request.CommandID,
		request.Fingerprint[:], request.CreatedAt,
	).Scan(&decisionID); err != nil {
		return 0, fmt.Errorf("insert moderation decision: %w", err)
	}
	for i, action := range request.Actions {
		commandID := fmt.Sprintf("%s:%03d", request.CommandID, i)
		if _, err := tx.Exec(ctx, `
INSERT INTO moderation_actions (
  case_id, decision_id, kind, payload, status, attempts, available_at,
  command_id, created_at, updated_at
) VALUES ($1,$2,$3,$4::jsonb,'pending',0,$5,$6,$5,$5)`,
			request.CaseID, decisionID, string(action.Kind), []byte(action.Payload),
			request.CreatedAt, commandID,
		); err != nil {
			return 0, fmt.Errorf("insert moderation action %d: %w", i, err)
		}
	}
	return decisionID, nil
}

func (s *ModerationReportStore) CreateModerationAppeal(ctx context.Context, appeal domain.ModerationAppeal) (domain.ModerationAppeal, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("moderation case store is not configured")
	}
	if err := appeal.Validate(); err != nil || appeal.ID != 0 ||
		appeal.Status != domain.ModerationAppealPending {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ModerationAppeal{}, false, fmt.Errorf("moderation case store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("begin moderation appeal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, found, err := getModerationAppealByFingerprint(ctx, tx, appeal.Fingerprint)
	if err != nil {
		return domain.ModerationAppeal{}, false, err
	}
	if found {
		return existing, false, nil
	}
	current, err := scanModerationCase(tx.QueryRow(ctx, `
SELECT id, target_peer_type, target_peer_id, status, severity,
       assigned_to, version, report_count, distinct_reporter_count,
       first_report_at, last_report_at, created_at, updated_at
FROM moderation_cases
WHERE id = $1
FOR UPDATE`, appeal.CaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseNotFound
	}
	if err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("lock moderation case for appeal: %w", err)
	}
	if current.Status != domain.ModerationCaseResolved &&
		current.Status != domain.ModerationCaseDismissed {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseConflict
	}
	if current.Target.Type == domain.PeerTypeUser &&
		current.Target.ID != appeal.AppellantUserID {
		return domain.ModerationAppeal{}, false, domain.ErrModerationPermissionDenied
	}
	var id int64
	if err := tx.QueryRow(ctx, `
INSERT INTO moderation_appeals (
  case_id, appellant_user_id, appeal_text, text_hash, fingerprint,
  status, previous_case_status, created_at
) VALUES ($1,$2,$3,$4,$5,'pending',$6,$7)
RETURNING id`,
		appeal.CaseID, appeal.AppellantUserID, appeal.Text,
		appeal.TextHash[:], appeal.Fingerprint[:],
		string(appeal.PreviousCaseStatus), appeal.CreatedAt,
	).Scan(&id); err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("insert moderation appeal: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE moderation_cases
SET status = 'appeal_review',
    assigned_to = '',
    version = version + 1,
    updated_at = greatest(updated_at, $2)
WHERE id = $1`, appeal.CaseID, appeal.CreatedAt); err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("queue moderation appeal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("commit moderation appeal: %w", err)
	}
	appeal.ID = id
	return appeal, true, nil
}

func (s *ModerationReportStore) GetModerationAppeal(ctx context.Context, appealID int64) (domain.ModerationAppeal, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("moderation case store is not configured")
	}
	if appealID <= 0 {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseInvalid
	}
	appeal, err := scanModerationAppeal(s.db.QueryRow(ctx, `
SELECT id, case_id, appellant_user_id, appeal_text, text_hash,
       fingerprint, status, previous_case_status, reviewer, review_reason,
       created_at, reviewed_at
FROM moderation_appeals
WHERE id = $1`, appealID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationAppeal{}, false, nil
	}
	if err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("get moderation appeal: %w", err)
	}
	return appeal, true, nil
}

func (s *ModerationReportStore) IssueModerationAppealLink(ctx context.Context, link domain.ModerationAppealLink) (domain.ModerationAppealLink, error) {
	if s == nil || s.db == nil {
		return domain.ModerationAppealLink{}, fmt.Errorf("moderation case store is not configured")
	}
	if err := link.Validate(); err != nil || link.ID != 0 ||
		link.AppealID != 0 || !link.ConsumedAt.IsZero() {
		return domain.ModerationAppealLink{}, domain.ErrModerationAppealLinkInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ModerationAppealLink{}, fmt.Errorf("moderation case store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ModerationAppealLink{}, fmt.Errorf("begin moderation appeal link: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var peerType, status string
	var peerID int64
	if err := tx.QueryRow(ctx, `
SELECT target_peer_type, target_peer_id, status
FROM moderation_cases
WHERE id = $1
FOR UPDATE`, link.CaseID).Scan(&peerType, &peerID, &status); errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationAppealLink{}, domain.ErrModerationCaseNotFound
	} else if err != nil {
		return domain.ModerationAppealLink{}, fmt.Errorf("get moderation case for appeal link: %w", err)
	}
	if domain.PeerType(peerType) != domain.PeerTypeUser ||
		peerID != link.AppellantUserID {
		return domain.ModerationAppealLink{}, domain.ErrModerationPermissionDenied
	}
	if domain.ModerationCaseStatus(status) != domain.ModerationCaseActionPending &&
		domain.ModerationCaseStatus(status) != domain.ModerationCaseResolved {
		return domain.ModerationAppealLink{}, domain.ErrModerationCaseConflict
	}
	var linkCount int
	if err := tx.QueryRow(ctx, `
SELECT count(*)
FROM moderation_appeal_links
WHERE case_id = $1`, link.CaseID).Scan(&linkCount); err != nil {
		return domain.ModerationAppealLink{}, fmt.Errorf("count moderation appeal links: %w", err)
	}
	if linkCount >= domain.MaxModerationAppealLinksPerCase {
		return domain.ModerationAppealLink{}, domain.ErrModerationActionConflict
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO moderation_appeal_links (
  case_id, appellant_user_id, token_hash, expires_at, created_at
) VALUES ($1,$2,$3,$4,$5)
RETURNING id`,
		link.CaseID, link.AppellantUserID, link.TokenHash[:],
		link.ExpiresAt, link.CreatedAt,
	).Scan(&link.ID); err != nil {
		if isUniqueViolation(err) {
			return domain.ModerationAppealLink{}, domain.ErrModerationActionConflict
		}
		return domain.ModerationAppealLink{}, fmt.Errorf("insert moderation appeal link: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModerationAppealLink{}, fmt.Errorf("commit moderation appeal link: %w", err)
	}
	return link, nil
}

func (s *ModerationReportStore) GetModerationAppealLink(ctx context.Context, tokenHash [32]byte, now time.Time) (domain.ModerationAppealLink, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationAppealLink{}, false, fmt.Errorf("moderation case store is not configured")
	}
	if tokenHash == ([32]byte{}) || now.IsZero() {
		return domain.ModerationAppealLink{}, false, domain.ErrModerationAppealLinkInvalid
	}
	link, err := scanModerationAppealLink(s.db.QueryRow(ctx, `
SELECT id, case_id, appellant_user_id, token_hash, expires_at,
       appeal_id, created_at, consumed_at
FROM moderation_appeal_links
WHERE token_hash = $1 AND expires_at > $2`, tokenHash[:], now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationAppealLink{}, false, nil
	}
	if err != nil {
		return domain.ModerationAppealLink{}, false, fmt.Errorf("get moderation appeal link: %w", err)
	}
	return link, true, nil
}

func (s *ModerationReportStore) SubmitModerationAppealByLink(ctx context.Context, tokenHash [32]byte, text string, now time.Time) (domain.ModerationAppeal, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("moderation case store is not configured")
	}
	if tokenHash == ([32]byte{}) || now.IsZero() {
		return domain.ModerationAppeal{}, false, domain.ErrModerationAppealLinkInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ModerationAppeal{}, false, fmt.Errorf("moderation case store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("begin moderation linked appeal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	link, err := scanModerationAppealLink(tx.QueryRow(ctx, `
SELECT id, case_id, appellant_user_id, token_hash, expires_at,
       appeal_id, created_at, consumed_at
FROM moderation_appeal_links
WHERE token_hash = $1
FOR UPDATE`, tokenHash[:]))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !link.ExpiresAt.After(now)) {
		return domain.ModerationAppeal{}, false, domain.ErrModerationAppealLinkInvalid
	}
	if err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("lock moderation appeal link: %w", err)
	}
	if link.AppealID > 0 {
		appeal, err := scanModerationAppeal(tx.QueryRow(ctx, `
SELECT id, case_id, appellant_user_id, appeal_text, text_hash,
       fingerprint, status, previous_case_status, reviewer, review_reason,
       created_at, reviewed_at
FROM moderation_appeals
WHERE id = $1`, link.AppealID))
		if err != nil {
			return domain.ModerationAppeal{}, false, fmt.Errorf("get linked moderation appeal: %w", err)
		}
		return appeal, false, nil
	}
	current, err := scanModerationCase(tx.QueryRow(ctx, `
SELECT id, target_peer_type, target_peer_id, status, severity,
       assigned_to, version, report_count, distinct_reporter_count,
       first_report_at, last_report_at, created_at, updated_at
FROM moderation_cases
WHERE id = $1
FOR UPDATE`, link.CaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseNotFound
	}
	if err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("lock moderation linked appeal case: %w", err)
	}
	if current.Target.Type != domain.PeerTypeUser ||
		current.Target.ID != link.AppellantUserID {
		return domain.ModerationAppeal{}, false, domain.ErrModerationPermissionDenied
	}
	existing, found, err := getPendingModerationAppeal(ctx, tx, link.CaseID, link.AppellantUserID)
	if err != nil {
		return domain.ModerationAppeal{}, false, err
	}
	if found {
		if _, err := tx.Exec(ctx, `
UPDATE moderation_appeal_links
SET appeal_id = $2, consumed_at = $3
WHERE id = $1`, link.ID, existing.ID, now); err != nil {
			return domain.ModerationAppeal{}, false, fmt.Errorf("consume moderation appeal link: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.ModerationAppeal{}, false, fmt.Errorf("commit existing linked appeal: %w", err)
		}
		return existing, false, nil
	}
	if current.Status != domain.ModerationCaseResolved &&
		current.Status != domain.ModerationCaseDismissed {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseConflict
	}
	appeal, err := domain.NewModerationAppeal(
		current.ID, link.AppellantUserID, current.Status, text, now,
	)
	if err != nil {
		return domain.ModerationAppeal{}, false, err
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO moderation_appeals (
  case_id, appellant_user_id, appeal_text, text_hash, fingerprint,
  status, previous_case_status, created_at
) VALUES ($1,$2,$3,$4,$5,'pending',$6,$7)
RETURNING id`,
		appeal.CaseID, appeal.AppellantUserID, appeal.Text,
		appeal.TextHash[:], appeal.Fingerprint[:],
		string(appeal.PreviousCaseStatus), appeal.CreatedAt,
	).Scan(&appeal.ID); err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("insert linked moderation appeal: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE moderation_cases
SET status = 'appeal_review', assigned_to = '',
    version = version + 1, updated_at = greatest(updated_at, $2)
WHERE id = $1`, current.ID, now); err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("queue linked moderation appeal: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE moderation_appeal_links
SET appeal_id = $2, consumed_at = $3
WHERE id = $1`, link.ID, appeal.ID, now); err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("consume linked moderation appeal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("commit linked moderation appeal: %w", err)
	}
	return appeal, true, nil
}

func (s *ModerationReportStore) DeleteExpiredModerationAppealLinks(ctx context.Context, olderThan time.Time, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("moderation case store is not configured")
	}
	if olderThan.IsZero() || limit <= 0 || limit > 10000 {
		return 0, domain.ErrModerationAppealLinkInvalid
	}
	tag, err := s.db.Exec(ctx, `
WITH doomed AS (
  SELECT id
  FROM moderation_appeal_links
  WHERE expires_at < $1
  ORDER BY expires_at, id
  LIMIT $2
)
DELETE FROM moderation_appeal_links l
USING doomed d
WHERE l.id = d.id`, olderThan, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired moderation appeal links: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *ModerationReportStore) ClaimModerationActions(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.ModerationAction, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("moderation case store is not configured")
	}
	if now.IsZero() || limit <= 0 || limit > 100 || lease <= 0 {
		return nil, domain.ErrModerationActionInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return nil, fmt.Errorf("moderation case store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim moderation actions: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
WITH candidates AS (
  SELECT id
  FROM moderation_actions
  WHERE attempts < $4
    AND (
      (status IN ('pending', 'retry') AND available_at <= $1)
      OR (status = 'processing' AND lease_until <= $1)
    )
  ORDER BY available_at, id
  FOR UPDATE SKIP LOCKED
  LIMIT $2
)
UPDATE moderation_actions a
SET status = 'processing',
    attempts = a.attempts + 1,
    lease_until = $1 + make_interval(secs => $3::double precision),
    updated_at = $1
FROM candidates c
WHERE a.id = c.id
RETURNING a.id, a.case_id, a.decision_id, a.kind, a.payload, a.status,
          a.attempts, a.available_at, a.lease_until, a.last_error,
          a.command_id, a.created_at, a.updated_at`,
		now, limit, max(int64(1), int64((lease+time.Second-1)/time.Second)),
		domain.MaxModerationActionAttempts,
	)
	if err != nil {
		return nil, fmt.Errorf("claim moderation actions: %w", err)
	}
	out := make([]domain.ModerationAction, 0, limit)
	for rows.Next() {
		action, err := scanModerationAction(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan claimed moderation action: %w", err)
		}
		out = append(out, action)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate claimed moderation actions: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claimed moderation actions: %w", err)
	}
	return out, nil
}

func (s *ModerationReportStore) IsModerationActionCurrent(ctx context.Context, action domain.ModerationAction) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("moderation case store is not configured")
	}
	if action.ID <= 0 || action.CaseID <= 0 {
		return false, domain.ErrModerationActionInvalid
	}
	family, scoped := action.Kind.SanctionFamily()
	if !scoped {
		return true, nil
	}
	kinds := moderationSanctionKinds(family)
	var latestID int64
	err := s.db.QueryRow(ctx, `
SELECT candidate.id
FROM moderation_cases current_case
JOIN moderation_cases target_case
  ON target_case.target_peer_type = current_case.target_peer_type
 AND target_case.target_peer_id = current_case.target_peer_id
JOIN moderation_actions candidate ON candidate.case_id = target_case.id
WHERE current_case.id = $1
  AND candidate.kind = ANY($2::text[])
ORDER BY candidate.id DESC
LIMIT 1`, action.CaseID, kinds).Scan(&latestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrModerationActionConflict
	}
	if err != nil {
		return false, fmt.Errorf("get current moderation sanction action: %w", err)
	}
	return latestID == action.ID, nil
}

func (s *ModerationReportStore) SupersedeModerationAction(ctx context.Context, actionID int64, expectedAttempts int, now time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("moderation case store is not configured")
	}
	if actionID <= 0 || expectedAttempts <= 0 || now.IsZero() {
		return domain.ErrModerationActionInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return fmt.Errorf("moderation case store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin supersede moderation action: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var caseID, decisionID int64
	var attempts int
	if err := tx.QueryRow(ctx, `
SELECT case_id, decision_id, attempts
FROM moderation_actions
WHERE id = $1 AND status = 'processing'
FOR UPDATE`, actionID).Scan(&caseID, &decisionID, &attempts); errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrModerationActionConflict
	} else if err != nil {
		return fmt.Errorf("lock superseded moderation action: %w", err)
	}
	if attempts != expectedAttempts {
		return domain.ErrModerationActionConflict
	}
	if _, err := tx.Exec(ctx, `
UPDATE moderation_actions
SET status = 'superseded', lease_until = NULL, last_error = '',
    updated_at = $2
WHERE id = $1`, actionID, now); err != nil {
		return fmt.Errorf("supersede moderation action: %w", err)
	}
	if err := finishModerationDecision(ctx, tx, caseID, decisionID, now); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit superseded moderation action: %w", err)
	}
	return nil
}

func (s *ModerationReportStore) CompleteModerationAction(ctx context.Context, actionID int64, expectedAttempts int, succeeded bool, errorText string, retryAt, now time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("moderation case store is not configured")
	}
	if actionID <= 0 || expectedAttempts <= 0 ||
		len(errorText) > 4000 || now.IsZero() ||
		(!succeeded && retryAt.Before(now)) {
		return domain.ErrModerationActionInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return fmt.Errorf("moderation case store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin complete moderation action: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var caseID, decisionID int64
	var attempts int
	if err := tx.QueryRow(ctx, `
SELECT case_id, decision_id, attempts
FROM moderation_actions
WHERE id = $1 AND status = 'processing'
FOR UPDATE`, actionID).Scan(&caseID, &decisionID, &attempts); errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrModerationActionConflict
	} else if err != nil {
		return fmt.Errorf("lock moderation action: %w", err)
	}
	if attempts != expectedAttempts {
		return domain.ErrModerationActionConflict
	}
	if succeeded {
		if _, err := tx.Exec(ctx, `
UPDATE moderation_actions
SET status = 'succeeded', lease_until = NULL, last_error = '',
    updated_at = $2
WHERE id = $1`, actionID, now); err != nil {
			return fmt.Errorf("complete moderation action: %w", err)
		}
		if err := finishModerationDecision(ctx, tx, caseID, decisionID, now); err != nil {
			return err
		}
	} else {
		nextStatus := domain.ModerationActionRetry
		if attempts >= domain.MaxModerationActionAttempts {
			nextStatus = domain.ModerationActionFailed
		}
		if _, err := tx.Exec(ctx, `
UPDATE moderation_actions
SET status = $2, lease_until = NULL, last_error = $3,
    available_at = $4, updated_at = $5
WHERE id = $1`,
			actionID, string(nextStatus), errorText, retryAt, now,
		); err != nil {
			return fmt.Errorf("retry moderation action: %w", err)
		}
		if nextStatus == domain.ModerationActionFailed {
			if _, err := tx.Exec(ctx, `
UPDATE moderation_cases
SET status = 'action_failed', version = version + 1,
    updated_at = greatest(updated_at, $2)
WHERE id = $1 AND status = 'action_pending'`, caseID, now); err != nil {
				return fmt.Errorf("mark moderation case action failed: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit moderation action completion: %w", err)
	}
	return nil
}

func finishModerationDecision(ctx context.Context, tx pgx.Tx, caseID, decisionID int64, now time.Time) error {
	var incomplete int
	if err := tx.QueryRow(ctx, `
SELECT count(*)
FROM moderation_actions
WHERE decision_id = $1 AND status NOT IN ('succeeded', 'superseded')`,
		decisionID,
	).Scan(&incomplete); err != nil {
		return fmt.Errorf("count incomplete moderation actions: %w", err)
	}
	if incomplete != 0 {
		return nil
	}
	var decisionKind string
	if err := tx.QueryRow(ctx, `
SELECT kind FROM moderation_decisions WHERE id = $1`,
		decisionID,
	).Scan(&decisionKind); err != nil {
		return fmt.Errorf("get completed moderation decision: %w", err)
	}
	finalStatus := domain.ModerationCaseResolved
	if decisionKind == string(domain.ModerationDecisionAppealGrant) {
		finalStatus = domain.ModerationCaseDismissed
	}
	if _, err := tx.Exec(ctx, `
UPDATE moderation_cases
SET status = $3, assigned_to = '', version = version + 1,
    updated_at = greatest(updated_at, $2)
WHERE id = $1 AND status = 'action_pending'`,
		caseID, now, string(finalStatus),
	); err != nil {
		return fmt.Errorf("resolve moderation case: %w", err)
	}
	return nil
}

func lockModerationTarget(ctx context.Context, tx pgx.Tx, target domain.Peer) error {
	key := fmt.Sprintf("moderation-target:%s:%d", target.Type, target.ID)
	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("lock moderation target: %w", err)
	}
	return nil
}

func validateAppealSanctionOwnership(ctx context.Context, tx pgx.Tx, current domain.ModerationCase, actions []domain.ModerationActionDraft) error {
	for _, action := range actions {
		family, scoped := action.Kind.SanctionFamily()
		if !scoped {
			continue
		}
		var caseID int64
		var kind string
		err := tx.QueryRow(ctx, `
SELECT candidate.case_id, candidate.kind
FROM moderation_cases target_case
JOIN moderation_actions candidate ON candidate.case_id = target_case.id
WHERE target_case.target_peer_type = $1
  AND target_case.target_peer_id = $2
  AND candidate.kind = ANY($3::text[])
ORDER BY candidate.id DESC
LIMIT 1`, string(current.Target.Type), current.Target.ID,
			moderationSanctionKinds(family),
		).Scan(&caseID, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrModerationActionConflict
		}
		if err != nil {
			return fmt.Errorf("get current appeal sanction owner: %w", err)
		}
		if caseID != current.ID {
			return domain.ErrModerationActionConflict
		}
		switch action.Kind {
		case domain.ModerationActionClearPeerFlags:
			if domain.ModerationActionKind(kind) != domain.ModerationActionMarkScam &&
				domain.ModerationActionKind(kind) != domain.ModerationActionMarkFake {
				return domain.ErrModerationActionConflict
			}
		case domain.ModerationActionUnfreezeAccount:
			if domain.ModerationActionKind(kind) != domain.ModerationActionFreezeAccount {
				return domain.ErrModerationActionConflict
			}
		default:
			return domain.ErrModerationActionConflict
		}
	}
	return nil
}

func moderationSanctionKinds(family domain.ModerationSanctionFamily) []string {
	switch family {
	case domain.ModerationSanctionPeerFlags:
		return []string{
			string(domain.ModerationActionMarkScam),
			string(domain.ModerationActionMarkFake),
			string(domain.ModerationActionClearPeerFlags),
		}
	case domain.ModerationSanctionAccountFreeze:
		return []string{
			string(domain.ModerationActionFreezeAccount),
			string(domain.ModerationActionUnfreezeAccount),
		}
	default:
		return nil
	}
}

func validateModerationActionsForTarget(target domain.Peer, actions []domain.ModerationActionDraft) error {
	for _, action := range actions {
		switch action.Kind {
		case domain.ModerationActionMarkScam, domain.ModerationActionMarkFake,
			domain.ModerationActionClearPeerFlags:
			// Both users and channels support protocol scam/fake flags.
		case domain.ModerationActionFreezeAccount,
			domain.ModerationActionUnfreezeAccount,
			domain.ModerationActionDeletePrivateMessage,
			domain.ModerationActionDeleteAccount:
			if target.Type != domain.PeerTypeUser {
				return domain.ErrModerationActionInvalid
			}
		case domain.ModerationActionDeleteChannelMessage:
			if target.Type != domain.PeerTypeChannel {
				return domain.ErrModerationActionInvalid
			}
		default:
			return domain.ErrModerationActionInvalid
		}
	}
	return nil
}

type moderationCaseScanner interface {
	Scan(dest ...any) error
}

func scanModerationCase(row moderationCaseScanner) (domain.ModerationCase, error) {
	var item domain.ModerationCase
	var peerType, status string
	var severity int16
	if err := row.Scan(
		&item.ID, &peerType, &item.Target.ID, &status, &severity,
		&item.AssignedTo, &item.Version, &item.ReportCount,
		&item.DistinctReporterCount, &item.FirstReportAt,
		&item.LastReportAt, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return domain.ModerationCase{}, err
	}
	item.Target.Type = domain.PeerType(peerType)
	item.Status = domain.ModerationCaseStatus(status)
	item.Severity = domain.ModerationSeverity(severity)
	if err := item.Validate(); err != nil {
		return domain.ModerationCase{}, err
	}
	return item, nil
}

func getModerationCase(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, caseID int64) (domain.ModerationCaseDetail, bool, error) {
	item, err := scanModerationCase(db.QueryRow(ctx, `
SELECT id, target_peer_type, target_peer_id, status, severity,
       assigned_to, version, report_count, distinct_reporter_count,
       first_report_at, last_report_at, created_at, updated_at
FROM moderation_cases
WHERE id = $1`, caseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationCaseDetail{}, false, nil
	}
	if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("get moderation case: %w", err)
	}
	detail := domain.ModerationCaseDetail{Case: item}

	rows, err := db.Query(ctx, `
SELECT report_id
FROM moderation_case_reports
WHERE case_id = $1
ORDER BY report_id DESC
LIMIT $2`, caseID, domain.MaxModerationCaseDetailEntries)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("list moderation case reports: %w", err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return domain.ModerationCaseDetail{}, false, err
		}
		detail.ReportIDs = append(detail.ReportIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ModerationCaseDetail{}, false, err
	}
	rows.Close()

	rows, err = db.Query(ctx, `
SELECT id, case_id, appeal_id, kind, actor, reason, command_id,
       fingerprint, created_at
FROM moderation_decisions
WHERE case_id = $1
ORDER BY id DESC
LIMIT $2`, caseID, domain.MaxModerationCaseDetailEntries)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("list moderation decisions: %w", err)
	}
	for rows.Next() {
		var decision domain.ModerationDecision
		var kind string
		var fingerprint []byte
		var appealID *int64
		if err := rows.Scan(
			&decision.ID, &decision.CaseID, &appealID, &kind, &decision.Actor,
			&decision.Reason, &decision.CommandID, &fingerprint,
			&decision.CreatedAt,
		); err != nil {
			rows.Close()
			return domain.ModerationCaseDetail{}, false, err
		}
		if len(fingerprint) != len(decision.Fingerprint) {
			rows.Close()
			return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseInvalid
		}
		decision.Kind = domain.ModerationDecisionKind(kind)
		if appealID != nil {
			decision.AppealID = *appealID
		}
		copy(decision.Fingerprint[:], fingerprint)
		detail.Decisions = append(detail.Decisions, decision)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ModerationCaseDetail{}, false, err
	}
	rows.Close()

	rows, err = db.Query(ctx, `
SELECT id, case_id, decision_id, kind, payload, status, attempts,
       available_at, lease_until, last_error, command_id, created_at,
       updated_at
FROM moderation_actions
WHERE case_id = $1
ORDER BY id DESC
LIMIT $2`, caseID, domain.MaxModerationCaseDetailEntries)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("list moderation actions: %w", err)
	}
	for rows.Next() {
		action, err := scanModerationAction(rows)
		if err != nil {
			rows.Close()
			return domain.ModerationCaseDetail{}, false, err
		}
		detail.Actions = append(detail.Actions, action)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ModerationCaseDetail{}, false, err
	}
	rows.Close()

	rows, err = db.Query(ctx, `
SELECT id, case_id, appellant_user_id, appeal_text, text_hash,
       fingerprint, status, previous_case_status, reviewer, review_reason,
       created_at, reviewed_at
FROM moderation_appeals
WHERE case_id = $1
ORDER BY id DESC
LIMIT $2`, caseID, domain.MaxModerationCaseDetailEntries)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("list moderation appeals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		appeal, err := scanModerationAppeal(rows)
		if err != nil {
			return domain.ModerationCaseDetail{}, false, err
		}
		detail.Appeals = append(detail.Appeals, appeal)
	}
	if err := rows.Err(); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	return detail, true, nil
}

func scanModerationAction(row moderationCaseScanner) (domain.ModerationAction, error) {
	var action domain.ModerationAction
	var kind, status string
	var payload []byte
	var leaseUntil *time.Time
	if err := row.Scan(
		&action.ID, &action.CaseID, &action.DecisionID, &kind, &payload,
		&status, &action.Attempts, &action.AvailableAt, &leaseUntil,
		&action.LastError, &action.CommandID, &action.CreatedAt,
		&action.UpdatedAt,
	); err != nil {
		return domain.ModerationAction{}, err
	}
	action.Kind = domain.ModerationActionKind(kind)
	action.Status = domain.ModerationActionStatus(status)
	if leaseUntil != nil {
		action.LeaseUntil = *leaseUntil
	}
	canonical, err := domain.CanonicalModerationActionPayload(payload)
	if err != nil {
		return domain.ModerationAction{}, err
	}
	action.Payload = canonical
	if err := action.Validate(); err != nil {
		return domain.ModerationAction{}, err
	}
	return action, nil
}

func getModerationAppealByFingerprint(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, fingerprint [32]byte) (domain.ModerationAppeal, bool, error) {
	appeal, err := scanModerationAppeal(db.QueryRow(ctx, `
SELECT id, case_id, appellant_user_id, appeal_text, text_hash,
       fingerprint, status, previous_case_status, reviewer, review_reason,
       created_at, reviewed_at
FROM moderation_appeals
WHERE fingerprint = $1`, fingerprint[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationAppeal{}, false, nil
	}
	if err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("get moderation appeal: %w", err)
	}
	return appeal, true, nil
}

func getPendingModerationAppeal(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, caseID, appellantUserID int64) (domain.ModerationAppeal, bool, error) {
	appeal, err := scanModerationAppeal(db.QueryRow(ctx, `
SELECT id, case_id, appellant_user_id, appeal_text, text_hash,
       fingerprint, status, previous_case_status, reviewer, review_reason,
       created_at, reviewed_at
FROM moderation_appeals
WHERE case_id = $1 AND appellant_user_id = $2 AND status = 'pending'`,
		caseID, appellantUserID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationAppeal{}, false, nil
	}
	if err != nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("get pending moderation appeal: %w", err)
	}
	return appeal, true, nil
}

func scanModerationAppeal(row moderationCaseScanner) (domain.ModerationAppeal, error) {
	var appeal domain.ModerationAppeal
	var textHash, fingerprint []byte
	var status, previousStatus string
	var reviewedAt *time.Time
	if err := row.Scan(
		&appeal.ID, &appeal.CaseID, &appeal.AppellantUserID,
		&appeal.Text, &textHash, &fingerprint, &status,
		&previousStatus, &appeal.Reviewer, &appeal.ReviewReason,
		&appeal.CreatedAt,
		&reviewedAt,
	); err != nil {
		return domain.ModerationAppeal{}, err
	}
	if len(textHash) != len(appeal.TextHash) ||
		len(fingerprint) != len(appeal.Fingerprint) {
		return domain.ModerationAppeal{}, domain.ErrModerationCaseInvalid
	}
	copy(appeal.TextHash[:], textHash)
	copy(appeal.Fingerprint[:], fingerprint)
	appeal.Status = domain.ModerationAppealStatus(status)
	appeal.PreviousCaseStatus = domain.ModerationCaseStatus(previousStatus)
	if reviewedAt != nil {
		appeal.ReviewedAt = *reviewedAt
	}
	if err := appeal.Validate(); err != nil {
		return domain.ModerationAppeal{}, err
	}
	return appeal, nil
}

func scanModerationAppealLink(row moderationCaseScanner) (domain.ModerationAppealLink, error) {
	var link domain.ModerationAppealLink
	var tokenHash []byte
	var appealID *int64
	var consumedAt *time.Time
	if err := row.Scan(
		&link.ID, &link.CaseID, &link.AppellantUserID, &tokenHash,
		&link.ExpiresAt, &appealID, &link.CreatedAt, &consumedAt,
	); err != nil {
		return domain.ModerationAppealLink{}, err
	}
	if len(tokenHash) != len(link.TokenHash) {
		return domain.ModerationAppealLink{}, domain.ErrModerationAppealLinkInvalid
	}
	copy(link.TokenHash[:], tokenHash)
	if appealID != nil {
		link.AppealID = *appealID
	}
	if consumedAt != nil {
		link.ConsumedAt = *consumedAt
	}
	if err := link.Validate(); err != nil {
		return domain.ModerationAppealLink{}, err
	}
	return link, nil
}

func requireModerationCase(found bool, err error) error {
	if err != nil {
		return err
	}
	if !found {
		return domain.ErrModerationCaseNotFound
	}
	return nil
}
