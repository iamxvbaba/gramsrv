package files

import (
	"context"

	"telesrv/internal/domain"
)

// Bind external document references before persistence. The importing owner is
// authoritative for a new set; other catalogs use the already-published set.
func (s *Service) bindSeedStickerReferences(ctx context.Context, attrs []domain.DocumentAttribute, owner domain.StickerSetRef) error {
	for i := range attrs {
		a := &attrs[i]
		if (a.Kind != domain.DocAttrSticker && a.Kind != domain.DocAttrCustomEmoji) || a.StickerSetID == 0 {
			continue
		}
		if owner.ID != 0 && a.StickerSetID == owner.ID {
			a.StickerSetAccessHash = owner.AccessHash
			continue
		}
		set, found, err := s.media.GetStickerSetByID(ctx, a.StickerSetID)
		if err != nil {
			return err
		}
		if found {
			a.StickerSetAccessHash = set.AccessHash
		}
	}
	return nil
}
