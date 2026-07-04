package memory

import (
	"context"
	"sync"
	"telesrv/internal/domain"
)

// UpdateStateStore 是 store.UpdateStateStore 的内存实现。
type UpdateStateStore struct {
	mu     sync.RWMutex
	states map[updateStateKey]domain.UpdateState
}

// UpdateEventStore 是 store.UpdateEventStore 的内存实现。
type UpdateEventStore struct {
	mu     sync.RWMutex
	events map[int64][]domain.UpdateEvent
}

type updateStateKey struct {
	authKeyID [8]byte
	userID    int64
}

// NewUpdateEventStore 创建内存 UpdateEventStore。
func NewUpdateEventStore() *UpdateEventStore {
	return &UpdateEventStore{events: make(map[int64][]domain.UpdateEvent)}
}

func (s *UpdateEventStore) Append(_ context.Context, userID int64, event domain.UpdateEvent) error {
	_, err := s.append(userID, event, false)
	return err
}

func (s *UpdateEventStore) AppendAllocated(_ context.Context, userID int64, event domain.UpdateEvent) (domain.UpdateEvent, error) {
	return s.append(userID, event, true)
}

func (s *UpdateEventStore) AppendAllocatedWithDispatch(_ context.Context, userID int64, event domain.UpdateEvent, _ [8]byte, _ int64) (domain.UpdateEvent, error) {
	return s.append(userID, event, true)
}

func (s *UpdateEventStore) append(userID int64, event domain.UpdateEvent, allocate bool) (domain.UpdateEvent, error) {
	if event.PtsCount <= 0 {
		event.PtsCount = 1
	}
	event.UserID = userID
	event = cloneUpdateEvent(event)
	s.mu.Lock()
	if allocate {
		current := 0
		for _, item := range s.events[userID] {
			if item.Pts > current {
				current = item.Pts
			}
		}
		event.Pts = current + event.PtsCount
	}
	s.events[userID] = append(s.events[userID], event)
	s.mu.Unlock()
	return event, nil
}

func (s *UpdateEventStore) ListAfter(_ context.Context, userID int64, pts, limit int) ([]domain.UpdateEvent, error) {
	s.mu.RLock()
	items := append([]domain.UpdateEvent(nil), s.events[userID]...)
	s.mu.RUnlock()
	out := make([]domain.UpdateEvent, 0, len(items))
	for _, event := range items {
		if event.Pts <= pts {
			continue
		}
		out = append(out, cloneUpdateEvent(event))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *UpdateEventStore) FindNewMessageEvent(_ context.Context, userID int64, messageBoxID int) (domain.UpdateEvent, bool, error) {
	if userID == 0 || messageBoxID <= 0 {
		return domain.UpdateEvent{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, event := range s.events[userID] {
		if event.Type == domain.UpdateEventNewMessage && event.Message.ID == messageBoxID {
			return cloneUpdateEvent(event), true, nil
		}
	}
	return domain.UpdateEvent{}, false, nil
}

func cloneUpdateEvent(event domain.UpdateEvent) domain.UpdateEvent {
	event.Message = cloneMessage(event.Message)
	event.Story = cloneUpdateStory(event.Story)
	event.MessageIDs = append([]int(nil), event.MessageIDs...)
	event.Peers = append([]domain.Peer(nil), event.Peers...)
	event.Users = append([]domain.User(nil), event.Users...)
	event.Channels = append([]domain.Channel(nil), event.Channels...)
	event.Reaction = cloneUpdateReaction(event.Reaction)
	event.QuickReplies = cloneUpdateQuickReplies(event.QuickReplies)
	event.QuickReplyMessage = cloneUpdateQuickReplyMessage(event.QuickReplyMessage)
	return event
}

func cloneUpdateStory(story domain.Story) domain.Story {
	story.Entities = append([]domain.MessageEntity(nil), story.Entities...)
	story.Views.Reactions = append([]domain.ChannelMessageReactionCount(nil), story.Views.Reactions...)
	story.Views.RecentViewers = append([]int64(nil), story.Views.RecentViewers...)
	story.SentReaction = cloneUpdateReaction(story.SentReaction)
	return story
}

func cloneUpdateReaction(in *domain.MessageReaction) *domain.MessageReaction {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneUpdateQuickReplies(in []domain.QuickReply) []domain.QuickReply {
	return append([]domain.QuickReply(nil), in...)
}

func cloneUpdateQuickReplyMessage(in domain.QuickReplyMessage) domain.QuickReplyMessage {
	out := in
	out.Entities = append([]domain.MessageEntity(nil), in.Entities...)
	return out
}

// MaxContiguousPts 返回从 1 起无空洞的最大 pts（内存版按 pts_count 连续扫描）。
func (s *UpdateEventStore) MaxContiguousPts(_ context.Context, userID int64) (int, error) {
	s.mu.RLock()
	nextByStart := make(map[int]int, len(s.events[userID]))
	for _, event := range s.events[userID] {
		count := event.PtsCount
		if count <= 0 {
			count = 1
		}
		nextByStart[event.Pts-count] = event.Pts
	}
	s.mu.RUnlock()
	contiguous := 0
	for {
		next, ok := nextByStart[contiguous]
		if !ok {
			break
		}
		contiguous = next
	}
	return contiguous, nil
}

// NewUpdateStateStore 创建内存 UpdateStateStore。
func NewUpdateStateStore() *UpdateStateStore {
	return &UpdateStateStore{states: make(map[updateStateKey]domain.UpdateState)}
}

func (s *UpdateStateStore) Get(_ context.Context, id [8]byte, userID int64) (domain.UpdateState, bool, error) {
	s.mu.RLock()
	st, ok := s.states[updateStateKey{authKeyID: id, userID: userID}]
	s.mu.RUnlock()
	return st, ok, nil
}

func (s *UpdateStateStore) Save(_ context.Context, id [8]byte, userID int64, st domain.UpdateState) error {
	s.mu.Lock()
	key := updateStateKey{authKeyID: id, userID: userID}
	// 确认水位只增不减（与 PG 的 GREATEST upsert 对齐）：一个旧 from（较小 pts）
	// 的 getDifference 不得把设备已确认水位回退，否则 getPeerDialogs.state 会下发
	// 倒退的 pts 基线。
	prev := s.states[key]
	if st.Pts < prev.Pts {
		st.Pts = prev.Pts
	}
	if st.Qts < prev.Qts {
		st.Qts = prev.Qts
	}
	if st.Date < prev.Date {
		st.Date = prev.Date
	}
	if st.Seq < prev.Seq {
		st.Seq = prev.Seq
	}
	s.states[key] = st
	s.mu.Unlock()
	return nil
}

func (s *UpdateStateStore) Delete(_ context.Context, id [8]byte, userID int64) error {
	s.mu.Lock()
	delete(s.states, updateStateKey{authKeyID: id, userID: userID})
	s.mu.Unlock()
	return nil
}

func (s *UpdateStateStore) DeleteAuthKey(_ context.Context, id [8]byte) error {
	s.mu.Lock()
	for k := range s.states {
		if k.authKeyID == id {
			delete(s.states, k)
		}
	}
	s.mu.Unlock()
	return nil
}
