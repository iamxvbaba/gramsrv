package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type BootstrapUpdateJobStore struct {
	db sqlcgen.DBTX
}

func NewBootstrapUpdateJobStore(db sqlcgen.DBTX) *BootstrapUpdateJobStore {
	return &BootstrapUpdateJobStore{db: db}
}

func (s *BootstrapUpdateJobStore) EnqueueLoginMessage(ctx context.Context, job domain.BootstrapUpdateJob) (domain.BootstrapUpdateJob, error) {
	if job.Kind == "" {
		job.Kind = domain.BootstrapUpdateJobLoginMessage
	}
	if job.Status == "" {
		job.Status = domain.BootstrapUpdateJobPending
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO bootstrap_update_jobs (
  kind, user_id, auth_key_id, session_id, message_box_id, status
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (kind, user_id, message_box_id) DO UPDATE SET
  auth_key_id = EXCLUDED.auth_key_id,
  session_id = EXCLUDED.session_id,
  status = CASE
    WHEN bootstrap_update_jobs.status = 'failed' THEN 'pending'
    ELSE bootstrap_update_jobs.status
  END,
  last_error = CASE
    WHEN bootstrap_update_jobs.status = 'failed' THEN ''
    ELSE bootstrap_update_jobs.last_error
  END,
  updated_at = now()
RETURNING id, kind, user_id, auth_key_id, session_id, message_box_id, status, attempts, last_error, created_at, updated_at, ready_at, published_at`,
		string(job.Kind),
		job.UserID,
		authKeyIDToInt64(job.AuthKeyID),
		job.SessionID,
		job.MessageBoxID,
		string(job.Status),
	)
	out, err := scanBootstrapUpdateJob(row)
	if err != nil {
		return domain.BootstrapUpdateJob{}, fmt.Errorf("enqueue bootstrap login message: %w", err)
	}
	return out, nil
}

func (s *BootstrapUpdateJobStore) MarkReadyForSession(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64) (int, error) {
	tag, err := s.db.Exec(ctx, `
UPDATE bootstrap_update_jobs
SET status = 'ready',
    ready_at = now(),
    updated_at = now()
WHERE user_id = $1
  AND auth_key_id = $2
  AND session_id = $3
  AND status = 'pending'`,
		userID, authKeyIDToInt64(authKeyID), sessionID)
	if err != nil {
		return 0, fmt.Errorf("mark bootstrap jobs ready: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *BootstrapUpdateJobStore) ClaimReady(ctx context.Context, limit int, leaseTimeout time.Duration) ([]domain.BootstrapUpdateJob, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	leaseSeconds := int(leaseTimeout / time.Second)
	if leaseSeconds <= 0 {
		leaseSeconds = int(defaultDispatchLease / time.Second)
	}
	rows, err := s.db.Query(ctx, `
WITH picked AS (
  SELECT id
  FROM bootstrap_update_jobs
  WHERE (status = 'ready' AND COALESCE(ready_at, created_at) <= now())
     OR (status = 'publishing' AND updated_at < now() - make_interval(secs => $1::int))
  ORDER BY COALESCE(ready_at, created_at) ASC, user_id ASC, id ASC
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
UPDATE bootstrap_update_jobs j
SET status = 'publishing',
    attempts = j.attempts + 1,
    updated_at = now()
FROM picked p
WHERE j.id = p.id
RETURNING j.id, j.kind, j.user_id, j.auth_key_id, j.session_id, j.message_box_id, j.status, j.attempts, j.last_error, j.created_at, j.updated_at, j.ready_at, j.published_at`,
		leaseSeconds, limit)
	if err != nil {
		return nil, fmt.Errorf("claim bootstrap jobs: %w", err)
	}
	defer rows.Close()
	out := make([]domain.BootstrapUpdateJob, 0)
	for rows.Next() {
		job, err := scanBootstrapUpdateJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim bootstrap jobs rows: %w", err)
	}
	return out, nil
}

func (s *BootstrapUpdateJobStore) MarkPublished(ctx context.Context, id int64) error {
	if _, err := s.db.Exec(ctx, `
UPDATE bootstrap_update_jobs
SET status = 'published',
    published_at = now(),
    updated_at = now()
WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark bootstrap job published: %w", err)
	}
	return nil
}

func (s *BootstrapUpdateJobStore) MarkFailed(ctx context.Context, id int64, lastError string) error {
	if _, err := s.db.Exec(ctx, `
UPDATE bootstrap_update_jobs
SET status = CASE WHEN attempts >= $3 THEN 'failed' ELSE 'ready' END,
    ready_at = CASE
      WHEN attempts >= $3 THEN ready_at
      ELSE now() + make_interval(secs => LEAST(60, attempts * attempts))
    END,
    last_error = $2,
    updated_at = now()
WHERE id = $1`, id, lastError, domain.BootstrapUpdateMaxAttempts); err != nil {
		return fmt.Errorf("mark bootstrap job failed: %w", err)
	}
	return nil
}

type bootstrapJobScanner interface {
	Scan(dest ...any) error
}

func scanBootstrapUpdateJob(row bootstrapJobScanner) (domain.BootstrapUpdateJob, error) {
	var (
		job                domain.BootstrapUpdateJob
		kind, status       string
		authKeyID          int64
		readyAt, published sql.NullTime
	)
	if err := row.Scan(
		&job.ID,
		&kind,
		&job.UserID,
		&authKeyID,
		&job.SessionID,
		&job.MessageBoxID,
		&status,
		&job.Attempts,
		&job.LastError,
		&job.CreatedAt,
		&job.UpdatedAt,
		&readyAt,
		&published,
	); err != nil {
		return domain.BootstrapUpdateJob{}, fmt.Errorf("scan bootstrap job: %w", err)
	}
	job.Kind = domain.BootstrapUpdateJobKind(kind)
	job.Status = domain.BootstrapUpdateJobStatus(status)
	job.AuthKeyID = authKeyIDFromInt64(authKeyID)
	if readyAt.Valid {
		job.ReadyAt = readyAt.Time
	}
	if published.Valid {
		job.PublishedAt = published.Time
	}
	return job, nil
}
