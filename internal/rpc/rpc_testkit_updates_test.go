package rpc

import (
	"context"
	"telesrv/internal/domain"
)

type captureUpdates struct {
	state            domain.UpdateState
	currentState     *domain.UpdateState
	acknowledged     bool
	authKeyID        [8]byte
	userID           int64
	clearedAuthKeyID [8]byte
	cleared          bool
	date             int
	events           []domain.UpdateEvent
	excludeSessionID int64
	reliableDispatch bool
}

func (s *captureUpdates) UsesReliableDispatch() bool {
	return s.reliableDispatch
}

func (s *captureUpdates) GetState(_ context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	return s.state, nil
}

func (s *captureUpdates) CurrentState(_ context.Context, userID int64) (domain.UpdateState, error) {
	s.userID = userID
	if s.currentState != nil {
		return *s.currentState, nil
	}
	return s.state, nil
}

func (s *captureUpdates) AcknowledgeCurrentState(_ context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	st := s.state
	if s.currentState != nil {
		st = *s.currentState
	}
	// 模拟真实 service：确认水位推进到账号最新。
	s.state = st
	s.acknowledged = true
	return st, nil
}

func (s *captureUpdates) GetDifference(_ context.Context, authKeyID [8]byte, userID int64, _ domain.UpdateState) (domain.UpdateDifference, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	return domain.UpdateDifference{State: s.state}, nil
}

func (s *captureUpdates) ClearAuthKey(_ context.Context, authKeyID [8]byte) error {
	s.clearedAuthKeyID = authKeyID
	s.cleared = true
	return nil
}

func (s *captureUpdates) RecordNewMessage(_ context.Context, authKeyID [8]byte, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	s.date = msg.Date
	event := domain.UpdateEvent{Type: domain.UpdateEventNewMessage, Pts: s.state.Pts, PtsCount: 1, Date: msg.Date, Message: msg}
	s.events = append(s.events, event)
	return event, s.state, nil
}

func (s *captureUpdates) PublishNewMessage(_ context.Context, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error) {
	s.userID = userID
	s.date = msg.Date
	event := domain.UpdateEvent{Type: domain.UpdateEventNewMessage, Pts: s.state.Pts + 1, PtsCount: 1, Date: msg.Date, Message: msg}
	s.events = append(s.events, event)
	st := s.state
	st.Pts = event.Pts
	if st.Date == 0 {
		st.Date = msg.Date
	}
	return event, st, nil
}

func (s *captureUpdates) RecordStory(_ context.Context, authKeyID [8]byte, userID int64, story domain.Story, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{
		Type:  domain.UpdateEventStory,
		Peer:  story.Owner,
		Story: story,
		MaxID: story.ID,
	})
}

func (s *captureUpdates) RecordStoryFanout(_ context.Context, userID int64, story domain.Story) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordCapturedEvent([8]byte{}, userID, domain.UpdateEvent{
		Type:  domain.UpdateEventStory,
		Peer:  story.Owner,
		Story: story,
		MaxID: story.ID,
	})
}

func (s *captureUpdates) RecordReadHistory(_ context.Context, authKeyID [8]byte, userID int64, read domain.ReadHistoryResult, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	s.excludeSessionID = excludeSessionID
	event := domain.UpdateEvent{
		Type:             domain.UpdateEventReadHistoryInbox,
		Pts:              s.state.Pts,
		PtsCount:         1,
		Date:             s.state.Date,
		Peer:             read.Peer,
		MaxID:            read.MaxID,
		StillUnreadCount: read.StillUnreadCount,
		ChannelPts:       read.ChannelPts,
	}
	s.events = append(s.events, event)
	return event, s.state, nil
}

func (s *captureUpdates) RecordReadStories(_ context.Context, authKeyID [8]byte, userID int64, read domain.StoryReadResult, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{
		Type:  domain.UpdateEventReadStories,
		Peer:  read.Peer,
		MaxID: read.MaxReadID,
	})
}

func (s *captureUpdates) RecordSentStoryReaction(_ context.Context, authKeyID [8]byte, userID int64, reaction domain.StoryReactionResult, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventSentStoryReaction,
		Peer:     reaction.Peer,
		MaxID:    reaction.StoryID,
		Story:    reaction.Story,
		Reaction: reaction.Reaction,
	})
}

func (s *captureUpdates) RecordNewStoryReaction(_ context.Context, authKeyID [8]byte, ownerUserID int64, reaction domain.StoryReactionResult, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, ownerUserID, domain.UpdateEvent{
		Type:     domain.UpdateEventNewStoryReaction,
		Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: reaction.ViewerID},
		MaxID:    reaction.StoryID,
		Story:    reaction.Story,
		Reaction: reaction.Reaction,
	})
}

