package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestBotInfoDeliveryAtomicLocalizationFallbackAndReplay(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	dialogs := NewDialogStore()
	bots := NewBotStore(users)
	bots.AttachDeliveryDependencies(dialogs, NewDeliveryOutboxStore())
	owner, err := users.Create(ctx, domain.User{Phone: "+83001", FirstName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := users.Create(ctx, domain.User{Phone: "+83002", FirstName: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := bots.CreateBotAccountWithDelivery(ctx, domain.User{FirstName: "Default", Username: "localized_bot"}, domain.BotProfile{OwnerUserID: owner.ID}, testBotLifecycleEffects)
	if err != nil {
		t.Fatal(err)
	}
	outbox := NewDeliveryOutboxStore()
	bots.AttachDeliveryDependencies(dialogs, outbox)
	if err := dialogs.SaveList(ctx, viewer.ID, domain.DialogList{Dialogs: []domain.Dialog{{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: bot.ID}}}}); err != nil {
		t.Fatal(err)
	}

	builds := 0
	effects := func(snapshot store.UserAudienceDeliverySnapshot) ([]store.DeliveryEffect, error) {
		builds++
		if len(snapshot.Audience) != 2 || snapshot.Audience[0] != owner.ID || snapshot.Audience[1] != viewer.ID {
			t.Fatalf("audience = %v", snapshot.Audience)
		}
		out := make([]store.DeliveryEffect, len(snapshot.Audience))
		for i, viewerID := range snapshot.Audience {
			out[i] = store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
				TargetUserID: viewerID, Payload: []byte{byte(snapshot.User.BotInfoVersion)}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
			})
		}
		return out, nil
	}
	before, _, _ := users.ByID(ctx, bot.ID)
	version, changed, err := bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{
		SetName: true, Name: "Renamed", SetAbout: true, About: "default about", SetDescription: true, Description: "default description",
	}, effects)
	if err != nil || !changed || version != before.BotInfoVersion+1 {
		t.Fatalf("default update = version:%d changed:%v err:%v", version, changed, err)
	}
	if len(outbox.Snapshot()) != 2 {
		t.Fatalf("default update rows = %d", len(outbox.Snapshot()))
	}
	defaults, found, err := bots.GetBotInfo(ctx, bot.ID, "")
	if err != nil || !found || defaults.Name != "Renamed" || defaults.About != "default about" || defaults.Description != "default description" {
		t.Fatalf("default info = %+v found=%v err=%v", defaults, found, err)
	}

	version, changed, err = bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{
		LangCode: "zh-hans", SetName: true, Name: "Localized Name",
	}, effects)
	if err != nil || !changed || version != before.BotInfoVersion+2 {
		t.Fatalf("localized update = version:%d changed:%v err:%v", version, changed, err)
	}
	localized, found, err := bots.GetBotInfo(ctx, bot.ID, "zh-hans")
	if err != nil || !found || localized.Name != "Localized Name" || localized.About != defaults.About || localized.Description != defaults.Description {
		t.Fatalf("localized info = %+v found=%v err=%v", localized, found, err)
	}
	global, _, _ := bots.GetBotInfo(ctx, bot.ID, "")
	if global.Name != "Renamed" {
		t.Fatalf("localized write changed global name to %q", global.Name)
	}

	version, changed, err = bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{LangCode: "fr"}, effects)
	if err != nil || !changed || version != before.BotInfoVersion+3 {
		t.Fatalf("lang-only update = version:%d changed:%v err:%v", version, changed, err)
	}
	rowsBeforeReplay := len(outbox.Snapshot())
	version, changed, err = bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{LangCode: "fr"}, func(store.UserAudienceDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return nil, errors.New("idempotent replay must not project")
	})
	if err != nil || changed || version != before.BotInfoVersion+3 || len(outbox.Snapshot()) != rowsBeforeReplay {
		t.Fatalf("lang replay = version:%d changed:%v rows:%d err:%v", version, changed, len(outbox.Snapshot()), err)
	}

	projectionErr := errors.New("projection failed")
	if _, _, err := bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{SetDescription: true, Description: "must roll back"}, func(store.UserAudienceDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return nil, projectionErr
	}); !errors.Is(err, projectionErr) {
		t.Fatalf("projection error = %v", err)
	}
	afterFailure, _, _ := bots.GetBotInfo(ctx, bot.ID, "")
	userAfterFailure, _, _ := users.ByID(ctx, bot.ID)
	if afterFailure.Description != "default description" || userAfterFailure.BotInfoVersion != before.BotInfoVersion+3 || len(outbox.Snapshot()) != rowsBeforeReplay {
		t.Fatalf("failed mutation leaked: info=%+v version=%d rows=%d", afterFailure, userAfterFailure.BotInfoVersion, len(outbox.Snapshot()))
	}
	if builds != 3 {
		t.Fatalf("builder calls = %d, want three committed mutations", builds)
	}
}

