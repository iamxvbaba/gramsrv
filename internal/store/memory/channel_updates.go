package memory

import (
	"context"
	"strings"
	"telesrv/internal/domain"
)

func (s *ChannelStore) ListChannelDifference(_ context.Context, req domain.ChannelDifferenceRequest) (domain.ChannelDifference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, preview, err := s.channelForViewerLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelDifference{}, err
	}
	if req.Pts < 0 || req.Pts > channel.Pts {
		return domain.ChannelDifference{}, domain.ErrPersistentTimestamp
	}
	if !preview && member.AvailableMinPts > req.Pts {
		req.Pts = minInt(member.AvailableMinPts, channel.Pts)
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelDifferenceLimit {
		limit = domain.MaxChannelDifferenceLimit
	}
	dialog := s.dialogForUserLocked(req.UserID, channel)
	if preview {
		dialog = previewChannelDialog(req.UserID, channel, member)
	}
	if preview && member.Status != domain.ChannelMemberActive {
		return domain.ChannelDifference{
			Channel: channel,
			Self:    member,
			Pts:     channel.Pts,
			Final:   true,
			Timeout: 30,
			Dialog:  dialog,
		}, nil
	}
	if channel.Pts-req.Pts > limit {
		messages := make([]domain.ChannelMessage, 0, domain.MaxChannelDifferenceTooLongMessages)
		for i := len(s.messages[req.ChannelID]) - 1; i >= 0 && len(messages) < domain.MaxChannelDifferenceTooLongMessages; i-- {
			msg := s.messages[req.ChannelID][i]
			if msg.Deleted {
				continue
			}
			if msg.ID <= member.AvailableMinID {
				continue
			}
			messages = append(messages, cloneChannelMessage(msg))
		}
		s.populateChannelMessageUnreadFlagsLocked(req.UserID, messages)
		return domain.ChannelDifference{
			Channel:     channel,
			Self:        member,
			NewMessages: messages,
			Pts:         channel.Pts,
			Final:       true,
			TooLong:     true,
			Timeout:     30,
			Dialog:      dialog,
		}, nil
	}
	events := make([]domain.ChannelUpdateEvent, 0, limit)
	lastPts := req.Pts
	for _, event := range s.events[req.ChannelID] {
		if event.Pts <= req.Pts {
			continue
		}
		lastPts = event.Pts
		visible, ok := domain.FilterChannelUpdateEventForAvailableMinID(cloneChannelEvent(event), member.AvailableMinID)
		if !ok {
			continue
		}
		if preview && visible.Type == domain.ChannelUpdateParticipant {
			continue
		}
		events = append(events, visible)
	}
	if len(events) == 0 {
		return domain.ChannelDifference{
			Channel: channel,
			Self:    member,
			Pts:     maxInt(lastPts, req.Pts),
			Final:   true,
			Timeout: 30,
			Dialog:  dialog,
		}, nil
	}
	diff := domain.ChannelDifference{
		Channel: channel,
		Self:    member,
		Events:  events,
		Pts:     lastPts,
		Final:   lastPts >= channel.Pts,
		Timeout: 30,
		Dialog:  dialog,
	}
	for _, event := range events {
		switch event.Type {
		case domain.ChannelUpdateNewMessage:
			diff.NewMessages = append(diff.NewMessages, cloneChannelMessage(event.Message))
		default:
			diff.OtherUpdates = append(diff.OtherUpdates, cloneChannelEvent(event))
		}
	}
	s.populateChannelMessageUnreadFlagsLocked(req.UserID, diff.NewMessages)
	for i := range diff.OtherUpdates {
		if diff.OtherUpdates[i].Message.ID == 0 {
			continue
		}
		messages := []domain.ChannelMessage{diff.OtherUpdates[i].Message}
		s.populateChannelMessageUnreadFlagsLocked(req.UserID, messages)
		diff.OtherUpdates[i].Message = messages[0]
	}
	return diff, nil
}

