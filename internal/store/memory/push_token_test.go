package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestPushTokenStoreUpsertReassignAndDelete(t *testing.T) {
	ctx := context.Background()
	store := NewPushTokenStore()

	if err := store.UpsertPushToken(ctx, domain.PushToken{
		UserID: 1, TokenType: domain.PushTokenTypeFCM, Token: "tok-a",
		Secret: []byte{1, 2}, OtherUIDs: []int64{1, 2},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	tokens, err := store.ListPushTokensForUser(ctx, 1)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("list user 1 = %d tokens, %v", len(tokens), err)
	}
	if got := tokens[0]; got.Token != "tok-a" || got.TokenType != domain.PushTokenTypeFCM ||
		len(got.Secret) != 2 || len(got.OtherUIDs) != 2 {
		t.Fatalf("stored token = %+v", got)
	}

	// Re-registering the same token for another account reassigns ownership.
	if err := store.UpsertPushToken(ctx, domain.PushToken{
		UserID: 2, TokenType: domain.PushTokenTypeFCM, Token: "tok-a",
	}); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if tokens, _ := store.ListPushTokensForUser(ctx, 1); len(tokens) != 0 {
		t.Fatalf("user 1 after reassign = %d tokens, want 0", len(tokens))
	}
	if tokens, _ := store.ListPushTokensForUser(ctx, 2); len(tokens) != 1 {
		t.Fatalf("user 2 after reassign = %d tokens, want 1", len(tokens))
	}

	// Deleting as the previous owner must not remove another account's token.
	if deleted, err := store.DeletePushToken(ctx, 1, domain.PushTokenTypeFCM, "tok-a"); err != nil || deleted {
		t.Fatalf("delete as old owner = %v, %v; want false, nil", deleted, err)
	}
	if deleted, err := store.DeletePushToken(ctx, 2, domain.PushTokenTypeFCM, "tok-a"); err != nil || !deleted {
		t.Fatalf("delete as owner = %v, %v; want true, nil", deleted, err)
	}
	if deleted, _ := store.DeletePushToken(ctx, 2, domain.PushTokenTypeFCM, "tok-a"); deleted {
		t.Fatal("second delete = true, want idempotent false")
	}
}

func TestPushTokenStoreBatchListAndDeleteByValue(t *testing.T) {
	ctx := context.Background()
	store := NewPushTokenStore()
	_ = store.UpsertPushToken(ctx, domain.PushToken{UserID: 1, TokenType: domain.PushTokenTypeFCM, Token: "a"})
	_ = store.UpsertPushToken(ctx, domain.PushToken{UserID: 1, TokenType: domain.PushTokenTypeAPNs, Token: "b"})
	_ = store.UpsertPushToken(ctx, domain.PushToken{UserID: 2, TokenType: domain.PushTokenTypeFCM, Token: "c"})

	grouped, err := store.ListPushTokensForUsers(ctx, []int64{1, 2, 3})
	if err != nil {
		t.Fatalf("batch list: %v", err)
	}
	if len(grouped[1]) != 2 || len(grouped[2]) != 1 {
		t.Fatalf("batch list sizes = %d, %d; want 2, 1", len(grouped[1]), len(grouped[2]))
	}
	if _, ok := grouped[3]; ok {
		t.Fatal("batch list returned an entry for a user with no tokens")
	}

	if deleted, err := store.DeletePushTokenByValue(ctx, domain.PushTokenTypeFCM, "c"); err != nil || !deleted {
		t.Fatalf("delete by value = %v, %v; want true, nil", deleted, err)
	}
	if tokens, _ := store.ListPushTokensForUser(ctx, 2); len(tokens) != 0 {
		t.Fatalf("user 2 after delete by value = %d tokens, want 0", len(tokens))
	}
}

func TestPushTokenStoreClonesSlices(t *testing.T) {
	ctx := context.Background()
	store := NewPushTokenStore()
	secret := []byte{1, 2, 3}
	_ = store.UpsertPushToken(ctx, domain.PushToken{UserID: 1, TokenType: domain.PushTokenTypeFCM, Token: "a", Secret: secret})
	secret[0] = 99 // must not leak into the store

	tokens, _ := store.ListPushTokensForUser(ctx, 1)
	tokens[0].Secret[1] = 88 // must not leak back either
	again, _ := store.ListPushTokensForUser(ctx, 1)
	if again[0].Secret[0] != 1 || again[0].Secret[1] != 2 {
		t.Fatalf("stored secret mutated: %v", again[0].Secret)
	}
}
