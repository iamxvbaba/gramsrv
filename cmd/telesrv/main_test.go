package main

import (
	"testing"

	telegramloginapp "telesrv/internal/app/telegramlogin"
)

func TestTelegramLoginRPCDependencyPreservesDisabledNil(t *testing.T) {
	var disabled *telegramloginapp.Service
	if dependency := telegramLoginRPCDependency(disabled); dependency != nil {
		t.Fatalf("disabled Telegram Login dependency = %#v, want nil interface", dependency)
	}
}

func TestTelegramLoginRPCDependencyPreservesEnabledService(t *testing.T) {
	enabled := new(telegramloginapp.Service)
	if dependency := telegramLoginRPCDependency(enabled); dependency != enabled {
		t.Fatalf("enabled Telegram Login dependency = %#v, want %p", dependency, enabled)
	}
}
