package stickerlinks

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

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/links"
)

type Config struct {
	Addr          string
	PublicBaseURL string
}

type Resolver interface {
	ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error)
}

func Start(ctx context.Context, cfg Config, resolver Resolver, logger *zap.Logger) (*http.Server, error) {
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		return nil, nil
	}
	if resolver == nil {
		return nil, fmt.Errorf("sticker links resolver is nil")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	handler := NewHandler(resolver, cfg.PublicBaseURL)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		logger.Info("Public link Web endpoint enabled", zap.String("addr", addr), zap.String("public_base_url", normalizePublicBaseURL(cfg.PublicBaseURL)))
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

func NewHandler(resolver Resolver, publicBaseURL string) http.Handler {
	h := &handler{
		resolver:      resolver,
		publicBaseURL: normalizePublicBaseURL(publicBaseURL),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /addstickers/{shortName}", h.addStickers)
	mux.HandleFunc("GET /addemoji/{shortName}", h.addEmoji)
	mux.HandleFunc("GET /addlist/{slug}", h.addList)
	return mux
}

type handler struct {
	resolver      Resolver
	publicBaseURL string
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
	app := appURL("addlist", "slug", slug)
	data := pageData{
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

func (h *handler) serveSet(w http.ResponseWriter, r *http.Request, pathKind string) {
	shortName := strings.TrimSpace(r.PathValue("shortName"))
	if !validShortNamePath(shortName) {
		http.NotFound(w, r)
		return
	}
	set, docs, found, err := h.resolver.ResolveStickerSet(r.Context(), domain.StickerSetRef{
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
	app := appURL(canonicalKind, "set", set.ShortName)
	data := pageData{
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

func normalizePublicBaseURL(raw string) string {
	u, err := url.Parse(links.NormalizeBaseURL(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return links.DefaultPublicBaseURL
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
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

func appURL(kind, key, value string) string {
	return schemeURL("telesrv", kind, key, value)
}

func legacyTgURL(kind, key, value string) string {
	return schemeURL("tg", kind, key, value)
}

func schemeURL(scheme, kind, key, value string) string {
	return scheme + "://" + kind + "?" + key + "=" + url.QueryEscape(value)
}

type pageData struct {
	Title        string
	KindLabel    string
	Subtitle     string
	Description  string
	CanonicalURL string
	AppURL       template.URL
	LegacyTgURL  template.URL
	AppURLJS     template.JS
}

var landingTemplate = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}} - telesrv</title>
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
    <p><a class="button" href="{{.AppURL}}">Open in telesrv</a></p>
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
