package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/html"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

// Link preview (webpage preview): fetch the URL in a message, parse OpenGraph/Twitter-card/<title>+meta
// metadata, and mint a preview card (with an optional preview image). The security model is
// isomorphic with external_media (SSRF IP check at dial time, http/https only, redirect/size/timeout
// limits, global rate limit), but it uses an independent rate limiter and cache to avoid contending
// for the same budget as external media fetching. HTML and the preview image share one total duration
// budget (the parent ctx deadline).
//
// The parse result (done / empty) goes through the L1 in-process cache (singleflight folding concurrent
// fetches of the same URL) + the L3 web_pages table (deduplicated across instances by normalized URL
// hash). A transient failure (network/rate limit/SSRF block) returns an error and is not cached,
// avoiding one glitch poisoning a popular link into "no preview".

var (
	// ErrWebPagePreviewDisabled means link preview fetching is not enabled.
	ErrWebPagePreviewDisabled = errors.New("web page preview disabled")
	// ErrWebPagePreviewInvalid means the URL is invalid / blocked by SSRF / upstream failed / over a limit.
	ErrWebPagePreviewInvalid = errors.New("web page preview invalid")
	// errWebPageTerminal marks "deterministic, short-term-unchangeable" failures (SSRF block/4xx/
	// invalid URL). These resolve to a terminal empty preview and are negatively cached, avoiding a
	// repeated PG + external hit on every keystroke/send; transient failures (5xx/timeout/rate limit/
	// dial failure) do not carry this marker, are not cached, and are retryable.
	errWebPageTerminal = errors.New("web page terminal")
)

// terminalFetchErr builds a terminal failure error (which is negatively cached).
func terminalFetchErr(msg string) error {
	return fmt.Errorf("%w: %w: %s", ErrWebPagePreviewInvalid, errWebPageTerminal, msg)
}

const (
	webpageRequestTimeout = 15 * time.Second
	webpageTotalTimeout   = 20 * time.Second
	webpageMaxRedirects   = 5
	// DefaultWebPagePreviewMaxBytes covers HTML fetching and preview image fetching (the head is at the top of the page, which is enough).
	DefaultWebPagePreviewMaxBytes = int64(5 << 20)
	// DefaultWebPagePreviewRatePerMin is the global per-minute fetch limit; one resolution makes at
	// most 2 upstream requests (HTML + image). 60 is a single-user figure and is on the low side for a
	// multi-user instance (typed-preview and real send share this budget and easily starve each other),
	// so it is raised to 300.
	DefaultWebPagePreviewRatePerMin = 300
	webpageRateWindow               = time.Minute
	// maxWebpageImagePixels is the upper bound against a preview image decompression bomb (intercepted by DecodeConfig dimensions before decoding).
	maxWebpageImagePixels  = int64(25_000_000)
	webpageCacheMaxEntries = 4096
	webpageCacheTTL        = 10 * time.Minute
	// webPageRefreshTTL is the staleness threshold for a resolved card: when an L3 hit exceeds this
	// age it is refreshed in the background with stale-while-revalidate (still returning the old card,
	// without blocking). webPageRefreshConcurrency limits concurrent refresh goroutines.
	webPageRefreshTTL         = 24 * time.Hour
	webPageRefreshConcurrency = 8
	// webpageUserAgent uses the Telegram crawler identity: many sites only emit OG tags to known crawlers.
	webpageUserAgent = "TelegramBot (like TwitterBot)"
	acceptHTML       = "text/html,application/xhtml+xml"
	acceptImage      = "image/*"
)

type webpageFetcher struct {
	client     *http.Client
	maxBytes   int64
	rateLimit  int
	cache      *readmodelcache.Cache[int64, domain.MessageWebPage]
	refreshSem chan struct{}

	mu         sync.Mutex
	fetchTimes []time.Time
}

// WithWebPagePreview enables link preview fetching. maxBytes<=0 / ratePerMin<=0 uses the default. SSRF protection is always on.
func WithWebPagePreview(maxBytes int64, ratePerMin int) Option {
	return func(s *Service) {
		if maxBytes <= 0 {
			maxBytes = DefaultWebPagePreviewMaxBytes
		}
		if ratePerMin <= 0 {
			ratePerMin = DefaultWebPagePreviewRatePerMin
		}
		s.webpage = newWebpageFetcher(maxBytes, ratePerMin, false)
	}
}

