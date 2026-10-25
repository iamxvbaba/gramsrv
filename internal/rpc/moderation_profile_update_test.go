package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type moderationProjectionChannels struct {
	ChannelsService
	freshCalls int
}

func (s *moderationProjectionChannels) GetChannels(_ context.Context, _ int64, ids []int64) ([]domain.ChannelView, error) {
	return []domain.ChannelView{{Channel: domain.Channel{ID: ids[0], Title: "stale", Megagroup: true}}}, nil
}

func (s *moderationProjectionChannels) GetChannelsAuthoritative(_ context.Context, viewerUserID int64, ids []int64) ([]domain.ChannelView, error) {
	s.freshCalls++
	return []domain.ChannelView{{
		Channel: domain.Channel{ID: ids[0], Title: "fresh", Megagroup: true, Scam: true},
		Self:    domain.ChannelMember{ChannelID: ids[0], UserID: viewerUserID, Status: domain.ChannelMemberActive},
	}}, nil
}

func (s *moderationProjectionChannels) FilterActiveMemberIDs(_ context.Context, _ int64, userIDs []int64) ([]int64, error) {
	return append([]int64(nil), userIDs...), nil
}

func TestUserAudienceDeliveryEffectsBuildStandardNonPTSUpdate(t *testing.T) {
	const (
		targetID        = int64(2002)
		onlineViewerID  = int64(1001)
		offlineViewerID = int64(3003)
	)
	r := New(Config{}, Deps{}, zap.NewNop(), clock.System)
	effects, err := r.UserAudienceDeliveryEffects(store.UserAudienceDeliverySnapshot{
		User:     domain.User{ID: targetID, FirstName: "Flagged", Scam: true},
		Audience: []int64{targetID, onlineViewerID, offlineViewerID},
	})
	if err != nil {
		t.Fatalf("build moderation effects: %v", err)
	}
	if len(effects) != 3 {
		t.Fatalf("effects = %d, want one durable row per frozen viewer", len(effects))
	}
	for i, viewerID := range []int64{targetID, onlineViewerID, offlineViewerID} {
		if effects[i].Kind != store.DeliveryEffectAbsolute || effects[i].TargetUserID != viewerID {
			t.Fatalf("effect[%d] = %+v, want absolute target %d", i, effects[i], viewerID)
		}
	}
	decoded, err := decodeDeliveryUpdate(effects[0].Payload)
	if err != nil {
		t.Fatalf("decode effect: %v", err)
	}
	updates, ok := decoded.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("updates = %T %+v", decoded, decoded)
	}
	refresh, ok := updates.Updates[0].(*tg.UpdateUser)
	if !ok || refresh.UserID != targetID {
		t.Fatalf("refresh = %T %+v", updates.Updates[0], updates.Updates[0])
	}
	for _, update := range updates.Updates {
		if _, syntheticDelete := update.(*tg.UpdateDeleteMessages); syntheticDelete {
			t.Fatalf("synthetic delete bookkeeping leaked into moderation update: %+v", update)
		}
	}
	if len(updates.Users) != 0 {
		t.Fatalf("audience refresh unexpectedly embedded viewer-specific users: %+v", updates.Users)
	}
}

