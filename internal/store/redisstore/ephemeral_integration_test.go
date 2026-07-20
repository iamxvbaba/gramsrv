package redisstore

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestRedisEphemeralAtomicLifecycleCallbackAndBroker(t *testing.T) {
	addr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TELESRV_TEST_REDIS_ADDR to run redis integration test")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	ctx := context.Background()
	storeImpl := NewEphemeralMessageStore(client)
	now := time.Now()
	seed := now.UnixNano() & 0x3fffffff
	message := domain.EphemeralMessage{
		ID: int(seed) + 1, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: seed + 2},
		SenderUserID: seed + 3, ReceiverUserID: seed + 4, Date: int(now.Unix()), RandomID: seed + 5,
		Content: domain.EphemeralContent{Message: "/private"}, PayloadHash: sha256.Sum256([]byte("payload")),
		Version: 1, CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralMessageRetention),
	}
	t.Cleanup(func() {
		_ = client.Del(context.Background(), ephemeralMessageKey(message.Peer, message.ID), ephemeralRandomKey(message), ephemeralCallbackActionKey(seed+6)).Err()
	})
	created, fresh, err := storeImpl.CreateEphemeralMessage(ctx, message)
	if err != nil || !fresh || created.ID != message.ID {
		t.Fatalf("create=%+v fresh=%v err=%v", created, fresh, err)
	}
	replay, fresh, err := storeImpl.CreateEphemeralMessage(ctx, message)
	if err != nil || fresh || replay.ID != message.ID {
		t.Fatalf("replay=%+v fresh=%v err=%v", replay, fresh, err)
	}
	edited, err := storeImpl.EditEphemeralMessage(ctx, message.Peer, message.ID, 1, domain.EphemeralContent{Message: "edited"}, message.Date+1, now)
	if err != nil || edited.Version != 2 || edited.Content.Message != "edited" {
		t.Fatalf("edit=%+v err=%v", edited, err)
	}
	deleted, changed, err := storeImpl.DeleteEphemeralMessage(ctx, message.Peer, message.ID, 2, now)
	if err != nil || !changed || !deleted.Deleted || deleted.Version != 3 {
		t.Fatalf("delete=%+v changed=%v err=%v", deleted, changed, err)
	}

	action := domain.EphemeralCallbackAction{
		QueryID: seed + 6, BotUserID: seed + 3, UserID: seed + 4, Peer: message.Peer,
		MessageID: message.ID, TopMessageID: 42,
		Device:    domain.EphemeralDevice{UserID: seed + 4, BusinessAuthKeyID: [8]byte{7}, SessionID: 8},
		CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralReplyWindow),
	}
	if created, err := storeImpl.PutEphemeralCallbackAction(ctx, action); err != nil || !created {
		t.Fatalf("put callback created=%v err=%v", created, err)
	}
	got, found, err := storeImpl.GetEphemeralCallbackAction(ctx, action.BotUserID, action.QueryID, now)
	if err != nil || !found || got.TopMessageID != 42 || got.Device.BusinessAuthKeyID != action.Device.BusinessAuthKeyID {
		t.Fatalf("callback=%+v found=%v err=%v", got, found, err)
	}

	brokerCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	received := make(chan store.EphemeralPush, 1)
	go func() {
		_ = storeImpl.SubscribeEphemeralPushes(brokerCtx, func(_ context.Context, event store.EphemeralPush) {
			select {
			case received <- event:
			default:
			}
		})
	}()
	event := store.EphemeralPush{
		SourceID: "redis-test", Kind: store.EphemeralPushDelete,
		TargetUserID: message.ReceiverUserID, Message: deleted, Date: int(now.Unix()),
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := storeImpl.PublishEphemeralPush(ctx, event); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-received:
			if got.SourceID != event.SourceID || got.Message.ID != event.Message.ID || got.Kind != event.Kind {
				t.Fatalf("broker event=%+v", got)
			}
			return
		case <-brokerCtx.Done():
			t.Fatal("redis ephemeral broker did not deliver")
		case <-ticker.C:
		}
	}
}
