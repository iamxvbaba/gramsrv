package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

func (r *Router) registerFolders(d *tlprofile.Dispatcher) {
	registerRPC[*tg.FoldersEditPeerFoldersRequest](d, tlprofile.SemanticMethodFoldersEditPeerFolders, func(ctx context.Context, layerRequest *tg.FoldersEditPeerFoldersRequest) (any, error) {
		return r.onFoldersEditPeerFolders(ctx, layerRequest.
			FolderPeers)
	})

}

func (r *Router) onFoldersEditPeerFolders(ctx context.Context, folderPeers []tg.InputFolderPeer) (tg.UpdatesClass, error) {
	if len(folderPeers) > maxDialogInputPeers {
		return nil, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	updates := make([]domain.FolderPeerUpdate, 0, len(folderPeers))
	seen := make(map[domain.Peer]struct{}, len(folderPeers))
	for _, item := range folderPeers {
		if item.FolderID != domain.DialogMainFolderID && item.FolderID != domain.DialogArchiveFolderID {
			return nil, folderIDInvalidErr()
		}
		peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, item.Peer)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		updates = append(updates, domain.FolderPeerUpdate{Peer: peer, FolderID: item.FolderID})
	}
	if len(updates) == 0 {
		return &tg.Updates{Date: int(r.clock.Now().Unix()), Seq: 0}, nil
	}
	if r.deps.Dialogs != nil {
		if err := r.deps.Dialogs.EditPeerFolders(ctx, userID, updates); err != nil {
			return nil, internalErr()
		}
	}
	event := domain.UpdateEvent{
		Type:        domain.UpdateEventFolderPeers,
		FolderPeers: updates,
		PtsCount:    1,
		Date:        int(r.clock.Now().Unix()),
	}
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, _, err = r.deps.Updates.RecordFolderPeers(ctx, authKeyID, userID, updates, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return nil, internalErr()
		}
	}
	out := tgUpdateForOutboxEvent(event)
	if out == nil {
		out = &tg.Updates{Date: event.Date, Seq: 0}
	}
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, out)
	return out, nil
}
