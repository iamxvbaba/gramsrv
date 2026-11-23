package push

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type captureSender struct {
	calls     int
	token     string
	data      map[string]string
	returnErr error
}

func (s *captureSender) Send(_ context.Context, deviceToken string, data map[string]string) error {
	s.calls++
	s.token = deviceToken
	s.data = data
	return s.returnErr
}

func TestServiceSendSealsPayload(t *testing.T) {
	sender := &captureSender{}
	service := NewService(sender, nil, zaptest.NewLogger(t))
	secret := make([]byte, 256)
	for i := range secret {
		secret[i] = byte(i)
	}
	err := service.Send(context.Background(), domain.PushToken{TokenType: domain.PushTokenTypeFCM, Token: "device-token", Secret: secret}, Notification{
		LocKey: "MESSAGE_TEXT", LocArgs: []string{"Alice", "hi"}, UserID: 5,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sender.calls != 1 || sender.token != "device-token" {
		t.Fatalf("sender calls=%d token=%q", sender.calls, sender.token)
	}
	sealed, ok := sender.data["p"]
	if !ok {
		t.Fatalf("data = %+v, want a p field", sender.data)
	}
	decoded := decryptPushPayload(t, secret, sealed)
	var notif Notification
	if err := json.Unmarshal(decoded, &notif); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if notif.LocKey != "MESSAGE_TEXT" || notif.UserID != 5 {
		t.Fatalf("decrypted = %+v", notif)
	}
}

func TestServiceSendSkipsTokenWithoutSecret(t *testing.T) {
	sender := &captureSender{}
	service := NewService(sender, nil, zaptest.NewLogger(t))
	err := service.Send(context.Background(), domain.PushToken{TokenType: domain.PushTokenTypeFCM, Token: "device-token"}, Notification{LocKey: "MESSAGE_TEXT"})
	if err != errSecretTooShort {
		t.Fatalf("no-secret token = %v, want errSecretTooShort", err)
	}
	if sender.calls != 0 {
		t.Fatalf("sender was called %d times for a token without a secret", sender.calls)
	}
}

func TestWebPushPayloadShape(t *testing.T) {
	n := Notification{
		LocKey:      "MESSAGE_TEXT",
		LocArgs:     []string{"Alice", "hi"},
		Custom:      map[string]any{"from_id": int64(42), "msg_id": 7},
		Title:       "Alice",
		Description: "hi",
		UserID:      5,
	}
	payload, err := n.webPushPayload(5)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if obj["loc_key"] != "MESSAGE_TEXT" || obj["title"] != "Alice" || obj["description"] != "hi" {
		t.Fatalf("payload = %s", payload)
	}
	if obj["mute"] != "0" {
		t.Fatalf("mute = %v, want 0", obj["mute"])
	}
	if obj["user_id"].(float64) != 5 {
		t.Fatalf("user_id = %v", obj["user_id"])
	}
	n.Muted = true
	payload, _ = n.webPushPayload(5)
	_ = json.Unmarshal(payload, &obj)
	if obj["mute"] != "1" {
		t.Fatalf("muted payload mute = %v, want 1", obj["mute"])
	}
}
