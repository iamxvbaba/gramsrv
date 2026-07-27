package domain

import (
	"errors"
	"time"
)

// Composite account rating.
//
// The protocol surface is starsRating#1b0e4f07 (level, current_level_stars,
// stars, next_level_stars) carried by userFull.stars_rating, with
// userFull.stars_my_pending_rating reporting a score that has been earned but is
// not yet reflected in the visible level.
//
// Telegram derives that number from Stars alone. This server derives it from a
// weighted composite so the rating reflects behaviour rather than only wallet
// throughput: Stars received and spent, account activity, and moderation
// penalties. Every component is kept separately in the read model, so the admin
// panel can explain a level and recomputing one signal never discards another.
const (
	// MaxAccountRatingLevel bounds the projected level. Clients render the number
	// verbatim, so the bound only has to be stable, not large.
	MaxAccountRatingLevel = 50
	// accountRatingLevelUnit is the score required for level 1. Thresholds grow
	// quadratically from it: level n needs accountRatingLevelUnit * n^2.
	accountRatingLevelUnit = 100
	// MaxAccountRatingReasonLength matches the event ledger CHECK on reason.
	MaxAccountRatingReasonLength = 512
	// MaxAccountRatingActorLength matches the event ledger CHECK on actor.
	MaxAccountRatingActorLength = 128
	// MaxAccountRatingCommandKeyLength matches the idempotency CHECK.
	MaxAccountRatingCommandKeyLength = 128
)

var (
	// ErrAccountRatingNotFound reports a user with no rating row yet.
	ErrAccountRatingNotFound = errors.New("account rating not found")
	// ErrAccountRatingWeightsInvalid rejects a non-sensical weight set.
	ErrAccountRatingWeightsInvalid = errors.New("account rating weights invalid")
	// ErrAccountRatingAdjustmentInvalid rejects a malformed manual adjustment.
	ErrAccountRatingAdjustmentInvalid = errors.New("account rating adjustment invalid")
)

// AccountRatingEventKind is the contribution source of a ledger row. Only
// 'manual' rows survive a full recompute; the rest are audit trail.
type AccountRatingEventKind string

const (
	AccountRatingEventStars      AccountRatingEventKind = "stars"
	AccountRatingEventActivity   AccountRatingEventKind = "activity"
	AccountRatingEventModeration AccountRatingEventKind = "moderation"
	AccountRatingEventManual     AccountRatingEventKind = "manual"
	AccountRatingEventRecompute  AccountRatingEventKind = "recompute"
)

// Valid reports whether the kind is modelled.
func (k AccountRatingEventKind) Valid() bool {
	switch k {
	case AccountRatingEventStars, AccountRatingEventActivity, AccountRatingEventModeration,
		AccountRatingEventManual, AccountRatingEventRecompute:
		return true
	default:
		return false
	}
}

// AccountRating is the stored read model for one user.
type AccountRating struct {
	UserID            int64
	Level             int
	Stars             int64
	CurrentLevelStars int64
	// NextLevelStars is meaningful only when HasNextLevel is true; the TL flag is
	// omitted for a maxed-out account.
	NextLevelStars int64
	HasNextLevel   bool
	// Components are the explainable breakdown. PenaltyComponent is a
	// non-negative magnitude that is subtracted.
	StarsComponent    int64
	ActivityComponent int64
	PenaltyComponent  int64
	ManualComponent   int64
	// PendingStars is a score delta not yet applied to the visible level, with
	// PendingDate reporting when it becomes effective.
	PendingStars int64
	PendingDate  time.Time
	ComputedAt   time.Time
	UpdatedAt    time.Time
	Version      int64
}

// StarsRating is the TL-shaped projection of the visible rating.
type StarsRating struct {
	Level             int
	CurrentLevelStars int64
	Stars             int64
	NextLevelStars    int64
	HasNextLevelStars bool
}

// RatableAccount reports whether an account may carry a composite rating.
//
// The rating measures what an account did with Stars -- gifts bought, paid
// messages sent, activity, moderation history. Two kinds of account have no
// meaningful answer there and are excluded everywhere the rating is computed,
// seeded or projected:
//
//   - Bots. A bot does not buy gifts or send paid messages on its own behalf, so
//     its score would only ever be the flat account-age term.
//   - The built-in service accounts (the platform account, BotFather, @Stickers,
//     @ChatBot, the verification bots). They are infrastructure rather than
//     participants: a leaderboard entry for the platform account is noise, and a
//     level badge on it would claim something about transaction volume that means
//     nothing.
//
// Note that the platform account is not flagged is_bot, so the bot check alone
// does not cover it -- which is exactly how it ended up in the seeding pass.
func RatableAccount(userID int64, bot bool) bool {
	return userID > 0 && !bot && !IsSystemUserID(userID)
}

