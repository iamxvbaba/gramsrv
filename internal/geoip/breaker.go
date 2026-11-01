package geoip

import (
	"sync"
	"time"
)

// breaker is the health state machine of a single geographic backend.
//
// Free geographic backends all rate limit, and they fail in completely different ways:
//
//   - reallyfreegeoip.org keeps returning 429 within its quota window (measured:
//     429 begins after about 5 consecutive requests, clears after about 60s of silence).
//   - geojs.io only carries ASN data, and some ranges (measured: 1.1.1.1) have no
//     country field at all -- that is missing data, not a fault, and must yield to
//     the next backend rather than trip the breaker.
//   - hackmyip.com returns 400/5xx outright when its server is failing (measured:
//     intermittent 400 during 2026-09).
//   - any backend may fail DNS resolution or time out on connect.
//
// So there are two independent states here: a long rate-limit window (a long
// cooldown, since hammering on only extends it) and a short unreachable window
// (one network hiccup should not keep a backend offline for long). While either is
// open the caller skips the whole batch and sends no requests at all.
type breaker struct {
	mu sync.Mutex
	// now is injectable so tests can advance the cooldown deterministically without waiting two real minutes.
	now               func() time.Time
	rateLimitThreshod int
	rateLimitCooldown time.Duration
	downThreshold     int
	downCooldown      time.Duration
	// consecutiveRateLimited / consecutiveHardFailure are the consecutive counts
	// since the last success. The two failure classes are mutually exclusive: a
	// success clears both, a 429 only affects the former, a timeout only the latter.
	consecutiveRateLimited int
	consecutiveHardFailure int
	rateLimitUntil         time.Time
	downUntil              time.Time
}

type breakerConfig struct {
	rateLimitThreshold int
	rateLimitCooldown  time.Duration
	downThreshold      int
	downCooldown       time.Duration
}

func newBreaker(cfg breakerConfig) *breaker {
	return &breaker{
		now:               time.Now,
		rateLimitThreshod: positiveInt(cfg.rateLimitThreshold, defaultRateLimitThreshold),
		rateLimitCooldown: positiveDuration(cfg.rateLimitCooldown, defaultRateLimitCooldown),
		downThreshold:     positiveInt(cfg.downThreshold, defaultDownThreshold),
		downCooldown:      positiveDuration(cfg.downCooldown, defaultDownCooldown),
	}
}

// breakerState describes why requests cannot currently be sent.
type breakerState struct {
	// Open means this backend should be skipped for the whole batch right now.
	Open bool
	// Remaining is the time left in the currently active window.
	Remaining time.Duration
	// Reason is the short label used in logs: rate_limit / down / "".
	Reason string
}

// state returns the current state. When open, the caller should skip the whole batch and send no requests.
func (b *breaker) state() breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stateLocked()
}

func (b *breaker) stateLocked() breakerState {
	now := b.now()
	// When a window expires, clear the counter along with it so the next round gets the full threshold.
	if !b.rateLimitUntil.IsZero() && !now.Before(b.rateLimitUntil) {
		b.rateLimitUntil = time.Time{}
		b.consecutiveRateLimited = 0
	}
	if !b.downUntil.IsZero() && !now.Before(b.downUntil) {
		b.downUntil = time.Time{}
		b.consecutiveHardFailure = 0
	}
	switch {
	case !b.rateLimitUntil.IsZero():
		return breakerState{Open: true, Remaining: b.rateLimitUntil.Sub(now), Reason: "rate_limit"}
	case !b.downUntil.IsZero():
		return breakerState{Open: true, Remaining: b.downUntil.Sub(now), Reason: "down"}
	default:
		return breakerState{}
	}
}

// record records the result of one lookup.
//
// statusOK clears both counters: the backend is alive. statusNotFound touches no
// counter -- it is a definite answer from the backend (missing data), the caller
// continues to the next backend, and it must not trip the breaker.
// statusRateLimited only advances the rate-limit counter; timeouts and transport
// errors only advance the unreachable counter.
func (b *breaker) record(status lookupStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stateLocked().Open {
		// Already cooling down; extending it is pointless: the cooldown itself means "send no requests for this while".
		return
	}
	switch status {
	case statusOK:
		b.consecutiveRateLimited = 0
		b.consecutiveHardFailure = 0
	case statusRateLimited:
		b.consecutiveHardFailure = 0
		b.consecutiveRateLimited++
		if b.consecutiveRateLimited >= b.rateLimitThreshod {
			b.rateLimitUntil = b.now().Add(b.rateLimitCooldown)
			b.consecutiveRateLimited = 0
		}
	case statusTimeout, statusFailed:
		b.consecutiveRateLimited = 0
		b.consecutiveHardFailure++
		if b.consecutiveHardFailure >= b.downThreshold {
			// Short window: the backend may have only hiccuped, so one network failure must not take it offline for minutes.
			b.downUntil = b.now().Add(b.downCooldown)
			b.consecutiveHardFailure = 0
		}
	}
}
