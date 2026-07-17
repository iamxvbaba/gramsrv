package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/otpdelivery"
)

const (
	protocolVersion  = "1"
	maxResponseBytes = 64 << 10
)

type Config struct {
	URL     string
	Secret  string
	Timeout time.Duration
	Client  *http.Client
	Logger  *zap.Logger
}

type Sender struct {
	endpoint *url.URL
	secret   []byte
	client   *http.Client
	logger   *zap.Logger
}

func New(cfg Config) (*Sender, error) {
	endpoint, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return nil, fmt.Errorf("parse OTP webhook URL: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("OTP webhook URL scheme must be http or https")
	}
	if endpoint.Host == "" || endpoint.User != nil {
		return nil, fmt.Errorf("OTP webhook URL must contain a host and no userinfo")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Sender{endpoint: endpoint, secret: []byte(cfg.Secret), client: client, logger: logger}, nil
}

type requestBody struct {
	Version    string              `json:"version"`
	DeliveryID string              `json:"delivery_id"`
	Purpose    otpdelivery.Purpose `json:"purpose"`
	Channel    otpdelivery.Channel `json:"channel"`
	Recipient  string              `json:"recipient"`
	Code       string              `json:"code"`
	ExpiresAt  string              `json:"expires_at"`
	ExpiresIn  int64               `json:"expires_in"`
	Locale     string              `json:"locale,omitempty"`
}

type responseBody struct {
	Accepted  *bool  `json:"accepted"`
	MessageID string `json:"message_id"`
	ErrorCode string `json:"error_code"`
	Retryable bool   `json:"retryable"`
}

func (s *Sender) Deliver(ctx context.Context, delivery otpdelivery.Request) (otpdelivery.Result, error) {
	now := time.Now()
	if err := delivery.Validate(now); err != nil {
		return otpdelivery.Result{}, err
	}
	expiresIn := int64(delivery.ExpiresAt.Sub(now) / time.Second)
	if expiresIn < 1 {
		expiresIn = 1
	}
	body, err := json.Marshal(requestBody{
		Version:    protocolVersion,
		DeliveryID: delivery.DeliveryID,
		Purpose:    delivery.Purpose,
		Channel:    delivery.Channel,
		Recipient:  delivery.Recipient,
		Code:       delivery.Code,
		ExpiresAt:  delivery.ExpiresAt.UTC().Format(time.RFC3339),
		ExpiresIn:  expiresIn,
		Locale:     delivery.Locale,
	})
	if err != nil {
		return otpdelivery.Result{}, fmt.Errorf("encode OTP webhook request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return otpdelivery.Result{}, fmt.Errorf("create OTP webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Idempotency-Key", delivery.DeliveryID)
	timestamp := fmt.Sprint(now.Unix())
	req.Header.Set("X-Telesrv-Timestamp", timestamp)
	if len(s.secret) > 0 {
		req.Header.Set("X-Telesrv-Signature", signature(s.secret, timestamp, body))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Warn("OTP webhook delivery outcome is unknown",
			zap.String("delivery_id", delivery.DeliveryID),
			zap.String("purpose", string(delivery.Purpose)),
			zap.String("channel", string(delivery.Channel)),
			zap.Error(err))
		return otpdelivery.Result{}, &otpdelivery.OutcomeUnknownError{Cause: err}
	}
	defer resp.Body.Close()

	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil || len(payload) > maxResponseBytes {
		cause := readErr
		if cause == nil {
			cause = fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			s.logger.Warn("OTP webhook acknowledgement is unreadable",
				zap.String("delivery_id", delivery.DeliveryID),
				zap.Int("status", resp.StatusCode),
				zap.Error(cause))
			return otpdelivery.Result{}, &otpdelivery.OutcomeUnknownError{Cause: cause}
		}
		return otpdelivery.Result{}, &otpdelivery.RejectedError{StatusCode: resp.StatusCode, Code: "RESPONSE_UNREADABLE", Retryable: resp.StatusCode >= 500}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		provider := decodeResponse(payload)
		return otpdelivery.Result{}, &otpdelivery.RejectedError{
			StatusCode: resp.StatusCode,
			Code:       provider.ErrorCode,
			Retryable:  provider.Retryable || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
		}
	}
	if resp.StatusCode == http.StatusNoContent {
		return otpdelivery.Result{}, nil
	}
	provider := decodeResponse(payload)
	if provider.Accepted == nil {
		cause := fmt.Errorf("2xx response is missing accepted")
		s.logger.Warn("OTP webhook acknowledgement is invalid",
			zap.String("delivery_id", delivery.DeliveryID),
			zap.Int("status", resp.StatusCode),
			zap.Error(cause))
		return otpdelivery.Result{}, &otpdelivery.OutcomeUnknownError{Cause: cause}
	}
	if !*provider.Accepted {
		return otpdelivery.Result{}, &otpdelivery.RejectedError{
			StatusCode: resp.StatusCode,
			Code:       provider.ErrorCode,
			Retryable:  provider.Retryable,
		}
	}
	return otpdelivery.Result{ProviderMessageID: provider.MessageID}, nil
}

func decodeResponse(payload []byte) responseBody {
	var result responseBody
	if len(bytes.TrimSpace(payload)) == 0 {
		return result
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return responseBody{}
	}
	return result
}

func signature(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
