package config

import "testing"

func TestWalletTransfersRequireZeroStartingGrant(t *testing.T) {
	cfg := Config{WalletStateDir: "data/wallet", WalletKeyFile: "data/wallet/key", WalletInternalTransfers: true}
	for _, grant := range []int64{1, 10_000_000_000, -1} {
		cfg.StarGiftTONStartingGrant = grant
		if err := validateWalletConfig(cfg); err == nil {
			t.Fatalf("transfers accepted with starting grant %d", grant)
		}
	}
	cfg.StarGiftTONStartingGrant = 0
	if err := validateWalletConfig(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.WalletInternalTransfers = false
	cfg.StarGiftTONStartingGrant = 10_000_000_000
	if err := validateWalletConfig(cfg); err != nil {
		t.Fatalf("disabled transfers must retain existing grant configuration: %v", err)
	}
}
