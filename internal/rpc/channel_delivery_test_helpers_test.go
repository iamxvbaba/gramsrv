package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func testPendingJoinEffects(snapshot store.ChannelPendingJoinDeliverySnapshot) ([]store.DeliveryEffect, error) {
	effects := make([]store.DeliveryEffect, 0, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		effects = append(effects, store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: target.TargetUserID, Payload: []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		}))
	}
	return effects, nil
}

func testAvailableMinEffects(snapshot store.ChannelAvailableMinDeliverySnapshot) ([]store.DeliveryEffect, error) {
	if snapshot.TargetUserID == 0 {
		return nil, nil
	}
	return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
		TargetUserID: snapshot.TargetUserID, Payload: []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	})}, nil
}

func TestPendingJoinDeliveryEffectsUseViewerPayloadAndOnlyCurrentSessionExclusion(t *testing.T) {
	r := &Router{}
	authKeyID := [8]byte{9, 8, 7}
	ctx := WithSessionID(WithRawAuthKeyID(WithUserID(context.Background(), 10), authKeyID), 77)
	snapshot := store.ChannelPendingJoinDeliverySnapshot{Targets: []store.ChannelPendingJoinDeliveryTarget{
		{TargetUserID: 10, Channel: domain.Channel{ID: 55, AccessHash: 66, Title: "group", Megagroup: true}, Pending: domain.ChannelPendingJoinRequests{ChannelID: 55, Count: 2, RecentRequesters: []int64{12, 11}}, RecentUsers: []domain.User{{ID: 12, AccessHash: 120}, {ID: 11, AccessHash: 110}}},
		{TargetUserID: 20, Channel: domain.Channel{ID: 55, AccessHash: 66, Title: "group", Megagroup: true}, Pending: domain.ChannelPendingJoinRequests{ChannelID: 55, Count: 2, RecentRequesters: []int64{12, 11}}, RecentUsers: []domain.User{{ID: 12, AccessHash: 120}, {ID: 11, AccessHash: 110}}},
	}}
	effects, err := r.pendingJoinDeliveryEffects(ctx, 10, 123)(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 {
		t.Fatalf("effects = %d, want 2", len(effects))
	}
	if effects[0].ExcludeAuthKeyID != authKeyID || effects[0].ExcludeSessionID != 77 {
		t.Fatalf("current-user exclusion = %x/%d", effects[0].ExcludeAuthKeyID, effects[0].ExcludeSessionID)
	}
	if effects[1].ExcludeAuthKeyID != ([8]byte{}) || effects[1].ExcludeSessionID != 0 {
		t.Fatalf("cross-user exclusion leaked = %x/%d", effects[1].ExcludeAuthKeyID, effects[1].ExcludeSessionID)
	}
	for _, effect := range effects {
		decoded, err := decodeDeliveryUpdate(effect.Payload)
		if err != nil {
			t.Fatal(err)
		}
		updates, ok := decoded.(*tg.Updates)
		if !ok || updates.Date != 123 || len(updates.Updates) != 1 {
			t.Fatalf("decoded pending payload = %#v", decoded)
		}
		pending, ok := updates.Updates[0].(*tg.UpdatePendingJoinRequests)
		if !ok || pending.RequestsPending != 2 || len(pending.RecentRequesters) != 2 {
			t.Fatalf("pending update = %#v", updates.Updates[0])
		}
	}
}

func TestAvailableMinDeliveryEffectsPreserveCurrentSessionExclusion(t *testing.T) {
	r := &Router{}
	authKeyID := [8]byte{4, 3, 2}
	ctx := WithSessionID(WithRawAuthKeyID(WithUserID(context.Background(), 10), authKeyID), 88)
	effects, err := r.channelAvailableMinDeliveryEffects(ctx, 10, 222)(store.ChannelAvailableMinDeliverySnapshot{
		TargetUserID: 10, Channel: domain.Channel{ID: 99, AccessHash: 100, Title: "channel", Broadcast: true}, AvailableMinID: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 1 || effects[0].ExcludeAuthKeyID != authKeyID || effects[0].ExcludeSessionID != 88 {
		t.Fatalf("available-min effect = %+v", effects)
	}
	decoded, err := decodeDeliveryUpdate(effects[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	updates, ok := decoded.(*tg.Updates)
	if !ok || updates.Date != 222 || len(updates.Updates) != 1 {
		t.Fatalf("decoded available-min payload = %#v", decoded)
	}
	available, ok := updates.Updates[0].(*tg.UpdateChannelAvailableMessages)
	if !ok || available.ChannelID != 99 || available.AvailableMinID != 7 {
		t.Fatalf("available-min update = %#v", updates.Updates[0])
	}
}
