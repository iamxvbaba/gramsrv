package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	MaxProviderRequest  = 4 << 20
	MaxProviderResponse = 16 << 20
)

var (
	ErrProviderRequest     = errors.New("invalid wallet provider request")
	ErrProviderResponse    = errors.New("invalid wallet provider response")
	ErrProviderUnavailable = errors.New("wallet provider unavailable")
)

// ProviderHTTPError carries only the upstream status. URLs, credentials and
// bodies are intentionally excluded from errors that may be logged by RPC.
type ProviderHTTPError struct{ Status int }

func (e *ProviderHTTPError) Error() string {
	return fmt.Sprintf("wallet provider HTTP status %d", e.Status)
}

// Toncenter implements the transport of toncenter.performApiRequest. It never
// retries: the same endpoint can submit a signed transfer as well as read data.
type Toncenter struct {
	client *http.Client
	apiKey string
}

func NewToncenter(apiKey string) *Toncenter {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxConnsPerHost = 16
	transport.MaxIdleConnsPerHost = 16
	transport.ResponseHeaderTimeout = 15 * time.Second
	return &Toncenter{client: &http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, apiKey: apiKey}
}

// Perform accepts a path, never a caller-selected origin. A nil payload selects
// GET; a present payload selects POST, preserving the TL optional distinction.
func (p *Toncenter) Perform(ctx context.Context, endpoint string, query, payload *string) (string, error) {
	if p == nil || p.client == nil {
		return "", ErrProviderUnavailable
	}
	if len(endpoint) > 2048 || len(endpoint) == 0 || endpoint[0] != '/' || strings.ContainsAny(endpoint, "\\%?#\r\n\x00") || path.Clean(endpoint) != endpoint {
		return "", ErrProviderRequest
	}
	if !strings.HasPrefix(endpoint, "/api/v2/") && !strings.HasPrefix(endpoint, "/api/v3/") {
		return "", ErrProviderRequest
	}
	u := url.URL{Scheme: "https", Host: "toncenter.com", Path: endpoint}
	if query != nil {
		if len(*query) > 64<<10 || strings.ContainsAny(*query, "\r\n\x00#") {
			return "", ErrProviderRequest
		}
		values, err := url.ParseQuery(*query)
		if err != nil {
			return "", ErrProviderRequest
		}
		// Provider credentials belong to server configuration, not request input.
		for k := range values {
			if strings.EqualFold(k, "api_key") || strings.EqualFold(k, "x-api-key") {
				return "", ErrProviderRequest
			}
		}
		u.RawQuery = *query
	}
	method := http.MethodGet
	var body io.Reader
	if payload != nil {
		if len(*payload) > MaxProviderRequest || !json.Valid([]byte(*payload)) {
			return "", ErrProviderRequest
		}
		method = http.MethodPost
		body = strings.NewReader(*payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return "", ErrProviderRequest
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.apiKey != "" {
		req.Header.Set("X-API-Key", p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrProviderUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &ProviderHTTPError{Status: resp.StatusCode}
	}
	if resp.ContentLength > MaxProviderResponse {
		return "", ErrProviderResponse
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxProviderResponse+1))
	if err != nil || len(data) > MaxProviderResponse || !json.Valid(data) {
		return "", ErrProviderResponse
	}
	// Preserve JSON-RPC errors and IDs verbatim. HTTP success does not imply a
	// successful JSON-RPC result or a confirmed blockchain transaction.
	return string(data), nil
}
