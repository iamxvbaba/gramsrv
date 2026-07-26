package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// Default page sizes for the rating reads, so an unset limit resolves to a
// finite page the way the PostgreSQL LIMIT does.
const (
	defaultAccountRatingListLimit  = 50
	defaultAccountRatingEventLimit = 50
	defaultAccountRatingStaleLimit = 50
)

// AccountRatingStore is the in-memory implementation of store.AccountRatingStore.
// It reproduces the invariants migration 0151 encodes:
//
//   - account_rating is keyed by user_id, so one projection row per user.
//   - the version CHECK plus optimistic concurrency: a write is applied only when
//     it carries the successor of the stored version, which is exactly what
//     domain.ResolveAccountRatingPending produces.
//   - the pending pair CHECK: a pending delta and its date exist together or not
//     at all.
//   - the component CHECKs: stars/activity/penalty components and level are
//     non-negative, and next_level_stars is either absent or above
//     current_level_stars.
//   - account_rating_events_command_idx: a replayed command key never appends a
//     second adjustment.
type AccountRatingStore struct {
	mu     sync.Mutex
	nextID int64
	// ratings is the account_rating read model.
	ratings map[int64]domain.AccountRating
	// events is the append-only contribution ledger in insertion order.
	events []domain.AccountRatingEvent
	// commands maps an adjustment command key onto the ledger row it created.
	commands map[string]int64
	// signals holds the raw contribution snapshot per user.
	//
	// PostgreSQL aggregates it from stars_transactions, message counts, saved
	// gifts and moderation cases. In memory those live in unrelated store types
	// (StarsStore, MessageStore, StarGiftStore, ModerationReportStore) that this
	// store has no handle on, and wiring them in would make the rating depend on
	// which stores a test happens to construct. The snapshot is therefore
	// injected -- deterministic, and identical for a unit test and for the
	// recompute worker, which is what domain.AccountRatingSignals promises. Only
	// the manual total is derived here, from the ledger, because the ledger is
	// this store's own data.
	signals map[int64]domain.AccountRatingSignals
}

// NewAccountRatingStore creates an empty rating store.
func NewAccountRatingStore() *AccountRatingStore {
	return &AccountRatingStore{
		nextID:   1,
		ratings:  make(map[int64]domain.AccountRating),
		commands: make(map[string]int64),
		signals:  make(map[int64]domain.AccountRatingSignals),
	}
}

// SeedAccountRatingSignals installs raw contribution snapshots for tests in other
// packages; see the signals field for why they are injected rather than derived.
func (s *AccountRatingStore) SeedAccountRatingSignals(signals ...domain.AccountRatingSignals) {
	for _, item := range signals {
		s.setAccountRatingSignals(item)
	}
}

// setAccountRatingSignals is the same hook for this package's tests.
func (s *AccountRatingStore) setAccountRatingSignals(signals domain.AccountRatingSignals) {
	if signals.UserID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals[signals.UserID] = signals
}

// AccountRating returns the stored projection, or domain.ErrAccountRatingNotFound
// when the user was never computed.
func (s *AccountRatingStore) AccountRating(_ context.Context, userID int64) (domain.AccountRating, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rating, ok := s.ratings[userID]
	if !ok {
		return domain.AccountRating{}, domain.ErrAccountRatingNotFound
	}
	return rating, nil
}

