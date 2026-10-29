package bots

import (
	"context"
	"strings"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// lastBotFatherReply 取 owner 收件箱里 BotFather 的最新一条消息（不发新消息）。
func lastBotFatherReply(t *testing.T, messages *memory.MessageStore, userID int64) domain.Message {
	t.Helper()
	list, err := messages.ListByUser(context.Background(), userID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.BotFatherUserID},
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	var latest domain.Message
	for _, msg := range list.Messages {
		if msg.From.ID == domain.BotFatherUserID && msg.ID > latest.ID {
			latest = msg
		}
	}
	if latest.ID == 0 {
		t.Fatal("no BotFather reply in history")
	}
	return latest
}

// botFatherPickerButtonTexts 返回 reply keyboard（输入框下方的 keyboard）里的按钮文本。
func botFatherPickerButtonTexts(t *testing.T, msg domain.Message) []string {
	t.Helper()
	if msg.ReplyMarkup == nil || msg.ReplyMarkup.Kind() != domain.MessageReplyMarkupKeyboard {
		t.Fatalf("reply %q does not carry a reply keyboard: %+v", msg.Body, msg.ReplyMarkup)
	}
	if !msg.ReplyMarkup.Resize || !msg.ReplyMarkup.SingleUse {
		t.Fatalf("reply keyboard flags = resize:%t single_use:%t, want both true",
			msg.ReplyMarkup.Resize, msg.ReplyMarkup.SingleUse)
	}
	var out []string
	for _, row := range msg.ReplyMarkup.Keyboard {
		for _, button := range row {
			if button.Type != domain.MarkupButtonText {
				t.Fatalf("picker button %q type = %q, want a plain text button", button.Text, button.Type)
			}
			out = append(out, button.Text)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// 选择 bot 的键盘是 reply keyboard：点按钮等于把 @username 当普通文本发给 BotFather。
func TestBotFatherSetNameUsesReplyKeyboard(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2020")
	bot := makeBot(t, svc, owner, "Button Bot", "button_name_bot")
	ctx := context.Background()

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setname")
	if picker.Body != "Choose a bot to change name." {
		t.Fatalf("/setname reply = %q, want the picker prompt only", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); len(got) != 1 || got[0] != "@button_name_bot" {
		t.Fatalf("/setname keyboard = %v, want [@button_name_bot]", got)
	}

	// 点击 reply keyboard 按钮 = 客户端发送该按钮文本。
	if reply := sendToBotFather(t, svc, messages, owner, "@button_name_bot"); reply != "OK. Send me the new name for your bot." {
		t.Fatalf("after picking a bot reply = %q", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "Renamed By Button"); reply != "Success! Name updated. /help" {
		t.Fatalf("setname reply = %q", reply)
	}
	if u, _, _ := users.ByID(ctx, bot.ID); u.FirstName != "Renamed By Button" {
		t.Fatalf("bot first_name = %q, want renamed", u.FirstName)
	}
}

// /setdescription 与 /setname 同一套键盘选择，文案对齐官方 BotFather。
func TestBotFatherSetDescriptionUsesReplyKeyboard(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2023")
	bot := makeBot(t, svc, owner, "Description Bot", "button_desc_bot")
	ctx := context.Background()

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setdescription")
	if picker.Body != "Choose a bot to change description." {
		t.Fatalf("/setdescription reply = %q", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); len(got) != 1 || got[0] != "@button_desc_bot" {
		t.Fatalf("/setdescription keyboard = %v", got)
	}

	const prompt = "OK. Send me the new description for the bot. People will see this description when they open a chat with your bot, in a block titled 'What can this bot do?'."
	if reply := sendToBotFather(t, svc, messages, owner, "@button_desc_bot"); reply != prompt {
		t.Fatalf("after picking a bot reply = %q", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "123"); reply != "Success! Description updated. You will be able to see the changes within a few minutes. /help" {
		t.Fatalf("setdescription reply = %q", reply)
	}
	info, err := svc.GetBotInfo(ctx, bot.ID, "")
	if err != nil || info.Description != "123" {
		t.Fatalf("stored description = %q, err=%v", info.Description, err)
	}
}

// /setabouttext 与 /setname、/setdescription 同一套键盘选择，文案对齐官方 BotFather。
func TestBotFatherSetAboutUsesReplyKeyboard(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2024")
	bot := makeBot(t, svc, owner, "About Bot", "button_about_bot")
	ctx := context.Background()

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setabouttext")
	if picker.Body != "Choose a bot to change the about section." {
		t.Fatalf("/setabouttext reply = %q", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); len(got) != 1 || got[0] != "@button_about_bot" {
		t.Fatalf("/setabouttext keyboard = %v", got)
	}

	const prompt = "OK. Send me the new 'About' text. People will see this text on the bot's profile page and it will be sent together with a link to your bot when they share it with someone."
	if reply := sendToBotFather(t, svc, messages, owner, "@button_about_bot"); reply != prompt {
		t.Fatalf("after picking a bot reply = %q", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "123"); reply != "Success! About section updated. /help" {
		t.Fatalf("setabouttext reply = %q", reply)
	}
	info, err := svc.GetBotInfo(ctx, bot.ID, "")
	if err != nil || info.About != "123" {
		t.Fatalf("stored about = %q, err=%v", info.About, err)
	}
}

// /setcommands 的键盘选择与格式文案对齐官方 BotFather。
func TestBotFatherSetCommandsUsesReplyKeyboard(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2025")
	bot := makeBot(t, svc, owner, "Commands Bot", "button_cmd_bot")
	ctx := context.Background()

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setcommands")
	if picker.Body != "Choose a bot to change the list of commands." {
		t.Fatalf("/setcommands reply = %q", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); len(got) != 1 || got[0] != "@button_cmd_bot" {
		t.Fatalf("/setcommands keyboard = %v", got)
	}

	const prompt = "OK. Send me a list of commands for your bot. Please use this format:\n\ncommand1 - Description\ncommand2 - Another description\n\nSend /empty to keep the list empty."
	if reply := sendToBotFather(t, svc, messages, owner, "@button_cmd_bot"); reply != prompt {
		t.Fatalf("after picking a bot reply = %q", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "comand1 tsddsdd\ncommand2 fdfdfddfd"); reply != botFatherCommandListInvalidText() {
		t.Fatalf("malformed list reply = %q", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "command1 - Description\ncommand2 - Another description"); reply != "Success! Command list updated. /help" {
		t.Fatalf("setcommands reply = %q", reply)
	}
	commands, err := svc.GetBotCommands(ctx, bot.ID)
	if err != nil || len(commands) != 2 || commands[0].Command != "command1" || commands[0].Description != "Description" {
		t.Fatalf("stored commands = %+v, err=%v", commands, err)
	}
}

// /token 也是键盘选择；token 以 code entity 返回，客户端里点一下就复制。
func TestBotFatherTokenUsesReplyKeyboard(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+2026")
	bot := makeBot(t, svc, owner, "Token Button Bot", "button_token_bot")
	ctx := context.Background()

	picker := latestBotFatherMessage(t, svc, messages, owner, "/token")
	if picker.Body != "Choose a bot to get Bot API access token." {
		t.Fatalf("/token reply = %q", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); len(got) != 1 || got[0] != "@button_token_bot" {
		t.Fatalf("/token keyboard = %v", got)
	}

	sendToBotFather(t, svc, messages, owner, "@button_token_bot")
	profile, found, err := bots.GetBot(ctx, bot.ID)
	if err != nil || !found || profile.TokenSecret == "" {
		t.Fatalf("bot profile: found=%v err=%v", found, err)
	}
	token := domain.FormatBotToken(bot.ID, profile.TokenSecret)
	want := "You can use this token to access HTTP API:\n" + token +
		"\n\nFor a description of the Bot API, see this page: https://core.telegram.org/bots/api"
	reply := lastBotFatherReply(t, messages, owner.ID)
	if reply.Body != want {
		t.Fatalf("token reply = %q, want %q", reply.Body, want)
	}
	copied := false
	for _, entity := range reply.Entities {
		if entity.Type == domain.MessageEntityCode && reply.Body[entity.Offset:entity.Offset+entity.Length] == token {
			copied = true
		}
	}
	if !copied {
		t.Fatalf("token reply entities = %+v, want a code entity around %q", reply.Entities, token)
	}
}

// 键盘两列：3 个 bot 排成 2 + 1。
func TestBotFatherPickerLaysOutTwoButtonsPerRow(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2027")
	for _, username := range []string{"row_one_bot", "row_two_bot", "row_three_bot"} {
		makeBot(t, svc, owner, "Row Bot", username)
	}

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setname")
	botFatherPickerButtonTexts(t, picker) // 校验类型与 resize/single_use
	rows := picker.ReplyMarkup.Keyboard
	if len(rows) != 2 || len(rows[0]) != 2 || len(rows[1]) != 1 {
		t.Fatalf("keyboard rows = %+v, want [2 1]", rows)
	}
	if rows[0][0].Text != "@row_one_bot" || rows[0][1].Text != "@row_two_bot" || rows[1][0].Text != "@row_three_bot" {
		t.Fatalf("keyboard = %+v, want creation order", rows)
	}
}

// /setinline：键盘选择 + 官方 placeholder 提示。
func TestBotFatherSetInlineUsesReplyKeyboard(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+2028")
	bot := makeBot(t, svc, owner, "Inline Bot", "button_inline_bot")
	ctx := context.Background()

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setinline")
	if picker.Body != "Choose a bot to change inline queries status." {
		t.Fatalf("/setinline reply = %q", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); len(got) != 1 || got[0] != "@button_inline_bot" {
		t.Fatalf("/setinline keyboard = %v", got)
	}
	const prompt = "This will enable inline queries for your bot. https://core.telegram.org/bots/inline. Use /empty to disable.\n\nTo enable inline mode, please send me the placeholder message for queries to your bot. People will see this placeholder whenever they type @button_inline_bot in the message field – in any chat. Tell them what they can get from your bot (e.g., Search GIFs...)."
	if reply := sendToBotFather(t, svc, messages, owner, "@button_inline_bot"); reply != prompt {
		t.Fatalf("after picking a bot reply = %q", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "Search GIFs"); reply != "Success! Inline settings updated. /help" {
		t.Fatalf("setinline reply = %q", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.InlinePlaceholder != "Search GIFs" {
		t.Fatalf("inline placeholder = %q", p.InlinePlaceholder)
	}
}

// /setinlinegeo：只列 inline mode 已开启的 bot，显示当前值，Enable/Disable 键盘。
func TestBotFatherSetInlineGeoUsesReplyKeyboard(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+2029")
	inlineBot := makeBot(t, svc, owner, "Inline On", "geo_inline_bot")
	plainBot := makeBot(t, svc, owner, "Inline Off", "geo_plain_bot")
	ctx := context.Background()
	if _, err := svc.SetInlinePlaceholder(ctx, inlineBot.ID, "Search GIFs"); err != nil {
		t.Fatalf("enable inline: %v", err)
	}

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setinlinegeo")
	if picker.Body != "Choose a bot to change inline location request settings." {
		t.Fatalf("/setinlinegeo reply = %q", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); !containsString(got, "@geo_inline_bot") || containsString(got, "@geo_plain_bot") {
		t.Fatalf("/setinlinegeo keyboard = %v, want only the inline-enabled bot", got)
	}

	status := latestBotFatherMessage(t, svc, messages, owner, "@geo_inline_bot")
	const prompt = "If enabled, @geo_inline_bot will ask for permission to get the user's location each time they send a request.\nCurrent value is: DISABLED."
	if status.Body != prompt {
		t.Fatalf("setinlinegeo prompt = %q", status.Body)
	}
	if got := botFatherPickerButtonTexts(t, status); len(got) != 2 || got[0] != "Enable" || got[1] != "Disable" {
		t.Fatalf("setinlinegeo keyboard = %v, want [Enable Disable]", got)
	}

	if reply := sendToBotFather(t, svc, messages, owner, "Enable"); reply != "Success! Inline location request settings updated. New status is ENABLED /help" {
		t.Fatalf("setinlinegeo enable reply = %q", reply)
	}
	if p, _, _ := bots.GetBot(ctx, inlineBot.ID); !p.InlineGeo {
		t.Fatal("inline_geo = false, want enabled")
	}
	if p, _, _ := bots.GetBot(ctx, plainBot.ID); p.InlineGeo {
		t.Fatal("the non-inline bot was touched")
	}
}

// /setjoingroups：显示当前状态 + Enable/Disable 键盘。
func TestBotFatherSetJoinGroupsUsesReplyKeyboard(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+2030")
	bot := makeBot(t, svc, owner, "Join Bot", "button_join_bot")
	ctx := context.Background()

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setjoingroups")
	if picker.Body != "Choose a bot to change group membership settings." {
		t.Fatalf("/setjoingroups reply = %q", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); len(got) != 1 || got[0] != "@button_join_bot" {
		t.Fatalf("/setjoingroups keyboard = %v", got)
	}

	const enabledPrompt = "'Enable' - bot can be added to groups.\n'Disable' - block group invitations, the bot can't be added to groups.\nCurrent status is: ENABLED"
	status := latestBotFatherMessage(t, svc, messages, owner, "@button_join_bot")
	if status.Body != enabledPrompt {
		t.Fatalf("setjoingroups prompt = %q", status.Body)
	}
	if got := botFatherPickerButtonTexts(t, status); len(got) != 2 || got[0] != "Enable" || got[1] != "Disable" {
		t.Fatalf("setjoingroups keyboard = %v, want [Enable Disable]", got)
	}

	if reply := sendToBotFather(t, svc, messages, owner, "Disable"); reply != "Success! The new status is: DISABLED. /help" {
		t.Fatalf("setjoingroups disable reply = %q", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); !p.Nochats {
		t.Fatal("nochats = false, want true after disable")
	}

	sendToBotFather(t, svc, messages, owner, "/setjoingroups")
	disabledStatus := latestBotFatherMessage(t, svc, messages, owner, "@button_join_bot")
	const disabledPrompt = "'Enable' - bot can be added to groups.\n'Disable' - block group invitations, the bot can't be added to groups.\nCurrent status is: DISABLED"
	if disabledStatus.Body != disabledPrompt {
		t.Fatalf("setjoingroups prompt after disable = %q", disabledStatus.Body)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "Enable"); reply != "Success! The new status is: ENABLED. /help" {
		t.Fatalf("setjoingroups enable reply = %q", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.Nochats {
		t.Fatal("nochats = true, want false after enable")
	}
}

// /setprivacy：显示当前状态 + Enable/Disable 键盘。
func TestBotFatherSetPrivacyUsesReplyKeyboard(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+2031")
	bot := makeBot(t, svc, owner, "Privacy Bot", "button_privacy_bot")
	ctx := context.Background()

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setprivacy")
	if picker.Body != "Choose a bot to change group messages settings." {
		t.Fatalf("/setprivacy reply = %q", picker.Body)
	}
	if got := botFatherPickerButtonTexts(t, picker); len(got) != 1 || got[0] != "@button_privacy_bot" {
		t.Fatalf("/setprivacy keyboard = %v", got)
	}

	const enabledPrompt = "'Enable' - your bot will only receive messages that either start with the '/' symbol or mention the bot by username.\n'Disable' - your bot will receive all messages that people send to groups.\nCurrent status is: ENABLED"
	status := latestBotFatherMessage(t, svc, messages, owner, "@button_privacy_bot")
	if status.Body != enabledPrompt {
		t.Fatalf("setprivacy prompt = %q", status.Body)
	}
	if got := botFatherPickerButtonTexts(t, status); len(got) != 2 || got[0] != "Enable" || got[1] != "Disable" {
		t.Fatalf("setprivacy keyboard = %v, want [Enable Disable]", got)
	}

	if reply := sendToBotFather(t, svc, messages, owner, "Disable"); reply != "Success! The new status is: DISABLED. /help" {
		t.Fatalf("setprivacy disable reply = %q", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); !p.ChatHistory {
		t.Fatal("chat_history = false, want true after disabling privacy")
	}

	sendToBotFather(t, svc, messages, owner, "/setprivacy")
	disabledStatus := latestBotFatherMessage(t, svc, messages, owner, "@button_privacy_bot")
	const disabledPrompt = "'Enable' - your bot will only receive messages that either start with the '/' symbol or mention the bot by username.\n'Disable' - your bot will receive all messages that people send to groups.\nCurrent status is: DISABLED"
	if disabledStatus.Body != disabledPrompt {
		t.Fatalf("setprivacy prompt after disable = %q", disabledStatus.Body)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "Enable"); reply != "Success! The new status is: ENABLED. /help" {
		t.Fatalf("setprivacy enable reply = %q", reply)
	}
	if p, _, _ := bots.GetBot(ctx, bot.ID); p.ChatHistory {
		t.Fatal("chat_history = true, want false after enabling privacy")
	}
}

// 键盘里只能出现 owner 自己的 bot；手输别人的用户名也选不中。
func TestBotFatherPickerKeyboardOnlyListsOwnBots(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2021")
	other := newOwner(t, users, "+2022")
	makeBot(t, svc, owner, "Own Bot", "picker_own_bot")
	makeBot(t, svc, other, "Other Bot", "picker_other_bot")

	picker := latestBotFatherMessage(t, svc, messages, owner, "/setname")
	if got := botFatherPickerButtonTexts(t, picker); !containsString(got, "@picker_own_bot") || containsString(got, "@picker_other_bot") {
		t.Fatalf("/setname keyboard = %v, want only the owner's bot", got)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "@picker_other_bot"); reply != "I don't see that bot among yours. Send the username of one of your bots, or /cancel." {
		t.Fatalf("foreign bot reply = %q", reply)
	}
}

// /setinlinegeo 选不出 bot 时必须清掉上一条命令的步骤：否则用户看到「没有 inline
// bot」后随手发的下一条文本，会被旧步骤当成自己的输入（曾把 /setname 的收值文本
// 直接写成 bot 名字）。
func TestBotFatherInlineGeoPickerDropsStaleStep(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2032")
	bot := makeBot(t, svc, owner, "Stale Step Bot", "stale_step_bot")
	ctx := context.Background()

	sendToBotFather(t, svc, messages, owner, "/setname")
	sendToBotFather(t, svc, messages, owner, "@stale_step_bot") // 进入 /setname 收值步骤

	if reply := sendToBotFather(t, svc, messages, owner, "/setinlinegeo"); reply != "You don't have any bots with inline mode enabled. Use /setinline to enable it first." {
		t.Fatalf("/setinlinegeo reply = %q", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "Renamed By Accident"); reply != "I can only help you create and manage bots. Send /help for a list of commands." {
		t.Fatalf("follow-up reply = %q, want the no-active-command notice", reply)
	}
	if u, _, _ := users.ByID(ctx, bot.ID); u.FirstName != "Stale Step Bot" {
		t.Fatalf("bot name = %q, want untouched", u.FirstName)
	}
}

// With a public Bot API URL configured, BotFather tells the owner where to
// send requests next to the token; without one the reply is unchanged.
func TestBotFatherTokenShowsConfiguredBotAPIURL(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	WithBotAPIURL(" https://api.example.com/ ")(svc)
	owner := newOwner(t, users, "+2027")
	makeBot(t, svc, owner, "Hint Bot", "hint_url_bot")

	latestBotFatherMessage(t, svc, messages, owner, "/token")
	sendToBotFather(t, svc, messages, owner, "@hint_url_bot")
	reply := lastBotFatherReply(t, messages, owner.ID)
	if !strings.Contains(reply.Body, "\n\nBot API on this server: https://api.example.com/bot<token>/<method>\n\n") {
		t.Fatalf("token reply = %q, want the configured Bot API URL", reply.Body)
	}
}
