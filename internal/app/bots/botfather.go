package bots

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	telegramloginapp "telesrv/internal/app/telegramlogin"
	"telesrv/internal/branding"
	"telesrv/internal/domain"
)

// BotFather dialog state machine: each private message a user sends to BotFather enters OnPrivateMessage via the
// app/messages responder hook; the user's message has already been stored first, so here we only generate and
// write BotFather's reply (the full SendPrivateText chain: both boxes + events + outbox push).

const (
	botFatherCmdNewBot         = "newbot"
	botFatherCmdToken          = "token"
	botFatherCmdRevoke         = "revoke"
	botFatherCmdSetName        = "setname"
	botFatherCmdSetDescription = "setdescription"
	botFatherCmdSetAbout       = "setabouttext"
	botFatherCmdSetCommands    = "setcommands"
	botFatherCmdSetInline      = "setinline"
	botFatherCmdSetInlineGeo   = "setinlinegeo"
	botFatherCmdSetInlineFB    = "setinlinefeedback"
	botFatherCmdSetJoinGroups  = "setjoingroups"
	botFatherCmdSetPrivacy     = "setprivacy"
	botFatherCmdSetUserPic     = "setuserpic"
	botFatherCmdDeleteBot      = "deletebot"
	botFatherCmdSetLogin       = "setlogin"
	botFatherCmdLoginInfo      = "logininfo"
	botFatherCmdResetLogin     = "resetloginsecret"
	botFatherCmdDone           = "done"

	botFatherStepName     = "name"
	botFatherStepUsername = "username"
	botFatherStepChoose   = "choose"
	botFatherStepValue    = "value"
	botFatherStepConfirm  = "confirm"

	botFatherDraftBotID       = "bot_id"
	botFatherDraftBotUsername = "bot_username"

	maxTelegramLoginCommandsPerMessage = 32
)

// botFatherHelpText lists commands grouped by function like the official BotFather, rather than one flat long
// table: the readability of /help in the client depends directly on this layout. Commands the official BotFather
// has but this server does not (/setuserpic) are explicitly marked unsupported -- the help text does not promise
// what cannot be done; the extra Telegram Login commands this server adds are listed in their own section, so
// they do not mix with the official entries.
func botFatherHelpText() string {
	return `I can help you create and manage ` + branding.ProductName() + ` bots.

You can control me by sending these commands:

/newbot - create a new bot
/mybots - edit your bots

Edit Bots
/setname - change a bot's name
/setdescription - change bot description
/setabouttext - change bot about info
/setuserpic - change bot profile photo (not available on this server yet)
/setcommands - change the list of commands
/deletebot - delete a bot

Bot Settings
/token - get authorization token
/revoke - revoke bot access token
/setinline - toggle inline mode
/setinlinegeo - toggle inline location requests
/setinlinefeedback - change inline feedback settings
/setjoingroups - can your bot be added to groups?
/setprivacy - toggle privacy mode in groups

Telegram Login
/setlogin - configure Telegram Login for a bot
/logininfo - show a bot's Telegram Login configuration
/resetloginsecret - rotate a bot's OIDC Client Secret
/done - finish the active Telegram Login configuration

/cancel - cancel the current operation
/help - show this message`
}

// botReply is one reply from a built-in service bot. ReplyMarkup is an optional inline keyboard snapshot (used
// by @verifybot's button dialog); it is validated via domain.ValidateReplyMarkup before being stored.
type botReply struct {
	Text        string
	Entities    []domain.MessageEntity
	ReplyMarkup *domain.MessageReplyMarkup
	Media       *domain.MessageMedia
}

// HandlesBot reports whether the recipient is a built-in responder bot (messages.BotResponder implementation).
func (s *Service) HandlesBot(botUserID int64) bool {
	if s == nil {
		return false
	}
	if s.premium != nil && botUserID == s.premium.BotUserID() {
		return true
	}
	if botUserID == gramsrvOperatorBotUserID && len(s.gramsrvOperators) > 0 && s.gramsrvFreezes != nil {
		return true
	}
	switch botUserID {
	case domain.BotFatherUserID, domain.StickersBotUserID, domain.ChatBotUserID,
		domain.VerifyBotUserID, domain.VerifierBotUserID:
		return true
	default:
		return false
	}
}

// OnPrivateMessage handles private messages delivered to a built-in bot (messages.BotResponder implementation).
// msg is the recipient box row from the bot's perspective. Replies are generated asynchronously (not occupying
// the user's sendMessage RPC goroutine -- official bot replies arrive asynchronously anyway); a failure only logs
// and never affects the user's message itself.
func (s *Service) OnPrivateMessage(ctx context.Context, botUserID int64, msg domain.Message, session domain.ClientSessionMetadata) {
	if s == nil || s.messages == nil || !s.HandlesBot(botUserID) {
		return
	}
	userID := msg.From.ID
	if msg.From.Type != domain.PeerTypeUser || userID == 0 || userID == botUserID {
		return
	}
	switch botUserID {
	case domain.BotFatherUserID:
		go s.respondAsBotFather(userID, msg.Body)
	case domain.StickersBotUserID:
		go s.respondAsStickers(userID, msg)
	case domain.ChatBotUserID:
		go s.respondAsChatBot(userID, msg)
	case domain.VerifyBotUserID:
		go s.respondAsVerify(userID, msg)
	case domain.VerifierBotUserID:
		go s.respondAsVerifier(userID, msg)
	case gramsrvOperatorBotUserID:
		go s.respondAsGramsrvOperator(userID, msg.Body)
	default:
		if s.premium != nil && botUserID == s.premium.BotUserID() {
			go s.respondAsPremium(botUserID, userID, msg, session)
		}
	}
}

// respondAsBotFather generates and writes the BotFather reply (called by OnPrivateMessage inside a goroutine).
// Serialized per user by stripe lock: the state machine's Get->modify->Upsert/Delete RMW is therefore atomic and
// replies stay ordered, while different users run concurrently unaffected. ctx uses Background (detached from the
// already-returned user RPC) with a long timeout.
func (s *Service) respondAsBotFather(userID int64, body string) {
	mu := s.serviceBotReplyLock(domain.BotFatherUserID, userID)
	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reply := s.handleBotFather(ctx, userID, body)
	s.sendServiceBotReply(ctx, domain.BotFatherUserID, userID, reply)
}

func (s *Service) serviceBotReplyLock(botUserID, userID int64) *sync.Mutex {
	key := uint64(userID) ^ (uint64(botUserID) * 11400714819323198485)
	return &s.replyLocks[key%replyLockStripes]
}

func (s *Service) sendServiceBotReply(ctx context.Context, botUserID, userID int64, reply botReply) {
	_, _ = s.sendServiceBotReplyResult(ctx, botUserID, userID, reply)
}

func (s *Service) serviceBotRecipientBlocked(ctx context.Context, botUserID, userID int64) bool {
	if s == nil || s.blocker == nil {
		return false
	}
	blocked, err := s.blocker.IsBlocked(ctx, userID, botUserID)
	if err != nil {
		s.log.Warn("service bot: check block", zap.Int64("bot_user_id", botUserID), zap.Int64("user_id", userID), zap.Error(err))
		return false
	}
	return blocked
}

