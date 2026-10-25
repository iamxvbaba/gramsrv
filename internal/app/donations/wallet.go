// Package donations implements crypto deposit-to-Stars: one deterministic
// BIP32 address per user (same address on every EVM-compatible chain), a
// background watcher per enabled chain that detects native-currency and
// ERC-20 (USDT/USDC) transfers to those addresses, and automatic Stars
// crediting once a deposit reaches the chain's confirmation depth. See
// docs/donations.md for the full design and deployment notes.
package donations

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
	"github.com/tyler-smith/go-bip39"

	"telesrv/internal/domain"
)

// donationDerivationPath is BIP44 for Ethereum (coin type 60), account 0,
// external chain, address index = the reserved donation_address_index_seq
// value. Every EVM-compatible chain (mainnet, Base, Polygon, BSC, Sepolia,
// Ganache) shares this same curve and path, so one derived address is
// simultaneously valid -- and is watched -- on all of them.
const donationDerivationPathFormat = "m/44'/60'/0'/0/%d"

// maxDerivationIndex keeps every index within BIP32's unhardened uint32
// range with headroom; Telegram user IDs alone could theoretically exceed
// it someday, which is exactly why addresses are keyed by a compact
// sequence (internal/store/postgres donation_address_index_seq) rather than
// the raw user ID.
const maxDerivationIndex = 1<<31 - 1

// Wallet holds a decrypted BIP39 mnemonic in memory only and derives
// per-user addresses on demand; the private keys it implies are never
// persisted or transmitted anywhere. Deriving an address is a pure,
// local computation -- no signing, no network call.
type Wallet struct {
	hd *hdwallet.Wallet
}

// GenerateMnemonic creates a new random 12-word BIP39 mnemonic (128 bits of
// entropy). Call this exactly once, the first time a server ever enables
// donations -- see Seal for what happens to the result afterward.
func GenerateMnemonic() (string, error) {
	entropy, err := bip39.NewEntropy(128)
	if err != nil {
		return "", fmt.Errorf("donations: generate wallet entropy: %w", err)
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return "", fmt.Errorf("donations: generate wallet mnemonic: %w", err)
	}
	return mnemonic, nil
}

// NewWallet loads a wallet from an already-decrypted mnemonic.
func NewWallet(mnemonic string) (*Wallet, error) {
	hd, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return nil, fmt.Errorf("donations: load wallet: %w", err)
	}
	return &Wallet{hd: hd}, nil
}

// DeriveAddress returns the lowercase hex address at
// m/44'/60'/0'/0/{index}. Deterministic: the same index always yields the
// same address for the life of this mnemonic.
func (w *Wallet) DeriveAddress(index int64) (string, error) {
	if w == nil || w.hd == nil {
		return "", domain.ErrDonationWalletNotConfigured
	}
	if index < 0 || index > maxDerivationIndex {
		return "", fmt.Errorf("donations: derivation index %d out of range", index)
	}
	path, err := hdwallet.ParseDerivationPath(fmt.Sprintf(donationDerivationPathFormat, index))
	if err != nil {
		return "", fmt.Errorf("donations: parse derivation path: %w", err)
	}
	account, err := w.hd.Derive(path, false)
	if err != nil {
		return "", fmt.Errorf("donations: derive address: %w", err)
	}
	return normalizeAddress(account.Address.Hex()), nil
}

// PrivateKeyHex returns the raw private key at the given index, hex
// encoded with no 0x prefix, ready for ethclient/bind signing. This is the
// ONLY function in the package that exposes key material, and it is never
// called from the watcher's normal deposit-detection path -- only from an
// operator-triggered sweep. See the "manual sweep, never automatic"
// custody note in docs/donations.md before wiring this into anything that
// runs unattended.
func (w *Wallet) PrivateKeyHex(index int64) (string, error) {
	if w == nil || w.hd == nil {
		return "", domain.ErrDonationWalletNotConfigured
	}
	if index < 0 || index > maxDerivationIndex {
		return "", fmt.Errorf("donations: derivation index %d out of range", index)
	}
	path, err := hdwallet.ParseDerivationPath(fmt.Sprintf(donationDerivationPathFormat, index))
	if err != nil {
		return "", fmt.Errorf("donations: parse derivation path: %w", err)
	}
	account, err := w.hd.Derive(path, false)
	if err != nil {
		return "", fmt.Errorf("donations: derive account: %w", err)
	}
	key, err := w.hd.PrivateKeyBytes(account)
	if err != nil {
		return "", fmt.Errorf("donations: export private key: %w", err)
	}
	return hex.EncodeToString(key), nil
}

// normalizeAddress lowercases an address for storage/comparison. EVM
// addresses are case-insensitive on chain -- the mixed-case "checksum" form
// is purely a client-side typo-catching convention -- so the database and
// every watcher comparison in this package work in lowercase throughout;
// only a client-facing display layer would need to re-checksum.
func normalizeAddress(address string) string {
	out := make([]byte, len(address))
	for i := 0; i < len(address); i++ {
		c := address[i]
		if c >= 'A' && c <= 'F' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// EncryptionKey is the AES-256-GCM key that encrypts the wallet mnemonic at
// rest. Never stored in the database -- losing it means losing the ability to
// derive new addresses or sweep funds, exactly like losing a hardware wallet's
// seed phrase.
type EncryptionKey [32]byte

// ParseEncryptionKey parses a raw 64-hex-char encryption key value.
func ParseEncryptionKey(hexKey string) (EncryptionKey, error) {
	var key EncryptionKey
	raw, err := hex.DecodeString(hexKey)
	if err != nil || len(raw) != len(key) {
		return key, fmt.Errorf("donations: wallet key must be %d hex characters (%d bytes)", len(key)*2, len(key))
	}
	copy(key[:], raw)
	return key, nil
}

// LoadEncryptionKeyFile reads the shared wallet key named by the Core YAML
// donations.wallet_key_file. v2 (decision D7) never generates the key: every
// Core instance must decrypt the same mnemonic, so a per-instance generated
// file would silently fork the wallet. The file holds either the 32 raw key
// bytes or 64 hex characters (surrounding whitespace ignored).
func LoadEncryptionKeyFile(path string) (EncryptionKey, error) {
	var key EncryptionKey
	if path == "" {
		return key, fmt.Errorf("donations: wallet key file is not configured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return key, fmt.Errorf("donations: read wallet key file %q: %w", path, err)
	}
	if len(data) == len(key) {
		copy(key[:], data)
	} else if parsed, err := ParseEncryptionKey(strings.TrimSpace(string(data))); err == nil {
		key = parsed
	} else {
		return key, fmt.Errorf("donations: wallet key file %q must hold %d raw bytes or %d hex characters", path, len(key), len(key)*2)
	}
	if key == (EncryptionKey{}) {
		return key, fmt.Errorf("donations: wallet key file %q is all zero", path)
	}
	return key, nil
}

// Seal encrypts a mnemonic for storage (DonationStore.CreateWalletSeed).
func Seal(key EncryptionKey, mnemonic string) (ciphertext, nonce []byte, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("donations: generate seal nonce: %w", err)
	}
	return gcm.Seal(nil, nonce, []byte(mnemonic), nil), nonce, nil
}

// Open decrypts a mnemonic previously produced by Seal.
func Open(key EncryptionKey, ciphertext, nonce []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("donations: decrypt wallet seed (wrong donations.wallet_key_file?): %w", err)
	}
	return string(plain), nil
}

func newGCM(key EncryptionKey) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("donations: init cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("donations: init AEAD: %w", err)
	}
	return gcm, nil
}
