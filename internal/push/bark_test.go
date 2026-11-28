package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestBarkSenderSend(t *testing.T) {
	var gotPath string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"code":200,"message":"success"}`)
	}))
	defer srv.Close()

	sender, err := NewBarkSender(srv.URL, "birdsong", "Telegram", time.Second)
	if err != nil {
		t.Fatalf("new bark: %v", err)
	}
	if err := sender.Send(context.Background(), "devicekey123", Notification{Title: "Alice", Description: "hi there"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/push" {
		t.Fatalf("path = %q, want /push", gotPath)
	}
	if got["device_key"] != "devicekey123" || got["title"] != "Alice" || got["body"] != "hi there" {
		t.Fatalf("bark payload = %v", got)
	}
	if got["sound"] != "birdsong" || got["group"] != "Telegram" || got["level"] != "active" {
		t.Fatalf("bark defaults = %v", got)
	}

	// Muted -> passive, no sound change.
	if err := sender.Send(context.Background(), "devicekey123", Notification{Muted: true}); err != nil {
		t.Fatalf("muted send: %v", err)
	}
	if got["level"] != "passive" {
		t.Fatalf("muted level = %v, want passive", got["level"])
	}

	// JSON token overrides endpoint/sound and carries a tap URL.
	token := `{"endpoint":` + jsonString(srv.URL) + `,"key":"abc","sound":"alarm","url":"srv://teleram.ru"}`
	if err := sender.Send(context.Background(), token, Notification{Title: "T", Description: "B"}); err != nil {
		t.Fatalf("json token send: %v", err)
	}
	if got["device_key"] != "abc" || got["sound"] != "alarm" || got["url"] != "srv://teleram.ru" {
		t.Fatalf("json token payload = %v", got)
	}
}

func TestBarkSenderUnregistered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	sender, _ := NewBarkSender(srv.URL, "", "", time.Second)
	if err := sender.Send(context.Background(), "k", Notification{}); err != ErrTokenUnregistered {
		t.Fatalf("404 => %v, want ErrTokenUnregistered", err)
	}
}

func TestBarkSenderRequiresServer(t *testing.T) {
	if _, err := NewBarkSender("", "", "", time.Second); err != errNoBark {
		t.Fatalf("empty server = %v, want errNoBark", err)
	}
}

func TestServiceDispatchesBark(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":200}`)
	}))
	defer srv.Close()
	sender, _ := NewBarkSender(srv.URL, "", "", time.Second)
	service := NewService(nil, nil, nil).WithBark(sender)
	if err := service.Send(context.Background(), domain.PushToken{TokenType: domain.PushTokenTypeBark, Token: "k"}, Notification{Title: "t"}); err != nil {
		t.Fatalf("bark dispatch: %v", err)
	}
	// Without a bark sender the token is skipped, not fatal.
	if err := NewService(nil, nil, nil).Send(context.Background(), domain.PushToken{TokenType: domain.PushTokenTypeBark, Token: "k"}, Notification{}); err != errNoBark {
		t.Fatalf("no bark sender = %v, want errNoBark", err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
