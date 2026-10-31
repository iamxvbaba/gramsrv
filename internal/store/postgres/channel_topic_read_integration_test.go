package postgres

import (
	"context"
	"telesrv/internal/domain"
	"testing"
)

// TestChannelTopicReadIsolation verifies that forum topic-level reads are not crosstalked by a
// single channel-level watermark: interleaved sending makes all of topicB's message ids smaller
// than topicA's latest message id; after reading topicA up to the latest, the old channel-level
// watermark would misjudge topicB as read, whereas under per-topic watermarks topicB's unread
// stays unchanged. Also verifies the outbox read receipt (S6).
func TestChannelTopicReadIsolation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 81, Phone: "+1778" + suffix + "81", FirstName: "TROwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 82, Phone: "+1778" + suffix + "82", FirstName: "TRMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "TopicRead " + suffix,
		Megagroup:     true,
		Forum:         true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700002100,
	})
	if err != nil {
		t.Fatalf("create forum channel: %v", err)
	}
	channelID = created.Channel.ID

	topicA, err := channels.CreateForumTopic(ctx, domain.CreateChannelForumTopicRequest{UserID: owner.ID, ChannelID: channelID, Title: "A " + suffix, RandomID: 222001, Date: 1700002101})
	if err != nil {
		t.Fatalf("topic A: %v", err)
	}
	topicB, err := channels.CreateForumTopic(ctx, domain.CreateChannelForumTopicRequest{UserID: owner.ID, ChannelID: channelID, Title: "B " + suffix, RandomID: 222002, Date: 1700002102})
	if err != nil {
		t.Fatalf("topic B: %v", err)
	}

	send := func(rid int64, topicID, date int) domain.ChannelMessage {
		t.Helper()
		res, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    owner.ID,
			ChannelID: channelID,
			RandomID:  rid,
			Message:   "m",
			ReplyTo:   &domain.MessageReply{TopMessageID: topicID, ForumTopic: true},
			Date:      date,
		})
		if err != nil {
			t.Fatalf("send to topic %d: %v", topicID, err)
		}
		return res.Message
	}
	// Interleaved sending: both of topicB's messages precede topicA's latest one and have smaller ids.
	_ = send(222003, topicA.Topic.TopicID, 1700002103)        // A1
	_ = send(222004, topicB.Topic.TopicID, 1700002104)        // B1
	_ = send(222005, topicB.Topic.TopicID, 1700002105)        // B2
	a2 := send(222006, topicA.Topic.TopicID, 1700002106)      // A2 (global max id)

	if u := topicUnread(t, channels, ctx, member.ID, channelID, topicA.Topic.TopicID); u != 2 {
		t.Fatalf("before read: topicA unread = %d, want 2", u)
	}
	if u := topicUnread(t, channels, ctx, member.ID, channelID, topicB.Topic.TopicID); u != 2 {
		t.Fatalf("before read: topicB unread = %d, want 2", u)
	}

	res, err := channels.ReadChannelTopicHistory(ctx, domain.ReadChannelTopicHistoryRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		TopicID:   topicA.Topic.TopicID,
		MaxID:     a2.ID,
		Date:      1700002110,
	})
	if err != nil {
		t.Fatalf("read topic A: %v", err)
	}
	if !res.Changed || res.MaxID != a2.ID {
		t.Fatalf("read topic A result = %+v, want changed with maxID %d", res, a2.ID)
	}

	// Key assertion: after reading topicA, topicA unread=0 and topicB unread still=2 (not polluted by the channel-level high-water mark).
	if u := topicUnread(t, channels, ctx, member.ID, channelID, topicA.Topic.TopicID); u != 0 {
		t.Fatalf("after read: topicA unread = %d, want 0", u)
	}
	if u := topicUnread(t, channels, ctx, member.ID, channelID, topicB.Topic.TopicID); u != 2 {
		t.Fatalf("after read: topicB unread = %d, want 2 (cross-topic contamination)", u)
	}

	// Idempotent: reading again at the same watermark → Changed=false.
	again, err := channels.ReadChannelTopicHistory(ctx, domain.ReadChannelTopicHistoryRequest{
		UserID: member.ID, ChannelID: channelID, TopicID: topicA.Topic.TopicID, MaxID: a2.ID, Date: 1700002111,
	})
	if err != nil || again.Changed {
		t.Fatalf("re-read topic A = %+v err %v, want not changed", again, err)
	}

	// Outbox receipt (S6): member reads owner's messages in topicA up to a2 → the receipt contains owner.
	found := false
	for _, o := range res.OutboxUpdates {
		if o.UserID == owner.ID && o.MaxID == a2.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("outbox updates = %+v, want owner %d at %d", res.OutboxUpdates, owner.ID, a2.ID)
	}
}

