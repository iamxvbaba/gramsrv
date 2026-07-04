package rpc

import (
	"context"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// registerUpdates 注册 updates.* RPC handler。
func (r *Router) registerUpdates(d *tg.ServerDispatcher) {
	d.OnUpdatesGetState(r.onUpdatesGetState)
	d.OnUpdatesGetDifference(r.onUpdatesGetDifference)
}

// onUpdatesGetState 处理 updates.getState：返回账号当前最新连续状态并推进该设备
// 的确认水位。协议语义是客户端宣告「从现在开始同步」，启动期离线数据由
// getDialogs 快照承载——返回设备旧确认水位会让 TDesktop（不持久化 pts、每次
// 启动都调 getState）在 getDialogs 快照之上重放历史差分，未读重复累计、
// dialog 预览被旧消息抢占。
func (r *Router) onUpdatesGetState(ctx context.Context) (*tg.UpdatesState, error) {
	id, _ := AuthKeyIDFrom(ctx)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Updates == nil {
		r.markSessionReceivesUpdates(ctx, userID)
		return &tg.UpdatesState{Date: int(r.clock.Now().Unix()), Qts: r.deviceEncryptedQts(ctx)}, nil
	}
	st, err := r.deps.Updates.AcknowledgeCurrentState(ctx, id, userID)
	if err != nil {
		return nil, internalErr()
	}
	r.markSessionReceivesUpdates(ctx, userID)
	r.registerBootstrapAfterBaseline(ctx, userID)
	// 密聊 qts 是设备级、独立于账号级 pts 引擎：注入当前设备已分配的 qts（无密聊设备为 0）。
	out := tgUpdateState(st)
	out.Qts = r.deviceEncryptedQts(ctx)
	return ptr(out), nil
}

func (r *Router) onUpdatesGetDifference(ctx context.Context, req *tg.UpdatesGetDifferenceRequest) (tg.UpdatesDifferenceClass, error) {
	id, _ := AuthKeyIDFrom(ctx)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Updates == nil {
		now := int(r.clock.Now().Unix())
		r.markSessionReceivesUpdates(ctx, userID)
		return &tg.UpdatesDifferenceEmpty{Date: now}, nil
	}
	// pts_total_limit 是客户端显式请求的 fast-skip：差距超限时返回
	// differenceTooLong{pts}，客户端据此整体重置会话列表而不是串行翻
	// 上千页 slice。不传该参数的客户端（TDesktop）永远不会收到 tooLong。
	if limit, ok := req.GetPtsTotalLimit(); ok && limit > 0 && req.Pts > 0 {
		current, err := r.deps.Updates.CurrentState(ctx, userID)
		if err == nil && current.Pts-req.Pts > limit {
			r.markSessionReceivesUpdates(ctx, userID)
			return &tg.UpdatesDifferenceTooLong{Pts: current.Pts}, nil
		}
	}
	st, err := r.deps.Updates.GetDifference(ctx, id, userID, domain.UpdateState{
		Pts:  req.Pts,
		Qts:  req.Qts,
		Date: req.Date,
	})
	if err != nil {
		return nil, internalErr()
	}
	r.markSessionReceivesUpdates(ctx, userID)
	st.ChannelNudges = r.accountChannelDifferenceNudges(ctx, userID, req.Date)
	// 密聊设备级 qts 消息（独立于账号级 pts 事件）：按当前设备 req.Qts 补回。
	encMsgs, newQts := r.encryptedDifference(ctx, req.Qts)
	// 密聊握手/已读状态事件（无 qts）：按未投递标记补回 OtherUpdates。
	stateUpdates, statePeerUserIDs, stateEventIDs := r.encryptedStateUpdates(ctx, userID)
	if len(st.Events) == 0 && len(st.ChannelNudges) == 0 && len(encMsgs) == 0 && len(stateUpdates) == 0 {
		r.registerBootstrapAfterBaseline(ctx, userID)
		return &tg.UpdatesDifferenceEmpty{Date: st.State.Date, Seq: st.State.Seq}, nil
	}
	st.Events = r.enrichUpdateEvents(ctx, userID, st.Events)
	diff := r.tgUpdatesDifference(ctx, userID, st)
	diff = injectEncryptedMessages(diff, encMsgs, newQts)
	diff = r.injectEncryptedOtherUpdates(ctx, userID, diff, stateUpdates, statePeerUserIDs)
	if len(stateEventIDs) > 0 {
		if deviceKey, ok := businessAuthKeyIDFrom(ctx); ok {
			_ = r.deps.SecretChats.MarkStateEventsDelivered(ctx, deviceKey, stateEventIDs)
		}
	}
	r.registerBootstrapAfterBaseline(ctx, userID)
	return diff, nil
}

func (r *Router) accountChannelDifferenceNudges(ctx context.Context, userID int64, sinceDate int) []domain.ChannelDifferenceNudge {
	if r.deps.Channels == nil || userID == 0 || sinceDate <= 0 {
		return nil
	}
	// 按 channel_id 翻页注入全部 dirty channel 的 nudge（仍有总量硬上限），
	// 避免加入超过一页活跃频道的账号在长离线恢复时漏掉 channel_id 较大的频道。
	const maxNudges = 500
	out := make([]domain.ChannelDifferenceNudge, 0, domain.MaxChannelDifferenceLimit)
	afterChannelID := int64(0)
	for len(out) < maxNudges {
		dirty, err := r.deps.Channels.DirtyActiveChannelsForUser(ctx, userID, sinceDate, afterChannelID, domain.MaxChannelDifferenceLimit)
		if err != nil || len(dirty) == 0 {
			break
		}
		channelIDs := make([]int64, 0, len(dirty))
		for _, item := range dirty {
			if item.ChannelID != 0 {
				channelIDs = append(channelIDs, item.ChannelID)
			}
		}
		viewsByID := make(map[int64]domain.ChannelView, len(channelIDs))
		if len(channelIDs) != 0 {
			if views, err := r.deps.Channels.GetChannels(ctx, userID, channelIDs); err == nil {
				for _, view := range views {
					if view.Channel.ID != 0 {
						viewsByID[view.Channel.ID] = view
					}
				}
			}
		}
		for _, item := range dirty {
			if item.ChannelID == 0 {
				continue
			}
			nudge := domain.ChannelDifferenceNudge{ChannelID: item.ChannelID, Pts: item.Pts}
			if view, ok := viewsByID[item.ChannelID]; ok {
				nudge.Channel = &view
			}
			out = append(out, nudge)
			if item.ChannelID > afterChannelID {
				afterChannelID = item.ChannelID
			}
		}
		if len(dirty) < domain.MaxChannelDifferenceLimit {
			break
		}
	}
	return out
}

// maybeMarkSessionReceivesUpdates 把已登录连接发出的裸 RPC（未包 invokeWithoutUpdates）
// 视为该 session 的 updates 接收声明，对齐官方语义：客户端只在主连接上发裸请求，
// media/temp 连接一律带 invokeWithoutUpdates 包装。已在接收的 session 直接短路，
// 避免每条 RPC 重复同步 channel membership。
func (r *Router) maybeMarkSessionReceivesUpdates(ctx context.Context) {
	if invokeWithoutUpdatesFrom(ctx) {
		return
	}
	userID, ok := UserIDFrom(ctx)
	if !ok {
		return
	}
	if provider, ok := r.deps.Sessions.(SessionUpdatesStateProvider); ok {
		rawAuthKeyID, okRaw := RawAuthKeyIDFrom(ctx)
		sessionID, okSess := SessionIDFrom(ctx)
		if okRaw && okSess && provider.ReceivesUpdatesForAuthKey(rawAuthKeyID, sessionID) {
			return
		}
	}
	r.markSessionReceivesUpdates(ctx, userID)
}

func (r *Router) markSessionReceivesUpdates(ctx context.Context, userID int64) {
	if r.deps.Sessions == nil {
		return
	}
	r.syncSessionChannelMemberships(ctx, userID)
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	if scoped, ok := r.scopedSessions(); ok {
		if rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx); ok {
			scoped.SetReceivesUpdatesForAuthKey(rawAuthKeyID, sessionID, true)
			return
		}
	}
	r.deps.Sessions.SetReceivesUpdates(sessionID, true)
}

func ptr[T any](v T) *T { return &v }
