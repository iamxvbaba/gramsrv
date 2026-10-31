package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"telesrv/internal/domain"
)

func (s *MessageStore) GetByIDs(_ context.Context, userID int64, ids []int) (domain.MessageList, error) {
	if userID == 0 || len(ids) == 0 {
		return domain.MessageList{}, nil
	}
	s.mu.RLock()
	byID := make(map[int]domain.Message, len(s.m[userID]))
	for _, msg := range s.m[userID] {
		item := cloneMessage(msg)
		reactions := s.privateMessageReactionsForMessageLocked(item)
		if len(reactions.Results) > 0 || len(reactions.Recent) > 0 {
			item.Reactions = cloneChannelMessageReactionsPtr(&reactions)
		}
		byID[msg.ID] = item
	}
	s.mu.RUnlock()
	out := domain.MessageList{Messages: make([]domain.Message, 0, len(ids))}
	for _, id := range ids {
		if msg, ok := byID[id]; ok {
			out.Messages = append(out.Messages, msg)
		}
	}
	s.enrichPrivateMessagePolls(out.Messages, int(time.Now().Unix()))
	out.Users = usersForMessages(out.Messages)
	out.Hash = messageListHash(out.Messages)
	return out, nil
}

// GetByUID resolves one owner's box row by the shared private message id. Callback delivery
// uses it to translate the clicker's box id to the bot's box id without scanning history.
func (s *MessageStore) GetByUID(_ context.Context, userID, uid int64) (domain.Message, bool, error) {
	if userID == 0 || uid == 0 {
		return domain.Message{}, false, nil
	}
	s.mu.RLock()
	var found domain.Message
	for _, msg := range s.m[userID] {
		if msg.UID == uid {
			found = cloneMessage(msg)
			reactions := s.privateMessageReactionsForMessageLocked(found)
			if len(reactions.Results) > 0 || len(reactions.Recent) > 0 {
				found.Reactions = cloneChannelMessageReactionsPtr(&reactions)
			}
			break
		}
	}
	s.mu.RUnlock()
	if found.ID == 0 {
		return domain.Message{}, false, nil
	}
	items := []domain.Message{found}
	s.enrichPrivateMessagePolls(items, int(time.Now().Unix()))
	return items[0], true, nil
}

func (s *MessageStore) ListByUser(_ context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error) {
	s.mu.RLock()
	messages := cloneMessages(s.m[userID])
	for i := range messages {
		reactions := s.privateMessageReactionsForMessageLocked(messages[i])
		if len(reactions.Results) > 0 || len(reactions.Recent) > 0 {
			messages[i].Reactions = cloneChannelMessageReactionsPtr(&reactions)
		}
	}
	s.mu.RUnlock()
	s.enrichPrivateMessagePolls(messages, int(time.Now().Unix()))
	return filterMessageList(messages, filter), nil
}

