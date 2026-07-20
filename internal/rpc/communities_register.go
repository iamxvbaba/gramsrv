package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

func (r *Router) registerCommunities(d *tlprofile.Dispatcher) {
	registerRPC[*tg.CommunitiesCreateRequest](d, tlprofile.SemanticMethodCommunitiesCreate, func(ctx context.Context, req *tg.CommunitiesCreateRequest) (any, error) {
		return r.onCommunitiesCreate(ctx, req)
	})
	registerRPC[*tg.CommunitiesTogglePeerLinkRequest](d, tlprofile.SemanticMethodCommunitiesTogglePeerLink, func(ctx context.Context, req *tg.CommunitiesTogglePeerLinkRequest) (any, error) {
		return r.onCommunitiesTogglePeerLink(ctx, req)
	})
	registerRPC[*tg.CommunitiesGetJoinedCommunitiesRequest](d, tlprofile.SemanticMethodCommunitiesGetJoinedCommunities, func(ctx context.Context, req *tg.CommunitiesGetJoinedCommunitiesRequest) (any, error) {
		return r.onCommunitiesGetJoined(ctx)
	})
	registerRPC[*tg.CommunitiesToggleCommunityCollapsedInDialogsRequest](d, tlprofile.SemanticMethodCommunitiesToggleCommunityCollapsedInDialogs, func(ctx context.Context, req *tg.CommunitiesToggleCommunityCollapsedInDialogsRequest) (any, error) {
		return r.onCommunitiesToggleCollapsed(ctx, req)
	})
	registerRPC[*tg.CommunitiesGetPeerLinkRequestsRequest](d, tlprofile.SemanticMethodCommunitiesGetPeerLinkRequests, func(ctx context.Context, req *tg.CommunitiesGetPeerLinkRequestsRequest) (any, error) {
		return r.onCommunitiesGetPeerLinkRequests(ctx, req)
	})
	registerRPC[*tg.CommunitiesTogglePeerLinkRequestApprovalRequest](d, tlprofile.SemanticMethodCommunitiesTogglePeerLinkRequestApproval, func(ctx context.Context, req *tg.CommunitiesTogglePeerLinkRequestApprovalRequest) (any, error) {
		return r.onCommunitiesTogglePeerLinkRequestApproval(ctx, req)
	})
	registerRPC[*tg.CommunitiesToggleAllPeerLinkRequestApprovalRequest](d, tlprofile.SemanticMethodCommunitiesToggleAllPeerLinkRequestApproval, func(ctx context.Context, req *tg.CommunitiesToggleAllPeerLinkRequestApprovalRequest) (any, error) {
		return r.onCommunitiesToggleAllPeerLinkRequestApproval(ctx, req)
	})
	registerRPC[*tg.CommunitiesToggleParticipantBannedRequest](d, tlprofile.SemanticMethodCommunitiesToggleParticipantBanned, func(ctx context.Context, req *tg.CommunitiesToggleParticipantBannedRequest) (any, error) {
		return r.onCommunitiesToggleParticipantBanned(ctx, req)
	})
	registerRPC[*tg.CommunitiesGetParticipantJoinedChatsRequest](d, tlprofile.SemanticMethodCommunitiesGetParticipantJoinedChats, func(ctx context.Context, req *tg.CommunitiesGetParticipantJoinedChatsRequest) (any, error) {
		return r.onCommunitiesGetParticipantJoinedChats(ctx, req)
	})
}
