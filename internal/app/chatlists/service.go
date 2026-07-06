package chatlists

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"unicode/utf8"

	"telesrv/internal/domain"
	"telesrv/internal/links"
	"telesrv/internal/store"
)

type Service struct {
	chatlists store.ChatlistStore
	dialogs   store.DialogStore
	channels  ChannelService
	premium   PremiumChecker
	newSlug   func() (string, error)
}

type Option func(*Service)

// ChannelService is the domain-only channel dependency used for shared-folder
// peer membership side effects.
type ChannelService interface {
	GetChannel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
	InviteToChannel(ctx context.Context, userID, channelID int64, userIDs []int64, date int) (domain.CreateChannelResult, error)
	JoinChannel(ctx context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error)
	LeaveChannel(ctx context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error)
}

type PremiumChecker func(ctx context.Context, userID int64) bool

func WithChannels(channels ChannelService) Option {
	return func(s *Service) {
		s.channels = channels
	}
}

func WithPremiumChecker(fn PremiumChecker) Option {
	return func(s *Service) {
		s.premium = fn
	}
}

func WithSlugGenerator(fn func() (string, error)) Option {
	return func(s *Service) {
		if fn != nil {
			s.newSlug = fn
		}
	}
}

func NewService(chatlists store.ChatlistStore, dialogs store.DialogStore, opts ...Option) *Service {
	s := &Service{
		chatlists: chatlists,
		dialogs:   dialogs,
		newSlug:   randomChatlistSlug,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) ExportInvite(ctx context.Context, userID int64, filterID int, title string, peers []domain.DialogFolderPeer, date int) (domain.DialogFolder, domain.ChatlistInvite, error) {
	if err := validateChatlistUserFilter(userID, filterID); err != nil {
		return domain.DialogFolder{}, domain.ChatlistInvite{}, err
	}
	if utf8.RuneCountInString(title) > domain.MaxDialogFolderTitleRunes {
		return domain.DialogFolder{}, domain.ChatlistInvite{}, domain.ErrChatlistInvalid
	}
	folder, err := s.ownerFolder(ctx, userID, filterID)
	if err != nil {
		return domain.DialogFolder{}, domain.ChatlistInvite{}, err
	}
	if !folderShareable(folder) {
		return domain.DialogFolder{}, domain.ChatlistInvite{}, domain.ErrChatlistNotShareable
	}
	selected, err := selectChatlistPeers(folder, peers, true)
	if err != nil {
		return domain.DialogFolder{}, domain.ChatlistInvite{}, err
	}
	if err := s.validateShareableChannelPeers(ctx, userID, selected); err != nil {
		return domain.DialogFolder{}, domain.ChatlistInvite{}, err
	}
	count, err := s.chatlists.CountActiveInvites(ctx, userID, filterID)
	if err != nil {
		return domain.DialogFolder{}, domain.ChatlistInvite{}, err
	}
	if count >= s.invitesLimit(ctx, userID) {
		return domain.DialogFolder{}, domain.ChatlistInvite{}, domain.ErrChatlistInvitesTooMuch
	}
	folder = exportedChatlistFolder(folder, true)
	var lastErr error
	for i := 0; i < 8; i++ {
		slug, err := s.newSlug()
		if err != nil {
			return domain.DialogFolder{}, domain.ChatlistInvite{}, err
		}
		invite := domain.ChatlistInvite{
			OwnerUserID: userID,
			FilterID:    filterID,
			Slug:        slug,
			Title:       title,
			Peers:       selected,
			Date:        date,
		}
		saved, err := s.chatlists.SaveInvite(ctx, invite)
		if err == nil {
			if err := s.dialogs.UpsertFolder(ctx, userID, folder); err != nil {
				rollbackErr := s.deleteInviteIfSaved(ctx, userID, filterID, saved.Slug)
				return domain.DialogFolder{}, domain.ChatlistInvite{}, errors.Join(fmt.Errorf("save exported chatlist folder: %w", err), rollbackErr)
			}
			return folder, saved, nil
		}
		if !errors.Is(err, domain.ErrChatlistSlugOccupied) {
			return domain.DialogFolder{}, domain.ChatlistInvite{}, err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = domain.ErrChatlistSlugOccupied
	}
	return domain.DialogFolder{}, domain.ChatlistInvite{}, lastErr
}

func (s *Service) ListInvites(ctx context.Context, userID int64, filterID int) ([]domain.ChatlistInvite, error) {
	if err := validateChatlistUserFilter(userID, filterID); err != nil {
		return nil, err
	}
	if _, err := s.ownerFolder(ctx, userID, filterID); err != nil {
		return nil, err
	}
	return s.chatlists.ListInvites(ctx, userID, filterID)
}

func (s *Service) EditInvite(ctx context.Context, userID int64, filterID int, slug string, title *string, peers *[]domain.DialogFolderPeer, revoke bool) (domain.ChatlistInvite, error) {
	if err := validateChatlistUserFilter(userID, filterID); err != nil {
		return domain.ChatlistInvite{}, err
	}
	slug = CleanSlug(slug)
	if !links.ValidChatlistSlug(slug) {
		return domain.ChatlistInvite{}, domain.ErrChatlistInviteInvalid
	}
	folder, err := s.ownerFolder(ctx, userID, filterID)
	if err != nil {
		return domain.ChatlistInvite{}, err
	}
	existing, found, err := s.chatlists.GetInvite(ctx, userID, filterID, slug)
	if err != nil {
		return domain.ChatlistInvite{}, err
	}
	if !found {
		return domain.ChatlistInvite{}, domain.ErrChatlistInviteExpired
	}
	if title != nil {
		if utf8.RuneCountInString(*title) > domain.MaxDialogFolderTitleRunes {
			return domain.ChatlistInvite{}, domain.ErrChatlistInvalid
		}
		existing.Title = *title
	}
	if peers != nil {
		selected, err := selectChatlistPeers(folder, *peers, true)
		if err != nil {
			return domain.ChatlistInvite{}, err
		}
		if err := s.validateShareableChannelPeers(ctx, userID, selected); err != nil {
			return domain.ChatlistInvite{}, err
		}
		existing.Peers = selected
	}
	if revoke {
		existing.Revoked = true
	}
	return s.chatlists.SaveInvite(ctx, existing)
}

func (s *Service) DeleteInvite(ctx context.Context, userID int64, filterID int, slug string) (domain.DialogFolder, bool, error) {
	if err := validateChatlistUserFilter(userID, filterID); err != nil {
		return domain.DialogFolder{}, false, err
	}
	slug = CleanSlug(slug)
	if !links.ValidChatlistSlug(slug) {
		return domain.DialogFolder{}, false, domain.ErrChatlistInviteInvalid
	}
	folder, err := s.ownerFolder(ctx, userID, filterID)
	if err != nil {
		return domain.DialogFolder{}, false, err
	}
	deleted, err := s.chatlists.DeleteInvite(ctx, userID, filterID, slug)
	if err != nil || !deleted {
		return domain.DialogFolder{}, false, err
	}
	count, err := s.chatlists.CountInvites(ctx, userID, filterID)
	if err != nil {
		return domain.DialogFolder{}, false, err
	}
	if count > 0 || !folder.HasMyInvites {
		return domain.DialogFolder{}, false, nil
	}
	folder = exportedChatlistFolder(folder, false)
	if err := s.dialogs.UpsertFolder(ctx, userID, folder); err != nil {
		return domain.DialogFolder{}, false, fmt.Errorf("clear exported chatlist folder flag: %w", err)
	}
	return folder, true, nil
}

func (s *Service) CheckInvite(ctx context.Context, userID int64, slug string) (domain.ChatlistInvitePreview, error) {
	if userID == 0 {
		return domain.ChatlistInvitePreview{}, domain.ErrChatlistInvalid
	}
	slug = CleanSlug(slug)
	if !links.ValidChatlistSlug(slug) {
		return domain.ChatlistInvitePreview{}, domain.ErrChatlistInviteInvalid
	}
	invite, folder, err := s.inviteWithFolder(ctx, slug)
	if err != nil {
		return domain.ChatlistInvitePreview{}, err
	}
	out := domain.ChatlistInvitePreview{Invite: invite, OwnerFolder: folder}
	if userID == invite.OwnerUserID {
		ownerFolder := exportedChatlistFolder(folder, true)
		out.LocalFolder = &ownerFolder
		out.Already = peersIntersection(invite.Peers, folderPeers(ownerFolder))
		return out, nil
	}
	membership, found, err := s.chatlists.GetMembershipBySlug(ctx, userID, slug)
	if err != nil {
		return domain.ChatlistInvitePreview{}, err
	}
	if !found {
		out.Missing = cloneFolderPeers(invite.Peers)
		return out, nil
	}
	local, found, err := s.dialogs.GetFolder(ctx, userID, membership.LocalFilterID)
	if err != nil {
		return domain.ChatlistInvitePreview{}, err
	}
	if !found {
		return domain.ChatlistInvitePreview{}, domain.ErrChatlistInvalid
	}
	out.Membership = &membership
	out.LocalFolder = &local
	out.Already = peersIntersection(invite.Peers, folderPeers(local))
	out.Missing = peersDifference(invite.Peers, folderPeers(local))
	return out, nil
}

func (s *Service) JoinInvite(ctx context.Context, userID int64, slug string, peers []domain.DialogFolderPeer, date int) (domain.ChatlistJoinResult, error) {
	if userID == 0 {
		return domain.ChatlistJoinResult{}, domain.ErrChatlistInvalid
	}
	slug = CleanSlug(slug)
	if !links.ValidChatlistSlug(slug) {
		return domain.ChatlistJoinResult{}, domain.ErrChatlistInviteInvalid
	}
	invite, ownerFolder, err := s.inviteWithFolder(ctx, slug)
	if err != nil {
		return domain.ChatlistJoinResult{}, err
	}
	if userID == invite.OwnerUserID {
		return domain.ChatlistJoinResult{Folder: exportedChatlistFolder(ownerFolder, true), Date: date}, nil
	}
	if existing, found, err := s.chatlists.GetMembershipBySlug(ctx, userID, slug); err != nil {
		return domain.ChatlistJoinResult{}, err
	} else if found {
		folder, found, err := s.dialogs.GetFolder(ctx, userID, existing.LocalFilterID)
		if err != nil {
			return domain.ChatlistJoinResult{}, err
		}
		if !found {
			return domain.ChatlistJoinResult{}, domain.ErrChatlistInvalid
		}
		return domain.ChatlistJoinResult{Folder: folder, Membership: existing, Date: date}, nil
	}
	selected, err := selectInvitePeers(invite, peers, true)
	if err != nil {
		return domain.ChatlistJoinResult{}, err
	}
	count, err := s.chatlists.CountMemberships(ctx, userID)
	if err != nil {
		return domain.ChatlistJoinResult{}, err
	}
	if count >= s.joinedLimit(ctx, userID) {
		return domain.ChatlistJoinResult{}, domain.ErrChatlistsTooMuch
	}
	channelResults, err := s.joinChannelPeers(ctx, invite.OwnerUserID, userID, selected, date)
	if err != nil {
		return domain.ChatlistJoinResult{}, err
	}
	filterID, err := s.nextLocalFilterID(ctx, userID)
	if err != nil {
		return domain.ChatlistJoinResult{}, err
	}
	folder := importedChatlistFolder(ownerFolder, filterID, selected)
	membership := domain.ChatlistMembership{
		UserID:        userID,
		LocalFilterID: filterID,
		OwnerUserID:   invite.OwnerUserID,
		OwnerFilterID: invite.FilterID,
		Slug:          invite.Slug,
		Date:          date,
	}
	if err := s.dialogs.UpsertFolder(ctx, userID, folder); err != nil {
		if rollbackErr := s.leaveJoinedChannelResults(ctx, userID, channelResults, date); rollbackErr != nil {
			return domain.ChatlistJoinResult{}, errors.Join(fmt.Errorf("save joined chatlist folder: %w", err), rollbackErr)
		}
		return domain.ChatlistJoinResult{}, fmt.Errorf("save joined chatlist folder: %w", err)
	}
	if err := s.chatlists.SaveMembership(ctx, membership); err != nil {
		rollbackErr := errors.Join(
			s.dialogs.DeleteFolder(ctx, userID, filterID),
			s.leaveJoinedChannelResults(ctx, userID, channelResults, date),
		)
		return domain.ChatlistJoinResult{}, errors.Join(err, rollbackErr)
	}
	return domain.ChatlistJoinResult{Folder: folder, Membership: membership, Date: date, ChannelResults: channelResults}, nil
}

func (s *Service) GetUpdates(ctx context.Context, userID int64, localFilterID int) (domain.ChatlistUpdates, error) {
	membership, folder, invite, err := s.memberFolderInvite(ctx, userID, localFilterID)
	if err != nil {
		if errors.Is(err, domain.ErrChatlistInviteExpired) {
			return domain.ChatlistUpdates{Membership: membership}, nil
		}
		if errors.Is(err, domain.ErrChatlistInvalid) {
			if owner, ownerErr := s.ownerFolder(ctx, userID, localFilterID); ownerErr == nil && owner.IsChatlist && owner.HasMyInvites {
				return domain.ChatlistUpdates{}, nil
			}
		}
		return domain.ChatlistUpdates{}, err
	}
	if membership.HiddenUpdates {
		return domain.ChatlistUpdates{Membership: membership}, nil
	}
	return domain.ChatlistUpdates{
		Membership: membership,
		Missing:    peersDifference(invite.Peers, folderPeers(folder)),
	}, nil
}

func (s *Service) JoinUpdates(ctx context.Context, userID int64, localFilterID int, peers []domain.DialogFolderPeer, date int) (domain.ChatlistJoinResult, error) {
	membership, folder, invite, err := s.memberFolderInvite(ctx, userID, localFilterID)
	if err != nil {
		return domain.ChatlistJoinResult{}, err
	}
	selected, err := selectInvitePeers(invite, peers, true)
	if err != nil {
		return domain.ChatlistJoinResult{}, err
	}
	if _, err := s.chatlists.SetMembershipHidden(ctx, userID, membership.LocalFilterID, false); err != nil {
		return domain.ChatlistJoinResult{}, err
	}
	channelResults, err := s.joinChannelPeers(ctx, membership.OwnerUserID, userID, selected, date)
	if err != nil {
		return domain.ChatlistJoinResult{}, err
	}
	folder.IncludePeers = mergeFolderPeers(folder.IncludePeers, selected)
	folder.IsChatlist = true
	if err := s.dialogs.UpsertFolder(ctx, userID, folder); err != nil {
		if rollbackErr := s.leaveJoinedChannelResults(ctx, userID, channelResults, date); rollbackErr != nil {
			return domain.ChatlistJoinResult{}, errors.Join(fmt.Errorf("save chatlist updates: %w", err), rollbackErr)
		}
		return domain.ChatlistJoinResult{}, fmt.Errorf("save chatlist updates: %w", err)
	}
	return domain.ChatlistJoinResult{Folder: folder, Membership: membership, ChannelResults: channelResults}, nil
}

func (s *Service) HideUpdates(ctx context.Context, userID int64, localFilterID int) error {
	if err := validateChatlistUserFilter(userID, localFilterID); err != nil {
		return err
	}
	if _, found, err := s.chatlists.GetMembershipByLocalFilter(ctx, userID, localFilterID); err != nil {
		return err
	} else if !found {
		return domain.ErrChatlistInvalid
	}
	_, err := s.chatlists.SetMembershipHidden(ctx, userID, localFilterID, true)
	return err
}

func (s *Service) Leave(ctx context.Context, userID int64, localFilterID int, peers []domain.DialogFolderPeer, date int) (domain.ChatlistLeaveResult, error) {
	if err := validateChatlistUserFilter(userID, localFilterID); err != nil {
		return domain.ChatlistLeaveResult{}, err
	}
	if _, found, err := s.chatlists.GetMembershipByLocalFilter(ctx, userID, localFilterID); err != nil {
		return domain.ChatlistLeaveResult{}, err
	} else if !found {
		return domain.ChatlistLeaveResult{}, domain.ErrChatlistInvalid
	}
	folder, found, err := s.dialogs.GetFolder(ctx, userID, localFilterID)
	if err != nil {
		return domain.ChatlistLeaveResult{}, err
	}
	if !found {
		return domain.ChatlistLeaveResult{}, domain.ErrChatlistInvalid
	}
	selected, err := selectPeersFromAllowed(folderPeerMap(channelFolderPeers(folderPeers(folder))), peers, false)
	if err != nil {
		return domain.ChatlistLeaveResult{}, err
	}
	channelResults, err := s.leaveChannelPeers(ctx, userID, selected, date)
	if err != nil {
		return domain.ChatlistLeaveResult{}, err
	}
	if err := s.dialogs.DeleteFolder(ctx, userID, localFilterID); err != nil {
		return domain.ChatlistLeaveResult{}, err
	}
	if _, err := s.chatlists.DeleteMembershipByLocalFilter(ctx, userID, localFilterID); err != nil {
		restoreErr := s.dialogs.UpsertFolder(ctx, userID, folder)
		return domain.ChatlistLeaveResult{}, errors.Join(err, restoreErr)
	}
	return domain.ChatlistLeaveResult{FilterID: localFilterID, ChannelResults: channelResults, RequestedLeaves: selected}, nil
}

func (s *Service) LeaveSuggestions(ctx context.Context, userID int64, localFilterID int) ([]domain.DialogFolderPeer, error) {
	if err := validateChatlistUserFilter(userID, localFilterID); err != nil {
		return nil, err
	}
	if _, found, err := s.chatlists.GetMembershipByLocalFilter(ctx, userID, localFilterID); err != nil {
		return nil, err
	} else if !found {
		return nil, domain.ErrChatlistInvalid
	}
	folder, found, err := s.dialogs.GetFolder(ctx, userID, localFilterID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, domain.ErrChatlistInvalid
	}
	return channelFolderPeers(folderPeers(folder)), nil
}

func (s *Service) ownerFolder(ctx context.Context, userID int64, filterID int) (domain.DialogFolder, error) {
	if s == nil || s.chatlists == nil || s.dialogs == nil {
		return domain.DialogFolder{}, domain.ErrChatlistInvalid
	}
	folder, found, err := s.dialogs.GetFolder(ctx, userID, filterID)
	if err != nil {
		return domain.DialogFolder{}, err
	}
	if !found {
		return domain.DialogFolder{}, domain.ErrChatlistInvalid
	}
	folder.ID = filterID
	return folder, nil
}

func (s *Service) inviteWithFolder(ctx context.Context, slug string) (domain.ChatlistInvite, domain.DialogFolder, error) {
	invite, found, err := s.chatlists.GetInviteBySlug(ctx, slug)
	if err != nil {
		return domain.ChatlistInvite{}, domain.DialogFolder{}, err
	}
	if !found {
		return domain.ChatlistInvite{}, domain.DialogFolder{}, domain.ErrChatlistInviteExpired
	}
	folder, found, err := s.dialogs.GetFolder(ctx, invite.OwnerUserID, invite.FilterID)
	if err != nil {
		return domain.ChatlistInvite{}, domain.DialogFolder{}, err
	}
	if !found {
		return domain.ChatlistInvite{}, domain.DialogFolder{}, domain.ErrChatlistInviteExpired
	}
	return invite, exportedChatlistFolder(folder, true), nil
}

func (s *Service) memberFolderInvite(ctx context.Context, userID int64, localFilterID int) (domain.ChatlistMembership, domain.DialogFolder, domain.ChatlistInvite, error) {
	if err := validateChatlistUserFilter(userID, localFilterID); err != nil {
		return domain.ChatlistMembership{}, domain.DialogFolder{}, domain.ChatlistInvite{}, err
	}
	membership, found, err := s.chatlists.GetMembershipByLocalFilter(ctx, userID, localFilterID)
	if err != nil {
		return domain.ChatlistMembership{}, domain.DialogFolder{}, domain.ChatlistInvite{}, err
	}
	if !found {
		return domain.ChatlistMembership{}, domain.DialogFolder{}, domain.ChatlistInvite{}, domain.ErrChatlistInvalid
	}
	folder, found, err := s.dialogs.GetFolder(ctx, userID, localFilterID)
	if err != nil {
		return membership, domain.DialogFolder{}, domain.ChatlistInvite{}, err
	}
	if !found {
		return membership, domain.DialogFolder{}, domain.ChatlistInvite{}, domain.ErrChatlistInvalid
	}
	invite, found, err := s.chatlists.GetInviteBySlug(ctx, membership.Slug)
	if err != nil {
		return membership, domain.DialogFolder{}, domain.ChatlistInvite{}, err
	}
	if !found {
		return membership, folder, domain.ChatlistInvite{}, domain.ErrChatlistInviteExpired
	}
	return membership, folder, invite, nil
}

func (s *Service) nextLocalFilterID(ctx context.Context, userID int64) (int, error) {
	list, err := s.dialogs.ListFolders(ctx, userID)
	if err != nil {
		return 0, err
	}
	used := make(map[int]struct{}, len(list.Folders))
	for _, folder := range list.Folders {
		used[folder.ID] = struct{}{}
	}
	for id := domain.DialogCustomFolderMinID; id < domain.DialogCustomFolderMinID+domain.MaxDialogFolders; id++ {
		if _, ok := used[id]; !ok {
			return id, nil
		}
	}
	return 0, domain.ErrChatlistsTooMuch
}

func validateChatlistUserFilter(userID int64, filterID int) error {
	if userID == 0 || filterID < domain.DialogCustomFolderMinID {
		return domain.ErrChatlistInvalid
	}
	return nil
}

func folderShareable(folder domain.DialogFolder) bool {
	if len(folder.ExcludePeers) > 0 || folder.Contacts || folder.NonContacts || folder.Groups ||
		folder.Broadcasts || folder.Bots || folder.ExcludeMuted || folder.ExcludeRead || folder.ExcludeArchived {
		return false
	}
	return len(folder.IncludePeers)+len(folder.PinnedPeers) > 0
}

func (s *Service) validateShareableChannelPeers(ctx context.Context, userID int64, peers []domain.DialogFolderPeer) error {
	if len(peers) == 0 {
		return domain.ErrChatlistPeersEmpty
	}
	for _, item := range peers {
		if item.Peer.Type != domain.PeerTypeChannel || item.Peer.ID == 0 {
			return domain.ErrChatlistNotShareable
		}
		if s.channels == nil {
			continue
		}
		view, err := s.channels.GetChannel(ctx, userID, item.Peer.ID)
		if err != nil {
			return err
		}
		if !channelShareableForChatlist(view) {
			return domain.ErrChatlistNotShareable
		}
	}
	return nil
}

func channelShareableForChatlist(view domain.ChannelView) bool {
	if view.Channel.Deleted || view.Forbidden {
		return false
	}
	return channelCanInviteForChatlist(view) || channelPublicJoinableForChatlist(view)
}

func channelCanInviteForChatlist(view domain.ChannelView) bool {
	switch view.Self.Role {
	case domain.ChannelRoleCreator:
		return true
	case domain.ChannelRoleAdmin:
		if view.Self.AdminRights.InviteUsers {
			return true
		}
	}
	return false
}

func channelPublicJoinableForChatlist(view domain.ChannelView) bool {
	if view.Channel.Deleted || view.Forbidden {
		return false
	}
	return view.Channel.Username != "" && !view.Channel.JoinRequest
}

func (s *Service) joinChannelPeers(ctx context.Context, ownerUserID, userID int64, peers []domain.DialogFolderPeer, date int) ([]domain.CreateChannelResult, error) {
	if s.channels == nil {
		return nil, nil
	}
	type joinPlan struct {
		peer      domain.DialogFolderPeer
		useInvite bool
	}
	channelPeers := channelFolderPeers(peers)
	plans := make([]joinPlan, 0, len(channelPeers))
	for _, item := range channelPeers {
		view, err := s.channels.GetChannel(ctx, ownerUserID, item.Peer.ID)
		if err != nil {
			return nil, err
		}
		if !channelShareableForChatlist(view) {
			return nil, domain.ErrChatlistNotShareable
		}
		plans = append(plans, joinPlan{peer: item, useInvite: channelCanInviteForChatlist(view)})
	}
	results := make([]domain.CreateChannelResult, 0, len(plans))
	for _, plan := range plans {
		var res domain.CreateChannelResult
		var err error
		if plan.useInvite {
			res, err = s.channels.InviteToChannel(ctx, ownerUserID, plan.peer.Peer.ID, []int64{userID}, date)
		} else {
			res, err = s.channels.JoinChannel(ctx, userID, plan.peer.Peer.ID, date)
		}
		if err != nil {
			if errors.Is(err, domain.ErrUserAlreadyParticipant) {
				continue
			}
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

func (s *Service) leaveChannelPeers(ctx context.Context, userID int64, peers []domain.DialogFolderPeer, date int) ([]domain.CreateChannelResult, error) {
	if s.channels == nil {
		return nil, nil
	}
	results := make([]domain.CreateChannelResult, 0)
	for _, item := range channelFolderPeers(peers) {
		res, err := s.channels.LeaveChannel(ctx, userID, item.Peer.ID, date)
		if err != nil {
			if errors.Is(err, domain.ErrUserNotParticipant) || errors.Is(err, domain.ErrChannelPrivate) {
				continue
			}
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

func (s *Service) leaveJoinedChannelResults(ctx context.Context, userID int64, results []domain.CreateChannelResult, date int) error {
	if s.channels == nil || len(results) == 0 {
		return nil
	}
	var joinedErr error
	for _, res := range results {
		if res.Channel.ID == 0 {
			continue
		}
		if _, err := s.channels.LeaveChannel(ctx, userID, res.Channel.ID, date); err != nil &&
			!errors.Is(err, domain.ErrUserNotParticipant) &&
			!errors.Is(err, domain.ErrChannelPrivate) {
			joinedErr = errors.Join(joinedErr, err)
		}
	}
	return joinedErr
}

func (s *Service) deleteInviteIfSaved(ctx context.Context, ownerUserID int64, filterID int, slug string) error {
	if slug == "" {
		return nil
	}
	if _, err := s.chatlists.DeleteInvite(ctx, ownerUserID, filterID, slug); err != nil {
		return fmt.Errorf("rollback chatlist invite: %w", err)
	}
	return nil
}

func (s *Service) invitesLimit(ctx context.Context, userID int64) int {
	if s != nil && s.premium != nil && s.premium(ctx, userID) {
		return domain.MaxChatlistInvitesPremium
	}
	return domain.MaxChatlistInvitesDefault
}

func (s *Service) joinedLimit(ctx context.Context, userID int64) int {
	if s != nil && s.premium != nil && s.premium(ctx, userID) {
		return domain.MaxChatlistsJoinedPremium
	}
	return domain.MaxChatlistsJoinedDefault
}

func exportedChatlistFolder(folder domain.DialogFolder, hasMyInvites bool) domain.DialogFolder {
	folder.Contacts = false
	folder.NonContacts = false
	folder.Groups = false
	folder.Broadcasts = false
	folder.Bots = false
	folder.ExcludeMuted = false
	folder.ExcludeRead = false
	folder.ExcludeArchived = false
	folder.ExcludePeers = nil
	folder.IsChatlist = true
	folder.HasMyInvites = hasMyInvites
	return cloneDialogFolder(folder)
}

func importedChatlistFolder(source domain.DialogFolder, filterID int, peers []domain.DialogFolderPeer) domain.DialogFolder {
	source = exportedChatlistFolder(source, false)
	source.ID = filterID
	source.PinnedPeers = nil
	source.IncludePeers = cloneFolderPeers(peers)
	source.HasMyInvites = false
	return source
}

func selectChatlistPeers(folder domain.DialogFolder, requested []domain.DialogFolderPeer, requireNonEmpty bool) ([]domain.DialogFolderPeer, error) {
	allowed := folderPeerMap(folderPeers(folder))
	return selectPeersFromAllowed(allowed, requested, requireNonEmpty)
}

func selectInvitePeers(invite domain.ChatlistInvite, requested []domain.DialogFolderPeer, requireNonEmpty bool) ([]domain.DialogFolderPeer, error) {
	if len(requested) == 0 {
		requested = invite.Peers
	}
	return selectPeersFromAllowed(folderPeerMap(invite.Peers), requested, requireNonEmpty)
}

func selectPeersFromAllowed(allowed map[domain.Peer]domain.DialogFolderPeer, requested []domain.DialogFolderPeer, requireNonEmpty bool) ([]domain.DialogFolderPeer, error) {
	if len(requested) > domain.MaxChatlistInvitePeers {
		return nil, domain.ErrChatlistPeersTooMuch
	}
	out := make([]domain.DialogFolderPeer, 0, len(requested))
	seen := make(map[domain.Peer]struct{}, len(requested))
	for _, item := range requested {
		if item.Peer.Type == "" || item.Peer.ID == 0 {
			return nil, domain.ErrChatlistInvalid
		}
		allowedPeer, ok := allowed[item.Peer]
		if !ok {
			return nil, domain.ErrChatlistInvalid
		}
		if _, ok := seen[item.Peer]; ok {
			continue
		}
		seen[item.Peer] = struct{}{}
		if item.AccessHash == 0 {
			item.AccessHash = allowedPeer.AccessHash
		}
		out = append(out, item)
	}
	if requireNonEmpty && len(out) == 0 {
		return nil, domain.ErrChatlistPeersEmpty
	}
	return out, nil
}

func channelFolderPeers(peers []domain.DialogFolderPeer) []domain.DialogFolderPeer {
	out := make([]domain.DialogFolderPeer, 0, len(peers))
	for _, item := range peers {
		if item.Peer.Type == domain.PeerTypeChannel && item.Peer.ID != 0 {
			out = append(out, item)
		}
	}
	return out
}

func folderPeers(folder domain.DialogFolder) []domain.DialogFolderPeer {
	return mergeFolderPeers(folder.PinnedPeers, folder.IncludePeers)
}

func folderPeerMap(peers []domain.DialogFolderPeer) map[domain.Peer]domain.DialogFolderPeer {
	out := make(map[domain.Peer]domain.DialogFolderPeer, len(peers))
	for _, item := range peers {
		if item.Peer.Type == "" || item.Peer.ID == 0 {
			continue
		}
		if _, ok := out[item.Peer]; !ok {
			out[item.Peer] = item
		}
	}
	return out
}

func mergeFolderPeers(a, b []domain.DialogFolderPeer) []domain.DialogFolderPeer {
	out := make([]domain.DialogFolderPeer, 0, len(a)+len(b))
	seen := make(map[domain.Peer]struct{}, len(a)+len(b))
	for _, list := range [][]domain.DialogFolderPeer{a, b} {
		for _, item := range list {
			if item.Peer.Type == "" || item.Peer.ID == 0 {
				continue
			}
			if _, ok := seen[item.Peer]; ok {
				continue
			}
			seen[item.Peer] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

func peersDifference(all, existing []domain.DialogFolderPeer) []domain.DialogFolderPeer {
	existingMap := folderPeerMap(existing)
	out := make([]domain.DialogFolderPeer, 0)
	for _, item := range all {
		if _, ok := existingMap[item.Peer]; !ok {
			out = append(out, item)
		}
	}
	return out
}

func peersIntersection(all, existing []domain.DialogFolderPeer) []domain.DialogFolderPeer {
	existingMap := folderPeerMap(existing)
	out := make([]domain.DialogFolderPeer, 0)
	for _, item := range all {
		if _, ok := existingMap[item.Peer]; ok {
			out = append(out, item)
		}
	}
	return out
}

func cloneDialogFolder(folder domain.DialogFolder) domain.DialogFolder {
	folder.TitleEntities = append([]domain.MessageEntity(nil), folder.TitleEntities...)
	folder.PinnedPeers = cloneFolderPeers(folder.PinnedPeers)
	folder.IncludePeers = cloneFolderPeers(folder.IncludePeers)
	folder.ExcludePeers = cloneFolderPeers(folder.ExcludePeers)
	return folder
}

func cloneFolderPeers(peers []domain.DialogFolderPeer) []domain.DialogFolderPeer {
	return append([]domain.DialogFolderPeer(nil), peers...)
}

func CleanSlug(raw string) string {
	return links.CleanChatlistSlug(raw)
}

func randomChatlistSlug() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("chatlist slug rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
