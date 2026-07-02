package rpc

import (
	"context"

	"github.com/gotd/td/tg"
)

func (r *Router) onMessagesGetRecentLocations(ctx context.Context, req *tg.MessagesGetRecentLocationsRequest) (tg.MessagesMessagesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req == nil || inputPeerClassNil(req.Peer) {
		return nil, peerIDInvalidErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	return &tg.MessagesMessages{
		Messages: []tg.MessageClass{},
		Chats:    r.chatsForInputPeer(ctx, userID, req.Peer),
		Users:    []tg.UserClass{},
	}, nil
}
