package memory

import (
	"context"
	"errors"
	"strings"
	"telesrv/internal/domain"
	"time"
)

func (s *ChannelStore) CreateChannel(_ context.Context, req domain.CreateChannelRequest) (domain.CreateChannelResult, error) {
	if req.CreatorUserID == 0 || strings.TrimSpace(req.Title) == "" {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	channelID := s.nextChannelIDLocked()
	channel := domain.Channel{
		ID:                channelID,
		AccessHash:        s.nextAccessHashLocked(),
		CreatorUserID:     req.CreatorUserID,
		Title:             strings.TrimSpace(req.Title),
		About:             req.About,
		Broadcast:         req.Broadcast,
		Megagroup:         req.Megagroup,
		Forum:             req.Forum,
		ForumTabs:         req.ForumTabs,
		ParticipantsCount: 1,
		AdminsCount:       1,
		TTLPeriod:         req.TTLPeriod,
		Date:              req.Date,
	}
	if !channel.Broadcast && !channel.Megagroup {
		channel.Broadcast = true
	}
	inviteID, err := randomMemoryPositiveInt64()
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	inviteHash, err := randomMemoryInviteHash()
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	inviteDate := channel.Date
	if inviteDate == 0 {
		inviteDate = int(time.Now().Unix())
	}
	channel.HasLink = true
	creator := domain.ChannelMember{
		ChannelID:   channelID,
		UserID:      req.CreatorUserID,
		Role:        domain.ChannelRoleCreator,
		Status:      domain.ChannelMemberActive,
		JoinedAt:    req.Date,
		AdminRights: domain.CreatorChannelAdminRights(),
	}
	s.channels[channelID] = channel
	s.invites[inviteHash] = domain.ChannelInvite{
		ChannelID:   channelID,
		InviteID:    inviteID,
		Hash:        inviteHash,
		AdminUserID: req.CreatorUserID,
		Permanent:   true,
		Date:        inviteDate,
	}
	s.members[channelID] = map[int64]domain.ChannelMember{creator.UserID: creator}
	members := []domain.ChannelMember{creator}
	for _, userID := range uniqueNonZero(req.MemberUserIDs, req.CreatorUserID) {
		member := domain.ChannelMember{
			ChannelID:     channelID,
			UserID:        userID,
			InviterUserID: req.CreatorUserID,
			Role:          domain.ChannelRoleMember,
			Status:        domain.ChannelMemberActive,
			JoinedAt:      req.Date,
		}
		s.members[channelID][userID] = member
		members = append(members, member)
		channel.ParticipantsCount++
	}
	msg, event := s.appendChannelServiceMessageLocked(channelID, req.CreatorUserID, req.Date, domain.ChannelMessageAction{
		Type:  domain.ChannelActionCreate,
		Title: channel.Title,
	})
	channel.TopMessageID = msg.ID
	channel.Pts = event.Pts
	s.channels[channelID] = channel
	for _, member := range members {
		s.upsertChannelDialogLocked(member.UserID, channel, msg, member.UserID == req.CreatorUserID)
	}
	return domain.CreateChannelResult{
		Channel:    channel,
		Members:    cloneChannelMembers(members),
		Message:    cloneChannelMessage(msg),
		Event:      cloneChannelEvent(event),
		Recipients: s.activeMemberIDsLocked(channelID, 0, 0),
	}, nil
}

func (s *ChannelStore) GetChannel(_ context.Context, viewerUserID, channelID int64) (domain.ChannelView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, preview, err := s.channelForViewerLocked(viewerUserID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	dialog := s.dialogForUserLocked(viewerUserID, channel)
	if preview {
		dialog = previewChannelDialog(viewerUserID, channel, member)
	}
	var exportedInvite *domain.ChannelInvite
	if !preview && canExportChannelInvite(member) {
		if invite, ok := s.permanentInviteForAdminLocked(channelID, viewerUserID); ok {
			exportedInvite = &invite
		}
	}
	return domain.ChannelView{
		Channel:           cloneChannel(channel),
		Self:              member,
		Dialog:            dialog,
		SelfBoostsApplied: s.selfBoostsAppliedLocked(viewerUserID, channelID, int(time.Now().Unix())),
		ExportedInvite:    exportedInvite,
	}, nil
}

// GetLinkedDiscussionChannel returns the discussion peer through an active
// membership in its source broadcast channel. It deliberately does not turn
// the viewer into a discussion-group member: callers need the Left projection
// so clients can show the comment entry and, when needed, the Join Group gate.
func (s *ChannelStore) GetLinkedDiscussionChannel(_ context.Context, viewerUserID, sourceChannelID int64) (domain.ChannelView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	source, _, err := s.channelAndMemberLocked(viewerUserID, sourceChannelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if !source.Broadcast || source.LinkedChatID == 0 {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	linked, ok := s.channels[source.LinkedChatID]
	if !ok || linked.Deleted || !linked.Megagroup || linked.Broadcast {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	self, guest, guestErr := s.linkedDiscussionGuestLocked(viewerUserID, linked)
	if guestErr != nil {
		return domain.ChannelView{}, guestErr
	}
	if !guest {
		if member, ok := s.members[linked.ID][viewerUserID]; ok {
			self = member
		} else {
			return domain.ChannelView{}, domain.ErrChannelPrivate
		}
	}
	return domain.ChannelView{Channel: cloneChannel(linked), Self: self}, nil
}

// ResolveChannel 是 GetChannel 的轻量版：只做访问校验并返回 Channel+Self，跳过 dialog/boost。
// 与 postgres 实现语义一致（内存侧 dialog/boost 本就便宜，但保持接口行为对齐）。
func (s *ChannelStore) ResolveChannel(_ context.Context, viewerUserID, channelID int64) (domain.ChannelView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, preview, err := s.channelForViewerLocked(viewerUserID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	view := domain.ChannelView{Channel: cloneChannel(channel), Self: member}
	if preview {
		view.Dialog = previewChannelDialog(viewerUserID, channel, member)
	}
	return view, nil
}

func (s *ChannelStore) GetChannels(_ context.Context, viewerUserID int64, channelIDs []int64) ([]domain.ChannelView, error) {
	if viewerUserID == 0 || len(channelIDs) == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ChannelView, 0, len(channelIDs))
	seen := make(map[int64]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		channel, member, preview, err := s.channelForViewerLocked(viewerUserID, channelID)
		if err != nil {
			if errors.Is(err, domain.ErrChannelUserBanned) {
				if banned, ok := s.channels[channelID]; ok && !banned.Deleted {
					out = append(out, domain.ChannelView{
						Channel:   cloneChannel(banned),
						Self:      s.members[channelID][viewerUserID],
						Forbidden: true,
					})
				}
				continue
			}
			if errors.Is(err, domain.ErrChannelInvalid) || errors.Is(err, domain.ErrChannelPrivate) {
				continue
			}
			return nil, err
		}
		dialog := s.dialogForUserLocked(viewerUserID, channel)
		if preview {
			dialog = previewChannelDialog(viewerUserID, channel, member)
		}
		out = append(out, domain.ChannelView{
			Channel:           cloneChannel(channel),
			Self:              member,
			Dialog:            dialog,
			SelfBoostsApplied: s.selfBoostsAppliedLocked(viewerUserID, channelID, int(time.Now().Unix())),
		})
	}
	return out, nil
}

func (s *ChannelStore) GetChannelByID(_ context.Context, channelID int64) (domain.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, ok := s.channels[channelID]
	if !ok || channel.Deleted {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return cloneChannel(channel), nil
}

func publicPreviewableChannel(channel domain.Channel) bool {
	return publicSearchableChannel(channel)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func uniqueNonZero(ids []int64, exclude int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 || id == exclude {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func uniqueNonZeroInt64s(items ...int64) []int64 {
	seen := make(map[int64]struct{}, len(items))
	out := make([]int64, 0, len(items))
	for _, item := range items {
		if item == 0 {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func cloneChannel(in domain.Channel) domain.Channel {
	in.ReactionPolicy = copyChannelReactionPolicy(in.ReactionPolicy)
	in.Wallpaper = domain.CloneWallpaperPtr(in.Wallpaper)
	return in
}
