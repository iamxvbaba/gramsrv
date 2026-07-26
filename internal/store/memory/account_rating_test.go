package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var _ store.AccountRatingStore = (*AccountRatingStore)(nil)

// accountRatingFixture builds a projection the table's CHECK constraints accept.
func accountRatingFixture(userID, stars, version int64, computedAt time.Time) domain.AccountRating {
	level, current, next, hasNext := domain.AccountRatingLevelForStars(stars)
	return domain.AccountRating{
		UserID:            userID,
		Level:             level,
		Stars:             stars,
		CurrentLevelStars: current,
		NextLevelStars:    next,
		HasNextLevel:      hasNext,
		StarsComponent:    stars,
		ComputedAt:        computedAt,
		UpdatedAt:         computedAt,
		Version:           version,
	}
}

func TestSaveAccountRating(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()

	tests := []struct {
		name        string
		seed        []domain.AccountRating
		input       domain.AccountRating
		wantErr     error
		wantChanged bool
		check       func(t *testing.T, s *AccountRatingStore, stored domain.AccountRating)
	}{
		{
			name:        "insert",
			input:       accountRatingFixture(11, 450, 1, now),
			wantChanged: true,
			check: func(t *testing.T, s *AccountRatingStore, stored domain.AccountRating) {
				if stored.Level != 2 || stored.Stars != 450 || stored.Version != 1 ||
					stored.CurrentLevelStars != 400 || stored.NextLevelStars != 900 || !stored.HasNextLevel {
					t.Fatalf("stored=%+v", stored)
				}
				read, err := s.AccountRating(ctx, 11)
				if err != nil || read != stored {
					t.Fatalf("read=%+v err=%v", read, err)
				}
			},
		},
		{
			name:        "successor version applied",
			seed:        []domain.AccountRating{accountRatingFixture(11, 450, 1, now)},
			input:       accountRatingFixture(11, 1000, 2, now.Add(time.Minute)),
			wantChanged: true,
			check: func(t *testing.T, s *AccountRatingStore, stored domain.AccountRating) {
				if stored.Version != 2 || stored.Stars != 1000 || stored.Level != 3 {
					t.Fatalf("stored=%+v", stored)
				}
			},
		},
		{
			name:        "replayed version is stale",
			seed:        []domain.AccountRating{accountRatingFixture(11, 450, 1, now)},
			input:       accountRatingFixture(11, 9999, 1, now.Add(time.Minute)),
			wantChanged: false,
			check: func(t *testing.T, s *AccountRatingStore, stored domain.AccountRating) {
				// The loser of the race gets the current row back, untouched.
				if stored.Stars != 450 || stored.Version != 1 {
					t.Fatalf("stored=%+v", stored)
				}
				read, err := s.AccountRating(ctx, 11)
				if err != nil || read.Stars != 450 || read.Version != 1 {
					t.Fatalf("read=%+v err=%v", read, err)
				}
			},
		},
		{
			name:        "version from the future is rejected",
			seed:        []domain.AccountRating{accountRatingFixture(11, 450, 1, now)},
			input:       accountRatingFixture(11, 9999, 7, now.Add(time.Minute)),
			wantChanged: false,
			check: func(t *testing.T, s *AccountRatingStore, stored domain.AccountRating) {
				if stored.Stars != 450 || stored.Version != 1 {
					t.Fatalf("stored=%+v", stored)
				}
			},
		},
		{
			name:        "insert must carry version one",
			input:       accountRatingFixture(11, 450, 3, now),
			wantChanged: false,
			check: func(t *testing.T, s *AccountRatingStore, stored domain.AccountRating) {
				if stored != (domain.AccountRating{}) {
					t.Fatalf("stored=%+v", stored)
				}
				if _, err := s.AccountRating(ctx, 11); !errors.Is(err, domain.ErrAccountRatingNotFound) {
					t.Fatalf("row was written: %v", err)
				}
			},
		},
		{
			name:    "no user",
			input:   accountRatingFixture(0, 450, 1, now),
			wantErr: domain.ErrAccountRatingNotFound,
		},
		{
			name: "pending delta without a date is dropped",
			input: func() domain.AccountRating {
				rating := accountRatingFixture(11, 450, 1, now)
				rating.PendingStars = 120
				return rating
			}(),
			wantChanged: true,
			check: func(t *testing.T, s *AccountRatingStore, stored domain.AccountRating) {
				if stored.PendingStars != 0 || !stored.PendingDate.IsZero() {
					t.Fatalf("stored=%+v", stored)
				}
				if _, ok := stored.PendingStarsRating(); ok {
					t.Fatalf("pending projection survived: %+v", stored)
				}
			},
		},
		{
			name: "pending pair is kept",
			input: func() domain.AccountRating {
				rating := accountRatingFixture(11, 450, 1, now)
				rating.PendingStars = 500
				rating.PendingDate = now.Add(time.Hour)
				return rating
			}(),
			wantChanged: true,
			check: func(t *testing.T, s *AccountRatingStore, stored domain.AccountRating) {
				pending, ok := stored.PendingStarsRating()
				if !ok || pending.Stars != 950 || pending.Level != 3 {
					t.Fatalf("pending=%+v ok=%v", pending, ok)
				}
			},
		},
		{
			name: "impossible components are folded",
			input: func() domain.AccountRating {
				rating := accountRatingFixture(11, 450, 1, now)
				rating.Level = -3
				rating.StarsComponent = -10
				rating.ActivityComponent = -1
				rating.PenaltyComponent = -7
				rating.CurrentLevelStars = -5
				rating.NextLevelStars = -9
				rating.HasNextLevel = true
				return rating
			}(),
			wantChanged: true,
			check: func(t *testing.T, s *AccountRatingStore, stored domain.AccountRating) {
				if stored.Level != 0 || stored.StarsComponent != 0 || stored.ActivityComponent != 0 ||
					stored.PenaltyComponent != 0 || stored.CurrentLevelStars != 0 {
					t.Fatalf("stored=%+v", stored)
				}
				if stored.HasNextLevel || stored.NextLevelStars != 0 {
					t.Fatalf("next level survived: %+v", stored)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewAccountRatingStore()
			for _, seed := range tc.seed {
				if _, changed, err := s.SaveAccountRating(ctx, seed); err != nil || !changed {
					t.Fatalf("seed changed=%v err=%v", changed, err)
				}
			}
			stored, changed, err := s.SaveAccountRating(ctx, tc.input)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want %v", err, tc.wantErr)
			}
			if changed != tc.wantChanged {
				t.Fatalf("changed=%v want %v", changed, tc.wantChanged)
			}
			if tc.wantErr != nil {
				return
			}
			if tc.check != nil {
				tc.check(t, s, stored)
			}
		})
	}
}

