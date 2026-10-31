package postgres

import (
	"context"
	"errors"
	"testing"

	appchannels "telesrv/internal/app/channels"
	"telesrv/internal/domain"
)

// TestChannelFactCheckSurvivesReadPaths verifies the Layer 231 fact-check label
// lives in the channel_messages.factcheck column, and that:
//   - the author can write the verdict, an ordinary non-author cannot;
//   - the read path (GetChannelMessages) carries the label back;
//   - batch reading (GetFactChecks) returns the same verdict;
//   - the label disappears after deletion.
func TestChannelFactCheckSurvivesReadPaths(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 91, Phone: "+1997" + suffix + "01", FirstName: "FactOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 92, Phone: "+1997" + suffix + "02", FirstName: "FactMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	ids := []int64{owner.ID, member.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM channel_update_events WHERE channel_id = ANY($1::bigint[])", []int64{})
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Fact Check " + suffix, Broadcast: true, Date: 1_700_100_000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM channel_update_events WHERE channel_id = $1", channelID)
		_, _ = pool.Exec(ctx, "DELETE FROM channel_messages WHERE channel_id = $1", channelID)
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
	})

	post, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, RandomID: 1, Message: "post", Date: 1_700_100_100,
	})
	if err != nil {
		t.Fatalf("send post: %v", err)
	}
	if _, err := channels.JoinChannel(ctx, channelID, member.ID, 1_700_100_200); err != nil {
		t.Fatalf("join member: %v", err)
	}

	// An ordinary non-author member cannot write a fact check.
	if _, err := channels.EditFactCheck(ctx, domain.EditFactCheckRequest{
		UserID: member.ID, ChannelID: channelID, MessageID: post.Message.ID, Text: "not allowed", Date: 1_700_100_300,
	}); !errors.Is(err, domain.ErrFactCheckForbidden) {
		t.Fatalf("member editFactCheck error = %v, want ErrFactCheckForbidden", err)
	}

	// The author writes the verdict.
	res, err := channels.EditFactCheck(ctx, domain.EditFactCheckRequest{
		UserID: owner.ID, ChannelID: channelID, MessageID: post.Message.ID,
		Text: "The claim is false.", Country: "DE", Date: 1_700_100_400,
	})
	if err != nil {
		t.Fatalf("editFactCheck: %v", err)
	}
	if res.FactCheck == nil || res.FactCheck.Text != "The claim is false." {
		t.Fatalf("editFactCheck result = %+v", res.FactCheck)
	}
	if res.FactCheck.Hash == 0 {
		t.Fatal("fact check hash is zero")
	}

	// The read path carries the label back.
	history, err := channels.GetChannelMessages(ctx, owner.ID, channelID, []int{post.Message.ID})
	if err != nil {
		t.Fatalf("get channel messages: %v", err)
	}
	if len(history.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(history.Messages))
	}
	got := history.Messages[0].FactCheck
	if got == nil {
		t.Fatal("channel message has no fact check")
	}
	if got.Text != "The claim is false." || got.Country != "DE" {
		t.Fatalf("fact check = %+v", got)
	}
	if got.Hash != res.FactCheck.Hash {
		t.Fatalf("fact check hash = %d, want %d", got.Hash, res.FactCheck.Hash)
	}

	// Batch reading returns the same verdict.
	list, err := appchannels.NewService(channels).GetFactChecks(ctx, owner.ID, channelID, []int{post.Message.ID})
	if err != nil {
		t.Fatalf("getFactChecks: %v", err)
	}
	if len(list.FactChecks) != 1 || list.FactChecks[0].Text != "The claim is false." {
		t.Fatalf("getFactChecks = %+v", list.FactChecks)
	}
	service := appchannels.NewService(channels)
	ordered, err := service.GetFactChecks(ctx, member.ID, channelID, []int{999999, post.Message.ID, post.Message.ID})
	if err != nil || len(ordered.FactChecks) != 3 || ordered.FactChecks[0].Hash != 0 || ordered.FactChecks[1].Hash != got.Hash || ordered.FactChecks[2].Hash != got.Hash {
		t.Fatalf("ordered/duplicate batch = %+v, %v", ordered, err)
	}
	if _, err := channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{UserID: member.ID, ChannelID: channelID, MaxID: post.Message.ID, Date: 1_700_100_450}); err != nil {
		t.Fatal(err)
	}
	cleared, err := service.GetFactChecks(ctx, member.ID, channelID, []int{post.Message.ID})
	if err != nil || len(cleared.FactChecks) != 1 || cleared.FactChecks[0].Hash != 0 {
		t.Fatalf("cleared history verdict = %+v, %v", cleared, err)
	}
	ownerRead, err := service.GetFactChecks(ctx, owner.ID, channelID, []int{post.Message.ID})
	if err != nil || ownerRead.FactChecks[0].Hash == 0 {
		t.Fatalf("other viewer lost verdict: %+v, %v", ownerRead, err)
	}

	// The label disappears after deletion.
	if _, err := channels.EditFactCheck(ctx, domain.EditFactCheckRequest{
		UserID: owner.ID, ChannelID: channelID, MessageID: post.Message.ID, Text: "", Date: 1_700_100_500,
	}); err != nil {
		t.Fatalf("deleteFactCheck: %v", err)
	}
	history, err = channels.GetChannelMessages(ctx, owner.ID, channelID, []int{post.Message.ID})
	if err != nil {
		t.Fatalf("get channel messages after delete: %v", err)
	}
	if history.Messages[0].FactCheck != nil {
		t.Fatalf("message still carries a fact check: %+v", history.Messages[0].FactCheck)
	}
	// A fresh store reads both committed edits from the durable channel stream.
	difference, err := NewChannelStore(pool).ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: owner.ID, ChannelID: channelID, Pts: res.Event.Pts - 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var editChecks []*domain.FactCheck
	for _, event := range difference.Events {
		if event.Type == domain.ChannelUpdateEditMessage {
			editChecks = append(editChecks, event.Message.FactCheck)
		}
	}
	if len(editChecks) != 2 || editChecks[0] == nil || editChecks[1] != nil {
		t.Fatalf("durable edit/delete snapshots = %+v", editChecks)
	}
	if _, err := channels.EditFactCheck(ctx, domain.EditFactCheckRequest{UserID: owner.ID, ChannelID: channelID, MessageID: post.Message.ID, Text: "must disappear with the message"}); err != nil {
		t.Fatal(err)
	}
	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{UserID: owner.ID, ChannelID: channelID, IDs: []int{post.Message.ID}, Date: 1_700_100_600}); err != nil {
		t.Fatal(err)
	}
	deleted, err := service.GetFactChecks(ctx, owner.ID, channelID, []int{post.Message.ID})
	if err != nil || len(deleted.FactChecks) != 1 || deleted.FactChecks[0].Hash != 0 {
		t.Fatalf("deleted message verdict = %+v, %v", deleted, err)
	}
}
