package files

import (
	"context"
	"path/filepath"
	"testing"

	"telesrv/internal/domain"
)

// The fixture is the golden output of cmd/stickerfetch's reactions export
// (TestWriteReactionsExportMatchesGolden), so a format change on either side
// breaks one of the two tests.
func TestSeedMediaImportsStickerfetchReactionsExport(t *testing.T) {
	root := filepath.Join("..", "..", "..", "cmd", "stickerfetch", "testdata", "reactions_export")
	media := newFakeMediaStore()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	svc := NewService(media, local, 2)
	stats, err := svc.SeedMedia(context.Background(), root, 0)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if stats.Reactions != 2 {
		t.Fatalf("stats = %+v, want 2 reactions", stats)
	}
	reactions, err := media.ListAvailableReactions(context.Background())
	if err != nil {
		t.Fatalf("list reactions: %v", err)
	}
	if len(reactions) != 2 {
		t.Fatalf("got %d reactions, want 2", len(reactions))
	}
	byReaction := map[string]domain.AvailableReaction{}
	for _, r := range reactions {
		byReaction[r.Reaction] = r
	}
	like, fire := byReaction["👍"], byReaction["🔥"]
	if like.Title != "Thumbs Up" || like.StaticIconID != 1000001 || like.SelectAnimationID != 1000003 ||
		like.AroundAnimationID != 3000001 || like.CenterIconID != 1000006 {
		t.Fatalf("👍 = %+v", like)
	}
	if !fire.Premium || fire.AroundAnimationID != 3000001 || fire.CenterIconID != 0 {
		t.Fatalf("🔥 = %+v", fire)
	}

	doc, ok, err := media.GetDocument(context.Background(), 1000003)
	if err != nil || !ok {
		t.Fatalf("select animation document ok=%v err=%v", ok, err)
	}
	if doc.MimeType != "application/x-tgsticker" || doc.Size != int64(len("document 1000003")) {
		t.Fatalf("document = %+v", doc)
	}
	var path, size bool
	for _, th := range doc.Thumbs {
		switch {
		case th.Kind == domain.PhotoSizeKindPath && th.Type == "j" && len(th.Bytes) == 2:
			path = true
		case th.Type == "m" && th.W == 128 && string(th.Bytes) == "thumb 1000003 m":
			// small renditions are inlined as photoCachedSize
			size = true
		}
	}
	if !path || !size {
		t.Fatalf("thumbs = %+v, want the path and the downloaded m size", doc.Thumbs)
	}
	for _, key := range []string{"doc:1000003", "doc:1000003:m"} {
		if _, ok, _ := media.GetFileBlob(context.Background(), key); !ok {
			t.Fatalf("blob %s not imported", key)
		}
	}
}