func (s *Service) sendServiceBotReplyResult(ctx context.Context, botUserID, userID int64, reply botReply) (domain.SendPrivateTextResult, bool) {
	if s == nil || s.messages == nil || (reply.Text == "" && reply.Media.IsZero()) {
		return domain.SendPrivateTextResult{}, false
	}
	markup := reply.ReplyMarkup
	if err := domain.ValidateReplyMarkup(markup); err != nil {
		// Keyboard validation must precede persistence (I9): a structurally invalid markup is never written to the
		// store, but the body is still sent -- the user at least receives the prompt text and is not completely cut
		// off by one bad button.
		s.log.Error("service bot: invalid reply markup",
			zap.Int64("bot_user_id", botUserID), zap.Int64("user_id", userID), zap.Error(err))
		markup = nil
	}
	if markup.IsZero() {
		markup = nil
	}
	res, err := s.messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:     botUserID,
		RecipientUserID:  userID,
		RandomID:         s.botReplyRandomID(),
		Message:          reply.Text,
		Entities:         serviceBotReplyEntities(reply.Text, reply.Entities),
		Media:            reply.Media,
		ReplyMarkup:      markup,
		Date:             int(s.now().Unix()),
		RecipientBlocked: s.serviceBotRecipientBlocked(ctx, botUserID, userID),
	})
	if err != nil {
		s.log.Error("service bot: send reply", zap.Int64("bot_user_id", botUserID), zap.Int64("user_id", userID), zap.Error(err))
		return domain.SendPrivateTextResult{}, false
	}
	return res, true
}

// botReplyRandomID constructs a non-zero idempotency key for a server reply (the (sender, random_id) unique index).
// All service-bot replies are unique per their own sender namespace -- using crypto/rand for a 64-bit random number
// (collision probability negligible), falling back to nanoseconds + a monotonic sequence when the entropy source fails.
func (s *Service) botReplyRandomID() int64 {
	if v, err := randomInt64(); err == nil && v != 0 {
		return v
	}
	v := s.now().UnixNano() + s.replySeq.Add(1)
	if v == 0 {
		v = 1
	}
	return v
}

// botFatherGlobalCommands are global commands handled as commands in any state. Other text starting with "/" (such
// as /empty, or the pasted first line of a "/start - Begin" command list) must be passed through to the state
// machine as raw content during a value step; otherwise /setcommands' /empty would be unreachable, and a
// slash-prefixed command list's first line would be truncated into the command name "start", silently destroying
// the whole flow.
var botFatherGlobalCommands = map[string]bool{
	"start": true, "help": true, "cancel": true, botFatherCmdDone: true,
	botFatherCmdNewBot: true, "mybots": true,
	botFatherCmdToken: true, botFatherCmdRevoke: true,
	botFatherCmdSetName: true, botFatherCmdSetDescription: true, botFatherCmdSetAbout: true,
	botFatherCmdSetCommands: true, botFatherCmdSetInline: true, botFatherCmdSetInlineGeo: true,
	botFatherCmdSetInlineFB: true, botFatherCmdSetJoinGroups: true, botFatherCmdSetPrivacy: true,
	botFatherCmdSetUserPic: true, botFatherCmdDeleteBot: true,
	botFatherCmdSetLogin: true, botFatherCmdLoginInfo: true, botFatherCmdResetLogin: true,
}