func topicUnread(t *testing.T, channels *ChannelStore, ctx context.Context, userID, channelID int64, topicID int) int {
	t.Helper()
	list, err := channels.GetForumTopicsByID(ctx, userID, channelID, []int{topicID})
	if err != nil {
		t.Fatalf("get topic %d: %v", topicID, err)
	}
	for _, topic := range list.Topics {
		if topic.TopicID == topicID {
			return topic.UnreadCount
		}
	}
	t.Fatalf("topic %d not found in %+v", topicID, list.Topics)
	return -1
}

// TestChannelGeneralTopicRead verifies independent reads for the General topic (id=1): General's
// unread excludes other topics' root service messages, and ReadChannelTopicHistory(General) only
// advances General's own watermark.
func TestChannelGeneralTopicRead(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 83, Phone: "+1778" + suffix + "83", FirstName: "GenOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 84, Phone: "+1778" + suffix + "84", FirstName: "GenMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "General " + suffix, Megagroup: true, Forum: true,
		MemberUserIDs: []int64{member.ID}, Date: 1700003100,
	})
	if err != nil {
		t.Fatalf("create forum channel: %v", err)
	}
	channelID = created.Channel.ID

	// Create an ordinary topic: its root service message has reply_to_top_id=0 and must be excluded by General's on-the-fly computation.
	if _, err := channels.CreateForumTopic(ctx, domain.CreateChannelForumTopicRequest{
		UserID: owner.ID, ChannelID: channelID, Title: "Topic " + suffix, RandomID: 333009, Date: 1700003101,
	}); err != nil {
		t.Fatalf("create topic: %v", err)
	}

	sendGeneral := func(rid int64, date int) domain.ChannelMessage {
		t.Helper()
		// Following real client behavior, General messages carry no top_msg_id (reply_to_top_id=0) and are merged in by General's on-the-fly computation.
		res, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    owner.ID,
			ChannelID: channelID,
			RandomID:  rid,
			Message:   "g",
			Date:      date,
		})
		if err != nil {
			t.Fatalf("send general: %v", err)
		}
		return res.Message
	}
	// Baseline: General unread after creating the topic and before sending General messages (the topic root is excluded, leaving only the channel-creation system message).
	base, err := channels.GeneralForumTopic(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("general baseline: %v", err)
	}

	_ = sendGeneral(333010, 1700003110)
	g2 := sendGeneral(333011, 1700003111)

	// After the baseline, create a new topic A2 and send one message: A2's root service message and the ordinary message must never count toward General.
	topicA2, err := channels.CreateForumTopic(ctx, domain.CreateChannelForumTopicRequest{
		UserID: owner.ID, ChannelID: channelID, Title: "A2 " + suffix, RandomID: 333012, Date: 1700003112,
	})
	if err != nil {
		t.Fatalf("create topic A2: %v", err)
	}
	a1, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, RandomID: 333013, Message: "a",
		ReplyTo: &domain.MessageReply{TopMessageID: topicA2.Topic.TopicID, ForumTopic: true}, Date: 1700003113,
	})
	if err != nil {
		t.Fatalf("send topic A2 message: %v", err)
	}

	gen, err := channels.GeneralForumTopic(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("general topic: %v", err)
	}
	// Only 2 more General messages; A2's root service message and a1 do not count (otherwise the difference would be 4).
	if gen.UnreadCount != base.UnreadCount+2 {
		t.Fatalf("general unread = %d, want baseline %d + 2 (A2 root+message excluded)", gen.UnreadCount, base.UnreadCount)
	}
	if gen.TopMessageID != g2.ID {
		t.Fatalf("general top message = %d, want %d (A2 root/message excluded)", gen.TopMessageID, g2.ID)
	}

	// Reading topic A2 does not pollute General.
	if _, err := channels.ReadChannelTopicHistory(ctx, domain.ReadChannelTopicHistoryRequest{
		UserID: member.ID, ChannelID: channelID, TopicID: topicA2.Topic.TopicID, MaxID: a1.Message.ID, Date: 1700003120,
	}); err != nil {
		t.Fatalf("read topic A2: %v", err)
	}
	genAfterTopic, err := channels.GeneralForumTopic(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("general after topic read: %v", err)
	}
	if genAfterTopic.UnreadCount != gen.UnreadCount {
		t.Fatalf("reading topic A2 changed general unread %d -> %d (contamination)", gen.UnreadCount, genAfterTopic.UnreadCount)
	}

	// Reading General clears it and advances General's own watermark.
	res, err := channels.ReadChannelTopicHistory(ctx, domain.ReadChannelTopicHistoryRequest{
		UserID: member.ID, ChannelID: channelID, TopicID: domain.ForumGeneralTopicID, MaxID: g2.ID, Date: 1700003121,
	})
	if err != nil || !res.Changed {
		t.Fatalf("read general = %+v err %v, want changed", res, err)
	}
	genAfter, err := channels.GeneralForumTopic(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("general after read: %v", err)
	}
	if genAfter.UnreadCount != 0 || genAfter.ReadInboxMaxID != g2.ID {
		t.Fatalf("general after read = %+v, want unread 0 read_inbox %d", genAfter, g2.ID)
	}
}
