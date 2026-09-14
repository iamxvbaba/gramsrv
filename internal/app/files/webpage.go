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

// 链接预览（webpage preview）：抓取消息里的 URL，解析 OpenGraph/Twitter-card/<title>+meta
// 元数据，铸造预览卡片（含可选预览图）。安全模型与 external_media 同构（SSRF 拨号期 IP 校验、
// 仅 http/https、重定向/大小/超时上限、全局限速），但用独立的限速器与缓存，避免与外链媒体抓取
// 争用同一预算。HTML 与预览图共用一个总时长预算（父 ctx deadline）。
//
// 解析结果（done / empty）经 L1 进程内缓存（singleflight 折叠并发同 URL 抓取）+ L3 web_pages
// 表（按规范化 URL 哈希跨实例去重）。瞬时失败（网络/限速/SSRF 拦截）返回 error 不缓存，避免一次
// 抖动把热门链接毒成"无预览"。

var (
	// ErrWebPagePreviewDisabled 表示未启用链接预览抓取。
	ErrWebPagePreviewDisabled = errors.New("web page preview disabled")
	// ErrWebPagePreviewInvalid 表示 URL 不合法/被 SSRF 拦截/上游失败/超限。
	ErrWebPagePreviewInvalid = errors.New("web page preview invalid")
	// errWebPageTerminal 标记「确定性、短期不会变」的失败（SSRF 拦截/4xx/非法 URL）。这类
	// 解析为终态空预览并负缓存，避免每次按键/发送重复打 PG+外网；瞬时失败（5xx/超时/限速/
	// dial 失败）不带此标记、不缓存、可重试。
	errWebPageTerminal = errors.New("web page terminal")
)

// terminalFetchErr 构造一个终态失败错误（会被负缓存）。
func terminalFetchErr(msg string) error {
	return fmt.Errorf("%w: %w: %s", ErrWebPagePreviewInvalid, errWebPageTerminal, msg)
}

