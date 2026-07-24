package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type ModerationReportStore struct {
	db sqlcgen.DBTX
}

func NewModerationReportStore(db sqlcgen.DBTX) *ModerationReportStore {
	return &ModerationReportStore{db: db}
}

func (s *ModerationReportStore) CreateModerationReport(ctx context.Context, report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if err := report.Validate(); err != nil {
		return domain.ModerationReport{}, false, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("begin moderation report: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stored, created, err := createModerationReportTx(ctx, tx, report)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("commit moderation report: %w", err)
	}
	return stored, created, nil
}

func createModerationReportTx(ctx context.Context, tx pgx.Tx, report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	var (
		reportID int64
		err      error
	)
	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(
  hashtextextended('moderation-report:' || $1::bigint::text, 0)
)`, report.ReporterUserID); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("lock moderation reporter: %w", err)
	}
	err = tx.QueryRow(ctx, `
SELECT id
FROM moderation_reports
WHERE reporter_user_id = $1 AND fingerprint = $2`,
		report.ReporterUserID, report.Fingerprint[:]).Scan(&reportID)
	if err == nil {
		existing, found, err := getModerationReport(ctx, tx, reportID)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		if !found {
			return domain.ModerationReport{}, false, fmt.Errorf("duplicate moderation report disappeared")
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationReport{}, false, fmt.Errorf("lookup moderation report fingerprint: %w", err)
	}
	var hourly, daily int
	if err := tx.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE created_at >= $2::timestamptz - interval '1 hour'),
  count(*) FILTER (WHERE created_at >= $2::timestamptz - interval '24 hours')
FROM moderation_reports
WHERE reporter_user_id = $1
  AND created_at <= $2::timestamptz`,
		report.ReporterUserID, report.CreatedAt).Scan(&hourly, &daily); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("count moderation reporter submissions: %w", err)
	}
	if hourly >= domain.MaxModerationReportsPerHour || daily >= domain.MaxModerationReportsPerDay {
		return domain.ModerationReport{}, false, domain.ErrModerationRateLimited
	}
	reportID, created, err := insertModerationReport(ctx, tx, report)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	if !created {
		existing, found, err := getModerationReport(ctx, tx, reportID)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		if !found {
			return domain.ModerationReport{}, false, fmt.Errorf("duplicate moderation report disappeared")
		}
		return existing, false, nil
	}
	report.ID = reportID
	return domain.CloneModerationReport(report), true, nil
}

func (s *ModerationReportStore) ImportLegacyEphemeralReport(ctx context.Context, legacyReportID int64, report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if legacyReportID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	if err := report.Validate(); err != nil {
		return domain.ModerationReport{}, false, err
	}
	if report.Source != domain.ModerationSourceEphemeral {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("begin legacy ephemeral report import: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(
  hashtextextended('moderation-legacy-ephemeral:' || $1::bigint::text, 0)
)`, legacyReportID); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("lock legacy ephemeral report: %w", err)
	}
	var reportID int64
	err = tx.QueryRow(ctx, `
SELECT moderation_report_id
FROM moderation_legacy_ephemeral_migrations
WHERE legacy_report_id = $1`, legacyReportID).Scan(&reportID)
	if err == nil {
		existing, found, err := getModerationReport(ctx, tx, reportID)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		if !found {
			return domain.ModerationReport{}, false, fmt.Errorf("legacy ephemeral report mapping points to missing moderation report")
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationReport{}, false, fmt.Errorf("lookup legacy ephemeral report mapping: %w", err)
	}
	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(
  hashtextextended('moderation-report:' || $1::bigint::text, 0)
)`, report.ReporterUserID); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("lock moderation reporter: %w", err)
	}
	reportID, created, err := insertModerationReport(ctx, tx, report)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO moderation_legacy_ephemeral_migrations (
  legacy_report_id, moderation_report_id, migrated_at
) VALUES ($1,$2,clock_timestamp())`,
		legacyReportID, reportID,
	); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("insert legacy ephemeral report mapping: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("commit legacy ephemeral report import: %w", err)
	}
	if created {
		report.ID = reportID
		return domain.CloneModerationReport(report), true, nil
	}
	existing, found, err := getModerationReport(ctx, s.db, reportID)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	if !found {
		return domain.ModerationReport{}, false, fmt.Errorf("imported duplicate moderation report disappeared")
	}
	return existing, false, nil
}

func insertModerationReport(ctx context.Context, tx pgx.Tx, report domain.ModerationReport) (int64, bool, error) {
	var reportID int64
	err := tx.QueryRow(ctx, `
