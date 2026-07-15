package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// onMessagesGetRecentLocations returns the peer's newest live-location
// messages through the existing media seek indexes. Expired/stopped items are
// deliberately retained: iOS tags every messageMediaGeoLive in its local
// history and applies the active-period check in PeerLiveLocationsContext.
func (r *Router) onMessagesGetRecentLocations(ctx context.Context, req *tg.MessagesGetRecentLocationsRequest) (tg.MessagesMessagesClass, error) {
	if req == nil || req.Limit < 0 || req.Limit > maxSearchResultsLimit {
		return nil, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if input, ok := req.Peer.(*tg.InputPeerUser); ok && input != nil {
		if err := r.validateInputUser(ctx, &tg.InputUser{UserID: input.UserID, AccessHash: input.AccessHash}); err != nil {
			return nil, err
		}
	}
	search := domain.MediaSearchRequest{
		Categories: []domain.MediaCategory{domain.MediaCategoryGeoLive},
		Limit:      req.Limit,
	}
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return &tg.MessagesMessages{Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
		}
		history, err := r.deps.Channels.SearchChannelMedia(ctx, userID, peer.ID, search)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		history = r.enrichChannelHistory(ctx, userID, history)
		r.trackChannelInterest(ctx, userID, peer.ID)
		return r.tgChannelHistoryMessages(ctx, userID, history), nil
	}
	r.clearChannelInterest(ctx, userID)
	if r.deps.Messages == nil {
		return &tg.MessagesMessages{Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	list, err := r.deps.Messages.SearchPrivateMedia(ctx, userID, peer.ID, search)
	if err != nil {
		return nil, internalErr()
	}
	return r.tgMessagesMessages(ctx, userID, r.enrichMessageList(ctx, userID, list)), nil
}
