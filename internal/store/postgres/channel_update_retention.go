package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

const (
	channelUpdateRetentionCandidateBatch = 256
	// Keep one channel row/checkpoint hot-lock window short even when the maintenance pass has a
	// large global budget. The outer seek loop may consume many chunks; this is a transaction cap,
	// not a per-pass correctness cap.
	channelUpdateRetentionTransactionBatch = 256
)

// PruneChannelUpdateEvents atomically removes a bounded contiguous prefix of one channel's durable
// event log. The retained floor advances only through complete event rows actually deleted; a target
// inside a pts_count interval leaves that row and the floor untouched.
func (s *ChannelStore) PruneChannelUpdateEvents(ctx context.Context, channelID int64, throughPts, limit int) (domain.ChannelUpdateRetentionResult, error) {
	return s.pruneChannelUpdateEvents(ctx, channelID, throughPts, 0, limit)
}

// DeleteExpiredChannelUpdateEvents performs a bounded global retention pass without OFFSET. The
// candidate seek uses (date,channel_id,pts), selects only the oldest retained row of each channel,
// then delegates deletion/floor advancement to the per-channel transactional primitive.
func (s *ChannelStore) DeleteExpiredChannelUpdateEvents(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	limit = normalizeChannelUpdateRetentionLimit(limit)
	cutoff := int(time.Now().Add(-olderThan).Unix())
	deleted := 0
	excluded := make([]int64, 0)
	var isolatedErrors []error
	for deleted < limit {
		candidateLimit := limit - deleted
		if candidateLimit > channelUpdateRetentionCandidateBatch {
			candidateLimit = channelUpdateRetentionCandidateBatch
		}
		channelIDs, err := s.expiredChannelUpdateCandidates(ctx, cutoff, candidateLimit, excluded)
		if err != nil {
			isolatedErrors = append(isolatedErrors, err)
			return deleted, errors.Join(isolatedErrors...)
		}
		if len(channelIDs) == 0 {
			break
		}
		for _, channelID := range channelIDs {
			if deleted >= limit {
				break
			}
			chunkLimit := limit - deleted
			if chunkLimit > channelUpdateRetentionTransactionBatch {
				chunkLimit = channelUpdateRetentionTransactionBatch
			}
			result, err := s.pruneChannelUpdateEvents(ctx, channelID, math.MaxInt32, cutoff, chunkLimit)
			if err != nil {
				// A durable-log gap/invalid row is an invariant violation for this channel, but it must
				// not starve every healthy channel behind the oldest candidate. Isolate it for this pass,
				// keep its floor unchanged (the tx rolled back), continue globally, then report all errors.
				excluded = append(excluded, channelID)
				isolatedErrors = append(isolatedErrors, fmt.Errorf("channel %d retention isolated: %w", channelID, err))
				continue
			}
			if result.Deleted == 0 {
				// Another retention worker may have consumed this head after the seek.
				// Exclude it for this pass so one raced channel cannot spin forever.
				excluded = append(excluded, channelID)
				continue
			}
			deleted += result.Deleted
		}
	}
	return deleted, errors.Join(isolatedErrors...)
}

