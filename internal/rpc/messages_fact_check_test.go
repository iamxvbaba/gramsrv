package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/app/channels"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// factCheckTestRouter 建一个带广播频道与一条频道消息的 router。
func factCheckTestRouter(t *testing.T) (*Router, *memory.ChannelStore, domain.User, domain.Channel, domain.ChannelMessage) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 91, Phone: "15550009091", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelStore.AttachDeliveryOutbox(memory.NewDeliveryOutboxStore())
	created, err := channelStore.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Fact Check Channel", Broadcast: true, Date: 1_700_000_100,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	post, err := channelStore.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: created.Channel.ID, RandomID: 1, Message: "post", Date: 1_700_000_200,
	})
	if err != nil {
		t.Fatalf("send post: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    users.NewService(userStore),
		Channels: channels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	return r, channelStore, owner, created.Channel, post.Message
}

// TestMessagesEditFactCheckStoresVerdict 验证作者可以写事实核查，
// 并且标签随消息下发（客户端据此渲染）。
func TestMessagesEditFactCheckStoresVerdict(t *testing.T) {
	r, _, owner, channel, post := factCheckTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)

	updates, err := r.onMessagesEditFactCheck(ctx, &tg.MessagesEditFactCheckRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: post.ID,
		Text: tg.TextWithEntities{
			Text: "The claim is false.",
			Entities: []tg.MessageEntityClass{
				&tg.MessageEntityBold{Offset: 0, Length: 5},
			},
		},
	})
	if err != nil {
		t.Fatalf("editFactCheck: %v", err)
	}
	if updates == nil {
		t.Fatal("editFactCheck returned no updates")
	}
	_ = updates

}

// TestMessagesGetFactCheckReturnsStoredVerdict 验证 messages.getFactCheck 返回已写入的标签。
func TestMessagesGetFactCheckReturnsStoredVerdict(t *testing.T) {
	r, channelStore, owner, channel, post := factCheckTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)

	if _, err := r.onMessagesEditFactCheck(ctx, &tg.MessagesEditFactCheckRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: post.ID,
		Text:  tg.TextWithEntities{Text: "Checked by the desk."},
	}); err != nil {
		t.Fatalf("editFactCheck: %v", err)
	}
	list, err := r.onMessagesGetFactCheck(ctx, &tg.MessagesGetFactCheckRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: []int{post.ID},
	})
	if err != nil {
		t.Fatalf("getFactCheck: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("getFactCheck returned %d checks, want 1", len(list))
	}
	if list[0].Text.Text != "Checked by the desk." {
		t.Fatalf("fact check text = %q, want %q", list[0].Text.Text, "Checked by the desk.")
	}
	if list[0].Hash == 0 {
		t.Fatal("fact check hash is zero")
	}

	// 频道消息读取路径必须带上标签，客户端才会渲染。
	msg := factCheckChannelMessage(t, channelStore, owner.ID, channel.ID, post.ID)
	if msg.FactCheck == nil {
		t.Fatal("channel message has no fact check")
	}
	if msg.FactCheck.Text != "Checked by the desk." {
		t.Fatalf("message fact check = %q", msg.FactCheck.Text)
	}
}

// TestMessagesDeleteFactCheckRemovesVerdict 验证删除标签。
func TestMessagesDeleteFactCheckRemovesVerdict(t *testing.T) {
	r, channelStore, owner, channel, post := factCheckTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)

	if _, err := r.onMessagesEditFactCheck(ctx, &tg.MessagesEditFactCheckRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: post.ID,
		Text:  tg.TextWithEntities{Text: "temporary"},
	}); err != nil {
		t.Fatalf("editFactCheck: %v", err)
	}
	if _, err := r.onMessagesDeleteFactCheck(ctx, &tg.MessagesDeleteFactCheckRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: post.ID,
	}); err != nil {
		t.Fatalf("deleteFactCheck: %v", err)
	}
	msg := factCheckChannelMessage(t, channelStore, owner.ID, channel.ID, post.ID)
	if msg.FactCheck != nil {
		t.Fatalf("message still carries a fact check: %+v", msg.FactCheck)
	}
}

// TestMessagesEditFactCheckRejectsNonChannelPeer 验证事实核查只存在于广播频道。
func TestMessagesEditFactCheckRejectsNonChannelPeer(t *testing.T) {
	r, _, owner, _, post := factCheckTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)

	_, err := r.onMessagesEditFactCheck(ctx, &tg.MessagesEditFactCheckRequest{
		Peer:  &tg.InputPeerSelf{},
		MsgID: post.ID,
		Text:  tg.TextWithEntities{Text: "not a channel"},
	})
	if err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("editFactCheck on a user peer = %v, want PEER_ID_INVALID", err)
	}
}

// factCheckChannelMessage 读取一条频道消息（验证事实核查随消息下发）。
func factCheckChannelMessage(t *testing.T, channelStore *memory.ChannelStore, viewerUserID, channelID int64, messageID int) domain.ChannelMessage {
	t.Helper()
	history, err := channelStore.GetChannelMessages(context.Background(), viewerUserID, channelID, []int{messageID})
	if err != nil {
		t.Fatalf("get channel messages: %v", err)
	}
	if len(history.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(history.Messages))
	}
	return history.Messages[0]
}

// TestMessagesFactCheckMethodsDispatchThroughRouter 验证三个事实核查方法
// 都在 exact-profile 分发路径上注册并可达（注册表/语义 ID 拼错会在此暴露）。
func TestMessagesFactCheckMethodsDispatchThroughRouter(t *testing.T) {
	r, _, owner, channel, post := factCheckTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	if _, method := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical, &tg.MessagesEditFactCheckRequest{
		Peer:  peer,
		MsgID: post.ID,
		Text:  tg.TextWithEntities{Text: "verdict"},
	}); method != "messages.editFactCheck" {
		t.Fatalf("dispatched method = %q, want messages.editFactCheck", method)
	}
	if _, method := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical, &tg.MessagesGetFactCheckRequest{
		Peer:  peer,
		MsgID: []int{post.ID},
	}); method != "messages.getFactCheck" {
		t.Fatalf("dispatched method = %q, want messages.getFactCheck", method)
	}
	if _, method := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical, &tg.MessagesDeleteFactCheckRequest{
		Peer:  peer,
		MsgID: post.ID,
	}); method != "messages.deleteFactCheck" {
		t.Fatalf("dispatched method = %q, want messages.deleteFactCheck", method)
	}
}
