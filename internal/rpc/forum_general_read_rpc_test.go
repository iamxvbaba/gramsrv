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

// TestChannelsReadHistoryAdvancesForumGeneralReadRPC 覆盖 forum「General 话题幽灵未读」修复：
// 频道级 channels.readHistory 推进频道级已读后，必须把 General(topic 1) 的话题级已读水位也推进
// （General 消息即频道根历史，被频道级已读覆盖），否则 getForumTopics 会一直报 General 未读。
// 同时验证反向不变量：读「非 General 子话题」(readDiscussion) 绝不污染 General —— 零话题间 cross-talk。
func TestChannelsReadHistoryAdvancesForumGeneralReadRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 71, Phone: "15550007301", FirstName: "Owner"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 72, Phone: "15550007302", FirstName: "Member"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
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

	// 一个非 General 子话题，用于 cross-talk 反例。
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

	// id 序：Side 根 < g1 < g2 < side1 < g3。Side 消息夹在 General 消息中间，
	// 是「读 Side 用 GREATEST 误清 General」这类污染的最小复现条件。
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

	// 反向不变量：读「Side」子话题（readDiscussion）不得污染 General。readDiscussion 内部对频道级
	// ReadHistory 的「保守叠加」只经 service，不触达 channels.readHistory 的 RPC handler，故不回灌 General。
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

	// 修复点：直接的 channels.readHistory 把频道级读到顶后，General 话题级水位随之推进 → 未读清零。
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
