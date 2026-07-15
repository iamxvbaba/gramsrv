package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	accountapp "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestQuickReplyRPCSaveListAndDeleteMessage(t *testing.T) {
	const userID int64 = 1000000001
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), userID), [8]byte{1}), 77)
	r, updates := newChatAutomationTestRouter(t)

	got, err := r.onMessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:               &tg.InputPeerSelf{},
		Message:            "Saved template",
		RandomID:           12345,
		QuickReplyShortcut: &tg.InputQuickReplyShortcut{Shortcut: "hello"},
	})
	if err != nil {
		t.Fatalf("onMessagesSendMessage quick reply: %v", err)
	}
	result, ok := got.(*tg.Updates)
	if !ok {
		t.Fatalf("result type = %T, want *tg.Updates", got)
	}
	var messageID int
	var shortcutID int
	var sawNew bool
	var sawMessageID bool
	for _, update := range result.Updates {
		switch u := update.(type) {
		case *tg.UpdateMessageID:
			sawMessageID = true
			messageID = u.ID
			if u.RandomID != 12345 {
				t.Fatalf("UpdateMessageID random_id = %d", u.RandomID)
			}
		case *tg.UpdateNewQuickReply:
			sawNew = true
			shortcutID = u.QuickReply.ShortcutID
			if u.QuickReply.Shortcut != "hello" {
				t.Fatalf("UpdateNewQuickReply shortcut = %q", u.QuickReply.Shortcut)
			}
		}
	}
	if !sawMessageID || !sawNew || messageID == 0 || shortcutID == 0 {
		t.Fatalf("updates = %#v, want updateMessageID and updateNewQuickReply", result.Updates)
	}
	if len(updates.events) != 1 || updates.events[0].Type != domain.UpdateEventNewQuickReply {
		t.Fatalf("recorded events = %+v", updates.events)
	}

	list, err := r.onMessagesGetQuickReplies(ctx, 0)
	if err != nil {
		t.Fatalf("onMessagesGetQuickReplies: %v", err)
	}
	replies, ok := list.(*tg.MessagesQuickReplies)
	if !ok || len(replies.QuickReplies) != 1 || len(replies.Messages) != 1 {
		t.Fatalf("quick replies = %#v", list)
	}

	deleted, err := r.onMessagesDeleteQuickReplyMessages(ctx, &tg.MessagesDeleteQuickReplyMessagesRequest{
		ShortcutID: shortcutID,
		ID:         []int{messageID},
	})
	if err != nil {
		t.Fatalf("onMessagesDeleteQuickReplyMessages: %v", err)
	}
	deleteUpdates, ok := deleted.(*tg.Updates)
	if !ok {
		t.Fatalf("delete result type = %T", deleted)
	}
	var sawDelete bool
	for _, update := range deleteUpdates.Updates {
		if u, ok := update.(*tg.UpdateDeleteQuickReplyMessages); ok {
			sawDelete = true
			if u.ShortcutID != shortcutID || len(u.Messages) != 1 || u.Messages[0] != messageID {
				t.Fatalf("delete update = %+v", u)
			}
		}
	}
	if !sawDelete {
		t.Fatalf("delete updates = %#v, want updateDeleteQuickReplyMessages", deleteUpdates.Updates)
	}
}

