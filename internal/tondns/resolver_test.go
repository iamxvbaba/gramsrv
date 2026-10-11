package tondns

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestResolveKnownTonName is the live mainnet check: DNS lookup needs working
// lite server access, so it runs only when explicitly asked for.
func TestResolveKnownTonName(t *testing.T) {
	if os.Getenv("TELESRV_TEST_TON_DNS") == "" {
		t.Skip("set TELESRV_TEST_TON_DNS=1 to resolve against mainnet")
	}
	name := os.Getenv("TELESRV_TEST_TON_DNS_NAME")
	if name == "" {
		name = "foundation.ton"
	}
	resolver := New("")
	defer resolver.Close()
	addr, err := resolver.Resolve(context.Background(), name)
	if err != nil {
		t.Fatalf("resolve %s: %v", name, err)
	}
	if !strings.Contains(addr, ":") {
		t.Fatalf("resolved %q is not a raw address", addr)
	}
	t.Logf("%s -> %s", name, addr)
}
