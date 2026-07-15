package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

type userStickerSetService interface {
	InstallUserStickerSet(ctx context.Context, userID int64, setID int64, kind domain.StickerSetKind, archived bool, installedDate int) error
	UninstallUserStickerSet(ctx context.Context, userID int64, setID int64) error
	SetUserStickerSetArchived(ctx context.Context, userID int64, setID int64, archived bool, now int) error
	ReorderUserStickerSets(ctx context.Context, userID int64, kind domain.StickerSetKind, order []int64, now int) error
	ListUserStickerSets(ctx context.Context, userID int64, kind domain.StickerSetKind, archived *bool, offsetID int64, limit int) ([]domain.UserStickerSet, int, error)
}

func (r *Router) userStickerSetSvc() (userStickerSetService, bool) {
	svc, ok := r.deps.Account.(userStickerSetService)
	return svc, ok
}

func (r *Router) onMessagesInstallStickerSet(ctx context.Context, req *tg.MessagesInstallStickerSetRequest) (tg.MessagesStickerSetInstallResultClass, error) {
	if req == nil {
		return nil, stickersetInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	set, _, err := r.resolveInstallableStickerSet(ctx, req.Stickerset)
	if err != nil {
		return nil, err
	}
	if svc, ok := r.userStickerSetSvc(); ok {
		if err := svc.InstallUserStickerSet(ctx, userID, set.ID, userStickerSetKind(set), req.Archived, int(r.clock.Now().Unix())); err != nil {
			return nil, internalErr()
		}
	}
	r.pushStickerSetsUpdate(ctx, userID, userStickerSetKind(set))
	return &tg.MessagesStickerSetInstallResultSuccess{}, nil
}

func (r *Router) onMessagesUninstallStickerSet(ctx context.Context, input tg.InputStickerSetClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	set, _, err := r.resolveInstallableStickerSet(ctx, input)
	if err != nil {
		return false, err
	}
	if svc, ok := r.userStickerSetSvc(); ok {
		if err := svc.UninstallUserStickerSet(ctx, userID, set.ID); err != nil {
			return false, internalErr()
		}
	}
	r.pushStickerSetsUpdate(ctx, userID, userStickerSetKind(set))
	return true, nil
}

func (r *Router) onMessagesReorderStickerSets(ctx context.Context, req *tg.MessagesReorderStickerSetsRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	kind := stickerSetKindFromFlags(req.Masks, req.Emojis)
	order := uniqueNonZeroInt64s(req.Order, domain.MaxInstalledStickerSets)
	if len(order) == 0 {
		return true, nil
	}
	if svc, ok := r.userStickerSetSvc(); ok {
		if err := svc.ReorderUserStickerSets(ctx, userID, kind, order, int(r.clock.Now().Unix())); err != nil {
			return false, internalErr()
		}
	}
	r.pushStickerSetsOrderUpdate(ctx, userID, kind, order)
	return true, nil
}

func (r *Router) onMessagesToggleStickerSets(ctx context.Context, req *tg.MessagesToggleStickerSetsRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if len(req.Stickersets) > domain.MaxInstalledStickerSets {
		return false, limitInvalidErr()
	}
	svc, hasSvc := r.userStickerSetSvc()
	kinds := make(map[domain.StickerSetKind]bool)
	now := int(r.clock.Now().Unix())
	for _, input := range req.Stickersets {
		set, _, err := r.resolveInstallableStickerSet(ctx, input)
		if err != nil {
			return false, err
		}
		kind := userStickerSetKind(set)
		kinds[kind] = true
		if !hasSvc {
			continue
		}
		switch {
		case req.Uninstall:
			if err := svc.UninstallUserStickerSet(ctx, userID, set.ID); err != nil {
				return false, internalErr()
			}
		case req.Archive:
			if err := svc.SetUserStickerSetArchived(ctx, userID, set.ID, true, now); err != nil {
				return false, internalErr()
			}
		case req.Unarchive:
			if err := svc.SetUserStickerSetArchived(ctx, userID, set.ID, false, now); err != nil {
				return false, internalErr()
			}
		default:
			if err := svc.InstallUserStickerSet(ctx, userID, set.ID, kind, false, now); err != nil {
				return false, internalErr()
			}
		}
	}
	for kind := range kinds {
		r.pushStickerSetsUpdate(ctx, userID, kind)
	}
	return true, nil
}

func (r *Router) onMessagesGetMyStickers(ctx context.Context, req *tg.MessagesGetMyStickersRequest) (*tg.MessagesMyStickers, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Files == nil {
		return &tg.MessagesMyStickers{Count: 0, Sets: []tg.StickerSetCoveredClass{}}, nil
	}
	limit := 50
	var offsetID int64
	if req != nil {
		if req.OffsetID < 0 {
			return nil, offsetInvalidErr()
		}
		if req.Limit < 0 || req.Limit > domain.MaxCreatedStickerSets {
			return nil, limitInvalidErr()
		}
		if req.Limit > 0 {
			limit = req.Limit
		}
		offsetID = req.OffsetID
	}
	sets, total, err := r.deps.Files.ListCreatedStickerSets(ctx, userID, offsetID, limit)
	if err != nil {
		return nil, internalErr()
	}
	covered := make([]tg.StickerSetCoveredClass, 0, len(sets))
	for _, set := range sets {
		set.Creator = true
		covered = append(covered, &tg.StickerSetNoCovered{Set: tgStickerSet(set)})
	}
	return &tg.MessagesMyStickers{Count: total, Sets: covered}, nil
}

func (r *Router) resolveInstallableStickerSet(ctx context.Context, input tg.InputStickerSetClass) (domain.StickerSet, []domain.Document, error) {
	ref, ok := stickerSetRefFromInput(input)
	if !ok || (ref.Kind != domain.StickerSetRefByID && ref.Kind != domain.StickerSetRefByShortName) {
		return domain.StickerSet{}, nil, stickersetInvalidErr()
	}
	if r.deps.Files == nil {
		return domain.StickerSet{}, nil, stickersetInvalidErr()
	}
	set, docs, found, err := r.deps.Files.ResolveStickerSet(ctx, ref)
	if err != nil {
		return domain.StickerSet{}, nil, internalErr()
	}
	if !found || set.ID == 0 {
		return domain.StickerSet{}, nil, stickersetInvalidErr()
	}
	if ref.Kind == domain.StickerSetRefByID && set.AccessHash != ref.AccessHash {
		return domain.StickerSet{}, nil, stickersetInvalidErr()
	}
	return set, docs, nil
}

func userStickerSetKind(set domain.StickerSet) domain.StickerSetKind {
	switch {
	case set.Kind == domain.StickerSetKindMasks || set.Masks:
		return domain.StickerSetKindMasks
	case set.Kind == domain.StickerSetKindEmoji || set.Emojis:
		return domain.StickerSetKindEmoji
	default:
		return domain.StickerSetKindStickers
	}
}

func stickerSetKindFromFlags(masks, emojis bool) domain.StickerSetKind {
	switch {
	case masks:
		return domain.StickerSetKindMasks
	case emojis:
		return domain.StickerSetKindEmoji
	default:
		return domain.StickerSetKindStickers
	}
}

func uniqueNonZeroInt64s(in []int64, max int) []int64 {
	if max <= 0 {
		max = len(in)
	}
	out := make([]int64, 0, len(in))
	seen := make(map[int64]struct{}, len(in))
	for _, v := range in {
		if v == 0 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
		if len(out) >= max {
			break
		}
	}
	return out
}

func (r *Router) pushStickerSetsUpdate(ctx context.Context, userID int64, kind domain.StickerSetKind) {
	update := &tg.UpdateStickerSets{}
	switch kind {
	case domain.StickerSetKindMasks:
		update.SetMasks(true)
	case domain.StickerSetKindEmoji:
		update.SetEmojis(true)
	}
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
	})
}

func (r *Router) pushStickerSetsOrderUpdate(ctx context.Context, userID int64, kind domain.StickerSetKind, order []int64) {
	update := &tg.UpdateStickerSetsOrder{Order: append([]int64(nil), order...)}
	switch kind {
	case domain.StickerSetKindMasks:
		update.SetMasks(true)
	case domain.StickerSetKindEmoji:
		update.SetEmojis(true)
	}
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
	})
}
