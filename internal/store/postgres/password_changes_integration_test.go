package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestPasswordChangePersistenceAndConcurrentFactorsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	user := createTestUser(t, ctx, users, fmt.Sprintf("15573%d", time.Now().UnixNano()), "Password", "Fixture")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, user.ID) })
	store := NewPasswordStore(pool)
	until := int(time.Now().Add(7 * 24 * time.Hour).Unix())
	pending := &domain.PendingPasswordChange{DeliveryID: "fixture-delivery", Email: "owner@example.test", BaseBinding: "factor", CodeHash: []byte{1, 2, 3}, ExpiresAt: time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute), MaxAttempts: 3, CodeLength: 6, Input: domain.PasswordInputSettings{HasEmail: true, Email: "owner@example.test"}}
	if err := store.Mutate(ctx, user.ID, func(s *domain.PasswordSettings) error {
		s.PendingResetDate = until
		s.PendingChange = pending
		s.EmailUnconfirmedPattern = "o***@example.test"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// New instances exercise durable restart semantics, including the entire
	// confirmation proof and reset date rather than a process-local cache.
	loaded, found, err := NewPasswordStore(pool).GetByUser(ctx, user.ID)
	if err != nil || !found || loaded.PendingResetDate != until || loaded.PendingChange == nil || !loaded.PendingChange.ExpiresAt.Equal(pending.ExpiresAt) || loaded.PendingChange.DeliveryID != pending.DeliveryID {
		t.Fatalf("pending state did not round trip: found=%v err=%v", found, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- NewPasswordStore(pool).Mutate(ctx, user.ID, func(s *domain.PasswordSettings) error {
				if i == 0 {
					s.LoginEmail = "independent@example.test"
				} else {
					s.PendingChange.Attempts++
				}
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, _, err = NewPasswordStore(pool).GetByUser(ctx, user.ID)
	if err != nil || loaded.LoginEmail != "independent@example.test" || loaded.PendingChange.Attempts != 7 || loaded.PendingResetDate != until {
		t.Fatal("concurrent factor mutation lost state", err)
	}
	if _, err := NewAccountLifecycleStore(pool, testAllocatorsFor(pool).boxIDs).ExecuteAccountDeletion(ctx, user.ID, domain.AccountDeletionManual, "fixture", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate(ctx, user.ID, func(s *domain.PasswordSettings) error { s.LoginEmail = "resurrected@example.test"; return nil }); err == nil {
		t.Fatal("deleted account accepted a factor write")
	}
}
