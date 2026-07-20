package memory

import (
	"container/heap"
	"context"
	"sync"
	"sync/atomic"
	"time"

	"telesrv/internal/domain"
)

const ephemeralShardCount = 64

type ephemeralMessageKey struct {
	peerType domain.PeerType
	peerID   int64
	id       int
}

type ephemeralRandomKey struct {
	peerType   domain.PeerType
	peerID     int64
	senderID   int64
	receiverID int64
	randomID   int64
}

type ephemeralEntry struct {
	message    domain.EphemeralMessage
	generation uint64
}

type ephemeralExpiry struct {
	key        ephemeralMessageKey
	expiresAt  int64
	generation uint64
}

type ephemeralExpiryHeap []ephemeralExpiry

func (h ephemeralExpiryHeap) Len() int           { return len(h) }
func (h ephemeralExpiryHeap) Less(i, j int) bool { return h[i].expiresAt < h[j].expiresAt }
func (h ephemeralExpiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *ephemeralExpiryHeap) Push(value any) {
	*h = append(*h, value.(ephemeralExpiry))
}

func (h *ephemeralExpiryHeap) Pop() any {
	old := *h
	n := len(old)
	value := old[n-1]
	old[n-1] = ephemeralExpiry{}
	*h = old[:n-1]
	return value
}

type ephemeralShard struct {
	mu             sync.RWMutex
	messages       map[ephemeralMessageKey]ephemeralEntry
	random         map[ephemeralRandomKey]ephemeralMessageKey
	expiry         ephemeralExpiryHeap
	nextGeneration uint64
}

type ephemeralCallbackActionShard struct {
	mu             sync.RWMutex
	actions        map[int64]ephemeralCallbackActionEntry
	expiry         ephemeralCallbackExpiryHeap
	nextGeneration uint64
}

type ephemeralCallbackActionEntry struct {
	action     domain.EphemeralCallbackAction
	generation uint64
}

type ephemeralCallbackExpiry struct {
	queryID    int64
	expiresAt  int64
	generation uint64
}

type ephemeralCallbackExpiryHeap []ephemeralCallbackExpiry

func (h ephemeralCallbackExpiryHeap) Len() int           { return len(h) }
func (h ephemeralCallbackExpiryHeap) Less(i, j int) bool { return h[i].expiresAt < h[j].expiresAt }
func (h ephemeralCallbackExpiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *ephemeralCallbackExpiryHeap) Push(value any) {
	*h = append(*h, value.(ephemeralCallbackExpiry))
}

func (h *ephemeralCallbackExpiryHeap) Pop() any {
	old := *h
	n := len(old)
	value := old[n-1]
	old[n-1] = ephemeralCallbackExpiry{}
	*h = old[:n-1]
	return value
}

// EphemeralMessageStore shards by peer. A create touches one shard, so the ID
// and random-ID indexes can be updated atomically without a process-wide lock.
type EphemeralMessageStore struct {
	shards          [ephemeralShardCount]ephemeralShard
	callbackActions [ephemeralShardCount]ephemeralCallbackActionShard
	messageCursor   atomic.Uint32
	callbackCursor  atomic.Uint32
}

func NewEphemeralMessageStore() *EphemeralMessageStore {
	s := &EphemeralMessageStore{}
	for i := range s.shards {
		s.shards[i].messages = make(map[ephemeralMessageKey]ephemeralEntry)
		s.shards[i].random = make(map[ephemeralRandomKey]ephemeralMessageKey)
		s.callbackActions[i].actions = make(map[int64]ephemeralCallbackActionEntry)
	}
	return s
}

func (s *EphemeralMessageStore) PutEphemeralCallbackAction(_ context.Context, action domain.EphemeralCallbackAction) (bool, error) {
	if action.QueryID == 0 || action.BotUserID <= 0 || action.UserID <= 0 || action.Peer.Type != domain.PeerTypeChannel ||
		action.Peer.ID <= 0 || action.MessageID <= 0 || action.Device.UserID != action.UserID ||
		action.Device.BusinessAuthKeyID == ([8]byte{}) || action.CreatedAt.IsZero() || !action.ExpiresAt.After(action.CreatedAt) ||
		action.ExpiresAt.Sub(action.CreatedAt) > domain.EphemeralReplyWindow {
		return false, domain.ErrEphemeralInvalid
	}
	shard := &s.callbackActions[uint64(action.QueryID)&(ephemeralShardCount-1)]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if existing, ok := shard.actions[action.QueryID]; ok && action.CreatedAt.Before(existing.action.ExpiresAt) {
		return false, nil
	}
	shard.nextGeneration++
	entry := ephemeralCallbackActionEntry{action: action, generation: shard.nextGeneration}
	shard.actions[action.QueryID] = entry
	heap.Push(&shard.expiry, ephemeralCallbackExpiry{
		queryID: action.QueryID, expiresAt: action.ExpiresAt.UnixNano(), generation: entry.generation,
	})
	return true, nil
}

