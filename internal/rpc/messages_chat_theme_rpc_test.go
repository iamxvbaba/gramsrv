package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/seed/appearance"
	"telesrv/internal/store/memory"
)

func TestMessagesSetChatThemePrivatePersistsServiceMessageAndUserFull(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 1001, Phone: "15550003101", FirstName: "Alice"})
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 1002, Phone: "15550003102", FirstName: "Bob"})
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	router := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Dialogs:  appdialogs.NewService(dialogStore),
		Messages: appmessages.NewService(messageStore, dialogStore),
	}, zaptest.NewLogger(t), clock.System)

	theme := "\U0001f338"
	if cts := appearance.Default().ChatThemes; len(cts) > 0 {
		theme = cts[0].Emoticon
	}
	set, err := router.onMessagesSetChatTheme(WithUserID(ctx, alice.ID), &tg.MessagesSetChatThemeRequest{
		Peer:  &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Theme: &tg.InputChatTheme{Emoticon: theme},
	})
	if err != nil {
		t.Fatalf("set chat theme: %v", err)
	}
	setAction := chatThemeActionFromUpdates(t, set)
	if got := setAction.Theme.(*tg.ChatTheme).Emoticon; got != theme {
		t.Fatalf("set action emoticon = %q, want %q", got, theme)
	}
	assertDialogTheme(t, dialogStore, alice.ID, bob.ID, theme)
	assertDialogTheme(t, dialogStore, bob.ID, alice.ID, theme)
	full, err := router.onUsersGetFullUser(WithUserID(ctx, alice.ID), &tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash})
	if err != nil {
		t.Fatalf("get full user after set: %v", err)
	}
	if gotTheme, ok := full.FullUser.GetTheme(); !ok || gotTheme.(*tg.ChatTheme).Emoticon != theme {
		t.Fatalf("full user theme = %#v ok=%v, want %q", gotTheme, ok, theme)
	}

	clear, err := router.onMessagesSetChatTheme(WithUserID(ctx, alice.ID), &tg.MessagesSetChatThemeRequest{
		Peer:  &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Theme: &tg.InputChatTheme{Emoticon: ""},
	})
	if err != nil {
		t.Fatalf("clear chat theme: %v", err)
	}
	clearAction := chatThemeActionFromUpdates(t, clear)
	if got := clearAction.Theme.(*tg.ChatTheme).Emoticon; got != "" {
		t.Fatalf("clear action emoticon = %q, want empty", got)
	}
	assertDialogTheme(t, dialogStore, alice.ID, bob.ID, "")
	assertDialogTheme(t, dialogStore, bob.ID, alice.ID, "")
	full, err = router.onUsersGetFullUser(WithUserID(ctx, alice.ID), &tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash})
	if err != nil {
		t.Fatalf("get full user after clear: %v", err)
	}
	if gotTheme, ok := full.FullUser.GetTheme(); ok {
		t.Fatalf("full user theme after clear = %#v, want absent", gotTheme)
	}
}

func TestMessagesSetChatThemeRejectsUnsupportedThemes(t *testing.T) {
	router := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)
	for _, req := range []*tg.MessagesSetChatThemeRequest{
		{Peer: &tg.InputPeerUser{UserID: 1000000002}, Theme: &tg.InputChatTheme{Emoticon: "\U0001f47b"}},
		{Peer: &tg.InputPeerUser{UserID: 1000000002}, Theme: &tg.InputChatThemeUniqueGift{Slug: "gift"}},
	} {
		_, err := router.onMessagesSetChatTheme(ctx, req)
		if err == nil || !strings.Contains(err.Error(), "THEME_INVALID") {
			t.Fatalf("set unsupported theme err = %v, want THEME_INVALID", err)
		}
	}
}

