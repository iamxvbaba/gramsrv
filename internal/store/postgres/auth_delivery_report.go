package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type AuthDeliveryReportStore struct {
	db sqlcgen.DBTX
}

func NewAuthDeliveryReportStore(db sqlcgen.DBTX) *AuthDeliveryReportStore {
	return &AuthDeliveryReportStore{db: db}
}

func (s *AuthDeliveryReportStore) CreateAuthDeliveryReport(ctx context.Context, report domain.AuthDeliveryReport) (domain.AuthDeliveryReport, bool, error) {
	if s == nil || s.db == nil {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("auth delivery report store is not configured")
	}
	if err := report.Validate(); err != nil {
		return domain.AuthDeliveryReport{}, false, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("auth delivery report store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("begin auth delivery report: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(
  hashtextextended('auth-delivery:' || encode($1::bytea, 'hex'), 0)
)`, report.AuthKeyID[:]); err != nil {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("lock auth delivery reporter: %w", err)
	}
	existing, found, err := getAuthDeliveryReportByFingerprint(ctx, tx, report.AuthKeyID, report.Fingerprint)
	if err != nil {
		return domain.AuthDeliveryReport{}, false, err
	}
	if found {
		return existing, false, nil
	}
	var hourly, phoneDaily int
	if err := tx.QueryRow(ctx, `
SELECT
  count(*) FILTER (
    WHERE auth_key_id = $1 AND created_at >= $3::timestamptz - interval '1 hour'
  ),
  count(*) FILTER (
    WHERE phone_hash = $2 AND created_at >= $3::timestamptz - interval '24 hours'
  )
FROM auth_delivery_reports
WHERE created_at <= $3::timestamptz
  AND (auth_key_id = $1 OR phone_hash = $2)`,
		report.AuthKeyID[:], report.PhoneHash[:], report.CreatedAt,
	).Scan(&hourly, &phoneDaily); err != nil {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("count auth delivery reports: %w", err)
	}
	if hourly >= domain.MaxAuthDeliveryReportsPerHour ||
		phoneDaily >= domain.MaxAuthDeliveryReportsPerPhoneDay {
		return domain.AuthDeliveryReport{}, false, domain.ErrAuthDeliveryRateLimited
	}
	err = tx.QueryRow(ctx, `
INSERT INTO auth_delivery_reports (
  auth_key_id, session_id, client_type, phone_hash, code_hash,
  issued_user_id, delivery_id, channel, mnc, fingerprint, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
RETURNING id`,
		report.AuthKeyID[:], report.SessionID, report.ClientType,
		report.PhoneHash[:], report.CodeHash[:], report.IssuedUserID,
		report.DeliveryID, string(report.Channel), report.MNC,
		report.Fingerprint[:], report.CreatedAt,
	).Scan(&report.ID)
	if err != nil {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("insert auth delivery report: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("commit auth delivery report: %w", err)
	}
	return report, true, nil
}

func getAuthDeliveryReportByFingerprint(ctx context.Context, db sqlcgen.DBTX, authKeyID [8]byte, fingerprint [32]byte) (domain.AuthDeliveryReport, bool, error) {
	var report domain.AuthDeliveryReport
	var storedAuthKey, phoneHash, codeHash, storedFingerprint []byte
	var channel string
	err := db.QueryRow(ctx, `
SELECT id, auth_key_id, session_id, client_type, phone_hash, code_hash,
       issued_user_id, delivery_id, channel, mnc, fingerprint, created_at
FROM auth_delivery_reports
WHERE auth_key_id = $1 AND fingerprint = $2`,
		authKeyID[:], fingerprint[:],
	).Scan(
		&report.ID, &storedAuthKey, &report.SessionID, &report.ClientType,
		&phoneHash, &codeHash, &report.IssuedUserID, &report.DeliveryID,
		&channel, &report.MNC, &storedFingerprint, &report.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AuthDeliveryReport{}, false, nil
	}
	if err != nil {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("get auth delivery report: %w", err)
	}
	if len(storedAuthKey) != len(report.AuthKeyID) ||
		len(phoneHash) != len(report.PhoneHash) ||
		len(codeHash) != len(report.CodeHash) ||
		len(storedFingerprint) != len(report.Fingerprint) {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("get auth delivery report: invalid hash shape")
	}
	copy(report.AuthKeyID[:], storedAuthKey)
	copy(report.PhoneHash[:], phoneHash)
	copy(report.CodeHash[:], codeHash)
	copy(report.Fingerprint[:], storedFingerprint)
	report.Channel = domain.AuthCodeDeliveryKind(channel)
	if err := report.Validate(); err != nil {
		return domain.AuthDeliveryReport{}, false, fmt.Errorf("validate auth delivery report: %w", err)
	}
	return report, true, nil
}

func (s *AuthDeliveryReportStore) DeleteExpiredAuthDeliveryReports(ctx context.Context, olderThan time.Time, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("auth delivery report store is not configured")
	}
	if olderThan.IsZero() || limit <= 0 || limit > 10000 {
		return 0, domain.ErrAuthDeliveryReportInvalid
	}
	tag, err := s.db.Exec(ctx, `
WITH doomed AS (
  SELECT id
  FROM auth_delivery_reports
  WHERE created_at < $1
  ORDER BY created_at, id
  LIMIT $2
)
DELETE FROM auth_delivery_reports r
USING doomed d
WHERE r.id = d.id`, olderThan, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired auth delivery reports: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
