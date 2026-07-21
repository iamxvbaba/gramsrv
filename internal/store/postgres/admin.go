package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type AdminStore struct {
	db sqlcgen.DBTX
}

func NewAdminStore(db sqlcgen.DBTX) *AdminStore {
	return &AdminStore{db: db}
}

func (s *AdminStore) BeginCommand(ctx context.Context, cmd domain.AdminCommand) (domain.AdminCommand, bool, error) {
	inserted, err := scanAdminCommand(s.db.QueryRow(ctx, `
INSERT INTO admin_commands (
	command_id, actor, action, target_user_id, target_peer_type, target_peer_id,
	dry_run, reason, request, result, status, error, created_at
) VALUES (
	$1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,'{}'::jsonb,$10,'',$11
)
ON CONFLICT (command_id) DO NOTHING
RETURNING command_id, actor, action, target_user_id, target_peer_type, target_peer_id,
	dry_run, reason, request, result, status, error, created_at, completed_at`,
		cmd.CommandID, cmd.Actor, cmd.Action, cmd.TargetUserID, string(cmd.TargetPeer.Type), cmd.TargetPeer.ID,
		cmd.DryRun, cmd.Reason, string(cmd.RequestJSON), string(cmd.Status), cmd.CreatedAt,
	))
	if err == nil {
		return inserted, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.AdminCommand{}, false, fmt.Errorf("insert admin command: %w", err)
	}
	existing, err := s.commandByID(ctx, cmd.CommandID)
	if err != nil {
		return domain.AdminCommand{}, false, err
	}
	return existing, false, nil
}

