package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// AccountRatingStore is the PostgreSQL implementation of the composite account
// rating read model and its contribution ledger.
//
// account_rating is derived state: it is always rebuildable from the contributing
// tables plus the 'manual' rows of account_rating_events, which is exactly what
// AccountRatingSignals gathers. Writes use optimistic concurrency on the stored
// version so a background recompute and an admin adjustment cannot silently
// overwrite each other.
type AccountRatingStore struct {
	db sqlcgen.DBTX
}

// NewAccountRatingStore builds the store on a pgx pool or transaction.
func NewAccountRatingStore(db sqlcgen.DBTX) *AccountRatingStore {
	return &AccountRatingStore{db: db}
}

var _ store.AccountRatingStore = (*AccountRatingStore)(nil)

const (
	defaultAccountRatingListLimit = 50
	maxAccountRatingListLimit     = 200
)

const accountRatingColumns = `user_id, level, stars, current_level_stars, next_level_stars,
       stars_component, activity_component, penalty_component, manual_component,
       pending_stars, pending_date, computed_at, updated_at, version`

// accountRatingColumnsQualified is the same projection for queries that join, so
// the shared column names stay unambiguous.
const accountRatingColumnsQualified = `r.user_id, r.level, r.stars, r.current_level_stars, r.next_level_stars,
       r.stars_component, r.activity_component, r.penalty_component, r.manual_component,
       r.pending_stars, r.pending_date, r.computed_at, r.updated_at, r.version`

// AccountRating returns the stored projection, distinguishing "never computed"
// from "computed as zero".
func (s *AccountRatingStore) AccountRating(ctx context.Context, userID int64) (domain.AccountRating, error) {
	if s == nil || s.db == nil {
		return domain.AccountRating{}, fmt.Errorf("account rating store is not configured")
	}
	if userID <= 0 {
		return domain.AccountRating{}, domain.ErrAccountRatingNotFound
	}
	rating, err := scanAccountRating(s.db.QueryRow(ctx, `
SELECT `+accountRatingColumns+` FROM account_rating WHERE user_id = $1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AccountRating{}, domain.ErrAccountRatingNotFound
	}
	if err != nil {
		return domain.AccountRating{}, fmt.Errorf("get account rating: %w", err)
	}
	return rating, nil
}

// AccountRatingBatch resolves several users in one round trip.
func (s *AccountRatingStore) AccountRatingBatch(ctx context.Context, userIDs []int64) (map[int64]domain.AccountRating, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("account rating store is not configured")
	}
	out := make(map[int64]domain.AccountRating, len(userIDs))
	filtered := make([]int64, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID > 0 {
			filtered = append(filtered, userID)
		}
	}
	if len(filtered) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT `+accountRatingColumns+` FROM account_rating WHERE user_id = ANY($1::bigint[])`, filtered)
	if err != nil {
		return nil, fmt.Errorf("list account ratings batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		rating, err := scanAccountRating(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account rating batch: %w", err)
		}
		out[rating.UserID] = rating
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account ratings batch: %w", err)
	}
	return out, nil
}