func (s *Service) handleBotFather(ctx context.Context, userID int64, text string) botReply {
	text = strings.TrimSpace(text)
	state, found, err := s.bots.GetBotChatState(ctx, domain.BotFatherUserID, userID)
	if err != nil {
		s.log.Error("botfather: get chat state", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	// Command interception: dispatch as a command only when not in a value step, or when the text is a known
	// global command. Non-global "/..." text during a value step (/empty, a command list's first line) must be
	// passed through to the state machine as a raw value.
	if cmd, ok := parseBotCommand(text); ok {
		inValueStep := found && state.Step == botFatherStepValue
		if !inValueStep || botFatherGlobalCommands[cmd] {
			return s.handleBotFatherCommand(ctx, userID, cmd)
		}
	}
	if text == "" {
		// Blank text / sticker / captionless media: when there is an active state, return the current step prompt;
		// when there is no state, stay silent (to avoid spamming on arbitrary non-text messages).
		if !found {
			return botReply{}
		}
		return s.stepPrompt(state)
	}
	if !found {
		return botReply{Text: "I can only help you create and manage bots. Send /help for a list of commands."}
	}
	switch {
	case state.Command == botFatherCmdNewBot && state.Step == botFatherStepName:
		return s.handleNewBotName(ctx, state, text)
	case state.Command == botFatherCmdNewBot && state.Step == botFatherStepUsername:
		return s.handleNewBotUsername(ctx, state, text)
	case state.Step == botFatherStepChoose:
		return s.handleChooseBot(ctx, state, text)
	case state.Step == botFatherStepConfirm:
		return s.handleBotConfirm(ctx, state, text)
	case state.Step == botFatherStepValue:
		return s.handleSetValue(ctx, state, text)
	default:
		// Unreachable dirty state: clear it and start over, so the user is not stuck.
		_ = s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID)
		return botReply{Text: "Something went wrong, I forgot what we were doing. Send /help for a list of commands."}
	}
}

// pickerCommands is the set of "choose a bot first" commands (after the choose step, dispatch by command).
var pickerPrompts = map[string]string{
	botFatherCmdToken:          "Choose a bot to get Bot API access token.",
	botFatherCmdRevoke:         "Choose a bot to revoke the token of. Send the bot's username:",
	botFatherCmdSetName:        "Choose a bot to change name.",
	botFatherCmdSetDescription: "Choose a bot to change description.",
	botFatherCmdSetAbout:       "Choose a bot to change the about section.",
	botFatherCmdSetCommands:    "Choose a bot to change the list of commands.",
	botFatherCmdSetInline:      "Choose a bot to change inline queries status.",
	botFatherCmdSetInlineGeo:   "Choose a bot to change inline location request settings.",
	botFatherCmdSetJoinGroups:  "Choose a bot to change group membership settings.",
	botFatherCmdSetPrivacy:     "Choose a bot to change group messages settings.",
	botFatherCmdDeleteBot:      "Choose a bot to delete. Send the bot's username:",
	botFatherCmdSetLogin:       "Choose a bot to configure Telegram Login for. Send the bot's username:",
	botFatherCmdLoginInfo:      "Choose a bot whose Telegram Login configuration you want to inspect:",
	botFatherCmdResetLogin:     "Choose a bot whose OIDC Client Secret you want to rotate:",
}

// startBotPicker lists the owner's bots and enters the choose step (shared by all commands that first require
// choosing a bot). Each bot is rendered as an @username text button in a reply keyboard (below the input box, not
// a message attachment); tapping it sends the client the username, which goes through exactly the same
// manual-entry path as handleChooseBot -- so no callback query is needed, and there is no forgeable/expirable
// button data. The keyboard is single_use: selecting one collapses it automatically.
func (s *Service) startBotPicker(ctx context.Context, userID int64, cmd string) botReply {
	owned, err := s.ownedBots(ctx, userID)
	if err != nil {
		s.log.Error("botfather: list bots", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	if len(owned) == 0 {
		// Do not clear state here: with no bots at all, the only thing that could be pending is a /newbot draft, so
		// leave it for the user to continue creating (bot-level steps will fail when they re-check owner themselves
		// and clean up on their own).
		return botReply{Text: "You don't have any bots yet. Use /newbot to create one."}
	}
	// /setinlinegeo only makes sense for bots with inline mode enabled, so the keyboard also lists only those bots.
	if cmd == botFatherCmdSetInlineGeo {
		owned = botFatherInlineBots(owned)
		if len(owned) == 0 {
			// If the command cannot start, the pending step must be cleared: otherwise, after the user sees "no
			// inline bot", the next text they casually send would be executed as that prior command's step input --
			// for example /setname's value collection would write that text as the bot name, or /deletebot's confirm
			// would still be awaiting confirmation.
			s.clearState(ctx, userID)
			return botReply{Text: "You don't have any bots with inline mode enabled. Use /setinline to enable it first."}
		}
	}
	if err := s.bots.UpsertBotChatState(ctx, domain.BotChatState{
		BotUserID: domain.BotFatherUserID,
		UserID:    userID,
		Command:   cmd,
		Step:      botFatherStepChoose,
	}); err != nil {
		s.log.Error("botfather: save chat state", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	reply := botReply{Text: pickerPrompts[cmd]}
	var buttons []domain.MarkupButton
	for _, b := range owned {
		if b.user.Username == "" {
			continue
		}
		buttons = append(buttons, domain.MarkupButton{
			Type: domain.MarkupButtonText,
			Text: "@" + b.user.Username,
		})
	}
	// Two per row: an owner usually has more than one bot, and two columns is half as tall as one.
	var rows [][]domain.MarkupButton
	for len(buttons) > 0 {
		n := min(2, len(buttons))
		rows = append(rows, buttons[:n])
		buttons = buttons[n:]
	}
	if len(rows) > 0 {
		reply.ReplyMarkup = &domain.MessageReplyMarkup{
			Type:      domain.MessageReplyMarkupKeyboard,
			Keyboard:  rows,
			Resize:    true,
			SingleUse: true,
		}
	}
	return reply
}

// botFatherInlineBots keeps only bots with inline mode enabled (a non-empty placeholder means enabled).
func botFatherInlineBots(owned []ownedBot) []ownedBot {
	out := make([]ownedBot, 0, len(owned))
	for _, b := range owned {
		if b.profile.InlinePlaceholder != "" {
			out = append(out, b)
		}
	}
	return out
}

// botFatherToggleKeyboard is a two-button Enable/Disable reply keyboard; tapping sends the corresponding text,
// going through exactly the manual-entry path already supported by applyToggle/applySetInlineGeo.
func botFatherToggleKeyboard() *domain.MessageReplyMarkup {
	return &domain.MessageReplyMarkup{
		Type: domain.MessageReplyMarkupKeyboard,
		Keyboard: [][]domain.MarkupButton{
			{
				{Type: domain.MarkupButtonText, Text: "Enable"},
				{Type: domain.MarkupButtonText, Text: "Disable"},
			},
		},
		Resize:    true,
		SingleUse: true,
	}
}

// stepPrompt returns the guidance text for the current dialog step (used as the fallback for empty input).
func (s *Service) stepPrompt(state domain.BotChatState) botReply {
	switch {
	case state.Command == botFatherCmdNewBot && state.Step == botFatherStepName:
		return botReply{Text: "Please choose a name for your bot, or /cancel."}
	case state.Command == botFatherCmdNewBot && state.Step == botFatherStepUsername:
		return botReply{Text: "Please send a username for your bot. It must end in `bot`. Or /cancel."}
	case state.Step == botFatherStepChoose:
		return botReply{Text: "Tap one of the buttons above, or send the username of one of your bots, or /cancel."}
	case state.Step == botFatherStepConfirm:
		return botReply{Text: botDeleteConfirmPrompt(state.Draft[botFatherDraftBotUsername])}
	case state.Step == botFatherStepValue:
		return botReply{Text: valuePrompt(state.Command, state.Draft[botFatherDraftBotUsername])}
	default:
		return botReply{Text: "Send /help for a list of commands."}
	}
}

func (s *Service) handleBotFatherCommand(ctx context.Context, userID int64, cmd string) botReply {
	switch cmd {
	case "start", "help":
		_ = s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID)
		return botReply{Text: botFatherHelpText()}
	case "cancel":
		state, found, err := s.bots.GetBotChatState(ctx, domain.BotFatherUserID, userID)
		if err != nil {
			s.log.Error("botfather: get chat state", zap.Int64("user_id", userID), zap.Error(err))
			return internalReply()
		}
		if !found {
			return botReply{Text: "No active command to cancel. I wasn't doing anything anyway."}
		}
		if err := s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID); err != nil {
			s.log.Error("botfather: delete chat state", zap.Int64("user_id", userID), zap.Error(err))
			return internalReply()
		}
		if state.Command == botFatherCmdSetLogin && state.Step == botFatherStepValue {
			return botReply{Text: "Telegram Login configuration closed. Changes that were already applied have been kept."}
		}
		return botReply{Text: "The command has been cancelled. Anything else I can do for you? Send /help for a list of commands."}
	case botFatherCmdDone:
		return s.finishTelegramLoginConfiguration(ctx, userID)
	case botFatherCmdNewBot:
		count, err := s.bots.CountBotsByOwner(ctx, userID)
		if err != nil {
			s.log.Error("botfather: count bots", zap.Int64("user_id", userID), zap.Error(err))
			return internalReply()
		}
		if count >= domain.MaxBotsPerOwner {
			return botReply{Text: fmt.Sprintf("That I cannot do. You have reached the limit of %d bots per account.", domain.MaxBotsPerOwner)}
		}
		if err := s.bots.UpsertBotChatState(ctx, domain.BotChatState{
			BotUserID: domain.BotFatherUserID,
			UserID:    userID,
			Command:   botFatherCmdNewBot,
			Step:      botFatherStepName,
		}); err != nil {
			s.log.Error("botfather: save chat state", zap.Int64("user_id", userID), zap.Error(err))
			return internalReply()
		}
		return botReply{Text: "Alright, a new bot. How are we going to call it? Please choose a name for your bot."}
	case "mybots":
		usernames, err := s.ownedBotUsernames(ctx, userID)
		if err != nil {
			s.log.Error("botfather: list bots", zap.Int64("user_id", userID), zap.Error(err))
			return internalReply()
		}
		if len(usernames) == 0 {
			return botReply{Text: "You don't have any bots yet. Use /newbot to create one."}
		}
		return botReply{Text: "Here are your bots:\n\n@" + strings.Join(usernames, "\n@")}
	case botFatherCmdToken, botFatherCmdRevoke, botFatherCmdDeleteBot,
		botFatherCmdSetName, botFatherCmdSetDescription, botFatherCmdSetAbout,
		botFatherCmdSetCommands, botFatherCmdSetInline, botFatherCmdSetInlineGeo,
		botFatherCmdSetJoinGroups, botFatherCmdSetPrivacy,
		botFatherCmdSetLogin, botFatherCmdLoginInfo, botFatherCmdResetLogin:
		return s.startBotPicker(ctx, userID, cmd)
	case "confirm":
		return s.handleBotFatherConfirmCommand(ctx, userID)
	case botFatherCmdSetInlineFB:
		_ = s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID)
		return botReply{Text: "Inline feedback settings are not supported yet. Use /setinline to enable or disable inline mode."}
	case botFatherCmdSetUserPic:
		_ = s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID)
		return botReply{Text: "Sorry, changing a bot's profile photo is not supported on this server yet. Send /help for a list of commands."}
	default:
		return botReply{Text: "Unrecognized command. Say what? Send /help for a list of commands."}
	}
}

// valuePrompts is the value-collection prompt for the value step after a bot is selected (by command).
func valuePrompt(cmd, username string) string {
	switch cmd {
	case botFatherCmdSetName:
		return "OK. Send me the new name for your bot."
	case botFatherCmdSetDescription:
		return "OK. Send me the new description for the bot. People will see this description when they open a chat with your bot, in a block titled 'What can this bot do?'."
	case botFatherCmdSetAbout:
		return "OK. Send me the new 'About' text. People will see this text on the bot's profile page and it will be sent together with a link to your bot when they share it with someone."
	case botFatherCmdSetCommands:
		return "OK. Send me a list of commands for your bot. Please use this format:\n\ncommand1 - Description\ncommand2 - Another description\n\nSend /empty to keep the list empty."
	case botFatherCmdSetInline:
		return fmt.Sprintf("This will enable inline queries for your bot. https://core.telegram.org/bots/inline. Use /empty to disable.\n\nTo enable inline mode, please send me the placeholder message for queries to your bot. People will see this placeholder whenever they type @%s in the message field – in any chat. Tell them what they can get from your bot (e.g., Search GIFs...).", username)
	case botFatherCmdSetInlineGeo:
		return fmt.Sprintf("If enabled, @%s will ask for permission to get the user's location each time they send a request.\n\nSend 'enable' or 'disable'.", username)
	case botFatherCmdSetJoinGroups:
		return "'Enable' - bot can be added to groups.\n'Disable' - block group invitations, the bot can't be added to groups.\n\nSend 'enable' or 'disable'."
	case botFatherCmdSetPrivacy:
		return "'Enable' - your bot will only receive messages that either start with the '/' symbol or mention the bot by username.\n'Disable' - your bot will receive all messages that people send to groups.\n\nSend 'enable' or 'disable'."
	case botFatherCmdSetLogin:
		return telegramLoginConfigurationPrompt(username)
	default:
		return "Send the new value, or /cancel."
	}
}

func inlineGeoPrompt(username string, enabled bool) string {
	status := "DISABLED"
	if enabled {
		status = "ENABLED"
	}
	return fmt.Sprintf("If enabled, @%s will ask for permission to get the user's location each time they send a request.\nCurrent value is: %s.", username, status)
}

func joinGroupsPrompt(allowed bool) string {
	status := "DISABLED"
	if allowed {
		status = "ENABLED"
	}
	return fmt.Sprintf("'Enable' - bot can be added to groups.\n'Disable' - block group invitations, the bot can't be added to groups.\nCurrent status is: %s", status)
}

func privacyPrompt(enabled bool) string {
	status := "DISABLED"
	if enabled {
		status = "ENABLED"
	}
	return fmt.Sprintf("'Enable' - your bot will only receive messages that either start with the '/' symbol or mention the bot by username.\n'Disable' - your bot will receive all messages that people send to groups.\nCurrent status is: %s", status)
}

func (s *Service) handleNewBotName(ctx context.Context, state domain.BotChatState, name string) botReply {
	if name == "" || len([]rune(name)) > domain.MaxBotNameLength {
		return botReply{Text: fmt.Sprintf("Sorry, the bot name must be 1-%d characters long. Please choose a different name.", domain.MaxBotNameLength)}
	}
	state.Step = botFatherStepUsername
	if state.Draft == nil {
		state.Draft = map[string]string{}
	}
	state.Draft["name"] = name
	if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
		s.log.Error("botfather: save chat state", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	return botReply{Text: "Good. Now let's choose a username for your bot. It must end in `bot`. Like this, for example: TetrisBot or tetris_bot."}
}

func (s *Service) handleNewBotUsername(ctx context.Context, state domain.BotChatState, username string) botReply {
	u, token, err := s.CreateBot(ctx, state.UserID, state.Draft["name"], username)
	switch {
	case errors.Is(err, domain.ErrBotUsernameInvalid):
		return botReply{Text: "Sorry, this username is invalid. A bot username must be 5-32 characters long, start with a letter, contain only Latin letters, digits and underscores, and end in 'bot' (e.g. tetris_bot)."}
	case errors.Is(err, domain.ErrUsernameOccupied):
		return botReply{Text: "Sorry, this username is already taken. Please try something different."}
	case errors.Is(err, domain.ErrBotsTooMany):
		_ = s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, state.UserID)
		return botReply{Text: fmt.Sprintf("That I cannot do. You have reached the limit of %d bots per account.", domain.MaxBotsPerOwner)}
	case errors.Is(err, domain.ErrBotNameInvalid):
		// The name step already validated; this can only be dirty state; re-run the name step.
		state.Step = botFatherStepName
		if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
			s.log.Error("botfather: save chat state", zap.Int64("user_id", state.UserID), zap.Error(err))
			return internalReply()
		}
		return botReply{Text: "Please choose a name for your bot first."}
	case err != nil:
		s.log.Error("botfather: create bot", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	if err := s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, state.UserID); err != nil {
		s.log.Error("botfather: delete chat state", zap.Int64("user_id", state.UserID), zap.Error(err))
	}
	head := fmt.Sprintf("Done! Congratulations on your new bot. You will find it at %s.\n\nUse this token to access the HTTP API:\n", s.publicURL(u.Username))
	return tokenReply(head, token, s.botAPIHint()+"\n\nKeep your token secure and store it safely, it can be used by anyone to control your bot.")
}

// botAPIHint names this server's Bot API base URL, when one is configured.
func (s *Service) botAPIHint() string {
	if s.botAPIURL == "" {
		return ""
	}
	return "\n\nBot API on this server: " + s.botAPIURL + "/bot<token>/<method>"
}

func (s *Service) handleChooseBot(ctx context.Context, state domain.BotChatState, text string) botReply {
	username := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(text, "@")))
	profiles, err := s.ownedBots(ctx, state.UserID)
	if err != nil {
		s.log.Error("botfather: list bots", zap.Int64("user_id", state.UserID), zap.Error(err))
		return internalReply()
	}
	var chosen *domain.User
	for i := range profiles {
		if strings.ToLower(profiles[i].user.Username) == username {
			chosen = &profiles[i].user
			break
		}
	}
	if chosen == nil {
		return botReply{Text: "I don't see that bot among yours. Send the username of one of your bots, or /cancel."}
	}
	return s.applyBotChoice(ctx, state, *chosen)
}

