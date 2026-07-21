//go:build !jwx_es256k

package telegramlogin

import (
	"testing"

	"telesrv/internal/domain"
)

func TestES256KFailsClosedWithoutBuildTag(t *testing.T) {
	if _, err := NewSigningKeyRing([]SigningKeyMaterial{{
		Algorithm: domain.TelegramLoginSigningES256K, PrivateKey: struct{}{}, Active: true,
	}}, nil); err == nil {
		t.Fatal("ES256K configuration unexpectedly accepted without jwx_es256k")
	}
}
