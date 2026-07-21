package botapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"telesrv/internal/domain"
)

type recordingWebhookGateway struct {
	*fakeBotAPIGateway
	mu            sync.Mutex
	failure       string
	failureNext   time.Time
	successNext   time.Time
	recordedOwner string
}

func (g *recordingWebhookGateway) RecordBotAPIWebhookFailure(_ context.Context, _ int64, owner string, next time.Time, message string) error {
	g.mu.Lock()
	g.recordedOwner, g.failure, g.failureNext = owner, message, next
	g.mu.Unlock()
	return nil
}

func (g *recordingWebhookGateway) RecordBotAPIWebhookSuccess(_ context.Context, _ int64, owner string, next time.Time) error {
	g.mu.Lock()
	g.recordedOwner, g.successNext = owner, next
	g.mu.Unlock()
	return nil
}

func webhookEvents(ids ...int) []domain.UpdateEvent {
	out := make([]domain.UpdateEvent, 0, len(ids))
	for _, id := range ids {
		out = append(out, domain.UpdateEvent{
			UserID: 1001, Type: domain.UpdateEventNewMessage, Pts: id, Date: 1700000000 + id,
			Message: domain.Message{
				ID: id, OwnerUserID: 1001, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
				From: domain.Peer{Type: domain.PeerTypeUser, ID: 2001}, Date: 1700000000 + id, Body: "message", Out: false,
			},
			Users: []domain.User{{ID: 2001, FirstName: "Alice"}},
		})
	}
	return out
}

func TestWebhookDispatcherPostsInParallelWithSecretAndConfirmsContiguousBatch(t *testing.T) {
	var mu sync.Mutex
	received := make(map[int]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token"); got != "secret_1" {
			t.Errorf("secret header = %q", got)
		}
		var update struct {
			UpdateID int `json:"update_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			t.Errorf("decode webhook: %v", err)
		}
		mu.Lock()
		received[update.UpdateID] = true
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	base := &fakeBotAPIGateway{
		updates:      webhookEvents(11, 12, 13),
		webhook:      domain.BotAPIWebhook{BotUserID: 1001, URL: server.URL, SecretToken: "secret_1", MaxConnections: 3},
		webhookFound: true,
	}
	gateway := &recordingWebhookGateway{fakeBotAPIGateway: base}
	d := &webhookDispatcher{control: gateway, gateway: gateway, client: server.Client(), logger: zap.NewNop(), botSem: make(chan struct{}, 1), httpSem: make(chan struct{}, 8)}
	d.deliver(context.Background(), base.webhook)

	mu.Lock()
	count := len(received)
	mu.Unlock()
	if count != 3 || base.webhookConfirmed != 13 {
		t.Fatalf("received=%v confirmed=%d", received, base.webhookConfirmed)
	}
	gateway.mu.Lock()
	successNext, failure := gateway.successNext, gateway.failure
	gateway.mu.Unlock()
	if !successNext.After(time.Now().Add(30*time.Minute)) || failure != "" {
		t.Fatalf("success next=%v failure=%q", successNext, failure)
	}
}

func TestWebhookDispatcherOnlyConfirmsSuccessfulPrefixAndSchedulesRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var update struct {
			UpdateID int `json:"update_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&update)
		if update.UpdateID == 22 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	base := &fakeBotAPIGateway{
		updates:      webhookEvents(21, 22, 23),
		webhook:      domain.BotAPIWebhook{BotUserID: 1001, URL: server.URL, MaxConnections: 3},
		webhookFound: true,
	}
	gateway := &recordingWebhookGateway{fakeBotAPIGateway: base}
	logCore, observedLogs := observer.New(zap.WarnLevel)
	d := &webhookDispatcher{control: gateway, gateway: gateway, client: server.Client(), logger: zap.New(logCore), botSem: make(chan struct{}, 1), httpSem: make(chan struct{}, 8)}
	d.deliver(context.Background(), base.webhook)

	gateway.mu.Lock()
	failure, retryAt := gateway.failure, gateway.failureNext
	gateway.mu.Unlock()
	if base.webhookConfirmed != 21 || failure != "webhook returned HTTP 503" || !retryAt.After(time.Now()) {
		t.Fatalf("confirmed=%d failure=%q retry=%v", base.webhookConfirmed, failure, retryAt)
	}
	entries := observedLogs.FilterMessage("bot api webhook delivery failed").All()
	if len(entries) != 1 {
		t.Fatalf("delivery failure warning count = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["bot_user_id"] != int64(1001) || fields["reason"] != "webhook returned HTTP 503" {
		t.Fatalf("delivery failure warning fields = %#v", fields)
	}
	if _, ok := fields["url"]; ok {
		t.Fatalf("delivery failure warning must not include webhook URL: %#v", fields)
	}
	if _, ok := fields["secret_token"]; ok {
		t.Fatalf("delivery failure warning must not include webhook secret: %#v", fields)
	}
}
