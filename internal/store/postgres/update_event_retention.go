package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const userUpdateRetentionTransactionBatch = 256

// DeleteConfirmedPrefix deletes an account's durable update common-confirmation safe prefix.
//
// Safety boundary: only current authorizations are considered; when an authorization lacks
// update_states, its observed watermark is treated as 0, so events it may still need are not deleted.
// AuthorizationStore.Bind initializes a new authorization's delivered state to the account's current
// watermark and its observed baseline to the reclaimed floor: a new device does not need to recover
// events deleted before its authorization was created, but it still blocks further prefix reclamation
// until it actively reports subsequent pts.
func (s *UpdateEventStore) DeleteConfirmedPrefix(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	if olderThan <= 0 {
		olderThan = 7 * 24 * time.Hour
	}
	if limit <= 0 {
		limit = 10000
	}
	if limit > 100000 {
		limit = 100000
	}
	cutoff := int32(time.Now().Add(-olderThan).Unix())
	deletedTotal := 0
	excluded := make([]int64, 0)
	for deletedTotal < limit {
		userID, err := s.oldestConfirmedRetentionCandidate(ctx, cutoff, excluded)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			return deletedTotal, err
		}
		deleted := 0
		err = withTx(ctx, s.db, "delete confirmed user update prefix", func(tx pgx.Tx) error {
			var pruneErr error
			chunkLimit := limit - deletedTotal
			if chunkLimit > userUpdateRetentionTransactionBatch {
				chunkLimit = userUpdateRetentionTransactionBatch
			}
			deleted, pruneErr = pruneConfirmedUserPrefixTx(ctx, tx, userID, cutoff, chunkLimit)
			return pruneErr
		})
		if err != nil {
			return deletedTotal, err
		}
		if deleted == 0 {
			// The candidate may have been handled by another worker between selection and locking, or
			// hit a pre-existing hole; exclude it this round and keep looking for other users so one
			// racing account does not starve global reclamation. The candidate SQL already allows only
			// the immediate head after the floor, so a "new head + old tail" or gapped account no longer
			// consumes any 256-pass quota; hence no artificial 256 truncation is set here either.
			excluded = append(excluded, userID)
			continue
		}
		deletedTotal += deleted
	}
	return deletedTotal, nil
}

func (s *UpdateEventStore) oldestConfirmedRetentionCandidate(ctx context.Context, cutoff int32, excluded []int64) (int64, error) {
	var userID int64
	err := s.db.QueryRow(ctx, `
SELECT e.user_id
FROM user_update_events e
LEFT JOIN user_update_retention r ON r.user_id = e.user_id
WHERE e.date < $1
  AND e.pts > COALESCE(r.retained_through_pts, 0)
  AND e.pts_count > 0
  -- Only the first complete event immediately after the retained floor may make
  -- a user a candidate.  A later old-dated tail behind a recent head must not
  -- repeatedly win the global date seek and then produce a zero-row prune.
  AND e.pts = COALESCE(r.retained_through_pts, 0) + e.pts_count
  AND NOT EXISTS (
    SELECT 1
    FROM user_update_events earlier
    WHERE earlier.user_id = e.user_id
      AND earlier.pts > COALESCE(r.retained_through_pts, 0)
      AND earlier.pts < e.pts
  )
  AND NOT (e.user_id = ANY($2::bigint[]))
  AND EXISTS (SELECT 1 FROM authorizations a WHERE a.user_id = e.user_id)
  AND e.pts <= COALESCE((
    SELECT MIN(COALESCE(s.observed_pts, 0))
    FROM authorizations a
    LEFT JOIN update_states s
      ON s.auth_key_id = a.auth_key_id
     AND s.user_id = a.user_id
    WHERE a.user_id = e.user_id
  ), 0)
ORDER BY e.date ASC, e.user_id ASC, e.pts ASC
LIMIT 1`, cutoff, excluded).Scan(&userID)
	if err != nil {
		return 0, err
	}
	return userID, nil
}

