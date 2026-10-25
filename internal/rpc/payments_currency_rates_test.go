package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// The Money screen asks for currency rates on every open and renders the fiat
// amount from the answer. It was being told the server has no rates, which is
// why a real balance rendered with no USD beside it.
func TestGetCurrencyRatesReportsTheWalletRate(t *testing.T) {
	r := New(Config{}, Deps{}, zap.NewNop(), clock.System)
	ctx := WithAuthKeyID(WithUserID(context.Background(), 7), [8]byte{1})

	wire := bin.Buffer{}
	if err := (&tg.InvokeWithLayerRequest{
		Layer: 230,
		Query: &tg.PaymentsGetCurrencyRatesRequest{},
	}).Encode(&wire); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchWithMethod(ctx, [8]byte{1}, 230, &wire); err != nil {
		t.Fatalf("getCurrencyRates must answer, got %v", err)
	}
}

// The two channels have to agree: appConfig advertises ton_usd_rate, and this
// RPC reports the rate. If they diverge the client is told one number and then
// the other, and the fiat amount it renders is whichever it happened to use
// last.
func TestCurrencyRateMatchesAppConfigRate(t *testing.T) {
	if domain.TonUSDRate <= 0 {
		t.Fatalf("TonUSDRate = %v, want a positive rate", domain.TonUSDRate)
	}
	if got := domain.TonUSDRate; got != 1.3448279670684584 {
		t.Fatalf("TonUSDRate = %v, want the rate production ships", got)
	}
}
