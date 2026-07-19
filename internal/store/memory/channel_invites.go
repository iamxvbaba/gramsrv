package memory

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"time"
)

func (s *ChannelStore) InviteToChannel(_ context.Context, channelID, inviterUserID int64, userIDs []int64, date int) (domain.CreateChannelResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(inviterUserID, channelID)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	if channel.Monoforum {
		return domain.CreateChannelResult{}, domain.ErrChannelMonoforumUnsupported
	}
	inviter := s.members[channelID][inviterUserID]
	if !canInviteToChannel(channel, inviter) {
		return domain.CreateChannelResult{}, domain.ErrChannelAdminRequired
	}
	requested := uniqueNonZero(userIDs, 0)
	inviteOne := len(requested) == 1
	canRestoreKicked := canBanChannelUsers(inviter)
	added := make([]int64, 0, len(requested))
	members := make([]domain.ChannelMember, 0, len(requested))
	restoredKicked := 0
	for _, userID := range requested {
		if existing, ok := s.members[channelID][userID]; ok {
			if existing.Status == domain.ChannelMemberActive {
				if inviteOne {
					return domain.CreateChannelResult{}, domain.ErrUserAlreadyParticipant
				}
				continue
			}
			if existing.Status == domain.ChannelMemberBanned || existing.Status == domain.ChannelMemberKicked || existing.BannedRights.ViewMessages {
				if !canRestoreKicked {
					if inviteOne {
						return domain.CreateChannelResult{}, domain.ErrUserKicked
					}
					continue
				}
				if existing.Status == domain.ChannelMemberKicked {
					restoredKicked++
				}
			}
		}
		member := domain.ChannelMember{
			ChannelID:       channelID,
			UserID:          userID,
			InviterUserID:   inviterUserID,
			Role:            domain.ChannelRoleMember,
			Status:          domain.ChannelMemberActive,
			JoinedAt:        date,
			AvailableMinID:  channelInitialAvailableMinID(channel),
			AvailableMinPts: channelInitialAvailableMinPts(channel),
			ReadInboxMaxID:  channel.TopMessageID,
		}
		s.members[channelID][userID] = member
		members = append(members, member)
		added = append(added, userID)
		channel.ParticipantsCount++
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID:   channelID,
			UserID:      inviterUserID,
			Date:        date,
			Type:        domain.ChannelAdminLogParticipantInvite,
			Participant: ptrChannelMember(member),
		})
	}
	if restoredKicked > 0 {
		channel.KickedCount = maxInt(channel.KickedCount-restoredKicked, 0)
	}
	s.channels[channelID] = channel
	var msg domain.ChannelMessage
	var event domain.ChannelUpdateEvent
	if len(added) > 0 && channel.Megagroup {
		msg, event = s.appendChannelServiceMessageLocked(channelID, inviterUserID, date, domain.ChannelMessageAction{
			Type:    domain.ChannelActionChatAddUser,
			UserIDs: append([]int64(nil), added...),
		})
		channel.TopMessageID = msg.ID
		channel.Pts = event.Pts
		s.channels[channelID] = channel
	}
	for _, member := range members {
		s.upsertChannelDialogLocked(member.UserID, channel, msg, false)
	}
	return domain.CreateChannelResult{
		Channel:    channel,
		Members:    cloneChannelMembers(members),
		Message:    cloneChannelMessage(msg),
		Event:      cloneChannelEvent(event),
		Recipients: s.activeMemberIDsLocked(channelID, 0, 0),
	}, nil
}

