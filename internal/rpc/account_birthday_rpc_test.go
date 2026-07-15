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

func TestAccountUpdateBirthdayPersistsFullUserAndPushesRefresh(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550003301", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sessions := &captureSessions{}
	router := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	birthday := tg.Birthday{Day: 14, Month: 2}
	birthday.SetYear(1990)
	req := &tg.AccountUpdateBirthdayRequest{}
	req.SetBirthday(birthday)
	callCtx := WithSessionID(WithUserID(ctx, owner.ID), 4242)
	ok, err := router.onAccountUpdateBirthday(callCtx, req)
	if err != nil || !ok {
		t.Fatalf("update birthday = ok %v err %v, want true/nil", ok, err)
	}

	saved, found, err := userStore.ByID(ctx, owner.ID)
	if err != nil || !found {
		t.Fatalf("load saved user found=%v err=%v", found, err)
	}
	if saved.Birthday != (domain.Birthday{Day: 14, Month: 2, Year: 1990}) {
		t.Fatalf("saved birthday = %+v, want 14/2/1990", saved.Birthday)
	}

	full, err := router.onUsersGetFullUser(WithUserID(ctx, owner.ID), &tg.InputUserSelf{})
	if err != nil {
		t.Fatalf("get full user: %v", err)
	}
	gotBirthday, ok := full.FullUser.GetBirthday()
	if !ok {
		t.Fatal("full user missing birthday")
	}
	gotYear, gotYearOK := gotBirthday.GetYear()
	if gotBirthday.Day != 14 || gotBirthday.Month != 2 || !gotYearOK || gotYear != 1990 {
		t.Fatalf("full user birthday = %+v yearOK=%v, want 14/2/1990", gotBirthday, gotYearOK)
	}

	snap := sessions.snapshot()
	if snap.userID != owner.ID || snap.sessionID != 4242 {
		t.Fatalf("push target user/session = %d/%d, want %d/4242", snap.userID, snap.sessionID, owner.ID)
	}
	updates, ok := snap.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", snap.message)
	}
	hasUserUpdate := false
	for _, update := range updates.Updates {
		if u, ok := update.(*tg.UpdateUser); ok && u.UserID == owner.ID {
			hasUserUpdate = true
		}
	}
	if !hasUserUpdate {
		t.Fatalf("updates = %+v, want UpdateUser for self", updates.Updates)
	}
	if len(updates.Users) != 1 {
		t.Fatalf("pushed users = %d, want 1 self user", len(updates.Users))
	}
	pushedUser, ok := updates.Users[0].(*tg.User)
	if !ok || pushedUser.ID != owner.ID {
		t.Fatalf("pushed user = %T %+v, want self user", updates.Users[0], updates.Users[0])
	}
}