func TestMessagesSetChatWallPaperAcksPrivatePeerAndSupportsChannel(t *testing.T) {
	router := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)
	okReq := &tg.MessagesSetChatWallPaperRequest{
		Peer: &tg.InputPeerUser{UserID: 1000000002},
	}
	okReq.SetWallpaper(&tg.InputWallPaperNoFile{ID: 930000000000000000})
	if got, err := router.onMessagesSetChatWallPaper(ctx, okReq); err != nil {
		t.Fatalf("set chat wallpaper: %v", err)
	} else if updates, ok := got.(*tg.Updates); !ok || len(updates.Updates) != 0 || updates.Date == 0 {
		t.Fatalf("set chat wallpaper updates = %#v, want dated empty updates", got)
	}

	for _, req := range []*tg.MessagesSetChatWallPaperRequest{
		nil,
		{Peer: &tg.InputPeerEmpty{}},
		{Peer: &tg.InputPeerChannel{ChannelID: 10}},
	} {
		_, err := router.onMessagesSetChatWallPaper(ctx, req)
		if err == nil {
			t.Fatalf("set chat wallpaper with %#v succeeded, want error", req)
		}
	}

	userStore := memory.NewUserStore()
	alice, _ := userStore.Create(context.Background(), domain.User{AccessHash: 2001, Phone: "15550003201", FirstName: "Alice"})
	bob, _ := userStore.Create(context.Background(), domain.User{AccessHash: 2002, Phone: "15550003202", FirstName: "Bob"})
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore)
	created, err := channels.CreateChannel(context.Background(), alice.ID, domain.CreateChannelRequest{
		Title:         "Wallpaper Channel",
		Broadcast:     true,
		MemberUserIDs: []int64{bob.ID},
		Date:          1700000100,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	router = New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channels,
	}, zaptest.NewLogger(t), clock.System)
	settings := tg.WallPaperSettings{}
	settings.SetBackgroundColor(0)
	settings.SetSecondBackgroundColor(0x95c46a)
	settings.SetRotation(45)
	channelReq := &tg.MessagesSetChatWallPaperRequest{
		Peer: &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
	}
	channelReq.SetWallpaper(&tg.InputWallPaperNoFile{ID: 930000000000000001})
	channelReq.SetSettings(settings)
	got, err := router.onMessagesSetChatWallPaper(WithUserID(context.Background(), alice.ID), channelReq)
	if err != nil {
		t.Fatalf("set channel wallpaper: %v", err)
	}
	updates, ok := got.(*tg.Updates)
	if !ok {
		t.Fatalf("set channel wallpaper updates = %T, want *tg.Updates", got)
	}
	action := channelWallpaperActionFromUpdates(t, updates)
	actionWallpaper, ok := action.Wallpaper.(*tg.WallPaperNoFile)
	if !ok || actionWallpaper.ID != 930000000000000001 {
		t.Fatalf("service wallpaper = %T %#v, want wallPaperNoFile id", action.Wallpaper, action.Wallpaper)
	}
	peerWallpaper := channelPeerWallpaperFromUpdates(t, updates)
	if gotID := peerWallpaper.ID; gotID != 930000000000000001 {
		t.Fatalf("updatePeerWallpaper id = %d, want selected wallpaper id", gotID)
	}
	if color, ok := peerWallpaper.Settings.GetBackgroundColor(); !ok || color != 0 {
		t.Fatalf("background color = %d ok=%v, want explicit black", color, ok)
	}
	view, err := channels.GetChannel(context.Background(), alice.ID, created.Channel.ID)
	if err != nil {
		t.Fatalf("get channel after wallpaper: %v", err)
	}
	if view.Channel.Wallpaper == nil || !view.Channel.Wallpaper.NoFile || !view.Channel.Wallpaper.Settings.HasBackgroundColor {
		t.Fatalf("stored channel wallpaper = %#v, want no-file with explicit settings", view.Channel.Wallpaper)
	}
	full := tgChannelFull(view)
	fullWallpaper, ok := full.GetWallpaper()
	if !ok {
		t.Fatalf("channelFull wallpaper missing")
	}
	fullNoFile, ok := fullWallpaper.(*tg.WallPaperNoFile)
	if !ok || fullNoFile.ID != 930000000000000001 {
		t.Fatalf("channelFull wallpaper = %T %#v, want selected no-file", fullWallpaper, fullWallpaper)
	}

	clearReq := &tg.MessagesSetChatWallPaperRequest{Peer: channelReq.Peer}
	clearGot, err := router.onMessagesSetChatWallPaper(WithUserID(context.Background(), alice.ID), clearReq)
	if err != nil {
		t.Fatalf("clear channel wallpaper: %v", err)
	}
	clearUpdates, ok := clearGot.(*tg.Updates)
	if !ok {
		t.Fatalf("clear channel wallpaper updates = %T, want *tg.Updates", clearGot)
	}
	if channelPeerWallpaperPresent(clearUpdates) {
		t.Fatalf("clear updatePeerWallpaper carried wallpaper, want absent wallpaper")
	}
	view, err = channels.GetChannel(context.Background(), alice.ID, created.Channel.ID)
	if err != nil {
		t.Fatalf("get channel after clear wallpaper: %v", err)
	}
	if view.Channel.Wallpaper != nil {
		t.Fatalf("stored channel wallpaper after clear = %#v, want nil", view.Channel.Wallpaper)
	}
	if _, ok := tgChannelFull(view).GetWallpaper(); ok {
		t.Fatalf("channelFull wallpaper after clear present, want absent")
	}

	_, err = router.onMessagesSetChatWallPaper(WithUserID(context.Background(), bob.ID), channelReq)
	if err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("non-admin set channel wallpaper err = %v, want CHAT_ADMIN_REQUIRED", err)
	}
}

