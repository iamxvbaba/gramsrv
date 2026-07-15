package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/deploy"
	"telesrv/internal/domain"
)

func TestAccountFreezeMigrationAndStoreRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	downSQL, err := deploy.Migrations.ReadFile("migrations/0088_account_freeze_state.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL, err := deploy.Migrations.ReadFile("migrations/0088_account_freeze_state.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("roll schema back to legacy restriction shape: %v", err)
	}

	const (
		frozenUserID = int64(1999999881)
		activeUserID = int64(1999999882)
	)
	for _, user := range []struct {
		id    int64
		phone string
	}{{frozenUserID, "1999999881"}, {activeUserID, "1999999882"}} {
		if _, err := tx.Exec(ctx, `
INSERT INTO users (id, access_hash, phone, first_name)
VALUES ($1, $1, $2, 'Freeze migration test')`, user.id, user.phone); err != nil {
			t.Fatalf("insert migration user %d: %v", user.id, err)
		}
	}
	legacyUpdatedAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if _, err := tx.Exec(ctx, `
INSERT INTO account_send_restrictions (user_id, frozen, reason, actor, command_id, updated_at)
VALUES ($1, true, 'legacy freeze', 'ops', 'legacy-freeze', $2)`, frozenUserID, legacyUpdatedAt); err != nil {
		t.Fatalf("insert legacy restriction: %v", err)
	}
	if _, err := tx.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("apply account freeze migration: %v", err)
	}

	store := NewAdminStore(tx)
	migrated, found, err := store.GetAccountFreeze(ctx, frozenUserID)
	if err != nil || !found {
		t.Fatalf("GetAccountFreeze migrated = %+v found=%v err=%v", migrated, found, err)
	}
	if !migrated.Frozen || !migrated.Since.Equal(legacyUpdatedAt) ||
		!migrated.Until.Equal(legacyUpdatedAt.Add(7*24*time.Hour)) || migrated.AppealURL != "https://t.me/SpamBot" {
		t.Fatalf("migrated freeze = %+v", migrated)
	}

	since := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	want := domain.AccountFreeze{
		UserID:    activeUserID,
		Frozen:    true,
		Since:     since,
		Until:     since.Add(48 * time.Hour),
		AppealURL: "https://appeals.example.test/users/1999999882",
		Reason:    "abuse review",
		Actor:     "ops",
		CommandID: "freeze-round-trip",
	}
	if _, err := store.SetAccountFreeze(ctx, want); err != nil {
		t.Fatalf("SetAccountFreeze active: %v", err)
	}
	got, found, err := store.GetAccountFreeze(ctx, activeUserID)
	if err != nil || !found || !got.Frozen || !got.Since.Equal(want.Since) ||
		!got.Until.Equal(want.Until) || got.AppealURL != want.AppealURL {
		t.Fatalf("active round trip = %+v found=%v err=%v", got, found, err)
	}
	if _, err := store.SetAccountFreeze(ctx, domain.AccountFreeze{
		UserID: activeUserID, Reason: "appeal accepted", Actor: "ops", CommandID: "unfreeze-round-trip",
	}); err != nil {
		t.Fatalf("SetAccountFreeze inactive: %v", err)
	}
	got, found, err = store.GetAccountFreeze(ctx, activeUserID)
	if err != nil || !found || got.Frozen || !got.Since.IsZero() || !got.Until.IsZero() || got.AppealURL != "" {
		t.Fatalf("inactive round trip = %+v found=%v err=%v", got, found, err)
	}

	if _, err := tx.Exec(ctx, "SAVEPOINT invalid_freeze"); err != nil {
		t.Fatal(err)
	}
	_, invalidErr := tx.Exec(ctx, `
UPDATE account_restrictions
SET frozen = true, frozen_since = NULL, frozen_until = NULL, appeal_url = ''
WHERE user_id = $1`, activeUserID)
	if invalidErr == nil {
		t.Fatal("database accepted an active freeze without client-visible state")
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT invalid_freeze"); err != nil {
		t.Fatalf("rollback invalid freeze savepoint: %v", err)
	}
}
