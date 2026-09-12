package files

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/seed/freeze"
)

// FreezeSeedStats reports the bundled "freeze" custom emoji import outcome.
type FreezeSeedStats struct {
	Imported bool
	Skipped  bool
}

// SeedFreezeEmoji imports the frozen-account mark emoji straight from the
// binary, not from an on-disk sticker seed directory. The document backs
// accountFrozenMarkIcon (bot_verification_projection.go), so the icon always
// resolves through messages.getCustomEmojiDocuments regardless of whether a
// server ships data/sticker-seed. The import is idempotent: a persisted
// document with its main blob is left untouched.
func (s *Service) SeedFreezeEmoji(ctx context.Context) (FreezeSeedStats, error) {
	var stats FreezeSeedStats
	body, err := freeze.FS.ReadFile("freeze.tgs")
	if err != nil {
		return stats, fmt.Errorf("read bundled freeze emoji: %w", err)
	}
	if len(body) == 0 {
		return stats, fmt.Errorf("bundled freeze emoji is empty")
	}
	docID := freeze.DocumentID
	locationKey := fmt.Sprintf("doc:%d", docID)

	if existing, found, err := s.media.GetDocument(ctx, docID); err != nil {
		return stats, err
	} else if found && existing.ID == docID {
		if _, _, err := s.media.GetFileBlob(ctx, locationKey); err == nil {
			stats.Skipped = true
			return stats, nil
		}
	}

	ref, _ := hex.DecodeString("00d1f6f438a12476426a7268240fc54ff7")
	now := int(time.Now().Unix())
	attrs := []domain.DocumentAttribute{
		{Kind: domain.DocAttrImageSize, W: 512, H: 512},
		{
			Kind:                 domain.DocAttrCustomEmoji,
			Alt:                  freeze.Emoticon,
			StickerSetID:         freeze.SetID,
			StickerSetAccessHash: freeze.SetAccessHash,
		},
		{Kind: domain.DocAttrFilename, FileName: "AnimatedSticker.tgs"},
	}
	doc := domain.Document{
		ID:            docID,
		AccessHash:    freeze.DocumentAccessHash,
		FileReference: ref,
		Date:          now,
		MimeType:      "application/x-tgsticker",
		Size:          int64(len(body)),
		DCID:          s.dc,
		Attributes:    attrs,
	}

	objectKey, err := s.blobs.Put(ctx, body)
	if err != nil {
		return stats, err
	}
	if err := s.media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: locationKey,
		Backend:     domain.MediaBackend(s.blobs.Name()),
		ObjectKey:   objectKey,
		Size:        int64(len(body)),
		MimeType:    "application/x-tgsticker",
	}); err != nil {
		return stats, err
	}
	s.prewarmSmallBlob(objectKey, body)

	if err := s.media.PutDocument(ctx, doc); err != nil {
		return stats, err
	}

	set := domain.StickerSet{
		ID:          freeze.SetID,
		AccessHash:  freeze.SetAccessHash,
		ShortName:   "freeze",
		Title:       "Freeze",
		Count:       1,
		Hash:        537219511,
		Kind:        domain.StickerSetKindEmoji,
		Animated:    true,
		Emojis:      true,
		SortOrder:   100000,
		DocumentIDs: []int64{docID},
		Packs:       []domain.StickerPack{{Emoticon: freeze.Emoticon, DocumentIDs: []int64{docID}}},
	}
	if err := s.media.PutStickerSet(ctx, set); err != nil {
		return stats, err
	}
	s.stickerSetCache.put(set, []domain.Document{doc})

	stats.Imported = true
	return stats, nil
}
