package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelStoreRecentRepliersPostgres regresses populateChannelMessageReplies: previously it
// only computed Replies/MaxID/RepliesPts and never filled RecentRepliers (always nil), so under
// Postgres (production) the channel post comment entry could not get the "recent replier avatars" —
// while the memory store always filled them, an inconsistency between the two stores. This verifies
// Postgres also backfills by "each replier's latest reply id, newest-first, deduped, top 3",
// matching memory.
func TestChannelStoreRecentRepliersPostgres(t *testing.T) {
	pool := testPool(t) // without TELESRV_TEST_POSTGRES_DSN set this will t.Skip
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 61, Phone: "+1779" + suffix + "01", FirstName: "ReplyStatOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	alice, err := users.Create(ctx, domain.User{AccessHash: 62, Phone: "+1779" + suffix + "02", FirstName: "ReplyStatAlice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{AccessHash: 63, Phone: "+1779" + suffix + "03", FirstName: "ReplyStatBob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, alice.ID, bob.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Recent Repliers " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{alice.ID, bob.ID},
		Date:          1700000800,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	root, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, RandomID: 9301, Message: "root post", Date: 1700000801,
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	reply := func(u, rid int64, date int) {
		t.Helper()
		if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: u, ChannelID: channelID, RandomID: rid, Message: "c",
			ReplyTo: &domain.MessageReply{MessageID: root.Message.ID}, Date: date,
		}); err != nil {
			t.Fatalf("reply from %d: %v", u, err)
		}
	}
	reply(alice.ID, 9302, 1700000802) // alice oldest
	reply(bob.ID, 9303, 1700000803)   // bob in the middle
	reply(alice.ID, 9304, 1700000804) // alice newest

	got, err := channels.GetChannelMessages(ctx, owner.ID, channelID, []int{root.Message.ID})
	if err != nil {
		t.Fatalf("get channel messages: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("get messages = %d, want 1", len(got.Messages))
	}
	replies := got.Messages[0].Replies
	if replies == nil {
		t.Fatalf("root.Replies == nil, want reply stats")
	}
	if replies.Replies != 3 {
		t.Fatalf("replies count = %d, want 3", replies.Replies)
	}
	// Dedup + newest-first: alice (latest 9304) first, bob (9303) next; alice's older entry
	// (9302) is deduped because alice already appeared, leaving only two distinct repliers.
	want := []domain.Peer{
		{Type: domain.PeerTypeUser, ID: alice.ID},
		{Type: domain.PeerTypeUser, ID: bob.ID},
	}
	if len(replies.RecentRepliers) != len(want) {
		t.Fatalf("recent repliers = %+v, want %+v", replies.RecentRepliers, want)
	}
	for i := range want {
		if replies.RecentRepliers[i] != want[i] {
			t.Fatalf("recent repliers[%d] = %+v, want %+v (full=%+v)", i, replies.RecentRepliers[i], want[i], replies.RecentRepliers)
		}
	}
}
