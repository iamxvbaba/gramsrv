package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func TestMessagesAttachMenuBotsEmptyCatalog(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)

	got, err := f.router.onMessagesGetAttachMenuBots(WithUserID(ctx, f.owner.ID), 0)
	if err != nil {
		t.Fatalf("get attach menu bots: %v", err)
	}
	list, ok := got.(*tg.AttachMenuBots)
	if !ok {
		t.Fatalf("attach menu bots = %T, want *tg.AttachMenuBots", got)
	}
	if list.Hash != 0 || len(list.Bots) != 0 || len(list.Users) != 0 {
		t.Fatalf("attach menu bots = %+v, want empty catalog", list)
	}
}

func TestMessagesGetAttachMenuBotRejectsWithoutCatalog(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)

	if _, err := f.router.onMessagesGetAttachMenuBot(ownerCtx, inputUser(f.bot)); !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("get attach menu bot err = %v, want BOT_INVALID", err)
	}
	if _, err := f.router.onMessagesGetAttachMenuBot(ownerCtx, inputUser(f.peer)); !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("get non-bot attach menu err = %v, want BOT_INVALID", err)
	}
}

func TestMessagesToggleBotInAttachMenuRejectsWithoutCatalog(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)

	if ok, err := f.router.onMessagesToggleBotInAttachMenu(ownerCtx, &tg.MessagesToggleBotInAttachMenuRequest{
		Bot:     inputUser(f.bot),
		Enabled: true,
	}); ok || !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("enable attach menu bot = %v,%v, want false,BOT_INVALID", ok, err)
	}
	if ok, err := f.router.onMessagesToggleBotInAttachMenu(ownerCtx, &tg.MessagesToggleBotInAttachMenuRequest{
		Bot:     inputUser(f.bot),
		Enabled: false,
	}); ok || !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("disable attach menu bot = %v,%v, want false,BOT_INVALID", ok, err)
	}
	if ok, err := f.router.onMessagesToggleBotInAttachMenu(ownerCtx, &tg.MessagesToggleBotInAttachMenuRequest{
		Bot:     inputUser(f.peer),
		Enabled: true,
	}); ok || !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("toggle non-bot attach menu = %v,%v, want false,BOT_INVALID", ok, err)
	}
}

func TestMessagesAttachMenuExplicitStubsAreRegistered(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)

	tests := []struct {
		name string
		req  bin.Encoder
	}{
		{
			name: "getAttachMenuBot",
			req:  &tg.MessagesGetAttachMenuBotRequest{Bot: inputUser(f.bot)},
		},
		{
			name: "toggleBotInAttachMenu",
			req: &tg.MessagesToggleBotInAttachMenuRequest{
				Bot:     inputUser(f.bot),
				Enabled: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in bin.Buffer
			if err := tt.req.Encode(&in); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			if _, err := f.router.Dispatch(userCtx, [8]byte{}, 0, &in); !tgerr.Is(err, "BOT_INVALID") {
				t.Fatalf("dispatch err = %v, want BOT_INVALID", err)
			}
		})
	}
}
