package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestGroupBotRPCShape(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(users, botStore, messages)
	channelStore := memory.NewChannelStore()
	baseChannels := appchannels.NewService(channelStore, appchannels.WithBotProfileResolver(bots))
	channels := &countingBotParticipantsChannelsService{Service: baseChannels}

	owner, err := users.Create(ctx, domain.User{AccessHash: 6201, Phone: "15550006201", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 6202, Phone: "15550006202", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	bot, _, err := bots.CreateBot(ctx, owner.ID, "Group Bot", "group_shape_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	if _, err := bots.SetJoinGroups(ctx, bot.ID, false); err != nil {
		t.Fatalf("disable join groups: %v", err)
	}

	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(users),
		Channels: channels,
		Bots:     bots,
	}, zaptest.NewLogger(t), clock.System)
	ownerCtx := WithUserID(ctx, owner.ID)
	created, err := r.onMessagesCreateChat(ownerCtx, &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Bot Group RPC",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)

	blockedUser := r.tgUser(bot)
	if !blockedUser.BotNochats {
		t.Fatalf("tg user bot_nochats = false, want true before invite")
	}
	if _, err := r.onChannelsInviteToChannel(ownerCtx, &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash}},
	}); err == nil || !strings.Contains(err.Error(), "BOT_GROUPS_BLOCKED") {
		t.Fatalf("invite blocked bot err = %v, want BOT_GROUPS_BLOCKED", err)
	}

	if _, err := bots.SetJoinGroups(ctx, bot.ID, true); err != nil {
		t.Fatalf("enable join groups: %v", err)
	}
	if _, err := bots.SetBotCommands(ctx, bot.ID, []domain.BotCommand{{Command: "status", Description: "Show status"}}); err != nil {
		t.Fatalf("set bot commands: %v", err)
	}
	if _, err := r.onChannelsInviteToChannel(ownerCtx, &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash}},
	}); err != nil {
		t.Fatalf("invite allowed bot: %v", err)
	}

	participants, err := r.onChannelsGetParticipants(ownerCtx, &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsBots{},
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("get bot participants: %v", err)
	}
	list := participants.(*tg.ChannelsChannelParticipants)
	if list.Count != 1 || len(list.Participants) != 1 {
		t.Fatalf("bot participants = count %d len %d, want one", list.Count, len(list.Participants))
	}
	if len(list.Users) != 1 {
		t.Fatalf("bot participants users = %d, want one", len(list.Users))
	}
	listBot := list.Users[0].(*tg.User)
	if !listBot.Bot || listBot.ID != bot.ID || listBot.BotNochats {
		t.Fatalf("participants user = %+v, want allowed bot", listBot)
	}

	channels.botParticipantCalls = 0
	full, err := r.onChannelsGetFullChannel(ownerCtx, &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel: %v", err)
	}
	channelFull := full.FullChat.(*tg.ChannelFull)
	if len(channelFull.BotInfo) != 1 || channelFull.BotInfo[0].UserID != bot.ID {
		t.Fatalf("channel full bot_info = %+v, want bot %d", channelFull.BotInfo, bot.ID)
	}
	if len(channelFull.BotInfo[0].Commands) != 1 || channelFull.BotInfo[0].Commands[0].Command != "status" {
		t.Fatalf("channel full bot commands = %+v, want status", channelFull.BotInfo[0].Commands)
	}
	foundUser := false
	for _, u := range full.Users {
		if got, ok := u.(*tg.User); ok && got.ID == bot.ID && got.Bot {
			foundUser = true
		}
	}
	if !foundUser {
		t.Fatalf("full channel users = %+v, want bot user", full.Users)
	}
	if _, err := r.onChannelsGetFullChannel(ownerCtx, &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}); err != nil {
		t.Fatalf("get full channel cached: %v", err)
	}
	if channels.botParticipantCalls != 1 {
		t.Fatalf("full channel bot participants calls = %d, want 1", channels.botParticipantCalls)
	}
}

