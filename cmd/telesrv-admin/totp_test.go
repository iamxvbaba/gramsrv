package main

import (
	"encoding/base32"
	"testing"
	"time"
)

func TestTOTPVerifier(t *testing.T) {
	// 20-byte secret encoded in base32
	rawKey := []byte("12345678901234567890")
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(rawKey)

	verifier, err := newTOTPVerifier(secret)
	if err != nil {
		t.Fatalf("newTOTPVerifier failed: %v", err)
	}

	now := time.Unix(1700000000, 0)
	counter := now.Unix() / int64(totpStep/time.Second)
	code := totpCode(verifier.key, counter)

	// Valid code for current time window
	if !verifier.Verify(code, now) {
		t.Fatalf("expected code %s to be valid at %v", code, now)
	}

	// Replay protection: same counter cannot be reused
	if verifier.Verify(code, now) {
		t.Fatalf("expected code replay to be rejected")
	}

	// Code from previous time step (counter - 1)
	pastCounter := counter - 1
	pastCode := totpCode(verifier.key, pastCounter)
	// Because verifier.last is already set to counter, past counter (< last) must be rejected
	if verifier.Verify(pastCode, now) {
		t.Fatalf("expected past counter to be rejected once newer counter verified")
	}

	// Next time window (counter + 1)
	nextTime := now.Add(totpStep)
	nextCounter := counter + 1
	nextCode := totpCode(verifier.key, nextCounter)
	if !verifier.Verify(nextCode, nextTime) {
		t.Fatalf("expected code %s to be valid at next window %v", nextCode, nextTime)
	}

	// Invalid format
	if verifier.Verify("123", nextTime) {
		t.Fatalf("expected 3-digit code to be rejected")
	}
	if verifier.Verify("abcdef", nextTime) {
		t.Fatalf("expected non-digit code to be rejected")
	}
}

func TestTOTPVerifierSecretValidation(t *testing.T) {
	// Short secret (< 16 chars)
	if _, err := newTOTPVerifier("JBSWY3DPEE"); err == nil {
		t.Fatalf("expected error on short secret")
	}

	// Invalid base32 characters (8, 9, etc.)
	if _, err := newTOTPVerifier("9999999999999999"); err == nil {
		t.Fatalf("expected error on invalid base32 characters")
	}
}
