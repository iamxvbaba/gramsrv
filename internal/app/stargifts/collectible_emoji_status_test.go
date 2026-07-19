package stargifts

import (
	"bytes"
	"testing"

	"telesrv/internal/domain"
)

func TestCollectiblePatternUsesTextColorCustomEmojiAttribute(t *testing.T) {
	pattern := collectibleDocumentAttributes(domain.StarGiftCollectiblePattern)
	if len(pattern) != 3 || pattern[1].Kind != domain.DocAttrCustomEmoji || !pattern[1].TextColor {
		t.Fatalf("pattern attributes = %+v, want text-color custom emoji", pattern)
	}
	model := collectibleDocumentAttributes(domain.StarGiftCollectibleModel)
	if len(model) != 3 || model[1].Kind != domain.DocAttrSticker || model[1].TextColor {
		t.Fatalf("model attributes = %+v, want ordinary sticker", model)
	}
}

func TestCollectiblePatternHasInlinePathThumbForAndroidStaticPreview(t *testing.T) {
	pattern := collectibleDocumentThumbs(domain.StarGiftCollectiblePattern)
	if len(pattern) != 1 || pattern[0].Kind != domain.PhotoSizeKindPath ||
		pattern[0].Type != "j" || !bytes.Equal(pattern[0].Bytes, collectiblePatternPathThumb) {
		t.Fatalf("pattern thumbs = %+v, want inline path placeholder", pattern)
	}
	pattern[0].Bytes[0] ^= 0xff
	if bytes.Equal(pattern[0].Bytes, collectiblePatternPathThumb) {
		t.Fatal("collectibleDocumentThumbs returned shared mutable bytes")
	}
	if model := collectibleDocumentThumbs(domain.StarGiftCollectibleModel); len(model) != 0 {
		t.Fatalf("model thumbs = %+v, want no synthetic pattern placeholder", model)
	}
}
