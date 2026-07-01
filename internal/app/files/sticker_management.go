package files

import (
	"context"
	"strings"

	"telesrv/internal/domain"
)

func (s *Service) AddStickerToSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef, item domain.StickerSetItemInput) (domain.StickerSet, []domain.Document, error) {
	set, docs, err := s.resolveOwnedStickerSet(ctx, actorUserID, ref)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	if len(set.DocumentIDs) >= domain.MaxStickerSetItems {
		return domain.StickerSet{}, nil, domain.ErrStickerSetTooMuch
	}
	doc, err := s.loadStickerMaterialDocument(ctx, item.DocumentID, item.DocumentAccessHash)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	doc, err = s.materialDocumentForStickerSet(ctx, doc, set.ID)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	if containsInt64(set.DocumentIDs, doc.ID) {
		return set, docs, nil
	}
	emoji := strings.TrimSpace(item.Emoji)
	if err := validateStickerEmoji(emoji); err != nil {
		return domain.StickerSet{}, nil, err
	}
	doc, err = s.prepareStickerSetDocument(ctx, doc, set, emoji)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	set.DocumentIDs = append(set.DocumentIDs, doc.ID)
	set.Count = len(set.DocumentIDs)
	set.Packs = addDocumentToStickerPacks(set.Packs, emoji, doc.ID)
	set.Keywords = upsertStickerKeywords(set.Keywords, parseStickerKeywords(doc.ID, item.Keywords))
	if set.ThumbDocumentID == 0 {
		setStickerSetThumbFromDocument(&set, doc)
	}
	set.Hash = stickerSetHash(set)
	docs = append(docs, doc)
	return s.persistStickerSetMutation(ctx, set, docs, []domain.Document{doc})
}

func (s *Service) RemoveStickerFromSet(ctx context.Context, actorUserID int64, documentID int64, accessHash int64) (domain.StickerSet, []domain.Document, error) {
	doc, err := s.loadStickerInputDocument(ctx, documentID, accessHash)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	setID, setAccessHash, ok := doc.StickerSetRef()
	if !ok || setID == 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	set, docs, err := s.resolveOwnedStickerSet(ctx, actorUserID, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: setID, AccessHash: setAccessHash})
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	if len(set.DocumentIDs) <= 1 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetEmpty
	}
	idx := indexInt64(set.DocumentIDs, documentID)
	if idx < 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	set.DocumentIDs = removeInt64At(set.DocumentIDs, idx)
	set.Count = len(set.DocumentIDs)
	set.Packs = removeDocumentFromStickerPacks(set.Packs, documentID)
	set.Keywords = removeStickerKeywords(set.Keywords, documentID)
	doc = detachStickerSetFromDocument(doc)
	docs = removeDocumentByID(docs, documentID)
	if set.ThumbDocumentID == documentID {
		clearStickerSetThumb(&set)
		if len(docs) > 0 {
			setStickerSetThumbFromDocument(&set, docs[0])
		}
	}
	set.Hash = stickerSetHash(set)
	return s.persistStickerSetMutation(ctx, set, docs, []domain.Document{doc})
}

func (s *Service) ChangeStickerPosition(ctx context.Context, actorUserID int64, documentID int64, accessHash int64, position int) (domain.StickerSet, []domain.Document, error) {
	doc, err := s.loadStickerInputDocument(ctx, documentID, accessHash)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	setID, setAccessHash, ok := doc.StickerSetRef()
	if !ok || setID == 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	set, docs, err := s.resolveOwnedStickerSet(ctx, actorUserID, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: setID, AccessHash: setAccessHash})
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	if position < 0 || position >= len(set.DocumentIDs) {
		return domain.StickerSet{}, nil, domain.ErrStickerSetPositionInvalid
	}
	from := indexInt64(set.DocumentIDs, documentID)
	if from < 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	set.DocumentIDs = moveInt64(set.DocumentIDs, from, position)
	docs = orderDocuments(docs, set.DocumentIDs)
	set.Hash = stickerSetHash(set)
	return s.persistStickerSetMutation(ctx, set, docs, nil)
}

