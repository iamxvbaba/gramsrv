package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func (r *Router) onPaymentsGetStarGiftCollectionsDisplaySettings(ctx context.Context, req *tg.PaymentsGetStarGiftCollectionsDisplaySettingsRequest) (tg.PaymentsStarGiftCollectionsDisplaySettingsClass, error) {
	if req == nil || r.deps.Gifts == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	owner, err := r.starGiftOwnerPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	settings, err := r.deps.Gifts.ListCollectionDisplaySettings(ctx, owner)
	if err != nil {
		return nil, starGiftCollectionDisplayErr(err)
	}
	hash := domain.StarGiftCollectionDisplaySettingsHash(settings)
	if req.Hash != 0 && req.Hash == hash {
		return &tg.PaymentsStarGiftCollectionsDisplaySettingsNotModified{}, nil
	}
	wireSettings, err := r.tgStarGiftCollectionDisplaySettings(ctx, owner, settings)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.PaymentsStarGiftCollectionsDisplaySettings{
		Settings: wireSettings,
		Hash:     hash,
	}, nil
}

func (r *Router) onPaymentsUpdateStarGiftCollectionDisplaySettings(ctx context.Context, req *tg.PaymentsUpdateStarGiftCollectionDisplaySettingsRequest) (*tg.StarGiftCollectionDisplaySettings, error) {
	if req == nil || req.CollectionID <= 0 || r.deps.Gifts == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	owner, err := r.starGiftOwnerPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if err := r.ensureCanManageStarGiftOwner(ctx, userID, owner); err != nil {
		return nil, err
	}
	patch := domain.StarGiftCollectionDisplayPatch{}
	if hidden, ok := req.GetHidden(); ok {
		patch.Hidden = &hidden
	}
	if mainTab, ok := req.GetMainTab(); ok {
		patch.MainTab = &mainTab
	}
	if filterMask, ok := req.GetFilterMask(); ok {
		patch.FilterMask = &filterMask
	}
	if items, ok := req.GetItems(); ok {
		patch.ItemsSet = true
		patch.Items, err = r.resolveStarGiftCollectionDisplayItems(ctx, owner, items)
		if err != nil {
			r.log.Warn("star gift collection display update rejected",
				zap.Int64("user_id", userID),
				zap.String("owner_type", string(owner.Type)),
				zap.Int64("owner_id", owner.ID),
				zap.Int("collection_id", req.CollectionID),
				zap.Int("items_count", len(items)),
				zap.Error(err),
			)
			return nil, inputRequestInvalidErr()
		}
	}
	settings, err := r.deps.Gifts.UpdateCollectionDisplaySettings(ctx, owner, req.CollectionID, patch)
	if err != nil {
		r.log.Warn("star gift collection display update failed",
			zap.Int64("user_id", userID),
			zap.String("owner_type", string(owner.Type)),
			zap.Int64("owner_id", owner.ID),
			zap.Int("collection_id", req.CollectionID),
			zap.Error(err),
		)
		return nil, starGiftCollectionDisplayErr(err)
	}
	r.invalidateStarGiftOwnerProjection(owner)
	wireSettings, err := r.tgStarGiftCollectionDisplaySettings(ctx, owner, []domain.StarGiftCollectionDisplaySettings{settings})
	if err != nil || len(wireSettings) != 1 {
		return nil, internalErr()
	}
	return &wireSettings[0], nil
}

func (r *Router) resolveStarGiftCollectionDisplayItems(ctx context.Context, owner domain.Peer, items []tg.StarGiftCollectionDisplayItem) ([]domain.StarGiftCollectionDisplayItem, error) {
	// The custom wire contract exposes only Telegram public identities:
	// user msg_id or channel saved_id. The database storage id is resolved
	// internally and never accepted from a client request.
	refs := make([]domain.SavedStarGiftRef, 0, len(items))
	for _, item := range items {
		if item.GiftID <= 0 {
			return nil, domain.ErrStarGiftCollectionDisplayInvalid
		}
		ref := domain.SavedStarGiftRef{Owner: owner}
		if owner.Type == domain.PeerTypeChannel {
			ref.SavedID = item.GiftID
		} else if owner.Type == domain.PeerTypeUser && item.GiftID <= int64(^uint(0)>>1) {
			ref.MsgID = int(item.GiftID)
		} else {
			return nil, domain.ErrStarGiftCollectionDisplayInvalid
		}
		refs = append(refs, ref)
	}
	resolvedIDs, err := r.deps.Gifts.ResolveSavedIDs(ctx, owner, refs)
	if err != nil {
		return nil, err
	}
	if len(resolvedIDs) != len(items) {
		return nil, domain.ErrStarGiftCollectionDisplayInvalid
	}
	out := make([]domain.StarGiftCollectionDisplayItem, 0, len(items))
	for i, item := range items {
		out = append(out, domain.StarGiftCollectionDisplayItem{
			GiftID: resolvedIDs[i],
			Order:  item.Order,
			Size:   item.Size,
		})
	}
	return out, nil
}

type savedStarGiftsByIDsService interface {
	SavedStarGiftsByIDs(context.Context, domain.Peer, []int64) (map[int64]domain.SavedStarGift, error)
}

func (r *Router) tgStarGiftCollectionDisplaySettings(ctx context.Context, owner domain.Peer, in []domain.StarGiftCollectionDisplaySettings) ([]tg.StarGiftCollectionDisplaySettings, error) {
	lookup, ok := r.deps.Gifts.(savedStarGiftsByIDsService)
	if !ok {
		return nil, domain.ErrStarGiftCollectionDisplayUnsupported
	}
	ids := make([]int64, 0)
	for _, setting := range in {
		for _, item := range setting.Items {
			ids = append(ids, item.GiftID)
		}
	}
	gifts, err := lookup.SavedStarGiftsByIDs(ctx, owner, ids)
	if err != nil {
		return nil, err
	}
	out := make([]tg.StarGiftCollectionDisplaySettings, 0, len(in))
	for _, setting := range in {
		wire := tgStarGiftCollectionDisplaySetting(setting)
		for i, item := range setting.Items {
			gift, found := gifts[item.GiftID]
			if !found {
				return nil, domain.ErrStarGiftNotFound
			}
			if owner.Type == domain.PeerTypeChannel {
				wire.Items[i].GiftID = gift.SavedID
			} else {
				if gift.MsgID <= 0 {
					return nil, domain.ErrStarGiftNotFound
				}
				wire.Items[i].GiftID = int64(gift.MsgID)
			}
		}
		out = append(out, wire)
	}
	return out, nil
}

func tgStarGiftCollectionDisplaySetting(in domain.StarGiftCollectionDisplaySettings) tg.StarGiftCollectionDisplaySettings {
	out := tg.StarGiftCollectionDisplaySettings{
		CollectionID: in.CollectionID,
		FilterMask:   in.FilterMask,
		Items:        make([]tg.StarGiftCollectionDisplayItem, 0, len(in.Items)),
	}
	if in.Hidden {
		out.Flags.Set(0)
	}
	if in.MainTab {
		out.Flags.Set(1)
	}
	for _, item := range in.Items {
		out.Items = append(out.Items, tg.StarGiftCollectionDisplayItem{GiftID: item.GiftID, Order: item.Order, Size: item.Size})
	}
	return out
}

func starGiftCollectionDisplayErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStarGiftCollectionNotFound),
		errors.Is(err, domain.ErrStarGiftCollectionDisplayInvalid),
		errors.Is(err, domain.ErrStarGiftCollectionDisplayUnsupported):
		return inputRequestInvalidErr()
	default:
		return internalErr()
	}
}
