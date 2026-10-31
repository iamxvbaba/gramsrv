// Package stars implements the local Stars ledger application service: balance queries,
// credit/debit, transaction pagination, and the "lazy grant on first read" starting
// balance (idempotent via the stars_balances.granted boolean, covering both old and new
// accounts with no backfill migration). Atomicity comes from store transactions; this
// layer only does validation + grant policy.
package stars

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service is the Stars ledger application service.
type Service struct {
	store         store.StarsStore
	purchaseStore store.StarsPurchaseStore
	grantAmount   int64
	now           func() time.Time
}

// Option configures Service.
type Option func(*Service)

// WithStartingGrant sets the starting balance granted lazily on first read; amount<=0 disables the automatic grant.
func WithStartingGrant(amount int64) Option {
	return func(s *Service) { s.grantAmount = amount }
}

// WithPurchaseStore enables the atomic fiat Stars checkout aggregate.
func WithPurchaseStore(st store.StarsPurchaseStore) Option {
	return func(s *Service) { s.purchaseStore = st }
}

// WithClock injects the clock (for tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// NewService creates the Stars ledger service, with domain.DefaultStarsStartingGrant as the default starting grant.
func NewService(st store.StarsStore, opts ...Option) *Service {
	s := &Service{store: st, grantAmount: domain.DefaultStarsStartingGrant, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ensureGranted lazily applies the starting grant once (idempotent) and returns the latest balance.
func (s *Service) ensureGranted(ctx context.Context, userID int64) (domain.StarsBalance, error) {
	if s.grantAmount > 0 {
		bal, _, err := s.store.EnsureGrant(ctx, userID, s.grantAmount, int(s.now().Unix()))
		return bal, err
	}
	return s.store.GetBalance(ctx, userID)
}

// GetBalance returns the account balance, lazily granting the starting balance on first read.
func (s *Service) GetBalance(ctx context.Context, userID int64) (domain.StarsBalance, error) {
	return s.ensureGranted(ctx, userID)
}

// Credit credits the account (amount>0).
func (s *Service) Credit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error) {
	if amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	return s.store.Credit(ctx, userID, amount, reason, peer, int(s.now().Unix()), title, desc)
}

// Debit debits the account (amount>0); an insufficient balance returns domain.ErrStarsInsufficient.
// It first ensures the starting grant has been applied, so a new account is not misjudged as
// insufficient before it has read its balance for the first time.
func (s *Service) Debit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error) {
	if amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	if _, err := s.ensureGranted(ctx, userID); err != nil {
		return domain.StarsBalance{}, err
	}
	return s.store.Debit(ctx, userID, amount, reason, peer, int(s.now().Unix()), title, desc)
}

// CountStarsAllUsers returns the number of real user candidates for bulk crediting (for a
// dry-run preview); if the underlying store does not support it, returns domain.ErrStarsAirdropUnsupported.
func (s *Service) CountStarsAllUsers(ctx context.Context) (int64, error) {
	airdrop, ok := s.store.(store.StarsAirdropStore)
	if !ok {
		return 0, domain.ErrStarsAirdropUnsupported
	}
	return airdrop.CountCreditAllUsers(ctx)
}

// GrantStarsAll bulk-credits all real users (the admin "send to everyone"); returns the
// number of affected accounts. If the underlying store does not support it, returns
// domain.ErrStarsAirdropUnsupported.
func (s *Service) GrantStarsAll(ctx context.Context, amount int64, title, desc string) (int64, error) {
	if amount <= 0 {
		return 0, domain.ErrStarsInvalidAmount
	}
	airdrop, ok := s.store.(store.StarsAirdropStore)
	if !ok {
		return 0, domain.ErrStarsAirdropUnsupported
	}
	return airdrop.CreditAll(ctx, amount, domain.StarsReasonAdjust, int(s.now().Unix()), title, desc)
}

// ListTransactions does keyset pagination by direction and order, lazily granting on first read.
func (s *Service) ListTransactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error) {
	query, err := domain.NormalizeStarsTransactionQuery(query)
	if err != nil {
		return domain.StarsTransactionPage{}, err
	}
	if _, err := s.ensureGranted(ctx, userID); err != nil {
		return domain.StarsTransactionPage{}, err
	}
	return s.store.ListTransactions(ctx, userID, query)
}

// IssuePurchaseForm persists a short-lived, exact checkout intent.
func (s *Service) IssuePurchaseForm(ctx context.Context, form domain.StarsPurchaseForm) (domain.StarsPurchaseForm, error) {
	if s.purchaseStore == nil || !validPurchaseForm(form) {
		return domain.StarsPurchaseForm{}, domain.ErrStarsPurchaseFormInvalid
	}
	return s.purchaseStore.IssueStarsPurchaseForm(ctx, form)
}

// Purchase settles one exact persisted form. Package validation remains at
// the RPC boundary as well, while the store revalidates the persisted tuple
// under lock before performing any write.
func (s *Service) Purchase(ctx context.Context, req domain.StarsPurchaseRequest) (domain.StarsPurchaseResult, error) {
	if s.purchaseStore == nil || req.FormID == 0 || req.Date <= 0 || !validPurchaseCommand(req.StarsPurchaseForm) {
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormInvalid
	}
	return s.purchaseStore.PurchaseStars(ctx, req)
}

// GetGiveawayInfo resolves one launch card from the same aggregate that
// persisted it. date is supplied by the RPC clock for deterministic tests.
func (s *Service) GetGiveawayInfo(ctx context.Context, viewerUserID, channelID int64, messageID, date int) (domain.StarsGiveawayInfo, error) {
	reader, ok := s.purchaseStore.(store.StarsGiveawayStore)
	if !ok || viewerUserID <= 0 || channelID <= 0 || messageID <= 0 || date <= 0 {
		return domain.StarsGiveawayInfo{}, domain.ErrStarsPurchaseFormInvalid
	}
	return reader.GetStarsGiveawayInfo(ctx, viewerUserID, channelID, messageID, date)
}

func validPurchaseForm(form domain.StarsPurchaseForm) bool {
	return validPurchaseCommand(form) && form.IssuedAt > 0 && form.ExpiresAt == form.IssuedAt+600
}

func validPurchaseCommand(form domain.StarsPurchaseForm) bool {
	if !form.Kind.Valid() || form.BuyerUserID <= 0 || form.Stars <= 0 || form.Amount <= 0 || form.Currency == "" {
		return false
	}
	switch form.Kind {
	case domain.StarsPurchaseTopup:
		return form.Giveaway == nil && form.RecipientUserID == 0 && ((form.SpendPurposePeer == domain.Peer{}) ||
			((form.SpendPurposePeer.Type == domain.PeerTypeUser || form.SpendPurposePeer.Type == domain.PeerTypeChannel) && form.SpendPurposePeer.ID > 0))
	case domain.StarsPurchaseGift:
		return form.Giveaway == nil && form.RecipientUserID > 0 && form.BuyerUserID != form.RecipientUserID && form.SpendPurposePeer == (domain.Peer{})
	case domain.StarsPurchaseGiveaway:
		g := form.Giveaway
		return form.RecipientUserID == 0 && form.SpendPurposePeer == (domain.Peer{}) && g != nil &&
			g.BoostPeer.Type == domain.PeerTypeChannel && g.BoostPeer.ID > 0 && g.RandomID != 0 &&
			g.UntilDate > 0 && g.Users > 0 && g.PerUserStars > 0 &&
			int64(g.Users) <= form.Stars/g.PerUserStars && int64(g.Users)*g.PerUserStars == form.Stars
	default:
		return false
	}
}
