package botapi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

type BotsService interface {
	BotInfo(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error)
	SetBotMenuButton(ctx context.Context, botUserID int64, button domain.BotMenuButton) (int, error)
	GetBotMenuButton(ctx context.Context, botUserID int64) (domain.BotMenuButton, error)
	BotEmojiStatusPermission(ctx context.Context, botUserID, userID int64) (bool, error)
}

type UsersService interface {
	UpdateEmojiStatus(ctx context.Context, userID int64, status domain.UserEmojiStatus) (domain.User, error)
}

type WebAppService interface {
	AnswerWebAppQueryFromBotAPI(ctx context.Context, botID int64, webAppQueryID string, result domain.BotInlineResult) (inlineMessageID string, err error)
	SavePreparedInlineMessageFromBotAPI(ctx context.Context, botID, userID int64, result domain.BotInlineResult, peerTypes []string) (id string, expireDate int, err error)
}

type GatewayService interface {
	BotAPISelf(ctx context.Context, botID int64) (domain.User, error)
	BotAPIUpdates(ctx context.Context, botID int64, offset int64) ([]domain.UpdateEvent, error)
	BotAPISendMessage(ctx context.Context, botID, chatID int64, text string, entities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview, silent bool, replyToMessageID int) (domain.Message, error)
	BotAPISendMedia(ctx context.Context, botID, chatID int64, kind, locationKey, remoteURL, fileName, mimeType string, fileBytes []byte, caption string, entities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent bool, replyToMessageID int) (domain.Message, error)
	BotAPIEditMessageText(ctx context.Context, botID, chatID int64, messageID int, text string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview bool) (domain.Message, error)
	BotAPIEditInlineMessageText(ctx context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, text string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview bool) (bool, error)
	BotAPIDeleteMessage(ctx context.Context, botID, chatID int64, messageID int) (bool, error)
	BotAPIAnswerCallbackQuery(ctx context.Context, botID int64, callbackQueryID, text, url string, showAlert bool, cacheTime int) (bool, error)
	BotAPIGetFile(ctx context.Context, botID int64, locationKey string, offset int64, limit int) (domain.FileChunk, bool, error)
}

type GatewayUpdateWaiter interface {
	BotAPIUpdateWaitVersion(botID int64) uint64
	WaitBotAPIUpdate(ctx context.Context, botID int64, version uint64, timeout time.Duration) bool
}

type GatewayUpdateControl interface {
	BotAPISetAllowedUpdates(ctx context.Context, botID int64, allowed []domain.BotAPIUpdateKind) error
	BotAPIDropPendingUpdates(ctx context.Context, botID int64) error
	BotAPIPendingUpdateCount(ctx context.Context, botID int64) (int, error)
}

type GatewayPollLease interface {
	AcquireBotAPIPollLease(ctx context.Context, botID int64, owner string, ttl time.Duration) (bool, error)
	ReleaseBotAPIPollLease(ctx context.Context, botID int64, owner string) error
}

type GatewayWebhookControl interface {
	BotAPISetWebhook(ctx context.Context, config domain.BotAPIWebhook, dropPending bool) error
	BotAPIDeleteWebhook(ctx context.Context, botID int64, dropPending bool) error
	BotAPIWebhook(ctx context.Context, botID int64) (domain.BotAPIWebhook, bool, error)
	ListDueBotAPIWebhooks(ctx context.Context, limit int) ([]domain.BotAPIWebhook, error)
	AcquireBotAPIWebhookLease(ctx context.Context, botID int64, owner string, ttl time.Duration) (bool, error)
	ReleaseBotAPIWebhookLease(ctx context.Context, botID int64, owner string) error
	RecordBotAPIWebhookFailure(ctx context.Context, botID int64, owner string, nextAttempt time.Time, message string) error
	RecordBotAPIWebhookSuccess(ctx context.Context, botID int64, owner string, nextAttempt time.Time) error
	ConfirmBotAPIWebhookDelivery(ctx context.Context, botID, updateID int64) error
}