func TestAccountRatingReadsAndLeaderboard(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	s := NewAccountRatingStore()
	for _, rating := range []domain.AccountRating{
		accountRatingFixture(11, 2500, 1, now),
		accountRatingFixture(12, 450, 1, now),
		accountRatingFixture(13, 2500, 1, now),
		accountRatingFixture(14, 0, 1, now),
	} {
		if _, changed, err := s.SaveAccountRating(ctx, rating); err != nil || !changed {
			t.Fatalf("seed %d changed=%v err=%v", rating.UserID, changed, err)
		}
	}

	batch, err := s.AccountRatingBatch(ctx, []int64{11, 13, 99, 0, 11})
	if err != nil || len(batch) != 2 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	if batch[11].Stars != 2500 || batch[13].Stars != 2500 {
		t.Fatalf("batch=%+v", batch)
	}
	if empty, err := s.AccountRatingBatch(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty batch=%+v err=%v", empty, err)
	}

	// level desc, stars desc, user id asc.
	board, err := s.ListAccountRatings(ctx, domain.AccountRatingFilter{})
	if err != nil || len(board) != 4 {
		t.Fatalf("board=%+v err=%v", board, err)
	}
	want := []int64{11, 13, 12, 14}
	for i, userID := range want {
		if board[i].UserID != userID {
			t.Fatalf("board order=%+v want %v", board, want)
		}
	}
	page, err := s.ListAccountRatings(ctx, domain.AccountRatingFilter{Limit: 2})
	if err != nil || len(page) != 2 || page[1].UserID != 13 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	next, err := s.ListAccountRatings(ctx, domain.AccountRatingFilter{
		BeforeID: page[len(page)-1].UserID, Limit: 2,
	})
	if err != nil || len(next) != 2 || next[0].UserID != 12 || next[1].UserID != 14 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	filtered, err := s.ListAccountRatings(ctx, domain.AccountRatingFilter{MinLevel: 5})
	if err != nil || len(filtered) != 2 {
		t.Fatalf("filtered=%+v err=%v", filtered, err)
	}
	single, err := s.ListAccountRatings(ctx, domain.AccountRatingFilter{UserID: 12})
	if err != nil || len(single) != 1 || single[0].UserID != 12 {
		t.Fatalf("single=%+v err=%v", single, err)
	}
	if _, err := s.AccountRating(ctx, 99); !errors.Is(err, domain.ErrAccountRatingNotFound) {
		t.Fatalf("unknown user err=%v", err)
	}
}

