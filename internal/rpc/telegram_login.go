package rpc

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func telegramLoginOAuthInvalidErr() error { return tgerr.New(500, "OAUTH_REQUEST_INVALID") }
func telegramLoginURLExpiredErr() error   { return tgerr.New(400, "URL_EXPIRED") }
func telegramLoginURLInvalidErr() error   { return tgerr.New(400, "URL_INVALID") }
func telegramLoginHashInvalidErr() error  { return tgerr.New(400, "HASH_INVALID") }

func telegramLoginRPCError(err error) error {
	switch {
	case errors.Is(err, domain.ErrTelegramLoginRequestExpired):
		return telegramLoginURLExpiredErr()
	case errors.Is(err, domain.ErrTelegramLoginURLInvalid):
		return telegramLoginURLInvalidErr()
	case errors.Is(err, domain.ErrTelegramLoginMatchCodeInvalid),
		errors.Is(err, domain.ErrTelegramLoginRequestInvalid),
		errors.Is(err, domain.ErrTelegramLoginRequestConflict),
		errors.Is(err, domain.ErrTelegramLoginClientDisabled),
		errors.Is(err, domain.ErrTelegramLoginOriginNotAllowed),
		errors.Is(err, domain.ErrTelegramLoginRedirectNotAllowed),
		errors.Is(err, domain.ErrTelegramLoginScopeInvalid),
		errors.Is(err, domain.ErrTelegramLoginAuthorizationsTooMany):
		return telegramLoginOAuthInvalidErr()
	default:
		return internalErr()
	}
}

func (r *Router) requireTelegramLoginUser(ctx context.Context) (int64, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID <= 0 || r.deps.TelegramLogin == nil || r.deps.Users == nil {
		return 0, internalErr()
	}
	self, err := r.deps.Users.Self(ctx, userID)
	if err != nil || self.Bot || self.Deleted {
		return 0, telegramLoginOAuthInvalidErr()
	}
	return userID, nil
}

