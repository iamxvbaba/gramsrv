package files

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"telesrv/internal/domain"
)

const (
	// tgsStickerMaxInputBytes bounds a .tgs file the server will inspect.
	// Telegram's own animated stickers are at most 64 KB; real-world .tgs
	// files are a little larger, so the cap is generous.
	tgsStickerMaxInputBytes = 1 << 20
	// tgsStickerMaxJSONBytes bounds the unpacked Lottie JSON.
	tgsStickerMaxJSONBytes = 8 << 20
	tgsStickerSide         = 512
)

// normalizeUploadedTGS turns an uploaded .tgs file into an animated sticker
// document, the way Telegram shows a .tgs sent as a file as a sticker. Only a
// gzip-packed Lottie animation qualifies; anything else stays a plain file.
func (s *Service) normalizeUploadedTGS(ctx context.Context, body AssembledUploadBlob, spec domain.DocumentSpec) domain.DocumentSpec {
	if !looksLikeTGSUpload(spec) || body.Size <= 0 || body.Size > tgsStickerMaxInputBytes {
		return spec
	}
	for _, attr := range spec.Attributes {
		if attr.Kind == domain.DocAttrSticker || attr.Kind == domain.DocAttrCustomEmoji {
			return spec
		}
	}
	data, total, err := s.blobs.GetRange(ctx, body.ObjectKey, 0, tgsStickerMaxInputBytes+1)
	if err != nil || total != body.Size || int64(len(data)) != body.Size || !validTGS(data) {
		return spec
	}
	spec.MimeType = "application/x-tgsticker"
	spec.ForceFile = false
	spec.Attributes = tgsStickerAttributes(spec.Attributes)
	return spec
}

func looksLikeTGSUpload(spec domain.DocumentSpec) bool {
	if strings.EqualFold(strings.TrimSpace(spec.MimeType), "application/x-tgsticker") {
		return true
	}
	for _, attr := range spec.Attributes {
		if attr.Kind == domain.DocAttrFilename && strings.EqualFold(filepath.Ext(strings.TrimSpace(attr.FileName)), ".tgs") {
			return true
		}
	}
	return false
}

// validTGS reports whether data is a gzip-packed Lottie animation.
func validTGS(data []byte) bool {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return false
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, tgsStickerMaxJSONBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > tgsStickerMaxJSONBytes {
		return false
	}
	var lottie struct {
		Version string            `json:"v"`
		Width   float64           `json:"w"`
		Height  float64           `json:"h"`
		InPoint *float64          `json:"ip"`
		OutPt   *float64          `json:"op"`
		Layers  []json.RawMessage `json:"layers"`
	}
	if err := json.Unmarshal(raw, &lottie); err != nil {
		return false
	}
	return lottie.Width > 0 && lottie.Height > 0 && lottie.InPoint != nil && lottie.OutPt != nil &&
		*lottie.OutPt > *lottie.InPoint && len(lottie.Layers) > 0
}

// tgsStickerAttributes keeps the file name and adds the sticker attributes.
func tgsStickerAttributes(attrs []domain.DocumentAttribute) []domain.DocumentAttribute {
	out := make([]domain.DocumentAttribute, 0, 3)
	for _, attr := range attrs {
		if attr.Kind == domain.DocAttrFilename {
			out = append(out, attr)
		}
	}
	return append(out,
		domain.DocumentAttribute{Kind: domain.DocAttrImageSize, W: tgsStickerSide, H: tgsStickerSide},
		domain.DocumentAttribute{Kind: domain.DocAttrSticker},
	)
}