// AccountRatingBatch resolves several users at once; users without a row are
// absent from the map.
func (s *AccountRatingStore) AccountRatingBatch(_ context.Context, userIDs []int64) (map[int64]domain.AccountRating, error) {
	out := make(map[int64]domain.AccountRating, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, userID := range userIDs {
		if userID <= 0 {
			continue
		}
		if rating, ok := s.ratings[userID]; ok {
			out[userID] = rating
		}
	}
	return out, nil
}

// SaveAccountRating upserts the projection under optimistic concurrency: the
// incoming Version must be the successor of the stored one, which is what
// domain.ResolveAccountRatingPending computes. A stale or missing version leaves
// the stored row untouched and reports changed=false, so a caller that lost a
// race can re-read and retry.
func (s *AccountRatingStore) SaveAccountRating(_ context.Context, rating domain.AccountRating) (domain.AccountRating, bool, error) {
	if rating.UserID <= 0 {
		// account_rating.user_id references users(id): there is no row to write.
		return domain.AccountRating{}, false, domain.ErrAccountRatingNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.ratings[rating.UserID]
	if rating.Version != stored.Version+1 {
		if !exists {
			return domain.AccountRating{}, false, nil
		}
		return stored, false, nil
	}
	next := normalizeAccountRating(rating)
	s.ratings[next.UserID] = next
	return next, true, nil
}

// AccountRatingSignals returns the injected raw snapshot with the manual total
// taken from the ledger, mirroring the PostgreSQL aggregate. A user with no
// contributions reports zeros rather than an error, because the aggregate has no
// "missing row" state.
func (s *AccountRatingStore) AccountRatingSignals(_ context.Context, userID int64) (domain.AccountRatingSignals, error) {
	if userID <= 0 {
		return domain.AccountRatingSignals{}, domain.ErrAccountRatingNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	signals := s.signals[userID]
	signals.UserID = userID
	signals.Manual += s.manualTotalLocked(userID)
	return signals, nil
}

// AdjustAccountRating appends a manual adjustment. A replayed command key returns
// the recorded event with applied=false and appends nothing.
func (s *AccountRatingStore) AdjustAccountRating(_ context.Context, req domain.AdjustAccountRatingRequest) (domain.AccountRatingEvent, bool, error) {
	if err := req.Validate(); err != nil {
		return domain.AccountRatingEvent{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.CommandKey != "" {
		if id, ok := s.commands[req.CommandKey]; ok {
			for _, event := range s.events {
				if event.ID == id {
					return event, false, nil
				}
			}
		}
	}
	event := domain.AccountRatingEvent{
		ID:         s.nextID,
		UserID:     req.UserID,
		Kind:       domain.AccountRatingEventManual,
		Amount:     req.Amount,
		Reason:     req.Reason,
		Actor:      req.Actor,
		CommandKey: req.CommandKey,
		CreatedAt:  time.Now().UTC(),
	}
	s.nextID++
	s.events = append(s.events, event)
	if event.CommandKey != "" {
		s.commands[event.CommandKey] = event.ID
	}
	return event, true, nil
}

// ListAccountRatings is the admin leaderboard: level desc, stars desc, user id
// asc, matching account_rating_leaderboard_idx. BeforeID is the keyset cursor and
// names the last row of the previous page.
func (s *AccountRatingStore) ListAccountRatings(_ context.Context, filter domain.AccountRatingFilter) ([]domain.AccountRating, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultAccountRatingListLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor, hasCursor := s.ratings[filter.BeforeID]
	out := make([]domain.AccountRating, 0, len(s.ratings))
	for _, rating := range s.ratings {
		if filter.MinLevel > 0 && rating.Level < filter.MinLevel {
			continue
		}
		if filter.UserID > 0 && rating.UserID != filter.UserID {
			continue
		}
		switch {
		case filter.BeforeID <= 0:
		case hasCursor:
			// Keyset paging over the leaderboard order.
			if !accountRatingLess(cursor, rating) {
				continue
			}
		default:
			// The cursor row is gone; fall back to the id tiebreak alone so paging
			// still terminates.
			if rating.UserID <= filter.BeforeID {
				continue
			}
		}
		out = append(out, rating)
	}
	sort.Slice(out, func(i, j int) bool { return accountRatingLess(out[i], out[j]) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// AccountRatingEvents returns the ledger for one user, newest first.
func (s *AccountRatingStore) AccountRatingEvents(_ context.Context, userID int64, limit int) ([]domain.AccountRatingEvent, error) {
	if limit <= 0 {
		limit = defaultAccountRatingEventLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.AccountRatingEvent, 0, limit)
	for i := len(s.events) - 1; i >= 0 && len(out) < limit; i-- {
		if s.events[i].UserID != userID {
			continue
		}
		out = append(out, s.events[i])
	}
	return out, nil
}

// StaleAccountRatings returns the users whose projection predates the horizon,
// oldest first, which is the order account_rating_stale_idx serves.
func (s *AccountRatingStore) StaleAccountRatings(_ context.Context, olderThanUnix int64, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = defaultAccountRatingStaleLimit
	}
	horizon := time.Unix(olderThanUnix, 0).UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	stale := make([]domain.AccountRating, 0, len(s.ratings))
	for _, rating := range s.ratings {
		if rating.ComputedAt.Before(horizon) {
			stale = append(stale, rating)
		}
	}
	sort.Slice(stale, func(i, j int) bool {
		if !stale[i].ComputedAt.Equal(stale[j].ComputedAt) {
			return stale[i].ComputedAt.Before(stale[j].ComputedAt)
		}
		return stale[i].UserID < stale[j].UserID
	})
	if len(stale) > limit {
		stale = stale[:limit]
	}
	out := make([]int64, 0, len(stale))
	for _, rating := range stale {
		out = append(out, rating.UserID)
	}
	return out, nil
}

// manualTotalLocked sums the manual ledger rows, the only kind that survives a
// recompute.
func (s *AccountRatingStore) manualTotalLocked(userID int64) int64 {
	var total int64
	for _, event := range s.events {
		if event.UserID == userID && event.Kind == domain.AccountRatingEventManual {
			total += event.Amount
		}
	}
	return total
}

// normalizeAccountRating makes the rows the table's CHECK constraints forbid
// unrepresentable. PostgreSQL raises an opaque constraint error there rather than
// a domain error, so the memory store folds the impossible shapes onto the
// closest representable one instead of inventing an error the RPC layer would
// then have to handle only in tests.
func normalizeAccountRating(rating domain.AccountRating) domain.AccountRating {
	if rating.Level < 0 {
		rating.Level = 0
	}
	if rating.Level > domain.MaxAccountRatingLevel {
		rating.Level = domain.MaxAccountRatingLevel
	}
	if rating.CurrentLevelStars < 0 {
		rating.CurrentLevelStars = 0
	}
	if rating.StarsComponent < 0 {
		rating.StarsComponent = 0
	}
	if rating.ActivityComponent < 0 {
		rating.ActivityComponent = 0
	}
	if rating.PenaltyComponent < 0 {
		rating.PenaltyComponent = 0
	}
	if !rating.HasNextLevel || rating.NextLevelStars <= rating.CurrentLevelStars {
		rating.HasNextLevel = false
		rating.NextLevelStars = 0
	}
	// The pending delta and its date only exist together.
	if rating.PendingStars == 0 || rating.PendingDate.IsZero() {
		rating.PendingStars = 0
		rating.PendingDate = time.Time{}
	}
	return rating
}

// accountRatingLess is the leaderboard order: highest level first, then the
// larger score, then the lower user id as a stable tiebreak.
func accountRatingLess(a, b domain.AccountRating) bool {
	if a.Level != b.Level {
		return a.Level > b.Level
	}
	if a.Stars != b.Stars {
		return a.Stars > b.Stars
	}
	return a.UserID < b.UserID
}
