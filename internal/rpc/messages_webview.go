package rpc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func (r *Router) onMessagesRequestWebView(ctx context.Context, req *tg.MessagesRequestWebViewRequest) (*tg.WebViewResultURL, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 || r.deps.Users == nil || r.deps.Bots == nil {
		return nil, botInvalidErr()
	}
	user, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	bot, profile, err := r.webViewBotFromInput(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	rawURL, err := webViewRequestURL(req.GetURL, req.FromBotMenu, profile)
	if err != nil {
		return nil, err
	}
	startParam, err := webViewStartParam(req.GetStartParam)
	if err != nil {
		return nil, err
	}
	if err := validateWebViewPlatform(req.Platform); err != nil {
		return nil, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	replyTo, err := r.messageReplyFromInput(ctx, userID, peer, req.ReplyTo)
	if err != nil {
		return nil, err
	}
	sendAs, err := r.resolveSendAsPeer(ctx, userID, peer, req.SendAs)
	if err != nil {
		return nil, err
	}
	now := r.clock.Now()
	session := r.webviews.registerContext(ctx, now, store.WebViewSession{
		BotUserID:  bot.ID,
		UserID:     userID,
		Peer:       peer,
		Source:     "webview",
		StartParam: startParam,
		Silent:     req.Silent,
		ReplyTo:    replyTo,
		SendAs:     sendAs,
	})
	signedURL, err := webViewURLWithInitData(rawURL, session.BotQueryID, profile, user, startParam, req.Platform, now)
	if err != nil {
		return nil, internalErr()
	}
	out := &tg.WebViewResultURL{URL: signedURL}
	out.SetQueryID(session.QueryID)
	if req.Fullscreen {
		out.SetFullscreen(true)
	}
	return out, nil
}

func (r *Router) onMessagesRequestSimpleWebView(ctx context.Context, req *tg.MessagesRequestSimpleWebViewRequest) (*tg.WebViewResultURL, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 || r.deps.Users == nil {
		return nil, botInvalidErr()
	}
	bot, found, err := r.userFromInput(ctx, userID, req.Bot)
	if err != nil {
		return nil, internalErr()
	}
	if !found || !bot.Bot {
		return nil, botInvalidErr()
	}
	urlValue, ok := req.GetURL()
	if !ok || validateInlineWebURL(urlValue, true) != nil {
		return nil, urlInvalidErr()
	}
	if startParam, ok := req.GetStartParam(); ok && !validInlineStartParam(startParam) {
		return nil, startParamInvalidErr()
	}
	if req.Platform == "" || utf8.RuneCountInString(req.Platform) > domain.MaxBotInlineSwitchTextLen || strings.TrimSpace(req.Platform) != req.Platform {
		return nil, platformInvalidErr()
	}
	out := &tg.WebViewResultURL{URL: urlValue}
	if req.Fullscreen {
		out.SetFullscreen(true)
	}
	return out, nil
}

func (r *Router) onMessagesGetBotApp(ctx context.Context, req *tg.MessagesGetBotAppRequest) (*tg.MessagesBotApp, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 || r.deps.Users == nil || r.deps.Bots == nil {
		return nil, botAppInvalidErr()
	}
	_, _, appDomain, app, _, err := r.webViewBotAppFromInput(ctx, userID, req.App)
	if err != nil {
		return nil, err
	}
	out := &tg.MessagesBotApp{}
	if req.Hash != 0 && req.Hash == appDomain.Hash {
		out.App = &tg.BotAppNotModified{}
		return out, nil
	}
	out.App = app
	return out, nil
}

func (r *Router) onMessagesRequestAppWebView(ctx context.Context, req *tg.MessagesRequestAppWebViewRequest) (*tg.WebViewResultURL, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 || r.deps.Users == nil || r.deps.Bots == nil {
		return nil, botAppInvalidErr()
	}
	user, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	bot, profile, app, _, rawURL, err := r.webViewBotAppFromInput(ctx, userID, req.App)
	if err != nil {
		return nil, err
	}
	startParam, err := webViewStartParam(req.GetStartParam)
	if err != nil {
		return nil, err
	}
	if err := validateWebViewPlatform(req.Platform); err != nil {
		return nil, err
	}
	peer, err := r.webViewPeerFromInput(ctx, userID, req.Peer, bot.ID)
	if err != nil {
		return nil, err
	}
	if req.WriteAllowed {
		created, err := r.deps.Bots.AllowSendMessage(ctx, userID, bot.ID, true)
		if err != nil {
			return nil, botInvalidErr()
		}
		if created {
			res, err := r.sendBotAllowedServiceMessageWith(ctx, userID, bot.ID, domain.MessageBotAllowedAction{FromRequest: true})
			if err != nil {
				return nil, internalErr()
			}
			if !res.Duplicate {
				senderUsers := r.usersForMessageUpdate(ctx, userID, res.SenderMessage)
				senderChats := r.chatsForMessageUpdate(ctx, userID, res.SenderMessage)
				r.pushUserMessage(ctx, userID, "push webview write allowed", tgPrivateMessageUpdates(res.SenderEvent, res.SenderMessage, 0, false, senderUsers, senderChats))
			}
		}
	}
	now := r.clock.Now()
	session := r.webviews.registerContext(ctx, now, store.WebViewSession{
		BotUserID:    bot.ID,
		UserID:       userID,
		Peer:         peer,
		AppID:        app.ID,
		Source:       "app",
		StartParam:   startParam,
		WriteAllowed: req.WriteAllowed,
	})
	signedURL, err := webViewURLWithInitData(rawURL, session.BotQueryID, profile, user, startParam, req.Platform, now)
	if err != nil {
		return nil, internalErr()
	}
	out := &tg.WebViewResultURL{URL: signedURL}
	out.SetQueryID(session.QueryID)
	if req.Fullscreen {
		out.SetFullscreen(true)
	}
	return out, nil
}

func (r *Router) onMessagesRequestMainWebView(ctx context.Context, req *tg.MessagesRequestMainWebViewRequest) (*tg.WebViewResultURL, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 || r.deps.Users == nil || r.deps.Bots == nil {
		return nil, botInvalidErr()
	}
	user, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	bot, profile, err := r.webViewBotFromInput(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	var app domain.BotApp
	if mainApp, found, err := r.deps.Bots.GetMainBotApp(ctx, bot.ID); err != nil {
		return nil, internalErr()
	} else if found {
		app = mainApp
	} else {
		rawURL, err := webViewMenuAppURL(profile, botWebviewDisabledErr())
		if err != nil {
			return nil, err
		}
		app = domain.BotApp{
			ID:          bot.ID,
			BotUserID:   bot.ID,
			ShortName:   "main",
			Title:       profile.MenuButton.Text,
			Description: profile.Description,
			URL:         rawURL,
			AccessHash:  menuBackedBotAppAccessHash(profile),
			Hash:        menuBackedBotAppHash(bot, profile, "main"),
			Main:        true,
		}
	}
	rawURL := app.URL
	startParam, err := webViewStartParam(req.GetStartParam)
	if err != nil {
		return nil, err
	}
	if err := validateWebViewPlatform(req.Platform); err != nil {
		return nil, err
	}
	peer, err := r.webViewPeerFromInput(ctx, userID, req.Peer, bot.ID)
	if err != nil {
		return nil, err
	}
	now := r.clock.Now()
	session := r.webviews.registerContext(ctx, now, store.WebViewSession{
		BotUserID:  bot.ID,
		UserID:     userID,
		Peer:       peer,
		AppID:      app.ID,
		Source:     "main",
		StartParam: startParam,
	})
	signedURL, err := webViewURLWithInitData(rawURL, session.BotQueryID, profile, user, startParam, req.Platform, now)
	if err != nil {
		return nil, internalErr()
	}
	out := &tg.WebViewResultURL{URL: signedURL}
	out.SetQueryID(session.QueryID)
	if req.Fullscreen {
		out.SetFullscreen(true)
	}
	return out, nil
}

func (r *Router) onMessagesProlongWebView(ctx context.Context, req *tg.MessagesProlongWebViewRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if userID == 0 || r.deps.Users == nil || r.deps.Bots == nil || req.QueryID == 0 {
		return false, queryIDInvalidErr()
	}
	bot, _, err := r.webViewBotFromInput(ctx, userID, req.Bot)
	if err != nil {
		return false, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	replyTo, err := r.messageReplyFromInput(ctx, userID, peer, req.ReplyTo)
	if err != nil {
		return false, err
	}
	sendAs, err := r.resolveSendAsPeer(ctx, userID, peer, req.SendAs)
	if err != nil {
		return false, err
	}
	if !r.webviews.prolongContext(ctx, r.clock.Now(), req.QueryID, userID, bot.ID, peer, req.Silent, replyTo, sendAs) {
		return false, queryIDInvalidErr()
	}
	return true, nil
}

func (r *Router) onMessagesSendWebViewResultMessage(ctx context.Context, req *tg.MessagesSendWebViewResultMessageRequest) (*tg.WebViewMessageSent, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	if req.BotQueryID == "" {
		return nil, queryIDInvalidErr()
	}
	result, err := r.domainInlineResultFromTG(ctx, botID, req.Result)
	if err != nil {
		return nil, err
	}
	if err := r.sendWebViewDomainResultMessage(ctx, botID, req.BotQueryID, result); err != nil {
		return nil, err
	}
	return &tg.WebViewMessageSent{}, nil
}

// AnswerWebAppQueryFromBotAPI answers a Bot API answerWebAppQuery request using
// the same webview session registry and message pipeline as MTProto
// messages.sendWebViewResultMessage. It intentionally accepts only domain
// values, keeping Bot API parsing out of the MTProto/tg boundary.
func (r *Router) AnswerWebAppQueryFromBotAPI(ctx context.Context, botID int64, botQueryID string, result domain.BotInlineResult) (string, error) {
	if botID == 0 || strings.TrimSpace(botQueryID) == "" {
		return "", queryIDInvalidErr()
	}
	if r.deps.Bots == nil {
		return "", userBotRequiredErr()
	}
	if _, found, err := r.deps.Bots.BotInfo(ctx, botID); err != nil {
		return "", internalErr()
	} else if !found {
		return "", userBotRequiredErr()
	}
	if err := r.sendWebViewDomainResultMessage(ctx, botID, botQueryID, result); err != nil {
		return "", err
	}
	return "", nil
}

// SavePreparedInlineMessageFromBotAPI stores a Bot API prepared inline message
// in the same short-lived local/shared inline registry used by
// messages.savePreparedInlineMessage.
func (r *Router) SavePreparedInlineMessageFromBotAPI(ctx context.Context, botID, userID int64, result domain.BotInlineResult, peerTypes []string) (string, int, error) {
	if botID == 0 {
		return "", 0, userBotRequiredErr()
	}
	if userID == 0 || r.deps.Users == nil || r.deps.Bots == nil {
		return "", 0, userIDInvalidErr()
	}
	if _, found, err := r.deps.Bots.BotInfo(ctx, botID); err != nil {
		return "", 0, internalErr()
	} else if !found {
		return "", 0, userBotRequiredErr()
	}
	if _, found, err := r.deps.Users.ByID(ctx, botID, userID); err != nil {
		return "", 0, internalErr()
	} else if !found {
		return "", 0, userIDInvalidErr()
	}
	id, expireDate := r.inlines.savePreparedInlineContext(ctx, r.clock.Now(), botID, userID, result, peerTypes)
	return id, expireDate, nil
}

func (r *Router) sendWebViewDomainResultMessage(ctx context.Context, botID int64, botQueryID string, result domain.BotInlineResult) error {
	now := r.clock.Now()
	session, ok := r.webviews.sessionForBotQueryContext(ctx, now, botID, botQueryID)
	if !ok {
		return queryIDInvalidErr()
	}
	if err := r.checkSendRateLimit(ctx, session.UserID, 1); err != nil {
		return err
	}
	media := cloneInlineMedia(result.Media)
	if result.MediaAuto {
		if result.Content != nil {
			r.inlines.registerWebDocumentContext(ctx, now, *result.Content, webViewSessionTTL)
		}
		var err error
		media, err = r.domainInlineExternalContentMedia(ctx, result)
		if err != nil {
			return err
		}
	}
	r.resolveInlineContactMediaForSender(ctx, session.UserID, media)
	_, _, err := r.sendOutgoing(ctx, session.UserID, session.Peer, outgoingSend{
		randomID:     randomNonZeroInt64(),
		message:      result.Message,
		entities:     tgMessageEntities(result.Entities),
		media:        media,
		silent:       session.Silent,
		replyTo:      session.ReplyTo,
		replyToReady: true,
		sendAs:       session.SendAs,
		sendAsReady:  true,
		replyMarkup:  result.ReplyMarkup,
		viaBotID:     botID,
	})
	if err != nil {
		return err
	}
	r.webviews.consumeContext(ctx, session.QueryID, session.BotQueryID)
	r.pushUserMessage(ctx, session.UserID, "push webview result sent", &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateWebViewResultSent{QueryID: session.QueryID}},
		Date:    int(now.Unix()),
	})
	return nil
}

func (r *Router) webViewBotFromInput(ctx context.Context, userID int64, input tg.InputUserClass) (domain.User, domain.BotProfile, error) {
	bot, found, err := r.userFromInput(ctx, userID, input)
	if err != nil {
		return domain.User{}, domain.BotProfile{}, internalErr()
	}
	if !found || !bot.Bot {
		return domain.User{}, domain.BotProfile{}, botInvalidErr()
	}
	profile, found, err := r.deps.Bots.BotInfo(ctx, bot.ID)
	if err != nil {
		return domain.User{}, domain.BotProfile{}, internalErr()
	}
	if !found {
		return domain.User{}, domain.BotProfile{}, botInvalidErr()
	}
	return bot, profile, nil
}

func (r *Router) webViewBotAppFromInput(ctx context.Context, userID int64, input tg.InputBotAppClass) (domain.User, domain.BotProfile, domain.BotApp, *tg.BotApp, string, error) {
	switch app := input.(type) {
	case *tg.InputBotAppShortName:
		if !validBotAppShortName(app.ShortName) {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", botAppShortNameInvalidErr()
		}
		bot, profile, err := r.webViewBotAppOwnerFromInput(ctx, userID, app.BotID)
		if err != nil {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", err
		}
		domainApp, found, err := r.deps.Bots.GetBotAppByShortName(ctx, bot.ID, app.ShortName)
		if err != nil {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", internalErr()
		}
		if !found {
			domainApp, err = r.ensureMenuBackedBotApp(ctx, bot, profile, app.ShortName)
			if err != nil {
				return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", err
			}
		}
		if domainApp.Inactive || validateInlineWebURL(domainApp.URL, true) != nil {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", botAppInvalidErr()
		}
		return bot, profile, domainApp, r.tgBotAppFromDomain(ctx, domainApp), domainApp.URL, nil
	case *tg.InputBotAppID:
		if app.ID <= 0 {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", botAppInvalidErr()
		}
		domainApp, found, err := r.deps.Bots.GetBotAppByID(ctx, app.ID, app.AccessHash)
		if err != nil {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", internalErr()
		}
		if !found || domainApp.Inactive || validateInlineWebURL(domainApp.URL, true) != nil {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", botAppInvalidErr()
		}
		bot, found, err := r.deps.Users.ByID(ctx, userID, domainApp.BotUserID)
		if err != nil {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", internalErr()
		}
		if !found || !bot.Bot {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", botAppInvalidErr()
		}
		profile, found, err := r.deps.Bots.BotInfo(ctx, bot.ID)
		if err != nil {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", internalErr()
		}
		if !found {
			return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", botAppInvalidErr()
		}
		return bot, profile, domainApp, r.tgBotAppFromDomain(ctx, domainApp), domainApp.URL, nil
	default:
		return domain.User{}, domain.BotProfile{}, domain.BotApp{}, nil, "", botAppInvalidErr()
	}
}

func (r *Router) ensureMenuBackedBotApp(ctx context.Context, bot domain.User, profile domain.BotProfile, shortName string) (domain.BotApp, error) {
	rawURL, err := webViewMenuAppURL(profile, botAppInvalidErr())
	if err != nil {
		return domain.BotApp{}, err
	}
	title := profile.MenuButton.Text
	if title == "" {
		title = bot.FirstName
	}
	app := domain.BotApp{
		BotUserID:          bot.ID,
		ShortName:          strings.ToLower(shortName),
		Title:              title,
		Description:        profile.Description,
		URL:                rawURL,
		Main:               strings.EqualFold(shortName, "main"),
		RequestWriteAccess: true,
	}
	out, _, err := r.deps.Bots.UpsertBotApp(ctx, bot.ID, app)
	if err != nil {
		return domain.BotApp{}, botAppInvalidErr()
	}
	return out, nil
}

func (r *Router) webViewBotAppOwnerFromInput(ctx context.Context, userID int64, input tg.InputUserClass) (domain.User, domain.BotProfile, error) {
	bot, found, err := r.userFromInput(ctx, userID, input)
	if err != nil {
		return domain.User{}, domain.BotProfile{}, internalErr()
	}
	if !found || !bot.Bot {
		return domain.User{}, domain.BotProfile{}, botAppBotInvalidErr()
	}
	profile, found, err := r.deps.Bots.BotInfo(ctx, bot.ID)
	if err != nil {
		return domain.User{}, domain.BotProfile{}, internalErr()
	}
	if !found {
		return domain.User{}, domain.BotProfile{}, botAppBotInvalidErr()
	}
	return bot, profile, nil
}

func (r *Router) webViewPeerFromInput(ctx context.Context, userID int64, input tg.InputPeerClass, fallbackBotID int64) (domain.Peer, error) {
	switch input.(type) {
	case nil, *tg.InputPeerEmpty:
		if fallbackBotID <= 0 {
			return domain.Peer{}, peerIDInvalidErr()
		}
		return domain.Peer{Type: domain.PeerTypeUser, ID: fallbackBotID}, nil
	default:
		return r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	}
}

func webViewRequestURL(getURL func() (string, bool), fromBotMenu bool, profile domain.BotProfile) (string, error) {
	rawURL, ok := getURL()
	if !ok {
		if fromBotMenu && profile.MenuButton.Type == domain.BotMenuButtonWebView {
			rawURL = profile.MenuButton.URL
		} else if fromBotMenu {
			return "", botWebviewDisabledErr()
		} else {
			return "", urlInvalidErr()
		}
	}
	if validateInlineWebURL(rawURL, true) != nil {
		return "", urlInvalidErr()
	}
	return rawURL, nil
}

func webViewMenuAppURL(profile domain.BotProfile, disabledErr error) (string, error) {
	if profile.MenuButton.Type != domain.BotMenuButtonWebView || profile.MenuButton.URL == "" {
		return "", disabledErr
	}
	if validateInlineWebURL(profile.MenuButton.URL, true) != nil {
		return "", disabledErr
	}
	return profile.MenuButton.URL, nil
}

func validBotAppShortName(shortName string) bool {
	if shortName == "" || len(shortName) > 64 {
		return false
	}
	for _, r := range shortName {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

func (r *Router) tgBotAppFromDomain(ctx context.Context, in domain.BotApp) *tg.BotApp {
	out := &tg.BotApp{
		ID:          in.ID,
		AccessHash:  in.AccessHash,
		ShortName:   in.ShortName,
		Title:       in.Title,
		Description: in.Description,
		Photo:       &tg.PhotoEmpty{ID: in.PhotoID},
		Hash:        in.Hash,
	}
	if in.PhotoID != 0 && r.deps.Files != nil {
		if photo, found, err := r.deps.Files.GetPhoto(ctx, in.PhotoID); err == nil && found {
			out.Photo = tgPhoto(photo)
		}
	}
	if out.Photo == nil {
		out.Photo = &tg.PhotoEmpty{ID: in.ID}
	}
	if in.DocumentID != 0 && r.deps.Files != nil {
		if doc, found, err := r.deps.Files.GetDocument(ctx, in.DocumentID); err == nil && found {
			out.SetDocument(tgDocument(doc))
		}
	}
	return out
}

func tgMenuBackedBotApp(bot domain.User, profile domain.BotProfile, shortName string) *tg.BotApp {
	title := profile.MenuButton.Text
	if title == "" {
		title = bot.FirstName
	}
	app := &tg.BotApp{
		ID:          bot.ID,
		AccessHash:  menuBackedBotAppAccessHash(profile),
		ShortName:   shortName,
		Title:       title,
		Description: profile.Description,
		Photo:       &tg.PhotoEmpty{ID: bot.ID},
		Hash:        menuBackedBotAppHash(bot, profile, shortName),
	}
	return app
}

func menuBackedBotAppAccessHash(profile domain.BotProfile) int64 {
	return stableBotAppInt64("access", strconv.FormatInt(profile.BotUserID, 10), profile.TokenSecret, profile.MenuButton.URL)
}

func menuBackedBotAppHash(bot domain.User, profile domain.BotProfile, shortName string) int64 {
	return stableBotAppInt64(
		"hash",
		strconv.FormatInt(profile.BotUserID, 10),
		shortName,
		profile.MenuButton.Text,
		profile.MenuButton.URL,
		profile.Description,
		bot.FirstName,
		bot.Username,
	)
}

func stableBotAppInt64(parts ...string) int64 {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	sum := hash.Sum(nil)
	value := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	if value == 0 {
		return 1
	}
	return value
}

func webViewStartParam(getStartParam func() (string, bool)) (string, error) {
	startParam, ok := getStartParam()
	if ok && !validInlineStartParam(startParam) {
		return "", startParamInvalidErr()
	}
	return startParam, nil
}

func validateWebViewPlatform(platform string) error {
	if platform == "" || utf8.RuneCountInString(platform) > domain.MaxBotInlineSwitchTextLen || strings.TrimSpace(platform) != platform {
		return platformInvalidErr()
	}
	return nil
}

func webViewURLWithInitData(rawURL, botQueryID string, profile domain.BotProfile, user domain.User, startParam, platform string, now time.Time) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	initData, err := signedWebViewInitData(botQueryID, profile, user, startParam, now)
	if err != nil {
		return "", err
	}
	q := parsed.Query()
	q.Set("tgWebAppData", initData)
	q.Set("tgWebAppVersion", "6.0")
	q.Set("tgWebAppPlatform", platform)
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

func signedWebViewInitData(botQueryID string, profile domain.BotProfile, user domain.User, startParam string, now time.Time) (string, error) {
	userJSON, err := json.Marshal(webViewInitUser{
		ID:        user.ID,
		FirstName: user.FirstName,
		LastName:  user.LastName,
		Username:  user.Username,
	})
	if err != nil {
		return "", err
	}
	values := url.Values{}
	values.Set("query_id", botQueryID)
	values.Set("user", string(userJSON))
	values.Set("auth_date", strconv.FormatInt(now.Unix(), 10))
	if startParam != "" {
		values.Set("start_param", startParam)
	}
	token := domain.FormatBotToken(profile.BotUserID, profile.TokenSecret)
	values.Set("hash", webViewInitDataHash(values, token))
	return values.Encode(), nil
}

type webViewInitUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

func webViewInitDataHash(values url.Values, botToken string) string {
	check := webViewDataCheckString(values)
	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secretMAC.Write([]byte(botToken))
	secret := secretMAC.Sum(nil)
	dataMAC := hmac.New(sha256.New, secret)
	_, _ = dataMAC.Write([]byte(check))
	return hex.EncodeToString(dataMAC.Sum(nil))
}

func webViewDataCheckString(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, key := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(values.Get(key))
	}
	return b.String()
}
