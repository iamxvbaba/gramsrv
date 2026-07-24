// Package web serves telesrv's read-only public link landing pages.
package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/links"
)

type Config struct {
	Addr              string
	PublicBaseURL     string
	AppScheme         string
	AppLinkBase       string
	WebBaseURL        string
	AppName           string
	StickerSets       StickerSetResolver
	Users             UsernameResolver
	Channels          PublicChannelResolver
	Privacy           AnonymousPrivacyResolver
	Photos            ProfilePhotoResolver
	UniqueGifts       UniqueStarGiftResolver
	GiftWithdrawals   StarGiftWithdrawalResolver
	ModerationAppeals ModerationAppealResolver
	// TelegramLogin is the optional OIDC/Login HTTP adapter. Public Web owns
	// the listener so discovery/auth/token and public links share the exact
	// externally registered origin behind one reverse proxy.
	TelegramLogin http.Handler
}

type StickerSetResolver interface {
	ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error)
}

type UsernameResolver interface {
	ByUsername(ctx context.Context, username string) (domain.User, bool, error)
}

// PublicChannelResolver exposes only the viewer-independent public username
// projection. viewerUserID is always zero for this anonymous Web endpoint.
type PublicChannelResolver interface {
	ResolvePublicChannelUsername(ctx context.Context, viewerUserID int64, username string) (domain.Channel, bool, error)
}

type AnonymousPrivacyResolver interface {
	CanSeeAnonymous(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (bool, error)
}

type ProfilePhotoResolver interface {
	CurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (domain.Photo, bool, error)
	GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error)
	GetFile(ctx context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error)
}

type UniqueStarGiftResolver interface {
	UniqueBySlug(ctx context.Context, slug string) (domain.UniqueStarGift, bool, error)
}

type StarGiftWithdrawalResolver interface {
	ResolveWithdrawal(ctx context.Context, providerRequestID string) (domain.StarGiftWithdrawal, bool, error)
	CompleteWithdrawal(ctx context.Context, providerRequestID string, date int) (domain.StarGiftWithdrawal, error)
}

type ModerationAppealResolver interface {
	ResolveAppealLink(ctx context.Context, token string, now time.Time) (domain.ModerationAppealLink, bool, error)
	Appeal(ctx context.Context, appealID int64) (domain.ModerationAppeal, bool, error)
	SubmitAppealLink(ctx context.Context, token, text string, now time.Time) (domain.ModerationAppeal, bool, error)
}

func Start(ctx context.Context, cfg Config, logger *zap.Logger) (*http.Server, error) {
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		return nil, nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	handler, err := newHandler(cfg, logger)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		logger.Info("Public link Web endpoint enabled",
			zap.String("addr", addr),
			zap.String("public_base_url", cfg.PublicBaseURL),
			zap.String("app_scheme", cfg.AppScheme),
			zap.String("app_link_base", cfg.AppLinkBase),
			zap.String("web_base_url", cfg.WebBaseURL))
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("Public link Web endpoint exited", zap.Error(err))
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	return srv, nil
}

func NewHandler(cfg Config) (http.Handler, error) {
	return newHandler(cfg, zap.NewNop())
}

