package postgres

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/redisstore"
)

func TestMessageStoreRedisBoxAllocatorColdCountersDoesNotPoolDeadlock(t *testing.T) {
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	redisAddr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if dsn == "" || redisAddr == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN and TELESRV_TEST_REDIS_ADDR to run postgres/redis integration test")
	}
	if err := Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	baseCtx := context.Background()
	pool, err := Open(baseCtx, dsn, WithMaxConns(2))
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	rdb, err := redisstore.Open(baseCtx, redisAddr, os.Getenv("TELESRV_TEST_REDIS_PASSWORD"), 0)
	if err != nil {
		t.Fatalf("open redis: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	a, err := users.Create(baseCtx, domain.User{AccessHash: 81, Phone: "+1998" + suffix + "01", FirstName: "ColdCounterA"})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := users.Create(baseCtx, domain.User{AccessHash: 82, Phone: "+1998" + suffix + "02", FirstName: "ColdCounterB"})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	ids := []int64{a.ID, b.ID}
	t.Cleanup(func() {
		_, _ = rdb.Del(baseCtx, boxIDCounterKeyForTest(a.ID), boxIDCounterKeyForTest(b.ID)).Result()
		_, _ = pool.Exec(baseCtx, "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM user_update_watermarks WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM message_boxes WHERE owner_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM private_messages WHERE sender_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(baseCtx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})
	if _, err := rdb.Del(baseCtx, boxIDCounterKeyForTest(a.ID), boxIDCounterKeyForTest(b.ID)).Result(); err != nil {
		t.Fatalf("delete redis counters: %v", err)
	}

	boxIDs := redisstore.NewBoxIDAllocator(rdb, NewMessageBoxCounterSource(pool))
	messages := NewMessageStore(pool, WithMessageAllocators(boxIDs))
	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Second)
	defer cancel()

	start := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	send := func(senderID, recipientID int64, randomID int64) {
		defer wg.Done()
		<-start
		_, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    senderID,
			RecipientUserID: recipientID,
			RandomID:        randomID,
			Message:         "cold redis box counter",
			Date:            int(time.Now().Unix()),
		})
		errCh <- err
	}

	wg.Add(2)
	go send(a.ID, b.ID, time.Now().UnixNano())
	go send(b.ID, a.ID, time.Now().UnixNano()+1)
	close(start)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("concurrent sends with cold Redis counters timed out: %v", ctx.Err())
	}
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("SendPrivateText: %v", err)
		}
	}
}

func boxIDCounterKeyForTest(userID int64) string {
	return fmt.Sprintf("counter:box_id:{%d}", userID)
}
