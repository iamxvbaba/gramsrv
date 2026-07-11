package rpc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/fnv"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) resolveBotUserForViewer(ctx context.Context, viewerID int64, bot tg.InputUserClass) (domain.User, error) {
	if bot == nil || r.deps.Bots == nil {
		return domain.User{}, botInvalidErr()
	}
	u, found, err := r.userFromInput(ctx, viewerID, bot)
	if err != nil || !found {
		return domain.User{}, botInvalidErr()
	}
	if _, found, err := r.deps.Bots.BotInfo(ctx, u.ID); err != nil {
		return domain.User{}, internalErr()
	} else if !found {
		return domain.User{}, botInvalidErr()
	}
	return u, nil
}

func (r *Router) resolveOwnedBotUser(ctx context.Context, ownerID int64, bot tg.InputUserClass) (domain.User, error) {
	u, err := r.resolveBotUserForViewer(ctx, ownerID, bot)
	if err != nil {
		return domain.User{}, err
	}
	owns, err := r.deps.Bots.OwnsBot(ctx, ownerID, u.ID)
	if err != nil {
		return domain.User{}, internalErr()
	}
	if !owns {
		return domain.User{}, botInvalidErr()
	}
	return u, nil
}

func (r *Router) onBotsSendCustomRequest(ctx context.Context, req *tg.BotsSendCustomRequestRequest) (*tg.DataJSON, error) {
	if _, err := r.callerBotID(ctx); err != nil {
		return nil, err
	}
	return nil, methodInvalidErr()
}

func (r *Router) onBotsAnswerWebhookJSONQuery(ctx context.Context, req *tg.BotsAnswerWebhookJSONQueryRequest) (bool, error) {
	if _, err := r.callerBotID(ctx); err != nil {
		return false, err
	}
	return false, queryIDInvalidErr()
}

func (r *Router) onBotsSetBotBroadcastDefaultAdminRights(ctx context.Context, _ tg.ChatAdminRights) (bool, error) {
	if _, err := r.callerBotID(ctx); err != nil {
		return false, err
	}
	return false, rightsNotModifiedErr()
}

func (r *Router) onBotsSetBotGroupDefaultAdminRights(ctx context.Context, _ tg.ChatAdminRights) (bool, error) {
	if _, err := r.callerBotID(ctx); err != nil {
		return false, err
	}
	return false, rightsNotModifiedErr()
}

func (r *Router) onBotsReorderUsernames(ctx context.Context, req *tg.BotsReorderUsernamesRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if _, err := r.resolveOwnedBotUser(ctx, userID, req.Bot); err != nil {
		return false, err
	}
	return false, usernameNotModifiedErr()
}

func (r *Router) onBotsToggleUsername(ctx context.Context, req *tg.BotsToggleUsernameRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if _, err := r.resolveOwnedBotUser(ctx, userID, req.Bot); err != nil {
		return false, err
	}
	return false, usernameNotModifiedErr()
}

func (r *Router) onBotsCanSendMessage(ctx context.Context, bot tg.InputUserClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	botUser, err := r.resolveBotUserForViewer(ctx, userID, bot)
	if err != nil {
		return false, err
	}
	allowed, err := r.deps.Bots.CanSendMessage(ctx, userID, botUser.ID)
	if err != nil {
		return false, internalErr()
	}
	return allowed, nil
}

func (r *Router) onBotsAllowSendMessage(ctx context.Context, bot tg.InputUserClass) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	botUser, err := r.resolveBotUserForViewer(ctx, userID, bot)
	if err != nil {
		return nil, err
	}
	if _, err := r.deps.Bots.AllowSendMessage(ctx, userID, botUser.ID, true); err != nil {
		return nil, botInvalidErr()
	}
	res, err := r.sendBotAllowedServiceMessage(ctx, userID, botUser.ID)
	if err != nil {
		return nil, internalErr()
	}
	if res.Duplicate {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	users := r.usersForMessageUpdate(ctx, userID, res.SenderMessage)
	chats := r.chatsForMessageUpdate(ctx, userID, res.SenderMessage)
	return tgPrivateMessageUpdates(res.SenderEvent, res.SenderMessage, 0, false, users, chats), nil
}

func (r *Router) sendBotAllowedServiceMessage(ctx context.Context, userID, botUserID int64) (domain.SendPrivateTextResult, error) {
	return r.sendBotAllowedServiceMessageWith(ctx, userID, botUserID, domain.MessageBotAllowedAction{FromRequest: true})
}

