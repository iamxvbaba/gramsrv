package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
	"telesrv/internal/seed/appearance"
)

// AppearanceSeedStats reports default appearance resources imported into media storage.
type AppearanceSeedStats struct {
	Wallpapers int
	Documents  int
	Blobs      int
	Skipped    bool
}

// SeedAppearance imports the default wallpaper document catalog into telesrv media storage.
func (s *Service) SeedAppearance(ctx context.Context) (AppearanceSeedStats, error) {
	var stats AppearanceSeedStats
	catalog := appearance.Default()
	if len(catalog.Wallpapers) == 0 && len(catalog.ChatThemes) == 0 {
		stats.Skipped = true
		return stats, nil
	}
	stateHash, err := s.seedAppearanceStateHash()
	if err != nil {
		return stats, err
	}
	ready, err := s.appearanceSeedReady(ctx, catalog)
	if err != nil {
		return stats, err
	}
	matched, err := s.seedStateMatches(ctx, seedAppearanceStateKey, stateHash)
	if err != nil {
		return stats, err
	}
	if matched && ready {
		stats.Wallpapers = len(catalog.Wallpapers)
		stats.Skipped = true
		return stats, nil
	}
	seen := make(map[int64]bool)
	seedDoc := func(in appearance.Document, label string) error {
		if in.ID == 0 || seen[in.ID] {
			return nil
		}
		seen[in.ID] = true
		doc, blobs, err := s.seedAppearanceDocument(ctx, in)
		if err != nil {
			return fmt.Errorf("seed %s %d: %w", label, in.ID, err)
		}
		if doc.ID != 0 {
			stats.Documents++
		}
		stats.Blobs += blobs
		return nil
	}
	for _, wallpaper := range catalog.Wallpapers {
		if err := seedDoc(wallpaper.Document, "wallpaper"); err != nil {
			return stats, err
		}
		stats.Wallpapers++
	}
	// 聊天主题的主题背景墙纸文档也要进媒体库,否则客户端取主题背景会 404。
	for _, ct := range catalog.ChatThemes {
		for _, setting := range ct.Settings {
			if err := seedDoc(setting.Wallpaper.Document, "chat theme wallpaper"); err != nil {
				return stats, err
			}
		}
	}
	if err := s.putSeedState(ctx, seedAppearanceStateKey, stateHash); err != nil {
		return stats, err
	}
	return stats, nil
}

func (s *Service) seedAppearanceStateHash() (string, error) {
	raw, err := appearance.FS.ReadFile("default_appearance_seed.json")
	if err != nil {
		return "", err
	}
	return seedStateHash(func(h hash.Hash) error {
		writeSeedStateHeader(h, seedAppearanceStateVersion, s.dc)
		_, _ = h.Write(raw)
		return nil
	})
}

