package postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

// AccountLifecycleStore is the PostgreSQL implementation of the unified
// account tombstone, delayed deletion and deletion notification boundary.
type AccountLifecycleStore struct {
	pool *pgxpool.Pool
}

func NewAccountLifecycleStore(pool *pgxpool.Pool) *AccountLifecycleStore {
	return &AccountLifecycleStore{pool: pool}
}

func (s *AccountLifecycleStore) AccountDeletionSnapshot(ctx context.Context, userID int64) (domain.AccountDeletionSnapshot, bool, error) {
	if s == nil || s.pool == nil || userID == 0 {
		return domain.AccountDeletionSnapshot{}, false, nil
	}
	u, found, err := NewUserStore(s.pool).ByID(ctx, userID)
	if err != nil || !found {
		return domain.AccountDeletionSnapshot{}, found, err
	}
	var snapshot = domain.AccountDeletionSnapshot{User: u}
	if err := s.pool.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM account_passwords WHERE user_id = $1 AND has_password),
       COALESCE((SELECT password_changed_at FROM account_passwords WHERE user_id = $1), u.created_at)
FROM users u WHERE u.id = $1`, userID).Scan(&snapshot.HasPassword, &snapshot.PasswordUpdatedAt); err != nil {
		return domain.AccountDeletionSnapshot{}, false, fmt.Errorf("load account deletion password facts: %w", err)
	}
	pending, ok, err := pendingAccountDeletion(ctx, s.pool, userID, nil)
	if err != nil {
		return domain.AccountDeletionSnapshot{}, false, err
	}
	if ok {
		snapshot.Pending = &pending
	}
	return snapshot, true, nil
}

func (s *AccountLifecycleStore) ScheduleAccountDeletion(ctx context.Context, req domain.ScheduleAccountDeletion) (domain.AccountDeletionRequest, bool, error) {
	if s == nil || s.pool == nil || req.UserID == 0 || req.RequestedAt.IsZero() || !req.ExecuteAt.After(req.RequestedAt) {
		return domain.AccountDeletionRequest{}, false, domain.ErrAccountDeletionForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AccountDeletionRequest{}, false, fmt.Errorf("begin schedule account deletion: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockUsersForUpdate(ctx, tx, req.UserID, domain.OfficialSystemUserID); err != nil {
		return domain.AccountDeletionRequest{}, false, fmt.Errorf("lock schedule account deletion users: %w", err)
	}
	var deletedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT deleted_at FROM users WHERE id = $1 FOR UPDATE`, req.UserID).Scan(&deletedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AccountDeletionRequest{}, false, domain.ErrUserNotFound
		}
		return domain.AccountDeletionRequest{}, false, fmt.Errorf("lock account deletion user: %w", err)
	}
	if deletedAt != nil {
		return domain.AccountDeletionRequest{}, false, domain.ErrAccountDeleted
	}
	if existing, ok, err := pendingAccountDeletion(ctx, tx, req.UserID, nil); err != nil {
		return domain.AccountDeletionRequest{}, false, err
	} else if ok {
		if err := tx.Commit(ctx); err != nil {
			return domain.AccountDeletionRequest{}, false, fmt.Errorf("commit existing account deletion: %w", err)
		}
		return existing, false, nil
	}
	row := tx.QueryRow(ctx, `
INSERT INTO account_deletion_requests (
  user_id, requester_auth_key_id, reason, confirm_hash_digest, requested_at, execute_at
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, user_id, requester_auth_key_id, state, reason, confirm_hash_digest,
          requested_at, execute_at, completed_at`,
		req.UserID, authKeyIDToInt64(req.RequesterAuthKeyID), req.Reason,
		req.ConfirmHashDigest[:], req.RequestedAt, req.ExecuteAt)
	pending, err := scanAccountDeletionRequest(row)
	if err != nil {
		return domain.AccountDeletionRequest{}, false, fmt.Errorf("insert account deletion request: %w", err)
	}
	randomID := int64(binary.LittleEndian.Uint64(req.ConfirmHashDigest[:8]))
	if randomID == 0 {
		randomID = pending.ID
	}
	if _, err := NewMessageStore(tx).SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    domain.OfficialSystemUserID,
		RecipientUserID: req.UserID,
		RandomID:        randomID,
		Message:         req.ServiceMessage,
		Date:            int(req.RequestedAt.Unix()),
	}); err != nil {
		return domain.AccountDeletionRequest{}, false, fmt.Errorf("send account deletion confirmation message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AccountDeletionRequest{}, false, fmt.Errorf("commit schedule account deletion: %w", err)
	}
	return pending, true, nil
}