func (r *Router) sendBotAllowedServiceMessageWith(ctx context.Context, userID, botUserID int64, action domain.MessageBotAllowedAction) (domain.SendPrivateTextResult, error) {
	if r.deps.Messages == nil {
		return domain.SendPrivateTextResult{}, botInvalidErr()
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, userID, botUserID)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	sessionID, _ := SessionIDFrom(ctx)
	return r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:    userID,
		RecipientUserID: botUserID,
		RandomID:        botAllowedServiceMessageRandomID(userID, botUserID, action),
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind:       domain.MessageServiceActionBotAllowed,
				BotAllowed: &action,
			},
		},
		Date:             int(r.clock.Now().Unix()),
		OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:  sessionID,
		RecipientBlocked: recipientBlocked,
	})
}

func botAllowedServiceMessageRandomID(userID, botUserID int64, action domain.MessageBotAllowedAction) int64 {
	var raw [16]byte
	binary.LittleEndian.PutUint64(raw[:8], uint64(userID))
	binary.LittleEndian.PutUint64(raw[8:], uint64(botUserID))
	h := fnv.New64a()
	_, _ = h.Write(raw[:])
	if action.AttachMenu {
		_, _ = h.Write([]byte("attach_menu"))
	}
	if action.FromRequest {
		_, _ = h.Write([]byte("from_request"))
	}
	if action.Domain != "" {
		_, _ = h.Write([]byte(action.Domain))
	}
	id := int64(h.Sum64() & ((uint64(1) << 63) - 1))
	if id == 0 {
		return -1
	}
	return -id
}