func (s *captureUpdates) RecordQuickReplyMutation(_ context.Context, authKeyID [8]byte, userID int64, mutation domain.QuickReplyMutation, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	event := domain.UpdateEvent{
		Date:              mutation.Date,
		QuickReplies:      append([]domain.QuickReply(nil), mutation.List.QuickReplies...),
		QuickReply:        mutation.QuickReply,
		QuickReplyMessage: mutation.Message,
		MessageIDs:        append([]int(nil), mutation.MessageIDs...),
		MaxID:             mutation.ShortcutID,
	}
	switch mutation.Kind {
	case domain.QuickReplyMutationNew:
		event.Type = domain.UpdateEventNewQuickReply
	case domain.QuickReplyMutationDelete:
		event.Type = domain.UpdateEventDeleteQuickReply
	case domain.QuickReplyMutationMessage:
		event.Type = domain.UpdateEventQuickReplyMessage
	case domain.QuickReplyMutationIDs:
		event.Type = domain.UpdateEventDeleteQuickReplyMessages
	default:
		event.Type = domain.UpdateEventQuickReplies
	}
	return s.recordCapturedEvent(authKeyID, userID, event)
}

func (s *captureUpdates) RecordChannelState(_ context.Context, authKeyID [8]byte, userID, channelID int64, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventChannelState, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}})
}

func (s *captureUpdates) RecordContactsReset(_ context.Context, authKeyID [8]byte, userID int64, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventContactsReset})
}

func (s *captureUpdates) RecordDraftMessage(_ context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, topMsgID int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDraftMessage, Peer: peer, MaxID: topMsgID})
}

func (s *captureUpdates) RecordDialogPinned(_ context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, folderID int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogPinned, Peer: peer, Bool: pinned, FolderID: folderID})
}

func (s *captureUpdates) RecordPinnedDialogs(_ context.Context, authKeyID [8]byte, userID int64, folderID int, order []domain.Peer, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventPinnedDialogs, Peers: append([]domain.Peer(nil), order...), FolderID: folderID})
}

func (s *captureUpdates) RecordSavedDialogPinned(_ context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventSavedDialogPinned, Peer: peer, Bool: pinned})
}

func (s *captureUpdates) RecordPinnedSavedDialogs(_ context.Context, authKeyID [8]byte, userID int64, order []domain.Peer, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventPinnedSavedDialogs, Peers: append([]domain.Peer(nil), order...)})
}

func (s *captureUpdates) RecordDialogUnreadMark(_ context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, unread bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogUnreadMark, Peer: peer, Bool: unread})
}

func (s *captureUpdates) RecordPeerSettings(_ context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, settings domain.PeerSettings, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventPeerSettings, Peer: peer, Settings: settings})
}

func (s *captureUpdates) RecordPeerStoryBlocked(_ context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, blocked bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventPeerStoryBlocked, Peer: peer, Bool: blocked})
}

func (s *captureUpdates) RecordDialogFilter(_ context.Context, authKeyID [8]byte, userID int64, folderID int, folder *domain.DialogFolder, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogFilter, FilterID: folderID, DialogFilter: folder})
}

func (s *captureUpdates) RecordDialogFilterOrder(_ context.Context, authKeyID [8]byte, userID int64, order []int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogFilterOrder, FilterOrder: append([]int(nil), order...)})
}

func (s *captureUpdates) RecordDialogFiltersReload(_ context.Context, authKeyID [8]byte, userID int64, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogFilters})
}

func (s *captureUpdates) RecordFolderPeers(_ context.Context, authKeyID [8]byte, userID int64, peers []domain.FolderPeerUpdate, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventFolderPeers, FolderPeers: append([]domain.FolderPeerUpdate(nil), peers...)})
}

func (s *captureUpdates) RecordChannelAvailableMessages(_ context.Context, authKeyID [8]byte, userID, channelID int64, availableMinID int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{
		Type:  domain.UpdateEventChannelAvailable,
		Peer:  domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		MaxID: availableMinID,
	})
}

func (s *captureUpdates) RecordChannelViewForumAsMessages(_ context.Context, authKeyID [8]byte, userID, channelID int64, enabled bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{
		Type: domain.UpdateEventChannelViewForum,
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		Bool: enabled,
	})
}

func (s *captureUpdates) RecordChannelDiscussionInbox(_ context.Context, authKeyID [8]byte, userID, channelID int64, topicID, maxID int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventReadChannelDiscussionInbox,
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		TopMsgID: topicID,
		MaxID:    maxID,
	})
}

func (s *captureUpdates) recordCapturedEvent(authKeyID [8]byte, userID int64, event domain.UpdateEvent) (domain.UpdateEvent, domain.UpdateState, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	if event.Pts == 0 {
		event.Pts = s.state.Pts
	}
	if event.PtsCount == 0 {
		event.PtsCount = 1
	}
	if event.Date == 0 {
		event.Date = s.state.Date
	}
	event.UserID = userID
	s.events = append(s.events, event)
	return event, s.state, nil
}
