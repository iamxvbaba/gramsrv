package memory

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestEphemeralMessageStoreCreateReplayEditDeleteAndExpiry(t *testing.T) {
	ctx := context.Background()
	store := NewEphemeralMessageStore()
	now := time.Unix(1_800_000_000, 0)
	message := testEphemeralMessage(now)
	created, fresh, err := store.CreateEphemeralMessage(ctx, message)
	if err != nil || !fresh || created.ID != message.ID {
		t.Fatalf("create = %+v fresh=%v err=%v", created, fresh, err)
	}

	replayed, fresh, err := store.CreateEphemeralMessage(ctx, message)
	if err != nil || fresh || replayed.Version != 1 {
		t.Fatalf("replay = %+v fresh=%v err=%v", replayed, fresh, err)
	}
	conflict := message
	conflict.ID++
	conflict.PayloadHash = sha256.Sum256([]byte("different"))
	if _, _, err := store.CreateEphemeralMessage(ctx, conflict); !errors.Is(err, domain.ErrEphemeralRandomIDConflict) {
		t.Fatalf("random-id conflict err=%v", err)
	}

	edited, err := store.EditEphemeralMessage(ctx, message.Peer, message.ID, 1, domain.EphemeralContent{Message: "edited"}, int(now.Unix())+1, now)
	if err != nil || edited.Version != 2 || edited.Content.Message != "edited" {
		t.Fatalf("edit = %+v err=%v", edited, err)
	}
	if _, err := store.EditEphemeralMessage(ctx, message.Peer, message.ID, 1, domain.EphemeralContent{Message: "stale"}, int(now.Unix())+2, now); !errors.Is(err, domain.ErrEphemeralVersionConflict) {
		t.Fatalf("stale edit err=%v", err)
	}

	deleted, changed, err := store.DeleteEphemeralMessage(ctx, message.Peer, message.ID, 2, now)
	if err != nil || !changed || !deleted.Deleted || deleted.Version != 3 || deleted.Content.Message != "" {
		t.Fatalf("delete = %+v changed=%v err=%v", deleted, changed, err)
	}
	deleted, changed, err = store.DeleteEphemeralMessage(ctx, message.Peer, message.ID, 3, now)
	if err != nil || changed || !deleted.Deleted {
		t.Fatalf("repeat delete = %+v changed=%v err=%v", deleted, changed, err)
	}
	if _, err := store.EditEphemeralMessage(ctx, message.Peer, message.ID, 3, domain.EphemeralContent{Message: "resurrect"}, int(now.Unix())+3, now); !errors.Is(err, domain.ErrEphemeralDeleted) {
		t.Fatalf("edit deleted err=%v", err)
	}

	if _, found, err := store.GetEphemeralMessage(ctx, message.Peer, message.ID, message.ExpiresAt); err != nil || found {
		t.Fatalf("expired found=%v err=%v", found, err)
	}
}

func TestEphemeralMessageStoreIDCollisionAndBoundedPrune(t *testing.T) {
	ctx := context.Background()
	store := NewEphemeralMessageStore()
	now := time.Unix(1_800_000_100, 0)
	first := testEphemeralMessage(now)
	if _, _, err := store.CreateEphemeralMessage(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.RandomID++
	second.PayloadHash = sha256.Sum256([]byte("second"))
	if _, _, err := store.CreateEphemeralMessage(ctx, second); !errors.Is(err, domain.ErrEphemeralIDCollision) {
		t.Fatalf("id collision err=%v", err)
	}
	if got, err := store.PruneExpiredEphemeralMessages(ctx, first.ExpiresAt, 1); err != nil || got != 1 {
		t.Fatalf("prune=%d err=%v", got, err)
	}
}

func TestEphemeralCallbackActionExactBotAndExpiry(t *testing.T) {
	ctx := context.Background()
	store := NewEphemeralMessageStore()
	now := time.Unix(1_800_000_000, 0)
	action := domain.EphemeralCallbackAction{
		QueryID: 81, BotUserID: 2001, UserID: 3001,
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 1001}, MessageID: 17, TopMessageID: 42,
		Device:    domain.EphemeralDevice{UserID: 3001, BusinessAuthKeyID: [8]byte{1}, SessionID: 9},
		CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralReplyWindow),
	}
	if created, err := store.PutEphemeralCallbackAction(ctx, action); err != nil || !created {
		t.Fatalf("put created=%v err=%v", created, err)
	}
	if created, err := store.PutEphemeralCallbackAction(ctx, action); err != nil || created {
		t.Fatalf("duplicate created=%v err=%v", created, err)
	}
	if _, found, err := store.GetEphemeralCallbackAction(ctx, action.BotUserID+1, action.QueryID, now); err != nil || found {
		t.Fatalf("wrong bot found=%v err=%v", found, err)
	}
	got, found, err := store.GetEphemeralCallbackAction(ctx, action.BotUserID, action.QueryID, now)
	if err != nil || !found || got.TopMessageID != 42 {
		t.Fatalf("get=%+v found=%v err=%v", got, found, err)
	}
	if _, found, err := store.GetEphemeralCallbackAction(ctx, action.BotUserID, action.QueryID, action.ExpiresAt); err != nil || found {
		t.Fatalf("expired found=%v err=%v", found, err)
	}
}

