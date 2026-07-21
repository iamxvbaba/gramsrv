package domain

import (
	"strings"
	"testing"
	"time"
)

func TestValidateTelegramLoginScopes(t *testing.T) {
	tests := []struct {
		name   string
		scopes []TelegramLoginScope
		alg    TelegramLoginSigningAlgorithm
		valid  bool
	}{
		{name: "rs profile phone", scopes: []TelegramLoginScope{TelegramLoginScopeOpenID, TelegramLoginScopeProfile, TelegramLoginScopePhone}, alg: TelegramLoginSigningRS256, valid: true},
		{name: "missing openid", scopes: []TelegramLoginScope{TelegramLoginScopeProfile}, alg: TelegramLoginSigningRS256},
		{name: "duplicate", scopes: []TelegramLoginScope{TelegramLoginScopeOpenID, TelegramLoginScopeOpenID}, alg: TelegramLoginSigningRS256},
		{name: "unknown", scopes: []TelegramLoginScope{TelegramLoginScopeOpenID, "admin"}, alg: TelegramLoginSigningRS256},
		{name: "eddsa openid", scopes: []TelegramLoginScope{TelegramLoginScopeOpenID}, alg: TelegramLoginSigningEdDSA, valid: true},
		{name: "eddsa profile forbidden", scopes: []TelegramLoginScope{TelegramLoginScopeOpenID, TelegramLoginScopeProfile}, alg: TelegramLoginSigningEdDSA},
		{name: "es256k phone forbidden", scopes: []TelegramLoginScope{TelegramLoginScopeOpenID, TelegramLoginScopePhone}, alg: TelegramLoginSigningES256K},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateTelegramLoginScopes(test.scopes, test.alg)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateTelegramLoginScopes() error = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestTelegramLoginRequestTransitions(t *testing.T) {
	for _, terminal := range []TelegramLoginRequestState{
		TelegramLoginRequestApproved,
		TelegramLoginRequestDeclined,
		TelegramLoginRequestExpired,
	} {
		if !CanTransitionTelegramLoginRequest(TelegramLoginRequestPending, terminal) {
			t.Fatalf("pending -> %s must be valid", terminal)
		}
		if CanTransitionTelegramLoginRequest(terminal, TelegramLoginRequestPending) {
			t.Fatalf("%s -> pending must be forbidden", terminal)
		}
	}
	if CanTransitionTelegramLoginRequest(TelegramLoginRequestApproved, TelegramLoginRequestDeclined) {
		t.Fatal("approved -> declined must be forbidden")
	}
}

func TestTelegramLoginRequestSourceShapeMatrix(t *testing.T) {
	now := time.Unix(1_780_000_000, 0).UTC()
	base := TelegramLoginRequest{
		RequestTokenHash: make([]byte, 32), BrowserTokenHash: make([]byte, 32),
		BotUserID: 9001, ClientID: "9001", SigningAlgorithm: TelegramLoginSigningRS256,
		Source: TelegramLoginRequestWeb, ResponseType: "code", RedirectURI: "https://rp.example/callback",
		Origin: "https://rp.example", Domain: "rp.example", Scopes: []TelegramLoginScope{TelegramLoginScopeOpenID},
		CodeChallenge: strings.Repeat("A", 43), CodeChallengeMethod: "S256",
		Browser: "Firefox", Platform: "Windows", IP: "192.0.2.1", Region: "Test",
		Status: TelegramLoginRequestPending, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid web request: %v", err)
	}
	invalid := []struct {
		name   string
		mutate func(*TelegramLoginRequest)
	}{
		{name: "web post message", mutate: func(r *TelegramLoginRequest) {
			r.ResponseType = "post_message"
			r.CodeChallenge = ""
			r.CodeChallengeMethod = ""
		}},
		{name: "javascript code", mutate: func(r *TelegramLoginRequest) { r.Source = TelegramLoginRequestJavaScript }},
		{name: "message button code", mutate: func(r *TelegramLoginRequest) { r.Source = TelegramLoginRequestMessageButton }},
		{name: "web app flag", mutate: func(r *TelegramLoginRequest) { r.IsApp = true; r.VerifiedAppName = "Forged" }},
		{name: "web missing origin", mutate: func(r *TelegramLoginRequest) { r.Origin = "" }},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			request := base.Clone()
			tc.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("forbidden source shape was accepted")
			}
		})
	}

	native := base.Clone()
	native.Source, native.Origin, native.Domain = TelegramLoginRequestNative, "", "dev.bedolaga.demo"
	native.IsApp, native.VerifiedAppName = true, "Bedolaga"
	if err := native.Validate(); err != nil {
		t.Fatalf("valid native request: %v", err)
	}
	native.IsApp = false
	if err := native.Validate(); err == nil {
		t.Fatal("native request without verified app state was accepted")
	}

	mini := base.Clone()
	mini.Source, mini.ResponseType = TelegramLoginRequestMiniApp, "post_message"
	mini.CodeChallenge, mini.CodeChallengeMethod = "", ""
	mini.RedirectURI, mini.InAppOrigin = "https://rp.example/", mini.Origin
	if err := mini.Validate(); err != nil {
		t.Fatalf("valid Mini App request: %v", err)
	}
	mini.InAppOrigin = "https://other.example"
	if err := mini.Validate(); err == nil {
		t.Fatal("Mini App origin mismatch was accepted")
	}
}