func (r *Router) telegramLoginRequestResult(ctx context.Context, viewerUserID int64, request domain.TelegramLoginRequest, deepLink string) (tg.URLAuthResultClass, error) {
	switch request.Status {
	case domain.TelegramLoginRequestApproved:
		if request.AuthorizedUserID != viewerUserID {
			return nil, telegramLoginOAuthInvalidErr()
		}
		return r.telegramLoginAcceptedResult(ctx, request, deepLink)
	case domain.TelegramLoginRequestPending:
		// Continue below.
	case domain.TelegramLoginRequestDeclined, domain.TelegramLoginRequestExpired:
		return nil, telegramLoginURLExpiredErr()
	default:
		return nil, telegramLoginOAuthInvalidErr()
	}
	bot, found, err := r.deps.Users.ByID(ctx, viewerUserID, request.BotUserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || !bot.Bot || bot.Deleted {
		return nil, telegramLoginOAuthInvalidErr()
	}
	botTL := r.withBotProfileFlags(ctx, r.tgUser(bot))
	out := &tg.URLAuthResultRequest{
		RequestWriteAccess: request.Requests(domain.TelegramLoginScopeBotAccess),
		RequestPhoneNumber: request.Requests(domain.TelegramLoginScopePhone),
		MatchCodesFirst:    request.MatchCodesFirst,
		IsApp:              request.IsApp,
		Bot:                botTL,
		Domain:             request.Domain,
	}
	// OAuth requests carry the complete device tuple. Keep the four fields on
	// their shared flag together so old exact-layer codecs never see a partial
	// conditional shape.
	if request.Browser != "" && request.Platform != "" && request.IP != "" && request.Region != "" {
		out.SetBrowser(request.Browser)
		out.SetPlatform(request.Platform)
		out.SetIP(request.IP)
		out.SetRegion(request.Region)
	}
	if len(request.MatchCodes) > 0 {
		out.SetMatchCodes(append([]string(nil), request.MatchCodes...))
	}
	if request.UserIDHint > 0 {
		out.SetUserIDHint(request.UserIDHint)
	}
	if request.IsApp && request.VerifiedAppName != "" {
		out.SetVerifiedAppName(request.VerifiedAppName)
	}
	return out, nil
}

func (r *Router) telegramLoginAcceptedResult(ctx context.Context, request domain.TelegramLoginRequest, deepLink string) (tg.URLAuthResultClass, error) {
	accepted := &tg.URLAuthResultAccepted{}
	switch {
	case request.Source == domain.TelegramLoginRequestNative && request.IsApp:
		redirectURL, err := r.deps.TelegramLogin.FinalizeRedirectByDeepLink(ctx, deepLink)
		if err != nil {
			return nil, telegramLoginRPCError(err)
		}
		accepted.SetURL(redirectURL)
	case request.Source == domain.TelegramLoginRequestMiniApp:
		resultURL, err := r.deps.TelegramLogin.FinalizeInAppRedirectByDeepLink(ctx, deepLink)
		if err != nil {
			return nil, telegramLoginRPCError(err)
		}
		accepted.SetURL(resultURL)
	}
	return accepted, nil
}

func (r *Router) onMessagesRequestURLAuth(ctx context.Context, req *tg.MessagesRequestURLAuthRequest) (tg.URLAuthResultClass, error) {
	userID, err := r.requireTelegramLoginUser(ctx)
	if err != nil {
		return nil, err
	}
	_, hasPeer := req.GetPeer()
	urlValue, hasURL := req.GetURL()
	_, hasOrigin := req.GetInAppOrigin()
	if hasPeer == hasURL || (!hasPeer && strings.TrimSpace(urlValue) == "") || hasOrigin && !hasURL {
		return nil, telegramLoginOAuthInvalidErr()
	}
	if hasPeer {
		button, peer, err := r.telegramLoginButtonFromMessage(ctx, userID, req.Peer, req.MsgID, req.ButtonID)
		if err != nil {
			return nil, err
		}
		u, err := url.Parse(button.URL)
		if err != nil || u.Hostname() == "" {
			return nil, telegramLoginURLInvalidErr()
		}
		request := domain.TelegramLoginRequest{
			BotUserID: button.LoginBotUserID, Source: domain.TelegramLoginRequestMessageButton,
			ResponseType: "legacy_url", RedirectURI: button.URL, Domain: u.Hostname(),
			Scopes:   []domain.TelegramLoginScope{domain.TelegramLoginScopeOpenID, domain.TelegramLoginScopeProfile},
			PeerType: peer.Type, PeerID: peer.ID, MessageID: req.MsgID, ButtonID: req.ButtonID,
			Status: domain.TelegramLoginRequestPending,
		}
		if button.RequestWriteAccess {
			request.Scopes = append(request.Scopes, domain.TelegramLoginScopeBotAccess)
		}
		return r.telegramLoginRequestResult(ctx, userID, request, "")
	}
	if hasOrigin && req.InAppOrigin == "" {
		return nil, telegramLoginURLInvalidErr()
	}
	request, err := r.deps.TelegramLogin.RequestByDeepLinkForOrigin(ctx, urlValue, req.InAppOrigin)
	if err != nil {
		return nil, telegramLoginRPCError(err)
	}
	return r.telegramLoginRequestResult(ctx, userID, request, urlValue)
}

func (r *Router) onMessagesAcceptURLAuth(ctx context.Context, req *tg.MessagesAcceptURLAuthRequest) (tg.URLAuthResultClass, error) {
	userID, err := r.requireTelegramLoginUser(ctx)
	if err != nil {
		return nil, err
	}
	_, hasPeer := req.GetPeer()
	deepLink, hasURL := req.GetURL()
	matchCode, hasMatchCode := req.GetMatchCode()
	if hasPeer == hasURL || (!hasPeer && strings.TrimSpace(deepLink) == "") || (hasMatchCode && matchCode == "") {
		return nil, telegramLoginOAuthInvalidErr()
	}
	if hasPeer {
		if hasMatchCode || req.SharePhoneNumber {
			return nil, telegramLoginOAuthInvalidErr()
		}
		button, peer, err := r.telegramLoginButtonFromMessage(ctx, userID, req.Peer, req.MsgID, req.ButtonID)
		if err != nil {
			return nil, err
		}
		if r.deps.Bots == nil {
			return nil, internalErr()
		}
		profile, found, err := r.deps.Bots.BotInfo(ctx, button.LoginBotUserID)
		if err != nil {
			return nil, internalErr()
		}
		if !found || profile.TokenSecret == "" {
			return nil, telegramLoginOAuthInvalidErr()
		}
		self, err := r.deps.Users.Self(ctx, userID)
		if err != nil {
			return nil, internalErr()
		}
		identity := r.telegramLoginIdentity(self)
		result, err := r.deps.TelegramLogin.AuthorizeMessageButton(ctx, domain.TelegramLoginMessageButtonAuthorization{
			UserID: userID, BotUserID: button.LoginBotUserID,
			BotToken: domain.FormatBotToken(button.LoginBotUserID, profile.TokenSecret), URL: button.URL,
			RequestWriteAccess: button.RequestWriteAccess, WriteAllowed: req.WriteAllowed,
			Peer: peer, MessageID: req.MsgID, ButtonID: req.ButtonID,
			Browser: "Telegram", Platform: "Telegram Client", IP: "Unknown IP", Region: "Unknown region",
			Identity: identity,
		})
		if err != nil {
			return nil, telegramLoginRPCError(err)
		}
		accepted := &tg.URLAuthResultAccepted{}
		accepted.SetURL(result.URL)
		return accepted, nil
	}
	request, err := r.deps.TelegramLogin.RequestByDeepLink(ctx, deepLink)
	if err != nil {
		return nil, telegramLoginRPCError(err)
	}
	if request.Status == domain.TelegramLoginRequestApproved {
		if request.AuthorizedUserID == userID {
			return r.telegramLoginAcceptedResult(ctx, request, deepLink)
		}
		return nil, telegramLoginOAuthInvalidErr()
	}
	if request.Status != domain.TelegramLoginRequestPending {
		return nil, telegramLoginURLExpiredErr()
	}
	self, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	identity := r.telegramLoginIdentity(self)
	approved, _, err := r.deps.TelegramLogin.Approve(ctx, deepLink, identity, req.WriteAllowed, req.SharePhoneNumber, matchCode)
	if err != nil {
		return nil, telegramLoginRPCError(err)
	}
	if approved.AuthorizedUserID != userID {
		return nil, telegramLoginOAuthInvalidErr()
	}
	return r.telegramLoginAcceptedResult(ctx, approved, deepLink)
}

func (r *Router) telegramLoginIdentity(self domain.User) domain.TelegramLoginIdentitySnapshot {
	identity := domain.TelegramLoginIdentitySnapshot{
		UserID: self.ID, Name: strings.TrimSpace(strings.TrimSpace(self.FirstName) + " " + strings.TrimSpace(self.LastName)),
		GivenName: self.FirstName, FamilyName: self.LastName,
		PreferredUsername: self.Username, PhoneNumber: self.Phone,
	}
	if strings.TrimSpace(r.cfg.PublicBaseURL) != "" && self.Username != "" && self.PhotoID > 0 {
		identity.Picture = strings.TrimSuffix(r.cfg.PublicBaseURL, "/") + "/_public/avatar/" + url.PathEscape(self.Username) + "/" + strconv.FormatInt(self.PhotoID, 10)
	}
	return identity
}

func (r *Router) telegramLoginButtonFromMessage(ctx context.Context, userID int64, inputPeer tg.InputPeerClass, messageID, buttonID int) (domain.MarkupButton, domain.Peer, error) {
	if messageID <= 0 || messageID > domain.MaxMessageBoxID || buttonID < 0 {
		return domain.MarkupButton{}, domain.Peer{}, telegramLoginOAuthInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, inputPeer)
	if err != nil {
		return domain.MarkupButton{}, domain.Peer{}, err
	}
	var markup *domain.MessageReplyMarkup
	switch peer.Type {
	case domain.PeerTypeUser:
		message, found, err := r.lookupOwnerMessage(ctx, userID, messageID)
		if err != nil {
			return domain.MarkupButton{}, domain.Peer{}, internalErr()
		}
		if !found || message.Peer != peer {
			return domain.MarkupButton{}, domain.Peer{}, telegramLoginOAuthInvalidErr()
		}
		markup = message.ReplyMarkup
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return domain.MarkupButton{}, domain.Peer{}, telegramLoginOAuthInvalidErr()
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{messageID})
		if err != nil || len(history.Messages) != 1 || history.Messages[0].ID != messageID {
			return domain.MarkupButton{}, domain.Peer{}, telegramLoginOAuthInvalidErr()
		}
		markup = history.Messages[0].ReplyMarkup
	default:
		return domain.MarkupButton{}, domain.Peer{}, telegramLoginOAuthInvalidErr()
	}
	if markup == nil || markup.Kind() != domain.MessageReplyMarkupInline {
		return domain.MarkupButton{}, domain.Peer{}, telegramLoginOAuthInvalidErr()
	}
	for _, row := range markup.Inline {
		for _, button := range row {
			if button.Type == domain.MarkupButtonLoginURL && button.ButtonID == buttonID && button.LoginBotUserID > 0 {
				return button, peer, nil
			}
		}
	}
	return domain.MarkupButton{}, domain.Peer{}, telegramLoginOAuthInvalidErr()
}

