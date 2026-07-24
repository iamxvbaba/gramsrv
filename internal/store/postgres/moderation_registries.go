package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *ModerationReportStore) CreateSponsoredMessageImpression(ctx context.Context, impression domain.SponsoredMessageImpression) (domain.SponsoredMessageImpression, bool, error) {
	if s == nil || s.db == nil {
		return domain.SponsoredMessageImpression{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if err := impression.Validate(); err != nil || impression.ID != 0 ||
		impression.ReportID != 0 {
		return domain.SponsoredMessageImpression{}, false, domain.ErrModerationReportInvalid
	}
	err := s.db.QueryRow(ctx, `
INSERT INTO sponsored_message_impressions (
  user_id, random_id_hash, target_peer_type, target_peer_id,
  author_user_id, evidence_schema_version, evidence, evidence_hash,
  created_at, expires_at
) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10)
ON CONFLICT (user_id, random_id_hash) DO NOTHING
RETURNING id`,
		impression.UserID, impression.RandomIDHash[:],
		string(impression.Target.Type), impression.Target.ID,
		impression.AuthorUserID, impression.EvidenceSchemaVersion,
		[]byte(impression.Evidence), impression.EvidenceHash[:],
		impression.CreatedAt, impression.ExpiresAt,
	).Scan(&impression.ID)
	if err == nil {
		return impression, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SponsoredMessageImpression{}, false, fmt.Errorf("insert sponsored impression: %w", err)
	}
	existing, found, err := s.GetSponsoredMessageImpression(
		ctx, impression.UserID, impression.RandomIDHash, impression.CreatedAt,
	)
	if err != nil {
		return domain.SponsoredMessageImpression{}, false, err
	}
	if !found || existing.Target != impression.Target ||
		existing.AuthorUserID != impression.AuthorUserID ||
		existing.EvidenceHash != impression.EvidenceHash ||
		!existing.ExpiresAt.Equal(impression.ExpiresAt) {
		return domain.SponsoredMessageImpression{}, false, domain.ErrModerationActionConflict
	}
	return existing, false, nil
}

func (s *ModerationReportStore) GetSponsoredMessageImpression(ctx context.Context, userID int64, randomIDHash [32]byte, now time.Time) (domain.SponsoredMessageImpression, bool, error) {
	if s == nil || s.db == nil {
		return domain.SponsoredMessageImpression{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if userID <= 0 || randomIDHash == ([32]byte{}) || now.IsZero() {
		return domain.SponsoredMessageImpression{}, false, domain.ErrModerationReportInvalid
	}
	impression, err := scanSponsoredMessageImpression(s.db.QueryRow(ctx, `
SELECT id, user_id, random_id_hash, target_peer_type, target_peer_id,
       author_user_id, evidence_schema_version, evidence, evidence_hash,
       report_id, created_at, expires_at
FROM sponsored_message_impressions
WHERE user_id = $1 AND random_id_hash = $2 AND expires_at > $3`,
		userID, randomIDHash[:], now,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SponsoredMessageImpression{}, false, nil
	}
	if err != nil {
		return domain.SponsoredMessageImpression{}, false, fmt.Errorf("get sponsored impression: %w", err)
	}
	return impression, true, nil
}

func (s *ModerationReportStore) CreateSponsoredModerationReport(ctx context.Context, impressionID int64, report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if impressionID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("begin sponsored moderation report: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	impression, err := scanSponsoredMessageImpression(tx.QueryRow(ctx, `
SELECT id, user_id, random_id_hash, target_peer_type, target_peer_id,
       author_user_id, evidence_schema_version, evidence, evidence_hash,
       report_id, created_at, expires_at
FROM sponsored_message_impressions
WHERE id = $1
FOR UPDATE`, impressionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
	}
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("lock sponsored impression: %w", err)
	}
	if !report.CreatedAt.Before(impression.ExpiresAt) {
		return domain.ModerationReport{}, false, domain.ErrModerationImpressionExpired
	}
	if err := domain.ValidateSponsoredModerationReport(impression, report); err != nil {
		return domain.ModerationReport{}, false, err
	}
	if impression.ReportID > 0 {
		existing, found, err := getModerationReport(ctx, tx, impression.ReportID)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		if !found {
			return domain.ModerationReport{}, false, domain.ErrModerationReportNotFound
		}
		return existing, false, nil
	}
	stored, created, err := createModerationReportTx(ctx, tx, report)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE sponsored_message_impressions
SET report_id = $2
WHERE id = $1 AND report_id IS NULL`, impressionID, stored.ID)
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("link sponsored report: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ModerationReport{}, false, domain.ErrModerationActionConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("commit sponsored moderation report: %w", err)
	}
	return stored, created, nil
}

func (s *ModerationReportStore) CreateChannelAntiSpamDecision(ctx context.Context, decision domain.ChannelAntiSpamDecision) (domain.ChannelAntiSpamDecision, bool, error) {
	if s == nil || s.db == nil {
		return domain.ChannelAntiSpamDecision{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if err := decision.Validate(); err != nil || decision.ID != 0 ||
		decision.ReportID != 0 {
		return domain.ChannelAntiSpamDecision{}, false, domain.ErrModerationReportInvalid
	}
	err := s.db.QueryRow(ctx, `
INSERT INTO channel_antispam_decisions (
  channel_id, message_id, author_user_id, evidence_schema_version,
  evidence, evidence_hash, created_at
) VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7)
ON CONFLICT (channel_id, message_id) DO NOTHING
RETURNING id`,
		decision.ChannelID, decision.MessageID, decision.AuthorUserID,
		decision.EvidenceSchemaVersion, []byte(decision.Evidence),
		decision.EvidenceHash[:], decision.CreatedAt,
	).Scan(&decision.ID)
	if err == nil {
		return decision, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelAntiSpamDecision{}, false, fmt.Errorf("insert anti-spam decision: %w", err)
	}
	existing, found, err := s.GetChannelAntiSpamDecision(
		ctx, decision.ChannelID, decision.MessageID,
	)
	if err != nil {
		return domain.ChannelAntiSpamDecision{}, false, err
	}
	if !found || existing.AuthorUserID != decision.AuthorUserID ||
		existing.EvidenceHash != decision.EvidenceHash {
		return domain.ChannelAntiSpamDecision{}, false, domain.ErrModerationActionConflict
	}
	return existing, false, nil
}

func (s *ModerationReportStore) GetChannelAntiSpamDecision(ctx context.Context, channelID int64, messageID int) (domain.ChannelAntiSpamDecision, bool, error) {
	if s == nil || s.db == nil {
		return domain.ChannelAntiSpamDecision{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if channelID <= 0 || messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return domain.ChannelAntiSpamDecision{}, false, domain.ErrModerationReportInvalid
	}
	decision, err := scanChannelAntiSpamDecision(s.db.QueryRow(ctx, `
SELECT id, channel_id, message_id, author_user_id,
       evidence_schema_version, evidence, evidence_hash, report_id,
       created_at
FROM channel_antispam_decisions
WHERE channel_id = $1 AND message_id = $2`, channelID, messageID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelAntiSpamDecision{}, false, nil
	}
	if err != nil {
		return domain.ChannelAntiSpamDecision{}, false, fmt.Errorf("get anti-spam decision: %w", err)
	}
	return decision, true, nil
}

func (s *ModerationReportStore) CreateAntiSpamFalsePositiveReport(ctx context.Context, decisionID int64, report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if decisionID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("begin anti-spam false-positive report: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	decision, err := scanChannelAntiSpamDecision(tx.QueryRow(ctx, `
SELECT id, channel_id, message_id, author_user_id,
       evidence_schema_version, evidence, evidence_hash, report_id,
       created_at
FROM channel_antispam_decisions
WHERE id = $1
FOR UPDATE`, decisionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
	}
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("lock anti-spam decision: %w", err)
	}
	if err := domain.ValidateAntiSpamFalsePositiveReport(decision, report); err != nil {
		return domain.ModerationReport{}, false, err
	}
	if decision.ReportID > 0 {
		existing, found, err := getModerationReport(ctx, tx, decision.ReportID)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		if !found {
			return domain.ModerationReport{}, false, domain.ErrModerationReportNotFound
		}
		return existing, false, nil
	}
	stored, created, err := createModerationReportTx(ctx, tx, report)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE channel_antispam_decisions
SET report_id = $2
WHERE id = $1 AND report_id IS NULL`, decisionID, stored.ID)
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("link anti-spam report: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ModerationReport{}, false, domain.ErrModerationActionConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("commit anti-spam false-positive report: %w", err)
	}
	return stored, created, nil
}

func (s *ModerationReportStore) DeleteExpiredSponsoredMessageImpressions(ctx context.Context, olderThan time.Time, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("moderation report store is not configured")
	}
	if olderThan.IsZero() || limit <= 0 || limit > 10000 {
		return 0, domain.ErrModerationReportInvalid
	}
	tag, err := s.db.Exec(ctx, `
WITH doomed AS (
  SELECT id
  FROM sponsored_message_impressions
  WHERE expires_at < $1
  ORDER BY expires_at, id
  LIMIT $2
)
DELETE FROM sponsored_message_impressions i
USING doomed d
WHERE i.id = d.id`, olderThan, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired sponsored impressions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func scanSponsoredMessageImpression(row moderationCaseScanner) (domain.SponsoredMessageImpression, error) {
	var impression domain.SponsoredMessageImpression
	var randomIDHash, evidence, evidenceHash []byte
	var peerType string
	var reportID *int64
	if err := row.Scan(
		&impression.ID, &impression.UserID, &randomIDHash, &peerType,
		&impression.Target.ID, &impression.AuthorUserID,
		&impression.EvidenceSchemaVersion, &evidence, &evidenceHash,
		&reportID, &impression.CreatedAt, &impression.ExpiresAt,
	); err != nil {
		return domain.SponsoredMessageImpression{}, err
	}
	if len(randomIDHash) != len(impression.RandomIDHash) ||
		len(evidenceHash) != len(impression.EvidenceHash) {
		return domain.SponsoredMessageImpression{}, domain.ErrModerationReportInvalid
	}
	copy(impression.RandomIDHash[:], randomIDHash)
	copy(impression.EvidenceHash[:], evidenceHash)
	impression.Target.Type = domain.PeerType(peerType)
	canonical, err := domain.CanonicalModerationEvidence(evidence)
	if err != nil {
		return domain.SponsoredMessageImpression{}, err
	}
	impression.Evidence = canonical
	if reportID != nil {
		impression.ReportID = *reportID
	}
	if err := impression.Validate(); err != nil {
		return domain.SponsoredMessageImpression{}, err
	}
	return impression, nil
}

func scanChannelAntiSpamDecision(row moderationCaseScanner) (domain.ChannelAntiSpamDecision, error) {
	var decision domain.ChannelAntiSpamDecision
	var evidence, evidenceHash []byte
	var reportID *int64
	if err := row.Scan(
		&decision.ID, &decision.ChannelID, &decision.MessageID,
		&decision.AuthorUserID, &decision.EvidenceSchemaVersion,
		&evidence, &evidenceHash, &reportID, &decision.CreatedAt,
	); err != nil {
		return domain.ChannelAntiSpamDecision{}, err
	}
	if len(evidenceHash) != len(decision.EvidenceHash) {
		return domain.ChannelAntiSpamDecision{}, domain.ErrModerationReportInvalid
	}
	copy(decision.EvidenceHash[:], evidenceHash)
	canonical, err := domain.CanonicalModerationEvidence(evidence)
	if err != nil {
		return domain.ChannelAntiSpamDecision{}, err
	}
	decision.Evidence = canonical
	if reportID != nil {
		decision.ReportID = *reportID
	}
	if err := decision.Validate(); err != nil {
		return domain.ChannelAntiSpamDecision{}, err
	}
	return decision, nil
}
