package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) onPaymentsGetStarGiftCollections(ctx context.Context, req *tg.PaymentsGetStarGiftCollectionsRequest) (tg.PaymentsStarGiftCollectionsClass, error) {
	if req == nil {
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
	if r.deps.Gifts == nil {
		return &tg.PaymentsStarGiftCollections{Collections: []tg.StarGiftCollection{}}, nil
	}
	collections, err := r.deps.Gifts.ListCollections(ctx, owner)
	if err != nil {
		return nil, starGiftCollectionErr(err)
	}
	if req.Hash != 0 && req.Hash == domain.StarGiftCollectionsHash(collections) {
		return &tg.PaymentsStarGiftCollectionsNotModified{}, nil
	}
	return &tg.PaymentsStarGiftCollections{Collections: tgStarGiftCollections(collections)}, nil
}

func (r *Router) onPaymentsCreateStarGiftCollection(ctx context.Context, req *tg.PaymentsCreateStarGiftCollectionRequest) (*tg.StarGiftCollection, error) {
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
	if err := r.ensureCanManageStarGiftOwner(ctx, userID, owner); err != nil {
		return nil, err
	}
	ids, err := r.resolveStarGiftCollectionRefs(ctx, userID, owner, req.Stargift)
	if err != nil {
		return nil, err
	}
	collection, err := r.deps.Gifts.CreateCollection(ctx, owner, strings.TrimSpace(req.Title), ids)
	if err != nil {
		return nil, starGiftCollectionErr(err)
	}
	r.invalidateStarGiftOwnerProjection(owner)
	out := tgStarGiftCollection(collection)
	return &out, nil
}

func (r *Router) onPaymentsUpdateStarGiftCollection(ctx context.Context, req *tg.PaymentsUpdateStarGiftCollectionRequest) (*tg.StarGiftCollection, error) {
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
	patch := domain.StarGiftCollectionPatch{}
	if title, ok := req.GetTitle(); ok {
		title = strings.TrimSpace(title)
		patch.Title = &title
	}
	if refs, ok := req.GetDeleteStargift(); ok {
		patch.DeleteIDs, err = r.resolveStarGiftCollectionRefs(ctx, userID, owner, refs)
		if err != nil {
			return nil, err
		}
	}
	if refs, ok := req.GetAddStargift(); ok {
		patch.AddIDs, err = r.resolveStarGiftCollectionRefs(ctx, userID, owner, refs)
		if err != nil {
			return nil, err
		}
	}
	if refs, ok := req.GetOrder(); ok {
		patch.Order, err = r.resolveStarGiftCollectionRefs(ctx, userID, owner, refs)
		if err != nil {
			return nil, err
		}
	}
	collection, err := r.deps.Gifts.UpdateCollection(ctx, owner, req.CollectionID, patch)
	if err != nil {
		return nil, starGiftCollectionErr(err)
	}
	r.invalidateStarGiftOwnerProjection(owner)
	out := tgStarGiftCollection(collection)
	return &out, nil
}

func (r *Router) onPaymentsDeleteStarGiftCollection(ctx context.Context, req *tg.PaymentsDeleteStarGiftCollectionRequest) (bool, error) {
	if req == nil || req.CollectionID <= 0 || r.deps.Gifts == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	owner, err := r.starGiftOwnerPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if err := r.ensureCanManageStarGiftOwner(ctx, userID, owner); err != nil {
		return false, err
	}
	deleted, err := r.deps.Gifts.DeleteCollection(ctx, owner, req.CollectionID)
	if err != nil {
		return false, starGiftCollectionErr(err)
	}
	if !deleted {
		return false, starGiftCollectionErr(domain.ErrStarGiftCollectionNotFound)
	}
	r.invalidateStarGiftOwnerProjection(owner)
	return true, nil
}

func (r *Router) onPaymentsReorderStarGiftCollections(ctx context.Context, req *tg.PaymentsReorderStarGiftCollectionsRequest) (bool, error) {
	if req == nil || r.deps.Gifts == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	owner, err := r.starGiftOwnerPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if err := r.ensureCanManageStarGiftOwner(ctx, userID, owner); err != nil {
		return false, err
	}
	if err := r.deps.Gifts.ReorderCollections(ctx, owner, req.Order); err != nil {
		return false, starGiftCollectionErr(err)
	}
	return true, nil
}

func (r *Router) onPaymentsToggleStarGiftsPinnedToTop(ctx context.Context, req *tg.PaymentsToggleStarGiftsPinnedToTopRequest) (bool, error) {
	if req == nil || r.deps.Gifts == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	owner, err := r.starGiftOwnerPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if err := r.ensureCanManageStarGiftOwner(ctx, userID, owner); err != nil {
		return false, err
	}
	ids, err := r.resolveStarGiftCollectionRefs(ctx, userID, owner, req.Stargift)
	if err != nil {
		return false, err
	}
	if err := r.deps.Gifts.SetPinned(ctx, owner, ids); err != nil {
		return false, starGiftCollectionErr(err)
	}
	r.invalidateStarGiftOwnerProjection(owner)
	return true, nil
}

func (r *Router) resolveStarGiftCollectionRefs(ctx context.Context, userID int64, owner domain.Peer, refs []tg.InputSavedStarGiftClass) ([]int64, error) {
	if len(refs) > domain.MaxStarGiftCollectionItems {
		return nil, inputRequestInvalidErr()
	}
	domainRefs := make([]domain.SavedStarGiftRef, 0, len(refs))
	for _, input := range refs {
		ref, ok, err := r.starGiftRefFromInput(ctx, userID, input)
		if err != nil {
			return nil, err
		}
		if !ok || ref.Owner != owner {
			return nil, starGiftInvalidErr()
		}
		domainRefs = append(domainRefs, ref)
	}
	ids, err := r.deps.Gifts.ResolveSavedIDs(ctx, owner, domainRefs)
	if err != nil {
		return nil, starGiftCollectionErr(err)
	}
	return ids, nil
}

func tgStarGiftCollections(in []domain.StarGiftCollection) []tg.StarGiftCollection {
	out := make([]tg.StarGiftCollection, 0, len(in))
	for _, collection := range in {
		out = append(out, tgStarGiftCollection(collection))
	}
	return out
}

func tgStarGiftCollection(in domain.StarGiftCollection) tg.StarGiftCollection {
	return tg.StarGiftCollection{
		CollectionID: in.CollectionID,
		Title:        in.Title,
		GiftsCount:   len(in.GiftIDs),
		Hash:         in.Hash,
	}
}

func starGiftCollectionErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStarGiftNotFound),
		errors.Is(err, domain.ErrStarGiftCollectibleInvalid),
		errors.Is(err, domain.ErrStarGiftCollectionNotFound),
		errors.Is(err, domain.ErrStarGiftCollectionsFull):
		return inputRequestInvalidErr()
	default:
		return internalErr()
	}
}
