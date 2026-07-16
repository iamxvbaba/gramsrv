package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

func (r *Router) registerStickers(d *tlprofile.Dispatcher) {
	registerRPC[*tg.StickersCreateStickerSetRequest](d, tlprofile.SemanticMethodStickersCreateStickerSet, func(ctx context.Context, layerRequest *tg.StickersCreateStickerSetRequest) (any, error) {
		return r.onStickersCreateStickerSet(ctx, layerRequest)
	})
	registerRPC[*tg.StickersCheckShortNameRequest](d, tlprofile.SemanticMethodStickersCheckShortName, func(ctx context.Context, layerRequest *tg.StickersCheckShortNameRequest) (any, error) {
		return r.onStickersCheckShortName(ctx, layerRequest.
			ShortName)
	})
	registerRPC[*tg.StickersSuggestShortNameRequest](d, tlprofile.SemanticMethodStickersSuggestShortName, func(ctx context.Context, layerRequest *tg.StickersSuggestShortNameRequest) (any, error) {
		return r.onStickersSuggestShortName(ctx, layerRequest.
			Title)
	})
	registerRPC[*tg.StickersAddStickerToSetRequest](d, tlprofile.SemanticMethodStickersAddStickerToSet, func(ctx context.Context, layerRequest *tg.StickersAddStickerToSetRequest) (any, error) {
		return r.onStickersAddStickerToSet(ctx, layerRequest)
	})
	registerRPC[*tg.StickersRemoveStickerFromSetRequest](d, tlprofile.SemanticMethodStickersRemoveStickerFromSet, func(ctx context.Context, layerRequest *tg.StickersRemoveStickerFromSetRequest) (any, error) {
		return r.onStickersRemoveStickerFromSet(ctx, layerRequest.
			Sticker)
	})
	registerRPC[*tg.StickersChangeStickerPositionRequest](d, tlprofile.SemanticMethodStickersChangeStickerPosition, func(ctx context.Context, layerRequest *tg.StickersChangeStickerPositionRequest) (any, error) {
		return r.onStickersChangeStickerPosition(ctx, layerRequest)
	})
	registerRPC[*tg.StickersRenameStickerSetRequest](d, tlprofile.SemanticMethodStickersRenameStickerSet, func(ctx context.Context, layerRequest *tg.StickersRenameStickerSetRequest) (any, error) {
		return r.onStickersRenameStickerSet(ctx, layerRequest)
	})
	registerRPC[*tg.StickersDeleteStickerSetRequest](d, tlprofile.SemanticMethodStickersDeleteStickerSet, func(ctx context.Context, layerRequest *tg.StickersDeleteStickerSetRequest) (any, error) {
		return r.onStickersDeleteStickerSet(ctx, layerRequest.
			Stickerset)
	})

}

func (r *Router) onStickersCreateStickerSet(ctx context.Context, req *tg.StickersCreateStickerSetRequest) (tg.MessagesStickerSetClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Files == nil {
		return nil, internalErr()
	}
	if req.Masks && req.Emojis {
		return nil, packTypeInvalidErr()
	}
	userID, err := r.stickerSetCreatorUserID(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	items := make([]domain.StickerSetItemInput, 0, len(req.Stickers))
	for _, item := range req.Stickers {
		id, accessHash, ok := inputDocumentRef(item.Document)
		if !ok {
			return nil, stickerFileInvalidErr()
		}
		items = append(items, domain.StickerSetItemInput{
			DocumentID:         id,
			DocumentAccessHash: accessHash,
			Emoji:              item.Emoji,
			Keywords:           item.Keywords,
		})
	}
	thumbID, thumbAccessHash, ok := inputDocumentRef(req.Thumb)
	if req.Thumb != nil && !ok {
		return nil, stickerFileInvalidErr()
	}
	kind := domain.StickerSetKindStickers
	if req.Emojis {
		kind = domain.StickerSetKindEmoji
	} else if req.Masks {
		kind = domain.StickerSetKindMasks
	}
	set, docs, err := r.deps.Files.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID:   userID,
		Title:           req.Title,
		ShortName:       req.ShortName,
		Kind:            kind,
		TextColor:       req.TextColor,
		ThumbDocumentID: thumbID,
		ThumbAccessHash: thumbAccessHash,
		Items:           items,
		Software:        req.Software,
		Date:            int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, stickerSetCreateErr(err)
	}
	if svc, ok := r.userStickerSetSvc(); ok {
		if err := svc.InstallUserStickerSet(ctx, userID, set.ID, userStickerSetKind(set), false, int(r.clock.Now().Unix())); err != nil {
			return nil, internalErr()
		}
		set.Installed = true
		set.InstalledDate = int(r.clock.Now().Unix())
	}
	r.invalidateStickerCatalog(userStickerSetKind(set))
	r.pushStickerSetsUpdate(ctx, userID, userStickerSetKind(set))
	return tgMessagesStickerSet(set, docs), nil
}

