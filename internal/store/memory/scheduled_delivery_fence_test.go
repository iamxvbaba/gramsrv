package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
)

// D6: the memory store mirrors the PostgreSQL scheduled fence. A stale claim
// cannot send, and a fresh claim commits the message, the sent transition,
// the repeat occurrence and one durable owner delivery together.
func TestScheduledDeliveryFenceAndCompletionMemory(t *testing.T) {
	ctx := context.Background()
	const owner, recipient = int64(5101), int64(5102)
	const now = 1_800_001_000
	messages := NewMessageStore(NewDialogStore())
	outbox := NewDeliveryOutboxStore()
	messages.AttachDeliveryOutbox(outbox)
	messages.AttachUpdateEventStore(NewUpdateEventStore())
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient}
	build := func(msg domain.ScheduledMessage) ([]storepkg.DeliveryEffect, error) {
		return []storepkg.DeliveryEffect{memoryAbsoluteEffect(msg.OwnerUserID)}, nil
	}
	created, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
		OwnerUserID: owner, Peer: peer, RandomID: 7001, Message: "repeat",
		ScheduleDate: now, ScheduleRepeatPeriod: 86400, Date: now - 60,
	}, build)
	if err != nil {
		t.Fatalf("create scheduled: %v", err)
	}
	claim := func(date int) domain.ScheduledMessage {
		t.Helper()
		items, err := messages.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{OwnerUserID: owner, Peer: peer, IDs: []int{created.ID}, Now: date, LeaseUntil: date + 60})
		if err != nil || len(items) != 1 {
			t.Fatalf("claim = %+v err=%v", items, err)
		}
		return items[0]
	}
	var completions []domain.ScheduledCompletion
	send := func(msg domain.ScheduledMessage) (domain.SendPrivateTextResult, error) {
		return messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID: owner, RecipientUserID: recipient, RandomID: msg.RandomID, Message: msg.Message, Date: now + 62,
			ScheduledDelivery: &domain.ScheduledDelivery{
				OwnerUserID: owner, ID: msg.ID, Generation: msg.Generation,
				Payload: func(c domain.ScheduledCompletion) ([]byte, error) {
					completions = append(completions, c)
					return []byte{1}, nil
				},
			},
		})
	}
	stale := claim(now)
	active := claim(now + 61) // lease expired: reclaim bumps the generation
	before := len(outbox.Snapshot())
	if _, err := send(stale); !errors.Is(err, domain.ErrScheduledClaimLost) {
		t.Fatalf("stale claim err = %v, want ErrScheduledClaimLost", err)
	}
	if got := len(outbox.Snapshot()); got != before || len(completions) != 0 {
		t.Fatalf("stale claim wrote delivery: %d -> %d completions=%d", before, got, len(completions))
	}
	if err := messages.ReleaseScheduledMessage(ctx, owner, stale.ID, stale.Generation, "stale worker"); err != nil {
		t.Fatal(err)
	}
	result, err := send(active)
	if err != nil {
		t.Fatalf("send active claim: %v", err)
	}
	if result.NextScheduled == nil || result.NextScheduled.ScheduleDate != now+62+86400 || result.NextScheduled.RandomID == active.RandomID {
		t.Fatalf("next occurrence = %+v", result.NextScheduled)
	}
	if got := len(outbox.Snapshot()); got != before+1 || len(completions) != 1 ||
		completions[0].Sent.SentMessageID != result.SenderMessage.ID || completions[0].Next == nil {
		t.Fatalf("completion delivery: outbox %d -> %d completions=%+v", before, got, completions)
	}
	pending, err := messages.ListScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner, Peer: peer})
	if err != nil || len(pending.Messages) != 1 || pending.Messages[0].ID != result.NextScheduled.ID {
		t.Fatalf("pending after delivery = %+v err=%v", pending.Messages, err)
	}
}

func TestMemoryChannelStoreRejectsScheduledDelivery(t *testing.T) {
	channels := NewChannelStore()
	_, err := channels.SendChannelMessage(context.Background(), domain.SendChannelMessageRequest{
		UserID: 1, ChannelID: 2, Message: "scheduled",
		ScheduledDelivery: &domain.ScheduledDelivery{OwnerUserID: 1, ID: 1, Generation: 1},
	})
	if !errors.Is(err, errMemoryChannelScheduledDelivery) {
		t.Fatalf("channel scheduled delivery err = %v, want fail-closed rejection", err)
	}
}
