package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestAccountUpdatePersonalChannelUsesOwnerAbsoluteAggregate(t *testing.T) {
	ctx := context.Background()
	outbox := memory.NewDeliveryOutboxStore()
	userStore := memory.NewUserStore()
	userStore.AttachDeliveryOutbox(outbox)
	owner, err := userStore.Create(ctx, domain.User{
		Phone: "15550009992", FirstName: "Owner", PersonalChannelID: 77,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{
		Users: appusers.NewService(userStore), DeliveryOutbox: outbox,
	}, zaptest.NewLogger(t), clock.System)
	if ok, err := r.onAccountUpdatePersonalChannel(WithUserID(ctx, owner.ID), &tg.InputChannelEmpty{}); err != nil || !ok {
		t.Fatalf("clear personal channel = ok=%v err=%v", ok, err)
	}
	current, _, _ := userStore.ByID(ctx, owner.ID)
	items := outbox.Snapshot()
	if current.PersonalChannelID != 0 || len(items) != 1 || items[0].TargetUserID != owner.ID ||
		items[0].ExcludeAuthKeyID != ([8]byte{}) || items[0].ExcludeSessionID != 0 {
		t.Fatalf("personal channel aggregate: user=%+v outbox=%+v", current, items)
	}
	updates := lastQueuedDeliveryUpdates(t, outbox)
	if len(updates.Updates) != 1 {
		t.Fatalf("personal channel updates = %+v, want one updateUser", updates.Updates)
	}
	if update, ok := updates.Updates[0].(*tg.UpdateUser); !ok || update.UserID != owner.ID {
		t.Fatalf("personal channel update = %T %+v", updates.Updates[0], updates.Updates[0])
	}
}

func TestAccountSetReactionsNotifySettingsPersistsOrFailsClosed(t *testing.T) {
	ctx := WithUserID(context.Background(), 1001)
	passwords := memory.NewPasswordStore()
	r := New(Config{}, Deps{
		Account: appaccount.NewService(passwords, appaccount.WithReactionSettings(passwords)),
	}, zaptest.NewLogger(t), clock.System)
	input := tg.ReactionsNotifySettings{ShowPreviews: false, Sound: &tg.NotificationSoundDefault{}}
	if _, err := r.onAccountSetReactionsNotifySettings(ctx, input); err != nil {
		t.Fatalf("set reaction notify: %v", err)
	}
	settings, found, err := passwords.GetReactionSettings(context.Background(), 1001)
	if err != nil || !found || settings.Notify.ShowPreviews {
		t.Fatalf("stored reaction notify = %+v found=%v err=%v", settings.Notify, found, err)
	}

	missing := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	if _, err := missing.onAccountSetReactionsNotifySettings(ctx, input); err == nil {
		t.Fatal("set reaction notify without persistence unexpectedly succeeded")
	}
}