func (r *Router) onMessagesDeclineURLAuth(ctx context.Context, deepLink string) (bool, error) {
	userID, err := r.requireTelegramLoginUser(ctx)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(deepLink) == "" {
		return false, telegramLoginURLInvalidErr()
	}
	request, err := r.deps.TelegramLogin.RequestByDeepLink(ctx, deepLink)
	if err != nil {
		return false, telegramLoginRPCError(err)
	}
	if request.Status == domain.TelegramLoginRequestDeclined {
		return true, nil
	}
	if request.Status != domain.TelegramLoginRequestPending {
		return false, telegramLoginOAuthInvalidErr()
	}
	if _, err := r.deps.TelegramLogin.Decline(ctx, deepLink, userID); err != nil {
		return false, telegramLoginRPCError(err)
	}
	return true, nil
}

func (r *Router) onMessagesCheckURLAuthMatchCode(ctx context.Context, deepLink, matchCode string) (bool, error) {
	if _, err := r.requireTelegramLoginUser(ctx); err != nil {
		return false, err
	}
	if strings.TrimSpace(deepLink) == "" || matchCode == "" {
		return false, telegramLoginURLInvalidErr()
	}
	ok, err := r.deps.TelegramLogin.CheckMatchCode(ctx, deepLink, matchCode)
	if err != nil {
		return false, telegramLoginRPCError(err)
	}
	return ok, nil
}

