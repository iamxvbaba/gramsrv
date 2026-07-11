package memory

import (
	"context"

	"telesrv/internal/domain"
)

// memoryTopicRead 是单个 (channel,user,topic) 的已读水位。
type memoryTopicRead struct {
	ReadInbox  int
	ReadOutbox int
}

func topicReadFallbackInboxMem(topicID, availableMinID int) int {
	base := topicID - 1
	if availableMinID > base {
		base = availableMinID
	}
	if base < 0 {
		base = 0
	}
	return base
}

// channelMessageInTopicMem 判断消息是否属于某 topic，与 postgres channelTopicMessageCond 严格对齐：
// 普通 topic 仅 reply_to_top_id==topicID（不含 root 服务消息，其 reply_to_top_id=0）；
// General(=1) 归并 reply_to_top_id∈{0,1}。
func (s *ChannelStore) channelMessageInTopicLocked(channelID int64, msg domain.ChannelMessage, topicID int) bool {
	top := 0
	if msg.ReplyTo != nil {
		top = msg.ReplyTo.TopMessageID
	}
	if topicID == domain.ForumGeneralTopicID {
		if top != domain.ForumGeneralTopicID && top != 0 {
			return false
		}
		// 排除其它话题的根服务消息（reply_to_top_id=0 但 id 是某话题根，不属于 General）。
		if _, isRoot := s.topics[channelID][msg.ID]; isRoot {
			return false
		}
		return true
	}
	return top == topicID
}

func (s *ChannelStore) channelTopicReadInboxLocked(channelID, userID int64, topicID, availableMinID int) int {
	fb := topicReadFallbackInboxMem(topicID, availableMinID)
	if tr, ok := s.topicReads[channelID][userID][topicID]; ok && tr.ReadInbox > fb {
		return tr.ReadInbox
	}
	return fb
}

func (s *ChannelStore) channelTopicReadOutboxLocked(channelID, userID int64, topicID int) int {
	if tr, ok := s.topicReads[channelID][userID][topicID]; ok {
		return tr.ReadOutbox
	}
	return 0
}

func (s *ChannelStore) setTopicReadLocked(channelID, userID int64, topicID int, mutate func(*memoryTopicRead)) {
	if s.topicReads[channelID] == nil {
		s.topicReads[channelID] = make(map[int64]map[int]memoryTopicRead)
	}
	if s.topicReads[channelID][userID] == nil {
		s.topicReads[channelID][userID] = make(map[int]memoryTopicRead)
	}
	tr := s.topicReads[channelID][userID][topicID]
	mutate(&tr)
	s.topicReads[channelID][userID][topicID] = tr
}

func (s *ChannelStore) channelTopicTopMessageIDLocked(channelID int64, topicID, availableMinID int) int {
	maxID := 0
	for _, msg := range s.messages[channelID] {
		if msg.Deleted || msg.ID <= availableMinID {
			continue
		}
		if s.channelMessageInTopicLocked(channelID, msg, topicID) && msg.ID > maxID {
			maxID = msg.ID
		}
	}
	return maxID
}

// channelTopicUnreadCountLocked 现算某 topic 对 viewer 的未读消息数（per-topic 水位 readMaxID）。
// 与 postgres populateForumTopicUnreadCounts 同口径：reply_to_top_id==topicID、id>water、sender≠viewer。
func (s *ChannelStore) channelTopicUnreadCountLocked(viewerUserID, channelID int64, topicID, readMaxID int) int {
	unread := 0
	for _, msg := range s.messages[channelID] {
		if msg.Deleted || msg.ID <= readMaxID || msg.SenderUserID == viewerUserID {
			continue
		}
		if s.channelMessageInTopicLocked(channelID, msg, topicID) {
			unread++
			if unread >= domain.MaxDialogUnreadCount {
				break
			}
		}
	}
	return unread
}

