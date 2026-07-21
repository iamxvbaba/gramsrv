package telegramlogin

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"telesrv/internal/domain"
)

func telegramLoginTestSigningKeys(t *testing.T, now *time.Time) *SigningKeyRing {
	t.Helper()
	oldRSA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	activeRSA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	es256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, ed25519Key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ring, err := NewSigningKeyRing([]SigningKeyMaterial{
		{Algorithm: domain.TelegramLoginSigningRS256, KeyID: "rsa-old", PrivateKey: oldRSA, PublishUntil: now.Add(2 * time.Hour)},
		{Algorithm: domain.TelegramLoginSigningRS256, KeyID: "rsa-active", PrivateKey: activeRSA, Active: true},
		{Algorithm: domain.TelegramLoginSigningES256, KeyID: "p256-active", PrivateKey: es256, Active: true},
		{Algorithm: domain.TelegramLoginSigningEdDSA, KeyID: "ed25519-active", PrivateKey: ed25519Key, Active: true},
	}, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func TestSigningKeyRingRotationAndAlgorithms(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	ring := telegramLoginTestSigningKeys(t, &now)
	if got := ring.SupportedAlgorithms(); len(got) != 3 || got[0] != "RS256" || got[1] != "ES256" || got[2] != "EdDSA" {
		t.Fatalf("SupportedAlgorithms = %#v", got)
	}
	body, etag, err := ring.JWKS()
	if err != nil {
		t.Fatal(err)
	}
	set, err := jwk.Parse(body)
	if err != nil {
		t.Fatalf("parse JWKS: %v", err)
	}
	if set.Len() != 4 || etag == "" {
		t.Fatalf("JWKS len=%d etag=%q body=%s", set.Len(), etag, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < set.Len(); i++ {
		key, _ := set.Key(i)
		if key.Has("d") || key.Has("p") || key.Has("q") {
			t.Fatalf("JWKS leaked private key material: %s", body)
		}
	}
	now = now.Add(3 * time.Hour)
	body, _, err = ring.JWKS()
	if err != nil {
		t.Fatal(err)
	}
	set, err = jwk.Parse(body)
	if err != nil || set.Len() != 3 {
		t.Fatalf("JWKS after retirement len=%d err=%v body=%s", set.Len(), err, body)
	}
}

func TestIDTokenIssuerScopeProjectionAndVerification(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	ring := telegramLoginTestSigningKeys(t, &now)
	issuer, err := NewIDTokenIssuer(ring, IDTokenIssuerConfig{
		Issuer: "https://oauth.telesrv.test", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	profileRequest := domain.TelegramLoginRequest{
		ClientID: "9001", SigningAlgorithm: domain.TelegramLoginSigningRS256,
		Scopes: []domain.TelegramLoginScope{
			domain.TelegramLoginScopeOpenID, domain.TelegramLoginScopeProfile, domain.TelegramLoginScopePhone,
		},
		Nonce: "request-nonce", Status: domain.TelegramLoginRequestApproved, AuthorizedUserID: 42,
		ProfileName: "Alice Example", GivenName: "Alice", FamilyName: "Example",
		PreferredUsername: "alice", Picture: "https://oauth.telesrv.test/userpic/42",
		PhoneNumber: "15551234567", PhoneShared: true, ApprovedAt: now.Add(-time.Minute),
	}
	signed, err := issuer.Issue(profileRequest)
	if err != nil {
		t.Fatal(err)
	}
	jwksBody, _, err := ring.JWKS()
	if err != nil {
		t.Fatal(err)
	}
	set, err := jwk.Parse(jwksBody)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Parse([]byte(signed), jwt.WithKeySet(set), jwt.WithValidate(false))
	if err != nil {
		t.Fatalf("verify signed token: %v", err)
	}
	issuerValue, _ := token.Issuer()
	subject, _ := token.Subject()
	audience, _ := token.Audience()
	if issuerValue != "https://oauth.telesrv.test" || subject != "42" || len(audience) != 1 || audience[0] != "9001" {
		t.Fatalf("standard claims iss=%q sub=%q aud=%#v", issuerValue, subject, audience)
	}
	var id float64
	var name, phone, nonce string
	var verified bool
	if err := token.Get("id", &id); err != nil || id != 42 {
		t.Fatalf("id claim=%v err=%v", id, err)
	}
	if err := token.Get("name", &name); err != nil || name != "Alice Example" {
		t.Fatalf("name claim=%q err=%v", name, err)
	}
	if err := token.Get("phone_number", &phone); err != nil || phone != "15551234567" {
		t.Fatalf("phone claim=%q err=%v", phone, err)
	}
	if err := token.Get("phone_number_verified", &verified); err != nil || !verified {
		t.Fatalf("phone verified=%v err=%v", verified, err)
	}
	if err := token.Get("nonce", &nonce); err != nil || nonce != "request-nonce" {
		t.Fatalf("nonce=%q err=%v", nonce, err)
	}

	openidOnly := profileRequest
	openidOnly.SigningAlgorithm = domain.TelegramLoginSigningEdDSA
	openidOnly.Scopes = []domain.TelegramLoginScope{domain.TelegramLoginScopeOpenID}
	openidOnly.ProfileName = ""
	openidOnly.GivenName = ""
	openidOnly.FamilyName = ""
	openidOnly.PreferredUsername = ""
	openidOnly.Picture = ""
	openidOnly.PhoneNumber = ""
	openidOnly.PhoneShared = false
	signed, err = issuer.Issue(openidOnly)
	if err != nil {
		t.Fatal(err)
	}
	token, err = jwt.Parse([]byte(signed), jwt.WithKeySet(set), jwt.WithValidate(false))
	if err != nil {
		t.Fatalf("verify EdDSA token: %v", err)
	}
	if token.Has("id") || token.Has("name") || token.Has("phone_number") {
		t.Fatalf("openid-only token leaked optional claims: %#v", token.Keys())
	}
}

func TestIDTokenIssuerAcceptsHTTPIPOnlyWhenEnabled(t *testing.T) {
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	ring := telegramLoginTestSigningKeys(t, &now)
	if _, err := NewIDTokenIssuer(ring, IDTokenIssuerConfig{Issuer: "http://192.0.2.25:2401"}); err == nil {
		t.Fatal("HTTP issuer was accepted while AllowHTTP was false")
	}
	issuer, err := NewIDTokenIssuer(ring, IDTokenIssuerConfig{Issuer: "http://192.0.2.25:2401", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if issuer.Issuer() != "http://192.0.2.25:2401" {
		t.Fatalf("issuer=%q", issuer.Issuer())
	}
}

func TestSigningKeyRingRejectsWrongCurveAndDuplicateActiveKey(t *testing.T) {
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSigningKeyRing([]SigningKeyMaterial{{
		Algorithm: domain.TelegramLoginSigningES256, PrivateKey: p384, Active: true,
	}}, nil); err == nil {
		t.Fatal("P-384 key unexpectedly accepted for ES256")
	}
	key1, _ := rsa.GenerateKey(rand.Reader, 2048)
	key2, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, err := NewSigningKeyRing([]SigningKeyMaterial{
		{Algorithm: domain.TelegramLoginSigningRS256, PrivateKey: key1, Active: true},
		{Algorithm: domain.TelegramLoginSigningRS256, PrivateKey: key2, Active: true},
	}, nil); err == nil {
		t.Fatal("two active RS256 keys unexpectedly accepted")
	}
}
