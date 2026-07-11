package rpc

import (
	"bytes"
	"context"
	"fmt"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// 个人贴纸集合：收藏贴纸 / 最近贴纸 / 保存的 GIF。读写都经 stickerCollectionService
// （由 *app/account.Service 实现，类型断言自 r.deps.Account）。文档经 Files 解析为完整
// 文档对象，未接通服务时回落历史空 stub 行为。

type stickerCollectionService interface {
	SaveStickerCollectionItem(ctx context.Context, userID int64, kind domain.StickerCollectionKind, documentID int64, unsave bool, now int) error
	ListStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind, limit int) ([]domain.StickerCollectionItem, error)
	ClearStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind) error
}

func (r *Router) stickerCollectionSvc() (stickerCollectionService, bool) {
	svc, ok := r.deps.Account.(stickerCollectionService)
	return svc, ok
}

// stickerDocumentFromInput 校验输入文档存在且为贴纸（faveSticker/saveRecentSticker）。
func (r *Router) stickerDocumentFromInput(ctx context.Context, input tg.InputDocumentClass, requireGif bool) (domain.Document, error) {
	in, ok := input.(*tg.InputDocument)
	if !ok || in.ID == 0 || r.deps.Files == nil {
		if requireGif {
			return domain.Document{}, gifIDInvalidErr()
		}
		return domain.Document{}, stickerInvalidErr()
	}
	doc, found, err := r.deps.Files.GetDocument(ctx, in.ID)
	if err != nil {
		return domain.Document{}, internalErr()
	}
	ok = found && doc.AccessHash == in.AccessHash
	if ok && len(in.FileReference) > 0 && !bytes.Equal(in.FileReference, doc.FileReference) {
		ok = false
	}
	if ok {
		if requireGif {
			ok = doc.IsGif()
		} else {
			ok = doc.IsSticker()
		}
	}
	if !ok {
		if requireGif {
			return domain.Document{}, gifIDInvalidErr()
		}
		return domain.Document{}, stickerInvalidErr()
	}
	return doc, nil
}