func TestAdjustAccountRating(t *testing.T) {
	ctx := context.Background()
	s := NewAccountRatingStore()
	req := domain.AdjustAccountRatingRequest{
		UserID: 11, Amount: 750, Reason: "contest prize", Actor: "admin", CommandKey: "cmd-adjust",
	}

	event, applied, err := s.AdjustAccountRating(ctx, req)
	if err != nil || !applied {
		t.Fatalf("adjust applied=%v err=%v", applied, err)
	}
	if event.ID == 0 || event.Kind != domain.AccountRatingEventManual || event.Amount != 750 ||
		event.Reason != "contest prize" || event.Actor != "admin" || event.CreatedAt.IsZero() {
		t.Fatalf("event=%+v", event)
	}

	// Replaying the command key returns the recorded row and appends nothing.
	replay, applied, err := s.AdjustAccountRating(ctx, req)
	if err != nil || applied {
		t.Fatalf("replay applied=%v err=%v", applied, err)
	}
	if replay != event {
		t.Fatalf("replay=%+v want %+v", replay, event)
	}
	ledger, err := s.AccountRatingEvents(ctx, 11, 10)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger=%+v err=%v", ledger, err)
	}

	second := req
	second.Amount = -200
	second.CommandKey = "cmd-adjust-2"
	if _, applied, err := s.AdjustAccountRating(ctx, second); err != nil || !applied {
		t.Fatalf("second adjust applied=%v err=%v", applied, err)
	}
	// Newest first, and other users are not mixed in.
	if _, applied, err := s.AdjustAccountRating(ctx, domain.AdjustAccountRatingRequest{
		UserID: 12, Amount: 5, CommandKey: "cmd-other",
	}); err != nil || !applied {
		t.Fatalf("other user applied=%v err=%v", applied, err)
	}
	ledger, err = s.AccountRatingEvents(ctx, 11, 10)
	if err != nil || len(ledger) != 2 || ledger[0].Amount != -200 || ledger[1].Amount != 750 {
		t.Fatalf("ledger=%+v err=%v", ledger, err)
	}
	if capped, err := s.AccountRatingEvents(ctx, 11, 1); err != nil || len(capped) != 1 ||
		capped[0].Amount != -200 {
		t.Fatalf("capped=%+v err=%v", capped, err)
	}

	// An unkeyed adjustment is always appended.
	if _, applied, err := s.AdjustAccountRating(ctx, domain.AdjustAccountRatingRequest{
		UserID: 11, Amount: 10,
	}); err != nil || !applied {
		t.Fatalf("unkeyed applied=%v err=%v", applied, err)
	}

	for _, invalid := range []domain.AdjustAccountRatingRequest{
		{UserID: 11, Amount: 0, CommandKey: "cmd-zero"},
		{UserID: 0, Amount: 5, CommandKey: "cmd-nouser"},
	} {
		if _, applied, err := s.AdjustAccountRating(ctx, invalid); !errors.Is(err, domain.ErrAccountRatingAdjustmentInvalid) || applied {
			t.Fatalf("invalid adjust applied=%v err=%v", applied, err)
		}
	}

	// The manual total is carried out of the ledger into the signal snapshot.
	signals, err := s.AccountRatingSignals(ctx, 11)
	if err != nil || signals.UserID != 11 || signals.Manual != 560 {
		t.Fatalf("signals=%+v err=%v", signals, err)
	}
}

