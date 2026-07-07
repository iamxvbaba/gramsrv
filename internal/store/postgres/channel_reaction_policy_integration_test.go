package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

type reactionPolicyTestEnv struct {
	channels  *ChannelStore
	channelID int64
	messageID int
	ownerID   int64
	memberID  int64
	member2ID int64
}

func newReactionPolicyTestEnv(t *testing.T, broadcast bool) reactionPolicyTestEnv {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 91, Phone: "+1892" + suffix + "01", FirstName: "PolicyOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 92, Phone: "+1892" + suffix + "02", FirstName: "PolicyMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	member2, err := users.Create(ctx, domain.User{AccessHash: 93, Phone: "+1892" + suffix + "03", FirstName: "PolicyMember2"})
	if err != nil {
		t.Fatalf("create member2: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID, member2.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Reaction Policy " + suffix,
		Broadcast:     broadcast,
		Megagroup:     !broadcast,
		MemberUserIDs: []int64{member.ID, member2.ID},
		Date:          1700001000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  91_001,
		Message:   "react to this",
		Date:      1700001001,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	return reactionPolicyTestEnv{
		channels:  channels,
		channelID: channelID,
		messageID: sent.Message.ID,
		ownerID:   owner.ID,
		memberID:  member.ID,
		member2ID: member2.ID,
	}
}

func (e reactionPolicyTestEnv) react(t *testing.T, userID int64, emoticons ...string) (domain.ChannelMessageReactionsResult, error) {
	t.Helper()
	reactions := make([]domain.MessageReaction, 0, len(emoticons))
	for _, emoticon := range emoticons {
		reactions = append(reactions, domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: emoticon})
	}
	return e.reactDomain(t, userID, reactions...)
}

func (e reactionPolicyTestEnv) reactDomain(t *testing.T, userID int64, reactions ...domain.MessageReaction) (domain.ChannelMessageReactionsResult, error) {
	t.Helper()
	return e.channels.SetChannelMessageReactions(context.Background(), domain.SetChannelMessageReactionsRequest{
		UserID:    userID,
		ChannelID: e.channelID,
		MessageID: e.messageID,
		Reactions: reactions,
		Date:      1700001002,
	})
}

func TestChannelStoreReactionPolicyEnforcedOnWrite(t *testing.T) {
	env := newReactionPolicyTestEnv(t, false)
	ctx := context.Background()

	if _, err := env.channels.SetAvailableReactions(ctx, env.ownerID, env.channelID, domain.ChannelReactionPolicy{
		Type:      domain.ChannelReactionPolicySome,
		Emoticons: []string{"\U0001f44d"},
	}); err != nil {
		t.Fatalf("set whitelist policy: %v", err)
	}
	if _, err := env.react(t, env.memberID, "❤"); !errors.Is(err, domain.ErrReactionInvalid) {
		t.Fatalf("off-whitelist reaction err = %v, want ErrReactionInvalid", err)
	}
	if _, err := env.react(t, env.memberID, "\U0001f44d"); err != nil {
		t.Fatalf("whitelisted reaction: %v", err)
	}

	if _, err := env.channels.SetAvailableReactions(ctx, env.ownerID, env.channelID, domain.ChannelReactionPolicy{
		Type: domain.ChannelReactionPolicyNone,
	}); err != nil {
		t.Fatalf("set none policy: %v", err)
	}
	if _, err := env.react(t, env.ownerID, "\U0001f44d"); !errors.Is(err, domain.ErrReactionInvalid) {
		t.Fatalf("reaction under none policy err = %v, want ErrReactionInvalid", err)
	}
	// 策略收紧后撤销存量 reaction 必须仍然可行。
	if _, err := env.react(t, env.memberID); err != nil {
		t.Fatalf("retract reaction under none policy: %v", err)
	}
}

func TestChannelStoreCustomEmojiReactionPolicyRoundTrips(t *testing.T) {
	env := newReactionPolicyTestEnv(t, false)
	ctx := context.Background()
	const customDocumentID int64 = 8800007

	if _, err := env.channels.SetAvailableReactions(ctx, env.ownerID, env.channelID, domain.ChannelReactionPolicy{
		Type:           domain.ChannelReactionPolicySome,
		CustomEmojiIDs: []int64{customDocumentID},
	}); err != nil {
		t.Fatalf("set custom whitelist policy: %v", err)
	}
	if _, err := env.react(t, env.memberID, "\U0001f44d"); !errors.Is(err, domain.ErrReactionInvalid) {
		t.Fatalf("off-whitelist emoji err = %v, want ErrReactionInvalid", err)
	}
	res, err := env.reactDomain(t, env.memberID, domain.MessageReaction{
		Type:       domain.MessageReactionCustomEmoji,
		DocumentID: customDocumentID,
	})
	if err != nil {
		t.Fatalf("custom emoji reaction: %v", err)
	}
	if len(res.Reactions.Results) != 1 || res.Reactions.Results[0].Reaction.Type != domain.MessageReactionCustomEmoji || res.Reactions.Results[0].Reaction.DocumentID != customDocumentID {
		t.Fatalf("custom reaction aggregate = %+v, want document %d", res.Reactions.Results, customDocumentID)
	}
	list, err := env.channels.ListChannelMessageReactions(ctx, domain.ChannelMessageReactionsListRequest{
		UserID:    env.ownerID,
		ChannelID: env.channelID,
		MessageID: env.messageID,
		Reaction:  &domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: customDocumentID},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list custom reactions: %v", err)
	}
	if list.Count != 1 || len(list.Reactions) != 1 || list.Reactions[0].Reaction.DocumentID != customDocumentID {
		t.Fatalf("custom reactions list = %+v, want one document %d", list, customDocumentID)
	}
	var reactionValue string
	if err := env.channels.db.QueryRow(ctx, `
SELECT reaction_value FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND reaction_type = $3`, env.channelID, env.messageID, string(domain.MessageReactionCustomEmoji)).Scan(&reactionValue); err != nil {
		t.Fatalf("query custom reaction row: %v", err)
	}
	if reactionValue != "8800007" {
		t.Fatalf("reaction_value = %q, want custom document id string", reactionValue)
	}
}

func TestChannelStoreSetAvailableReactionsRefreshesRowCache(t *testing.T) {
	pool := testPool(t) // 未设 DSN 会 t.Skip
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 94, Phone: "+1892" + suffix + "04", FirstName: "PolicyCacheOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	rowCache := NewChannelRowCache(100)
	channels := NewChannelStore(pool, WithChannelRowCache(rowCache))
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Reaction Policy Cache " + suffix,
		Broadcast:     true,
		Date:          1700001010,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	if _, err := channels.GetChannel(ctx, owner.ID, channelID); err != nil {
		t.Fatalf("warm GetChannel: %v", err)
	}
	cached, ok := rowCache.get(channelID)
	if !ok {
		t.Fatalf("GetChannel should warm row cache")
	}
	if cached.ReactionPolicy.PaidEnabled {
		t.Fatalf("cached paid enabled before update = true, want false")
	}

	if _, err := channels.SetAvailableReactions(ctx, owner.ID, channelID, domain.ChannelReactionPolicy{
		Type:        domain.ChannelReactionPolicyAll,
		AllowCustom: true,
		Limit:       9,
		PaidEnabled: true,
	}); err != nil {
		t.Fatalf("set paid reaction policy: %v", err)
	}
	view, err := channels.GetChannel(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("get channel after set policy: %v", err)
	}
	if !view.Channel.ReactionPolicy.PaidEnabled || view.Channel.ReactionPolicy.Limit != 9 || !view.Channel.ReactionPolicy.AllowCustom {
		t.Fatalf("view policy after set = %+v, want paid+limit+allow_custom from write-through cache", view.Channel.ReactionPolicy)
	}
	cached, ok = rowCache.get(channelID)
	if !ok {
		t.Fatalf("row cache missing after set policy")
	}
	if !cached.ReactionPolicy.PaidEnabled || cached.ReactionPolicy.Limit != 9 || !cached.ReactionPolicy.AllowCustom {
		t.Fatalf("cached policy after set = %+v, want paid+limit+allow_custom", cached.ReactionPolicy)
	}
}

func TestChannelStoreUniqueReactionsLimitOnlyBlocksNewKinds(t *testing.T) {
	env := newReactionPolicyTestEnv(t, false)
	ctx := context.Background()

	// 默认策略下先造出 {👍, ❤}，再调低 reactions_limit 模拟存量超限。
	if _, err := env.react(t, env.ownerID, "\U0001f44d"); err != nil {
		t.Fatalf("seed owner reaction: %v", err)
	}
	if _, err := env.react(t, env.memberID, "❤"); err != nil {
		t.Fatalf("seed member reaction: %v", err)
	}
	if _, err := env.channels.SetAvailableReactions(ctx, env.ownerID, env.channelID, domain.ChannelReactionPolicy{
		Type:  domain.ChannelReactionPolicyAll,
		Limit: 1,
	}); err != nil {
		t.Fatalf("lower uniq limit: %v", err)
	}

	if _, err := env.react(t, env.ownerID, "\U0001f44d"); err != nil {
		t.Fatalf("owner re-send own reaction on over-limit message: %v", err)
	}
	if _, err := env.react(t, env.memberID, "❤"); err != nil {
		t.Fatalf("member re-send own reaction on over-limit message: %v", err)
	}
	if _, err := env.react(t, env.member2ID, "\U0001f44d"); err != nil {
		t.Fatalf("third user piles onto existing kind on over-limit message: %v", err)
	}
	if _, err := env.react(t, env.member2ID, "\U0001f525"); !errors.Is(err, domain.ErrReactionsTooMany) {
		t.Fatalf("new kind on over-limit message err = %v, want ErrReactionsTooMany", err)
	}
}

func TestChannelStoreReactionVectorTrimsToPerUserMax(t *testing.T) {
	env := newReactionPolicyTestEnv(t, false)

	// 超出 reactions_user_max_default 的向量保留尾部最新项，不报错。
	res, err := env.react(t, env.memberID, "\U0001f44d", "❤")
	if err != nil {
		t.Fatalf("send oversized reaction vector: %v", err)
	}
	if len(res.Reactions.Results) != 1 {
		t.Fatalf("results = %+v, want single trimmed reaction", res.Reactions.Results)
	}
	if res.Reactions.Results[0].Reaction.Emoticon != "❤" || res.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("kept reaction = %+v, want newest vector entry with chosen_order 1", res.Reactions.Results[0])
	}
}