func TestFullChannelBotInfoCacheCachesEmptyResult(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(users, botStore, messages)
	channelStore := memory.NewChannelStore()
	baseChannels := appchannels.NewService(channelStore, appchannels.WithBotProfileResolver(bots))
	channels := &countingBotParticipantsChannelsService{Service: baseChannels}

	owner, err := users.Create(ctx, domain.User{AccessHash: 6221, Phone: "15550006221", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 6222, Phone: "15550006222", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(users),
		Channels: channels,
		Bots:     bots,
	}, zaptest.NewLogger(t), clock.System)
	ownerCtx := WithUserID(ctx, owner.ID)
	created, err := r.onMessagesCreateChat(ownerCtx, &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Empty Bot Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)

	channels.botParticipantCalls = 0
	first, err := r.onChannelsGetFullChannel(ownerCtx, &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get first full channel: %v", err)
	}
	if got := len(first.FullChat.(*tg.ChannelFull).BotInfo); got != 0 {
		t.Fatalf("first full channel bot_info len = %d, want 0", got)
	}
	second, err := r.onChannelsGetFullChannel(ownerCtx, &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get second full channel: %v", err)
	}
	if got := len(second.FullChat.(*tg.ChannelFull).BotInfo); got != 0 {
		t.Fatalf("second full channel bot_info len = %d, want 0", got)
	}
	if channels.botParticipantCalls != 1 {
		t.Fatalf("empty full channel bot participants calls = %d, want 1", channels.botParticipantCalls)
	}
}

func TestTGUsersForIDsUsesBatchBotProfiles(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	baseBots := botsapp.NewService(users, botStore, messages)
	bots := &countingBatchBotsService{Service: baseBots}

	owner, err := users.Create(ctx, domain.User{AccessHash: 6211, Phone: "15550006211", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	botA, _, err := baseBots.CreateBot(ctx, owner.ID, "Batch Bot A", "batcha_bot")
	if err != nil {
		t.Fatalf("create bot A: %v", err)
	}
	botB, _, err := baseBots.CreateBot(ctx, owner.ID, "Batch Bot B", "batchb_bot")
	if err != nil {
		t.Fatalf("create bot B: %v", err)
	}
	if _, err := baseBots.SetJoinGroups(ctx, botA.ID, false); err != nil {
		t.Fatalf("disable bot A groups: %v", err)
	}
	if _, err := baseBots.SetInlinePlaceholder(ctx, botB.ID, "Search B"); err != nil {
		t.Fatalf("set bot B inline placeholder: %v", err)
	}

	r := New(Config{}, Deps{
		Users: appusers.NewService(users),
		Bots:  bots,
	}, zaptest.NewLogger(t), clock.System)
	got := r.tgUsersForIDs(ctx, owner.ID, []int64{botA.ID, botB.ID})
	if bots.batchCalls != 1 {
		t.Fatalf("BotInfos calls = %d, want 1", bots.batchCalls)
	}
	if bots.singleCalls != 0 {
		t.Fatalf("BotInfo calls = %d, want 0", bots.singleCalls)
	}
	byID := make(map[int64]*tg.User)
	for _, item := range got {
		if u, ok := item.(*tg.User); ok {
			byID[u.ID] = u
		}
	}
	if u := byID[botA.ID]; u == nil || !u.BotNochats {
		t.Fatalf("bot A user = %+v, want bot_nochats", u)
	}
	if u := byID[botB.ID]; u == nil || u.BotInlinePlaceholder != "Search B" {
		t.Fatalf("bot B user = %+v, want inline placeholder", u)
	}
}

type countingBatchBotsService struct {
	*botsapp.Service
	singleCalls int
	batchCalls  int
}

func (s *countingBatchBotsService) BotInfo(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	s.singleCalls++
	return s.Service.BotInfo(ctx, botUserID)
}

func (s *countingBatchBotsService) BotInfos(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	s.batchCalls++
	return s.Service.BotInfos(ctx, botUserIDs)
}

type countingBotParticipantsChannelsService struct {
	*appchannels.Service
	botParticipantCalls int
}

func (s *countingBotParticipantsChannelsService) GetParticipants(ctx context.Context, userID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error) {
	if filter.Kind == domain.ChannelParticipantsBots {
		s.botParticipantCalls++
	}
	return s.Service.GetParticipants(ctx, userID, channelID, filter, offset, limit)
}
