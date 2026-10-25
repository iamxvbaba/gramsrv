package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tlprofile"
)

// Layer 232 is the canonical profile of the published schema. The server must
// admit it, keep it as the session default and never clamp it to an older
// profile.
func TestLayer232IsSupportedCanonicalProfile(t *testing.T) {
	profile, ok := tlprofile.ResolveProfile(232)
	if !ok || profile != tlprofile.Profile232 {
		t.Fatalf("ResolveProfile(232) = %d, %v, want Profile232", profile, ok)
	}
	if !isSupportedLayer(232) {
		t.Fatal("isSupportedLayer(232) = false, want true")
	}
	if isSupportedLayer(233) {
		t.Fatal("isSupportedLayer(233) = true, want false for an ungenerated future layer")
	}
	if int(tlprofile.ProfileCanonical) != 232 {
		t.Fatalf("canonical profile = %d, want 232", tlprofile.ProfileCanonical)
	}
	if currentClientLayer != 232 {
		t.Fatalf("currentClientLayer = %d, want 232", currentClientLayer)
	}
}
