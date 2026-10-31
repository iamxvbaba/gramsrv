package store

import (
	"context"

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
}

// StarsPurchaseStore owns fiat self-topup, friend-gift and giveaway-launch
// aggregates. Successful settlement commits the affected ledger/message box
// atomically; exact form retries return the original receipt.
type StarsPurchaseStore interface {
	IssueStarsPurchaseForm(context.Context, domain.StarsPurchaseForm) (domain.StarsPurchaseForm, error)
	PurchaseStars(context.Context, domain.StarsPurchaseRequest) (domain.StarsPurchaseResult, error)
}

// StarsGiveawayStore exposes the viewer-specific state of launch cards without
// forcing lightweight purchase-store fakes to implement the read model.
type StarsGiveawayStore interface {
	GetStarsGiveawayInfo(context.Context, int64, int64, int, int) (domain.StarsGiveawayInfo, error)
}

// StarsAirdropStore is the optional capability to bulk-credit "all real users" (the admin's
// GrantStarsAll). Only the postgres backend implements it; the in-memory backend is used for
// fast RPC tests and need not simulate a full users table.
type StarsAirdropStore interface {
	// CountCreditAllUsers returns the number of candidate accounts for bulk crediting (users table is_bot=false, excluding system accounts).
	CountCreditAllUsers(ctx context.Context) (int64, error)
	// CreditAll credits amount to all real users in a single transaction (new accounts get a row
	// with granted=true; existing accounts accumulate and also set granted=true to absorb the
	// starting grant), writing one journal entry per person; returns the affected account count.
	CreditAll(ctx context.Context, amount int64, reason domain.StarsTransactionReason, date int, title, desc string) (int64, error)
}
