package memory

import (
	"sync"
	"telesrv/internal/domain"
)

const firstMemoryChannelID int64 = 2000000000

type channelRandomKey struct {
	channelID int64
	userID    int64
	randomID  int64
}

type channelMessageReplayKey struct {
	channelID int64
	messageID int
}

type boostSlotKey struct {
	userID int64
	slot   int
}

// channelReadWatermark 是 channel 级公共已读水位：任一成员推进过的最高两个
// read_inbox。sender 的 read_outbox 由它派生（top1 持有者本人取 top2）。
// memoryMention 是 owner 视角一条 mention 的状态：topID 支持 topic 过滤，
// unread 翻转为 false 表示已读但 mentioned 高亮永久保留。
type memoryMention struct {
	topID  int
	unread bool
}

type channelReadWatermark struct {
	top1User int64
	top1     int
	top2     int
}

func (w channelReadWatermark) forSender(userID int64) int {
	if w.top1User == userID {
		return w.top2
	}
	return w.top1
}

func (w channelReadWatermark) advance(userID int64, maxID int) channelReadWatermark {
	switch {
	case w.top1User == userID:
		if maxID > w.top1 {
			w.top1 = maxID
		}
	case maxID >= w.top1:
		w.top2 = w.top1
		w.top1User = userID
		w.top1 = maxID
	case maxID > w.top2:
		w.top2 = maxID
	}
	return w
}

// ChannelStore is an in-memory channel/supergroup store for tests and local development.
type ChannelStore struct {
	mu        sync.RWMutex
	nextID    int64
	nextHash  int64
	channels  map[int64]domain.Channel
	members   map[int64]map[int64]domain.ChannelMember
	dialogs   map[int64]map[int64]domain.ChannelDialog
	topics    map[int64]map[int]domain.ChannelForumTopic
	messages  map[int64][]domain.ChannelMessage
	reactions map[int64]map[int]map[int64][]domain.ChannelMessagePeerReaction
	// paidReactions 是 per-(channel,message,user) 付费 reaction 累计星数 + 匿名标志。
	paidReactions          map[int64]map[int]map[int64]memoryPaidReaction
	top                    map[int64]map[string]domain.TopMessageReaction
	recent                 map[int64]map[string]domain.RecentMessageReaction
	savedTags              map[int64]map[string]domain.SavedReactionTag
	mentions               map[int64]map[int64]map[int]memoryMention
	msgViews               map[int64]map[int]int
	msgViewers             map[int64]map[int]map[int64]struct{}
	events                 map[int64][]domain.ChannelUpdateEvent
	retention              map[int64]domain.ChannelUpdateRetentionCheckpoint
	adminLogs              map[int64][]domain.ChannelAdminLogEvent
	invites                map[string]domain.ChannelInvite
	importers              map[int64]map[int64]domain.ChannelInviteImporter
	msgSeq                 map[int64]int
	ptsSeq                 map[int64]int
	logSeq                 map[int64]int64
	randomToID             map[channelRandomKey]int
	sendSnapshots          map[channelMessageReplayKey][]byte
	sendFingerprints       map[channelMessageReplayKey][]byte
	deleteReceipts         map[channelMessageReplayKey]*domain.ChannelUpdateEvent
	starsBalances          map[int64]int64
	channelStarsBalances   map[int64]int64
	tonBalances            map[int64]int64
	channelTONBalances     map[int64]int64
	suggestedPostApprovals map[memorySuggestedPostKey]memorySuggestedPostApproval
	boostSlots             map[boostSlotKey]domain.PremiumBoostSlot
	readMarks              map[int64]channelReadWatermark
	// topicReads 是 per-(channel,user,topic) 已读水位（forum 话题独立已读，不碰频道级 member 水位）。
	topicReads map[int64]map[int64]map[int]memoryTopicRead
	// polls 是共享 poll 权威（与 MessageStore 同一实例）；nil 时 poll 链路按未接入处理。
	polls *PollStore
}

// AttachPollStore 注入共享 poll 权威。
func (s *ChannelStore) AttachPollStore(polls *PollStore) {
	s.polls = polls
}

// NewChannelStore creates an in-memory ChannelStore.
func NewChannelStore() *ChannelStore {
	return &ChannelStore{
		nextID:                 firstMemoryChannelID,
		nextHash:               900000000000,
		channels:               make(map[int64]domain.Channel),
		members:                make(map[int64]map[int64]domain.ChannelMember),
		dialogs:                make(map[int64]map[int64]domain.ChannelDialog),
		topics:                 make(map[int64]map[int]domain.ChannelForumTopic),
		messages:               make(map[int64][]domain.ChannelMessage),
		reactions:              make(map[int64]map[int]map[int64][]domain.ChannelMessagePeerReaction),
		paidReactions:          make(map[int64]map[int]map[int64]memoryPaidReaction),
		top:                    make(map[int64]map[string]domain.TopMessageReaction),
		recent:                 make(map[int64]map[string]domain.RecentMessageReaction),
		savedTags:              make(map[int64]map[string]domain.SavedReactionTag),
		mentions:               make(map[int64]map[int64]map[int]memoryMention),
		msgViews:               make(map[int64]map[int]int),
		msgViewers:             make(map[int64]map[int]map[int64]struct{}),
		events:                 make(map[int64][]domain.ChannelUpdateEvent),
		retention:              make(map[int64]domain.ChannelUpdateRetentionCheckpoint),
		adminLogs:              make(map[int64][]domain.ChannelAdminLogEvent),
		invites:                make(map[string]domain.ChannelInvite),
		importers:              make(map[int64]map[int64]domain.ChannelInviteImporter),
		msgSeq:                 make(map[int64]int),
		ptsSeq:                 make(map[int64]int),
		logSeq:                 make(map[int64]int64),
		randomToID:             make(map[channelRandomKey]int),
		sendSnapshots:          make(map[channelMessageReplayKey][]byte),
		sendFingerprints:       make(map[channelMessageReplayKey][]byte),
		deleteReceipts:         make(map[channelMessageReplayKey]*domain.ChannelUpdateEvent),
		starsBalances:          make(map[int64]int64),
		channelStarsBalances:   make(map[int64]int64),
		tonBalances:            make(map[int64]int64),
		channelTONBalances:     make(map[int64]int64),
		suggestedPostApprovals: make(map[memorySuggestedPostKey]memorySuggestedPostApproval),
		boostSlots:             make(map[boostSlotKey]domain.PremiumBoostSlot),
		readMarks:              make(map[int64]channelReadWatermark),
		topicReads:             make(map[int64]map[int64]map[int]memoryTopicRead),
	}
}
