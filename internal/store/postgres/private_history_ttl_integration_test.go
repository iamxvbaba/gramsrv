package postgres

import (
	"context"
	"fmt"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestPrivateHistoryTTLSnapshotsAcrossSendPaths(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	for _, path := range []string{"plain", "media", "batch"} {
		t.Run(path, func(t *testing.T) {
			suffix := randomSuffix(t)
			a := createTestUser(t, ctx, users, "+1611"+suffix+"01", "TTL sender", "")
			b := createTestUser(t, ctx, users, "+1611"+suffix+"02", "TTL receiver", "")
			t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{a.ID, b.ID}) })
			messages := newTestMessageStore(pool)
			if err := messages.SetDefaultHistoryTTL(ctx, a.ID, 86400); err != nil {
				t.Fatal(err)
			}
			if err := messages.SetDefaultHistoryTTL(ctx, b.ID, 7*86400); err != nil {
				t.Fatal(err)
			}
			sequence := int64(0)
			send := func(sender, receiver int64, explicit, want int) {
				t.Helper()
				sequence++
				req := domain.SendPrivateTextRequest{SenderUserID: sender, RecipientUserID: receiver, RandomID: sequence, Message: fmt.Sprint(sequence), Date: 1800001000 + int(sequence), TTLPeriod: explicit}
				if path == "media" {
					req.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 123, AccessHash: 456}}
				}
				if path == "batch" {
					results := executePlainPrivateSendBatch(plainPrivateSendTestTasks(t, ctx, messages, []domain.SendPrivateTextRequest{req}))
					if results[0].err != nil {
						t.Fatal(results[0].err)
					}
					if results[0].result.SenderMessage.TTLPeriod != want {
						t.Fatalf("batch ttl = %d, want %d", results[0].result.SenderMessage.TTLPeriod, want)
					}
				} else {
					result, err := messages.SendPrivateText(ctx, req)
					if err != nil {
						t.Fatal(err)
					}
					if result.SenderMessage.TTLPeriod != want {
						t.Fatalf("ttl = %d, want %d", result.SenderMessage.TTLPeriod, want)
					}
				}
			}
			send(a.ID, b.ID, 60, 60) // A message-specific timer must not become the chat timer.
			for _, pair := range [][2]int64{{a.ID, b.ID}, {b.ID, a.ID}} {
				var ttl int
				if err := pool.QueryRow(ctx, "SELECT ttl_period FROM dialogs WHERE user_id=$1 AND peer_type='user' AND peer_id=$2", pair[0], pair[1]).Scan(&ttl); err != nil {
					t.Fatal(err)
				}
				if ttl != 86400 {
					t.Fatalf("dialog ttl = %d", ttl)
				}
			}
			if err := messages.SetDefaultHistoryTTL(ctx, a.ID, 30*86400); err != nil {
				t.Fatal(err)
			}
			send(b.ID, a.ID, 0, 86400)
			if err := messages.SetPrivateHistoryTTL(ctx, a.ID, domain.Peer{Type: domain.PeerTypeUser, ID: b.ID}, 0, func(domain.PrivateHistoryTTLResult) ([]store.DeliveryEffect, error) {
				// v2 TTL changes deliver to both private dialog owners.
				return append(postgresAbsoluteOwnerEffect(a.ID), postgresAbsoluteOwnerEffect(b.ID)...), nil
			}); err != nil {
				t.Fatal(err)
			}
			send(a.ID, b.ID, 0, 0)
			send(a.ID, a.ID, 0, 0)
		})
	}
}

// plainPrivateSendTestTasks builds batch actor tasks the way the actor does.
func plainPrivateSendTestTasks(t *testing.T, ctx context.Context, messages *MessageStore, requests []domain.SendPrivateTextRequest) []*plainPrivateSendBatchTask {
	t.Helper()
	tasks := make([]*plainPrivateSendBatchTask, len(requests))
	for i, req := range requests {
		fingerprint, err := store.PrivateSendFingerprint(req)
		if err != nil {
			t.Fatal(err)
		}
		tasks[i] = &plainPrivateSendBatchTask{ctx: ctx, store: messages, req: req, fingerprint: fingerprint}
	}
	return tasks
}
