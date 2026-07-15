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

// privacyBotResolver 让指定 user 成为 bot_chat_history=false 的隐私 bot（命令/@/回复以外的群消息不可见）。
type privacyBotResolver map[int64]bool

func (p privacyBotResolver) BotInfo(_ context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	if _, ok := p[botUserID]; !ok {
		return domain.BotProfile{}, false, nil
	}
	return domain.BotProfile{BotUserID: botUserID, ChatHistory: false}, true, nil
}

// TestChannelMessageFanoutSkipsPrivacyBotOnlinePush 回归:在线 privacy bot 不得经实时 fanout 收到群里
// 的纯文本消息。修复前 channelFanoutRecipients 按「在线活跃成员」重算 recipients,会把 send 时已按
// SkipDeliveryUserIDs 排除的 bot 加回 → 在线 bot 实时收到全部群聊内容(持久 history/difference 已正确
// 隐藏,仅此直接推送泄漏内容)。修复后 fanout build 对被 skip 的 viewer 返回 nil,不推送。
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

	// 纯文本消息:privacy bot 1003 不可见 → SendMessage 把它放进 SkipDeliveryUserIDs、排除出 Recipients。
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

	// 两成员都在线;channelFanoutRecipients 据此把 1003 当在线活跃成员加回 recipients。
	sessions := &captureSessions{
		channelMembers: map[int64][]int64{created.Channel.ID: {1002, 1003}},
	}
	r := New(Config{}, Deps{Channels: channelService, Sessions: sessions}, zaptest.NewLogger(t), clock.System)

	// 复核前置:修复前后 channelFanoutRecipients 都会把 1003 列进 recipients(在线活跃成员),
	// 漏洞/修复的差异在 build 是否对它返回 nil。
	got := r.channelFanoutRecipients(ctx, channelFanoutMembers, created.Channel.ID, res.Recipients)
	if !fanoutHasID(got, 1003) {
		t.Fatalf("channelFanoutRecipients = %v, 期望在线活跃成员 1003 仍被列入(否则测不到 build 跳过)", got)
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