func Start(ctx context.Context, addr string, bots BotsService, users UsersService, webapps WebAppService, gateway GatewayService, logger *zap.Logger) (*http.Server, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	handler := &handler{bots: bots, users: users, webapps: webapps, gateway: gateway, logger: logger, webhookClient: newWebhookHTTPClient()}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		logger.Info("Bot API 网关已启用", zap.String("addr", addr))
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("Bot API 网关退出", zap.Error(err))
		}
	}()
	if webhooks, ok := gateway.(GatewayWebhookControl); ok {
		go runWebhookDispatcher(ctx, webhooks, gateway, handler.webhookClient, logger.Named("webhook"))
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	return srv, nil
}

type handler struct {
	bots          BotsService
	users         UsersService
	webapps       WebAppService
	gateway       GatewayService
	logger        *zap.Logger
	polls         botAPIPollRegistry
	webhookClient *http.Client
}

type botAPIPollRegistry struct {
	mu     sync.Mutex
	active map[int64]struct{}
}

func (p *botAPIPollRegistry) acquire(botID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil {
		p.active = make(map[int64]struct{})
	}
	if _, exists := p.active[botID]; exists {
		return false
	}
	p.active[botID] = struct{}{}
	return true
}

func (p *botAPIPollRegistry) release(botID int64) {
	p.mu.Lock()
	delete(p.active, botID)
	p.mu.Unlock()
}

const (
	maxBotAPIUploadBytes          = 25 << 20
	maxBotAPIRequestOverheadBytes = 1 << 20
	maxBotAPIRequestBytes         = maxBotAPIUploadBytes + maxBotAPIRequestOverheadBytes
	botAPILongPollFallback        = 5 * time.Second
)

type uploadedFile struct {
	Name     string
	MimeType string
	Bytes    []byte
}

func (h *handler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handle)
	return mux
}

func (h *handler) handle(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBotAPIRequestBytes)
	if strings.HasPrefix(r.URL.Path, "/file/bot") {
		h.downloadFile(w, r)
		return
	}
	token, method, ok := splitBotPath(r.URL.Path)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "METHOD_NOT_FOUND")
		return
	}
	botID, ok := h.authenticate(r.Context(), token)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "ACCESS_TOKEN_INVALID")
		return
	}
	switch strings.ToLower(method) {
	case "getme":
		h.getMe(w, r, botID)
	case "getupdates":
		h.getUpdates(w, r, botID)
	case "sendmessage":
		h.sendMessage(w, r, botID)
	case "sendphoto":
		h.sendMedia(w, r, botID, "photo")
	case "senddocument":
		h.sendMedia(w, r, botID, "document")
	case "editmessagetext":
		h.editMessageText(w, r, botID)
	case "deletemessage":
		h.deleteMessage(w, r, botID)
	case "answercallbackquery":
		h.answerCallbackQuery(w, r, botID)
	case "getfile":
		h.getFile(w, r, botID)
	case "deletewebhook":
		h.deleteWebhook(w, r, botID)
	case "getwebhookinfo":
		h.getWebhookInfo(w, r, botID)
	case "setwebhook":
		h.setWebhook(w, r, botID)
	case "setchatmenubutton":
		h.setChatMenuButton(w, r, botID)
	case "getchatmenubutton":
		h.getChatMenuButton(w, r, botID)
	case "setuseremojistatus":
		h.setUserEmojiStatus(w, r, botID)
	case "answerwebappquery":
		h.answerWebAppQuery(w, r, botID)
	case "savepreparedinlinemessage":
		h.savePreparedInlineMessage(w, r, botID)
	case "answershippingquery", "answerprecheckoutquery":
		writeAPIError(w, http.StatusNotImplemented, "BLOCKED_DURABLE_QUERY_STATE_MISSING")
	default:
		writeAPIError(w, http.StatusNotFound, "METHOD_NOT_FOUND")
	}
}

func splitBotPath(path string) (token, method string, ok bool) {
	rest := strings.TrimPrefix(path, "/bot")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return "", "", false
	}
	token, method, found := strings.Cut(rest, "/")
	if !found || token == "" || method == "" {
		return "", "", false
	}
	return token, method, true
}

func splitFilePath(path string) (token, fileID string, ok bool) {
	rest := strings.TrimPrefix(path, "/file/bot")
	rest = strings.TrimPrefix(rest, "/")
	token, fileID, found := strings.Cut(rest, "/")
	if !found || token == "" || fileID == "" || strings.Contains(fileID, "/") {
		return "", "", false
	}
	return token, fileID, true
}

func (h *handler) getMe(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	u, err := h.gateway.BotAPISelf(r.Context(), botID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, apiUser(u))
}

