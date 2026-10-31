package memory

import (
	"context"
	"errors"
	"sort"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PinPrivateMessage has the same semantics as the PG implementation: a non-pm_oneside pin
// sets both sides, unpin always clears both sides, it is an idempotent no-op when the state
// is unchanged, and service messages cannot be pinned.
func (s *MessageStore) PinPrivateMessage(_ context.Context, req domain.PinPrivateMessageRequest) (domain.PinPrivateMessageResult, error) {
	res := domain.PinPrivateMessageResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 {
		return res, domain.ErrMessageIDInvalid
	}
	if req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return res, domain.ErrMessageIDInvalid
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ownIdx := -1
	for i, msg := range s.m[req.OwnerUserID] {
		if msg.Peer == req.Peer && msg.ID == req.MessageID {
			ownIdx = i
			break
		}
	}
	if ownIdx < 0 {
		return res, domain.ErrMessageIDInvalid
	}
	owned := s.m[req.OwnerUserID][ownIdx]
	if owned.Media != nil && owned.Media.Kind == domain.MessageMediaKindService {
		return res, domain.ErrMessageIDInvalid
	}
	type pinSide struct {
		userID int64
		peer   domain.Peer
		idx    int
	}
	sides := []pinSide{{userID: req.OwnerUserID, peer: req.Peer, idx: ownIdx}}
	if req.Peer.ID != req.OwnerUserID && (!req.Pinned || !req.PmOneside) {
		for i, msg := range s.m[req.Peer.ID] {
			if msg.UID == owned.UID {
				sides = append(sides, pinSide{
					userID: req.Peer.ID,
					peer:   domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID},
					idx:    i,
				})
				break
			}
		}
	}
	changes := make([]pinSide, 0, len(sides))
	var pinEvents []domain.UpdateEvent
	for _, side := range sides {
		if s.m[side.userID][side.idx].Pinned == req.Pinned {
			continue
		}
		changes = append(changes, side)
		pinEvents = append(pinEvents, domain.UpdateEvent{UserID: side.userID, Type: domain.UpdateEventPinnedMessages,
			PtsCount: 1, Date: req.Date, Peer: side.peer, Bool: req.Pinned, MessageIDs: []int{s.m[side.userID][side.idx].ID}})
	}
	if len(changes) == 0 {
		return res, nil
	}
	apply := func(events []domain.UpdateEvent) {
		for i, side := range changes {
			s.m[side.userID][side.idx].Pinned = req.Pinned
			res.Updated = append(res.Updated, domain.PinnedMessagesForUser{UserID: side.userID, Peer: side.peer,
				MessageIDs: events[i].MessageIDs, Pinned: req.Pinned, Event: events[i]})
		}
	}
	if req.Pinned && !req.PmOneside && req.Peer.ID != req.OwnerUserID {
		sendReq, err := store.PrivatePinServiceRequest(req)
		if err != nil {
			return res, err
		}
		fingerprint, err := store.PrivateSendFingerprint(sendReq)
		if err != nil {
			return res, err
		}
		sent, err := s.sendPrivateTextLocked(sendReq, fingerprint, &memoryPrivateSendPrefix{events: pinEvents, apply: apply})
		if errors.Is(err, domain.ErrReplyMessageIDInvalid) {
			err = domain.ErrMessageIDInvalid
		}
		if err != nil {
			return domain.PinPrivateMessageResult{OwnerUserID: req.OwnerUserID}, err
		}
		res.ServiceMessage = sent
		return res, nil
	}
	events := s.updateEvents
	if events == nil {
		return res, store.ErrDeliveryOutboxRequired
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	for i := range pinEvents {
		event := &pinEvents[i]
		event.Pts = s.nextPtsWithEventsLocked(events, event.UserID)
		auth, session := [8]byte{}, int64(0)
		if event.UserID == req.OwnerUserID {
			auth, session = req.OriginAuthKeyID, req.OriginSessionID
		}
		appendMemorySendEventLocked(events, event.UserID, *event, auth, session)
	}
	apply(pinEvents)
	return res, nil
}

// UnpinAllPrivateMessages has the same semantics as the PG implementation: it clears the
// whole batch on this side and, via UID, also clears the peer's shared pins.
func (s *MessageStore) UnpinAllPrivateMessages(_ context.Context, req domain.UnpinAllPrivateMessagesRequest) (domain.PinPrivateMessageResult, error) {
	res := domain.PinPrivateMessageResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 {
		return res, domain.ErrMessageIDInvalid
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.updateEvents
	if events == nil {
		return res, store.ErrDeliveryOutboxRequired
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	// Same semantics as PG: take one batch to clear in descending box_id, and clear the remaining batches with Offset=1.
	pinnedIdx := make([]int, 0)
	for i, msg := range s.m[req.OwnerUserID] {
		if msg.Peer == req.Peer && msg.Pinned {
			pinnedIdx = append(pinnedIdx, i)
		}
	}
	sort.Slice(pinnedIdx, func(a, b int) bool {
		return s.m[req.OwnerUserID][pinnedIdx[a]].ID > s.m[req.OwnerUserID][pinnedIdx[b]].ID
	})
	if len(pinnedIdx) > domain.MaxUnpinAllBatch {
		pinnedIdx = pinnedIdx[:domain.MaxUnpinAllBatch]
		res.Offset = 1
	}
	ownIDs := make([]int, 0, len(pinnedIdx))
	uids := make(map[int64]struct{})
	for _, i := range pinnedIdx {
		s.m[req.OwnerUserID][i].Pinned = false
		ownIDs = append(ownIDs, s.m[req.OwnerUserID][i].ID)
		uids[s.m[req.OwnerUserID][i].UID] = struct{}{}
	}
	if len(ownIDs) == 0 {
		return res, nil
	}
	appendSide := func(userID int64, peer domain.Peer, ids []int) {
		res.Updated = append(res.Updated, domain.PinnedMessagesForUser{
			UserID:     userID,
			Peer:       peer,
			MessageIDs: ids,
			Pinned:     false,
			Event: domain.UpdateEvent{
				UserID:     userID,
				Type:       domain.UpdateEventPinnedMessages,
				Pts:        s.nextPtsWithEventsLocked(events, userID),
				PtsCount:   1,
				Date:       req.Date,
				Peer:       peer,
				Bool:       false,
				MessageIDs: ids,
			},
		})
		event := res.Updated[len(res.Updated)-1].Event
		auth, session := [8]byte{}, int64(0)
		if userID == req.OwnerUserID {
			auth, session = req.OriginAuthKeyID, req.OriginSessionID
		}
		appendMemorySendEventLocked(events, userID, event, auth, session)
	}
	appendSide(req.OwnerUserID, req.Peer, ownIDs)
	if req.Peer.ID != req.OwnerUserID {
		peerIDs := make([]int, 0)
		for i, msg := range s.m[req.Peer.ID] {
			if _, ok := uids[msg.UID]; ok && msg.Pinned {
				s.m[req.Peer.ID][i].Pinned = false
				peerIDs = append(peerIDs, msg.ID)
			}
		}
		if len(peerIDs) > 0 {
			appendSide(req.Peer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID}, peerIDs)
		}
	}
	return res, nil
}
