package otpdelivery

import (
	"strings"
	"testing"
	"time"
)

func TestNewDeliveryIDIsOpaqueAndUnique(t *testing.T) {
	first, err := NewDeliveryID()
	if err != nil {
		t.Fatalf("first id: %v", err)
	}
	second, err := NewDeliveryID()
	if err != nil {
		t.Fatalf("second id: %v", err)
	}
	if first == second || !strings.HasPrefix(first, "otp_") || len(first) != len("otp_")+32 {
		t.Fatalf("ids = %q / %q", first, second)
	}
}

func TestRequestRejectsExpiredCode(t *testing.T) {
	now := time.Now()
	err := (Request{
		DeliveryID: "otp_expired",
		Purpose:    PurposeLoginEmail,
		Channel:    ChannelEmail,
		Recipient:  "a@example.test",
		Code:       "123456",
		ExpiresAt:  now,
	}).Validate(now)
	if err == nil {
		t.Fatal("expired request accepted")
	}
}
