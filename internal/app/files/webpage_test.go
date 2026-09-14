package files

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// newWebpageTestService 构造一个带 loopback-allowed 抓取器的 Service（生产恒禁 loopback）。
func newWebpageTestService(t *testing.T, allowPrivate bool) *Service {
	t.Helper()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2)
	svc.webpage = newWebpageFetcher(DefaultWebPagePreviewMaxBytes, 600, allowPrivate)
	return svc
}

func TestResolveWebPageDoneCardWithImage(t *testing.T) {
	imgBytes := testJPEG(t, 8, 6)
	var pageHits, imgHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/img.jpg", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&imgHits, 1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imgBytes)
	})
	mux.HandleFunc("/article", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&pageHits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head>
			<title>Fallback Title</title>
			<meta property="og:title" content="OG Title">
			<meta property="og:description" content="OG Description">
			<meta property="og:site_name" content="Example Site">
			<meta property="og:type" content="article">
			<meta property="og:image" content="/img.jpg">
		</head><body>ignored body</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := newWebpageTestService(t, true)
	ctx := context.Background()
	pageURL := srv.URL + "/article"

	page, err := svc.ResolveWebPage(ctx, pageURL)
	if err != nil {
		t.Fatalf("ResolveWebPage: %v", err)
	}
	if page.State != domain.MessageWebPageStateDone {
		t.Fatalf("state = %q, want done", page.State)
	}
	if page.Title != "OG Title" || page.Description != "OG Description" || page.SiteName != "Example Site" || page.Type != "article" {
		t.Fatalf("card fields = %+v", page)
	}
	if page.Photo == nil || page.Photo.ID == 0 || len(page.Photo.Sizes) == 0 {
		t.Fatalf("expected minted preview photo, got %+v", page.Photo)
	}
	// id == url_hash（保证 pending↔done 关联）。
	normalized, _ := domain.NormalizeWebPageURL(pageURL)
	if page.ID != domain.WebPageURLHash(normalized) {
		t.Fatalf("webPage id %d != url_hash %d", page.ID, domain.WebPageURLHash(normalized))
	}

	// 第二次解析命中 L1 缓存（singleflight/LRU），不再打上游。
	if _, err := svc.ResolveWebPage(ctx, pageURL); err != nil {
		t.Fatalf("second ResolveWebPage: %v", err)
	}
	if h := atomic.LoadInt32(&pageHits); h != 1 {
		t.Fatalf("page fetched %d times, want 1 (cache dedup)", h)
	}
	if h := atomic.LoadInt32(&imgHits); h != 1 {
		t.Fatalf("image fetched %d times, want 1", h)
	}
}

// TestWebPageMaybeRefreshStale 验证超龄卡片触发后台 stale-while-revalidate 刷新（写回 done）。
func TestWebPageMaybeRefreshStale(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/article", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><meta property="og:title" content="Fresh"></head></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	svc := newWebpageTestService(t, true)
	normalized, _ := domain.NormalizeWebPageURL(srv.URL + "/article")
	urlHash := domain.WebPageURLHash(normalized)

	// 触发刷新（refreshedAt=1 → 远超 TTL）。
	svc.webpage.maybeRefresh(svc, normalized, urlHash, 1)

	// 轮询直到刷新写回 done 卡片。
	var ok bool
	for i := 0; i < 100; i++ {
		if page, _, found, err := svc.media.GetWebPageByURLHash(ctx, urlHash); err == nil && found && page.State == domain.MessageWebPageStateDone && page.Title == "Fresh" {
			ok = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("stale refresh did not write fresh done card")
	}

	// refreshedAt=0 或新鲜 → 不刷新（不 panic、立即返回）。
	svc.webpage.maybeRefresh(svc, normalized, urlHash, 0)
	svc.webpage.maybeRefresh(svc, normalized, urlHash, int(time.Now().Unix()))
}

// TestResolveWebPageTerminalFailureNegativeCached 验证 4xx（终态）解析为空预览并负缓存——
// 第二次不再打上游（否则每次按键/发送重复抓取坏 URL）。
func TestResolveWebPageTerminalFailureNegativeCached(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	svc := newWebpageTestService(t, true)
	ctx := context.Background()

	page, err := svc.ResolveWebPage(ctx, srv.URL+"/x")
	if err != nil {
		t.Fatalf("404 should be terminal-empty (not error): %v", err)
	}
	if page.State != domain.MessageWebPageStateEmpty {
		t.Fatalf("state = %q, want empty", page.State)
	}
	if _, err := svc.ResolveWebPage(ctx, srv.URL+"/x"); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if h := atomic.LoadInt32(&hits); h != 1 {
		t.Fatalf("terminal URL fetched %d times, want 1 (negative cached)", h)
	}
}

// TestResolveWebPageTransientFailureNotCached 验证 5xx（瞬时）返回 error 且不缓存——可重试。
func TestResolveWebPageTransientFailureNotCached(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	svc := newWebpageTestService(t, true)
	ctx := context.Background()

	if _, err := svc.ResolveWebPage(ctx, srv.URL+"/x"); err == nil {
		t.Fatalf("500 should return transient error")
	}
	_, _ = svc.ResolveWebPage(ctx, srv.URL+"/x")
	if h := atomic.LoadInt32(&hits); h != 2 {
		t.Fatalf("transient URL fetched %d times, want 2 (not cached, retryable)", h)
	}
}

func TestResolveWebPageNoMetadataIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head></head><body>no meta here</body></html>`)
	}))
	defer srv.Close()

	svc := newWebpageTestService(t, true)
	page, err := svc.ResolveWebPage(context.Background(), srv.URL+"/x")
	if err != nil {
		t.Fatalf("ResolveWebPage: %v", err)
	}
	if page.State != domain.MessageWebPageStateEmpty {
		t.Fatalf("state = %q, want empty", page.State)
	}
}

func TestResolveWebPageNonHTMLIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4 binary"))
	}))
	defer srv.Close()

	svc := newWebpageTestService(t, true)
	page, err := svc.ResolveWebPage(context.Background(), srv.URL+"/doc")
	if err != nil {
		t.Fatalf("ResolveWebPage: %v", err)
	}
	if page.State != domain.MessageWebPageStateEmpty {
		t.Fatalf("state = %q, want empty for non-HTML", page.State)
	}
}

// TestResolveWebPageSSRFBlocksLoopback 验证生产配置（allowPrivate=false）拦截指向 loopback 的 URL，
// 并把该确定性失败收敛为空预览。
func TestResolveWebPageSSRFBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><meta property="og:title" content="secret"></head></html>`)
	}))
	defer srv.Close()

	svc := newWebpageTestService(t, false) // 生产口径：禁 loopback
	page, err := svc.ResolveWebPage(context.Background(), srv.URL+"/x")
	if err != nil {
		t.Fatalf("SSRF guard should resolve as terminal-empty: %v", err)
	}
	if page.State != domain.MessageWebPageStateEmpty {
		t.Fatalf("state = %q, want empty", page.State)
	}
}