// newWebpageFetcher constructs the fetcher. allowPrivate is for tests only (pointing at httptest loopback); in production it is always false.
func newWebpageFetcher(maxBytes int64, ratePerMin int, allowPrivate bool) *webpageFetcher {
	dialer := &net.Dialer{Timeout: webpageRequestTimeout}
	dialer.Control = func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrWebPagePreviewInvalid
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return ErrWebPagePreviewInvalid
		}
		if !allowPrivate && isBlockedExternalIP(ip) {
			// An SSRF block is a deterministic failure → mark it terminal for negative caching (otherwise every keystroke re-hits PG and re-dials).
			return terminalFetchErr("blocked address " + host + " (SSRF guard)")
		}
		return nil
	}
	client := &http.Client{
		Timeout:   webpageRequestTimeout,
		Transport: &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, Proxy: nil},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= webpageMaxRedirects {
				return fmt.Errorf("%w: too many redirects", ErrWebPagePreviewInvalid)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("%w: blocked redirect scheme %q", ErrWebPagePreviewInvalid, req.URL.Scheme)
			}
			return nil
		},
	}
	return &webpageFetcher{
		client:     client,
		maxBytes:   maxBytes,
		rateLimit:  ratePerMin,
		refreshSem: make(chan struct{}, webPageRefreshConcurrency),
		cache: readmodelcache.New(readmodelcache.Config[int64, domain.MessageWebPage]{
			MaxEntries: webpageCacheMaxEntries,
			TTL:        webpageCacheTTL,
		}),
	}
}

func (f *webpageFetcher) allowFetch() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	kept := f.fetchTimes[:0]
	for _, at := range f.fetchTimes {
		if now.Sub(at) <= webpageRateWindow {
			kept = append(kept, at)
		}
	}
	f.fetchTimes = kept
	if len(f.fetchTimes) >= f.rateLimit {
		return false
	}
	f.fetchTimes = append(f.fetchTimes, now)
	return true
}

// fetch fetches a URL and returns (bytes, content-type). The SSRF check happens at dial time; ctx carries the shared total budget.
func (f *webpageFetcher) fetch(ctx context.Context, rawURL, accept string) ([]byte, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", terminalFetchErr("bad url") // an invalid URL is terminal
	}
	if !f.allowFetch() {
		return nil, "", fmt.Errorf("%w: rate limited", ErrWebPagePreviewInvalid) // rate limiting is transient, retryable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", terminalFetchErr("bad request")
	}
	req.Header.Set("User-Agent", webpageUserAgent)
	req.Header.Set("Accept", accept)
	resp, err := f.client.Do(req)
	if err != nil {
		// An SSRF block (the terminal returned by dial Control) comes up through url.Error and
		// errors.Is can still identify it; the other dial/timeout errors are transient.
		return nil, "", fmt.Errorf("%w: %w", ErrWebPagePreviewInvalid, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 4xx = deterministic (404/403/410…) and terminal, negatively cached; 5xx = transient and retryable.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, "", terminalFetchErr(fmt.Sprintf("upstream status %d", resp.StatusCode))
		}
		return nil, "", fmt.Errorf("%w: upstream status %d", ErrWebPagePreviewInvalid, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: read body: %v", ErrWebPagePreviewInvalid, err)
	}
	if len(data) == 0 || int64(len(data)) > f.maxBytes {
		return nil, "", fmt.Errorf("%w: body size %d", ErrWebPagePreviewInvalid, len(data))
	}
	contentType := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return data, strings.TrimSpace(strings.ToLower(contentType)), nil
}

// WebPagePreviewEnabled reports whether link preview fetching is enabled.
func (s *Service) WebPagePreviewEnabled() bool {
	return s != nil && s.webpage != nil
}

// LookupWebPage returns a resolved link preview from the cache only (L1 in-process → L3 web_pages
// table) without fetching. On a hit it returns (page,true); when uncached or disabled it returns
// false. The send path uses it to attach the done card directly on echo. It Peek's L1 first: while the
// client is typing, getWebPagePreview has most likely already resolved the same URL into L1, saving a
// PG lookup at send time.
func (s *Service) LookupWebPage(ctx context.Context, rawURL string) (domain.MessageWebPage, bool) {
	if s == nil || s.webpage == nil {
		return domain.MessageWebPage{}, false
	}
	normalized, ok := domain.NormalizeWebPageURL(rawURL)
	if !ok {
		return domain.MessageWebPage{}, false
	}
	urlHash := domain.WebPageURLHash(normalized)
	if page, ok := s.webpage.cache.Peek(urlHash); ok {
		return page, true
	}
	page, _, found, err := s.media.GetWebPageByURLHash(ctx, urlHash)
	if err != nil || !found {
		return domain.MessageWebPage{}, false
	}
	s.webpage.cache.Store(urlHash, page) // backfill L1 so later Peek hits.
	return page, true
}