func channelWallpaperActionFromUpdates(t *testing.T, updates *tg.Updates) *tg.MessageActionSetChatWallPaper {
	t.Helper()
	for _, update := range updates.Updates {
		newMessage, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		service, ok := newMessage.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		action, ok := service.Action.(*tg.MessageActionSetChatWallPaper)
		if !ok {
			t.Fatalf("channel service action = %T, want MessageActionSetChatWallPaper", service.Action)
		}
		return action
	}
	t.Fatalf("updates = %#v, want UpdateNewChannelMessage wallpaper service action", updates.Updates)
	return nil
}

func channelPeerWallpaperFromUpdates(t *testing.T, updates *tg.Updates) *tg.WallPaperNoFile {
	t.Helper()
	for _, update := range updates.Updates {
		peerWallpaper, ok := update.(*tg.UpdatePeerWallpaper)
		if !ok {
			continue
		}
		wallpaper, ok := peerWallpaper.GetWallpaper()
		if !ok {
			t.Fatalf("updatePeerWallpaper missing wallpaper")
		}
		noFile, ok := wallpaper.(*tg.WallPaperNoFile)
		if !ok {
			t.Fatalf("updatePeerWallpaper wallpaper = %T, want WallPaperNoFile", wallpaper)
		}
		return noFile
	}
	t.Fatalf("updates = %#v, want UpdatePeerWallpaper", updates.Updates)
	return nil
}

func channelPeerWallpaperPresent(updates *tg.Updates) bool {
	for _, update := range updates.Updates {
		peerWallpaper, ok := update.(*tg.UpdatePeerWallpaper)
		if !ok {
			continue
		}
		_, ok = peerWallpaper.GetWallpaper()
		return ok
	}
	return false
}

func chatThemeActionFromUpdates(t *testing.T, updates tg.UpdatesClass) *tg.MessageActionSetChatTheme {
	t.Helper()
	got, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, update := range got.Updates {
		newMessage, ok := update.(*tg.UpdateNewMessage)
		if !ok {
			continue
		}
		service, ok := newMessage.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		action, ok := service.Action.(*tg.MessageActionSetChatTheme)
		if !ok {
			t.Fatalf("service action = %T, want MessageActionSetChatTheme", service.Action)
		}
		return action
	}
	t.Fatalf("updates = %#v, want UpdateNewMessage service action", got.Updates)
	return nil
}

func assertDialogTheme(t *testing.T, dialogs *memory.DialogStore, ownerID, peerID int64, want string) {
	t.Helper()
	list, err := dialogs.ListByPeers(context.Background(), ownerID, []domain.Peer{{Type: domain.PeerTypeUser, ID: peerID}})
	if err != nil {
		t.Fatalf("list dialogs for %d/%d: %v", ownerID, peerID, err)
	}
	if len(list.Dialogs) != 1 {
		t.Fatalf("dialogs for %d/%d = %d, want 1", ownerID, peerID, len(list.Dialogs))
	}
	if got := list.Dialogs[0].ThemeEmoticon; got != want {
		t.Fatalf("dialog theme for %d/%d = %q, want %q", ownerID, peerID, got, want)
	}
}
