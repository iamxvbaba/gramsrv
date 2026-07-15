package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestWebPageForURL 验证 getWebPage 决策：done 卡片→webPage；hash 匹配→webPageNotModified；
// 无预览/失败→webPageEmpty（带 URL）。
func TestWebPageForURL(t *testing.T) {
	ctx := context.Background()
	const url = "https://example.com/x"
	done := domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: 7, URL: url, DisplayURL: "example.com", Title: "T", Hash: 4242}

	files := &fakeFiles{resolveWebPageFn: func(string) (domain.MessageWebPage, error) { return done, nil }}
	r := &Router{deps: Deps{Files: files}}

	t.Run("done-card", func(t *testing.T) {
		res := r.webPageForURL(ctx, url, 0)
		page, ok := res.Webpage.(*tg.WebPage)
		if !ok {
			t.Fatalf("webpage = %T, want *tg.WebPage", res.Webpage)
		}
		if page.URL != url || page.Hash != 4242 {
			t.Errorf("page = %+v", page)
		}
	})

	t.Run("hash-match-not-modified", func(t *testing.T) {
		res := r.webPageForURL(ctx, url, 4242)
		if _, ok := res.Webpage.(*tg.WebPageNotModified); !ok {
			t.Fatalf("webpage = %T, want *tg.WebPageNotModified", res.Webpage)
		}
	})

	t.Run("error-empty", func(t *testing.T) {
		files.resolveWebPageFn = func(string) (domain.MessageWebPage, error) {
			return domain.MessageWebPage{}, errors.New("boom")
		}
		res := r.webPageForURL(ctx, url, 0)
		empty, ok := res.Webpage.(*tg.WebPageEmpty)
		if !ok {
			t.Fatalf("webpage = %T, want *tg.WebPageEmpty", res.Webpage)
		}
		if u, _ := empty.GetURL(); u != url {
			t.Errorf("empty url = %q, want %q", u, url)
		}
	})

	t.Run("cache-first-skips-resolve", func(t *testing.T) {
		// 缓存命中时不应再调 ResolveWebPage（避免请求路径阻塞抓取）。
		cf := &fakeFiles{
			lookupWebPageFn: func(u string) (domain.MessageWebPage, bool) {
				return domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: 1, URL: u, DisplayURL: "x", Title: "Cached", Hash: 7}, true
			},
			resolveWebPageFn: func(string) (domain.MessageWebPage, error) {
				t.Fatalf("ResolveWebPage must not be called on cache hit")
				return domain.MessageWebPage{}, nil
			},
		}
		rc := &Router{deps: Deps{Files: cf}}
		res := rc.webPageForURL(ctx, url, 0)
		page, ok := res.Webpage.(*tg.WebPage)
		if !ok {
			t.Fatalf("webpage = %T, want *tg.WebPage", res.Webpage)
		}
		if v, _ := page.GetTitle(); v != "Cached" {
			t.Errorf("title = %q, want Cached", v)
		}
	})

	t.Run("empty-state-empty", func(t *testing.T) {
		files.resolveWebPageFn = func(string) (domain.MessageWebPage, error) {
			return domain.MessageWebPage{State: domain.MessageWebPageStateEmpty, ID: 7, URL: url}, nil
		}
		res := r.webPageForURL(ctx, url, 0)
		if _, ok := res.Webpage.(*tg.WebPageEmpty); !ok {
			t.Fatalf("webpage = %T, want *tg.WebPageEmpty for empty state", res.Webpage)
		}
	})
}
