package memory

import (
	"context"
	"fmt"
	"sort"
	"telesrv/internal/domain"
	"time"
)

func (s *MessageStore) DeleteMessages(_ context.Context, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error) {
	res := domain.DeleteMessagesResult{OwnerUserID: req.OwnerUserID}
	ids := normalizeMemoryMessageIDs(req.IDs)
	if req.OwnerUserID == 0 || len(ids) == 0 {
		return res, nil
	}
	if len(ids) > domain.MaxDeleteMessageIDs {
		return res, fmt.Errorf("delete messages: too many ids: %d > %d", len(ids), domain.MaxDeleteMessageIDs)
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	idSet := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted, revokeUIDs, _ := s.deleteMemoryMessagesLocked(req.OwnerUserID, 0, func(msg domain.Message) bool {
		_, ok := idSet[msg.ID]
		return ok
	})
	if req.Revoke && len(revokeUIDs) > 0 {
		deleted = append(deleted, s.deleteMemoryMessagesByUIDLocked(revokeUIDs, req.OwnerUserID)...)
	}
	return s.finishMemoryDeleteLocked(res, deleted, req.Date, false), nil
}

type deletedMemoryMessage struct {
	userID           int64
	peer             domain.Peer
	id               int
	privateMessageID int64
	messageSenderID  int64
	randomID         int64
}

func (s *MessageStore) finishMemoryDeleteLocked(res domain.DeleteMessagesResult, deleted []deletedMemoryMessage, date int, preserveEmptyDialogs bool) domain.DeleteMessagesResult {
	if len(deleted) == 0 {
		return res
	}
	idsByOwner := make(map[int64][]int)
	peersByOwner := make(map[int64]map[domain.Peer]struct{})
	for _, row := range deleted {
		idsByOwner[row.userID] = append(idsByOwner[row.userID], row.id)
		if peersByOwner[row.userID] == nil {
			peersByOwner[row.userID] = make(map[domain.Peer]struct{})
		}
		peersByOwner[row.userID][row.peer] = struct{}{}
	}
	if s.dialogs != nil {
		s.dialogs.mu.Lock()
		for userID, peers := range peersByOwner {
			for peer := range peers {
				s.rebuildMemoryDialogLocked(userID, peer, preserveEmptyDialogs)
			}
		}
		s.dialogs.mu.Unlock()
	}
	ownerIDs := make([]int64, 0, len(idsByOwner))
	for userID := range idsByOwner {
		ownerIDs = append(ownerIDs, userID)
	}
	sort.Slice(ownerIDs, func(i, j int) bool { return ownerIDs[i] < ownerIDs[j] })
	for _, userID := range ownerIDs {
		ids := normalizeMemoryMessageIDs(idsByOwner[userID])
		if len(ids) == 0 {
			continue
		}
		pts := s.nextPtsNLocked(userID, len(ids))
		event := domain.UpdateEvent{
			UserID:     userID,
			Type:       domain.UpdateEventDeleteMessages,
			Pts:        pts,
			PtsCount:   len(ids),
			Date:       date,
			MessageIDs: ids,
		}
		for _, row := range deleted {
			if row.userID != userID || row.messageSenderID != userID || row.randomID == 0 || row.privateMessageID == 0 {
				continue
			}
			key := privateSendDedupKey{senderUserID: userID, randomID: row.randomID}
			record, ok := s.privateSendDedup[key]
			if !ok {
				continue
			}
			cloned := cloneUpdateEvent(event)
			record.senderDeleteEvent = &cloned
			s.privateSendDedup[key] = record
		}
		res.Deleted = append(res.Deleted, domain.DeletedMessagesForUser{
			UserID:     userID,
			MessageIDs: ids,
			Event:      event,
		})
	}
	return res
}

func (s *MessageStore) rebuildMemoryDialogLocked(userID int64, peer domain.Peer, preserveEmpty bool) {
	list := s.dialogs.m[userID]
	topID := 0
	topDate := 0
	unread := 0
	for _, msg := range s.m[userID] {
		if msg.Peer != peer {
			continue
		}
		if msg.ID > topID {
			topID = msg.ID
			topDate = msg.Date
		}
	}
	dialogs := list.Dialogs[:0]
	for _, dialog := range list.Dialogs {
		if dialog.Peer != peer {
			dialogs = append(dialogs, dialog)
			continue
		}
		if topID == 0 {
			if preserveEmpty {
				oldTop := dialog.TopMessage
				dialog.TopMessage = 0
				dialog.TopMessageDate = 0
				if dialog.ReadInboxMaxID < oldTop {
					dialog.ReadInboxMaxID = oldTop
				}
				if dialog.ReadOutboxMaxID < oldTop {
					dialog.ReadOutboxMaxID = oldTop
				}
				dialog.UnreadCount = 0
				dialog.UnreadMark = false
				dialog.UnreadMentions = 0
				dialog.UnreadReactions = 0
				dialogs = append(dialogs, dialog)
			}
			continue
		}
		for _, msg := range s.m[userID] {
			if msg.Peer == peer && !msg.Out && msg.ID > dialog.ReadInboxMaxID {
				unread++
			}
		}
		dialog.TopMessage = topID
		dialog.TopMessageDate = topDate
		dialog.UnreadCount = unread
		dialog.UnreadMentions = 0
		dialog.UnreadReactions = 0
		dialogs = append(dialogs, dialog)
	}
	list.Dialogs = dialogs
	list.Messages = cloneMessages(s.m[userID])
	s.dialogs.m[userID] = list
}