func (s *MessageStore) ReadHistory(_ context.Context, req domain.ReadHistoryRequest) (domain.ReadHistoryResult, error) {
	res := domain.ReadHistoryResult{OwnerUserID: req.OwnerUserID, Peer: req.Peer, MaxID: req.MaxID}
	if req.OwnerUserID == 0 || req.Peer.ID == 0 {
		return res, nil
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dialogs == nil {
		return res, nil
	}
	s.dialogs.mu.Lock()
	defer s.dialogs.mu.Unlock()
	list := s.dialogs.m[req.OwnerUserID]
	for i, dialog := range list.Dialogs {
		if dialog.Peer != req.Peer {
			continue
		}
		readMax := req.MaxID
		if readMax <= 0 {
			readMax = dialog.TopMessage
		}
		if readMax > dialog.TopMessage {
			readMax = dialog.TopMessage
		}
		oldRead := dialog.ReadInboxMaxID
		res.MaxID = readMax
		advancesRead := readMax > oldRead
		if !advancesRead {
			if dialog.UnreadCount > 0 {
				unread := 0
				for _, msg := range s.m[req.OwnerUserID] {
					if msg.Peer == req.Peer && !msg.Out && msg.ID > oldRead {
						unread++
					}
				}
				dialog.UnreadCount = unread
				dialog.UnreadMentions = 0
				// readHistory does not clear the reaction badge (matching PG; unread
				// reactions are cleared separately by readReactions/readMessageContents,
				// otherwise the badge count and the getUnreadReactions jump list would disagree).
				dialog.UnreadMark = false
				res.MaxID = dialog.ReadInboxMaxID
				res.StillUnreadCount = unread
				list.Dialogs[i] = dialog
				s.dialogs.m[req.OwnerUserID] = list
			}
			return res, nil
		}
		res.Changed = true
		var latestIncoming domain.Message
		unread := 0
		for _, msg := range s.m[req.OwnerUserID] {
			if msg.Peer != req.Peer || msg.Out {
				continue
			}
			if msg.ID > readMax {
				unread++
				continue
			}
			if msg.ID > oldRead && msg.ID > latestIncoming.ID {
				latestIncoming = msg
			}
		}
		if readMax > dialog.ReadInboxMaxID {
			dialog.ReadInboxMaxID = readMax
		}
		dialog.UnreadCount = unread
		dialog.UnreadMentions = 0
		// readHistory does not clear the reaction badge (matching PG; see above).
		dialog.UnreadMark = false
		res.StillUnreadCount = unread
		pts := s.nextPtsLocked(req.OwnerUserID)
		res.InboxEvent = domain.UpdateEvent{
			UserID:           req.OwnerUserID,
			Type:             domain.UpdateEventReadHistoryInbox,
			Pts:              pts,
			PtsCount:         1,
			Date:             req.Date,
			Peer:             req.Peer,
			MaxID:            readMax,
			StillUnreadCount: unread,
		}
		list.Dialogs[i] = dialog
		s.dialogs.m[req.OwnerUserID] = list

		if latestIncoming.ID != 0 && latestIncoming.From.ID != 0 && latestIncoming.From.ID != req.OwnerUserID {
			senderUserID := latestIncoming.From.ID
			senderBoxID := 0
			for _, msg := range s.m[senderUserID] {
				if msg.UID == latestIncoming.UID && msg.Out {
					senderBoxID = msg.ID
					break
				}
			}
			if senderBoxID > 0 {
				senderList := s.dialogs.m[senderUserID]
				for j, senderDialog := range senderList.Dialogs {
					if senderDialog.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID}) {
						continue
					}
					if senderBoxID <= senderDialog.ReadOutboxMaxID {
						break
					}
					oldOutbox := senderDialog.ReadOutboxMaxID
					senderDialog.ReadOutboxMaxID = senderBoxID
					senderList.Dialogs[j] = senderDialog
					s.dialogs.m[senderUserID] = senderList
					for _, msg := range s.m[senderUserID] {
						if msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID}) && msg.Out && msg.ID > oldOutbox && msg.ID <= senderBoxID {
							s.readOutboxDates[readOutboxDateKey{ownerUserID: senderUserID, peerID: req.OwnerUserID, msgID: msg.ID}] = req.Date
						}
					}
					outPts := s.nextPtsLocked(senderUserID)
					res.OutboxChanged = true
					res.OutboxUserID = senderUserID
					res.OutboxEvent = domain.UpdateEvent{
						UserID:   senderUserID,
						Type:     domain.UpdateEventReadHistoryOutbox,
						Pts:      outPts,
						PtsCount: 1,
						Date:     req.Date,
						Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID},
						MaxID:    senderBoxID,
					}
					break
				}
			}
		}
		return res, nil
	}
	return res, nil
}

