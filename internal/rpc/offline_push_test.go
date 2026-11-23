package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/push"
	"telesrv/internal/store/memory"
)

type bridgeCaptureSender struct {
	calls int
	err   error
}

func (s *bridgeCaptureSender) Send(context.Context, string, map[string]string) error {
	s.calls++
	return s.err
}

func TestOfflinePushBridgeDebouncesPerUser(t *testing.T) {
	log := zaptest.NewLogger(t)
	store := memory.NewPushTokenStore()
	bridge := NewOfflinePushBridge(store, push.NewService(&bridgeCaptureSender{}, nil, log), 30*time.Second, clock.System, log)

	ctx := context.Background()
	event := domain.UpdateEvent{Type: domain.UpdateEventNewMessage, UserID: 1001}
	updates := privateUpdate(1001, 42, "hello", nil, nil)

	bridge.PushOfflineUpdate(ctx, 1001, event, updates)
	if len(bridge.jobs) != 1 {
		t.Fatalf("first update enqueued %d jobs, want 1", len(bridge.jobs))
	}
	bridge.PushOfflineUpdate(ctx, 1001, event, updates)
	if len(bridge.jobs) != 1 {
		t.Fatalf("debounced update enqueued %d jobs, want 1", len(bridge.jobs))
	}
	bridge.PushOfflineUpdate(ctx, 1001, domain.UpdateEvent{Type: domain.UpdateEventEditMessage, UserID: 1001}, updates)
	if len(bridge.jobs) != 1 {
		t.Fatalf("non-new-message enqueued %d jobs, want 1", len(bridge.jobs))
	}
}

func TestOfflinePushBridgeMutesAndRefreshesBadge(t *testing.T) {
	ctx := context.Background()
	log := zaptest.NewLogger(t)
	notify := memory.NewPasswordStore()
	until := int(time.Now().Add(time.Hour).Unix())
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 42}
	if err := notify.SaveNotifySettings(ctx, 1001, domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer}, domain.PeerNotifySettings{MuteUntil: &until}); err != nil {
		t.Fatalf("seed mute: %v", err)
	}

	bridge := NewOfflinePushBridge(memory.NewPushTokenStore(), push.NewService(&bridgeCaptureSender{}, nil, log), 30*time.Second, clock.System, log).
		WithNotifyStore(notify)
	bridge.PushOfflineUpdate(ctx, 1001, domain.UpdateEvent{Type: domain.UpdateEventNewMessage, UserID: 1001}, privateUpdate(1001, 42, "hi", nil, nil))

	if len(bridge.jobs) != 1 {
		t.Fatalf("muted message enqueued %d jobs, want 1 badge refresh", len(bridge.jobs))
	}
	job := <-bridge.jobs
	if !job.muted || job.notif.LocKey != "MESSAGE_MUTED" {
		t.Fatalf("muted job = %+v", job)
	}
}

func TestOfflinePushBridgeDeliversOnlyFCMAndDeletesInvalid(t *testing.T) {
	ctx := context.Background()
	log := zaptest.NewLogger(t)
	store := memory.NewPushTokenStore()
	secret := make([]byte, 256)
	for i := range secret {
		secret[i] = byte(i)
	}
	if err := store.UpsertPushToken(ctx, domain.PushToken{UserID: 1, TokenType: domain.PushTokenTypeFCM, Token: "fcm", Secret: secret}); err != nil {
		t.Fatalf("seed fcm: %v", err)
	}
	if err := store.UpsertPushToken(ctx, domain.PushToken{UserID: 1, TokenType: domain.PushTokenTypeAPNs, Token: "apns", Secret: secret}); err != nil {
		t.Fatalf("seed apns: %v", err)
	}
	capture := &bridgeCaptureSender{}
	bridge := NewOfflinePushBridge(store, push.NewService(capture, nil, log), time.Second, clock.System, log)

	bridge.deliver(ctx, offlinePushJob{userID: 1, notif: push.Notification{LocKey: "MESSAGE_TEXT"}})
	if capture.calls != 1 {
		t.Fatalf("sent %d pushes, want 1 (FCM only; APNs is unsupported)", capture.calls)
	}

	capture.err = push.ErrTokenUnregistered
	bridge.deliver(ctx, offlinePushJob{userID: 1, notif: push.Notification{LocKey: "MESSAGE_TEXT"}})
	tokens, err := store.ListPushTokensForUser(ctx, 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, token := range tokens {
		if token.TokenType == domain.PushTokenTypeFCM {
			t.Fatalf("invalidated FCM token was not deleted: %+v", token)
		}
	}
}

func TestOfflinePushBridgeSkipsMutedBadgeForNoMutedDevice(t *testing.T) {
	ctx := context.Background()
	log := zaptest.NewLogger(t)
	store := memory.NewPushTokenStore()
	secret := make([]byte, 256)
	if err := store.UpsertPushToken(ctx, domain.PushToken{UserID: 1, TokenType: domain.PushTokenTypeFCM, Token: "quiet", Secret: secret, NoMuted: true}); err != nil {
		t.Fatalf("seed quiet: %v", err)
	}
	if err := store.UpsertPushToken(ctx, domain.PushToken{UserID: 1, TokenType: domain.PushTokenTypeFCM, Token: "loud", Secret: secret}); err != nil {
		t.Fatalf("seed loud: %v", err)
	}
	capture := &bridgeCaptureSender{}
	bridge := NewOfflinePushBridge(store, push.NewService(capture, nil, log), time.Second, clock.System, log)

	bridge.deliver(ctx, offlinePushJob{userID: 1, notif: push.Notification{LocKey: "MESSAGE_MUTED", Muted: true}, muted: true})
	if capture.calls != 1 {
		t.Fatalf("muted badge sent %d pushes, want 1 (the no_muted device is skipped)", capture.calls)
	}
}
