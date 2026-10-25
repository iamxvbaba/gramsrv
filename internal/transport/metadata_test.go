package transport

import (
	"context"
	"testing"
)

func TestWithClientIPRoundTrip(t *testing.T) {
	ctx := WithClientIP(context.Background(), "203.0.113.7")
	ip, ok := ClientIPFrom(ctx)
	if !ok || ip != "203.0.113.7" {
		t.Fatalf("round-trip mismatch: got %q ok=%v", ip, ok)
	}
}

func TestWithClientIPEmptyIgnored(t *testing.T) {
	ctx := WithClientIP(context.Background(), "")
	if _, ok := ClientIPFrom(ctx); ok {
		t.Fatalf("empty ip must not be stored")
	}
}

func TestClientIPFromNotSet(t *testing.T) {
	if _, ok := ClientIPFrom(context.Background()); ok {
		t.Fatalf("no ip set, ok must be false")
	}
}
