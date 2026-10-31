package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// privacyBotResolver makes the given user a privacy bot with bot_chat_history=false (group messages other than commands/@/replies are invisible to it).
type privacyBotResolver map[int64]bool

func (p privacyBotResolver) BotInfo(_ context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	if _, ok := p[botUserID]; !ok {
		return domain.BotProfile{}, false, nil
	}
	return domain.BotProfile{BotUserID: botUserID, ChatHistory: false}, true, nil
}

// TestChannelMessageFanoutSkipsPrivacyBotOnlinePush regression: an online privacy bot must not
// receive a group's plain-text message via realtime fanout. Before the fix,
// channelFanoutRecipients recomputed recipients by "online active members", adding back the bot
// that send had already excluded via SkipDeliveryUserIDs -> the online bot received all group
// content in realtime (persistent history/difference already hid it correctly; only this direct
// push leaked content). After the fix, fanout build returns nil for a skipped viewer and does not
// push.
func TestChannelMessageFanoutSkipsPrivacyBotOnlinePush(t *testing.T) {
	ctx := context.Background()
	channelStore := memory.NewChannelStore()
	bots := privacyBotResolver{1003: true}
	channelService := appchannels.NewService(channelStore, appchannels.WithBotProfileResolver(bots))
	created, err := channelService.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Privacy",
		MemberUserIDs: []int64{1002, 1003},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	// Plain-text message: invisible to privacy bot 1003 -> SendMessage places it in SkipDeliveryUserIDs and excludes it from Recipients.
	res, err := channelService.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2001,
		Message:   "plain group chatter",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !fanoutHasID(res.SkipDeliveryUserIDs, 1003) {
		t.Fatalf("res.SkipDeliveryUserIDs = %v, want privacy bot 1003 excluded at send", res.SkipDeliveryUserIDs)
	}
	if fanoutHasID(res.Recipients, 1003) {
		t.Fatalf("res.Recipients = %v, privacy bot 1003 must not be a delivery recipient", res.Recipients)
	}

	// Both members are online; channelFanoutRecipients therefore adds 1003 back to recipients as an online active member.
	sessions := &captureSessions{
		channelMembers: map[int64][]int64{created.Channel.ID: {1002, 1003}},
	}
	users := &prefetchRecordingUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{
		1001: {ID: 1001, FirstName: "sender"},
	}}}
	r := New(Config{}, Deps{Channels: channelService, Sessions: sessions, Users: users}, zaptest.NewLogger(t), clock.System)

	// Recheck precondition: both before and after the fix, channelFanoutRecipients lists 1003 in
	// recipients (online active member), so the difference between the bug and the fix is whether
	// build returns nil for it.
	got := r.channelFanoutRecipients(ctx, channelFanoutMembers, created.Channel.ID, res.Recipients)
	if !fanoutHasID(got, 1003) {
		t.Fatalf("channelFanoutRecipients = %v, expected online active member 1003 to still be listed (otherwise the build-skip cannot be tested)", got)
	}

	r.enqueueChannelMessageFanout(ctx, 1001, res, nil)
	pushed := sessions.pushedUserIDs()
	if !fanoutHasID(pushed, 1002) {
		t.Fatalf("fanout pushed = %v, want human member 1002 to receive online push", pushed)
	}
	if fanoutHasID(pushed, 1003) {
		t.Fatalf("fanout pushed = %v, online privacy bot 1003 must NOT receive plain-message push (privacy leak)", pushed)
	}
}
