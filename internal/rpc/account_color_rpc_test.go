package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestAccountUpdateColorPersistsExplicitZeroAndPushesSelfUser(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550003201", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sessions := &captureSessions{}
	router := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	color := &tg.PeerColor{}
	color.SetColor(0)
	color.SetBackgroundEmojiID(123456)
	req := &tg.AccountUpdateColorRequest{}
	req.SetColor(color)
	ok, err := router.onAccountUpdateColor(WithUserID(ctx, owner.ID), req)
	if err != nil || !ok {
		t.Fatalf("update color = ok %v err %v, want true/nil", ok, err)
	}

	saved, found, err := userStore.ByID(ctx, owner.ID)
	if err != nil || !found {
		t.Fatalf("load saved user found=%v err=%v", found, err)
	}
	assertDomainPeerColor(t, saved.Color, true, 0, 123456)
	assertTgUserPeerColor(t, tgSelfUser(saved).GetColor, true, 0, clientDocumentIDFromServerID(123456))

	snap := sessions.snapshot()
	if snap.userID != owner.ID {
		t.Fatalf("push user = %d, want %d", snap.userID, owner.ID)
	}
	updates, ok := snap.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", snap.message)
	}
	if len(updates.Users) != 1 {
		t.Fatalf("pushed users = %d, want 1 self user", len(updates.Users))
	}
	pushedUser, ok := updates.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("pushed user = %T, want *tg.User", updates.Users[0])
	}
	assertTgUserPeerColor(t, pushedUser.GetColor, true, 0, clientDocumentIDFromServerID(123456))
}

func TestAccountUpdateColorProfileSetAndClear(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550003202", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	router := New(Config{}, Deps{
		Users: appusers.NewService(userStore),
	}, zaptest.NewLogger(t), clock.System)

	color := &tg.PeerColor{}
	color.SetColor(3)
	color.SetBackgroundEmojiID(777)
	req := &tg.AccountUpdateColorRequest{}
	req.SetForProfile(true)
	req.SetColor(color)
	if ok, err := router.onAccountUpdateColor(WithUserID(ctx, owner.ID), req); err != nil || !ok {
		t.Fatalf("update profile color = ok %v err %v, want true/nil", ok, err)
	}
	saved, _, _ := userStore.ByID(ctx, owner.ID)
	assertDomainPeerColor(t, saved.ProfileColor, true, 3, 777)
	assertTgUserPeerColor(t, tgSelfUser(saved).GetProfileColor, true, 3, clientDocumentIDFromServerID(777))

	clear := &tg.AccountUpdateColorRequest{}
	clear.SetForProfile(true)
	if ok, err := router.onAccountUpdateColor(WithUserID(ctx, owner.ID), clear); err != nil || !ok {
		t.Fatalf("clear profile color = ok %v err %v, want true/nil", ok, err)
	}
	saved, _, _ = userStore.ByID(ctx, owner.ID)
	if !saved.ProfileColor.Empty() {
		t.Fatalf("profile color after clear = %+v, want empty", saved.ProfileColor)
	}
	if _, ok := tgSelfUser(saved).GetProfileColor(); ok {
		t.Fatal("self user profile_color after clear must be absent")
	}
}

func TestAccountUpdateColorRejectsUnsupportedInputs(t *testing.T) {
	router := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)

	invalid := &tg.PeerColor{}
	invalid.SetColor(99)
	req := &tg.AccountUpdateColorRequest{}
	req.SetColor(invalid)
	if ok, err := router.onAccountUpdateColor(ctx, req); ok || !tgerr.Is(err, "COLOR_INVALID") {
		t.Fatalf("invalid color = ok %v err %v, want COLOR_INVALID", ok, err)
	}

	collectible := &tg.AccountUpdateColorRequest{}
	collectible.SetColor(&tg.InputPeerColorCollectible{CollectibleID: 42})
	if ok, err := router.onAccountUpdateColor(ctx, collectible); ok || !tgerr.Is(err, "COLOR_INVALID") {
		t.Fatalf("collectible color = ok %v err %v, want COLOR_INVALID", ok, err)
	}
}

func assertDomainPeerColor(t *testing.T, got domain.PeerColor, hasColor bool, color int, backgroundEmojiID int64) {
	t.Helper()
	if got.HasColor != hasColor || got.Color != color || got.BackgroundEmojiID != backgroundEmojiID {
		t.Fatalf("domain peer color = %+v, want has=%v color=%d bg=%d", got, hasColor, color, backgroundEmojiID)
	}
}

func assertTgUserPeerColor(t *testing.T, get func() (tg.PeerColorClass, bool), hasColor bool, color int, backgroundEmojiID int64) {
	t.Helper()
	got, ok := get()
	if !ok {
		t.Fatal("tg user missing peer color")
	}
	peerColor, ok := got.(*tg.PeerColor)
	if !ok {
		t.Fatalf("tg peer color = %T, want *tg.PeerColor", got)
	}
	gotColor, gotHasColor := peerColor.GetColor()
	if gotHasColor != hasColor || gotColor != color {
		t.Fatalf("tg peer color id = %d has=%v, want %d has=%v", gotColor, gotHasColor, color, hasColor)
	}
	gotBackgroundEmojiID, gotHasBackgroundEmojiID := peerColor.GetBackgroundEmojiID()
	if gotBackgroundEmojiID != backgroundEmojiID || gotHasBackgroundEmojiID != (backgroundEmojiID != 0) {
		t.Fatalf("tg peer color bg = %d has=%v, want %d", gotBackgroundEmojiID, gotHasBackgroundEmojiID, backgroundEmojiID)
	}
}
