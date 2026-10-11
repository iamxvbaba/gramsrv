package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"telesrv/internal/domain"
)

// MiniAppsConfig is the narrow dependency boundary for the self-hosted
// BotFather and Stickers mini apps. The handlers never reach into PostgreSQL;
// all writes go through the same application services used by MTProto.
type MiniAppsConfig struct {
	AppName  string
	Bots     MiniAppBotManager
	Stickers MiniAppStickerManager
	Tokens   MiniAppBotTokenStore

	// Bot tokens are server-only secrets used to verify Telegram Mini App
	// initData. When empty, the handler falls back to the corresponding system
	// bot row in Tokens. They are never rendered or returned by an API.
	BotFatherToken string
	StickersToken  string
}

type MiniAppBotManager interface {
	CreateBot(ctx context.Context, ownerUserID int64, name, username string) (domain.User, string, error)
	ListOwnedBots(ctx context.Context, ownerUserID int64) ([]domain.User, error)
}

type miniAppBotInfoManager interface {
	SetBotInfo(ctx context.Context, botUserID int64, update domain.BotInfoUpdate) (int, error)
}

type MiniAppStickerManager interface {
	ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error)
	ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error)
	ListCreatedStickerSets(ctx context.Context, userID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error)
	CreateStickerSet(ctx context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error)
}

type MiniAppBotTokenStore interface {
	GetBot(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error)
}

type miniAppUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

var errMiniAppAuth = errors.New("mini app authorization failed")

func (m *MiniApps) authenticate(r *http.Request, botUserID int64) (miniAppUser, error) {
	token, err := m.botToken(r.Context(), botUserID)
	if err != nil {
		return miniAppUser{}, errMiniAppAuth
	}
	raw := strings.TrimSpace(r.Header.Get("X-Telegram-Init-Data"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("tgWebAppData"))
	}
	user, err := verifyMiniAppInitData(raw, token, 15*time.Minute, time.Now().UTC())
	if err != nil {
		return miniAppUser{}, errMiniAppAuth
	}
	return user, nil
}

func (m *MiniApps) botToken(ctx context.Context, botUserID int64) (string, error) {
	configured := m.botFatherToken
	if botUserID == domain.StickersBotUserID {
		configured = m.stickersToken
	}
	if token := validMiniAppToken(configured); token != "" {
		return token, nil
	}
	if m.tokens == nil {
		return "", errors.New("mini app token is not configured")
	}
	profile, found, err := m.tokens.GetBot(ctx, botUserID)
	if err != nil || !found || strings.TrimSpace(profile.TokenSecret) == "" {
		return "", errors.New("mini app token is not configured")
	}
	return domain.FormatBotToken(botUserID, profile.TokenSecret), nil
}

func validMiniAppToken(raw string) string {
	id, secret, ok := domain.ParseBotToken(strings.TrimSpace(raw))
	if !ok || id <= 0 || strings.TrimSpace(secret) == "" {
		return ""
	}
	return domain.FormatBotToken(id, secret)
}

func verifyMiniAppInitData(raw, botToken string, maxAge time.Duration, now time.Time) (miniAppUser, error) {
	if len(raw) == 0 || len(raw) > 16<<10 || strings.TrimSpace(botToken) == "" {
		return miniAppUser{}, errMiniAppAuth
	}
	values, err := url.ParseQuery(raw)
	if err != nil || values.Get("hash") == "" || values.Get("user") == "" ||
		len(values["hash"]) != 1 || len(values["auth_date"]) != 1 || len(values["user"]) != 1 {
		return miniAppUser{}, errMiniAppAuth
	}
	provided, err := hex.DecodeString(values.Get("hash"))
	if err != nil || len(provided) != sha256.Size {
		return miniAppUser{}, errMiniAppAuth
	}
	want := miniAppInitDataHash(values, botToken)
	if !hmac.Equal(provided, want) {
		return miniAppUser{}, errMiniAppAuth
	}
	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || authDate <= 0 {
		return miniAppUser{}, errMiniAppAuth
	}
	authTime := time.Unix(authDate, 0)
	if authTime.After(now.Add(time.Minute)) || (maxAge > 0 && now.Sub(authTime) > maxAge) {
		return miniAppUser{}, errMiniAppAuth
	}
	var user miniAppUser
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 {
		return miniAppUser{}, errMiniAppAuth
	}
	return user, nil
}