func (s *Service) RenameStickerSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef, title string) (domain.StickerSet, []domain.Document, error) {
	set, docs, err := s.resolveOwnedStickerSet(ctx, actorUserID, ref)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	title = strings.TrimSpace(title)
	if err := validateStickerSetTitle(title); err != nil {
		return domain.StickerSet{}, nil, err
	}
	set.Title = title
	set.Hash = stickerSetHash(set)
	return s.persistStickerSetMutation(ctx, set, docs, nil)
}

func (s *Service) DeleteStickerSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef) (domain.StickerSetKind, error) {
	set, _, err := s.resolveOwnedStickerSet(ctx, actorUserID, ref)
	if err != nil {
		return "", err
	}
	if err := s.media.DeleteStickerSet(ctx, set.ID, actorUserID); err != nil {
		return "", err
	}
	s.deleteCachedStickerSet(set)
	return set.Kind, nil
}

func (s *Service) resolveOwnedStickerSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, error) {
	if actorUserID <= 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetCreatorInvalid
	}
	if ref.Kind != domain.StickerSetRefByID && ref.Kind != domain.StickerSetRefByShortName {
		return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
	}
	set, docs, found, err := s.ResolveStickerSet(ctx, ref)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	if !found || set.ID == 0 || set.Deleted {
		return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
	}
	if ref.Kind == domain.StickerSetRefByID && set.AccessHash != ref.AccessHash {
		return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
	}
	if set.CreatorUserID != actorUserID {
		return domain.StickerSet{}, nil, domain.ErrStickerSetNotOwned
	}
	return set, docs, nil
}

