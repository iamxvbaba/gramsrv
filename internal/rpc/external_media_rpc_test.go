package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// TestResolveInputMediaExternal verifies inputMediaPhoto/DocumentExternal routes to
// the Files external-link fetch and projects the result into photo/document media;
// an empty URL (rejected by fakeFiles) → MEDIA_INVALID.
func TestResolveInputMediaExternal(t *testing.T) {
	r := &Router{deps: Deps{Files: &fakeFiles{}}}
	ctx := context.Background()

	// External-link photo.
	media, err := r.resolveInputMedia(ctx, 1001, &tg.InputMediaPhotoExternal{URL: "https://example.com/cat.png", Spoiler: true})
	if err != nil {
		t.Fatalf("photo external: %v", err)
	}
	if media == nil || media.Kind != domain.MessageMediaKindPhoto || media.Photo == nil || !media.Spoiler {
		t.Fatalf("photo external media = %+v, want spoiler photo", media)
	}

	// External-link document.
	media, err = r.resolveInputMedia(ctx, 1001, &tg.InputMediaDocumentExternal{URL: "https://example.com/doc.pdf"})
	if err != nil {
		t.Fatalf("document external: %v", err)
	}
	if media == nil || media.Kind != domain.MessageMediaKindDocument || media.Document == nil {
		t.Fatalf("document external media = %+v, want document", media)
	}

	// Empty URL (fetch fails) → MEDIA_INVALID.
	if _, err := r.resolveInputMedia(ctx, 1001, &tg.InputMediaPhotoExternal{URL: ""}); !tgerr.Is(err, "MEDIA_INVALID") {
		t.Fatalf("empty url photo err = %v, want MEDIA_INVALID", err)
	}
}
