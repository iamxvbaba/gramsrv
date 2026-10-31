package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"telesrv/internal/domain"
)

// savedDialogTopsLocked aggregates the top message per saved_peer group in the self-chat.
// It returns them in descending top box id order.
func (s *MessageStore) savedDialogTopsLocked(userID int64) []domain.Message {
	tops := make(map[domain.Peer]domain.Message)
	selfPeer := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
	for _, msg := range s.m[userID] {
		if msg.Peer != selfPeer || msg.SavedPeer.ID == 0 {
			continue
		}
		if cur, ok := tops[msg.SavedPeer]; !ok || msg.ID > cur.ID {
			tops[msg.SavedPeer] = msg
		}
	}
	out := make([]domain.Message, 0, len(tops))
	for _, msg := range tops {
		out = append(out, msg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func (s *MessageStore) savedPinIndexLocked(userID int64, peer domain.Peer) int {
	for i, p := range s.savedPins[userID] {
		if p == peer {
			return i
		}
	}
	return -1
}

func (s *MessageStore) appendSavedDialogLocked(out *domain.SavedDialogList, msg domain.Message, pinned bool) {
	out.Dialogs = append(out.Dialogs, domain.SavedDialog{
		Peer:       msg.SavedPeer,
		TopMessage: msg.ID,
		Pinned:     pinned,
	})
	out.Messages = append(out.Messages, cloneMessage(msg))
}

func (s *MessageStore) ListSavedDialogs(_ context.Context, userID int64, filter domain.SavedDialogsFilter) (domain.SavedDialogList, error) {
	out := domain.SavedDialogList{}
	if userID == 0 {
		return out, fmt.Errorf("list saved dialogs: missing user id")
	}
	limit := filter.Limit
	if limit <= 0 || limit > domain.MaxSavedDialogsLimit {
		limit = domain.MaxSavedDialogsLimit
	}
	offsetID := filter.OffsetID
	// DrKLO Android sends offset_id = MaxMessageBoxID (int32 max) for the first page so that
	// the >= branch matches the first page, avoiding misclassification as a continuation page that skips the pin block (matching postgres).
	firstPage := offsetID <= 0 || offsetID >= domain.MaxMessageBoxID
	s.mu.RLock()
	defer s.mu.RUnlock()
	tops := s.savedDialogTopsLocked(userID)
	topByPeer := make(map[domain.Peer]domain.Message, len(tops))
	for _, msg := range tops {
		topByPeer[msg.SavedPeer] = msg
	}
	// First page and pins are not excluded: the pin block comes first by pinned_order.
	if firstPage && !filter.ExcludePinned {
		for _, peer := range s.savedPins[userID] {
			if len(out.Dialogs) >= limit {
				break
			}
			if msg, ok := topByPeer[peer]; ok {
				s.appendSavedDialogLocked(&out, msg, true)
			}
		}
	}
	// The normal block always excludes pins (pins are only returned with the first page).
	hasMore := false
	for _, msg := range tops {
		if len(out.Dialogs) >= limit {
			if s.savedPinIndexLocked(userID, msg.SavedPeer) < 0 &&
				(firstPage || msg.ID < offsetID) {
				hasMore = true
			}
			continue
		}
		if s.savedPinIndexLocked(userID, msg.SavedPeer) >= 0 {
			continue
		}
		if !firstPage && msg.ID >= offsetID {
			continue
		}
		s.appendSavedDialogLocked(&out, msg, false)
	}
	total := 0
	for _, msg := range tops {
		if filter.ExcludePinned && s.savedPinIndexLocked(userID, msg.SavedPeer) >= 0 {
			continue
		}
		total++
	}
	out.Count = total
	out.Full = !hasMore
	return out, nil
}

func (s *MessageStore) ListPinnedSavedDialogs(_ context.Context, userID int64) (domain.SavedDialogList, error) {
	out := domain.SavedDialogList{Full: true}
	if userID == 0 {
		return out, fmt.Errorf("list pinned saved dialogs: missing user id")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	tops := s.savedDialogTopsLocked(userID)
	topByPeer := make(map[domain.Peer]domain.Message, len(tops))
	for _, msg := range tops {
		topByPeer[msg.SavedPeer] = msg
	}
	for _, peer := range s.savedPins[userID] {
		if msg, ok := topByPeer[peer]; ok {
			s.appendSavedDialogLocked(&out, msg, true)
		}
	}
	out.Count = len(out.Dialogs)
	return out, nil
}

func (s *MessageStore) ListSavedDialogsByPeers(_ context.Context, userID int64, peers []domain.Peer) (domain.SavedDialogList, error) {
	out := domain.SavedDialogList{Full: true}
	if userID == 0 {
		return out, fmt.Errorf("list saved dialogs by peers: missing user id")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	tops := s.savedDialogTopsLocked(userID)
	topByPeer := make(map[domain.Peer]domain.Message, len(tops))
	for _, msg := range tops {
		topByPeer[msg.SavedPeer] = msg
	}
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, peer := range peers {
		if peer.Type == "" || peer.ID == 0 {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		if msg, ok := topByPeer[peer]; ok {
			s.appendSavedDialogLocked(&out, msg, s.savedPinIndexLocked(userID, peer) >= 0)
		}
	}
	out.Count = len(out.Dialogs)
	return out, nil
}

func (s *MessageStore) ToggleSavedDialogPin(_ context.Context, userID int64, peer domain.Peer, pinned bool) (bool, error) {
	if userID == 0 || peer.Type == "" || peer.ID == 0 {
		return false, fmt.Errorf("toggle saved dialog pin: invalid input")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.savedPinIndexLocked(userID, peer)
	if !pinned {
		if idx < 0 {
			return false, nil
		}
		s.savedPins[userID] = append(s.savedPins[userID][:idx], s.savedPins[userID][idx+1:]...)
		return true, nil
	}
	if idx >= 0 {
		return false, nil
	}
	if len(s.savedPins[userID]) >= domain.MaxPinnedSavedDialogs {
		return false, domain.ErrPinnedSavedDialogsTooMuch
	}
	s.savedPins[userID] = append([]domain.Peer{peer}, s.savedPins[userID]...)
	return true, nil
}

func (s *MessageStore) ReorderPinnedSavedDialogs(_ context.Context, userID int64, order []domain.Peer, force bool) error {
	if userID == 0 {
		return fmt.Errorf("reorder pinned saved dialogs: missing user id")
	}
	if len(order) > domain.MaxPinnedSavedDialogs {
		return domain.ErrPinnedSavedDialogsTooMuch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([]domain.Peer, 0, len(order))
	seen := make(map[domain.Peer]struct{}, len(order))
	for _, peer := range order {
		if peer.Type == "" || peer.ID == 0 {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		next = append(next, peer)
	}
	if !force {
		// Non-force: existing pins outside order keep their relative order and are appended after.
		for _, peer := range s.savedPins[userID] {
			if _, ok := seen[peer]; !ok {
				next = append(next, peer)
			}
		}
	}
	s.savedPins[userID] = next
	return nil
}

func (s *MessageStore) DeleteSavedHistory(_ context.Context, req domain.DeleteSavedHistoryRequest) (domain.DeleteSavedHistoryResult, error) {
	res := domain.DeleteSavedHistoryResult{}
	if req.OwnerUserID == 0 || req.SavedPeer.Type == "" || req.SavedPeer.ID == 0 {
		return res, fmt.Errorf("delete saved history: invalid input")
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	selfPeer := domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID}
	s.mu.Lock()
	defer s.mu.Unlock()
	match := func(msg domain.Message) bool {
		if msg.Peer != selfPeer || msg.SavedPeer != req.SavedPeer {
			return false
		}
		if req.MaxID > 0 && msg.ID > req.MaxID {
			return false
		}
		if req.MinDate > 0 && msg.Date < req.MinDate {
			return false
		}
		if req.MaxDate > 0 && msg.Date > req.MaxDate {
			return false
		}
		return true
	}
	deleted, _, more := s.deleteMemoryMessagesLocked(req.OwnerUserID, domain.MaxDeleteHistoryBatch, match)
	delRes := s.finishMemoryDeleteLocked(domain.DeleteMessagesResult{OwnerUserID: req.OwnerUserID}, deleted, req.Date, nil)
	res.More = more
	for _, d := range delRes.Deleted {
		if d.UserID == req.OwnerUserID {
			res.MessageIDs = d.MessageIDs
			res.Event = d.Event
		}
	}
	if len(deleted) > 0 && !more {
		// When a sub-conversation is emptied, clear its pin row (same semantics as the PG implementation).
		alive := false
		for _, msg := range s.m[req.OwnerUserID] {
			if msg.Peer == selfPeer && msg.SavedPeer == req.SavedPeer {
				alive = true
				break
			}
		}
		if !alive {
			if idx := s.savedPinIndexLocked(req.OwnerUserID, req.SavedPeer); idx >= 0 {
				s.savedPins[req.OwnerUserID] = append(s.savedPins[req.OwnerUserID][:idx], s.savedPins[req.OwnerUserID][idx+1:]...)
			}
		}
	}
	return res, nil
}
