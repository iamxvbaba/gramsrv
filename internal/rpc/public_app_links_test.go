package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap"
)

func TestRouterPublicAppLinkUsesConfiguredBaseAndLegacyDefault(t *testing.T) {
	legacy := New(Config{}, Deps{}, zap.NewNop(), clock.System)
	if got, want := legacy.publicAppLink("business-bot"), "telesrv://business-bot"; got != want {
		t.Fatalf("legacy business bot link = %q, want %q", got, want)
	}

	hosted := New(Config{
		PublicAppScheme:   "telesrv",
		PublicAppLinkBase: "owpg://tenant.example.test",
	}, Deps{}, zap.NewNop(), clock.System)
	if got, want := hosted.publicAppLink("business-bot"), "owpg://tenant.example.test/business-bot"; got != want {
		t.Fatalf("hosted business bot link = %q, want %q", got, want)
	}
}

func TestRouterRejectsInvalidPublicAppLinkConfig(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New did not fail fast for an invalid public app link base")
		}
	}()
	_ = New(Config{PublicAppLinkBase: "owpg://tenant.example.test/root"}, Deps{}, zap.NewNop(), clock.System)
}
