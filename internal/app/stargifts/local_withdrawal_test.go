package stargifts

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLocalWithdrawalProviderIsInternalAndBounded(t *testing.T) {
	for _, invalid := range []string{"", "ftp://example.test", "https://user@example.test", "https://example.test/?token=bad", "https://example.test/#bad"} {
		if _, err := NewLocalWithdrawalProvider(invalid); err == nil {
			t.Fatalf("invalid withdrawal base URL %q accepted", invalid)
		}
	}
	if _, err := NewLocalWithdrawalProvider("http://example.test"); err == nil {
		t.Fatal("plaintext public withdrawal bearer URL accepted")
	}
	if _, err := NewLocalWithdrawalProvider("http://127.0.0.1:2401"); err != nil {
		t.Fatalf("loopback development withdrawal URL rejected: %v", err)
	}
	provider, err := NewLocalWithdrawalProvider("https://example.test/base/")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	result, err := provider.CreateWithdrawal(context.Background(), StarGiftWithdrawalProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "telesrv-local" || len(result.RequestID) != 43 ||
		result.URL != "https://example.test/base/gift-withdrawal/"+result.RequestID ||
		strings.ContainsAny(result.RequestID, "+/=") {
		t.Fatalf("local withdrawal result = %+v", result)
	}
	expires := time.Unix(int64(result.ExpiresAt), 0)
	if expires.Before(before.Add(14*time.Minute)) || expires.After(before.Add(16*time.Minute)) {
		t.Fatalf("local withdrawal expiry = %v, want about 15 minutes", expires)
	}
	revenue, err := provider.CreateRevenueWithdrawal(context.Background(), ChannelRevenueWithdrawalProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(revenue.RequestID) != 43 || revenue.RequestID == result.RequestID ||
		revenue.URL != "https://example.test/base/revenue-withdrawal/"+revenue.RequestID {
		t.Fatalf("local revenue withdrawal result = %+v", revenue)
	}
}

func TestCustomFragmentWithdrawalProviderName(t *testing.T) {
	provider, err := NewCustomFragmentWithdrawalProvider("https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "customfragment-ton-mainnet" {
		t.Fatalf("provider name = %q", provider.Name())
	}
}
