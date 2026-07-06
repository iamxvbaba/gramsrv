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

	"telesrv/internal/domain"
)

// BotFather 对话状态机：用户发给 BotFather 的每条私聊消息经 app/messages 的
// responder hook 进入 OnPrivateMessage；用户消息已先行入库，这里只负责生成并
// 写入 BotFather 的回复（完整 SendPrivateText 链路：双盒+事件+outbox 推送）。

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

	botFatherStepName     = "name"
	botFatherStepUsername = "username"
	botFatherStepChoose   = "choose"
	botFatherStepValue    = "value"

	botFatherDraftBotID       = "bot_id"
	botFatherDraftBotUsername = "bot_username"
)

const botFatherHelpText = `I can help you create and manage Telegram bots.

You can control me by sending these commands:

/newbot - create a new bot
/mybots - list your bots
/token - show a bot's token
/revoke - revoke a bot's token
/setname - change a bot's name
/setdescription - change a bot's description
/setabouttext - change a bot's about info
/setcommands - change a bot's command list
/setinline - toggle inline mode
/setinlinegeo - toggle inline location requests
/setinlinefeedback - change inline feedback settings
/setjoingroups - toggle whether a bot can join groups
/setprivacy - toggle a bot's group privacy mode
/cancel - cancel the current operation
/help - show this message`

// botReply 是 BotFather 的一条回复。
type botReply struct {
	Text     string
	Entities []domain.MessageEntity
}

// HandlesBot 报告该收件人是否为内置应答 bot（messages.BotResponder 实现）。
func (s *Service) HandlesBot(botUserID int64) bool {
	return s != nil && (botUserID == domain.BotFatherUserID || botUserID == domain.StickersBotUserID || botUserID == domain.ChatBotUserID)
}

// OnPrivateMessage 处理投递给内置 bot 的私聊消息（messages.BotResponder 实现）。
// msg 是 bot 视角的收件 box 行。回复异步生成（不占用户 sendMessage 的 RPC
// goroutine——官方 bot 回复本就异步到达），失败只记日志，绝不影响用户消息本身。
func (s *Service) OnPrivateMessage(ctx context.Context, botUserID int64, msg domain.Message) {
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
	}
}

// respondAsBotFather 生成并写入 BotFather 回复（OnPrivateMessage 在 goroutine 内调用）。
// 按用户取条带锁串行：状态机 Get→modify→Upsert/Delete 的 RMW 因此原子、回复保序，
// 不同用户并发不受影响。ctx 用 Background（脱离已返回的用户 RPC），限较长超时。
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
	if s == nil || s.messages == nil || reply.Text == "" {
		return domain.SendPrivateTextResult{}, false
	}
	res, err := s.messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:     botUserID,
		RecipientUserID:  userID,
		RandomID:         s.botReplyRandomID(),
		Message:          reply.Text,
		Entities:         serviceBotReplyEntities(reply.Text, reply.Entities),
		Date:             int(s.now().Unix()),
		RecipientBlocked: s.serviceBotRecipientBlocked(ctx, botUserID, userID),
	})
	if err != nil {
		s.log.Error("service bot: send reply", zap.Int64("bot_user_id", botUserID), zap.Int64("user_id", userID), zap.Error(err))
		return domain.SendPrivateTextResult{}, false
	}
	return res, true
}

// botReplyRandomID 为服务端回复构造非零幂等键（(sender, random_id) 唯一索引）。
// 所有服务 bot 回复按各自 sender 命名空间唯一——用
// crypto/rand 取 64 位随机数（碰撞概率可忽略），熵源失败时退化为纳秒+单调序列。
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

// botFatherGlobalCommands 是任何状态下都优先按命令处理的全局命令。其余以 "/"
// 开头的文本（如 /empty、或粘贴的 "/start - Begin" 命令列表首行）在收值步骤里
// 必须作为原始内容透传给状态机，否则 /setcommands 的 /empty 永不可达、且首行
// 带斜杠的命令列表会被截成命令名 "start" 静默销毁整个流程。
var botFatherGlobalCommands = map[string]bool{
	"start": true, "help": true, "cancel": true,
	botFatherCmdNewBot: true, "mybots": true,
	botFatherCmdToken: true, botFatherCmdRevoke: true,
	botFatherCmdSetName: true, botFatherCmdSetDescription: true, botFatherCmdSetAbout: true,
	botFatherCmdSetCommands: true, botFatherCmdSetInline: true, botFatherCmdSetInlineGeo: true,
	botFatherCmdSetInlineFB: true, botFatherCmdSetJoinGroups: true, botFatherCmdSetPrivacy: true,
}

