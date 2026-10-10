package donations

import (
	"context"
	"fmt"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// CreditNotifier receives one notice per deposit the watcher just credited,
// so whatever owns the actual chat channel (app/bots.Service and its
// built-in @premiumbot, which satisfies this as-is via
// NotifyDonationCredited) can tell the user their Stars landed. A narrow
// port rather than a concrete dependency for the usual reason: donations
// must not need to import bots to send a chat message, and bots already
// depends on donations (donationsSource) the other way for /deposit, so a
// direct import back would cycle.
type CreditNotifier interface {
	NotifyDonationCredited(ctx context.Context, notice domain.DonationCreditNotice)
}

// Service is the application boundary for crypto donations: wallet
// lifecycle, per-user address assignment, and (in watcher.go) the
// on-chain watcher that detects and credits deposits. It holds the
// decrypted wallet in memory only -- never persists it anywhere itself.
type Service struct {
	store    store.DonationStore
	key      EncryptionKey
	wallet   *Wallet
	notifier CreditNotifier
}

// SetNotifier wires the chat notification sent after each deposit is
// credited. Without a call to this (a nil notifier, the zero value),
// crediting still happens exactly the same -- the user just isn't told
// about it until they check /deposit or their Stars balance themselves.
func (s *Service) SetNotifier(n CreditNotifier) {
	if s == nil {
		return
	}
	s.notifier = n
}

// NewService constructs the service with an already-resolved encryption
// key (see LoadOrGenerateEncryptionKey -- the normal path -- or
// ParseEncryptionKey for an operator-supplied override) and, if a wallet
// already exists in the store, loads and decrypts it immediately. If none
// exists yet, the service comes back not Ready(); call EnsureWallet to
// provision one -- cmd/telesrv does this automatically at startup so
// donations work with zero manual setup, see docs/donations.md.
func NewService(ctx context.Context, st store.DonationStore, key EncryptionKey) (*Service, error) {
	s := &Service{store: st, key: key}
	seed, nonce, found, err := st.WalletSeed(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return s, nil
	}
	mnemonic, err := Open(key, seed, nonce)
	if err != nil {
		return nil, err
	}
	wallet, err := NewWallet(mnemonic)
	if err != nil {
		return nil, err
	}
	s.wallet = wallet
	return s, nil
}

// EnsureWallet generates and persists a new encrypted wallet the first time
// donations run against a store that doesn't have one yet. cmd/telesrv
// calls this automatically at startup whenever !Ready() -- see
// LoadOrGenerateEncryptionKey's doc comment for the matching "no manual
// step" reasoning on the key file. It's still idempotent/safe to call by
// hand: calling it again once a wallet exists returns
// domain.ErrDonationWalletAlreadyExists rather than silently doing nothing,
// so nothing mistakes "already set up" for "just set up, please save this
// phrase".
func (s *Service) EnsureWallet(ctx context.Context) (mnemonic string, err error) {
	if s == nil || s.store == nil {
		return "", domain.ErrDonationWalletNotConfigured
	}
	if s.wallet != nil {
		return "", domain.ErrDonationWalletAlreadyExists
	}
	mnemonic, err = GenerateMnemonic()
	if err != nil {
		return "", err
	}
	ciphertext, nonce, err := Seal(s.key, mnemonic)
	if err != nil {
		return "", err
	}
	if err := s.store.CreateWalletSeed(ctx, ciphertext, nonce); err != nil {
		return "", err
	}
	wallet, err := NewWallet(mnemonic)
	if err != nil {
		return "", err
	}
	s.wallet = wallet
	return mnemonic, nil
}

// Ready reports whether the service has a usable, decrypted wallet.
func (s *Service) Ready() bool {
	return s != nil && s.wallet != nil
}

// AddressForUser returns the user's permanent donation deposit address,
// assigning one on first call. The address is identical across every
// enabled EVM chain -- there is exactly one per user, full stop.
func (s *Service) AddressForUser(ctx context.Context, userID int64) (string, error) {
	if s == nil || s.store == nil {
		return "", domain.ErrDonationWalletNotConfigured
	}
	if userID <= 0 {
		return "", fmt.Errorf("donations: invalid user id %d", userID)
	}
	if existing, found, err := s.store.DonationAddressForUser(ctx, userID); err != nil {
		return "", err
	} else if found {
		return existing.Address, nil
	}
	if !s.Ready() {
		return "", domain.ErrDonationWalletNotConfigured
	}
	index, err := s.store.NextDonationAddressIndex(ctx)
	if err != nil {
		return "", err
	}
	address, err := s.wallet.DeriveAddress(index)
	if err != nil {
		return "", err
	}
	row, err := s.store.InsertDonationAddress(ctx, userID, index, address)
	if err != nil {
		return "", err
	}
	return row.Address, nil
}

// UpdateChainConfig is the admin panel's write path for a chain's
// operator-editable settings (RPC/WS endpoint, enabled flag, confirmation
// depth, price feed/manual rate) -- see internal/admin.Service.UpdateDonationChain,
// the only caller. It never touches the chain's identity or native currency.
func (s *Service) UpdateChainConfig(ctx context.Context, upd domain.DonationChainConfigUpdate) (domain.DonationChain, error) {
	if s == nil || s.store == nil {
		return domain.DonationChain{}, domain.ErrDonationWalletNotConfigured
	}
	if strings.TrimSpace(upd.ChainKey) == "" {
		return domain.DonationChain{}, domain.ErrDonationChainNotFound
	}
	if upd.ConfirmationsRequired <= 0 {
		return domain.DonationChain{}, fmt.Errorf("donations: confirmations_required must be positive")
	}
	return s.store.UpdateDonationChainConfig(ctx, upd)
}

// EnabledChains lists every chain configured as enabled, whether or not
// it's actually watchable yet (see domain.DonationChain.Watchable).
func (s *Service) EnabledChains(ctx context.Context) ([]domain.DonationChain, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return s.store.EnabledDonationChains(ctx)
}

// UserDeposits lists a user's donation history, newest first.
func (s *Service) UserDeposits(ctx context.Context, userID int64, limit int) ([]domain.DonationDeposit, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return s.store.UserDonationDeposits(ctx, userID, limit)
}

// FormatAssetAmount renders a raw smallest-unit amount (wei, or an ERC-20's
// base units) as a human string with the asset's actual decimal point, for
// chat replies and logs -- never for anything that feeds back into pricing
// math, which stays in raw big.Int form throughout (see pricing.go).
func FormatAssetAmount(amountRaw string, decimals int) string {
	if decimals <= 0 || len(amountRaw) == 0 {
		return amountRaw
	}
	neg := strings.HasPrefix(amountRaw, "-")
	digits := strings.TrimPrefix(amountRaw, "-")
	for len(digits) <= decimals {
		digits = "0" + digits
	}
	intPart, fracPart := digits[:len(digits)-decimals], strings.TrimRight(digits[len(digits)-decimals:], "0")
	out := intPart
	if fracPart != "" {
		out += "." + fracPart
	}
	if neg {
		out = "-" + out
	}
	return out
}