func miniAppInitDataHash(values url.Values, botToken string) []byte {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var check strings.Builder
	for i, key := range keys {
		if i > 0 {
			check.WriteByte('\n')
		}
		check.WriteString(key)
		check.WriteByte('=')
		check.WriteString(values.Get(key))
	}
	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secretMAC.Write([]byte(botToken))
	dataMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
	_, _ = dataMAC.Write([]byte(check.String()))
	return dataMAC.Sum(nil)
}

func miniAppSameOrigin(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (m *MiniApps) requireWriteAuth(w http.ResponseWriter, r *http.Request, botID int64) (miniAppUser, bool) {
	if !miniAppSameOrigin(r) {
		writeMiniAPIError(w, http.StatusForbidden, "cross-origin request")
		return miniAppUser{}, false
	}
	user, err := m.authenticate(r, botID)
	if err != nil {
		writeMiniAPIError(w, http.StatusUnauthorized, "Mini App authorization is required")
		return miniAppUser{}, false
	}
	return user, true
}

func (m *MiniApps) listBots(w http.ResponseWriter, r *http.Request) {
	user, ok := m.requireWriteAuth(w, r, domain.BotFatherUserID)
	if !ok {
		return
	}
	if m.bots == nil {
		writeMiniAPIError(w, http.StatusServiceUnavailable, "Bot management is not configured")
		return
	}
	bots, err := m.bots.ListOwnedBots(r.Context(), user.ID)
	if err != nil {
		writeMiniAPIError(w, miniServiceErrorStatus(err), "Unable to load bots")
		return
	}
	views := make([]miniBotView, 0, len(bots))
	for _, bot := range bots {
		views = append(views, miniBotView{ID: bot.ID, Username: bot.Username, Name: bot.FirstName, LastName: bot.LastName})
	}
	writeMiniJSON(w, http.StatusOK, map[string]any{"bots": views})
}

func (m *MiniApps) createBot(w http.ResponseWriter, r *http.Request) {
	user, ok := m.requireWriteAuth(w, r, domain.BotFatherUserID)
	if !ok {
		return
	}
	if m.bots == nil {
		writeMiniAPIError(w, http.StatusServiceUnavailable, "Bot management is not configured")
		return
	}
	var input struct {
		Name     string `json:"name"`
		About    string `json:"about"`
		Username string `json:"username"`
	}
	if !decodeMiniJSON(w, r, 4<<10, &input) {
		return
	}
	input.About = strings.TrimSpace(input.About)
	if utf8.RuneCountInString(input.About) > domain.MaxBotAboutLen {
		writeMiniAPIError(w, http.StatusBadRequest, "about text must be 120 characters or fewer")
		return
	}
	bot, token, err := m.bots.CreateBot(r.Context(), user.ID, input.Name, input.Username)
	if err != nil {
		writeMiniAPIError(w, miniServiceErrorStatus(err), miniServiceErrorMessage(err))
		return
	}
	warnings := make([]string, 0, 1)
	if input.About != "" {
		info, ok := m.bots.(miniAppBotInfoManager)
		if !ok {
			warnings = append(warnings, "Bot was created, but about text is not supported by this Gramsrv configuration.")
		} else if _, err := info.SetBotInfo(r.Context(), bot.ID, domain.BotInfoUpdate{SetAbout: true, About: input.About}); err != nil {
			// Creation has already committed and the token cannot safely be hidden
			// from the caller. Return it once together with an actionable warning.
			warnings = append(warnings, "Bot was created, but its about text could not be saved.")
		} else {
			bot.About = input.About
		}
	}
	// This is the only response that contains the newly generated token. It is
	// deliberately not persisted in a browser session or included in list APIs.
	w.Header().Set("Cache-Control", "no-store")
	response := map[string]any{
		"bot":   miniBotView{ID: bot.ID, Username: bot.Username, Name: bot.FirstName, LastName: bot.LastName},
		"token": token,
	}
	if len(warnings) != 0 {
		response["warnings"] = warnings
	}
	writeMiniJSON(w, http.StatusCreated, response)
}

func (m *MiniApps) listMyStickerSets(w http.ResponseWriter, r *http.Request) {
	user, ok := m.requireWriteAuth(w, r, domain.StickersBotUserID)
	if !ok {
		return
	}
	if m.stickers == nil {
		writeMiniAPIError(w, http.StatusServiceUnavailable, "Sticker management is not configured")
		return
	}
	sets, _, err := m.stickers.ListCreatedStickerSets(r.Context(), user.ID, 0, 100)
	if err != nil {
		writeMiniAPIError(w, miniServiceErrorStatus(err), "Unable to load sticker sets")
		return
	}
	writeMiniJSON(w, http.StatusOK, map[string]any{"sets": miniStickerViews(sets)})
}

type miniStickerCreateRequest struct {
	Title           string                 `json:"title"`
	ShortName       string                 `json:"short_name"`
	Kind            string                 `json:"kind"`
	TextColor       bool                   `json:"text_color"`
	ThumbDocumentID int64                  `json:"thumb_document_id"`
	ThumbAccessHash int64                  `json:"thumb_access_hash"`
	Software        string                 `json:"software"`
	Items           []miniStickerItemInput `json:"items"`
}

type miniStickerItemInput struct {
	DocumentID         int64  `json:"document_id"`
	DocumentAccessHash int64  `json:"document_access_hash"`
	Emoji              string `json:"emoji"`
	Keywords           string `json:"keywords"`
}

func (m *MiniApps) createStickerSet(w http.ResponseWriter, r *http.Request) {
	user, ok := m.requireWriteAuth(w, r, domain.StickersBotUserID)
	if !ok {
		return
	}
	if m.stickers == nil {
		writeMiniAPIError(w, http.StatusServiceUnavailable, "Sticker management is not configured")
		return
	}
	var input miniStickerCreateRequest
	if !decodeMiniJSON(w, r, 64<<10, &input) {
		return
	}
	if len(input.Items) == 0 || len(input.Items) > domain.MaxStickerSetItems {
		writeMiniAPIError(w, http.StatusBadRequest, "a sticker set must contain 1..120 items")
		return
	}
	kind := domain.StickerSetKind(strings.ToLower(strings.TrimSpace(input.Kind)))
	if kind == "" {
		kind = domain.StickerSetKindStickers
	}
	if kind != domain.StickerSetKindStickers && kind != domain.StickerSetKindEmoji && kind != domain.StickerSetKindMasks {
		writeMiniAPIError(w, http.StatusBadRequest, "unsupported sticker set type")
		return
	}
	items := make([]domain.StickerSetItemInput, 0, len(input.Items))
	for _, item := range input.Items {
		if item.DocumentID <= 0 || item.DocumentAccessHash == 0 || strings.TrimSpace(item.Emoji) == "" || utf8.RuneCountInString(item.Keywords) > 1024 {
			writeMiniAPIError(w, http.StatusBadRequest, "invalid sticker document reference")
			return
		}
		items = append(items, domain.StickerSetItemInput{
			DocumentID: item.DocumentID, DocumentAccessHash: item.DocumentAccessHash,
			Emoji: strings.TrimSpace(item.Emoji), Keywords: strings.TrimSpace(item.Keywords),
		})
	}
	if input.ThumbDocumentID < 0 || (input.ThumbDocumentID != 0 && input.ThumbAccessHash == 0) {
		writeMiniAPIError(w, http.StatusBadRequest, "invalid sticker thumbnail reference")
		return
	}
	set, _, err := m.stickers.CreateStickerSet(r.Context(), domain.CreateStickerSetRequest{
		CreatorUserID: user.ID, Title: input.Title, ShortName: input.ShortName, Kind: kind,
		TextColor: input.TextColor, ThumbDocumentID: input.ThumbDocumentID, ThumbAccessHash: input.ThumbAccessHash,
		Items: items, Software: strings.TrimSpace(input.Software), Date: int(time.Now().Unix()),
	})
	if err != nil {
		writeMiniAPIError(w, miniServiceErrorStatus(err), miniServiceErrorMessage(err))
		return
	}
	writeMiniJSON(w, http.StatusCreated, map[string]any{"set": miniStickerViewOf(set)})
}

func (m *MiniApps) listStickerCatalog(w http.ResponseWriter, r *http.Request) {
	if m.stickers == nil {
		writeMiniAPIError(w, http.StatusServiceUnavailable, "Sticker catalog is not configured")
		return
	}
	var all []domain.StickerSet
	for _, kind := range []domain.StickerSetKind{domain.StickerSetKindStickers, domain.StickerSetKindEmoji, domain.StickerSetKindMasks} {
		sets, err := m.stickers.ListStickerSets(r.Context(), kind)
		if err != nil {
			writeMiniAPIError(w, http.StatusServiceUnavailable, "Sticker catalog is unavailable")
			return
		}
		all = append(all, sets...)
		if len(all) >= 100 {
			break
		}
	}
	if len(all) > 100 {
		all = all[:100]
	}
	writeMiniJSON(w, http.StatusOK, miniStickerViews(all))
}

func (m *MiniApps) stickerSet(w http.ResponseWriter, r *http.Request) {
	shortName := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/miniapps/stickers/"))
	if shortName == "" || strings.ContainsAny(shortName, "/?#") || m.stickers == nil {
		http.NotFound(w, r)
		return
	}
	set, _, found, err := m.stickers.ResolveStickerSet(r.Context(), domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: shortName})
	if err != nil {
		writeMiniAPIError(w, http.StatusServiceUnavailable, "Sticker set is unavailable")
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	writeMiniJSON(w, http.StatusOK, miniStickerViewOf(set))
}

type miniBotView struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	LastName string `json:"last_name,omitempty"`
}

