package memory

import (
	"context"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"time"
)

func (s *ChannelStore) EditChannelTitle(_ context.Context, req domain.EditChannelTitleRequest) (domain.EditChannelTitleResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || strings.TrimSpace(req.Title) == "" {
		return domain.EditChannelTitleResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelTitleResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canChangeChannelInfo(member) {
		return domain.EditChannelTitleResult{}, domain.ErrChannelAdminRequired
	}
	title := strings.TrimSpace(req.Title)
	if channel.Title == title {
		return domain.EditChannelTitleResult{}, domain.ErrChannelNotModified
	}
	prevTitle := channel.Title
	channel.Title = title
	msg, event := s.appendChannelServiceMessageLocked(req.ChannelID, req.UserID, req.Date, domain.ChannelMessageAction{
		Type:  domain.ChannelActionEditTitle,
		Title: title,
	})
	channel.TopMessageID = msg.ID
	channel.Pts = event.Pts
	s.channels[req.ChannelID] = channel
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID:  req.ChannelID,
		UserID:     req.UserID,
		Date:       req.Date,
		Type:       domain.ChannelAdminLogChangeTitle,
		PrevString: prevTitle,
		NewString:  title,
	})
	s.upsertChannelDialogLocked(req.UserID, channel, msg, true)
	return domain.EditChannelTitleResult{
		Channel:    channel,
		Message:    cloneChannelMessage(msg),
		Event:      cloneChannelEvent(event),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
	}, nil
}

func (s *ChannelStore) SetChannelWallpaper(_ context.Context, req domain.SetChannelWallpaperRequest) (domain.SetChannelWallpaperResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.SetChannelWallpaperResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	wallpaper := domain.CloneWallpaperPtr(req.Wallpaper)
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.SetChannelWallpaperResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canChangeChannelInfo(member) {
		return domain.SetChannelWallpaperResult{}, domain.ErrChannelAdminRequired
	}
	if domain.WallpaperEqual(channel.Wallpaper, wallpaper) {
		return domain.SetChannelWallpaperResult{Channel: cloneChannel(channel)}, nil
	}
	channel.Wallpaper = domain.CloneWallpaperPtr(wallpaper)
	if wallpaper == nil {
		s.channels[req.ChannelID] = channel
		return domain.SetChannelWallpaperResult{
			Channel:    cloneChannel(channel),
			Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
			Changed:    true,
		}, nil
	}
	msg, event := s.appendChannelServiceMessageLocked(req.ChannelID, req.UserID, req.Date, domain.ChannelMessageAction{
		Type:      domain.ChannelActionSetChatWallpaper,
		Wallpaper: domain.CloneWallpaperPtr(wallpaper),
	})
	channel.TopMessageID = msg.ID
	channel.Pts = event.Pts
	s.channels[req.ChannelID] = channel
	s.upsertChannelDialogLocked(req.UserID, channel, msg, true)
	return domain.SetChannelWallpaperResult{
		Channel:    cloneChannel(channel),
		Message:    cloneChannelMessage(msg),
		Event:      cloneChannelEvent(event),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
		Changed:    true,
	}, nil
}

func (s *ChannelStore) EditChannelAbout(_ context.Context, req domain.EditChannelAboutRequest) (domain.Channel, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	channel.About = req.About
	s.channels[req.ChannelID] = channel
	return channel, nil
}

func (s *ChannelStore) CheckUsername(_ context.Context, userID, channelID int64, username string) (bool, error) {
	if userID == 0 || channelID == 0 || strings.TrimSpace(username) == "" {
		return false, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.channelForMemberLocked(userID, channelID); err != nil {
		return false, err
	}
	usernameLower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	for id, channel := range s.channels {
		if channel.Deleted || channel.Username == "" {
			continue
		}
		if strings.ToLower(channel.Username) == usernameLower && id != channelID {
			return false, nil
		}
	}
	return true, nil
}

func (s *ChannelStore) UpdateUsername(_ context.Context, req domain.UpdateChannelUsernameRequest) (domain.Channel, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if member.Role != domain.ChannelRoleCreator {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	username := strings.TrimSpace(strings.TrimPrefix(req.Username, "@"))
	usernameLower := strings.ToLower(username)
	if strings.EqualFold(channel.Username, username) {
		return domain.Channel{}, domain.ErrChannelNotModified
	}
	if usernameLower != "" {
		for id, existing := range s.channels {
			if existing.Deleted || existing.Username == "" {
				continue
			}
			if strings.ToLower(existing.Username) == usernameLower && id != req.ChannelID {
				return domain.Channel{}, domain.ErrUsernameOccupied
			}
		}
	}
	prevUsername := channel.Username
	channel.Username = username
	s.channels[req.ChannelID] = channel
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID:  req.ChannelID,
		UserID:     req.UserID,
		Date:       int(time.Now().Unix()),
		Type:       domain.ChannelAdminLogChangeUsername,
		PrevString: prevUsername,
		NewString:  username,
	})
	return channel, nil
}