// StarsRating returns the userFull.stars_rating projection.
func (r AccountRating) StarsRating() StarsRating {
	return StarsRating{
		Level:             r.Level,
		CurrentLevelStars: r.CurrentLevelStars,
		Stars:             r.Stars,
		NextLevelStars:    r.NextLevelStars,
		HasNextLevelStars: r.HasNextLevel,
	}
}

// PendingStarsRating returns the userFull.stars_my_pending_rating projection and
// reports whether a pending record exists at all. The pending rating is the
// level the account will show once PendingDate passes, so it is projected
// through the same threshold policy as the visible one.
func (r AccountRating) PendingStarsRating() (StarsRating, bool) {
	if r.PendingStars == 0 || r.PendingDate.IsZero() {
		return StarsRating{}, false
	}
	total := r.Stars + r.PendingStars
	if total < 0 {
		total = 0
	}
	level, current, next, hasNext := AccountRatingLevelForStars(total)
	return StarsRating{
		Level:             level,
		CurrentLevelStars: current,
		Stars:             total,
		NextLevelStars:    next,
		HasNextLevelStars: hasNext,
	}, true
}

// AccountRatingWeights is the composite formula. All weights are integers so the
// score is exactly reproducible across a recompute and across store backends.
type AccountRatingWeights struct {
	// StarsReceivedPermille weighs Stars credited to the account (gifts,
	// reactions, paid messages received), in permille of the raw amount.
	StarsReceivedPermille int64
	// StarsSpentPermille weighs Stars the account spent. Spending is a weaker
	// signal than receiving, so the default is lower.
	StarsSpentPermille int64
	// PerMessageSent rewards sustained use.
	PerMessageSent int64
	// PerAccountAgeDay rewards account longevity.
	PerAccountAgeDay int64
	// PerGiftReceived rewards collectible gifts held.
	PerGiftReceived int64
	// PerModerationCase is the penalty for each upheld moderation case.
	PerModerationCase int64
	// ScamPenalty and FakePenalty are flat penalties for the peer flags.
	ScamPenalty int64
	FakePenalty int64
	// ActivityCap bounds the activity component so activity alone cannot
	// outweigh everything else. Zero means uncapped.
	ActivityCap int64
}

// DefaultAccountRatingWeights returns the shipped defaults. Stars dominate, which
// keeps the number close to the semantics Telegram clients present, while
// activity contributes a bounded floor and moderation subtracts.
func DefaultAccountRatingWeights() AccountRatingWeights {
	return AccountRatingWeights{
		StarsReceivedPermille: 1000,
		StarsSpentPermille:    250,
		PerMessageSent:        1,
		PerAccountAgeDay:      2,
		PerGiftReceived:       25,
		PerModerationCase:     150,
		ScamPenalty:           5000,
		FakePenalty:           5000,
		ActivityCap:           5000,
	}
}

// Validate rejects negative weights and an impossible cap.
func (w AccountRatingWeights) Validate() error {
	values := []int64{
		w.StarsReceivedPermille, w.StarsSpentPermille, w.PerMessageSent,
		w.PerAccountAgeDay, w.PerGiftReceived, w.PerModerationCase,
		w.ScamPenalty, w.FakePenalty, w.ActivityCap,
	}
	for _, v := range values {
		if v < 0 {
			return ErrAccountRatingWeightsInvalid
		}
	}
	return nil
}

// AccountRatingSignals is the raw snapshot gathered from the contributing
// sources for one user. It is deliberately a plain value: the same snapshot must
// produce the same score in a unit test and in production.
type AccountRatingSignals struct {
	UserID          int64
	StarsReceived   int64
	StarsSpent      int64
	MessagesSent    int64
	AccountAgeDays  int64
	GiftsReceived   int64
	ModerationCases int64
	Scam            bool
	Fake            bool
	// Manual is the sum of admin adjustments, carried across recomputes.
	Manual int64
}

// ComputeAccountRating turns a signal snapshot into the read model. The score is
// clamped at zero: penalties can erase a rating but never invert it, because the
// TL field is rendered as a non-negative count.
func ComputeAccountRating(signals AccountRatingSignals, weights AccountRatingWeights, now time.Time) AccountRating {
	if err := weights.Validate(); err != nil {
		weights = DefaultAccountRatingWeights()
	}
	starsComponent := permille(max64(signals.StarsReceived, 0), weights.StarsReceivedPermille) +
		permille(max64(signals.StarsSpent, 0), weights.StarsSpentPermille)

	activityComponent := max64(signals.MessagesSent, 0)*weights.PerMessageSent +
		max64(signals.AccountAgeDays, 0)*weights.PerAccountAgeDay +
		max64(signals.GiftsReceived, 0)*weights.PerGiftReceived
	if weights.ActivityCap > 0 && activityComponent > weights.ActivityCap {
		activityComponent = weights.ActivityCap
	}

	penalty := max64(signals.ModerationCases, 0) * weights.PerModerationCase
	if signals.Scam {
		penalty += weights.ScamPenalty
	}
	if signals.Fake {
		penalty += weights.FakePenalty
	}

	total := starsComponent + activityComponent + signals.Manual - penalty
	if total < 0 {
		total = 0
	}
	level, current, next, hasNext := AccountRatingLevelForStars(total)

	return AccountRating{
		UserID:            signals.UserID,
		Level:             level,
		Stars:             total,
		CurrentLevelStars: current,
		NextLevelStars:    next,
		HasNextLevel:      hasNext,
		StarsComponent:    starsComponent,
		ActivityComponent: activityComponent,
		PenaltyComponent:  penalty,
		ManualComponent:   signals.Manual,
		ComputedAt:        now,
		UpdatedAt:         now,
		Version:           1,
	}
}

