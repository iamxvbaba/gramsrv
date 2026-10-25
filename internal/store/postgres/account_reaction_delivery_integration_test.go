package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestAccountReactionSettingsAndAbsoluteDeliveryCommitAtomicallyPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	user, err := users.Create(ctx, domain.User{AccessHash: 111, Phone: "+1897" + suffix + "01", FirstName: "Paid Privacy"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM edge_delivery_outbox WHERE target_user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM account_reaction_settings WHERE user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})
	passwords := NewPasswordStore(pool)
	settings := domain.DefaultAccountReactionSettings()
	settings.PaidPrivacy = domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyAnonymous}
	var beforePTS int
	if err := pool.QueryRow(ctx, "SELECT COALESCE((SELECT contiguous_pts FROM user_update_watermarks WHERE user_id = $1), 0)", user.ID).Scan(&beforePTS); err != nil {
		t.Fatalf("read pts before paid privacy: %v", err)
	}
	projectionErr := errors.New("paid privacy projection failed")
	patch := domain.AccountReactionSettingsPatch{PaidPrivacy: &settings.PaidPrivacy}
	if _, err := passwords.UpdateReactionSettings(ctx, user.ID, patch, func(domain.AccountReactionSettings) ([]store.DeliveryEffect, error) {
		return nil, projectionErr
	}); !errors.Is(err, projectionErr) {
		t.Fatalf("failed paid privacy err = %v, want projection error", err)
	}
	if _, found, err := passwords.GetReactionSettings(ctx, user.ID); err != nil || found {
		t.Fatalf("paid privacy survived failed builder: found=%v err=%v", found, err)
	}

	if _, err := passwords.UpdateReactionSettings(ctx, user.ID, patch, func(snapshot domain.AccountReactionSettings) ([]store.DeliveryEffect, error) {
		if snapshot.PaidPrivacy.Kind != domain.PaidReactionPrivacyAnonymous {
			return nil, errors.New("unexpected paid privacy snapshot")
		}
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: user.ID, Payload: []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}); err != nil {
		t.Fatalf("save paid privacy with delivery: %v", err)
	}
	stored, found, err := passwords.GetReactionSettings(ctx, user.ID)
	if err != nil || !found || stored.PaidPrivacy.Kind != domain.PaidReactionPrivacyAnonymous {
		t.Fatalf("stored paid privacy = %+v found=%v err=%v", stored, found, err)
	}
	var deliveryRows, afterPTS int
	if err := pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id = $1),
  COALESCE((SELECT contiguous_pts FROM user_update_watermarks WHERE user_id = $1), 0)`, user.ID).Scan(&deliveryRows, &afterPTS); err != nil {
		t.Fatalf("read paid privacy durable facts: %v", err)
	}
	if deliveryRows != 1 {
		t.Fatalf("paid privacy delivery rows = %d, want 1", deliveryRows)
	}
	if afterPTS != beforePTS {
		t.Fatalf("paid privacy changed account pts %d -> %d", beforePTS, afterPTS)
	}
}
