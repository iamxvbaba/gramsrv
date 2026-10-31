package memory

import (
	"sync"
	"telesrv/internal/domain"
)

// MessageStore is the in-memory implementation of store.MessageStore.
type MessageStore struct {
	mu                  sync.RWMutex
	noForwardsMu        sync.Mutex
	m                   map[int64][]domain.Message
	nextUID             int64
	nextBox             map[int64]int
	nextPts             map[int64]int
	readOutboxDates     map[readOutboxDateKey]int
	privateReactions    map[int64]map[int64][]domain.ChannelMessagePeerReaction
	savedMessageTags    map[int64]map[int][]domain.MessageReaction
	savedTagTitles      map[int64]map[string]string
	privateSendDedup    map[privateSendDedupKey]privateSendDedupRecord
	loginCodeDeliveries map[[32]byte]loginCodeDeliveryRecord
	albumGroups         map[albumGroupKey]albumGroupRecord
	dialogs             *DialogStore
	updateEvents        *UpdateEventStore
	// polls is the shared poll authority (vote validation and read-path enrichment); when nil, the poll path is treated as not wired in.
	polls *PollStore
	// savedPins is the pin order of saved-message sub-conversations (the index is pinned_order; smaller is earlier).
	savedPins map[int64][]domain.Peer
	// privateNoForwards is keyed by the sorted user pair. Requests are keyed by
	// the shared logical private-message id so both local box ids resolve to one
	// one-shot response fact.
	privateNoForwards         map[privateNoForwardsPair]domain.PrivateNoForwardsState
	privateNoForwardsRequests map[int64]memoryNoForwardsRequest
}

// AttachPollStore injects the shared poll authority (the same instance as ChannelStore uses).
func (s *MessageStore) AttachPollStore(polls *PollStore) {
	s.polls = polls
}

func (s *MessageStore) AttachUpdateEventStore(events *UpdateEventStore) {
	s.updateEvents = events
}

type readOutboxDateKey struct {
	ownerUserID int64
	peerID      int64
	msgID       int
}

// NewMessageStore creates an in-memory MessageStore.
func NewMessageStore(dialogs ...*DialogStore) *MessageStore {
	s := &MessageStore{
		m:                         make(map[int64][]domain.Message),
		nextUID:                   1,
		nextBox:                   make(map[int64]int),
		nextPts:                   make(map[int64]int),
		readOutboxDates:           make(map[readOutboxDateKey]int),
		privateReactions:          make(map[int64]map[int64][]domain.ChannelMessagePeerReaction),
		savedMessageTags:          make(map[int64]map[int][]domain.MessageReaction),
		savedTagTitles:            make(map[int64]map[string]string),
		privateSendDedup:          make(map[privateSendDedupKey]privateSendDedupRecord),
		loginCodeDeliveries:       make(map[[32]byte]loginCodeDeliveryRecord),
		albumGroups:               make(map[albumGroupKey]albumGroupRecord),
		savedPins:                 make(map[int64][]domain.Peer),
		privateNoForwards:         make(map[privateNoForwardsPair]domain.PrivateNoForwardsState),
		privateNoForwardsRequests: make(map[int64]memoryNoForwardsRequest),
		updateEvents:              NewUpdateEventStore(),
	}
	if len(dialogs) > 0 {
		s.dialogs = dialogs[0]
	}
	return s
}
