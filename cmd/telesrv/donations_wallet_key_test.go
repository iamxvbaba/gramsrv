package main

import (
	"path/filepath"
	"strings"
	"testing"

	donationsapp "telesrv/internal/app/donations"
	"telesrv/internal/config"
)

// TestDonationsWalletKeyGeneratesFileByDefault pins the "works out of the
// box" contract at the exact seam cmd/telesrv actually calls: with no
// TELESRV_DONATION_WALLET_KEY override, donationsWalletKey must generate
// and persist a key file at DonationWalletKeyPath rather than erroring or
// requiring any manual step, and a second call against the same path (the
// next server restart) must load that identical key back.
func TestDonationsWalletKeyGeneratesFileByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "donation_wallet.key")
	cfg := config.Config{DonationWalletKeyPath: path}

	first, err := donationsWalletKey(cfg)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first == (donationsapp.EncryptionKey{}) {
		t.Fatal("generated key is all zero, want real random bytes")
	}

	second, err := donationsWalletKey(cfg)
	if err != nil {
		t.Fatalf("second call (simulated restart): %v", err)
	}
	if second != first {
		t.Fatal("restart with the same DonationWalletKeyPath produced a different key")
	}
}

// TestDonationsWalletKeyPrefersExplicitOverride pins that an operator-set
// TELESRV_DONATION_WALLET_KEY wins over the file path entirely -- no file
// is read or written, so it can't drift from whatever the operator supplied
// (e.g. from a secrets manager, not this filesystem).
func TestDonationsWalletKeyPrefersExplicitOverride(t *testing.T) {
	explicit := strings.Repeat("02", 32)
	cfg := config.Config{
		DonationWalletKeyPath: filepath.Join(t.TempDir(), "unused.key"),
		DonationWalletKey:     explicit,
	}
	want, err := donationsapp.ParseEncryptionKey(explicit)
	if err != nil {
		t.Fatalf("parse expected key: %v", err)
	}
	got, err := donationsWalletKey(cfg)
	if err != nil {
		t.Fatalf("donationsWalletKey: %v", err)
	}
	if got != want {
		t.Fatal("donationsWalletKey did not honor the explicit TELESRV_DONATION_WALLET_KEY override")
	}
}