func TestChannelModerationFlagsPushStandardNonPTSUpdate(t *testing.T) {
	const (
		ownerID   = int64(3003)
		memberID  = int64(3004)
		channelID = int64(4004)
	)
	channels := &moderationProjectionChannels{}
	sessions := &captureSessions{
		onlineUserIDs:  []int64{ownerID, memberID},
		channelMembers: map[int64][]int64{channelID: {ownerID, memberID}},
	}
	r := New(Config{}, Deps{Channels: channels, Sessions: sessions}, zap.NewNop(), clock.System)
	if err := r.NotifyChannelChanged(context.Background(), domain.Channel{
		ID: channelID, AccessHash: 44, CreatorUserID: ownerID,
		Title: "Flagged channel", Megagroup: true, Scam: true,
	}); err != nil {
		t.Fatalf("notify channel flags: %v", err)
	}

	pushed := sessions.pushedUserIDs()
	if len(pushed) != 2 {
		t.Fatalf("pushed user ids = %v", pushed)
	}
	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("updates = %T %+v", sessions.lastUserPush(), sessions.lastUserPush())
	}
	refresh, ok := updates.Updates[0].(*tg.UpdateChannel)
	if !ok || refresh.ChannelID != channelID {
		t.Fatalf("refresh = %T %+v", updates.Updates[0], updates.Updates[0])
	}
	for _, update := range updates.Updates {
		if _, syntheticDelete := update.(*tg.UpdateDeleteMessages); syntheticDelete {
			t.Fatalf("synthetic delete bookkeeping leaked into moderation update: %+v", update)
		}
	}
	if len(updates.Chats) != 1 {
		t.Fatalf("chats = %+v", updates.Chats)
	}
	channel, ok := updates.Chats[0].(*tg.Channel)
	if !ok || channel.ID != channelID || !channel.Scam || channel.Fake {
		t.Fatalf("channel = %T %+v", updates.Chats[0], updates.Chats[0])
	}
}

func TestChannelStateRefreshEventBypassesServerProjectionCache(t *testing.T) {
	channels := &moderationProjectionChannels{}
	r := New(Config{}, Deps{Channels: channels}, zap.NewNop(), clock.System)
	const viewerID = int64(5005)
	events := r.enrichUpdateEvents(context.Background(), viewerID, []domain.UpdateEvent{
		{
			UserID: viewerID, Type: domain.UpdateEventChannelState,
			Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 7007},
		},
	})
	if channels.freshCalls != 1 || len(events[0].Channels) != 1 ||
		events[0].Channels[0].Title != "fresh" || !events[0].Channels[0].Scam {
		t.Fatalf("authoritative channel refresh = calls:%d channels:%+v", channels.freshCalls, events[0].Channels)
	}
}

// A deleted peer has to reach the client as Deleted Account, not as an unnamed
// stranger. updateUser alone only refreshes the photo on several clients, so the
// v2 durable tombstone delivery carries the name through updateUserName and an
// explicit empty status as well.
func TestDeletedAccountDeliveryCarriesDisplayName(t *testing.T) {
	const (
		targetID = int64(4004)
		viewerID = int64(1001)
	)
	r := New(Config{}, Deps{}, zap.NewNop(), clock.System)
	effects, err := r.UserAudienceDeliveryEffects(store.UserAudienceDeliverySnapshot{
		User:     domain.User{ID: targetID, Deleted: true, DeletedAt: 1_800_000_000},
		Audience: []int64{viewerID},
	})
	if err != nil || len(effects) != 1 {
		t.Fatalf("build tombstone effects = %+v err=%v", effects, err)
	}
	decoded, err := decodeDeliveryUpdate(effects[0].Payload)
	if err != nil {
		t.Fatalf("decode effect: %v", err)
	}
	updates, ok := decoded.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T", decoded)
	}
	var sawRefresh, sawName, sawEmptyStatus bool
	for _, update := range updates.Updates {
		switch typed := update.(type) {
		case *tg.UpdateUser:
			sawRefresh = typed.UserID == targetID
		case *tg.UpdateUserName:
			sawName = typed.UserID == targetID && typed.FirstName == deletedAccountDisplayName
		case *tg.UpdateUserStatus:
			_, empty := typed.Status.(*tg.UserStatusEmpty)
			sawEmptyStatus = typed.UserID == targetID && empty
		}
	}
	if !sawRefresh || !sawName || !sawEmptyStatus {
		t.Fatalf("tombstone delivery refresh=%v name=%v emptyStatus=%v: %+v", sawRefresh, sawName, sawEmptyStatus, updates.Updates)
	}
}
