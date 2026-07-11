package memory

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"telesrv/internal/domain"
)

func TestLoginCodeDeliveryStoreCommitsMessageEventDialogAndReplay(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1000000001
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	events := NewUpdateEventStore()
	deliveries := NewLoginCodeDeliveryStore(messages, events)
	req := domain.LoginCodeDeliveryRequest{
		UserID:        userID,
		PhoneCodeHash: "phone-code-hash-one",
		Code:          "12345",
		Date:          1700000000,
		ExpiresAt:     1700000300,
	}

	first, err := deliveries.DeliverLoginCodeMessage(ctx, req)
	if err != nil {
		t.Fatalf("DeliverLoginCodeMessage: %v", err)
	}
	if !first.Created || first.Message.ID != 1 || first.Message.UID != 1 || first.Message.Pts != 1 || first.Message.Out ||
		first.Message.OwnerUserID != userID || first.Message.Peer.ID != domain.OfficialSystemUserID || first.Message.From.ID != domain.OfficialSystemUserID {
		t.Fatalf("first delivery = %+v, want first incoming 777000 message", first)
	}
	if len(messages.m[userID]) != 1 || !reflect.DeepEqual(messages.m[userID][0], first.Message) {
		t.Fatalf("message projection = %+v, want committed message", messages.m[userID])
	}
	if len(events.events[userID]) != 1 {
		t.Fatalf("durable events = %+v, want one new_message", events.events[userID])
	}
	event := events.events[userID][0]
	if event.Type != domain.UpdateEventNewMessage || event.Pts != first.Message.Pts || event.PtsCount != 1 || !reflect.DeepEqual(event.Message, first.Message) {
		t.Fatalf("event = %+v, want message-identical new_message", event)
	}
	list := dialogs.m[userID]
	if len(list.Dialogs) != 1 || list.Dialogs[0].Peer.ID != domain.OfficialSystemUserID || list.Dialogs[0].TopMessage != first.Message.ID || list.Dialogs[0].UnreadCount != 1 {
		t.Fatalf("dialog projection = %+v, want unread 777000 dialog", list.Dialogs)
	}
	if len(list.Users) != 1 || list.Users[0].ID != domain.OfficialSystemUserID {
		t.Fatalf("dialog users = %+v, want official system user", list.Users)
	}

	replayReq := req
	replayReq.Date++
	replay, err := deliveries.DeliverLoginCodeMessage(ctx, replayReq)
	if err != nil {
		t.Fatalf("replay DeliverLoginCodeMessage: %v", err)
	}
	if replay.Created || !reflect.DeepEqual(replay.Message, first.Message) {
		t.Fatalf("replay = %+v, want immutable first result %+v", replay, first)
	}
	if len(messages.m[userID]) != 1 || len(events.events[userID]) != 1 || len(messages.loginCodeDeliveries) != 1 {
		t.Fatalf("replay created facts: messages=%d events=%d receipts=%d", len(messages.m[userID]), len(events.events[userID]), len(messages.loginCodeDeliveries))
	}

	second, err := deliveries.DeliverLoginCodeMessage(ctx, domain.LoginCodeDeliveryRequest{
		UserID:        userID,
		PhoneCodeHash: "phone-code-hash-two",
		Code:          "67890",
		Date:          1700000010,
		ExpiresAt:     1700000310,
	})
	if err != nil {
		t.Fatalf("second distinct delivery: %v", err)
	}
	if !second.Created || second.Message.ID != 2 || second.Message.UID != 2 || second.Message.Pts != 2 || len(events.events[userID]) != 2 {
		t.Fatalf("second delivery = %+v events=%+v, want contiguous allocations", second, events.events[userID])
	}
	if got := dialogs.m[userID].Dialogs[0].UnreadCount; got != 2 {
		t.Fatalf("dialog unread = %d, want 2", got)
	}
}

func TestLoginCodeDeliveryStoreConcurrentReplayAndConflict(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1000000002
	messages := NewMessageStore(NewDialogStore())
	events := NewUpdateEventStore()
	deliveries := NewLoginCodeDeliveryStore(messages, events)
	req := domain.LoginCodeDeliveryRequest{
		UserID:        userID,
		PhoneCodeHash: "concurrent-phone-code-hash",
		Code:          "24680",
		Date:          1700000100,
		ExpiresAt:     1700000400,
	}

	const workers = 32
	var created atomic.Int32
	results := make(chan domain.LoginCodeDeliveryResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := deliveries.DeliverLoginCodeMessage(ctx, req)
			if err != nil {
				errs <- err
				return
			}
			if got.Created {
				created.Add(1)
			}
			results <- got
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatalf("concurrent delivery: %v", err)
	}
	if created.Load() != 1 {
		t.Fatalf("created calls = %d, want exactly 1", created.Load())
	}
	for got := range results {
		if got.Message.ID != 1 || got.Message.UID != 1 || got.Message.Pts != 1 {
			t.Fatalf("concurrent result = %+v, want the same first allocation", got)
		}
	}
	if len(messages.m[userID]) != 1 || len(events.events[userID]) != 1 || len(messages.loginCodeDeliveries) != 1 {
		t.Fatalf("concurrent facts: messages=%d events=%d receipts=%d", len(messages.m[userID]), len(events.events[userID]), len(messages.loginCodeDeliveries))
	}

	changedCode := req
	changedCode.Code = "13579"
	if _, err := deliveries.DeliverLoginCodeMessage(ctx, changedCode); !errors.Is(err, domain.ErrLoginCodeDeliveryConflict) {
		t.Fatalf("changed-code replay err = %v, want ErrLoginCodeDeliveryConflict", err)
	}
	changedUser := req
	changedUser.UserID++
	if _, err := deliveries.DeliverLoginCodeMessage(ctx, changedUser); !errors.Is(err, domain.ErrLoginCodeDeliveryConflict) {
		t.Fatalf("changed-user replay err = %v, want ErrLoginCodeDeliveryConflict", err)
	}
	if len(messages.m[userID]) != 1 || len(events.events[userID]) != 1 {
		t.Fatal("conflicting replay changed committed facts")
	}
}

func TestLoginCodeDeliveryStoreRequiresSharedEventStore(t *testing.T) {
	messages := NewMessageStore()
	_, err := NewLoginCodeDeliveryStore(messages, nil).DeliverLoginCodeMessage(context.Background(), domain.LoginCodeDeliveryRequest{
		UserID:        1000000003,
		PhoneCodeHash: "missing-event-store",
		Code:          "12345",
		Date:          1700000200,
		ExpiresAt:     1700000500,
	})
	if !errors.Is(err, domain.ErrLoginCodeDeliveryInvalid) {
		t.Fatalf("missing event store err = %v, want ErrLoginCodeDeliveryInvalid", err)
	}
}