func newHandler(cfg Config, logger *zap.Logger) (http.Handler, error) {
	var err error
	if cfg.StickerSets == nil {
		return nil, fmt.Errorf("public Web sticker set resolver is nil")
	}
	if strings.TrimSpace(cfg.WebBaseURL) == "" {
		cfg.WebBaseURL = links.DefaultWebBaseURL
	}
	if strings.TrimSpace(cfg.AppName) == "" {
		cfg.AppName = links.DefaultAppName
	}
	if cfg.PublicBaseURL, err = links.ValidateBaseURL(cfg.PublicBaseURL); err != nil {
		return nil, fmt.Errorf("public base URL: %w", err)
	}
	appLinks, err := links.NewAppLinkBuilder(cfg.AppScheme, cfg.AppLinkBase)
	if err != nil {
		return nil, fmt.Errorf("app links: %w", err)
	}
	if cfg.WebBaseURL, err = links.ValidateBaseURL(cfg.WebBaseURL); err != nil {
		return nil, fmt.Errorf("Web base URL: %w", err)
	}
	if cfg.AppName, err = links.ValidateAppName(cfg.AppName); err != nil {
		return nil, fmt.Errorf("app name: %w", err)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	h := &handler{
		stickerSets:     cfg.StickerSets,
		users:           cfg.Users,
		channels:        cfg.Channels,
		privacy:         cfg.Privacy,
		photos:          cfg.Photos,
		uniqueGifts:     cfg.UniqueGifts,
		giftWithdrawals: cfg.GiftWithdrawals,
		appeals:         cfg.ModerationAppeals,
		publicBaseURL:   cfg.PublicBaseURL,
		appLinks:        appLinks,
		webBaseURL:      cfg.WebBaseURL,
		appName:         cfg.AppName,
		logger:          logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /_public/avatar/{username}/{photoID}", h.publicAvatar)
	mux.HandleFunc("GET /addstickers/{shortName}", h.addStickers)
	mux.HandleFunc("GET /addemoji/{shortName}", h.addEmoji)
	mux.HandleFunc("GET /addlist/{slug}", h.addList)
	mux.HandleFunc("GET /nft/{slug}", h.uniqueGift)
	mux.HandleFunc("GET /nft/{slug}/{$}", h.uniqueGift)
	mux.HandleFunc("GET /gift-withdrawal/{requestID}", h.starGiftWithdrawal)
	mux.HandleFunc("POST /gift-withdrawal/{requestID}", h.completeStarGiftWithdrawal)
	if cfg.ModerationAppeals != nil {
		mux.HandleFunc("GET /appeal/{token}", h.moderationAppeal)
		mux.HandleFunc("POST /appeal/{token}", h.moderationAppeal)
	}
	if cfg.TelegramLogin != nil {
		mux.Handle("GET /.well-known/openid-configuration", cfg.TelegramLogin)
		mux.Handle("GET /.well-known/jwks.json", cfg.TelegramLogin)
		mux.Handle("GET /auth", cfg.TelegramLogin)
		mux.Handle("GET /crossapp", cfg.TelegramLogin)
		mux.Handle("GET /inapp", cfg.TelegramLogin)
		mux.Handle("POST /auth/status", cfg.TelegramLogin)
		mux.Handle("POST /token", cfg.TelegramLogin)
		mux.Handle("GET /telegram-login.js", cfg.TelegramLogin)
		mux.Handle("GET /js/telegram-login.js", cfg.TelegramLogin)
	}
	mux.HandleFunc("GET /{username}", h.usernameLink)
	mux.HandleFunc("GET /{username}/{$}", h.usernameLink)
	return publicSecurityHeaders(mux), nil
}

type handler struct {
	stickerSets     StickerSetResolver
	users           UsernameResolver
	channels        PublicChannelResolver
	privacy         AnonymousPrivacyResolver
	photos          ProfilePhotoResolver
	uniqueGifts     UniqueStarGiftResolver
	giftWithdrawals StarGiftWithdrawalResolver
	appeals         ModerationAppealResolver
	publicBaseURL   string
	appLinks        links.AppLinkBuilder
	webBaseURL      string
	appName         string
	logger          *zap.Logger
}

type moderationAppealPage struct {
	AppName      string
	CaseID       int64
	ExpiresAt    string
	Submitted    bool
	AppealID     int64
	AppealStatus domain.ModerationAppealStatus
	Error        string
	AppealText   string
	CanSubmit    bool
}

var moderationAppealTemplate = template.Must(template.New("moderation-appeal").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer"><title>Moderation appeal · {{.AppName}}</title><style>
body{font:16px/1.5 system-ui,sans-serif;background:#f4f6f8;color:#17212b;margin:0;padding:24px}.card{max-width:620px;margin:6vh auto;background:#fff;border-radius:16px;padding:28px;box-shadow:0 8px 32px #0002}h1{margin-top:0}textarea{box-sizing:border-box;width:100%;min-height:160px;padding:12px;border:1px solid #ccd4dc;border-radius:10px;font:inherit}button{border:0;border-radius:10px;padding:12px 18px;background:#2481cc;color:#fff;font-weight:600;cursor:pointer}.meta{color:#53606d}.error{color:#b42318}.done{color:#18864b;font-weight:600}
</style></head><body><main class="card"><h1>Moderation appeal</h1><p class="meta">Case #{{.CaseID}} · link expires {{.ExpiresAt}}</p>
{{if .Submitted}}{{if eq .AppealStatus "granted"}}<p class="done">Your appeal #{{.AppealID}} was granted. Reversible restrictions are being removed through the audited action queue.</p>
{{else if eq .AppealStatus "rejected"}}<p class="error">Your appeal #{{.AppealID}} was not granted.</p>
{{else}}<p class="done">Your appeal #{{.AppealID}} has been submitted. It will be reviewed by a separate moderation step.</p>{{end}}
{{else}}{{if .Error}}<p class="error">{{.Error}}</p>{{end}}{{if .CanSubmit}}<p>Explain why the moderation decision should be reviewed. Do not include passwords, login codes, or payment details.</p><form method="post"><textarea name="appeal_text" maxlength="4000" required>{{.AppealText}}</textarea><p><button type="submit">Submit appeal</button></p></form>{{end}}{{end}}
</main></body></html>`))

func (h *handler) moderationAppeal(w http.ResponseWriter, r *http.Request) {
	if h.appeals == nil {
		http.NotFound(w, r)
		return
	}
	token := r.PathValue("token")
	now := time.Now().UTC()
	link, found, err := h.appeals.ResolveAppealLink(r.Context(), token, now)
	if err != nil || !found {
		http.NotFound(w, r)
		return
	}
	page := moderationAppealPage{
		AppName: h.appName, CaseID: link.CaseID,
		ExpiresAt: link.ExpiresAt.UTC().Format(time.RFC3339),
		Submitted: link.AppealID > 0, AppealID: link.AppealID,
		CanSubmit: link.AppealID == 0,
	}
	if link.AppealID > 0 {
		appeal, appealFound, appealErr := h.appeals.Appeal(r.Context(), link.AppealID)
		if appealErr != nil || !appealFound || appeal.CaseID != link.CaseID {
			h.logger.Warn("resolve linked moderation appeal status failed",
				zap.Int64("case_id", link.CaseID),
				zap.Int64("appeal_id", link.AppealID),
				zap.Error(appealErr))
			http.Error(w, "The appeal status is temporarily unavailable.", http.StatusServiceUnavailable)
			return
		}
		page.AppealStatus = appeal.Status
	}
	status := http.StatusOK
	if r.Method == http.MethodPost && !page.Submitted {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			page.Error = "The appeal form is too large or invalid."
			status = http.StatusBadRequest
		} else {
			page.AppealText = strings.TrimSpace(r.FormValue("appeal_text"))
			appeal, _, submitErr := h.appeals.SubmitAppealLink(
				r.Context(), token, page.AppealText, now,
			)
			switch {
			case submitErr == nil:
				page.Submitted = true
				page.CanSubmit = false
				page.AppealID = appeal.ID
				page.AppealStatus = appeal.Status
				page.AppealText = ""
			case errors.Is(submitErr, domain.ErrModerationCaseConflict):
				page.Error = "The moderation action is still being finalized. Please retry shortly."
				status = http.StatusConflict
			case errors.Is(submitErr, domain.ErrModerationCaseInvalid):
				page.Error = "The appeal text is invalid."
				status = http.StatusBadRequest
			default:
				h.logger.Warn("moderation appeal submission failed",
					zap.Int64("case_id", link.CaseID),
					zap.Error(submitErr))
				page.Error = "The appeal could not be submitted. Please retry."
				status = http.StatusInternalServerError
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	if err := moderationAppealTemplate.Execute(w, page); err != nil {
		h.logger.Warn("render moderation appeal page failed",
			zap.Int64("case_id", link.CaseID), zap.Error(err))
	}
}

type starGiftWithdrawalPage struct {
	AppName      string
	Title        string
	Slug         string
	Status       string
	OwnerAddress string
	GiftAddress  string
	ExpiresAt    string
	CanComplete  bool
}

var starGiftWithdrawalTemplate = template.Must(template.New("star-gift-withdrawal").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · {{.AppName}}</title><style>
body{font:16px/1.5 system-ui,sans-serif;background:#f4f6f8;color:#17212b;margin:0;padding:32px}.card{max-width:560px;margin:8vh auto;background:#fff;border-radius:16px;padding:28px;box-shadow:0 8px 32px #0002}h1{margin-top:0}.meta{overflow-wrap:anywhere;color:#53606d}button{border:0;border-radius:10px;padding:12px 18px;background:#2481cc;color:#fff;font-weight:600;cursor:pointer}.done{color:#18864b;font-weight:600}
</style></head><body><main class="card"><h1>{{.Title}}</h1><p class="meta">Collectible: {{.Slug}}</p>
{{if .CanComplete}}<p>This export is handled only by {{.AppName}}'s internal ledger. No external blockchain or wallet is contacted.</p><form method="post"><button type="submit">Complete local export</button></form><p class="meta">Expires: {{.ExpiresAt}}</p>{{else}}<p class="done">Status: {{.Status}}</p>{{if .OwnerAddress}}<p class="meta">Owner address: {{.OwnerAddress}}</p><p class="meta">Gift address: {{.GiftAddress}}</p>{{end}}{{end}}
</main></body></html>`))

func (h *handler) starGiftWithdrawal(w http.ResponseWriter, r *http.Request) {
	h.renderStarGiftWithdrawal(w, r, false)
}

func (h *handler) completeStarGiftWithdrawal(w http.ResponseWriter, r *http.Request) {
	h.renderStarGiftWithdrawal(w, r, true)
}

func (h *handler) renderStarGiftWithdrawal(w http.ResponseWriter, r *http.Request, complete bool) {
	requestID := strings.TrimSpace(r.PathValue("requestID"))
	if h.giftWithdrawals == nil || requestID == "" || len(requestID) > 256 {
		http.NotFound(w, r)
		return
	}
	var withdrawal domain.StarGiftWithdrawal
	var found bool
	var err error
	if complete {
		withdrawal, err = h.giftWithdrawals.CompleteWithdrawal(r.Context(), requestID, int(time.Now().Unix()))
		found = err == nil
	} else {
		withdrawal, found, err = h.giftWithdrawals.ResolveWithdrawal(r.Context(), requestID)
	}
	if err != nil || !found {
		http.NotFound(w, r)
		return
	}
	page := starGiftWithdrawalPage{AppName: h.appName, Title: withdrawal.Gift.Title, Slug: withdrawal.Gift.Slug,
		Status: withdrawal.Status, OwnerAddress: withdrawal.Gift.OwnerAddress, GiftAddress: withdrawal.Gift.GiftAddress,
		ExpiresAt:   time.Unix(int64(withdrawal.ExpiresAt), 0).UTC().Format(time.RFC3339),
		CanComplete: withdrawal.Status == "pending" && withdrawal.ExpiresAt > int(time.Now().Unix())}
	if page.Title == "" {
		page.Title = "Collectible gift export"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := starGiftWithdrawalTemplate.Execute(w, page); err != nil {
		h.logger.Warn("render star gift withdrawal", zap.Error(err))
	}
}

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (h *handler) addStickers(w http.ResponseWriter, r *http.Request) {
	h.serveSet(w, r, "addstickers")
}

func (h *handler) addEmoji(w http.ResponseWriter, r *http.Request) {
	h.serveSet(w, r, "addemoji")
}

func (h *handler) addList(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.PathValue("slug"))
	if !validSlugPath(slug) {
		http.NotFound(w, r)
		return
	}
	app := h.appURL("addlist", "slug", slug)
	data := pageData{
		AppName:      h.appName,
		Title:        "Shared Folder",
		KindLabel:    "shared folder",
		Subtitle:     slug,
		Description:  "This page opens the app so you can preview and add this shared folder.",
		CanonicalURL: h.publicURL("addlist", slug),
		AppURL:       template.URL(app),
		LegacyTgURL:  template.URL(legacyTgURL("addlist", "slug", slug)),
	}
	data.AppURLJS = template.JS(strconv.Quote(app))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	if err := landingTemplate.Execute(w, data); err != nil {
		http.Error(w, "render shared folder page failed", http.StatusInternalServerError)
	}
}

func (h *handler) uniqueGift(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if h.uniqueGifts == nil || !validStarGiftSlugPath(slug) {
		http.NotFound(w, r)
		return
	}
	unique, found, err := h.uniqueGifts.UniqueBySlug(r.Context(), slug)
	if err != nil {
		h.logger.Error("Public unique star gift lookup failed", zap.String("slug", slug), zap.Error(err))
		http.Error(w, "collectible gift lookup failed", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	canonicalSlug := unique.Slug
	if unique.ID <= 0 || unique.GiftID <= 0 || unique.Num <= 0 ||
		!validStarGiftSlugPath(canonicalSlug) || !strings.EqualFold(slug, canonicalSlug) ||
		!utf8.ValidString(unique.Title) || utf8.RuneCountInString(unique.Title) > domain.MaxStarGiftTitleRunes {
		h.logger.Error("Public unique star gift resolver returned invalid aggregate",
			zap.String("requested_slug", slug), zap.String("resolved_slug", canonicalSlug),
			zap.Int64("unique_id", unique.ID), zap.Int64("gift_id", unique.GiftID), zap.Int("num", unique.Num))
		http.Error(w, "collectible gift lookup failed", http.StatusInternalServerError)
		return
	}
	if slug != canonicalSlug || strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, h.publicURL("nft", canonicalSlug), http.StatusPermanentRedirect)
		return
	}
	title := strings.TrimSpace(unique.Title)
	if title == "" {
		title = "Collectible gift"
	}
	subtitle := fmt.Sprintf("Collectible #%d", unique.Num)
	if unique.AvailabilityIssued > 0 && unique.AvailabilityTotal >= unique.AvailabilityIssued {
		subtitle += fmt.Sprintf(" · %s/%s issued", groupedDecimal(unique.AvailabilityIssued), groupedDecimal(unique.AvailabilityTotal))
	}
	app := h.appURL("nft", "slug", canonicalSlug)
	data := pageData{
		AppName:      h.appName,
		Title:        title,
		KindLabel:    "collectible gift",
		Subtitle:     subtitle,
		Description:  "This collectible was created from a gift on " + h.appName + ". Open it in the app to view its current details.",
		CanonicalURL: h.publicURL("nft", canonicalSlug),
		AppURL:       template.URL(app),
		LegacyTgURL:  template.URL(legacyTgURL("nft", "slug", canonicalSlug)),
	}
	data.AppURLJS = template.JS(strconv.Quote(app))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	if err := landingTemplate.Execute(w, data); err != nil {
		h.logger.Error("Render public unique star gift page failed", zap.String("slug", canonicalSlug), zap.Error(err))
	}
}

func (h *handler) usernameLink(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.PathValue("username"))
	if !validUsernamePath(username) {
		h.serveUsernameNotFound(w, username)
		return
	}
	params, ok := publicResolveQuery(r.URL.RawQuery)
	if !ok {
		http.Error(w, "public link query is too large or invalid", http.StatusRequestURITooLong)
		return
	}
	peer, found, err := h.resolvePublicPeer(r.Context(), username)
	if err != nil {
		h.logger.Error("Public username lookup failed", zap.String("username", username), zap.Error(err))
		http.Error(w, "username lookup failed", http.StatusInternalServerError)
		return
	}
	if !found {
		h.serveUsernameNotFound(w, username)
		return
	}
	app := h.appLinks.BuildUsername(peer.username, params)
	params.Set("domain", peer.username)
	legacy := schemeURLValues("tg", "resolve", params)
	description := peer.about
	if description == "" {
		description = peer.fallbackDescription(h.appName)
	}
	data := usernamePageData{
		AppName:      h.appName,
		AppInitial:   appInitial(h.appName),
		Title:        peer.title,
		Username:     peer.username,
		Verified:     peer.verified,
		Extra:        peer.extra(),
		Description:  description,
		CanonicalURL: h.publicUsernameURL(peer.username),
		HomeURL:      h.publicBaseURL + "/",
		AppURL:       template.URL(app),
		LegacyTgURL:  template.URL(legacy),
		WebURL:       template.URL(publicWebAppURL(h.webBaseURL, legacy)),
		ButtonLabel:  peer.buttonLabel(),
		Initials:     peer.initials(),
	}
	if peer.hasPhoto {
		data.PhotoURL = h.publicAvatarURL(peer.username, peer.photo.ID)
	}
	data.AppURLJS = template.JS(strconv.Quote(app))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	if err := usernameLandingTemplate.Execute(w, data); err != nil {
		h.logger.Error("Render public username page failed", zap.String("username", peer.username), zap.Error(err))
	}
}

const maxPublicAvatarBytes = 4 << 20

func (h *handler) publicAvatar(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.PathValue("username"))
	photoID, err := strconv.ParseInt(r.PathValue("photoID"), 10, 64)
	if err != nil || photoID <= 0 || !validUsernamePath(username) || h.photos == nil {
		http.NotFound(w, r)
		return
	}
	peer, found, err := h.resolvePublicPeer(r.Context(), username)
	if err != nil {
		h.logger.Error("Public avatar peer lookup failed", zap.String("username", username), zap.Error(err))
		http.Error(w, "avatar lookup failed", http.StatusInternalServerError)
		return
	}
	if !found || !peer.hasPhoto || peer.photo.ID != photoID {
		http.NotFound(w, r)
		return
	}
	size, inline, ok := bestPublicPhotoSize(peer.photo.Sizes)
	if !ok {
		http.NotFound(w, r)
		return
	}
	etag := fmt.Sprintf("\"public-avatar-%d-%s-%d\"", peer.photo.ID, size.Type, size.Size)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	data := inline
	mimeType := ""
	if len(data) == 0 {
		chunk, found, err := h.photos.GetFile(r.Context(), domain.FileDownloadRequest{
			LocationKey: fmt.Sprintf("photo:%d:%s", peer.photo.ID, size.Type),
			Limit:       maxPublicAvatarBytes + 1,
		})
		if err != nil {
			h.logger.Error("Read public avatar blob failed", zap.String("username", username), zap.Int64("photo_id", photoID), zap.Error(err))
			http.Error(w, "avatar read failed", http.StatusInternalServerError)
			return
		}
		if !found || chunk.Total <= 0 || chunk.Total > maxPublicAvatarBytes || int64(len(chunk.Bytes)) != chunk.Total {
			h.logger.Warn("Public avatar blob is missing or outside bounds", zap.String("username", username), zap.Int64("photo_id", photoID), zap.Int64("total", chunk.Total), zap.Int("bytes", len(chunk.Bytes)))
			http.NotFound(w, r)
			return
		}
		data = chunk.Bytes
		mimeType = chunk.MimeType
	}
	if len(data) == 0 || len(data) > maxPublicAvatarBytes {
		http.NotFound(w, r)
		return
	}
	detected := http.DetectContentType(data)
	if !safePublicImageType(detected) {
		h.logger.Warn("Public avatar blob is not a safe raster image", zap.String("username", username), zap.Int64("photo_id", photoID), zap.String("detected_type", detected), zap.String("stored_type", mimeType))
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", detected)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if peer.photo.Date > 0 {
		w.Header().Set("Last-Modified", time.Unix(int64(peer.photo.Date), 0).UTC().Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *handler) serveSet(w http.ResponseWriter, r *http.Request, pathKind string) {
	shortName := strings.TrimSpace(r.PathValue("shortName"))
	if !validShortNamePath(shortName) {
		http.NotFound(w, r)
		return
	}
	set, docs, found, err := h.stickerSets.ResolveStickerSet(r.Context(), domain.StickerSetRef{
		Kind:      domain.StickerSetRefByShortName,
		ShortName: shortName,
	})
	if err != nil {
		http.Error(w, "sticker set lookup failed", http.StatusInternalServerError)
		return
	}
	if !found || set.Deleted {
		http.NotFound(w, r)
		return
	}
	canonicalKind := linkKind(set)
	if canonicalKind != pathKind {
		http.Redirect(w, r, h.publicURL(canonicalKind, set.ShortName), http.StatusPermanentRedirect)
		return
	}
	count := set.Count
	if count == 0 {
		count = len(docs)
	}
	app := h.appURL(canonicalKind, "set", set.ShortName)
	data := pageData{
		AppName:      h.appName,
		Title:        fallbackTitle(set),
		KindLabel:    kindLabel(set),
		Subtitle:     fmt.Sprintf("@%s · %d %s", set.ShortName, count, itemNoun(set, count)),
		Description:  "This page opens the app so you can preview and install the set. Files are still fetched by the app through MTProto.",
		CanonicalURL: h.publicURL(canonicalKind, set.ShortName),
		AppURL:       template.URL(app),
		LegacyTgURL:  template.URL(legacyTgURL(canonicalKind, "set", set.ShortName)),
	}
	data.AppURLJS = template.JS(strconv.Quote(app))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	if err := landingTemplate.Execute(w, data); err != nil {
		http.Error(w, "render sticker set page failed", http.StatusInternalServerError)
	}
}

func (h *handler) publicURL(kind, value string) string {
	return h.publicBaseURL + "/" + kind + "/" + url.PathEscape(value)
}

func (h *handler) publicUsernameURL(username string) string {
	return h.publicBaseURL + "/" + url.PathEscape(username)
}

func (h *handler) publicAvatarURL(username string, photoID int64) string {
	return h.publicBaseURL + "/_public/avatar/" + url.PathEscape(username) + "/" + strconv.FormatInt(photoID, 10)
}

type publicPeerKind string

const (
	publicPeerUser       publicPeerKind = "user"
	publicPeerBot        publicPeerKind = "bot"
	publicPeerChannel    publicPeerKind = "channel"
	publicPeerSupergroup publicPeerKind = "supergroup"
)

type publicPeer struct {
	kind        publicPeerKind
	username    string
	title       string
	about       string
	verified    bool
	memberCount int
	photo       domain.Photo
	hasPhoto    bool
}

func (h *handler) resolvePublicPeer(ctx context.Context, username string) (publicPeer, bool, error) {
	var (
		u      domain.User
		userOK bool
		ch     domain.Channel
		chOK   bool
		err    error
	)
	if h.users != nil {
		u, userOK, err = h.users.ByUsername(ctx, username)
		if err != nil {
			return publicPeer{}, false, err
		}
	}
	if h.channels != nil {
		ch, chOK, err = h.channels.ResolvePublicChannelUsername(ctx, 0, username)
		if err != nil {
			return publicPeer{}, false, err
		}
	}
	if userOK && chOK {
		return publicPeer{}, false, fmt.Errorf("public username %q has multiple owners", username)
	}
	if userOK {
		return h.publicUserPeer(ctx, username, u)
	}
	if chOK {
		return h.publicChannelPeer(ctx, username, ch)
	}
	return publicPeer{}, false, nil
}

func (h *handler) publicUserPeer(ctx context.Context, requested string, u domain.User) (publicPeer, bool, error) {
	if u.ID == 0 || !strings.EqualFold(strings.TrimSpace(u.Username), requested) || !validUsernamePath(u.Username) {
		return publicPeer{}, false, fmt.Errorf("user username lookup returned invalid owner for %q", requested)
	}
	title := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if title == "" {
		title = u.Username
	}
	if err := validatePublicPeerText(title, u.About); err != nil {
		return publicPeer{}, false, fmt.Errorf("invalid public user %q: %w", u.Username, err)
	}
	about := strings.TrimSpace(u.About)
	photoKind := domain.ProfilePhotoKindProfile
	if !u.Bot && h.privacy != nil {
		visible, err := h.privacy.CanSeeAnonymous(ctx, u.ID, domain.PrivacyKeyAbout)
		if err != nil {
			return publicPeer{}, false, fmt.Errorf("evaluate public about privacy: %w", err)
		}
		if !visible {
			about = ""
		}
		visible, err = h.privacy.CanSeeAnonymous(ctx, u.ID, domain.PrivacyKeyProfilePhoto)
		if err != nil {
			return publicPeer{}, false, fmt.Errorf("evaluate public profile photo privacy: %w", err)
		}
		if !visible {
			photoKind = domain.ProfilePhotoKindFallback
		}
	}
	peer := publicPeer{
		kind:     publicPeerUser,
		username: u.Username,
		title:    title,
		about:    about,
		verified: u.Verified,
	}
	if u.Bot {
		peer.kind = publicPeerBot
	}
	if h.photos != nil {
		photo, found, err := h.photos.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, u.ID, photoKind)
		if err != nil {
			return publicPeer{}, false, fmt.Errorf("load public user photo: %w", err)
		}
		if found && photo.ID != 0 {
			if _, _, renderable := bestPublicPhotoSize(photo.Sizes); renderable {
				peer.photo, peer.hasPhoto = photo, true
			}
		}
	}
	return peer, true, nil
}

func (h *handler) publicChannelPeer(ctx context.Context, requested string, ch domain.Channel) (publicPeer, bool, error) {
	if ch.ID == 0 || ch.Deleted || ch.ParticipantsCount < 0 || (!ch.Broadcast && !ch.Megagroup) || !strings.EqualFold(strings.TrimSpace(ch.Username), requested) || !validUsernamePath(ch.Username) {
		return publicPeer{}, false, fmt.Errorf("channel username lookup returned invalid owner for %q", requested)
	}
	if err := validatePublicPeerText(ch.Title, ch.About); err != nil {
		return publicPeer{}, false, fmt.Errorf("invalid public channel %q: %w", ch.Username, err)
	}
	peer := publicPeer{
		kind:        publicPeerChannel,
		username:    ch.Username,
		title:       strings.TrimSpace(ch.Title),
		about:       strings.TrimSpace(ch.About),
		verified:    ch.Verified,
		memberCount: ch.ParticipantsCount,
	}
	if ch.Megagroup {
		peer.kind = publicPeerSupergroup
	}
	if h.photos != nil && ch.PhotoID != 0 {
		photo, found, err := h.photos.GetPhoto(ctx, ch.PhotoID)
		if err != nil {
			return publicPeer{}, false, fmt.Errorf("load public channel photo: %w", err)
		}
		if !found {
			return publicPeer{}, false, fmt.Errorf("channel %q current photo %d is missing", ch.Username, ch.PhotoID)
		}
		if photo.ID == ch.PhotoID {
			if _, _, renderable := bestPublicPhotoSize(photo.Sizes); renderable {
				peer.photo, peer.hasPhoto = photo, true
			}
		} else {
			return publicPeer{}, false, fmt.Errorf("channel %q photo lookup returned id %d, want %d", ch.Username, photo.ID, ch.PhotoID)
		}
	}
	return peer, true, nil
}

func validatePublicPeerText(title, about string) error {
	if strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 256 {
		return fmt.Errorf("title is empty or too long")
	}
	if utf8.RuneCountInString(about) > 4096 {
		return fmt.Errorf("about is too long")
	}
	return nil
}

func (p publicPeer) buttonLabel() string {
	switch p.kind {
	case publicPeerBot:
		return "Start Bot"
	case publicPeerChannel:
		return "View Channel"
	case publicPeerSupergroup:
		return "View Group"
	default:
		return "Send Message"
	}
}

func (p publicPeer) extra() string {
	switch p.kind {
	case publicPeerBot:
		return "bot"
	case publicPeerChannel:
		return groupedDecimal(p.memberCount) + " " + plural(p.memberCount, "subscriber", "subscribers")
	case publicPeerSupergroup:
		return groupedDecimal(p.memberCount) + " " + plural(p.memberCount, "member", "members")
	default:
		return ""
	}
}

func (p publicPeer) fallbackDescription(appName string) string {
	switch p.kind {
	case publicPeerBot:
		return "Open " + appName + " to start a chat with this bot."
	case publicPeerChannel:
		return "Open " + appName + " to view and join this channel."
	case publicPeerSupergroup:
		return "Open " + appName + " to view and join this group."
	default:
		return "Open " + appName + " to send a message to @" + p.username + "."
	}
}

func (p publicPeer) initials() string {
	words := strings.Fields(p.title)
	if len(words) == 0 {
		words = []string{p.username}
	}
	first := []rune(words[0])
	if len(first) == 0 {
		return "T"
	}
	out := []rune{first[0]}
	if len(words) > 1 {
		last := []rune(words[len(words)-1])
		if len(last) > 0 {
			out = append(out, last[0])
		}
	}
	return strings.ToUpper(string(out))
}

func groupedDecimal(n int) string {
	if n < 0 {
		n = 0
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + " " + s[i:]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func appInitial(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "T"
}

const (
	maxPublicLinkRawQuery = 2048
	maxPublicLinkParams   = 16
	maxPublicLinkValues   = 2
	maxPublicLinkValueLen = 512
)

func publicResolveQuery(raw string) (url.Values, bool) {
	if len(raw) > maxPublicLinkRawQuery {
		return nil, false
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) > maxPublicLinkParams {
		return nil, false
	}
	out := make(url.Values, len(values)+1)
	for key, items := range values {
		if strings.EqualFold(key, "domain") {
			continue
		}
		if !validPublicQueryKey(key) || len(items) > maxPublicLinkValues {
			return nil, false
		}
		for _, value := range items {
			if len(value) > maxPublicLinkValueLen || !utf8.ValidString(value) {
				return nil, false
			}
			out.Add(key, value)
		}
	}
	return out, true
}

func validPublicQueryKey(key string) bool {
	if key == "" || len(key) > 32 {
		return false
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func bestPublicPhotoSize(sizes []domain.PhotoSize) (domain.PhotoSize, []byte, bool) {
	var (
		best      domain.PhotoSize
		bestBytes []byte
		bestScore int64 = -1
	)
	for _, size := range sizes {
		if !validPhotoSizeType(size.Type) {
			continue
		}
		var inline []byte
		switch size.Kind {
		case domain.PhotoSizeKindCached:
			if len(size.Bytes) == 0 || len(size.Bytes) > maxPublicAvatarBytes {
				continue
			}
			inline = size.Bytes
		case domain.PhotoSizeKindDefault, domain.PhotoSizeKindProgressive:
			// Downloadable static raster size.
		default:
			continue
		}
		score := int64(size.W) * int64(size.H)
		if score <= 0 {
			score = int64(size.Size)
		}
		if score > bestScore {
			best, bestBytes, bestScore = size, inline, score
		}
	}
	return best, bestBytes, bestScore >= 0
}

func validPhotoSizeType(value string) bool {
	if value == "" || len(value) > 8 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func safePublicImageType(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func schemeURLValues(scheme, kind string, values url.Values) string {
	return (&url.URL{Scheme: scheme, Host: kind, RawQuery: values.Encode()}).String()
}

func publicWebAppURL(webBaseURL, legacyURL string) string {
	return strings.TrimRight(webBaseURL, "/") + "/#?tgaddr=" + url.QueryEscape(legacyURL)
}

func publicSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func validShortNamePath(shortName string) bool {
	if shortName == "" || len(shortName) > 64 {
		return false
	}
	for _, r := range shortName {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func validSlugPath(slug string) bool {
	return links.ValidChatlistSlug(slug)
}

func validStarGiftSlugPath(slug string) bool {
	if slug == "" || len(slug) > domain.MaxStarGiftSlugBytes {
		return false
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func validUsernamePath(username string) bool {
	if username == "" || len(username) < 5 || len(username) > 32 {
		return false
	}
	for i, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9', r == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func linkKind(set domain.StickerSet) string {
	if set.Kind == domain.StickerSetKindEmoji || set.Emojis {
		return "addemoji"
	}
	return "addstickers"
}

func fallbackTitle(set domain.StickerSet) string {
	if title := strings.TrimSpace(set.Title); title != "" {
		return title
	}
	return set.ShortName
}

func kindLabel(set domain.StickerSet) string {
	switch {
	case set.Kind == domain.StickerSetKindEmoji || set.Emojis:
		return "custom emoji set"
	case set.Kind == domain.StickerSetKindMasks || set.Masks:
		return "mask set"
	default:
		return "sticker set"
	}
}

func itemNoun(set domain.StickerSet, count int) string {
	if set.Kind == domain.StickerSetKindEmoji || set.Emojis {
		if count == 1 {
			return "custom emoji"
		}
		return "custom emoji"
	}
	if count == 1 {
		return "sticker"
	}
	return "stickers"
}

func (h *handler) appURL(kind, key, value string) string {
	return h.appLinks.Build(kind, url.Values{key: []string{value}})
}

func legacyTgURL(kind, key, value string) string {
	return schemeURL("tg", kind, key, value)
}

func schemeURL(scheme, kind, key, value string) string {
	return scheme + "://" + kind + "?" + key + "=" + url.QueryEscape(value)
}

type pageData struct {
	AppName      string
	Title        string
	KindLabel    string
	Subtitle     string
	Description  string
	CanonicalURL string
	AppURL       template.URL
	LegacyTgURL  template.URL
	AppURLJS     template.JS
}

type usernamePageData struct {
	AppName      string
	AppInitial   string
	Title        string
	Username     string
	Verified     bool
	Extra        string
	Description  string
	CanonicalURL string
	HomeURL      string
	PhotoURL     string
	Initials     string
	ButtonLabel  string
	AppURL       template.URL
	LegacyTgURL  template.URL
	WebURL       template.URL
	AppURLJS     template.JS
}

func (h *handler) serveUsernameNotFound(w http.ResponseWriter, username string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=30, must-revalidate")
	w.WriteHeader(http.StatusNotFound)
	if err := usernameNotFoundTemplate.Execute(w, struct {
		Username string
		HomeURL  string
		AppName  string
	}{Username: username, HomeURL: h.publicBaseURL + "/", AppName: h.appName}); err != nil {
		h.logger.Error("Render public username not-found page failed", zap.String("username", username), zap.Error(err))
	}
}

var usernameLandingTemplate = template.Must(template.New("username-landing").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
  <meta name="theme-color" content="#0e1621">
  <title>{{.Title}} (@{{.Username}}) - {{.AppName}}</title>
  <meta name="description" content="{{.Description}}">
  <meta name="robots" content="index,follow,max-image-preview:large">
  <link rel="canonical" href="{{.CanonicalURL}}">
  <meta property="og:type" content="profile">
  <meta property="og:site_name" content="{{.AppName}}">
  <meta property="og:title" content="{{.Title}}">
  <meta property="og:description" content="{{.Description}}">
  <meta property="og:url" content="{{.CanonicalURL}}">
  {{if .PhotoURL}}<meta property="og:image" content="{{.PhotoURL}}">{{end}}
  <meta property="al:android:url" content="{{.AppURL}}">
  <meta property="al:ios:url" content="{{.AppURL}}">
  <meta name="twitter:card" content="summary">
  <meta name="twitter:title" content="{{.Title}}">
  <meta name="twitter:description" content="{{.Description}}">
  {{if .PhotoURL}}<meta name="twitter:image" content="{{.PhotoURL}}">{{end}}
  <style>
    :root { color-scheme: dark; font-family: Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100svh; color: #f5f8fb; background:
      radial-gradient(circle at 50% -20%, rgba(50, 161, 255, .25), transparent 42%), #0e1621; }
    .shell { min-height: 100svh; display: grid; grid-template-rows: auto 1fr auto; }
    .brand { display: flex; align-items: center; gap: 10px; width: fit-content; margin: 28px auto 0; color: #dceeff;
      font-size: 17px; font-weight: 700; letter-spacing: .01em; text-decoration: none; }
    .brand-mark { display: grid; place-items: center; width: 34px; height: 34px; border-radius: 50%; color: white;
      background: linear-gradient(145deg, #52b8ff, #168de2); box-shadow: 0 8px 24px rgba(31, 151, 232, .3); }
    main { display: grid; place-items: center; padding: 32px 18px; }
    .card { width: min(100%, 420px); padding: 34px 30px 28px; text-align: center; border: 1px solid rgba(255,255,255,.08);
      border-radius: 24px; background: rgba(23, 33, 43, .92); box-shadow: 0 28px 90px rgba(0,0,0,.34); backdrop-filter: blur(18px); }
    .avatar { display: grid; place-items: center; width: 112px; height: 112px; margin: 0 auto 22px; overflow: hidden;
      border-radius: 50%; background: linear-gradient(145deg, #47b7ff, #167bc1); box-shadow: 0 16px 44px rgba(10, 112, 183, .3); }
    .avatar img { display: block; width: 100%; height: 100%; object-fit: cover; }
    .initials { font-size: 38px; font-weight: 750; letter-spacing: -.04em; color: white; }
    h1 { display: flex; align-items: center; justify-content: center; gap: 8px; margin: 0; font-size: clamp(25px, 7vw, 32px);
      line-height: 1.18; letter-spacing: -.025em; overflow-wrap: anywhere; }
    .verified { display: inline-grid; flex: 0 0 auto; place-items: center; width: 21px; height: 21px; border-radius: 50%;
      color: #fff; background: #3aa8f7; font-size: 13px; font-weight: 900; }
    .username { margin: 8px 0 0; color: #67bff9; font-size: 16px; overflow-wrap: anywhere; }
    .extra { margin: 7px 0 0; color: #91a3b5; font-size: 14px; }
    .description { margin: 22px auto 0; color: #c5d0da; font-size: 15px; line-height: 1.55; white-space: pre-line;
      overflow-wrap: anywhere; }
    .actions { display: grid; gap: 11px; margin-top: 28px; }
    .button { display: inline-flex; align-items: center; justify-content: center; min-height: 48px; padding: 0 20px; border-radius: 13px;
      font-size: 15px; font-weight: 720; text-decoration: none; transition: transform .16s ease, background .16s ease; }
    .button:hover { transform: translateY(-1px); }
    .primary { color: #fff; background: linear-gradient(135deg, #31a9f5, #168de2); box-shadow: 0 10px 28px rgba(22,141,226,.25); }
    .secondary { color: #a9dafa; background: rgba(72, 164, 226, .12); border: 1px solid rgba(89, 180, 241, .15); }
    .legacy { margin: 18px 0 0; color: #718395; font-size: 12px; }
    .legacy a { color: #83bddd; text-decoration: none; }
    footer { padding: 0 18px 26px; color: #657789; font-size: 12px; text-align: center; }
    @media (max-width: 480px) {
      .brand { margin-top: 20px; }
      main { padding: 24px 14px; align-items: start; }
      .card { padding: 28px 22px 24px; border-radius: 20px; }
      .avatar { width: 96px; height: 96px; }
    }
    @media (prefers-reduced-motion: reduce) { .button { transition: none; } }
  </style>
</head>
<body>
  <div class="shell">
    <a class="brand" href="{{.HomeURL}}" aria-label="{{.AppName}} home"><span class="brand-mark">{{.AppInitial}}</span><span>{{.AppName}}</span></a>
    <main>
      <article class="card">
        <div class="avatar">{{if .PhotoURL}}<img src="{{.PhotoURL}}" alt="{{.Title}} profile photo" width="112" height="112">{{else}}<span class="initials" aria-hidden="true">{{.Initials}}</span>{{end}}</div>
        <h1><span>{{.Title}}</span>{{if .Verified}}<span class="verified" title="Verified" aria-label="Verified">✓</span>{{end}}</h1>
        <p class="username">@{{.Username}}</p>
        {{if .Extra}}<p class="extra">{{.Extra}}</p>{{end}}
        <p class="description">{{.Description}}</p>
        <div class="actions">
          <a class="button primary" href="{{.AppURL}}">{{.ButtonLabel}}</a>
          <a class="button secondary" href="{{.WebURL}}">Open in Web</a>
        </div>
        <p class="legacy">Old test clients only: <a href="{{.LegacyTgURL}}">open with tg://</a></p>
      </article>
    </main>
    <footer>If you have {{.AppName}}, this page can open the chat directly.</footer>
  </div>
  <script>window.setTimeout(function () { window.location.href = {{.AppURLJS}}; }, 250);</script>
</body>
</html>
`))

var usernameNotFoundTemplate = template.Must(template.New("username-not-found").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex,nofollow"><title>Username not found - {{.AppName}}</title>
<style>:root{color-scheme:dark;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}body{margin:0;min-height:100svh;display:grid;place-items:center;padding:24px;background:#0e1621;color:#f5f8fb}.card{width:min(100%,420px);padding:34px 28px;border:1px solid rgba(255,255,255,.08);border-radius:22px;background:#17212b;text-align:center}h1{margin:0 0 12px;font-size:26px}p{margin:0;color:#9fb0bf;line-height:1.55;overflow-wrap:anywhere}a{display:inline-block;margin-top:24px;color:#67bff9;text-decoration:none}</style>
</head><body><main class="card"><h1>Username not found</h1><p>{{if .Username}}@{{.Username}} is not an active public {{.AppName}} username.{{else}}This is not a valid public {{.AppName}} username.{{end}}</p><a href="{{.HomeURL}}">Back to {{.AppName}}</a></main></body></html>`))

var landingTemplate = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}} - {{.AppName}}</title>
  <link rel="canonical" href="{{.CanonicalURL}}">
  <meta property="og:title" content="{{.Title}}">
  <meta property="og:description" content="{{.Description}}">
  <meta property="og:url" content="{{.CanonicalURL}}">
  <meta name="robots" content="noindex">
  <style>
    :root { color-scheme: light dark; font-family: Arial, Helvetica, sans-serif; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #f4f7fb; color: #15202b; }
    main { width: min(92vw, 460px); padding: 32px; border: 1px solid #d9e1ea; border-radius: 8px; background: #fff; box-shadow: 0 16px 48px rgba(21, 32, 43, .08); }
    h1 { margin: 0 0 10px; font-size: 28px; line-height: 1.18; font-weight: 700; }
    p { margin: 0 0 18px; line-height: 1.5; color: #4a5b6b; }
    .meta { font-size: 14px; color: #6b7b8b; }
    a.button { display: inline-flex; align-items: center; justify-content: center; min-height: 44px; padding: 0 18px; border-radius: 6px; background: #1677c8; color: #fff; text-decoration: none; font-weight: 700; }
    a.raw { color: #1677c8; overflow-wrap: anywhere; }
    @media (prefers-color-scheme: dark) {
      body { background: #111820; color: #e9eef5; }
      main { background: #18222d; border-color: #293849; box-shadow: none; }
      p, .meta { color: #aebdca; }
      a.button { background: #45a3ff; color: #06131f; }
      a.raw { color: #74baff; }
    }
  </style>
</head>
<body>
  <main>
    <p class="meta">{{.KindLabel}}</p>
    <h1>{{.Title}}</h1>
    <p class="meta">{{.Subtitle}}</p>
    <p><a class="button" href="{{.AppURL}}">Open in {{.AppName}}</a></p>
    <p>{{.Description}}</p>
    <p class="meta">Old test clients only: <a class="raw" href="{{.LegacyTgURL}}">open with tg://</a></p>
    <p class="meta"><a class="raw" href="{{.CanonicalURL}}">{{.CanonicalURL}}</a></p>
  </main>
  <script>
    window.setTimeout(function () {
      window.location.href = {{.AppURLJS}};
    }, 250);
  </script>
</body>
</html>
`))