func TestAccountRatingSignals(t *testing.T) {
	ctx := context.Background()
	s := NewAccountRatingStore()

	// A user with no contributions reports zeros rather than an error.
	signals, err := s.AccountRatingSignals(ctx, 11)
	if err != nil || signals != (domain.AccountRatingSignals{UserID: 11}) {
		t.Fatalf("signals=%+v err=%v", signals, err)
	}
	if _, err := s.AccountRatingSignals(ctx, 0); !errors.Is(err, domain.ErrAccountRatingNotFound) {
		t.Fatalf("missing user err=%v", err)
	}

	s.setAccountRatingSignals(domain.AccountRatingSignals{
		UserID: 11, StarsReceived: 4000, StarsSpent: 2000, MessagesSent: 300,
		AccountAgeDays: 100, GiftsReceived: 4, ModerationCases: 1,
	})
	if _, applied, err := s.AdjustAccountRating(ctx, domain.AdjustAccountRatingRequest{
		UserID: 11, Amount: 250, CommandKey: "cmd-bonus",
	}); err != nil || !applied {
		t.Fatalf("adjust applied=%v err=%v", applied, err)
	}
	signals, err = s.AccountRatingSignals(ctx, 11)
	if err != nil || signals.StarsReceived != 4000 || signals.MessagesSent != 300 ||
		signals.ModerationCases != 1 || signals.Manual != 250 {
		t.Fatalf("signals=%+v err=%v", signals, err)
	}

	// The snapshot feeds the domain formula, and the result round-trips.
	now := time.Unix(1700000000, 0).UTC()
	computed := domain.ComputeAccountRating(signals, domain.DefaultAccountRatingWeights(), now)
	stored, changed, err := s.SaveAccountRating(ctx, computed)
	if err != nil || !changed {
		t.Fatalf("save changed=%v err=%v", changed, err)
	}
	if stored.Stars != computed.Stars || stored.Level != computed.Level ||
		stored.ManualComponent != 250 {
		t.Fatalf("stored=%+v computed=%+v", stored, computed)
	}

	// A recompute uses the pending resolution the domain owns.
	s.setAccountRatingSignals(domain.AccountRatingSignals{UserID: 11, StarsReceived: 40000})
	signals, err = s.AccountRatingSignals(ctx, 11)
	if err != nil {
		t.Fatal(err)
	}
	recomputed := domain.ResolveAccountRatingPending(stored,
		domain.ComputeAccountRating(signals, domain.DefaultAccountRatingWeights(), now.Add(time.Hour)),
		24*time.Hour, now.Add(time.Hour))
	saved, changed, err := s.SaveAccountRating(ctx, recomputed)
	if err != nil || !changed || saved.Version != stored.Version+1 {
		t.Fatalf("saved=%+v changed=%v err=%v", saved, changed, err)
	}
	if saved.PendingStars <= 0 || saved.PendingDate.IsZero() {
		t.Fatalf("pending was not parked: %+v", saved)
	}
}

func TestStaleAccountRatings(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	s := NewAccountRatingStore()
	for _, rating := range []domain.AccountRating{
		accountRatingFixture(11, 100, 1, now.Add(-3*time.Hour)),
		accountRatingFixture(12, 100, 1, now.Add(-2*time.Hour)),
		accountRatingFixture(13, 100, 1, now.Add(-time.Hour)),
		accountRatingFixture(14, 100, 1, now),
	} {
		if _, changed, err := s.SaveAccountRating(ctx, rating); err != nil || !changed {
			t.Fatalf("seed %d changed=%v err=%v", rating.UserID, changed, err)
		}
	}

	stale, err := s.StaleAccountRatings(ctx, now.Add(-90*time.Minute).Unix(), 10)
	if err != nil || len(stale) != 2 || stale[0] != 11 || stale[1] != 12 {
		t.Fatalf("stale=%v err=%v", stale, err)
	}
	if limited, err := s.StaleAccountRatings(ctx, now.Add(-90*time.Minute).Unix(), 1); err != nil ||
		len(limited) != 1 || limited[0] != 11 {
		t.Fatalf("limited=%v err=%v", limited, err)
	}
	if none, err := s.StaleAccountRatings(ctx, now.Add(-4*time.Hour).Unix(), 10); err != nil || len(none) != 0 {
		t.Fatalf("none=%v err=%v", none, err)
	}
	// A recompute refreshes computed_at and takes the row out of the horizon.
	refreshed := accountRatingFixture(11, 100, 2, now)
	if _, changed, err := s.SaveAccountRating(ctx, refreshed); err != nil || !changed {
		t.Fatalf("refresh changed=%v err=%v", changed, err)
	}
	stale, err = s.StaleAccountRatings(ctx, now.Add(-90*time.Minute).Unix(), 10)
	if err != nil || len(stale) != 1 || stale[0] != 12 {
		t.Fatalf("stale=%v err=%v", stale, err)
	}
}