func (s *AccountLifecycleStore) PendingAccountDeletionByHash(ctx context.Context, userID int64, digest [32]byte) (domain.AccountDeletionRequest, bool, error) {
	if s == nil || s.pool == nil || userID == 0 {
		return domain.AccountDeletionRequest{}, false, nil
	}
	return pendingAccountDeletion(ctx, s.pool, userID, digest[:])
}

func (s *AccountLifecycleStore) ExecuteAccountDeletion(ctx context.Context, userID int64, source domain.AccountDeletionSource, reason string, now time.Time) (domain.AccountDeletionResult, error) {
	if s == nil || s.pool == nil || userID == 0 || now.IsZero() || !validAccountDeletionSource(source) {
		return domain.AccountDeletionResult{}, domain.ErrAccountDeletionForbidden
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AccountDeletionResult{}, fmt.Errorf("begin execute account deletion: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockUsersForUpdate(ctx, tx, userID); err != nil {
		return domain.AccountDeletionResult{}, fmt.Errorf("lock account deletion user: %w", err)
	}
	var lockedID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AccountDeletionResult{}, domain.ErrUserNotFound
		}
		return domain.AccountDeletionResult{}, fmt.Errorf("lock account deletion row: %w", err)
	}
	u, found, err := NewUserStore(tx).ByID(ctx, userID)
	if err != nil {
		return domain.AccountDeletionResult{}, err
	}
	if !found {
		return domain.AccountDeletionResult{}, domain.ErrUserNotFound
	}
	if u.Deleted {
		return domain.AccountDeletionResult{User: u, Changed: false}, nil
	}
	if u.Bot || domain.IsSystemUserID(u.ID) {
		return domain.AccountDeletionResult{}, domain.ErrAccountDeletionForbidden
	}
	due, err := accountDeletionStillDue(ctx, tx, u, source, now)
	if err != nil {
		return domain.AccountDeletionResult{}, err
	}
	if !due {
		return domain.AccountDeletionResult{User: u, Changed: false}, nil
	}
	if err := enqueueAccountDeletionNotifications(ctx, tx, userID); err != nil {
		return domain.AccountDeletionResult{}, err
	}
	if err := settleDeletedAccountFinancialState(ctx, tx, userID, now); err != nil {
		return domain.AccountDeletionResult{}, err
	}
	revoked, err := revokeByUserExceptTx(ctx, tx, userID, 0)
	if err != nil {
		return domain.AccountDeletionResult{}, fmt.Errorf("revoke deleted account authorizations: %w", err)
	}
	if err := purgeDeletedAccountPrivateState(ctx, tx, userID, now); err != nil {
		return domain.AccountDeletionResult{}, err
	}
	if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeUser, userID, ""); err != nil {
		return domain.AccountDeletionResult{}, fmt.Errorf("release deleted account username: %w", err)
	}
	reason = strings.TrimSpace(reason)
	reason = truncateUTF8Bytes(reason, 1024)
	if _, err := tx.Exec(ctx, `
UPDATE users SET
  phone = '', first_name = '', last_name = '', username = '', country_code = '', about = '',
  verified = false, support = false, last_seen_at = 0,
  premium_expires_at = NULL, emoji_status_document_id = 0, emoji_status_until = 0,
  emoji_status_collectible_id = NULL, emoji_status_collectible = '{}'::jsonb,
  color_set = false, color = 0, color_background_emoji_id = 0,
  profile_color_set = false, profile_color = 0, profile_color_background_emoji_id = 0,
  birthday_day = 0, birthday_month = 0, birthday_year = 0, personal_channel_id = 0,
  deleted_at = $2, deletion_source = $3, deletion_reason = $4,
  account_delete_at = NULL, updated_at = $2
WHERE id = $1 AND deleted_at IS NULL`, userID, now, string(source), reason); err != nil {
		return domain.AccountDeletionResult{}, fmt.Errorf("write deleted account tombstone: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE account_deletion_requests
SET state = 'executed', completed_at = $2, updated_at = $2
WHERE user_id = $1 AND state = 'pending'`, userID, now); err != nil {
		return domain.AccountDeletionResult{}, fmt.Errorf("complete account deletion request: %w", err)
	}
	u, found, err = NewUserStore(tx).ByID(ctx, userID)
	if err != nil || !found {
		if err == nil {
			err = domain.ErrUserNotFound
		}
		return domain.AccountDeletionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AccountDeletionResult{}, fmt.Errorf("commit execute account deletion: %w", err)
	}
	return domain.AccountDeletionResult{User: u, Changed: true, RevokedAuthorizations: revoked}, nil
}

func (s *AccountLifecycleStore) CancelAccountDeletion(ctx context.Context, userID int64, digest [32]byte, now time.Time) ([]domain.Authorization, error) {
	if s == nil || s.pool == nil || userID == 0 || now.IsZero() {
		return nil, domain.ErrAccountDeletionHashInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin cancel account deletion: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockUsersForUpdate(ctx, tx, userID); err != nil {
		return nil, fmt.Errorf("lock cancel account deletion: %w", err)
	}
	pending, ok, err := pendingAccountDeletionForUpdate(ctx, tx, userID, digest[:])
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrAccountDeletionHashInvalid
	}
	revoked, err := revokeOneAuthorizationTx(ctx, tx, userID, pending.RequesterAuthKeyID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE account_deletion_requests
SET state = 'cancelled', completed_at = $2, updated_at = $2
WHERE id = $1 AND state = 'pending'`, pending.ID, now); err != nil {
		return nil, fmt.Errorf("cancel account deletion request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit cancel account deletion: %w", err)
	}
	return revoked, nil
}