func (r *Router) onBotsInvokeWebViewCustomMethod(ctx context.Context, req *tg.BotsInvokeWebViewCustomMethodRequest) (*tg.DataJSON, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	bot, err := r.resolveBotUserForViewer(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	params := req.Params.Data
	if !json.Valid([]byte(params)) {
		return nil, methodInvalidErr()
	}
	if _, err := r.deps.Bots.PutWebViewCustomMethodQuery(ctx, bot.ID, userID, req.CustomMethod, params); err != nil {
		return nil, methodInvalidErr()
	}
	return nil, methodInvalidErr()
}

func (r *Router) onBotsGetPopularAppBots(ctx context.Context, req *tg.BotsGetPopularAppBotsRequest) (*tg.BotsPopularAppBots, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return &tg.BotsPopularAppBots{Users: []tg.UserClass{}}, nil
}

func (r *Router) onBotsAddPreviewMedia(ctx context.Context, req *tg.BotsAddPreviewMediaRequest) (*tg.BotPreviewMedia, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	bot, err := r.resolveOwnedBotUser(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	app, err := r.mainBotAppForOwner(ctx, bot.ID)
	if err != nil {
		return nil, err
	}
	media, err := r.botPreviewMediaFromInput(ctx, userID, req.Media)
	if err != nil {
		return nil, err
	}
	media.BotUserID = bot.ID
	media.AppID = app.ID
	out, _, err := r.deps.Bots.UpsertBotAppPreviewMedia(ctx, media)
	if err != nil {
		return nil, botAppInvalidErr()
	}
	return r.tgBotPreviewMedia(ctx, out), nil
}

func (r *Router) onBotsEditPreviewMedia(ctx context.Context, req *tg.BotsEditPreviewMediaRequest) (*tg.BotPreviewMedia, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	bot, err := r.resolveOwnedBotUser(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	app, err := r.mainBotAppForOwner(ctx, bot.ID)
	if err != nil {
		return nil, err
	}
	current, err := r.botPreviewMediaIDFromInput(ctx, userID, bot.ID, app.ID, req.Media)
	if err != nil {
		return nil, err
	}
	next, err := r.botPreviewMediaFromInput(ctx, userID, req.NewMedia)
	if err != nil {
		return nil, err
	}
	next.ID = current.ID
	next.Position = current.Position
	next.BotUserID = bot.ID
	next.AppID = app.ID
	out, _, err := r.deps.Bots.UpsertBotAppPreviewMedia(ctx, next)
	if err != nil {
		return nil, botAppInvalidErr()
	}
	return r.tgBotPreviewMedia(ctx, out), nil
}

func (r *Router) onBotsDeletePreviewMedia(ctx context.Context, req *tg.BotsDeletePreviewMediaRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	bot, err := r.resolveOwnedBotUser(ctx, userID, req.Bot)
	if err != nil {
		return false, err
	}
	app, err := r.mainBotAppForOwner(ctx, bot.ID)
	if err != nil {
		return false, err
	}
	if len(req.Media) == 0 || len(req.Media) > domain.MaxBotPreviewMedia {
		return false, mediaInvalidErr()
	}
	for _, input := range req.Media {
		current, err := r.botPreviewMediaIDFromInput(ctx, userID, bot.ID, app.ID, input)
		if err != nil {
			return false, err
		}
		if _, err := r.deps.Bots.DeleteBotAppPreviewMedia(ctx, bot.ID, app.ID, current.ID); err != nil {
			return false, botAppInvalidErr()
		}
	}
	return true, nil
}

func (r *Router) onBotsReorderPreviewMedias(ctx context.Context, req *tg.BotsReorderPreviewMediasRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	bot, err := r.resolveOwnedBotUser(ctx, userID, req.Bot)
	if err != nil {
		return false, err
	}
	app, err := r.mainBotAppForOwner(ctx, bot.ID)
	if err != nil {
		return false, err
	}
	if len(req.Order) == 0 || len(req.Order) > domain.MaxBotPreviewMedia {
		return false, mediaInvalidErr()
	}
	ids := make([]int64, 0, len(req.Order))
	seen := map[int64]bool{}
	for _, media := range req.Order {
		current, err := r.botPreviewMediaIDFromInput(ctx, userID, bot.ID, app.ID, media)
		if err != nil {
			return false, err
		}
		if seen[current.ID] {
			return false, mediaInvalidErr()
		}
		seen[current.ID] = true
		ids = append(ids, current.ID)
	}
	if _, err := r.deps.Bots.ReorderBotAppPreviewMedia(ctx, bot.ID, app.ID, ids); err != nil {
		return false, botAppInvalidErr()
	}
	return true, nil
}

func (r *Router) onBotsGetPreviewInfo(ctx context.Context, req *tg.BotsGetPreviewInfoRequest) (*tg.BotsPreviewInfo, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	bot, err := r.resolveBotUserForViewer(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	app, found, err := r.deps.Bots.GetMainBotApp(ctx, bot.ID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return &tg.BotsPreviewInfo{Media: []tg.BotPreviewMedia{}, LangCodes: []string{}}, nil
	}
	media, err := r.botPreviewMedias(ctx, bot.ID, app.ID)
	if err != nil {
		return nil, err
	}
	langs := []string{}
	if len(media) > 0 {
		langs = append(langs, "")
	}
	return &tg.BotsPreviewInfo{Media: media, LangCodes: langs}, nil
}

func (r *Router) onBotsGetPreviewMedias(ctx context.Context, bot tg.InputUserClass) ([]tg.BotPreviewMedia, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	botUser, err := r.resolveBotUserForViewer(ctx, userID, bot)
	if err != nil {
		return nil, err
	}
	app, found, err := r.deps.Bots.GetMainBotApp(ctx, botUser.ID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return []tg.BotPreviewMedia{}, nil
	}
	return r.botPreviewMedias(ctx, botUser.ID, app.ID)
}

func (r *Router) onBotsUpdateUserEmojiStatus(ctx context.Context, req *tg.BotsUpdateUserEmojiStatusRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req.UserID == nil || req.EmojiStatus == nil {
		return false, userIDInvalidErr()
	}
	if r.deps.Bots == nil || r.deps.Users == nil {
		return false, userPermissionDeniedErr()
	}
	target, found, err := r.userFromInput(ctx, userID, req.UserID)
	if err != nil {
		return false, internalErr()
	}
	if !found {
		return false, userIDInvalidErr()
	}
	allowed, err := r.deps.Bots.BotEmojiStatusPermission(ctx, botID, target.ID)
	if err != nil {
		return false, internalErr()
	}
	if !allowed {
		return false, userPermissionDeniedErr()
	}
	documentID, until, err := botEmojiStatusFromTG(req.EmojiStatus)
	if err != nil {
		return false, err
	}
	svc, ok := r.deps.Users.(UserPremiumService)
	if !ok {
		return false, userPermissionDeniedErr()
	}
	u, err := svc.UpdateEmojiStatus(ctx, target.ID, documentID, until)
	if err != nil {
		if errors.Is(err, domain.ErrPremiumRequired) {
			return false, tgerr400("PREMIUM_ACCOUNT_REQUIRED")
		}
		return false, internalErr()
	}
	r.invalidateRPCProjectionForUser(u.ID)
	r.pushUserUpdates(ctx, u.ID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUserEmojiStatus{
			UserID:      u.ID,
			EmojiStatus: tgUserEmojiStatus(u, r.clock.Now().Unix()),
		}},
		Users: []tg.UserClass{r.tgUser(u)},
		Date:  int(r.clock.Now().Unix()),
	})
	return true, nil
}

func (r *Router) onBotsToggleUserEmojiStatusPermission(ctx context.Context, req *tg.BotsToggleUserEmojiStatusPermissionRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Bots == nil {
		return false, botInvalidErr()
	}
	bot, err := r.resolveBotUserForViewer(ctx, userID, req.Bot)
	if err != nil {
		return false, err
	}
	if err := r.deps.Bots.SetBotEmojiStatusPermission(ctx, bot.ID, userID, req.Enabled); err != nil {
		return false, botInvalidErr()
	}
	return true, nil
}

func (r *Router) onBotsCheckDownloadFileParams(ctx context.Context, req *tg.BotsCheckDownloadFileParamsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if _, err := r.resolveBotUserForViewer(ctx, userID, req.Bot); err != nil {
		return false, err
	}
	return checkBotDownloadFileParams(ctx, req.FileName, req.URL), nil
}

func (r *Router) onBotsUpdateStarRefProgram(ctx context.Context, req *tg.BotsUpdateStarRefProgramRequest) (*tg.StarRefProgram, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if _, err := r.resolveOwnedBotUser(ctx, userID, req.Bot); err != nil {
		return nil, err
	}
	return nil, botInvalidErr()
}

func (r *Router) onBotsSetCustomVerification(ctx context.Context, req *tg.BotsSetCustomVerificationRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if bot, ok := req.GetBot(); ok {
		if _, err := r.resolveOwnedBotUser(ctx, userID, bot); err != nil {
			return false, err
		}
	} else if _, err := r.callerBotID(ctx); err != nil {
		return false, err
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return false, err
	}
	return false, botVerifierForbiddenErr()
}

func (r *Router) onBotsGetBotRecommendations(ctx context.Context, _ tg.InputUserClass) (tg.UsersUsersClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return &tg.UsersUsers{Users: []tg.UserClass{}}, nil
}

func (r *Router) onBotsRequestWebViewButton(ctx context.Context, req *tg.BotsRequestWebViewButtonRequest) (*tg.BotsRequestedButton, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.UserID == nil || req.Button == nil {
		return nil, buttonDataInvalidErr()
	}
	if r.deps.Bots == nil {
		return nil, buttonDataInvalidErr()
	}
	if _, found, err := r.userFromInput(ctx, userID, req.UserID); err != nil || !found {
		return nil, userIDInvalidErr()
	}
	button, err := domainRequestedButtonFromTG(botID, req.UserID, req.Button)
	if err != nil {
		return nil, err
	}
	target, found, err := r.userFromInput(ctx, userID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, userIDInvalidErr()
	}
	button.UserID = target.ID
	saved, err := r.deps.Bots.SaveRequestedWebViewButton(ctx, button)
	if err != nil {
		return nil, buttonDataInvalidErr()
	}
	return &tg.BotsRequestedButton{WebappReqID: saved.WebAppReqID}, nil
}

func (r *Router) onBotsGetRequestedWebViewButton(ctx context.Context, req *tg.BotsGetRequestedWebViewButtonRequest) (tg.KeyboardButtonClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	bot, err := r.resolveBotUserForViewer(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	button, found, err := r.deps.Bots.GetRequestedWebViewButton(ctx, bot.ID, userID, req.WebappReqID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, buttonDataInvalidErr()
	}
	return tgKeyboardButtonRequestPeer(button), nil
}

func botEmojiStatusFromTG(status tg.EmojiStatusClass) (documentID int64, until int, err error) {
	switch s := status.(type) {
	case *tg.EmojiStatusEmpty:
		return 0, 0, nil
	case *tg.EmojiStatus:
		if s.DocumentID < 0 {
			return 0, 0, tgerr400("EMOJI_STATUS_INVALID")
		}
		if v, ok := s.GetUntil(); ok {
			until = v
		}
		return s.DocumentID, until, nil
	case *tg.EmojiStatusCollectible:
		if s.DocumentID < 0 {
			return 0, 0, tgerr400("EMOJI_STATUS_INVALID")
		}
		if v, ok := s.GetUntil(); ok {
			until = v
		}
		return s.DocumentID, until, nil
	default:
		return 0, 0, tgerr400("EMOJI_STATUS_INVALID")
	}
}

func (r *Router) mainBotAppForOwner(ctx context.Context, botUserID int64) (domain.BotApp, error) {
	app, found, err := r.deps.Bots.GetMainBotApp(ctx, botUserID)
	if err != nil {
		return domain.BotApp{}, internalErr()
	}
	if !found {
		return domain.BotApp{}, botAppInvalidErr()
	}
	return app, nil
}

func (r *Router) botPreviewMediaFromInput(ctx context.Context, userID int64, input tg.InputMediaClass) (domain.BotAppPreviewMedia, error) {
	media, err := r.resolveInputMedia(ctx, userID, input)
	if err != nil {
		return domain.BotAppPreviewMedia{}, err
	}
	if media == nil {
		return domain.BotAppPreviewMedia{}, mediaInvalidErr()
	}
	switch media.Kind {
	case domain.MessageMediaKindPhoto:
		if media.Photo == nil || media.Photo.ID == 0 {
			return domain.BotAppPreviewMedia{}, mediaInvalidErr()
		}
		return domain.BotAppPreviewMedia{PhotoID: media.Photo.ID}, nil
	case domain.MessageMediaKindDocument:
		if media.Document == nil || media.Document.ID == 0 {
			return domain.BotAppPreviewMedia{}, mediaInvalidErr()
		}
		return domain.BotAppPreviewMedia{DocumentID: media.Document.ID}, nil
	default:
		return domain.BotAppPreviewMedia{}, mediaTypeInvalidErr()
	}
}

func (r *Router) botPreviewMediaIDFromInput(ctx context.Context, userID, botUserID, appID int64, input tg.InputMediaClass) (domain.BotAppPreviewMedia, error) {
	target, err := r.botPreviewMediaFromInput(ctx, userID, input)
	if err != nil {
		return domain.BotAppPreviewMedia{}, err
	}
	items, err := r.deps.Bots.ListBotAppPreviewMedia(ctx, botUserID, appID)
	if err != nil {
		return domain.BotAppPreviewMedia{}, internalErr()
	}
	for _, item := range items {
		if target.PhotoID != 0 && item.PhotoID == target.PhotoID {
			return item, nil
		}
		if target.DocumentID != 0 && item.DocumentID == target.DocumentID {
			return item, nil
		}
	}
	return domain.BotAppPreviewMedia{}, mediaInvalidErr()
}

func (r *Router) botPreviewMedias(ctx context.Context, botUserID, appID int64) ([]tg.BotPreviewMedia, error) {
	items, err := r.deps.Bots.ListBotAppPreviewMedia(ctx, botUserID, appID)
	if err != nil {
		return nil, internalErr()
	}
	out := make([]tg.BotPreviewMedia, 0, len(items))
	for _, item := range items {
		converted := r.tgBotPreviewMedia(ctx, item)
		if converted != nil {
			out = append(out, *converted)
		}
	}
	return out, nil
}

func (r *Router) tgBotPreviewMedia(ctx context.Context, item domain.BotAppPreviewMedia) *tg.BotPreviewMedia {
	out := &tg.BotPreviewMedia{Date: int(r.clock.Now().Unix())}
	switch {
	case item.PhotoID != 0:
		var photo tg.PhotoClass = &tg.PhotoEmpty{ID: item.PhotoID}
		if r.deps.Files != nil {
			if p, found, err := r.deps.Files.GetPhoto(ctx, item.PhotoID); err == nil && found {
				photo = tgPhoto(p)
			}
		}
		out.Media = &tg.MessageMediaPhoto{Photo: photo}
	case item.DocumentID != 0:
		var doc tg.DocumentClass = &tg.DocumentEmpty{ID: item.DocumentID}
		if r.deps.Files != nil {
			if d, found, err := r.deps.Files.GetDocument(ctx, item.DocumentID); err == nil && found {
				doc = tgDocument(d)
			}
		}
		out.Media = &tg.MessageMediaDocument{Document: doc}
	default:
		out.Media = &tg.MessageMediaEmpty{}
	}
	return out
}

func domainRequestedButtonFromTG(botUserID int64, _ tg.InputUserClass, button tg.KeyboardButtonClass) (domain.BotRequestedWebViewButton, error) {
	var out domain.BotRequestedWebViewButton
	out.BotUserID = botUserID
	switch b := button.(type) {
	case *tg.InputKeyboardButtonRequestPeer:
		out.ButtonID = b.ButtonID
		out.Text = strings.TrimSpace(b.Text)
		out.PeerType = requestPeerTypeName(b.PeerType)
		out.MaxQuantity = b.MaxQuantity
	case *tg.KeyboardButtonRequestPeer:
		out.ButtonID = b.ButtonID
		out.Text = strings.TrimSpace(b.Text)
		out.PeerType = requestPeerTypeName(b.PeerType)
		out.MaxQuantity = b.MaxQuantity
	default:
		return domain.BotRequestedWebViewButton{}, buttonDataInvalidErr()
	}
	if out.ButtonID == 0 || out.Text == "" || out.PeerType == "" {
		return domain.BotRequestedWebViewButton{}, buttonDataInvalidErr()
	}
	return out, nil
}

func requestPeerTypeName(peerType tg.RequestPeerTypeClass) string {
	switch peerType.(type) {
	case *tg.RequestPeerTypeUser:
		return "user"
	case *tg.RequestPeerTypeChat:
		return "chat"
	case *tg.RequestPeerTypeBroadcast:
		return "broadcast"
	default:
		return ""
	}
}

func tgKeyboardButtonRequestPeer(button domain.BotRequestedWebViewButton) tg.KeyboardButtonClass {
	return &tg.KeyboardButtonRequestPeer{
		Text:        button.Text,
		ButtonID:    button.ButtonID,
		PeerType:    tgRequestPeerType(button.PeerType),
		MaxQuantity: button.MaxQuantity,
	}
}

func tgRequestPeerType(kind string) tg.RequestPeerTypeClass {
	switch kind {
	case "chat":
		return &tg.RequestPeerTypeChat{}
	case "broadcast":
		return &tg.RequestPeerTypeBroadcast{}
	default:
		return &tg.RequestPeerTypeUser{}
	}
}

const maxBotDownloadBytes = 25 << 20

func checkBotDownloadFileParams(ctx context.Context, fileName, rawURL string) bool {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" || len(fileName) > 128 || filepath.Base(fileName) != fileName {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || len(rawURL) > domain.MaxBotAppURLLen {
		return false
	}
	if !downloadURLHostAllowed(ctx, parsed) || !downloadExtensionAllowed(fileName, parsed.Path) {
		return false
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, parsed.String(), nil)
	if err != nil {
		return false
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL == nil || req.URL.Scheme != "https" || !downloadURLHostAllowed(req.Context(), req.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return false
	}
	if resp.ContentLength > maxBotDownloadBytes {
		return false
	}
	if ct := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])); ct != "" && !downloadMIMEAllowed(ct) {
		return false
	}
	return true
}