func (s *Service) appearanceSeedReady(ctx context.Context, catalog appearance.Catalog) (bool, error) {
	docs := make(map[int64]appearance.Document)
	add := func(doc appearance.Document) {
		if doc.ID != 0 {
			docs[doc.ID] = doc
		}
	}
	for _, wallpaper := range catalog.Wallpapers {
		add(wallpaper.Document)
	}
	for _, ct := range catalog.ChatThemes {
		for _, setting := range ct.Settings {
			add(setting.Wallpaper.Document)
		}
	}
	if len(docs) == 0 {
		return true, nil
	}
	ids := make([]int64, 0, len(docs))
	locationKeys := make([]string, 0, len(docs)*2)
	for id, doc := range docs {
		ids = append(ids, id)
		if doc.Path != "" {
			locationKeys = append(locationKeys, fmt.Sprintf("doc:%d", id))
		}
		for _, thumb := range doc.Thumbs {
			if thumb.Path != "" && thumb.Type != "" {
				locationKeys = append(locationKeys, fmt.Sprintf("doc:%d:%s", id, thumb.Type))
			}
		}
	}
	stored, err := s.media.GetDocuments(ctx, ids)
	if err != nil {
		return false, err
	}
	if len(stored) < len(docs) {
		return false, nil
	}
	for _, doc := range stored {
		want, ok := docs[doc.ID]
		if !ok {
			continue
		}
		if doc.DCID != s.dc || doc.MimeType != want.MimeType || doc.Size != want.Size {
			return false, nil
		}
		delete(docs, doc.ID)
	}
	if len(docs) > 0 {
		return false, nil
	}
	if len(locationKeys) == 0 {
		return true, nil
	}
	blobs, err := s.media.GetFileBlobs(ctx, locationKeys)
	if err != nil {
		return false, err
	}
	for _, key := range locationKeys {
		if _, ok := blobs[key]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) seedAppearanceDocument(ctx context.Context, in appearance.Document) (domain.Document, int, error) {
	if in.ID == 0 {
		return domain.Document{}, 0, nil
	}
	doc := domain.Document{
		ID:         in.ID,
		AccessHash: in.AccessHash,
		Date:       in.Date,
		MimeType:   in.MimeType,
		Size:       in.Size,
		DCID:       s.dc,
		Attributes: appearanceDocumentAttributes(in.Attributes),
		Thumbs:     appearanceDocumentThumbs(in.Thumbs),
	}
	blobs := 0
	if in.Path != "" {
		data, sum, err := readAppearanceSeedBlob(in.Path, in.SHA256)
		if err != nil {
			return domain.Document{}, blobs, err
		}
		objectKey, err := s.blobs.Put(ctx, data)
		if err != nil {
			return domain.Document{}, blobs, err
		}
		if err := s.media.PutFileBlob(ctx, domain.FileBlob{
			LocationKey: fmt.Sprintf("doc:%d", in.ID),
			Backend:     domain.MediaBackend(s.blobs.Name()),
			ObjectKey:   objectKey,
			Size:        int64(len(data)),
			SHA256:      sum,
			MimeType:    in.MimeType,
		}); err != nil {
			return domain.Document{}, blobs, err
		}
		s.prewarmSmallBlob(objectKey, data)
		blobs++
	}
	for _, thumb := range in.Thumbs {
		if thumb.Path == "" || thumb.Type == "" {
			continue
		}
		data, sum, err := readAppearanceSeedBlob(thumb.Path, thumb.SHA256)
		if err != nil {
			return domain.Document{}, blobs, err
		}
		objectKey, err := s.blobs.Put(ctx, data)
		if err != nil {
			return domain.Document{}, blobs, err
		}
		if err := s.media.PutFileBlob(ctx, domain.FileBlob{
			LocationKey: fmt.Sprintf("doc:%d:%s", in.ID, thumb.Type),
			Backend:     domain.MediaBackend(s.blobs.Name()),
			ObjectKey:   objectKey,
			Size:        int64(len(data)),
			SHA256:      sum,
			MimeType:    seedThumbMimeType(data),
		}); err != nil {
			return domain.Document{}, blobs, err
		}
		s.prewarmSmallBlob(objectKey, data)
		blobs++
	}
	if err := s.media.PutDocument(ctx, doc); err != nil {
		return domain.Document{}, blobs, err
	}
	return doc, blobs, nil
}

func readAppearanceSeedBlob(path, wantSHA string) ([]byte, []byte, error) {
	data, err := appearance.FS.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if wantSHA != "" && got != wantSHA {
		return nil, nil, fmt.Errorf("%s sha256 = %s, want %s", path, got, wantSHA)
	}
	return data, append([]byte(nil), sum[:]...), nil
}

func appearanceDocumentAttributes(in []appearance.DocumentAttribute) []domain.DocumentAttribute {
	out := make([]domain.DocumentAttribute, 0, len(in))
	for _, attr := range in {
		switch attr.Kind {
		case "image_size":
			out = append(out, domain.DocumentAttribute{
				Kind: domain.DocAttrImageSize,
				W:    attr.W,
				H:    attr.H,
			})
		case "filename":
			if attr.FileName != "" {
				out = append(out, domain.DocumentAttribute{
					Kind:     domain.DocAttrFilename,
					FileName: branding.UserVisibleText(attr.FileName, ""),
				})
			}
		}
	}
	return out
}

func appearanceDocumentThumbs(in []appearance.PhotoSize) []domain.PhotoSize {
	out := make([]domain.PhotoSize, 0, len(in))
	for _, thumb := range in {
		if thumb.Type == "" {
			continue
		}
		switch thumb.Kind {
		case "size":
			out = append(out, domain.PhotoSize{
				Kind: domain.PhotoSizeKindDefault,
				Type: thumb.Type,
				W:    thumb.W,
				H:    thumb.H,
				Size: thumb.Size,
			})
		}
	}
	return out
}