func (r *Router) onStickersCheckShortName(ctx context.Context, shortName string) (bool, error) {
	if r.deps.Files == nil {
		return false, internalErr()
	}
	if _, _, err := r.currentUserID(ctx); err != nil {
		return false, internalErr()
	}
	available, err := r.deps.Files.CheckStickerSetShortName(ctx, shortName)
	if err != nil {
		return false, stickerSetShortNameCheckErr(err)
	}
	return available, nil
}

func (r *Router) onStickersSuggestShortName(ctx context.Context, title string) (*tg.StickersSuggestedShortName, error) {
	if r.deps.Files == nil {
		return nil, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	shortName, err := r.deps.Files.SuggestStickerSetShortName(ctx, title, userID)
	if err != nil {
		return nil, stickerSetSuggestShortNameErr(err)
	}
	return &tg.StickersSuggestedShortName{ShortName: shortName}, nil
}

func (r *Router) onStickersAddStickerToSet(ctx context.Context, req *tg.StickersAddStickerToSetRequest) (tg.MessagesStickerSetClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, err := r.stickerSetActorUserID(ctx)
	if err != nil {
		return nil, err
	}
	ref, ok := stickerSetRefFromInput(req.Stickerset)
	if !ok {
		return nil, stickersetInvalidErr()
	}
	documentID, accessHash, ok := inputDocumentRef(req.Sticker.Document)
	if !ok {
		return nil, stickerFileInvalidErr()
	}
	set, docs, err := r.deps.Files.AddStickerToSet(ctx, userID, ref, domain.StickerSetItemInput{
		DocumentID:         documentID,
		DocumentAccessHash: accessHash,
		Emoji:              req.Sticker.Emoji,
		Keywords:           req.Sticker.Keywords,
	})
	if err != nil {
		return nil, stickerSetManagementErr(err)
	}
	r.notifyStickerSetMutated(ctx, userID, set)
	return tgMessagesStickerSet(set, docs), nil
}

func (r *Router) onStickersRemoveStickerFromSet(ctx context.Context, input tg.InputDocumentClass) (tg.MessagesStickerSetClass, error) {
	userID, err := r.stickerSetActorUserID(ctx)
	if err != nil {
		return nil, err
	}
	documentID, accessHash, ok := inputDocumentRef(input)
	if !ok {
		return nil, stickerFileInvalidErr()
	}
	set, docs, err := r.deps.Files.RemoveStickerFromSet(ctx, userID, documentID, accessHash)
	if err != nil {
		return nil, stickerSetManagementErr(err)
	}
	r.notifyStickerSetMutated(ctx, userID, set)
	return tgMessagesStickerSet(set, docs), nil
}

func (r *Router) onStickersChangeStickerPosition(ctx context.Context, req *tg.StickersChangeStickerPositionRequest) (tg.MessagesStickerSetClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, err := r.stickerSetActorUserID(ctx)
	if err != nil {
		return nil, err
	}
	documentID, accessHash, ok := inputDocumentRef(req.Sticker)
	if !ok {
		return nil, stickerFileInvalidErr()
	}
	set, docs, err := r.deps.Files.ChangeStickerPosition(ctx, userID, documentID, accessHash, req.Position)
	if err != nil {
		return nil, stickerSetManagementErr(err)
	}
	r.notifyStickerSetMutated(ctx, userID, set)
	return tgMessagesStickerSet(set, docs), nil
}

func (r *Router) onStickersRenameStickerSet(ctx context.Context, req *tg.StickersRenameStickerSetRequest) (tg.MessagesStickerSetClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, err := r.stickerSetActorUserID(ctx)
	if err != nil {
		return nil, err
	}
	ref, ok := stickerSetRefFromInput(req.Stickerset)
	if !ok {
		return nil, stickersetInvalidErr()
	}
	set, docs, err := r.deps.Files.RenameStickerSet(ctx, userID, ref, req.Title)
	if err != nil {
		return nil, stickerSetManagementErr(err)
	}
	r.notifyStickerSetMutated(ctx, userID, set)
	return tgMessagesStickerSet(set, docs), nil
}

func (r *Router) onStickersDeleteStickerSet(ctx context.Context, input tg.InputStickerSetClass) (bool, error) {
	userID, err := r.stickerSetActorUserID(ctx)
	if err != nil {
		return false, err
	}
	ref, ok := stickerSetRefFromInput(input)
	if !ok {
		return false, stickersetInvalidErr()
	}
	kind, err := r.deps.Files.DeleteStickerSet(ctx, userID, ref)
	if err != nil {
		return false, stickerSetManagementErr(err)
	}
	r.invalidateStickerCatalog(kind)
	r.pushStickerSetsUpdate(ctx, userID, kind)
	return true, nil
}

func (r *Router) stickerSetActorUserID(ctx context.Context) (int64, error) {
	if r.deps.Files == nil {
		return 0, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, internalErr()
	}
	return userID, nil
}

func (r *Router) notifyStickerSetMutated(ctx context.Context, userID int64, set domain.StickerSet) {
	kind := userStickerSetKind(set)
	r.invalidateStickerCatalog(kind)
	r.pushStickerSetsUpdate(ctx, userID, kind)
}

func (r *Router) stickerSetCreatorUserID(ctx context.Context, input tg.InputUserClass) (int64, error) {
	currentUserID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, internalErr()
	}
	if r.deps.Users == nil {
		switch v := input.(type) {
		case *tg.InputUserSelf:
			return currentUserID, nil
		case *tg.InputUser:
			if v != nil && v.UserID == currentUserID {
				return currentUserID, nil
			}
		}
		return 0, userIDInvalidErr()
	}
	user, found, err := r.userFromInput(ctx, currentUserID, input)
	if err != nil {
		return 0, internalErr()
	}
	if !found || user.ID != currentUserID {
		return 0, userIDInvalidErr()
	}
	return currentUserID, nil
}

