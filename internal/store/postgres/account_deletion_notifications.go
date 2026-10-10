package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// enqueueAccountDeletionNotifications fills the durable tombstone queue inside
// the deletion transaction, so the audience is committed together with the
// tombstone itself. A crash after the commit can therefore never leave a deleted
// account without anyone to tell.
func enqueueAccountDeletionNotifications(ctx context.Context, tx pgx.Tx, userID int64, now time.Time) error {
	_, err := tx.Exec(ctx, `
INSERT INTO account_deletion_notifications (target_user_id, deleted_user_id, created_at, updated_at)
SELECT audience.user_id, $1, $2, $2
FROM (
  SELECT user_id
  FROM (
    SELECT contact_user_id AS user_id, 0 AS priority, 0 AS activity
      FROM contacts WHERE user_id = $1
    UNION ALL
    SELECT user_id, 0, 0 FROM contacts WHERE contact_user_id = $1
    UNION ALL
    SELECT peer_id, 1, top_message_date
      FROM dialogs WHERE user_id = $1 AND peer_type = 'user'
    UNION ALL
    SELECT user_id, 1, top_message_date
      FROM dialogs WHERE peer_type = 'user' AND peer_id = $1
  ) candidates
  GROUP BY user_id
  ORDER BY min(priority), max(activity) DESC, user_id
) audience
JOIN users u ON u.id = audience.user_id
WHERE audience.user_id <> $1 AND u.deleted_at IS NULL
ON CONFLICT (target_user_id, deleted_user_id) DO UPDATE SET
  status = 'pending',
  attempts = 0,
  next_attempt_at = EXCLUDED.next_attempt_at,
  lease_until = NULL,
  last_error = '',
  updated_at = EXCLUDED.updated_at`, userID, now)
	if err != nil {
		return fmt.Errorf("enqueue account deletion notifications: %w", err)
	}
	return nil
}

// ClaimAccountDeletionNotifications leases a batch of pending tombstones. An
// expired lease is reclaimable, so a worker crash mid-dispatch retries instead
// of stranding the viewer on a stale peer.
func (s *AccountLifecycleStore) ClaimAccountDeletionNotifications(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.AccountDeletionNotification, error) {
	if s == nil || s.pool == nil || limit <= 0 || lease <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
WITH claim AS (
  SELECT id FROM account_deletion_notifications
  WHERE (status = 'pending' AND next_attempt_at <= $1)
     OR (status = 'dispatching' AND lease_until <= $1)
  ORDER BY next_attempt_at, id FOR UPDATE SKIP LOCKED LIMIT $2
)
UPDATE account_deletion_notifications n
SET status = 'dispatching', attempts = attempts + 1, lease_until = $3, updated_at = $1
FROM claim WHERE n.id = claim.id
RETURNING n.id, n.target_user_id, n.deleted_user_id, n.attempts`, now, limit, now.Add(lease))
	if err != nil {
		return nil, fmt.Errorf("claim account deletion notifications: %w", err)
	}
	defer rows.Close()
	out := make([]domain.AccountDeletionNotification, 0, limit)
	for rows.Next() {
		var n domain.AccountDeletionNotification
		if err := rows.Scan(&n.ID, &n.TargetUserID, &n.DeletedUserID, &n.Attempts); err != nil {
			return nil, fmt.Errorf("scan account deletion notification: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CompleteAccountDeletionNotification marks a tombstone delivered. A repeated
// call is a no-op, which keeps a duplicate dispatch harmless.
func (s *AccountLifecycleStore) CompleteAccountDeletionNotification(ctx context.Context, id int64, now time.Time) error {
	if s == nil || s.pool == nil || id == 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `
UPDATE account_deletion_notifications
SET status = 'delivered', lease_until = NULL, last_error = '', updated_at = $2
WHERE id = $1`, id, now); err != nil {
		return fmt.Errorf("complete account deletion notification: %w", err)
	}
	return nil
}
