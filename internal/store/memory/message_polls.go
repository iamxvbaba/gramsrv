package memory

import (
	"context"
	"sort"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Private-message poll voting/closing: message visibility is checked in this store, while
// all poll-level validation and state are delegated to the shared PollStore (sharing domain
// pure-function semantics with the postgres implementation).

func (s *MessageStore) VoteMessagePoll(ctx context.Context, req domain.VotePrivateMessagePollRequest, effects store.DeliveryEffectsBuilder[domain.PrivateMessagePollResult]) (domain.PrivateMessagePollResult, error) {
	target, err := s.pollMessageTarget(req.UserID, req.Peer, req.MessageID)
	if err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	pollID := target.Media.Poll.ID
	previous, existed := s.snapshotPollVote(pollID, req.UserID)
	if err := s.polls.Vote(pollID, req.UserID, req.Options, req.Date); err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	res, err := s.privatePollResultWithEffects(ctx, target.UID, pollID, req.Date, effects)
	if err != nil {
		s.restorePollVote(pollID, req.UserID, previous, existed)
		return domain.PrivateMessagePollResult{}, err
	}
	return res, nil
}

func (s *MessageStore) CloseMessagePoll(ctx context.Context, req domain.ClosePrivateMessagePollRequest, effects store.DeliveryEffectsBuilder[domain.PrivateMessagePollResult]) (domain.PrivateMessagePollResult, error) {
	target, err := s.pollMessageTarget(req.UserID, req.Peer, req.MessageID)
	if err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	pollID := target.Media.Poll.ID
	previous, existed := s.snapshotPollDefinition(pollID)
	if err := s.polls.Close(pollID, req.UserID); err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	res, err := s.privatePollResultWithEffects(ctx, target.UID, pollID, req.Date, effects)
	if err != nil {
		s.restorePollDefinition(pollID, previous, existed)
		return domain.PrivateMessagePollResult{}, err
	}
	return res, nil
}

func (s *MessageStore) privatePollResultWithEffects(ctx context.Context, uid, pollID int64, now int, effects store.DeliveryEffectsBuilder[domain.PrivateMessagePollResult]) (domain.PrivateMessagePollResult, error) {
	if effects == nil {
		return domain.PrivateMessagePollResult{}, store.ErrDeliveryOutboxRequired
	}
	out := s.privatePollResult(uid, pollID, now)
	sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].OwnerUserID < out.Messages[j].OwnerUserID })
	intents, err := effects(out)
	if err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	if len(intents) == 0 && len(out.Messages) > 0 {
		return domain.PrivateMessagePollResult{}, store.ErrDeliveryOutboxRequired
	}
	s.mu.RLock()
	outbox := s.deliveryOutbox
	s.mu.RUnlock()
	if _, err := applyDeliveryEffects(ctx, intents, outbox, nil); err != nil {
		return domain.PrivateMessagePollResult{}, err
	}
	return out, nil
}

func (s *MessageStore) snapshotPollVote(pollID, userID int64) (domain.PollVote, bool) {
	s.polls.mu.RLock()
	defer s.polls.mu.RUnlock()
	vote, ok := s.polls.votes[pollID][userID]
	return clonePollVote(vote), ok
}

func (s *MessageStore) restorePollVote(pollID, userID int64, vote domain.PollVote, existed bool) {
	s.polls.mu.Lock()
	defer s.polls.mu.Unlock()
	if !existed {
		delete(s.polls.votes[pollID], userID)
		return
	}
	if s.polls.votes[pollID] == nil {
		s.polls.votes[pollID] = make(map[int64]domain.PollVote)
	}
	s.polls.votes[pollID][userID] = clonePollVote(vote)
}

func (s *MessageStore) snapshotPollDefinition(pollID int64) (domain.PollDefinition, bool) {
	s.polls.mu.RLock()
	defer s.polls.mu.RUnlock()
	def, ok := s.polls.polls[pollID]
	return clonePollDefinition(def), ok
}

func (s *MessageStore) restorePollDefinition(pollID int64, def domain.PollDefinition, existed bool) {
	s.polls.mu.Lock()
	defer s.polls.mu.Unlock()
	if !existed {
		delete(s.polls.polls, pollID)
		return
	}
	s.polls.polls[pollID] = clonePollDefinition(def)
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
