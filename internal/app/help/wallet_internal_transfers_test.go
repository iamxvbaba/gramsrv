package help

import (
	"context"
	"telesrv/internal/domain"
	"testing"
)

type walletOperatorConfig struct{ cfg domain.AppConfig }

func (s walletOperatorConfig) GetAppConfig(context.Context, string) (domain.AppConfig, bool, error) {
	return s.cfg, true, nil
}
func (s walletOperatorConfig) UpsertAppConfig(context.Context, domain.AppConfig) error { return nil }

func TestInternalWalletTransfersAreExplicitAndPreserveOperatorConfig(t *testing.T) {
	for _, options := range [][]Option{{}, {WithWalletAvailable(true)}, {WithInternalWalletTransfers(true)}} {
		cfg := walletConfigValues(t, NewService(nil, nil, options...))
		if cfg["wallet_internal_gram"] == true {
			t.Fatal("transfer capability enabled implicitly")
		}
	}
	on := NewService(nil, nil, WithWalletAvailable(true), WithInternalWalletTransfers(true))
	values := walletConfigValues(t, on)
	if values["wallet_internal_gram"] != true || values["wallet_internal_gram_transfer_version"] != float64(1) || values["wallet_transfer_requires_password"] != true {
		t.Fatal("explicit extension missing")
	}
	if values["wallet_gasless_daily_transfers"] != float64(0) || values["wallet_gasless_min_nanos"] != nil {
		t.Fatal("gasless advertised")
	}
	offCfg, _, _ := NewService(nil, nil, WithWalletAvailable(true)).GetAppConfig(context.Background(), 0, 0)
	if _, same, err := on.GetAppConfig(context.Background(), 0, offCfg.Hash); err != nil || same {
		t.Fatal("capability change retained cache hash")
	}
	custom := walletOperatorConfig{domain.AppConfig{Client: "tdesktop", Hash: defaultAppConfigHash + 100, JSON: []byte(`{"wallet_available":false,"wallet_internal_gram":false,"operator_key":"keep"}`)}}
	values = walletConfigValues(t, NewService(custom, nil, WithWalletAvailable(true), WithInternalWalletTransfers(true)))
	if values["wallet_available"] != false || values["wallet_internal_gram"] != false || values["operator_key"] != "keep" {
		t.Fatal("operator configuration overwritten")
	}
}
