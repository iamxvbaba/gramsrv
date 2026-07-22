package rpc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap"

	telegramloginapp "telesrv/internal/app/telegramlogin"
)

func TestAssertNoTypedNilDepsRejectsTelegramLogin(t *testing.T) {
	var service *telegramloginapp.Service
	defer func() {
		value := recover()
		if value == nil {
			t.Fatal("assertNoTypedNilDeps accepted a typed-nil Telegram Login service")
		}
		message := fmt.Sprint(value)
		if !strings.Contains(message, "dependency TelegramLogin is a typed nil *telegramlogin.Service") {
			t.Fatalf("panic = %q, want TelegramLogin typed-nil diagnostic", message)
		}
	}()
	New(Config{}, Deps{TelegramLogin: service}, zap.NewNop(), clock.System)
}

func TestAssertNoTypedNilDepsAcceptsAbsentTelegramLogin(t *testing.T) {
	assertNoTypedNilDeps(Deps{})
}

func TestDisabledTelegramLoginWebAuthorizationRPCs(t *testing.T) {
	router := New(Config{}, Deps{}, zap.NewNop(), clock.System)
	ctx := WithUserID(context.Background(), 42)

	listed, err := router.onAccountGetWebAuthorizations(ctx)
	if err != nil {
		t.Fatalf("get disabled web authorizations: %v", err)
	}
	if len(listed.Authorizations) != 0 || len(listed.Users) != 0 {
		t.Fatalf("disabled web authorizations = %#v, want empty vectors", listed)
	}
	if reset, err := router.onAccountResetWebAuthorization(ctx, 123); err != nil || !reset {
		t.Fatalf("reset disabled web authorization = %v, %v; want true, nil", reset, err)
	}
	if reset, err := router.onAccountResetWebAuthorizations(ctx); err != nil || !reset {
		t.Fatalf("reset all disabled web authorizations = %v, %v; want true, nil", reset, err)
	}
}
