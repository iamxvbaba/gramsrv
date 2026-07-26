// Package rating implements the composite account rating use cases: reading the
// stored projection, recomputing it from the raw contribution signals, and
// applying operator adjustments through the contribution ledger.
//
// The protocol surface (userFull.stars_rating / stars_my_pending_rating) is a
// pure projection of domain.AccountRating, so all policy lives in the domain:
// this service only gathers signals, applies the configured weights and the
// pending-delay policy, and persists the result under optimistic concurrency.
package rating

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	// defaultPendingDelay parks a rating increase for a day, matching the
	// shipped TELESRV_RATING_PENDING_DELAY default.
	defaultPendingDelay = 24 * time.Hour
	// defaultStaleAfter is the recompute horizon used when none is configured.
	defaultStaleAfter = 6 * time.Hour
	// defaultListLimit / maxListLimit bound one leaderboard page.
	defaultListLimit = 50
	maxListLimit     = 200
	// defaultEventLimit / maxEventLimit bound one ledger page.
	defaultEventLimit = 50
	maxEventLimit     = 200
	// defaultRecomputeBatch / maxRecomputeBatch bound one worker cycle.
	defaultRecomputeBatch = 500
	maxRecomputeBatch     = 10000
)

// ErrDisabled reports that the composite rating feature is switched off. Read
// paths degrade to an empty projection instead, because a client that asks for a
// userFull must still get an answer; only writes are refused, so an operator
// never believes an adjustment was recorded when it was not.
var ErrDisabled = errors.New("account rating is disabled")

// Service is the composite account rating use-case layer.
type Service struct {
	store        store.AccountRatingStore
	weights      domain.AccountRatingWeights
	pendingDelay time.Duration
	staleAfter   time.Duration
	enabled      bool

	now func() time.Time
	log *zap.Logger
}

// Option adjusts optional service dependencies.
type Option func(*Service)

// WithStore injects the rating read model and ledger store.
func WithStore(st store.AccountRatingStore) Option {
	return func(s *Service) { s.store = st }
}

// WithWeights installs the composite formula. An invalid set is rejected in
// favour of the shipped defaults, so a misconfigured deployment produces a
// conservative rating instead of an inconsistent one.
func WithWeights(weights domain.AccountRatingWeights) Option {
	return func(s *Service) {
		if err := weights.Validate(); err != nil {
			return
		}
		s.weights = weights
	}
}

// WithPendingDelay configures how long a rating increase stays parked as
// pending. Zero applies every change immediately.
func WithPendingDelay(delay time.Duration) Option {
	return func(s *Service) {
		if delay >= 0 {
			s.pendingDelay = delay
		}
	}
}

// WithStaleAfter configures the projection age after which the background
// worker recomputes a user.
func WithStaleAfter(staleAfter time.Duration) Option {
	return func(s *Service) {
		if staleAfter > 0 {
			s.staleAfter = staleAfter
		}
	}
}

// WithEnabled toggles the feature.
func WithEnabled(enabled bool) Option {
	return func(s *Service) { s.enabled = enabled }
}

// WithClock injects the clock (tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithLogger injects the service logger.
func WithLogger(log *zap.Logger) Option {
	return func(s *Service) {
		if log != nil {
			s.log = log
		}
	}
}

// NewService creates the rating service. It is enabled by default so that the
// only switch is the configuration flag, and it stays safe without a store:
// reads answer empty and writes report a configuration error.
func NewService(opts ...Option) *Service {
	s := &Service{
		weights:      domain.DefaultAccountRatingWeights(),
		pendingDelay: defaultPendingDelay,
		staleAfter:   defaultStaleAfter,
		enabled:      true,
		now:          time.Now,
		log:          zap.NewNop(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = zap.NewNop()
	}
	if s.pendingDelay < 0 {
		s.pendingDelay = 0
	}
	if s.staleAfter <= 0 {
		s.staleAfter = defaultStaleAfter
	}
	if err := s.weights.Validate(); err != nil {
		s.weights = domain.DefaultAccountRatingWeights()
	}
	return s
}

// Enabled reports whether the feature is switched on.
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// Ready reports whether the feature is on and backed by a store.
func (s *Service) Ready() bool { return s.Enabled() && s.store != nil }

// Weights returns the configured composite formula, so the admin panel can
// explain a level with the same numbers that produced it.
func (s *Service) Weights() domain.AccountRatingWeights {
	if s == nil {
		return domain.DefaultAccountRatingWeights()
	}
	return s.weights
}

func (s *Service) ratingStore() (store.AccountRatingStore, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("account rating store is not configured")
	}
	return s.store, nil
}