type miniStickerView struct {
	ID        int64  `json:"id"`
	ShortName string `json:"short_name"`
	Title     string `json:"title"`
	Count     int    `json:"count"`
	Kind      string `json:"kind"`
}

func miniStickerViewOf(set domain.StickerSet) miniStickerView {
	return miniStickerView{ID: set.ID, ShortName: set.ShortName, Title: set.Title, Count: set.Count, Kind: string(set.Kind)}
}

func miniStickerViews(sets []domain.StickerSet) []miniStickerView {
	out := make([]miniStickerView, 0, len(sets))
	for _, set := range sets {
		out = append(out, miniStickerViewOf(set))
	}
	return out
}

func decodeMiniJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeMiniAPIError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMiniAPIError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

func miniServiceErrorStatus(err error) int {
	switch {
	case errors.Is(err, domain.ErrBotsTooMany):
		return http.StatusTooManyRequests
	case errors.Is(err, domain.ErrUsernameOccupied), errors.Is(err, domain.ErrStickerSetShortNameOccupied):
		return http.StatusConflict
	case errors.Is(err, domain.ErrStickerSetNotOwned):
		return http.StatusForbidden
	case errors.Is(err, domain.ErrBotNameInvalid), errors.Is(err, domain.ErrBotUsernameInvalid),
		errors.Is(err, domain.ErrStickerSetTitleInvalid), errors.Is(err, domain.ErrStickerSetShortNameInvalid),
		errors.Is(err, domain.ErrStickerSetTypeInvalid), errors.Is(err, domain.ErrStickerSetEmpty),
		errors.Is(err, domain.ErrStickerSetTooMuch), errors.Is(err, domain.ErrStickerSetEmojiInvalid),
		errors.Is(err, domain.ErrStickerSetFileInvalid), errors.Is(err, domain.ErrStickerSetInvalid),
		errors.Is(err, domain.ErrStickerSetCreatorInvalid), errors.Is(err, domain.ErrStickerSetPositionInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func miniServiceErrorMessage(err error) string {
	switch {
	case errors.Is(err, domain.ErrBotsTooMany):
		return "bot creation limit reached"
	case errors.Is(err, domain.ErrUsernameOccupied), errors.Is(err, domain.ErrStickerSetShortNameOccupied):
		return "this username or short name is already used"
	case errors.Is(err, domain.ErrStickerSetFileInvalid):
		return "one or more sticker documents are invalid or not accessible"
	case errors.Is(err, domain.ErrStickerSetEmpty):
		return "add at least one sticker"
	default:
		return "request could not be completed"
	}
}
