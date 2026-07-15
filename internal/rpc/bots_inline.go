package rpc

import (
	"context"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	botInlineQueryTTL        = 25 * time.Second
	botInlineCacheMaxTTL     = 5 * time.Minute
	botInlineCacheMaxEntries = 256
)

func botInlineDisabledErr() error      { return tgerr.New(400, "BOT_INLINE_DISABLED") }
func botInlineGeoNotAllowedErr() error { return tgerr.New(400, "BOT_INLINE_GEO_NOT_ALLOWED") }
func botWebviewDisabledErr() error     { return tgerr.New(400, "BOT_WEBVIEW_DISABLED") }
func queryIDInvalidErr() error         { return tgerr.New(400, "QUERY_ID_INVALID") }
func queryIDEmptyErr() error           { return tgerr.New(400, "QUERY_ID_EMPTY") }
func resultIDEmptyErr() error          { return tgerr.New(400, "RESULT_ID_EMPTY") }
func resultIDInvalidErr() error        { return tgerr.New(400, "RESULT_ID_INVALID") }
func resultIDDuplicateErr() error      { return tgerr.New(400, "RESULT_ID_DUPLICATE") }
func resultTypeInvalidErr() error      { return tgerr.New(400, "RESULT_TYPE_INVALID") }
func resultsTooMuchErr() error         { return tgerr.New(400, "RESULTS_TOO_MUCH") }
func sendMessageTypeInvalidErr() error { return tgerr.New(400, "SEND_MESSAGE_TYPE_INVALID") }
func nextOffsetInvalidErr() error      { return tgerr.New(400, "NEXT_OFFSET_INVALID") }
func startParamEmptyErr() error        { return tgerr.New(400, "START_PARAM_EMPTY") }
func switchPmTextEmptyErr() error      { return tgerr.New(400, "SWITCH_PM_TEXT_EMPTY") }
func switchWebviewInvalidErr() error   { return tgerr.New(400, "SWITCH_WEBVIEW_URL_INVALID") }
func inlineResultExpiredErr() error    { return tgerr.New(400, "INLINE_RESULT_EXPIRED") }
func webDocumentInvalidErr() error     { return tgerr.New(400, "WEBDOCUMENT_INVALID") }
func webDocumentMimeInvalidErr() error { return tgerr.New(400, "WEBDOCUMENT_MIME_INVALID") }
func webDocumentSizeTooBigErr() error  { return tgerr.New(400, "WEBDOCUMENT_SIZE_TOO_BIG") }
func webDocumentURLEmptyErr() error    { return tgerr.New(400, "WEBDOCUMENT_URL_EMPTY") }
func webDocumentURLInvalidErr() error  { return tgerr.New(400, "WEBDOCUMENT_URL_INVALID") }

