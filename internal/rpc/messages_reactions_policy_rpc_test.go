package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

type reactionPolicyFixture struct {
	router     *Router
	channelSvc *appchannels.Service
	sessions   *captureSessions
	channel    domain.Channel
	messageID  int
	ownerID    int64
	memberID   int64
	member2ID  int64
}

func newReactionPolicyFixture(t *testing.T, broadcast bool) reactionPolicyFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550003001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550003002", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	member2, err := userStore.Create(ctx, domain.User{AccessHash: 33, Phone: "15550003003", FirstName: "Member2"})
	if err != nil {
		t.Fatalf("create member2: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "Reaction Policy",
		Broadcast:     broadcast,
		Megagroup:     !broadcast,
		MemberUserIDs: []int64{member.ID, member2.ID},
		Date:          1000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := channelSvc.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  9001,
		Message:   "react to me",
		Date:      1100,
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	return reactionPolicyFixture{
		router:     r,
		channelSvc: channelSvc,
		sessions:   sessions,
		channel:    created.Channel,
		messageID:  sent.Message.ID,
		ownerID:    owner.ID,
		memberID:   member.ID,
		member2ID:  member2.ID,
	}
}

func (f reactionPolicyFixture) sendReaction(t *testing.T, userID int64, emoticons ...string) (tg.UpdatesClass, error) {
	t.Helper()
	reactions := make([]tg.ReactionClass, 0, len(emoticons))
	if len(emoticons) > 0 {
		for _, emoticon := range emoticons {
			reactions = append(reactions, &tg.ReactionEmoji{Emoticon: emoticon})
		}
	}
	return f.sendTLReactions(t, userID, reactions...)
}

func (f reactionPolicyFixture) sendTLReactions(t *testing.T, userID int64, reactions ...tg.ReactionClass) (tg.UpdatesClass, error) {
	t.Helper()
	req := &tg.MessagesSendReactionRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		MsgID: f.messageID,
	}
	if len(reactions) > 0 {
		req.SetReaction(reactions)
	}
	return f.router.onMessagesSendReaction(WithUserID(context.Background(), userID), req)
}

func reactionUpdateFromUpdates(t *testing.T, updates tg.UpdatesClass) *tg.UpdateMessageReactions {
	t.Helper()
	box, ok := updates.(*tg.Updates)
	if !ok || len(box.Updates) == 0 {
		t.Fatalf("updates = %T %+v, want non-empty *tg.Updates", updates, updates)
	}
	update, ok := box.Updates[0].(*tg.UpdateMessageReactions)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateMessageReactions", box.Updates[0])
	}
	return update
}

func TestSendReactionRespectsChannelReactionPolicy(t *testing.T) {
	f := newReactionPolicyFixture(t, false)
	ctx := context.Background()

	if _, err := f.channelSvc.SetAvailableReactions(ctx, f.ownerID, f.channel.ID, domain.ChannelReactionPolicy{
		Type:      domain.ChannelReactionPolicySome,
		Emoticons: []string{"\U0001f44d"},
	}); err != nil {
		t.Fatalf("set whitelist policy: %v", err)
	}
	if _, err := f.sendReaction(t, f.memberID, "❤"); err == nil || !strings.Contains(err.Error(), "REACTION_INVALID") {
		t.Fatalf("off-whitelist reaction err = %v, want REACTION_INVALID", err)
	}
	if _, err := f.sendReaction(t, f.memberID, "\U0001f44d"); err != nil {
		t.Fatalf("whitelisted reaction: %v", err)
	}

	if _, err := f.channelSvc.SetAvailableReactions(ctx, f.ownerID, f.channel.ID, domain.ChannelReactionPolicy{
		Type: domain.ChannelReactionPolicyNone,
	}); err != nil {
		t.Fatalf("set none policy: %v", err)
	}
	if _, err := f.sendReaction(t, f.ownerID, "\U0001f44d"); err == nil || !strings.Contains(err.Error(), "REACTION_INVALID") {
		t.Fatalf("reaction under none policy err = %v, want REACTION_INVALID", err)
	}
	// 策略收紧后撤销存量 reaction 必须仍然可行。
	if _, err := f.sendReaction(t, f.memberID); err != nil {
		t.Fatalf("retract reaction under none policy: %v", err)
	}
}