func (r *Router) onMessagesFaveSticker(ctx context.Context, req *tg.MessagesFaveStickerRequest) (bool, error) {
	if req == nil {
		return false, stickerInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	doc, err := r.stickerDocumentFromInput(ctx, req.ID, false)
	if err != nil {
		return false, err
	}
	if svc, ok := r.stickerCollectionSvc(); ok {
		if err := svc.SaveStickerCollectionItem(ctx, userID, domain.StickerCollectionFaved, doc.ID, req.Unfave, int(r.clock.Now().Unix())); err != nil {
			return false, internalErr()
		}
	}
	r.pushStickerCollectionUpdate(ctx, userID, &tg.UpdateFavedStickers{})
	return true, nil
}

func (r *Router) onMessagesSaveRecentSticker(ctx context.Context, req *tg.MessagesSaveRecentStickerRequest) (bool, error) {
	if req == nil {
		return false, stickerInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	doc, err := r.stickerDocumentFromInput(ctx, req.ID, false)
	if err != nil {
		return false, err
	}
	kind := domain.StickerCollectionRecent
	if req.Attached {
		kind = domain.StickerCollectionRecentAttached
	}
	if svc, ok := r.stickerCollectionSvc(); ok {
		if err := svc.SaveStickerCollectionItem(ctx, userID, kind, doc.ID, req.Unsave, int(r.clock.Now().Unix())); err != nil {
			return false, internalErr()
		}
	}
	r.pushStickerCollectionUpdate(ctx, userID, &tg.UpdateRecentStickers{})
	return true, nil
}

func (r *Router) onMessagesSaveGif(ctx context.Context, req *tg.MessagesSaveGifRequest) (bool, error) {
	if req == nil {
		return false, gifIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	doc, err := r.stickerDocumentFromInput(ctx, req.ID, true)
	if err != nil {
		return false, err
	}
	if svc, ok := r.stickerCollectionSvc(); ok {
		if err := svc.SaveStickerCollectionItem(ctx, userID, domain.StickerCollectionGif, doc.ID, req.Unsave, int(r.clock.Now().Unix())); err != nil {
			return false, internalErr()
		}
	}
	r.pushStickerCollectionUpdate(ctx, userID, &tg.UpdateSavedGifs{})
	return true, nil
}

func (r *Router) onMessagesClearRecentStickers(ctx context.Context, req *tg.MessagesClearRecentStickersRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	kind := domain.StickerCollectionRecent
	if req != nil && req.Attached {
		kind = domain.StickerCollectionRecentAttached
	}
	if svc, ok := r.stickerCollectionSvc(); ok {
		if err := svc.ClearStickerCollection(ctx, userID, kind); err != nil {
			return false, internalErr()
		}
	}
	r.pushStickerCollectionUpdate(ctx, userID, &tg.UpdateRecentStickers{})
	return true, nil
}

func (r *Router) onMessagesGetFavedStickers(ctx context.Context, hash int64) (tg.MessagesFavedStickersClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	docs, err := r.stickerCollectionDocuments(ctx, userID, domain.StickerCollectionFaved, nil)
	if err != nil {
		return nil, internalErr()
	}
	catalogHash := stickerDocumentsHash(docs)
	if hash != 0 && hash == catalogHash {
		return &tg.MessagesFavedStickersNotModified{}, nil
	}
	return &tg.MessagesFavedStickers{
		Hash:     catalogHash,
		Packs:    []tg.StickerPack{},
		Stickers: tgDocuments(docs),
	}, nil
}

func (r *Router) onMessagesGetRecentStickers(ctx context.Context, req *tg.MessagesGetRecentStickersRequest) (tg.MessagesRecentStickersClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	kind := domain.StickerCollectionRecent
	if req != nil && req.Attached {
		kind = domain.StickerCollectionRecentAttached
	}
	var dates []int
	docs, err := r.stickerCollectionDocuments(ctx, userID, kind, &dates)
	if err != nil {
		return nil, internalErr()
	}
	catalogHash := stickerDocumentsHash(docs)
	if req != nil && req.Hash != 0 && req.Hash == catalogHash {
		return &tg.MessagesRecentStickersNotModified{}, nil
	}
	return &tg.MessagesRecentStickers{
		Hash:     catalogHash,
		Packs:    []tg.StickerPack{},
		Stickers: tgDocuments(docs),
		Dates:    dates,
	}, nil
}

func (r *Router) onMessagesGetSavedGifs(ctx context.Context, hash int64) (tg.MessagesSavedGifsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	docs, err := r.stickerCollectionDocuments(ctx, userID, domain.StickerCollectionGif, nil)
	if err != nil {
		return nil, internalErr()
	}
	catalogHash := stickerDocumentsHash(docs)
	if hash != 0 && hash == catalogHash {
		return &tg.MessagesSavedGifsNotModified{}, nil
	}
	return &tg.MessagesSavedGifs{Hash: catalogHash, Gifs: tgDocuments(docs)}, nil
}

// stickerCollectionDocuments 取某集合并解析为完整文档（最新在前，顺序与集合一致）。
// 集合引用缺失或类型错误属于坏数据并 fail-fast；若 datesOut 非 nil，按相同顺序填充 used_at。
func (r *Router) stickerCollectionDocuments(ctx context.Context, userID int64, kind domain.StickerCollectionKind, datesOut *[]int) ([]domain.Document, error) {
	svc, ok := r.stickerCollectionSvc()
	if !ok || r.deps.Files == nil {
		if datesOut != nil {
			*datesOut = []int{}
		}
		return nil, nil
	}
	items, err := svc.ListStickerCollection(ctx, userID, kind, domain.MaxStickerCollectionItems(kind))
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if datesOut != nil {
			*datesOut = []int{}
		}
		return nil, nil
	}
	ids := make([]int64, 0, len(items))
	dateByID := make(map[int64]int, len(items))
	for _, it := range items {
		ids = append(ids, it.DocumentID)
		dateByID[it.DocumentID] = it.Date
	}
	resolved, err := r.deps.Files.GetDocuments(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := documentsByID(resolved)
	docs := make([]domain.Document, 0, len(items))
	dates := make([]int, 0, len(items))
	for _, id := range ids { // 保持集合顺序（最新在前）
		doc, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("sticker collection %s references missing document %d", kind, id)
		}
		if kind == domain.StickerCollectionGif && !doc.IsGif() {
			return nil, fmt.Errorf("saved gif collection references non-GIFv document %d", id)
		}
		if kind != domain.StickerCollectionGif && !doc.IsSticker() {
			return nil, fmt.Errorf("sticker collection %s references non-sticker document %d", kind, id)
		}
		docs = append(docs, doc)
		dates = append(dates, dateByID[id])
	}
	if datesOut != nil {
		*datesOut = dates
	}
	return docs, nil
}

func stickerDocumentsHash(docs []domain.Document) int64 {
	values := make([]int64, 0, len(docs))
	for _, d := range docs {
		values = append(values, d.ID)
	}
	return int64(tdesktopCountHash(values))
}

// pushStickerCollectionUpdate 把 updateFaved/Recent/SavedGifs nudge 推给本人其它在线设备。
func (r *Router) pushStickerCollectionUpdate(ctx context.Context, userID int64, update tg.UpdateClass) {
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
	})
}