func (r *Router) onAccountGetWebAuthorizations(ctx context.Context) (*tg.AccountWebAuthorizations, error) {
	if r.deps.TelegramLogin == nil {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return &tg.AccountWebAuthorizations{Authorizations: []tg.WebAuthorization{}, Users: []tg.UserClass{}}, nil
	}
	userID, err := r.requireTelegramLoginUser(ctx)
	if err != nil {
		return nil, err
	}
	authorizations, err := r.deps.TelegramLogin.ListWebAuthorizations(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	result := &tg.AccountWebAuthorizations{
		Authorizations: make([]tg.WebAuthorization, 0, len(authorizations)),
		Users:          []tg.UserClass{},
	}
	botIDs := make([]int64, 0, len(authorizations))
	seenBots := make(map[int64]struct{}, len(authorizations))
	for _, authorization := range authorizations {
		result.Authorizations = append(result.Authorizations, tg.WebAuthorization{
			Hash: authorization.Hash, BotID: authorization.BotUserID, Domain: authorization.Domain,
			Browser: authorization.Browser, Platform: authorization.Platform,
			DateCreated: telegramLoginUnixInt(authorization.CreatedAt), DateActive: telegramLoginUnixInt(authorization.LastActiveAt),
			IP: authorization.IP, Region: authorization.Region,
		})
		if _, duplicate := seenBots[authorization.BotUserID]; !duplicate {
			seenBots[authorization.BotUserID] = struct{}{}
			botIDs = append(botIDs, authorization.BotUserID)
		}
	}
	if len(botIDs) > 0 {
		bots, err := r.deps.Users.ByIDs(ctx, userID, botIDs)
		if err != nil {
			return nil, internalErr()
		}
		for _, bot := range bots {
			if bot.Bot && !bot.Deleted {
				result.Users = append(result.Users, r.withBotProfileFlags(ctx, r.tgUser(bot)))
			}
		}
	}
	return result, nil
}

func (r *Router) onAccountResetWebAuthorization(ctx context.Context, hash int64) (bool, error) {
	if r.deps.TelegramLogin == nil {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return false, internalErr()
		}
		return true, nil
	}
	userID, err := r.requireTelegramLoginUser(ctx)
	if err != nil {
		return false, err
	}
	if hash == 0 {
		return false, telegramLoginHashInvalidErr()
	}
	if err := r.deps.TelegramLogin.RevokeWebAuthorization(ctx, userID, hash); err != nil {
		if errors.Is(err, domain.ErrTelegramLoginWebAuthHashInvalid) {
			return false, telegramLoginHashInvalidErr()
		}
		return false, internalErr()
	}
	return true, nil
}

func (r *Router) onAccountResetWebAuthorizations(ctx context.Context) (bool, error) {
	if r.deps.TelegramLogin == nil {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return false, internalErr()
		}
		return true, nil
	}
	userID, err := r.requireTelegramLoginUser(ctx)
	if err != nil {
		return false, err
	}
	if _, err := r.deps.TelegramLogin.RevokeAllWebAuthorizations(ctx, userID); err != nil {
		return false, internalErr()
	}
	return true, nil
}

func telegramLoginUnixInt(value time.Time) int {
	unix := value.Unix()
	if unix < 0 {
		return 0
	}
	if unix > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(unix)
}
