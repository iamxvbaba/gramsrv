package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// TestResolveInputMediaExternal 验证 inputMediaPhoto/DocumentExternal 路由到 Files
// 外链抓取，并把结果投影为 photo/document 媒体；空 URL（fakeFiles 拒）→ MEDIA_INVALID。
func TestResolveInputMediaExternal(t *testing.T) {
	r := &Router{deps: Deps{Files: &fakeFiles{}}}
	ctx := context.Background()

	// 外链图片。
	media, err := r.resolveInputMedia(ctx, 1001, &tg.InputMediaPhotoExternal{URL: "https://example.com/cat.png", Spoiler: true})
	if err != nil {
		t.Fatalf("photo external: %v", err)
	}
	if media == nil || media.Kind != domain.MessageMediaKindPhoto || media.Photo == nil || !media.Spoiler {
		t.Fatalf("photo external media = %+v, want spoiler photo", media)
	}

	// 外链文档。
	media, err = r.resolveInputMedia(ctx, 1001, &tg.InputMediaDocumentExternal{URL: "https://example.com/doc.pdf"})
	if err != nil {
		t.Fatalf("document external: %v", err)
	}
	if media == nil || media.Kind != domain.MessageMediaKindDocument || media.Document == nil {
		t.Fatalf("document external media = %+v, want document", media)
	}

	// 空 URL（抓取失败）→ MEDIA_INVALID。
	if _, err := r.resolveInputMedia(ctx, 1001, &tg.InputMediaPhotoExternal{URL: ""}); !tgerr.Is(err, "MEDIA_INVALID") {
		t.Fatalf("empty url photo err = %v, want MEDIA_INVALID", err)
	}
}