// Rating returns the stored projection.
//
// domain.ErrAccountRatingNotFound is propagated rather than flattened to a zero
// value: the userFull projection distinguishes "no rating to show" (both TL flags
// stay unset, the pre-rating wire shape) from "rating of zero", and a disabled
// feature is the former. A missing store reports a configuration error, which
// read paths treat the same way but an operator can diagnose.
func (s *Service) Rating(ctx context.Context, userID int64) (domain.AccountRating, error) {
	if s == nil || !s.enabled || userID <= 0 {
		return domain.AccountRating{}, domain.ErrAccountRatingNotFound
	}
	st, err := s.ratingStore()
	if err != nil {
		return domain.AccountRating{}, err
	}
	return st.AccountRating(ctx, userID)
}

// RatingBatch resolves several users in one round trip. Users without a stored
// projection are absent from the map, so a disabled feature and an unconfigured
// store both read as "nobody has a rating" -- the batch shape already encodes
// absence and needs no error to express it.
func (s *Service) RatingBatch(ctx context.Context, userIDs []int64) (map[int64]domain.AccountRating, error) {
	if s == nil || !s.enabled || s.store == nil {
		return map[int64]domain.AccountRating{}, nil
	}
	unique := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		unique = append(unique, userID)
	}
	if len(unique) == 0 {
		return map[int64]domain.AccountRating{}, nil
	}
	batch, err := s.store.AccountRatingBatch(ctx, unique)
	if err != nil {
		return nil, err
	}
	if batch == nil {
		return map[int64]domain.AccountRating{}, nil
	}
	return batch, nil
}

// Recompute gathers the contribution signals, applies the configured weights
// and the pending-delay policy relative to the stored value, and persists the
// result.
//
// The save is guarded by the stored version. A concurrent writer (another
// recompute, an adjustment, the worker) only invalidates the base the pending
// policy was resolved against, so exactly one retry against the freshly
// returned row is both sufficient and terminating.
func (s *Service) Recompute(ctx context.Context, userID int64) (domain.AccountRating, error) {
	if s == nil || !s.enabled {
		return domain.AccountRating{}, ErrDisabled
	}
	st, err := s.ratingStore()
	if err != nil {
		return domain.AccountRating{}, err
	}
	if userID <= 0 {
		return domain.AccountRating{}, domain.ErrAccountRatingAdjustmentInvalid
	}
	signals, err := st.AccountRatingSignals(ctx, userID)
	if err != nil {
		return domain.AccountRating{}, err
	}
	signals.UserID = userID
	prev, err := s.previous(ctx, st, userID)
	if err != nil {
		return domain.AccountRating{}, err
	}
	now := s.now().UTC()
	computed := domain.ComputeAccountRating(signals, s.weights, now)
	stored, changed, err := st.SaveAccountRating(ctx, domain.ResolveAccountRatingPending(prev, computed, s.pendingDelay, now))
	if err != nil {
		return domain.AccountRating{}, err
	}
	if changed {
		return stored, nil
	}
	// One retry: `stored` is the row that won the race, so resolving the pending
	// policy against it produces the correct next version.
	stored, changed, err = st.SaveAccountRating(ctx, domain.ResolveAccountRatingPending(stored, computed, s.pendingDelay, now))
	if err != nil {
		return domain.AccountRating{}, err
	}
	if !changed {
		return stored, fmt.Errorf("recompute account rating %d: concurrent version conflict", userID)
	}
	return stored, nil
}

