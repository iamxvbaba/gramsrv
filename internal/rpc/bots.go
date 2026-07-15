package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// registerBots 注册 bots.* RPC handler。
//
// 权限语义（官方访问矩阵）：
//   - setBotCommands/getBotCommands/resetBotCommands/setBotMenuButton/getBotMenuButton：
//     仅 bot 自己（调用者必须是 bot 账号）。
//   - setBotInfo/getBotInfo：owner 经 bot:InputUser 代设，或 bot 自己（不带 bot 参数）。
//
// P2 范围：仅 default scope、单语言（非 default scope 与非空 lang_code 一律接受
// 但不存储，避免覆盖全局——见各 handler 的 isDefaultBotCommandScope/lang_code 闸门）；
// menu button 为 per-bot 全局（per-user 维度记 todo）。元数据写入由 service 在事务内
// bump bot_info_version；命令变更后 bots_hooks.PushBotCommandsChanged 给在线相关用户
// 推 updateBotCommands（扇出封顶 100，无 pts），离线/超界用户靠 version bump 在下次
// getFullUser 重拉兜底。
func (r *Router) registerBots(d *tg.ServerDispatcher) {
	d.OnBotsSendCustomRequest(r.onBotsSendCustomRequest)
	d.OnBotsAnswerWebhookJSONQuery(r.onBotsAnswerWebhookJSONQuery)
	d.OnBotsSetBotBroadcastDefaultAdminRights(r.onBotsSetBotBroadcastDefaultAdminRights)
	d.OnBotsSetBotGroupDefaultAdminRights(r.onBotsSetBotGroupDefaultAdminRights)
	d.OnBotsSetBotCommands(r.onBotsSetBotCommands)
	d.OnBotsResetBotCommands(r.onBotsResetBotCommands)
	d.OnBotsGetBotCommands(r.onBotsGetBotCommands)
	d.OnBotsSetBotInfo(r.onBotsSetBotInfo)
	d.OnBotsGetBotInfo(r.onBotsGetBotInfo)
	d.OnBotsSetBotMenuButton(r.onBotsSetBotMenuButton)
	d.OnBotsGetBotMenuButton(r.onBotsGetBotMenuButton)
	d.OnBotsReorderUsernames(r.onBotsReorderUsernames)
	d.OnBotsToggleUsername(r.onBotsToggleUsername)
	d.OnBotsCanSendMessage(r.onBotsCanSendMessage)
	d.OnBotsAllowSendMessage(r.onBotsAllowSendMessage)
	d.OnBotsInvokeWebViewCustomMethod(r.onBotsInvokeWebViewCustomMethod)
	d.OnBotsGetPopularAppBots(r.onBotsGetPopularAppBots)
	d.OnBotsAddPreviewMedia(r.onBotsAddPreviewMedia)
	d.OnBotsEditPreviewMedia(r.onBotsEditPreviewMedia)
	d.OnBotsDeletePreviewMedia(r.onBotsDeletePreviewMedia)
	d.OnBotsReorderPreviewMedias(r.onBotsReorderPreviewMedias)
	d.OnBotsGetPreviewInfo(r.onBotsGetPreviewInfo)
	d.OnBotsGetPreviewMedias(r.onBotsGetPreviewMedias)
	d.OnBotsUpdateUserEmojiStatus(r.onBotsUpdateUserEmojiStatus)
	d.OnBotsToggleUserEmojiStatusPermission(r.onBotsToggleUserEmojiStatusPermission)
	d.OnBotsCheckDownloadFileParams(r.onBotsCheckDownloadFileParams)
	d.OnBotsGetAdminedBots(r.onBotsGetAdminedBots)
	d.OnBotsUpdateStarRefProgram(r.onBotsUpdateStarRefProgram)
	d.OnBotsSetCustomVerification(r.onBotsSetCustomVerification)
	d.OnBotsGetBotRecommendations(r.onBotsGetBotRecommendations)
	d.OnBotsCheckUsername(r.onBotsCheckUsername)
	d.OnBotsCreateBot(r.onBotsCreateBot)
	d.OnBotsExportBotToken(r.onBotsExportBotToken)
	d.OnBotsRequestWebViewButton(r.onBotsRequestWebViewButton)
	d.OnBotsGetRequestedWebViewButton(r.onBotsGetRequestedWebViewButton)
	d.OnBotsGetAccessSettings(r.onBotsGetAccessSettings)
	d.OnBotsEditAccessSettings(r.onBotsEditAccessSettings)
	// P3：startBot 深链 + inline callback 闭环。
	d.OnMessagesStartBot(r.onMessagesStartBot)
	d.OnMessagesGetBotCallbackAnswer(r.onMessagesGetBotCallbackAnswer)
	d.OnMessagesSetBotCallbackAnswer(r.onMessagesSetBotCallbackAnswer)
	d.OnMessagesGetInlineBotResults(r.onMessagesGetInlineBotResults)
	d.OnMessagesSetInlineBotResults(r.onMessagesSetInlineBotResults)
	d.OnMessagesSendInlineBotResult(r.onMessagesSendInlineBotResult)
	d.OnMessagesSavePreparedInlineMessage(r.onMessagesSavePreparedInlineMessage)
	d.OnMessagesEditInlineBotMessage(r.onMessagesEditInlineBotMessage)
	d.OnMessagesSetBotShippingResults(r.onMessagesSetBotShippingResults)
	d.OnMessagesSetBotPrecheckoutResults(r.onMessagesSetBotPrecheckoutResults)
}

