package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func (s *Service) ValidateGifUpload(_ string, data []byte) (string, bool) {
	if len(data) == 0 || int64(len(data)) > domain.MaxGifCatalogUploadSize {
		return "", false
	}
	return detectGifCatalogUploadMime(data)
}

// AdminUploadGifMaterial always normalizes to silent H.264 MP4. The blob is
// written only to the explicitly selected Service backend; there is no cross-
// backend retry or read fallback.
func (s *Service) AdminUploadGifMaterial(ctx context.Context, fileName string, data []byte) (domain.Document, error) {
	if _, ok := s.ValidateGifUpload(fileName, data); !ok || s.gifs == nil {
		return domain.Document{}, domain.ErrGifCatalogFileInvalid
	}
	converted, err := s.gifs.Transcode(ctx, data)
	if err != nil || len(converted.Data) == 0 || int64(len(converted.Data)) > domain.MaxGifCatalogDocumentSize ||
		converted.Width <= 0 || converted.Height <= 0 || converted.Duration <= 0 {
		s.log.Warn("GIF catalog conversion failed", zap.Int("input_bytes", len(data)), zap.Error(err))
		return domain.Document{}, domain.ErrGifCatalogFileInvalid
	}
	objectKey, err := s.blobs.Put(ctx, converted.Data)
	if err != nil {
		return domain.Document{}, err
	}
	sum := sha256.Sum256(converted.Data)
	docID := randomID()
	if err := s.media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: fmt.Sprintf("doc:%d", docID), Backend: domain.MediaBackend(s.blobs.Name()),
		ObjectKey: objectKey, Size: int64(len(converted.Data)), SHA256: append([]byte(nil), sum[:]...), MimeType: "video/mp4",
	}); err != nil {
		return domain.Document{}, err
	}
	name := strings.TrimSpace(fileName)
	if name == "" {
		name = "animation.gif"
	}
	doc := domain.Document{
		ID: docID, AccessHash: randomID(), FileReference: randomFileReference(), Date: int(time.Now().Unix()),
		MimeType: "video/mp4", Size: int64(len(converted.Data)), DCID: s.dc,
		Attributes: canonicalGIFVideoAttributes([]domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: name}}, converted, false),
	}
	if err := s.media.PutDocument(ctx, doc); err != nil {
		return domain.Document{}, err
	}
	return doc, nil
}

func detectGifCatalogUploadMime(data []byte) (string, bool) {
	switch {
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif", true
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		return "video/mp4", true
	default:
		return "", false
	}
}

func (s *Service) AdminCreateGifCatalogEntry(ctx context.Context, title string, documentID int64, fileName string) (domain.GifCatalogEntry, error) {
	return s.createGifCatalogEntry(ctx, title, documentID, fileName, "", "")
}