func TestResolveWebPageDisabled(t *testing.T) {
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2) // 未启用 webpage 抓取
	if _, err := svc.ResolveWebPage(context.Background(), "https://example.com"); err == nil {
		t.Fatalf("expected ErrWebPagePreviewDisabled")
	}
}

type fakeUniqueGiftLookup struct {
	bySlug map[string]domain.UniqueStarGift
}

func (f fakeUniqueGiftLookup) UniqueBySlug(_ context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	g, ok := f.bySlug[slug]
	return g, ok, nil
}

// TestResolveWebPageGiftLinkFastPath 验证 /nft/{slug} 链接直接查库铸造 telegram_nft 卡片，
// 完全不打 HTTP（httptest server 上没挂任何 handler，命中就会 404）。
func TestResolveWebPageGiftLinkFastPath(t *testing.T) {
	// 用一个没挂任何 handler 的 server 当"我们自己的站点"：一旦快路径失手落回 HTTP 抓取，
	// 请求会打空路由 404，断言就会因为不是 telegram_nft 卡片而失败。
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	svc := newWebpageTestService(t, true)
	svc.SetGiftLinkPreview(srv.URL, "ShuzaGram", fakeUniqueGiftLookup{bySlug: map[string]domain.UniqueStarGift{
		"chill-flame-1": {Title: "Chill Flame", Num: 168424},
	}})

	page, err := svc.ResolveWebPage(context.Background(), srv.URL+"/nft/chill-flame-1")
	if err != nil {
		t.Fatalf("ResolveWebPage: %v", err)
	}
	if page.State != domain.MessageWebPageStateDone {
		t.Fatalf("state = %q, want done", page.State)
	}
	if page.Type != "telegram_nft" {
		t.Fatalf("type = %q, want telegram_nft", page.Type)
	}
	if page.SiteName != "ShuzaGram" {
		t.Fatalf("site name = %q, want ShuzaGram", page.SiteName)
	}
	if page.Title != "Chill Flame #168424" {
		t.Fatalf("title = %q, want %q", page.Title, "Chill Flame #168424")
	}
	if page.UniqueGift == nil || page.UniqueGift.Title != "Chill Flame" {
		t.Fatalf("UniqueGift = %+v, want the resolved gift snapshot", page.UniqueGift)
	}
}

// TestResolveWebPageGiftLinkFastPathNotFound 验证未知 slug 收敛为空预览而不是报错。
func TestResolveWebPageGiftLinkFastPathNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	svc := newWebpageTestService(t, true)
	svc.SetGiftLinkPreview(srv.URL, "ShuzaGram", fakeUniqueGiftLookup{bySlug: map[string]domain.UniqueStarGift{}})

	page, err := svc.ResolveWebPage(context.Background(), srv.URL+"/nft/does-not-exist")
	if err != nil {
		t.Fatalf("ResolveWebPage: %v", err)
	}
	if page.State != domain.MessageWebPageStateEmpty {
		t.Fatalf("state = %q, want empty", page.State)
	}
}

// TestResolveWebPageGiftLinkFastPathDoesNotShadowOtherPaths 验证只有 /nft/ 路径走快路径：
// 同一站点的其它路径仍然照常发起 HTTP 抓取。
func TestResolveWebPageGiftLinkFastPathDoesNotShadowOtherPaths(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/other", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><meta property="og:title" content="Other Page"></head></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := newWebpageTestService(t, true)
	svc.SetGiftLinkPreview(srv.URL, "ShuzaGram", fakeUniqueGiftLookup{bySlug: map[string]domain.UniqueStarGift{
		"chill-flame-1": {Title: "Chill Flame", Num: 168424},
	}})

	page, err := svc.ResolveWebPage(context.Background(), srv.URL+"/other")
	if err != nil {
		t.Fatalf("ResolveWebPage: %v", err)
	}
	if page.Type == "telegram_nft" || page.UniqueGift != nil {
		t.Fatalf("non-/nft/ path incorrectly took the gift-link fast path: %+v", page)
	}
	if page.Title != "Other Page" {
		t.Fatalf("title = %q, want Other Page (should have gone through normal HTTP fetch)", page.Title)
	}
}