func (r *Router) onMessagesGetInlineBotResults(ctx context.Context, req *tg.MessagesGetInlineBotResultsRequest) (*tg.MessagesBotResults, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 || r.deps.Bots == nil {
		return nil, botInvalidErr()
	}
	if len(req.Offset) > domain.MaxBotInlineNextOffsetLen {
		return nil, nextOffsetInvalidErr()
	}
	bot, found, err := r.userFromInput(ctx, userID, req.Bot)
	if err != nil {
		return nil, internalErr()
	}
	if !found || !bot.Bot {
		return nil, botInvalidErr()
	}
	profile, found, err := r.deps.Bots.BotInfo(ctx, bot.ID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || profile.InlinePlaceholder == "" {
		return nil, botInlineDisabledErr()
	}
	var queryGeo *domain.MessageGeoPoint
	if geo, ok := req.GetGeoPointAsNotEmpty(); ok {
		if !profile.InlineGeo {
			return nil, botInlineGeoNotAllowedErr()
		}
		queryGeo, err = domainGeoPointFromInput(geo)
		if err != nil {
			return nil, err
		}
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	cacheKey := inlineCacheKey{
		botUserID: bot.ID,
		userID:    userID,
		peer:      peer,
		query:     req.Query,
		offset:    req.Offset,
	}
	if queryGeo != nil {
		cacheKey.hasGeo = true
		cacheKey.geoLat = queryGeo.Lat
		cacheKey.geoLong = queryGeo.Long
		cacheKey.geoAccuracy = queryGeo.AccuracyRadius
	}
	now := r.clock.Now()
	if cached, ok := r.inlines.cachedContext(ctx, now, cacheKey); ok {
		results := r.inlines.registerCachedContext(ctx, now, bot.ID, userID, peer, cached)
		return r.tgBotInlineResults(ctx, userID, results), nil
	}
	queryID, pending := r.inlines.registerWithCacheKeyContext(ctx, now, bot.ID, userID, peer, cacheKey)
	defer r.inlines.deregisterIfUnansweredContext(ctx, queryID)

	peerType := tgInlineQueryPeerType(ctx, r, userID, bot.ID, peer)
	update := &tg.UpdateBotInlineQuery{
		QueryID:  queryID,
		UserID:   userID,
		Query:    req.Query,
		Offset:   req.Offset,
		PeerType: peerType,
	}
	if queryGeo != nil {
		update.SetGeo(tgGeoPoint(*queryGeo))
	}
	r.pushUserMessage(ctx, bot.ID, "push bot inline query", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(now.Unix()),
	})
	push := store.BotInlineQueryPush{
		QueryID:   queryID,
		BotUserID: bot.ID,
		UserID:    userID,
		Query:     req.Query,
		Offset:    req.Offset,
		PeerType:  storeInlineQueryPeerType(peerType),
		Date:      int(now.Unix()),
	}
	if queryGeo != nil {
		geo := *queryGeo
		push.Geo = &geo
	}
	r.publishInlineBotQuery(ctx, push)

	waitCtx, cancel := context.WithTimeout(ctx, botInlineQueryTTL)
	defer cancel()
	results, err := r.awaitInlineBotResults(waitCtx, pending, userID, queryID)
	if err != nil {
		return nil, err
	}
	return r.tgBotInlineResults(ctx, userID, results), nil
}

func (r *Router) onMessagesSetInlineBotResults(ctx context.Context, req *tg.MessagesSetInlineBotResultsRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	results, err := r.domainInlineResultsFromTG(ctx, botID, req)
	if err != nil {
		return false, err
	}
	if !r.inlines.resolveContext(ctx, r.clock.Now(), botID, req.QueryID, results) {
		return false, queryIDInvalidErr()
	}
	return true, nil
}

func (r *Router) onMessagesSendInlineBotResult(ctx context.Context, req *tg.MessagesSendInlineBotResultRequest) (tg.UpdatesClass, error) {
	if req.QueryID == 0 {
		return nil, queryIDEmptyErr()
	}
	if req.ID == "" {
		return nil, resultIDEmptyErr()
	}
	if req.RandomID == 0 {
		return nil, randomIDEmptyErr()
	}
	if req.ScheduleDate != 0 && !scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix())) {
		return nil, scheduleDateInvalidErr()
	}
	if req.QuickReplyShortcut != nil {
		return nil, shortcutInvalidErr()
	}
	if req.AllowPaidStars < 0 {
		return nil, starsAmountInvalidErr()
	}
	if req.AllowPaidStars > 0 {
		return nil, paymentUnsupportedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	if err := r.checkSendRateLimit(ctx, userID, 1); err != nil {
		return nil, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	results, result, ok := r.inlines.resultForSendContext(ctx, r.clock.Now(), userID, req.QueryID, req.ID)
	if !ok {
		return nil, inlineResultExpiredErr()
	}
	if !r.inlineResultsAllowPeer(ctx, userID, results, peer) {
		return nil, peerIDInvalidErr()
	}
	media := cloneInlineMedia(result.Media)
	if result.MediaAuto {
		media, err = r.domainInlineExternalContentMedia(ctx, result)
		if err != nil {
			return nil, err
		}
	}
	r.resolveInlineContactMediaForSender(ctx, userID, media)
	updates, duplicate, err := r.sendOutgoing(ctx, userID, peer, outgoingSend{
		randomID:     req.RandomID,
		message:      result.Message,
		entities:     tgMessageEntities(result.Entities),
		media:        media,
		silent:       req.Silent,
		replyToInput: req.ReplyTo,
		sendAsInput:  req.SendAs,
		clearDraft:   req.ClearDraft,
		replyMarkup:  result.ReplyMarkup,
		viaBotID:     results.BotUserID,
	})
	if err != nil {
		return nil, err
	}
	if !duplicate {
		r.pushInlineBotSendFeedback(ctx, userID, results, result, updates)
		r.inlines.consumeContext(ctx, req.QueryID)
	}
	return updates, nil
}

func (r *Router) inlineResultsAllowPeer(ctx context.Context, userID int64, results domain.BotInlineResults, peer domain.Peer) bool {
	if results.Peer.Type != "" || results.Peer.ID != 0 {
		return results.Peer == peer
	}
	if len(results.PeerTypes) == 0 {
		return true
	}
	actual := storeInlineQueryPeerType(tgInlineQueryPeerType(ctx, r, userID, results.BotUserID, peer))
	if actual == "" {
		return false
	}
	for _, allowed := range results.PeerTypes {
		if allowed == actual {
			return true
		}
	}
	return false
}

func (r *Router) awaitInlineBotResults(ctx context.Context, pending *pendingInlineQuery, userID, queryID int64) (domain.BotInlineResults, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case results := <-pending.ch:
			return results, nil
		case <-ticker.C:
			if results, ok := r.inlines.resultsForQueryContext(ctx, r.clock.Now(), userID, queryID); ok {
				return results, nil
			}
		case <-ctx.Done():
			return domain.BotInlineResults{}, botResponseTimeoutErr()
		}
	}
}

func (r *Router) domainInlineResultsFromTG(ctx context.Context, botID int64, req *tg.MessagesSetInlineBotResultsRequest) (domain.BotInlineResults, error) {
	if len(req.Results) > domain.MaxBotInlineResults {
		return domain.BotInlineResults{}, resultsTooMuchErr()
	}
	if len(req.NextOffset) > domain.MaxBotInlineNextOffsetLen {
		return domain.BotInlineResults{}, nextOffsetInvalidErr()
	}
	switchPM, err := domainInlineSwitchPM(req)
	if err != nil {
		return domain.BotInlineResults{}, err
	}
	switchWeb, err := domainInlineSwitchWebView(req)
	if err != nil {
		return domain.BotInlineResults{}, err
	}
	seen := make(map[string]struct{}, len(req.Results))
	out := domain.BotInlineResults{
		BotUserID:  botID,
		QueryID:    req.QueryID,
		Gallery:    req.Gallery,
		Private:    req.Private,
		CacheTime:  req.CacheTime,
		NextOffset: req.NextOffset,
		SwitchPM:   switchPM,
		SwitchWeb:  switchWeb,
		Results:    make([]domain.BotInlineResult, 0, len(req.Results)),
	}
	for _, raw := range req.Results {
		item, err := r.domainInlineResultFromTG(ctx, botID, raw)
		if err != nil {
			return domain.BotInlineResults{}, err
		}
		if _, ok := seen[item.ID]; ok {
			return domain.BotInlineResults{}, resultIDDuplicateErr()
		}
		seen[item.ID] = struct{}{}
		out.Results = append(out.Results, item)
	}
	return out, nil
}