// expiredChannelUpdateCandidates keeps each SQL seek bounded, while the caller loops through as
// many seeks as needed to consume the requested deletion budget.  The 256 value is a fetch/page
// size, not a per-maintenance-pass correctness cap.
func (s *ChannelStore) expiredChannelUpdateCandidates(ctx context.Context, cutoff, limit int, excluded []int64) ([]int64, error) {
	rows, err := s.db.Query(ctx, `
SELECT e.channel_id
FROM channel_update_events e
LEFT JOIN channel_update_checkpoints cp ON cp.channel_id = e.channel_id
WHERE e.date < $1
  AND e.pts > COALESCE(cp.retained_through_pts, 0)
  AND NOT (e.channel_id = ANY($3::bigint[]))
  AND NOT EXISTS (
      SELECT 1
      FROM channel_update_events earlier
      WHERE earlier.channel_id = e.channel_id
        AND earlier.pts > COALESCE(cp.retained_through_pts, 0)
        AND earlier.pts < e.pts
  )
ORDER BY e.date ASC, e.channel_id ASC, e.pts ASC
LIMIT $2`, cutoff, limit, excluded)
	if err != nil {
		return nil, fmt.Errorf("list expired channel update candidates: %w", err)
	}
	defer rows.Close()
	channelIDs := make([]int64, 0, limit)
	for rows.Next() {
		var channelID int64
		if err := rows.Scan(&channelID); err != nil {
			return nil, fmt.Errorf("scan expired channel update candidate: %w", err)
		}
		channelIDs = append(channelIDs, channelID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired channel update candidates: %w", err)
	}
	return channelIDs, nil
}

func (s *ChannelStore) pruneChannelUpdateEvents(ctx context.Context, channelID int64, throughPts, beforeDate, limit int) (domain.ChannelUpdateRetentionResult, error) {
	if channelID == 0 || throughPts < 0 {
		return domain.ChannelUpdateRetentionResult{}, domain.ErrChannelInvalid
	}
	limit = normalizeChannelUpdateRetentionLimit(limit)
	if limit > channelUpdateRetentionTransactionBatch {
		limit = channelUpdateRetentionTransactionBatch
	}
	var result domain.ChannelUpdateRetentionResult
	err := withTx(ctx, s.db, "prune channel update events", func(tx pgx.Tx) error {
		checkpoint, err := lockChannelUpdateCheckpoint(ctx, tx, channelID)
		if err != nil {
			return err
		}
		if throughPts > checkpoint.LatestPts {
			throughPts = checkpoint.LatestPts
		}
		if throughPts <= checkpoint.RetainedThroughPts {
			result.Checkpoint = checkpoint
			return nil
		}

		rows, err := tx.Query(ctx, `
SELECT pts, pts_count, date
FROM channel_update_events
WHERE channel_id = $1
  AND pts > $2
  AND pts <= $3
ORDER BY pts ASC
LIMIT $4
FOR UPDATE`, channelID, checkpoint.RetainedThroughPts, throughPts, limit)
		if err != nil {
			return fmt.Errorf("list channel update prune prefix: %w", err)
		}
		defer rows.Close()
		cursor := checkpoint.RetainedThroughPts
		ptsToDelete := make([]int32, 0, limit)
		for rows.Next() {
			var pts, ptsCount, date int
			if err := rows.Scan(&pts, &ptsCount, &date); err != nil {
				return fmt.Errorf("scan channel update prune prefix: %w", err)
			}
			if beforeDate > 0 && date >= beforeDate {
				break
			}
			if ptsCount <= 0 {
				return fmt.Errorf("prune channel update events: channel %d has invalid pts_count=%d at pts=%d", channelID, ptsCount, pts)
			}
			if pts != cursor+ptsCount {
				return fmt.Errorf(
					"prune channel update events: channel %d has gap after pts %d: event pts=%d pts_count=%d",
					channelID, cursor, pts, ptsCount,
				)
			}
			cursor = pts
			ptsToDelete = append(ptsToDelete, int32(pts))
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate channel update prune prefix: %w", err)
		}
		rows.Close()

		if len(ptsToDelete) == 0 {
			result.Checkpoint = checkpoint
			return nil
		}
		tag, err := tx.Exec(ctx, `
DELETE FROM channel_update_events
WHERE channel_id = $1
  AND pts = ANY($2::int[])`, channelID, ptsToDelete)
		if err != nil {
			return fmt.Errorf("delete channel update prune prefix: %w", err)
		}
		if got := int(tag.RowsAffected()); got != len(ptsToDelete) {
			return fmt.Errorf("delete channel update prune prefix: deleted %d rows, expected %d", got, len(ptsToDelete))
		}
		tag, err = tx.Exec(ctx, `
UPDATE channel_update_checkpoints
SET retained_through_pts = $2,
    latest_event_date = GREATEST(latest_event_date, $3),
    latest_pts = GREATEST(latest_pts, $4),
    updated_at = now()
WHERE channel_id = $1`, channelID, cursor, checkpoint.LatestEventDate, checkpoint.LatestPts)
		if err != nil {
			return fmt.Errorf("advance channel update retained floor: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("advance channel update retained floor: checkpoint row disappeared for channel %d", channelID)
		}
		checkpoint.RetainedThroughPts = cursor
		result = domain.ChannelUpdateRetentionResult{Checkpoint: checkpoint, Deleted: len(ptsToDelete)}
		return nil
	})
	if err != nil {
		return domain.ChannelUpdateRetentionResult{}, err
	}
	return result, nil
}

// lockChannelUpdateCheckpoint follows the channel writer lock order: channels row first, checkpoint
// second. Event insertion updates channels.pts before upserting the checkpoint, so retention cannot
// race a committed pts without its durable event/checkpoint.
func lockChannelUpdateCheckpoint(ctx context.Context, tx pgx.Tx, channelID int64) (domain.ChannelUpdateRetentionCheckpoint, error) {
	var lockedChannelID int64
	if err := tx.QueryRow(ctx, `
SELECT id
FROM channels
WHERE id = $1
FOR UPDATE`, channelID).Scan(&lockedChannelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelUpdateRetentionCheckpoint{}, domain.ErrChannelInvalid
		}
		return domain.ChannelUpdateRetentionCheckpoint{}, fmt.Errorf("lock channel for update retention: %w", err)
	}
	checkpoint := domain.ChannelUpdateRetentionCheckpoint{ChannelID: channelID}
	if err := tx.QueryRow(ctx, `
SELECT retained_through_pts, latest_event_date, latest_pts
FROM channel_update_checkpoints
WHERE channel_id = $1
FOR UPDATE`, channelID).Scan(
		&checkpoint.RetainedThroughPts,
		&checkpoint.LatestEventDate,
		&checkpoint.LatestPts,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelUpdateRetentionCheckpoint{}, fmt.Errorf(
				"lock channel update checkpoint: invariant violation: channel %d has no retention checkpoint",
				channelID,
			)
		}
		return domain.ChannelUpdateRetentionCheckpoint{}, fmt.Errorf("lock channel update checkpoint: %w", err)
	}
	return checkpoint, nil
}

func normalizeChannelUpdateRetentionLimit(limit int) int {
	if limit <= 0 || limit > domain.MaxChannelUpdateRetentionBatch {
		return domain.MaxChannelUpdateRetentionBatch
	}
	return limit
}

func getChannelUpdateCheckpoint(ctx context.Context, db sqlcgen.DBTX, channelID int64) (domain.ChannelUpdateRetentionCheckpoint, error) {
	checkpoint := domain.ChannelUpdateRetentionCheckpoint{ChannelID: channelID}
	err := db.QueryRow(ctx, `
SELECT retained_through_pts, latest_event_date, latest_pts
FROM channel_update_checkpoints
WHERE channel_id = $1`, channelID).Scan(
		&checkpoint.RetainedThroughPts,
		&checkpoint.LatestEventDate,
		&checkpoint.LatestPts,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelUpdateRetentionCheckpoint{}, fmt.Errorf(
			"get channel update checkpoint: invariant violation: channel %d has no retention checkpoint",
			channelID,
		)
	}
	if err != nil {
		return domain.ChannelUpdateRetentionCheckpoint{}, fmt.Errorf("get channel update checkpoint: %w", err)
	}
	return checkpoint, nil
}