const (
	webpageRequestTimeout = 15 * time.Second
	webpageTotalTimeout   = 20 * time.Second
	webpageMaxRedirects   = 5
	// DefaultWebPagePreviewMaxBytes 覆盖 HTML 抓取与预览图抓取（head 在页首，足够）。
	DefaultWebPagePreviewMaxBytes = int64(5 << 20)
	// DefaultWebPagePreviewRatePerMin 是全局每分钟抓取上限；一次解析最多 2 次上游（HTML+图）。
	// 60 是单用户口径，多用户实例偏低（输入预览与真实发送共用此预算易互相饿死），上调到 300。
	DefaultWebPagePreviewRatePerMin = 300
	webpageRateWindow               = time.Minute
	// maxWebpageImagePixels 是预览图解压炸弹上界（解码前按 DecodeConfig 尺寸拦截）。
	maxWebpageImagePixels  = int64(25_000_000)
	webpageCacheMaxEntries = 4096
	webpageCacheTTL        = 10 * time.Minute
	// webPageRefreshTTL 是已解析卡片的陈旧阈值：L3 命中且超过此龄时后台 stale-while-revalidate
	// 刷新（返回的仍是旧卡片，不阻塞）。webPageRefreshConcurrency 限并发刷新 goroutine。
	webPageRefreshTTL         = 24 * time.Hour
	webPageRefreshConcurrency = 8
	// webpageUserAgent 用 Telegram 爬虫标识：很多站点只对已知爬虫吐 OG 标签。
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

// UniqueStarGiftLookup 按 slug 解析唯一藏品，供 SetGiftLinkPreview 使用。
type UniqueStarGiftLookup interface {
	UniqueBySlug(ctx context.Context, slug string) (domain.UniqueStarGift, bool, error)
}

// SetGiftLinkPreview 打开对我们自己 /nft/{slug} 落地页链接的特判：resolve() 不再把这类
// URL 当成任意网页去抓取+解析 OG 标签，而是直接查库铸造一张携带 UniqueGift 快照的卡片
// （见 domain.MessageWebPage.UniqueGift），使客户端能用与应用内直接打开该礼物时相同的本
// 地 Lottie 渲染出 pattern/model/backdrop，而不是退化成一张服务端合成的静态图。
//
// 礼物服务在这个服务构造完之后才建好，故走构造后注入（与 botsService.SetPremium 等同一
// 惯例），baseURL 不带末尾斜杠（如 "https://sgq.me"，即 cfg.PublicBaseURL）。
func (s *Service) SetGiftLinkPreview(baseURL, appName string, lookup UniqueStarGiftLookup) {
	if s == nil || lookup == nil {
		return
	}
	s.giftLinkBaseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	s.giftLinkAppName = appName
	s.giftLinks = lookup
}

// giftLinkSlug 报告 normalizedURL 是否为我们自己的 /nft/{slug} 落地页链接，返回其 slug。
// normalizedURL 来自 domain.NormalizeWebPageURL：scheme/host 已小写、无 fragment。
func (s *Service) giftLinkSlug(normalizedURL string) (string, bool) {
	if s.giftLinkBaseURL == "" || s.giftLinks == nil {
		return "", false
	}
	prefix := s.giftLinkBaseURL + "/nft/"
	if !strings.HasPrefix(normalizedURL, prefix) {
		return "", false
	}
	slug := strings.TrimPrefix(normalizedURL, prefix)
	if i := strings.IndexAny(slug, "?#/"); i >= 0 {
		slug = slug[:i] // 拒绝额外路径段/查询串，只接受裸 slug（镜像 web 层 validStarGiftSlugPath 的用法）。
	}
	if slug == "" || len(slug) > domain.MaxStarGiftSlugBytes || !giftSlugCharsetValid(slug) {
		return "", false
	}
	return slug, true
}

func giftSlugCharsetValid(slug string) bool {
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

// resolveGiftLinkWebPage 直接查库铸造一张 telegram_nft 类型的卡片，跳过 HTTP 抓取。
func (s *Service) resolveGiftLinkWebPage(ctx context.Context, normalizedURL string, urlHash int64, slug string) (domain.MessageWebPage, error) {
	unique, found, err := s.giftLinks.UniqueBySlug(ctx, slug)
	if err != nil {
		return domain.MessageWebPage{}, err // 瞬时失败：不缓存，GetOrLoad 允许重试。
	}
	if !found {
		return emptyWebPage(normalizedURL, urlHash), nil
	}
	title := strings.TrimSpace(unique.Title)
	if title == "" {
		title = "Collectible gift"
	}
	if unique.Num > 0 {
		title = fmt.Sprintf("%s #%d", title, unique.Num)
	}
	displayURL := strings.TrimPrefix(strings.TrimPrefix(normalizedURL, "https://"), "http://")
	return domain.MessageWebPage{
		State:      domain.MessageWebPageStateDone,
		ID:         urlHash,
		URL:        normalizedURL,
		DisplayURL: displayURL,
		Type:       "telegram_nft",
		SiteName:   s.giftLinkAppName,
		Title:      title,
		UniqueGift: &unique,
	}, nil
}

// WithWebPagePreview 启用链接预览抓取。maxBytes<=0 / ratePerMin<=0 用默认。SSRF 防护恒开。
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

// newWebpageFetcher 构造抓取器。allowPrivate 仅供测试（指向 httptest loopback）；生产恒 false。
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
			// SSRF 拦截是确定性失败 → 标记终态供负缓存（否则每次按键重打 PG+重 dial）。
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

// fetch 抓取 URL，返回 (字节, content-type)。SSRF 检查在 dial 阶段发生；ctx 承载共享总预算。
func (f *webpageFetcher) fetch(ctx context.Context, rawURL, accept string) ([]byte, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", terminalFetchErr("bad url") // 非法 URL 是终态
	}
	if !f.allowFetch() {
		return nil, "", fmt.Errorf("%w: rate limited", ErrWebPagePreviewInvalid) // 限速=瞬时，可重试
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", terminalFetchErr("bad request")
	}
	req.Header.Set("User-Agent", webpageUserAgent)
	req.Header.Set("Accept", accept)
	resp, err := f.client.Do(req)
	if err != nil {
		// SSRF 拦截（dial Control 返回的 terminal）经 url.Error 传上来，errors.Is 仍能识别；
		// 其余 dial/超时错误是瞬时。
		return nil, "", fmt.Errorf("%w: %w", ErrWebPagePreviewInvalid, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 4xx=确定性（404/403/410…）终态负缓存；5xx=瞬时可重试。
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

// WebPagePreviewEnabled 报告链接预览抓取是否启用。
func (s *Service) WebPagePreviewEnabled() bool {
	return s != nil && s.webpage != nil
}

// LookupWebPage 仅查缓存（L1 进程内 → L3 web_pages 表）返回已解析的链接预览，不抓取。命中
// 返回 (page,true)；未缓存或未启用返回 false。发送路径用它在 echo 直接带 done 卡片。
// 先 Peek L1：客户端输入时 getWebPagePreview 多半已把同一 URL 解析进 L1，发送时即免一次 PG。
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
	s.webpage.cache.Store(urlHash, page) // 回填 L1，后续 Peek 命中。
	return page, true
}

// ResolveWebPage 解析链接预览，经 L1 缓存（singleflight 去重）+ L3 web_pages 持久去重。
// 返回 done / empty 形态的 MessageWebPage；瞬时失败返回 error（调用方降级为空，不报错给用户）。
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
		// L3 durable 命中：直接复用，跨实例去重；超龄则后台刷新（返回旧卡片不阻塞）。
		if page, refreshedAt, found, err := s.media.GetWebPageByURLHash(ctx, urlHash); err == nil && found {
			s.webpage.maybeRefresh(s, normalized, urlHash, refreshedAt)
			return page, nil
		}
		// miss：抓取 + 解析（+ 图）。瞬时失败返回 error → GetOrLoad 不缓存（热门链接不被毒化）。
		page, err := s.webpage.resolve(ctx, s, normalized, urlHash)
		if err != nil {
			return domain.MessageWebPage{}, err
		}
		// 终态（done / empty）：落 L3 供跨实例 + 重启复用。
		if perr := s.media.PutWebPage(ctx, urlHash, page, int(time.Now().Unix())); perr != nil {
			s.log.Warn("persist web page preview failed", zap.Int64("url_hash", urlHash), zap.Error(perr))
		}
		return page, nil
	})
}

