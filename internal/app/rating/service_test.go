package rating

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

var testNow = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

// fakeRatingStore is an in-memory AccountRatingStore with the same optimistic
// concurrency contract as PostgreSQL: a save whose version does not follow the
// stored one reports changed=false and returns the row that won.
type fakeRatingStore struct {
	signals map[int64]domain.AccountRatingSignals
	ratings map[int64]domain.AccountRating
	manual  map[int64]int64
	events  map[int64][]domain.AccountRatingEvent
	keys    map[string]domain.AccountRatingEvent

	stale          []int64
	staleOlderThan int64
	staleLimit     int

	unrated      []int64
	unratedLimit int
	unratedCalls int
	unratedErr   error

	saves          []domain.AccountRating
	forceConflicts int
	signalsErr     error
}

func newFakeRatingStore() *fakeRatingStore {
	return &fakeRatingStore{
		signals: map[int64]domain.AccountRatingSignals{},
		ratings: map[int64]domain.AccountRating{},
		manual:  map[int64]int64{},
		events:  map[int64][]domain.AccountRatingEvent{},
		keys:    map[string]domain.AccountRatingEvent{},
	}
}

func (f *fakeRatingStore) AccountRating(_ context.Context, userID int64) (domain.AccountRating, error) {
	rating, ok := f.ratings[userID]
	if !ok {
		return domain.AccountRating{}, domain.ErrAccountRatingNotFound
	}
	return rating, nil
}

func (f *fakeRatingStore) AccountRatingBatch(_ context.Context, userIDs []int64) (map[int64]domain.AccountRating, error) {
	out := make(map[int64]domain.AccountRating, len(userIDs))
	for _, userID := range userIDs {
		if rating, ok := f.ratings[userID]; ok {
			out[userID] = rating
		}
	}
	return out, nil
}

func (f *fakeRatingStore) SaveAccountRating(_ context.Context, rating domain.AccountRating) (domain.AccountRating, bool, error) {
	f.saves = append(f.saves, rating)
	current := f.ratings[rating.UserID]
	if f.forceConflicts > 0 {
		f.forceConflicts--
		return current, false, nil
	}
	if rating.Version != current.Version+1 {
		return current, false, nil
	}
	f.ratings[rating.UserID] = rating
	return rating, true, nil
}

func (f *fakeRatingStore) AccountRatingSignals(_ context.Context, userID int64) (domain.AccountRatingSignals, error) {
	if f.signalsErr != nil {
		return domain.AccountRatingSignals{}, f.signalsErr
	}
	signals := f.signals[userID]
	signals.UserID = userID
	signals.Manual = f.manual[userID]
	return signals, nil
}

func (f *fakeRatingStore) AdjustAccountRating(_ context.Context, req domain.AdjustAccountRatingRequest) (domain.AccountRatingEvent, bool, error) {
	if req.CommandKey != "" {
		if event, ok := f.keys[req.CommandKey]; ok {
			return event, false, nil
		}
	}
	event := domain.AccountRatingEvent{
		ID: int64(len(f.events[req.UserID]) + 1), UserID: req.UserID, Kind: domain.AccountRatingEventManual,
		Amount: req.Amount, Reason: req.Reason, Actor: req.Actor, CommandKey: req.CommandKey, CreatedAt: testNow,
	}
	f.events[req.UserID] = append(f.events[req.UserID], event)
	f.manual[req.UserID] += req.Amount
	if req.CommandKey != "" {
		f.keys[req.CommandKey] = event
	}
	return event, true, nil
}