func TestBotInfoDeliveryMissingDependencyFailsBeforeMutation(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	dialogs := NewDialogStore()
	bots := NewBotStore(users)
	bots.AttachDeliveryDependencies(dialogs, NewDeliveryOutboxStore())
	owner, _ := users.Create(ctx, domain.User{Phone: "+84001"})
	bot, _, err := bots.CreateBotAccountWithDelivery(ctx, domain.User{FirstName: "Before", Username: "dependency_bot"}, domain.BotProfile{OwnerUserID: owner.ID}, testBotLifecycleEffects)
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := users.ByID(ctx, bot.ID)
	bots.AttachDeliveryDependencies(dialogs, nil)
	if _, _, err := bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{SetName: true, Name: "After"}, appUserAudienceTestEffects); !errors.Is(err, store.ErrDeliveryOutboxRequired) {
		t.Fatalf("missing outbox error = %v", err)
	}
	after, _, _ := users.ByID(ctx, bot.ID)
	if after.FirstName != before.FirstName || after.BotInfoVersion != before.BotInfoVersion {
		t.Fatalf("missing dependency leaked mutation: before=%+v after=%+v", before, after)
	}
}

func TestBotInfoDeliveryAudienceOverflowRollsBack(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	dialogs := NewDialogStore()
	outbox := NewDeliveryOutboxStore()
	bots := NewBotStore(users)
	bots.AttachDeliveryDependencies(dialogs, outbox)
	owner, _ := users.Create(ctx, domain.User{Phone: "+85001"})
	bot, _, err := bots.CreateBotAccountWithDelivery(ctx, domain.User{FirstName: "Before", Username: "bounded_info_bot"}, domain.BotProfile{OwnerUserID: owner.ID}, testBotLifecycleEffects)
	if err != nil {
		t.Fatal(err)
	}
	outbox = NewDeliveryOutboxStore()
	bots.AttachDeliveryDependencies(dialogs, outbox)
	users.mu.Lock()
	dialogs.mu.Lock()
	for i := 0; i < store.MaxBotInfoDeliveryAudience; i++ {
		viewerID := int64(950000 + i)
		users.byID[viewerID] = domain.User{ID: viewerID, FirstName: "viewer"}
		dialogs.m[viewerID] = domain.DialogList{Dialogs: []domain.Dialog{{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: bot.ID}}}}
	}
	dialogs.mu.Unlock()
	users.mu.Unlock()
	before, _, _ := users.ByID(ctx, bot.ID)
	called := false
	if _, _, err := bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{SetName: true, Name: "After"}, func(store.UserAudienceDeliverySnapshot) ([]store.DeliveryEffect, error) {
		called = true
		return nil, nil
	}); err == nil {
		t.Fatal("audience overflow unexpectedly succeeded")
	}
	if called {
		t.Fatal("builder called after audience overflow")
	}
	after, _, _ := users.ByID(ctx, bot.ID)
	if after.FirstName != before.FirstName || after.BotInfoVersion != before.BotInfoVersion || len(outbox.Snapshot()) != 0 {
		t.Fatalf("overflow leaked mutation: before=%+v after=%+v rows=%d", before, after, len(outbox.Snapshot()))
	}
}

func appUserAudienceTestEffects(snapshot store.UserAudienceDeliverySnapshot) ([]store.DeliveryEffect, error) {
	effects := make([]store.DeliveryEffect, len(snapshot.Audience))
	for i, viewerID := range snapshot.Audience {
		effects[i] = store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: viewerID, Payload: []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})
	}
	return effects, nil
}