func (s *ChannelStore) MaxChannelPts(_ context.Context, channelID int64) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ptsSeq[channelID], nil
}

func (s *ChannelStore) nextChannelPtsLocked(channelID int64) int {
	s.ptsSeq[channelID]++
	return s.ptsSeq[channelID]
}

func (s *ChannelStore) nextChannelPtsNLocked(channelID int64, count int) int {
	if count <= 0 {
		return s.ptsSeq[channelID]
	}
	s.ptsSeq[channelID] += count
	return s.ptsSeq[channelID]
}

func transientChannelParticipantEvent(channelID, actorUserID int64, previous, participant domain.ChannelMember, date int) domain.ChannelUpdateEvent {
	return domain.ChannelUpdateEvent{
		ChannelID:    channelID,
		Type:         domain.ChannelUpdateParticipant,
		Date:         date,
		SenderUserID: actorUserID,
		UserIDs:      uniqueNonZeroInt64s(actorUserID, previous.UserID, previous.InviterUserID, participant.UserID, participant.InviterUserID),
		Previous:     previous,
		Participant:  participant,
	}
}

func channelInitialAvailableMinPts(channel domain.Channel) int {
	return channel.Pts
}

func adminLogEventMatchesFilter(typ domain.ChannelAdminLogEventType, filter domain.ChannelAdminLogFilter) bool {
	if filter.Empty() {
		return true
	}
	switch typ {
	case domain.ChannelAdminLogParticipantJoin:
		return filter.Join
	case domain.ChannelAdminLogParticipantLeave:
		return filter.Leave
	case domain.ChannelAdminLogParticipantInvite:
		return filter.Invite || filter.Invites
	case domain.ChannelAdminLogParticipantBan:
		return filter.Ban
	case domain.ChannelAdminLogParticipantUnban:
		return filter.Unban
	case domain.ChannelAdminLogParticipantKick:
		return filter.Kick
	case domain.ChannelAdminLogParticipantUnkick:
		return filter.Unkick
	case domain.ChannelAdminLogParticipantPromote:
		return filter.Promote
	case domain.ChannelAdminLogParticipantDemote:
		return filter.Demote
	case domain.ChannelAdminLogParticipantEditRank:
		return filter.EditRank
	case domain.ChannelAdminLogChangeTitle, domain.ChannelAdminLogChangeUsername, domain.ChannelAdminLogChangeLinkedChat, domain.ChannelAdminLogToggleSlowMode:
		return filter.Info
	case domain.ChannelAdminLogToggleSignatures, domain.ChannelAdminLogTogglePreHistoryHidden, domain.ChannelAdminLogToggleAntiSpam, domain.ChannelAdminLogToggleAutotranslation:
		return filter.Settings
	case domain.ChannelAdminLogToggleForum:
		return filter.Settings || filter.Forums
	case domain.ChannelAdminLogUpdatePinned:
		return filter.Pinned
	case domain.ChannelAdminLogEditMessage:
		return filter.Edit
	case domain.ChannelAdminLogDeleteMessage:
		return filter.Delete
	case domain.ChannelAdminLogSendMessage:
		return filter.Send
	default:
		return false
	}
}

func adminLogEventMatchesQuery(event domain.ChannelAdminLogEvent, query string) bool {
	if strings.Contains(strings.ToLower(event.PrevString), query) ||
		strings.Contains(strings.ToLower(event.NewString), query) ||
		strings.Contains(event.Query, query) {
		return true
	}
	for _, msg := range []*domain.ChannelMessage{event.Message, event.PrevMessage, event.NewMessage} {
		if msg != nil && strings.Contains(strings.ToLower(msg.Body), query) {
			return true
		}
	}
	return false
}

func cloneChannelEvent(in domain.ChannelUpdateEvent) domain.ChannelUpdateEvent {
	in.Message = cloneChannelMessage(in.Message)
	in.MessageIDs = append([]int(nil), in.MessageIDs...)
	in.UserIDs = append([]int64(nil), in.UserIDs...)
	return in
}
