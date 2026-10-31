package memory

import (
	"context"
	"sort"
	"strings"

	"telesrv/internal/domain"
)

// Shared media-tab read path (memory implementation): it scans in-memory messages
// directly and filters by domain.ClassifyMediaCategories, with no index table needed
// (small data volume). The category truth is shared with the postgres write path/backfill.

func mediaCategorySet(cats []domain.MediaCategory) map[domain.MediaCategory]bool {
	set := make(map[domain.MediaCategory]bool, len(cats))
	for _, c := range cats {
		if c != domain.MediaCategoryNone {
			set[c] = true
		}
	}
	return set
}

func mediaCategoryMatches(media *domain.MessageMedia, entities []domain.MessageEntity, set map[domain.MediaCategory]bool) bool {
	for _, c := range domain.ClassifyMediaCategories(media, entities) {
		if set[c] {
			return true
		}
	}
	return false
}

func mediaSearchCommonMatches(id, date int, body string, reply *domain.MessageReply, req domain.MediaSearchRequest) bool {
	if req.Query != "" && !strings.Contains(strings.ToLower(body), strings.ToLower(req.Query)) {
		return false
	}
	if req.MinDate > 0 && date <= req.MinDate {
		return false
	}
	if req.MaxDate > 0 && date >= req.MaxDate {
		return false
	}
	if req.TopMsgID != 0 && id != req.TopMsgID && (reply == nil || reply.TopMessageID != req.TopMsgID) {
		return false
	}
	return true
}

func savedMessageHasAnyTag(tags []domain.MessageReaction, wanted []domain.MessageReaction) bool {
	if len(wanted) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(tags))
	for _, reaction := range tags {
		have[reaction.Key()] = struct{}{}
	}
	for _, reaction := range wanted {
		if _, ok := have[reaction.Key()]; ok {
			return true
		}
	}
	return false
}

// pageMediaIDs paginates all matching ids newest-first (returns this page's ids plus the total satisfying max/min).
func pageMediaIDs(ids []int, req domain.MediaSearchRequest) ([]int, int) {
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	inRange := make([]int, 0, len(ids))
	for _, id := range ids {
		if req.MaxID != 0 && id >= req.MaxID {
			continue
		}
		if req.MinID != 0 && id <= req.MinID {
			continue
		}
		inRange = append(inRange, id)
	}
	count := len(inRange)

	limit := req.Limit
	if limit == 0 {
		return nil, count
	}
	if limit < 0 || limit > 100 {
		limit = 100
	}
	pivot := 0
	if req.OffsetID > 0 {
		pivot = sort.Search(len(inRange), func(i int) bool { return inRange[i] < req.OffsetID })
	}
	start := pivot + domain.ClampMessageHistoryAddOffset(req.AddOffset)
	end := start + limit
	start = min(max(start, 0), len(inRange))
	end = min(max(end, start), len(inRange))
	page := inRange[start:end]
	return page, count
}

// SearchPrivateMedia implements store.MessageStore.
func (s *MessageStore) SearchPrivateMedia(ctx context.Context, ownerUserID, peerID int64, req domain.MediaSearchRequest) (domain.MessageList, error) {
	set := mediaCategorySet(req.Categories)
	if ownerUserID == 0 || peerID == 0 || len(set) == 0 {
		return domain.MessageList{}, nil
	}
	s.mu.RLock()
	matched := make([]int, 0, len(s.m[ownerUserID]))
	for _, msg := range s.m[ownerUserID] {
		if msg.Deleted || msg.Peer.Type != domain.PeerTypeUser || msg.Peer.ID != peerID {
			continue
		}
		if req.SenderUserID != 0 && (msg.From.Type != domain.PeerTypeUser || msg.From.ID != req.SenderUserID) {
			continue
		}
		if !mediaSearchCommonMatches(msg.ID, msg.Date, msg.Body, msg.ReplyTo, req) {
			continue
		}
		if req.SavedPeer.ID != 0 && msg.SavedPeer != req.SavedPeer {
			continue
		}
		if !savedMessageHasAnyTag(s.savedMessageTags[ownerUserID][msg.ID], req.SavedReactions) {
			continue
		}
		if mediaCategoryMatches(msg.Media, msg.Entities, set) {
			matched = append(matched, msg.ID)
		}
	}
	s.mu.RUnlock()

	ids, count := pageMediaIDs(matched, req)
	if req.HasKnownCount {
		count = req.KnownCount
	}
	list, err := s.GetByIDs(ctx, ownerUserID, ids)
	if err != nil {
		return domain.MessageList{}, err
	}
	list.Count = count
	return list, nil
}