func TestChannelStoreBroadcastReactionsAnonymousAndSkipUnread(t *testing.T) {
	env := newReactionPolicyTestEnv(t, true)
	ctx := context.Background()

	res, err := env.react(t, env.memberID, "\U0001f44d")
	if err != nil {
		t.Fatalf("send broadcast reaction: %v", err)
	}
	if len(res.Reactions.Results) != 1 || res.Reactions.Results[0].Count != 1 {
		t.Fatalf("broadcast results = %+v, want count-only aggregate", res.Reactions.Results)
	}
	if len(res.Reactions.Recent) != 0 {
		t.Fatalf("broadcast recent reactors = %+v, want anonymous (empty)", res.Reactions.Recent)
	}
	if res.Reactions.CanSeeList {
		t.Fatalf("broadcast can_see_list = true, want false")
	}
	var unreadRows int
	if err := env.channels.db.QueryRow(ctx, `
SELECT COUNT(*) FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND unread`, env.channelID, env.messageID).Scan(&unreadRows); err != nil {
		t.Fatalf("count unread reaction rows: %v", err)
	}
	if unreadRows != 0 {
		t.Fatalf("broadcast unread reaction rows = %d, want unread bookkeeping skipped", unreadRows)
	}
	dialogs, err := env.channels.GetChannelDialogs(ctx, env.ownerID, []int64{env.channelID})
	if err != nil {
		t.Fatalf("get owner dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].UnreadReactions != 0 {
		t.Fatalf("owner dialogs = %+v, want no unread reaction badge on broadcast", dialogs.Dialogs)
	}
}
