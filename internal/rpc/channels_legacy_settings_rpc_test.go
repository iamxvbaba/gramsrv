package rpc

import (
	"context"
	"fmt"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestLegacyChannelSettingsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 81, Phone: "15550002181", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 82, Phone: "15550002182", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Legacy Settings Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	themeUpdates, err := r.onMessagesSetChatTheme(WithUserID(ctx, owner.ID), &tg.MessagesSetChatThemeRequest{Peer: peer})
	if err != nil {
		t.Fatalf("set chat theme channel peer: %v", err)
	}
	if len(themeUpdates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("set chat theme updates = %+v, want channel context", themeUpdates)
	}
	privateTheme, err := r.onMessagesSetChatTheme(WithUserID(ctx, owner.ID), &tg.MessagesSetChatThemeRequest{
		Peer:  &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Theme: &tg.InputChatThemeEmpty{},
	})
	if err != nil {
		t.Fatalf("set chat theme private peer: %v", err)
	}
	if len(privateTheme.(*tg.Updates).Updates) != 0 {
		t.Fatalf("private set chat theme updates = %+v, want empty compat ack", privateTheme)
	}

	setReactionsReq := &tg.MessagesSetChatAvailableReactionsRequest{
		Peer:               peer,
		AvailableReactions: &tg.ChatReactionsSome{Reactions: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}}},
	}
	setReactionsReq.SetReactionsLimit(8)
	reactionUpdates, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), setReactionsReq)
	if err != nil {
		t.Fatalf("set available reactions: %v", err)
	}
	if len(reactionUpdates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("set reactions updates = %+v, want channel state update", reactionUpdates)
	}
	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel after reactions: %v", err)
	}
	fullChannel := full.FullChat.(*tg.ChannelFull)
	reactions, ok := fullChannel.GetAvailableReactions()
	if !ok {
		t.Fatalf("full channel reactions missing after set")
	}
	some, ok := reactions.(*tg.ChatReactionsSome)
	if !ok || len(some.Reactions) != 1 {
		t.Fatalf("full channel reactions = %#v, want one explicit reaction", reactions)
	}
	if fullChannel.ReactionsLimit != 8 {
		t.Fatalf("full channel reactions limit = %d, want 8", fullChannel.ReactionsLimit)
	}
	if _, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), &tg.MessagesSetChatAvailableReactionsRequest{
		Peer:               peer,
		AvailableReactions: &tg.ChatReactionsSome{Reactions: make([]tg.ReactionClass, domain.MaxChannelReactionTypes+1)},
	}); err == nil {
		t.Fatalf("set too many reactions err = nil, want limit error")
	}

	noForwards, err := r.onMessagesToggleNoForwards(WithUserID(ctx, owner.ID), &tg.MessagesToggleNoForwardsRequest{
		Peer:    peer,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("toggle noforwards: %v", err)
	}
	if got := noForwards.(*tg.Updates).Chats[0].(*tg.Channel); !got.Noforwards {
		t.Fatalf("noforwards channel = %+v, want enabled", got)
	}
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  "protected content",
		RandomID: 8181,
	})
	if err != nil {
		t.Fatalf("send protected message: %v", err)
	}
	msg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if !msg.Noforwards {
		t.Fatalf("protected channel message = %+v, want noforwards inherited", msg)
	}
}

// TestBroadcastChannelAcceptsFullReactionCatalog 复现并锁定真机 bug：广播频道开启
// reactions 时，DrKLO 把「启用全部标准 reaction」发成显式 chatReactionsSome 列表
// （megagroup 走 chatReactionsAll），列表长度等于 getAvailableReactions 目录大小
// （当前 ~74）。此前 MaxChannelReactionItems=64 把它误判成 LIMIT_INVALID。修复后
// 任何不超过 MaxChannelReactionTypes 的列表都必须被接受。
func TestBroadcastChannelAcceptsFullReactionCatalog(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 91, Phone: "15550002191", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Broadcast Reactions",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	if channel.Megagroup {
		t.Fatalf("created channel = %+v, want broadcast (not megagroup)", channel)
	}
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	// 用一个明显超过旧上限(64)的目录大小，证明修复生效。每个 emoticon 取互不相同
	// 的合法短串即可（验证只关心非空且 rune 数 <= MaxChannelReactionEmoticonLength）。
	const catalogSize = 74
	if catalogSize <= 64 || catalogSize > domain.MaxChannelReactionTypes {
		t.Fatalf("test catalog size %d must exceed the old 64 cap and stay within %d",
			catalogSize, domain.MaxChannelReactionTypes)
	}
	reactions := make([]tg.ReactionClass, 0, catalogSize)
	for i := 0; i < catalogSize; i++ {
		reactions = append(reactions, &tg.ReactionEmoji{Emoticon: fmt.Sprintf("r%02d", i)})
	}
	setCatalogReq := &tg.MessagesSetChatAvailableReactionsRequest{
		Peer:               peer,
		AvailableReactions: &tg.ChatReactionsSome{Reactions: reactions},
	}
	setCatalogReq.SetReactionsLimit(11)
	updates, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), setCatalogReq)
	if err != nil {
		t.Fatalf("set full-catalog reactions on broadcast channel: %v", err)
	}
	if len(updates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("set reactions updates = %+v, want channel state update", updates)
	}

	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel after reactions: %v", err)
	}
	fullChannel := full.FullChat.(*tg.ChannelFull)
	stored, ok := fullChannel.GetAvailableReactions()
	if !ok {
		t.Fatalf("full channel reactions missing after set")
	}
	some, ok := stored.(*tg.ChatReactionsSome)
	if !ok || len(some.Reactions) != catalogSize {
		t.Fatalf("full channel reactions = %#v, want %d explicit reactions", stored, catalogSize)
	}
	if fullChannel.GetPaidReactionsAvailable() {
		t.Fatalf("full channel paid reactions = true, want false without paid_enabled flag")
	}
}

