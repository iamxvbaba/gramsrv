package rpc

import (
	"context"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) onMessagesSearchStickerSets(ctx context.Context, req *tg.MessagesSearchStickerSetsRequest) (tg.MessagesFoundStickerSetsClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	return r.searchStickerSetsOfKind(ctx, domain.StickerSetKindStickers, req.Q, req.ExcludeFeatured, req.Hash)
}

// onMessagesSearchEmojiStickerSets searches custom emoji packs the same way
// sticker packs are searched.
func (r *Router) onMessagesSearchEmojiStickerSets(ctx context.Context, req *tg.MessagesSearchEmojiStickerSetsRequest) (tg.MessagesFoundStickerSetsClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	return r.searchStickerSetsOfKind(ctx, domain.StickerSetKindEmoji, req.Q, req.ExcludeFeatured, req.Hash)
}

func (r *Router) searchStickerSetsOfKind(ctx context.Context, kind domain.StickerSetKind, q string, excludeFeatured bool, reqHash int64) (tg.MessagesFoundStickerSetsClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if utf8.RuneCountInString(q) > maxStickerSearchQLength {
		return nil, limitInvalidErr()
	}
	sets, err := r.loadStickerCatalogSets(ctx, kind)
	if err != nil {
		return nil, internalErr()
	}
	found := domain.SearchStickerSets(sets, kind, q)
	// The current featured catalog contains every public set of the kind.
	if excludeFeatured {
		found = nil
	}
	if len(found) > 100 {
		found = found[:100]
	}
	found, err = r.stickerSetsWithViewerInstallState(ctx, kind, found)
	if err != nil {
		return nil, err
	}
	hash := stickerSetsCatalogHash(found)
	if reqHash != 0 && reqHash == hash {
		return &tg.MessagesFoundStickerSetsNotModified{}, nil
	}
	ids := make([]int64, 0, len(found)*5)
	for _, set := range found {
		for i, id := range set.DocumentIDs {
			if i == 5 {
				break
			}
			ids = append(ids, id)
		}
	}
	docs, err := r.deps.Files.GetDocuments(ctx, ids)
	if err != nil {
		return nil, internalErr()
	}
	byID := documentsByID(docs)
	covered := make([]tg.StickerSetCoveredClass, 0, len(found))
	for _, set := range found {
		covers := make(map[int64]domain.Document)
		for i, id := range set.DocumentIDs {
			if i == 5 {
				break
			}
			doc, ok := byID[id]
			if !ok {
				return nil, internalErr()
			}
			covers[id] = doc
		}
		covered = append(covered, featuredCoveredSet(set, covers))
	}
	return &tg.MessagesFoundStickerSets{Hash: hash, Sets: covered}, nil
}

func (r *Router) onMessagesSearchStickers(ctx context.Context, req *tg.MessagesSearchStickersRequest) (tg.MessagesFoundStickersClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if req.Offset < 0 || req.Offset > domain.MaxMessageBoxID || req.Limit < 0 || req.Limit > maxSearchResultsLimit {
		return nil, limitInvalidErr()
	}
	if utf8.RuneCountInString(req.Q) > maxStickerSearchQLength || utf8.RuneCountInString(req.Emoticon) > maxStickerSearchQLength || len(req.LangCode) > maxStickerSearchLangs {
		return nil, limitInvalidErr()
	}
	for _, lang := range req.LangCode {
		if err := validateEmojiLangCode(lang); err != nil {
			return nil, err
		}
	}
	kind := domain.StickerSetKindStickers
	if req.Emojis {
		kind = domain.StickerSetKindEmoji
	}
	sets, err := r.loadStickerCatalogSets(ctx, kind)
	if err != nil {
		return nil, internalErr()
	}
	ids := domain.SearchStickerDocuments(sets, kind, req.Q, req.Emoticon, req.Offset, req.Limit)
	hash := int64(tdesktopCountHash(ids))
	if req.Hash != 0 && req.Hash == hash {
		return &tg.MessagesFoundStickersNotModified{}, nil
	}
	docs, err := r.deps.Files.GetDocuments(ctx, ids)
	if err != nil {
		return nil, internalErr()
	}
	byID := documentsByID(docs)
	ordered := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		doc, ok := byID[id]
		if !ok {
			return nil, internalErr()
		}
		ordered = append(ordered, doc)
	}
	return &tg.MessagesFoundStickers{Hash: hash, Stickers: tgDocuments(ordered)}, nil
}