// SaveAccountRating upserts the projection under optimistic concurrency: the
// caller submits the version it intends to write (prev.Version + 1, which is what
// domain.ResolveAccountRatingPending produces), and the update only lands when the
// stored row is still one version behind. A stale write reports changed=false and
// returns the row that won, so the caller can recompute instead of retrying blind.
func (s *AccountRatingStore) SaveAccountRating(ctx context.Context, rating domain.AccountRating) (domain.AccountRating, bool, error) {
	if s == nil || s.db == nil {
		return domain.AccountRating{}, false, fmt.Errorf("account rating store is not configured")
	}
	if rating.UserID <= 0 || rating.Level < 0 || rating.Level > domain.MaxAccountRatingLevel ||
		rating.CurrentLevelStars < 0 || rating.StarsComponent < 0 ||
		rating.ActivityComponent < 0 || rating.PenaltyComponent < 0 {
		return domain.AccountRating{}, false, domain.ErrAccountRatingAdjustmentInvalid
	}
	if rating.Version <= 0 {
		rating.Version = 1
	}
	now := time.Now().UTC()
	if rating.ComputedAt.IsZero() {
		rating.ComputedAt = now
	}
	if rating.UpdatedAt.IsZero() {
		rating.UpdatedAt = now
	}
	// The schema pairs pending_stars with pending_date; a half-filled pending
	// record is normalised away rather than rejected by the CHECK at runtime.
	if rating.PendingStars == 0 || rating.PendingDate.IsZero() {
		rating.PendingStars = 0
		rating.PendingDate = time.Time{}
	}
	var nextLevelStars any
	if rating.HasNextLevel && rating.NextLevelStars > rating.CurrentLevelStars {
		nextLevelStars = rating.NextLevelStars
	}
	var pendingDate any
	if rating.PendingStars != 0 {
		pendingDate = rating.PendingDate.UTC()
	}
	stored, err := scanAccountRating(s.db.QueryRow(ctx, `
INSERT INTO account_rating (
  user_id, level, stars, current_level_stars, next_level_stars,
  stars_component, activity_component, penalty_component, manual_component,
  pending_stars, pending_date, computed_at, updated_at, version
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (user_id) DO UPDATE SET
  level = EXCLUDED.level,
  stars = EXCLUDED.stars,
  current_level_stars = EXCLUDED.current_level_stars,
  next_level_stars = EXCLUDED.next_level_stars,
  stars_component = EXCLUDED.stars_component,
  activity_component = EXCLUDED.activity_component,
  penalty_component = EXCLUDED.penalty_component,
  manual_component = EXCLUDED.manual_component,
  pending_stars = EXCLUDED.pending_stars,
  pending_date = EXCLUDED.pending_date,
  computed_at = EXCLUDED.computed_at,
  updated_at = EXCLUDED.updated_at,
  version = EXCLUDED.version
WHERE account_rating.version = EXCLUDED.version - 1
RETURNING `+accountRatingColumns,
		rating.UserID, rating.Level, rating.Stars, rating.CurrentLevelStars, nextLevelStars,
		rating.StarsComponent, rating.ActivityComponent, rating.PenaltyComponent, rating.ManualComponent,
		rating.PendingStars, pendingDate, rating.ComputedAt.UTC(), rating.UpdatedAt.UTC(), rating.Version,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		// The guard rejected the write; report the row that is actually stored.
		current, getErr := s.AccountRating(ctx, rating.UserID)
		if getErr != nil {
			return domain.AccountRating{}, false, getErr
		}
		return current, false, nil
	}
	if err != nil {
		return domain.AccountRating{}, false, fmt.Errorf("save account rating: %w", err)
	}
	return stored, true, nil
}

// AccountRatingSignals gathers the raw contribution snapshot for one user.
//
// Sources, and why each one:
//
//	stars received / spent  stars_transactions, split by the sign of amount: that
//	                        ledger is the single authoritative record of Stars
//	                        movement for a user, and stars_transactions_user_id_idx
//	                        (user_id, id DESC) bounds the scan to the user's rows.
//	gifts received          peer_star_gifts for the user peer, restricted to
//	                        lifecycle_status = 'active' -- the weight rewards gifts
//	                        actually held, not ones converted, burned or exported
//	                        away. peer_star_gifts_owner_profile_order_idx is the
//	                        partial index on exactly that predicate and leads with
//	                        (owner_peer_type, owner_peer_id).
//	moderation cases        moderation_cases against this user peer, restricted to
//	                        the statuses that follow a *violation* decision:
//	                        'action_pending' (violation decided, actions running),
//	                        'action_failed' (violation decided, action delivery
//	                        broke) and 'resolved' (violation decided and actions
//	                        applied). 'dismissed' covers both no_violation and a
//	                        granted appeal, and 'open'/'in_review'/'appeal_review'
//	                        are undecided, so none of them penalise the account.
//	scam / fake             users.scam / users.fake, the peer flags 0136 added.
//	account age             users.created_at, floored to whole days.
//	messages sent           message_boxes, counted through
//	                        message_boxes_private_sender_live_idx
//	                        (message_sender_id, private_message_id) WHERE NOT
//	                        deleted. This is the cheapest trustworthy source: the
//	                        index leads with the sender and is coverable, so the
//	                        count needs no heap access. Every private message
//	                        materialises one box per participant, hence
//	                        count(DISTINCT private_message_id) rather than
//	                        count(*). Channel posts are deliberately excluded --
//	                        channel_messages has no sender-leading index, so
//	                        attributing them would cost a full table scan.
//	manual                  sum of account_rating_events.amount where
//	                        kind = 'manual', which is the part of the score that
//	                        must survive a full recompute.
func (s *AccountRatingStore) AccountRatingSignals(ctx context.Context, userID int64) (domain.AccountRatingSignals, error) {
	if s == nil || s.db == nil {
		return domain.AccountRatingSignals{}, fmt.Errorf("account rating store is not configured")
	}
	if userID <= 0 {
		return domain.AccountRatingSignals{}, domain.ErrUserNotFound
	}
	signals := domain.AccountRatingSignals{UserID: userID}
	err := s.db.QueryRow(ctx, `
SELECT
  COALESCE((SELECT sum(amount) FROM stars_transactions WHERE user_id = u.id AND amount > 0), 0),
  COALESCE((SELECT -sum(amount) FROM stars_transactions WHERE user_id = u.id AND amount < 0), 0),
  COALESCE((
    SELECT count(DISTINCT private_message_id) FROM message_boxes
    WHERE message_sender_id = u.id AND NOT deleted
  ), 0),
  GREATEST(0, FLOOR(EXTRACT(EPOCH FROM ($2::timestamptz - u.created_at)) / 86400))::bigint,
  COALESCE((
    SELECT count(*) FROM peer_star_gifts
    WHERE owner_peer_type = 'user' AND owner_peer_id = u.id AND lifecycle_status = 'active'
  ), 0),
  COALESCE((
    SELECT count(*) FROM moderation_cases
    WHERE target_peer_type = 'user' AND target_peer_id = u.id
      AND status IN ('action_pending', 'action_failed', 'resolved')
  ), 0),
  u.scam,
  u.fake,
  COALESCE((
    SELECT sum(amount) FROM account_rating_events
    WHERE user_id = u.id AND kind = 'manual'
  ), 0)
FROM users u
WHERE u.id = $1`, userID, time.Now().UTC()).Scan(
		&signals.StarsReceived, &signals.StarsSpent, &signals.MessagesSent,
		&signals.AccountAgeDays, &signals.GiftsReceived, &signals.ModerationCases,
		&signals.Scam, &signals.Fake, &signals.Manual,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AccountRatingSignals{}, domain.ErrUserNotFound
	}
	if err != nil {
		return domain.AccountRatingSignals{}, fmt.Errorf("gather account rating signals: %w", err)
	}
	return signals, nil
}

// AdjustAccountRating appends a manual adjustment to the ledger. It does not
// recompute the projection: the caller pairs it with SaveAccountRating so the new
// manual total is folded in through the same formula as every other signal.
// Replaying the same CommandKey returns the recorded event and applied=false.
func (s *AccountRatingStore) AdjustAccountRating(ctx context.Context, req domain.AdjustAccountRatingRequest) (domain.AccountRatingEvent, bool, error) {
	if s == nil || s.db == nil {
		return domain.AccountRatingEvent{}, false, fmt.Errorf("account rating store is not configured")
	}
	req.Reason = strings.TrimSpace(req.Reason)
	req.Actor = strings.TrimSpace(req.Actor)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if err := req.Validate(); err != nil {
		return domain.AccountRatingEvent{}, false, err
	}
	var event domain.AccountRatingEvent
	applied := false
	err := withTx(ctx, s.db, "adjust account rating", func(tx pgx.Tx) error {
		if existing, found, err := accountRatingEventByCommandKey(ctx, tx, req.CommandKey); err != nil {
			return err
		} else if found {
			event = existing
			return nil
		}
		event = domain.AccountRatingEvent{
			UserID:     req.UserID,
			Kind:       domain.AccountRatingEventManual,
			Amount:     req.Amount,
			Reason:     req.Reason,
			Actor:      req.Actor,
			CommandKey: req.CommandKey,
			CreatedAt:  time.Now().UTC(),
		}
		// DO NOTHING on the partial command_key index closes the window between the
		// replay lookup and the insert: a concurrent retry of the same command
		// records nothing and falls back to reading the row that won.
		err := tx.QueryRow(ctx, `
INSERT INTO account_rating_events (user_id, kind, amount, reason, actor, command_key, created_at)
VALUES ($1,'manual',$2,$3,$4,NULLIF($5,''),$6)
ON CONFLICT (command_key) WHERE command_key IS NOT NULL DO NOTHING
RETURNING id`, event.UserID, event.Amount, event.Reason, event.Actor, event.CommandKey, event.CreatedAt).
			Scan(&event.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			existing, found, lookupErr := accountRatingEventByCommandKey(ctx, tx, req.CommandKey)
			if lookupErr != nil {
				return lookupErr
			}
			if !found {
				return fmt.Errorf("insert account rating adjustment: conflicting command %q vanished", req.CommandKey)
			}
			event = existing
			return nil
		}
		if err != nil {
			return fmt.Errorf("insert account rating adjustment: %w", err)
		}
		applied = true
		return nil
	})
	if err != nil {
		return domain.AccountRatingEvent{}, false, err
	}
	return event, applied, nil
}

// ListAccountRatings is the admin leaderboard query. The order matches
// account_rating_leaderboard_idx (level DESC, stars DESC, user_id) and BeforeID is
// a keyset cursor: the cursor row's own (level, stars) are read back so paging
// stays consistent across the compound order instead of only over user ids.
func (s *AccountRatingStore) ListAccountRatings(ctx context.Context, filter domain.AccountRatingFilter) ([]domain.AccountRating, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("account rating store is not configured")
	}
	if filter.MinLevel < 0 || filter.MinLevel > domain.MaxAccountRatingLevel {
		return nil, domain.ErrAccountRatingAdjustmentInvalid
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultAccountRatingListLimit
	}
	if limit > maxAccountRatingListLimit {
		limit = maxAccountRatingListLimit
	}
	rows, err := s.db.Query(ctx, `
WITH cursor_row AS (
  SELECT level AS c_level, stars AS c_stars, user_id AS c_user_id
  FROM account_rating WHERE $3 <> 0 AND user_id = $3
)
SELECT `+accountRatingColumnsQualified+`
FROM account_rating r
LEFT JOIN cursor_row c ON true
WHERE r.level >= $1
  AND ($2 = 0 OR r.user_id = $2)
  AND (
    c.c_user_id IS NULL
    OR r.level < c.c_level
    OR (r.level = c.c_level AND r.stars < c.c_stars)
    OR (r.level = c.c_level AND r.stars = c.c_stars AND r.user_id > c.c_user_id)
  )
ORDER BY r.level DESC, r.stars DESC, r.user_id
LIMIT $4`, filter.MinLevel, filter.UserID, filter.BeforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list account ratings: %w", err)
	}
	defer rows.Close()
	out := make([]domain.AccountRating, 0, limit)
	for rows.Next() {
		rating, err := scanAccountRating(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account rating: %w", err)
		}
		out = append(out, rating)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account ratings: %w", err)
	}
	return out, nil
}