func (s *Service) createGifCatalogEntry(ctx context.Context, title string, documentID int64, fileName, sourceFilename, sourceSHA256 string) (domain.GifCatalogEntry, error) {
	if s.gifCatalog == nil {
		return domain.GifCatalogEntry{}, domain.ErrGifCatalogUnavailable
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > domain.MaxGifCatalogTitleLen || utf8.RuneCountInString(fileName) > domain.MaxGifCatalogFileNameLen || documentID == 0 ||
		((sourceFilename == "") != (sourceSHA256 == "")) {
		return domain.GifCatalogEntry{}, domain.ErrGifCatalogEntryInvalid
	}
	if sourceSHA256 != "" {
		decoded, err := hex.DecodeString(sourceSHA256)
		if err != nil || len(decoded) != sha256.Size {
			return domain.GifCatalogEntry{}, domain.ErrGifCatalogEntryInvalid
		}
	}
	if _, found, err := s.media.GetDocument(ctx, documentID); err != nil {
		return domain.GifCatalogEntry{}, err
	} else if !found {
		return domain.GifCatalogEntry{}, domain.ErrGifCatalogEntryInvalid
	}
	return s.gifCatalog.CreateGifCatalogEntry(ctx, domain.GifCatalogEntry{
		ID: randomID(), Title: title, FileName: fileName, DocumentID: documentID,
		SourceFilename: sourceFilename, SourceSHA256: sourceSHA256,
	})
}

func (s *Service) AdminSetGifCatalogCategory(ctx context.Context, id int64, category string) (bool, error) {
	if s.gifCatalog == nil {
		return false, domain.ErrGifCatalogUnavailable
	}
	if category != "" && !domain.ValidGifCategory(category) {
		return false, domain.ErrGifCatalogEntryInvalid
	}
	changed, err := s.gifCatalog.SetGifCatalogCategory(ctx, id, category)
	if err == nil && !changed {
		err = domain.ErrGifCatalogEntryNotFound
	}
	return changed, err
}

func (s *Service) AdminListGifCatalog(ctx context.Context) ([]domain.GifCatalogEntry, error) {
	if s.gifCatalog == nil {
		return nil, domain.ErrGifCatalogUnavailable
	}
	return s.gifCatalog.ListGifCatalog(ctx, false)
}

func (s *Service) ListGifCatalog(ctx context.Context, onlyEnabled bool) ([]domain.GifCatalogEntry, error) {
	if s.gifCatalog == nil {
		return nil, domain.ErrGifCatalogUnavailable
	}
	return s.gifCatalog.ListGifCatalog(ctx, onlyEnabled)
}

func (s *Service) ValidateGifCatalog(ctx context.Context) error {
	entries, err := s.ListGifCatalog(ctx, false)
	if err != nil {
		return err
	}
	ids := make([]int64, len(entries))
	for i := range entries {
		ids[i] = entries[i].DocumentID
	}
	docs, err := s.media.GetDocuments(ctx, ids)
	if err != nil {
		return err
	}
	if len(docs) != len(ids) {
		return fmt.Errorf("gif catalog contains a missing document")
	}
	for _, doc := range docs {
		if !doc.IsGif() {
			return fmt.Errorf("gif catalog document %d is not canonical GIFv", doc.ID)
		}
		blob, ok, err := s.media.GetFileBlob(ctx, fmt.Sprintf("doc:%d", doc.ID))
		if err != nil {
			return err
		}
		if !ok || blob.Backend != domain.MediaBackend(s.blobs.Name()) {
			return fmt.Errorf("gif catalog document %d is unavailable in configured %s backend", doc.ID, s.blobs.Name())
		}
		if blob.Size <= 0 || blob.Size != doc.Size {
			return fmt.Errorf("gif catalog document %d size mismatch: document=%d blob=%d", doc.ID, doc.Size, blob.Size)
		}
		probe, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, 0, 1)
		if err != nil {
			return fmt.Errorf("probe gif catalog document %d in configured %s backend: %w", doc.ID, s.blobs.Name(), err)
		}
		if len(probe) != 1 || total != blob.Size {
			return fmt.Errorf("gif catalog document %d backend object size mismatch: metadata=%d object=%d", doc.ID, blob.Size, total)
		}
	}
	return nil
}

func (s *Service) AdminSetGifCatalogEnabled(ctx context.Context, id int64, enabled bool) (bool, error) {
	if s.gifCatalog == nil {
		return false, domain.ErrGifCatalogUnavailable
	}
	changed, err := s.gifCatalog.SetGifCatalogEnabled(ctx, id, enabled)
	if err == nil && !changed {
		err = domain.ErrGifCatalogEntryNotFound
	}
	return changed, err
}

func (s *Service) AdminSetGifCatalogSortOrder(ctx context.Context, id int64, order int) (bool, error) {
	if s.gifCatalog == nil {
		return false, domain.ErrGifCatalogUnavailable
	}
	changed, err := s.gifCatalog.SetGifCatalogSortOrder(ctx, id, order)
	if err == nil && !changed {
		err = domain.ErrGifCatalogEntryNotFound
	}
	return changed, err
}

func (s *Service) AdminDeleteGifCatalogEntry(ctx context.Context, id int64) (bool, error) {
	if s.gifCatalog == nil {
		return false, domain.ErrGifCatalogUnavailable
	}
	changed, err := s.gifCatalog.DeleteGifCatalogEntry(ctx, id)
	if err == nil && !changed {
		err = domain.ErrGifCatalogEntryNotFound
	}
	return changed, err
}
