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

func TestRenderEmailIsBrandedAndBilingual(t *testing.T) {
	for _, purpose := range []otpdelivery.Purpose{otpdelivery.PurposeLoginEmail, otpdelivery.PurposeLoginEmailSetup, otpdelivery.PurposePasswordRecovery} {
		content := renderEmail(otpdelivery.Request{Purpose: purpose, Code: "48213"}, 5*time.Minute)
		for _, part := range []string{content.Subject, content.Text, content.HTML} {
			if strings.Contains(strings.ToLower(part), "telesrv") || strings.Contains(part, "Telegram") {
				t.Fatalf("%s: unbranded text: %q", purpose, part)
			}
		}
		if !strings.Contains(content.Subject, "48213") || !strings.Contains(content.Text, "48213") || !strings.Contains(content.HTML, "48213") {
			t.Fatalf("%s: code missing", purpose)
		}
		if !strings.Contains(content.Text, "5 минут") || !strings.Contains(content.Text, "5 minutes") {
			t.Fatalf("%s: bilingual expiry missing: %q", purpose, content.Text)
		}
	}
	msg := string(buildMessage("ShuzaGram <a@b.com>", "a@b.com", "c@d.com", emailContent{Subject: "s", Text: "t", HTML: "<p>h</p>"}, time.Unix(0, 0)))
	for _, want := range []string{"multipart/alternative", "text/plain", "text/html", "Message-ID: <", "@b.com>", "Date: "} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
	if ttlRU(time.Minute) != "1 минуту" || ttlRU(3*time.Minute) != "3 минуты" || ttlRU(11*time.Minute) != "11 минут" {
		t.Fatal("russian plural forms")
	}
}