// callerBotID 校验调用者本身是 bot 账号，返回其 user_id（bot-only RPC 用）。
func (r *Router) callerBotID(ctx context.Context) (int64, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, internalErr()
	}
	if r.deps.Bots == nil || userID == 0 {
		return 0, userBotRequiredErr()
	}
	if _, found, err := r.deps.Bots.BotInfo(ctx, userID); err != nil {
		return 0, internalErr()
	} else if !found {
		return 0, userBotRequiredErr()
	}
	return userID, nil
}

func (r *Router) onBotsGetAdminedBots(ctx context.Context) ([]tg.UserClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Bots == nil {
		return []tg.UserClass{}, nil
	}
	bots, err := r.deps.Bots.ListOwnedBots(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return r.tgUsersForViewer(userID, bots), nil
}

func (r *Router) onBotsCheckUsername(ctx context.Context, username string) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Bots == nil {
		return false, internalErr()
	}
	ok, err := r.deps.Bots.CheckUsername(ctx, userID, username)
	if err != nil {
		return false, botUsernameErr(err)
	}
	return ok, nil
}

func (r *Router) onBotsCreateBot(ctx context.Context, req *tg.BotsCreateBotRequest) (tg.UserClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Bots == nil {
		return nil, internalErr()
	}
	if err := r.validateBotManager(ctx, userID, req.ManagerID); err != nil {
		return nil, err
	}
	u, _, err := r.deps.Bots.CreateBot(ctx, userID, req.Name, req.Username)
	if err != nil {
		return nil, createBotErr(err)
	}
	return r.tgUser(u), nil
}

func (r *Router) validateBotManager(ctx context.Context, currentUserID int64, manager tg.InputUserClass) error {
	if manager == nil || r.deps.Bots == nil {
		return managerPermissionMissingErr()
	}
	u, found, err := r.userFromInput(ctx, currentUserID, manager)
	if err != nil || !found {
		return managerPermissionMissingErr()
	}
	if _, found, err := r.deps.Bots.BotInfo(ctx, u.ID); err != nil {
		return internalErr()
	} else if !found {
		return managerPermissionMissingErr()
	}
	return nil
}

