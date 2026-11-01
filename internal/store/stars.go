package store

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

// StarsStore persists the local Stars ledger: per-user balance + transaction journal.
// Debit/credit/grant must each complete atomically in a single transaction (balance and
// journal must not drift).
type StarsStore interface {
	// GetBalance returns the account's current balance; with no row it returns zero values (Balance 0, Granted false).
	GetBalance(ctx context.Context, userID int64) (domain.StarsBalance, error)
	// EnsureGrant idempotently applies a one-time starting grant: only when never granted does it
	// credit amount, set granted=true, and write one grant journal entry, all in a single
	// transaction. Returns the latest balance + whether this call actually granted.
	EnsureGrant(ctx context.Context, userID, amount int64, date int) (domain.StarsBalance, bool, error)
	// Credit credits the account (amount>0) and writes the journal entry (amount=+x) in a single transaction; the balance row is created if absent.
	Credit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) (domain.StarsBalance, error)
	// Debit debits (amount>0) after a SELECT ... FOR UPDATE sufficiency check in a single
	// transaction, and writes the journal entry (amount=-x). Insufficient balance returns
	// domain.ErrStarsInsufficient.
	Debit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) (domain.StarsBalance, error)
	// ListTransactions paginates by keyset over direction and order, returning one page of the journal + the current balance.
	ListTransactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error)
	// Client-visible ledger mutations use these mandatory aggregate methods so
	// the balance, transaction row and absolute delivery fact commit together.
	CreditWithDelivery(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string, effects DeliveryEffectsBuilder[domain.StarsBalance]) (domain.StarsBalance, error)
	DebitWithDelivery(ctx context.Context, userID, amount, startingGrant int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string, effects DeliveryEffectsBuilder[domain.StarsBalance]) (domain.StarsBalance, error)
}

func ValidateStarsBalanceDeliveryEffects(userID int64, effects []DeliveryEffect) error {
	if userID <= 0 || len(effects) != 1 {
		return fmt.Errorf("stars balance delivery requires exactly one target effect")
	}
	effect := effects[0]
	if effect.Kind != DeliveryEffectAbsolute || effect.TargetUserID != userID {
		return fmt.Errorf("stars balance delivery target does not match ledger owner")
	}
	return effect.Validate()
}

// StarsPurchaseStore owns fiat self-topup, friend-gift and giveaway-launch
// aggregates. Successful settlement commits the affected ledger/message box
// atomically; exact form retries return the original receipt.
type StarsPurchaseStore interface {
	IssueStarsPurchaseForm(context.Context, domain.StarsPurchaseForm) (domain.StarsPurchaseForm, error)
	PurchaseStarsWithDelivery(context.Context, domain.StarsPurchaseRequest, DeliveryEffectsBuilder[domain.StarsPurchaseResult]) (domain.StarsPurchaseResult, error)
	GetStarsGiveawayInfo(context.Context, int64, int64, int, int) (domain.StarsGiveawayInfo, error)
}

// ValidateStarsPurchaseDeliveryEffects enforces the exact non-PTS balance
// owner affected by a checkout. Giveaways do not mutate a user ledger and
// therefore must not manufacture a balance delivery.
func ValidateStarsPurchaseDeliveryEffects(result domain.StarsPurchaseResult, effects []DeliveryEffect) error {
	if result.Balance.UserID == 0 {
		if len(effects) != 0 {
			return fmt.Errorf("stars purchase without user ledger mutation requires no delivery effects")
		}
		return nil
	}
	return ValidateStarsBalanceDeliveryEffects(result.Balance.UserID, effects)
}
