package files

import (
	"context"
	"fmt"
	"strings"

	"telesrv/internal/domain"
)

const (
	stickerMaterialMimeTGS   = "application/x-tgsticker"
	stickerMaterialMimeWebP  = "image/webp"
	stickerMaterialMimeWebM  = "video/webm"
	stickerMaterialMimeMP4   = "video/mp4"
	stickerMaterialMimeJSON  = "application/json"
	stickerMaterialMimeOctet = "application/octet-stream"
)

func normalizeStickerMaterialDocumentMIME(doc domain.Document) (domain.Document, bool) {
	mimeType := canonicalStickerMaterialMime(doc.StickerSetMaterialMime())
	if mimeType == "" || mimeType == stickerMaterialMimeJSON {
		return doc, false
	}
	if !shouldReplaceStickerMaterialMime(doc.MimeType, mimeType) {
		return doc, false
	}
	doc.MimeType = mimeType
	return doc, true
}

func (s *Service) materialDocumentForStickerSet(ctx context.Context, doc domain.Document, targetSetID int64) (domain.Document, error) {
	if ownedSetID, _, ok := doc.StickerSetRef(); ok && ownedSetID != 0 && ownedSetID != targetSetID {
		return s.cloneStickerSetDocument(ctx, doc)
	}
	return doc, nil
}

func (s *Service) cloneStickerSetDocument(ctx context.Context, source domain.Document) (domain.Document, error) {
	copied := copyDocuments([]domain.Document{source})
	if len(copied) == 0 {
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	clone := copied[0]
	oldID := clone.ID
	clone.ID = randomID()
	clone.AccessHash = randomID()
	clone.FileReference = randomFileReference()

	if source.Size > 0 {
		blob, found, err := s.media.GetFileBlob(ctx, fmt.Sprintf("doc:%d", oldID))
		if err != nil {
			return domain.Document{}, err
		}
		if !found {
			return domain.Document{}, domain.ErrStickerSetFileInvalid
		}
		blob.LocationKey = fmt.Sprintf("doc:%d", clone.ID)
		if normalized, changed := normalizeStickerMaterialDocumentMIME(clone); changed {
			clone = normalized
		}
		if mimeType := canonicalStickerMaterialMime(clone.StickerSetMaterialMime()); mimeType != "" && shouldReplaceStickerMaterialMime(blob.MimeType, mimeType) {
			blob.MimeType = mimeType
		}
		if err := s.media.PutFileBlob(ctx, blob); err != nil {
			return domain.Document{}, err
		}
		s.blobCache.put(blob.LocationKey, blob)
	}

	for _, thumb := range source.Thumbs {
		if !thumb.Downloadable() || thumb.Type == "" {
			continue
		}
		oldKey := fmt.Sprintf("doc:%d:%s", oldID, thumb.Type)
		blob, found, err := s.media.GetFileBlob(ctx, oldKey)
		if err != nil {
			return domain.Document{}, err
		}
		if !found {
			continue
		}
		blob.LocationKey = fmt.Sprintf("doc:%d:%s", clone.ID, thumb.Type)
		if err := s.media.PutFileBlob(ctx, blob); err != nil {
			return domain.Document{}, err
		}
		s.blobCache.put(blob.LocationKey, blob)
	}

	return clone, nil
}

func (s *Service) ensureStickerMaterialMIME(ctx context.Context, doc domain.Document, mimeType string) (domain.Document, error) {
	mimeType = canonicalStickerMaterialMime(mimeType)
	if mimeType == "" || mimeType == stickerMaterialMimeJSON {
		return doc, nil
	}
	if shouldReplaceStickerMaterialMime(doc.MimeType, mimeType) {
		doc.MimeType = mimeType
	}
	if s == nil || s.media == nil || doc.ID == 0 {
		return doc, nil
	}
	blob, found, err := s.media.GetFileBlob(ctx, fmt.Sprintf("doc:%d", doc.ID))
	if err != nil {
		return domain.Document{}, err
	}
	if found && shouldReplaceStickerMaterialMime(blob.MimeType, mimeType) {
		blob.MimeType = mimeType
		if err := s.media.PutFileBlob(ctx, blob); err != nil {
			return domain.Document{}, err
		}
		s.blobCache.put(blob.LocationKey, blob)
	}
	return doc, nil
}

func canonicalStickerMaterialMime(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case stickerMaterialMimeTGS:
		return stickerMaterialMimeTGS
	case stickerMaterialMimeWebP:
		return stickerMaterialMimeWebP
	case stickerMaterialMimeWebM:
		return stickerMaterialMimeWebM
	case stickerMaterialMimeMP4:
		return stickerMaterialMimeMP4
	case stickerMaterialMimeJSON, "text/json", "application/lottie+json":
		return stickerMaterialMimeJSON
	default:
		return ""
	}
}

func shouldReplaceStickerMaterialMime(current, inferred string) bool {
	inferred = canonicalStickerMaterialMime(inferred)
	if inferred == "" {
		return false
	}
	current = strings.ToLower(strings.TrimSpace(current))
	return current == "" || current == stickerMaterialMimeOctet
}
