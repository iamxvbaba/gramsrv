package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	usernamesapp "telesrv/internal/app/usernames"
	"telesrv/internal/domain"
)

var _ usernamesapp.PeerUsernameNotifier = (*Router)(nil)

func TestNotifyPeerUsernamesChangedUserPushesPreloadedVector(t *testing.T) {
	const (
		targetID = int64(2002)
		viewerID = int64(1001)
	)
	users := &verifiedNotifyUsers{
		user:     domain.User{ID: targetID, FirstName: "Owner", Username: "owner_slot"},
		found:    true,
		audience: []int64{targetID, viewerID},
	}
	registry := newFakeUsernameRegistry()
	registry.byPeer[domain.Peer{Type: domain.PeerTypeUser, ID: targetID}] = []domain.Username{
		{Username: "owner_slot", Editable: true, Active: true},
		{Username: "nft", Active: true, CollectibleID: 7},
	}
	sessions := &captureSessions{onlineUserIDs: []int64{targetID, viewerID}}
	r := New(Config{}, Deps{Users: users, Usernames: registry, Sessions: sessions}, zap.NewNop(), clock.System)

	if err := r.NotifyPeerUsernamesChanged(context.Background(), domain.Peer{
		Type: domain.PeerTypeUser, ID: targetID,
	}); err != nil {
		t.Fatalf("notify user usernames: %v", err)
	}
	if registry.peerCalls != 1 || registry.batchCalls != 0 {
		t.Fatalf("registry reads = peer %d / batch %d, want one peer-wide read", registry.peerCalls, registry.batchCalls)
	}
	updates := sessions.lastUserPush().(*tg.Updates)
	user := updates.Users[0].(*tg.User)
	if scalar, ok := user.GetUsername(); ok || scalar != "" {
		t.Fatalf("pushed scalar username = %q (set %v), want absent", scalar, ok)
	}
	vector, ok := user.GetUsernames()
	if !ok || len(vector) != 2 || vector[1].Username != "nft" {
		t.Fatalf("pushed username vector = %+v (set %v)", vector, ok)
	}
}

func TestNotifyPeerUsernamesChangedChannelPushesPreloadedVector(t *testing.T) {
	const (
		channelID = int64(4004)
		ownerID   = int64(3003)
		memberID  = int64(3004)
	)
	channels := &verifiedNotifyChannels{channel: domain.Channel{
		ID: channelID, AccessHash: 44, CreatorUserID: ownerID,
		Title: "Collectible", Username: "channel_slot", Broadcast: true,
	}}
	registry := newFakeUsernameRegistry()
	registry.byPeer[domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}] = []domain.Username{
		{Username: "channel_slot", Editable: true, Active: true},
		{Username: "collectible", Active: true, CollectibleID: 9},
	}
	sessions := &captureSessions{
		onlineUserIDs:  []int64{ownerID, memberID},
		channelMembers: map[int64][]int64{channelID: {ownerID, memberID}},
	}
	r := New(Config{}, Deps{
		Channels: channels, Usernames: registry, Sessions: sessions,
	}, zap.NewNop(), clock.System)

	if err := r.NotifyPeerUsernamesChanged(context.Background(), domain.Peer{
		Type: domain.PeerTypeChannel, ID: channelID,
	}); err != nil {
		t.Fatalf("notify channel usernames: %v", err)
	}
	if registry.peerCalls != 1 || registry.batchCalls != 0 {
		t.Fatalf("registry reads = peer %d / batch %d, want one peer-wide read", registry.peerCalls, registry.batchCalls)
	}
	updates := sessions.lastUserPush().(*tg.Updates)
	channel := updates.Chats[0].(*tg.Channel)
	if scalar, ok := channel.GetUsername(); ok || scalar != "" {
		t.Fatalf("pushed scalar username = %q (set %v), want absent", scalar, ok)
	}
	vector, ok := channel.GetUsernames()
	if !ok || len(vector) != 2 || vector[1].Username != "collectible" {
		t.Fatalf("pushed username vector = %+v (set %v)", vector, ok)
	}
}