func (s *ChannelStore) ListAdminLog(_ context.Context, req domain.ChannelAdminLogRequest) (domain.ChannelAdminLogResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MaxID < 0 || req.MinID < 0 {
		return domain.ChannelAdminLogResult{}, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelAdminLogResult{}, err
	}
	if !isChannelAdmin(s.members[req.ChannelID][req.UserID]) {
		return domain.ChannelAdminLogResult{}, domain.ErrChannelAdminRequired
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelAdminLogLimit {
		limit = domain.MaxChannelAdminLogLimit
	}
	admins := int64Set(req.AdminUserIDs)
	query := strings.ToLower(strings.TrimSpace(req.Query))
	out := make([]domain.ChannelAdminLogEvent, 0, limit)
	events := s.adminLogs[req.ChannelID]
	for i := len(events) - 1; i >= 0 && len(out) < limit; i-- {
		event := events[i]
		if req.MaxID > 0 && event.ID >= req.MaxID {
			continue
		}
		if req.MinID > 0 && event.ID <= req.MinID {
			continue
		}
		if len(admins) > 0 {
			if _, ok := admins[event.UserID]; !ok {
				continue
			}
		}
		if !adminLogEventMatchesFilter(event.Type, req.Filter) {
			continue
		}
		if query != "" && !adminLogEventMatchesQuery(event, query) {
			continue
		}
		out = append(out, cloneChannelAdminLogEvent(event))
	}
	return domain.ChannelAdminLogResult{Channel: channel, Events: out}, nil
}

func (s *ChannelStore) ExportInvite(_ context.Context, req domain.ExportChannelInviteRequest) (domain.ExportChannelInviteResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ExportChannelInviteResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ExportChannelInviteResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canExportChannelInvite(member) {
		return domain.ExportChannelInviteResult{}, domain.ErrChannelAdminRequired
	}
	if req.LegacyRevokePermanent {
		for hash, invite := range s.invites {
			if invite.ChannelID == req.ChannelID && invite.AdminUserID == req.UserID && invite.Permanent {
				invite.Revoked = true
				s.invites[hash] = invite
			}
		}
	}
	inviteID, err := randomMemoryPositiveInt64()
	if err != nil {
		return domain.ExportChannelInviteResult{}, err
	}
	hash, err := randomMemoryInviteHash()
	if err != nil {
		return domain.ExportChannelInviteResult{}, err
	}
	invite := domain.ChannelInvite{
		ChannelID:     req.ChannelID,
		InviteID:      inviteID,
		Hash:          hash,
		AdminUserID:   req.UserID,
		Title:         req.Title,
		Permanent:     req.ExpireDate == 0 && req.UsageLimit == 0 && !req.RequestNeeded && req.Title == "",
		RequestNeeded: req.RequestNeeded,
		ExpireDate:    req.ExpireDate,
		UsageLimit:    req.UsageLimit,
		Date:          req.Date,
	}
	s.invites[hash] = invite
	channel = s.refreshChannelHasLinkLocked(req.ChannelID)
	return domain.ExportChannelInviteResult{Channel: channel, Invite: invite}, nil
}

// EnsurePermanentInvite 幂等返回 (channel, admin) 当前未撤销的永久邀请；缺失则创建。
// 与 postgres 实现语义对齐（官方语义：邀请权限管理员必有主链接）。
func (s *ChannelStore) EnsurePermanentInvite(_ context.Context, channelID, adminUserID int64, date int) (domain.ChannelInvite, error) {
	if channelID == 0 || adminUserID == 0 {
		return domain.ChannelInvite{}, domain.ErrChannelInvalid
	}
	if date == 0 {
		date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.channelForMemberLocked(adminUserID, channelID); err != nil {
		return domain.ChannelInvite{}, err
	}
	member := s.members[channelID][adminUserID]
	if !canExportChannelInvite(member) {
		return domain.ChannelInvite{}, domain.ErrChannelAdminRequired
	}
	var existing *domain.ChannelInvite
	for hash := range s.invites {
		invite := s.invites[hash]
		if invite.ChannelID != channelID || invite.AdminUserID != adminUserID || !invite.Permanent || invite.Revoked {
			continue
		}
		// map 遍历无序：取最早创建的一条，对齐 postgres ORDER BY created_at ASC。
		if existing == nil || invite.Date < existing.Date || (invite.Date == existing.Date && invite.Hash < existing.Hash) {
			copied := invite
			existing = &copied
		}
	}
	if existing != nil {
		s.setChannelHasLinkLocked(channelID, true)
		return *existing, nil
	}
	inviteID, err := randomMemoryPositiveInt64()
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	hash, err := randomMemoryInviteHash()
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	invite := domain.ChannelInvite{
		ChannelID:   channelID,
		InviteID:    inviteID,
		Hash:        hash,
		AdminUserID: adminUserID,
		Permanent:   true,
		Date:        date,
	}
	s.invites[hash] = invite
	s.setChannelHasLinkLocked(channelID, true)
	return invite, nil
}

func (s *ChannelStore) CheckInvite(_ context.Context, userID int64, hash string, date int) (domain.CheckChannelInviteResult, error) {
	if userID == 0 || strings.TrimSpace(hash) == "" {
		return domain.CheckChannelInviteResult{}, domain.ErrInviteHashEmpty
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	invite, ok := s.invites[strings.TrimSpace(hash)]
	if !ok || invite.Revoked {
		return domain.CheckChannelInviteResult{}, domain.ErrInviteHashInvalid
	}
	if invite.ExpireDate > 0 && invite.ExpireDate < date {
		return domain.CheckChannelInviteResult{}, domain.ErrInviteHashExpired
	}
	channel, ok := s.channels[invite.ChannelID]
	if !ok || channel.Deleted {
		return domain.CheckChannelInviteResult{}, domain.ErrInviteHashInvalid
	}
	member := s.members[invite.ChannelID][userID]
	if member.Status == domain.ChannelMemberKicked || member.Status == domain.ChannelMemberBanned || member.BannedRights.ViewMessages {
		return domain.CheckChannelInviteResult{}, domain.ErrInviteHashInvalid
	}
	return domain.CheckChannelInviteResult{
		Channel: channel,
		Invite:  invite,
		Already: member.Status == domain.ChannelMemberActive,
		Self:    member,
	}, nil
}

func (s *ChannelStore) ImportInvite(_ context.Context, req domain.ImportChannelInviteRequest) (domain.CreateChannelResult, error) {
	if req.UserID == 0 || strings.TrimSpace(req.Hash) == "" {
		return domain.CreateChannelResult{}, domain.ErrInviteHashEmpty
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	invite, ok := s.invites[strings.TrimSpace(req.Hash)]
	if !ok || invite.Revoked {
		return domain.CreateChannelResult{}, domain.ErrInviteHashInvalid
	}
	if invite.ExpireDate > 0 && invite.ExpireDate < req.Date {
		return domain.CreateChannelResult{}, domain.ErrInviteHashExpired
	}
	channel, ok := s.channels[invite.ChannelID]
	if !ok || channel.Deleted {
		return domain.CreateChannelResult{}, domain.ErrInviteHashInvalid
	}
	if invite.RequestNeeded {
		if err := s.recordPendingInviteRequestLocked(invite, req.UserID, req.Date); err != nil {
			return domain.CreateChannelResult{}, err
		}
		return domain.CreateChannelResult{Channel: channel}, domain.ErrInviteRequestSent
	}
	return s.approveInviteImporterLocked(channel, invite, req.UserID, 0, req.Date)
}

func (s *ChannelStore) ListExportedInvites(_ context.Context, req domain.ChannelInviteListRequest) (domain.ChannelInviteList, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.AdminUserID == 0 {
		return domain.ChannelInviteList{}, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.channelForMemberLocked(req.UserID, req.ChannelID); err != nil {
		return domain.ChannelInviteList{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canExportChannelInvite(member) {
		return domain.ChannelInviteList{}, domain.ErrChannelAdminRequired
	}
	all := make([]domain.ChannelInvite, 0)
	for _, invite := range s.invites {
		if invite.ChannelID == req.ChannelID && invite.AdminUserID == req.AdminUserID && invite.Revoked == req.Revoked {
			all = append(all, invite)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Date != all[j].Date {
			return all[i].Date > all[j].Date
		}
		return all[i].Hash > all[j].Hash
	})
	total := len(all)
	start := 0
	if req.OffsetDate > 0 || req.OffsetHash != "" {
		start = len(all)
		for i, invite := range all {
			if invite.Date == req.OffsetDate && invite.Hash == req.OffsetHash {
				start = i + 1
				break
			}
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelInviteListLimit {
		limit = domain.MaxChannelInviteListLimit
	}
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	return domain.ChannelInviteList{Count: total, Invites: cloneChannelInvites(all[start:end])}, nil
}

func (s *ChannelStore) permanentInviteForAdminLocked(channelID, adminUserID int64) (domain.ChannelInvite, bool) {
	var (
		best domain.ChannelInvite
		ok   bool
	)
	for _, invite := range s.invites {
		if invite.ChannelID != channelID || invite.AdminUserID != adminUserID || !invite.Permanent || invite.Revoked {
			continue
		}
		if !ok || invite.Date < best.Date || (invite.Date == best.Date && invite.Hash < best.Hash) {
			best = invite
			ok = true
		}
	}
	return best, ok
}

func (s *ChannelStore) GetExportedInvite(_ context.Context, req domain.GetChannelInviteRequest) (domain.ChannelInvite, error) {
	if req.UserID == 0 || req.ChannelID == 0 || strings.TrimSpace(req.Hash) == "" {
		return domain.ChannelInvite{}, domain.ErrInviteHashEmpty
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.channelForMemberLocked(req.UserID, req.ChannelID); err != nil {
		return domain.ChannelInvite{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canExportChannelInvite(member) {
		return domain.ChannelInvite{}, domain.ErrChannelAdminRequired
	}
	return s.inviteByChannelHashLocked(req.ChannelID, req.Hash)
}

func (s *ChannelStore) EditExportedInvite(_ context.Context, req domain.EditChannelInviteRequest) (domain.EditChannelInviteResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || strings.TrimSpace(req.Hash) == "" {
		return domain.EditChannelInviteResult{}, domain.ErrInviteHashEmpty
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.channelForMemberLocked(req.UserID, req.ChannelID); err != nil {
		return domain.EditChannelInviteResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canExportChannelInvite(member) {
		return domain.EditChannelInviteResult{}, domain.ErrChannelAdminRequired
	}
	invite, err := s.inviteByChannelHashLocked(req.ChannelID, req.Hash)
	if err != nil {
		return domain.EditChannelInviteResult{}, err
	}
	if req.Revoked {
		if invite.Revoked {
			return domain.EditChannelInviteResult{}, domain.ErrInviteRevokedMissing
		}
		invite.Revoked = true
		s.invites[invite.Hash] = invite
		if !invite.Permanent {
			s.refreshChannelHasLinkLocked(req.ChannelID)
			return domain.EditChannelInviteResult{Invite: invite}, nil
		}
		newInvite, err := s.newReplacementInviteLocked(invite, req.Date)
		if err != nil {
			return domain.EditChannelInviteResult{}, err
		}
		s.invites[newInvite.Hash] = newInvite
		s.refreshChannelHasLinkLocked(req.ChannelID)
		return domain.EditChannelInviteResult{Invite: invite, NewInvite: &newInvite}, nil
	}
	if invite.Permanent && ((req.HasExpireDate && req.ExpireDate > 0) || (req.HasUsageLimit && req.UsageLimit > 0) || (req.HasRequestNeeded && req.RequestNeeded)) {
		return domain.EditChannelInviteResult{}, domain.ErrInvitePermanent
	}
	if req.HasExpireDate {
		invite.ExpireDate = req.ExpireDate
	}
	if req.HasUsageLimit {
		invite.UsageLimit = req.UsageLimit
	}
	if req.HasRequestNeeded {
		invite.RequestNeeded = req.RequestNeeded
	}
	if req.HasTitle {
		invite.Title = req.Title
	}
	invite.Permanent = invite.ExpireDate == 0 && invite.UsageLimit == 0 && !invite.RequestNeeded && invite.Title == ""
	s.invites[invite.Hash] = invite
	s.refreshChannelHasLinkLocked(req.ChannelID)
	return domain.EditChannelInviteResult{Invite: invite}, nil
}

func (s *ChannelStore) DeleteExportedInvite(_ context.Context, req domain.DeleteChannelInviteRequest) error {
	if req.UserID == 0 || req.ChannelID == 0 || strings.TrimSpace(req.Hash) == "" {
		return domain.ErrInviteHashEmpty
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.channelForMemberLocked(req.UserID, req.ChannelID); err != nil {
		return err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canExportChannelInvite(member) {
		return domain.ErrChannelAdminRequired
	}
	invite, err := s.inviteByChannelHashLocked(req.ChannelID, req.Hash)
	if err != nil {
		return err
	}
	delete(s.invites, invite.Hash)
	s.refreshChannelHasLinkLocked(req.ChannelID)
	return nil
}

func (s *ChannelStore) DeleteRevokedExportedInvites(_ context.Context, req domain.DeleteRevokedChannelInvitesRequest) error {
	if req.UserID == 0 || req.ChannelID == 0 || req.AdminUserID == 0 {
		return domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.channelForMemberLocked(req.UserID, req.ChannelID); err != nil {
		return err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canExportChannelInvite(member) {
		return domain.ErrChannelAdminRequired
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelHideJoinRequests {
		limit = domain.MaxChannelHideJoinRequests
	}
	deleted := 0
	for hash, invite := range s.invites {
		if invite.ChannelID == req.ChannelID && invite.AdminUserID == req.AdminUserID && invite.Revoked {
			delete(s.invites, hash)
			deleted++
			if deleted >= limit {
				break
			}
		}
	}
	return nil
}

func (s *ChannelStore) ListAdminsWithInvites(_ context.Context, userID, channelID int64) ([]domain.ChannelAdminInviteCount, error) {
	if userID == 0 || channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.channelForMemberLocked(userID, channelID); err != nil {
		return nil, err
	}
	member := s.members[channelID][userID]
	if !canExportChannelInvite(member) {
		return nil, domain.ErrChannelAdminRequired
	}
	byAdmin := map[int64]*domain.ChannelAdminInviteCount{}
	for _, invite := range s.invites {
		if invite.ChannelID != channelID {
			continue
		}
		count := byAdmin[invite.AdminUserID]
		if count == nil {
			count = &domain.ChannelAdminInviteCount{AdminUserID: invite.AdminUserID}
			byAdmin[invite.AdminUserID] = count
		}
		if invite.Revoked {
			count.RevokedInvitesCount++
		} else {
			count.InvitesCount++
		}
	}
	out := make([]domain.ChannelAdminInviteCount, 0, len(byAdmin))
	for _, count := range byAdmin {
		out = append(out, *count)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AdminUserID < out[j].AdminUserID })
	return out, nil
}

func (s *ChannelStore) ListInviteImporters(_ context.Context, req domain.ChannelInviteImportersRequest) (domain.ChannelInviteImporterList, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ChannelInviteImporterList{}, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.channelForMemberLocked(req.UserID, req.ChannelID); err != nil {
		return domain.ChannelInviteImporterList{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canExportChannelInvite(member) {
		return domain.ChannelInviteImporterList{}, domain.ErrChannelAdminRequired
	}
	var inviteID int64
	if req.Hash != "" {
		invite, err := s.inviteByChannelHashLocked(req.ChannelID, req.Hash)
		if err != nil {
			return domain.ChannelInviteImporterList{}, err
		}
		inviteID = invite.InviteID
	}
	if req.Query != "" {
		return domain.ChannelInviteImporterList{}, nil
	}
	all := make([]domain.ChannelInviteImporter, 0)
	for _, importer := range s.importers[req.ChannelID] {
		if importer.Requested != req.Requested {
			continue
		}
		if inviteID != 0 && importer.InviteID != inviteID {
			continue
		}
		all = append(all, importer)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Date != all[j].Date {
			return all[i].Date > all[j].Date
		}
		return all[i].UserID > all[j].UserID
	})
	total := len(all)
	start := 0
	if req.OffsetDate > 0 || req.OffsetUserID != 0 {
		start = len(all)
		for i, importer := range all {
			if importer.Date == req.OffsetDate && importer.UserID == req.OffsetUserID {
				start = i + 1
				break
			}
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelInviteListLimit {
		limit = domain.MaxChannelInviteListLimit
	}
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	return domain.ChannelInviteImporterList{Count: total, Importers: cloneChannelInviteImporters(all[start:end])}, nil
}

func (s *ChannelStore) PendingJoinRequests(_ context.Context, channelID int64, limit int) (domain.ChannelPendingJoinRequests, error) {
	if channelID == 0 {
		return domain.ChannelPendingJoinRequests{}, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, ok := s.channels[channelID]
	if !ok || channel.Deleted {
		return domain.ChannelPendingJoinRequests{}, domain.ErrChannelInvalid
	}
	all := make([]domain.ChannelInviteImporter, 0)
	for _, importer := range s.importers[channelID] {
		if importer.Requested {
			all = append(all, importer)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Date != all[j].Date {
			return all[i].Date > all[j].Date
		}
		return all[i].UserID > all[j].UserID
	})
	if limit <= 0 || limit > domain.MaxChannelPendingJoinRecentRequesters {
		limit = domain.MaxChannelPendingJoinRecentRequesters
	}
	if len(all) < limit {
		limit = len(all)
	}
	recent := make([]int64, 0, limit)
	for _, importer := range all[:limit] {
		recent = append(recent, importer.UserID)
	}
	return domain.ChannelPendingJoinRequests{
		ChannelID:        channelID,
		Count:            len(all),
		RecentRequesters: recent,
	}, nil
}

func (s *ChannelStore) HideAllChatJoinRequests(_ context.Context, req domain.HideChannelJoinRequestsRequest) (domain.CreateChannelResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canExportChannelInvite(member) {
		return domain.CreateChannelResult{}, domain.ErrChannelAdminRequired
	}
	var inviteID int64
	if req.Hash != "" {
		invite, err := s.inviteByChannelHashLocked(req.ChannelID, req.Hash)
		if err != nil {
			return domain.CreateChannelResult{}, err
		}
		inviteID = invite.InviteID
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelHideJoinRequests {
		limit = domain.MaxChannelHideJoinRequests
	}
	targets := make([]domain.ChannelInviteImporter, 0, limit)
	for _, importer := range s.importers[req.ChannelID] {
		if !importer.Requested {
			continue
		}
		if inviteID != 0 && importer.InviteID != inviteID {
			continue
		}
		targets = append(targets, importer)
		if len(targets) >= limit {
			break
		}
	}
	var result domain.CreateChannelResult
	for _, importer := range targets {
		invite := domain.ChannelInvite{ChannelID: req.ChannelID, AdminUserID: req.UserID}
		if importer.InviteID != 0 {
			var err error
			invite, err = s.inviteByIDLocked(req.ChannelID, importer.InviteID)
			if err != nil {
				return domain.CreateChannelResult{}, err
			}
		}
		if !req.Approved {
			s.deletePendingInviteImporterLocked(invite, importer.UserID)
			result = domain.CreateChannelResult{Channel: channel, Recipients: s.activeMemberIDsLocked(req.ChannelID, importer.UserID, 0)}
			continue
		}
		result, err = s.approveInviteImporterLocked(channel, invite, importer.UserID, req.UserID, req.Date)
		if err != nil {
			return domain.CreateChannelResult{}, err
		}
		channel = result.Channel
	}
	if result.Channel.ID == 0 {
		result = domain.CreateChannelResult{Channel: channel, Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0)}
	}
	return result, nil
}

func (s *ChannelStore) approveInviteImporterLocked(channel domain.Channel, invite domain.ChannelInvite, userID, approvedBy int64, date int) (domain.CreateChannelResult, error) {
	if invite.InviteID != 0 && invite.UsageLimit > 0 && invite.UsageCount >= invite.UsageLimit {
		return domain.CreateChannelResult{}, domain.ErrUsersTooMuch
	}
	channelID := channel.ID
	if channelID == 0 {
		channelID = invite.ChannelID
	}
	if existing, ok := s.members[channelID][userID]; ok {
		if existing.Status == domain.ChannelMemberActive {
			return domain.CreateChannelResult{}, domain.ErrUserAlreadyParticipant
		}
		if existing.Status == domain.ChannelMemberKicked || existing.Status == domain.ChannelMemberBanned || existing.BannedRights.ViewMessages {
			return domain.CreateChannelResult{}, domain.ErrInviteHashInvalid
		}
	}
	preJoinTopID := channel.TopMessageID
	minID := channelInitialAvailableMinID(channel)
	inviterID := invite.AdminUserID
	if inviterID == 0 {
		inviterID = approvedBy
	}
	member := domain.ChannelMember{
		ChannelID:       channelID,
		UserID:          userID,
		InviterUserID:   inviterID,
		Role:            domain.ChannelRoleMember,
		Status:          domain.ChannelMemberActive,
		JoinedAt:        date,
		AvailableMinID:  minID,
		AvailableMinPts: channelInitialAvailableMinPts(channel),
		ReadInboxMaxID:  maxInt(minID, preJoinTopID),
	}
	if s.members[channelID] == nil {
		s.members[channelID] = make(map[int64]domain.ChannelMember)
	}
	s.members[channelID][userID] = member
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID: channelID,
		UserID:    userID,
		Date:      date,
		Type:      domain.ChannelAdminLogParticipantJoin,
	})
	if importer, ok := s.importers[channelID][userID]; ok && importer.Requested {
		if importer.InviteID == invite.InviteID {
			if invite.InviteID != 0 && invite.RequestedCount > 0 {
				invite.RequestedCount--
			}
		} else if importer.InviteID != 0 {
			if pendingInvite, err := s.inviteByIDLocked(channelID, importer.InviteID); err == nil && pendingInvite.RequestedCount > 0 {
				pendingInvite.RequestedCount--
				s.invites[pendingInvite.Hash] = pendingInvite
			}
		}
	}
	if invite.InviteID != 0 && invite.Hash != "" {
		invite.UsageCount++
		s.invites[invite.Hash] = invite
	}
	s.refreshChannelCountsLocked(channelID)
	channel = s.channels[channelID]
	var msg domain.ChannelMessage
	var event domain.ChannelUpdateEvent
	if channel.Megagroup {
		msg, event = s.appendChannelServiceMessageLocked(channelID, userID, date, domain.ChannelMessageAction{
			Type:          domain.ChannelActionChatJoinedByLink,
			InviterUserID: invite.AdminUserID,
			UserIDs:       []int64{userID},
		})
		channel.TopMessageID = msg.ID
		channel.Pts = event.Pts
		s.channels[channelID] = channel
	}
	member.ReadInboxMaxID = maxInt(member.ReadInboxMaxID, channel.TopMessageID)
	if msg.ID != 0 {
		member.ReadOutboxMaxID = maxInt(member.ReadOutboxMaxID, msg.ID)
	}
	s.members[channelID][userID] = member
	s.upsertChannelDialogLocked(userID, channel, msg, true)
	if s.importers[channelID] == nil {
		s.importers[channelID] = make(map[int64]domain.ChannelInviteImporter)
	}
	s.importers[channelID][userID] = domain.ChannelInviteImporter{
		ChannelID:  channelID,
		InviteID:   invite.InviteID,
		UserID:     userID,
		Date:       date,
		ApprovedBy: approvedBy,
	}
	return domain.CreateChannelResult{
		Channel:    channel,
		Members:    []domain.ChannelMember{member},
		Message:    cloneChannelMessage(msg),
		Event:      cloneChannelEvent(event),
		Recipients: s.activeMemberIDsLocked(channelID, 0, 0),
	}, nil
}

func (s *ChannelStore) recordPendingInviteRequestLocked(invite domain.ChannelInvite, userID int64, date int) error {
	if existing, ok := s.members[invite.ChannelID][userID]; ok {
		if existing.Status == domain.ChannelMemberActive {
			return domain.ErrUserAlreadyParticipant
		}
		if existing.Status == domain.ChannelMemberKicked || existing.Status == domain.ChannelMemberBanned || existing.BannedRights.ViewMessages {
			return domain.ErrInviteHashInvalid
		}
	}
	if s.importers[invite.ChannelID] == nil {
		s.importers[invite.ChannelID] = make(map[int64]domain.ChannelInviteImporter)
	}
	if existing, ok := s.importers[invite.ChannelID][userID]; ok && existing.Requested {
		return domain.ErrInviteRequestSent
	}
	s.importers[invite.ChannelID][userID] = domain.ChannelInviteImporter{
		ChannelID: invite.ChannelID,
		InviteID:  invite.InviteID,
		UserID:    userID,
		Date:      date,
		Requested: true,
	}
	invite.RequestedCount++
	s.invites[invite.Hash] = invite
	return nil
}

func (s *ChannelStore) deletePendingInviteImporterLocked(invite domain.ChannelInvite, userID int64) {
	if existing, ok := s.importers[invite.ChannelID][userID]; ok && existing.Requested {
		delete(s.importers[invite.ChannelID], userID)
		if invite.InviteID != 0 && invite.Hash != "" && invite.RequestedCount > 0 {
			invite.RequestedCount--
			s.invites[invite.Hash] = invite
		}
	}
}

func (s *ChannelStore) newReplacementInviteLocked(old domain.ChannelInvite, date int) (domain.ChannelInvite, error) {
	inviteID, err := randomMemoryPositiveInt64()
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	hash, err := randomMemoryInviteHash()
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	if date == 0 {
		date = int(time.Now().Unix())
	}
	return domain.ChannelInvite{
		ChannelID:   old.ChannelID,
		InviteID:    inviteID,
		Hash:        hash,
		AdminUserID: old.AdminUserID,
		Permanent:   old.Permanent,
		Date:        date,
	}, nil
}

func (s *ChannelStore) setChannelHasLinkLocked(channelID int64, hasLink bool) {
	channel, ok := s.channels[channelID]
	if !ok {
		return
	}
	channel.HasLink = hasLink
	s.channels[channelID] = channel
}

func (s *ChannelStore) refreshChannelHasLinkLocked(channelID int64) domain.Channel {
	channel, ok := s.channels[channelID]
	if !ok {
		return domain.Channel{}
	}
	channel.HasLink = s.channelHasNonRevokedInviteLocked(channelID)
	s.channels[channelID] = channel
	return channel
}

func (s *ChannelStore) channelHasNonRevokedInviteLocked(channelID int64) bool {
	for _, invite := range s.invites {
		if invite.ChannelID == channelID && !invite.Revoked {
			return true
		}
	}
	return false
}

func cloneChannelInvites(in []domain.ChannelInvite) []domain.ChannelInvite {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.ChannelInvite, len(in))
	copy(out, in)
	return out
}

func cloneChannelInviteImporters(in []domain.ChannelInviteImporter) []domain.ChannelInviteImporter {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.ChannelInviteImporter, len(in))
	copy(out, in)
	return out
}

func (s *ChannelStore) ListChannelInviteAdminMemberIDs(_ context.Context, channelID int64, limit int) ([]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, ok := s.channels[channelID]
	if channelID == 0 || !ok || channel.Deleted {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxChannelRealtimeFanout {
		limit = domain.MaxChannelRealtimeFanout
	}
	members := s.members[channelID]
	out := make([]int64, 0, minInt(len(members), limit))
	for _, member := range members {
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		if member.Role == domain.ChannelRoleCreator {
			out = append(out, member.UserID)
			if len(out) >= limit {
				break
			}
			continue
		}
		if member.Role == domain.ChannelRoleAdmin && (member.AdminRights.InviteUsers || member.AdminRights.ChangeInfo) {
			out = append(out, member.UserID)
			if len(out) >= limit {
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *ChannelStore) appendChannelAdminLogLocked(event domain.ChannelAdminLogEvent) {
	if event.ChannelID == 0 || event.UserID == 0 || event.Type == "" {
		return
	}
	s.logSeq[event.ChannelID]++
	event.ID = s.logSeq[event.ChannelID]
	event.Query = adminLogSearchText(event)
	s.adminLogs[event.ChannelID] = append(s.adminLogs[event.ChannelID], cloneChannelAdminLogEvent(event))
}

func canInviteToChannel(channel domain.Channel, member domain.ChannelMember) bool {
	if member.Role == domain.ChannelRoleCreator ||
		(member.Role == domain.ChannelRoleAdmin && (member.AdminRights.InviteUsers || member.AdminRights.ChangeInfo)) {
		return true
	}
	return channel.Megagroup && !channel.DefaultBannedRights.InviteUsers && !member.BannedRights.InviteUsers
}

func canExportChannelInvite(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator ||
		(member.Role == domain.ChannelRoleAdmin && (member.AdminRights.InviteUsers || member.AdminRights.ChangeInfo))
}

func randomMemoryInviteHash() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func cloneChannelAdminLogEvent(in domain.ChannelAdminLogEvent) domain.ChannelAdminLogEvent {
	if in.PrevParticipant != nil {
		in.PrevParticipant = ptrChannelMember(*in.PrevParticipant)
	}
	if in.NewParticipant != nil {
		in.NewParticipant = ptrChannelMember(*in.NewParticipant)
	}
	if in.Participant != nil {
		in.Participant = ptrChannelMember(*in.Participant)
	}
	if in.Message != nil {
		in.Message = ptrChannelMessage(*in.Message)
	}
	if in.PrevMessage != nil {
		in.PrevMessage = ptrChannelMessage(*in.PrevMessage)
	}
	if in.NewMessage != nil {
		in.NewMessage = ptrChannelMessage(*in.NewMessage)
	}
	return in
}