INSERT INTO moderation_reports (
  reporter_user_id, source, target_peer_type, target_peer_id, reason,
  report_option, report_comment, comment_hash, fingerprint,
  taxonomy_version, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (reporter_user_id, fingerprint) DO NOTHING
RETURNING id`,
		report.ReporterUserID, string(report.Source), string(report.Target.Type),
		report.Target.ID, string(report.Reason), report.Option, report.Comment,
		report.CommentHash[:], report.Fingerprint[:], report.TaxonomyVersion,
		report.CreatedAt,
	).Scan(&reportID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `
SELECT id
FROM moderation_reports
WHERE reporter_user_id = $1 AND fingerprint = $2`,
			report.ReporterUserID, report.Fingerprint[:]).Scan(&reportID); err != nil {
			return 0, false, fmt.Errorf("lookup duplicate moderation report: %w", err)
		}
		return reportID, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("insert moderation report: %w", err)
	}
	for ordinal, item := range report.Items {
		if _, err := tx.Exec(ctx, `
INSERT INTO moderation_report_items (
  report_id, ordinal, item_kind, peer_type, peer_id, item_id,
  secondary_id, author_user_id, evidence_schema_version, evidence,
  evidence_hash
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11)`,
			reportID, ordinal, string(item.Kind), string(item.Peer.Type),
			item.Peer.ID, item.ItemID, item.SecondaryID, item.AuthorUserID,
			item.EvidenceSchemaVersion, []byte(item.Evidence), item.EvidenceHash[:],
		); err != nil {
			return 0, false, fmt.Errorf("insert moderation report item %d: %w", ordinal, err)
		}
	}
	for _, hold := range report.MediaHolds {
		if _, err := tx.Exec(ctx, `
INSERT INTO moderation_media_holds (
  report_id, item_ordinal, media_kind, storage_key, created_at
) VALUES ($1,$2,$3,$4,$5)`,
			reportID, hold.ItemIndex, string(hold.Kind), hold.StorageKey,
			report.CreatedAt,
		); err != nil {
			return 0, false, fmt.Errorf("insert moderation media hold: %w", err)
		}
	}
	if err := attachModerationReportToCase(ctx, tx, reportID, report); err != nil {
		return 0, false, err
	}
	return reportID, true, nil
}

func attachModerationReportToCase(ctx context.Context, tx pgx.Tx, reportID int64, report domain.ModerationReport) error {
	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(
  hashtextextended(
    'moderation-case:' || $1::text || ':' || $2::bigint::text,
    0
  )
)`, string(report.Target.Type), report.Target.ID); err != nil {
		return fmt.Errorf("lock moderation case target: %w", err)
	}
	var caseID int64
	err := tx.QueryRow(ctx, `
SELECT id
FROM moderation_cases
WHERE target_peer_type = $1
  AND target_peer_id = $2
  AND status IN ('open', 'in_review')
FOR UPDATE`,
		string(report.Target.Type), report.Target.ID,
	).Scan(&caseID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
INSERT INTO moderation_cases (
  target_peer_type, target_peer_id, status, severity, assigned_to,
  version, report_count, distinct_reporter_count, first_report_at,
  last_report_at, created_at, updated_at
) VALUES ($1,$2,'open',$3,'',1,1,1,$4,$4,$4,$4)
RETURNING id`,
			string(report.Target.Type), report.Target.ID,
			int16(domain.ModerationSeverityForReason(report.Reason)),
			report.CreatedAt,
		).Scan(&caseID)
		if err != nil {
			return fmt.Errorf("create moderation case: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO moderation_case_reports (case_id, report_id, attached_at)
VALUES ($1,$2,$3)`, caseID, reportID, report.CreatedAt); err != nil {
			return fmt.Errorf("attach report to new moderation case: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("find active moderation case: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO moderation_case_reports (case_id, report_id, attached_at)
VALUES ($1,$2,$3)`, caseID, reportID, report.CreatedAt); err != nil {
		return fmt.Errorf("attach report to moderation case: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE moderation_cases c
SET severity = greatest(c.severity, $2),
    version = c.version + 1,
    report_count = (
      SELECT count(*)::integer
      FROM moderation_case_reports cr
      WHERE cr.case_id = c.id
    ),
    distinct_reporter_count = (
      SELECT count(DISTINCT r.reporter_user_id)::integer
      FROM moderation_case_reports cr
      JOIN moderation_reports r ON r.id = cr.report_id
      WHERE cr.case_id = c.id
    ),
    first_report_at = least(c.first_report_at, $3),
    last_report_at = greatest(c.last_report_at, $3),
    updated_at = greatest(c.updated_at, $3)
WHERE c.id = $1`,
		caseID, int16(domain.ModerationSeverityForReason(report.Reason)),
		report.CreatedAt,
	); err != nil {
		return fmt.Errorf("update moderation case aggregates: %w", err)
	}
	return nil
}

