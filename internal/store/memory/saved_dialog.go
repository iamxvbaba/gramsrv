package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
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

func (s *MessageStore) MutateSavedDialogs(_ context.Context, mutation store.SavedDialogMutation, effects store.DeliveryEffectsBuilder[store.SavedDialogMutationSnapshot]) (store.SavedDialogMutationSnapshot, error) {
	if err := mutation.Validate(); err != nil {
		return store.SavedDialogMutationSnapshot{}, err
	}
	if effects == nil {
		return store.SavedDialogMutationSnapshot{}, store.ErrDeliveryOutboxRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.updateEvents
	if events == nil {
		return store.SavedDialogMutationSnapshot{}, store.ErrDeliveryOutboxRequired
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	before := append([]domain.Peer(nil), s.savedPins[mutation.UserID]...)
	after := append([]domain.Peer(nil), before...)
	switch mutation.Kind {
	case store.SavedDialogSetPinned:
		index := savedDialogPeerIndex(after, mutation.Peer)
		if mutation.Pinned && index < 0 {
			if len(after) >= domain.MaxPinnedSavedDialogs {
				return store.SavedDialogMutationSnapshot{}, domain.ErrPinnedSavedDialogsTooMuch
			}
			after = append([]domain.Peer{mutation.Peer}, after...)
		} else if !mutation.Pinned && index >= 0 {
			after = append(after[:index], after[index+1:]...)
		}
	case store.SavedDialogReorder:
		after = append([]domain.Peer(nil), mutation.Order...)
		if !mutation.Force {
			seen := make(map[domain.Peer]struct{}, len(after))
			for _, peer := range after {
				seen[peer] = struct{}{}
			}
			for _, peer := range before {
				if _, exists := seen[peer]; !exists {
					after = append(after, peer)
				}
			}
		}
		if len(after) > domain.MaxPinnedSavedDialogs {
			return store.SavedDialogMutationSnapshot{}, domain.ErrPinnedSavedDialogsTooMuch
		}
	}
	snapshot := store.SavedDialogMutationSnapshot{
		Mutation: mutation, Changed: !savedDialogPeerOrderEqual(before, after), Order: append([]domain.Peer(nil), after...),
	}
	intents, err := effects(snapshot)
	if err != nil {
		return store.SavedDialogMutationSnapshot{}, fmt.Errorf("build memory saved dialog effects: %w", err)
	}
	if err := store.ValidateSavedDialogEffects(snapshot, intents); err != nil {
		return store.SavedDialogMutationSnapshot{}, fmt.Errorf("validate memory saved dialog effects: %w", err)
	}
	if len(intents) == 1 {
		event := events.appendLocked(mutation.UserID, intents[0].Event, true)
		events.dispatches[mutation.UserID] = append(events.dispatches[mutation.UserID], memoryUpdateDispatch{Pts: event.Pts})
		intents[0].Event = event
		if event.Pts > s.nextPts[mutation.UserID] {
			s.nextPts[mutation.UserID] = event.Pts
		}
	}
	s.savedPins[mutation.UserID] = after
	snapshot.Effects = intents
	return snapshot, nil
}

func savedDialogPeerIndex(peers []domain.Peer, target domain.Peer) int {
	for i, peer := range peers {
		if peer == target {
			return i
		}
	}
	return -1
}

func savedDialogPeerOrderEqual(a, b []domain.Peer) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