type retainedUserEventRow struct {
	pts      int
	ptsCount int
	date     int
}

func pruneConfirmedUserPrefixTx(ctx context.Context, tx pgx.Tx, userID int64, cutoff int32, limit int) (int, error) {
	if userID == 0 || limit <= 0 {
		return 0, nil
	}
	// Share the watermark row lock with all pts allocations: new business events must not slip in between floor computation and deletion.
	var currentPts int
	if err := tx.QueryRow(ctx, `
SELECT contiguous_pts
FROM user_update_watermarks
WHERE user_id = $1
FOR UPDATE`, userID).Scan(&currentPts); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("lock user update watermark: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO user_update_retention (user_id)
VALUES ($1)
ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return 0, fmt.Errorf("ensure user update retention: %w", err)
	}
	var floor int
	if err := tx.QueryRow(ctx, `
SELECT retained_through_pts
FROM user_update_retention
WHERE user_id = $1
FOR UPDATE`, userID).Scan(&floor); err != nil {
		return 0, fmt.Errorf("lock user update retention: %w", err)
	}
	var authCount, safePts int
	if err := tx.QueryRow(ctx, `
SELECT COUNT(*)::int, COALESCE(MIN(COALESCE(s.observed_pts, 0)), 0)::int
FROM authorizations a
LEFT JOIN update_states s
  ON s.auth_key_id = a.auth_key_id
 AND s.user_id = a.user_id
WHERE a.user_id = $1`, userID).Scan(&authCount, &safePts); err != nil {
		return 0, fmt.Errorf("load confirmed user update floor: %w", err)
	}
	if authCount == 0 || safePts <= floor {
		return 0, nil
	}
	if safePts > currentPts {
		return 0, fmt.Errorf("confirmed user update pts %d exceeds current %d for user %d", safePts, currentPts, userID)
	}
	rows, err := tx.Query(ctx, `
SELECT pts, pts_count, date
FROM user_update_events
WHERE user_id = $1
  AND pts > $2
  AND pts <= $3
  AND date < $4
ORDER BY pts ASC
LIMIT $5`, userID, floor, safePts, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("list confirmed user update prefix: %w", err)
	}
	defer rows.Close()
	events := make([]retainedUserEventRow, 0, limit)
	expected := floor
	for rows.Next() {
		var event retainedUserEventRow
		if err := rows.Scan(&event.pts, &event.ptsCount, &event.date); err != nil {
			return 0, fmt.Errorf("scan confirmed user update prefix: %w", err)
		}
		if event.ptsCount <= 0 {
			return 0, fmt.Errorf("invalid pts_count %d at user %d pts %d", event.ptsCount, userID, event.pts)
		}
		expected += event.ptsCount
		if event.pts != expected {
			// Never advance the retained floor across a pre-existing hole.
			break
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate confirmed user update prefix: %w", err)
	}
	// A gap/date boundary may stop iteration before pgx consumed the result set. Close explicitly
	// before issuing DELETE on the same transaction connection; otherwise pgx reports conn busy.
	rows.Close()
	if len(events) == 0 {
		return 0, nil
	}
	pts := make([]int32, len(events))
	for i, event := range events {
		pts[i] = int32(event.pts)
	}
	// Retention can remove the final online-delivery task for this user. Take the
	// exclusive lane fence before locking the durable head so the subsequent
	// READ COMMITTED statements observe every producer that completed first and
	// later producers cannot miss the empty-lane transition.
	if err := lockDispatchOutboxLanesExclusive(ctx, tx, []int64{userID}); err != nil {
		return 0, fmt.Errorf("lock retained user update dispatch lane: %w", err)
	}
	// Every outbox mutation follows user_heads→outbox. Retention may remove a pending or leased
	// task after the client has already confirmed its durable event; lock the lane head first so
	// it cannot deadlock a lease-expiry claim/completion. No head means these events have no online
	// task and the durable prefix can still be pruned safely.
	var lockedDispatchUserID int64
	err = tx.QueryRow(ctx, `
SELECT target_user_id
FROM dispatch_outbox_user_heads
WHERE target_user_id = $1
FOR UPDATE`, userID).Scan(&lockedDispatchUserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("lock retained user update dispatch head: %w", err)
	}
	// A retained durable event can still have a pending/dispatching outbox row (for example a
	// client confirmed the pts through difference while an online push lease was in flight).
	// Remove those leases first, in this same transaction. The outbox head trigger promotes the
	// next user lane row; any worker holding an old attempts token is fenced by MarkDelivered/
	// MarkFailed returning ErrDispatchLeaseLost after this commit.
	if _, err := tx.Exec(ctx, `
DELETE FROM dispatch_outbox
WHERE target_user_id = $1
  AND pts = ANY($2::int[])`, userID, pts); err != nil {
		return 0, fmt.Errorf("delete retained user update dispatch outbox: %w", err)
	}
	tag, err := tx.Exec(ctx, `
DELETE FROM user_update_events
WHERE user_id = $1
  AND pts = ANY($2::int[])`, userID, pts)
	if err != nil {
		return 0, fmt.Errorf("delete confirmed user update prefix: %w", err)
	}
	if tag.RowsAffected() != int64(len(events)) {
		return 0, fmt.Errorf("delete confirmed user update prefix affected %d rows, want %d", tag.RowsAffected(), len(events))
	}
	last := events[len(events)-1]
	if _, err := tx.Exec(ctx, `
UPDATE user_update_retention
SET retained_through_pts = $2,
    retained_through_date = $3,
    updated_at = now()
WHERE user_id = $1`, userID, last.pts, last.date); err != nil {
		return 0, fmt.Errorf("advance user update retention: %w", err)
	}
	return len(events), nil
}

// UserUpdateRetentionCheckpoint returns the safe prefix the current auth key has explicitly
// confirmed and can skip via the ordinary differenceSlice.
//
// Once retained floor > 0, any surviving authorization must also have observed_pts >= floor;
// AuthorizationStore.Bind establishes this baseline in the same transaction. If an authorization
// exists here but its state is missing/regressed, the lifecycle invariant has already been broken.
// At that point we must fail fast, not return ok=false and let GetDifference keep reading from the
// deleted prefix while pretending it is an empty difference.
func (s *UpdateEventStore) UserUpdateRetentionCheckpoint(ctx context.Context, authKeyID [8]byte, userID int64) (pts, date int, ok bool, err error) {
	if s == nil || s.db == nil || userID == 0 || authKeyID == ([8]byte{}) {
		return 0, 0, false, nil
	}
	var (
		authorized bool
		observed   int
	)
	err = s.db.QueryRow(ctx, `
SELECT
  r.retained_through_pts,
  r.retained_through_date,
  EXISTS (
    SELECT 1
    FROM authorizations a
    WHERE a.auth_key_id = $1
      AND a.user_id = r.user_id
  ) AS authorized,
  COALESCE((
    SELECT s.observed_pts
    FROM update_states s
    WHERE s.auth_key_id = $1
      AND s.user_id = r.user_id
  ), -1)::int AS observed_pts
FROM user_update_retention r
WHERE r.user_id = $2
  AND r.retained_through_pts > 0`, authKeyIDToInt64(authKeyID), userID).Scan(&pts, &date, &authorized, &observed)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("get user update retention checkpoint: %w", err)
	}
	if !authorized {
		return 0, 0, false, nil
	}
	if observed < pts {
		return 0, 0, false, fmt.Errorf(
			"get user update retention checkpoint: invariant violation: auth key %x user %d observed pts %d below retained floor %d",
			authKeyID, userID, observed, pts,
		)
	}
	return pts, date, true, nil
}
