package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appaccount "telesrv/internal/app/account"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
	"time"
)

type reactionPolicyFixture struct {
	router     *Router
	accountSvc *appaccount.Service
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
	passwordStore := memory.NewPasswordStore()
	accountSvc := appaccount.NewService(passwordStore, appaccount.WithReactionSettings(passwordStore))
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
		Account:  accountSvc,
		Channels: channelSvc,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	return reactionPolicyFixture{
		router:     r,
		accountSvc: accountSvc,
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
	// Retracting an existing reaction must still work after the policy tightens.
	if _, err := f.sendReaction(t, f.memberID); err != nil {
		t.Fatalf("retract reaction under none policy: %v", err)
	}
}

func TestSendPaidReactionUsesAtomicChannelSettlement(t *testing.T) {
	f := newReactionPolicyFixture(t, true)
	ctx := context.Background()
	if _, err := f.channelSvc.SetAvailableReactions(ctx, f.ownerID, f.channel.ID, domain.ChannelReactionPolicy{
		Type: domain.ChannelReactionPolicyAll, PaidEnabled: true,
	}); err != nil {
		t.Fatalf("enable paid reactions: %v", err)
	}
	f.sessions.channelViewers = map[int64][]int64{f.channel.ID: {f.member2ID}}
	req := &tg.MessagesSendPaidReactionRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		MsgID:    f.messageID,
		Count:    100,
		RandomID: int64(uint64(uint32(time.Now().Unix()))<<32 | 88001),
	}
	updates, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), req)
	if err != nil {
		t.Fatalf("first paid reaction: %v", err)
	}
	update := reactionUpdateFromUpdates(t, updates)
	if len(update.Reactions.Results) == 0 || update.Reactions.Results[0].Count != 100 {
		t.Fatalf("first paid reaction projection = %+v", update.Reactions.Results)
	}
	box := updates.(*tg.Updates)
	balance := int64(-1)
	for _, item := range box.Updates {
		if b, ok := item.(*tg.UpdateStarsBalance); ok {
			if amount, ok := b.Balance.(*tg.StarsAmount); ok {
				balance = amount.Amount
			}
		}
	}
	if balance != domain.DefaultStarsStartingGrant-100 {
		t.Fatalf("payer balance update=%d", balance)
	}
	later := *req
	later.Count = 25
	later.RandomID++
	if _, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), &later); err != nil {
		t.Fatalf("later paid reaction: %v", err)
	}
	expired := *req
	expired.RandomID = int64(uint64(uint32(time.Now().Unix()-domain.PaidReactionRandomIDMaxAgeSeconds-1))<<32 | 88009)
	if _, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), &expired); err == nil || !strings.Contains(err.Error(), "RANDOM_ID_EXPIRED") {
		t.Fatalf("expired paid reaction random_id err=%v", err)
	}
	if _, err := f.channelSvc.LeaveChannel(ctx, f.memberID, f.channel.ID, int(time.Now().Unix())); err != nil {
		t.Fatalf("leave channel before replay: %v", err)
	}
	if _, err := f.channelSvc.DeleteMessages(ctx, f.ownerID, domain.DeleteChannelMessagesRequest{
		ChannelID: f.channel.ID, IDs: []int{f.messageID}, Date: int(time.Now().Unix()),
	}); err != nil {
		t.Fatalf("delete paid reaction target before replay: %v", err)
	}

	f.sessions.clearMessages()
	replay, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), req)
	if err != nil {
		t.Fatalf("exact paid reaction replay: %v", err)
	}
	if got := reactionUpdateFromUpdates(t, replay).Reactions.Results[0].Count; got != 100 {
		t.Fatalf("replay paid reaction count=%d, want frozen first receipt", got)
	}
	replayBalance := int64(-1)
	for _, item := range replay.(*tg.Updates).Updates {
		if update, ok := item.(*tg.UpdateStarsBalance); ok {
			if amount, ok := update.Balance.(*tg.StarsAmount); ok {
				replayBalance = amount.Amount
			}
		}
	}
	if replayBalance != domain.DefaultStarsStartingGrant-125 {
		t.Fatalf("replay current payer balance=%d, want %d", replayBalance, domain.DefaultStarsStartingGrant-125)
	}
	if pushed := f.sessions.pushedUserIDs(); len(pushed) != 0 {
		t.Fatalf("exact replay pushed duplicate fanout to %v", pushed)
	}

	conflict := *req
	conflict.Count++
	if _, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), &conflict); err == nil || !strings.Contains(err.Error(), "RANDOM_ID_DUPLICATE") {
		t.Fatalf("changed paid reaction random_id err=%v", err)
	}
	explicitSelf := *req
	explicitSelf.SetPrivate(&tg.PaidReactionPrivacyDefault{})
	if _, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), &explicitSelf); err == nil || !strings.Contains(err.Error(), "RANDOM_ID_DUPLICATE") {
		t.Fatalf("account-default versus explicit-self random_id err=%v", err)
	}
	zero := *req
	zero.RandomID = 0
	if _, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), &zero); err == nil || !strings.Contains(err.Error(), "RANDOM_ID_EMPTY") {
		t.Fatalf("zero paid reaction random_id err=%v", err)
	}
}