func domainInlineSwitchWebView(req *tg.MessagesSetInlineBotResultsRequest) (*domain.BotInlineSwitchWebView, error) {
	switchWeb, ok := req.GetSwitchWebview()
	if !ok {
		return nil, nil
	}
	if strings.TrimSpace(switchWeb.Text) == "" || utf8.RuneCountInString(switchWeb.Text) > domain.MaxBotInlineSwitchTextLen {
		return nil, buttonInvalidErr()
	}
	if err := validateInlineWebURL(switchWeb.URL, true); err != nil {
		return nil, switchWebviewInvalidErr()
	}
	return &domain.BotInlineSwitchWebView{
		Text: switchWeb.Text,
		URL:  switchWeb.URL,
	}, nil
}

func domainInlineSwitchPM(req *tg.MessagesSetInlineBotResultsRequest) (*domain.BotInlineSwitchPM, error) {
	switchPM, ok := req.GetSwitchPm()
	if !ok {
		return nil, nil
	}
	if switchPM.Text == "" {
		return nil, switchPmTextEmptyErr()
	}
	if utf8.RuneCountInString(switchPM.Text) > domain.MaxBotInlineSwitchTextLen {
		return nil, buttonInvalidErr()
	}
	if switchPM.StartParam == "" {
		return nil, startParamEmptyErr()
	}
	if !validInlineStartParam(switchPM.StartParam) {
		return nil, startParamInvalidErr()
	}
	return &domain.BotInlineSwitchPM{
		Text:       switchPM.Text,
		StartParam: switchPM.StartParam,
	}, nil
}