func TestSendReactionAllowsCustomEmojiFromChannelPolicy(t *testing.T) {
	f := newReactionPolicyFixture(t, false)
	ctx := context.Background()
	const customDocumentID int64 = 7770001

	updates, err := f.router.onMessagesSetChatAvailableReactions(WithUserID(ctx, f.ownerID), &tg.MessagesSetChatAvailableReactionsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		AvailableReactions: &tg.ChatReactionsSome{Reactions: []tg.ReactionClass{
			&tg.ReactionCustomEmoji{DocumentID: customDocumentID},
		}},
	})
	if err != nil {
		t.Fatalf("set custom emoji reactions: %v", err)
	}
	if _, ok := updates.(*tg.Updates); !ok {
		t.Fatalf("set custom emoji updates = %T, want *tg.Updates", updates)
	}

	if _, err := f.sendReaction(t, f.memberID, "\U0001f44d"); err == nil || !strings.Contains(err.Error(), "REACTION_INVALID") {
		t.Fatalf("off-whitelist emoji err = %v, want REACTION_INVALID", err)
	}

	f.sessions.channelViewers = map[int64][]int64{f.channel.ID: {f.member2ID}}
	sent, err := f.sendTLReactions(t, f.memberID, &tg.ReactionCustomEmoji{DocumentID: customDocumentID})
	if err != nil {
		t.Fatalf("send custom emoji reaction: %v", err)
	}
	update := reactionUpdateFromUpdates(t, sent)
	if len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 1 || update.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("custom reaction results = %+v, want one chosen custom reaction", update.Reactions.Results)
	}
	reaction, ok := update.Reactions.Results[0].Reaction.(*tg.ReactionCustomEmoji)
	if !ok || reaction.DocumentID != customDocumentID {
		t.Fatalf("custom reaction = %T %+v, want document %d", update.Reactions.Results[0].Reaction, update.Reactions.Results[0].Reaction, customDocumentID)
	}

	pushed := f.sessions.pushedUserIDs()
	foundOtherViewer := false
	for _, userID := range pushed {
		if userID == f.member2ID {
			foundOtherViewer = true
			break
		}
	}
	if !foundOtherViewer {
		t.Fatalf("pushed users = %+v, want other online member %d", pushed, f.member2ID)
	}
}

func TestSendReactionNormalizesDefaultReactionDocumentID(t *testing.T) {
	f := newReactionPolicyFixture(t, false)
	f.router.deps.Files = &fakeFiles{reactions: []domain.AvailableReaction{
		{Reaction: "\U0001f44d", ActivateAnimationID: 8801},
	}}
	ctx := context.Background()

	if _, err := f.channelSvc.SetAvailableReactions(ctx, f.ownerID, f.channel.ID, domain.ChannelReactionPolicy{
		Type:      domain.ChannelReactionPolicySome,
		Emoticons: []string{"\U0001f44d"},
	}); err != nil {
		t.Fatalf("set emoji whitelist policy: %v", err)
	}

	updates, err := f.sendTLReactions(t, f.memberID, &tg.ReactionCustomEmoji{DocumentID: 8801})
	if err != nil {
		t.Fatalf("send default reaction by document id: %v", err)
	}
	update := reactionUpdateFromUpdates(t, updates)
	if len(update.Reactions.Results) != 1 {
		t.Fatalf("reaction results = %+v, want one normalized emoji result", update.Reactions.Results)
	}
	emoji, ok := update.Reactions.Results[0].Reaction.(*tg.ReactionEmoji)
	if !ok || emoji.Emoticon != "\U0001f44d" {
		t.Fatalf("normalized reaction = %T %+v, want reactionEmoji thumbs up", update.Reactions.Results[0].Reaction, update.Reactions.Results[0].Reaction)
	}
}

func TestSendReactionEnforcesUniqueReactionsLimit(t *testing.T) {
	f := newReactionPolicyFixture(t, false)
	ctx := context.Background()

	// reactions_limit 是 appConfig reactions_uniq_max 的 per-chat 覆盖，用 1 触发上限。
	if _, err := f.channelSvc.SetAvailableReactions(ctx, f.ownerID, f.channel.ID, domain.ChannelReactionPolicy{
		Type:  domain.ChannelReactionPolicyAll,
		Limit: 1,
	}); err != nil {
		t.Fatalf("set uniq limit policy: %v", err)
	}
	if _, err := f.sendReaction(t, f.ownerID, "\U0001f44d"); err != nil {
		t.Fatalf("first reaction: %v", err)
	}
	if _, err := f.sendReaction(t, f.memberID, "❤"); err == nil || !strings.Contains(err.Error(), "REACTIONS_TOO_MANY") {
		t.Fatalf("new unique emoji past limit err = %v, want REACTIONS_TOO_MANY", err)
	}
	// 追加已存在的种类不受 uniq 上限约束。
	updates, err := f.sendReaction(t, f.memberID, "\U0001f44d")
	if err != nil {
		t.Fatalf("existing emoji reaction: %v", err)
	}
	update := reactionUpdateFromUpdates(t, updates)
	if len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 2 {
		t.Fatalf("results = %+v, want one emoji with count 2", update.Reactions.Results)
	}
}

