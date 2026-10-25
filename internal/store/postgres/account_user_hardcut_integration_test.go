package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func postgresAbsoluteOwnerEffect(targetUserID int64) []store.DeliveryEffect {
	return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
		TargetUserID: targetUserID,
		Payload:      []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	})}
}

func TestAccountUserReliableMutationsAreAtomicPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner := createTestUser(t, ctx, users, "+1911"+suffix+"01", "Owner", "")
	friend := createTestUser(t, ctx, users, "+1911"+suffix+"02", "Friend", "")
	photoID := time.Now().UnixNano() & 0x3fffffffffffffff
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM edge_delivery_outbox WHERE target_user_id=$1`, owner.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM notify_settings WHERE owner_user_id=$1`, owner.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM account_reaction_settings WHERE user_id=$1`, owner.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM contacts WHERE user_id=$1`, owner.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM photos WHERE id=$1`, photoID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::bigint[])`, []int64{owner.ID, friend.ID})
	})
	wantErr := errors.New("encode failed")

	if _, err := users.UpdatePersonalChannelWithDelivery(ctx, owner.ID, 77, func(store.UserDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("personal channel builder error = %v", err)
	}
	current, _, _ := users.ByID(ctx, owner.ID)
	if current.PersonalChannelID != 0 {
		t.Fatalf("failed personal channel leaked state: %+v", current)
	}
	if _, err := users.UpdatePersonalChannelWithDelivery(ctx, owner.ID, 77, func(snapshot store.UserDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return postgresAbsoluteOwnerEffect(snapshot.User.ID), nil
	}); err != nil {
		t.Fatalf("personal channel aggregate: %v", err)
	}
	current, _, _ = users.ByID(ctx, owner.ID)
	if current.PersonalChannelID != 77 {
		t.Fatalf("personal channel = %d, want 77", current.PersonalChannelID)
	}

	passwords := NewPasswordStore(pool)
	scope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: friend.ID}}
	mute := 100
	if err := passwords.SaveNotifySettings(ctx, owner.ID, scope, domain.PeerNotifySettings{MuteUntil: &mute}); err != nil {
		t.Fatal(err)
	}
	if err := passwords.ResetNotifySettingsWithDelivery(ctx, owner.ID, func(store.NotifySettingsResetSnapshot) ([]store.DeliveryEffect, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("reset builder error = %v", err)
	}
	if _, found, _ := passwords.GetNotifySettings(ctx, owner.ID, scope); !found {
		t.Fatal("failed reset deleted notify setting")
	}
	if err := passwords.ResetNotifySettingsWithDelivery(ctx, owner.ID, func(snapshot store.NotifySettingsResetSnapshot) ([]store.DeliveryEffect, error) {
		return postgresAbsoluteOwnerEffect(snapshot.OwnerUserID), nil
	}); err != nil {
		t.Fatalf("reset aggregate: %v", err)
	}
	if _, found, _ := passwords.GetNotifySettings(ctx, owner.ID, scope); found {
		t.Fatal("notify setting survived successful reset")
	}

	settings := domain.DefaultAccountReactionSettings()
	settings.Notify.ShowPreviews = false
	if _, err := passwords.UpdateReactionSettings(ctx, owner.ID, domain.AccountReactionSettingsPatch{Notify: &settings.Notify}, nil); err != nil {
		t.Fatalf("save reaction notify: %v", err)
	}
	if got, found, err := passwords.GetReactionSettings(ctx, owner.ID); err != nil || !found || got.Notify.ShowPreviews {
		t.Fatalf("reaction notify persistence = %+v found=%v err=%v", got.Notify, found, err)
	}

	if err := NewMediaStore(pool).PutPhoto(ctx, domain.Photo{ID: photoID, AccessHash: photoID + 1, Date: 100, DCID: 2}); err != nil {
		t.Fatalf("put photo: %v", err)
	}
	contacts := NewContactStore(pool)
	if _, err := contacts.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: friend.ID, FirstName: "Friend"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := contacts.SetPersonalPhotoWithDelivery(ctx, owner.ID, friend.ID, photoID, 100, func(store.ContactPersonalPhotoDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("contact builder error = %v", err)
	}
	if photos, _ := contacts.PersonalPhotos(ctx, owner.ID, []int64{friend.ID}); photos[friend.ID].PhotoID != 0 {
		t.Fatalf("failed contact write leaked photo: %+v", photos)
	}
	if _, found, err := contacts.SetPersonalPhotoWithDelivery(ctx, owner.ID, friend.ID, photoID, 100, func(snapshot store.ContactPersonalPhotoDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return postgresAbsoluteOwnerEffect(snapshot.ViewerUserID), nil
	}); err != nil || !found {
		t.Fatalf("contact aggregate: found=%v err=%v", found, err)
	}
	if photos, _ := contacts.PersonalPhotos(ctx, owner.ID, []int64{friend.ID}); photos[friend.ID].PhotoID != photoID {
		t.Fatalf("contact photo = %+v, want %d", photos, photoID)
	}

	var outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1`, owner.ID).Scan(&outboxCount); err != nil || outboxCount != 3 {
		t.Fatalf("absolute outbox count=%d err=%v, want 3", outboxCount, err)
	}
}
