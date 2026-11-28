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

// errNoNtfy means ntfy delivery was requested without a configured server.
var errNoNtfy = errors.New("ntfy is not configured")

// ntfySubscription is the optional JSON form of an ntfy token. A bare token is
// treated as the topic name.
type ntfySubscription struct {
	Endpoint string   `json:"endpoint"`
	Topic    string   `json:"topic"`
	Token    string   `json:"token"`
	Click    string   `json:"click"`
	Tags     []string `json:"tags"`
	Priority int      `json:"priority"`
}

func parseNtfyToken(token string) ntfySubscription {
	var sub ntfySubscription
	if err := json.Unmarshal([]byte(token), &sub); err == nil && sub.Topic != "" {
		return sub
	}
	return ntfySubscription{Topic: strings.TrimSpace(token)}
}

// NtfySender delivers notifications through a (self-hosted) ntfy server. See
// https://ntfy.sh and https://github.com/binwiederhier/ntfy.
type NtfySender struct {
	server    string
	authToken string
	client    *http.Client
}

// NewNtfySender builds an ntfy sender. server is required (empty disables ntfy);
// authToken is an optional bearer token for protected self-hosted servers.
func NewNtfySender(server, authToken string, timeout time.Duration) (*NtfySender, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if server == "" {
		return nil, errNoNtfy
	}
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		server = "https://" + server
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &NtfySender{
		server:    server,
		authToken: authToken,
		client:    &http.Client{Timeout: timeout},
	}, nil
}

// Send publishes one notification to the ntfy topic. ntfy JSON publishing uses
// POST {server}/{topic} with the message fields in the body.
func (s *NtfySender) Send(ctx context.Context, token string, n Notification) error {
	sub := parseNtfyToken(token)
	if sub.Topic == "" {
		return errors.New("ntfy: empty topic")
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
	message := strings.TrimSpace(n.Description)
	if message == "" {
		message = "You have a new message"
	}
	priority := sub.Priority
	if priority == 0 {
		priority = 4
	}
	if n.Muted {
		// MESSAGE_MUTED: silent badge refresh.
		priority = 1
	}
	payload := map[string]any{
		"title":    title,
		"message":  message,
		"priority": priority,
	}
	if sub.Click != "" {
		payload["click"] = sub.Click
	}
	if len(sub.Tags) > 0 {
		payload["tags"] = sub.Tags
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/"+sub.Topic, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth := firstNonEmpty(sub.Token, s.authToken); auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return fmt.Errorf("ntfy: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}
