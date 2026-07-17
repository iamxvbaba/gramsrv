package smtp

import (
	"context"
	"strings"
	"testing"
	"time"

	"telesrv/internal/otpdelivery"
)

func TestSenderRejectsNonEmailChannelBeforeDial(t *testing.T) {
	sender := New(Config{Host: "smtp.example.test", Port: 25, From: "noreply@example.test"})
	_, err := sender.Deliver(context.Background(), otpdelivery.Request{
		DeliveryID: "otp_sms",
		Purpose:    otpdelivery.PurposeLoginSMS,
		Channel:    otpdelivery.ChannelSMS,
		Recipient:  "15550001001",
		Code:       "12345",
		ExpiresAt:  time.Now().Add(time.Minute),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot deliver") {
		t.Fatalf("err = %v", err)
	}
}

func TestHumanTTLRoundsNetworkSkew(t *testing.T) {
	if got := humanTTL(5*time.Minute - 200*time.Millisecond); got != "5 minutes" {
		t.Fatalf("humanTTL = %q", got)
	}
}
