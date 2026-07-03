package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	aiapp "telesrv/internal/app/ai"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestWebPagePreviewMedia 验证 getWebPagePreview 的 media 决策：done 卡片→messageMediaWebPage，
// 无链接/解析失败/非 done 一律 messageMediaEmpty（绝不返回 pending、绝不报错）。
func TestWebPagePreviewMedia(t *testing.T) {
	ctx := context.Background()
	urlEntities := []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 0, Length: 9}} // "https://x"

	doneCard := domain.MessageWebPage{
		State:      domain.MessageWebPageStateDone,
		ID:         42,
		URL:        "https://x",
		DisplayURL: "x",
		Title:      "Hello",
		Photo: &domain.Photo{
			ID: 7, AccessHash: 8, DCID: 2,
			Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 320, H: 200, Size: 1024}},
		},
	}

	files := &fakeFiles{resolveWebPageFn: func(string) (domain.MessageWebPage, error) { return doneCard, nil }}
	r := &Router{deps: Deps{Files: files}}

	t.Run("done-card", func(t *testing.T) {
		media := r.webPagePreviewMedia(ctx, "https://x", urlEntities)
		wrap, ok := media.(*tg.MessageMediaWebPage)
		if !ok {
			t.Fatalf("media = %T, want *tg.MessageMediaWebPage", media)
		}
		page, ok := wrap.Webpage.(*tg.WebPage)
		if !ok {
			t.Fatalf("webpage = %T, want *tg.WebPage", wrap.Webpage)
		}
		if v, _ := page.GetTitle(); v != "Hello" {
			t.Fatalf("title = %q, want Hello", v)
		}
	})

	t.Run("no-url", func(t *testing.T) {
		if media := r.webPagePreviewMedia(ctx, "plain text", nil); !isEmptyMedia(media) {
			t.Fatalf("media = %T, want empty", media)
		}
	})

	t.Run("resolve-error-degrades", func(t *testing.T) {
		files.resolveWebPageFn = func(string) (domain.MessageWebPage, error) {
			return domain.MessageWebPage{}, errors.New("boom")
		}
		if media := r.webPagePreviewMedia(ctx, "https://x", urlEntities); !isEmptyMedia(media) {
			t.Fatalf("media = %T, want empty on error", media)
		}
	})

	t.Run("non-done-state-degrades", func(t *testing.T) {
		files.resolveWebPageFn = func(string) (domain.MessageWebPage, error) {
			return domain.MessageWebPage{State: domain.MessageWebPageStateEmpty, ID: 1, URL: "https://x"}, nil
		}
		if media := r.webPagePreviewMedia(ctx, "https://x", urlEntities); !isEmptyMedia(media) {
			t.Fatalf("media = %T, want empty for empty-state webpage", media)
		}
	})
}

func TestAIComposeStyleWebPagePreview(t *testing.T) {
	const userID int64 = 1001
	ctx := WithUserID(context.Background(), userID)
	aiSvc := aiapp.NewService(memory.NewAIComposeStore())
	tone, err := aiSvc.CreateTone(ctx, domain.AIComposeToneInput{
		UserID:  userID,
		EmojiID: 12345,
		Title:   "Sharp",
		Prompt:  "Make it crisp.",
	})
	if err != nil {
		t.Fatalf("CreateTone: %v", err)
	}
	r := New(Config{}, Deps{AICompose: aiSvc}, zaptest.NewLogger(t), clock.System)
	link := "https://t.me/addstyle/" + tone.Slug

	media := r.webPagePreviewMedia(ctx, "try "+link, nil)
	wrap, ok := media.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("media = %T, want *tg.MessageMediaWebPage", media)
	}
	page, ok := wrap.Webpage.(*tg.WebPage)
	if !ok {
		t.Fatalf("webpage = %T, want *tg.WebPage", wrap.Webpage)
	}
	if typ, ok := page.GetType(); !ok || typ != "telegram_aicomposetone" {
		t.Fatalf("type = %q ok=%v, want telegram_aicomposetone", typ, ok)
	}
	if title, ok := page.GetTitle(); !ok || title != "Sharp" {
		t.Fatalf("title = %q ok=%v, want Sharp", title, ok)
	}
	attrs, ok := page.GetAttributes()
	if !ok || len(attrs) != 1 {
		t.Fatalf("attributes = %#v ok=%v, want one", attrs, ok)
	}
	attr, ok := attrs[0].(*tg.WebPageAttributeAiComposeTone)
	if !ok || attr.EmojiID != 12345 {
		t.Fatalf("attribute = %#v, want ai compose tone emoji", attrs[0])
	}

	got := r.webPageForURL(ctx, "https://t.me/addstyle?slug="+tone.Slug, 0)
	if page, ok := got.Webpage.(*tg.WebPage); !ok {
		t.Fatalf("getWebPage webpage = %T, want *tg.WebPage", got.Webpage)
	} else if typ, ok := page.GetType(); !ok || typ != "telegram_aicomposetone" {
		t.Fatalf("getWebPage type = %q ok=%v", typ, ok)
	}
}

func isEmptyMedia(m tg.MessageMediaClass) bool {
	_, ok := m.(*tg.MessageMediaEmpty)
	return ok
}