func TestPaidReactionCutoverAmbiguityDoesNotTriggerExpiredAutoRetry(t *testing.T) {
	err := channelReactionErr(domain.ErrPaidReactionCutoverAmbiguous)
	if err == nil || !strings.Contains(err.Error(), "RANDOM_ID_DUPLICATE") || strings.Contains(err.Error(), "RANDOM_ID_EXPIRED") {
		t.Fatalf("cutover paid reaction error=%v, want non-retrying RANDOM_ID_DUPLICATE", err)
	}
}

func TestSendPaidReactionChannelSendAsAuthorizationAndProjection(t *testing.T) {
	f := newReactionPolicyFixture(t, true)
	ctx := context.Background()
	if _, err := f.channelSvc.SetAvailableReactions(ctx, f.ownerID, f.channel.ID, domain.ChannelReactionPolicy{
		Type: domain.ChannelReactionPolicyAll, PaidEnabled: true,
	}); err != nil {
		t.Fatalf("enable paid reactions: %v", err)
	}
	owned, err := f.channelSvc.CreateChannel(ctx, f.memberID, domain.CreateChannelRequest{
		Title: "Paid Reactor Identity", Broadcast: true, Date: 1200,
	})
	if err != nil {
		t.Fatalf("create owned send-as channel: %v", err)
	}
	req := &tg.MessagesSendPaidReactionRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		MsgID: f.messageID, Count: 33,
		RandomID: int64(uint64(uint32(time.Now().Unix()))<<32 | 88101),
	}
	req.SetPrivate(&tg.PaidReactionPrivacyPeer{Peer: &tg.InputPeerChannel{
		ChannelID: owned.Channel.ID, AccessHash: owned.Channel.AccessHash,
	}})
	updates, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), req)
	if err != nil {
		t.Fatalf("owned channel paid reaction: %v", err)
	}
	update := reactionUpdateFromUpdates(t, updates)
	top, ok := update.Reactions.GetTopReactors()
	if !ok || len(top) != 1 {
		t.Fatalf("top reactors=%+v set=%v", top, ok)
	}
	peer, ok := top[0].PeerID.(*tg.PeerChannel)
	if !ok || peer.ChannelID != owned.Channel.ID {
		t.Fatalf("top reactor peer=%T %+v, want channel %d", top[0].PeerID, top[0].PeerID, owned.Channel.ID)
	}
	chatFound := false
	for _, chat := range updates.(*tg.Updates).Chats {
		if channel, ok := chat.(*tg.Channel); ok && channel.ID == owned.Channel.ID {
			chatFound = true
		}
	}
	if !chatFound {
		t.Fatalf("updates chats=%+v, missing send-as channel %d", updates.(*tg.Updates).Chats, owned.Channel.ID)
	}
	ownedPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: owned.Channel.ID}
	if _, err := f.accountSvc.SetPaidReactionPrivacy(ctx, f.memberID, domain.PaidReactionPrivacy{
		Kind: domain.PaidReactionPrivacyPeer, Peer: &ownedPeer,
	}); err != nil {
		t.Fatalf("save account-default paid reaction channel: %v", err)
	}
	accountDefault := &tg.MessagesSendPaidReactionRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		MsgID: f.messageID, Count: 7, RandomID: req.RandomID + 10,
	}
	defaultUpdates, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), accountDefault)
	if err != nil {
		t.Fatalf("account-default channel paid reaction: %v", err)
	}
	defaultTop, ok := reactionUpdateFromUpdates(t, defaultUpdates).Reactions.GetTopReactors()
	if !ok || len(defaultTop) != 1 {
		t.Fatalf("account-default top reactors=%+v set=%v", defaultTop, ok)
	}
	defaultPeer, ok := defaultTop[0].PeerID.(*tg.PeerChannel)
	if !ok || defaultPeer.ChannelID != owned.Channel.ID {
		t.Fatalf("account-default top peer=%T %+v", defaultTop[0].PeerID, defaultTop[0].PeerID)
	}

	foreign, err := f.channelSvc.CreateChannel(ctx, f.member2ID, domain.CreateChannelRequest{
		Title: "Foreign Paid Reactor Identity", Broadcast: true, MemberUserIDs: []int64{f.memberID}, Date: 1201,
	})
	if err != nil {
		t.Fatalf("create foreign send-as channel: %v", err)
	}
	if _, err := f.channelSvc.EditAdmin(ctx, f.member2ID, domain.EditChannelAdminRequest{
		ChannelID: foreign.Channel.ID, MemberID: f.memberID,
		AdminRights: domain.ChannelAdminRights{PostMessages: true}, Date: 1202,
	}); err != nil {
		t.Fatalf("promote non-owner paid reaction admin: %v", err)
	}
	invalid := *req
	invalid.RandomID++
	invalid.SetPrivate(&tg.PaidReactionPrivacyPeer{Peer: &tg.InputPeerChannel{
		ChannelID: foreign.Channel.ID, AccessHash: foreign.Channel.AccessHash,
	}})
	if _, err := f.router.onMessagesSendPaidReaction(WithUserID(ctx, f.memberID), &invalid); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("post-messages admin paid reaction send-as err=%v", err)
	}
}