func (s *ChannelStore) SetChannelVerified(_ context.Context, channelID int64, verified bool) (domain.Channel, error) {
	if channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, ok := s.channels[channelID]
	if !ok || channel.Deleted {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	channel.Verified = verified
	s.channels[channelID] = channel
	return cloneChannel(channel), nil
}

func (s *ChannelStore) ResolvePublicChannelUsername(_ context.Context, viewerUserID int64, username string) (domain.Channel, bool, error) {
	_ = viewerUserID // zero is the anonymous public-web view; no membership state is projected.
	username = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	if username == "" {
		return domain.Channel{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, channel := range s.channels {
		if !publicSearchableChannel(channel) {
			continue
		}
		if strings.ToLower(channel.Username) == username {
			return cloneChannel(channel), true, nil
		}
	}
	return domain.Channel{}, false, nil
}

func (s *ChannelStore) SetChannelPhoto(_ context.Context, userID, channelID int64, photo *domain.Photo, date int) (domain.SetChannelPhotoResult, error) {
	if userID == 0 || channelID == 0 {
		return domain.SetChannelPhotoResult{}, domain.ErrChannelInvalid
	}
	if date == 0 {
		date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.SetChannelPhotoResult{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.SetChannelPhotoResult{}, domain.ErrChannelAdminRequired
	}
	var action domain.ChannelMessageAction
	if photo != nil && photo.ID != 0 {
		if channel.PhotoID == photo.ID {
			return domain.SetChannelPhotoResult{}, domain.ErrChannelNotModified
		}
		action = domain.ChannelMessageAction{
			Type:  domain.ChannelActionChatEditPhoto,
			Photo: domain.ClonePhotoPtr(photo),
		}
		channel.PhotoID = photo.ID
		channel.PhotoDCID = photo.DCID
		channel.PhotoStripped = domain.StrippedFromSizes(photo.Sizes)
	} else {
		if channel.PhotoID == 0 {
			return domain.SetChannelPhotoResult{}, domain.ErrChannelNotModified
		}
		action = domain.ChannelMessageAction{Type: domain.ChannelActionChatDeletePhoto}
		channel.PhotoID = 0
		channel.PhotoDCID = 0
		channel.PhotoStripped = nil
	}
	msg, event := s.appendChannelServiceMessageLocked(channelID, userID, date, action)
	channel.TopMessageID = msg.ID
	channel.Pts = event.Pts
	s.channels[channelID] = channel
	s.upsertChannelDialogLocked(userID, channel, msg, true)
	return domain.SetChannelPhotoResult{
		Channel:    cloneChannel(channel),
		Message:    cloneChannelMessage(msg),
		Event:      cloneChannelEvent(event),
		Recipients: s.activeMemberIDsLocked(channelID, 0, 0),
		Changed:    true,
	}, nil
}

func (s *ChannelStore) SetSignatures(_ context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.Signatures
	channel.Signatures = enabled
	s.channels[channelID] = channel
	if prev != enabled {
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      int(time.Now().Unix()),
			Type:      domain.ChannelAdminLogToggleSignatures,
			PrevBool:  prev,
			NewBool:   enabled,
		})
	}
	return channel, nil
}

func (s *ChannelStore) SetAutotranslation(_ context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.Autotranslation
	channel.Autotranslation = enabled
	s.channels[channelID] = channel
	if prev != enabled {
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      int(time.Now().Unix()),
			Type:      domain.ChannelAdminLogToggleAutotranslation,
			PrevBool:  prev,
			NewBool:   enabled,
		})
	}
	return cloneChannel(channel), nil
}

func (s *ChannelStore) SetRestrictedSponsored(_ context.Context, userID, channelID int64, restricted bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	channel.RestrictedSponsored = restricted
	s.channels[channelID] = channel
	return cloneChannel(channel), nil
}

func (s *ChannelStore) SetAntiSpam(_ context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !channel.Megagroup || !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.AntiSpam
	channel.AntiSpam = enabled
	s.channels[channelID] = channel
	if prev != enabled {
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      int(time.Now().Unix()),
			Type:      domain.ChannelAdminLogToggleAntiSpam,
			PrevBool:  prev,
			NewBool:   enabled,
		})
	}
	return cloneChannel(channel), nil
}

