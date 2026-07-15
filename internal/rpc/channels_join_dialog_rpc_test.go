package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestBroadcastSelfJoinParticipantCarriesSelfInviter pins the fix for "joined a broadcast channel but
// no dialog appears". A broadcast join emits no service message; the official client materializes the
// chat-list entry by generating a LOCAL "you joined this channel" service message, which it only does
// when channel->inviter is a valid user — set from channels.getParticipant(self).inviter_id. Real TG
// returns inviter_id == user_id (self) for a self-join. telesrv previously left InviterUserID==0, so
// the client never generated the joined message and the channel stayed out of the list.
func TestBroadcastSelfJoinParticipantCarriesSelfInviter(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550002161", FirstName: "Owner"})
	joiner, _ := userStore.Create(ctx, domain.User{AccessHash: 62, Phone: "15550002162", FirstName: "Joiner"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Self Join Broadcast",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)

	if _, err := r.onChannelsJoinChannel(WithUserID(ctx, joiner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}); err != nil {
		t.Fatalf("self join broadcast: %v", err)
	}

	res, err := r.onChannelsGetParticipant(WithUserID(ctx, joiner.ID), &tg.ChannelsGetParticipantRequest{
		Channel:     &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Participant: &tg.InputPeerSelf{},
	})
	if err != nil {
		t.Fatalf("get participant self: %v", err)
	}
	self, ok := res.Participant.(*tg.ChannelParticipantSelf)
	if !ok {
		t.Fatalf("self participant = %T, want *tg.ChannelParticipantSelf", res.Participant)
	}
	if self.UserID != joiner.ID {
		t.Fatalf("participant user_id = %d, want %d", self.UserID, joiner.ID)
	}
	if self.InviterID != joiner.ID {
		t.Fatalf("participant inviter_id = %d, want self %d (so the client generates the joined message)", self.InviterID, joiner.ID)
	}
	if self.Date == 0 {
		t.Fatalf("participant date = 0, want the join date")
	}
}