func TestSendReactionOverLimitMessageStillAllowsExistingKinds(t *testing.T) {
	f := newReactionPolicyFixture(t, false)
	ctx := context.Background()

	// 默认策略下先造出 {👍, ❤} 两个去重种类，再把 reactions_limit 调低到 1，
	// 模拟「存量已超限」（管理员事后调低 / 部署前无 uniq 闸门的旧数据）。
	if _, err := f.sendReaction(t, f.ownerID, "\U0001f44d"); err != nil {
		t.Fatalf("seed owner reaction: %v", err)
	}
	if _, err := f.sendReaction(t, f.memberID, "❤"); err != nil {
		t.Fatalf("seed member reaction: %v", err)
	}
	if _, err := f.channelSvc.SetAvailableReactions(ctx, f.ownerID, f.channel.ID, domain.ChannelReactionPolicy{
		Type:  domain.ChannelReactionPolicyAll,
		Limit: 1,
	}); err != nil {
		t.Fatalf("lower uniq limit: %v", err)
	}

	// 重发自己已有的 reaction（no-op）必须放行。
	if _, err := f.sendReaction(t, f.ownerID, "\U0001f44d"); err != nil {
		t.Fatalf("owner re-send own reaction on over-limit message: %v", err)
	}
	if _, err := f.sendReaction(t, f.memberID, "❤"); err != nil {
		t.Fatalf("member re-send own reaction on over-limit message: %v", err)
	}
	// 第三人给已有种类投票必须放行（不引入新种类）。
	if _, err := f.sendReaction(t, f.member2ID, "\U0001f44d"); err != nil {
		t.Fatalf("third user piles onto existing kind on over-limit message: %v", err)
	}
	// 引入新种类仍然 REACTIONS_TOO_MANY。
	if _, err := f.sendReaction(t, f.member2ID, "\U0001f525"); err == nil || !strings.Contains(err.Error(), "REACTIONS_TOO_MANY") {
		t.Fatalf("new kind on over-limit message err = %v, want REACTIONS_TOO_MANY", err)
	}
}

func TestSendReactionTrimsVectorToPerUserMax(t *testing.T) {
	f := newReactionPolicyFixture(t, false)

	// 超出 reactions_user_max_default 的向量保留尾部最新项，不报错。
	updates, err := f.sendReaction(t, f.memberID, "\U0001f44d", "❤")
	if err != nil {
		t.Fatalf("send oversized reaction vector: %v", err)
	}
	update := reactionUpdateFromUpdates(t, updates)
	if len(update.Reactions.Results) != 1 {
		t.Fatalf("results = %+v, want single trimmed reaction", update.Reactions.Results)
	}
	emoji, ok := update.Reactions.Results[0].Reaction.(*tg.ReactionEmoji)
	if !ok || emoji.Emoticon != "❤" {
		t.Fatalf("kept reaction = %+v, want newest vector entry kept", update.Reactions.Results[0].Reaction)
	}
	if update.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("chosen_order = %d, want 1", update.Reactions.Results[0].ChosenOrder)
	}
	recent, ok := update.Reactions.GetRecentReactions()
	if !ok || len(recent) != 1 {
		t.Fatalf("megagroup recent reactions = %+v set=%v, want one entry", recent, ok)
	}
}

func TestBroadcastReactionsHideRecentReactors(t *testing.T) {
	f := newReactionPolicyFixture(t, true)

	updates, err := f.sendReaction(t, f.memberID, "\U0001f44d")
	if err != nil {
		t.Fatalf("send broadcast reaction: %v", err)
	}
	update := reactionUpdateFromUpdates(t, updates)
	if len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 1 {
		t.Fatalf("broadcast results = %+v, want count-only aggregate", update.Reactions.Results)
	}
	if recent, ok := update.Reactions.GetRecentReactions(); ok && len(recent) > 0 {
		t.Fatalf("broadcast recent reactions = %+v, want anonymous (absent)", recent)
	}
	if update.Reactions.CanSeeList {
		t.Fatalf("broadcast can_see_list = true, want false")
	}
}

func TestChannelRealtimeFanoutPassesCapToProvider(t *testing.T) {
	f := newReactionPolicyFixture(t, false)
	f.sessions.channelViewers = map[int64][]int64{f.channel.ID: {f.memberID}}

	if _, err := f.sendReaction(t, f.memberID, "\U0001f44d"); err != nil {
		t.Fatalf("send reaction: %v", err)
	}
	f.sessions.mu.Lock()
	gotLimit := f.sessions.channelViewersLimit
	f.sessions.mu.Unlock()
	if gotLimit != domain.MaxChannelRealtimeFanout {
		t.Fatalf("online viewers limit = %d, want MaxChannelRealtimeFanout %d", gotLimit, domain.MaxChannelRealtimeFanout)
	}
}
