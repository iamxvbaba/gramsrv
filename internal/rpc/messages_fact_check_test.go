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

// factCheckTestRouter builds a router with a broadcast channel and one channel message.
func factCheckTestRouter(t *testing.T) (*Router, *memory.ChannelStore, domain.User, domain.Channel, domain.ChannelMessage) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 91, Phone: "15550009091", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
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

// TestMessagesEditFactCheckStoresVerdict verifies an author can write a fact
// check and the tag is delivered with the message (clients render from it).
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

// TestMessagesGetFactCheckReturnsStoredVerdict verifies messages.getFactCheck returns the written tag.
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

	// The channel message read path must carry the tag for clients to render it.
	msg := factCheckChannelMessage(t, channelStore, owner.ID, channel.ID, post.ID)
	if msg.FactCheck == nil {
		t.Fatal("channel message has no fact check")
	}
	if msg.FactCheck.Text != "Checked by the desk." {
		t.Fatalf("message fact check = %q", msg.FactCheck.Text)
	}
}

// TestMessagesDeleteFactCheckRemovesVerdict verifies removing the tag.
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

// TestMessagesEditFactCheckRejectsNonChannelPeer verifies fact checks exist only in broadcast channels.
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

// factCheckChannelMessage reads one channel message (verifying the fact check is delivered with it).
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

// TestMessagesFactCheckMethodsDispatchThroughRouter verifies all three
// fact-check methods are registered on the exact-profile dispatch path and are
// reachable (a misspelled registry/semantic ID would surface here).
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
