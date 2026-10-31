package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

// registerBots registers the bots.* RPC handlers.
//
// Permission semantics (official access matrix):
//   - setBotCommands/getBotCommands/resetBotCommands/setBotMenuButton/getBotMenuButton:
//     only the bot itself (the caller must be a bot account).
//   - setBotInfo/getBotInfo: the owner sets it on the bot's behalf via bot:InputUser, or the bot
//     itself (without the bot parameter).
//
// P2 scope: only the default scope, single language (any non-default scope or non-empty
// lang_code is accepted but not stored, to avoid overwriting the global -- see each handler's
// isDefaultBotCommandScope/lang_code gate); the menu button is per-bot global (the per-user
// dimension is a todo). Metadata writes bump bot_info_version inside the service's transaction;
// after a command change bots_hooks.PushBotCommandsChanged pushes updateBotCommands to the
// relevant online users (fan-out capped at 100, no pts), and offline/over-cap users re-fetch via
// a version bump on the next getFullUser as a fallback.
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
		return r.onBotsCheckUsername(ctx, layerRequest)
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

		// P3: startBot deep link + inline callback loop.
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

		// callerBotID validates that the caller itself is a bot account and returns its user_id (for bot-only RPCs).
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

func (r *Router) onBotsCheckUsername(ctx context.Context, req *tg.BotsCheckUsernameRequest) (bool, error) {
	if req == nil {
		return false, usernameInvalidErr()
	}
	// Additional bot usernames have distinct ownership/lifecycle semantics.
	if req.Additional || req.Flags.Has(0) {
		return false, methodInvalidErr()
	}
	username := req.Username
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

// isDefaultCommandsTarget reports whether the request falls in the one bucket P2 persists
// (default scope + empty lang_code). Any non-default scope or non-empty lang_code is accepted but
// not stored -- otherwise a command for some language/scope would overwrite the single global
// default bucket (and reset would wrongly clear the global one).
func isDefaultCommandsTarget(scope tg.BotCommandScopeClass, langCode string) bool {
	return isDefaultBotCommandScope(scope) && langCode == ""
}

func (r *Router) onBotsSetBotCommands(ctx context.Context, req *tg.BotsSetBotCommandsRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	// P2 persists only default scope + empty lang_code; anything else is accepted but not stored
	// (todo), to avoid overwriting the global bucket and to avoid the client retrying on an error.
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

// resolveBotInfoTarget resolves the target bot of setBotInfo/getBotInfo: with a bot parameter ->
// the owner is validated against that bot; without -> the caller itself must be a bot.
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
	// No bot parameter: the caller must be the bot itself.
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
	// Non-empty lang_code: the localized name/about/description is accepted but not stored (otherwise it would write through to the global columns).
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

// --- tg <-> domain conversion ---

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
		out = append(out, domain.BotCommand{Command: c.Command, Description: c.Description, Ephemeral: c.Ephemeral})
	}
	return out
}

func tgBotCommands(in []domain.BotCommand) []tg.BotCommand {
	out := make([]tg.BotCommand, 0, len(in))
	for _, c := range in {
		out = append(out, tg.BotCommand{Command: c.Command, Description: c.Description, Ephemeral: c.Ephemeral})
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