func (r *Router) onBotsExportBotToken(ctx context.Context, req *tg.BotsExportBotTokenRequest) (*tg.BotsExportedBotToken, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Bots == nil {
		return nil, internalErr()
	}
	target, found, err := r.userFromInput(ctx, userID, req.Bot)
	if err != nil || !found {
		return nil, botInvalidErr()
	}
	token, err := r.deps.Bots.ExportBotToken(ctx, userID, target.ID, req.Revoke)
	if err != nil {
		if errors.Is(err, domain.ErrBotSessionsNotRevoked) && token != "" {
			return &tg.BotsExportedBotToken{Token: token}, nil
		}
		return nil, exportBotTokenErr(err)
	}
	return &tg.BotsExportedBotToken{Token: token}, nil
}

// isDefaultCommandsTarget 报告请求是否落在 P2 唯一持久化的桶（default scope +
// 空 lang_code）。非 default scope 或非空 lang_code 一律接受但不存储——否则会
// 用某语言/某 scope 的命令覆盖唯一的全局 default 桶（reset 还会误清全局）。
func isDefaultCommandsTarget(scope tg.BotCommandScopeClass, langCode string) bool {
	return isDefaultBotCommandScope(scope) && langCode == ""
}

func (r *Router) onBotsSetBotCommands(ctx context.Context, req *tg.BotsSetBotCommandsRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	// P2 仅持久化 default scope + 空 lang_code；其它接受但不存储（记 todo），避免
	// 覆盖全局桶，也避免客户端因 error 重试。
	if !isDefaultCommandsTarget(req.Scope, req.LangCode) {
		return true, nil
	}
	if _, err := r.deps.Bots.SetBotCommands(ctx, botID, domainBotCommands(req.Commands)); err != nil {
		return false, setBotCommandsErr(err)
	}
	r.invalidateChannelFullBotInfoCache()
	r.invalidateRPCProjectionForUser(botID)
	return true, nil
}

func (r *Router) onBotsResetBotCommands(ctx context.Context, req *tg.BotsResetBotCommandsRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	if !isDefaultCommandsTarget(req.Scope, req.LangCode) {
		return true, nil
	}
	if _, err := r.deps.Bots.SetBotCommands(ctx, botID, nil); err != nil {
		return false, setBotCommandsErr(err)
	}
	r.invalidateChannelFullBotInfoCache()
	r.invalidateRPCProjectionForUser(botID)
	return true, nil
}

func (r *Router) onBotsGetBotCommands(ctx context.Context, req *tg.BotsGetBotCommandsRequest) ([]tg.BotCommand, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	if !isDefaultCommandsTarget(req.Scope, req.LangCode) {
		return []tg.BotCommand{}, nil
	}
	commands, err := r.deps.Bots.GetBotCommands(ctx, botID)
	if err != nil {
		return nil, internalErr()
	}
	return tgBotCommands(commands), nil
}

// resolveBotInfoTarget 解析 setBotInfo/getBotInfo 的目标 bot：带 bot 参数→owner 校验
// 该 bot；不带→调用者本身须为 bot。
func (r *Router) resolveBotInfoTarget(ctx context.Context, bot tg.InputUserClass, hasBot bool) (int64, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, internalErr()
	}
	if r.deps.Bots == nil {
		return 0, userBotInvalidErr()
	}
	if hasBot && bot != nil {
		target, found, err := r.userFromInput(ctx, userID, bot)
		if err != nil || !found {
			return 0, botInvalidErr()
		}
		owns, err := r.deps.Bots.OwnsBot(ctx, userID, target.ID)
		if err != nil {
			return 0, internalErr()
		}
		if !owns {
			return 0, botInvalidErr()
		}
		return target.ID, nil
	}
	// 无 bot 参数：调用者必须是 bot 自己。
	if _, found, err := r.deps.Bots.BotInfo(ctx, userID); err != nil {
		return 0, internalErr()
	} else if !found {
		return 0, userBotInvalidErr()
	}
	return userID, nil
}

