package webauthn_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"telesrv/internal/webauthn"
	"telesrv/internal/webauthn/webauthntest"
)

const (
	testRPID   = "telesrv.test"
	testOrigin = "android:apk-key-hash:Zm9vYmFy"
)

func randChallenge(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

func registerAuth(t *testing.T) (*webauthntest.Authenticator, *webauthn.Credential) {
	t.Helper()
	auth, err := webauthntest.New()
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	ch := randChallenge(t)
	cd, att, err := auth.Register(testRPID, testOrigin, ch)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	cred, err := webauthn.VerifyRegistration(webauthn.VerifyRegistrationInput{
		ClientDataJSON: cd, AttestationObject: att, RPID: testRPID, Challenge: ch,
	})
	if err != nil {
		t.Fatalf("VerifyRegistration: %v", err)
	}
	if !bytes.Equal(cred.ID, auth.CredentialID()) {
		t.Fatalf("credential id mismatch: got %x want %x", cred.ID, auth.CredentialID())
	}
	if len(cred.COSEPublicKey) == 0 {
		t.Fatal("empty COSE public key")
	}
	return auth, cred
}

func TestRegisterThenAssert(t *testing.T) {
	auth, cred := registerAuth(t)
	ch := randChallenge(t)
	cd, ad, sig, err := auth.Assert(testRPID, testOrigin, ch)
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	count, err := webauthn.VerifyAssertion(webauthn.VerifyAssertionInput{
		COSEPublicKey: cred.COSEPublicKey, ClientDataJSON: cd, AuthenticatorData: ad, Signature: sig,
		RPID: testRPID, Challenge: ch, StoredSignCount: cred.SignCount,
	})
	if err != nil {
		t.Fatalf("VerifyAssertion: %v", err)
	}
	if count != 1 {
		t.Fatalf("sign count = %d, want 1", count)
	}
}

func TestAssertCounterRegressionRejected(t *testing.T) {
	auth, cred := registerAuth(t)
	ch := randChallenge(t)
	cd, ad, sig, _ := auth.Assert(testRPID, testOrigin, ch) // signCount -> 1
	if _, err := webauthn.VerifyAssertion(webauthn.VerifyAssertionInput{
		COSEPublicKey: cred.COSEPublicKey, ClientDataJSON: cd, AuthenticatorData: ad, Signature: sig,
		RPID: testRPID, Challenge: ch, StoredSignCount: 5, // stored counter larger => suspected clone
	}); !errors.Is(err, webauthn.ErrCounterRegressed) {
		t.Fatalf("err = %v, want ErrCounterRegressed", err)
	}
}

func TestAssertChallengeMismatch(t *testing.T) {
	auth, cred := registerAuth(t)
	ch := randChallenge(t)
	cd, ad, sig, _ := auth.Assert(testRPID, testOrigin, ch)
	if _, err := webauthn.VerifyAssertion(webauthn.VerifyAssertionInput{
		COSEPublicKey: cred.COSEPublicKey, ClientDataJSON: cd, AuthenticatorData: ad, Signature: sig,
		RPID: testRPID, Challenge: randChallenge(t), StoredSignCount: cred.SignCount,
	}); !errors.Is(err, webauthn.ErrChallengeMismatch) {
		t.Fatalf("err = %v, want ErrChallengeMismatch", err)
	}
}

func TestAssertSignatureTamperRejected(t *testing.T) {
	auth, cred := registerAuth(t)
	ch := randChallenge(t)
	cd, ad, sig, _ := auth.Assert(testRPID, testOrigin, ch)
	sig[len(sig)-1] ^= 0xFF
	if _, err := webauthn.VerifyAssertion(webauthn.VerifyAssertionInput{
		COSEPublicKey: cred.COSEPublicKey, ClientDataJSON: cd, AuthenticatorData: ad, Signature: sig,
		RPID: testRPID, Challenge: ch, StoredSignCount: cred.SignCount,
	}); !errors.Is(err, webauthn.ErrSignatureInvalid) {
		t.Fatalf("err = %v, want ErrSignatureInvalid", err)
	}
}

func TestAssertWrongRPIDRejected(t *testing.T) {
	auth, cred := registerAuth(t)
	ch := randChallenge(t)
	cd, ad, sig, _ := auth.Assert("other.rp", testOrigin, ch)
	if _, err := webauthn.VerifyAssertion(webauthn.VerifyAssertionInput{
		COSEPublicKey: cred.COSEPublicKey, ClientDataJSON: cd, AuthenticatorData: ad, Signature: sig,
		RPID: testRPID, Challenge: ch, StoredSignCount: cred.SignCount,
	}); !errors.Is(err, webauthn.ErrRPIDMismatch) {
		t.Fatalf("err = %v, want ErrRPIDMismatch", err)
	}
}

func TestRegistrationWrongRPIDRejected(t *testing.T) {
	auth, _ := func() (*webauthntest.Authenticator, *webauthn.Credential) {
		a, err := webauthntest.New()
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		return a, nil
	}()
	ch := randChallenge(t)
	cd, att, _ := auth.Register("attacker.rp", testOrigin, ch)
	if _, err := webauthn.VerifyRegistration(webauthn.VerifyRegistrationInput{
		ClientDataJSON: cd, AttestationObject: att, RPID: testRPID, Challenge: ch,
	}); !errors.Is(err, webauthn.ErrRPIDMismatch) {
		t.Fatalf("err = %v, want ErrRPIDMismatch", err)
	}
}

func TestOriginEnforcement(t *testing.T) {
	auth, cred := registerAuth(t)
	ch := randChallenge(t)
	cd, ad, sig, _ := auth.Assert(testRPID, testOrigin, ch)
	// Allowlist match => pass.
	if _, err := webauthn.VerifyAssertion(webauthn.VerifyAssertionInput{
		COSEPublicKey: cred.COSEPublicKey, ClientDataJSON: cd, AuthenticatorData: ad, Signature: sig,
		RPID: testRPID, Challenge: ch, AllowedOrigins: []string{testOrigin}, StoredSignCount: cred.SignCount,
	}); err != nil {
		t.Fatalf("matching origin should pass: %v", err)
	}
	// Allowlist does not contain this origin => reject.
	if _, err := webauthn.VerifyAssertion(webauthn.VerifyAssertionInput{
		COSEPublicKey: cred.COSEPublicKey, ClientDataJSON: cd, AuthenticatorData: ad, Signature: sig,
		RPID: testRPID, Challenge: ch, AllowedOrigins: []string{"android:apk-key-hash:other"}, StoredSignCount: cred.SignCount,
	}); !errors.Is(err, webauthn.ErrOriginNotAllowed) {
		t.Fatalf("err = %v, want ErrOriginNotAllowed", err)
	}
}

func TestBuildOptionsContainPublicKey(t *testing.T) {
	reg, err := webauthn.BuildRegistrationOptions(webauthn.RegistrationParams{
		RPID: testRPID, UserID: []byte("2:123"), UserName: "user", Challenge: randChallenge(t),
	})
	if err != nil {
		t.Fatalf("BuildRegistrationOptions: %v", err)
	}
	if !bytes.Contains(reg, []byte(`"publicKey"`)) || !bytes.Contains(reg, []byte(`"challenge"`)) {
		t.Fatalf("registration options missing publicKey/challenge: %s", reg)
	}
	login, err := webauthn.BuildLoginOptions(webauthn.LoginParams{RPID: testRPID, Challenge: randChallenge(t)})
	if err != nil {
		t.Fatalf("BuildLoginOptions: %v", err)
	}
	if !bytes.Contains(login, []byte(`"publicKey"`)) || !bytes.Contains(login, []byte(`"rpId"`)) {
		t.Fatalf("login options missing publicKey/rpId: %s", login)
	}
}