func (s *ChannelStore) SetSlowMode(_ context.Context, userID, channelID int64, seconds int) (domain.Channel, error) {
	if userID == 0 || channelID == 0 || !domain.ValidChannelSlowModeSeconds(seconds) {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.SlowmodeSeconds
	channel.SlowmodeSeconds = seconds
	s.channels[channelID] = channel
	if prev != seconds {
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      int(time.Now().Unix()),
			Type:      domain.ChannelAdminLogToggleSlowMode,
			PrevInt:   prev,
			NewInt:    seconds,
		})
	}
	return channel, nil
}

func (s *ChannelStore) SetBoostsToUnblockRestrictions(_ context.Context, userID, channelID int64, boosts int) (domain.Channel, error) {
	if userID == 0 || channelID == 0 || boosts < 0 || boosts > domain.MaxChannelBoostsToUnblockRestrictions {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	channel.BoostsUnrestrict = boosts
	s.channels[channelID] = channel
	return cloneChannel(channel), nil
}

func (s *ChannelStore) SetNoForwards(_ context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	channel.NoForwards = enabled
	s.channels[channelID] = channel
	return channel, nil
}

func (s *ChannelStore) SetColor(_ context.Context, userID, channelID int64, forProfile bool, color domain.ChannelPeerColor) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if forProfile {
		channel.ProfileColor = color
	} else {
		channel.Color = color
	}
	s.channels[channelID] = channel
	return cloneChannel(channel), nil
}

func (s *ChannelStore) SetEmojiStatus(_ context.Context, userID, channelID int64, status domain.ChannelEmojiStatus) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	channel.EmojiStatus = status
	s.channels[channelID] = channel
	return cloneChannel(channel), nil
}

func (s *ChannelStore) ListChannelRecommendations(_ context.Context, req domain.ChannelRecommendationsRequest) (domain.ChannelRecommendationsResult, error) {
	if req.UserID == 0 || req.SourceChannelID < 0 {
		return domain.ChannelRecommendationsResult{}, domain.ErrChannelInvalid
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelRecommendationsLimit {
		limit = domain.DefaultChannelRecommendationsLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]domain.Channel, 0, limit)
	for channelID, channel := range s.channels {
		if !recommendableChannel(channel) || channelID == req.SourceChannelID {
			continue
		}
		if req.SourceChannelID == 0 {
			if member, ok := s.members[channelID][req.UserID]; ok && member.Status == domain.ChannelMemberActive {
				continue
			}
		}
		items = append(items, cloneChannel(channel))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ParticipantsCount != items[j].ParticipantsCount {
			return items[i].ParticipantsCount > items[j].ParticipantsCount
		}
		if items[i].Date != items[j].Date {
			return items[i].Date > items[j].Date
		}
		return items[i].ID > items[j].ID
	})
	out := domain.ChannelRecommendationsResult{Count: len(items)}
	if len(items) > limit {
		items = items[:limit]
	}
	out.Channels = append(out.Channels, items...)
	return out, nil
}

