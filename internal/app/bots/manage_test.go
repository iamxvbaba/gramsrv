package bots

import (
	"context"
	"errors"
	"strings"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// makeBot creates an owned bot and returns its user.
func makeBot(t *testing.T, svc *Service, owner domain.User, name, username string) domain.User {
	t.Helper()
	u, _, err := svc.CreateBot(context.Background(), owner.ID, name, username)
	if err != nil {
		t.Fatalf("create bot %q: %v", username, err)
	}
	return u
}

func TestSetBotCommandsAndBump(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2000")
	bot := makeBot(t, svc, owner, "Cmd Bot", "cmd_test_bot")
	ctx := context.Background()

	before, _, _ := users.ByID(ctx, bot.ID)
	v1, err := svc.SetBotCommands(ctx, bot.ID, []domain.BotCommand{
		{Command: "/Start", Description: "begin", Ephemeral: true},
		{Command: "help", Description: "show help"},
	})
	if err != nil {
		t.Fatalf("set commands: %v", err)
	}
	if v1 <= before.BotInfoVersion {
		t.Fatalf("bot_info_version not bumped: before=%d after=%d", before.BotInfoVersion, v1)
	}
	got, err := svc.GetBotCommands(ctx, bot.ID)
	if err != nil {
		t.Fatalf("get commands: %v", err)
	}
	if len(got) != 2 || got[0].Command != "start" || !got[0].Ephemeral || got[1].Command != "help" || got[1].Ephemeral {
		t.Fatalf("commands = %+v, want normalized [start,help]", got)
	}

	// Invalid command name -> ErrBotCommandInvalid.
	if _, err := svc.SetBotCommands(ctx, bot.ID, []domain.BotCommand{{Command: "bad name!", Description: "x"}}); err != domain.ErrBotCommandInvalid {
		t.Fatalf("invalid command err = %v, want ErrBotCommandInvalid", err)
	}
	// Empty description -> invalid.
	if _, err := svc.SetBotCommands(ctx, bot.ID, []domain.BotCommand{{Command: "ok", Description: ""}}); err != domain.ErrBotCommandInvalid {
		t.Fatalf("empty desc err = %v, want ErrBotCommandInvalid", err)
	}
	// Clear.
	if _, err := svc.SetBotCommands(ctx, bot.ID, nil); err != nil {
		t.Fatalf("reset commands: %v", err)
	}
	if got, _ := svc.GetBotCommands(ctx, bot.ID); len(got) != 0 {
		t.Fatalf("after reset commands = %+v, want empty", got)
	}
}

func TestSetBotInfoFields(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2001")
	bot := makeBot(t, svc, owner, "Info Bot", "info_test_bot")
	ctx := context.Background()

	if _, err := svc.SetBotInfo(ctx, bot.ID, domain.BotInfoUpdate{
		SetName: true, Name: "Renamed Bot",
		SetAbout: true, About: "about line",
		SetDescription: true, Description: "what this bot does",
	}); err != nil {
		t.Fatalf("set bot info: %v", err)
	}
	name, about, description, err := svc.GetBotInfo(ctx, bot.ID)
	if err != nil {
		t.Fatalf("get bot info: %v", err)
	}
	if name != "Renamed Bot" || about != "about line" || description != "what this bot does" {
		t.Fatalf("bot info = name=%q about=%q desc=%q", name, about, description)
	}
	// name lands in users.first_name.
	u, _, _ := users.ByID(ctx, bot.ID)
	if u.FirstName != "Renamed Bot" || u.About != "about line" {
		t.Fatalf("user row = first_name=%q about=%q, want name/about persisted", u.FirstName, u.About)
	}
	// Empty name is invalid.
	if _, err := svc.SetBotInfo(ctx, bot.ID, domain.BotInfoUpdate{SetName: true, Name: "  "}); err != domain.ErrBotInfoInvalid {
		t.Fatalf("empty name err = %v, want ErrBotInfoInvalid", err)
	}
	// An all-empty update is invalid.
	if _, err := svc.SetBotInfo(ctx, bot.ID, domain.BotInfoUpdate{}); err != domain.ErrBotInfoInvalid {
		t.Fatalf("noop update err = %v, want ErrBotInfoInvalid", err)
	}
}

func TestSetBotMenuButton(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2002")
	bot := makeBot(t, svc, owner, "Menu Bot", "menu_test_bot")
	ctx := context.Background()

	if _, err := svc.SetBotMenuButton(ctx, bot.ID, domain.BotMenuButton{
		Type: domain.BotMenuButtonWebView, Text: "Open", URL: "https://example.com/app",
	}); err != nil {
		t.Fatalf("set menu button: %v", err)
	}
	btn, err := svc.GetBotMenuButton(ctx, bot.ID)
	if err != nil {
		t.Fatalf("get menu button: %v", err)
	}
	if btn.Type != domain.BotMenuButtonWebView || btn.Text != "Open" || btn.URL != "https://example.com/app" {
		t.Fatalf("menu button = %+v", btn)
	}
	// webview missing text/url is invalid.
	if _, err := svc.SetBotMenuButton(ctx, bot.ID, domain.BotMenuButton{Type: domain.BotMenuButtonWebView, URL: "https://x"}); err != domain.ErrBotMenuButtonInvalid {
		t.Fatalf("webview missing text err = %v, want ErrBotMenuButtonInvalid", err)
	}
	// The commands type clears text/url.
	if _, err := svc.SetBotMenuButton(ctx, bot.ID, domain.BotMenuButton{Type: domain.BotMenuButtonCommands, Text: "x", URL: "y"}); err != nil {
		t.Fatalf("set commands menu: %v", err)
	}
	if btn, _ := svc.GetBotMenuButton(ctx, bot.ID); btn.Type != domain.BotMenuButtonCommands || btn.Text != "" || btn.URL != "" {
		t.Fatalf("commands menu = %+v, want cleared text/url", btn)
	}
}

func TestSetInlinePlaceholder(t *testing.T) {
	svc, users, bots, _ := newTestService(t)
	owner := newOwner(t, users, "+2010")
	bot := makeBot(t, svc, owner, "Inline Bot", "inline_test_bot")
	ctx := context.Background()

	before, _, _ := users.ByID(ctx, bot.ID)
	version, err := svc.SetInlinePlaceholder(ctx, bot.ID, "Search things")
	if err != nil {
		t.Fatalf("set inline placeholder: %v", err)
	}
	if version <= before.BotInfoVersion {
		t.Fatalf("bot_info_version not bumped: before=%d after=%d", before.BotInfoVersion, version)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.InlinePlaceholder != "Search things" {
		t.Fatalf("inline placeholder = %q, want Search things", p.InlinePlaceholder)
	}
	if _, err := svc.SetInlinePlaceholder(ctx, bot.ID, strings.Repeat("x", domain.MaxBotInlinePlaceholderLen+1)); err != domain.ErrBotInlinePlaceholderInvalid {
		t.Fatalf("overlong placeholder err = %v, want ErrBotInlinePlaceholderInvalid", err)
	}
	if _, err := svc.SetInlinePlaceholder(ctx, bot.ID, ""); err != nil {
		t.Fatalf("clear inline placeholder: %v", err)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.InlinePlaceholder != "" {
		t.Fatalf("inline placeholder after clear = %q, want empty", p.InlinePlaceholder)
	}
}

func TestSetJoinGroupsAndPrivacy(t *testing.T) {
	svc, users, bots, _ := newTestService(t)
	owner := newOwner(t, users, "+2003")
	bot := makeBot(t, svc, owner, "Flag Bot", "flag_test_bot")
	ctx := context.Background()

	// joingroups disable → nochats=true。
	if _, err := svc.SetJoinGroups(ctx, bot.ID, false); err != nil {
		t.Fatalf("set join groups: %v", err)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); !p.Nochats {
		t.Fatalf("nochats = false, want true after disable join")
	}
	if _, err := svc.SetJoinGroups(ctx, bot.ID, true); err != nil {
		t.Fatalf("re-enable join: %v", err)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.Nochats {
		t.Fatalf("nochats = true, want false after enable join")
	}
	// privacy enable -> chat_history=false (privacy on = only receive commands).
	if _, err := svc.SetPrivacy(ctx, bot.ID, true); err != nil {
		t.Fatalf("set privacy: %v", err)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.ChatHistory {
		t.Fatalf("chat_history = true, want false when privacy enabled")
	}
	if _, err := svc.SetPrivacy(ctx, bot.ID, false); err != nil {
		t.Fatalf("disable privacy: %v", err)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); !p.ChatHistory {
		t.Fatalf("chat_history = false, want true when privacy disabled")
	}
	before, _, _ := users.ByID(ctx, bot.ID)
	version, err := svc.SetInlineGeo(ctx, bot.ID, true)
	if err != nil {
		t.Fatalf("set inline geo: %v", err)
	}
	if version <= before.BotInfoVersion {
		t.Fatalf("bot_info_version not bumped for inline geo: before=%d after=%d", before.BotInfoVersion, version)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); !p.InlineGeo {
		t.Fatalf("inline_geo = false, want true after enable")
	}
	if _, err := svc.SetInlineGeo(ctx, bot.ID, false); err != nil {
		t.Fatalf("disable inline geo: %v", err)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.InlineGeo {
		t.Fatalf("inline_geo = true, want false after disable")
	}
}

func TestOwnsBot(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2004")
	other := newOwner(t, users, "+2005")
	bot := makeBot(t, svc, owner, "Owned Bot", "owned_test_bot")
	ctx := context.Background()

	if owns, err := svc.OwnsBot(ctx, owner.ID, bot.ID); err != nil || !owns {
		t.Fatalf("owner OwnsBot = %v,%v, want true", owns, err)
	}
	if owns, _ := svc.OwnsBot(ctx, other.ID, bot.ID); owns {
		t.Fatalf("non-owner OwnsBot = true, want false")
	}
	// BotFather itself counts as owned by no one.
	if owns, _ := svc.OwnsBot(ctx, domain.BotFatherUserID, domain.BotFatherUserID); owns {
		t.Fatalf("BotFather self OwnsBot = true, want false")
	}
}

func TestBotFatherSetCommandsFlow(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+2006")
	bot := makeBot(t, svc, owner, "Flow Bot", "flow_test_bot")
	ctx := context.Background()

	if reply := sendToBotFather(t, svc, messages, owner, "/setcommands"); !strings.Contains(reply, "list of commands") {
		t.Fatalf("/setcommands reply = %q, want pick bot", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "@flow_test_bot"); !strings.Contains(reply, "list of commands") {
		t.Fatalf("choose reply = %q, want value prompt", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "start - Begin\nhelp - Show help"); !strings.Contains(reply, "Success") {
		t.Fatalf("set commands reply = %q, want success", reply)
	}
	got, _, _ := bots.GetBot(ctx, bot.ID)
	if len(got.Commands) != 2 || got.Commands[0].Command != "start" {
		t.Fatalf("stored commands = %+v, want [start,help]", got.Commands)
	}
	// Invalid format (no -) keeps the state and prompts a retry.
	sendToBotFather(t, svc, messages, owner, "/setcommands")
	sendToBotFather(t, svc, messages, owner, "@flow_test_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "noseparator"); !strings.Contains(reply, "list of commands is invalid") {
		t.Fatalf("invalid format reply = %q", reply)
	}
}

func TestBotFatherSetNameAndAboutFlow(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2007")
	bot := makeBot(t, svc, owner, "Name Bot", "name_test_bot")
	ctx := context.Background()

	sendToBotFather(t, svc, messages, owner, "/setname")
	sendToBotFather(t, svc, messages, owner, "@name_test_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "Brand New Name"); !strings.Contains(reply, "Success") {
		t.Fatalf("setname reply = %q", reply)
	}
	if u, _, _ := users.ByID(ctx, bot.ID); u.FirstName != "Brand New Name" {
		t.Fatalf("bot first_name = %q, want renamed", u.FirstName)
	}

	sendToBotFather(t, svc, messages, owner, "/setabouttext")
	sendToBotFather(t, svc, messages, owner, "@name_test_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "my about"); !strings.Contains(reply, "Success") {
		t.Fatalf("setabouttext reply = %q", reply)
	}
	if u, _, _ := users.ByID(ctx, bot.ID); u.About != "my about" {
		t.Fatalf("bot about = %q, want updated", u.About)
	}
}

func TestBotFatherSetJoinGroupsFlow(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+2008")
	bot := makeBot(t, svc, owner, "Join Bot", "join_test_bot")
	ctx := context.Background()

	sendToBotFather(t, svc, messages, owner, "/setjoingroups")
	sendToBotFather(t, svc, messages, owner, "@join_test_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "disable"); !strings.Contains(reply, "Success") {
		t.Fatalf("setjoingroups disable reply = %q", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); !p.Nochats {
		t.Fatalf("nochats = false, want true after disable")
	}
	// Non enable/disable input keeps the state and prompts.
	sendToBotFather(t, svc, messages, owner, "/setprivacy")
	sendToBotFather(t, svc, messages, owner, "@join_test_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "maybe"); !strings.Contains(reply, "enable") {
		t.Fatalf("bad toggle reply = %q, want hint", reply)
	}
}

func TestBotFatherSetInlineFlow(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+2011")
	bot := makeBot(t, svc, owner, "Inline Flow", "inline_flow_bot")
	ctx := context.Background()

	if reply := sendToBotFather(t, svc, messages, owner, "/setinline"); !strings.Contains(reply, "inline queries status") {
		t.Fatalf("/setinline reply = %q, want pick bot", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "@inline_flow_bot"); !strings.Contains(reply, "placeholder") {
		t.Fatalf("choose reply = %q, want placeholder prompt", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "Search inline stuff"); !strings.Contains(reply, "Success") {
		t.Fatalf("setinline reply = %q, want success", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.InlinePlaceholder != "Search inline stuff" {
		t.Fatalf("inline placeholder = %q", p.InlinePlaceholder)
	}

	sendToBotFather(t, svc, messages, owner, "/setinline")
	sendToBotFather(t, svc, messages, owner, "@inline_flow_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "/empty"); !strings.Contains(reply, "disabled") {
		t.Fatalf("setinline /empty reply = %q, want disabled", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.InlinePlaceholder != "" {
		t.Fatalf("inline placeholder after /empty = %q, want empty", p.InlinePlaceholder)
	}

	// /setinlinegeo lists only bots with inline mode enabled.
	if reply := sendToBotFather(t, svc, messages, owner, "/setinlinegeo"); !strings.Contains(reply, "inline mode enabled") {
		t.Fatalf("/setinlinegeo without inline bots reply = %q", reply)
	}
	sendToBotFather(t, svc, messages, owner, "/setinline")
	sendToBotFather(t, svc, messages, owner, "@inline_flow_bot")
	sendToBotFather(t, svc, messages, owner, "Search inline stuff")

	if reply := sendToBotFather(t, svc, messages, owner, "/setinlinegeo"); !strings.Contains(reply, "location request settings") {
		t.Fatalf("/setinlinegeo reply = %q, want pick bot", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "@inline_flow_bot"); !strings.Contains(reply, "location") {
		t.Fatalf("choose inline geo reply = %q, want location prompt", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "enable"); !strings.Contains(reply, "Success") {
		t.Fatalf("setinlinegeo enable reply = %q, want success", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); !p.InlineGeo {
		t.Fatalf("inline_geo = false, want enabled")
	}
	sendToBotFather(t, svc, messages, owner, "/setinlinegeo")
	sendToBotFather(t, svc, messages, owner, "@inline_flow_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "disable"); !strings.Contains(reply, "Success") {
		t.Fatalf("setinlinegeo disable reply = %q, want success", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.InlineGeo {
		t.Fatalf("inline_geo = true, want disabled")
	}
	if reply := sendToBotFather(t, svc, messages, owner, "/setinlinefeedback"); !strings.Contains(reply, "not supported yet") {
		t.Fatalf("/setinlinefeedback reply = %q, want explicit stub", reply)
	}
}

func TestRevokeBotTokenRevokesSessions(t *testing.T) {
	users := memory.NewUserStore()
	bots := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	rev := &captureRevoker{}
	svc := NewService(users, bots, messages)
	svc.SetRouterHooks(rev)
	owner := newOwner(t, users, "+2009")
	bot := makeBot(t, svc, owner, "Rev Bot", "rev_test_bot")
	ctx := context.Background()

	if _, err := svc.RevokeBotToken(ctx, owner.ID, bot.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if rev.botUserID != bot.ID {
		t.Fatalf("RevokeBotSessions called with %d, want %d", rev.botUserID, bot.ID)
	}
}

func TestDeleteBotFailsClosedWhenSessionRevocationFails(t *testing.T) {
	users := memory.NewUserStore()
	botStore := &countingBotStore{BotStore: memory.NewBotStore(users)}
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	revocationErr := errors.New("authorization store unavailable")
	rev := &captureRevoker{err: revocationErr}
	svc := NewService(users, botStore, messages)
	svc.SetRouterHooks(rev)
	owner := newOwner(t, users, "+2099")
	bot := makeBot(t, svc, owner, "Delete Guard Bot", "delete_guard_bot")

	if _, err := svc.DeleteBot(context.Background(), bot.ID); !errors.Is(err, domain.ErrBotSessionsNotRevoked) {
		t.Fatalf("DeleteBot error=%v, want ErrBotSessionsNotRevoked", err)
	}
	if botStore.deleteCalls != 0 {
		t.Fatalf("DeleteBotAccount calls=%d after failed session revocation", botStore.deleteCalls)
	}
	if _, found, err := botStore.GetBot(context.Background(), bot.ID); err != nil || !found {
		t.Fatalf("bot disappeared after failed revocation: found=%v err=%v", found, err)
	}

	rev.err = nil
	deleted, err := svc.DeleteBot(context.Background(), bot.ID)
	if err != nil {
		t.Fatalf("DeleteBot after revocation recovery: %v", err)
	}
	if botStore.deleteCalls != 1 || deleted.ID != bot.ID || !deleted.Deleted {
		t.Fatalf("deleted=%+v deleteCalls=%d", deleted, botStore.deleteCalls)
	}
}

func TestBotWriteAccessGrant(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2012")
	bot := makeBot(t, svc, owner, "Write Bot", "write_access_bot")
	ctx := context.Background()

	can, err := svc.CanSendMessage(ctx, owner.ID, bot.ID)
	if err != nil || can {
		t.Fatalf("CanSendMessage before allow = %v,%v, want false,nil", can, err)
	}
	created, err := svc.AllowSendMessage(ctx, owner.ID, bot.ID, true)
	if err != nil || !created {
		t.Fatalf("AllowSendMessage first = %v,%v, want true,nil", created, err)
	}
	can, err = svc.CanSendMessage(ctx, owner.ID, bot.ID)
	if err != nil || !can {
		t.Fatalf("CanSendMessage after allow = %v,%v, want true,nil", can, err)
	}
	created, err = svc.AllowSendMessage(ctx, owner.ID, bot.ID, true)
	if err != nil || created {
		t.Fatalf("AllowSendMessage repeat = %v,%v, want false,nil", created, err)
	}
}

func TestBotFatherHelpTextIsSectioned(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2010")

	reply := sendToBotFather(t, svc, messages, owner, "/help")
	// Section headings match the official ones; commands the official BotFather has but this server does not implement must be explicitly marked, not falsely claimed as supported.
	for _, want := range []string{
		"You can control me by sending these commands:",
		"Edit Bots\n",
		"Bot Settings\n",
		"Telegram Login\n",
		"/deletebot - delete a bot",
		"/setuserpic - change bot profile photo (not available on this server yet)",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("/help reply missing %q:\n%s", want, reply)
		}
	}
}

func TestBotFatherSetUserPicIsExplicitlyUnsupported(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2011")
	bot := makeBot(t, svc, owner, "Pic Bot", "pic_test_bot")

	if reply := sendToBotFather(t, svc, messages, owner, "/setuserpic"); !strings.Contains(reply, "not supported on this server yet") {
		t.Fatalf("/setuserpic reply = %q, want explicit stub", reply)
	}
	// An unsupported command must not leave dangling state.
	if reply := sendToBotFather(t, svc, messages, owner, "Some Name"); !strings.Contains(reply, "/help") {
		t.Fatalf("post-/setuserpic reply = %q, want fallback", reply)
	}
	// It must not touch any of the bot's settings either.
	if _, found, err := svc.bots.GetBot(context.Background(), bot.ID); err != nil || !found {
		t.Fatalf("bot profile missing: found=%v err=%v", found, err)
	}
}

func TestBotFatherDeleteBotRequiresConfirmation(t *testing.T) {
	users := memory.NewUserStore()
	botStore := &countingBotStore{BotStore: memory.NewBotStore(users)}
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	rev := &captureRevoker{}
	svc := NewService(users, botStore, messages)
	svc.SetRouterHooks(rev)
	owner := newOwner(t, users, "+2012")
	bot := makeBot(t, svc, owner, "Doomed Bot", "doomed_bot")
	ctx := context.Background()

	if reply := sendToBotFather(t, svc, messages, owner, "/deletebot"); !strings.Contains(reply, "Choose a bot to delete") {
		t.Fatalf("/deletebot reply = %q, want pick bot", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "@doomed_bot"); !strings.Contains(reply, "Are you sure") {
		t.Fatalf("choose reply = %q, want confirmation prompt", reply)
	}
	// No non-confirmation input may delete the bot.
	for _, noise := range []string{"", "no", "what?", "@doomed_bot"} {
		if reply := sendToBotFather(t, svc, messages, owner, noise); !strings.Contains(reply, "Are you sure") {
			t.Fatalf("input %q reply = %q, want confirmation prompt", noise, reply)
		}
		if botStore.deleteCalls != 0 {
			t.Fatalf("input %q triggered deletion (deleteCalls=%d)", noise, botStore.deleteCalls)
		}
	}
	if rev.botUserID != 0 {
		t.Fatalf("RevokeBotSessions called before confirmation: bot=%d", rev.botUserID)
	}
	if _, found, err := botStore.GetBot(ctx, bot.ID); err != nil || !found {
		t.Fatalf("bot disappeared before confirmation: found=%v err=%v", found, err)
	}

	// Only explicit confirmation deletes the bot and revokes its logged-in sessions along with it.
	if reply := sendToBotFather(t, svc, messages, owner, "confirm"); !strings.Contains(reply, "has been deleted") {
		t.Fatalf("confirm reply = %q, want deletion notice", reply)
	}
	if botStore.deleteCalls != 1 || rev.botUserID != bot.ID {
		t.Fatalf("deleteCalls=%d revoked=%d, want 1 and %d", botStore.deleteCalls, rev.botUserID, bot.ID)
	}
	// State has been reset: subsequent text no longer goes through the confirm branch.
	if reply := sendToBotFather(t, svc, messages, owner, "hello"); !strings.Contains(reply, "/help") {
		t.Fatalf("post-delete reply = %q, want fallback", reply)
	}
}

func TestBotFatherDeleteBotReportsRevocationFailure(t *testing.T) {
	users := memory.NewUserStore()
	botStore := &countingBotStore{BotStore: memory.NewBotStore(users)}
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	rev := &captureRevoker{err: errors.New("authorization store unavailable")}
	svc := NewService(users, botStore, messages)
	svc.SetRouterHooks(rev)
	owner := newOwner(t, users, "+2013")
	bot := makeBot(t, svc, owner, "Stuck Bot", "stuck_bot")

	sendToBotFather(t, svc, messages, owner, "/deletebot")
	sendToBotFather(t, svc, messages, owner, "@stuck_bot")
	reply := sendToBotFather(t, svc, messages, owner, "confirm")
	// If it was not deleted, say so; never falsely claim success.
	if !strings.Contains(reply, "Nothing was deleted") {
		t.Fatalf("confirm reply = %q, want honest failure", reply)
	}
	if botStore.deleteCalls != 0 {
		t.Fatalf("deleteCalls=%d after failed revocation", botStore.deleteCalls)
	}
	if _, found, err := botStore.GetBot(context.Background(), bot.ID); err != nil || !found {
		t.Fatalf("bot deleted despite failed revocation: found=%v err=%v", found, err)
	}
	// State preserved: a retry can continue (the same confirmation word takes effect once the revoker recovers).
	rev.err = nil
	if reply := sendToBotFather(t, svc, messages, owner, "confirm"); !strings.Contains(reply, "has been deleted") {
		t.Fatalf("retry reply = %q, want deletion notice", reply)
	}
}

func TestBotFatherDeleteBotUnsupportedByStore(t *testing.T) {
	svc, users, _, messages := newTestService(t) // pure memory store: no DeleteBotAccount
	owner := newOwner(t, users, "+2014")
	makeBot(t, svc, owner, "Sticky Bot", "sticky_bot")

	sendToBotFather(t, svc, messages, owner, "/deletebot")
	sendToBotFather(t, svc, messages, owner, "@sticky_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "confirm"); !strings.Contains(reply, "not supported on this server") {
		t.Fatalf("confirm reply = %q, want unsupported notice", reply)
	}
}

func TestBotFatherDeleteBotRejectsForeignBot(t *testing.T) {
	users := memory.NewUserStore()
	botStore := &countingBotStore{BotStore: memory.NewBotStore(users)}
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	svc := NewService(users, botStore, messages)
	svc.SetRouterHooks(&captureRevoker{})
	owner := newOwner(t, users, "+2015")
	other := newOwner(t, users, "+2016")
	makeBot(t, svc, owner, "Own Bot", "own_owner_bot")
	makeBot(t, svc, other, "Other Bot", "other_owner_bot")

	picker := latestBotFatherMessage(t, svc, messages, owner, "/deletebot")
	if !strings.Contains(picker.Body, "Choose a bot to delete") {
		t.Fatalf("/deletebot reply = %q, want picker prompt", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); !containsString(got, "@own_owner_bot") || containsString(got, "@other_owner_bot") {
		t.Fatalf("/deletebot buttons = %v, want only the owner's bot", got)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "@other_owner_bot"); !strings.Contains(reply, "among yours") {
		t.Fatalf("foreign bot reply = %q, want rejection", reply)
	}
	if botStore.deleteCalls != 0 {
		t.Fatalf("deleteCalls=%d after foreign bot attempt", botStore.deleteCalls)
	}
}

type captureRevoker struct {
	botUserID        int64
	pushedCommandsTo int64
	pushedCommands   []domain.BotCommand
	err              error
}

func (c *captureRevoker) RevokeBotSessions(_ context.Context, botUserID int64) error {
	c.botUserID = botUserID
	return c.err
}

func (c *captureRevoker) PushBotCommandsChanged(_ context.Context, botUserID int64, commands []domain.BotCommand) {
	c.pushedCommandsTo = botUserID
	c.pushedCommands = append([]domain.BotCommand(nil), commands...)
}

func (c *captureRevoker) PushStickerSetsChanged(context.Context, int64, domain.StickerSetKind) {}
