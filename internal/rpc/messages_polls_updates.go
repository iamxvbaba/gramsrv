package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 本文件是 poll 链路的响应/推送/事件辅助，与 reaction（messages_reactions_send.go）同构：
//   - 私聊：双方各记一条 message_poll durable event（无 pts 的 updateMessagePoll + aux pts 簿记），
//     离线端经 getDifference 拿到消息快照与最新 poll 状态；
//   - 频道：实时 fan-out 给在线 viewer（作者进 explicit 收件人），无 durable event——
//     与 channel reaction 现状一致（离线缺口见 compatibility-matrix.md，客户端靠 getPollResults 刷新）。

// pollMutationErr 把 poll/消息域错误映射为 RPC error。
func pollMutationErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrPollClosed):
		return pollClosedErr()
	case errors.Is(err, domain.ErrPollRevoteNotAllowed):
		return revoteNotAllowedErr()
	case errors.Is(err, domain.ErrPollOptionInvalid):
		return optionInvalidErr()
	case errors.Is(err, domain.ErrPollNotCreator):
		return tgerr.New(403, "MESSAGE_AUTHOR_REQUIRED")
	case errors.Is(err, domain.ErrPollNotFound), errors.Is(err, domain.ErrPollInvalid), errors.Is(err, domain.ErrMessageIDInvalid):
		return messageIDInvalidErr()
	default:
		return channelInvalidErr(err)
	}
}

// loadMessagePoll 按 viewer 视角加载消息上的 poll（已 enrich）。
func (r *Router) loadMessagePoll(ctx context.Context, userID int64, peer domain.Peer, msgID int) (*domain.MessagePoll, domain.Peer, bool) {
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil, peer, false
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{msgID})
		if err != nil {
			return nil, peer, false
		}
		for _, msg := range history.Messages {
			if msg.ID == msgID && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindPoll && msg.Media.Poll != nil {
				return msg.Media.Poll, peer, true
			}
		}
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, peer, false
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, []int{msgID})
		if err != nil {
			return nil, peer, false
		}
		for _, msg := range list.Messages {
			if msg.ID == msgID && msg.Peer == peer && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindPoll && msg.Media.Poll != nil {
				return msg.Media.Poll, peer, true
			}
		}
	}
	return nil, peer, false
}

// pollUpdateRefs 收集 updateMessagePoll 响应需要的 users/chats（recent voters 头像 + channel 实体）。
// channel 由调用方传入（频道 poll 用结果里的 res.Channel；私聊传零值）：与 reaction fan-out 同款用
// 单个 channel 一次性投影 tgChannels，避免频道 poll fan-out 每 viewer 一次 GetChannel 的 DB N+1
// （poll 聚合 N+1 已由 ChannelPollFanoutViews 消除，这是 poll fan-out 残留的 per-viewer DB 调用）。
func (r *Router) pollUpdateRefs(ctx context.Context, viewerUserID int64, peer domain.Peer, poll *domain.MessagePoll, channel domain.Channel) ([]tg.UserClass, []tg.ChatClass) {
	userIDs := make([]int64, 0, domain.MaxPollRecentVoters)
	if poll != nil && poll.Results != nil {
		userIDs = append(userIDs, poll.Results.RecentVoters...)
	}
	users := r.tgUsersForIDs(ctx, viewerUserID, userIDs)
	chats := []tg.ChatClass{}
	if peer.Type == domain.PeerTypeChannel {
		if channel.ID != 0 {
			// fan-out 路径：调用方已带 channel，一次性投影（同 reaction），免 per-viewer GetChannel。
			chats = tgChannels(viewerUserID, []domain.Channel{channel})
		} else if r.deps.Channels != nil {
			// 非 fan-out 单 viewer 路径（getPollResults）：channel 未带，保持原 GetChannel 行为不变。
			if view, err := r.deps.Channels.GetChannel(ctx, viewerUserID, peer.ID); err == nil && view.Channel.ID != 0 {
				chats = []tg.ChatClass{tgChannelChatForView(viewerUserID, view)}
			}
		}
	}
	return users, chats
}

// privatePollUpdates 记录双方 durable event、推送双方在线 session，并返回投票者视角响应。
func (r *Router) privatePollUpdates(ctx context.Context, requestUserID int64, res domain.PrivateMessagePollResult) tg.UpdatesClass {
	recordedEvents := r.recordPrivateMessagePollEvents(ctx, requestUserID, res)
	var requesterUpdates *tg.Updates
	for _, msg := range res.Messages {
		if msg.ID <= 0 || msg.Media == nil || msg.Media.Poll == nil {
			continue
		}
		update := tgUpdateMessagePoll(msg.Peer, msg.ID, msg.Media.Poll)
		if update == nil {
			continue
		}
		users, chats := r.pollUpdateRefs(ctx, msg.OwnerUserID, msg.Peer, msg.Media.Poll, domain.Channel{})
		updates := &tg.Updates{
			Updates: []tg.UpdateClass{update},
			Users:   users,
			Chats:   chats,
			Date:    int(r.clock.Now().Unix()),
		}
		// message_poll 事件占账号 pts 但 updateMessagePoll 无 pts 字段，附 aux 簿记推水位。
		updates.Updates = appendAuxPtsBookkeeping(updates.Updates, recordedEvents[msg.OwnerUserID])
		if msg.OwnerUserID == requestUserID {
			requesterUpdates = updates
		}
		r.pushUserUpdates(ctx, msg.OwnerUserID, updates)
	}
	if requesterUpdates == nil {
		return tgEmptyUpdates(int(r.clock.Now().Unix()))
	}
	return requesterUpdates
}

