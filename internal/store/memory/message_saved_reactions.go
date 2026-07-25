package memory

import (
	"context"
	"sort"

	"telesrv/internal/domain"
)

func (s *MessageStore) setSavedMessageTagsLocked(req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	var target domain.Message
	for _, msg := range s.m[req.UserID] {
		if msg.ID == req.MessageID &&
			msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) {
			target = msg
			break
		}
	}
	if target.ID == 0 {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	for _, reaction := range req.Reactions {
		if !reaction.Valid() {
			return domain.PrivateMessageReactionsResult{}, domain.ErrReactionInvalid
		}
	}
	if len(req.Reactions) == 0 {
		if byMessage := s.savedMessageTags[req.UserID]; byMessage != nil {
			delete(byMessage, target.ID)
			if len(byMessage) == 0 {
				delete(s.savedMessageTags, req.UserID)
			}
		}
	} else {
		if s.savedMessageTags[req.UserID] == nil {
			s.savedMessageTags[req.UserID] = make(map[int][]domain.MessageReaction)
		}
		s.savedMessageTags[req.UserID][target.ID] = append([]domain.MessageReaction(nil), req.Reactions...)
	}
	item := cloneMessage(target)
	reactions := s.savedMessageTagsForMessageLocked(item)
	item.Reactions = cloneChannelMessageReactionsPtr(&reactions)
	return domain.PrivateMessageReactionsResult{
		Messages:  []domain.Message{item},
		Reactions: reactions,
	}, nil
}

func (s *MessageStore) savedMessageTagsForMessageLocked(msg domain.Message) domain.ChannelMessageReactions {
	out := domain.ChannelMessageReactions{
		AsTags:  true,
		Results: []domain.ChannelMessageReactionCount{},
		Recent:  []domain.ChannelMessagePeerReaction{},
	}
	for i, reaction := range s.savedMessageTags[msg.OwnerUserID][msg.ID] {
		out.Results = append(out.Results, domain.ChannelMessageReactionCount{
			Reaction:    reaction,
			Count:       1,
			ChosenOrder: i + 1,
		})
	}
	return out
}

func (s *MessageStore) ListSavedReactionTags(_ context.Context, req domain.SavedReactionTagsRequest) ([]domain.SavedReactionTag, error) {
	if req.UserID == 0 {
		return nil, domain.ErrReactionInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxSavedReactionTags {
		req.Limit = domain.MaxSavedReactionTags
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	visible := make(map[int]domain.Message, len(s.m[req.UserID]))
	for _, msg := range s.m[req.UserID] {
		if msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) {
			visible[msg.ID] = msg
		}
	}
	byKey := make(map[string]domain.SavedReactionTag)
	for messageID, reactions := range s.savedMessageTags[req.UserID] {
		msg, ok := visible[messageID]
		if !ok || (req.SavedPeer.ID != 0 && msg.SavedPeer != req.SavedPeer) {
			continue
		}
		for _, reaction := range reactions {
			key := reaction.Key()
			tag := byKey[key]
			tag.UserID = req.UserID
			tag.Reaction = reaction
			tag.Count++
			if req.SavedPeer.ID == 0 {
				tag.Title = s.savedTagTitles[req.UserID][key]
			}
			byKey[key] = tag
		}
	}
	out := make([]domain.SavedReactionTag, 0, len(byKey))
	for _, tag := range byKey {
		out = append(out, tag)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reaction.Key() > out[j].Reaction.Key()
	})
	if len(out) > req.Limit {
		out = out[:req.Limit]
	}
	return out, nil
}

func (s *MessageStore) UpsertSavedReactionTag(_ context.Context, tag domain.SavedReactionTag) error {
	if tag.UserID == 0 || !tag.Reaction.Valid() {
		return domain.ErrReactionInvalid
	}
	key := tag.Reaction.Key()
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for messageID, reactions := range s.savedMessageTags[tag.UserID] {
		alive := false
		for _, msg := range s.m[tag.UserID] {
			if msg.ID == messageID &&
				msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: tag.UserID}) {
				alive = true
				break
			}
		}
		if !alive {
			continue
		}
		for _, reaction := range reactions {
			if reaction.Key() == key {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return domain.ErrReactionInvalid
	}
	if tag.Title == "" {
		if titles := s.savedTagTitles[tag.UserID]; titles != nil {
			delete(titles, key)
		}
		return nil
	}
	if s.savedTagTitles[tag.UserID] == nil {
		s.savedTagTitles[tag.UserID] = make(map[string]string)
	}
	s.savedTagTitles[tag.UserID][key] = tag.Title
	return nil
}
