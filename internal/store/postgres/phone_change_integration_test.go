package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestPhoneChangeStoreUpdatesUserWithoutPTSPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	changes := NewPhoneChangeStore(pool)
	events := NewUpdateEventStore(pool)
	suffix := time.Now().UnixNano() % 1000
	// NANP numbers must be 11 digits ("1650555" + 3 subscriber digits) to pass
	// domain.ValidPhone's strict E.164 check; the suffix keeps each run's set
	// unique across repeated runs against the same database.
	oldPhone := fmt.Sprintf("16505550%03d", (suffix+1)%1000)
	occupiedPhone := fmt.Sprintf("16505550%03d", (suffix+2)%1000)
	newPhone := fmt.Sprintf("16505550%03d", (suffix+3)%1000)
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
	if !result.Changed || result.User.Phone != newPhone {
		t.Fatalf("result = %+v", result)
	}
	loaded, found, err := users.ByID(ctx, u1.ID)
	if err != nil || !found || loaded.Phone != newPhone {
		t.Fatalf("loaded user = %+v found=%v err=%v", loaded, found, err)
	}
	storedEvents, err := events.ListAfter(ctx, u1.ID, 0, 10)
	if err != nil || len(storedEvents) != 0 {
		t.Fatalf("stored events = %+v err=%v", storedEvents, err)
	}
	var outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dispatch_outbox WHERE target_user_id = $1`, u1.ID).Scan(&outboxCount); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if outboxCount != 0 {
		t.Fatalf("outbox count = %d, want 0", outboxCount)
	}

	// Retrying the same number is an idempotent read and must not produce PTS/event/outbox.
	retry, err := changes.ChangePhone(ctx, domain.PhoneChangeRequest{UserID: u1.ID, Phone: newPhone, Date: 1700000002})
	if err != nil || retry.Changed || retry.User.Phone != newPhone {
		t.Fatalf("idempotent retry = %+v err=%v", retry, err)
	}
	// A phone change allocates no account PTS (updateUserPhone has no pts), and a retry must likewise
	// produce no PTS/event/outbox.
	if pts, err := events.MaxContiguousPts(ctx, u1.ID); err != nil || pts != 0 {
		t.Fatalf("pts after retry = %d err=%v", pts, err)
	}

	// A conflicting update rolls back entirely: the number is unchanged and no PTS/event is produced.
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
