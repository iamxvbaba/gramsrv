package memory

import (
	"context"
	"encoding/binary"
	"hash"
	"sort"
	"telesrv/internal/domain"
	"time"
)

func (s *MessageStore) SetMessageReactions(_ context.Context, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	if req.UserID == 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	if len(req.Reactions) > domain.MaxChannelMessageReactionsPerUser {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	req.Reactions = domain.TrimMessageReactionsToUserMax(req.Reactions, req.ReactionsPerUserMax)
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Peer.ID == req.UserID {
		return s.setSavedMessageTagsLocked(req)
	}
	var target domain.Message
	for _, msg := range s.m[req.UserID] {
		if msg.ID == req.MessageID && msg.Peer == req.Peer {
			target = msg
			break
		}
	}
	if target.ID == 0 || target.UID == 0 {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	if _, ok := s.privateReactions[target.UID]; !ok {
		s.privateReactions[target.UID] = make(map[int64][]domain.ChannelMessagePeerReaction)
	}
	rows := make([]domain.ChannelMessagePeerReaction, 0, len(req.Reactions))
	for i, reaction := range req.Reactions {
		if !reaction.Valid() {
			return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
		}
		rows = append(rows, domain.ChannelMessagePeerReaction{
			UserID:      req.UserID,
			Reaction:    reaction,
			Big:         req.Big,
			My:          true,
			ChosenOrder: i + 1,
			Date:        req.Date,
		})
	}
	if len(rows) == 0 {
		delete(s.privateReactions[target.UID], req.UserID)
	} else {
		s.privateReactions[target.UID][req.UserID] = rows
	}
	if target.From.ID != 0 && target.From.ID != req.UserID {
		for i := range s.m[target.From.ID] {
			if s.m[target.From.ID][i].UID != target.UID {
				continue
			}
			s.m[target.From.ID][i].ReactionUnread = len(rows) > 0
			break
		}
	}
	s.refreshPrivateReactionDialogSnapshotsLocked(target.UID)
	return s.privateReactionResultLocked(target.UID), nil
}

func (s *MessageStore) GetMessageReactions(_ context.Context, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	if req.OwnerUserID == 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || len(req.IDs) > domain.MaxGetMessageIDs {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	ids := make(map[int]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
		}
		ids[id] = struct{}{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := domain.PrivateMessageReactionsResult{}
	for _, msg := range s.m[req.OwnerUserID] {
		if msg.Peer != req.Peer {
			continue
		}
		if _, ok := ids[msg.ID]; !ok {
			continue
		}
		item := cloneMessage(msg)
		reactions := s.privateMessageReactionsForMessageLocked(item)
		item.Reactions = cloneChannelMessageReactionsPtr(&reactions)
		out.Messages = append(out.Messages, item)
		if len(out.Reactions.Results) == 0 && len(out.Reactions.Recent) == 0 {
			out.Reactions = reactions
		}
	}
	return out, nil
}

func (s *MessageStore) privateReactionResultLocked(uid int64) domain.PrivateMessageReactionsResult {
	out := domain.PrivateMessageReactionsResult{}
	for _, messages := range s.m {
		for _, msg := range messages {
			if msg.UID != uid {
				continue
			}
			item := cloneMessage(msg)
			reactions := s.privateMessageReactionsForMessageLocked(item)
			item.Reactions = cloneChannelMessageReactionsPtr(&reactions)
			out.Messages = append(out.Messages, item)
			if len(out.Reactions.Results) == 0 && len(out.Reactions.Recent) == 0 {
				out.Reactions = reactions
			}
		}
	}
	return out
}

func (s *MessageStore) privateMessageReactionsForMessageLocked(msg domain.Message) domain.ChannelMessageReactions {
	if msg.OwnerUserID != 0 &&
		msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: msg.OwnerUserID}) {
		return s.savedMessageTagsForMessageLocked(msg)
	}
	reactions := s.privateMessageReactionsLocked(msg.UID, msg.OwnerUserID)
	if len(reactions.Recent) == 0 || msg.From.ID == 0 {
		return reactions
	}
	for i := range reactions.Recent {
		reactions.Recent[i].SenderUserID = msg.From.ID
		if msg.ReactionUnread && msg.From.ID == msg.OwnerUserID && reactions.Recent[i].UserID != msg.OwnerUserID {
			reactions.Recent[i].Unread = true
		}
	}
	return reactions
}

func (s *MessageStore) privateMessageReactionsLocked(uid, viewerUserID int64) domain.ChannelMessageReactions {
	byUser := s.privateReactions[uid]
	out := domain.ChannelMessageReactions{CanSeeList: true}
	if len(byUser) == 0 {
		return out
	}
	counts := make(map[string]int)
	recent := make([]domain.ChannelMessagePeerReaction, 0, len(byUser))
	for userID, rows := range byUser {
		for _, row := range rows {
			key := row.Reaction.Key()
			index, ok := counts[key]
			if !ok {
				out.Results = append(out.Results, domain.ChannelMessageReactionCount{Reaction: row.Reaction})
				index = len(out.Results) - 1
				counts[key] = index
			}
			out.Results[index].Count++
			if userID == viewerUserID && (out.Results[index].ChosenOrder == 0 || row.ChosenOrder < out.Results[index].ChosenOrder) {
				out.Results[index].ChosenOrder = row.ChosenOrder
			}
			item := row
			item.UserID = userID
			item.My = userID == viewerUserID
			recent = append(recent, item)
		}
	}
	sort.Slice(out.Results, func(i, j int) bool {
		if out.Results[i].Count != out.Results[j].Count {
			return out.Results[i].Count > out.Results[j].Count
		}
		return out.Results[i].Reaction.Key() < out.Results[j].Reaction.Key()
	})
	sort.Slice(recent, func(i, j int) bool {
		if recent[i].Date != recent[j].Date {
			return recent[i].Date > recent[j].Date
		}
		return recent[i].UserID < recent[j].UserID
	})
	if len(recent) > domain.MaxChannelMessageReactionRecent {
		recent = recent[:domain.MaxChannelMessageReactionRecent]
	}
	out.Recent = recent
	return out
}

func (s *MessageStore) countPrivateUnreadReactionsLocked(ownerUserID int64, peer domain.Peer) int {
	count := 0
	for _, msg := range s.m[ownerUserID] {
		if msg.Peer == peer && msg.ReactionUnread {
			count++
		}
	}
	return count
}

func (s *MessageStore) refreshPrivateReactionDialogSnapshotsLocked(uid int64) {
	if s.dialogs == nil || uid == 0 {
		return
	}
	s.dialogs.mu.Lock()
	defer s.dialogs.mu.Unlock()
	for ownerID, messages := range s.m {
		list := s.dialogs.m[ownerID]
		changed := false
		for _, msg := range messages {
			if msg.UID != uid {
				continue
			}
			enriched := cloneMessage(msg)
			reactions := s.privateMessageReactionsForMessageLocked(enriched)
			if len(reactions.Results) > 0 || len(reactions.Recent) > 0 {
				enriched.Reactions = cloneChannelMessageReactionsPtr(&reactions)
			}
			for i := range list.Messages {
				if list.Messages[i].UID == uid && list.Messages[i].OwnerUserID == ownerID {
					list.Messages[i] = enriched
					changed = true
				}
			}
			for i := range list.Dialogs {
				if list.Dialogs[i].Peer == msg.Peer {
					list.Dialogs[i].UnreadReactions = s.countPrivateUnreadReactionsLocked(ownerID, msg.Peer)
					changed = true
					break
				}
			}
		}
		if changed {
			s.dialogs.m[ownerID] = list
		}
	}
}

func writeMessageReactionsHash(h hash.Hash64, reactions *domain.ChannelMessageReactions) {
	if reactions == nil {
		_, _ = h.Write([]byte{0})
		return
	}
	var buf [16]byte
	if reactions.AsTags {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
	for _, item := range reactions.Results {
		_, _ = h.Write([]byte(item.Reaction.Type))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(item.Reaction.Value()))
		_, _ = h.Write([]byte{0})
		binary.LittleEndian.PutUint32(buf[:4], uint32(item.Count))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(item.ChosenOrder))
		_, _ = h.Write(buf[:8])
	}
	_, _ = h.Write([]byte{0xfe})
	for _, item := range reactions.Recent {
		_, _ = h.Write([]byte(item.Reaction.Type))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(item.Reaction.Value()))
		_, _ = h.Write([]byte{0})
		binary.LittleEndian.PutUint64(buf[:8], uint64(item.UserID))
		binary.LittleEndian.PutUint32(buf[8:12], uint32(item.Date))
		binary.LittleEndian.PutUint32(buf[12:16], uint32(item.ChosenOrder))
		_, _ = h.Write(buf[:])
	}
}
