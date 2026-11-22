package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestPushTokenStoreIntegration verifies the device_push_tokens store against
// live Postgres: upsert round-trips, a token is globally unique per
// (token_type, token), re-registration reassigns ownership, deletes are
// owner-scoped, and nil OtherUIDs persist as an empty array. Requires
// TELESRV_TEST_POSTGRES_DSN.
func TestPushTokenStoreIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPushTokenStore(pool)

	fcm := "fcm-" + randomSuffix(t)
	apns := "apns-" + randomSuffix(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM device_push_tokens WHERE token = ANY($1::text[])", []string{fcm, apns})
	})

	if err := store.UpsertPushToken(ctx, domain.PushToken{
		UserID: 1001, TokenType: domain.PushTokenTypeFCM, Token: fcm,
		Secret: []byte{1, 2, 3}, AppSandbox: true, NoMuted: true, OtherUIDs: []int64{1001, 1002},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	tokens, err := store.ListPushTokensForUser(ctx, 1001)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("list user 1001 = %d, %v", len(tokens), err)
	}
	if got := tokens[0]; got.Token != fcm || got.TokenType != domain.PushTokenTypeFCM ||
		!got.AppSandbox || !got.NoMuted || string(got.Secret) != "\x01\x02\x03" || len(got.OtherUIDs) != 2 ||
		got.UpdatedAt.IsZero() {
		t.Fatalf("stored token = %+v", got)
	}

	// Re-registering the same token for another account reassigns ownership, and
	// a nil OtherUIDs must persist as an empty array (not NULL).
	if err := store.UpsertPushToken(ctx, domain.PushToken{
		UserID: 2002, TokenType: domain.PushTokenTypeFCM, Token: fcm,
	}); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if tokens, _ := store.ListPushTokensForUser(ctx, 1001); len(tokens) != 0 {
		t.Fatalf("user 1001 after reassign = %d, want 0", len(tokens))
	}
	tokens, _ = store.ListPushTokensForUser(ctx, 2002)
	if len(tokens) != 1 || tokens[0].OtherUIDs == nil || len(tokens[0].OtherUIDs) != 0 {
		t.Fatalf("reassigned token = %+v", tokens)
	}

	// A second token for batch listing.
	if err := store.UpsertPushToken(ctx, domain.PushToken{
		UserID: 2002, TokenType: domain.PushTokenTypeAPNs, Token: apns,
	}); err != nil {
		t.Fatalf("upsert apns: %v", err)
	}
	grouped, err := store.ListPushTokensForUsers(ctx, []int64{2002, 3003})
	if err != nil {
		t.Fatalf("batch list: %v", err)
	}
	if len(grouped[2002]) != 2 {
		t.Fatalf("batch list user 2002 = %d, want 2", len(grouped[2002]))
	}
	if _, ok := grouped[3003]; ok {
		t.Fatal("batch list included a user with no tokens")
	}

	// Deletes are owner-scoped; repeat deletes are idempotent.
	if deleted, err := store.DeletePushToken(ctx, 1001, domain.PushTokenTypeFCM, fcm); err != nil || deleted {
		t.Fatalf("delete as wrong owner = %v, %v; want false, nil", deleted, err)
	}
	if deleted, err := store.DeletePushToken(ctx, 2002, domain.PushTokenTypeFCM, fcm); err != nil || !deleted {
		t.Fatalf("delete as owner = %v, %v; want true, nil", deleted, err)
	}
	if deleted, _ := store.DeletePushToken(ctx, 2002, domain.PushTokenTypeFCM, fcm); deleted {
		t.Fatal("second delete = true, want false")
	}
	if deleted, err := store.DeletePushTokenByValue(ctx, domain.PushTokenTypeAPNs, apns); err != nil || !deleted {
		t.Fatalf("delete by value = %v, %v; want true, nil", deleted, err)
	}
}
