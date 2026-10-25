package domain

import (
	"errors"
	"time"
)

// Crypto donations: one deterministic BIP32 deposit address per user, the
// same address on every EVM-compatible chain (same secp256k1 curve and
// derivation path; only the RPC endpoint and token contracts differ per
// chain). A background watcher per enabled chain detects incoming native
// currency or stablecoin transfers to a user's address and credits Stars
// once the deposit reaches the chain's configured confirmation depth. See
// docs/donations.md.

var (
	ErrDonationWalletNotConfigured = errors.New("donations: wallet not configured")
	ErrDonationWalletAlreadyExists = errors.New("donations: wallet already exists")
	ErrDonationChainDisabled       = errors.New("donations: chain disabled or misconfigured")
	ErrDonationChainNotFound       = errors.New("donations: chain not found")
	ErrDonationDepositInvalid      = errors.New("donations: deposit invalid")
)

// DonationDepositStatus is the deposit's lifecycle: pending (seen, not yet
// at the required confirmation depth) -> confirmed (depth reached, about to
// be priced and credited) -> credited (Stars granted; terminal) or orphaned
// (the block it was in was reorganized out of the canonical chain before
// reaching depth; terminal, never credited).
type DonationDepositStatus string

const (
	DonationDepositPending   DonationDepositStatus = "pending"
	DonationDepositConfirmed DonationDepositStatus = "confirmed"
	DonationDepositCredited  DonationDepositStatus = "credited"
	DonationDepositOrphaned  DonationDepositStatus = "orphaned"
)

// DonationChain is one configured EVM-compatible network. Disabled chains
// (Enabled false, typically because RPCURL is still empty) are never
// watched. ManualUSDRateMicros is USD per one whole native unit, scaled by
// 1e6, used when PriceFeedAddress has no reachable Chainlink aggregator
// (e.g. local Ganache, or before an operator sets one).
type DonationChain struct {
	Key                   string
	Name                  string
	ChainID               int64
	RPCURL                string
	WSURL                 string
	NativeSymbol          string
	NativeDecimals        int
	ConfirmationsRequired int
	PriceFeedAddress      string
	ManualUSDRateMicros   int64
	Enabled               bool
}

// Valid reports whether the chain is enabled and has enough configuration
// to actually be watched.
func (c DonationChain) Valid() bool {
	return c.Key != "" && c.ChainID > 0 && c.NativeDecimals > 0 && c.ConfirmationsRequired > 0
}

// Watchable reports whether the chain is enabled and has an RPC endpoint to
// connect to -- the two things a migration-seeded placeholder row lacks
// until an operator fills them in.
func (c DonationChain) Watchable() bool {
	return c.Valid() && c.Enabled && c.RPCURL != ""
}

// DonationToken is a stablecoin contract watched on one chain. An empty
// ContractAddress means the operator hasn't filled it in yet (e.g. no
// citable official Sepolia USDT deployment, or Ganache's mock not deployed
// yet) -- the watcher skips it rather than watching a zero address.
type DonationToken struct {
	ChainKey        string
	Symbol          string
	ContractAddress string
	Decimals        int
}

// Watchable reports whether this token has a contract address to watch.
func (t DonationToken) Watchable() bool {
	return t.ContractAddress != ""
}

// DonationAddress is a user's permanent, chain-agnostic deposit address.
type DonationAddress struct {
	UserID          int64
	DerivationIndex int64
	Address         string
	CreatedAt       time.Time
}

// DonationDeposit is one detected on-chain transfer to a user's deposit
// address, tracked from first sight through crediting (or orphaning on a
// reorg). TokenSymbol is empty for a native-currency deposit; LogIndex is
// -1 for one (there is no ERC-20 Transfer log to index).
type DonationDeposit struct {
	ID             int64
	UserID         int64
	ChainKey       string
	TokenSymbol    string
	TxHash         string
	LogIndex       int
	BlockNumber    int64
	AmountRaw      string // decimal string; arbitrary precision, smallest unit (wei etc.)
	USDValueMicros int64
	StarsCredited  int64
	Status         DonationDepositStatus
	Confirmations  int
	DetectedAt     time.Time
	CreditedAt     time.Time
}

// DonationChainConfigUpdate is the operator-editable subset of a
// DonationChain: everything except the chain's identity (Key/Name/ChainID)
// and its currency's fixed properties (NativeSymbol/NativeDecimals), which
// describe the network itself rather than how this server watches it.
type DonationChainConfigUpdate struct {
	ChainKey              string
	RPCURL                string
	WSURL                 string
	ConfirmationsRequired int
	PriceFeedAddress      string
	ManualUSDRateMicros   int64
	Enabled               bool
}

// DonationCreditNotice is everything a "your deposit was credited" chat
// notification needs, handed from the watcher (which knows the chain/asset
// it just priced) to whatever sends the actual message (app/bots.Service,
// via the donations.CreditNotifier port) without either side depending on
// the other's package.
type DonationCreditNotice struct {
	UserID        int64
	ChainName     string
	AssetSymbol   string
	AssetDecimals int
	Deposit       DonationDeposit // already Status == DonationDepositCredited
}