// applyBotChoice is the single entry point that executes the command for the choose step after the bot is
// confirmed to belong to that user -- both direct username entry and tapping the @username button go through here.
func (s *Service) applyBotChoice(ctx context.Context, state domain.BotChatState, chosen domain.User) botReply {
	switch state.Command {
	case botFatherCmdToken:
		defer s.clearState(ctx, state.UserID)
		profile, found, err := s.bots.GetBot(ctx, chosen.ID)
		if err != nil || !found || profile.TokenSecret == "" {
			if err != nil {
				s.log.Error("botfather: get bot", zap.Int64("bot_user_id", chosen.ID), zap.Error(err))
			}
			return internalReply()
		}
		head := "You can use this token to access HTTP API:\n"
		return tokenReply(head, domain.FormatBotToken(chosen.ID, profile.TokenSecret), s.botAPIHint()+"\n\nFor a description of the Bot API, see this page: https://core.telegram.org/bots/api")
	case botFatherCmdRevoke:
		defer s.clearState(ctx, state.UserID)
		token, err := s.RevokeBotToken(ctx, state.UserID, chosen.ID)
		switch {
		case errors.Is(err, domain.ErrBotSessionsNotRevoked):
			// The token has been changed (the old token can no longer log in), but established sessions could not
			// be terminated -- honestly tell the user to retry, rather than falsely claiming the bleeding has stopped.
			head := fmt.Sprintf("Token for @%s has been changed, so the old token can no longer log in. But I couldn't terminate sessions that are already logged in — please run /revoke again to make sure they're cut off. New token:\n", chosen.Username)
			return tokenReply(head, token, "\n\nKeep your token secure and store it safely, it can be used by anyone to control your bot.")
		case err != nil:
			s.log.Error("botfather: revoke token", zap.Int64("bot_user_id", chosen.ID), zap.Error(err))
			return internalReply()
		}
		head := fmt.Sprintf("Token for @%s has been revoked. The old token will stop working immediately. New token:\n", chosen.Username)
		return tokenReply(head, token, "\n\nKeep your token secure and store it safely, it can be used by anyone to control your bot.")
	case botFatherCmdLoginInfo:
		defer s.clearState(ctx, state.UserID)
		if s.telegramLogin == nil {
			return botReply{Text: "Telegram Login is not enabled on this server."}
		}
		configuration, found, err := s.telegramLogin.ClientConfiguration(ctx, chosen.ID)
		if err != nil {
			s.log.Error("botfather: get telegram login configuration", zap.Int64("bot_user_id", chosen.ID), zap.Error(err))
			return internalReply()
		}
		if !found {
			return botReply{Text: fmt.Sprintf("Telegram Login is not configured for @%s. Use /setlogin to create it.", chosen.Username)}
		}
		return botReply{Text: formatTelegramLoginConfiguration(chosen.Username, configuration)}
	case botFatherCmdResetLogin:
		defer s.clearState(ctx, state.UserID)
		if s.telegramLogin == nil {
			return botReply{Text: "Telegram Login is not enabled on this server."}
		}
		credentials, err := s.telegramLogin.RotateClientSecret(ctx, chosen.ID)
		if errors.Is(err, domain.ErrTelegramLoginClientInvalid) {
			return botReply{Text: fmt.Sprintf("Telegram Login is not configured for @%s. Use /setlogin first.", chosen.Username)}
		}
		if err != nil {
			s.log.Error("botfather: rotate telegram login secret", zap.Int64("bot_user_id", chosen.ID), zap.Error(err))
			return internalReply()
		}
		head := fmt.Sprintf("The previous OIDC Client Secret for @%s is now invalid. Save this new secret; it will only be shown once:\n", chosen.Username)
		return tokenReply(head, credentials.Secret, "\n\nClient ID: "+credentials.Client.ClientID)
	case botFatherCmdSetLogin:
		if s.telegramLogin == nil {
			s.clearState(ctx, state.UserID)
			return botReply{Text: "Telegram Login is not enabled on this server."}
		}
		credentials, created, err := s.telegramLogin.EnsureClient(ctx, chosen.ID)
		if err != nil {
			s.log.Error("botfather: ensure telegram login client", zap.Int64("bot_user_id", chosen.ID), zap.Error(err))
			return internalReply()
		}
		state.Step = botFatherStepValue
		if state.Draft == nil {
			state.Draft = map[string]string{}
		}
		state.Draft[botFatherDraftBotID] = strconv.FormatInt(chosen.ID, 10)
		state.Draft[botFatherDraftBotUsername] = chosen.Username
		if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
			s.log.Error("botfather: save telegram login state", zap.Int64("user_id", state.UserID), zap.Error(err))
			return internalReply()
		}
		prompt := telegramLoginConfigurationPrompt(chosen.Username)
		if !created {
			return botReply{Text: fmt.Sprintf("Telegram Login client %s is ready for @%s.\n\n%s", credentials.Client.ClientID, chosen.Username, prompt)}
		}
		head := fmt.Sprintf("Telegram Login is now enabled for @%s.\nClient ID: %s\nSave this Client Secret; it will only be shown once:\n", chosen.Username, credentials.Client.ClientID)
		return tokenReply(head, credentials.Secret, "\n\n"+prompt)
	case botFatherCmdSetName, botFatherCmdSetDescription, botFatherCmdSetAbout,
		botFatherCmdSetCommands, botFatherCmdSetInline, botFatherCmdSetInlineGeo,
		botFatherCmdSetJoinGroups, botFatherCmdSetPrivacy:
		// After selecting a bot, enter the value step and stash the target bot in Draft.
		state.Step = botFatherStepValue
		if state.Draft == nil {
			state.Draft = map[string]string{}
		}
		state.Draft[botFatherDraftBotID] = strconv.FormatInt(chosen.ID, 10)
		state.Draft[botFatherDraftBotUsername] = chosen.Username
		if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
			s.log.Error("botfather: save chat state", zap.Int64("user_id", state.UserID), zap.Error(err))
			return internalReply()
		}
		switch state.Command {
		case botFatherCmdSetInlineGeo, botFatherCmdSetJoinGroups, botFatherCmdSetPrivacy:
			profile, found, err := s.bots.GetBot(ctx, chosen.ID)
			if err != nil {
				s.log.Error("botfather: get bot settings", zap.Int64("bot_user_id", chosen.ID), zap.Error(err))
				return internalReply()
			}
			if !found {
				s.clearState(ctx, state.UserID)
				return botReply{Text: "That bot is no longer available."}
			}
			reply := botReply{ReplyMarkup: botFatherToggleKeyboard()}
			switch state.Command {
			case botFatherCmdSetInlineGeo:
				reply.Text = inlineGeoPrompt(chosen.Username, profile.InlineGeo)
			case botFatherCmdSetJoinGroups:
				reply.Text = joinGroupsPrompt(!profile.Nochats)
			default:
				// When privacy mode is on, the bot only sees commands/replies, i.e. profile.ChatHistory=false.
				reply.Text = privacyPrompt(!profile.ChatHistory)
			}
			return reply
		default:
			return botReply{Text: valuePrompt(state.Command, chosen.Username)}
		}
	case botFatherCmdDeleteBot:
		// Deleting a bot is irreversible and terminates all its sessions, so stop at the confirm step first: only an
		// explicit confirmation word proceeds; an accidentally triggered username/empty message returns to the same
		// confirmation prompt.
		state.Step = botFatherStepConfirm
		if state.Draft == nil {
			state.Draft = map[string]string{}
		}
		state.Draft[botFatherDraftBotID] = strconv.FormatInt(chosen.ID, 10)
		state.Draft[botFatherDraftBotUsername] = chosen.Username
		if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
			s.log.Error("botfather: save chat state", zap.Int64("user_id", state.UserID), zap.Error(err))
			return internalReply()
		}
		return botReply{Text: botDeleteConfirmPrompt(chosen.Username)}
	default:
		s.clearState(ctx, state.UserID)
		return internalReply()
	}
}