func (s *MessageStore) DeleteHistory(_ context.Context, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error) {
	res := domain.DeleteMessagesResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 || req.Peer.ID == 0 {
		return res, nil
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inDateRange := func(msg domain.Message) bool {
		if req.MinDate > 0 && msg.Date < req.MinDate {
			return false
		}
		if req.MaxDate > 0 && msg.Date > req.MaxDate {
			return false
		}
		return true
	}
	var anchors map[int64]memoryHistoryClearAnchor
	fullJustClear := req.JustClear && req.MaxID <= 0 && req.MinDate <= 0 && req.MaxDate <= 0
	if fullJustClear {
		anchors = make(map[int64]memoryHistoryClearAnchor, 2)
		if anchor, found := s.memoryHistoryClearAnchorLocked(req.OwnerUserID, req.Peer); found {
			anchors[req.OwnerUserID] = anchor
		}
		if req.Revoke && req.Peer.ID != req.OwnerUserID {
			peer := domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID}
			if anchor, found := s.memoryHistoryClearAnchorLocked(req.Peer.ID, peer); found {
				anchors[req.Peer.ID] = anchor
			}
		}
	}
	deleted, revokeUIDs, more := s.deleteMemoryMessagesLocked(req.OwnerUserID, domain.MaxDeleteHistoryBatch, func(msg domain.Message) bool {
		if anchor, ok := anchors[req.OwnerUserID]; ok && msg.ID == anchor.message.ID {
			return false
		}
		return msg.Peer == req.Peer && (req.MaxID <= 0 || msg.ID <= req.MaxID) && inDateRange(msg)
	})
	if req.Revoke {
		if req.MaxID > 0 && len(revokeUIDs) > 0 {
			deleted = append(deleted, s.deleteMemoryMessagesByUIDLocked(revokeUIDs, req.OwnerUserID)...)
		}
		// Same semantics as PG: a full/date-range two-way history clear scans the peer's
		// leftovers directly, so messages we already deleted one-way must not linger on the peer.
		if req.MaxID <= 0 && req.Peer.ID != req.OwnerUserID {
			peerDeleted, _, peerMore := s.deleteMemoryMessagesLocked(req.Peer.ID, domain.MaxDeleteHistoryBatch, func(msg domain.Message) bool {
				if anchor, ok := anchors[req.Peer.ID]; ok && msg.ID == anchor.message.ID {
					return false
				}
				return msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID}) && inDateRange(msg)
			})
			deleted = append(deleted, peerDeleted...)
			more = more || peerMore
		}
	}
	res = s.finishMemoryDeleteLocked(res, deleted, req.Date, anchors)
	if more {
		res.Offset = 1
	}
	return res, nil
}

func (s *MessageStore) memoryHistoryClearAnchorLocked(userID int64, peer domain.Peer) (memoryHistoryClearAnchor, bool) {
	var top domain.Message
	for _, msg := range s.m[userID] {
		if msg.Peer == peer && msg.ID > top.ID {
			top = msg
		}
	}
	if top.ID == 0 {
		return memoryHistoryClearAnchor{}, false
	}
	return memoryHistoryClearAnchor{
		message:      cloneMessage(top),
		materialized: domain.IsHistoryClearServiceMessage(top),
	}, true
}

func filterMessageList(messages []domain.Message, filter domain.MessageFilter) domain.MessageList {
	filter.AddOffset = domain.ClampMessageHistoryAddOffset(filter.AddOffset)
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].ID > messages[j].ID
	})

	query := strings.ToLower(filter.Query)
	peerIDs := make(map[int64]struct{}, len(filter.PeerIDs))
	for _, id := range filter.PeerIDs {
		peerIDs[id] = struct{}{}
	}
	base := make([]domain.Message, 0, len(messages))
	for _, msg := range messages {
		if filter.HasPeer && msg.Peer != filter.Peer {
			continue
		}
		if filter.SenderUserID != 0 && msg.From != (domain.Peer{Type: domain.PeerTypeUser, ID: filter.SenderUserID}) {
			continue
		}
		if filter.RestrictPeerIDs {
			if msg.Peer.Type != domain.PeerTypeUser {
				continue
			}
			if _, ok := peerIDs[msg.Peer.ID]; !ok {
				continue
			}
		}
		if query != "" && !strings.Contains(strings.ToLower(msg.Body), query) {
			continue
		}
		if filter.MinDate > 0 && msg.Date <= filter.MinDate {
			continue
		}
		if filter.MaxDate > 0 && msg.Date >= filter.MaxDate {
			continue
		}
		if filter.MaxID > 0 && msg.ID >= filter.MaxID {
			continue
		}
		if filter.MinID > 0 && msg.ID <= filter.MinID {
			continue
		}
		if filter.PinnedOnly && !msg.Pinned {
			continue
		}
		if filter.MusicOnly && !msg.Media.IsMusic() {
			continue
		}
		if filter.SavedPeer.ID != 0 && msg.SavedPeer != filter.SavedPeer {
			continue
		}
		if len(filter.SavedReactions) > 0 && !messageHasAnySavedTag(msg, filter.SavedReactions) {
			continue
		}
		base = append(base, msg)
	}

	if filter.CountOnly {
		return domain.MessageList{Count: len(base)}
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	page := pageMessageHistory(base, filter, limit)
	return domain.MessageList{
		Messages: page,
		Users:    usersForMessages(page),
		Count:    len(base),
		Hash:     messageListHash(base),
	}
}

