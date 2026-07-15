package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestImportChatInviteErrorsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 58, Phone: "15550002258", FirstName: "Owner"})
	first, _ := userStore.Create(ctx, domain.User{AccessHash: 59, Phone: "15550002259", FirstName: "First"})
	second, _ := userStore.Create(ctx, domain.User{AccessHash: 60, Phone: "15550002260", FirstName: "Second"})
	channelStore := memory.NewChannelStore()
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "RPC Import Errors",
		Megagroup: true,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	inputChannel := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	requestInvite, err := r.onMessagesExportChatInvite(WithUserID(ctx, owner.ID), &tg.MessagesExportChatInviteRequest{
		Peer:          input,
		Title:         "approval",
		RequestNeeded: true,
	})
	if err != nil {
		t.Fatalf("export request-needed invite: %v", err)
	}
	requestHash := strings.TrimPrefix(requestInvite.(*tg.ChatInviteExported).Link, "https://telesrv.net/+")
	if _, err := r.onMessagesImportChatInvite(WithUserID(ctx, first.ID), requestHash); err == nil || !strings.Contains(err.Error(), "INVITE_REQUEST_SENT") {
		t.Fatalf("import request-needed err = %v, want INVITE_REQUEST_SENT", err)
	}
	pushedPending := sessions.snapshot()
	if pushedPending.userID != owner.ID || pushedPending.messageType != proto.MessageFromServer {
		t.Fatalf("pending request push = %+v, want owner server update", pushedPending)
	}
	pushedPendingUpdates, ok := pushedPending.message.(*tg.Updates)
	if !ok || len(pushedPendingUpdates.Updates) != 1 {
		t.Fatalf("pending request push message = %T %+v, want one update", pushedPending.message, pushedPending.message)
	}
	pushedPendingUpdate, ok := pushedPendingUpdates.Updates[0].(*tg.UpdatePendingJoinRequests)
	if !ok || pushedPendingUpdate.RequestsPending != 1 || len(pushedPendingUpdate.RecentRequesters) != 1 || pushedPendingUpdate.RecentRequesters[0] != first.ID {
		t.Fatalf("pending request update = %+v, want first requester", pushedPendingUpdates.Updates[0])
	}
	fullAfterPending, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), inputChannel)
	if err != nil {
		t.Fatalf("get full channel after pending request: %v", err)
	}
	fullPending := fullAfterPending.FullChat.(*tg.ChannelFull)
	requestsPending, ok := fullPending.GetRequestsPending()
	recentRequesters, recentOK := fullPending.GetRecentRequesters()
	if !ok || requestsPending != 1 || !recentOK || len(recentRequesters) != 1 || recentRequesters[0] != first.ID {
		t.Fatalf("full pending = count %d ok %v recent %+v ok %v, want first requester", requestsPending, ok, recentRequesters, recentOK)
	}
	pendingReq := &tg.MessagesGetChatInviteImportersRequest{
		Requested: true,
		Peer:      input,
		Limit:     10,
	}
	pendingReq.SetLink(requestInvite.(*tg.ChatInviteExported).Link)
	pending, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, owner.ID), pendingReq)
	if err != nil {
		t.Fatalf("get pending invite importers: %v", err)
	}
	if pending.Count != 1 || len(pending.Importers) != 1 || pending.Importers[0].UserID != first.ID || !pending.Importers[0].Requested {
		t.Fatalf("pending importers = %+v, want first pending request", pending)
	}
	limitedInvite, err := r.onMessagesExportChatInvite(WithUserID(ctx, owner.ID), &tg.MessagesExportChatInviteRequest{
		Peer:       input,
		Title:      "one",
		UsageLimit: 1,
	})
	if err != nil {
		t.Fatalf("export limited invite: %v", err)
	}
	limitedHash := strings.TrimPrefix(limitedInvite.(*tg.ChatInviteExported).Link, "https://telesrv.net/+")
	if _, err := r.onMessagesImportChatInvite(WithUserID(ctx, first.ID), limitedHash); err != nil {
		t.Fatalf("first import limited invite: %v", err)
	}
	pendingAfterJoin, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, owner.ID), pendingReq)
	if err != nil {
		t.Fatalf("get pending invite importers after join: %v", err)
	}
	if pendingAfterJoin.Count != 0 || len(pendingAfterJoin.Importers) != 0 {
		t.Fatalf("pending importers after alternate invite join = %+v, want cleared", pendingAfterJoin)
	}
	if _, err := r.onMessagesImportChatInvite(WithUserID(ctx, second.ID), limitedHash); err == nil || !strings.Contains(err.Error(), "USERS_TOO_MUCH") {
		t.Fatalf("second import limited err = %v, want USERS_TOO_MUCH", err)
	}
	if _, err := r.onMessagesImportChatInvite(WithUserID(ctx, second.ID), requestHash); err == nil || !strings.Contains(err.Error(), "INVITE_REQUEST_SENT") {
		t.Fatalf("second import request-needed err = %v, want INVITE_REQUEST_SENT", err)
	}
	pendingSecond, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, owner.ID), pendingReq)
	if err != nil {
		t.Fatalf("get second pending invite importers: %v", err)
	}
	if pendingSecond.Count != 1 || len(pendingSecond.Importers) != 1 || pendingSecond.Importers[0].UserID != second.ID || !pendingSecond.Importers[0].Requested {
		t.Fatalf("second pending importers = %+v, want second pending request", pendingSecond)
	}
	approved, err := r.onMessagesHideChatJoinRequest(WithUserID(ctx, owner.ID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     input,
		UserID:   &tg.InputUser{UserID: second.ID, AccessHash: second.AccessHash},
	})
	if err != nil {
		t.Fatalf("approve chat join request: %v", err)
	}
	if updates := approved.(*tg.Updates); len(updates.Chats) != 1 || len(updates.Updates) == 0 {
		t.Fatalf("approve join request updates = %+v, want channel updates", updates)
	}
	approvedUpdates := approved.(*tg.Updates)
	var pendingCleared *tg.UpdatePendingJoinRequests
	for _, update := range approvedUpdates.Updates {
		if pending, ok := update.(*tg.UpdatePendingJoinRequests); ok {
			pendingCleared = pending
			break
		}
	}
	if pendingCleared == nil || pendingCleared.RequestsPending != 0 || len(pendingCleared.RecentRequesters) != 0 {
		t.Fatalf("approve pending update = %+v, want cleared pending requests", pendingCleared)
	}
	if _, err := r.onMessagesHideChatJoinRequest(WithUserID(ctx, owner.ID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     input,
		UserID:   &tg.InputUser{UserID: second.ID, AccessHash: second.AccessHash},
	}); err == nil || !strings.Contains(err.Error(), "HIDE_REQUESTER_MISSING") {
		t.Fatalf("approve missing join request err = %v, want HIDE_REQUESTER_MISSING", err)
	}
}
