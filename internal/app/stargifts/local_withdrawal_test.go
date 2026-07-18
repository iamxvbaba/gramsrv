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
}
