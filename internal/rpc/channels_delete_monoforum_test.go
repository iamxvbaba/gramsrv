package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestChannelsDeleteChannelCascadesMonoforumForbiddenRPC 锁定 Risk A 的 RPC 层契约:删除开启了
// Direct Messages 的母广播频道时,响应与推送都必须为母频道 **和** 其关联 monoforum 各下发一条
// ChannelForbidden 墓碑,且删后 getDialogs 两者都不再返回。否则在线客户端会留着 mono 会话
// (isMonoforum=true 但 link 不可解析)继续渲染崩溃。
func TestChannelsDeleteChannelCascadesMonoforumForbiddenRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550004041", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}

	channelStore := memory.NewChannelStore()
	bc, err := channelStore.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "DM Broadcast", Broadcast: true, Date: 1_700_000_900,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	parentID := bc.Channel.ID
	enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, parentID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}

	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	getDialogChannelIDs := func() map[int64]struct{} {
		t.Helper()
		req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 50}
		var b bin.Buffer
		if err := req.Encode(&b); err != nil {
			t.Fatalf("encode get dialogs: %v", err)
		}
		enc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &b)
		if err != nil {
			t.Fatalf("dispatch get dialogs: %v", err)
		}
		ids := map[int64]struct{}{}
		switch d := enc.(type) {
		case *tg.MessagesDialogs:
			for _, ch := range d.Chats {
				if c, ok := ch.(*tg.Channel); ok {
					ids[c.ID] = struct{}{}
				}
			}
		case *tg.MessagesDialogsSlice:
			for _, ch := range d.Chats {
				if c, ok := ch.(*tg.Channel); ok {
					ids[c.ID] = struct{}{}
				}
			}
		default:
			t.Fatalf("dialogs = %T, want messages.dialogs(Slice)", enc)
		}
		return ids
	}

	before := getDialogChannelIDs()
	if _, ok := before[parentID]; !ok {
		t.Fatalf("before delete: parent %d missing from dialogs %v", parentID, before)
	}
	if _, ok := before[monoID]; !ok {
		t.Fatalf("before delete: monoforum %d missing from dialogs %v", monoID, before)
	}

	deleted, err := r.onChannelsDeleteChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{
		ChannelID:  parentID,
		AccessHash: bc.Channel.AccessHash,
	})
	if err != nil {
		t.Fatalf("delete channel: %v", err)
	}

	forbidden := func(updates *tg.Updates) map[int64]struct{} {
		ids := map[int64]struct{}{}
		for _, ch := range updates.Chats {
			if f, ok := ch.(*tg.ChannelForbidden); ok {
				ids[f.ID] = struct{}{}
			}
		}
		return ids
	}

	respUpdates, ok := deleted.(*tg.Updates)
	if !ok {
		t.Fatalf("delete response = %T, want *tg.Updates", deleted)
	}
	respForbidden := forbidden(respUpdates)
	if _, ok := respForbidden[parentID]; !ok {
		t.Fatalf("delete response missing ChannelForbidden for parent %d: %+v", parentID, respUpdates.Chats)
	}
	if _, ok := respForbidden[monoID]; !ok {
		t.Fatalf("delete response missing ChannelForbidden for monoforum %d: %+v", monoID, respUpdates.Chats)
	}

	// 推送给接收方(含 owner 本人)的更新同样要带 mono 墓碑。
	pushed := sessions.snapshot()
	pushedUpdates, ok := pushed.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", pushed.message)
	}
	pushedForbidden := forbidden(pushedUpdates)
	if _, ok := pushedForbidden[monoID]; !ok {
		t.Fatalf("pushed update missing ChannelForbidden for monoforum %d: %+v", monoID, pushedUpdates.Chats)
	}

	after := getDialogChannelIDs()
	if _, ok := after[parentID]; ok {
		t.Fatalf("after delete: parent %d still in dialogs %v", parentID, after)
	}
	if _, ok := after[monoID]; ok {
		t.Fatalf("after delete: monoforum %d still in dialogs %v", monoID, after)
	}
}
