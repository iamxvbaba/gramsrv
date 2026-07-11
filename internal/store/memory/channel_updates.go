package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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
	checkpoint := s.channelUpdateCheckpointLocked(req.ChannelID, channel)
	if req.Pts < checkpoint.RetainedThroughPts || channel.Pts-req.Pts > limit {
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

func (s *ChannelStore) MaxChannelPtsBatch(_ context.Context, channelIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(channelIDs))
	s.mu.RLock()
	for _, channelID := range channelIDs {
		if pts, ok := s.ptsSeq[channelID]; ok {
			out[channelID] = pts
		}
	}
	s.mu.RUnlock()
	return out, nil
}

// appendChannelEventLocked is the only memory-store append boundary for channel-scoped durable
// events. Keeping the checkpoint current here mirrors the PostgreSQL event+checkpoint transaction.
func (s *ChannelStore) appendChannelEventLocked(event domain.ChannelUpdateEvent) {
	s.events[event.ChannelID] = append(s.events[event.ChannelID], event)
	checkpoint := s.retention[event.ChannelID]
	checkpoint.ChannelID = event.ChannelID
	if event.Pts > checkpoint.LatestPts {
		checkpoint.LatestPts = event.Pts
	}
	if event.Date > checkpoint.LatestEventDate {
		checkpoint.LatestEventDate = event.Date
	}
	s.retention[event.ChannelID] = checkpoint
}

func (s *ChannelStore) channelUpdateCheckpointLocked(channelID int64, channel domain.Channel) domain.ChannelUpdateRetentionCheckpoint {
	checkpoint := s.retention[channelID]
	checkpoint.ChannelID = channelID
	if channel.Pts > checkpoint.LatestPts {
		checkpoint.LatestPts = channel.Pts
	}
	for _, event := range s.events[channelID] {
		if event.Pts > checkpoint.LatestPts {
			checkpoint.LatestPts = event.Pts
		}
		if event.Date > checkpoint.LatestEventDate {
			checkpoint.LatestEventDate = event.Date
		}
	}
	return checkpoint
}

// PruneChannelUpdateEvents removes a bounded contiguous prefix and advances the retained floor in
// the same memory-store critical section. throughPts may land inside a pts_count interval; that row
// is retained because channel event rows are indivisible.
func (s *ChannelStore) PruneChannelUpdateEvents(_ context.Context, channelID int64, throughPts, limit int) (domain.ChannelUpdateRetentionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneChannelUpdateEventsLocked(channelID, throughPts, 0, limit)
}

// DeleteExpiredChannelUpdateEvents uses the oldest retained event of each channel as an indexed-seek
// analogue, then prunes candidates oldest-first. There is no offset scan and total deleted rows never
// exceeds limit.
func (s *ChannelStore) DeleteExpiredChannelUpdateEvents(_ context.Context, olderThan time.Duration, limit int) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	limit = normalizeChannelRetentionLimit(limit)
	cutoff := int(time.Now().Add(-olderThan).Unix())

	s.mu.Lock()
	defer s.mu.Unlock()
	type candidate struct {
		channelID int64
		date      int
	}
	candidates := make([]candidate, 0)
	for channelID, channel := range s.channels {
		checkpoint := s.channelUpdateCheckpointLocked(channelID, channel)
		for _, event := range s.events[channelID] {
			if event.Pts <= checkpoint.RetainedThroughPts {
				continue
			}
			if event.Date < cutoff {
				candidates = append(candidates, candidate{channelID: channelID, date: event.Date})
			}
			break
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].date == candidates[j].date {
			return candidates[i].channelID < candidates[j].channelID
		}
		return candidates[i].date < candidates[j].date
	})

	deleted := 0
	for _, item := range candidates {
		if deleted >= limit {
			break
		}
		channel := s.channels[item.channelID]
		result, err := s.pruneChannelUpdateEventsLocked(item.channelID, channel.Pts, cutoff, limit-deleted)
		if err != nil {
			return deleted, err
		}
		deleted += result.Deleted
	}
	return deleted, nil
}

func (s *ChannelStore) pruneChannelUpdateEventsLocked(channelID int64, throughPts, beforeDate, limit int) (domain.ChannelUpdateRetentionResult, error) {
	channel, ok := s.channels[channelID]
	if !ok || channelID == 0 || throughPts < 0 {
		return domain.ChannelUpdateRetentionResult{}, domain.ErrChannelInvalid
	}
	limit = normalizeChannelRetentionLimit(limit)
	checkpoint := s.channelUpdateCheckpointLocked(channelID, channel)
	if throughPts > checkpoint.LatestPts {
		throughPts = checkpoint.LatestPts
	}
	if throughPts <= checkpoint.RetainedThroughPts {
		s.retention[channelID] = checkpoint
		return domain.ChannelUpdateRetentionResult{Checkpoint: checkpoint}, nil
	}

	cursor := checkpoint.RetainedThroughPts
	deleted := 0
	keep := make([]domain.ChannelUpdateEvent, 0, len(s.events[channelID]))
	canPrune := true
	for _, event := range s.events[channelID] {
		if event.Pts <= checkpoint.RetainedThroughPts {
			keep = append(keep, event)
			continue
		}
		if !canPrune || deleted >= limit || event.Pts > throughPts || (beforeDate > 0 && event.Date >= beforeDate) {
			canPrune = false
			keep = append(keep, event)
			continue
		}
		ptsCount := event.PtsCount
		if ptsCount <= 0 {
			return domain.ChannelUpdateRetentionResult{}, fmt.Errorf(
				"prune channel update events: channel %d has invalid pts_count=%d at pts=%d",
				channelID, ptsCount, event.Pts,
			)
		}
		if event.Pts != cursor+ptsCount {
			return domain.ChannelUpdateRetentionResult{}, fmt.Errorf(
				"prune channel update events: channel %d has gap after pts %d: event pts=%d pts_count=%d",
				channelID, cursor, event.Pts, ptsCount,
			)
		}
		cursor = event.Pts
		deleted++
	}
	if deleted > 0 {
		s.events[channelID] = keep
		checkpoint.RetainedThroughPts = cursor
	}
	s.retention[channelID] = checkpoint
	return domain.ChannelUpdateRetentionResult{Checkpoint: checkpoint, Deleted: deleted}, nil
}

func normalizeChannelRetentionLimit(limit int) int {
	if limit <= 0 || limit > domain.MaxChannelUpdateRetentionBatch {
		return domain.MaxChannelUpdateRetentionBatch
	}
	return limit
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