// handleBotFatherConfirmCommand handles an explicit /confirm (it does not guess when there is no active confirm step).
func (s *Service) handleBotFatherConfirmCommand(ctx context.Context, userID int64) botReply {
	state, found, err := s.bots.GetBotChatState(ctx, domain.BotFatherUserID, userID)
	if err != nil {
		s.log.Error("botfather: get chat state", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	if !found || state.Step != botFatherStepConfirm {
		return botReply{Text: "There is nothing to confirm right now. Send /help for a list of commands."}
	}
	return s.handleBotConfirm(ctx, state, "confirm")
}

func botDeleteConfirmPrompt(username string) string {
	return fmt.Sprintf("Are you sure you want to delete @%s? This cannot be undone: the token stops working and every session of the bot is terminated.\n\nSend `confirm` to proceed, or /cancel.", username)
}

// handleBotConfirm handles the confirmation step of an irreversible operation (currently only /deletebot). Only an
// explicit confirmation word acts: any other input returns to the same confirmation prompt, so accidentally touched
// messages are not treated as confirmation.
func (s *Service) handleBotConfirm(ctx context.Context, state domain.BotChatState, text string) botReply {
	if state.Command != botFatherCmdDeleteBot {
		s.clearState(ctx, state.UserID)
		return internalReply()
	}
	username := state.Draft[botFatherDraftBotUsername]
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "confirm", "yes", "delete":
	default:
		return botReply{Text: botDeleteConfirmPrompt(username)}
	}
	botID, _ := strconv.ParseInt(state.Draft[botFatherDraftBotID], 10, 64)
	if botID == 0 || username == "" {
		s.clearState(ctx, state.UserID)
		return botReply{Text: "Something went wrong, I forgot which bot we were deleting. Send /deletebot to start again."}
	}
	// Defensive owner re-check (the state is server-stored; normally it is already an owned bot).
	if owns, err := s.OwnsBot(ctx, state.UserID, botID); err != nil {
		s.log.Error("botfather: owns bot", zap.Int64("bot_user_id", botID), zap.Error(err))
		return internalReply()
	} else if !owns {
		s.clearState(ctx, state.UserID)
		return botReply{Text: "That bot is no longer available."}
	}
	// DeleteBot first revokes sessions and then deletes the account: if revocation fails nothing was deleted, so be honest about it and keep the state for the user to retry.
	if _, err := s.DeleteBot(ctx, botID); err != nil {
		switch {
		case errors.Is(err, domain.ErrBotSessionsNotRevoked):
			return botReply{Text: fmt.Sprintf("I couldn't delete @%s because I couldn't terminate the sessions it is already logged into. Nothing was deleted — please run /deletebot again in a moment.", username)}
		case errors.Is(err, domain.ErrBotDeletionUnsupported):
			s.clearState(ctx, state.UserID)
			return botReply{Text: "Sorry, deleting bots is not supported on this server."}
		case errors.Is(err, domain.ErrBotNotFound):
			s.clearState(ctx, state.UserID)
			return botReply{Text: "That bot is no longer available."}
		default:
			s.log.Error("botfather: delete bot", zap.Int64("bot_user_id", botID), zap.Error(err))
			return internalReply()
		}
	}
	s.clearState(ctx, state.UserID)
	return botReply{Text: fmt.Sprintf("Success! @%s has been deleted. Its token no longer works and all of its sessions have been terminated.", username)}
}

// handleSetValue handles the value-collection step after a bot is selected (/setname etc.).
func (s *Service) handleSetValue(ctx context.Context, state domain.BotChatState, text string) botReply {
	botID, _ := strconv.ParseInt(state.Draft[botFatherDraftBotID], 10, 64)
	username := state.Draft[botFatherDraftBotUsername]
	if botID == 0 {
		s.clearState(ctx, state.UserID)
		return botReply{Text: "Something went wrong, I forgot which bot we were editing. Send /help."}
	}
	// Defensive owner re-check (the state is server-stored; normally it is already an owned bot).
	if owns, err := s.OwnsBot(ctx, state.UserID, botID); err != nil {
		s.log.Error("botfather: owns bot", zap.Int64("bot_user_id", botID), zap.Error(err))
		return internalReply()
	} else if !owns {
		s.clearState(ctx, state.UserID)
		return botReply{Text: "That bot is no longer available."}
	}

	var (
		reply botReply
		err   error
	)
	switch state.Command {
	case botFatherCmdSetName:
		_, err = s.SetBotInfo(ctx, botID, domain.BotInfoUpdate{SetName: true, Name: text})
		reply = okReply(err, "Success! Name updated. /help", "Sorry, that name is invalid. Please try a different one.")
	case botFatherCmdSetDescription:
		_, err = s.SetBotInfo(ctx, botID, domain.BotInfoUpdate{SetDescription: true, Description: text})
		reply = okReply(err, "Success! Description updated. You will be able to see the changes within a few minutes. /help", "Sorry, that description is too long.")
	case botFatherCmdSetAbout:
		_, err = s.SetBotInfo(ctx, botID, domain.BotInfoUpdate{SetAbout: true, About: text})
		reply = okReply(err, "Success! About section updated. /help", "Sorry, that about text is too long.")
	case botFatherCmdSetCommands:
		reply, err = s.applySetCommands(ctx, botID, text)
	case botFatherCmdSetInline:
		reply, err = s.applySetInline(ctx, botID, text)
	case botFatherCmdSetInlineGeo:
		reply, err = s.applySetInlineGeo(ctx, botID, text)
	case botFatherCmdSetJoinGroups:
		reply, err = s.applyToggle(ctx, botID, text, true)
	case botFatherCmdSetPrivacy:
		reply, err = s.applyToggle(ctx, botID, text, false)
	case botFatherCmdSetLogin:
		return s.handleTelegramLoginConfigurationInput(ctx, state, botID, username, text)
	default:
		s.clearState(ctx, state.UserID)
		return internalReply()
	}
	if err != nil {
		// Validation errors have already been turned into prompt text; non-validation errors are logged internally. Keep the state so the user can retry.
		if reply.Text == "" {
			return internalReply()
		}
		return reply
	}
	s.clearState(ctx, state.UserID)
	return reply
}

// botFatherCommandListInvalidText is /setcommands' unified rejection text: format errors and invalid content
// (non-compliant command names/descriptions) are the same thing to the user -- resend a compliant list or /empty.
func botFatherCommandListInvalidText() string {
	return `Sorry, the list of commands is invalid. Please use this format:

command1 - Description
command2 - Another description

You can also send /empty to clear the list or keep it empty.`
}

// applySetCommands parses multi-line "command - Description" (/empty clears) and writes it.
func (s *Service) applySetCommands(ctx context.Context, botID int64, text string) (botReply, error) {
	var commands []domain.BotCommand
	if strings.TrimSpace(text) != "/empty" {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			cmd, desc, ok := strings.Cut(line, "-")
			if !ok {
				return botReply{Text: botFatherCommandListInvalidText()}, domain.ErrBotCommandInvalid
			}
			commands = append(commands, domain.BotCommand{
				Command:     strings.TrimSpace(cmd),
				Description: strings.TrimSpace(desc),
			})
		}
	}
	if _, err := s.SetBotCommands(ctx, botID, commands); err != nil {
		return botReply{Text: botFatherCommandListInvalidText()}, err
	}
	return botReply{Text: "Success! Command list updated. /help"}, nil
}