func (s *Service) handleBotFather(ctx context.Context, userID int64, text string) botReply {
	text = strings.TrimSpace(text)
	state, found, err := s.bots.GetBotChatState(ctx, domain.BotFatherUserID, userID)
	if err != nil {
		s.log.Error("botfather: get chat state", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	// 命令拦截：仅当不在收值步骤、或文本是已知全局命令时，才走命令分发。收值步骤
	// 下的非全局 "/..." 文本（/empty、命令列表首行）必须当原始值透传给状态机。
	if cmd, ok := parseBotCommand(text); ok {
		inValueStep := found && state.Step == botFatherStepValue
		if !inValueStep || botFatherGlobalCommands[cmd] {
			return s.handleBotFatherCommand(ctx, userID, cmd)
		}
	}
	if text == "" {
		// 空白文本 / 贴纸 / 无 caption 媒体：有活动状态时回当前步骤提示，
		// 无状态保持沉默（避免对任意非文本消息刷屏）。
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
	case state.Step == botFatherStepValue:
		return s.handleSetValue(ctx, state, text)
	default:
		// 不可达的脏状态：清掉重来，避免用户被卡死。
		_ = s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID)
		return botReply{Text: "Something went wrong, I forgot what we were doing. Send /help for a list of commands."}
	}
}

// pickerCommands 是「先选 bot」的命令集（choose step 后按命令分流）。
var pickerPrompts = map[string]string{
	botFatherCmdToken:          "Choose a bot to generate a token for. Send the bot's username:",
	botFatherCmdRevoke:         "Choose a bot to revoke the token of. Send the bot's username:",
	botFatherCmdSetName:        "Choose a bot to change the name of. Send the bot's username:",
	botFatherCmdSetDescription: "Choose a bot to change the description of. Send the bot's username:",
	botFatherCmdSetAbout:       "Choose a bot to change the about info of. Send the bot's username:",
	botFatherCmdSetCommands:    "Choose a bot to change the command list of. Send the bot's username:",
	botFatherCmdSetInline:      "Choose a bot to change inline mode for. Send the bot's username:",
	botFatherCmdSetInlineGeo:   "Choose a bot to change inline location requests for. Send the bot's username:",
	botFatherCmdSetJoinGroups:  "Choose a bot to configure group joining for. Send the bot's username:",
	botFatherCmdSetPrivacy:     "Choose a bot to configure group privacy for. Send the bot's username:",
}

// startBotPicker 列出 owner 的 bot 并进入 choose step（所有需先选 bot 的命令共用）。
func (s *Service) startBotPicker(ctx context.Context, userID int64, cmd string) botReply {
	usernames, err := s.ownedBotUsernames(ctx, userID)
	if err != nil {
		s.log.Error("botfather: list bots", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	if len(usernames) == 0 {
		return botReply{Text: "You don't have any bots yet. Use /newbot to create one."}
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
	return botReply{Text: pickerPrompts[cmd] + "\n\n@" + strings.Join(usernames, "\n@")}
}

// stepPrompt 返回当前对话步骤的引导文案（空输入兜底用）。
func (s *Service) stepPrompt(state domain.BotChatState) botReply {
	switch {
	case state.Command == botFatherCmdNewBot && state.Step == botFatherStepName:
		return botReply{Text: "Please choose a name for your bot, or /cancel."}
	case state.Command == botFatherCmdNewBot && state.Step == botFatherStepUsername:
		return botReply{Text: "Please send a username for your bot. It must end in `bot`. Or /cancel."}
	case state.Step == botFatherStepChoose:
		return botReply{Text: "Please send the username of one of your bots, or /cancel."}
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
		return botReply{Text: botFatherHelpText}
	case "cancel":
		_, found, err := s.bots.GetBotChatState(ctx, domain.BotFatherUserID, userID)
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
		return botReply{Text: "The command has been cancelled. Anything else I can do for you? Send /help for a list of commands."}
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
	case botFatherCmdToken, botFatherCmdRevoke,
		botFatherCmdSetName, botFatherCmdSetDescription, botFatherCmdSetAbout,
		botFatherCmdSetCommands, botFatherCmdSetInline, botFatherCmdSetInlineGeo,
		botFatherCmdSetJoinGroups, botFatherCmdSetPrivacy:
		return s.startBotPicker(ctx, userID, cmd)
	case botFatherCmdSetInlineFB:
		_ = s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID)
		return botReply{Text: "Inline feedback settings are not supported yet. Use /setinline to enable or disable inline mode."}
	default:
		return botReply{Text: "Unrecognized command. Say what? Send /help for a list of commands."}
	}
}

// valuePrompts 是选中 bot 后、value step 的收值提示（按命令）。
func valuePrompt(cmd, username string) string {
	switch cmd {
	case botFatherCmdSetName:
		return fmt.Sprintf("OK. Send me the new name for @%s.", username)
	case botFatherCmdSetDescription:
		return fmt.Sprintf("OK. Send me the new description for @%s. People will see it on the bot's profile page, before they start a chat with it.", username)
	case botFatherCmdSetAbout:
		return fmt.Sprintf("OK. Send me the new about text for @%s. People will see this text on the bot's profile page and it will be sent together with a link to your bot when they share it with someone.", username)
	case botFatherCmdSetCommands:
		return fmt.Sprintf("OK. Send me a list of commands for @%s. Please use this format:\n\ncommand1 - Description\ncommand2 - Another description\n\nSend /empty to clear the list.", username)
	case botFatherCmdSetInline:
		return fmt.Sprintf("This will enable inline queries for @%s. Send me the placeholder text people will see after typing the bot username, or send /empty to disable inline mode.", username)
	case botFatherCmdSetInlineGeo:
		return fmt.Sprintf("Send 'enable' to allow @%s to receive location in inline queries, or 'disable' to turn it off.", username)
	case botFatherCmdSetJoinGroups:
		return fmt.Sprintf("Send 'enable' to allow @%s to be added to groups, or 'disable' to prevent it.", username)
	case botFatherCmdSetPrivacy:
		return fmt.Sprintf("Send 'enable' to turn ON group privacy for @%s (it will only receive commands and replies), or 'disable' to let it receive all group messages.", username)
	default:
		return "Send the new value, or /cancel."
	}
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
		// name 步已校验，这里只可能是脏状态；重新走 name 步。
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
	return tokenReply(head, token, "\n\nKeep your token secure and store it safely, it can be used by anyone to control your bot.")
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
		head := fmt.Sprintf("You can use this token to access the HTTP API for @%s:\n", chosen.Username)
		return tokenReply(head, domain.FormatBotToken(chosen.ID, profile.TokenSecret), "\n\nKeep your token secure and store it safely, it can be used by anyone to control your bot.")
	case botFatherCmdRevoke:
		defer s.clearState(ctx, state.UserID)
		token, err := s.RevokeBotToken(ctx, state.UserID, chosen.ID)
		switch {
		case errors.Is(err, domain.ErrBotSessionsNotRevoked):
			// token 已换（旧 token 不可再登录），但未能终止已建立的 session——
			// 诚实告知用户重试，不谎称已止血。
			head := fmt.Sprintf("Token for @%s has been changed, so the old token can no longer log in. But I couldn't terminate sessions that are already logged in — please run /revoke again to make sure they're cut off. New token:\n", chosen.Username)
			return tokenReply(head, token, "\n\nKeep your token secure and store it safely, it can be used by anyone to control your bot.")
		case err != nil:
			s.log.Error("botfather: revoke token", zap.Int64("bot_user_id", chosen.ID), zap.Error(err))
			return internalReply()
		}
		head := fmt.Sprintf("Token for @%s has been revoked. The old token will stop working immediately. New token:\n", chosen.Username)
		return tokenReply(head, token, "\n\nKeep your token secure and store it safely, it can be used by anyone to control your bot.")
	case botFatherCmdSetName, botFatherCmdSetDescription, botFatherCmdSetAbout,
		botFatherCmdSetCommands, botFatherCmdSetInline, botFatherCmdSetInlineGeo,
		botFatherCmdSetJoinGroups, botFatherCmdSetPrivacy:
		// 选中 bot 后进入收值 step，把目标 bot 暂存进 Draft。
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
		return botReply{Text: valuePrompt(state.Command, chosen.Username)}
	default:
		s.clearState(ctx, state.UserID)
		return internalReply()
	}
}

// handleSetValue 处理选中 bot 后的收值步骤（/setname 等）。
func (s *Service) handleSetValue(ctx context.Context, state domain.BotChatState, text string) botReply {
	botID, _ := strconv.ParseInt(state.Draft[botFatherDraftBotID], 10, 64)
	username := state.Draft[botFatherDraftBotUsername]
	if botID == 0 {
		s.clearState(ctx, state.UserID)
		return botReply{Text: "Something went wrong, I forgot which bot we were editing. Send /help."}
	}
	// 防御性复核 owner（状态是服务端存的，正常已是 owned bot）。
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
		reply = okReply(err, fmt.Sprintf("Success! Name updated for @%s.", username), "Sorry, that name is invalid. Please try a different one.")
	case botFatherCmdSetDescription:
		_, err = s.SetBotInfo(ctx, botID, domain.BotInfoUpdate{SetDescription: true, Description: text})
		reply = okReply(err, "Success! Description updated.", "Sorry, that description is too long.")
	case botFatherCmdSetAbout:
		_, err = s.SetBotInfo(ctx, botID, domain.BotInfoUpdate{SetAbout: true, About: text})
		reply = okReply(err, "Success! About section updated.", "Sorry, that about text is too long.")
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
	default:
		s.clearState(ctx, state.UserID)
		return internalReply()
	}
	if err != nil {
		// 校验类错误已转成提示文案；非校验错误内部已记日志。保留 state 让用户重试。
		if reply.Text == "" {
			return internalReply()
		}
		return reply
	}
	s.clearState(ctx, state.UserID)
	return reply
}

// applySetCommands 解析多行 "command - Description"（/empty 清空）并写入。
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
				return botReply{Text: "Invalid format. Each line must be: command - Description. Try again or /cancel."}, domain.ErrBotCommandInvalid
			}
			commands = append(commands, domain.BotCommand{
				Command:     strings.TrimSpace(cmd),
				Description: strings.TrimSpace(desc),
			})
		}
	}
	if _, err := s.SetBotCommands(ctx, botID, commands); err != nil {
		return botReply{Text: "Invalid command list. Each command must be 1-32 chars (letters, digits, underscores) with a non-empty description. Try again or /cancel."}, err
	}
	return botReply{Text: "Success! Command list updated. /help"}, nil
}

// applySetInline 写入 inline placeholder；/empty 清空并关闭 inline mode。
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
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	return botReply{Text: fmt.Sprintf("Success! Inline location requests are now %s.", state)}, nil
}

// applyToggle 解析 enable/disable 并设置 joingroups（join=true）或 privacy（join=false）。
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
	what := "Group privacy"
	if join {
		what = "Group joining"
	}
	state := "disabled"
	if on {
		state = "enabled"
	}
	return botReply{Text: fmt.Sprintf("Success! %s is now %s.", what, state)}, nil
}

// okReply 把 service 调用结果转成提示：err==nil 回成功，否则回校验失败提示。
func okReply(err error, ok, fail string) botReply {
	if err != nil {
		return botReply{Text: fail}
	}
	return botReply{Text: ok}
}

// clearState 删除 BotFather 对话状态（忽略错误，仅记日志）。
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

// tokenReply 拼装带 code entity 的 token 消息（全 ASCII 文本，offset 按字节即按
// UTF-16 code unit 成立）。
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

// parseBotCommand 解析行首 "/cmd"（容忍 "/cmd@BotFather" 与尾随参数），返回小写命令名。
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