// ResolveWebPage resolves a link preview through the L1 cache (singleflight dedup) + L3 web_pages
// durable dedup. It returns a MessageWebPage in done / empty form; a transient failure returns an
// error (the caller degrades to empty without erroring to the user).
func (s *Service) ResolveWebPage(ctx context.Context, rawURL string) (domain.MessageWebPage, error) {
	if s == nil || s.webpage == nil {
		return domain.MessageWebPage{}, ErrWebPagePreviewDisabled
	}
	normalized, ok := domain.NormalizeWebPageURL(rawURL)
	if !ok {
		return domain.MessageWebPage{}, ErrWebPagePreviewInvalid
	}
	urlHash := domain.WebPageURLHash(normalized)
	return s.webpage.cache.GetOrLoad(ctx, urlHash, func() (domain.MessageWebPage, error) {
		// L3 durable hit: reuse directly, deduplicated across instances; when stale, refresh in the background (returning the old card without blocking).
		if page, refreshedAt, found, err := s.media.GetWebPageByURLHash(ctx, urlHash); err == nil && found {
			s.webpage.maybeRefresh(s, normalized, urlHash, refreshedAt)
			return page, nil
		}
		// miss: fetch + parse (+ image). A transient failure returns an error → GetOrLoad does not cache (popular links are not poisoned).
		page, err := s.webpage.resolve(ctx, s, normalized, urlHash)
		if err != nil {
			return domain.MessageWebPage{}, err
		}
		// Terminal (done / empty): store in L3 for cross-instance + restart reuse.
		if perr := s.media.PutWebPage(ctx, urlHash, page, int(time.Now().Unix())); perr != nil {
			s.log.Warn("persist web page preview failed", zap.Int64("url_hash", urlHash), zap.Error(perr))
		}
		return page, nil
	})
}

// maybeRefresh refreshes once in the background with stale-while-revalidate when a card exceeds the
// webPageRefreshTTL age. Concurrency is limited by refreshSem; when full it skips (retries next time).
// A transient failure keeps the old card.
func (f *webpageFetcher) maybeRefresh(s *Service, normalizedURL string, urlHash int64, refreshedAt int) {
	if refreshedAt == 0 || time.Now().Unix()-int64(refreshedAt) < int64(webPageRefreshTTL/time.Second) {
		return
	}
	select {
	case f.refreshSem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-f.refreshSem }()
		ctx, cancel := context.WithTimeout(context.Background(), webpageTotalTimeout)
		defer cancel()
		page, err := f.resolve(ctx, s, normalizedURL, urlHash)
		if err != nil {
			return // transient failure: keep the old card.
		}
		if perr := s.media.PutWebPage(ctx, urlHash, page, int(time.Now().Unix())); perr != nil {
			return
		}
		f.cache.Store(urlHash, page) // refresh L1 so later reads see the new card.
	}()
}

// resolve actually fetches and builds the card. HTML and the preview image share the ctx total duration budget.
func (f *webpageFetcher) resolve(ctx context.Context, s *Service, normalizedURL string, urlHash int64) (domain.MessageWebPage, error) {
	ctx, cancel := context.WithTimeout(ctx, webpageTotalTimeout)
	defer cancel()

	data, contentType, err := f.fetch(ctx, normalizedURL, acceptHTML)
	if err != nil {
		// Terminal failure (SSRF/4xx/invalid URL) → negatively cached as an empty preview, avoiding a
		// repeated PG + external hit on each keystroke/send. Transient failure (5xx/timeout/dial/rate
		// limit) → propagate the error; GetOrLoad does not cache and it is retryable.
		if isTerminalWebPageFetchError(err) {
			return emptyWebPage(normalizedURL, urlHash), nil
		}
		return domain.MessageWebPage{}, err
	}
	if !isHTMLContentType(contentType) {
		// Non-HTML (e.g. pointing directly at an image/binary): terminal empty preview.
		return emptyWebPage(normalizedURL, urlHash), nil
	}
	meta := parseWebPageMeta(data, normalizedURL)
	if meta.empty() {
		return emptyWebPage(normalizedURL, urlHash), nil
	}
	page := doneWebPage(meta, normalizedURL, urlHash)
	if meta.image != "" {
		if photo, ok := f.fetchImage(ctx, s, meta.image); ok {
			page.Photo = &photo
			page.HasLargeMedia = true
		}
	}
	return page, nil
}

func isTerminalWebPageFetchError(err error) bool {
	if errors.Is(err, errWebPageTerminal) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// fetchImage fetches and mints the preview image (best-effort). It intercepts decompression bombs by size before decoding; non-images/failures are dropped.
func (f *webpageFetcher) fetchImage(ctx context.Context, s *Service, imageURL string) (domain.Photo, bool) {
	data, _, err := f.fetch(ctx, imageURL, acceptImage)
	if err != nil {
		return domain.Photo{}, false
	}
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(data))
	if derr != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxWebpageImagePixels {
		return domain.Photo{}, false
	}
	photo, err := s.CreatePhotoFromBytes(ctx, data)
	if err != nil {
		return domain.Photo{}, false
	}
	return photo, true
}