// applySetInline writes the inline placeholder; /empty clears it and disables inline mode.
func (s *Service) applySetInline(ctx context.Context, botID int64, text string) (botReply, error) {
	placeholder := strings.TrimSpace(text)
	if placeholder == "/empty" {
		placeholder = ""
	}
	if _, err := s.SetInlinePlaceholder(ctx, botID, placeholder); err != nil {
		return botReply{Text: fmt.Sprintf("Sorry, inline placeholder must be at most %d characters. Try again or /cancel.", domain.MaxBotInlinePlaceholderLen)}, err
	}
	if placeholder == "" {
		return botReply{Text: "Success! Inline mode disabled. /help"}, nil
	}
	return botReply{Text: "Success! Inline settings updated. /help"}, nil
}

func (s *Service) applySetInlineGeo(ctx context.Context, botID int64, text string) (botReply, error) {
	var enabled bool
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "enable", "on", "yes":
		enabled = true
	case "disable", "off", "no":
		enabled = false
	default:
		return botReply{Text: "Please send 'enable' or 'disable', or /cancel."}, domain.ErrBotInfoInvalid
	}
	if _, err := s.SetInlineGeo(ctx, botID, enabled); err != nil {
		s.log.Error("botfather: set inline geo", zap.Int64("bot_user_id", botID), zap.Error(err))
		return botReply{}, err
	}
	state := "DISABLED"
	if enabled {
		state = "ENABLED"
	}
	return botReply{Text: fmt.Sprintf("Success! Inline location request settings updated. New status is %s /help", state)}, nil
}

func telegramLoginConfigurationPrompt(username string) string {
	return fmt.Sprintf(`Configure Telegram Login for @%s. Send commands one at a time or paste up to %d commands on separate lines:

add origin https://example.com
add redirect https://example.com/auth/callback
add ios com.example.app ABCDE12345 exampleapp://tglogin Example iOS App
add android com.example.app AA:BB:...:FF exampleapp://telegram-login Example Android App
remove origin https://example.com
remove redirect https://example.com/auth/callback
remove app 12
algorithm RS256|ES256|EdDSA|ES256K
enable
disable

Origins authorize the JS SDK and legacy login_url buttons. Redirects are exact OIDC callbacks. Changes apply immediately. Send /done to finish, or /cancel to close this session without undoing changes already applied.`, username, maxTelegramLoginCommandsPerMessage)
}