// ReadChannelTopicHistory 推进 viewer 在 forum 单话题的 per-topic 已读水位（不碰频道级）。
func (s *ChannelStore) ReadChannelTopicHistory(_ context.Context, req domain.ReadChannelTopicHistoryRequest) (domain.ReadChannelTopicHistoryResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.TopicID <= 0 {
		return domain.ReadChannelTopicHistoryResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, member, err := s.channelAndMemberOrLinkedGuestLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReadChannelTopicHistoryResult{}, err
	}
	if !channel.Forum {
		return domain.ReadChannelTopicHistoryResult{}, domain.ErrChannelForumMissing
	}
	if member.Guest {
		return domain.ReadChannelTopicHistoryResult{}, domain.ErrChannelPrivate
	}
	topMax := s.channelTopicTopMessageIDLocked(req.ChannelID, req.TopicID, member.AvailableMinID)
	maxID := req.MaxID
	if maxID <= 0 || maxID > topMax {
		maxID = topMax
	}
	prev := s.channelTopicReadInboxLocked(req.ChannelID, req.UserID, req.TopicID, member.AvailableMinID)
	if maxID <= prev {
		return domain.ReadChannelTopicHistoryResult{Channel: cloneChannel(channel), TopicID: req.TopicID, MaxID: prev, Changed: false, Pts: channel.Pts}, nil
	}
	s.setTopicReadLocked(req.ChannelID, req.UserID, req.TopicID, func(tr *memoryTopicRead) {
		if maxID > tr.ReadInbox {
			tr.ReadInbox = maxID
		}
	})
	outbox := s.advanceTopicReadOutboxLocked(req.ChannelID, req.UserID, req.TopicID, prev, maxID)
	return domain.ReadChannelTopicHistoryResult{Channel: cloneChannel(channel), TopicID: req.TopicID, MaxID: maxID, Changed: true, Pts: channel.Pts, OutboxUpdates: outbox}, nil
}

func (s *ChannelStore) advanceTopicReadOutboxLocked(channelID, readerUserID int64, topicID, prev, maxID int) []domain.ChannelReadOutboxUpdate {
	senders := make(map[int64]struct{})
	order := make([]int64, 0, 8)
	for _, msg := range s.messages[channelID] {
		if msg.Deleted || msg.ID <= prev || msg.ID > maxID {
			continue
		}
		if msg.SenderUserID == 0 || msg.SenderUserID == readerUserID {
			continue
		}
		if !s.channelMessageInTopicLocked(channelID, msg, topicID) {
			continue
		}
		if _, ok := senders[msg.SenderUserID]; ok {
			continue
		}
		senders[msg.SenderUserID] = struct{}{}
		order = append(order, msg.SenderUserID)
	}
	if len(order) == 0 {
		return nil
	}
	updates := make([]domain.ChannelReadOutboxUpdate, 0, len(order))
	for _, sender := range order {
		s.setTopicReadLocked(channelID, sender, topicID, func(tr *memoryTopicRead) {
			if maxID > tr.ReadOutbox {
				tr.ReadOutbox = maxID
			}
		})
		updates = append(updates, domain.ChannelReadOutboxUpdate{UserID: sender, MaxID: maxID})
	}
	return updates
}

// GeneralForumTopic 现算 General 话题（id=1）对 viewer 的状态（per-topic 水位，排除其它话题根消息）。
func (s *ChannelStore) GeneralForumTopic(_ context.Context, viewerUserID, channelID int64) (domain.ChannelForumTopic, error) {
	if viewerUserID == 0 || channelID == 0 {
		return domain.ChannelForumTopic{}, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(viewerUserID, channelID)
	if err != nil {
		return domain.ChannelForumTopic{}, err
	}
	if !channel.Forum {
		return domain.ChannelForumTopic{}, domain.ErrChannelForumMissing
	}
	gid := domain.ForumGeneralTopicID
	water := s.channelTopicReadInboxLocked(channelID, viewerUserID, gid, member.AvailableMinID)
	return domain.ChannelForumTopic{
		ChannelID:            channelID,
		TopicID:              gid,
		Title:                "General",
		CreatorUserID:        channel.CreatorUserID,
		Date:                 channel.Date,
		TopMessageID:         s.channelTopicTopMessageIDLocked(channelID, gid, member.AvailableMinID),
		ReadInboxMaxID:       water,
		ReadOutboxMaxID:      s.channelTopicReadOutboxLocked(channelID, viewerUserID, gid),
		UnreadCount:          s.channelTopicUnreadCountLocked(viewerUserID, channelID, gid, water),
		UnreadMentionsCount:  s.countChannelUnreadMentionsLocked(viewerUserID, channelID, gid),
		UnreadReactionsCount: s.countChannelUnreadReactionsLocked(viewerUserID, channelID, gid),
	}, nil
}