func inputDocumentRef(input tg.InputDocumentClass) (int64, int64, bool) {
	doc, ok := input.(*tg.InputDocument)
	if !ok || doc == nil || doc.ID == 0 || doc.AccessHash == 0 {
		return 0, 0, false
	}
	return doc.ID, doc.AccessHash, true
}

func packShortNameInvalidErr() error  { return tgerr400("PACK_SHORT_NAME_INVALID") }
func packShortNameOccupiedErr() error { return tgerr400("PACK_SHORT_NAME_OCCUPIED") }
func packTitleInvalidErr() error      { return tgerr400("PACK_TITLE_INVALID") }
func packTypeInvalidErr() error       { return tgerr400("PACK_TYPE_INVALID") }
func stickersEmptyErr() error         { return tgerr400("STICKERS_EMPTY") }
func stickersTooMuchErr() error       { return tgerr400("STICKERS_TOO_MUCH") }
func stickerEmojiInvalidErr() error   { return tgerr400("STICKER_EMOJI_INVALID") }
func stickerFileInvalidErr() error    { return tgerr400("STICKER_FILE_INVALID") }
func shortNameInvalidErr() error      { return tgerr400("SHORT_NAME_INVALID") }
func titleInvalidErr() error          { return tgerr400("TITLE_INVALID") }
func positionInvalidErr() error       { return tgerr400("POSITION_INVALID") }

func stickerSetCreateErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStickerSetTitleInvalid):
		return packTitleInvalidErr()
	case errors.Is(err, domain.ErrStickerSetShortNameInvalid):
		return packShortNameInvalidErr()
	case errors.Is(err, domain.ErrStickerSetShortNameOccupied):
		return packShortNameOccupiedErr()
	case errors.Is(err, domain.ErrStickerSetTypeInvalid):
		return packTypeInvalidErr()
	case errors.Is(err, domain.ErrStickerSetEmpty):
		return stickersEmptyErr()
	case errors.Is(err, domain.ErrStickerSetTooMuch):
		return stickersTooMuchErr()
	case errors.Is(err, domain.ErrStickerSetEmojiInvalid):
		return stickerEmojiInvalidErr()
	case errors.Is(err, domain.ErrStickerSetFileInvalid), errors.Is(err, domain.ErrDocumentInvalid):
		return stickerFileInvalidErr()
	case errors.Is(err, domain.ErrStickerSetCreatorInvalid):
		return userIDInvalidErr()
	default:
		return internalErr()
	}
}

func stickerSetShortNameCheckErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStickerSetShortNameInvalid):
		return shortNameInvalidErr()
	default:
		return internalErr()
	}
}

func stickerSetSuggestShortNameErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStickerSetTitleInvalid):
		return titleInvalidErr()
	case errors.Is(err, domain.ErrStickerSetShortNameOccupied):
		return packShortNameOccupiedErr()
	case errors.Is(err, domain.ErrStickerSetCreatorInvalid):
		return userIDInvalidErr()
	default:
		return internalErr()
	}
}

func stickerSetManagementErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStickerSetTitleInvalid):
		return packTitleInvalidErr()
	case errors.Is(err, domain.ErrStickerSetEmpty):
		return stickersEmptyErr()
	case errors.Is(err, domain.ErrStickerSetTooMuch):
		return stickersTooMuchErr()
	case errors.Is(err, domain.ErrStickerSetEmojiInvalid):
		return stickerEmojiInvalidErr()
	case errors.Is(err, domain.ErrStickerSetFileInvalid), errors.Is(err, domain.ErrDocumentInvalid):
		return stickerFileInvalidErr()
	case errors.Is(err, domain.ErrStickerSetCreatorInvalid):
		return userIDInvalidErr()
	case errors.Is(err, domain.ErrStickerSetPositionInvalid):
		return positionInvalidErr()
	case errors.Is(err, domain.ErrStickerSetInvalid), errors.Is(err, domain.ErrStickerSetNotOwned):
		return stickersetInvalidErr()
	default:
		return internalErr()
	}
}