func (r *Router) recordPrivateMessagePollEvents(ctx context.Context, requestUserID int64, res domain.PrivateMessagePollResult) map[int64]domain.UpdateEvent {
	if r.deps.Updates == nil {
		return nil
	}
	recorder, ok := r.deps.Updates.(messagePollUpdateRecorder)
	if !ok {
		return nil
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	events := make(map[int64]domain.UpdateEvent, len(res.Messages))
	for _, msg := range res.Messages {
		if msg.OwnerUserID == 0 || msg.ID == 0 {
			continue
		}
		eventAuthKeyID := [8]byte{}
		if msg.OwnerUserID == requestUserID {
			eventAuthKeyID = authKeyID
		}
		event, _, err := recorder.RecordMessagePoll(ctx, eventAuthKeyID, msg.OwnerUserID, msg)
		if err != nil {
			r.log.Warn("record message poll event failed")
			continue
		}
		events[msg.OwnerUserID] = event
	}
	return events
}

// onEditMessageClosePoll 处理 editMessage + InputMediaPoll：当前唯一支持的 poll 编辑是
// 关闭（closed=true，仅 poll 创建者）；改题/改选项无官方客户端路径，显式拒绝。
func (r *Router) onEditMessageClosePoll(ctx context.Context, req *tg.MessagesEditMessageRequest, media *tg.InputMediaPoll) (tg.UpdatesClass, error) {
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if !media.Poll.Closed {
		return nil, mediaInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil, messageIDInvalidErr()
		}
		res, err := r.deps.Channels.CloseMessagePoll(ctx, userID, domain.CloseChannelMessagePollRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			MessageID: req.ID,
			Date:      now,
		})
		if err != nil {
			return nil, pollMutationErr(err)
		}
		return r.channelPollUpdates(ctx, userID, peer, req.ID, res, true), nil
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, messageIDInvalidErr()
		}
		res, err := r.deps.Messages.CloseMessagePoll(ctx, userID, domain.ClosePrivateMessagePollRequest{
			UserID:    userID,
			Peer:      peer,
			MessageID: req.ID,
			Date:      now,
		})
		if err != nil {
			return nil, pollMutationErr(err)
		}
		return r.privatePollUpdates(ctx, userID, res), nil
	default:
		return nil, peerIDInvalidErr()
	}
}

// channelPollUpdates 组装投票者视角响应；push 为 true 时按 viewer 重建并 fan-out 给在线成员。
//
// Phase 4 模板化：fan-out 前一次性批量加载所有收件人的 per-viewer poll 投影（ChannelPollFanoutViews
// 把 viewer-invariant 聚合只算一次 + 批量 viewerOptions/可见性），消除原先每 viewer 一次 GetMessages
// 的 N+1。dispatch 仍同步（poll 是 viewer-only 无 durable event，改异步丢队列即永久漏）。预取未覆盖
// 的 viewer 回退逐 viewer loadMessagePoll 保正确（与旧路径字节同源）。actor echo 仍用 res poll（旧行为）。
func (r *Router) channelPollUpdates(ctx context.Context, userID int64, peer domain.Peer, msgID int, res domain.ChannelMessagePollResult, push bool) tg.UpdatesClass {
	var batched map[int64]*domain.MessagePoll
	if push && peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		recipients := r.channelFanoutRecipients(ctx, channelFanoutViewers, res.Channel.ID, res.Recipients)
		if len(recipients) > 0 {
			views, err := r.deps.Channels.ChannelPollFanoutViews(ctx, res.Channel.ID, msgID, recipients, int(r.clock.Now().Unix()))
			if err != nil {
				r.log.Warn("channel poll fanout prefetch failed; falling back to per-viewer reload",
					zap.Int64("channel_id", res.Channel.ID), zap.Int("msg_id", msgID), zap.Error(err))
			} else {
				batched = views
			}
		}
	}
	build := func(viewerUserID int64) *tg.Updates {
		poll := res.Message.Media.Poll
		if viewerUserID != userID {
			// 其它 viewer 的 chosen/correct/solution 门控不同，按其视角取投影。
			if p, evaluated := batched[viewerUserID]; evaluated {
				if p == nil {
					return nil // 预取已判定该 viewer 不可见
				}
				poll = p
			} else {
				// 预取未覆盖（极少：并发/收件人集漂移）→ 回退逐 viewer 重载。
				reloaded, _, found := r.loadMessagePoll(ctx, viewerUserID, peer, msgID)
				if !found {
					return nil
				}
				poll = reloaded
			}
		}
		update := tgUpdateMessagePoll(peer, msgID, poll)
		if update == nil {
			return nil
		}
		users, chats := r.pollUpdateRefs(ctx, viewerUserID, peer, poll, res.Channel)
		return &tg.Updates{
			Updates: []tg.UpdateClass{update},
			Users:   users,
			Chats:   chats,
			Date:    int(r.clock.Now().Unix()),
		}
	}
	updates := build(userID)
	if push {
		r.pushChannelViewerUpdates(ctx, userID, res.Channel.ID, res.Recipients, build)
	}
	if updates == nil {
		return tgEmptyUpdates(int(r.clock.Now().Unix()))
	}
	return updates
}
