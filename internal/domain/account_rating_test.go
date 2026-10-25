package domain

import (
	"testing"
	"time"
)

// TestAccountRatingLevelScale pins the server-local ladder: 100 levels, the
// top one at 250k score, and every threshold strictly increasing.
func TestAccountRatingLevelScale(t *testing.T) {
	if MaxAccountRatingLevel != 100 {
		t.Fatalf("MaxAccountRatingLevel = %d, want 100 (the configured local ceiling)", MaxAccountRatingLevel)
	}
	top := AccountRatingLevelThreshold(MaxAccountRatingLevel)
	if top != 250_000 {
		t.Fatalf("top level threshold = %d, want 250000", top)
	}
	previous := int64(0)
	for level := 1; level <= MaxAccountRatingLevel; level++ {
		threshold := AccountRatingLevelThreshold(level)
		if threshold <= previous {
			t.Fatalf("level %d threshold = %d, want above the previous %d", level, threshold, previous)
		}
		previous = threshold
	}
	// Beyond the top the curve is clamped rather than extrapolated: a level outside the
	// local policy must never leak into the wire projection.
	if got := AccountRatingLevelThreshold(MaxAccountRatingLevel + 10); got != top {
		t.Fatalf("over-max threshold = %d, want the clamped %d", got, top)
	}
	if got := AccountRatingLevelThreshold(0); got != 0 {
		t.Fatalf("level 0 threshold = %d, want 0", got)
	}
}

// TestAccountRatingLevelForStarsBoundary pins the exact-threshold behaviour the
// progress bar depends on: a score that lands on a threshold is already on that
// level, and the pair (current, next) always brackets it.
func TestAccountRatingLevelForStarsBoundary(t *testing.T) {
	cases := []struct {
		stars         int64
		level         int
		current, next int64
		hasNext       bool
	}{
		{stars: 0, level: 0, current: 0, next: 25, hasNext: true},
		{stars: 24, level: 0, current: 0, next: 25, hasNext: true},
		{stars: 25, level: 1, current: 25, next: 100, hasNext: true},
		{stars: 99, level: 1, current: 25, next: 100, hasNext: true},
		{stars: 100, level: 2, current: 100, next: 225, hasNext: true},
		{stars: 2_500, level: 10, current: 2_500, next: 3_025, hasNext: true},
		{stars: 10_000, level: 20, current: 10_000, next: 11_025, hasNext: true},
		{stars: 62_500, level: 50, current: 62_500, next: 65_025, hasNext: true},
		{stars: 249_999, level: 99, current: 245_025, next: 250_000, hasNext: true},
		{stars: 250_000, level: 100, current: 250_000, next: 0, hasNext: false},
		{stars: 10_000_000, level: 100, current: 250_000, next: 0, hasNext: false},
		{stars: -5, level: 0, current: 0, next: 25, hasNext: true},
	}
	for _, tc := range cases {
		level, current, next, hasNext := AccountRatingLevelForStars(tc.stars)
		if level != tc.level || current != tc.current || next != tc.next || hasNext != tc.hasNext {
			t.Fatalf("AccountRatingLevelForStars(%d) = %d/%d/%d/%v, want %d/%d/%d/%v",
				tc.stars, level, current, next, hasNext,
				tc.level, tc.current, tc.next, tc.hasNext)
		}
	}
	// No score below the top threshold may report itself as the top level, and no
	// score above one may report anything else: the ladder has exactly 100 rungs.
	for level := 1; level < MaxAccountRatingLevel; level++ {
		levelAt, _, _, _ := AccountRatingLevelForStars(AccountRatingLevelThreshold(level))
		if levelAt != level {
			t.Fatalf("threshold %d resolved to level %d", AccountRatingLevelThreshold(level), levelAt)
		}
		if below, _, _, _ := AccountRatingLevelForStars(AccountRatingLevelThreshold(level) - 1); below != level-1 {
			t.Fatalf("just below threshold %d resolved to level %d, want %d",
				AccountRatingLevelThreshold(level), below, level-1)
		}
	}
}

// TestComputeAccountRatingTopLevelDropsNextThreshold mirrors what the clients
// draw: no next_level_stars once the ceiling is reached, so the profile shows a
// max-level badge instead of an endless progress bar.
func TestComputeAccountRatingTopLevelDropsNextThreshold(t *testing.T) {
	top := AccountRatingLevelThreshold(MaxAccountRatingLevel)
	rating := ComputeAccountRating(
		AccountRatingSignals{UserID: 7, StarsReceived: top},
		DefaultAccountRatingWeights(),
		time.Unix(1_700_000_000, 0).UTC(),
	)
	if rating.Level != MaxAccountRatingLevel || rating.HasNextLevel || rating.NextLevelStars != 0 {
		t.Fatalf("top rating = %+v, want level %d without next", rating, MaxAccountRatingLevel)
	}
	if rating.CurrentLevelStars != top || rating.Stars != top {
		t.Fatalf("top rating thresholds = %d/%d, want %d/%d",
			rating.CurrentLevelStars, rating.Stars, top, top)
	}
	// A pending level is derived from the same curve, so it cannot report a
	// next_level_stars the visible level would not have either.
	pending := rating
	pending.PendingStars = 1
	pending.PendingDate = time.Unix(1_700_000_000, 0).UTC()
	if snapshot, ok := pending.PendingLevel(); !ok ||
		snapshot.Level != MaxAccountRatingLevel || snapshot.HasNextLevelStars {
		t.Fatalf("pending top snapshot = %+v/%v, want level %d without next",
			snapshot, ok, MaxAccountRatingLevel)
	}
}