func (s *AdminStore) FinishCommand(ctx context.Context, commandID string, status domain.AdminCommandStatus, resultJSON []byte, errorText string) (domain.AdminCommand, error) {
	if len(resultJSON) == 0 {
		resultJSON = []byte("{}")
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return s.finishCommandNoTx(ctx, commandID, status, resultJSON, errorText)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.AdminCommand{}, fmt.Errorf("begin finish admin command tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	cmd, err := finishAdminCommand(ctx, tx, commandID, status, resultJSON, errorText)
	if err != nil {
		return domain.AdminCommand{}, err
	}
	if err := appendAdminAuditLog(ctx, tx, commandID); err != nil {
		return domain.AdminCommand{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AdminCommand{}, fmt.Errorf("commit finish admin command tx: %w", err)
	}
	committed = true
	return cmd, nil
}

func (s *AdminStore) finishCommandNoTx(ctx context.Context, commandID string, status domain.AdminCommandStatus, resultJSON []byte, errorText string) (domain.AdminCommand, error) {
	cmd, err := finishAdminCommand(ctx, s.db, commandID, status, resultJSON, errorText)
	if err != nil {
		return domain.AdminCommand{}, err
	}
	if err := appendAdminAuditLog(ctx, s.db, commandID); err != nil {
		return domain.AdminCommand{}, err
	}
	return cmd, nil
}

func finishAdminCommand(ctx context.Context, db sqlcgen.DBTX, commandID string, status domain.AdminCommandStatus, resultJSON []byte, errorText string) (domain.AdminCommand, error) {
	cmd, err := scanAdminCommand(db.QueryRow(ctx, `
UPDATE admin_commands
SET status = $2, result = $3::jsonb, error = $4, completed_at = now()
WHERE command_id = $1
RETURNING command_id, actor, action, target_user_id, target_peer_type, target_peer_id,
	dry_run, reason, request, result, status, error, created_at, completed_at`,
		commandID, string(status), string(resultJSON), errorText,
	))
	if err != nil {
		return domain.AdminCommand{}, fmt.Errorf("finish admin command: %w", err)
	}
	return cmd, nil
}

func appendAdminAuditLog(ctx context.Context, db sqlcgen.DBTX, commandID string) error {
	if _, err := db.Exec(ctx, `
INSERT INTO admin_audit_logs (
	command_id, actor, action, target_user_id, target_peer_type, target_peer_id,
	dry_run, reason, request, result, status, error, created_at
)
SELECT command_id, actor, action, target_user_id, target_peer_type, target_peer_id,
	dry_run, reason, request, result, status, error, now()
FROM admin_commands
WHERE command_id = $1
ON CONFLICT (command_id) DO NOTHING`, commandID); err != nil {
		return fmt.Errorf("append admin audit log: %w", err)
	}
	return nil
}

func (s *AdminStore) commandByID(ctx context.Context, commandID string) (domain.AdminCommand, error) {
	cmd, err := scanAdminCommand(s.db.QueryRow(ctx, `
SELECT command_id, actor, action, target_user_id, target_peer_type, target_peer_id,
	dry_run, reason, request, result, status, error, created_at, completed_at
FROM admin_commands
WHERE command_id = $1`, commandID))
	if err != nil {
		return domain.AdminCommand{}, fmt.Errorf("get admin command: %w", err)
	}
	return cmd, nil
}

func scanAdminCommand(row pgx.Row) (domain.AdminCommand, error) {
	var cmd domain.AdminCommand
	var peerType string
	var status string
	var completed pgtype.Timestamptz
	if err := row.Scan(
		&cmd.CommandID,
		&cmd.Actor,
		&cmd.Action,
		&cmd.TargetUserID,
		&peerType,
		&cmd.TargetPeer.ID,
		&cmd.DryRun,
		&cmd.Reason,
		&cmd.RequestJSON,
		&cmd.ResultJSON,
		&status,
		&cmd.Error,
		&cmd.CreatedAt,
		&completed,
	); err != nil {
		return domain.AdminCommand{}, err
	}
	cmd.TargetPeer.Type = domain.PeerType(peerType)
	cmd.Status = domain.AdminCommandStatus(status)
	if completed.Valid {
		t := completed.Time
		cmd.CompletedAt = &t
	}
	return cmd, nil
}

func (s *AdminStore) GetAccountFreeze(ctx context.Context, userID int64) (domain.AccountFreeze, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT user_id, frozen, version, frozen_since, frozen_until, appeal_url, reason, actor, command_id, updated_at
FROM account_restrictions
WHERE user_id = $1`, userID)
	r, err := scanAccountFreeze(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AccountFreeze{}, false, nil
		}
		return domain.AccountFreeze{}, false, fmt.Errorf("get account freeze: %w", err)
	}
	return r, true, nil
}

func (s *AdminStore) GetAccountFreezes(ctx context.Context, userIDs []int64) (map[int64]domain.AccountFreeze, error) {
	out := make(map[int64]domain.AccountFreeze)
	if s == nil || s.db == nil || len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT user_id, frozen, version, frozen_since, frozen_until, appeal_url, reason, actor, command_id, updated_at
FROM account_restrictions
WHERE user_id = ANY($1::bigint[]) AND frozen = true`, userIDs)
	if err != nil {
		return nil, fmt.Errorf("get account freezes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		freeze, err := scanAccountFreeze(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account freeze: %w", err)
		}
		out[freeze.UserID] = freeze
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account freezes: %w", err)
	}
	return out, nil
}

func (s *AdminStore) SetAccountFreeze(ctx context.Context, freeze domain.AccountFreeze) (domain.AccountFreeze, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return setAccountFreezeRow(ctx, s.db, freeze)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.AccountFreeze{}, fmt.Errorf("begin set account freeze: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	out, err := setAccountFreezeRow(ctx, tx, freeze)
	if err != nil {
		return domain.AccountFreeze{}, err
	}
	if err := enqueueAccountFreezeNotifications(ctx, tx, out); err != nil {
		return domain.AccountFreeze{}, err
	}
	// User visibility participates in the same cache/version invalidation spine
	// as profile and dialog changes. These functions emit cross-instance NOTIFY
	// events only after the surrounding transaction commits.
	if _, err := tx.Exec(ctx, `SELECT telesrv_bump_contact_accounts_for_user($1)`, out.UserID); err != nil {
		return domain.AccountFreeze{}, fmt.Errorf("bump frozen user contact projections: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT telesrv_bump_private_dialog_light_for_user($1)`, out.UserID); err != nil {
		return domain.AccountFreeze{}, fmt.Errorf("bump frozen user dialog projections: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT telesrv_bump_read_model_version('user_visibility', 0, 'user', $1)`, out.UserID); err != nil {
		return domain.AccountFreeze{}, fmt.Errorf("bump frozen user visibility: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AccountFreeze{}, fmt.Errorf("commit set account freeze: %w", err)
	}
	committed = true
	return out, nil
}

func setAccountFreezeRow(ctx context.Context, db sqlcgen.DBTX, freeze domain.AccountFreeze) (domain.AccountFreeze, error) {
	var since, until any
	if freeze.Frozen {
		since = freeze.Since
		until = freeze.Until
	}
	row := db.QueryRow(ctx, `
INSERT INTO account_restrictions (
	user_id, frozen, frozen_since, frozen_until, appeal_url, reason, actor, command_id, updated_at
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
ON CONFLICT (user_id) DO UPDATE SET
	frozen = EXCLUDED.frozen,
	frozen_since = EXCLUDED.frozen_since,
	frozen_until = EXCLUDED.frozen_until,
	appeal_url = EXCLUDED.appeal_url,
	reason = EXCLUDED.reason,
	actor = EXCLUDED.actor,
	command_id = EXCLUDED.command_id,
	version = account_restrictions.version + 1,
	updated_at = now()
RETURNING user_id, frozen, version, frozen_since, frozen_until, appeal_url, reason, actor, command_id, updated_at`,
		freeze.UserID, freeze.Frozen, since, until, freeze.AppealURL, freeze.Reason, freeze.Actor, freeze.CommandID,
	)
	out, err := scanAccountFreeze(row)
	if err != nil {
		return domain.AccountFreeze{}, fmt.Errorf("set account freeze: %w", err)
	}
	return out, nil
}

type accountFreezeScanner interface {
	Scan(dest ...any) error
}

func scanAccountFreeze(row accountFreezeScanner) (domain.AccountFreeze, error) {
	var r domain.AccountFreeze
	var since, until pgtype.Timestamptz
	var updated time.Time
	if err := row.Scan(
		&r.UserID, &r.Frozen, &r.Version, &since, &until, &r.AppealURL,
		&r.Reason, &r.Actor, &r.CommandID, &updated,
	); err != nil {
		return domain.AccountFreeze{}, err
	}
	if since.Valid {
		r.Since = since.Time
	}
	if until.Valid {
		r.Until = until.Time
	}
	r.UpdatedAt = updated
	return r, nil
}

func enqueueAccountFreezeNotifications(ctx context.Context, tx pgx.Tx, freeze domain.AccountFreeze) error {
	const maxAccountFreezeNotificationAudience = 4096
	_, err := tx.Exec(ctx, `
INSERT INTO account_freeze_notifications (target_user_id, frozen_user_id, version, frozen)
SELECT audience.user_id, $1, $2, $3
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
  LIMIT $4
) audience
JOIN users u ON u.id = audience.user_id
WHERE audience.user_id <> $1 AND u.deleted_at IS NULL
ON CONFLICT (target_user_id, frozen_user_id) DO UPDATE SET
  version = EXCLUDED.version,
  frozen = EXCLUDED.frozen,
  status = 'pending',
  attempts = 0,
  next_attempt_at = now(),
  lease_until = NULL,
  last_error = '',
  updated_at = now()`, freeze.UserID, freeze.Version, freeze.Frozen, maxAccountFreezeNotificationAudience)
	if err != nil {
		return fmt.Errorf("enqueue account freeze notifications: %w", err)
	}
	return nil
}

func (s *AdminStore) ClaimAccountFreezeNotifications(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.AccountFreezeNotification, error) {
	if s == nil || s.db == nil || limit <= 0 || lease <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
WITH claim AS (
  SELECT id FROM account_freeze_notifications
  WHERE (status = 'pending' AND next_attempt_at <= $1)
     OR (status = 'dispatching' AND lease_until <= $1)
  ORDER BY next_attempt_at, id FOR UPDATE SKIP LOCKED LIMIT $2
)
UPDATE account_freeze_notifications n
SET status = 'dispatching', attempts = attempts + 1, lease_until = $3, updated_at = $1
FROM claim WHERE n.id = claim.id
RETURNING n.id, n.target_user_id, n.frozen_user_id, n.version, n.frozen, n.attempts`, now, limit, now.Add(lease))
	if err != nil {
		return nil, fmt.Errorf("claim account freeze notifications: %w", err)
	}
	defer rows.Close()
	out := make([]domain.AccountFreezeNotification, 0)
	for rows.Next() {
		var n domain.AccountFreezeNotification
		if err := rows.Scan(&n.ID, &n.TargetUserID, &n.FrozenUserID, &n.Version, &n.Frozen, &n.Attempts); err != nil {
			return nil, fmt.Errorf("scan account freeze notification: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *AdminStore) CompleteAccountFreezeNotification(ctx context.Context, id, version int64, now time.Time) error {
	_, err := s.db.Exec(ctx, `
UPDATE account_freeze_notifications
SET status = 'delivered', lease_until = NULL, last_error = '', updated_at = $3
WHERE id = $1 AND version = $2`, id, version, now)
	if err != nil {
		return fmt.Errorf("complete account freeze notification: %w", err)
	}
	return nil
}
