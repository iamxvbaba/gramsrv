package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestPhoneChangeStoreAtomicUserEventOutboxPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	changes := NewPhoneChangeStore(pool)
	events := NewUpdateEventStore(pool)
	suffix := time.Now().UnixNano() % 1_000_000_000
	oldPhone := fmt.Sprintf("1661%d01", suffix)
	occupiedPhone := fmt.Sprintf("1661%d02", suffix)
	newPhone := fmt.Sprintf("1661%d03", suffix)
	u1, err := users.Create(ctx, domain.User{AccessHash: 301, Phone: oldPhone, FirstName: "PhoneOne"})
	if err != nil {
		t.Fatalf("create user1: %v", err)
	}
	u2, err := users.Create(ctx, domain.User{AccessHash: 302, Phone: occupiedPhone, FirstName: "PhoneTwo"})
	if err != nil {
		t.Fatalf("create user2: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
		_, _ = pool.Exec(context.Background(), "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
		_, _ = pool.Exec(context.Background(), "DELETE FROM user_update_watermarks WHERE user_id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
	})

	authKeyID := [8]byte{7, 6, 5, 4}
	result, err := changes.ChangePhone(ctx, domain.PhoneChangeRequest{
		UserID: u1.ID, Phone: newPhone, Date: 1700000001,
		ExcludeAuthKeyID: authKeyID, ExcludeSessionID: 77,
	})
	if err != nil {
		t.Fatalf("change phone: %v", err)
	}
	if !result.Changed || result.User.Phone != newPhone || result.Event.Pts != 1 || result.Event.Phone != newPhone {
		t.Fatalf("result = %+v", result)
	}
	loaded, found, err := users.ByID(ctx, u1.ID)
	if err != nil || !found || loaded.Phone != newPhone {
		t.Fatalf("loaded user = %+v found=%v err=%v", loaded, found, err)
	}
	storedEvents, err := events.ListAfter(ctx, u1.ID, 0, 10)
	if err != nil || len(storedEvents) != 1 || storedEvents[0].Type != domain.UpdateEventUserPhone || storedEvents[0].Phone != newPhone {
		t.Fatalf("stored events = %+v err=%v", storedEvents, err)
	}
	var outboxCount int
	var excludedAuth, excludedSession int64
	if err := pool.QueryRow(ctx, `SELECT count(*), max(exclude_auth_key_id), max(exclude_session_id) FROM dispatch_outbox WHERE target_user_id = $1 AND pts = $2`, u1.ID, result.Event.Pts).Scan(&outboxCount, &excludedAuth, &excludedSession); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if outboxCount != 1 || excludedAuth != authKeyIDToInt64(authKeyID) || excludedSession != 77 {
		t.Fatalf("outbox count/auth/session = %d/%d/%d", outboxCount, excludedAuth, excludedSession)
	}

	// 同号重试是幂等读，不得重复推进 pts 或重复入 outbox。
	retry, err := changes.ChangePhone(ctx, domain.PhoneChangeRequest{UserID: u1.ID, Phone: newPhone, Date: 1700000002})
	if err != nil || retry.Changed || retry.Event.Pts != 0 || retry.User.Phone != newPhone {
		t.Fatalf("idempotent retry = %+v err=%v", retry, err)
	}
	if pts, err := events.MaxContiguousPts(ctx, u1.ID); err != nil || pts != 1 {
		t.Fatalf("pts after retry = %d err=%v", pts, err)
	}

	// 冲突更新整体回滚：号码和 pts/event 都不变。
	if _, err := changes.ChangePhone(ctx, domain.PhoneChangeRequest{UserID: u2.ID, Phone: newPhone}); !errors.Is(err, domain.ErrPhoneNumberOccupied) {
		t.Fatalf("occupied change err = %v", err)
	}
	loaded2, found, err := users.ByID(ctx, u2.ID)
	if err != nil || !found || loaded2.Phone != occupiedPhone {
		t.Fatalf("occupied rollback user = %+v found=%v err=%v", loaded2, found, err)
	}
	if pts, err := events.MaxContiguousPts(ctx, u2.ID); err != nil || pts != 0 {
		t.Fatalf("occupied rollback pts = %d err=%v", pts, err)
	}
}
