package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
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

func gotUserIDs(users []tg.UserClass) []int64 {
	out := make([]int64, 0, len(users))
	for _, item := range users {
		if user, ok := item.(*tg.User); ok {
			out = append(out, user.ID)
		}
	}
	return out
}