func downloadURLHostAllowed(ctx context.Context, u *url.URL) bool {
	host := u.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return publicDownloadIP(ip)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, addr := range addrs {
		if !publicDownloadIP(addr.IP) {
			return false
		}
	}
	return true
}

func publicDownloadIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast())
}

func downloadExtensionAllowed(fileName, urlPath string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(urlPath))
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".mp4", ".mov", ".pdf", ".txt", ".json", ".zip":
		return true
	default:
		return false
	}
}

func downloadMIMEAllowed(mime string) bool {
	if strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/") {
		return true
	}
	switch mime {
	case "application/pdf", "application/json", "application/zip", "application/octet-stream", "text/plain":
		return true
	default:
		return false
	}
}

func (r *Router) onBotsGetAccessSettings(ctx context.Context, bot tg.InputUserClass) (*tg.BotsAccessSettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if _, err := r.resolveBotUserForViewer(ctx, userID, bot); err != nil {
		return nil, err
	}
	return &tg.BotsAccessSettings{}, nil
}

func (r *Router) onBotsEditAccessSettings(ctx context.Context, req *tg.BotsEditAccessSettingsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if _, err := r.resolveOwnedBotUser(ctx, userID, req.Bot); err != nil {
		return false, err
	}
	return false, botInvalidErr()
}
