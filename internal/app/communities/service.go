package communities

import (
	"context"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service owns Community business validation. The store is the aggregate
// transaction boundary because link changes span community/link and peer rows.
type Service struct {
	communities store.CommunityStore
}

func NewService(communities store.CommunityStore) *Service {
	return &Service{communities: communities}
}

func validPeer(peer domain.Peer) bool {
	return peer.ID > 0 && (peer.Type == domain.PeerTypeChannel || peer.Type == domain.PeerTypeUser)
}

func validVisibility(v domain.CommunityPeerVisibility) bool {
	return v == domain.CommunityPeerVisible || v == domain.CommunityPeerHidden
}

func (s *Service) Create(ctx context.Context, userID int64, req domain.CreateCommunityRequest) (domain.CommunityView, error) {
	if s == nil || s.communities == nil || userID == 0 || !validPeer(req.InitialPeer) || !validVisibility(req.Visibility) {
		return domain.CommunityView{}, domain.ErrCommunityInvalid
	}
	req.CreatorUserID = userID
	req.Title = strings.TrimSpace(req.Title)
	req.About = strings.TrimSpace(req.About)
	if req.Title == "" || utf8.RuneCountInString(req.Title) > domain.MaxCommunityTitleRunes {
		return domain.CommunityView{}, domain.ErrChannelTitleInvalid
	}
	if utf8.RuneCountInString(req.About) > domain.MaxCommunityAboutRunes {
		return domain.CommunityView{}, domain.ErrAboutTooLong
	}
	return s.communities.CreateCommunity(ctx, req)
}

func (s *Service) Get(ctx context.Context, userID, communityID int64) (domain.CommunityView, error) {
	if s == nil || s.communities == nil || userID == 0 || communityID == 0 {
		return domain.CommunityView{}, domain.ErrCommunityInvalid
	}
	return s.communities.GetCommunity(ctx, userID, communityID)
}

func (s *Service) GetMany(ctx context.Context, userID int64, ids []int64) ([]domain.CommunityView, error) {
	if s == nil || s.communities == nil || userID == 0 {
		return nil, domain.ErrCommunityInvalid
	}
	return s.communities.GetCommunities(ctx, userID, ids)
}

func (s *Service) ListJoined(ctx context.Context, userID int64) ([]domain.CommunityView, error) {
	if s == nil || s.communities == nil || userID == 0 {
		return nil, domain.ErrCommunityInvalid
	}
	return s.communities.ListJoinedCommunities(ctx, userID)
}

func (s *Service) TogglePeerLink(ctx context.Context, userID int64, req domain.CommunityTogglePeerLinkRequest) (domain.CommunityTogglePeerLinkResult, error) {
	if s == nil || s.communities == nil || userID == 0 || req.CommunityID == 0 || !validPeer(req.Peer) {
		return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityInvalid
	}
	if !req.Deleted && !validVisibility(req.Visibility) {
		return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityPeerInvalid
	}
	req.ActorUserID = userID
	return s.communities.ToggleCommunityPeerLink(ctx, req)
}

func (s *Service) SetCollapsed(ctx context.Context, userID, communityID int64, collapsed bool) (domain.CommunityView, bool, error) {
	if s == nil || s.communities == nil || userID == 0 || communityID == 0 {
		return domain.CommunityView{}, false, domain.ErrCommunityInvalid
	}
	return s.communities.SetCommunityCollapsed(ctx, userID, communityID, collapsed)
}

func (s *Service) ListPeerLinkRequests(ctx context.Context, userID, communityID int64, offset string, limit int) (domain.CommunityPeerLinkRequestPage, error) {
	if s == nil || s.communities == nil || userID == 0 || communityID == 0 {
		return domain.CommunityPeerLinkRequestPage{}, domain.ErrCommunityInvalid
	}
	if limit <= 0 || limit > domain.MaxCommunityLinkRequests {
		limit = domain.MaxCommunityLinkRequests
	}
	return s.communities.ListCommunityPeerLinkRequests(ctx, userID, communityID, offset, limit)
}

func (s *Service) DecidePeerLinkRequest(ctx context.Context, userID, communityID int64, peer domain.Peer, reject bool, date int) (domain.CommunityTogglePeerLinkResult, error) {
	if s == nil || s.communities == nil || userID == 0 || communityID == 0 || !validPeer(peer) {
		return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityInvalid
	}
	return s.communities.DecideCommunityPeerLinkRequest(ctx, userID, communityID, peer, reject, date)
}

func (s *Service) DecideAllPeerLinkRequests(ctx context.Context, userID, communityID int64, reject bool, date int) ([]domain.CommunityTogglePeerLinkResult, error) {
	if s == nil || s.communities == nil || userID == 0 || communityID == 0 {
		return nil, domain.ErrCommunityInvalid
	}
	return s.communities.DecideAllCommunityPeerLinkRequests(ctx, userID, communityID, reject, date)
}

func (s *Service) ToggleParticipantBanned(ctx context.Context, userID, communityID, participantUserID int64, unban bool, date int) (domain.CommunityParticipantBanResult, error) {
	if s == nil || s.communities == nil || userID == 0 || communityID == 0 || participantUserID == 0 {
		return domain.CommunityParticipantBanResult{}, domain.ErrCommunityInvalid
	}
	return s.communities.ToggleCommunityParticipantBanned(ctx, userID, communityID, participantUserID, unban, date)
}

func (s *Service) ParticipantJoinedChats(ctx context.Context, userID, communityID, participantUserID int64) (domain.CommunityParticipantJoinedChats, error) {
	if s == nil || s.communities == nil || userID == 0 || communityID == 0 || participantUserID == 0 {
		return domain.CommunityParticipantJoinedChats{}, domain.ErrCommunityInvalid
	}
	return s.communities.GetCommunityParticipantJoinedChats(ctx, userID, communityID, participantUserID)
}

func (s *Service) Participants(ctx context.Context, userID, communityID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.CommunityParticipantList, error) {
	if s == nil || s.communities == nil || userID == 0 || communityID == 0 {
		return domain.CommunityParticipantList{}, domain.ErrCommunityInvalid
	}
	if offset < 0 {
		offset = 0
	}
	if offset > domain.MaxChannelParticipantsOffset {
		offset = domain.MaxChannelParticipantsOffset
	}
	if limit <= 0 || limit > domain.MaxCommunityParticipants {
		limit = domain.MaxCommunityParticipants
	}
	return s.communities.ListCommunityParticipants(ctx, userID, communityID, filter, offset, limit)
}

func (s *Service) EditTitle(ctx context.Context, userID, communityID int64, title string) (domain.CommunityView, bool, error) {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > domain.MaxCommunityTitleRunes {
		return domain.CommunityView{}, false, domain.ErrChannelTitleInvalid
	}
	return s.communities.EditCommunityTitle(ctx, userID, communityID, title)
}

func (s *Service) EditAbout(ctx context.Context, userID, communityID int64, about string) (domain.CommunityView, bool, error) {
	about = strings.TrimSpace(about)
	if utf8.RuneCountInString(about) > domain.MaxCommunityAboutRunes {
		return domain.CommunityView{}, false, domain.ErrAboutTooLong
	}
	return s.communities.EditCommunityAbout(ctx, userID, communityID, about)
}

func (s *Service) EditAdmin(ctx context.Context, userID int64, req domain.CommunityEditAdminRequest) (domain.CommunityView, bool, error) {
	if req.CommunityID == 0 || req.UserID == 0 || userID == 0 {
		return domain.CommunityView{}, false, domain.ErrCommunityInvalid
	}
	req.ActorUserID = userID
	return s.communities.EditCommunityAdmin(ctx, req)
}

func (s *Service) EditDefaultBannedRights(ctx context.Context, userID, communityID int64, rights domain.ChannelBannedRights) (domain.CommunityView, bool, error) {
	return s.communities.EditCommunityDefaultBannedRights(ctx, userID, communityID, rights)
}

func (s *Service) SetPhoto(ctx context.Context, userID, communityID int64, photo *domain.Photo, date int) (domain.CommunityView, bool, error) {
	return s.communities.SetCommunityPhoto(ctx, userID, communityID, photo, date)
}

func (s *Service) Delete(ctx context.Context, userID, communityID int64, date int) (domain.CommunityView, []domain.Peer, error) {
	return s.communities.DeleteCommunity(ctx, userID, communityID, date)
}

func (s *Service) SetPinned(ctx context.Context, userID, communityID int64, pinned bool) (bool, error) {
	return s.communities.SetCommunityPinned(ctx, userID, communityID, pinned)
}

func (s *Service) ReorderPinned(ctx context.Context, userID int64, order []domain.Peer, force bool) (bool, error) {
	return s.communities.ReorderCommunityPinned(ctx, userID, order, force)
}

func (s *Service) SearchScope(ctx context.Context, userID, communityID int64) (domain.CommunitySearchScope, error) {
	return s.communities.CommunitySearchScope(ctx, userID, communityID)
}
