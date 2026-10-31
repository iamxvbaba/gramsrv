package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"
	"telesrv/internal/domain"
)

// advanceForumGeneralReadAfterChannelRead, after a "direct channel-level
// readHistory" (channels.readHistory / messages.readHistory with a channel peer)
// advances channel-level read state, also advances the forum's General (topic 1)
// topic-level watermark to the same maxID, and sends
// updateReadChannelDiscussionInbox to the user's other devices/the current device
// plus read receipts to senders within General.
//
// Semantics: General messages ARE the channel root history
// (reply_to_top_id in {0,1}) and are covered by channel-level read state, so
// reading the channel to the top implies General is read to the top too. It only
// takes effect for forums (guarded by read.Forum, avoiding an extra query for
// ordinary channels). **A plain subtopic's readDiscussion does not go through
// this path** (its internal "conservative overlay" onto channel-level ReadHistory
// calls the service directly from onMessagesReadDiscussion and never reaches this
// RPC handler), so reading a subtopic does not feed back into General -- zero
// cross-talk between topics; only a genuine channel-wide full read advances
// General.
func (r *Router) advanceForumGeneralReadAfterChannelRead(ctx context.Context, userID int64, read domain.ReadChannelHistoryResult) {
	if r.deps.Channels == nil || !read.Forum || read.ChannelID == 0 || read.MaxID <= 0 {
		return
	}
	topicRes, err := r.deps.Channels.ReadTopicHistory(ctx, userID, domain.ReadChannelTopicHistoryRequest{
		UserID:    userID,
		ChannelID: read.ChannelID,
		TopicID:   domain.ForumGeneralTopicID,
		MaxID:     read.MaxID,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil || !topicRes.Changed {
		return
	}
	// The topic-level watermark is already persisted at this point; the durable
	// event + online push below only affect other devices' immediate refresh, and
	// a getForumTopics re-pull converges, so a failure is logged only and does not
	// block the already-successful channel-level readHistory.
	if err := r.recordChannelDiscussionInbox(ctx, userID, read.ChannelID, topicRes.TopicID, topicRes.MaxID, topicRes.Pts); err != nil {
		r.log.Warn("record general discussion inbox after channel read",
			zap.Int64("channel_id", read.ChannelID), zap.Error(err))
		return
	}
	r.pushChannelDiscussionOutboxUpdates(ctx, read.ChannelID, topicRes.TopicID, topicRes.OutboxUpdates)
}

// recordChannelDiscussionInbox records a forum topic-level read and sends
// updateReadChannelDiscussionInbox to the user's other devices/the current device
// (durable + account pts bookkeeping, for offline difference recovery).
func (r *Router) recordChannelDiscussionInbox(ctx context.Context, userID, channelID int64, topicID, maxID, channelPts int) error {
	if maxID <= 0 || channelID == 0 || topicID <= 0 {
		return nil
	}
	event := domain.UpdateEvent{
		Type:       domain.UpdateEventReadChannelDiscussionInbox,
		Peer:       domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		TopMsgID:   topicID,
		MaxID:      maxID,
		ChannelPts: channelPts,
		Date:       int(r.clock.Now().Unix()),
		PtsCount:   1,
	}
	recorded := event
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		rec, _, err := r.deps.Updates.RecordChannelDiscussionInbox(ctx, authKeyID, userID, channelID, topicID, maxID, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return internalErr()
		}
		recorded = rec
	}
	r.pushCurrentReadHistoryEvent(ctx, recorded)
	r.pushReadHistoryEvent(ctx, userID, recorded)
	return nil
}

// pushChannelDiscussionOutboxUpdates sends read receipts
// (updateReadChannelDiscussionOutbox) online to senders within a topic. Like the
// channel-level pushChannelReadOutboxUpdates it is online best-effort (not
// durable; on reconnect the client falls back to getForumTopics'
// read_outbox_max_id).
func (r *Router) pushChannelDiscussionOutboxUpdates(ctx context.Context, channelID int64, topicID int, updates []domain.ChannelReadOutboxUpdate) {
	if r.deps.Sessions == nil || channelID == 0 || topicID <= 0 || len(updates) == 0 {
		return
	}
	seen := make(map[int64]int, len(updates))
	for _, update := range updates {
		if update.UserID == 0 || update.MaxID <= 0 {
			continue
		}
		if seen[update.UserID] < update.MaxID {
			seen[update.UserID] = update.MaxID
		}
	}
	date := int(r.clock.Now().Unix())
	for userID, maxID := range seen {
		r.pushUserUpdates(ctx, userID, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateReadChannelDiscussionOutbox{ChannelID: channelID, TopMsgID: topicID, ReadMaxID: maxID}},
			Date:    date,
			Seq:     0,
		})
	}
}