func validInlineStartParam(in string) bool {
	if utf8.RuneCountInString(in) > domain.MaxStartParamLen {
		return false
	}
	for _, r := range in {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (r *Router) domainInlineResultFromTG(ctx context.Context, botID int64, raw tg.InputBotInlineResultClass) (domain.BotInlineResult, error) {
	switch result := raw.(type) {
	case *tg.InputBotInlineResult:
		if result.ID == "" {
			return domain.BotInlineResult{}, resultIDEmptyErr()
		}
		if len(result.ID) > domain.MaxBotInlineResultIDLen {
			return domain.BotInlineResult{}, resultIDInvalidErr()
		}
		if result.Type == "" {
			return domain.BotInlineResult{}, resultTypeInvalidErr()
		}
		switch msg := result.SendMessage.(type) {
		case *tg.InputBotInlineMessageText:
			if result.Type != "article" {
				return domain.BotInlineResult{}, resultTypeInvalidErr()
			}
			resultURL, thumb, content, err := domainInlineResultWebPreview(result)
			if err != nil {
				return domain.BotInlineResult{}, err
			}
			message, entities, markup, noWebpage, err := domainInlineTextMessage(botID, msg)
			if err != nil {
				return domain.BotInlineResult{}, err
			}
			return domain.BotInlineResult{
				ID:          result.ID,
				Type:        result.Type,
				Title:       result.Title,
				Description: result.Description,
				URL:         resultURL,
				Thumb:       thumb,
				Content:     content,
				Message:     message,
				Entities:    entities,
				ReplyMarkup: markup,
				NoWebpage:   noWebpage,
			}, nil
		case *tg.InputBotInlineMessageMediaAuto:
			if !validInlineExternalContentResultType(result.Type) {
				return domain.BotInlineResult{}, resultTypeInvalidErr()
			}
			resultURL, thumb, content, err := domainInlineResultWebPreview(result)
			if err != nil {
				return domain.BotInlineResult{}, err
			}
			if content == nil {
				return domain.BotInlineResult{}, webDocumentInvalidErr()
			}
			if !inlineExternalContentMimeAllowed(result.Type, content.MimeType) {
				return domain.BotInlineResult{}, mediaInvalidErr()
			}
			message, entities, markup, err := domainInlineMediaAutoMessage(botID, msg)
			if err != nil {
				return domain.BotInlineResult{}, err
			}
			return domain.BotInlineResult{
				ID:          result.ID,
				Type:        result.Type,
				Title:       result.Title,
				Description: result.Description,
				URL:         resultURL,
				Thumb:       thumb,
				Content:     content,
				Message:     message,
				Entities:    entities,
				ReplyMarkup: markup,
				MediaAuto:   true,
			}, nil
		case *tg.InputBotInlineMessageMediaGeo:
			if result.Type != "geo" {
				return domain.BotInlineResult{}, resultTypeInvalidErr()
			}
			if inlineResultHasWebPreview(result) {
				return domain.BotInlineResult{}, webDocumentInvalidErr()
			}
			media, markup, err := domainInlineGeoMessage(msg)
			if err != nil {
				return domain.BotInlineResult{}, err
			}
			return domain.BotInlineResult{
				ID:          result.ID,
				Type:        result.Type,
				Title:       result.Title,
				Description: result.Description,
				ReplyMarkup: markup,
				Media:       media,
			}, nil
		case *tg.InputBotInlineMessageMediaVenue:
			if result.Type != "venue" {
				return domain.BotInlineResult{}, resultTypeInvalidErr()
			}
			if inlineResultHasWebPreview(result) {
				return domain.BotInlineResult{}, webDocumentInvalidErr()
			}
			media, markup, err := domainInlineVenueMessage(msg)
			if err != nil {
				return domain.BotInlineResult{}, err
			}
			return domain.BotInlineResult{
				ID:          result.ID,
				Type:        result.Type,
				Title:       result.Title,
				Description: result.Description,
				ReplyMarkup: markup,
				Media:       media,
			}, nil
		case *tg.InputBotInlineMessageMediaContact:
			if result.Type != "contact" {
				return domain.BotInlineResult{}, resultTypeInvalidErr()
			}
			if inlineResultHasWebPreview(result) {
				return domain.BotInlineResult{}, webDocumentInvalidErr()
			}
			media, markup, err := domainInlineContactMessage(msg)
			if err != nil {
				return domain.BotInlineResult{}, err
			}
			return domain.BotInlineResult{
				ID:          result.ID,
				Type:        result.Type,
				Title:       result.Title,
				Description: result.Description,
				ReplyMarkup: markup,
				Media:       media,
			}, nil
		default:
			return domain.BotInlineResult{}, sendMessageTypeInvalidErr()
		}
	case *tg.InputBotInlineResultPhoto:
		if result.ID == "" {
			return domain.BotInlineResult{}, resultIDEmptyErr()
		}
		if len(result.ID) > domain.MaxBotInlineResultIDLen {
			return domain.BotInlineResult{}, resultIDInvalidErr()
		}
		if result.Type != "photo" {
			return domain.BotInlineResult{}, resultTypeInvalidErr()
		}
		media, err := r.domainInlinePhotoMedia(ctx, result.Photo)
		if err != nil {
			return domain.BotInlineResult{}, err
		}
		message, entities, markup, err := domainInlineMediaAutoMessage(botID, result.SendMessage)
		if err != nil {
			return domain.BotInlineResult{}, err
		}
		return domain.BotInlineResult{
			ID:          result.ID,
			Type:        result.Type,
			Message:     message,
			Entities:    entities,
			ReplyMarkup: markup,
			Media:       media,
		}, nil
	case *tg.InputBotInlineResultDocument:
		if result.ID == "" {
			return domain.BotInlineResult{}, resultIDEmptyErr()
		}
		if len(result.ID) > domain.MaxBotInlineResultIDLen {
			return domain.BotInlineResult{}, resultIDInvalidErr()
		}
		if !validInlineDocumentResultType(result.Type) {
			return domain.BotInlineResult{}, resultTypeInvalidErr()
		}
		media, err := r.domainInlineDocumentMedia(ctx, result.Document)
		if err != nil {
			return domain.BotInlineResult{}, err
		}
		message, entities, markup, err := domainInlineMediaAutoMessage(botID, result.SendMessage)
		if err != nil {
			return domain.BotInlineResult{}, err
		}
		return domain.BotInlineResult{
			ID:          result.ID,
			Type:        result.Type,
			Title:       result.Title,
			Description: result.Description,
			Message:     message,
			Entities:    entities,
			ReplyMarkup: markup,
			Media:       media,
		}, nil
	default:
		return domain.BotInlineResult{}, resultTypeInvalidErr()
	}
}

func domainInlineResultWebPreview(result *tg.InputBotInlineResult) (string, *domain.BotInlineWebDocument, *domain.BotInlineWebDocument, error) {
	var resultURL string
	if v, ok := result.GetURL(); ok {
		if err := validateInlineWebURL(v, false); err != nil {
			return "", nil, nil, err
		}
		resultURL = v
	}
	var thumb *domain.BotInlineWebDocument
	if raw, ok := result.GetThumb(); ok {
		var err error
		thumb, err = domainInlineWebDocument(raw)
		if err != nil {
			return "", nil, nil, err
		}
	}
	var content *domain.BotInlineWebDocument
	if raw, ok := result.GetContent(); ok {
		var err error
		content, err = domainInlineWebDocument(raw)
		if err != nil {
			return "", nil, nil, err
		}
	}
	return resultURL, thumb, content, nil
}

func inlineResultHasWebPreview(result *tg.InputBotInlineResult) bool {
	if _, ok := result.GetURL(); ok {
		return true
	}
	if _, ok := result.GetThumb(); ok {
		return true
	}
	if _, ok := result.GetContent(); ok {
		return true
	}
	return false
}

func domainInlineWebDocument(input tg.InputWebDocument) (*domain.BotInlineWebDocument, error) {
	if err := validateInlineWebURL(input.URL, true); err != nil {
		return nil, err
	}
	if input.Size <= 0 {
		return nil, webDocumentInvalidErr()
	}
	if input.Size > domain.MaxBotInlineWebSize {
		return nil, webDocumentSizeTooBigErr()
	}
	if !validInlineWebMime(input.MimeType) {
		return nil, webDocumentMimeInvalidErr()
	}
	return &domain.BotInlineWebDocument{
		URL:        input.URL,
		AccessHash: randomNonZeroInt64(),
		Size:       input.Size,
		MimeType:   input.MimeType,
		Attributes: domainDocumentAttributes(input.Attributes),
	}, nil
}

func validateInlineWebURL(raw string, emptyIsURLSpecific bool) error {
	if raw == "" {
		if emptyIsURLSpecific {
			return webDocumentURLEmptyErr()
		}
		return webDocumentURLInvalidErr()
	}
	if len(raw) > domain.MaxBotInlineWebURLLen || strings.TrimSpace(raw) != raw {
		return webDocumentURLInvalidErr()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return webDocumentURLInvalidErr()
	}
	if strings.ToLower(u.Scheme) != "https" {
		return webDocumentURLInvalidErr()
	}
	if strings.ContainsAny(u.Host, " \t\r\n") {
		return webDocumentURLInvalidErr()
	}
	return nil
}

func validInlineWebMime(in string) bool {
	if in == "" || len(in) > domain.MaxBotInlineWebMimeLen || strings.TrimSpace(in) != in {
		return false
	}
	slash := strings.IndexByte(in, '/')
	if slash <= 0 || slash == len(in)-1 {
		return false
	}
	for _, r := range in {
		if r <= 32 || r == 127 {
			return false
		}
	}
	return true
}

func domainInlineTextMessage(botID int64, msg *tg.InputBotInlineMessageText) (string, []domain.MessageEntity, *domain.MessageReplyMarkup, bool, error) {
	if msg.Message == "" {
		return "", nil, nil, false, messageEmptyErr()
	}
	if utf8.RuneCountInString(msg.Message) > maxSendMessageTextLength {
		return "", nil, nil, false, messageTooLongErr()
	}
	if len(msg.Entities) > maxMessageEntityCount {
		return "", nil, nil, false, entitiesTooLongErr()
	}
	markup, err := domainReplyMarkupForSender(msg.ReplyMarkup, true)
	if err != nil {
		return "", nil, nil, false, replyMarkupErr(err)
	}
	return msg.Message, domainMessageEntitiesForViewer(botID, msg.Entities), markup, msg.NoWebpage, nil
}

func domainInlineMediaAutoMessage(botID int64, raw tg.InputBotInlineMessageClass) (string, []domain.MessageEntity, *domain.MessageReplyMarkup, error) {
	msg, ok := raw.(*tg.InputBotInlineMessageMediaAuto)
	if !ok {
		return "", nil, nil, sendMessageTypeInvalidErr()
	}
	if utf8.RuneCountInString(msg.Message) > maxSendMessageTextLength {
		return "", nil, nil, messageTooLongErr()
	}
	if len(msg.Entities) > maxMessageEntityCount {
		return "", nil, nil, entitiesTooLongErr()
	}
	markup, err := domainReplyMarkupForSender(msg.ReplyMarkup, true)
	if err != nil {
		return "", nil, nil, replyMarkupErr(err)
	}
	return msg.Message, domainMessageEntitiesForViewer(botID, msg.Entities), markup, nil
}

func domainInlineGeoMessage(msg *tg.InputBotInlineMessageMediaGeo) (*domain.MessageMedia, *domain.MessageReplyMarkup, error) {
	if _, ok := msg.GetHeading(); ok {
		return nil, nil, mediaInvalidErr()
	}
	if _, ok := msg.GetPeriod(); ok {
		return nil, nil, mediaInvalidErr()
	}
	if _, ok := msg.GetProximityNotificationRadius(); ok {
		return nil, nil, mediaInvalidErr()
	}
	geo, err := domainGeoPointFromInput(msg.GeoPoint)
	if err != nil {
		return nil, nil, err
	}
	markup, err := domainReplyMarkupForSender(msg.ReplyMarkup, true)
	if err != nil {
		return nil, nil, replyMarkupErr(err)
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindGeo, Geo: geo}, markup, nil
}

func domainInlineVenueMessage(msg *tg.InputBotInlineMessageMediaVenue) (*domain.MessageMedia, *domain.MessageReplyMarkup, error) {
	geo, err := domainGeoPointFromInput(msg.GeoPoint)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(msg.Title) == "" {
		return nil, nil, mediaEmptyErr()
	}
	if utf8.RuneCountInString(msg.Title) > maxVenueTitleLength ||
		utf8.RuneCountInString(msg.Address) > maxVenueAddressLength ||
		utf8.RuneCountInString(msg.Provider) > maxVenueProviderLength ||
		utf8.RuneCountInString(msg.VenueID) > maxVenueIDLength ||
		utf8.RuneCountInString(msg.VenueType) > maxVenueIDLength {
		return nil, nil, mediaInvalidErr()
	}
	markup, err := domainReplyMarkupForSender(msg.ReplyMarkup, true)
	if err != nil {
		return nil, nil, replyMarkupErr(err)
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindVenue, Venue: &domain.MessageVenue{
		Geo:       *geo,
		Title:     msg.Title,
		Address:   msg.Address,
		Provider:  msg.Provider,
		VenueID:   msg.VenueID,
		VenueType: msg.VenueType,
	}}, markup, nil
}

func domainInlineContactMessage(msg *tg.InputBotInlineMessageMediaContact) (*domain.MessageMedia, *domain.MessageReplyMarkup, error) {
	if !validContactInput(msg.PhoneNumber, msg.FirstName, msg.LastName, "", 0) || utf8.RuneCountInString(msg.Vcard) > maxContactVcardLength {
		return nil, nil, mediaInvalidErr()
	}
	if strings.TrimSpace(msg.PhoneNumber) == "" && strings.TrimSpace(msg.FirstName) == "" && strings.TrimSpace(msg.LastName) == "" && strings.TrimSpace(msg.Vcard) == "" {
		return nil, nil, mediaEmptyErr()
	}
	markup, err := domainReplyMarkupForSender(msg.ReplyMarkup, true)
	if err != nil {
		return nil, nil, replyMarkupErr(err)
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{
		PhoneNumber: msg.PhoneNumber,
		FirstName:   msg.FirstName,
		LastName:    msg.LastName,
		Vcard:       msg.Vcard,
	}}, markup, nil
}

func (r *Router) resolveInlineContactMediaForSender(ctx context.Context, userID int64, media *domain.MessageMedia) {
	if media == nil || media.Kind != domain.MessageMediaKindContact || media.Contact == nil || media.Contact.UserID != 0 {
		return
	}
	media.Contact.UserID = r.messageContactUserID(ctx, userID, media.Contact.PhoneNumber)
}

func (r *Router) domainInlinePhotoMedia(ctx context.Context, input tg.InputPhotoClass) (*domain.MessageMedia, error) {
	if r.deps.Files == nil {
		return nil, mediaInvalidErr()
	}
	photoID, ok := inputPhotoID(input)
	if !ok {
		return nil, photoInvalidErr()
	}
	photo, found, err := r.deps.Files.GetPhoto(ctx, photoID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, photoInvalidErr()
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &photo}, nil
}

func (r *Router) domainInlineDocumentMedia(ctx context.Context, input tg.InputDocumentClass) (*domain.MessageMedia, error) {
	if r.deps.Files == nil {
		return nil, mediaInvalidErr()
	}
	docIDs, ok := inputDocumentCandidateIDs(input)
	if !ok {
		return nil, mediaInvalidErr()
	}
	for _, docID := range docIDs {
		doc, found, err := r.deps.Files.GetDocument(ctx, docID)
		if err != nil {
			return nil, internalErr()
		}
		if found {
			return messageMediaFromDocument(doc, false, 0), nil
		}
	}
	return nil, mediaInvalidErr()
}

func validInlineDocumentResultType(t string) bool {
	switch t {
	case "file", "gif", "video", "audio", "voice", "sticker":
		return true
	default:
		return false
	}
}

func validInlineExternalContentResultType(t string) bool {
	return t == "photo" || validInlineDocumentResultType(t)
}

func (r *Router) domainInlineExternalContentMedia(ctx context.Context, result domain.BotInlineResult) (*domain.MessageMedia, error) {
	if r.deps.Files == nil || result.Content == nil {
		return nil, mediaInvalidErr()
	}
	document, data, mime, err := r.registeredInlineWebDocumentBytes(ctx, result.Content.URL, result.Content.AccessHash)
	if err != nil {
		return nil, mediaInvalidErr()
	}
	document.MimeType = mime
	document.Size = len(data)
	if !inlineExternalContentMimeAllowed(result.Type, mime) {
		return nil, mediaInvalidErr()
	}
	if result.Type == "photo" {
		photo, err := r.deps.Files.CreatePhotoFromBytes(ctx, data)
		if err != nil {
			return nil, photoInvalidErr()
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &photo}, nil
	}
	spec := inlineExternalContentDocumentSpec(result.Type, document)
	doc, err := r.deps.Files.CreateDocumentFromBytes(ctx, data, spec)
	if err != nil {
		return nil, documentInvalidErr()
	}
	return messageMediaFromDocument(doc, false, 0), nil
}

func inlineExternalContentMimeAllowed(resultType, mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if !validInlineWebMime(mime) {
		return false
	}
	switch resultType {
	case "photo":
		return strings.HasPrefix(mime, "image/") && mime != "image/gif"
	case "gif":
		return mime == "image/gif" || mime == "video/mp4" || mime == "image/webp"
	case "video":
		return strings.HasPrefix(mime, "video/")
	case "audio", "voice":
		return strings.HasPrefix(mime, "audio/")
	case "sticker":
		return strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "application/x-tg")
	case "file":
		return true
	default:
		return false
	}
}

func inlineExternalContentDocumentSpec(resultType string, document domain.BotInlineWebDocument) domain.DocumentSpec {
	mime := strings.ToLower(strings.TrimSpace(document.MimeType))
	attrs := append([]domain.DocumentAttribute(nil), document.Attributes...)
	w, h := inlineExternalContentDimensions(attrs)
	switch resultType {
	case "gif":
		attrs = ensureDocumentAttribute(attrs, domain.DocumentAttribute{Kind: domain.DocAttrAnimated})
		attrs = ensureImageSizeAttribute(attrs, w, h)
	case "video":
		attrs = ensureVideoAttribute(attrs, w, h)
	case "audio":
		attrs = ensureAudioAttribute(attrs, false)
	case "voice":
		attrs = ensureAudioAttribute(attrs, true)
	case "sticker":
		attrs = ensureImageSizeAttribute(attrs, w, h)
		attrs = ensureDocumentAttribute(attrs, domain.DocumentAttribute{Kind: domain.DocAttrSticker})
	}
	attrs = ensureFilenameAttribute(attrs, inlineExternalContentFilename(resultType, mime))
	return domain.DocumentSpec{MimeType: mime, Attributes: attrs}
}

func inlineExternalContentDimensions(attrs []domain.DocumentAttribute) (int, int) {
	for _, attr := range attrs {
		if (attr.Kind == domain.DocAttrImageSize || attr.Kind == domain.DocAttrVideo) && attr.W > 0 && attr.H > 0 {
			return attr.W, attr.H
		}
	}
	return 0, 0
}

func ensureImageSizeAttribute(attrs []domain.DocumentAttribute, w, h int) []domain.DocumentAttribute {
	if hasDocumentAttribute(attrs, domain.DocAttrImageSize) || w <= 0 || h <= 0 {
		return attrs
	}
	return append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrImageSize, W: w, H: h})
}