func (s *AccountLifecycleStore) DueAccountDeletions(ctx context.Context, now time.Time, limit int) ([]domain.AccountDeletionCandidate, error) {
	if s == nil || s.pool == nil || limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
WITH candidates AS (
  SELECT user_id, 'password_reset_expiry'::text AS source, execute_at AS due_at, 1 AS priority
    FROM account_deletion_requests WHERE state = 'pending' AND execute_at <= $1
  UNION ALL
  SELECT id, 'account_ttl', account_delete_at, 2
    FROM users WHERE deleted_at IS NULL AND is_bot = false AND account_delete_at <= $1
  UNION ALL
  SELECT r.user_id, 'freeze_expiry', r.frozen_until, 3
    FROM account_restrictions r JOIN users u ON u.id = r.user_id
   WHERE r.frozen = true AND r.frozen_until IS NOT NULL AND r.frozen_until <= $1
     AND u.deleted_at IS NULL AND u.is_bot = false
), dedup AS (
  SELECT DISTINCT ON (user_id) user_id, source, due_at
    FROM candidates ORDER BY user_id, priority, due_at
)
SELECT user_id, source, due_at FROM dedup ORDER BY due_at, user_id LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list due account deletions: %w", err)
	}
	defer rows.Close()
	out := make([]domain.AccountDeletionCandidate, 0)
	for rows.Next() {
		var c domain.AccountDeletionCandidate
		var source string
		if err := rows.Scan(&c.UserID, &source, &c.DueAt); err != nil {
			return nil, fmt.Errorf("scan due account deletion: %w", err)
		}
		c.Source = domain.AccountDeletionSource(source)
		out = append(out, c)
	}
	return out, rows.Err()
}

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
	out := make([]domain.AccountDeletionNotification, 0)
	for rows.Next() {
		var n domain.AccountDeletionNotification
		if err := rows.Scan(&n.ID, &n.TargetUserID, &n.DeletedUserID, &n.Attempts); err != nil {
			return nil, fmt.Errorf("scan account deletion notification: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *AccountLifecycleStore) CompleteAccountDeletionNotification(ctx context.Context, id int64, now time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE account_deletion_notifications
SET status = 'delivered', lease_until = NULL, last_error = '', updated_at = $2 WHERE id = $1`, id, now)
	if err != nil {
		return fmt.Errorf("complete account deletion notification: %w", err)
	}
	return nil
}

type accountDeletionRowScanner interface {
	Scan(dest ...any) error
}

func scanAccountDeletionRequest(row accountDeletionRowScanner) (domain.AccountDeletionRequest, error) {
	var (
		r           domain.AccountDeletionRequest
		authKey     int64
		state       string
		digest      []byte
		completedAt *time.Time
	)
	if err := row.Scan(&r.ID, &r.UserID, &authKey, &state, &r.Reason, &digest,
		&r.RequestedAt, &r.ExecuteAt, &completedAt); err != nil {
		return domain.AccountDeletionRequest{}, err
	}
	if len(digest) != len(r.ConfirmHashDigest) {
		return domain.AccountDeletionRequest{}, fmt.Errorf("invalid account deletion digest length %d", len(digest))
	}
	copy(r.ConfirmHashDigest[:], digest)
	r.RequesterAuthKeyID = authKeyIDFromInt64(authKey)
	r.State = domain.AccountDeletionRequestState(state)
	if completedAt != nil {
		r.CompletedAt = *completedAt
	}
	return r, nil
}

func pendingAccountDeletion(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, userID int64, digest []byte) (domain.AccountDeletionRequest, bool, error) {
	query := `SELECT id, user_id, requester_auth_key_id, state, reason, confirm_hash_digest,
requested_at, execute_at, completed_at FROM account_deletion_requests
WHERE user_id = $1 AND state = 'pending'`
	args := []any{userID}
	if digest != nil {
		query += ` AND confirm_hash_digest = $2`
		args = append(args, digest)
	}
	r, err := scanAccountDeletionRequest(db.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AccountDeletionRequest{}, false, nil
	}
	if err != nil {
		return domain.AccountDeletionRequest{}, false, fmt.Errorf("load pending account deletion: %w", err)
	}
	return r, true, nil
}

func pendingAccountDeletionForUpdate(ctx context.Context, tx pgx.Tx, userID int64, digest []byte) (domain.AccountDeletionRequest, bool, error) {
	row := tx.QueryRow(ctx, `SELECT id, user_id, requester_auth_key_id, state, reason, confirm_hash_digest,
requested_at, execute_at, completed_at FROM account_deletion_requests
WHERE user_id = $1 AND confirm_hash_digest = $2 AND state = 'pending' FOR UPDATE`, userID, digest)
	r, err := scanAccountDeletionRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AccountDeletionRequest{}, false, nil
	}
	if err != nil {
		return domain.AccountDeletionRequest{}, false, fmt.Errorf("lock pending account deletion: %w", err)
	}
	return r, true, nil
}

func validAccountDeletionSource(source domain.AccountDeletionSource) bool {
	switch source {
	case domain.AccountDeletionManual, domain.AccountDeletionForgotPassword, domain.AccountDeletionTOSDecline,
		domain.AccountDeletionPasswordResetExpiry, domain.AccountDeletionAccountTTL, domain.AccountDeletionFreezeExpiry:
		return true
	default:
		return false
	}
}

// accountDeletionStillDue closes the list-then-execute race for every scheduled
// source. The user row is already locked; source-specific facts are read and,
// where applicable, locked again immediately before destructive work begins.
// Manual sources are admission decisions made by the caller and have no
// independently mutable deadline to revalidate.
func accountDeletionStillDue(ctx context.Context, tx pgx.Tx, user domain.User, source domain.AccountDeletionSource, now time.Time) (bool, error) {
	switch source {
	case domain.AccountDeletionManual, domain.AccountDeletionForgotPassword, domain.AccountDeletionTOSDecline:
		return true, nil
	case domain.AccountDeletionPasswordResetExpiry:
		var requestID int64
		err := tx.QueryRow(ctx, `
SELECT id FROM account_deletion_requests
WHERE user_id = $1 AND state = 'pending' AND execute_at <= $2
ORDER BY execute_at, id LIMIT 1 FOR UPDATE`, user.ID, now).Scan(&requestID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("revalidate pending account deletion: %w", err)
		}
		return true, nil
	case domain.AccountDeletionAccountTTL:
		return !user.AccountDeleteAt.IsZero() && !user.AccountDeleteAt.After(now), nil
	case domain.AccountDeletionFreezeExpiry:
		var frozen bool
		var frozenUntil *time.Time
		err := tx.QueryRow(ctx, `
SELECT frozen, frozen_until FROM account_restrictions WHERE user_id = $1 FOR UPDATE`, user.ID).Scan(&frozen, &frozenUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("revalidate frozen account deletion: %w", err)
		}
		return frozen && frozenUntil != nil && !frozenUntil.After(now), nil
	default:
		return false, domain.ErrAccountDeletionForbidden
	}
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes < 1 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}

func enqueueAccountDeletionNotifications(ctx context.Context, tx pgx.Tx, userID int64) error {
	const maxAccountDeletionNotificationAudience = 4096
	_, err := tx.Exec(ctx, `
INSERT INTO account_deletion_notifications (target_user_id, deleted_user_id)
SELECT audience.user_id, $1
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
  LIMIT $2
) audience
JOIN users u ON u.id = audience.user_id
WHERE audience.user_id <> $1 AND u.deleted_at IS NULL
ON CONFLICT (target_user_id, deleted_user_id) DO NOTHING`, userID, maxAccountDeletionNotificationAudience)
	if err != nil {
		return fmt.Errorf("enqueue account deletion notifications: %w", err)
	}
	return nil
}

func revokeOneAuthorizationTx(ctx context.Context, tx pgx.Tx, userID int64, authKeyID [8]byte) ([]domain.Authorization, error) {
	id := authKeyIDToInt64(authKeyID)
	if id == 0 {
		return nil, nil
	}
	if err := lockPermanentAuthIdentities(ctx, tx, []int64{id}); err != nil {
		return nil, err
	}
	var locked int64
	if err := tx.QueryRow(ctx, `SELECT auth_key_id FROM auth_keys WHERE auth_key_id = $1 FOR UPDATE`, id).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lock password reset auth key: %w", err)
	}
	a, found, err := scanRevokedAuthorization(tx.QueryRow(ctx, `
SELECT auth_key_id, user_id, hash, layer, device_model, platform, system_version,
       api_id, app_version, ip, password_pending, created_at, active_at
FROM authorizations WHERE auth_key_id = $1 AND user_id = $2 FOR UPDATE`, id, userID))
	if err != nil {
		return nil, fmt.Errorf("load password reset authorization: %w", err)
	}
	if !found {
		return nil, nil
	}
	if err := deleteRevocationTargetsTx(ctx, tx, []int64{id}); err != nil {
		return nil, err
	}
	return []domain.Authorization{a}, nil
}

func purgeDeletedAccountPrivateState(ctx context.Context, tx pgx.Tx, userID int64, now time.Time) error {
	// Leave shared private_messages/channel_messages and immutable transaction
	// ledgers intact. Only the deleted user's private projections and settings are
	// removed; other users continue to reference the tombstone sender.
	statements := []string{
		`DELETE FROM account_privacy_rules WHERE owner_user_id = $1`,
		`DELETE FROM account_reaction_settings WHERE user_id = $1`,
		`DELETE FROM account_restrictions WHERE user_id = $1`,
		`DELETE FROM account_settings WHERE user_id = $1`,
		`DELETE FROM account_passwords WHERE user_id = $1`,
		`DELETE FROM notify_settings WHERE owner_user_id = $1`,
		`DELETE FROM passkey_credentials WHERE user_id = $1`,
		`DELETE FROM contacts WHERE user_id = $1 OR contact_user_id = $1`,
		`DELETE FROM contact_blocks WHERE owner_user_id = $1 OR blocked_user_id = $1`,
		`DELETE FROM dialog_drafts WHERE user_id = $1`,
		`DELETE FROM dialog_filter_settings WHERE user_id = $1`,
		`DELETE FROM dialog_filters WHERE user_id = $1`,
		`DELETE FROM chatlist_memberships WHERE user_id = $1 OR owner_user_id = $1`,
		`DELETE FROM chatlist_invites WHERE owner_user_id = $1`,
		`DELETE FROM saved_dialog_pins WHERE user_id = $1`,
		`DELETE FROM message_box_media WHERE owner_user_id = $1`,
		`DELETE FROM private_media_category_counts WHERE owner_user_id = $1`,
		`DELETE FROM message_boxes WHERE owner_user_id = $1`,
		`DELETE FROM dialogs WHERE user_id = $1`,
		`DELETE FROM dispatch_outbox WHERE target_user_id = $1`,
		`DELETE FROM dispatch_outbox_user_heads WHERE target_user_id = $1`,
		`DELETE FROM user_update_events WHERE user_id = $1`,
		`DELETE FROM user_update_retention WHERE user_id = $1`,
		`DELETE FROM user_update_watermarks WHERE user_id = $1`,
		`DELETE FROM update_states WHERE user_id = $1`,
		`DELETE FROM bootstrap_update_jobs WHERE user_id = $1`,
		`DELETE FROM scheduled_messages WHERE owner_user_id = $1`,
		`DELETE FROM quick_reply_messages WHERE owner_user_id = $1`,
		`DELETE FROM quick_replies WHERE owner_user_id = $1`,
		`DELETE FROM saved_music WHERE user_id = $1`,
		`DELETE FROM user_sticker_collections WHERE owner_user_id = $1`,
		`DELETE FROM user_sticker_sets WHERE owner_user_id = $1`,
		`DELETE FROM user_recent_reactions WHERE user_id = $1`,
		`DELETE FROM user_saved_reaction_tags WHERE user_id = $1`,
		`DELETE FROM user_top_reactions WHERE user_id = $1`,
		`DELETE FROM theme_user_installs WHERE user_id = $1`,
		`DELETE FROM peer_translation_settings WHERE user_id = $1`,
		`DELETE FROM ai_compose_tone_saves WHERE user_id = $1`,
		`DELETE FROM ai_compose_tones WHERE owner_user_id = $1`,
		`DELETE FROM business_automation_deliveries WHERE owner_user_id = $1 OR peer_user_id = $1`,
		`DELETE FROM business_connected_bot_peer_states WHERE owner_user_id = $1 OR peer_user_id = $1`,
		`DELETE FROM business_connected_bots WHERE owner_user_id = $1`,
		`DELETE FROM business_chat_links WHERE owner_user_id = $1`,
		`DELETE FROM user_business_profiles WHERE user_id = $1`,
		`DELETE FROM attach_menu_user_states WHERE user_id = $1`,
		`DELETE FROM bot_emoji_status_permissions WHERE user_id = $1`,
		`DELETE FROM bot_user_permissions WHERE user_id = $1`,
		`DELETE FROM login_code_message_deliveries WHERE user_id = $1`,
		`DELETE FROM webview_custom_method_queries WHERE user_id = $1`,
		`DELETE FROM webview_requested_buttons WHERE user_id = $1`,
		`DELETE FROM profile_photos WHERE owner_peer_type = 'user' AND owner_peer_id = $1`,
		`DELETE FROM story_views WHERE viewer_user_id = $1 OR (owner_peer_type = 'user' AND owner_peer_id = $1)`,
		`DELETE FROM story_exposures WHERE viewer_user_id = $1 OR (owner_peer_type = 'user' AND owner_peer_id = $1)`,
		`DELETE FROM story_read_states WHERE viewer_user_id = $1 OR (owner_peer_type = 'user' AND owner_peer_id = $1)`,
		`DELETE FROM story_hidden_peers WHERE viewer_user_id = $1 OR (owner_peer_type = 'user' AND owner_peer_id = $1)`,
		`DELETE FROM stories WHERE owner_peer_type = 'user' AND owner_peer_id = $1`,
		`DELETE FROM group_call_schedule_subscribers WHERE user_id = $1`,
		`DELETE FROM group_call_participants WHERE user_id = $1`,
		`DELETE FROM group_call_invites WHERE inviter_user_id = $1 OR invitee_user_id = $1`,
		`DELETE FROM channel_boost_slots WHERE user_id = $1`,
		`DELETE FROM channel_invite_importers WHERE user_id = $1`,
		`DELETE FROM channel_topic_read WHERE user_id = $1`,
		`DELETE FROM channel_unread_mentions WHERE user_id = $1`,
		`DELETE FROM channel_unread_mention_index WHERE user_id = $1`,
		`DELETE FROM channel_dialogs WHERE user_id = $1`,
		`DELETE FROM user_channel_member_index WHERE user_id = $1`,
		`DELETE FROM account_deletion_notifications WHERE target_user_id = $1`,
		`DELETE FROM uploaded_media_receipts WHERE owner_user_id = $1`,
		`DELETE FROM upload_parts WHERE owner_user_id = $1`,
		`DELETE FROM encrypted_files WHERE owner_user_id = $1`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, userID); err != nil {
			return fmt.Errorf("purge deleted account private state (%s): %w", statement, err)
		}
	}
	if _, err := tx.Exec(ctx, `
WITH changed AS (
  UPDATE channel_members
     SET status = 'left', left_at = $2, unread_mark = false, updated_at = $3
   WHERE user_id = $1 AND status = 'active'
   RETURNING channel_id, role
), counts AS (
  SELECT channel_id, count(*) AS participants,
         count(*) FILTER (WHERE role IN ('creator', 'admin')) AS admins
    FROM changed GROUP BY channel_id
)
UPDATE channels c
SET participants_count = GREATEST(0, c.participants_count - counts.participants::int),
    admins_count = GREATEST(0, c.admins_count - counts.admins::int),
    updated_at = $3
FROM counts WHERE c.id = counts.channel_id`, userID, int(now.Unix()), now); err != nil {
		return fmt.Errorf("leave deleted account channels: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE secret_chats SET state = 'discarded', history_deleted = true,
       g_a = ''::bytea, g_b = ''::bytea, key_fingerprint = 0
WHERE admin_user_id = $1 OR participant_user_id = $1`, userID); err != nil {
		return fmt.Errorf("discard deleted account secret chats: %w", err)
	}
	return nil
}