func (h *handler) getUpdates(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	var offset int64
	if raw := strings.TrimSpace(values["offset"]); raw != "" {
		offset, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || offset < -10000 {
			writeAPIError(w, http.StatusBadRequest, "OFFSET_INVALID")
			return
		}
	}
	limit := apiInt(values["limit"], 100)
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	timeoutSeconds := apiInt(values["timeout"], 0)
	if timeoutSeconds < 0 {
		timeoutSeconds = 0
	}
	if timeoutSeconds > 50 {
		timeoutSeconds = 50
	}
	if !h.polls.acquire(botID) {
		writeAPIError(w, http.StatusConflict, "CONFLICT: another getUpdates request is active")
		return
	}
	defer h.polls.release(botID)
	if leases, ok := h.gateway.(GatewayPollLease); ok {
		owner := randomBotAPIOwner()
		leaseTTL := time.Duration(timeoutSeconds)*time.Second + 30*time.Second
		acquired, err := leases.AcquireBotAPIPollLease(r.Context(), botID, owner, leaseTTL)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
		if !acquired {
			writeAPIError(w, http.StatusConflict, "CONFLICT: another getUpdates request is active")
			return
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := leases.ReleaseBotAPIPollLease(releaseCtx, botID, owner); err != nil {
				h.logger.Warn("release bot api poll lease", zap.Int64("bot_user_id", botID), zap.Error(err))
			}
		}()
	}
	if webhooks, ok := h.gateway.(GatewayWebhookControl); ok {
		if _, configured, err := webhooks.BotAPIWebhook(r.Context(), botID); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		} else if configured {
			writeAPIError(w, http.StatusConflict, "CONFLICT: can't use getUpdates method while webhook is active")
			return
		}
	}
	if raw, present := values["allowed_updates"]; present {
		allowed, err := parseAllowedUpdates(raw)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		control, ok := h.gateway.(GatewayUpdateControl)
		if !ok {
			writeAPIError(w, http.StatusNotImplemented, "ALLOWED_UPDATES_UNSUPPORTED")
			return
		}
		if err := control.BotAPISetAllowedUpdates(r.Context(), botID, allowed); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
	}
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	for {
		version := botAPIUpdateWaitVersion(h.gateway, botID)
		events, err := h.gateway.BotAPIUpdates(r.Context(), botID, offset)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
		updates := apiUpdates(events, limit)
		if len(updates) > 0 || timeoutSeconds == 0 || time.Now().After(deadline) {
			writeAPIOK(w, updates)
			return
		}
		waitForBotAPIUpdate(r.Context(), h.gateway, botID, version, time.Until(deadline))
	}
}

func randomBotAPIOwner() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return fmt.Sprintf("%x", raw[:])
	}
	return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
}

func (h *handler) deleteWebhook(w http.ResponseWriter, r *http.Request, botID int64) {
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	control, ok := h.gateway.(GatewayWebhookControl)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "WEBHOOK_UNSUPPORTED")
		return
	}
	leaseOwner := randomBotAPIOwner()
	if _, found, err := control.BotAPIWebhook(r.Context(), botID); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	} else if found {
		acquired, err := control.AcquireBotAPIWebhookLease(r.Context(), botID, leaseOwner, 30*time.Second)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
		if !acquired {
			writeAPIError(w, http.StatusConflict, "CONFLICT: webhook delivery is active")
			return
		}
		defer func() { _ = control.ReleaseBotAPIWebhookLease(context.Background(), botID, leaseOwner) }()
	}
	if err := control.BotAPIDeleteWebhook(r.Context(), botID, apiBool(values["drop_pending_updates"])); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	writeAPIOK(w, true)
}

