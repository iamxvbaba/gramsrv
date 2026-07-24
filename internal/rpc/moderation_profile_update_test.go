package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

type moderationProjectionUsers struct {
	UsersService
	audience []int64
	viewers  []int64
}

func (s *moderationProjectionUsers) ByIDs(_ context.Context, viewerUserID int64, ids []int64) ([]domain.User, error) {
	s.viewers = append(s.viewers, viewerUserID)
	return []domain.User{{ID: ids[0], FirstName: "Flagged", Scam: true}}, nil
}

func (s *moderationProjectionUsers) ModerationFlagAudience(_ context.Context, _ int64, _ int) ([]int64, error) {
	return append([]int64(nil), s.audience...), nil
}

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

func TestUserModerationFlagsPushStandardNonPTSUpdate(t *testing.T) {
	const (
		targetID        = int64(2002)
		onlineViewerID  = int64(1001)
		offlineViewerID = int64(3003)
	)
	users := &moderationProjectionUsers{
		audience: []int64{targetID, onlineViewerID, offlineViewerID},
	}
	sessions := &captureSessions{onlineUserIDs: []int64{targetID, onlineViewerID}}
	r := New(Config{}, Deps{Users: users, Sessions: sessions}, zap.NewNop(), clock.System)

	if err := r.NotifyUserModerationFlagsChanged(context.Background(), domain.User{
		ID: targetID, FirstName: "Flagged", Scam: true,
	}); err != nil {
		t.Fatalf("notify moderation flags: %v", err)
	}
	pushed := sessions.pushedUserIDs()
	if len(pushed) != 2 || pushed[0] != targetID || pushed[1] != onlineViewerID {
		t.Fatalf("pushed user ids = %v", pushed)
	}
	if len(users.viewers) != 2 || users.viewers[0] != targetID || users.viewers[1] != onlineViewerID {
		t.Fatalf("projected viewers = %v", users.viewers)
	}
	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("updates = %T %+v", sessions.lastUserPush(), sessions.lastUserPush())
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
	if len(updates.Users) != 1 {
		t.Fatalf("users = %+v", updates.Users)
	}
	if user, ok := updates.Users[0].(*tg.User); !ok || user.ID != targetID || !user.Scam || user.Fake {
		t.Fatalf("user = %T %+v", updates.Users[0], updates.Users[0])
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