// Adjust records an operator adjustment in the contribution ledger and
// immediately recomputes the projection, so the manual component is visible
// without waiting for the background worker. Replaying the same CommandKey
// records nothing and reports applied=false; the current rating is still
// returned so a retried admin command stays idempotent.
func (s *Service) Adjust(ctx context.Context, req domain.AdjustAccountRatingRequest) (domain.AccountRating, bool, error) {
	if s == nil || !s.enabled {
		return domain.AccountRating{}, false, ErrDisabled
	}
	st, err := s.ratingStore()
	if err != nil {
		return domain.AccountRating{}, false, err
	}
	if err := req.Validate(); err != nil {
		return domain.AccountRating{}, false, err
	}
	_, applied, err := st.AdjustAccountRating(ctx, req)
	if err != nil {
		return domain.AccountRating{}, false, err
	}
	rating, err := s.Recompute(ctx, req.UserID)
	if err != nil {
		return domain.AccountRating{}, applied, err
	}
	return rating, applied, nil
}

// List is the admin leaderboard query with a bounded page size.
func (s *Service) List(ctx context.Context, filter domain.AccountRatingFilter) ([]domain.AccountRating, error) {
	if s == nil || !s.enabled {
		return nil, nil
	}
	st, err := s.ratingStore()
	if err != nil {
		return nil, err
	}
	if filter.MinLevel < 0 {
		filter.MinLevel = 0
	}
	if filter.MinLevel > domain.MaxAccountRatingLevel {
		filter.MinLevel = domain.MaxAccountRatingLevel
	}
	filter.Limit = clampLimit(filter.Limit, defaultListLimit, maxListLimit)
	return st.ListAccountRatings(ctx, filter)
}

// Events returns one user's contribution ledger, newest first.
func (s *Service) Events(ctx context.Context, userID int64, limit int) ([]domain.AccountRatingEvent, error) {
	if s == nil || !s.enabled {
		return nil, nil
	}
	st, err := s.ratingStore()
	if err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, domain.ErrAccountRatingAdjustmentInvalid
	}
	return st.AccountRatingEvents(ctx, userID, clampLimit(limit, defaultEventLimit, maxEventLimit))
}

// RunRecomputeCycle recomputes one bounded batch of stale projections and
// returns how many users were recomputed. A single user's failure is logged and
// skipped: one poisoned row must not stall the whole cycle.
func (s *Service) RunRecomputeCycle(ctx context.Context, limit int) (int, error) {
	if s == nil || !s.enabled {
		return 0, nil
	}
	st, err := s.ratingStore()
	if err != nil {
		return 0, err
	}
	limit = clampLimit(limit, defaultRecomputeBatch, maxRecomputeBatch)
	olderThan := s.now().UTC().Add(-s.staleAfter).Unix()
	userIDs, err := st.StaleAccountRatings(ctx, olderThan, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, userID := range userIDs {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		if userID <= 0 {
			continue
		}
		if _, err := s.Recompute(ctx, userID); err != nil {
			s.log.Warn("recompute account rating failed",
				zap.Int64("user_id", userID),
				zap.Error(err))
			continue
		}
		processed++
	}
	return processed, nil
}

// previous reads the stored projection the pending policy is resolved against.
// A never-computed user yields the zero value, which domain.ResolveAccountRating
// Pending treats as "apply immediately" -- a first rating is never parked.
func (s *Service) previous(ctx context.Context, st store.AccountRatingStore, userID int64) (domain.AccountRating, error) {
	prev, err := st.AccountRating(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrAccountRatingNotFound) {
			return domain.AccountRating{}, nil
		}
		return domain.AccountRating{}, err
	}
	return prev, nil
}

func clampLimit(limit, fallback, maximum int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > maximum {
		return maximum
	}
	return limit
}
