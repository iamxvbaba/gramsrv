package rpc

import (
	"context"
	"fmt"
	"sort"

	"telesrv/internal/domain"
)

func (r *Router) pinnedDialogsList(ctx context.Context, userID int64, folderID int) (domain.DialogList, error) {
	list, err := r.pinnedDialogsBaseList(ctx, userID, folderID)
	if err != nil {
		return domain.DialogList{}, err
	}
	return r.withCommunityDialogList(ctx, userID, domain.DialogFilter{PinnedOnly: true, HasFolderID: true, FolderID: folderID}, list)
}

func (r *Router) pinnedDialogsBaseList(ctx context.Context, userID int64, folderID int) (domain.DialogList, error) {
	if r == nil {
		return domain.DialogList{}, nil
	}
	key := fmt.Sprintf("%d:%d", userID, folderID)
	value, err, _ := r.dialogsPinnedSF.Do(key, func() (any, error) {
		if r.deps.Dialogs == nil {
			return domain.DialogList{}, nil
		}
		return r.deps.Dialogs.GetDialogs(ctx, userID, domain.DialogFilter{
			PinnedOnly:  true,
			HasFolderID: true,
			FolderID:    folderID,
			Limit:       100,
		})
	})
	if err != nil {
		return domain.DialogList{}, err
	}
	if list, ok := value.(domain.DialogList); ok {
		return list, nil
	}
	return domain.DialogList{}, nil
}

func (r *Router) combinedPinnedDialogsList(ctx context.Context, userID int64, folderID int) (domain.DialogList, error) {
	list, err := r.pinnedDialogsBaseList(ctx, userID, folderID)
	if err != nil {
		return domain.DialogList{}, err
	}
	return r.withCollapsedCommunityDialogs(ctx, userID, domain.DialogFilter{PinnedOnly: true, HasFolderID: true, FolderID: folderID}, list)
}

// combinedPinnedDialogPeers merges ordinary dialogs and collapsed Communities
// by their shared server order. The two persistence implementations deliberately
// store their own rows, but Layer 228 exposes one messages.getPinnedDialogs list.
func combinedPinnedDialogPeers(list domain.DialogList) []domain.Peer {
	type item struct {
		peer     domain.Peer
		order    int
		sequence int
	}
	items := make([]item, 0, len(list.Dialogs)+len(list.Communities))
	seen := make(map[domain.Peer]struct{}, cap(items))
	appendItem := func(peer domain.Peer, pinned bool, order int) {
		if !pinned || peer.ID == 0 {
			return
		}
		if _, ok := seen[peer]; ok {
			return
		}
		seen[peer] = struct{}{}
		items = append(items, item{peer: peer, order: order, sequence: len(items)})
	}
	for _, dialog := range list.Dialogs {
		appendItem(dialog.Peer, dialog.Pinned, dialog.PinnedOrder)
	}
	for _, community := range list.Communities {
		appendItem(domain.Peer{Type: domain.PeerTypeCommunity, ID: community.Community.ID}, community.State.Pinned, community.State.PinnedOrder)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].order != items[j].order {
			return items[i].order > items[j].order
		}
		return items[i].sequence < items[j].sequence
	})
	out := make([]domain.Peer, 0, len(items))
	for _, item := range items {
		out = append(out, item.peer)
	}
	return out
}

func (r *Router) ensureCombinedPinCapacity(ctx context.Context, userID int64, folderID int, peer domain.Peer) error {
	list, err := r.combinedPinnedDialogsList(ctx, userID, folderID)
	if err != nil {
		return err
	}
	peers := combinedPinnedDialogPeers(list)
	for _, pinned := range peers {
		if pinned == peer {
			return nil
		}
	}
	if len(peers) >= domain.PinnedDialogsLimit(folderID, r.userIsPremium(ctx, userID)) {
		return domain.ErrPinnedDialogsTooMuch
	}
	return nil
}

// promoteCombinedPinnedDialog assigns one collision-free order across ordinary
// dialogs and Communities. It is called after the underlying row is pinned so
// both stores can project the same mixed order without owning each other's data.
func (r *Router) promoteCombinedPinnedDialog(ctx context.Context, userID int64, folderID int, peer domain.Peer) error {
	list, err := r.combinedPinnedDialogsList(ctx, userID, folderID)
	if err != nil {
		return err
	}
	current := combinedPinnedDialogPeers(list)
	order := make([]domain.Peer, 0, len(current)+1)
	order = append(order, peer)
	for _, candidate := range current {
		if candidate != peer {
			order = append(order, candidate)
		}
	}
	if r.deps.Dialogs != nil {
		if _, err := r.deps.Dialogs.ReorderPinned(ctx, userID, folderID, order, false); err != nil {
			return err
		}
	}
	if folderID == domain.DialogMainFolderID && r.deps.Communities != nil {
		if _, err := r.deps.Communities.ReorderPinned(ctx, userID, order, false); err != nil {
			return err
		}
	}
	return nil
}