func (s *EphemeralMessageStore) GetEphemeralCallbackAction(_ context.Context, botUserID, queryID int64, now time.Time) (domain.EphemeralCallbackAction, bool, error) {
	if botUserID <= 0 || queryID == 0 {
		return domain.EphemeralCallbackAction{}, false, nil
	}
	shard := &s.callbackActions[uint64(queryID)&(ephemeralShardCount-1)]
	shard.mu.RLock()
	entry, ok := shard.actions[queryID]
	if ok && entry.action.BotUserID == botUserID && now.Before(entry.action.ExpiresAt) {
		shard.mu.RUnlock()
		return entry.action, true, nil
	}
	shard.mu.RUnlock()
	if !ok || entry.action.BotUserID != botUserID {
		return domain.EphemeralCallbackAction{}, false, nil
	}
	shard.mu.Lock()
	if current, exists := shard.actions[queryID]; exists && !now.Before(current.action.ExpiresAt) {
		delete(shard.actions, queryID)
	}
	shard.mu.Unlock()
	return domain.EphemeralCallbackAction{}, false, nil
}

func (s *EphemeralMessageStore) CreateEphemeralMessage(_ context.Context, message domain.EphemeralMessage) (domain.EphemeralMessage, bool, error) {
	now := message.CreatedAt
	if err := message.ValidateForCreate(now); err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	shard := s.shard(message.Peer)
	messageKey := ephemeralKey(message.Peer, message.ID)
	randomKey := ephemeralRandom(message)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	if existingKey, ok := shard.random[randomKey]; ok {
		if existing, found := shard.messages[existingKey]; found && !existing.message.Expired(now) {
			if existing.message.PayloadHash != message.PayloadHash {
				return domain.EphemeralMessage{}, false, domain.ErrEphemeralRandomIDConflict
			}
			return cloneEphemeralMessage(existing.message), false, nil
		}
		delete(shard.random, randomKey)
		delete(shard.messages, existingKey)
	}
	if existing, ok := shard.messages[messageKey]; ok {
		if !existing.message.Expired(now) {
			return domain.EphemeralMessage{}, false, domain.ErrEphemeralIDCollision
		}
		delete(shard.random, ephemeralRandom(existing.message))
		delete(shard.messages, messageKey)
	}
	stored := cloneEphemeralMessage(message)
	stored.BotAPIReply = nil
	shard.nextGeneration++
	entry := ephemeralEntry{message: stored, generation: shard.nextGeneration}
	shard.messages[messageKey] = entry
	shard.random[randomKey] = messageKey
	heap.Push(&shard.expiry, ephemeralExpiry{
		key:        messageKey,
		expiresAt:  stored.ExpiresAt.UnixNano(),
		generation: entry.generation,
	})
	return cloneEphemeralMessage(stored), true, nil
}

func (s *EphemeralMessageStore) GetEphemeralMessage(_ context.Context, peer domain.Peer, id int, now time.Time) (domain.EphemeralMessage, bool, error) {
	key := ephemeralKey(peer, id)
	shard := s.shard(peer)
	shard.mu.RLock()
	entry, ok := shard.messages[key]
	if ok && !entry.message.Expired(now) {
		message := cloneEphemeralMessage(entry.message)
		shard.mu.RUnlock()
		return message, true, nil
	}
	shard.mu.RUnlock()
	if !ok {
		return domain.EphemeralMessage{}, false, nil
	}
	shard.mu.Lock()
	if entry, ok = shard.messages[key]; ok && entry.message.Expired(now) {
		delete(shard.messages, key)
		delete(shard.random, ephemeralRandom(entry.message))
	}
	shard.mu.Unlock()
	return domain.EphemeralMessage{}, false, nil
}

func (s *EphemeralMessageStore) EditEphemeralMessage(_ context.Context, peer domain.Peer, id int, expectedVersion uint64, content domain.EphemeralContent, editDate int, now time.Time) (domain.EphemeralMessage, error) {
	key := ephemeralKey(peer, id)
	shard := s.shard(peer)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	entry, ok := shard.messages[key]
	if !ok {
		return domain.EphemeralMessage{}, domain.ErrEphemeralNotFound
	}
	if entry.message.Expired(now) {
		delete(shard.messages, key)
		delete(shard.random, ephemeralRandom(entry.message))
		return domain.EphemeralMessage{}, domain.ErrEphemeralExpired
	}
	if entry.message.Deleted {
		return domain.EphemeralMessage{}, domain.ErrEphemeralDeleted
	}
	if expectedVersion == 0 || entry.message.Version != expectedVersion {
		return domain.EphemeralMessage{}, domain.ErrEphemeralVersionConflict
	}
	if domain.ValidateEphemeralContent(content) != nil {
		return domain.EphemeralMessage{}, domain.ErrEphemeralInvalid
	}
	entry.message.Content = cloneEphemeralContent(content)
	entry.message.EditDate = editDate
	entry.message.Version++
	shard.messages[key] = entry
	return cloneEphemeralMessage(entry.message), nil
}

