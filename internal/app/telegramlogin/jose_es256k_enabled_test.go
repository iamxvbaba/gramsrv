//go:build jwx_es256k

package telegramlogin

import (
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"telesrv/internal/domain"
)

func TestES256KIDTokenRoundTripWithBuildTag(t *testing.T) {
	raw, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	ring, err := NewSigningKeyRing([]SigningKeyMaterial{{
		Algorithm:  domain.TelegramLoginSigningES256K,
		KeyID:      "secp256k1-active",
		PrivateKey: raw.ToECDSA(),
		Active:     true,
	}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewIDTokenIssuer(ring, IDTokenIssuerConfig{
		Issuer: "https://oauth.telesrv.test", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := issuer.Issue(domain.TelegramLoginRequest{
		ClientID: "9001", SigningAlgorithm: domain.TelegramLoginSigningES256K,
		Scopes: []domain.TelegramLoginScope{domain.TelegramLoginScopeOpenID},
		Status: domain.TelegramLoginRequestApproved, AuthorizedUserID: 42,
		ApprovedAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := ring.JWKS()
	if err != nil {
		t.Fatal(err)
	}
	set, err := jwk.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.Parse([]byte(signed), jwt.WithKeySet(set), jwt.WithValidate(false)); err != nil {
		t.Fatalf("verify ES256K token: %v", err)
	}
}
