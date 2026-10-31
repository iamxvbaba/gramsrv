package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestMessageStoreBidirectionalConcurrencyNoDeadlock verifies the watermark/dialog deadlock fix (advisory lock).
//
// Background: send/read/edit/delete within one transaction lock the sender's and recipient's
// user_update_watermarks and channel/private dialog rows in business order. During concurrent
// reverse A→B and B→A, the two transactions contend for these rows of the same pair of users in
// opposite order (watermark[A]→watermark[B] vs watermark[B]→watermark[A], dialog(A,B)→dialog(B,A)
// vs reverse), forming an AB-BA deadlock — PostgreSQL detects and aborts one of them (SQLSTATE
// 40P01), surfacing as an operation error.
//
// Fix: every write transaction, before any row lock, takes a transaction-level advisory lock on
// the users involved in ascending user_id order (lockUsersForUpdate), serializing concurrent write
// transactions for the same pair of users. The advisory and row locks live in separate lock spaces
// and are acquired in ascending order, so they neither cross with row locks into a new deadlock nor
// leave the watermark/dialog AB-BA. Under a high-concurrency reverse load this test should succeed
// entirely with zero errors; if the deadlock regresses it will be caught as a 40P01 error.
func TestMessageStoreBidirectionalConcurrencyNoDeadlock(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	a, err := users.Create(ctx, domain.User{AccessHash: 71, Phone: "+1997" + suffix + "01", FirstName: "BidiA"})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := users.Create(ctx, domain.User{AccessHash: 72, Phone: "+1997" + suffix + "02", FirstName: "BidiB"})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	ids := []int64{a.ID, b.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_watermarks WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM message_boxes WHERE owner_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM private_messages WHERE sender_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})

	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	date := int(time.Now().Unix())

	var ridMu sync.Mutex
	rid := time.Now().UnixNano()
	nextRID := func() int64 {
		ridMu.Lock()
		defer ridMu.Unlock()
		rid++
		return rid
	}

	// Warm-up: send several messages in each direction, establishing bidirectional dialogs and
	// unread history so later reads actually advance the watermark (hitting the row lock).
	for i := 0; i < 4; i++ {
		if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: a.ID, RecipientUserID: b.ID, RandomID: nextRID(), Message: "warmup a->b", Date: date}); err != nil {
			t.Fatalf("warmup a->b: %v", err)
		}
		if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: b.ID, RecipientUserID: a.ID, RandomID: nextRID(), Message: "warmup b->a", Date: date}); err != nil {
			t.Fatalf("warmup b->a: %v", err)
		}
	}

	// Reverse concurrency: each round simultaneously issues A→B send, B→A send, A reads B, B reads A, with the goroutines contending together for the same pair of users' row locks.
	const rounds = 80
	var wg sync.WaitGroup
	errCh := make(chan error, rounds*4)
	sem := make(chan struct{}, 24)

	submit := func(op func() error) {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if e := op(); e != nil {
				errCh <- e
			}
		}()
	}

	for r := 0; r < rounds; r++ {
		submit(func() error {
			_, e := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: a.ID, RecipientUserID: b.ID, RandomID: nextRID(), Message: "a->b", Date: date})
			return e
		})
		submit(func() error {
			_, e := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: b.ID, RecipientUserID: a.ID, RandomID: nextRID(), Message: "b->a", Date: date})
			return e
		})
		submit(func() error {
			_, e := messages.ReadHistory(ctx, domain.ReadHistoryRequest{OwnerUserID: a.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: b.ID}, MaxID: domain.MaxMessageBoxID, Date: date})
			return e
		})
		submit(func() error {
			_, e := messages.ReadHistory(ctx, domain.ReadHistoryRequest{OwnerUserID: b.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: a.ID}, MaxID: domain.MaxMessageBoxID, Date: date})
			return e
		})
	}
	wg.Wait()
	close(errCh)

	failed := 0
	for e := range errCh {
		failed++
		if failed <= 5 {
			t.Errorf("reverse concurrent operation failed (suspected deadlock regression): %v", e)
		}
	}
	if failed > 0 {
		t.Fatalf("%d/%d reverse concurrent operations failed", failed, rounds*4)
	}
}

func TestMessageStoreBidirectionalRevokeDeleteNoDeadlock(t *testing.T) {
	pool := testPool(t)
	baseCtx := context.Background()
	ctx, cancel := context.WithTimeout(baseCtx, 20*time.Second)
	defer cancel()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	a, err := users.Create(baseCtx, domain.User{AccessHash: 73, Phone: "+1997" + suffix + "11", FirstName: "DeleteA"})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := users.Create(baseCtx, domain.User{AccessHash: 74, Phone: "+1997" + suffix + "12", FirstName: "DeleteB"})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	ids := []int64{a.ID, b.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(baseCtx, "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM user_update_watermarks WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM message_boxes WHERE owner_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM private_messages WHERE sender_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})

	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	date := int(time.Now().Unix())
	type pair struct {
		senderBoxID    int
		recipientBoxID int
	}
	const count = 60
	pairs := make([]pair, 0, count)
	for i := 0; i < count; i++ {
		sent, err := messages.SendPrivateText(baseCtx, domain.SendPrivateTextRequest{
			SenderUserID:    a.ID,
			RecipientUserID: b.ID,
			RandomID:        time.Now().UnixNano() + int64(i),
			Message:         "revoke-delete",
			Date:            date,
		})
		if err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
		pairs = append(pairs, pair{senderBoxID: sent.SenderMessage.ID, recipientBoxID: sent.RecipientMessage.ID})
	}

	var wg sync.WaitGroup
	errCh := make(chan error, count*2)
	sem := make(chan struct{}, 24)
	submit := func(op func() error) {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if e := op(); e != nil {
				errCh <- e
			}
		}()
	}

	for _, p := range pairs {
		p := p
		submit(func() error {
			_, e := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
				OwnerUserID: a.ID,
				IDs:         []int{p.senderBoxID},
				Revoke:      true,
				Date:        date,
			})
			return e
		})
		submit(func() error {
			_, e := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
				OwnerUserID: b.ID,
				IDs:         []int{p.recipientBoxID},
				Revoke:      true,
				Date:        date,
			})
			return e
		})
	}
	wg.Wait()
	close(errCh)

	failed := 0
	for e := range errCh {
		failed++
		if failed <= 5 {
			t.Errorf("two-sided revoke delete failed (suspected deadlock regression): %v", e)
		}
	}
	if failed > 0 {
		t.Fatalf("%d/%d two-sided revoke delete operations failed", failed, count*2)
	}
}