func TestEphemeralCallbackActionBoundedHeapPrune(t *testing.T) {
	ctx := context.Background()
	store := NewEphemeralMessageStore()
	now := time.Unix(1_800_000_000, 0)
	action := domain.EphemeralCallbackAction{
		QueryID: 82, BotUserID: 2001, UserID: 3001,
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 1001}, MessageID: 17,
		Device:    domain.EphemeralDevice{UserID: 3001, BusinessAuthKeyID: [8]byte{1}, SessionID: 9},
		CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralReplyWindow),
	}
	if created, err := store.PutEphemeralCallbackAction(ctx, action); err != nil || !created {
		t.Fatalf("put created=%v err=%v", created, err)
	}
	if _, err := store.PruneExpiredEphemeralMessages(ctx, action.ExpiresAt, 1); err != nil {
		t.Fatalf("prune err=%v", err)
	}
	shard := &store.callbackActions[uint64(action.QueryID)&(ephemeralShardCount-1)]
	shard.mu.RLock()
	_, found := shard.actions[action.QueryID]
	shard.mu.RUnlock()
	if found {
		t.Fatal("expired callback action survived bounded heap prune")
	}
}

func TestEphemeralReportStoreIdempotency(t *testing.T) {
	store := NewEphemeralReportStore()
	now := time.Unix(1_800_000_000, 0)
	message := testEphemeralMessage(now)
	message.ReceiverUserID = 3001
	report := domain.NewEphemeralAbuseReport(message.ReceiverUserID, "spam", "evidence", message, now)
	if created, err := store.CreateEphemeralReport(context.Background(), report); err != nil || !created {
		t.Fatalf("create=%v err=%v", created, err)
	}
	if created, err := store.CreateEphemeralReport(context.Background(), report); err != nil || created {
		t.Fatalf("retry create=%v err=%v", created, err)
	}
	reports := store.Reports()
	if len(reports) != 1 || reports[0].Evidence.Content.Message != message.Content.Message {
		t.Fatalf("reports=%+v", reports)
	}
}

func testEphemeralMessage(now time.Time) domain.EphemeralMessage {
	return domain.EphemeralMessage{
		ID:             17,
		Peer:           domain.Peer{Type: domain.PeerTypeChannel, ID: 1001},
		SenderUserID:   2001,
		ReceiverUserID: 3001,
		Date:           int(now.Unix()),
		RandomID:       99,
		Content:        domain.EphemeralContent{Message: "/private"},
		PayloadHash:    sha256.Sum256([]byte("payload")),
		Version:        1,
		CreatedAt:      now,
		ExpiresAt:      now.Add(domain.EphemeralMessageRetention),
	}
}

func BenchmarkEphemeralMessageStoreParallelCreate(b *testing.B) {
	store := NewEphemeralMessageStore()
	base := time.Unix(1_800_000_000, 0)
	ctx := context.Background()
	var sequence atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := sequence.Add(1)
			message := testEphemeralMessage(base)
			message.ID = int(n%1_000_000) + 1
			message.Peer.ID += n
			message.RandomID += n
			message.PayloadHash = sha256.Sum256([]byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)})
			if _, _, err := store.CreateEphemeralMessage(ctx, message); err != nil {
				b.Errorf("create: %v", err)
			}
		}
	})
}
