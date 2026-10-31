package memory

import (
	"context"
	"fmt"
	"sort"
	"telesrv/internal/domain"
	"time"
)

func (s *MessageStore) ReadMessageContents(_ context.Context, req domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error) {
	res := domain.ReadMessageContentsResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 {
		return res, fmt.Errorf("read message contents: missing owner user id")
	}
	if len(req.IDs) > domain.MaxGetMessageIDs {
		return res, domain.ErrMessageIDInvalid
	}
	wanted := make(map[int]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return res, domain.ErrMessageIDInvalid
		}
		wanted[id] = struct{}{}
	}
	if len(wanted) == 0 {
		return res, nil
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reactionUIDs := make(map[int64]struct{})
	senderUIDs := make(map[int64]map[int64]struct{})
	for i := range s.m[req.OwnerUserID] {
		msg := &s.m[req.OwnerUserID][i]
		if _, ok := wanted[msg.ID]; !ok {
			continue
		}
		if !msg.MediaUnread && !msg.ReactionUnread {
			continue
		}
		if msg.ReactionUnread && msg.Peer.ID != 0 {
			reactionUIDs[msg.UID] = struct{}{}
		}
		if msg.MediaUnread && !msg.Out && msg.From.Type == domain.PeerTypeUser && msg.From.ID != 0 && msg.From.ID != req.OwnerUserID {
			if senderUIDs[msg.From.ID] == nil {
				senderUIDs[msg.From.ID] = make(map[int64]struct{})
			}
			senderUIDs[msg.From.ID][msg.UID] = struct{}{}
		}
		msg.MediaUnread = false
		msg.ReactionUnread = false
		res.MessageIDs = append(res.MessageIDs, msg.ID)
	}
	sort.Ints(res.MessageIDs)
	if len(res.MessageIDs) == 0 {
		return res, nil
	}
	senderIDs := make([]int64, 0, len(senderUIDs))
	for senderID := range senderUIDs {
		senderIDs = append(senderIDs, senderID)
	}
	sort.Slice(senderIDs, func(i, j int) bool { return senderIDs[i] < senderIDs[j] })
	for _, senderID := range senderIDs {
		boxIDs := make([]int, 0, len(senderUIDs[senderID]))
		for i := range s.m[senderID] {
			msg := &s.m[senderID][i]
			if _, ok := senderUIDs[senderID][msg.UID]; !ok || !msg.Out || !msg.MediaUnread {
				continue
			}
			msg.MediaUnread = false
			boxIDs = append(boxIDs, msg.ID)
		}
		if len(boxIDs) == 0 {
			continue
		}
		sort.Ints(boxIDs)
		res.SenderEvents = append(res.SenderEvents, domain.UpdateEvent{
			UserID:     senderID,
			Type:       domain.UpdateEventReadMessageContents,
			Pts:        s.nextPtsNLocked(senderID, len(boxIDs)),
			PtsCount:   len(boxIDs),
			Date:       req.Date,
			MessageIDs: boxIDs,
		})
	}
	for uid := range reactionUIDs {
		s.refreshPrivateReactionDialogSnapshotsLocked(uid)
	}
	pts := s.nextPtsNLocked(req.OwnerUserID, len(res.MessageIDs))
	res.Event = domain.UpdateEvent{
		UserID:     req.OwnerUserID,
		Type:       domain.UpdateEventReadMessageContents,
		Pts:        pts,
		PtsCount:   len(res.MessageIDs),
		Date:       req.Date,
		MessageIDs: append([]int(nil), res.MessageIDs...),
	}
	return res, nil
}

func (s *MessageStore) GetOutboxReadDate(_ context.Context, req domain.OutboxReadDateRequest) (int, error) {
	if req.OwnerUserID == 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return 0, domain.ErrMessageIDInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := false
	for _, msg := range s.m[req.OwnerUserID] {
		if msg.ID == req.ID && msg.Peer == req.Peer && msg.Out {
			found = true
			break
		}
	}
	if !found {
		return 0, domain.ErrMessageIDInvalid
	}
	date := s.readOutboxDates[readOutboxDateKey{ownerUserID: req.OwnerUserID, peerID: req.Peer.ID, msgID: req.ID}]
	if date == 0 {
		return 0, domain.ErrMessageNotReadYet
	}
	return date, nil
}

// ListUnreadReactionMessages returns the messages with reaction_unread for the current owner under this peer.
func (s *MessageStore) ListUnreadReactionMessages(_ context.Context, ownerUserID int64, peer domain.Peer, limit int) ([]domain.Message, error) {
	if ownerUserID == 0 || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > domain.MaxChannelUnreadReactionsLimit {
		limit = domain.MaxChannelUnreadReactionsLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Message, 0, limit)
	messages := s.m[ownerUserID]
	for i := len(messages) - 1; i >= 0 && len(out) < limit; i-- {
		msg := messages[i]
		if msg.Peer != peer || !msg.ReactionUnread {
			continue
		}
		out = append(out, cloneMessage(msg))
	}
	return out, nil
}

// ReadPeerReactions clears all unread reaction state for the current owner under this peer.
func (s *MessageStore) ReadPeerReactions(_ context.Context, ownerUserID int64, peer domain.Peer) (int, error) {
	if ownerUserID == 0 || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cleared := 0
	reactionUIDs := make(map[int64]struct{})
	for i := range s.m[ownerUserID] {
		msg := &s.m[ownerUserID][i]
		if msg.Peer != peer || !msg.ReactionUnread {
			continue
		}
		msg.ReactionUnread = false
		reactionUIDs[msg.UID] = struct{}{}
		cleared++
	}
	if cleared > 0 {
		for uid := range reactionUIDs {
			s.refreshPrivateReactionDialogSnapshotsLocked(uid)
		}
	}
	return cleared, nil
}
