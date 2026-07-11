package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestLinkedDiscussionGuestAuthorizationFailsClosedMemory(t *testing.T) {
	ctx := context.Background()
	channels := NewChannelStore()
	const (
		ownerID    int64 = 9101
		outsiderID int64 = 9102
	)
	privateGroup, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: ownerID,
		Title:         "unlinked private group",
		Megagroup:     true,
		Date:          1700009100,
	})
	if err != nil {
		t.Fatalf("create private group: %v", err)
	}
	if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    outsiderID,
		ChannelID: privateGroup.Channel.ID,
		RandomID:  91001,
		Message:   "must be rejected",
		Date:      1700009101,
	}); !errors.Is(err, domain.ErrChannelPrivate) {
		t.Fatalf("unlinked outsider send err = %v, want ErrChannelPrivate", err)
	}
	if _, err := channels.ResolveDiscussionReadTarget(ctx, outsiderID, privateGroup.Channel.ID, privateGroup.Message.ID, privateGroup.Message.ID); !errors.Is(err, domain.ErrChannelPrivate) {
		t.Fatalf("unlinked outsider discussion read err = %v, want ErrChannelPrivate", err)
	}
	if got := len(channels.messages[privateGroup.Channel.ID]); got != 1 {
		t.Fatalf("unlinked outsider changed history length = %d, want create message only", got)
	}
}

func TestLinkedDiscussionGuestRepliesKeepViewerProjectionMemory(t *testing.T) {
	ctx := context.Background()
	channels := NewChannelStore()
	const (
		ownerID      int64 = 9201
		subscriberID int64 = 9202
	)
	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: ownerID,
		Title:         "linked source",
		Broadcast:     true,
		Date:          1700009200,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	group, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: ownerID,
		Title:         "linked discussion",
		Megagroup:     true,
		Date:          1700009201,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if _, err := channels.SetDiscussionGroup(ctx, ownerID, broadcast.Channel.ID, group.Channel.ID); err != nil {
		t.Fatalf("set discussion group: %v", err)
	}
	if _, err := channels.InviteToChannel(ctx, broadcast.Channel.ID, ownerID, []int64{subscriberID}, 1700009202); err != nil {
		t.Fatalf("invite broadcast subscriber: %v", err)
	}
	post, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: ownerID, ChannelID: broadcast.Channel.ID, RandomID: 92001,
		Message: "post", Date: 1700009203,
	})
	if err != nil || post.Discussion == nil {
		t.Fatalf("send linked post = %+v err %v", post, err)
	}

	assertGuest := func(name string, history domain.ChannelHistory, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if history.Channel.ID != group.Channel.ID || history.Self.ChannelID != group.Channel.ID || history.Self.UserID != subscriberID || !history.Self.Guest || history.Self.Status != domain.ChannelMemberLeft {
			t.Fatalf("%s = channel %+v self %+v, want linked left guest", name, history.Channel, history.Self)
		}
	}
	direct, err := channels.ListChannelReplies(ctx, subscriberID, domain.ChannelRepliesFilter{
		ChannelID: group.Channel.ID, RootMessageID: post.Discussion.Message.ID, Limit: 20,
	})
	assertGuest("direct replies", direct, err)
	viaBroadcast, err := channels.ListChannelReplies(ctx, subscriberID, domain.ChannelRepliesFilter{
		ChannelID: broadcast.Channel.ID, RootMessageID: post.Message.ID, Limit: 20,
	})
	assertGuest("broadcast replies", viaBroadcast, err)

	if _, err := channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{
		UserID: ownerID, ChannelID: group.Channel.ID,
		Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: subscriberID},
		BannedRights: domain.ChannelBannedRights{ViewMessages: true, UntilDate: 2147483647},
		Date:         1700009204,
	}); err != nil {
		t.Fatalf("ban linked subscriber from target: %v", err)
	}
	if _, err := channels.ListChannelReplies(ctx, subscriberID, domain.ChannelRepliesFilter{
		ChannelID: group.Channel.ID, RootMessageID: post.Discussion.Message.ID, Limit: 20,
	}); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("target-banned direct replies err = %v, want ErrChannelUserBanned", err)
	}
	if _, err := channels.ListChannelReplies(ctx, subscriberID, domain.ChannelRepliesFilter{
		ChannelID: broadcast.Channel.ID, RootMessageID: post.Message.ID, Limit: 20,
	}); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("target-banned broadcast replies err = %v, want ErrChannelUserBanned", err)
	}
}
