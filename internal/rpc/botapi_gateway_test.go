package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/clock"
	"go.uber.org/zap/zaptest"

	appbots "telesrv/internal/app/bots"
	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestBotAPISendMessageToSupergroupChatID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1001, Phone: "15550008001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	bot, err := userStore.Create(ctx, domain.User{AccessHash: 2001, Phone: "15550008002", FirstName: "TetrisBot", Username: "TetrisBot", Bot: true})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "Group1",
		MemberUserIDs: []int64{bot.ID},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("create megagroup: %v", err)
	}
	sessions := &captureSessions{
		channelMembers: map[int64][]int64{created.Channel.ID: {owner.ID}},
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	chatID := -botAPIChannelChatIDBase - created.Channel.ID
	msg, err := r.BotAPISendMessage(ctx, bot.ID, chatID, "hello Group1 from bot api", nil, nil, false, false, 0)
	if err != nil {
		t.Fatalf("BotAPISendMessage: %v", err)
	}
	if msg.Peer.Type != domain.PeerTypeChannel || msg.Peer.ID != created.Channel.ID {
		t.Fatalf("msg peer = %+v, want channel %d", msg.Peer, created.Channel.ID)
	}
	if msg.From.Type != domain.PeerTypeUser || msg.From.ID != bot.ID || !msg.Out {
		t.Fatalf("msg from/out = %+v out=%v, want bot outbound", msg.From, msg.Out)
	}
	if msg.Body != "hello Group1 from bot api" || msg.ID == 0 || msg.Pts == 0 {
		t.Fatalf("msg = %+v, want durable channel message with id and pts", msg)
	}

	history, err := channelService.GetHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history.Messages) < 1 || history.Messages[0].SenderUserID != bot.ID || history.Messages[0].Body != msg.Body {
		t.Fatalf("history messages = %+v, want bot channel message", history.Messages)
	}
	if pushed := sessions.pushedUserIDs(); !fanoutHasID(pushed, owner.ID) {
		t.Fatalf("fanout pushed = %v, want owner %d to receive online channel update", pushed, owner.ID)
	}
}

func TestBotAPISendMessageRejectsUnsupportedNegativeChatID(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)

	_, err := r.BotAPISendMessage(context.Background(), 1234, -42, "hello", nil, nil, false, false, 0)
	if err == nil || !strings.Contains(err.Error(), "CHAT_ID_INVALID") {
		t.Fatalf("BotAPISendMessage err = %v, want CHAT_ID_INVALID", err)
	}
}

func TestBotAPISendMessageMissingSupergroupReturnsChatIDInvalid(t *testing.T) {
	channelService := appchannels.NewService(memory.NewChannelStore())
	r := New(Config{}, Deps{Channels: channelService}, zaptest.NewLogger(t), clock.System)

	chatID := -botAPIChannelChatIDBase - 9999
	_, err := r.BotAPISendMessage(context.Background(), 1234, chatID, "hello", nil, nil, false, false, 0)
	if err == nil || !strings.Contains(err.Error(), "CHAT_ID_INVALID") {
		t.Fatalf("BotAPISendMessage err = %v, want CHAT_ID_INVALID", err)
	}
}