func (h *handler) getWebhookInfo(w http.ResponseWriter, r *http.Request, botID int64) {
	pending := 0
	if control, ok := h.gateway.(GatewayUpdateControl); ok {
		var err error
		pending, err = control.BotAPIPendingUpdateCount(r.Context(), botID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
	}
	result := map[string]any{"url": "", "has_custom_certificate": false, "pending_update_count": pending}
	if control, ok := h.gateway.(GatewayWebhookControl); ok {
		config, found, err := control.BotAPIWebhook(r.Context(), botID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
		if found {
			result["url"] = config.URL
			result["max_connections"] = config.MaxConnections
			if config.AllowedUpdates != nil {
				allowed := make([]string, 0, len(config.AllowedUpdates))
				for _, kind := range config.AllowedUpdates {
					allowed = append(allowed, string(kind))
				}
				result["allowed_updates"] = allowed
			}
			if config.LastErrorDate > 0 {
				result["last_error_date"] = config.LastErrorDate
				result["last_error_message"] = config.LastErrorMessage
			}
		}
	}
	writeAPIOK(w, result)
}

func botAPIUpdateWaitVersion(gateway GatewayService, botID int64) uint64 {
	waiter, ok := gateway.(GatewayUpdateWaiter)
	if !ok {
		return 0
	}
	return waiter.BotAPIUpdateWaitVersion(botID)
}

func waitForBotAPIUpdate(ctx context.Context, gateway GatewayService, botID int64, version uint64, timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	if timeout > botAPILongPollFallback {
		timeout = botAPILongPollFallback
	}
	if waiter, ok := gateway.(GatewayUpdateWaiter); ok {
		waiter.WaitBotAPIUpdate(ctx, botID, version, timeout)
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (h *handler) sendMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(values["chat_id"]), 10, 64)
	if err != nil || chatID == 0 {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
		return
	}
	text := values["text"]
	if strings.TrimSpace(values["parse_mode"]) != "" {
		writeAPIError(w, http.StatusBadRequest, "ENTITY_PARSE_UNSUPPORTED")
		return
	}
	entities, err := botAPIMessageEntities(values["entities"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var markup *domain.MessageReplyMarkup
	if raw := strings.TrimSpace(values["reply_markup"]); raw != "" {
		markup, err = replyMarkupFromAPI(json.RawMessage(raw))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	replyTo := apiInt(values["reply_to_message_id"], 0)
	msg, err := h.gateway.BotAPISendMessage(r.Context(), botID, chatID, text, entities, markup, apiBool(values["disable_web_page_preview"]), apiBool(values["disable_notification"]), replyTo)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	users := []domain.User(nil)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	writeAPIOK(w, apiMessage(msg, users))
}

func (h *handler) sendMedia(w http.ResponseWriter, r *http.Request, botID int64, kind string) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, files, err := requestValuesWithFiles(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(values["chat_id"]), 10, 64)
	if err != nil || chatID == 0 {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
		return
	}
	if strings.TrimSpace(values["parse_mode"]) != "" {
		writeAPIError(w, http.StatusBadRequest, "ENTITY_PARSE_UNSUPPORTED")
		return
	}
	entities, err := botAPIMessageEntities(values["caption_entities"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var markup *domain.MessageReplyMarkup
	if raw := strings.TrimSpace(values["reply_markup"]); raw != "" {
		markup, err = replyMarkupFromAPI(json.RawMessage(raw))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	locationKey, remoteURL, fileName, mimeType, fileBytes, ok := mediaInput(values[kind], files, kind)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "FILE_ID_INVALID")
		return
	}
	msg, err := h.gateway.BotAPISendMedia(r.Context(), botID, chatID, kind, locationKey, remoteURL, fileName, mimeType, fileBytes, values["caption"], entities, markup, apiBool(values["disable_notification"]), apiInt(values["reply_to_message_id"], 0))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	users := []domain.User(nil)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	writeAPIOK(w, apiMessage(msg, users))
}

func (h *handler) editMessageText(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	rawInlineID := strings.TrimSpace(values["inline_message_id"])
	var chatID int64
	messageID := 0
	if rawInlineID == "" {
		chatID, err = strconv.ParseInt(strings.TrimSpace(values["chat_id"]), 10, 64)
		if err != nil || chatID == 0 {
			writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
			return
		}
		messageID = apiInt(values["message_id"], 0)
		if messageID <= 0 {
			writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
			return
		}
	} else if strings.TrimSpace(values["chat_id"]) != "" || strings.TrimSpace(values["message_id"]) != "" {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_IDENTIFIER_INVALID")
		return
	}
	if strings.TrimSpace(values["parse_mode"]) != "" {
		writeAPIError(w, http.StatusBadRequest, "ENTITY_PARSE_UNSUPPORTED")
		return
	}
	entities, err := botAPIMessageEntities(values["entities"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var markup *domain.MessageReplyMarkup
	_, setReplyMarkup := values["reply_markup"]
	if raw := strings.TrimSpace(values["reply_markup"]); raw != "" {
		markup, err = inlineReplyMarkupFromAPI(json.RawMessage(raw))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if rawInlineID != "" {
		inlineID, err := decodeBotAPIInlineMessageID(rawInlineID)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		ok, err := h.gateway.BotAPIEditInlineMessageText(r.Context(), botID, inlineID, values["text"], entities, setReplyMarkup, markup, apiBool(values["disable_web_page_preview"]))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
			return
		}
		writeAPIOK(w, ok)
		return
	}
	msg, err := h.gateway.BotAPIEditMessageText(r.Context(), botID, chatID, messageID, values["text"], entities, setReplyMarkup, markup, apiBool(values["disable_web_page_preview"]))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	users := []domain.User(nil)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	writeAPIOK(w, apiMessage(msg, users))
}

func (h *handler) deleteMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(values["chat_id"]), 10, 64)
	if err != nil || chatID == 0 {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
		return
	}
	messageID := apiInt(values["message_id"], 0)
	if messageID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return
	}
	ok, err := h.gateway.BotAPIDeleteMessage(r.Context(), botID, chatID, messageID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, ok)
}

func (h *handler) answerCallbackQuery(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	queryID := strings.TrimSpace(values["callback_query_id"])
	if queryID == "" {
		writeAPIError(w, http.StatusBadRequest, "QUERY_ID_INVALID")
		return
	}
	ok, err := h.gateway.BotAPIAnswerCallbackQuery(r.Context(), botID, queryID, values["text"], values["url"], apiBool(values["show_alert"]), apiInt(values["cache_time"], 0))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, ok)
}

func (h *handler) getFile(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	fileID := strings.TrimSpace(values["file_id"])
	locationKey, ok := decodeBotAPIFileID(fileID)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "FILE_ID_INVALID")
		return
	}
	chunk, found, err := h.gateway.BotAPIGetFile(r.Context(), botID, locationKey, 0, 1)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	if !found {
		writeAPIError(w, http.StatusBadRequest, "FILE_ID_INVALID")
		return
	}
	writeAPIOK(w, map[string]any{
		"file_id":        fileID,
		"file_unique_id": fileID,
		"file_size":      chunk.Total,
		"file_path":      fileID,
	})
}

func (h *handler) downloadFile(w http.ResponseWriter, r *http.Request) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	token, fileID, ok := splitFilePath(r.URL.Path)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "FILE_NOT_FOUND")
		return
	}
	botID, ok := h.authenticate(r.Context(), token)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "ACCESS_TOKEN_INVALID")
		return
	}
	locationKey, ok := decodeBotAPIFileID(fileID)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "FILE_ID_INVALID")
		return
	}
	var offset int64
	for {
		chunk, found, err := h.gateway.BotAPIGetFile(r.Context(), botID, locationKey, offset, 1<<20)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
		if !found {
			writeAPIError(w, http.StatusBadRequest, "FILE_ID_INVALID")
			return
		}
		if offset == 0 {
			if chunk.MimeType != "" {
				w.Header().Set("Content-Type", chunk.MimeType)
			}
			if chunk.Total > 0 {
				w.Header().Set("Content-Length", strconv.FormatInt(chunk.Total, 10))
			}
		}
		if len(chunk.Bytes) == 0 {
			return
		}
		if _, err := w.Write(chunk.Bytes); err != nil {
			return
		}
		offset += int64(len(chunk.Bytes))
		if chunk.Total == 0 || offset >= chunk.Total {
			return
		}
	}
}

