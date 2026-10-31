package memory

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// Private-message poll voting/closing: message visibility is checked in this store, while
// all poll-level validation and state are delegated to the shared PollStore (sharing domain
// pure-function semantics with the postgres implementation).

func (s *MessageStore) VoteMessagePoll(_ context.Context, req domain.VotePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	target, err := s.pollMessageTarget(req.UserID, req.Peer, req.MessageID)
	if err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	if err := s.polls.Vote(target.Media.Poll.ID, req.UserID, req.Options, req.Date); err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	return s.privatePollResult(target.UID, target.Media.Poll.ID, req.Date), nil
}

func (s *MessageStore) CloseMessagePoll(_ context.Context, req domain.ClosePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	target, err := s.pollMessageTarget(req.UserID, req.Peer, req.MessageID)
	if err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	if err := s.polls.Close(target.Media.Poll.ID, req.UserID); err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	return s.privatePollResult(target.UID, target.Media.Poll.ID, req.Date), nil
}

// pollMessageTarget locates the target message carrying a poll in the viewer's box.
func (s *MessageStore) pollMessageTarget(userID int64, peer domain.Peer, messageID int) (domain.Message, error) {
	if s == nil || s.polls == nil {
		return domain.Message{}, domain.ErrMessageIDInvalid
	}
	if userID == 0 || peer.Type != domain.PeerTypeUser || peer.ID == 0 || messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return domain.Message{}, domain.ErrMessageIDInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, msg := range s.m[userID] {
		if msg.ID != messageID || msg.Peer != peer {
			continue
		}
		if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindPoll || msg.Media.Poll == nil || msg.Media.Poll.ID == 0 {
			return domain.Message{}, domain.ErrMessageIDInvalid
		}
		return msg, nil
	}
	return domain.Message{}, domain.ErrMessageIDInvalid
}

// privatePollResult collects all owner copies of the same UID and enriches the poll from each owner's perspective.
func (s *MessageStore) privatePollResult(uid, pollID int64, now int) domain.PrivateMessagePollResult {
	out := domain.PrivateMessagePollResult{PollID: pollID}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, messages := range s.m {
		for _, msg := range messages {
			if msg.UID != uid {
				continue
			}
			item := cloneMessage(msg)
			item.Media = enrichPollMediaForViewer(s.polls, item.Media, item.OwnerUserID, now)
			out.Messages = append(out.Messages, item)
		}
	}
	return out
}

// enrichPrivateMessagePolls is the poll enrichment entry point for the private-chat read path (parallel to reaction filling).
func (s *MessageStore) enrichPrivateMessagePolls(messages []domain.Message, now int) {
	if s == nil || s.polls == nil {
		return
	}
	for i := range messages {
		messages[i].Media = enrichPollMediaForViewer(s.polls, messages[i].Media, messages[i].OwnerUserID, now)
	}
}