func TestBotAPIGetUpdatesReceivesVisibleSupergroupMessage(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	res, err := fixture.channels.SendMessage(fixture.ctx, fixture.owner.ID, domain.SendChannelMessageRequest{
		UserID:              fixture.owner.ID,
		ChannelID:           fixture.channel.ID,
		RandomID:            1001,
		Message:             "/ping from group",
		SkipRecipientLookup: true,
		Date:                100,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	fixture.router.enqueueChannelMessageFanout(fixture.ctx, fixture.owner.ID, res, nil)

	events, err := fixture.router.BotAPIUpdates(fixture.ctx, fixture.bot.ID, 0)
	if err != nil {
		t.Fatalf("BotAPIUpdates: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one bot api update", events)
	}
	event := events[0]
	if event.Type != domain.UpdateEventNewMessage || event.Pts <= 0 {
		t.Fatalf("event = %+v, want new_message with update_id", event)
	}
	if event.Message.Peer.Type != domain.PeerTypeChannel || event.Message.Peer.ID != fixture.channel.ID {
		t.Fatalf("message peer = %+v, want channel %d", event.Message.Peer, fixture.channel.ID)
	}
	if event.Message.From.Type != domain.PeerTypeUser || event.Message.From.ID != fixture.owner.ID || event.Message.Body != "/ping from group" || event.Message.Out {
		t.Fatalf("message = %+v, want incoming owner command", event.Message)
	}

	next, err := fixture.router.BotAPIUpdates(fixture.ctx, fixture.bot.ID, int64(event.Pts)+1)
	if err != nil {
		t.Fatalf("BotAPIUpdates confirm: %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("next events = %+v, want empty after offset confirm", next)
	}
}

func TestBotAPIGetUpdatesSkipsHiddenPrivacySupergroupMessage(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	res, err := fixture.channels.SendMessage(fixture.ctx, fixture.owner.ID, domain.SendChannelMessageRequest{
		UserID:              fixture.owner.ID,
		ChannelID:           fixture.channel.ID,
		RandomID:            1002,
		Message:             "plain group chatter",
		SkipRecipientLookup: true,
		Date:                101,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(res.SkipDeliveryUserIDs) == 0 {
		t.Fatalf("SkipDeliveryUserIDs empty, want privacy bot excluded")
	}
	fixture.router.enqueueChannelMessageFanout(fixture.ctx, fixture.owner.ID, res, nil)

	events, err := fixture.router.BotAPIUpdates(fixture.ctx, fixture.bot.ID, 0)
	if err != nil {
		t.Fatalf("BotAPIUpdates: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want hidden privacy message excluded", events)
	}
}

func TestBotAPIGetUpdatesReceivesPrivateBotMessage(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	res, err := fixture.messages.SendPrivateText(fixture.ctx, fixture.owner.ID, domain.SendPrivateTextRequest{
		SenderUserID:    fixture.owner.ID,
		RecipientUserID: fixture.bot.ID,
		RandomID:        2001,
		Message:         "private hello",
		Date:            102,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	fixture.router.enqueueBotAPIPrivateMessageUpdate(fixture.ctx, res)

	events, err := fixture.router.BotAPIUpdates(fixture.ctx, fixture.bot.ID, 0)
	if err != nil {
		t.Fatalf("BotAPIUpdates: %v", err)
	}
	if len(events) != 1 || events[0].Message.Peer.Type != domain.PeerTypeUser || events[0].Message.Peer.ID != fixture.owner.ID || events[0].Message.Body != "private hello" {
		t.Fatalf("events = %+v, want private incoming message", events)
	}
}

func TestBotAPIGetUpdatesBatchesPrivateMessageProjection(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	counting := &countingBotAPIMessagesService{Service: fixture.messages}
	fixture.router.deps.Messages = counting
	for i, text := range []string{"private one", "private two"} {
		res, err := fixture.messages.SendPrivateText(fixture.ctx, fixture.owner.ID, domain.SendPrivateTextRequest{
			SenderUserID:    fixture.owner.ID,
			RecipientUserID: fixture.bot.ID,
			RandomID:        int64(2100 + i),
			Message:         text,
			Date:            120 + i,
		})
		if err != nil {
			t.Fatalf("SendPrivateText %d: %v", i, err)
		}
		fixture.router.enqueueBotAPIPrivateMessageUpdate(fixture.ctx, res)
	}

	events, err := fixture.router.BotAPIUpdates(fixture.ctx, fixture.bot.ID, 0)
	if err != nil {
		t.Fatalf("BotAPIUpdates: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want two private updates", events)
	}
	if counting.getMessagesCalls != 1 {
		t.Fatalf("private GetMessages calls = %d, want 1 batched projection", counting.getMessagesCalls)
	}
}

func TestBotAPIUpdateWaiterWakesOnPrivateEnqueue(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	version := fixture.router.BotAPIUpdateWaitVersion(fixture.bot.ID)
	woke := make(chan bool, 1)
	go func() {
		woke <- fixture.router.WaitBotAPIUpdate(fixture.ctx, fixture.bot.ID, version, time.Second)
	}()
	res, err := fixture.messages.SendPrivateText(fixture.ctx, fixture.owner.ID, domain.SendPrivateTextRequest{
		SenderUserID:    fixture.owner.ID,
		RecipientUserID: fixture.bot.ID,
		RandomID:        2201,
		Message:         "wake bot api polling",
		Date:            121,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	fixture.router.enqueueBotAPIPrivateMessageUpdate(fixture.ctx, res)
	select {
	case ok := <-woke:
		if !ok {
			t.Fatalf("WaitBotAPIUpdate returned false, want notify wake")
		}
	case <-time.After(time.Second):
		t.Fatal("WaitBotAPIUpdate did not wake after enqueue")
	}
}

func TestBotAPIChannelBatchEnqueueLoadsBotCandidatesOnce(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	counting := &countingBotCandidateChannelsService{Service: fixture.channels}
	fixture.router.deps.Channels = counting
	first, err := fixture.channels.SendMessage(fixture.ctx, fixture.owner.ID, domain.SendChannelMessageRequest{
		UserID:              fixture.owner.ID,
		ChannelID:           fixture.channel.ID,
		RandomID:            3001,
		Message:             "/first batch command",
		SkipRecipientLookup: true,
		Date:                103,
	})
	if err != nil {
		t.Fatalf("SendMessage first: %v", err)
	}
	second, err := fixture.channels.SendMessage(fixture.ctx, fixture.owner.ID, domain.SendChannelMessageRequest{
		UserID:              fixture.owner.ID,
		ChannelID:           fixture.channel.ID,
		RandomID:            3002,
		Message:             "/second batch command",
		SkipRecipientLookup: true,
		Date:                104,
	})
	if err != nil {
		t.Fatalf("SendMessage second: %v", err)
	}

	fixture.router.enqueueBotAPIChannelMessagesUpdate(fixture.ctx, fixture.owner.ID, []domain.SendChannelMessageResult{first, second})
	if counting.activeBotMemberIDsCalls != 1 {
		t.Fatalf("ActiveBotMemberIDs calls = %d, want 1 for same-channel batch", counting.activeBotMemberIDsCalls)
	}
	if counting.activeMemberIDsCalls != 0 {
		t.Fatalf("ActiveMemberIDs calls = %d, want 0 on Bot API enqueue path", counting.activeMemberIDsCalls)
	}
	if counting.getMessagesCalls != 0 {
		t.Fatalf("channel GetMessages calls during enqueue = %d, want 0 on ordinary send path", counting.getMessagesCalls)
	}
	events, err := fixture.router.BotAPIUpdates(fixture.ctx, fixture.bot.ID, 0)
	if err != nil {
		t.Fatalf("BotAPIUpdates: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want two bot api updates", events)
	}
	if counting.getMessagesCalls != 1 {
		t.Fatalf("channel GetMessages calls after getUpdates = %d, want 1 batched projection", counting.getMessagesCalls)
	}
}

type countingBotCandidateChannelsService struct {
	*appchannels.Service
	activeBotMemberIDsCalls int
	activeMemberIDsCalls    int
	getMessagesCalls        int
}

func (s *countingBotCandidateChannelsService) ActiveBotMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error) {
	s.activeBotMemberIDsCalls++
	return s.Service.ActiveBotMemberIDs(ctx, viewerUserID, channelID, limit)
}

func (s *countingBotCandidateChannelsService) ActiveMemberIDs(ctx context.Context, userID, channelID int64, limit int) ([]int64, error) {
	s.activeMemberIDsCalls++
	return s.Service.ActiveMemberIDs(ctx, userID, channelID, limit)
}

func (s *countingBotCandidateChannelsService) GetMessages(ctx context.Context, userID, channelID int64, ids []int) (domain.ChannelHistory, error) {
	s.getMessagesCalls++
	return s.Service.GetMessages(ctx, userID, channelID, ids)
}

type countingBotAPIMessagesService struct {
	*appmessages.Service
	getMessagesCalls int
}

func (s *countingBotAPIMessagesService) GetMessages(ctx context.Context, userID int64, ids []int) (domain.MessageList, error) {
	s.getMessagesCalls++
	return s.Service.GetMessages(ctx, userID, ids)
}

type botAPIReceiveFixture struct {
	ctx      context.Context
	owner    domain.User
	bot      domain.User
	channel  domain.Channel
	router   *Router
	channels *appchannels.Service
	messages *appmessages.Service
}

func newBotAPIReceiveFixture(t *testing.T, botChatHistory bool) botAPIReceiveFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1001, Phone: "15550008101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	botStore := memory.NewBotStore(userStore)
	bot, _, err := botStore.CreateBotAccount(ctx, domain.User{AccessHash: 2001, FirstName: "TetrisBot", Username: "TetrisBot"}, domain.BotProfile{
		OwnerUserID: owner.ID,
		TokenSecret: "secret",
		ChatHistory: botChatHistory,
	})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	botsService := appbots.NewService(userStore, botStore, messageStore)
	channelStore := memory.NewChannelStore()
	channelsService := appchannels.NewService(channelStore, appchannels.WithBotProfileResolver(botsService))
	created, err := channelsService.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "Group1",
		MemberUserIDs: []int64{bot.ID},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("create megagroup: %v", err)
	}
	messagesService := appmessages.NewService(messageStore, dialogStore)
	router := New(Config{}, Deps{
		Users:         appusers.NewService(userStore),
		Messages:      messagesService,
		Channels:      channelsService,
		Bots:          botsService,
		BotAPIUpdates: memory.NewBotAPIUpdateStore(),
		Sessions:      &captureSessions{channelMembers: map[int64][]int64{created.Channel.ID: {owner.ID, bot.ID}}},
	}, zaptest.NewLogger(t), clock.System)
	return botAPIReceiveFixture{
		ctx:      ctx,
		owner:    owner,
		bot:      bot,
		channel:  created.Channel,
		router:   router,
		channels: channelsService,
		messages: messagesService,
	}
}
