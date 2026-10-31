package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestChannelsReadHistoryAdvancesForumGeneralReadRPC covers the forum "General topic
// phantom unread" fix: after channel-level channels.readHistory advances the
// channel-level read state, it must also advance the topic-level read watermark of
// General (topic 1) (General messages are the channel root history, covered by the
// channel-level read), otherwise getForumTopics keeps reporting General unread. It
// also verifies the reverse invariant: reading a "non-General sub-topic"
// (readDiscussion) never pollutes General -- zero cross-talk between topics.
func TestChannelsReadHistoryAdvancesForumGeneralReadRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 71, Phone: "15550007301", FirstName: "Owner"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 72, Phone: "15550007302", FirstName: "Member"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Account:  newForumNotifyTestAccount(),
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)

	ownerCtx := WithUserID(ctx, owner.ID)
	memberCtx := WithUserID(ctx, member.ID)

	created, err := r.onChannelsCreateChannel(ownerCtx, &tg.ChannelsCreateChannelRequest{Title: "Forum", Megagroup: true})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	forumPeer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	if _, err := r.onChannelsInviteToChannel(ownerCtx, &tg.ChannelsInviteToChannelRequest{
		Channel: input,
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash}},
	}); err != nil {
		t.Fatalf("invite member: %v", err)
	}
	if _, err := r.onChannelsToggleForum(ownerCtx, &tg.ChannelsToggleForumRequest{Channel: input, Enabled: true, Tabs: true}); err != nil {
		t.Fatalf("toggle forum: %v", err)
	}

	// A non-General sub-topic, used as the cross-talk counterexample.
	createdTopic, err := r.onMessagesCreateForumTopic(ownerCtx, &tg.MessagesCreateForumTopicRequest{
		Peer:      forumPeer,
		Title:     "Side",
		IconColor: domain.DefaultForumTopicIconColor,
		RandomID:  7301001,
	})
	if err != nil {
		t.Fatalf("create forum topic: %v", err)
	}
	sideTopicID := forumTopicRootMessageID(t, createdTopic, "Side")

	sendGeneral := func(text string, randomID int64) int {
		sent, err := r.onMessagesSendMessage(ownerCtx, &tg.MessagesSendMessageRequest{
			Peer:     forumPeer,
			Message:  text,
			RandomID: randomID,
		})
		if err != nil {
			t.Fatalf("send general %q: %v", text, err)
		}
		return forumNewChannelMessageID(t, sent, text)
	}
	sendSide := func(text string, randomID int64) int {
		reply := &tg.InputReplyToMessage{ReplyToMsgID: 0}
		reply.SetTopMsgID(sideTopicID)
		req := &tg.MessagesSendMessageRequest{Peer: forumPeer, Message: text, RandomID: randomID}
		req.SetReplyTo(reply)
		sent, err := r.onMessagesSendMessage(ownerCtx, req)
		if err != nil {
			t.Fatalf("send side %q: %v", text, err)
		}
		return forumNewChannelMessageID(t, sent, text)
	}

	// id order: Side root < g1 < g2 < side1 < g3. The Side message is sandwiched
	// between General messages, the minimal reproduction for pollution like "reading
	// Side with GREATEST wrongly clears General".
	_ = sendGeneral("g1", 7301002)
	_ = sendGeneral("g2", 7301003)
	sideMsg := sendSide("side1", 7301004)
	g3 := sendGeneral("g3", 7301005)

	generalUnread := func() int {
		topics, err := r.onMessagesGetForumTopicsByID(memberCtx, &tg.MessagesGetForumTopicsByIDRequest{
			Peer:   forumPeer,
			Topics: []int{forumGeneralTopicID},
		})
		if err != nil {
			t.Fatalf("member getForumTopicsByID General: %v", err)
		}
		for _, item := range topics.Topics {
			if topic, ok := item.(*tg.ForumTopic); ok && topic.ID == forumGeneralTopicID {
				return topic.UnreadCount
			}
		}
		t.Fatalf("member getForumTopicsByID General missing in %+v", topics.Topics)
		return -1
	}

	before := generalUnread()
	if before == 0 {
		t.Fatalf("General unread before any member read = 0, want owner's General messages counted as unread")
	}

	// Reverse invariant: reading the "Side" sub-topic (readDiscussion) must not
	// pollute General. readDiscussion's internal "conservative overlay" for the
	// channel-level ReadHistory goes only through the service, not the
	// channels.readHistory RPC handler, so it does not backfill General.
	if ok, err := r.onMessagesReadDiscussion(memberCtx, &tg.MessagesReadDiscussionRequest{
		Peer:      forumPeer,
		MsgID:     sideTopicID,
		ReadMaxID: sideMsg,
	}); err != nil || !ok {
		t.Fatalf("member readDiscussion Side = ok %v err %v, want true", ok, err)
	}
	if after := generalUnread(); after != before {
		t.Fatalf("General unread after reading Side topic = %d, want unchanged %d (no cross-topic pollution)", after, before)
	}

	// Fix point: a direct channels.readHistory that reads the channel to the top advances the General topic-level watermark accordingly → unread cleared.
	if ok, err := r.onChannelsReadHistory(memberCtx, &tg.ChannelsReadHistoryRequest{
		Channel: input,
		MaxID:   g3,
	}); err != nil || !ok {
		t.Fatalf("member channels.readHistory = ok %v err %v, want true", ok, err)
	}
	if after := generalUnread(); after != 0 {
		t.Fatalf("General unread after channels.readHistory = %d, want 0 (channel-level read covers General)", after)
	}
}

func forumNewChannelMessageID(t *testing.T, updates tg.UpdatesClass, text string) int {
	t.Helper()
	u, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, update := range u.Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		if msg, ok := newMsg.Message.(*tg.Message); ok && msg.Message == text {
			return msg.ID
		}
	}
	t.Fatalf("updates = %+v, want new channel message %q", u.Updates, text)
	return 0
}

func forumTopicRootMessageID(t *testing.T, updates tg.UpdatesClass, title string) int {
	t.Helper()
	u, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, update := range u.Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		msg, ok := newMsg.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		if action, ok := msg.Action.(*tg.MessageActionTopicCreate); ok && action.Title == title {
			return msg.ID
		}
	}
	t.Fatalf("updates = %+v, want topic create service message %q", u.Updates, title)
	return 0
}
