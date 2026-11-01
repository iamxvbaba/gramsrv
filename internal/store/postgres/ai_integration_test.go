package postgres

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"

	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
)

func TestAIComposeDeliveryFailureRollsBackMutation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	user, err := users.Create(ctx, domain.User{AccessHash: randomAIComposeID(), Phone: "+1772" + randomSuffix(t), FirstName: "AIRollback"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID) })
	tone := domain.AIComposeTone{
		ID: randomAIComposeID(), AccessHash: randomAIComposeID(), OwnerUserID: user.ID,
		Slug: "ai-rollback-" + randomSuffix(t), Title: "Rollback", Prompt: "Rollback me",
	}
	wantErr := errors.New("encode failed")
	err = NewAIComposeStore(pool).CreateAIComposeTone(ctx, tone, func(domain.AIComposeTone) ([]storepkg.DeliveryEffect, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("create error = %v, want %v", err, wantErr)
	}
	if _, found, err := NewAIComposeStore(pool).GetAIComposeToneByID(ctx, tone.ID, tone.AccessHash); err != nil || found {
		t.Fatalf("tone after delivery failure = found:%v err:%v, want rolled back", found, err)
	}
	var outboxRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id = $1`, user.ID).Scan(&outboxRows); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxRows != 0 {
		t.Fatalf("outbox rows = %d, want 0", outboxRows)
	}
}

// TestAIComposeStoreRoundTripPostgres verifies persistence of custom AI tones and the saved list,
// including creator/saved perspectives, slug/id resolution and delete cascade.
func TestAIComposeStoreRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewAIComposeStore(pool)
	users := NewUserStore(pool)
	suffix := randomSuffix(t)

	owner, err := users.Create(ctx, domain.User{AccessHash: randomAIComposeID(), Phone: "+1771" + suffix + "01", FirstName: "AIOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := users.Create(ctx, domain.User{AccessHash: randomAIComposeID(), Phone: "+1771" + suffix + "02", FirstName: "AISaver"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1)", []int64{owner.ID, other.ID})
	})

	tone := domain.AIComposeTone{
		ID:            randomAIComposeID(),
		AccessHash:    randomAIComposeID(),
		OwnerUserID:   owner.ID,
		Slug:          "ai-pg-" + suffix,
		Title:         "Sharp",
		Prompt:        "Make it direct and crisp.",
		DisplayAuthor: true,
		CreatedAt:     1700000000,
		UpdatedAt:     1700000000,
	}
	if err := store.CreateAIComposeTone(ctx, tone, aiComposeIntegrationEffects(owner.ID)); err != nil {
		t.Fatalf("create tone: %v", err)
	}
	if got, ok, err := store.GetAIComposeToneByID(ctx, tone.ID, tone.AccessHash); err != nil || !ok || got.Slug != tone.Slug || got.AuthorID != owner.ID {
		t.Fatalf("get by id = ok %v tone %#v err %v", ok, got, err)
	}
	if err := store.SaveAIComposeTone(ctx, other.ID, tone.ID, aiComposeIntegrationEffects(other.ID)); err != nil {
		t.Fatalf("save tone: %v", err)
	}
	list, err := store.ListAIComposeTonesForUser(ctx, other.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list saved = %d err %v, want 1", len(list), err)
	}
	if list[0].ID != tone.ID || !list[0].Saved || list[0].Creator {
		t.Fatalf("saved view = %#v, want saved non-creator", list[0])
	}
	if got, ok, err := store.GetAIComposeToneBySlug(ctx, tone.Slug); err != nil || !ok || got.InstallsCount != 1 {
		t.Fatalf("get by slug installs = ok %v tone %#v err %v, want installs=1", ok, got, err)
	}
	if count, err := store.SavedAIComposeToneCount(ctx, other.ID); err != nil || count != 1 {
		t.Fatalf("saved count = %d err %v, want 1", count, err)
	}
	if err := store.DeleteAIComposeTone(ctx, owner.ID, tone.ID, aiComposeIntegrationEffects(owner.ID)); err != nil {
		t.Fatalf("delete tone: %v", err)
	}
	if _, ok, err := store.GetAIComposeToneBySlug(ctx, tone.Slug); err != nil || ok {
		t.Fatalf("get after delete = ok %v err %v, want missing", ok, err)
	}
	if list, err := store.ListAIComposeTonesForUser(ctx, other.ID); err != nil || len(list) != 0 {
		t.Fatalf("list after delete = %d err %v, want empty", len(list), err)
	}
}

func aiComposeIntegrationEffects(userID int64) storepkg.DeliveryEffectsBuilder[domain.AIComposeTone] {
	return func(domain.AIComposeTone) ([]storepkg.DeliveryEffect, error) {
		return []storepkg.DeliveryEffect{storepkg.AbsoluteDeliveryEffect(storepkg.DeliveryOutboxEnqueue{
			TargetUserID: userID, Payload: []byte{1}, RecoveryPolicy: storepkg.OutboxRecoveryAbsoluteReload,
		})}, nil
	}
}

func randomAIComposeID() int64 {
	for {
		var b [8]byte
		_, _ = rand.Read(b[:])
		v := int64(binary.BigEndian.Uint64(b[:]) & 0x7fffffffffffffff)
		if v != 0 {
			return v
		}
	}
}
