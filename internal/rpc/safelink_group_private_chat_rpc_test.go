package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestSafeLinkGroupPrivateChatForbiddenRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 7101, Phone: "15550007101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 7102, Phone: "15550007102", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash}},
		Title: "Private Chat Policy",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	if got := safeLinkGetPrivateChatForbidden(t, r, WithUserID(ctx, member.ID), input); got {
		t.Fatalf("initial private chat forbidden = true, want false")
	}

	if _, _, err := r.trySafeLinkGroupPrivateChatRPC(WithUserID(ctx, member.ID), safeLinkTogglePrivateChatBuffer(t, input, true), safeLinkToggleGroupPrivateChatForbiddenTypeID); err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member toggle err = %v, want CHAT_ADMIN_REQUIRED", err)
	}

	enc, handled, err := r.trySafeLinkGroupPrivateChatRPC(WithUserID(ctx, owner.ID), safeLinkTogglePrivateChatBuffer(t, input, true), safeLinkToggleGroupPrivateChatForbiddenTypeID)
	if err != nil || !handled {
		t.Fatalf("owner toggle on handled=%v err=%v", handled, err)
	}
	updates, ok := enc.(*tg.Updates)
	if !ok {
		t.Fatalf("toggle response = %T, want *tg.Updates", enc)
	}
	if len(updates.Updates) == 0 {
		t.Fatalf("toggle response updates empty, want UpdateChannel refresh signal")
	}
	if refresh, ok := updates.Updates[0].(*tg.UpdateChannel); !ok || refresh.ChannelID != channel.ID {
		t.Fatalf("toggle response first update = %T %#v, want UpdateChannel(%d)", updates.Updates[0], updates.Updates[0], channel.ID)
	}
	if got := safeLinkGetPrivateChatForbidden(t, r, WithUserID(ctx, member.ID), input); !got {
		t.Fatalf("private chat forbidden after owner toggle = false, want true")
	}
	view, err := channelService.GetChannel(ctx, owner.ID, channel.ID)
	if err != nil {
		t.Fatalf("get channel after toggle: %v", err)
	}
	if !view.Channel.PrivateChatForbidden {
		t.Fatalf("stored private chat forbidden = false, want true")
	}

	if _, _, err := r.trySafeLinkGroupPrivateChatRPC(WithUserID(ctx, owner.ID), safeLinkTogglePrivateChatBuffer(t, input, false), safeLinkToggleGroupPrivateChatForbiddenTypeID); err != nil {
		t.Fatalf("owner toggle off: %v", err)
	}
	if got := safeLinkGetPrivateChatForbidden(t, r, WithUserID(ctx, member.ID), input); got {
		t.Fatalf("private chat forbidden after owner toggle off = true, want false")
	}
}

func safeLinkGetPrivateChatForbidden(t *testing.T, r *Router, ctx context.Context, channel tg.InputChannelClass) bool {
	t.Helper()
	b := &bin.Buffer{}
	b.PutID(safeLinkGetGroupPrivateChatForbiddenTypeID)
	if err := channel.Encode(b); err != nil {
		t.Fatalf("encode get channel: %v", err)
	}
	enc, handled, err := r.trySafeLinkGroupPrivateChatRPC(ctx, b, safeLinkGetGroupPrivateChatForbiddenTypeID)
	if err != nil || !handled {
		t.Fatalf("get private chat forbidden handled=%v err=%v", handled, err)
	}
	_, ok := enc.(*tg.BoolTrue)
	return ok
}

func safeLinkTogglePrivateChatBuffer(t *testing.T, channel tg.InputChannelClass, enabled bool) *bin.Buffer {
	t.Helper()
	b := &bin.Buffer{}
	b.PutID(safeLinkToggleGroupPrivateChatForbiddenTypeID)
	if err := channel.Encode(b); err != nil {
		t.Fatalf("encode toggle channel: %v", err)
	}
	var boolValue tg.BoolClass = &tg.BoolFalse{}
	if enabled {
		boolValue = &tg.BoolTrue{}
	}
	if err := boolValue.Encode(b); err != nil {
		t.Fatalf("encode toggle enabled: %v", err)
	}
	return b
}
