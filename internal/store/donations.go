package store

import (
	"context"

	"telesrv/internal/domain"
)

// DonationStore is the persistence boundary for crypto donations: the
// encrypted wallet seed, each user's deterministic deposit address, the
// chain/token registry, per-chain watcher cursors and the deposit ledger.
// See internal/app/donations for the wallet crypto and on-chain watcher
// that sit above this.
type DonationStore interface {
	// WalletSeed returns the single encrypted master seed row, if one has
	// been generated. found is false before the operator ever creates one.
	WalletSeed(ctx context.Context) (encryptedSeed, nonce []byte, found bool, err error)
	// CreateWalletSeed inserts the one-and-only wallet row. It fails with
	// domain.ErrDonationWalletAlreadyExists if a row already exists --
	// generating a second seed would silently orphan every address already
	// handed out under the first one.
	CreateWalletSeed(ctx context.Context, encryptedSeed, nonce []byte) error

	// DonationAddressForUser looks up a user's already-assigned address, if
	// any. It never allocates one -- see NextDonationAddressIndex.
	DonationAddressForUser(ctx context.Context, userID int64) (domain.DonationAddress, bool, error)
	// DonationUserByAddress is the watcher's reverse lookup: given an
	// on-chain "to" address (lowercase hex), which user (if any) owns it.
	DonationUserByAddress(ctx context.Context, address string) (userID int64, found bool, err error)
	// NextDonationAddressIndex reserves the next BIP32 derivation index.
	// Reserving does not persist an address by itself; the caller derives
	// one from this index and then calls InsertDonationAddress. A reserved
	// index that loses the insert race (see InsertDonationAddress) is
	// simply skipped -- gaps in the sequence are harmless.
	NextDonationAddressIndex(ctx context.Context) (int64, error)
	// InsertDonationAddress persists a newly derived address for a user
	// that doesn't have one yet. If another request already inserted one
	// for this user in the meantime (ON CONFLICT (user_id) DO NOTHING), it
	// returns THAT row instead -- the caller's freshly derived index/address
	// is discarded, not an error.
	InsertDonationAddress(ctx context.Context, userID, index int64, address string) (domain.DonationAddress, error)

	// EnabledDonationChains lists every chain row with enabled=true,
	// regardless of whether it's actually watchable yet (RPCURL may still
	// be empty) -- callers filter with domain.DonationChain.Watchable.
	EnabledDonationChains(ctx context.Context) ([]domain.DonationChain, error)
	// DonationChain looks up one chain by key, enabled or not.
	DonationChain(ctx context.Context, chainKey string) (domain.DonationChain, bool, error)
	// UpdateDonationChainConfig writes the operator-editable fields of one
	// chain (RPC/WS endpoints, enabled flag, confirmation depth, price
	// feed/manual rate) -- never its identity or native currency, which are
	// fixed at migration time. Returns domain.ErrDonationChainNotFound if
	// chainKey doesn't exist.
	UpdateDonationChainConfig(ctx context.Context, upd domain.DonationChainConfigUpdate) (domain.DonationChain, error)
	// DonationTokens lists the stablecoin rows configured for one chain,
	// including any with an empty ContractAddress the operator hasn't
	// filled in yet -- callers filter with domain.DonationToken.Watchable.
	DonationTokens(ctx context.Context, chainKey string) ([]domain.DonationToken, error)

	// DonationChainCursor is the last block height fully scanned for a
	// chain (0 before the watcher has run at all).
	DonationChainCursor(ctx context.Context, chainKey string) (int64, error)
	// SetDonationChainCursor advances the cursor after a block range has
	// been scanned end to end (deposits recorded, confirmations updated).
	SetDonationChainCursor(ctx context.Context, chainKey string, block int64) error

	// RecordDonationDeposit inserts a newly observed deposit
	// (chain_key, tx_hash, log_index) uniquely identifies it, so a watcher
	// that re-scans a block range it already covered is a no-op: created
	// is false and the existing row is returned unchanged.
	RecordDonationDeposit(ctx context.Context, deposit domain.DonationDeposit) (result domain.DonationDeposit, created bool, err error)
	// UpdateDonationDepositConfirmations refreshes a pending deposit's
	// confirmation count as later blocks arrive, advancing it to
	// DonationDepositConfirmed once it reaches the chain's required depth.
	// It is a no-op once the deposit has left the pending state (confirmed,
	// credited or orphaned), so a stale watcher pass can never regress it.
	UpdateDonationDepositConfirmations(ctx context.Context, chainKey, txHash string, logIndex int, confirmations int) (domain.DonationDeposit, error)
	// PendingDonationDeposits lists deposits still short of the chain's
	// required confirmation depth, for the watcher to refresh each pass.
	PendingDonationDeposits(ctx context.Context, chainKey string) ([]domain.DonationDeposit, error)
	// ConfirmedUncreditedDeposits lists deposits sitting in
	// DonationDepositConfirmed, ready to be priced and credited.
	ConfirmedUncreditedDeposits(ctx context.Context, chainKey string, limit int) ([]domain.DonationDeposit, error)
	// CreditDonationDeposit atomically credits the deposit's user with
	// stars (via the Stars ledger, in the same transaction) and marks the
	// deposit DonationDepositCredited. Idempotent: if the deposit is not in
	// DonationDepositConfirmed when called (already credited, or somehow
	// orphaned), credited is false and nothing is written twice.
	CreditDonationDeposit(ctx context.Context, depositID int64, usdValueMicros, stars int64, date int) (result domain.DonationDeposit, credited bool, err error)
	// UserDonationDeposits lists one user's deposit history, newest first,
	// for the premium bot's /deposit history and any future admin view.
	UserDonationDeposits(ctx context.Context, userID int64, limit int) ([]domain.DonationDeposit, error)
}
