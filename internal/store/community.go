package store

import (
	"context"

	"telesrv/internal/domain"
)

// CommunityStore persists the Layer 228 Community aggregate. Mutations that
// touch a link and the linked peer's denormalized linked_community_id must be
// atomic in durable implementations.
type CommunityStore interface {
	CreateCommunity(ctx context.Context, req domain.CreateCommunityRequest) (domain.CommunityView, error)
	GetCommunity(ctx context.Context, viewerUserID, communityID int64) (domain.CommunityView, error)
	GetCommunities(ctx context.Context, viewerUserID int64, communityIDs []int64) ([]domain.CommunityView, error)
	ListJoinedCommunities(ctx context.Context, viewerUserID int64) ([]domain.CommunityView, error)
	ToggleCommunityPeerLink(ctx context.Context, req domain.CommunityTogglePeerLinkRequest) (domain.CommunityTogglePeerLinkResult, error)
	SetCommunityCollapsed(ctx context.Context, userID, communityID int64, collapsed bool) (domain.CommunityView, bool, error)
	ListCommunityPeerLinkRequests(ctx context.Context, viewerUserID, communityID int64, offset string, limit int) (domain.CommunityPeerLinkRequestPage, error)
	DecideCommunityPeerLinkRequest(ctx context.Context, actorUserID, communityID int64, peer domain.Peer, reject bool, date int) (domain.CommunityTogglePeerLinkResult, error)
	DecideAllCommunityPeerLinkRequests(ctx context.Context, actorUserID, communityID int64, reject bool, date int) ([]domain.CommunityTogglePeerLinkResult, error)
	ToggleCommunityParticipantBanned(ctx context.Context, actorUserID, communityID, participantUserID int64, unban bool, date int) (domain.CommunityParticipantBanResult, error)
	GetCommunityParticipantJoinedChats(ctx context.Context, viewerUserID, communityID, participantUserID int64) (domain.CommunityParticipantJoinedChats, error)
	ListCommunityParticipants(ctx context.Context, viewerUserID, communityID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.CommunityParticipantList, error)
	EditCommunityTitle(ctx context.Context, actorUserID, communityID int64, title string) (domain.CommunityView, bool, error)
	EditCommunityAbout(ctx context.Context, actorUserID, communityID int64, about string) (domain.CommunityView, bool, error)
	EditCommunityAdmin(ctx context.Context, req domain.CommunityEditAdminRequest) (domain.CommunityView, bool, error)
	EditCommunityDefaultBannedRights(ctx context.Context, actorUserID, communityID int64, rights domain.ChannelBannedRights) (domain.CommunityView, bool, error)
	SetCommunityPhoto(ctx context.Context, actorUserID, communityID int64, photo *domain.Photo, date int) (domain.CommunityView, bool, error)
	DeleteCommunity(ctx context.Context, actorUserID, communityID int64, date int) (domain.CommunityView, []domain.Peer, error)
	SetCommunityPinned(ctx context.Context, userID, communityID int64, pinned bool) (changed bool, err error)
	ReorderCommunityPinned(ctx context.Context, userID int64, order []domain.Peer, force bool) (changed bool, err error)
	CommunitySearchScope(ctx context.Context, viewerUserID, communityID int64) (domain.CommunitySearchScope, error)
}
