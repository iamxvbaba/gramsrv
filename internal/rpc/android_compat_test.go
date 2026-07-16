package rpc

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"

	botsapp "telesrv/internal/app/bots"
	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestLegacyAndroidMessagesCreateChatCreatesMegagroupWithTTL(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 71, Phone: "15550001071", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 72, Phone: "15550001072", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	const ttlPeriod = 7 * 24 * 60 * 60
	var in bin.Buffer
	in.PutID(0x0034a818)
	if err := (&tg.MessagesCreateChatRequest{
		Users:     []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title:     "Android Group",
		TTLPeriod: ttlPeriod,
	}).EncodeBare(&in); err != nil {
		t.Fatalf("encode legacy createChat body: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(androidClientContext(), owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch legacy createChat: %v", err)
	}
	invited, ok := enc.(*tg.MessagesInvitedUsers)
	if !ok {
		t.Fatalf("response = %T, want messages.invitedUsers", enc)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) < 2 {
		t.Fatalf("updates = %T %+v, want legacy chat + channel", invited.Updates, invited.Updates)
	}
	legacy, ok := updates.Chats[0].(*tg.Chat)
	if !ok || !legacy.Deactivated {
		t.Fatalf("first chat = %#v, want migrated legacy chat", updates.Chats[0])
	}
	channel, ok := updates.Chats[1].(*tg.Channel)
	if !ok || !channel.Megagroup || !channel.Creator {
		t.Fatalf("second chat = %#v, want creator megagroup channel", updates.Chats[1])
	}
	view, err := channelService.GetChannel(ctx, owner.ID, channel.ID)
	if err != nil {
		t.Fatalf("get created channel: %v", err)
	}
	if !view.Channel.Megagroup || view.Channel.Broadcast {
		t.Fatalf("created channel = %+v, want megagroup only", view.Channel)
	}
	if view.Channel.TTLPeriod != ttlPeriod {
		t.Fatalf("ttl_period = %d, want %d", view.Channel.TTLPeriod, ttlPeriod)
	}
}

func TestLegacyAndroidAuthSignUpAllowedBeforeAuthorization(t *testing.T) {
	authKeyID := [8]byte{0x31, 0xd3, 0x36, 0xc1, 0x7a, 0x1a, 0x33, 0x48}
	auth := &captureAuthService{
		signUpUser: domain.User{
			ID:         1000004242,
			AccessHash: 424242,
			Phone:      "15550004242",
			FirstName:  "Android",
			LastName:   "Signup",
		},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	in.PutID(0x80eee427)
	in.PutString("+15550004242")
	in.PutString("phone-code-hash")
	in.PutString("Android")
	in.PutString("Signup")

	enc, err := r.Dispatch(androidClientContext(), authKeyID, 777, &in)
	if err != nil {
		t.Fatalf("dispatch legacy auth.signUp: %v", err)
	}
	// Routed through the generated static client overlay and sparse semantic
	// dispatcher, which exposes the canonical concrete result at this test seam.
	authorization, ok := enc.(*tg.AuthAuthorization)
	if !ok {
		t.Fatalf("response = %T, want *tg.AuthAuthorization", enc)
	}
	user, ok := authorization.User.(*tg.User)
	if !ok || user.ID != auth.signUpUser.ID {
		t.Fatalf("authorization user = %T %+v, want user %d", authorization.User, authorization.User, auth.signUpUser.ID)
	}
	if auth.signUpPhone != "+15550004242" ||
		auth.signUpHash != "phone-code-hash" ||
		auth.signUpFirstName != "Android" ||
		auth.signUpLastName != "Signup" {
		t.Fatalf("signup args = phone %q hash %q first %q last %q", auth.signUpPhone, auth.signUpHash, auth.signUpFirstName, auth.signUpLastName)
	}
	if auth.signUpAuth.AuthKeyID != authKeyID {
		t.Fatalf("signup auth key = %x, want %x", auth.signUpAuth.AuthKeyID, authKeyID)
	}
}

func TestModernAndroidChannelsInviteToChannelDispatch(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 91, Phone: "15550001091", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 92, Phone: "15550001092", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Android Channel",
		About:     "invite compat",
		Broadcast: true,
		Date:      1700000091,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	// DrKLO channels.inviteToChannel#199f3a6c — now handled by the unified
	// generated client overlay, not a dedicated handler.
	in.PutID(0x199f3a6c)
	if err := (&tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
	}).EncodeBare(&in); err != nil {
		t.Fatalf("encode modern inviteToChannel body: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(androidClientContext(), owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch modern inviteToChannel: %v", err)
	}
	invited, ok := enc.(*tg.MessagesInvitedUsers)
	if !ok {
		t.Fatalf("response = %T, want messages.invitedUsers", enc)
	}
	if invited.Updates == nil || len(invited.MissingInvitees) != 0 {
		t.Fatalf("invited users = %+v, want updates and no missing users", invited)
	}
	member, err := channelService.GetParticipant(ctx, owner.ID, created.Channel.ID, friend.ID)
	if err != nil {
		t.Fatalf("get invited participant: %v", err)
	}
	if member.UserID != friend.ID || member.Status != domain.ChannelMemberActive {
		t.Fatalf("participant = %+v, want active friend", member)
	}
	view, err := channelService.GetChannel(ctx, owner.ID, created.Channel.ID)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if view.Channel.ParticipantsCount != 2 {
		t.Fatalf("participants_count = %d, want 2", view.Channel.ParticipantsCount)
	}
}

func TestLegacyAndroidBotsExportBotTokenDispatch(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	botStore := memory.NewBotStore(userStore)
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	botService := botsapp.NewService(userStore, botStore, messageStore)
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93, Phone: "15550001093", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	bot, token, err := botService.CreateBot(ctx, owner.ID, "Android Export", "android_export_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users: appusers.NewService(userStore),
		Bots:  botService,
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	in.PutID(0x0063b089)
	in.PutLong(bot.ID)
	if err := (&tg.BoolFalse{}).Encode(&in); err != nil {
		t.Fatalf("encode revoke bool: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(androidClientContext(), owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch legacy exportBotToken: %v", err)
	}
	exported, ok := enc.(*tg.BotsExportedBotToken)
	if !ok {
		t.Fatalf("response = %T, want bots.exportedBotToken", enc)
	}
	if exported.Token != token {
		t.Fatalf("token = %q, want %q", exported.Token, token)
	}
}

func TestMessagesCreateChatRejectsNegativeTTLPeriod(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 81, Phone: "15550001081", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 82, Phone: "15550001082", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	_, err = r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users:     []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title:     "Bad TTL",
		TTLPeriod: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "TTL_PERIOD_INVALID") {
		t.Fatalf("createChat err = %v, want TTL_PERIOD_INVALID", err)
	}
}

func TestStoriesCanSendStoryDispatchReturnsPositiveRemainingCount(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	var in bin.Buffer
	if err := (&tg.StoriesCanSendStoryRequest{Peer: &tg.InputPeerSelf{}}).Encode(&in); err != nil {
		t.Fatalf("encode canSendStory: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(androidClientContext(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch canSendStory: %v", err)
	}
	count, ok := enc.(*tg.StoriesCanSendStoryCount)
	if !ok {
		t.Fatalf("response = %T, want stories.canSendStoryCount", enc)
	}
	if count.CountRemains <= 0 {
		t.Fatalf("count_remains = %d, want positive", count.CountRemains)
	}
}
