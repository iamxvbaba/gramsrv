package memory

import (
	"sync"
	"telesrv/internal/domain"
)

// MessageStore 是 store.MessageStore 的内存实现。
type MessageStore struct {
	mu                  sync.RWMutex
	m                   map[int64][]domain.Message
	nextUID             int64
	nextBox             map[int64]int
	nextPts             map[int64]int
	readOutboxDates     map[readOutboxDateKey]int
	privateReactions    map[int64]map[int64][]domain.ChannelMessagePeerReaction
	privateSendDedup    map[privateSendDedupKey]privateSendDedupRecord
	loginCodeDeliveries map[[32]byte]loginCodeDeliveryRecord
	albumGroups         map[albumGroupKey]albumGroupRecord
	dialogs             *DialogStore
	// polls 是共享 poll 权威（投票校验与读路径 enrichment）；nil 时 poll 链路按未接入处理。
	polls *PollStore
	// savedPins 是收藏夹子会话置顶顺序（下标即 pinned_order，越小越前）。
	savedPins map[int64][]domain.Peer
}

// AttachPollStore 注入共享 poll 权威（与 ChannelStore 共用同一实例）。
func (s *MessageStore) AttachPollStore(polls *PollStore) {
	s.polls = polls
}

type readOutboxDateKey struct {
	ownerUserID int64
	peerID      int64
	msgID       int
}

// NewMessageStore 创建内存 MessageStore。
func NewMessageStore(dialogs ...*DialogStore) *MessageStore {
	s := &MessageStore{
		m:                   make(map[int64][]domain.Message),
		nextUID:             1,
		nextBox:             make(map[int64]int),
		nextPts:             make(map[int64]int),
		readOutboxDates:     make(map[readOutboxDateKey]int),
		privateReactions:    make(map[int64]map[int64][]domain.ChannelMessagePeerReaction),
		privateSendDedup:    make(map[privateSendDedupKey]privateSendDedupRecord),
		loginCodeDeliveries: make(map[[32]byte]loginCodeDeliveryRecord),
		albumGroups:         make(map[albumGroupKey]albumGroupRecord),
		savedPins:           make(map[int64][]domain.Peer),
	}
	if len(dialogs) > 0 {
		s.dialogs = dialogs[0]
	}
	return s
}