// AccountRatingEvents returns the ledger for one user, newest first, over
// account_rating_events_user_idx (user_id, id DESC).
func (s *AccountRatingStore) AccountRatingEvents(ctx context.Context, userID int64, limit int) ([]domain.AccountRatingEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("account rating store is not configured")
	}
	if userID <= 0 {
		return nil, domain.ErrAccountRatingNotFound
	}
	if limit <= 0 {
		limit = defaultAccountRatingListLimit
	}
	if limit > maxAccountRatingListLimit {
		limit = maxAccountRatingListLimit
	}
	rows, err := s.db.Query(ctx, `
SELECT id, user_id, kind, amount, reason, actor, COALESCE(command_key, ''), created_at
FROM account_rating_events
WHERE user_id = $1
ORDER BY id DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list account rating events: %w", err)
	}
	defer rows.Close()
	out := make([]domain.AccountRatingEvent, 0, limit)
	for rows.Next() {
		event, err := scanAccountRatingEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account rating event: %w", err)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account rating events: %w", err)
	}
	return out, nil
}

// StaleAccountRatings returns the users whose projection is older than the
// horizon, ordered so the walk follows account_rating_stale_idx
// (computed_at, user_id) exactly.
func (s *AccountRatingStore) StaleAccountRatings(ctx context.Context, olderThanUnix int64, limit int) ([]int64, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("account rating store is not configured")
	}
	if olderThanUnix <= 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultAccountRatingListLimit
	}
	if limit > maxAccountRatingListLimit {
		limit = maxAccountRatingListLimit
	}
	rows, err := s.db.Query(ctx, `
SELECT user_id FROM account_rating
WHERE computed_at < to_timestamp($1)
ORDER BY computed_at, user_id
LIMIT $2`, olderThanUnix, limit)
	if err != nil {
		return nil, fmt.Errorf("list stale account ratings: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0, limit)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan stale account rating: %w", err)
		}
		out = append(out, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale account ratings: %w", err)
	}
	return out, nil
}

func accountRatingEventByCommandKey(ctx context.Context, db sqlcgen.DBTX, commandKey string) (domain.AccountRatingEvent, bool, error) {
	if commandKey == "" {
		return domain.AccountRatingEvent{}, false, nil
	}
	event, err := scanAccountRatingEvent(db.QueryRow(ctx, `
SELECT id, user_id, kind, amount, reason, actor, COALESCE(command_key, ''), created_at
FROM account_rating_events WHERE command_key = $1`, commandKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AccountRatingEvent{}, false, nil
	}
	if err != nil {
		return domain.AccountRatingEvent{}, false, fmt.Errorf("lookup account rating command: %w", err)
	}
	return event, true, nil
}

func scanAccountRating(row pgx.Row) (domain.AccountRating, error) {
	var rating domain.AccountRating
	var nextLevelStars pgtype.Int8
	var pendingDate pgtype.Timestamptz
	if err := row.Scan(&rating.UserID, &rating.Level, &rating.Stars, &rating.CurrentLevelStars,
		&nextLevelStars, &rating.StarsComponent, &rating.ActivityComponent,
		&rating.PenaltyComponent, &rating.ManualComponent, &rating.PendingStars,
		&pendingDate, &rating.ComputedAt, &rating.UpdatedAt, &rating.Version); err != nil {
		return domain.AccountRating{}, err
	}
	if nextLevelStars.Valid {
		rating.NextLevelStars = nextLevelStars.Int64
		rating.HasNextLevel = true
	}
	if pendingDate.Valid {
		rating.PendingDate = pendingDate.Time.UTC()
	}
	rating.ComputedAt = rating.ComputedAt.UTC()
	rating.UpdatedAt = rating.UpdatedAt.UTC()
	return rating, nil
}

func scanAccountRatingEvent(row pgx.Row) (domain.AccountRatingEvent, error) {
	var event domain.AccountRatingEvent
	var kind string
	if err := row.Scan(&event.ID, &event.UserID, &kind, &event.Amount, &event.Reason,
		&event.Actor, &event.CommandKey, &event.CreatedAt); err != nil {
		return domain.AccountRatingEvent{}, err
	}
	event.Kind = domain.AccountRatingEventKind(kind)
	event.CreatedAt = event.CreatedAt.UTC()
	return event, nil
}
