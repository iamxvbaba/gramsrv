// Package otpdelivery defines the outbound boundary for one-time-code
// delivery. Code generation, persistence and verification stay in the app
// services; implementations in this package only deliver an already-issued
// code through a concrete channel.
package otpdelivery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Channel string

const (
	ChannelEmail Channel = "email"
	ChannelSMS   Channel = "sms"
)

type Purpose string

const (
	PurposeLoginEmail       Purpose = "login_email"
	PurposeLoginSMS         Purpose = "login_sms"
	PurposeLoginEmailSetup  Purpose = "login_email_setup"
	PurposeLoginEmailChange Purpose = "login_email_change"
	PurposeChangePhone      Purpose = "change_phone"
)

type Request struct {
	DeliveryID string
	Purpose    Purpose
	Channel    Channel
	Recipient  string
	Code       string
	ExpiresAt  time.Time
	Locale     string
}

func (r Request) Validate(now time.Time) error {
	if strings.TrimSpace(r.DeliveryID) == "" || len(r.DeliveryID) > 128 {
		return fmt.Errorf("delivery id is empty or too long")
	}
	switch r.Purpose {
	case PurposeLoginEmail, PurposeLoginSMS, PurposeLoginEmailSetup, PurposeLoginEmailChange, PurposeChangePhone:
	default:
		return fmt.Errorf("unsupported delivery purpose %q", r.Purpose)
	}
	switch r.Channel {
	case ChannelEmail, ChannelSMS:
	default:
		return fmt.Errorf("unsupported delivery channel %q", r.Channel)
	}
	if strings.TrimSpace(r.Recipient) == "" || len(r.Recipient) > 512 {
		return fmt.Errorf("delivery recipient is empty or too long")
	}
	if strings.TrimSpace(r.Code) == "" || len(r.Code) > 64 {
		return fmt.Errorf("delivery code is empty or too long")
	}
	if len(r.Locale) > 32 {
		return fmt.Errorf("delivery locale is too long")
	}
	if !r.ExpiresAt.After(now) {
		return fmt.Errorf("delivery expiry is not in the future")
	}
	return nil
}

type Result struct {
	ProviderMessageID string
}

type Sender interface {
	Deliver(ctx context.Context, req Request) (Result, error)
}

// ErrOutcomeUnknown marks a transport result for which the provider may have
// accepted the request, but telesrv did not receive a valid acknowledgement.
// Callers must keep the issued code usable; deleting it could invalidate a code
// which has already reached the recipient.
var ErrOutcomeUnknown = errors.New("otp delivery outcome unknown")

type OutcomeUnknownError struct {
	Cause error
}

func (e *OutcomeUnknownError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrOutcomeUnknown.Error()
	}
	return fmt.Sprintf("%s: %v", ErrOutcomeUnknown, e.Cause)
}

func (e *OutcomeUnknownError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrOutcomeUnknown
	}
	return errors.Join(ErrOutcomeUnknown, e.Cause)
}

// RejectedError is a provider acknowledgement that the request was not
// accepted. It is safe for the caller to invalidate the corresponding code.
type RejectedError struct {
	StatusCode int
	Code       string
	Retryable  bool
}

func (e *RejectedError) Error() string {
	if e == nil {
		return "otp delivery rejected"
	}
	if e.Code != "" {
		return fmt.Sprintf("otp delivery rejected: status=%d code=%s retryable=%t", e.StatusCode, e.Code, e.Retryable)
	}
	return fmt.Sprintf("otp delivery rejected: status=%d retryable=%t", e.StatusCode, e.Retryable)
}

func NewDeliveryID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate otp delivery id: %w", err)
	}
	return "otp_" + hex.EncodeToString(raw[:]), nil
}
