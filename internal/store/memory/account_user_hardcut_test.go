package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func absoluteOwnerEffect(targetUserID int64) []store.DeliveryEffect {
	return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
		TargetUserID: targetUserID,
		Payload:      []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	})}
}

func TestUserReliableMutationsCommitWithDurableFactsMemory(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	outbox := NewDeliveryOutboxStore()
	events := NewUpdateEventStore()
	users.AttachDeliveryOutbox(outbox)
	users.AttachUpdateEventStore(events)
	user, err := users.Create(ctx, domain.User{Phone: "15550009991", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("encode failed")
	if _, err := users.UpdatePersonalChannelWithDelivery(ctx, user.ID, 77, func(store.UserDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("personal channel builder error = %v", err)
	}
	current, _, _ := users.ByID(ctx, user.ID)
	if current.PersonalChannelID != 0 || len(outbox.Snapshot()) != 0 {
		t.Fatalf("failed personal channel leaked state/outbox: user=%+v outbox=%+v", current, outbox.Snapshot())
	}
	if _, err := users.UpdatePersonalChannelWithDelivery(ctx, user.ID, 77, func(snapshot store.UserDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return absoluteOwnerEffect(snapshot.User.ID), nil
	}); err != nil {
		t.Fatalf("personal channel aggregate: %v", err)
	}
	current, _, _ = users.ByID(ctx, user.ID)
	if current.PersonalChannelID != 77 || len(outbox.Snapshot()) != 1 {
		t.Fatalf("personal channel state/outbox: user=%+v outbox=%+v", current, outbox.Snapshot())
	}

	status := domain.UserEmojiStatus{DocumentID: 42}
	event := domain.UpdateEvent{
		Type: domain.UpdateEventUserEmojiStatus, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: user.ID},
		EmojiStatus: status, Date: 100, PtsCount: 1,
	}
	badEvent := event
	badEvent.Pts = 9
	if _, _, err := users.UpdateEmojiStatusWithEvent(ctx, user.ID, status, badEvent); err == nil {
		t.Fatal("preallocated emoji event unexpectedly succeeded")
	}
	current, _, _ = users.ByID(ctx, user.ID)
	if !current.EmojiStatus().Empty() {
		t.Fatalf("failed emoji aggregate leaked state: %+v", current.EmojiStatus())
	}
	updated, allocated, err := users.UpdateEmojiStatusWithEvent(ctx, user.ID, status, event)
	if err != nil {
		t.Fatalf("emoji aggregate: %v", err)
	}
	if updated.EmojiStatus() != status || allocated.Pts != 1 {
		t.Fatalf("emoji aggregate result: user=%+v event=%+v", updated.EmojiStatus(), allocated)
	}
	events.mu.RLock()
	dispatches := append([]memoryUpdateDispatch(nil), events.dispatches[user.ID]...)
	events.mu.RUnlock()
	if len(dispatches) != 1 || dispatches[0].Pts != 1 || dispatches[0].ExcludeAuthKeyID != ([8]byte{}) || dispatches[0].ExcludeSessionID != 0 {
		t.Fatalf("emoji dispatches = %+v, want one zero-exclusion dispatch", dispatches)
	}
}

func TestNotifyAndContactReliableMutationsRollbackTogetherMemory(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("encode failed")

	passwords := NewPasswordStore()
	notifyOutbox := NewDeliveryOutboxStore()
	passwords.AttachDeliveryOutbox(notifyOutbox)
	scope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2002}}
	mute := 100
	if err := passwords.SaveNotifySettings(ctx, 1001, scope, domain.PeerNotifySettings{MuteUntil: &mute}); err != nil {
		t.Fatal(err)
	}
	if err := passwords.ResetNotifySettingsWithDelivery(ctx, 1001, func(store.NotifySettingsResetSnapshot) ([]store.DeliveryEffect, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("reset builder error = %v", err)
	}
	if _, found, _ := passwords.GetNotifySettings(ctx, 1001, scope); !found || len(notifyOutbox.Snapshot()) != 0 {
		t.Fatalf("failed reset leaked state/outbox: found=%v outbox=%+v", found, notifyOutbox.Snapshot())
	}
	if err := passwords.ResetNotifySettingsWithDelivery(ctx, 1001, func(snapshot store.NotifySettingsResetSnapshot) ([]store.DeliveryEffect, error) {
		return absoluteOwnerEffect(snapshot.OwnerUserID), nil
	}); err != nil {
		t.Fatalf("reset aggregate: %v", err)
	}
	if _, found, _ := passwords.GetNotifySettings(ctx, 1001, scope); found || len(notifyOutbox.Snapshot()) != 1 {
		t.Fatalf("reset state/outbox: found=%v outbox=%+v", found, notifyOutbox.Snapshot())
	}

	contacts := NewContactStore()
	contactOutbox := NewDeliveryOutboxStore()
	contacts.AttachDeliveryOutbox(contactOutbox)
	if _, err := contacts.Upsert(ctx, 1001, domain.ContactInput{ContactUserID: 2002, FirstName: "Friend"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := contacts.SetPersonalPhotoWithDelivery(ctx, 1001, 2002, 91, 100, func(store.ContactPersonalPhotoDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("contact builder error = %v", err)
	}
	if photos, _ := contacts.PersonalPhotos(ctx, 1001, []int64{2002}); photos[2002].PhotoID != 0 || len(contactOutbox.Snapshot()) != 0 {
		t.Fatalf("failed contact write leaked state/outbox: photos=%+v outbox=%+v", photos, contactOutbox.Snapshot())
	}
	if _, found, err := contacts.SetPersonalPhotoWithDelivery(ctx, 1001, 2002, 91, 100, func(snapshot store.ContactPersonalPhotoDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return absoluteOwnerEffect(snapshot.ViewerUserID), nil
	}); err != nil || !found {
		t.Fatalf("contact aggregate: found=%v err=%v", found, err)
	}
	if photos, _ := contacts.PersonalPhotos(ctx, 1001, []int64{2002}); photos[2002].PhotoID != 91 || len(contactOutbox.Snapshot()) != 1 {
		t.Fatalf("contact state/outbox: photos=%+v outbox=%+v", photos, contactOutbox.Snapshot())
	}
}

func TestReactionsNotifySettingsMustPersistMemory(t *testing.T) {
	ctx := context.Background()
	passwords := NewPasswordStore()
	settings := domain.DefaultAccountReactionSettings()
	settings.Notify.ShowPreviews = false
	if _, err := passwords.UpdateReactionSettings(ctx, 1001, domain.AccountReactionSettingsPatch{Notify: &settings.Notify}, nil); err != nil {
		t.Fatal(err)
	}
	got, found, err := passwords.GetReactionSettings(ctx, 1001)
	if err != nil || !found || got.Notify.ShowPreviews {
		t.Fatalf("reaction notify persistence = %+v found=%v err=%v", got.Notify, found, err)
	}
}