// maybeRefresh 在卡片超过 webPageRefreshTTL 龄时后台 stale-while-revalidate 刷新一次。
// 并发受 refreshSem 限；满则跳过（下次再刷）。瞬时失败保留旧卡片。
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
			return // 瞬时失败：保留旧卡片。
		}
		if perr := s.media.PutWebPage(ctx, urlHash, page, int(time.Now().Unix())); perr != nil {
			return
		}
		f.cache.Store(urlHash, page) // 刷新 L1，使后续读到新卡片。
	}()
}

// resolve 实际抓取并构造卡片。HTML 与预览图共享 ctx 总时长预算。
func (f *webpageFetcher) resolve(ctx context.Context, s *Service, normalizedURL string, urlHash int64) (domain.MessageWebPage, error) {
	// 我们自己的 /nft/{slug} 链接直接查库铸造卡片，不走下面的通用 HTTP 抓取+OG 解析——
	// 那条路径只对外部网站是对的，对内部链接会把服务端合成的静态预览图当成唯一信号，
	// 丢失客户端本可以本地渲染的完整 pattern/model/backdrop。
	if slug, ok := s.giftLinkSlug(normalizedURL); ok {
		return s.resolveGiftLinkWebPage(ctx, normalizedURL, urlHash, slug)
	}

	ctx, cancel := context.WithTimeout(ctx, webpageTotalTimeout)
	defer cancel()

	data, contentType, err := f.fetch(ctx, normalizedURL, acceptHTML)
	if err != nil {
		// 终态失败（SSRF/4xx/非法 URL）→ 负缓存为空预览，避免重复按键/发送重打 PG+外网。
		// 瞬时失败（5xx/超时/dial/限速）→ 上抛 error，GetOrLoad 不缓存、可重试。
		if isTerminalWebPageFetchError(err) {
			return emptyWebPage(normalizedURL, urlHash), nil
		}
		return domain.MessageWebPage{}, err
	}
	if !isHTMLContentType(contentType) {
		// 非 HTML（如直接指向图片/二进制）：终态空预览。
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

// fetchImage 抓取并铸造预览图（best-effort）。解码前按尺寸拦截解压炸弹；非图片/失败丢弃。
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

// webpageMeta 是从 HTML head 提取的预览元数据（已按 og>twitter>title/meta 优先级归并）。
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

// parseWebPageMeta 扫描 HTML head 的 <meta>/<title>，提取 OpenGraph/Twitter-card/标准元数据。
// 遇到 <body> 或 </head> 即停止（元数据都在 head）。og:image 相对 URL 按 baseURL 解析为绝对。
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

// metaKeyContent 从一个 <meta> 标签收集 (property|name) 与 content。
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

// webpageContentHash 对卡片内容算稳定 31-bit 哈希（webPage.hash 是 TL int，用于 getWebPage
// NotModified 短路）。仅覆盖文本字段，预览图变化不计入（同 URL 预览图按内容寻址已去重）。
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