func (s *Service) loadStickerInputDocument(ctx context.Context, documentID int64, accessHash int64) (domain.Document, error) {
	if documentID == 0 || accessHash == 0 {
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	docs, err := s.media.GetDocuments(ctx, []int64{documentID})
	if err != nil {
		return domain.Document{}, err
	}
	if len(docs) != 1 || docs[0].ID != documentID || docs[0].AccessHash != accessHash || !docs[0].IsStickerLike() {
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	return docs[0], nil
}

func (s *Service) loadStickerMaterialDocument(ctx context.Context, documentID int64, accessHash int64) (domain.Document, error) {
	if documentID == 0 || accessHash == 0 {
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	docs, err := s.media.GetDocuments(ctx, []int64{documentID})
	if err != nil {
		return domain.Document{}, err
	}
	if len(docs) != 1 || docs[0].ID != documentID || docs[0].AccessHash != accessHash || !docs[0].IsStickerSetMaterial() {
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	return docs[0], nil
}

func (s *Service) persistStickerSetMutation(ctx context.Context, set domain.StickerSet, docs []domain.Document, changedDocs []domain.Document) (domain.StickerSet, []domain.Document, error) {
	if err := s.media.UpdateStickerSet(ctx, set, changedDocs); err != nil {
		return domain.StickerSet{}, nil, err
	}
	ordered := orderDocuments(docs, set.DocumentIDs)
	s.cacheStickerSet(set, ordered)
	return set, ordered, nil
}

func (s *Service) deleteCachedStickerSet(set domain.StickerSet) {
	if s.stickerSetNegCache != nil {
		s.stickerSetNegCache.put(domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID})
		if set.ShortName != "" {
			s.stickerSetNegCache.put(domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: set.ShortName})
		}
	}
	if s.stickerSetCache != nil {
		s.stickerSetCache.delete(set)
	}
}

func addDocumentToStickerPacks(packs []domain.StickerPack, emoji string, documentID int64) []domain.StickerPack {
	out := copyStickerPacks(packs)
	for i := range out {
		if out[i].Emoticon == emoji {
			if !containsInt64(out[i].DocumentIDs, documentID) {
				out[i].DocumentIDs = append(out[i].DocumentIDs, documentID)
			}
			return out
		}
	}
	return append(out, domain.StickerPack{Emoticon: emoji, DocumentIDs: []int64{documentID}})
}

func removeDocumentFromStickerPacks(packs []domain.StickerPack, documentID int64) []domain.StickerPack {
	out := make([]domain.StickerPack, 0, len(packs))
	for _, pack := range packs {
		ids := removeInt64Value(pack.DocumentIDs, documentID)
		if len(ids) == 0 {
			continue
		}
		out = append(out, domain.StickerPack{Emoticon: pack.Emoticon, DocumentIDs: ids})
	}
	return out
}

func upsertStickerKeywords(in []domain.StickerKeyword, kw domain.StickerKeyword) []domain.StickerKeyword {
	out := removeStickerKeywords(in, kw.DocumentID)
	if len(kw.Keywords) == 0 {
		return out
	}
	return append(out, kw)
}

func removeStickerKeywords(in []domain.StickerKeyword, documentID int64) []domain.StickerKeyword {
	out := make([]domain.StickerKeyword, 0, len(in))
	for _, kw := range in {
		if kw.DocumentID == documentID {
			continue
		}
		out = append(out, domain.StickerKeyword{DocumentID: kw.DocumentID, Keywords: append([]string(nil), kw.Keywords...)})
	}
	return out
}

func detachStickerSetFromDocument(doc domain.Document) domain.Document {
	attrs := append([]domain.DocumentAttribute(nil), doc.Attributes...)
	for i := range attrs {
		if attrs[i].Kind != domain.DocAttrSticker && attrs[i].Kind != domain.DocAttrCustomEmoji {
			continue
		}
		attrs[i].StickerSetID = 0
		attrs[i].StickerSetAccessHash = 0
		attrs[i].Mask = false
		attrs[i].TextColor = false
		break
	}
	doc.Attributes = attrs
	return doc
}

func setStickerSetThumbFromDocument(set *domain.StickerSet, doc domain.Document) {
	set.ThumbDocumentID = doc.ID
	set.Thumbs = copyPhotoSizes(doc.Thumbs)
	set.ThumbDCID = doc.DCID
	set.ThumbVersion = 0
	if len(set.Thumbs) > 0 {
		set.ThumbVersion = 1
	}
}

func clearStickerSetThumb(set *domain.StickerSet) {
	set.ThumbDocumentID = 0
	set.Thumbs = nil
	set.ThumbDCID = 0
	set.ThumbVersion = 0
}

func copyStickerPacks(packs []domain.StickerPack) []domain.StickerPack {
	out := append([]domain.StickerPack(nil), packs...)
	for i := range out {
		out[i].DocumentIDs = append([]int64(nil), out[i].DocumentIDs...)
	}
	return out
}

func containsInt64(in []int64, value int64) bool {
	return indexInt64(in, value) >= 0
}

func indexInt64(in []int64, value int64) int {
	for i, v := range in {
		if v == value {
			return i
		}
	}
	return -1
}

func removeInt64At(in []int64, idx int) []int64 {
	out := append([]int64(nil), in[:idx]...)
	return append(out, in[idx+1:]...)
}

func removeInt64Value(in []int64, value int64) []int64 {
	out := make([]int64, 0, len(in))
	for _, v := range in {
		if v != value {
			out = append(out, v)
		}
	}
	return out
}

func moveInt64(in []int64, from, to int) []int64 {
	out := append([]int64(nil), in...)
	if from == to {
		return out
	}
	value := out[from]
	out = append(out[:from], out[from+1:]...)
	if to >= len(out) {
		return append(out, value)
	}
	out = append(out[:to], append([]int64{value}, out[to:]...)...)
	return out
}

func removeDocumentByID(docs []domain.Document, documentID int64) []domain.Document {
	out := make([]domain.Document, 0, len(docs))
	for _, doc := range docs {
		if doc.ID != documentID {
			out = append(out, doc)
		}
	}
	return out
}
