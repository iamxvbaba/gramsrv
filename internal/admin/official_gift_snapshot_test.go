package admin

import (
	"context"
	"os"
	"testing"

	stargiftapp "telesrv/internal/app/stargifts"
	"telesrv/internal/domain"
	"telesrv/internal/officialgifts"
	"telesrv/internal/store/memory"
)

// This opt-in regression uses the exact official snapshot reported by the
// Party Sparkler import issue. It crosses official catalog verification,
// admin mapping, animation materialization and the complete store validator.
func TestConfiguredOfficialPartySparklerImport(t *testing.T) {
	root := os.Getenv("TELESRV_TEST_OFFICIAL_GIFTS_DIR")
	if root == "" {
		t.Skip("TELESRV_TEST_OFFICIAL_GIFTS_DIR is not set")
	}
	ctx := context.Background()
	giftService := stargiftapp.NewService(memory.NewStarGiftStore(), &adminGiftBlob{data: map[string][]byte{}}, 2)
	svc := NewService(Dependencies{
		Commands:      newMemoryCommandRepo(),
		Gifts:         giftService,
		OfficialGifts: officialgifts.New(root),
		Now:           fixedNow,
	})
	result, err := svc.ImportOfficialStarGift(ctx, ImportOfficialStarGiftRequest{
		CommandMeta: CommandMeta{
			CommandID: "exec-party-sparkler-snapshot-regression",
			Actor:     "test",
			Reason:    "verify official collectible import",
		},
		SourceGiftID:       "6003643167683903930",
		Enabled:            true,
		IncludeCollectible: true,
	})
	if err != nil {
		t.Fatalf("import Party Sparkler: result=%+v err=%v", result, err)
	}
	if result.Details["models"] != 100 || result.Details["patterns"] != 136 || result.Details["backdrops"] != 60 {
		t.Fatalf("Party Sparkler details = %+v", result.Details)
	}
	catalog, err := giftService.Catalog(ctx)
	if err != nil || len(catalog) != 1 {
		t.Fatalf("catalog=%+v err=%v, want one gift", catalog, err)
	}
	preview, ok, err := giftService.CollectiblePreview(ctx, catalog[0].ID)
	if err != nil || !ok || len(preview.Models) != 100 || len(preview.Patterns) != 136 || len(preview.Backdrops) != 60 {
		t.Fatalf("preview counts=%d/%d/%d ok=%v err=%v", len(preview.Models), len(preview.Patterns), len(preview.Backdrops), ok, err)
	}
	for _, model := range preview.Models {
		if model.Document == nil || !model.Document.IsSticker() || model.Document.IsCustomEmoji() {
			t.Fatalf("model %q document=%+v, want ordinary sticker", model.Name, model.Document)
		}
	}
	for _, pattern := range preview.Patterns {
		if pattern.Document == nil || pattern.Document.IsSticker() || !pattern.Document.IsCustomEmoji() ||
			!hasTextColorCustomEmoji(pattern.Document.Attributes) || !hasInlinePathThumb(pattern.Document.Thumbs) {
			t.Fatalf("pattern %q document=%+v, want text-color custom emoji with inline path", pattern.Name, pattern.Document)
		}
	}
}

func hasTextColorCustomEmoji(attributes []domain.DocumentAttribute) bool {
	for _, attribute := range attributes {
		if attribute.Kind == domain.DocAttrCustomEmoji && attribute.TextColor {
			return true
		}
	}
	return false
}

func hasInlinePathThumb(thumbs []domain.PhotoSize) bool {
	for _, thumb := range thumbs {
		if thumb.Kind == domain.PhotoSizeKindPath && thumb.Type != "" && len(thumb.Bytes) > 0 {
			return true
		}
	}
	return false
}
