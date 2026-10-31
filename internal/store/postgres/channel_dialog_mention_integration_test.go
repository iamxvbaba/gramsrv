package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelDialogTopMessageCarriesMentionFlags reproduces the @ badge returning after restart:
// getDialogs' channel top message must carry mentioned/media_unread per viewer,
// otherwise the incomplete version TDesktop caches first never triggers contents-read, the server's unread
// mention stays stuck, and every cold start brings the @ badge back via unread_mentions_count.
func TestChannelDialogTopMessageCarriesMentionFlags(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 97, Phone: "+1675" + suffix + "01", FirstName: "MentionOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 98, Phone: "+1675" + suffix + "02", FirstName: "MentionMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool, WithChannelTopMessageCache(NewChannelTopMessageCache(32)))
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "MentionDialog " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000910,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:         owner.ID,
		ChannelID:      created.Channel.ID,
		RandomID:       851001,
		Message:        "ping member",
		MentionUserIDs: []int64{member.ID},
		Date:           1700000911,
	})
	if err != nil {
		t.Fatalf("send mention: %v", err)
	}

	assertFlags := func(name string, list domain.ChannelDialogList) {
		t.Helper()
		found := false
		for _, msg := range list.Messages {
			if msg.ChannelID != created.Channel.ID || msg.ID != sent.Message.ID {
				continue
			}
			found = true
			if !msg.Mentioned || !msg.MediaUnread {
				t.Fatalf("%s top message flags = mentioned %v media_unread %v, want both true (missing them makes the @ badge return after restart)", name, msg.Mentioned, msg.MediaUnread)
			}
		}
		if !found {
			t.Fatalf("%s lacks top message %d: %+v", name, sent.Message.ID, list.Messages)
		}
	}

	list, err := channels.ListChannelDialogs(ctx, member.ID, domain.DialogFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list channel dialogs: %v", err)
	}
	assertFlags("ListChannelDialogs", list)

	got, err := channels.GetChannelDialogs(ctx, member.ID, []int64{created.Channel.ID})
	if err != nil {
		t.Fatalf("get channel dialogs: %v", err)
	}
	assertFlags("GetChannelDialogs", got)

	// After the mention is read: mentioned is permanently kept, media_unread flips to false.
	if _, err := channels.ReadChannelMentions(ctx, domain.ReadChannelMentionsRequest{
		UserID:    member.ID,
		ChannelID: created.Channel.ID,
	}); err != nil {
		t.Fatalf("read mentions: %v", err)
	}
	after, err := channels.GetChannelDialogs(ctx, member.ID, []int64{created.Channel.ID})
	if err != nil {
		t.Fatalf("get channel dialogs after read: %v", err)
	}
	for _, msg := range after.Messages {
		if msg.ID == sent.Message.ID && (!msg.Mentioned || msg.MediaUnread) {
			t.Fatalf("after read flags = mentioned %v media_unread %v, want mentioned kept with media_unread cleared", msg.Mentioned, msg.MediaUnread)
		}
	}
}