func (f *fakeRatingStore) ListAccountRatings(_ context.Context, filter domain.AccountRatingFilter) ([]domain.AccountRating, error) {
	out := make([]domain.AccountRating, 0, len(f.ratings))
	for _, rating := range f.ratings {
		if rating.Level >= filter.MinLevel {
			out = append(out, rating)
		}
		if len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func (f *fakeRatingStore) AccountRatingEvents(_ context.Context, userID int64, limit int) ([]domain.AccountRatingEvent, error) {
	events := f.events[userID]
	if len(events) > limit {
		events = events[:limit]
	}
	return append([]domain.AccountRatingEvent(nil), events...), nil
}

func (f *fakeRatingStore) StaleAccountRatings(_ context.Context, olderThanUnix int64, limit int) ([]int64, error) {
	f.staleOlderThan = olderThanUnix
	f.staleLimit = limit
	if len(f.stale) > limit {
		return append([]int64(nil), f.stale[:limit]...), nil
	}
	return append([]int64(nil), f.stale...), nil
}

func (f *fakeRatingStore) UnratedAccounts(_ context.Context, limit int) ([]int64, error) {
	f.unratedCalls++
	f.unratedLimit = limit
	if f.unratedErr != nil {
		return nil, f.unratedErr
	}
	out := make([]int64, 0, len(f.unrated))
	for _, userID := range f.unrated {
		if _, rated := f.ratings[userID]; rated {
			continue
		}
		out = append(out, userID)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func newTestService(st *fakeRatingStore, opts ...Option) *Service {
	base := []Option{WithStore(st), WithClock(func() time.Time { return testNow })}
	return NewService(append(base, opts...)...)
}

func TestRecomputeAppliesConfiguredWeights(t *testing.T) {
	st := newFakeRatingStore()
	st.signals[7] = domain.AccountRatingSignals{
		StarsReceived: 1000, StarsSpent: 400, MessagesSent: 30, AccountAgeDays: 10,
		GiftsReceived: 2, ModerationCases: 1,
	}
	service := newTestService(st)

	rating, err := service.Recompute(context.Background(), 7)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	weights := domain.DefaultAccountRatingWeights()
	want := domain.ComputeAccountRating(domain.AccountRatingSignals{
		UserID: 7, StarsReceived: 1000, StarsSpent: 400, MessagesSent: 30, AccountAgeDays: 10,
		GiftsReceived: 2, ModerationCases: 1,
	}, weights, testNow)
	if rating.Stars != want.Stars || rating.Level != want.Level ||
		rating.StarsComponent != want.StarsComponent || rating.ActivityComponent != want.ActivityComponent ||
		rating.PenaltyComponent != want.PenaltyComponent {
		t.Fatalf("rating = %#v, want the domain formula result %#v", rating, want)
	}
	if rating.Version != 1 {
		t.Fatalf("first stored version = %d, want 1", rating.Version)
	}
	if !rating.ComputedAt.Equal(testNow) {
		t.Fatalf("ComputedAt = %v, want the injected clock %v", rating.ComputedAt, testNow)
	}
}

func TestRecomputePendingPolicy(t *testing.T) {
	t.Run("increase is parked", func(t *testing.T) {
		st := newFakeRatingStore()
		st.ratings[7] = domain.AccountRating{UserID: 7, Stars: 100, Level: 1, Version: 4}
		st.signals[7] = domain.AccountRatingSignals{StarsReceived: 500}
		service := newTestService(st, WithPendingDelay(24*time.Hour))

		rating, err := service.Recompute(context.Background(), 7)
		if err != nil {
			t.Fatalf("Recompute: %v", err)
		}
		if rating.Stars != 100 {
			t.Fatalf("visible stars = %d, want the previous 100 while the increase is pending", rating.Stars)
		}
		if rating.PendingStars != 400 {
			t.Fatalf("pending stars = %d, want 400", rating.PendingStars)
		}
		if want := testNow.Add(24 * time.Hour); !rating.PendingDate.Equal(want) {
			t.Fatalf("pending date = %v, want %v", rating.PendingDate, want)
		}
		if rating.Version != 5 {
			t.Fatalf("version = %d, want 5", rating.Version)
		}
	})

	t.Run("decrease applies immediately", func(t *testing.T) {
		st := newFakeRatingStore()
		st.ratings[7] = domain.AccountRating{UserID: 7, Stars: 500, Level: 2, Version: 1}
		st.signals[7] = domain.AccountRatingSignals{StarsReceived: 500, Scam: true}
		service := newTestService(st, WithPendingDelay(24*time.Hour))

		rating, err := service.Recompute(context.Background(), 7)
		if err != nil {
			t.Fatalf("Recompute: %v", err)
		}
		if rating.Stars != 0 || rating.PendingStars != 0 {
			t.Fatalf("rating = %d stars / %d pending, want a penalty applied at once", rating.Stars, rating.PendingStars)
		}
		if rating.PenaltyComponent != domain.DefaultAccountRatingWeights().ScamPenalty {
			t.Fatalf("penalty = %d, want the scam penalty", rating.PenaltyComponent)
		}
	})

	t.Run("expired parking is folded into the visible rating", func(t *testing.T) {
		st := newFakeRatingStore()
		st.ratings[7] = domain.AccountRating{
			UserID: 7, Stars: 100, Level: 1, Version: 2,
			PendingStars: 400, PendingDate: testNow.Add(-time.Hour),
		}
		st.signals[7] = domain.AccountRatingSignals{StarsReceived: 500}
		service := newTestService(st, WithPendingDelay(24*time.Hour))

		rating, err := service.Recompute(context.Background(), 7)
		if err != nil {
			t.Fatalf("Recompute: %v", err)
		}
		if rating.Stars != 500 || rating.PendingStars != 0 || !rating.PendingDate.IsZero() {
			t.Fatalf("rating = %#v, want the parked delta applied and cleared", rating)
		}
	})

	t.Run("zero delay never parks", func(t *testing.T) {
		st := newFakeRatingStore()
		st.ratings[7] = domain.AccountRating{UserID: 7, Stars: 100, Version: 1}
		st.signals[7] = domain.AccountRatingSignals{StarsReceived: 500}
		service := newTestService(st, WithPendingDelay(0))

		rating, err := service.Recompute(context.Background(), 7)
		if err != nil {
			t.Fatalf("Recompute: %v", err)
		}
		if rating.Stars != 500 || rating.PendingStars != 0 {
			t.Fatalf("rating = %d stars / %d pending, want an immediate apply", rating.Stars, rating.PendingStars)
		}
	})
}

func TestRecomputeRetriesOnceOnVersionConflict(t *testing.T) {
	st := newFakeRatingStore()
	st.ratings[7] = domain.AccountRating{UserID: 7, Stars: 100, Version: 3}
	st.signals[7] = domain.AccountRatingSignals{StarsReceived: 200}
	st.forceConflicts = 1
	service := newTestService(st, WithPendingDelay(0))

	rating, err := service.Recompute(context.Background(), 7)
	if err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	if len(st.saves) != 2 {
		t.Fatalf("saves = %d, want exactly one retry", len(st.saves))
	}
	if rating.Version != 4 || rating.Stars != 200 {
		t.Fatalf("rating = %#v, want version 4 with 200 stars", rating)
	}
}

func TestRecomputeFailsAfterPersistentConflict(t *testing.T) {
	st := newFakeRatingStore()
	st.signals[7] = domain.AccountRatingSignals{StarsReceived: 200}
	st.forceConflicts = 2
	service := newTestService(st)

	if _, err := service.Recompute(context.Background(), 7); err == nil {
		t.Fatal("Recompute reported success while every save lost the version race")
	}
	if len(st.saves) != 2 {
		t.Fatalf("saves = %d, want the bounded single retry", len(st.saves))
	}
}

func TestAdjustRecordsLedgerAndRecomputes(t *testing.T) {
	st := newFakeRatingStore()
	st.signals[7] = domain.AccountRatingSignals{StarsReceived: 100}
	service := newTestService(st, WithPendingDelay(0))

	rating, applied, err := service.Adjust(context.Background(), domain.AdjustAccountRatingRequest{
		UserID: 7, Amount: 300, Reason: "support compensation", Actor: "admin", CommandKey: "cmd-1",
	})
	if err != nil || !applied {
		t.Fatalf("Adjust = %v, %v", applied, err)
	}
	if rating.ManualComponent != 300 || rating.Stars != 400 {
		t.Fatalf("rating = %#v, want the manual component folded in", rating)
	}
	if len(st.events[7]) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(st.events[7]))
	}
}

func TestAdjustReplayByCommandKeyIsIdempotent(t *testing.T) {
	st := newFakeRatingStore()
	st.signals[7] = domain.AccountRatingSignals{StarsReceived: 100}
	service := newTestService(st, WithPendingDelay(0))
	req := domain.AdjustAccountRatingRequest{UserID: 7, Amount: 300, Actor: "admin", CommandKey: "cmd-1"}

	first, applied, err := service.Adjust(context.Background(), req)
	if err != nil || !applied {
		t.Fatalf("first Adjust = %v, %v", applied, err)
	}
	second, applied, err := service.Adjust(context.Background(), req)
	if err != nil {
		t.Fatalf("replayed Adjust: %v", err)
	}
	if applied {
		t.Fatal("replayed Adjust reported applied=true")
	}
	if len(st.events[7]) != 1 || st.manual[7] != 300 {
		t.Fatalf("ledger = %d rows / manual %d, want the replay recorded nothing", len(st.events[7]), st.manual[7])
	}
	if second.Stars != first.Stars || second.ManualComponent != first.ManualComponent {
		t.Fatalf("replayed rating = %#v, want the same score as %#v", second, first)
	}
}

func TestAdjustValidatesRequest(t *testing.T) {
	st := newFakeRatingStore()
	service := newTestService(st)
	tests := []domain.AdjustAccountRatingRequest{
		{UserID: 0, Amount: 10},
		{UserID: 7, Amount: 0},
		{UserID: 7, Amount: 10, Reason: string(make([]byte, domain.MaxAccountRatingReasonLength+1))},
	}
	for _, req := range tests {
		if _, _, err := service.Adjust(context.Background(), req); !errors.Is(err, domain.ErrAccountRatingAdjustmentInvalid) {
			t.Fatalf("Adjust(%#v) error = %v, want ErrAccountRatingAdjustmentInvalid", req, err)
		}
	}
	if len(st.events) != 0 || len(st.saves) != 0 {
		t.Fatal("store was touched by an invalid adjustment")
	}
}

func TestRunRecomputeCycleProcessesTheBatch(t *testing.T) {
	st := newFakeRatingStore()
	st.stale = []int64{1, 2, 3}
	for _, userID := range st.stale {
		st.signals[userID] = domain.AccountRatingSignals{StarsReceived: 100 * userID}
	}
	service := newTestService(st, WithStaleAfter(6*time.Hour))

	processed, err := service.RunRecomputeCycle(context.Background(), 10)
	if err != nil {
		t.Fatalf("RunRecomputeCycle: %v", err)
	}
	if processed != 3 {
		t.Fatalf("processed = %d, want 3", processed)
	}
	if st.staleLimit != 10 {
		t.Fatalf("stale limit = %d, want the requested 10", st.staleLimit)
	}
	if want := testNow.Add(-6 * time.Hour).Unix(); st.staleOlderThan != want {
		t.Fatalf("stale horizon = %d, want %d", st.staleOlderThan, want)
	}
	for _, userID := range st.stale {
		if _, ok := st.ratings[userID]; !ok {
			t.Fatalf("user %d was not recomputed", userID)
		}
	}
}

func TestRunRecomputeCycleSkipsFailingUsers(t *testing.T) {
	st := newFakeRatingStore()
	st.stale = []int64{1, 0, 2}
	st.forceConflicts = 2 // both saves of the first user lose the race
	service := newTestService(st)

	processed, err := service.RunRecomputeCycle(context.Background(), 0)
	if err != nil {
		t.Fatalf("RunRecomputeCycle: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want the surviving user only", processed)
	}
	if st.staleLimit != defaultRecomputeBatch {
		t.Fatalf("stale limit = %d, want the default batch", st.staleLimit)
	}
}

func TestReadPathsDegradeWhenDisabled(t *testing.T) {
	st := newFakeRatingStore()
	st.ratings[7] = domain.AccountRating{UserID: 7, Stars: 500, Level: 2, Version: 1}
	service := newTestService(st, WithEnabled(false))

	if service.Enabled() || service.Ready() {
		t.Fatal("disabled service reported enabled/ready")
	}
	// The userFull projection omits both TL flags on this error, which is exactly
	// the pre-rating wire shape.
	if _, err := service.Rating(context.Background(), 7); !errors.Is(err, domain.ErrAccountRatingNotFound) {
		t.Fatalf("Rating error = %v, want ErrAccountRatingNotFound", err)
	}
	batch, err := service.RatingBatch(context.Background(), []int64{7})
	if err != nil || len(batch) != 0 {
		t.Fatalf("RatingBatch = %#v, %v; want empty", batch, err)
	}
	if _, err := service.Recompute(context.Background(), 7); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Recompute error = %v, want ErrDisabled", err)
	}
	if _, _, err := service.Adjust(context.Background(), domain.AdjustAccountRatingRequest{UserID: 7, Amount: 5}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Adjust error = %v, want ErrDisabled", err)
	}
	processed, err := service.RunRecomputeCycle(context.Background(), 10)
	if err != nil || processed != 0 {
		t.Fatalf("RunRecomputeCycle = %d, %v; want a no-op", processed, err)
	}
}

func TestUnconfiguredStoreReportsConfiguration(t *testing.T) {
	service := NewService()
	if service.Ready() {
		t.Fatal("Ready = true without a store")
	}
	if _, err := service.Rating(context.Background(), 7); err == nil {
		t.Fatal("Rating accepted a missing store")
	}
	if _, err := service.Recompute(context.Background(), 7); err == nil {
		t.Fatal("Recompute accepted a missing store")
	}
	if _, _, err := service.Adjust(context.Background(), domain.AdjustAccountRatingRequest{UserID: 7, Amount: 5}); err == nil {
		t.Fatal("Adjust accepted a missing store")
	}
	if _, err := service.RunRecomputeCycle(context.Background(), 10); err == nil {
		t.Fatal("RunRecomputeCycle accepted a missing store")
	}
}

func TestNilServiceIsSafe(t *testing.T) {
	var service *Service
	if service.Enabled() || service.Ready() {
		t.Fatal("nil service reported enabled/ready")
	}
	if got := service.Weights(); got != domain.DefaultAccountRatingWeights() {
		t.Fatalf("nil service weights = %#v, want the defaults", got)
	}
	if _, err := service.Rating(context.Background(), 7); !errors.Is(err, domain.ErrAccountRatingNotFound) {
		t.Fatalf("nil service Rating error = %v, want ErrAccountRatingNotFound", err)
	}
	if batch, err := service.RatingBatch(context.Background(), []int64{7}); err != nil || len(batch) != 0 {
		t.Fatalf("nil service RatingBatch = %#v, %v; want empty", batch, err)
	}
	if _, err := service.Recompute(context.Background(), 7); !errors.Is(err, ErrDisabled) {
		t.Fatalf("nil service Recompute error = %v, want ErrDisabled", err)
	}
	if processed, err := service.RunRecomputeCycle(context.Background(), 10); err != nil || processed != 0 {
		t.Fatalf("nil service RunRecomputeCycle = %d, %v", processed, err)
	}
}

func TestInvalidWeightsFallBackToDefaults(t *testing.T) {
	st := newFakeRatingStore()
	service := newTestService(st, WithWeights(domain.AccountRatingWeights{StarsReceivedPermille: -1}))
	if got := service.Weights(); got != domain.DefaultAccountRatingWeights() {
		t.Fatalf("weights = %#v, want the defaults after rejecting a negative set", got)
	}
}

func TestListAndEventsBoundThePage(t *testing.T) {
	st := newFakeRatingStore()
	st.ratings[7] = domain.AccountRating{UserID: 7, Level: 3, Version: 1}
	for i := range maxEventLimit + 10 {
		st.events[7] = append(st.events[7], domain.AccountRatingEvent{ID: int64(i + 1), UserID: 7, Amount: 1})
	}
	service := newTestService(st)

	list, err := service.List(context.Background(), domain.AccountRatingFilter{MinLevel: -5, Limit: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List = %d rows, want 1", len(list))
	}
	events, err := service.Events(context.Background(), 7, 100000)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != maxEventLimit {
		t.Fatalf("Events = %d rows, want the %d cap", len(events), maxEventLimit)
	}
	if _, err := service.Events(context.Background(), 0, 10); !errors.Is(err, domain.ErrAccountRatingAdjustmentInvalid) {
		t.Fatalf("Events accepted a zero user id")
	}
}

func TestRecomputeWorkerRunsAndStops(t *testing.T) {
	st := newFakeRatingStore()
	st.stale = []int64{1}
	st.signals[1] = domain.AccountRatingSignals{StarsReceived: 100}
	service := newTestService(st)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		NewRecomputeWorker(service, nil, time.Hour, 10).Run(ctx)
	}()
	// The first cycle runs before the ticker, so cancelling immediately still
	// leaves exactly one recompute behind.
	<-time.After(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop on context cancellation")
	}
	if _, ok := st.ratings[1]; !ok {
		t.Fatal("worker did not recompute the stale user")
	}
}

func TestRecomputeWorkerExitsWhenNotReady(t *testing.T) {
	worker := NewRecomputeWorker(newTestService(newFakeRatingStore(), WithEnabled(false)), nil, time.Millisecond, 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled worker kept running")
	}
	if worker.batch != defaultRecomputeBatch {
		t.Fatalf("batch = %d, want the default fallback", worker.batch)
	}
}

// TestRunRecomputeCycleSeedsAccountsWithNoProjection is the report "the ratings tab
// is empty and no client shows a rating". StaleAccountRatings reads account_rating,
// so it can only ever refresh rows that already exist; without a seeding pass the
// very first row for a user has to come from an operator recomputing that user by
// hand, and the read model stays permanently empty.
func TestRunRecomputeCycleSeedsAccountsWithNoProjection(t *testing.T) {
	st := newFakeRatingStore()
	st.unrated = []int64{11, 12, 13}
	for _, userID := range st.unrated {
		st.signals[userID] = domain.AccountRatingSignals{StarsReceived: 100 * userID}
	}
	service := newTestService(st)

	processed, err := service.RunRecomputeCycle(context.Background(), 10)
	if err != nil {
		t.Fatalf("RunRecomputeCycle: %v", err)
	}
	if processed != 3 {
		t.Fatalf("processed = %d, want the three seeded accounts", processed)
	}
	for _, userID := range st.unrated {
		if _, ok := st.ratings[userID]; !ok {
			t.Fatalf("account %d was not seeded", userID)
		}
	}
	// A second cycle has nothing left to seed, so seeding converges instead of
	// rewriting the same rows every interval.
	if processed, err := service.RunRecomputeCycle(context.Background(), 10); err != nil || processed != 0 {
		t.Fatalf("second cycle = %d,%v, want 0,nil", processed, err)
	}
}

// The batch bound belongs to the cycle, not to each pass: a backlog of stale rows
// must not let one cycle do an unbounded amount of work.
func TestRunRecomputeCycleSharesTheBatchBudget(t *testing.T) {
	st := newFakeRatingStore()
	st.stale = []int64{1, 2}
	st.unrated = []int64{11, 12, 13, 14}
	service := newTestService(st)

	processed, err := service.RunRecomputeCycle(context.Background(), 3)
	if err != nil {
		t.Fatalf("RunRecomputeCycle: %v", err)
	}
	if processed != 3 {
		t.Fatalf("processed = %d, want the batch bound of 3", processed)
	}
	if st.unratedLimit != 1 {
		t.Fatalf("seeding limit = %d, want the 1 left after two stale rows", st.unratedLimit)
	}

	// A cycle whose stale pass already fills the batch does not query for seeds at
	// all: refreshing rows somebody is looking at comes first.
	full := newFakeRatingStore()
	full.stale = []int64{1, 2, 3}
	full.unrated = []int64{11}
	if _, err := newTestService(full).RunRecomputeCycle(context.Background(), 3); err != nil {
		t.Fatalf("RunRecomputeCycle: %v", err)
	}
	if full.unratedCalls != 0 {
		t.Fatalf("seeding was queried %d times, want none when the batch is already full", full.unratedCalls)
	}
}

// Seeding extends the cycle; it is not its purpose. A store that cannot enumerate
// accounts must not turn a successful stale pass into a failed cycle.
func TestRunRecomputeCycleSurvivesSeedingFailure(t *testing.T) {
	st := newFakeRatingStore()
	st.stale = []int64{1}
	st.unratedErr = errors.New("no users table")
	service := newTestService(st)

	processed, err := service.RunRecomputeCycle(context.Background(), 10)
	if err != nil {
		t.Fatalf("RunRecomputeCycle = %v, want the stale pass to stand", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want the one stale row", processed)
	}
}

// TestEnsureRatingMaterializesOnce covers the self-profile path: an account opening
// its own profile before the worker reached it must see a rating rather than
// nothing, and the second read must not write again.
func TestEnsureRatingMaterializesOnce(t *testing.T) {
	st := newFakeRatingStore()
	st.signals[7] = domain.AccountRatingSignals{StarsReceived: 900}
	service := newTestService(st)

	if _, err := service.Rating(context.Background(), 7); !errors.Is(err, domain.ErrAccountRatingNotFound) {
		t.Fatalf("Rating before materialising = %v, want ErrAccountRatingNotFound", err)
	}
	rating, err := service.EnsureRating(context.Background(), 7)
	if err != nil {
		t.Fatalf("EnsureRating: %v", err)
	}
	if rating.UserID != 7 || rating.Stars == 0 {
		t.Fatalf("materialised rating = %+v, want a computed rating for user 7", rating)
	}
	writes := len(st.saves)
	again, err := service.EnsureRating(context.Background(), 7)
	if err != nil {
		t.Fatalf("second EnsureRating: %v", err)
	}
	if again.Version != rating.Version {
		t.Fatalf("second EnsureRating rewrote the row: version %d then %d", rating.Version, again.Version)
	}
	if len(st.saves) != writes {
		t.Fatalf("second EnsureRating issued %d extra saves, want none", len(st.saves)-writes)
	}
}

// A disabled feature materialises nothing, which keeps "disabled" indistinguishable
// on the wire from "before ratings existed".
func TestEnsureRatingDisabledStaysEmpty(t *testing.T) {
	st := newFakeRatingStore()
	st.signals[7] = domain.AccountRatingSignals{StarsReceived: 900}
	service := newTestService(st, WithEnabled(false))

	if _, err := service.EnsureRating(context.Background(), 7); !errors.Is(err, domain.ErrAccountRatingNotFound) {
		t.Fatalf("EnsureRating while disabled = %v, want ErrAccountRatingNotFound", err)
	}
	if len(st.saves) != 0 {
		t.Fatalf("EnsureRating while disabled wrote %d rows, want none", len(st.saves))
	}
}

// TestRecomputeRefusesServiceAccounts pins that the platform account and the
// built-in bots carry no rating. The platform account is not flagged is_bot, so the
// bot exclusion in the seeding query does not cover it -- which is how it acquired a
// rating in the first place -- and an operator must not be able to create one by
// hand either.
func TestRecomputeRefusesServiceAccounts(t *testing.T) {
	for _, userID := range domain.SystemUserIDs() {
		st := newFakeRatingStore()
		st.signals[userID] = domain.AccountRatingSignals{StarsReceived: 5000}
		st.unrated = []int64{userID}
		service := newTestService(st)

		if _, err := service.Recompute(context.Background(), userID); !errors.Is(err, domain.ErrAccountRatingAdjustmentInvalid) {
			t.Fatalf("Recompute(%d) = %v, want ErrAccountRatingAdjustmentInvalid", userID, err)
		}
		if _, err := service.EnsureRating(context.Background(), userID); err == nil {
			t.Fatalf("EnsureRating(%d) succeeded, want a refusal", userID)
		}
		if len(st.ratings) != 0 {
			t.Fatalf("service account %d ended up with a projection: %#v", userID, st.ratings)
		}
		// A seeding pass that is somehow handed one skips it rather than failing the
		// whole cycle.
		if processed, err := service.RunRecomputeCycle(context.Background(), 10); err != nil || processed != 0 {
			t.Fatalf("cycle over service account %d = %d,%v, want 0,nil", userID, processed, err)
		}
	}

	// An ordinary account is unaffected.
	st := newFakeRatingStore()
	st.signals[42] = domain.AccountRatingSignals{StarsReceived: 5000}
	if _, err := newTestService(st).Recompute(context.Background(), 42); err != nil {
		t.Fatalf("Recompute of an ordinary account: %v", err)
	}
}