func (h *handler) setWebhook(w http.ResponseWriter, r *http.Request, botID int64) {
	values, files, err := requestValuesWithFiles(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	control, ok := h.gateway.(GatewayWebhookControl)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "WEBHOOK_UNSUPPORTED")
		return
	}
	rawURL := strings.TrimSpace(values["url"])
	if rawURL == "" {
		if err := control.BotAPIDeleteWebhook(r.Context(), botID, apiBool(values["drop_pending_updates"])); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
		writeAPIOK(w, true)
		return
	}
	if err := validateWebhookURL(rawURL); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(values["certificate"]) != "" || len(files) != 0 {
		writeAPIError(w, http.StatusBadRequest, "CERTIFICATE_PINNING_UNSUPPORTED")
		return
	}
	if strings.TrimSpace(values["ip_address"]) != "" {
		writeAPIError(w, http.StatusBadRequest, "IP_ADDRESS_UNSUPPORTED")
		return
	}
	secret := strings.TrimSpace(values["secret_token"])
	if !validWebhookSecret(secret) {
		writeAPIError(w, http.StatusBadRequest, "SECRET_TOKEN_INVALID")
		return
	}
	maxConnections := apiInt(values["max_connections"], 40)
	if maxConnections < 1 || maxConnections > 100 {
		writeAPIError(w, http.StatusBadRequest, "MAX_CONNECTIONS_INVALID")
		return
	}
	var allowed []domain.BotAPIUpdateKind
	_, allowedUpdatesSet := values["allowed_updates"]
	if raw, present := values["allowed_updates"]; present {
		allowed, err = parseAllowedUpdates(raw)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(allowed) == 0 {
			allowed = nil
		}
	}
	if !h.polls.acquire(botID) {
		writeAPIError(w, http.StatusConflict, "CONFLICT: another getUpdates request is active")
		return
	}
	defer h.polls.release(botID)
	if leases, ok := h.gateway.(GatewayPollLease); ok {
		owner := randomBotAPIOwner()
		acquired, err := leases.AcquireBotAPIPollLease(r.Context(), botID, owner, 30*time.Second)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
		if !acquired {
			writeAPIError(w, http.StatusConflict, "CONFLICT: another getUpdates request is active")
			return
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = leases.ReleaseBotAPIPollLease(releaseCtx, botID, owner)
		}()
	}
	webhookOwner := randomBotAPIOwner()
	if _, found, err := control.BotAPIWebhook(r.Context(), botID); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	} else if found {
		acquired, err := control.AcquireBotAPIWebhookLease(r.Context(), botID, webhookOwner, 30*time.Second)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
			return
		}
		if !acquired {
			writeAPIError(w, http.StatusConflict, "CONFLICT: webhook delivery is active")
			return
		}
		defer func() { _ = control.ReleaseBotAPIWebhookLease(context.Background(), botID, webhookOwner) }()
	}
	if err := control.BotAPISetWebhook(r.Context(), domain.BotAPIWebhook{
		BotUserID: botID, URL: rawURL, SecretToken: secret,
		MaxConnections: maxConnections, AllowedUpdates: allowed, AllowedUpdatesSet: allowedUpdatesSet,
	}, apiBool(values["drop_pending_updates"])); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	writeAPIOK(w, true)
}