func TestSetChatAvailableReactionsPreservesOptionalFlags(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 101, Phone: "15550002201", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Broadcast Optional Reactions",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	initial := &tg.MessagesSetChatAvailableReactionsRequest{
		Peer:               peer,
		AvailableReactions: &tg.ChatReactionsAll{AllowCustom: true},
	}
	initial.SetReactionsLimit(7)
	initial.SetPaidEnabled(true)
	if _, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), initial); err != nil {
		t.Fatalf("set initial reaction policy: %v", err)
	}

	omitOptional := &tg.MessagesSetChatAvailableReactionsRequest{
		Peer: peer,
		AvailableReactions: &tg.ChatReactionsSome{Reactions: []tg.ReactionClass{
			&tg.ReactionEmoji{Emoticon: "\U0001f44d"},
		}},
	}
	if _, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), omitOptional); err != nil {
		t.Fatalf("set reaction policy without optional flags: %v", err)
	}
	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel after omitted flags: %v", err)
	}
	fullChannel := full.FullChat.(*tg.ChannelFull)
	if fullChannel.ReactionsLimit != 7 {
		t.Fatalf("reactions limit after omitted flag = %d, want preserved 7", fullChannel.ReactionsLimit)
	}
	if !fullChannel.GetPaidReactionsAvailable() {
		t.Fatalf("paid reactions after omitted flag = false, want preserved true")
	}

	disablePaid := &tg.MessagesSetChatAvailableReactionsRequest{
		Peer: peer,
		AvailableReactions: &tg.ChatReactionsSome{Reactions: []tg.ReactionClass{
			&tg.ReactionEmoji{Emoticon: "\U0001f44d"},
			&tg.ReactionEmoji{Emoticon: "\u2764"},
		}},
	}
	disablePaid.SetPaidEnabled(false)
	if _, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), disablePaid); err != nil {
		t.Fatalf("disable paid reactions without limit flag: %v", err)
	}
	full, err = r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel after paid disable: %v", err)
	}
	fullChannel = full.FullChat.(*tg.ChannelFull)
	if fullChannel.ReactionsLimit != 7 {
		t.Fatalf("reactions limit after paid-only update = %d, want preserved 7", fullChannel.ReactionsLimit)
	}
	if fullChannel.GetPaidReactionsAvailable() {
		t.Fatalf("paid reactions after explicit false = true, want false")
	}
}

func TestAndroidChannelReactionEditorProjectsDefaultEmojiAsDocuments(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 111, Phone: "15550002211", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	files := &fakeFiles{reactions: []domain.AvailableReaction{
		{Reaction: "\U0001f44d", ActivateAnimationID: 7101},
		{Reaction: "\U0001f525", ActivateAnimationID: 7102},
	}}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Files:    files,
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Android Reaction Projection",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	if _, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), &tg.MessagesSetChatAvailableReactionsRequest{
		Peer: peer,
		AvailableReactions: &tg.ChatReactionsSome{Reactions: []tg.ReactionClass{
			&tg.ReactionEmoji{Emoticon: "\U0001f44d"},
			&tg.ReactionEmoji{Emoticon: "\U0001f525"},
		}},
	}); err != nil {
		t.Fatalf("set reaction policy: %v", err)
	}

	desktopFull, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get desktop full channel: %v", err)
	}
	desktopSome := mustChannelFullSomeReactions(t, desktopFull)
	if emoji, ok := desktopSome.Reactions[0].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" {
		t.Fatalf("desktop reaction[0] = %T %+v, want reactionEmoji thumbs up", desktopSome.Reactions[0], desktopSome.Reactions[0])
	}

	androidCtx := WithClientInfo(WithUserID(ctx, owner.ID), ClientInfo{Type: ClientTypeAndroid, AppVersion: "12.8.1"})
	androidFull, err := r.onChannelsGetFullChannel(androidCtx, &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get android full channel: %v", err)
	}
	androidSome := mustChannelFullSomeReactions(t, androidFull)
	if doc, ok := androidSome.Reactions[0].(*tg.ReactionCustomEmoji); !ok || doc.DocumentID != 7101 {
		t.Fatalf("android reaction[0] = %T %+v, want reactionCustomEmoji 7101", androidSome.Reactions[0], androidSome.Reactions[0])
	}
	if doc, ok := androidSome.Reactions[1].(*tg.ReactionCustomEmoji); !ok || doc.DocumentID != 7102 {
		t.Fatalf("android reaction[1] = %T %+v, want reactionCustomEmoji 7102", androidSome.Reactions[1], androidSome.Reactions[1])
	}
}

