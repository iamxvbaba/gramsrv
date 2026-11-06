package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tlprofile"
)

// Layer 232 stays a supported exact historical profile after canonical moved to
// 233: it resolves to itself and is never clamped to another profile.
func TestLayer232StaysSupportedExactProfile(t *testing.T) {
	profile, ok := tlprofile.ResolveProfile(232)
	if !ok || profile != tlprofile.Profile232 {
		t.Fatalf("ResolveProfile(232) = %d, %v, want Profile232", profile, ok)
	}
	if !isSupportedLayer(232) {
		t.Fatal("isSupportedLayer(232) = false, want true")
	}
}