func ensureVideoAttribute(attrs []domain.DocumentAttribute, w, h int) []domain.DocumentAttribute {
	if hasDocumentAttribute(attrs, domain.DocAttrVideo) {
		return attrs
	}
	return append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: w, H: h, SupportsStreaming: true})
}

func ensureAudioAttribute(attrs []domain.DocumentAttribute, voice bool) []domain.DocumentAttribute {
	for i := range attrs {
		if attrs[i].Kind == domain.DocAttrAudio {
			if voice {
				attrs[i].Voice = true
			}
			return attrs
		}
	}
	return append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrAudio, Voice: voice})
}

func ensureFilenameAttribute(attrs []domain.DocumentAttribute, name string) []domain.DocumentAttribute {
	for i := range attrs {
		if attrs[i].Kind == domain.DocAttrFilename {
			if strings.TrimSpace(attrs[i].FileName) == "" {
				attrs[i].FileName = name
			}
			return attrs
		}
	}
	return append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrFilename, FileName: name})
}

func ensureDocumentAttribute(attrs []domain.DocumentAttribute, attr domain.DocumentAttribute) []domain.DocumentAttribute {
	if hasDocumentAttribute(attrs, attr.Kind) {
		return attrs
	}
	return append(attrs, attr)
}

func hasDocumentAttribute(attrs []domain.DocumentAttribute, kind domain.DocumentAttributeKind) bool {
	for _, attr := range attrs {
		if attr.Kind == kind {
			return true
		}
	}
	return false
}

