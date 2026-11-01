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

// newWebpageTestService builds a Service with a loopback-allowed fetcher (production always blocks loopback).
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
	// id == url_hash (ensuring the pending↔done association).
	normalized, _ := domain.NormalizeWebPageURL(pageURL)
	if page.ID != domain.WebPageURLHash(normalized) {
		t.Fatalf("webPage id %d != url_hash %d", page.ID, domain.WebPageURLHash(normalized))
	}

	// The second resolution hits the L1 cache (singleflight/LRU) and no longer hits upstream.
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

// TestWebPageMaybeRefreshStale verifies that an over-age card triggers a background stale-while-revalidate refresh (writing back done).
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

	// Trigger refresh (refreshedAt=1 → far beyond the TTL).
	svc.webpage.maybeRefresh(svc, normalized, urlHash, 1)

	// Poll until the refresh writes back the done card.
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

	// refreshedAt=0 or fresh → no refresh (no panic, returns immediately).
	svc.webpage.maybeRefresh(svc, normalized, urlHash, 0)
	svc.webpage.maybeRefresh(svc, normalized, urlHash, int(time.Now().Unix()))
}

// TestResolveWebPageTerminalFailureNegativeCached verifies that a 4xx (terminal) resolves to an empty
// preview and is negatively cached — the second time no longer hits upstream (otherwise a bad URL is
// re-fetched on every keystroke/send).
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

// TestResolveWebPageTransientFailureNotCached verifies that a 5xx (transient) returns an error and is not cached — retryable.
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

// TestResolveWebPageSSRFBlocksLoopback verifies that the production config (allowPrivate=false)
// blocks a URL pointing at loopback and converges that deterministic failure to an empty preview.
func TestResolveWebPageSSRFBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><meta property="og:title" content="secret"></head></html>`)
	}))
	defer srv.Close()

	svc := newWebpageTestService(t, false) // production posture: loopback disabled
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
	svc := NewService(media, blobs, 2) // webpage fetching not enabled
	if _, err := svc.ResolveWebPage(context.Background(), "https://example.com"); err == nil {
		t.Fatalf("expected ErrWebPagePreviewDisabled")
	}
}
