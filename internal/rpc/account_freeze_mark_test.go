package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type freezeMarkTestFreezes struct {
	items map[int64]domain.AccountFreeze
}

func (f freezeMarkTestFreezes) AccountFreezes(_ context.Context, userIDs []int64) (map[int64]domain.AccountFreeze, error) {
	out := make(map[int64]domain.AccountFreeze, len(userIDs))
	for _, id := range userIDs {
		if freeze, ok := f.items[id]; ok {
			out[id] = freeze
		}
	}
	return out, nil
}

func TestUsersGetFullUserShowsFrozenMarkOnDeletedTombstone(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	viewer, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550001001", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	frozen, err := store.Create(ctx, domain.User{AccessHash: 2, Phone: "15550001002", FirstName: "Frozen"})
	if err != nil {
		t.Fatalf("create frozen: %v", err)
	}
	usersService := appusers.NewService(store, appusers.WithAccountFreezeProvider(freezeMarkTestFreezes{
		items: map[int64]domain.AccountFreeze{
			frozen.ID: {UserID: frozen.ID, Frozen: true, Version: 7},
		},
	}))
	r := New(Config{}, Deps{Users: usersService}, zaptest.NewLogger(t), clock.System)

	full, err := r.onUsersGetFullUser(WithUserID(ctx, viewer.ID), &tg.InputUser{
		UserID:     frozen.ID,
		AccessHash: frozen.AccessHash,
	})
	if err != nil {
		t.Fatalf("getFullUser: %v", err)
	}
	if full.FullUser.ID != frozen.ID {
		t.Fatalf("full user id = %d, want %d", full.FullUser.ID, frozen.ID)
	}
	if len(full.Users) != 1 {
		t.Fatalf("users = %d, want 1", len(full.Users))
	}
	projected, ok := full.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("user = %T, want *tg.User", full.Users[0])
	}
	if !projected.Deleted {
		t.Fatalf("projected user Deleted = false, want tombstone")
	}
	if icon, _ := projected.GetBotVerificationIcon(); icon != accountFrozenMarkIcon {
		t.Fatalf("projected user icon = %d, want %d", icon, accountFrozenMarkIcon)
	}
	if full.FullUser.BotVerification.BotID != accountFrozenMarkBotID ||
		full.FullUser.BotVerification.Icon != accountFrozenMarkIcon ||
		full.FullUser.BotVerification.Description != accountFrozenMarkDescription {
		t.Fatalf("full user bot_verification = %+v, want frozen mark", full.FullUser.BotVerification)
	}

	// The target viewing own profile keeps the live account without the mark.
	self, err := r.onUsersGetFullUser(WithUserID(ctx, frozen.ID), &tg.InputUserSelf{})
	if err != nil {
		t.Fatalf("getFullUser(self): %v", err)
	}
	selfUser, ok := self.Users[0].(*tg.User)
	if !ok || selfUser.Deleted {
		t.Fatalf("self user = %+v, want live account", self.Users[0])
	}
	if _, present := selfUser.GetBotVerificationIcon(); present {
		t.Fatalf("self user got frozen mark icon, want none")
	}
	if self.FullUser.BotVerification.Icon != 0 {
		t.Fatalf("self full user bot_verification = %+v, want none", self.FullUser.BotVerification)
	}
}