func inlineExternalContentFilename(resultType, mime string) string {
	switch resultType {
	case "gif":
		if mime == "video/mp4" {
			return "animation.mp4"
		}
		return "animation.gif"
	case "video":
		return "video.mp4"
	case "audio":
		return "audio.mp3"
	case "voice":
		return "audio.ogg"
	case "sticker":
		return "sticker.webp"
	default:
		if slash := strings.LastIndexByte(mime, '/'); slash >= 0 && slash < len(mime)-1 {
			ext := mime[slash+1:]
			if ext == "jpeg" {
				ext = "jpg"
			}
			if validInlineFilenameExt(ext) {
				return "file." + ext
			}
		}
		return "file"
	}
}

func validInlineFilenameExt(ext string) bool {
	if ext == "" || len(ext) > 16 {
		return false
	}
	for _, r := range ext {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func (r *Router) tgBotInlineResults(ctx context.Context, viewerUserID int64, in domain.BotInlineResults) *tg.MessagesBotResults {
	out := &tg.MessagesBotResults{
		Gallery:   in.Gallery,
		QueryID:   in.QueryID,
		Results:   make([]tg.BotInlineResultClass, 0, len(in.Results)),
		CacheTime: in.CacheTime,
	}
	if in.NextOffset != "" {
		out.SetNextOffset(in.NextOffset)
	}
	if in.SwitchPM != nil {
		out.SetSwitchPm(tg.InlineBotSwitchPM{
			Text:       in.SwitchPM.Text,
			StartParam: in.SwitchPM.StartParam,
		})
	}
	if in.SwitchWeb != nil {
		out.SetSwitchWebview(tg.InlineBotWebView{
			Text: in.SwitchWeb.Text,
			URL:  in.SwitchWeb.URL,
		})
	}
	for _, item := range in.Results {
		out.Results = append(out.Results, tgBotInlineResult(item))
	}
	if r.deps.Users != nil && in.BotUserID != 0 {
		if u, found, err := r.deps.Users.ByID(ctx, viewerUserID, in.BotUserID); err == nil && found {
			out.Users = append(out.Users, r.tgUser(u))
		}
	}
	return out
}

func tgBotInlineResult(in domain.BotInlineResult) tg.BotInlineResultClass {
	if in.Media != nil && !in.Media.IsZero() {
		msg := tgBotInlineMediaMessage(in)
		out := &tg.BotInlineResult{
			ID:          in.ID,
			Type:        in.Type,
			Title:       in.Title,
			Description: in.Description,
			SendMessage: msg,
		}
		if in.Media.Kind == domain.MessageMediaKindGeo ||
			in.Media.Kind == domain.MessageMediaKindVenue ||
			in.Media.Kind == domain.MessageMediaKindContact {
			return out
		}
		mediaMsg := &tg.BotInlineMessageMediaAuto{
			Message:  in.Message,
			Entities: tgMessageEntities(in.Entities),
		}
		if markup := tgReplyMarkup(in.ReplyMarkup); markup != nil {
			mediaMsg.SetReplyMarkup(markup)
		}
		mediaOut := &tg.BotInlineMediaResult{
			ID:          in.ID,
			Type:        in.Type,
			Title:       in.Title,
			Description: in.Description,
			SendMessage: mediaMsg,
		}
		if in.Media.Kind == domain.MessageMediaKindPhoto && in.Media.Photo != nil {
			mediaOut.SetPhoto(tgPhoto(*in.Media.Photo))
		}
		if in.Media.Kind == domain.MessageMediaKindDocument && in.Media.Document != nil {
			mediaOut.SetDocument(tgDocument(*in.Media.Document))
		}
		return mediaOut
	}
	if in.MediaAuto {
		msg := &tg.BotInlineMessageMediaAuto{
			Message:  in.Message,
			Entities: tgMessageEntities(in.Entities),
		}
		if markup := tgReplyMarkup(in.ReplyMarkup); markup != nil {
			msg.SetReplyMarkup(markup)
		}
		out := &tg.BotInlineResult{
			ID:          in.ID,
			Type:        in.Type,
			Title:       in.Title,
			Description: in.Description,
			SendMessage: msg,
		}
		if in.URL != "" {
			out.SetURL(in.URL)
		}
		if in.Thumb != nil {
			out.SetThumb(tgInlineWebDocument(*in.Thumb))
		}
		if in.Content != nil {
			out.SetContent(tgInlineWebDocument(*in.Content))
		}
		return out
	}
	msg := &tg.BotInlineMessageText{
		NoWebpage: in.NoWebpage,
		Message:   in.Message,
		Entities:  tgMessageEntities(in.Entities),
	}
	if markup := tgReplyMarkup(in.ReplyMarkup); markup != nil {
		msg.SetReplyMarkup(markup)
	}
	out := &tg.BotInlineResult{
		ID:          in.ID,
		Type:        in.Type,
		Title:       in.Title,
		Description: in.Description,
		SendMessage: msg,
	}
	if in.URL != "" {
		out.SetURL(in.URL)
	}
	if in.Thumb != nil {
		out.SetThumb(tgInlineWebDocument(*in.Thumb))
	}
	if in.Content != nil {
		out.SetContent(tgInlineWebDocument(*in.Content))
	}
	return out
}

func tgInlineWebDocument(in domain.BotInlineWebDocument) tg.WebDocumentClass {
	return &tg.WebDocument{
		URL:        in.URL,
		AccessHash: in.AccessHash,
		Size:       in.Size,
		MimeType:   in.MimeType,
		Attributes: tgDocumentAttributes(in.MimeType, in.Attributes),
	}
}

func tgBotInlineMediaMessage(in domain.BotInlineResult) tg.BotInlineMessageClass {
	switch {
	case in.Media.Kind == domain.MessageMediaKindGeo && in.Media.Geo != nil:
		msg := &tg.BotInlineMessageMediaGeo{Geo: tgGeoPoint(*in.Media.Geo)}
		if markup := tgReplyMarkup(in.ReplyMarkup); markup != nil {
			msg.SetReplyMarkup(markup)
		}
		return msg
	case in.Media.Kind == domain.MessageMediaKindVenue && in.Media.Venue != nil:
		msg := &tg.BotInlineMessageMediaVenue{
			Geo:       tgGeoPoint(in.Media.Venue.Geo),
			Title:     in.Media.Venue.Title,
			Address:   in.Media.Venue.Address,
			Provider:  in.Media.Venue.Provider,
			VenueID:   in.Media.Venue.VenueID,
			VenueType: in.Media.Venue.VenueType,
		}
		if markup := tgReplyMarkup(in.ReplyMarkup); markup != nil {
			msg.SetReplyMarkup(markup)
		}
		return msg
	case in.Media.Kind == domain.MessageMediaKindContact && in.Media.Contact != nil:
		msg := &tg.BotInlineMessageMediaContact{
			PhoneNumber: in.Media.Contact.PhoneNumber,
			FirstName:   in.Media.Contact.FirstName,
			LastName:    in.Media.Contact.LastName,
			Vcard:       in.Media.Contact.Vcard,
		}
		if markup := tgReplyMarkup(in.ReplyMarkup); markup != nil {
			msg.SetReplyMarkup(markup)
		}
		return msg
	default:
		return &tg.BotInlineMessageMediaAuto{
			Message:  in.Message,
			Entities: tgMessageEntities(in.Entities),
		}
	}
}

func tgInlineQueryPeerType(ctx context.Context, r *Router, userID, botID int64, peer domain.Peer) tg.InlineQueryPeerTypeClass {
	switch peer.Type {
	case domain.PeerTypeUser:
		if peer.ID == botID {
			return &tg.InlineQueryPeerTypeSameBotPM{}
		}
		if r.userIsBot(ctx, peer.ID) {
			return &tg.InlineQueryPeerTypeBotPM{}
		}
		return &tg.InlineQueryPeerTypePM{}
	case domain.PeerTypeChannel:
		if r.deps.Channels != nil {
			// 只读 Megagroup 标志：走轻量 ResolveChannel，省 dialog/读态/boost 查询。
			if view, err := r.deps.Channels.ResolveChannel(ctx, userID, peer.ID); err == nil && !view.Channel.Megagroup {
				return &tg.InlineQueryPeerTypeBroadcast{}
			}
		}
		return &tg.InlineQueryPeerTypeMegagroup{}
	default:
		return &tg.InlineQueryPeerTypePM{}
	}
}
