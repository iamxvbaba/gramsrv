package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"
)

func TestHelpStaticListsHonorHash(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := context.Background()

	t.Run("peerColors", func(t *testing.T) {
		full, method := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical,
			&tg.HelpGetPeerColorsRequest{Hash: 0})
		if method != "help.getPeerColors" {
			t.Fatalf("method = %q, want help.getPeerColors", method)
		}
		colors, ok := dispatchCanonicalValue(full).(*tg.HelpPeerColors)
		if !ok {
			t.Fatalf("response = %T, want *tg.HelpPeerColors", dispatchCanonicalValue(full))
		}
		if colors.Hash == 0 {
			t.Fatal("full response carries zero hash")
		}
		cached, _ := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical,
			&tg.HelpGetPeerColorsRequest{Hash: colors.Hash})
		if _, ok := dispatchCanonicalValue(cached).(*tg.HelpPeerColorsNotModified); !ok {
			t.Fatalf("cached response = %T, want *tg.HelpPeerColorsNotModified", dispatchCanonicalValue(cached))
		}
	})

	t.Run("peerProfileColors", func(t *testing.T) {
		full, method := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical,
			&tg.HelpGetPeerProfileColorsRequest{Hash: 0})
		if method != "help.getPeerProfileColors" {
			t.Fatalf("method = %q, want help.getPeerProfileColors", method)
		}
		colors, ok := dispatchCanonicalValue(full).(*tg.HelpPeerColors)
		if !ok {
			t.Fatalf("response = %T, want *tg.HelpPeerColors", dispatchCanonicalValue(full))
		}
		if colors.Hash == 0 {
			t.Fatal("full response carries zero hash")
		}
		cached, _ := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical,
			&tg.HelpGetPeerProfileColorsRequest{Hash: colors.Hash})
		if _, ok := dispatchCanonicalValue(cached).(*tg.HelpPeerColorsNotModified); !ok {
			t.Fatalf("cached response = %T, want *tg.HelpPeerColorsNotModified", dispatchCanonicalValue(cached))
		}
	})

	t.Run("timezones", func(t *testing.T) {
		full, method := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical,
			&tg.HelpGetTimezonesListRequest{Hash: 0})
		if method != "help.getTimezonesList" {
			t.Fatalf("method = %q, want help.getTimezonesList", method)
		}
		list, ok := dispatchCanonicalValue(full).(*tg.HelpTimezonesList)
		if !ok {
			t.Fatalf("response = %T, want *tg.HelpTimezonesList", dispatchCanonicalValue(full))
		}
		if list.Hash == 0 {
			t.Fatal("full response carries zero hash")
		}
		cached, _ := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical,
			&tg.HelpGetTimezonesListRequest{Hash: list.Hash})
		if _, ok := dispatchCanonicalValue(cached).(*tg.HelpTimezonesListNotModified); !ok {
			t.Fatalf("cached response = %T, want *tg.HelpTimezonesListNotModified", dispatchCanonicalValue(cached))
		}
	})
}
