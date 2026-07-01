package rpc

import (
	"context"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// 群通话推送策略（updateGroupCall / updateGroupCallParticipants / connection 均无
// pts，不进 getDifference）：扇出对象统一为「在线群成员」（OnlineChannelMemberUserIDs，
// MaxChannelRealtimeFanout 封顶）——覆盖未入会的面板观察者（Android 点 banner 的
// 参与者列表靠它增量刷新）。离线一致性三板斧：服务消息带频道 pts 可补收；banner 靠
// channel.call_active/call_not_empty flag（拉 dialogs/getFullChannel 重建）；房间内
// 状态靠 checkGroupCall 与 version 跳号触发 getGroupParticipants 全量 reload 自愈。

// groupCallOnlineRecipients 返回在线群成员（含 originUserID 本人，推送时由
// pushUserMessage 的 ctx except 排除发起 session、保留其它设备）。
func (r *Router) groupCallOnlineRecipients(channelID int64) []int64 {
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return nil
	}
	return provider.OnlineChannelMemberUserIDs(channelID, domain.MaxChannelRealtimeFanout)
}

// groupCallUpdateContainer 把单条 update 包进 viewer 视角的 Updates 容器。
func (r *Router) groupCallUpdateContainer(ctx context.Context, viewerUserID int64, channel domain.Channel, update tg.UpdateClass, userIDs []int64) *tg.Updates {
	chats := []tg.ChatClass{}
	if channel.ID != 0 {
		chats = append(chats, tgChannel(viewerUserID, channel, nil))
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   r.tgUsersForIDs(ctx, viewerUserID, userIDs),
		Chats:   chats,
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

// pushGroupCallUpdate 把 updateGroupCall（call 行变化）推给在线群成员。
func (r *Router) pushGroupCallUpdate(ctx context.Context, channel domain.Channel, call domain.GroupCall) {
	if call.Conference() {
		r.pushConferenceGroupCallUpdate(ctx, call)
		return
	}
	recipients := r.groupCallOnlineRecipients(channel.ID)
	for _, viewerID := range recipients {
		update := &tg.UpdateGroupCall{Call: tgGroupCall(call, viewerID, false)}
		update.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
		r.pushUserMessage(ctx, viewerID, "group call update",
			r.groupCallUpdateContainer(ctx, viewerID, channel, update, []int64{call.CreatorUserID}))
	}
}

// pushGroupCallParticipantsUpdate 把参与者增量（version=N）推给在线群成员。
// 每个 viewer 单独构建：participant.Self flag 是 per-viewer 的。
func (r *Router) pushGroupCallParticipantsUpdate(ctx context.Context, channel domain.Channel, call domain.GroupCall, rows []domain.GroupCallParticipant) {
	if call.Conference() {
		r.pushConferenceGroupCallParticipantsUpdate(ctx, call, rows)
		return
	}
	if len(rows) == 0 {
		return
	}
	userIDs := make([]int64, 0, len(rows))
	for _, p := range rows {
		userIDs = append(userIDs, p.UserID)
	}
	recipients := r.groupCallOnlineRecipients(channel.ID)
	for _, viewerID := range recipients {
		update := &tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
			Participants: tgGroupCallParticipants(rows, viewerID),
			Version:      call.Version,
		}
		r.pushUserMessage(ctx, viewerID, "group call participants",
			r.groupCallUpdateContainer(ctx, viewerID, channel, update, userIDs))
	}
}

func (r *Router) conferenceCallRecipients(ctx context.Context, callID int64) []int64 {
	return r.conferenceCallRecipientsWith(ctx, callID, nil)
}

func (r *Router) conferenceCallRecipientsWith(ctx context.Context, callID int64, extraUserIDs []int64) []int64 {
	if r.deps.GroupCalls == nil {
		return nil
	}
	recipients, err := r.deps.GroupCalls.ConferenceRecipients(ctx, callID)
	if err != nil {
		return nil
	}
	recipients = append(recipients, extraUserIDs...)
	recipients = uniquePositiveUserIDs(recipients)
	if len(recipients) == 0 {
		return nil
	}
	if provider, ok := r.deps.Sessions.(OnlineUserProvider); ok {
		return provider.OnlineUserIDsForCandidates(recipients, domain.MaxChannelRealtimeFanout)
	}
	return recipients
}

func (r *Router) pushConferenceGroupCallUpdate(ctx context.Context, call domain.GroupCall) {
	r.pushConferenceGroupCallUpdateTo(ctx, call, nil)
}

func (r *Router) pushConferenceGroupCallUpdateTo(ctx context.Context, call domain.GroupCall, extraUserIDs []int64) {
	recipients := r.conferenceCallRecipientsWith(ctx, call.ID, extraUserIDs)
	for _, viewerID := range recipients {
		update := &tg.UpdateGroupCall{Call: tgGroupCall(call, viewerID, viewerID == call.CreatorUserID)}
		r.pushUserMessage(ctx, viewerID, "conference call update",
			r.groupCallUpdateContainer(ctx, viewerID, domain.Channel{}, update, []int64{call.CreatorUserID}))
	}
}

func (r *Router) pushConferenceGroupCallParticipantsUpdate(ctx context.Context, call domain.GroupCall, rows []domain.GroupCallParticipant) {
	if len(rows) == 0 {
		return
	}
	userIDs := make([]int64, 0, len(rows))
	for _, p := range rows {
		userIDs = append(userIDs, p.UserID)
	}
	recipients := r.conferenceCallRecipients(ctx, call.ID)
	for _, viewerID := range recipients {
		update := &tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
			Participants: tgGroupCallParticipants(rows, viewerID),
			Version:      call.Version,
		}
		r.pushUserMessage(ctx, viewerID, "conference call participants",
			r.groupCallUpdateContainer(ctx, viewerID, domain.Channel{}, update, userIDs))
	}
}