func TestSetChatAvailableReactionsNormalizesDefaultReactionDocuments(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 112, Phone: "15550002212", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	files := &fakeFiles{reactions: []domain.AvailableReaction{
		{Reaction: "\U0001f44d", ActivateAnimationID: 7201},
	}}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Files:    files,
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Android Reaction Save",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	if _, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), &tg.MessagesSetChatAvailableReactionsRequest{
		Peer: peer,
		AvailableReactions: &tg.ChatReactionsSome{Reactions: []tg.ReactionClass{
			&tg.ReactionCustomEmoji{DocumentID: 7201},
		}},
	}); err != nil {
		t.Fatalf("set reaction policy with default document id: %v", err)
	}

	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel: %v", err)
	}
	some := mustChannelFullSomeReactions(t, full)
	if emoji, ok := some.Reactions[0].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" {
		t.Fatalf("stored reaction[0] = %T %+v, want normalized reactionEmoji thumbs up", some.Reactions[0], some.Reactions[0])
	}
}

func TestAvailableReactionDocumentMapsAreCached(t *testing.T) {
	ctx := context.Background()
	files := &countingAvailableReactionFiles{fakeFiles: &fakeFiles{reactions: []domain.AvailableReaction{
		{Reaction: "\U0001f44d", ActivateAnimationID: 7301},
	}}}
	r := &Router{deps: Deps{Files: files}}

	emojiToDoc, docToEmoji := r.availableReactionDocumentMaps(ctx)
	if got := emojiToDoc["\U0001f44d"]; got != 7301 {
		t.Fatalf("emoji->document map = %d, want 7301", got)
	}
	if got := docToEmoji[7301]; got != "\U0001f44d" {
		t.Fatalf("document->emoji map = %q, want thumbs up", got)
	}

	files.fakeFiles.reactions = append(files.fakeFiles.reactions, domain.AvailableReaction{
		Reaction:            "\U0001f525",
		ActivateAnimationID: 7302,
	})
	emojiToDoc, docToEmoji = r.availableReactionDocumentMaps(ctx)
	if files.calls != 1 {
		t.Fatalf("ListAvailableReactions calls = %d, want 1 cached load", files.calls)
	}
	if got := emojiToDoc["\U0001f525"]; got != 0 {
		t.Fatalf("cached emoji->document map unexpectedly saw later catalog mutation: %d", got)
	}
	if got := docToEmoji[7302]; got != "" {
		t.Fatalf("cached document->emoji map unexpectedly saw later catalog mutation: %q", got)
	}
}

type countingAvailableReactionFiles struct {
	*fakeFiles
	calls int
}

func (f *countingAvailableReactionFiles) ListAvailableReactions(ctx context.Context) ([]domain.AvailableReaction, error) {
	f.calls++
	return f.fakeFiles.ListAvailableReactions(ctx)
}

func mustChannelFullSomeReactions(t *testing.T, full *tg.MessagesChatFull) *tg.ChatReactionsSome {
	t.Helper()
	channelFull, ok := full.FullChat.(*tg.ChannelFull)
	if !ok {
		t.Fatalf("full chat = %T, want *tg.ChannelFull", full.FullChat)
	}
	reactions, ok := channelFull.GetAvailableReactions()
	if !ok {
		t.Fatalf("channel full reactions missing")
	}
	some, ok := reactions.(*tg.ChatReactionsSome)
	if !ok {
		t.Fatalf("channel full reactions = %T %+v, want *tg.ChatReactionsSome", reactions, reactions)
	}
	if len(some.Reactions) == 0 {
		t.Fatalf("channel full reactions empty")
	}
	return some
}