func telegramLoginConfigurationContinuePrompt(username string) string {
	return fmt.Sprintf("Still configuring @%s. Send another command, paste multiple commands on separate lines, or send /done to finish.", username)
}

func (s *Service) finishTelegramLoginConfiguration(ctx context.Context, userID int64) botReply {
	state, found, err := s.bots.GetBotChatState(ctx, domain.BotFatherUserID, userID)
	if err != nil {
		s.log.Error("botfather: get telegram login state", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	if !found || state.Command != botFatherCmdSetLogin || state.Step != botFatherStepValue {
		return botReply{Text: "There is no active Telegram Login configuration to finish. Send /setlogin to start one."}
	}
	botID, _ := strconv.ParseInt(state.Draft[botFatherDraftBotID], 10, 64)
	username := state.Draft[botFatherDraftBotUsername]
	if botID == 0 || username == "" {
		s.clearState(ctx, userID)
		return botReply{Text: "Something went wrong, I forgot which bot we were editing. Send /setlogin to start again."}
	}
	owns, err := s.OwnsBot(ctx, userID, botID)
	if err != nil {
		s.log.Error("botfather: verify telegram login owner", zap.Int64("user_id", userID), zap.Int64("bot_user_id", botID), zap.Error(err))
		return internalReply()
	}
	if !owns {
		s.clearState(ctx, userID)
		return botReply{Text: "That bot is no longer available."}
	}
	if s.telegramLogin == nil {
		s.clearState(ctx, userID)
		return botReply{Text: "Telegram Login is not enabled on this server."}
	}
	configuration, configured, err := s.telegramLogin.ClientConfiguration(ctx, botID)
	if err != nil {
		s.log.Error("botfather: get telegram login configuration", zap.Int64("bot_user_id", botID), zap.Error(err))
		return internalReply()
	}
	if !configured {
		s.clearState(ctx, userID)
		return botReply{Text: fmt.Sprintf("Telegram Login is not configured for @%s. Send /setlogin to create it.", username)}
	}
	if err := s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID); err != nil {
		s.log.Error("botfather: finish telegram login state", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	return botReply{Text: fmt.Sprintf("Finished configuring Telegram Login for @%s.\n\n%s", username, formatTelegramLoginConfiguration(username, configuration))}
}

func (s *Service) handleTelegramLoginConfigurationInput(
	ctx context.Context,
	state domain.BotChatState,
	botID int64,
	username string,
	text string,
) botReply {
	if strings.EqualFold(strings.TrimSpace(text), "done") {
		return s.finishTelegramLoginConfiguration(ctx, state.UserID)
	}
	lines := make([]string, 0, 4)
	for _, raw := range strings.Split(text, "\n") {
		if line := strings.TrimSpace(raw); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return botReply{Text: "Send a Telegram Login configuration command.\n\n" + telegramLoginConfigurationContinuePrompt(username)}
	}
	if len(lines) > maxTelegramLoginCommandsPerMessage {
		return botReply{Text: fmt.Sprintf("Too many commands in one message. Send at most %d lines at a time.\n\n%s", maxTelegramLoginCommandsPerMessage, telegramLoginConfigurationContinuePrompt(username))}
	}

	applied := make([]string, 0, len(lines))
	for i, line := range lines {
		reply, err := s.applyTelegramLoginConfiguration(ctx, botID, username, line)
		if err != nil {
			if len(lines) == 1 {
				if reply.Text == "" {
					return internalReply()
				}
				return botReply{Text: reply.Text + "\n\n" + telegramLoginConfigurationContinuePrompt(username)}
			}
			failure := reply.Text
			if failure == "" {
				failure = "Something went wrong on my side. Please try that line again later."
			}
			var out strings.Builder
			if len(applied) > 0 {
				fmt.Fprintf(&out, "Applied %d command(s) before the error:\n%s\n\n", len(applied), strings.Join(applied, "\n"))
			}
			fmt.Fprintf(&out, "Stopped at line %d:\n%s\n\n", i+1, failure)
			if i+1 < len(lines) {
				fmt.Fprintf(&out, "%d later command(s) were not applied.\n\n", len(lines)-i-1)
			}
			out.WriteString(telegramLoginConfigurationContinuePrompt(username))
			return botReply{Text: out.String()}
		}
		applied = append(applied, fmt.Sprintf("Line %d: %s", i+1, reply.Text))
	}

	var out strings.Builder
	if len(lines) == 1 {
		out.WriteString(strings.TrimPrefix(applied[0], "Line 1: "))
	} else {
		fmt.Fprintf(&out, "Applied all %d commands:\n%s", len(applied), strings.Join(applied, "\n"))
	}
	out.WriteString("\n\n")
	out.WriteString(telegramLoginConfigurationContinuePrompt(username))
	return botReply{Text: out.String()}
}

func formatTelegramLoginConfiguration(username string, configuration telegramloginapp.ClientConfiguration) string {
	status := "disabled"
	if configuration.Client.Enabled {
		status = "enabled"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Telegram Login for @%s\nClient ID: %s\nStatus: %s\nSigning algorithm: %s\nSecret version: %d",
		username, configuration.Client.ClientID, status, configuration.Client.SigningAlgorithm, configuration.Client.SecretVersion)
	if len(configuration.AllowedURLs) == 0 {
		out.WriteString("\nAllowed URLs: none")
	} else {
		out.WriteString("\nAllowed URLs:")
		for _, allowed := range configuration.AllowedURLs {
			fmt.Fprintf(&out, "\n- %s %s", allowed.Kind, allowed.NormalizedURL)
		}
	}
	if len(configuration.NativeApps) > 0 {
		out.WriteString("\nNative apps:")
		for _, app := range configuration.NativeApps {
			fmt.Fprintf(&out, "\n- #%d %s %s [%s] -> %s (%s)", app.ID, app.Platform, app.ApplicationID, app.VerificationID, app.CallbackURI, app.VerifiedDisplayName)
		}
	}
	return out.String()
}

func telegramLoginAllowedURLKind(raw string) (domain.TelegramLoginAllowedURLKind, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "origin":
		return domain.TelegramLoginAllowedWebOrigin, true
	case "redirect":
		return domain.TelegramLoginAllowedRedirectURI, true
	default:
		return "", false
	}
}

func telegramLoginSigningAlgorithm(raw string) (domain.TelegramLoginSigningAlgorithm, bool) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "RS256":
		return domain.TelegramLoginSigningRS256, true
	case "ES256":
		return domain.TelegramLoginSigningES256, true
	case "EDDSA":
		return domain.TelegramLoginSigningEdDSA, true
	case "ES256K":
		return domain.TelegramLoginSigningES256K, true
	default:
		return "", false
	}
}

