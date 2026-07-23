package files

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"

	"telesrv/internal/domain"
)

const maxEmojiAnimationBytes = 2 << 20

// DocumentAnimationJSON returns the Lottie JSON for an animated custom-emoji
// document, decompressing TGS (gzip) transparently. Non-emoji documents and
// documents without a stored blob return found=false. It backs the admin emoji
// browser preview and reuses the existing file-blob storage (doc:<id> key).
func (s *Service) DocumentAnimationJSON(ctx context.Context, documentID int64) ([]byte, bool, error) {
	if s == nil || s.media == nil || s.blobs == nil || documentID <= 0 {
		return nil, false, nil
	}
	doc, found, err := s.GetDocument(ctx, documentID)
	if err != nil {
		return nil, false, err
	}
	if !found || !documentIsCustomEmoji(doc) {
		return nil, false, nil
	}
	blob, found, err := s.media.GetFileBlob(ctx, fmt.Sprintf("doc:%d", documentID))
	if err != nil {
		return nil, false, err
	}
	if !found || blob.Size <= 0 || blob.Size > maxEmojiAnimationBytes {
		return nil, false, nil
	}
	data, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, 0, blob.Size)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) != total {
		return nil, false, nil
	}
	out, err := gunzipIfNeeded(data)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func documentIsCustomEmoji(doc domain.Document) bool {
	for _, a := range doc.Attributes {
		if a.Kind == domain.DocAttrCustomEmoji {
			return true
		}
	}
	return false
}

// gunzipIfNeeded transparently decompresses TGS (gzip-wrapped Lottie); raw JSON
// (non-gzip) is returned unchanged.
func gunzipIfNeeded(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open tgs gzip: %w", err)
	}
	defer gz.Close()
	out, err := io.ReadAll(io.LimitReader(gz, maxEmojiAnimationBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decompress tgs: %w", err)
	}
	if len(out) > maxEmojiAnimationBytes {
		return nil, fmt.Errorf("decompressed tgs too large")
	}
	return out, nil
}
