package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestResolveDiscussionReadTargetPostgres locks the one-query readDiscussion
// projection against the real schema. It verifies linked-root mapping and the
// durable idempotent boundary without invoking the full discussion aggregate.
func TestResolveDiscussionReadTargetPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 991,
		Phone:      "+1991" + suffix + "01",
		FirstName:  "DiscussionReadOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	subscriber, err := users.Create(ctx, domain.User{
		AccessHash: 992,
		Phone:      "+1992" + suffix + "02",
		FirstName:  "DiscussionGuest",
	})
	if err != nil {
		t.Fatalf("create subscriber: %v", err)
	}
	channels := NewChannelStore(pool,
		WithChannelRowCache(NewChannelRowCache(32)),
		WithChannelMemberCache(NewChannelMemberCache(64)))
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, subscriber.ID})
	})
	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Discussion Read Source " + suffix,
		Broadcast:     true,
		Date:          1700002900,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	channelIDs = append(channelIDs, broadcast.Channel.ID)
	group, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Discussion Read Group " + suffix,
		Megagroup:     true,
		Date:          1700002901,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	channelIDs = append(channelIDs, group.Channel.ID)
	if _, err := channels.SetDiscussionGroup(ctx, owner.ID, broadcast.Channel.ID, group.Channel.ID); err != nil {
		t.Fatalf("set discussion group: %v", err)
	}
	if _, err := channels.InviteToChannel(ctx, broadcast.Channel.ID, owner.ID, []int64{subscriber.ID}, 1700002902); err != nil {
		t.Fatalf("invite broadcast subscriber: %v", err)
	}
	post, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: broadcast.Channel.ID,
		RandomID:  9912901,
		Message:   "discussion read target",
		Date:      1700002902,
	})
	if err != nil {
		t.Fatalf("send post: %v", err)
	}
	if post.Discussion == nil {
		t.Fatal("send post discussion result is nil")
	}
	rootID := post.Discussion.Message.ID
	guestView, err := channels.GetChannel(ctx, subscriber.ID, group.Channel.ID)
	if err != nil || !guestView.Self.Guest || guestView.Self.Status != domain.ChannelMemberLeft {
		t.Fatalf("linked guest view = %+v err %v", guestView.Self, err)
	}
	guestTarget, err := channels.ResolveDiscussionReadTarget(ctx, subscriber.ID, broadcast.Channel.ID, post.Message.ID, rootID)
	if err != nil || !guestTarget.Guest {
		t.Fatalf("linked guest read target = %+v err %v", guestTarget, err)
	}
	directGuestTarget, err := channels.ResolveDiscussionReadTarget(ctx, subscriber.ID, group.Channel.ID, rootID, rootID)
	if err != nil || !directGuestTarget.Guest || directGuestTarget.ChannelID != group.Channel.ID {
		t.Fatalf("direct linked-group guest read target = %+v err %v", directGuestTarget, err)
	}
	if _, err := channels.ListActiveChannelBotMemberIDs(ctx, subscriber.ID, group.Channel.ID, 20); err != nil {
		t.Fatalf("linked guest bot-delivery preflight: %v", err)
	}
	if _, err := channels.ListActiveChannelBotMembers(ctx, subscriber.ID, group.Channel.ID, 0, 20); err != nil {
		t.Fatalf("linked guest bot participants: %v", err)
	}
	if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: subscriber.ID, ChannelID: group.Channel.ID, RandomID: 9912902,
		Message: "guest comment", Date: 1700002903,
		ReplyTo: &domain.MessageReply{MessageID: rootID, TopMessageID: rootID},
	}); err != nil {
		t.Fatalf("send linked guest comment: %v", err)
	}
	var persistedGuest bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM channel_members WHERE channel_id = $1 AND user_id = $2
)`, group.Channel.ID, subscriber.ID).Scan(&persistedGuest); err != nil {
		t.Fatalf("check guest member row: %v", err)
	}
	if persistedGuest {
		t.Fatal("linked guest send persisted a discussion-group member row")
	}
	target, err := channels.ResolveDiscussionReadTarget(ctx, owner.ID, broadcast.Channel.ID, post.Message.ID, rootID)
	if err != nil {
		t.Fatalf("resolve before read: %v", err)
	}
	if target.ChannelID != group.Channel.ID || target.RootID != rootID || target.AlreadyRead {
		t.Fatalf("target before read = %+v, want linked unread root", target)
	}
	read, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    owner.ID,
		ChannelID: group.Channel.ID,
		MaxID:     rootID,
		Date:      1700002903,
	})
	if err != nil || !read.Changed {
		t.Fatalf("read linked group = %+v err %v, want changed", read, err)
	}
	target, err = channels.ResolveDiscussionReadTarget(ctx, owner.ID, broadcast.Channel.ID, post.Message.ID, rootID)
	if err != nil || !target.AlreadyRead {
		t.Fatalf("resolve after read = %+v err %v, want already read", target, err)
	}
}