func (s *EphemeralMessageStore) DeleteEphemeralMessage(_ context.Context, peer domain.Peer, id int, expectedVersion uint64, now time.Time) (domain.EphemeralMessage, bool, error) {
	key := ephemeralKey(peer, id)
	shard := s.shard(peer)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	entry, ok := shard.messages[key]
	if !ok {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralNotFound
	}
	if entry.message.Expired(now) {
		delete(shard.messages, key)
		delete(shard.random, ephemeralRandom(entry.message))
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralExpired
	}
	if entry.message.Deleted {
		return cloneEphemeralMessage(entry.message), false, nil
	}
	if expectedVersion == 0 || entry.message.Version != expectedVersion {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralVersionConflict
	}
	entry.message.Deleted = true
	entry.message.Version++
	// Keep a small tombstone until the original TTL. It prevents a delayed
	// random-id retry from resurrecting a message after delete.
	entry.message.Content = domain.EphemeralContent{}
	shard.messages[key] = entry
	return cloneEphemeralMessage(entry.message), true, nil
}

func (s *EphemeralMessageStore) PruneExpiredEphemeralMessages(_ context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	deleted := 0
	nowUnixNano := now.UnixNano()
	start := int(s.messageCursor.Add(1)-1) & (ephemeralShardCount - 1)
	for offset := range ephemeralShardCount {
		shard := &s.shards[(start+offset)&(ephemeralShardCount-1)]
		shard.mu.Lock()
		for deleted < limit && shard.expiry.Len() > 0 && shard.expiry[0].expiresAt <= nowUnixNano {
			expiry := heap.Pop(&shard.expiry).(ephemeralExpiry)
			entry, ok := shard.messages[expiry.key]
			if !ok || entry.generation != expiry.generation {
				continue
			}
			delete(shard.messages, expiry.key)
			delete(shard.random, ephemeralRandom(entry.message))
			deleted++
		}
		shard.mu.Unlock()
		if deleted >= limit {
			break
		}
	}
	// Callback authorizations have an independent 15-second TTL. Give their
	// heap an independent bounded budget so a hot message shard cannot starve
	// callback cleanup and cause an in-memory deployment to grow forever.
	callbackDeleted := 0
	callbackStart := int(s.callbackCursor.Add(1)-1) & (ephemeralShardCount - 1)
	for offset := range ephemeralShardCount {
		shard := &s.callbackActions[(callbackStart+offset)&(ephemeralShardCount-1)]
		shard.mu.Lock()
		for callbackDeleted < limit && shard.expiry.Len() > 0 && shard.expiry[0].expiresAt <= nowUnixNano {
			expiry := heap.Pop(&shard.expiry).(ephemeralCallbackExpiry)
			entry, ok := shard.actions[expiry.queryID]
			if !ok || entry.generation != expiry.generation {
				continue
			}
			delete(shard.actions, expiry.queryID)
			callbackDeleted++
		}
		shard.mu.Unlock()
		if callbackDeleted >= limit {
			break
		}
	}
	return deleted, nil
}

func (s *EphemeralMessageStore) shard(peer domain.Peer) *ephemeralShard {
	// Peer IDs are already uniformly allocated monotonically; multiplicative
	// mixing avoids adjacent hot groups concentrating in neighboring low bits.
	index := (uint64(peer.ID) * 11400714819323198485) >> (64 - 6)
	return &s.shards[index]
}

func ephemeralKey(peer domain.Peer, id int) ephemeralMessageKey {
	return ephemeralMessageKey{peerType: peer.Type, peerID: peer.ID, id: id}
}

func ephemeralRandom(message domain.EphemeralMessage) ephemeralRandomKey {
	return ephemeralRandomKey{
		peerType:   message.Peer.Type,
		peerID:     message.Peer.ID,
		senderID:   message.SenderUserID,
		receiverID: message.ReceiverUserID,
		randomID:   message.RandomID,
	}
}

func cloneEphemeralMessage(message domain.EphemeralMessage) domain.EphemeralMessage {
	message.Content = cloneEphemeralContent(message.Content)
	if message.BotAPIReply != nil {
		reply := *message.BotAPIReply
		reply.Content = cloneEphemeralContent(reply.Content)
		reply.BotAPIReply = nil
		message.BotAPIReply = &reply
	}
	return message
}

func cloneEphemeralContent(content domain.EphemeralContent) domain.EphemeralContent {
	content.Entities = append([]domain.MessageEntity(nil), content.Entities...)
	content.Media = cloneRequestedPeerMedia(content.Media)
	content.ReplyMarkup = cloneReplyMarkup(content.ReplyMarkup)
	content.RichMessage = cloneRichMessage(content.RichMessage)
	return content
}
