package push

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestNtfySenderSend(t *testing.T) {
	var gotPath, gotAuth string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender, err := NewNtfySender(srv.URL, "tk_secret", time.Second)
	if err != nil {
		t.Fatalf("new ntfy: %v", err)
	}
	if err := sender.Send(context.Background(), "mytopic", Notification{Title: "Alice", Description: "hi there"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/" {
		t.Fatalf("path = %q, want / (JSON publishing posts to the base URL)", gotPath)
	}
	if gotAuth != "Bearer tk_secret" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if got["topic"] != "mytopic" || got["title"] != "Alice" || got["message"] != "hi there" {
		t.Fatalf("ntfy payload = %v", got)
	}
	if got["priority"].(float64) != 4 {
		t.Fatalf("priority = %v, want 4", got["priority"])
	}

	if err := sender.Send(context.Background(), "mytopic", Notification{Muted: true}); err != nil {
		t.Fatalf("muted send: %v", err)
	}
	if got["priority"].(float64) != 1 {
		t.Fatalf("muted priority = %v, want 1", got["priority"])
	}

	token := `{"topic":"other","endpoint":` + jsonString(srv.URL) + `,"token":"tk_x","click":"srv://teleram.ru"}`
	if err := sender.Send(context.Background(), token, Notification{Title: "T", Description: "B"}); err != nil {
		t.Fatalf("json token send: %v", err)
	}
	if gotPath != "/" || gotAuth != "Bearer tk_x" || got["topic"] != "other" || got["click"] != "srv://teleram.ru" {
		t.Fatalf("json token: path=%q auth=%q topic=%v click=%v", gotPath, gotAuth, got["topic"], got["click"])
	}
}

func TestNtfySenderValidation(t *testing.T) {
	if _, err := NewNtfySender("", "", time.Second); err != errNoNtfy {
		t.Fatalf("empty server = %v, want errNoNtfy", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	sender, _ := NewNtfySender(srv.URL, "", time.Second)
	if err := sender.Send(context.Background(), "  ", Notification{}); err == nil {
		t.Fatal("empty topic accepted")
	}
}

func TestServiceDispatchesNtfy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sender, _ := NewNtfySender(srv.URL, "", time.Second)
	service := NewService(nil, nil, nil).WithNtfy(sender)
	if err := service.Send(context.Background(), domain.PushToken{TokenType: domain.PushTokenTypeNtfy, Token: "topic"}, Notification{Title: "t"}); err != nil {
		t.Fatalf("ntfy dispatch: %v", err)
	}
	if err := NewService(nil, nil, nil).Send(context.Background(), domain.PushToken{TokenType: domain.PushTokenTypeNtfy, Token: "topic"}, Notification{}); err != errNoNtfy {
		t.Fatalf("no ntfy sender = %v, want errNoNtfy", err)
	}
}