func TestSendPaidReactionRejectsDisabledPolicy(t *testing.T) {
	f := newReactionPolicyFixture(t, true)
	now := uint64(uint32(time.Now().Unix()))
	_, err := f.router.onMessagesSendPaidReaction(WithUserID(context.Background(), f.memberID), &tg.MessagesSendPaidReactionRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		MsgID:    f.messageID,
		Count:    1,
		RandomID: int64(now<<32 | 88002),
	})
	if err == nil || !strings.Contains(err.Error(), "REACTION_INVALID") {
		t.Fatalf("disabled paid reaction err=%v", err)
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

	// reactions_limit is the per-chat override of appConfig reactions_uniq_max; use 1 to trigger the limit.
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
	// Appending an existing kind is not constrained by the uniq limit.
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

	// Under the default policy, first create two deduplicated kinds {👍, ❤}, then
	// lower reactions_limit to 1, simulating "existing state already over the
	// limit" (an admin lowering it afterwards / legacy data predating the uniq gate).
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

	// Re-sending your own existing reaction (no-op) must be allowed.
	if _, err := f.sendReaction(t, f.ownerID, "\U0001f44d"); err != nil {
		t.Fatalf("owner re-send own reaction on over-limit message: %v", err)
	}
	if _, err := f.sendReaction(t, f.memberID, "❤"); err != nil {
		t.Fatalf("member re-send own reaction on over-limit message: %v", err)
	}
	// A third user piling onto an existing kind must be allowed (no new kind introduced).
	if _, err := f.sendReaction(t, f.member2ID, "\U0001f44d"); err != nil {
		t.Fatalf("third user piles onto existing kind on over-limit message: %v", err)
	}
	// Introducing a new kind still yields REACTIONS_TOO_MANY.
	if _, err := f.sendReaction(t, f.member2ID, "\U0001f525"); err == nil || !strings.Contains(err.Error(), "REACTIONS_TOO_MANY") {
		t.Fatalf("new kind on over-limit message err = %v, want REACTIONS_TOO_MANY", err)
	}
}

func TestSendReactionTrimsVectorToPerUserMax(t *testing.T) {
	f := newReactionPolicyFixture(t, false)

	// A vector exceeding reactions_user_max_default keeps the newest tail items without erroring.
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
