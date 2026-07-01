package domain

import "testing"

func TestDocumentIsStickerSetMaterialAcceptsUploadedFormats(t *testing.T) {
	tests := []struct {
		name     string
		doc      Document
		want     bool
		wantMime string
	}{
		{
			name:     "existing sticker",
			doc:      Document{ID: 1, AccessHash: 11, Size: MaxStickerMaterialDocumentSize + 1, Attributes: []DocumentAttribute{{Kind: DocAttrSticker}}},
			want:     true,
			wantMime: "",
		},
		{
			name:     "tgs mime",
			doc:      Document{ID: 2, AccessHash: 22, MimeType: "application/x-tgsticker", Size: 4096},
			want:     true,
			wantMime: "application/x-tgsticker",
		},
		{
			name:     "webp extension",
			doc:      Document{ID: 3, AccessHash: 33, MimeType: "application/octet-stream", Size: 4096, Attributes: []DocumentAttribute{{Kind: DocAttrFilename, FileName: "sticker.WEBP"}}},
			want:     true,
			wantMime: "image/webp",
		},
		{
			name:     "lottie json mime",
			doc:      Document{ID: 31, AccessHash: 331, MimeType: "application/lottie+json", Size: 4096},
			want:     true,
			wantMime: "application/json",
		},
		{
			name:     "lottie json extension",
			doc:      Document{ID: 32, AccessHash: 332, MimeType: "application/octet-stream", Size: 4096, Attributes: []DocumentAttribute{{Kind: DocAttrFilename, FileName: "wave.JSON"}}},
			want:     true,
			wantMime: "application/json",
		},
		{
			name:     "mp4 mime",
			doc:      Document{ID: 4, AccessHash: 44, MimeType: "video/mp4", Size: 4096, Attributes: []DocumentAttribute{{Kind: DocAttrVideo, W: 512, H: 512, Duration: 1}}},
			want:     true,
			wantMime: "video/mp4",
		},
		{
			name:     "mp4 without video attribute",
			doc:      Document{ID: 40, AccessHash: 440, MimeType: "video/mp4", Size: 4096},
			wantMime: "video/mp4",
		},
		{
			name:     "arbitrary file",
			doc:      Document{ID: 5, AccessHash: 55, MimeType: "application/pdf", Size: 4096},
			wantMime: "application/pdf",
		},
		{
			name:     "oversized upload",
			doc:      Document{ID: 6, AccessHash: 66, MimeType: "image/webp", Size: MaxStickerMaterialDocumentSize + 1},
			wantMime: "image/webp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.doc.IsStickerSetMaterial(); got != tt.want {
				t.Fatalf("IsStickerSetMaterial() = %v, want %v", got, tt.want)
			}
			if got := tt.doc.StickerSetMaterialMime(); got != tt.wantMime {
				t.Fatalf("StickerSetMaterialMime() = %q, want %q", got, tt.wantMime)
			}
		})
	}
}
