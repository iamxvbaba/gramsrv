package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// TestMessageTranscriptionDoneCommitsDeliveryPostgres pins the v2 rewrite of
// main's direct push: the done row and the completion delivery commit together,
// a failed builder leaves the row pending, and a second Core finishing the same
// document keeps the stored text while still notifying its own subscriber.
func TestMessageTranscriptionDoneCommitsDeliveryPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	first := createTestUser(t, ctx, users, "+1782"+suffix+"01", "TranscriptionFirst", "")
	second := createTestUser(t, ctx, users, "+1782"+suffix+"02", "TranscriptionSecond", "")
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: second.ID}
	documentID := int64(first.ID)<<8 | 0x5a
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM message_transcriptions WHERE document_id=$1`, documentID)
		_, _ = pool.Exec(ctx, `DELETE FROM edge_delivery_outbox WHERE target_user_id = ANY($1::bigint[])`, []int64{first.ID, second.ID})
	})

	st := NewMessageTranscriptionStore(pool)
	if _, created, err := st.CreateTranscriptionPending(ctx, domain.PendingTranscriptionRequest{
		DocumentID: documentID, TranscriptionID: 77, RequestedBy: first.ID, Peer: peer, MsgID: 5,
	}); err != nil || !created {
		t.Fatalf("create pending = created %v err %v", created, err)
	}
	outboxCount := func(userID int64) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1`, userID).Scan(&count); err != nil {
			t.Fatalf("count outbox: %v", err)
		}
		return count
	}
	deliverTo := func(userID int64, seen *domain.MessageTranscription) store.DeliveryEffectsBuilder[domain.MessageTranscription] {
		return func(row domain.MessageTranscription) ([]store.DeliveryEffect, error) {
			*seen = row
			return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
				TargetUserID: userID, Payload: []byte("transcribed:" + row.Text),
				RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
			})}, nil
		}
	}

	if err := st.MarkTranscriptionDone(ctx, documentID, "lost", nil); !errors.Is(err, store.ErrDeliveryOutboxRequired) {
		t.Fatalf("nil builder err = %v, want ErrDeliveryOutboxRequired", err)
	}
	injected := errors.New("injected builder failure")
	if err := st.MarkTranscriptionDone(ctx, documentID, "lost", func(domain.MessageTranscription) ([]store.DeliveryEffect, error) {
		return nil, injected
	}); !errors.Is(err, injected) {
		t.Fatalf("builder failure err = %v, want injected", err)
	}
	if row, found, err := st.GetTranscription(ctx, documentID); err != nil || !found || row.Status != domain.TranscriptionStatusPending {
		t.Fatalf("row after failed builder = %+v found %v err %v, want pending", row, found, err)
	}

	var firstSeen domain.MessageTranscription
	if err := st.MarkTranscriptionDone(ctx, documentID, "hello", deliverTo(first.ID, &firstSeen)); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if firstSeen.Status != domain.TranscriptionStatusDone || firstSeen.Text != "hello" || firstSeen.TranscriptionID != 77 {
		t.Fatalf("builder row = %+v, want done hello id 77", firstSeen)
	}
	if got := outboxCount(first.ID); got != 1 {
		t.Fatalf("first subscriber outbox = %d, want 1", got)
	}

	// Another Core ran the same document: its text loses, its subscriber is told.
	var secondSeen domain.MessageTranscription
	if err := st.MarkTranscriptionDone(ctx, documentID, "different", deliverTo(second.ID, &secondSeen)); err != nil {
		t.Fatalf("second mark done: %v", err)
	}
	if secondSeen.Text != "hello" {
		t.Fatalf("second builder text = %q, want stored hello", secondSeen.Text)
	}
	if row, _, err := st.GetTranscription(ctx, documentID); err != nil || row.Text != "hello" {
		t.Fatalf("stored text = %q err %v, want hello", row.Text, err)
	}
	if got := outboxCount(second.ID); got != 1 {
		t.Fatalf("second subscriber outbox = %d, want 1", got)
	}
}
