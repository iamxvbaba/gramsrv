package store

import (
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"

	"telesrv/internal/domain"
)

func TestLoginCodeDeliveryKeyAndFingerprint(t *testing.T) {
	const phoneCodeHash = "opaque-high-entropy-phone-code-hash"
	key, err := LoginCodeDeliveryKey(phoneCodeHash)
	if err != nil {
		t.Fatalf("LoginCodeDeliveryKey: %v", err)
	}
	if want := sha256.Sum256([]byte(phoneCodeHash)); key != want {
		t.Fatalf("delivery key = %x, want SHA-256 %x", key, want)
	}

	fingerprint, err := LoginCodeFingerprint(phoneCodeHash, "12345")
	if err != nil {
		t.Fatalf("LoginCodeFingerprint: %v", err)
	}
	if !SameLoginCodeFingerprint(fingerprint[:], fingerprint) {
		t.Fatal("fingerprint does not compare equal to itself")
	}
	otherHash, err := LoginCodeFingerprint("another-phone-code-hash", "12345")
	if err != nil {
		t.Fatalf("other hash fingerprint: %v", err)
	}
	otherCode, err := LoginCodeFingerprint(phoneCodeHash, "54321")
	if err != nil {
		t.Fatalf("other code fingerprint: %v", err)
	}
	if fingerprint == otherHash || fingerprint == otherCode {
		t.Fatal("fingerprint must bind both the raw phone_code_hash and code")
	}
	if SameLoginCodeFingerprint(fingerprint[:31], fingerprint) {
		t.Fatal("truncated fingerprint compared equal")
	}
	if _, err := LoginCodeDeliveryKey(string(make([]byte, maxPhoneCodeHashBytes+1))); !errors.Is(err, domain.ErrLoginCodeDeliveryInvalid) {
		t.Fatalf("oversized phone_code_hash err = %v, want ErrLoginCodeDeliveryInvalid", err)
	}
	if _, err := LoginCodeFingerprint(phoneCodeHash, string(make([]byte, maxLoginCodeBytes+1))); !errors.Is(err, domain.ErrLoginCodeDeliveryInvalid) {
		t.Fatalf("oversized code err = %v, want ErrLoginCodeDeliveryInvalid", err)
	}
}

func TestRestoreLoginCodeDeliveryMessage(t *testing.T) {
	got, err := RestoreLoginCodeDeliveryMessage(1000000001, "12345", 1700000000, 91, 7, 12, "", nil)
	if err != nil {
		t.Fatalf("RestoreLoginCodeDeliveryMessage: %v", err)
	}
	want, err := domain.OfficialLoginCodeMessage(1000000001, "12345", 1700000000)
	if err != nil {
		t.Fatalf("OfficialLoginCodeMessage: %v", err)
	}
	want.UID, want.ID, want.Pts = 91, 7, 12
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored message = %+v, want %+v", got, want)
	}
	if _, err := RestoreLoginCodeDeliveryMessage(1000000001, "12345", 1700000000, 0, 7, 12, "", nil); !errors.Is(err, domain.ErrLoginCodeDeliveryInvalid) {
		t.Fatalf("invalid uid err = %v, want ErrLoginCodeDeliveryInvalid", err)
	}
}