func (s *ModerationReportStore) GetModerationReport(ctx context.Context, reportID int64) (domain.ModerationReport, bool, error) {
	if s == nil || s.db == nil {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if reportID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	return getModerationReport(ctx, s.db, reportID)
}

func getModerationReport(ctx context.Context, db sqlcgen.DBTX, reportID int64) (domain.ModerationReport, bool, error) {
	var (
		report                 domain.ModerationReport
		source, target, reason string
		commentHash            []byte
		fingerprint            []byte
	)
	err := db.QueryRow(ctx, `
SELECT id, reporter_user_id, source, target_peer_type, target_peer_id,
       reason, report_option, report_comment, comment_hash, fingerprint,
       taxonomy_version, created_at
FROM moderation_reports
WHERE id = $1`, reportID).Scan(
		&report.ID, &report.ReporterUserID, &source, &target,
		&report.Target.ID, &reason, &report.Option, &report.Comment,
		&commentHash, &fingerprint, &report.TaxonomyVersion,
		&report.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ModerationReport{}, false, nil
	}
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("get moderation report: %w", err)
	}
	if len(commentHash) != len(report.CommentHash) || len(fingerprint) != len(report.Fingerprint) {
		return domain.ModerationReport{}, false, fmt.Errorf("get moderation report: invalid persisted hash length")
	}
	copy(report.CommentHash[:], commentHash)
	copy(report.Fingerprint[:], fingerprint)
	report.Source = domain.ModerationReportSource(source)
	report.Target.Type = domain.PeerType(target)
	report.Reason = domain.ModerationReason(reason)

	rows, err := db.Query(ctx, `
SELECT ordinal, item_kind, peer_type, peer_id, item_id, secondary_id,
       author_user_id, evidence_schema_version, evidence, evidence_hash
FROM moderation_report_items
WHERE report_id = $1
ORDER BY ordinal`, reportID)
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("list moderation report items: %w", err)
	}
	for rows.Next() {
		var (
			ordinal                int
			item                   domain.ModerationReportItem
			kind, peerType         string
			evidence, evidenceHash []byte
		)
		if err := rows.Scan(
			&ordinal, &kind, &peerType, &item.Peer.ID, &item.ItemID,
			&item.SecondaryID, &item.AuthorUserID,
			&item.EvidenceSchemaVersion, &evidence, &evidenceHash,
		); err != nil {
			rows.Close()
			return domain.ModerationReport{}, false, fmt.Errorf("scan moderation report item: %w", err)
		}
		if ordinal != len(report.Items) || len(evidenceHash) != len(item.EvidenceHash) {
			rows.Close()
			return domain.ModerationReport{}, false, fmt.Errorf("scan moderation report item: invalid persisted ordering or hash")
		}
		canonical, err := domain.CanonicalModerationEvidence(evidence)
		if err != nil {
			rows.Close()
			return domain.ModerationReport{}, false, fmt.Errorf("scan moderation report item evidence: %w", err)
		}
		item.Kind = domain.ModerationReportItemKind(kind)
		item.Peer.Type = domain.PeerType(peerType)
		item.Evidence = canonical
		copy(item.EvidenceHash[:], evidenceHash)
		report.Items = append(report.Items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ModerationReport{}, false, fmt.Errorf("iterate moderation report items: %w", err)
	}
	rows.Close()

	holdRows, err := db.Query(ctx, `
SELECT item_ordinal, media_kind, storage_key
FROM moderation_media_holds
WHERE report_id = $1
ORDER BY item_ordinal, media_kind, storage_key`, reportID)
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("list moderation media holds: %w", err)
	}
	defer holdRows.Close()
	for holdRows.Next() {
		var hold domain.ModerationMediaHold
		var kind string
		if err := holdRows.Scan(&hold.ItemIndex, &kind, &hold.StorageKey); err != nil {
			return domain.ModerationReport{}, false, fmt.Errorf("scan moderation media hold: %w", err)
		}
		hold.Kind = domain.ModerationMediaKind(kind)
		report.MediaHolds = append(report.MediaHolds, hold)
	}
	if err := holdRows.Err(); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("iterate moderation media holds: %w", err)
	}
	if err := report.Validate(); err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("validate persisted moderation report: %w", err)
	}
	return report, true, nil
}
