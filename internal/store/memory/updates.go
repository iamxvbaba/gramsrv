package memory

import (
	"context"
	"fmt"
	"sync"
	"telesrv/internal/domain"
)

// UpdateStateStore is the in-memory implementation of store.UpdateStateStore.
type UpdateStateStore struct {
	mu       sync.RWMutex
	states   map[updateStateKey]domain.UpdateState
	observed map[updateStateKey]domain.UpdateState
}

// UpdateEventStore is the in-memory implementation of store.UpdateEventStore.
type UpdateEventStore struct {
	mu         sync.RWMutex
	events     map[int64][]domain.UpdateEvent
	dispatches map[int64][]memoryUpdateDispatch
}

type memoryUpdateDispatch struct {
	Pts              int
	ExcludeAuthKeyID [8]byte
	ExcludeSessionID int64
}

type updateStateKey struct {
	authKeyID [8]byte
	userID    int64
}

// NewUpdateEventStore creates an in-memory UpdateEventStore.
func NewUpdateEventStore() *UpdateEventStore {
	return &UpdateEventStore{
		events:     make(map[int64][]domain.UpdateEvent),
		dispatches: make(map[int64][]memoryUpdateDispatch),
	}
}

func (s *UpdateEventStore) Append(_ context.Context, userID int64, event domain.UpdateEvent) error {
	_, err := s.append(userID, event, false, false, [8]byte{}, 0)
	return err
}

func (s *UpdateEventStore) AppendAllocated(_ context.Context, userID int64, event domain.UpdateEvent) (domain.UpdateEvent, error) {
	return s.append(userID, event, true, false, [8]byte{}, 0)
}

func (s *UpdateEventStore) AppendAllocatedWithDispatch(_ context.Context, userID int64, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, error) {
	return s.append(userID, event, true, true, excludeAuthKeyID, excludeSessionID)
}

func (s *UpdateEventStore) append(userID int64, event domain.UpdateEvent, allocate, withDispatch bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, error) {
	if withDispatch && (excludeAuthKeyID == ([8]byte{})) != (excludeSessionID == 0) {
		return domain.UpdateEvent{}, fmt.Errorf("update dispatch exclusion requires both raw auth key and session id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event = s.appendLocked(userID, event, allocate)
	if withDispatch {
		s.dispatches[userID] = append(s.dispatches[userID], memoryUpdateDispatch{
			Pts: event.Pts, ExcludeAuthKeyID: excludeAuthKeyID, ExcludeSessionID: excludeSessionID,
		})
	}
	return event, nil
}

func (s *UpdateEventStore) appendLocked(userID int64, event domain.UpdateEvent, allocate bool) domain.UpdateEvent {
	if event.PtsCount <= 0 {
		event.PtsCount = 1
	}
	event.UserID = userID
	event = cloneUpdateEvent(event)
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
	return event
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

// MaxContiguousPts returns the largest gap-free pts starting from 1 (the in-memory version scans contiguously by pts_count).
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

// NewUpdateStateStore creates an in-memory UpdateStateStore.
func NewUpdateStateStore() *UpdateStateStore {
	return &UpdateStateStore{
		states:   make(map[updateStateKey]domain.UpdateState),
		observed: make(map[updateStateKey]domain.UpdateState),
	}
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
	// The acknowledged watermark only increases (aligning with PG's GREATEST upsert): a
	// getDifference from an old `from` (smaller pts) must not roll back the device's acknowledged
	// watermark, otherwise getPeerDialogs.state would hand out a regressed pts baseline.
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

func (s *UpdateStateStore) CommitDeliveredState(_ context.Context, id [8]byte, userID int64, st domain.UpdateState, mode domain.UpdateStateCommitMode) error {
	if mode != domain.UpdateStateCommitDeliveredOnly && mode != domain.UpdateStateCommitDeliveredAndObservedBaseline {
		return fmt.Errorf("commit delivered update state: invalid mode %d", mode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := updateStateKey{authKeyID: id, userID: userID}
	s.states[key] = monotonicUpdateState(s.states[key], st)
	if mode == domain.UpdateStateCommitDeliveredAndObservedBaseline {
		s.observed[key] = monotonicUpdateState(s.observed[key], st)
	}
	return nil
}

func (s *UpdateStateStore) ObserveClientState(_ context.Context, id [8]byte, userID int64, st domain.UpdateState) error {
	s.mu.Lock()
	key := updateStateKey{authKeyID: id, userID: userID}
	s.observed[key] = monotonicUpdateState(s.observed[key], st)
	s.mu.Unlock()
	return nil
}

func monotonicUpdateState(prev, next domain.UpdateState) domain.UpdateState {
	if next.Pts < prev.Pts {
		next.Pts = prev.Pts
	}
	if next.Qts < prev.Qts {
		next.Qts = prev.Qts
	}
	if next.Date < prev.Date {
		next.Date = prev.Date
	}
	if next.Seq < prev.Seq {
		next.Seq = prev.Seq
	}
	return next
}

// ObservedClientState is exposed to same-package/service tests to verify the retention safety watermark; the business read path still uses Get.
func (s *UpdateStateStore) ObservedClientState(id [8]byte, userID int64) (domain.UpdateState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.observed[updateStateKey{authKeyID: id, userID: userID}]
	return st, ok
}

func (s *UpdateStateStore) Delete(_ context.Context, id [8]byte, userID int64) error {
	s.mu.Lock()
	delete(s.states, updateStateKey{authKeyID: id, userID: userID})
	delete(s.observed, updateStateKey{authKeyID: id, userID: userID})
	s.mu.Unlock()
	return nil
}

func (s *UpdateStateStore) DeleteAuthKey(_ context.Context, id [8]byte) error {
	s.mu.Lock()
	for k := range s.states {
		if k.authKeyID == id {
			delete(s.states, k)
			delete(s.observed, k)
		}
	}
	for k := range s.observed {
		if k.authKeyID == id {
			delete(s.observed, k)
		}
	}
	s.mu.Unlock()
	return nil
}