func settleDeletedAccountFinancialState(ctx context.Context, tx pgx.Tx, userID int64, now time.Time) error {
	nowUnix := int(now.Unix())
	rows, err := tx.Query(ctx, `
SELECT id, buyer_user_id, currency, amount
FROM star_gift_offers
WHERE owner_peer_type = 'user' AND owner_peer_id = $1 AND status = 'pending'
ORDER BY id FOR UPDATE`, userID)
	if err != nil {
		return fmt.Errorf("lock deleted account gift offers: %w", err)
	}
	type offer struct {
		id, buyer, amount int64
		currency          string
	}
	offers := make([]offer, 0)
	for rows.Next() {
		var o offer
		if err := rows.Scan(&o.id, &o.buyer, &o.currency, &o.amount); err != nil {
			rows.Close()
			return fmt.Errorf("scan deleted account gift offer: %w", err)
		}
		offers = append(offers, o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, o := range offers {
		var balance int64
		if o.currency == "XTR" {
			if err := tx.QueryRow(ctx, `
INSERT INTO stars_balances (user_id, balance) VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()
RETURNING balance`, o.buyer, o.amount).Scan(&balance); err != nil {
				return fmt.Errorf("refund deleted account stars offer: %w", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO stars_transactions
(user_id, peer_type, peer_id, amount, reason, title, description, date)
VALUES ($1, 'user', $2, $3, 'gift_offer_refund_account_deleted', 'Gift offer refunded', '', $4)`, o.buyer, userID, o.amount, nowUnix); err != nil {
				return fmt.Errorf("record deleted account stars refund: %w", err)
			}
		} else {
			if err := tx.QueryRow(ctx, `
INSERT INTO ton_balances (user_id, balance_nanoton) VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET balance_nanoton = ton_balances.balance_nanoton + EXCLUDED.balance_nanoton, updated_at = now()
RETURNING balance_nanoton`, o.buyer, o.amount).Scan(&balance); err != nil {
				return fmt.Errorf("refund deleted account TON offer: %w", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO ton_transactions
(user_id, amount_nanoton, reason, peer_type, peer_id, date)
VALUES ($1, $2, 'gift_offer_refund_account_deleted', 'user', $3, $4)`, o.buyer, o.amount, userID, nowUnix); err != nil {
				return fmt.Errorf("record deleted account TON refund: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE star_gift_offers
SET status = 'cancelled', resolved_at = $2, balance_after = $3
WHERE id = $1 AND status = 'pending'`, o.id, nowUnix, balance); err != nil {
			return fmt.Errorf("cancel deleted account gift offer: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE star_gift_offers
SET status = 'cancelled', resolved_at = $2, balance_after = 0
WHERE buyer_user_id = $1 AND status = 'pending'`, userID, nowUnix); err != nil {
		return fmt.Errorf("cancel deleted buyer gift offers: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE star_gift_withdrawal_requests
SET status = 'failed', completed_at = $2 WHERE owner_user_id = $1 AND status = 'pending'`, userID, nowUnix); err != nil {
		return fmt.Errorf("fail deleted account withdrawals: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE star_gift_auction_bids SET active = false, version = version + 1
WHERE bidder_user_id = $1 AND active = true`, userID); err != nil {
		return fmt.Errorf("deactivate deleted account auction bids: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts
SET burned = true, owner_name = '', updated_at = $2
WHERE owner_peer_type = 'user' AND owner_peer_id = $1`, userID, now); err != nil {
		return fmt.Errorf("burn deleted account unique gifts: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts
SET lifecycle_status = 'burned', unsaved = true, pinned_order = 0
WHERE owner_peer_type = 'user' AND owner_peer_id = $1 AND unique_gift_id IS NOT NULL`, userID); err != nil {
		return fmt.Errorf("burn deleted account saved gifts: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM peer_star_gifts
WHERE owner_peer_type = 'user' AND owner_peer_id = $1 AND unique_gift_id IS NULL`, userID); err != nil {
		return fmt.Errorf("delete deleted account regular gifts: %w", err)
	}
	var stars int64
	if err := tx.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id = $1 FOR UPDATE`, userID).Scan(&stars); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock deleted account stars balance: %w", err)
	}
	if stars != 0 {
		if _, err := tx.Exec(ctx, `UPDATE stars_balances SET balance = 0, updated_at = $2 WHERE user_id = $1`, userID, now); err != nil {
			return fmt.Errorf("zero deleted account stars: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO stars_transactions
(user_id, peer_type, peer_id, amount, reason, title, description, date)
VALUES ($1, 'user', $1, $2, 'account_deleted', 'Account deleted', '', $3)`, userID, -stars, nowUnix); err != nil {
			return fmt.Errorf("record deleted account stars clearing: %w", err)
		}
	}
	var ton int64
	if err := tx.QueryRow(ctx, `SELECT balance_nanoton FROM ton_balances WHERE user_id = $1 FOR UPDATE`, userID).Scan(&ton); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock deleted account TON balance: %w", err)
	}
	if ton != 0 {
		if _, err := tx.Exec(ctx, `UPDATE ton_balances SET balance_nanoton = 0, updated_at = $2 WHERE user_id = $1`, userID, now); err != nil {
			return fmt.Errorf("zero deleted account TON: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ton_transactions
(user_id, amount_nanoton, reason, date) VALUES ($1, $2, 'account_deleted', $3)`, userID, -ton, nowUnix); err != nil {
			return fmt.Errorf("record deleted account TON clearing: %w", err)
		}
	}
	return nil
}