func emptyWebPage(rawURL string, urlHash int64) domain.MessageWebPage {
	return domain.MessageWebPage{State: domain.MessageWebPageStateEmpty, ID: urlHash, URL: rawURL}
}

func doneWebPage(meta webpageMeta, rawURL string, urlHash int64) domain.MessageWebPage {
	page := domain.MessageWebPage{
		State:       domain.MessageWebPageStateDone,
		ID:          urlHash,
		URL:         rawURL,
		DisplayURL:  webpageDisplayURL(rawURL),
		Type:        meta.pageType(),
		SiteName:    meta.siteName,
		Title:       meta.title,
		Description: meta.description,
		Author:      meta.author,
	}
	page.Hash = webpageContentHash(page)
	return page
}

// webpageMeta is the preview metadata extracted from the HTML head (merged by og>twitter>title/meta priority).
type webpageMeta struct {
	title       string
	description string
	siteName    string
	image       string
	author      string
	ogType      string
}

func (m webpageMeta) empty() bool {
	return m.title == "" && m.description == "" && m.siteName == "" && m.image == "" && m.author == ""
}

func (m webpageMeta) pageType() string {
	if m.ogType != "" {
		return m.ogType
	}
	if m.image != "" && m.title == "" && m.description == "" {
		return "photo"
	}
	return "article"
}

// parseWebPageMeta scans the HTML head's <meta>/<title> and extracts OpenGraph/Twitter-card/standard
// metadata. It stops at <body> or </head> (the metadata is all in the head). A relative og:image URL
// is resolved to absolute against baseURL.
func parseWebPageMeta(htmlBytes []byte, baseURL string) webpageMeta {
	var (
		m                          webpageMeta
		ogTitle, twTitle, docTitle string
		ogDesc, twDesc, metaDesc   string
		ogImage, twImage           string
		inTitle                    bool
	)
	z := html.NewTokenizer(bytes.NewReader(htmlBytes))
scan:
	for {
		switch z.Next() {
		case html.ErrorToken:
			break scan
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "meta":
				key, content := metaKeyContent(z, hasAttr)
				if content == "" {
					continue
				}
				switch key {
				case "og:title":
					ogTitle = content
				case "og:description":
					ogDesc = content
				case "og:site_name":
					m.siteName = content
				case "og:type":
					m.ogType = content
				case "og:image", "og:image:url", "og:image:secure_url":
					if ogImage == "" {
						ogImage = content
					}
				case "twitter:title":
					twTitle = content
				case "twitter:description":
					twDesc = content
				case "twitter:image", "twitter:image:src":
					if twImage == "" {
						twImage = content
					}
				case "description":
					metaDesc = content
				case "author", "article:author":
					if m.author == "" {
						m.author = content
					}
				}
			case "title":
				inTitle = true
			case "body":
				break scan
			}
		case html.TextToken:
			if inTitle && docTitle == "" {
				docTitle = strings.TrimSpace(string(z.Text()))
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "title":
				inTitle = false
			case "head":
				break scan
			}
		}
	}
	m.title = firstNonEmpty(ogTitle, twTitle, docTitle)
	m.description = firstNonEmpty(ogDesc, twDesc, metaDesc)
	if img := firstNonEmpty(ogImage, twImage); img != "" {
		if abs, ok := resolveAbsoluteURL(baseURL, img); ok {
			m.image = abs
		}
	}
	return m
}

// metaKeyContent collects (property|name) and content from one <meta> tag.
func metaKeyContent(z *html.Tokenizer, hasAttr bool) (string, string) {
	var key, content string
	for hasAttr {
		var k, v []byte
		k, v, hasAttr = z.TagAttr()
		switch strings.ToLower(string(k)) {
		case "property", "name":
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(string(v)))
			}
		case "content":
			content = strings.TrimSpace(string(v))
		}
	}
	return key, content
}

func resolveAbsoluteURL(base, ref string) (string, bool) {
	b, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", false
	}
	abs := b.ResolveReference(r)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return "", false
	}
	return abs.String(), true
}

func webpageDisplayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.TrimPrefix(u.Host, "www.")
}

// webpageContentHash computes a stable 31-bit hash of the card content (webPage.hash is a TL int,
// used for the getWebPage NotModified short-circuit). It covers only text fields; a preview image
// change is not counted (a preview image for the same URL is already deduplicated by content addressing).
func webpageContentHash(p domain.MessageWebPage) int {
	h := fnv.New32a()
	for _, s := range []string{p.URL, p.Title, p.Description, p.SiteName, p.Author, p.Type} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return int(h.Sum32() & 0x7fffffff)
}

func isHTMLContentType(ct string) bool {
	return ct == "" || ct == "text/html" || ct == "application/xhtml+xml"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