func validateWebhookURL(raw string) error {
	if len(raw) > 2048 {
		return errors.New("WEBHOOK_URL_INVALID")
	}
	u, err := neturl.ParseRequestURI(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("WEBHOOK_URL_INVALID")
	}
	if port := u.Port(); port != "" && port != "443" && port != "80" && port != "88" && port != "8443" {
		return errors.New("WEBHOOK_PORT_NOT_ALLOWED")
	}
	return nil
}

func validWebhookSecret(secret string) bool {
	if secret == "" {
		return true
	}
	if len(secret) > 256 {
		return false
	}
	for _, r := range secret {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (h *handler) authenticate(ctx context.Context, token string) (int64, bool) {
	if h.bots == nil {
		return 0, false
	}
	botID, secret, ok := domain.ParseBotToken(token)
	if !ok {
		return 0, false
	}
	profile, found, err := h.bots.BotInfo(ctx, botID)
	if err != nil || !found || profile.TokenSecret != secret {
		return 0, false
	}
	return botID, true
}

func (h *handler) setChatMenuButton(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.bots == nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	button, err := menuButtonFromAPI(values["menu_button"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BUTTON_INVALID")
		return
	}
	if _, err := h.bots.SetBotMenuButton(r.Context(), botID, button); err != nil {
		writeAPIError(w, http.StatusBadRequest, "BUTTON_INVALID")
		return
	}
	writeAPIOK(w, true)
}

func (h *handler) getChatMenuButton(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.bots == nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	button, err := h.bots.GetBotMenuButton(r.Context(), botID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	writeAPIOK(w, apiMenuButton(button))
}

func (h *handler) setUserEmojiStatus(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.bots == nil || h.users == nil {
		writeAPIError(w, http.StatusNotImplemented, "BLOCKED_USER_EMOJI_STATUS_SERVICE_MISSING")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	userID, err := strconv.ParseInt(values["user_id"], 10, 64)
	if err != nil || userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "USER_ID_INVALID")
		return
	}
	allowed, err := h.bots.BotEmojiStatusPermission(r.Context(), botID, userID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	if !allowed {
		writeAPIError(w, http.StatusForbidden, "USER_PERMISSION_DENIED")
		return
	}
	var documentID int64
	if raw := strings.TrimSpace(values["emoji_status_custom_emoji_id"]); raw != "" {
		documentID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || documentID < 0 {
			writeAPIError(w, http.StatusBadRequest, "EMOJI_STATUS_INVALID")
			return
		}
	}
	var until int
	if raw := strings.TrimSpace(values["emoji_status_expiration_date"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeAPIError(w, http.StatusBadRequest, "EMOJI_STATUS_INVALID")
			return
		}
		until = n
	}
	if _, err := h.users.UpdateEmojiStatus(r.Context(), userID, domain.UserEmojiStatus{DocumentID: documentID, Until: until}); err != nil {
		if errors.Is(err, domain.ErrPremiumRequired) {
			writeAPIError(w, http.StatusBadRequest, "PREMIUM_ACCOUNT_REQUIRED")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	writeAPIOK(w, true)
}

func (h *handler) answerWebAppQuery(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.webapps == nil {
		writeAPIError(w, http.StatusNotImplemented, "BLOCKED_WEBAPP_QUERY_SERVICE_MISSING")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	queryID := strings.TrimSpace(values["web_app_query_id"])
	if queryID == "" {
		writeAPIError(w, http.StatusBadRequest, "QUERY_ID_INVALID")
		return
	}
	result, err := inlineResultFromAPI(values["result"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	inlineID, err := h.webapps.AnswerWebAppQueryFromBotAPI(r.Context(), botID, queryID, result)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	resp := map[string]any{}
	if inlineID != "" {
		resp["inline_message_id"] = inlineID
	}
	writeAPIOK(w, resp)
}

func (h *handler) savePreparedInlineMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.webapps == nil {
		writeAPIError(w, http.StatusNotImplemented, "BLOCKED_PREPARED_INLINE_SERVICE_MISSING")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	userID, err := strconv.ParseInt(values["user_id"], 10, 64)
	if err != nil || userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "USER_ID_INVALID")
		return
	}
	result, err := inlineResultFromAPI(values["result"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	peerTypes := preparedPeerTypesFromAPI(values)
	id, expireDate, err := h.webapps.SavePreparedInlineMessageFromBotAPI(r.Context(), botID, userID, result, peerTypes)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, map[string]any{"id": id, "expiration_date": expireDate})
}

func requestValues(r *http.Request) (map[string]string, error) {
	values, _, err := requestValuesWithFiles(r)
	return values, err
}

func requestValuesWithFiles(r *http.Request) (map[string]string, map[string]uploadedFile, error) {
	out := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			return nil, nil, err
		}
		for k, v := range body {
			switch x := v.(type) {
			case string:
				out[k] = x
			default:
				b, _ := json.Marshal(x)
				out[k] = string(b)
			}
		}
		if _, nested := out["menu_button"]; !nested {
			if _, direct := body["type"]; direct {
				b, _ := json.Marshal(body)
				out["menu_button"] = string(b)
			}
		}
		return out, nil, nil
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(maxBotAPIUploadBytes); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "request body too large") {
				return nil, nil, errors.New("FILE_TOO_BIG")
			}
			return nil, nil, errors.New("FILE_ID_INVALID")
		}
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				out[k] = v[0]
			}
		}
		files := map[string]uploadedFile{}
		for field, headers := range r.MultipartForm.File {
			if len(headers) == 0 {
				continue
			}
			header := headers[0]
			file, err := header.Open()
			if err != nil {
				return nil, nil, errors.New("FILE_ID_INVALID")
			}
			data, readErr := io.ReadAll(io.LimitReader(file, maxBotAPIUploadBytes+1))
			closeErr := file.Close()
			if readErr != nil {
				return nil, nil, errors.New("FILE_ID_INVALID")
			}
			if closeErr != nil {
				return nil, nil, errors.New("FILE_ID_INVALID")
			}
			if len(data) > maxBotAPIUploadBytes {
				return nil, nil, errors.New("FILE_TOO_BIG")
			}
			files[field] = uploadedFile{
				Name:     header.Filename,
				MimeType: header.Header.Get("Content-Type"),
				Bytes:    data,
			}
		}
		return out, files, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, nil, err
	}
	for k, v := range r.Form {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out, nil, nil
}

func mediaInput(raw string, files map[string]uploadedFile, defaultField string) (locationKey, remoteURL, fileName, mimeType string, fileBytes []byte, ok bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "attach://") {
		field := strings.TrimPrefix(raw, "attach://")
		if file, found := files[field]; found && len(file.Bytes) > 0 {
			return "", "", file.Name, file.MimeType, file.Bytes, true
		}
		return "", "", "", "", nil, false
	}
	if file, found := files[defaultField]; found && len(file.Bytes) > 0 {
		return "", "", file.Name, file.MimeType, file.Bytes, true
	}
	if strings.HasPrefix(strings.ToLower(raw), "http://") || strings.HasPrefix(strings.ToLower(raw), "https://") {
		return "", raw, "", "", nil, true
	}
	if key, decoded := decodeBotAPIFileID(raw); decoded {
		return key, "", "", "", nil, true
	}
	return "", "", "", "", nil, false
}

func menuButtonFromAPI(raw string) (domain.BotMenuButton, error) {
	if strings.TrimSpace(raw) == "" {
		return domain.BotMenuButton{Type: domain.BotMenuButtonDefault}, nil
	}
	var payload struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		WebApp struct {
			URL string `json:"url"`
		} `json:"web_app"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return domain.BotMenuButton{}, err
	}
	switch payload.Type {
	case "default":
		return domain.BotMenuButton{Type: domain.BotMenuButtonDefault}, nil
	case "commands":
		return domain.BotMenuButton{Type: domain.BotMenuButtonCommands}, nil
	case "web_app":
		return domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: payload.Text, URL: payload.WebApp.URL}, nil
	default:
		return domain.BotMenuButton{}, fmt.Errorf("unknown menu button type")
	}
}

func apiMenuButton(button domain.BotMenuButton) map[string]any {
	switch button.Type {
	case domain.BotMenuButtonCommands:
		return map[string]any{"type": "commands"}
	case domain.BotMenuButtonWebView:
		return map[string]any{
			"type": "web_app",
			"text": button.Text,
			"web_app": map[string]any{
				"url": button.URL,
			},
		}
	default:
		return map[string]any{"type": "default"}
	}
}

func writeAPIOK(w http.ResponseWriter, result any) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func writeAPIError(w http.ResponseWriter, status int, description string) {
	writeJSON(w, status, map[string]any{"ok": false, "error_code": status, "description": description})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func apiErrorDescription(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToUpper(err.Error())
	for _, marker := range []string{
		"QUERY_ID_INVALID",
		"USER_ID_INVALID",
		"RESULT_ID_INVALID",
		"RESULT_ID_EMPTY",
		"RESULT_TYPE_INVALID",
		"MESSAGE_EMPTY",
		"MESSAGE_TOO_LONG",
		"BUTTON_INVALID",
		"BUTTON_DATA_INVALID",
		"BUTTON_URL_INVALID",
		"BOT_INVALID",
		"CHAT_ID_INVALID",
		"ENTITY_INVALID",
		"ENTITY_PARSE_UNSUPPORTED",
		"ENTITIES_TOO_LONG",
		"ENTITY_BOUNDS_INVALID",
		"ENTITY_TYPE_UNSUPPORTED",
		"FILE_ID_INVALID",
		"FILE_TOO_BIG",
		"MEDIA_INVALID",
		"USER_BOT_REQUIRED",
		"QUERY_ID_INVALID",
		"MESSAGE_ID_INVALID",
		"MESSAGE_NOT_MODIFIED",
		"CHAT_WRITE_FORBIDDEN",
		"CHAT_ADMIN_REQUIRED",
		"REPLY_MESSAGE_ID_INVALID",
		"WEBHOOK_NOT_IMPLEMENTED",
	} {
		if strings.Contains(text, marker) {
			return marker
		}
	}
	return "BAD_REQUEST"
}

func randomNonZeroInt64() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UnixNano()
	}
	v := int64(binary.LittleEndian.Uint64(b[:]) & 0x7fffffffffffffff)
	if v == 0 {
		return time.Now().UnixNano()
	}
	return v
}
