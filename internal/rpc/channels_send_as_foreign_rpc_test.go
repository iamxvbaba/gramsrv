package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// createBroadcastChannelRPC creates a broadcast channel owned by ownerID and returns its tg.Channel.
func createBroadcastChannelRPC(t *testing.T, r *Router, ownerID int64, title string) *tg.Channel {
	t.Helper()
	created, err := r.onChannelsCreateChannel(WithUserID(context.Background(), ownerID), &tg.ChannelsCreateChannelRequest{
		Title:     title,
		About:     "owned channel",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast %q: %v", title, err)
	}
	channel, ok := created.(*tg.Updates).Chats[0].(*tg.Channel)
	if !ok || !channel.Broadcast {
		t.Fatalf("create broadcast %q result = %#v, want broadcast channel", title, created)
	}
	return channel
}

func findSendAsChannel(peers []tg.SendAsPeer, channelID int64) (tg.SendAsPeer, bool) {
	for _, p := range peers {
		if ch, ok := p.Peer.(*tg.PeerChannel); ok && ch.ChannelID == channelID {
			return p, true
		}
	}
	return tg.SendAsPeer{}, false
}

func chatsContainChannel(chats []tg.ChatClass, channelID int64) bool {
	for _, c := range chats {
		switch ch := c.(type) {
		case *tg.Channel:
			if ch.ID == channelID {
				return true
			}
		case *tg.ChannelForbidden:
			if ch.ID == channelID {
				return true
			}
		}
	}
	return false
}

// TestChannelSendAsForeignChannelRPC verifies a premium user can post in another supergroup as one
// of their own broadcast channels: getSendAs lists the owned channel (flagged premium_required), the
// send path stamps from_id to that channel and ships it in Chats, a non-owner is rejected, and the
// default round-trips into channelFull.default_send_as.
func TestChannelSendAsForeignChannelRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	premiumUntil := int(time.Now().Add(72 * time.Hour).Unix())
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550002161", FirstName: "Owner", PremiumUntil: premiumUntil})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 62, Phone: "15550002162", FirstName: "Friend", PremiumUntil: premiumUntil})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	ownerCtx := WithUserID(ctx, owner.ID)
	friendCtx := WithUserID(ctx, friend.ID)

	// Target supergroup G (owner creator, friend member) and an owned broadcast channel C.
	created, err := r.onMessagesCreateChat(ownerCtx, &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Foreign Send As Group",
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	group := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	owned := createBroadcastChannelRPC(t, r, owner.ID, "Owner Channel")

	// getSendAs(G) by owner: self + G + owned channel; owned channel carries premium_required and is in Chats.
	sendAs, err := r.onChannelsGetSendAs(ownerCtx, &tg.ChannelsGetSendAsRequest{Peer: inputPeerChannel(group)})
	if err != nil {
		t.Fatalf("owner get send as: %v", err)
	}
	if _, ok := findSendAsChannel(sendAs.Peers, group.ID); !ok {
		t.Fatalf("send as peers %+v missing current group %d", sendAs.Peers, group.ID)
	}
	ownedPeer, ok := findSendAsChannel(sendAs.Peers, owned.ID)
	if !ok {
		t.Fatalf("send as peers %+v missing owned channel %d", sendAs.Peers, owned.ID)
	}
	if !ownedPeer.PremiumRequired {
		t.Fatalf("owned foreign channel candidate premium_required = false, want true")
	}
	if grpPeer, _ := findSendAsChannel(sendAs.Peers, group.ID); grpPeer.PremiumRequired {
		t.Fatalf("current group candidate premium_required = true, want false")
	}
	if !chatsContainChannel(sendAs.Chats, owned.ID) {
		t.Fatalf("send as chats %+v missing owned channel object %d", sendAs.Chats, owned.ID)
	}

	// Owner (premium) sends in G as the owned channel: from_id projected to the channel, channel in Chats.
	sent, err := r.onMessagesSendMessage(ownerCtx, &tg.MessagesSendMessageRequest{
		Peer:     inputPeerChannel(group),
		Message:  "as my channel",
		RandomID: 6101,
		SendAs:   inputPeerChannel(owned),
	})
	if err != nil {
		t.Fatalf("send as owned foreign channel: %v", err)
	}
	sentUpdates := sent.(*tg.Updates)
	sentMsg := sentUpdates.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := sentMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != owned.ID {
		t.Fatalf("send_as message from = %#v, want owned channel %d", sentMsg.FromID, owned.ID)
	}
	// The sender still sees their own send-as message as outgoing (out derives from SenderUserID, not from_id).
	if !sentMsg.Out {
		t.Fatalf("send_as message out = false for sender, want true")
	}
	if !chatsContainChannel(sentUpdates.Chats, owned.ID) {
		t.Fatalf("send_as echo chats %+v missing owned channel %d", sentUpdates.Chats, owned.ID)
	}

	// A different member who does not own the channel cannot post as it.
	if _, err := r.onMessagesSendMessage(friendCtx, &tg.MessagesSendMessageRequest{
		Peer:     inputPeerChannel(group),
		Message:  "not my channel",
		RandomID: 6102,
		SendAs:   inputPeerChannel(owned),
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("non-owner send_as err = %v, want SEND_AS_PEER_INVALID", err)
	}

	// saveDefaultSendAs(G, ownedChannel) by owner round-trips into channelFull.default_send_as + Chats.
	if ok, err := r.onMessagesSaveDefaultSendAs(ownerCtx, &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   inputPeerChannel(group),
		SendAs: inputPeerChannel(owned),
	}); err != nil || !ok {
		t.Fatalf("save default send as owned channel = ok %v err %v, want true", ok, err)
	}
	full, err := r.onChannelsGetFullChannel(ownerCtx, inputChannel(group))
	if err != nil {
		t.Fatalf("get full channel after default send as: %v", err)
	}
	channelFull := full.FullChat.(*tg.ChannelFull)
	def, ok := channelFull.GetDefaultSendAs()
	if !ok {
		t.Fatalf("channelFull default_send_as missing after saving owned channel")
	}
	if peer, ok := def.(*tg.PeerChannel); !ok || peer.ChannelID != owned.ID {
		t.Fatalf("channelFull default_send_as = %#v, want owned channel %d", def, owned.ID)
	}
	if !chatsContainChannel(full.Chats, owned.ID) {
		t.Fatalf("getFullChannel chats %+v missing owned default send-as channel %d", full.Chats, owned.ID)
	}

	// With the default stored, a send carrying no explicit send_as posts as the owned channel.
	defaultSent, err := r.onMessagesSendMessage(ownerCtx, &tg.MessagesSendMessageRequest{
		Peer:     inputPeerChannel(group),
		Message:  "default channel",
		RandomID: 6103,
	})
	if err != nil {
		t.Fatalf("send with stored default send_as: %v", err)
	}
	defaultMsg := defaultSent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := defaultMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != owned.ID {
		t.Fatalf("stored-default send_as from = %#v, want owned channel %d", defaultMsg.FromID, owned.ID)
	}
}

// TestChannelSendAsForeignChannelPremiumGateRPC verifies that posting as an owned personal channel in
// another supergroup is gated on Premium: a non-premium owner is rejected with SEND_AS_PEER_INVALID,
// and the same owner succeeds once Premium is active.
func TestChannelSendAsForeignChannelPremiumGateRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	// creator of the group is premium so the group exists with a member; the send-as actor is non-premium.
	host, _ := userStore.Create(ctx, domain.User{AccessHash: 71, Phone: "15550002171", FirstName: "Host", PremiumUntil: int(time.Now().Add(72 * time.Hour).Unix())})
	actor, _ := userStore.Create(ctx, domain.User{AccessHash: 72, Phone: "15550002172", FirstName: "Actor"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	hostCtx := WithUserID(ctx, host.ID)
	actorCtx := WithUserID(ctx, actor.ID)

	created, err := r.onMessagesCreateChat(hostCtx, &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: actor.ID, AccessHash: actor.AccessHash}},
		Title: "Premium Gate Group",
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	group := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	actorChannel := createBroadcastChannelRPC(t, r, actor.ID, "Actor Channel")

	// Non-premium actor cannot post as their own channel in the group.
	if _, err := r.onMessagesSendMessage(actorCtx, &tg.MessagesSendMessageRequest{
		Peer:     inputPeerChannel(group),
		Message:  "non-premium send as",
		RandomID: 7201,
		SendAs:   inputPeerChannel(actorChannel),
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("non-premium send_as err = %v, want SEND_AS_PEER_INVALID", err)
	}
	// And cannot persist it as the default.
	if _, err := r.onMessagesSaveDefaultSendAs(actorCtx, &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   inputPeerChannel(group),
		SendAs: inputPeerChannel(actorChannel),
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("non-premium save default send_as err = %v, want SEND_AS_PEER_INVALID", err)
	}

	// Grant Premium and the same send succeeds.
	if _, err := userStore.SetPremiumUntil(ctx, actor.ID, int(time.Now().Add(72*time.Hour).Unix())); err != nil {
		t.Fatalf("grant premium: %v", err)
	}
	sent, err := r.onMessagesSendMessage(actorCtx, &tg.MessagesSendMessageRequest{
		Peer:     inputPeerChannel(group),
		Message:  "premium send as",
		RandomID: 7202,
		SendAs:   inputPeerChannel(actorChannel),
	})
	if err != nil {
		t.Fatalf("premium send_as: %v", err)
	}
	sentMsg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := sentMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != actorChannel.ID {
		t.Fatalf("premium send_as from = %#v, want actor channel %d", sentMsg.FromID, actorChannel.ID)
	}
}
