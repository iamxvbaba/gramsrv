package domain

import (
	"strings"
	"testing"
)

func TestServiceIdentityAndLoginMessageUseTelesrvBrand(t *testing.T) {
	serviceUser := OfficialSystemUser()
	if serviceUser.FirstName != "Telesrv" || serviceUser.Username != "telesrv" {
		t.Fatalf("service user = %+v, want Telesrv identity", serviceUser)
	}
	message, err := OfficialLoginCodeMessage(42, "12345", 1)
	if err != nil {
		t.Fatalf("build login message: %v", err)
	}
	if !strings.Contains(message.Body, "Telesrv") || strings.Contains(strings.ToLower(message.Body), "telegram") {
		t.Fatalf("login message exposes wrong brand: %q", message.Body)
	}
}