// AccountRatingLevelThreshold returns the score needed to reach the given level.
// Level 0 needs nothing; growth is quadratic so early levels arrive quickly and
// later ones stay meaningful.
func AccountRatingLevelThreshold(level int) int64 {
	if level <= 0 {
		return 0
	}
	if level > MaxAccountRatingLevel {
		level = MaxAccountRatingLevel
	}
	n := int64(level)
	return accountRatingLevelUnit * n * n
}

// AccountRatingLevelForStars maps a score onto the level and the surrounding
// thresholds. hasNext is false at MaxAccountRatingLevel, where the TL flag is
// omitted entirely.
func AccountRatingLevelForStars(stars int64) (level int, currentLevelStars int64, nextLevelStars int64, hasNext bool) {
	if stars < 0 {
		stars = 0
	}
	level = 0
	for candidate := 1; candidate <= MaxAccountRatingLevel; candidate++ {
		if stars < AccountRatingLevelThreshold(candidate) {
			break
		}
		level = candidate
	}
	currentLevelStars = AccountRatingLevelThreshold(level)
	if level >= MaxAccountRatingLevel {
		return level, currentLevelStars, 0, false
	}
	return level, currentLevelStars, AccountRatingLevelThreshold(level + 1), true
}

// ResolveAccountRatingPending decides whether a freshly computed score becomes
// visible immediately or is parked as pending.
//
// A score that dropped is applied at once -- a penalty must not sit behind a
// delay. A score that grew is parked until delay has elapsed, which is what
// stars_my_pending_rating exists to express; once the parked window has passed
// the pending delta is folded into the visible rating.
func ResolveAccountRatingPending(prev, computed AccountRating, delay time.Duration, now time.Time) AccountRating {
	out := computed
	out.Version = prev.Version + 1
	if out.Version <= 0 {
		out.Version = 1
	}
	if delay <= 0 || prev.UserID == 0 {
		return out
	}
	if computed.Stars <= prev.Stars {
		return out
	}
	// A previously parked delta whose date has arrived is applied now.
	if prev.PendingStars != 0 && !prev.PendingDate.IsZero() && !now.Before(prev.PendingDate) {
		return out
	}
	pendingSince := prev.PendingDate
	if prev.PendingStars == 0 || pendingSince.IsZero() {
		pendingSince = now.Add(delay)
	}
	visible := prev
	visible.StarsComponent = computed.StarsComponent
	visible.ActivityComponent = computed.ActivityComponent
	visible.PenaltyComponent = computed.PenaltyComponent
	visible.ManualComponent = computed.ManualComponent
	visible.PendingStars = computed.Stars - prev.Stars
	visible.PendingDate = pendingSince
	visible.ComputedAt = now
	visible.UpdatedAt = now
	visible.Version = out.Version
	return visible
}

// AccountRatingEvent is one contribution ledger row.
type AccountRatingEvent struct {
	ID         int64
	UserID     int64
	Kind       AccountRatingEventKind
	Amount     int64
	Reason     string
	Actor      string
	CommandKey string
	CreatedAt  time.Time
}

// AdjustAccountRatingRequest is an operator adjustment to the manual component.
type AdjustAccountRatingRequest struct {
	UserID     int64
	Amount     int64
	Reason     string
	Actor      string
	CommandKey string
}

// Validate rejects a no-op or oversized adjustment.
func (r AdjustAccountRatingRequest) Validate() error {
	if r.UserID <= 0 || r.Amount == 0 {
		return ErrAccountRatingAdjustmentInvalid
	}
	if len(r.Reason) > MaxAccountRatingReasonLength {
		return ErrAccountRatingAdjustmentInvalid
	}
	if len(r.Actor) > MaxAccountRatingActorLength {
		return ErrAccountRatingAdjustmentInvalid
	}
	if len(r.CommandKey) > MaxAccountRatingCommandKeyLength {
		return ErrAccountRatingAdjustmentInvalid
	}
	return nil
}

// AccountRatingFilter bounds an admin listing query.
type AccountRatingFilter struct {
	MinLevel int
	UserID   int64
	BeforeID int64
	Limit    int
}

func permille(value, weight int64) int64 {
	if value <= 0 || weight <= 0 {
		return 0
	}
	return value * weight / 1000
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