// CountPrivateMediaCategories implements store.MessageStore.
func (s *MessageStore) CountPrivateMediaCategories(_ context.Context, ownerUserID, peerID int64) (domain.MediaCategoryCounts, error) {
	if ownerUserID == 0 || peerID == 0 {
		return domain.MediaCategoryCounts{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := domain.MediaCategoryCounts{}
	for _, msg := range s.m[ownerUserID] {
		if msg.Deleted || msg.Peer.Type != domain.PeerTypeUser || msg.Peer.ID != peerID {
			continue
		}
		for _, category := range domain.ClassifyMediaCategories(msg.Media, msg.Entities) {
			if category != domain.MediaCategoryNone {
				out[category]++
			}
		}
	}
	return out, nil
}

// SearchChannelMedia implements store.ChannelStore.
func (s *ChannelStore) SearchChannelMedia(ctx context.Context, viewerUserID, channelID int64, req domain.MediaSearchRequest) (domain.ChannelHistory, error) {
	set := mediaCategorySet(req.Categories)
	if viewerUserID == 0 || channelID == 0 || len(set) == 0 {
		return domain.ChannelHistory{}, nil
	}
	s.mu.RLock()
	channel, member, err := s.channelAndMemberLocked(viewerUserID, channelID)
	if err != nil {
		s.mu.RUnlock()
		return domain.ChannelHistory{}, err
	}
	matched := make([]int, 0, len(s.messages[channelID]))
	for _, msg := range s.messages[channelID] {
		if msg.Deleted || msg.ID <= member.AvailableMinID {
			continue
		}
		if channel.Monoforum {
			if member.CanManageDirectMessages() && msg.SavedPeer.ID != 0 {
				continue
			}
			if !member.CanManageDirectMessages() && msg.SavedPeer != (domain.Peer{Type: domain.PeerTypeUser, ID: viewerUserID}) {
				continue
			}
		}
		if req.SenderUserID != 0 && msg.SenderUserID != req.SenderUserID {
			continue
		}
		if !mediaSearchCommonMatches(msg.ID, msg.Date, msg.Body, msg.ReplyTo, req) {
			continue
		}
		if mediaCategoryMatches(msg.Media, msg.Entities, set) {
			matched = append(matched, msg.ID)
		}
	}
	s.mu.RUnlock()

	ids, count := pageMediaIDs(matched, req)
	if req.HasKnownCount {
		count = req.KnownCount
	}
	hist, err := s.GetChannelMessages(ctx, viewerUserID, channelID, ids)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	hist.Messages = reorderChannelMessagesByMediaOrder(hist.Messages, ids)
	hist.Count = count
	return hist, nil
}

// CountChannelMediaCategories implements store.ChannelStore.
func (s *ChannelStore) CountChannelMediaCategories(_ context.Context, viewerUserID, channelID int64) (domain.MediaCategoryCounts, error) {
	if viewerUserID == 0 || channelID == 0 {
		return domain.MediaCategoryCounts{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(viewerUserID, channelID)
	if err != nil {
		return domain.MediaCategoryCounts{}, err
	}
	out := domain.MediaCategoryCounts{}
	for _, msg := range s.messages[channelID] {
		if msg.Deleted || msg.ID <= member.AvailableMinID {
			continue
		}
		if channel.Monoforum {
			if member.CanManageDirectMessages() && msg.SavedPeer.ID != 0 {
				continue
			}
			if !member.CanManageDirectMessages() && msg.SavedPeer != (domain.Peer{Type: domain.PeerTypeUser, ID: viewerUserID}) {
				continue
			}
		}
		for _, category := range domain.ClassifyMediaCategories(msg.Media, msg.Entities) {
			if category != domain.MediaCategoryNone {
				out[category]++
			}
		}
	}
	return out, nil
}

func reorderChannelMessagesByMediaOrder(msgs []domain.ChannelMessage, order []int) []domain.ChannelMessage {
	byID := make(map[int]domain.ChannelMessage, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
	}
	out := make([]domain.ChannelMessage, 0, len(order))
	for _, id := range order {
		if m, ok := byID[id]; ok {
			out = append(out, m)
		}
	}
	return out
}
