package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const userUpdateRetentionTransactionBatch = 256

// DeleteConfirmedPrefix 删除账号 durable update 的共同确认安全前缀。
//
// 安全边界：只考虑当前 authorizations；任一授权缺 update_states 时其 observed 水位按 0，
// 因而不会删除它可能仍需的事件。AuthorizationStore.Bind 会为新授权以账号当前水位
// 初始化 delivered state、以已回收 floor 初始化 observed baseline：新设备无需恢复其授权
// 创建前已删除的事件，但在主动报告后续 pts 前仍会阻塞新的前缀回收。
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
			// candidate 在选取与加锁之间可能被其它 worker 处理，或遇到既有空洞；
			// 本轮排除后继续找其它用户，避免一个竞态账号饿死全局回收。
			// candidate SQL 已只允许 floor 后的 immediate head，不再让“新 head+旧 tail”
			// 或缺口账号占用任意 256-pass 配额；因此这里也不再设人为 256 截断。
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
	// 与所有 pts 分配共享 watermark 行锁：新业务事件不能在 floor 计算与删除之间穿插。
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
			// 绝不跨越既有空洞推进 retained floor。
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

// UserUpdateRetentionCheckpoint 返回当前 auth key 已明确确认、可通过普通
// differenceSlice 跳过的安全前缀。
//
// 一旦 retained floor > 0，仍存在的 authorization 必须同时有 observed_pts >= floor；
// AuthorizationStore.Bind 在同一事务建立这个 baseline。若这里看到 authorization 存在但
// state 缺失/倒退，说明生命周期不变量已经破坏。此时必须 fail-fast，不能返回 ok=false 后让
// GetDifference 从已删除前缀继续读并伪装成空差分。
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
