package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeliveryAcceptsSignedRequestAndDeduplicatesReplay(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	app := testApplication(now, func(_ context.Context, _ deliveryRequest) (string, error) {
		calls.Add(1)
		return "provider-message-1", nil
	})
	body := marshalRequest(t, validRequest(now))

	for range 2 {
		response := performDelivery(t, app.routes(), body, "otp_test_1", "test-secret", now)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var result deliveryResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Accepted || result.MessageID != "provider-message-1" {
			t.Fatalf("unexpected response: %+v", result)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("deliver calls = %d, want 1", got)
	}
}

func TestDeliveryRejectsIdempotencyConflict(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	app := testApplication(now, func(_ context.Context, _ deliveryRequest) (string, error) {
		return "provider-message-1", nil
	})
	first := validRequest(now)
	response := performDelivery(t, app.routes(), marshalRequest(t, first), first.DeliveryID, "test-secret", now)
	if response.Code != http.StatusOK {
		t.Fatalf("first status = %d", response.Code)
	}

	second := first
	second.Code = "654321"
	response = performDelivery(t, app.routes(), marshalRequest(t, second), second.DeliveryID, "test-secret", now)
	if response.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestDeliveryRejectsInvalidSignature(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	app := testApplication(now, func(_ context.Context, _ deliveryRequest) (string, error) {
		t.Fatal("deliver must not be called")
		return "", nil
	})
	request := validRequest(now)
	response := performDelivery(t, app.routes(), marshalRequest(t, request), request.DeliveryID, "wrong-secret", now)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestDeliveryRejectsExpiredCode(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	app := testApplication(now, func(_ context.Context, _ deliveryRequest) (string, error) {
		t.Fatal("deliver must not be called")
		return "", nil
	})
	request := validRequest(now)
	request.ExpiresAt = now.Add(-time.Second)
	response := performDelivery(t, app.routes(), marshalRequest(t, request), request.DeliveryID, "test-secret", now)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestDeliveryFailureIsAlsoDeduplicated(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	app := testApplication(now, func(_ context.Context, _ deliveryRequest) (string, error) {
		calls.Add(1)
		return "", errors.New("downstream outcome unknown")
	})
	request := validRequest(now)
	body := marshalRequest(t, request)

	for range 2 {
		response := performDelivery(t, app.routes(), body, request.DeliveryID, "test-secret", now)
		if response.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("deliver calls = %d, want 1", got)
	}
}

func testApplication(now time.Time, deliver deliveryFunc) *application {
	return newApplication(
		"test-secret",
		5*time.Minute,
		func() time.Time { return now },
		deliver,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func validRequest(now time.Time) deliveryRequest {
	return deliveryRequest{
		Version:    "1",
		DeliveryID: "otp_test_1",
		Purpose:    "login_email",
		Channel:    "email",
		Recipient:  "alice@example.test",
		Code:       "123456",
		ExpiresAt:  now.Add(5 * time.Minute),
		ExpiresIn:  300,
		Locale:     "zh-CN",
	}
}

func marshalRequest(t *testing.T, request deliveryRequest) []byte {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func performDelivery(
	t *testing.T,
	handler http.Handler,
	body []byte,
	deliveryID string,
	secret string,
	now time.Time,
) *httptest.ResponseRecorder {
	t.Helper()
	timestampText := strconv.FormatInt(now.Unix(), 10)
	request := httptest.NewRequest(http.MethodPost, "/v1/otp/deliveries", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", deliveryID)
	request.Header.Set("X-Telesrv-Timestamp", timestampText)
	request.Header.Set("X-Telesrv-Signature", signatureFor([]byte(secret), timestampText, body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