func TestBusinessChatLinkRPCs(t *testing.T) {
	const userID int64 = 1000000002
	ctx := WithUserID(context.Background(), userID)
	r, _ := newChatAutomationTestRouter(t)

	created, err := r.onAccountCreateBusinessChatLink(ctx, tg.InputBusinessChatLink{
		Message: "Prefilled message",
		Title:   "Support",
	})
	if err != nil {
		t.Fatalf("onAccountCreateBusinessChatLink: %v", err)
	}
	if created.Link == "" || created.Message != "Prefilled message" {
		t.Fatalf("created link = %+v", created)
	}
	slug := strings.TrimPrefix(created.Link, "https://telesrv.net/m/")
	if slug == created.Link || slug == "" {
		t.Fatalf("created link URL = %q, want telesrv.net/m slug", created.Link)
	}
	list, err := r.onAccountGetBusinessChatLinks(ctx)
	if err != nil || len(list.Links) != 1 {
		t.Fatalf("onAccountGetBusinessChatLinks len=%d err=%v", len(list.Links), err)
	}
	resolved, err := r.onAccountResolveBusinessChatLink(ctx, slug)
	if err != nil {
		t.Fatalf("onAccountResolveBusinessChatLink: %v", err)
	}
	peer, ok := resolved.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != userID || resolved.Message != "Prefilled message" {
		t.Fatalf("resolved = %+v", resolved)
	}
	list, err = r.onAccountGetBusinessChatLinks(ctx)
	if err != nil || len(list.Links) != 1 || list.Links[0].Views != 1 {
		t.Fatalf("post-resolve links = %+v err=%v", list, err)
	}
	if deleted, err := r.onAccountDeleteBusinessChatLink(ctx, slug); err != nil || !deleted {
		t.Fatalf("onAccountDeleteBusinessChatLink deleted=%v err=%v", deleted, err)
	}
}

func TestConnectedBusinessBotRPCFlow(t *testing.T) {
	const ownerID int64 = 1000000010
	const peerID int64 = 1000000011
	const botID int64 = 1000000012
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), ownerID), [8]byte{2}), 88)
	store := memory.NewPasswordStore()
	updates := &captureUpdates{state: domain.UpdateState{Pts: 20, Date: 1700000000}}
	users := mapUsersService{users: map[int64]domain.User{
		ownerID: {ID: ownerID, AccessHash: 101, FirstName: "Bob"},
		peerID:  {ID: peerID, AccessHash: 102, FirstName: "Alice"},
		botID:   {ID: botID, AccessHash: 103, FirstName: "Echo", Username: "echo_test_bot", Bot: true, BotInfoVersion: 1},
	}}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Account: accountapp.NewService(store, accountapp.WithBusinessAutomation(store)),
		Users:   users,
		Updates: updates,
	}, zaptest.NewLogger(t), clock.System)

	updateReq := &tg.AccountUpdateConnectedBotRequest{
		Bot:        &tg.InputUser{UserID: botID, AccessHash: 103},
		Recipients: tg.InputBusinessBotRecipients{ExcludeSelected: true},
	}
	updateReq.SetRights(tg.BusinessBotRights{Reply: true})
	if _, err := r.onAccountUpdateConnectedBot(ctx, updateReq); err != nil {
		t.Fatalf("onAccountUpdateConnectedBot: %v", err)
	}
	connected, err := r.onAccountGetConnectedBots(ctx)
	if err != nil {
		t.Fatalf("onAccountGetConnectedBots: %v", err)
	}
	if len(connected.ConnectedBots) != 1 || connected.ConnectedBots[0].BotID != botID || !connected.ConnectedBots[0].Rights.Reply {
		t.Fatalf("connected bots = %+v", connected.ConnectedBots)
	}
	botUser, ok := connected.Users[0].(*tg.User)
	if !ok || !botUser.Bot || !botUser.BotBusiness {
		t.Fatalf("connected bot user = %#v, want bot_business", connected.Users[0])
	}

	peerSettings, err := r.onMessagesGetPeerSettings(ctx, &tg.InputPeerUser{UserID: peerID, AccessHash: 102})
	if err != nil {
		t.Fatalf("onMessagesGetPeerSettings: %v", err)
	}
	if peerSettings.Settings.BusinessBotID != botID || !peerSettings.Settings.BusinessBotCanReply || peerSettings.Settings.BusinessBotPaused {
		t.Fatalf("peer settings before pause = %+v", peerSettings.Settings)
	}

	if ok, err := r.onAccountToggleConnectedBotPaused(ctx, &tg.AccountToggleConnectedBotPausedRequest{
		Peer:   &tg.InputPeerUser{UserID: peerID, AccessHash: 102},
		Paused: true,
	}); err != nil || !ok {
		t.Fatalf("onAccountToggleConnectedBotPaused = %v,%v", ok, err)
	}
	peerSettings, err = r.onMessagesGetPeerSettings(ctx, &tg.InputPeerUser{UserID: peerID, AccessHash: 102})
	if err != nil {
		t.Fatalf("onMessagesGetPeerSettings paused: %v", err)
	}
	if peerSettings.Settings.BusinessBotID != botID || !peerSettings.Settings.BusinessBotPaused || peerSettings.Settings.BusinessBotCanReply {
		t.Fatalf("peer settings paused = %+v", peerSettings.Settings)
	}

	if ok, err := r.onAccountDisablePeerConnectedBot(ctx, &tg.InputPeerUser{UserID: peerID, AccessHash: 102}); err != nil || !ok {
		t.Fatalf("onAccountDisablePeerConnectedBot = %v,%v", ok, err)
	}
	peerSettings, err = r.onMessagesGetPeerSettings(ctx, &tg.InputPeerUser{UserID: peerID, AccessHash: 102})
	if err != nil {
		t.Fatalf("onMessagesGetPeerSettings disabled: %v", err)
	}
	if peerSettings.Settings.BusinessBotID != 0 || peerSettings.Settings.BusinessBotCanReply || peerSettings.Settings.BusinessBotPaused {
		t.Fatalf("peer settings disabled = %+v", peerSettings.Settings)
	}
}

