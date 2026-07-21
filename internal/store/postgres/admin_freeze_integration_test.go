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
		observerID   = int64(1999999883)
	)
	for _, user := range []struct {
		id    int64
		phone string
	}{{frozenUserID, "1999999881"}, {activeUserID, "1999999882"}, {observerID, "1999999883"}} {
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
		!migrated.Until.Equal(legacyUpdatedAt.Add(7*24*time.Hour)) || migrated.AppealURL != "https://t.me/SpamBot" || migrated.Version != 1 {
		t.Fatalf("migrated freeze = %+v", migrated)
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO contacts (user_id, contact_user_id, contact_first_name)
VALUES ($1, $2, 'Visible frozen peer')`, observerID, activeUserID); err != nil {
		t.Fatalf("insert observer contact: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO dialogs (user_id, peer_type, peer_id, top_message_id, top_message_date)
VALUES ($1, 'user', $2, 1, 100)`, observerID, activeUserID); err != nil {
		t.Fatalf("insert observer dialog: %v", err)
	}
	var contactVersionBefore, dialogVersionBefore int64
	if err := tx.QueryRow(ctx, `
SELECT version FROM read_model_versions
WHERE model = 'contact_account' AND owner_user_id = $1 AND peer_type = 'user' AND peer_id = $1`, observerID).Scan(&contactVersionBefore); err != nil {
		t.Fatalf("read initial contact projection version: %v", err)
	}
	if err := tx.QueryRow(ctx, `
SELECT version FROM read_model_versions
WHERE model = 'dialog_light' AND owner_user_id = $1 AND peer_type = 'user' AND peer_id = $2`, observerID, activeUserID).Scan(&dialogVersionBefore); err != nil {
		t.Fatalf("read initial dialog projection version: %v", err)
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
	updated, err := store.SetAccountFreeze(ctx, want)
	if err != nil {
		t.Fatalf("SetAccountFreeze active: %v", err)
	}
	if updated.Version != 1 {
		t.Fatalf("first freeze version = %d, want 1", updated.Version)
	}
	got, found, err := store.GetAccountFreeze(ctx, activeUserID)
	if err != nil || !found || !got.Frozen || !got.Since.Equal(want.Since) ||
		!got.Until.Equal(want.Until) || got.AppealURL != want.AppealURL || got.Version != 1 {
		t.Fatalf("active round trip = %+v found=%v err=%v", got, found, err)
	}
	var contactVersionAfter, dialogVersionAfter, visibilityVersion int64
	if err := tx.QueryRow(ctx, `
SELECT version FROM read_model_versions
WHERE model = 'contact_account' AND owner_user_id = $1 AND peer_type = 'user' AND peer_id = $1`, observerID).Scan(&contactVersionAfter); err != nil {
		t.Fatalf("read frozen contact projection version: %v", err)
	}
	if err := tx.QueryRow(ctx, `
SELECT version FROM read_model_versions
WHERE model = 'dialog_light' AND owner_user_id = $1 AND peer_type = 'user' AND peer_id = $2`, observerID, activeUserID).Scan(&dialogVersionAfter); err != nil {
		t.Fatalf("read frozen dialog projection version: %v", err)
	}
	if contactVersionAfter <= contactVersionBefore || dialogVersionAfter <= dialogVersionBefore {
		t.Fatalf("projection versions contact %d->%d dialog %d->%d, want increments",
			contactVersionBefore, contactVersionAfter, dialogVersionBefore, dialogVersionAfter)
	}
	if err := tx.QueryRow(ctx, `
SELECT version FROM read_model_versions
WHERE model = 'user_visibility' AND owner_user_id = 0 AND peer_type = 'user' AND peer_id = $1`, activeUserID).Scan(&visibilityVersion); err != nil || visibilityVersion != 1 {
		t.Fatalf("user visibility version = %d err=%v, want 1", visibilityVersion, err)
	}

	claimAt := time.Now().UTC().Add(time.Minute)
	claimed, err := store.ClaimAccountFreezeNotifications(ctx, claimAt, 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim frozen notification = %+v err=%v, want one", claimed, err)
	}
	oldNotification := claimed[0]
	if oldNotification.TargetUserID != observerID || oldNotification.FrozenUserID != activeUserID || !oldNotification.Frozen || oldNotification.Version != 1 {
		t.Fatalf("frozen notification = %+v", oldNotification)
	}

	updated, err = store.SetAccountFreeze(ctx, domain.AccountFreeze{
		UserID: activeUserID, Reason: "appeal accepted", Actor: "ops", CommandID: "unfreeze-round-trip",
	})
	if err != nil {
		t.Fatalf("SetAccountFreeze inactive: %v", err)
	}
	if updated.Version != 2 {
		t.Fatalf("unfreeze version = %d, want 2", updated.Version)
	}
	// A worker that claimed v1 before the unfreeze cannot acknowledge the
	// coalesced v2 row and suppress its online refresh.
	if err := store.CompleteAccountFreezeNotification(ctx, oldNotification.ID, oldNotification.Version, claimAt); err != nil {
		t.Fatalf("complete stale notification: %v", err)
	}
	claimed, err = store.ClaimAccountFreezeNotifications(ctx, claimAt.Add(time.Minute), 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim unfreeze notification = %+v err=%v, want one", claimed, err)
	}
	newNotification := claimed[0]
	if newNotification.ID != oldNotification.ID || newNotification.Version != 2 || newNotification.Frozen {
		t.Fatalf("coalesced unfreeze notification = %+v, previous=%+v", newNotification, oldNotification)
	}
	if err := store.CompleteAccountFreezeNotification(ctx, newNotification.ID, newNotification.Version, claimAt.Add(2*time.Minute)); err != nil {
		t.Fatalf("complete unfreeze notification: %v", err)
	}
	var notificationStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM account_freeze_notifications WHERE id = $1`, newNotification.ID).Scan(&notificationStatus); err != nil || notificationStatus != "delivered" {
		t.Fatalf("notification status = %q err=%v, want delivered", notificationStatus, err)
	}
	got, found, err = store.GetAccountFreeze(ctx, activeUserID)
	if err != nil || !found || got.Frozen || !got.Since.IsZero() || !got.Until.IsZero() || got.AppealURL != "" || got.Version != 2 {
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
