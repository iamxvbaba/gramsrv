package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
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
func (r *Router) registerBots(d *tlprofile.Dispatcher) {
	registerRPC[*tg.BotsSendCustomRequestRequest](d, tlprofile.SemanticMethodBotsSendCustomRequest, func(ctx context.Context, layerRequest *tg.BotsSendCustomRequestRequest) (any, error) {
		return r.onBotsSendCustomRequest(ctx, layerRequest)
	})
	registerRPC[*tg.BotsAnswerWebhookJSONQueryRequest](d, tlprofile.SemanticMethodBotsAnswerWebhookJSONQuery, func(ctx context.Context, layerRequest *tg.BotsAnswerWebhookJSONQueryRequest) (any, error) {
		return r.onBotsAnswerWebhookJSONQuery(ctx, layerRequest)
	})
	registerRPC[*tg.BotsSetBotBroadcastDefaultAdminRightsRequest](d, tlprofile.SemanticMethodBotsSetBotBroadcastDefaultAdminRights, func(ctx context.Context, layerRequest *tg.BotsSetBotBroadcastDefaultAdminRightsRequest) (any, error) {
		return r.onBotsSetBotBroadcastDefaultAdminRights(ctx, layerRequest.
			AdminRights)
	})
	registerRPC[*tg.BotsSetBotGroupDefaultAdminRightsRequest](d, tlprofile.SemanticMethodBotsSetBotGroupDefaultAdminRights, func(ctx context.Context, layerRequest *tg.BotsSetBotGroupDefaultAdminRightsRequest) (any, error) {
		return r.onBotsSetBotGroupDefaultAdminRights(ctx, layerRequest.
			AdminRights)
	})
	registerRPC[*tg.BotsSetBotCommandsRequest](d, tlprofile.SemanticMethodBotsSetBotCommands, func(ctx context.Context, layerRequest *tg.BotsSetBotCommandsRequest) (any, error) {
		return r.onBotsSetBotCommands(ctx, layerRequest)
	})
	registerRPC[*tg.BotsResetBotCommandsRequest](d, tlprofile.SemanticMethodBotsResetBotCommands, func(ctx context.Context, layerRequest *tg.BotsResetBotCommandsRequest) (any, error) {
		return r.onBotsResetBotCommands(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetBotCommandsRequest](d, tlprofile.SemanticMethodBotsGetBotCommands, func(ctx context.Context, layerRequest *tg.BotsGetBotCommandsRequest) (any, error) {
		return r.onBotsGetBotCommands(ctx, layerRequest)
	})
	registerRPC[*tg.BotsSetBotInfoRequest](d, tlprofile.SemanticMethodBotsSetBotInfo, func(ctx context.Context, layerRequest *tg.BotsSetBotInfoRequest) (any, error) {
		return r.onBotsSetBotInfo(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetBotInfoRequest](d, tlprofile.SemanticMethodBotsGetBotInfo, func(ctx context.Context, layerRequest *tg.BotsGetBotInfoRequest) (any, error) {
		return r.onBotsGetBotInfo(ctx, layerRequest)
	})
	registerRPC[*tg.BotsSetBotMenuButtonRequest](d, tlprofile.SemanticMethodBotsSetBotMenuButton, func(ctx context.Context, layerRequest *tg.BotsSetBotMenuButtonRequest) (any, error) {
		return r.onBotsSetBotMenuButton(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetBotMenuButtonRequest](d, tlprofile.SemanticMethodBotsGetBotMenuButton, func(ctx context.Context, layerRequest *tg.BotsGetBotMenuButtonRequest) (any, error) {
		return r.onBotsGetBotMenuButton(ctx, layerRequest.
			UserID)
	})
	registerRPC[*tg.BotsReorderUsernamesRequest](d, tlprofile.SemanticMethodBotsReorderUsernames, func(ctx context.Context, layerRequest *tg.BotsReorderUsernamesRequest) (any, error) {
		return r.onBotsReorderUsernames(ctx, layerRequest)
	})
	registerRPC[*tg.BotsToggleUsernameRequest](d, tlprofile.SemanticMethodBotsToggleUsername, func(ctx context.Context, layerRequest *tg.BotsToggleUsernameRequest) (any, error) {
		return r.onBotsToggleUsername(ctx, layerRequest)
	})
	registerRPC[*tg.BotsCanSendMessageRequest](d, tlprofile.SemanticMethodBotsCanSendMessage, func(ctx context.Context, layerRequest *tg.BotsCanSendMessageRequest) (any, error) {
		return r.onBotsCanSendMessage(ctx, layerRequest.
			Bot)
	})
	registerRPC[*tg.BotsAllowSendMessageRequest](d, tlprofile.SemanticMethodBotsAllowSendMessage, func(ctx context.Context, layerRequest *tg.BotsAllowSendMessageRequest) (any, error) {
		return r.onBotsAllowSendMessage(ctx, layerRequest.
			Bot)
	})
	registerRPC[*tg.BotsInvokeWebViewCustomMethodRequest](d, tlprofile.SemanticMethodBotsInvokeWebViewCustomMethod, func(ctx context.Context, layerRequest *tg.BotsInvokeWebViewCustomMethodRequest) (any, error) {
		return r.onBotsInvokeWebViewCustomMethod(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetPopularAppBotsRequest](d, tlprofile.SemanticMethodBotsGetPopularAppBots, func(ctx context.Context, layerRequest *tg.BotsGetPopularAppBotsRequest) (any, error) {
		return r.onBotsGetPopularAppBots(ctx, layerRequest)
	})
	registerRPC[*tg.BotsAddPreviewMediaRequest](d, tlprofile.SemanticMethodBotsAddPreviewMedia, func(ctx context.Context, layerRequest *tg.BotsAddPreviewMediaRequest) (any, error) {
		return r.onBotsAddPreviewMedia(ctx, layerRequest)
	})
	registerRPC[*tg.BotsEditPreviewMediaRequest](d, tlprofile.SemanticMethodBotsEditPreviewMedia, func(ctx context.Context, layerRequest *tg.BotsEditPreviewMediaRequest) (any, error) {
		return r.onBotsEditPreviewMedia(ctx, layerRequest)
	})
	registerRPC[*tg.BotsDeletePreviewMediaRequest](d, tlprofile.SemanticMethodBotsDeletePreviewMedia, func(ctx context.Context, layerRequest *tg.BotsDeletePreviewMediaRequest) (any, error) {
		return r.onBotsDeletePreviewMedia(ctx, layerRequest)
	})
	registerRPC[*tg.BotsReorderPreviewMediasRequest](d, tlprofile.SemanticMethodBotsReorderPreviewMedias, func(ctx context.Context, layerRequest *tg.BotsReorderPreviewMediasRequest) (any, error) {
		return r.onBotsReorderPreviewMedias(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetPreviewInfoRequest](d, tlprofile.SemanticMethodBotsGetPreviewInfo, func(ctx context.Context, layerRequest *tg.BotsGetPreviewInfoRequest) (any, error) {
		return r.onBotsGetPreviewInfo(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetPreviewMediasRequest](d, tlprofile.SemanticMethodBotsGetPreviewMedias, func(ctx context.Context, layerRequest *tg.BotsGetPreviewMediasRequest) (any, error) {
		return r.onBotsGetPreviewMedias(ctx, layerRequest.
			Bot)
	})
	registerRPC[*tg.BotsUpdateUserEmojiStatusRequest](d, tlprofile.SemanticMethodBotsUpdateUserEmojiStatus, func(ctx context.Context, layerRequest *tg.BotsUpdateUserEmojiStatusRequest) (any, error) {
		return r.onBotsUpdateUserEmojiStatus(ctx, layerRequest)
	})
	registerRPC[*tg.BotsToggleUserEmojiStatusPermissionRequest](d, tlprofile.SemanticMethodBotsToggleUserEmojiStatusPermission, func(ctx context.Context, layerRequest *tg.BotsToggleUserEmojiStatusPermissionRequest) (any, error) {
		return r.onBotsToggleUserEmojiStatusPermission(ctx, layerRequest)
	})
	registerRPC[*tg.BotsCheckDownloadFileParamsRequest](d, tlprofile.SemanticMethodBotsCheckDownloadFileParams, func(ctx context.Context, layerRequest *tg.BotsCheckDownloadFileParamsRequest) (any, error) {
		return r.onBotsCheckDownloadFileParams(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetAdminedBotsRequest](d, tlprofile.SemanticMethodBotsGetAdminedBots, func(ctx context.Context, layerRequest *tg.BotsGetAdminedBotsRequest) (any, error) {
		return r.onBotsGetAdminedBots(ctx)
	})
	registerRPC[*tg.BotsUpdateStarRefProgramRequest](d, tlprofile.SemanticMethodBotsUpdateStarRefProgram, func(ctx context.Context, layerRequest *tg.BotsUpdateStarRefProgramRequest) (any, error) {
		return r.onBotsUpdateStarRefProgram(ctx, layerRequest)
	})
	registerRPC[*tg.BotsSetCustomVerificationRequest](d, tlprofile.SemanticMethodBotsSetCustomVerification, func(ctx context.Context, layerRequest *tg.BotsSetCustomVerificationRequest) (any, error) {
		return r.onBotsSetCustomVerification(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetBotRecommendationsRequest](d, tlprofile.SemanticMethodBotsGetBotRecommendations, func(ctx context.Context, layerRequest *tg.BotsGetBotRecommendationsRequest) (any, error) {
		return r.onBotsGetBotRecommendations(ctx, layerRequest.
			Bot)
	})
	registerRPC[*tg.BotsCheckUsernameRequest](d, tlprofile.SemanticMethodBotsCheckUsername, func(ctx context.Context, layerRequest *tg.BotsCheckUsernameRequest) (any, error) {
		return r.onBotsCheckUsername(ctx, layerRequest.
			Username)
	})
	registerRPC[*tg.BotsCreateBotRequest](d, tlprofile.SemanticMethodBotsCreateBot, func(ctx context.Context, layerRequest *tg.BotsCreateBotRequest) (any, error) {
		return r.onBotsCreateBot(ctx, layerRequest)
	})
	registerRPC[*tg.BotsExportBotTokenRequest](d, tlprofile.SemanticMethodBotsExportBotToken, func(ctx context.Context, layerRequest *tg.BotsExportBotTokenRequest) (any, error) {
		return r.onBotsExportBotToken(ctx, layerRequest)
	})
	registerRPC[*tg.BotsRequestWebViewButtonRequest](d, tlprofile.SemanticMethodBotsRequestWebViewButton, func(ctx context.Context, layerRequest *tg.BotsRequestWebViewButtonRequest) (any, error) {
		return r.onBotsRequestWebViewButton(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetRequestedWebViewButtonRequest](d, tlprofile.SemanticMethodBotsGetRequestedWebViewButton, func(ctx context.Context, layerRequest *tg.BotsGetRequestedWebViewButtonRequest) (any, error) {
		return r.onBotsGetRequestedWebViewButton(ctx, layerRequest)
	})
	registerRPC[*tg.BotsGetAccessSettingsRequest](d, tlprofile.SemanticMethodBotsGetAccessSettings, func(ctx context.Context, layerRequest *tg.BotsGetAccessSettingsRequest) (

		// P3：startBot 深链 + inline callback 闭环。
		any, error) {
		return r.onBotsGetAccessSettings(ctx, layerRequest.
			Bot)
	})
	registerRPC[*tg.BotsEditAccessSettingsRequest](d, tlprofile.SemanticMethodBotsEditAccessSettings, func(ctx context.Context, layerRequest *tg.BotsEditAccessSettingsRequest) (any, error) {
		return r.onBotsEditAccessSettings(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesStartBotRequest](d, tlprofile.SemanticMethodMessagesStartBot, func(ctx context.Context, layerRequest *tg.MessagesStartBotRequest) (any, error) {
		return r.onMessagesStartBot(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetBotCallbackAnswerRequest](d, tlprofile.SemanticMethodMessagesGetBotCallbackAnswer, func(ctx context.Context, layerRequest *tg.MessagesGetBotCallbackAnswerRequest) (any, error) {
		return r.onMessagesGetBotCallbackAnswer(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetBotCallbackAnswerRequest](d, tlprofile.SemanticMethodMessagesSetBotCallbackAnswer, func(ctx context.Context, layerRequest *tg.MessagesSetBotCallbackAnswerRequest) (any, error) {
		return r.onMessagesSetBotCallbackAnswer(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetInlineBotResultsRequest](d, tlprofile.SemanticMethodMessagesGetInlineBotResults, func(ctx context.Context, layerRequest *tg.MessagesGetInlineBotResultsRequest) (any, error) {
		return r.onMessagesGetInlineBotResults(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetInlineBotResultsRequest](d, tlprofile.SemanticMethodMessagesSetInlineBotResults, func(ctx context.Context, layerRequest *tg.MessagesSetInlineBotResultsRequest) (any, error) {
		return r.onMessagesSetInlineBotResults(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendInlineBotResultRequest](d, tlprofile.SemanticMethodMessagesSendInlineBotResult, func(ctx context.Context, layerRequest *tg.MessagesSendInlineBotResultRequest) (any, error) {
		return r.onMessagesSendInlineBotResult(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSavePreparedInlineMessageRequest](d, tlprofile.SemanticMethodMessagesSavePreparedInlineMessage, func(ctx context.Context, layerRequest *tg.MessagesSavePreparedInlineMessageRequest) (any, error) {
		return r.onMessagesSavePreparedInlineMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditInlineBotMessageRequest](d, tlprofile.SemanticMethodMessagesEditInlineBotMessage, func(ctx context.Context, layerRequest *tg.MessagesEditInlineBotMessageRequest) (any, error) {
		return r.onMessagesEditInlineBotMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetBotShippingResultsRequest](d, tlprofile.SemanticMethodMessagesSetBotShippingResults, func(ctx context.Context, layerRequest *tg.MessagesSetBotShippingResultsRequest) (any, error) {
		return r.onMessagesSetBotShippingResults(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetBotPrecheckoutResultsRequest](d, tlprofile.SemanticMethodMessagesSetBotPrecheckoutResults, func(ctx context.Context,

		// callerBotID 校验调用者本身是 bot 账号，返回其 user_id（bot-only RPC 用）。
		layerRequest *tg.MessagesSetBotPrecheckoutResultsRequest) (any, error) {
		return r.onMessagesSetBotPrecheckoutResults(ctx, layerRequest)
	})
}

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