func (s *ChannelStore) ListDiscussionGroups(_ context.Context, userID int64, limit int) ([]domain.Channel, error) {
	if userID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxDiscussionGroupsLimit {
		limit = domain.MaxDiscussionGroupsLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]domain.Channel, 0, limit)
	for channelID, channel := range s.channels {
		if !validDiscussionGroup(channel) || channel.Deleted {
			continue
		}
		member := s.members[channelID][userID]
		if member.Status != domain.ChannelMemberActive || !canManageDiscussionGroup(member) {
			continue
		}
		items = append(items, cloneChannel(channel))
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ID > items[j].ID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *ChannelStore) SetDiscussionGroup(_ context.Context, userID, broadcastID, groupID int64) (domain.DiscussionGroupUpdateResult, error) {
	if userID == 0 {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrChannelInvalid
	}
	if broadcastID == 0 && groupID == 0 {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrLinkNotModified
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := make(map[int64]domain.Channel)
	markChanged := func(channel domain.Channel) {
		if channel.ID != 0 {
			changed[channel.ID] = cloneChannel(channel)
		}
	}
	setLinked := func(channelID, linkedID int64) (domain.Channel, bool) {
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted {
			return domain.Channel{}, false
		}
		if channel.LinkedChatID == linkedID {
			return channel, true
		}
		channel.LinkedChatID = linkedID
		s.channels[channelID] = channel
		markChanged(channel)
		return channel, true
	}

	if broadcastID == 0 {
		group, groupMember, err := s.channelAndMemberLocked(userID, groupID)
		if err != nil || !validDiscussionGroup(group) {
			return domain.DiscussionGroupUpdateResult{}, domain.ErrMegagroupIDInvalid
		}
		if !canManageDiscussionGroup(groupMember) {
			return domain.DiscussionGroupUpdateResult{}, domain.ErrChannelAdminRequired
		}
		oldBroadcastID := group.LinkedChatID
		if oldBroadcastID == 0 {
			return domain.DiscussionGroupUpdateResult{}, domain.ErrLinkNotModified
		}
		if oldBroadcast, ok := s.channels[oldBroadcastID]; ok && oldBroadcast.LinkedChatID == groupID {
			if updated, ok := setLinked(oldBroadcastID, 0); ok {
				s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
					ChannelID: updated.ID,
					UserID:    userID,
					Date:      int(time.Now().Unix()),
					Type:      domain.ChannelAdminLogChangeLinkedChat,
					PrevInt:   int(groupID),
					NewInt:    0,
				})
			}
		}
		setLinked(groupID, 0)
		return discussionGroupUpdateResult(changed), nil
	}

	broadcast, broadcastMember, err := s.channelAndMemberLocked(userID, broadcastID)
	if err != nil || !broadcast.Broadcast || broadcast.Megagroup {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrBroadcastIDInvalid
	}
	if !canManageDiscussionBroadcast(broadcastMember) {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrChannelAdminRequired
	}
	oldGroupID := broadcast.LinkedChatID
	if groupID == 0 {
		if oldGroupID == 0 {
			return domain.DiscussionGroupUpdateResult{}, domain.ErrLinkNotModified
		}
		updated, _ := setLinked(broadcastID, 0)
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: updated.ID,
			UserID:    userID,
			Date:      int(time.Now().Unix()),
			Type:      domain.ChannelAdminLogChangeLinkedChat,
			PrevInt:   int(oldGroupID),
			NewInt:    0,
		})
		if oldGroup, ok := s.channels[oldGroupID]; ok && oldGroup.LinkedChatID == broadcastID {
			setLinked(oldGroupID, 0)
		}
		return discussionGroupUpdateResult(changed), nil
	}

	group, groupMember, err := s.channelAndMemberLocked(userID, groupID)
	if err != nil || !validDiscussionGroup(group) {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrMegagroupIDInvalid
	}
	if group.PreHistoryHidden {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrMegagroupPrehistoryHidden
	}
	if !canManageDiscussionGroup(groupMember) {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrChannelAdminRequired
	}
	if oldGroupID == groupID && group.LinkedChatID == broadcastID {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrLinkNotModified
	}
	oldBroadcastID := group.LinkedChatID
	if oldGroupID != 0 && oldGroupID != groupID {
		if oldGroup, ok := s.channels[oldGroupID]; ok && oldGroup.LinkedChatID == broadcastID {
			setLinked(oldGroupID, 0)
		}
	}
	if oldBroadcastID != 0 && oldBroadcastID != broadcastID {
		if oldBroadcast, ok := s.channels[oldBroadcastID]; ok && oldBroadcast.LinkedChatID == groupID {
			if updated, ok := setLinked(oldBroadcastID, 0); ok {
				s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
					ChannelID: updated.ID,
					UserID:    userID,
					Date:      int(time.Now().Unix()),
					Type:      domain.ChannelAdminLogChangeLinkedChat,
					PrevInt:   int(groupID),
					NewInt:    0,
				})
			}
		}
	}
	updatedBroadcast, _ := setLinked(broadcastID, groupID)
	setLinked(groupID, broadcastID)
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID: updatedBroadcast.ID,
		UserID:    userID,
		Date:      int(time.Now().Unix()),
		Type:      domain.ChannelAdminLogChangeLinkedChat,
		PrevInt:   int(oldGroupID),
		NewInt:    int(groupID),
	})
	return discussionGroupUpdateResult(changed), nil
}

func validDiscussionGroup(channel domain.Channel) bool {
	return channel.Megagroup && !channel.Broadcast && !channel.Forum && !channel.Deleted
}

func channelSlowModeWait(channel domain.Channel, member domain.ChannelMember, now int) int {
	if channel.SlowmodeSeconds <= 0 || member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin {
		return 0
	}
	next := member.SlowmodeLastSendDate + channel.SlowmodeSeconds
	if now >= next {
		return 0
	}
	return next - now
}

func cloneChannelDiscussionRef(in *domain.ChannelDiscussionRef) *domain.ChannelDiscussionRef {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