func (r *Router) onBotsSetBotInfo(ctx context.Context, req *tg.BotsSetBotInfoRequest) (bool, error) {
	bot, hasBot := req.GetBot()
	botID, err := r.resolveBotInfoTarget(ctx, bot, hasBot)
	if err != nil {
		return false, err
	}
	// 非空 lang_code：本地化 name/about/description 接受但不存储（否则写穿全局列）。
	if req.LangCode != "" {
		return true, nil
	}
	var upd domain.BotInfoUpdate
	if name, ok := req.GetName(); ok {
		upd.SetName, upd.Name = true, name
	}
	if about, ok := req.GetAbout(); ok {
		upd.SetAbout, upd.About = true, about
	}
	if description, ok := req.GetDescription(); ok {
		upd.SetDescription, upd.Description = true, description
	}
	if !upd.SetName && !upd.SetAbout && !upd.SetDescription {
		return true, nil
	}
	if _, err := r.deps.Bots.SetBotInfo(ctx, botID, upd); err != nil {
		return false, setBotInfoErr(err)
	}
	r.invalidateChannelFullBotInfoCache()
	r.invalidateRPCProjectionForUser(botID)
	return true, nil
}

func (r *Router) onBotsGetBotInfo(ctx context.Context, req *tg.BotsGetBotInfoRequest) (*tg.BotsBotInfo, error) {
	bot, hasBot := req.GetBot()
	botID, err := r.resolveBotInfoTarget(ctx, bot, hasBot)
	if err != nil {
		return nil, err
	}
	name, about, description, err := r.deps.Bots.GetBotInfo(ctx, botID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.BotsBotInfo{Name: name, About: about, Description: description}, nil
}

func (r *Router) onBotsSetBotMenuButton(ctx context.Context, req *tg.BotsSetBotMenuButtonRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	button, err := domainBotMenuButton(req.Button)
	if err != nil {
		return false, err
	}
	if _, err := r.deps.Bots.SetBotMenuButton(ctx, botID, button); err != nil {
		return false, setBotMenuButtonErr(err)
	}
	r.invalidateChannelFullBotInfoCache()
	r.invalidateRPCProjectionForUser(botID)
	return true, nil
}

func (r *Router) onBotsGetBotMenuButton(ctx context.Context, userid tg.InputUserClass) (tg.BotMenuButtonClass, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	button, err := r.deps.Bots.GetBotMenuButton(ctx, botID)
	if err != nil {
		return nil, internalErr()
	}
	return tgBotMenuButton(button), nil
}

// --- tg ↔ domain 转换 ---

func isDefaultBotCommandScope(scope tg.BotCommandScopeClass) bool {
	if scope == nil {
		return true
	}
	_, ok := scope.(*tg.BotCommandScopeDefault)
	return ok
}

func domainBotCommands(in []tg.BotCommand) []domain.BotCommand {
	out := make([]domain.BotCommand, 0, len(in))
	for _, c := range in {
		out = append(out, domain.BotCommand{Command: c.Command, Description: c.Description})
	}
	return out
}

func tgBotCommands(in []domain.BotCommand) []tg.BotCommand {
	out := make([]tg.BotCommand, 0, len(in))
	for _, c := range in {
		out = append(out, tg.BotCommand{Command: c.Command, Description: c.Description})
	}
	return out
}

func domainBotMenuButton(in tg.BotMenuButtonClass) (domain.BotMenuButton, error) {
	switch v := in.(type) {
	case *tg.BotMenuButtonDefault:
		return domain.BotMenuButton{Type: domain.BotMenuButtonDefault}, nil
	case *tg.BotMenuButtonCommands:
		return domain.BotMenuButton{Type: domain.BotMenuButtonCommands}, nil
	case *tg.BotMenuButton:
		return domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: v.Text, URL: v.URL}, nil
	default:
		return domain.BotMenuButton{}, botMenuButtonInvalidErr()
	}
}

func tgBotMenuButton(b domain.BotMenuButton) tg.BotMenuButtonClass {
	switch b.Type {
	case domain.BotMenuButtonCommands:
		return &tg.BotMenuButtonCommands{}
	case domain.BotMenuButtonWebView:
		return &tg.BotMenuButton{Text: b.Text, URL: b.URL}
	default:
		return &tg.BotMenuButtonDefault{}
	}
}
