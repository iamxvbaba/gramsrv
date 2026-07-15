package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func TestUsersGetUsersUsesSingleBatchLookupAndPreservesInputOrder(t *testing.T) {
	const (
		currentID = int64(1000000001)
		peerID    = int64(1000000002)
	)
	users := &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{
		currentID: {ID: currentID, AccessHash: 101, FirstName: "Alice"},
		peerID:    {ID: peerID, AccessHash: 202, FirstName: "Bob"},
	}}}
	r := New(Config{}, Deps{Users: users}, zaptest.NewLogger(t), clock.System)

	got, err := r.onUsersGetUsers(WithUserID(context.Background(), currentID), []tg.InputUserClass{
		&tg.InputUserSelf{},
		&tg.InputUser{UserID: peerID, AccessHash: 202},
		&tg.InputUser{UserID: peerID, AccessHash: 999},
		&tg.InputUser{UserID: peerID},
		&tg.InputUser{UserID: currentID, AccessHash: 101},
		&tg.InputUserSelf{},
	})
	if err != nil {
		t.Fatalf("get users: %v", err)
	}
	if users.selfCalls != 1 || users.byIDsCalls != 1 || users.byIDCalls != 0 {
		t.Fatalf("lookups self=%d byIDs=%d byID=%d, want one Self, one ByIDs and no ByID", users.selfCalls, users.byIDsCalls, users.byIDCalls)
	}
	if len(users.lastByIDs) != 2 || users.lastByIDs[0] != peerID || users.lastByIDs[1] != currentID {
		t.Fatalf("ByIDs ids = %+v, want [%d %d]", users.lastByIDs, peerID, currentID)
	}
	if len(got) != 5 {
		t.Fatalf("users = %+v, want bad access_hash skipped and duplicate inputs preserved", got)
	}
	// 显式自己 ID（InputUser{UserID: currentID}，index 3）也必须带 self 标志——
	// 否则 self=false 的自己 user 会污染 DrKLO 账号缓存（Saved Messages 变身）。
	wantIDs := []int64{currentID, peerID, peerID, currentID, currentID}
	wantSelf := []bool{true, false, false, true, true}
	for i, item := range got {
		user, ok := item.(*tg.User)
		if !ok {
			t.Fatalf("user[%d] = %T, want *tg.User", i, item)
		}
		if user.ID != wantIDs[i] || user.Self != wantSelf[i] {
			t.Fatalf("user[%d] = id %d self %v, want id %d self %v", i, user.ID, user.Self, wantIDs[i], wantSelf[i])
		}
	}
}
