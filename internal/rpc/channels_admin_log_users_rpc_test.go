package rpc

import (
	"context"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"
	"telesrv/internal/domain"
	"testing"
)

func TestChannelAdminLogUsersUsesSingleBatchLookup(t *testing.T) {
	users := &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{
		3: {ID: 3, FirstName: "Actor"},
		4: {ID: 4, FirstName: "Participant"},
		5: {ID: 5, FirstName: "Sender"},
	}}}
	r := New(Config{}, Deps{Users: users}, zaptest.NewLogger(t), clock.System)

	got := r.channelAdminLogUsers(context.Background(), 1, []domain.ChannelAdminLogEvent{
		{
			UserID: 3,
			Participant: &domain.ChannelMember{
				UserID:        4,
				InviterUserID: 3,
			},
			Message: &domain.ChannelMessage{
				SenderUserID: 5,
				From:         domain.Peer{Type: domain.PeerTypeUser, ID: 4},
			},
		},
		{
			UserID:          5,
			PrevParticipant: &domain.ChannelMember{UserID: 4},
		},
	})
	if users.byIDsCalls != 1 || users.byIDCalls != 0 {
		t.Fatalf("user lookups byIDs=%d byID=%d, want one ByIDs and no ByID", users.byIDsCalls, users.byIDCalls)
	}
	if len(users.lastByIDs) != 3 || users.lastByIDs[0] != 3 || users.lastByIDs[1] != 4 || users.lastByIDs[2] != 5 {
		t.Fatalf("ByIDs ids = %+v, want [3 4 5]", users.lastByIDs)
	}
	ids := gotUserIDs(got)
	if len(ids) != 3 || ids[0] != 3 || ids[1] != 4 || ids[2] != 5 {
		t.Fatalf("admin log users = %+v, want users 3/4/5", got)
	}
}

func TestInputChannelRefNormalizesIOSChannelInputs(t *testing.T) {
	const channelID int64 = 12345
	negativePackedID := -packedChannelPeerIDBase - channelID
	positivePackedID := packedChannelPeerIDBase + channelID

	tests := []struct {
		name  string
		input tg.InputChannelClass
	}{
		{
			name:  "negative packed input channel",
			input: &tg.InputChannel{ChannelID: negativePackedID, AccessHash: 77},
		},
		{
			name:  "positive packed input channel",
			input: &tg.InputChannel{ChannelID: positivePackedID, AccessHash: 77},
		},
		{
			name: "from message falls back to peer channel",
			input: &tg.InputChannelFromMessage{
				Peer: &tg.InputPeerChannel{ChannelID: channelID, AccessHash: 88},
			},
		},
		{
			name: "from message falls back to packed peer channel",
			input: &tg.InputChannelFromMessage{
				Peer: &tg.InputPeerChannel{ChannelID: positivePackedID, AccessHash: 99},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, ok := inputChannelRef(tt.input)
			if !ok || ref.ID != channelID {
				t.Fatalf("inputChannelRef() = %+v ok=%v, want id %d", ref, ok, channelID)
			}
		})
	}
}

func gotUserIDs(users []tg.UserClass) []int64 {
	out := make([]int64, 0, len(users))
	for _, item := range users {
		if user, ok := item.(*tg.User); ok {
			out = append(out, user.ID)
		}
	}
	return out
}
