package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestUpdateEmojiStatusWithEventIsAtomic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	u, err := users.Create(ctx, domain.User{
		AccessHash: time.Now().UnixNano(),
		Phone:      fmt.Sprintf("1666%d", time.Now().UnixNano()),
		FirstName:  "Emoji status event",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, u.ID) })

	status := domain.UserEmojiStatus{DocumentID: 42}
	event := domain.UpdateEvent{
		Type:        domain.UpdateEventUserEmojiStatus,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: u.ID},
		EmojiStatus: status,
		Date:        int(time.Now().Unix()),
		PtsCount:    1,
	}
	// A nonzero session without its auth-key half violates the outbox
	// exclusion-pair invariant. The event failure must roll back the users row.
	if _, _, err := users.UpdateEmojiStatusWithEvent(ctx, u.ID, status, event, [8]byte{}, 77); err == nil {
		t.Fatal("UpdateEmojiStatusWithEvent unexpectedly accepted a partial exclusion pair")
	}
	got, found, err := users.ByID(ctx, u.ID)
	if err != nil || !found || !got.EmojiStatus().Empty() {
		t.Fatalf("failed aggregate write leaked user state: user=%+v found=%v err=%v", got, found, err)
	}

	authKeyID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	got, storedEvent, err := users.UpdateEmojiStatusWithEvent(ctx, u.ID, status, event, authKeyID, 77)
	if err != nil {
		t.Fatalf("UpdateEmojiStatusWithEvent: %v", err)
	}
	if got.EmojiStatus() != status || storedEvent.Pts <= 0 || storedEvent.EmojiStatus != status {
		t.Fatalf("aggregate result: user=%+v event=%+v", got.EmojiStatus(), storedEvent)
	}
	loaded, err := NewUpdateEventStore(pool).ListAfter(ctx, u.ID, storedEvent.Pts-1, 1)
	if err != nil || len(loaded) != 1 || loaded[0].EmojiStatus != status {
		t.Fatalf("durable event: events=%+v err=%v", loaded, err)
	}
	var outboxCount int
	if err := pool.QueryRow(ctx, `
SELECT COUNT(*) FROM dispatch_outbox
WHERE target_user_id=$1 AND pts=$2 AND event_type='user_emoji_status'`, u.ID, storedEvent.Pts).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("dispatch outbox count=%d err=%v", outboxCount, err)
	}
}
