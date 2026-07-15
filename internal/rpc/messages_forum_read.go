package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"
	"telesrv/internal/domain"
)

// advanceForumGeneralReadAfterChannelRead 在「直接的频道级 readHistory」(channels.readHistory /
// messages.readHistory 频道 peer) 推进频道级已读后，顺带把 forum 的 General(topic 1) 话题级水位
// 也推进到同一个 maxID，并向自己其它设备/当前设备下发 updateReadChannelDiscussionInbox、向
// General 内发送者下发已读回执。
//
// 语义：General 消息即频道根历史（reply_to_top_id ∈ {0,1}），被频道级已读覆盖，故频道级读到顶
// ⇒ General 也读到顶。仅对 forum 生效（read.Forum 守卫，避免对普通频道多一次查询）。**普通子话题
// 的 readDiscussion 不经此路径**（其内部对频道级 ReadHistory 的「保守叠加」由 onMessagesReadDiscussion
// 直接调 service，不触达本 RPC handler），因此读子话题不会回灌 General —— 零话题间 cross-talk，
// 只有真正的频道级整读才推进 General。
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
	// 话题级水位此刻已落库；下面的 durable 事件+在线推送失败只影响其它设备的即时刷新，
	// getForumTopics 重拉会收敛，故失败仅记日志、不阻断已成功的频道级 readHistory。
	if err := r.recordChannelDiscussionInbox(ctx, userID, read.ChannelID, topicRes.TopicID, topicRes.MaxID, topicRes.Pts); err != nil {
		r.log.Warn("record general discussion inbox after channel read",
			zap.Int64("channel_id", read.ChannelID), zap.Error(err))
		return
	}
	r.pushChannelDiscussionOutboxUpdates(ctx, read.ChannelID, topicRes.TopicID, topicRes.OutboxUpdates)
}

// recordChannelDiscussionInbox 记录 forum 话题级已读并向自己其它设备/当前设备下发
// updateReadChannelDiscussionInbox（durable + 账号 pts 簿记，供离线差分恢复）。
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

// pushChannelDiscussionOutboxUpdates 在线向话题内发送者下发已读回执
// updateReadChannelDiscussionOutbox。与频道级 pushChannelReadOutboxUpdates 同为在线 best-effort
// （不进 durable，客户端重连可由 getForumTopics 的 read_outbox_max_id 兜底）。
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
