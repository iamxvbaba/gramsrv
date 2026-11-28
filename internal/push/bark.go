package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// errNoBark means Bark delivery was requested without a configured server.
var errNoBark = errors.New("bark is not configured")

// barkSubscription is the optional JSON form of a Bark token. A bare token is
// treated as the device key.
type barkSubscription struct {
	Endpoint string `json:"endpoint"`
	Key      string `json:"key"`
	Sound    string `json:"sound"`
	Group    string `json:"group"`
	URL      string `json:"url"`
	Icon     string `json:"icon"`
}

func parseBarkToken(token string) barkSubscription {
	var sub barkSubscription
	if err := json.Unmarshal([]byte(token), &sub); err == nil && sub.Key != "" {
		return sub
	}
	return barkSubscription{Key: strings.TrimSpace(token)}
}

// BarkSender delivers notifications through a (self-hosted) Bark server, which
// relays them to iOS via the Bark app. See https://github.com/Finb/Bark.
type BarkSender struct {
	server string
	sound  string
	group  string
	client *http.Client
}

// NewBarkSender builds a Bark sender. server is required (empty disables Bark);
// sound/group are defaults overridable per token.
func NewBarkSender(server, sound, group string, timeout time.Duration) (*BarkSender, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if server == "" {
		return nil, errNoBark
	}
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		server = "https://" + server
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &BarkSender{
		server: server,
		sound:  sound,
		group:  group,
		client: &http.Client{Timeout: timeout},
	}, nil
}

// Send posts one notification to the Bark push endpoint.
func (s *BarkSender) Send(ctx context.Context, token string, n Notification) error {
	sub := parseBarkToken(token)
	if sub.Key == "" {
		return errors.New("bark: empty device key")
	}
	endpoint := s.server
	if sub.Endpoint != "" {
		endpoint = strings.TrimRight(sub.Endpoint, "/")
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = "https://" + endpoint
		}
	}
	title := strings.TrimSpace(n.Title)
	if title == "" {
		title = "Telegram"
	}
	body := strings.TrimSpace(n.Description)
	if body == "" {
		body = "You have a new message"
	}
	payload := map[string]any{
		"device_key": sub.Key,
		"title":      title,
		"body":       body,
		"isArchive":  1,
	}
	payload["level"] = "active"
	if n.Muted {
		// MESSAGE_MUTED is a silent badge refresh: no sound, passive delivery.
		payload["level"] = "passive"
	}
	if sound := firstNonEmpty(sub.Sound, s.sound); sound != "" {
		payload["sound"] = sound
	}
	if group := firstNonEmpty(sub.Group, s.group); group != "" {
		payload["group"] = group
	}
	if sub.URL != "" {
		payload["url"] = sub.URL
	}
	if sub.Icon != "" {
		payload["icon"] = sub.Icon
	}
	return s.post(ctx, endpoint+"/push", payload, nil)
}

func (s *BarkSender) post(ctx context.Context, endpoint string, payload map[string]any, headers map[string]string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return ErrTokenUnregistered
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bark: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	// Bark answers 200 with {"code":200,...}; non-200 codes are errors.
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &parsed); err == nil && parsed.Code != 0 && parsed.Code != 200 {
		if parsed.Code == http.StatusNotFound {
			return ErrTokenUnregistered
		}
		return fmt.Errorf("bark: code %d: %s", parsed.Code, parsed.Message)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