func TestConnectedBusinessBotDefaultsMissingRightsToReply(t *testing.T) {
	const ownerID int64 = 1000000110
	const peerID int64 = 1000000111
	const botID int64 = 1000000112
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), ownerID), [8]byte{3}), 89)
	store := memory.NewPasswordStore()
	users := mapUsersService{users: map[int64]domain.User{
		ownerID: {ID: ownerID, AccessHash: 101, FirstName: "Bob"},
		peerID:  {ID: peerID, AccessHash: 102, FirstName: "Alice"},
		botID:   {ID: botID, AccessHash: 103, FirstName: "Echo", Username: "echo_default_bot", Bot: true, BotInfoVersion: 1},
	}}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Account: accountapp.NewService(store, accountapp.WithBusinessAutomation(store)),
		Users:   users,
	}, zaptest.NewLogger(t), clock.System)

	if _, err := r.onAccountUpdateConnectedBot(ctx, &tg.AccountUpdateConnectedBotRequest{
		Bot:        &tg.InputUser{UserID: botID, AccessHash: 103},
		Recipients: tg.InputBusinessBotRecipients{ExcludeSelected: true},
	}); err != nil {
		t.Fatalf("onAccountUpdateConnectedBot missing rights: %v", err)
	}
	connected, err := r.onAccountGetConnectedBots(ctx)
	if err != nil {
		t.Fatalf("onAccountGetConnectedBots: %v", err)
	}
	if len(connected.ConnectedBots) != 1 || !connected.ConnectedBots[0].Rights.Reply {
		t.Fatalf("missing rights connected bots = %+v, want reply default", connected.ConnectedBots)
	}

	explicitEmpty := &tg.AccountUpdateConnectedBotRequest{
		Bot:        &tg.InputUser{UserID: botID, AccessHash: 103},
		Recipients: tg.InputBusinessBotRecipients{ExcludeSelected: true},
	}
	explicitEmpty.SetRights(tg.BusinessBotRights{})
	if _, err := r.onAccountUpdateConnectedBot(ctx, explicitEmpty); err != nil {
		t.Fatalf("onAccountUpdateConnectedBot explicit empty rights: %v", err)
	}
	connected, err = r.onAccountGetConnectedBots(ctx)
	if err != nil {
		t.Fatalf("onAccountGetConnectedBots explicit empty: %v", err)
	}
	if len(connected.ConnectedBots) != 1 || connected.ConnectedBots[0].Rights.Reply {
		t.Fatalf("explicit empty rights connected bots = %+v, want reply disabled", connected.ConnectedBots)
	}
}

func newChatAutomationTestRouter(t *testing.T) (*Router, *captureUpdates) {
	t.Helper()
	store := memory.NewPasswordStore()
	updates := &captureUpdates{state: domain.UpdateState{Pts: 10, Date: 1700000000}}
	return New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Account: accountapp.NewService(store, accountapp.WithBusinessAutomation(store)),
		Updates: updates,
	}, zaptest.NewLogger(t), clock.System), updates
}
