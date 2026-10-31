package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestSendPrivateTextConcurrentNoPtsGap is the core correctness proof for "allocate pts inside a
// PG transaction": with N messages sent concurrently sender→recipient, after all commit
// successfully the recipient's account event pts must be strictly contiguous 1..N (no gaps, no
// duplicates, no loss), and MaxContiguousPts == N. box id may still come from an external counter,
// while pts is advanced by user_update_watermarks within the same PG tx.
func TestSendPrivateTextConcurrentNoPtsGap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 51,
		Phone:      "+1999" + suffix + "01",
		FirstName:  "ConcSender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 52,
		Phone:      "+1999" + suffix + "02",
		FirstName:  "ConcRecipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	ids := []int64{sender.ID, recipient.ID}
	t.Cleanup(func() {
		// Clean up child tables in FK dependency order (message_boxes.from_user_id is RESTRICT) before deleting users.
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM message_boxes WHERE owner_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM private_messages WHERE sender_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})
	var initialWatermarks int
	if err := pool.QueryRow(ctx, `
SELECT count(*)::int
FROM user_update_watermarks
WHERE user_id = ANY($1::bigint[])`, ids).Scan(&initialWatermarks); err != nil {
		t.Fatalf("count initial watermarks: %v", err)
	}
	if initialWatermarks != 0 {
		t.Fatalf("initial watermarks = %d, want 0 so concurrency covers first upsert", initialWatermarks)
	}

	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))

	const n = 200
	base := time.Now().UnixNano()
	date := int(time.Now().Unix())
	errs := make([]error, n)
	recipPts := make([]int, n)
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
				SenderUserID:    sender.ID,
				RecipientUserID: recipient.ID,
				RandomID:        base + int64(i),
				Message:         "concurrent body",
				Date:            date,
			})
			errs[i] = err
			recipPts[i] = res.RecipientMessage.Pts
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	// In-process check for whether the allocator assigned duplicate pts to the recipient.
	seen := map[int]int{}
	for i := 0; i < n; i++ {
		if prev, ok := seen[recipPts[i]]; ok {
			t.Errorf("recipient pts %d assigned by both send #%d and #%d (allocator concurrent duplicate)", recipPts[i], prev, i)
		}
		seen[recipPts[i]] = i
	}

	// The recipient should have n events with pts contiguous 1..n (no gaps, no duplicates, no loss).
	events := NewUpdateEventStore(pool)
	got, err := events.ListAfter(ctx, recipient.ID, 0, n+10)
	if err != nil {
		t.Fatalf("list recipient events: %v", err)
	}
	if len(got) != n {
		t.Fatalf("recipient events = %d, want %d (no loss, no duplicates)", len(got), n)
	}
	for i, ev := range got {
		if ev.Pts != i+1 {
			t.Fatalf("recipient event[%d].Pts = %d, want %d (pts must be contiguous with no gaps)", i, ev.Pts, i+1)
		}
	}
	contig, err := events.MaxContiguousPts(ctx, recipient.ID)
	if err != nil {
		t.Fatalf("MaxContiguousPts: %v", err)
	}
	if contig != n {
		t.Fatalf("MaxContiguousPts(recipient) = %d, want %d", contig, n)
	}
}