// pushGroupCallServiceMessage 把 started/ended/invite 服务消息（带频道 pts）推给
// 活跃成员（res.Recipients）。复用 channelOperationUpdates 的 per-viewer 构建。
func (r *Router) pushGroupCallServiceMessage(ctx context.Context, originUserID int64, res domain.SendChannelMessageResult) {
	if res.Event.Pts == 0 {
		return
	}
	op := domain.CreateChannelResult{
		Channel:    res.Channel,
		Message:    res.Message,
		Event:      res.Event,
		Recipients: res.Recipients,
	}
	r.pushChannelUpdates(ctx, originUserID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOperationUpdates(ctx, viewerUserID, op)
	})
}

// groupCallMutationFanout 是参与者维度变更后的统一扇出：participants 增量 +
// call_not_empty 翻转时的 channel 维度刷新（Android banner 对 flag 依赖更重）。
func (r *Router) groupCallMutationFanout(ctx context.Context, channel domain.Channel, mut domain.GroupCallMutation) domain.Channel {
	if mut.Call.Conference() {
		r.pushConferenceGroupCallParticipantsUpdate(ctx, mut.Call, []domain.GroupCallParticipant{mut.Participant})
		r.pushConferenceGroupCallUpdate(ctx, mut.Call)
		return domain.Channel{}
	}
	r.pushGroupCallParticipantsUpdate(ctx, channel, mut.Call, []domain.GroupCallParticipant{mut.Participant})
	wantNotEmpty := mut.Call.Active() && mut.Call.ParticipantsCount > 0
	if channel.ActiveCallNotEmpty != wantNotEmpty && r.deps.Channels != nil {
		updated, err := r.deps.Channels.SetActiveCall(ctx, channel.ID, channel.ActiveCallID, channel.ActiveCallAccessHash, wantNotEmpty)
		if err == nil {
			channel = updated
			r.pushGroupCallUpdate(ctx, channel, mut.Call)
			r.pushChannelStateToMembers(ctx, 0, channel)
		}
	} else {
		r.pushGroupCallUpdate(ctx, channel, mut.Call)
	}
	return channel
}

func groupCallParticipantUserIDs(rows []domain.GroupCallParticipant) []int64 {
	if len(rows) == 0 {
		return nil
	}
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.UserID)
	}
	return out
}

func uniquePositiveUserIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