func (s *Service) applyTelegramLoginConfiguration(ctx context.Context, botID int64, username, text string) (botReply, error) {
	if s.telegramLogin == nil {
		return botReply{Text: "Telegram Login is not enabled on this server."}, domain.ErrTelegramLoginClientDisabled
	}
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 1 {
		switch strings.ToLower(fields[0]) {
		case "enable":
			if err := s.telegramLogin.SetClientEnabled(ctx, botID, true); err != nil {
				return botReply{}, err
			}
			return botReply{Text: fmt.Sprintf("Telegram Login is enabled for @%s.", username)}, nil
		case "disable":
			if err := s.telegramLogin.SetClientEnabled(ctx, botID, false); err != nil {
				return botReply{}, err
			}
			return botReply{Text: fmt.Sprintf("Telegram Login is disabled for @%s. Pending requests can no longer be approved or exchanged.", username)}, nil
		}
	}
	if len(fields) == 2 && strings.EqualFold(fields[0], "algorithm") {
		algorithm, ok := telegramLoginSigningAlgorithm(fields[1])
		if !ok {
			return botReply{Text: "Unknown signing algorithm. Use RS256, ES256, EdDSA or ES256K, or /cancel."}, domain.ErrTelegramLoginClientInvalid
		}
		if _, err := s.telegramLogin.SetClientSigningAlgorithm(ctx, botID, algorithm); err != nil {
			if errors.Is(err, domain.ErrTelegramLoginClientInvalid) {
				return botReply{Text: fmt.Sprintf("%s is not available on this server because no active signing key is configured for it. Choose another algorithm or ask the operator to rotate the key ring.", algorithm)}, err
			}
			return botReply{}, err
		}
		return botReply{Text: fmt.Sprintf("Success! New ID tokens for @%s will use %s. EdDSA and ES256K accept only the openid scope.", username, algorithm)}, nil
	}
	if len(fields) == 3 && (strings.EqualFold(fields[0], "add") || strings.EqualFold(fields[0], "remove")) &&
		(strings.EqualFold(fields[1], "origin") || strings.EqualFold(fields[1], "redirect")) {
		kind, ok := telegramLoginAllowedURLKind(fields[1])
		if !ok {
			return botReply{Text: "URL kind must be origin or redirect. Try again or /cancel."}, domain.ErrTelegramLoginURLInvalid
		}
		if strings.EqualFold(fields[0], "add") {
			allowed, err := s.telegramLogin.AddAllowedURL(ctx, botID, kind, fields[2])
			if err != nil {
				return botReply{Text: "That URL is not allowed. Use an exact HTTP(S) URL permitted by this server without credentials, fragments or reserved OAuth query fields."}, err
			}
			return botReply{Text: fmt.Sprintf("Success! Added %s for @%s:\n%s", allowed.Kind, username, allowed.NormalizedURL)}, nil
		}
		deleted, err := s.telegramLogin.DeleteAllowedURL(ctx, botID, kind, fields[2])
		if err != nil {
			return botReply{Text: "That URL is invalid. Try again or /cancel."}, err
		}
		if !deleted {
			return botReply{Text: "That exact URL was not registered. Check /logininfo and try again."}, domain.ErrTelegramLoginURLInvalid
		}
		return botReply{Text: fmt.Sprintf("Success! Removed %s from @%s.", kind, username)}, nil
	}
	if len(fields) >= 6 && strings.EqualFold(fields[0], "add") && (strings.EqualFold(fields[1], "ios") || strings.EqualFold(fields[1], "android")) {
		platform := domain.TelegramLoginNativeIOS
		if strings.EqualFold(fields[1], "android") {
			platform = domain.TelegramLoginNativeAndroid
		}
		app, err := s.telegramLogin.AddNativeApp(ctx, botID, platform, fields[2], fields[3], fields[4], strings.Join(fields[5:], " "))
		if err != nil {
			return botReply{Text: "Invalid native app registration. iOS needs Bundle ID + 10-character Team ID; Android needs package name + SHA-256 signing fingerprint. Use an exact HTTPS callback or a custom scheme://host callback."}, err
		}
		return botReply{Text: fmt.Sprintf("Success! Registered native app #%d for @%s:\n%s %s -> %s", app.ID, username, app.Platform, app.ApplicationID, app.CallbackURI)}, nil
	}
	if len(fields) == 3 && strings.EqualFold(fields[0], "remove") && strings.EqualFold(fields[1], "app") {
		appID, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || appID <= 0 {
			return botReply{Text: "Native app ID must be the positive number shown by /logininfo."}, domain.ErrTelegramLoginClientInvalid
		}
		deleted, err := s.telegramLogin.DeleteNativeApp(ctx, botID, appID)
		if err != nil {
			return botReply{}, err
		}
		if !deleted {
			return botReply{Text: "That native app was not registered for this bot. Check /logininfo."}, domain.ErrTelegramLoginClientInvalid
		}
		return botReply{Text: fmt.Sprintf("Success! Removed native app #%d from @%s.", appID, username)}, nil
	}
	return botReply{Text: telegramLoginConfigurationPrompt(username)}, domain.ErrTelegramLoginRequestInvalid
}

// applyToggle parses enable/disable and sets joingroups (join=true) or privacy (join=false).
func (s *Service) applyToggle(ctx context.Context, botID int64, text string, join bool) (botReply, error) {
	var on bool
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "enable", "on", "yes":
		on = true
	case "disable", "off", "no":
		on = false
	default:
		return botReply{Text: "Please send 'enable' or 'disable', or /cancel."}, domain.ErrBotInfoInvalid
	}
	var err error
	if join {
		_, err = s.SetJoinGroups(ctx, botID, on)
	} else {
		_, err = s.SetPrivacy(ctx, botID, on)
	}
	if err != nil {
		s.log.Error("botfather: apply toggle", zap.Int64("bot_user_id", botID), zap.Bool("join", join), zap.Error(err))
		return botReply{}, err
	}
	state := "disabled"
	if on {
		state = "enabled"
	}
	return botReply{Text: fmt.Sprintf("Success! The new status is: %s. /help", strings.ToUpper(state))}, nil
}

// okReply turns a service call result into a prompt: err==nil returns success, otherwise returns the validation-failure prompt.
func okReply(err error, ok, fail string) botReply {
	if err != nil {
		return botReply{Text: fail}
	}
	return botReply{Text: ok}
}

// clearState deletes the BotFather dialog state (errors ignored, only logged).
func (s *Service) clearState(ctx context.Context, userID int64) {
	if err := s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID); err != nil {
		s.log.Error("botfather: delete chat state", zap.Int64("user_id", userID), zap.Error(err))
	}
}

type ownedBot struct {
	profile domain.BotProfile
	user    domain.User
}

func (s *Service) ownedBots(ctx context.Context, ownerUserID int64) ([]ownedBot, error) {
	profiles, err := s.bots.ListBotsByOwner(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(profiles))
	for _, p := range profiles {
		ids = append(ids, p.BotUserID)
	}
	users, err := s.users.ByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]domain.User, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}
	out := make([]ownedBot, 0, len(profiles))
	for _, p := range profiles {
		u, ok := byID[p.BotUserID]
		if !ok {
			continue
		}
		out = append(out, ownedBot{profile: p, user: u})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].user.ID < out[j].user.ID })
	return out, nil
}

func (s *Service) ownedBotUsernames(ctx context.Context, ownerUserID int64) ([]string, error) {
	owned, err := s.ownedBots(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(owned))
	for _, b := range owned {
		if b.user.Username != "" {
			out = append(out, b.user.Username)
		}
	}
	return out, nil
}

// tokenReply assembles a token message with a code entity (all-ASCII text, so the offset equals both the byte
// and UTF-16 code unit offset).
func tokenReply(head, token, tail string) botReply {
	return botReply{
		Text: head + token + tail,
		Entities: []domain.MessageEntity{
			{Type: domain.MessageEntityCode, Offset: len(head), Length: len(token)},
		},
	}
}

func internalReply() botReply {
	return botReply{Text: "Something went wrong on my side. Please try again later."}
}

// parseBotCommand parses a leading "/cmd" (tolerating "/cmd@BotFather" and trailing arguments) and returns the lowercased command name.
func parseBotCommand(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	cmd := text[1:]
	if i := strings.IndexAny(cmd, " \t\n"); i >= 0 {
		cmd = cmd[:i]
	}
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	if cmd == "" {
		return "", false
	}
	return strings.ToLower(cmd), true
}