func messageHasAnySavedTag(msg domain.Message, wanted []domain.MessageReaction) bool {
	if msg.Reactions == nil || !msg.Reactions.AsTags {
		return false
	}
	have := make(map[string]struct{}, len(msg.Reactions.Results))
	for _, result := range msg.Reactions.Results {
		have[result.Reaction.Key()] = struct{}{}
	}
	for _, reaction := range wanted {
		if _, ok := have[reaction.Key()]; ok {
			return true
		}
	}
	return false
}

func pageMessageHistory(base []domain.Message, filter domain.MessageFilter, limit int) []domain.Message {
	if limit <= 0 || len(base) == 0 {
		return nil
	}
	switch messageHistoryLoadType(filter.AddOffset, limit) {
	case messageHistoryLoadForward:
		return cloneMessages(forwardMessageHistory(base, filter, limit, -filter.AddOffset-limit))
	case messageHistoryLoadAround:
		forwardLimit := -filter.AddOffset
		if forwardLimit > limit {
			forwardLimit = limit
		}
		backwardLimit := limit + filter.AddOffset
		if backwardLimit < 0 {
			backwardLimit = 0
		}
		page := make([]domain.Message, 0, limit)
		page = append(page, forwardMessageHistory(base, filter, forwardLimit, 0)...)
		page = append(page, backwardMessageHistory(base, filter, backwardLimit)...)
		sort.SliceStable(page, func(i, j int) bool {
			return page[i].ID > page[j].ID
		})
		return cloneMessages(page)
	default:
		start := filter.AddOffset
		if start < 0 {
			start = 0
		}
		candidates := backwardMessageHistory(base, filter, limit+start)
		if start >= len(candidates) {
			return nil
		}
		return cloneMessages(candidates[start:])
	}
}

type messageHistoryLoad int

const (
	messageHistoryLoadBackward messageHistoryLoad = iota
	messageHistoryLoadForward
	messageHistoryLoadAround
)

func messageHistoryLoadType(addOffset, limit int) messageHistoryLoad {
	if addOffset >= 0 {
		return messageHistoryLoadBackward
	}
	if addOffset+limit > 0 {
		return messageHistoryLoadAround
	}
	return messageHistoryLoadForward
}

func backwardMessageHistory(base []domain.Message, filter domain.MessageFilter, limit int) []domain.Message {
	if limit <= 0 {
		return nil
	}
	out := make([]domain.Message, 0, limit)
	for _, msg := range base {
		if !messageBeforeHistoryOffset(msg, filter) {
			continue
		}
		out = append(out, msg)
		if len(out) == limit {
			break
		}
	}
	return out
}

func forwardMessageHistory(base []domain.Message, filter domain.MessageFilter, limit, skip int) []domain.Message {
	if limit <= 0 {
		return nil
	}
	out := make([]domain.Message, 0, limit)
	for i := len(base) - 1; i >= 0; i-- {
		msg := base[i]
		if !messageAfterHistoryOffset(msg, filter) {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		out = append(out, msg)
		if len(out) == limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ID > out[j].ID
	})
	return out
}

func messageBeforeHistoryOffset(msg domain.Message, filter domain.MessageFilter) bool {
	if filter.OffsetDate > 0 {
		return msg.Date < filter.OffsetDate
	}
	if filter.OffsetID <= 0 {
		return true
	}
	return msg.ID < filter.OffsetID
}

func messageAfterHistoryOffset(msg domain.Message, filter domain.MessageFilter) bool {
	if filter.OffsetDate > 0 {
		return msg.Date >= filter.OffsetDate
	}
	if filter.OffsetID <= 0 {
		return false
	}
	return msg.ID >= filter.OffsetID
}

func messageLess(a, b domain.Message) bool {
	if a.Date != b.Date {
		return a.Date > b.Date
	}
	return a.ID > b.ID
}

func usersForMessages(messages []domain.Message) []domain.User {
	seen := map[int64]struct{}{}
	users := make([]domain.User, 0, 1)
	for _, msg := range messages {
		for _, peer := range []domain.Peer{msg.Peer, msg.From} {
			if peer.Type != domain.PeerTypeUser {
				continue
			}
			if _, ok := seen[peer.ID]; ok {
				continue
			}
			seen[peer.ID] = struct{}{}
			if u, ok := domain.SystemUserByID(peer.ID); ok {
				users = append(users, u)
			}
		}
	}
	return users
}
