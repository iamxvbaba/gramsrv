package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

// registerUpdates 注册 updates.* RPC handler。
func (r *Router) registerUpdates(d *tlprofile.Dispatcher) {
	registerRPC[*tg.UpdatesGetStateRequest](d, tlprofile.SemanticMethodUpdatesGetState, func(ctx context.Context, layerRequest *tg.UpdatesGetStateRequest) (

		// onUpdatesGetState 处理 updates.getState。TDesktop 与 DrKLO 的启动路径把它当作
		// 「从当前快照开始同步」的显式 baseline：返回账号当前连续水位，并且只在 rpc_result
		// 物理交付后原子推进该设备 confirmed + observed。对尚未审计 baseline 语义的客户端
		// 仍返回同一 current state，但交付后只推进 confirmed；这保留 observed/durable
		// difference tail，避免把 TDesktop/DrKLO 的兼容例外扩散成所有客户端都能跨过未实际
		// 确认事件的 retention 后门。
		any, error) {
		return r.onUpdatesGetState(ctx)
	})
	registerRPC[*tg.UpdatesGetDifferenceRequest](d, tlprofile.SemanticMethodUpdatesGetDifference, func(ctx context.Context, layerRequest *tg.UpdatesGetDifferenceRequest) (any, error) {
		return r.onUpdatesGetDifference(ctx, layerRequest)
	})
}

func (r *Router) onUpdatesGetState(ctx context.Context) (*tg.UpdatesState, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Updates == nil {
		r.stageUpdatesBaselineAfterDelivery(ctx, userID, nil, 0, nil, false)
		return &tg.UpdatesState{Date: int(r.clock.Now().Unix()), Qts: r.deviceEncryptedQts(ctx)}, nil
	}
	st, err := r.deps.Updates.CurrentState(ctx, userID)
	mode := domain.UpdateStateCommitDeliveredOnly
	if getStateEstablishesObservedBaseline(ctx) {
		mode = domain.UpdateStateCommitDeliveredAndObservedBaseline
	} else if err == nil {
		r.log.Warn("updates.getState returned current snapshot without advancing observed baseline for client without audited baseline policy",
			r.contextLogFields(ctx)...)
	}
	if err != nil {
		return nil, internalErr()
	}
	// 密聊 qts 是设备级、独立于账号级 pts 引擎：注入当前设备已分配的 qts（无密聊设备为 0）。
	st.Qts = r.deviceEncryptedQts(ctx)
	r.stageUpdatesBaselineAfterDelivery(ctx, userID, &st, mode, nil, true)
	out := tgUpdateState(st)
	return ptr(out), nil
}

func getStateEstablishesObservedBaseline(ctx context.Context) bool {
	switch ClientTypeFrom(ctx) {
	case ClientTypeTDesktop, ClientTypeAndroid:
		return true
	default:
		return false
	}
}

func (r *Router) onUpdatesGetDifference(ctx context.Context, req *tg.UpdatesGetDifferenceRequest) (tg.UpdatesDifferenceClass, error) {
	id, _ := AuthKeyIDFrom(ctx)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Updates == nil {
		now := int(r.clock.Now().Unix())
		r.stageUpdatesBaselineAfterDelivery(ctx, userID, nil, 0, nil, false)
		return &tg.UpdatesDifferenceEmpty{Date: now}, nil
	}
	from, err := r.deps.Updates.ObserveDifferenceRequest(ctx, id, userID, domain.UpdateState{
		Pts:  req.Pts,
		Qts:  req.Qts,
		Date: req.Date,
	})
	if err != nil {
		return nil, internalErr()
	}
	// pts_total_limit 是客户端显式请求的 fast-skip：差距超限时返回
	// differenceTooLong{pts}，客户端据此整体重置会话列表而不是串行翻
	// 上千页 slice。不传该参数的客户端（TDesktop）永远不会收到 tooLong。
	if limit, ok := req.GetPtsTotalLimit(); ok && limit > 0 && req.Pts > 0 {
		current, err := r.deps.Updates.CurrentState(ctx, userID)
		if err == nil && current.Pts-from.Pts > limit {
			// differenceTooLong carries only the replacement pts. Preserve the
			// request-proven qts/date instead of over-confirming fields that were
			// not present on wire.
			returnedCursor := from
			returnedCursor.Pts = current.Pts
			r.stageUpdatesBaselineAfterDelivery(ctx, userID, &returnedCursor, domain.UpdateStateCommitDeliveredOnly, nil, false)
			return &tg.UpdatesDifferenceTooLong{Pts: current.Pts}, nil
		}
	}
	st, err := r.deps.Updates.GetDifference(ctx, id, userID, from)
	if err != nil {
		return nil, internalErr()
	}
	st.ChannelNudges = r.accountChannelDifferenceNudges(ctx, userID, req.Date)
	// 密聊设备级 qts 消息（独立于账号级 pts 事件）：按当前设备 req.Qts 补回。
	encMsgs, newQts := r.encryptedDifference(ctx, req.Qts)
	// 密聊握手/已读状态事件（无 qts）：按未投递标记补回 OtherUpdates。
	stateUpdates, statePeerUserIDs, stateEventIDs := r.encryptedStateUpdates(ctx, userID)
	if !st.Partial && len(st.Events) == 0 && len(st.ChannelNudges) == 0 && len(encMsgs) == 0 && len(stateUpdates) == 0 {
		// differenceEmpty carries no pts/qts. Both audited clients retain their
		// request cursor, so only that normalized cursor is proven delivered.
		emptyCursor := domain.UpdateState{Pts: from.Pts, Qts: from.Qts, Date: st.State.Date, Seq: st.State.Seq}
		r.stageUpdatesBaselineAfterDelivery(ctx, userID, &emptyCursor, domain.UpdateStateCommitDeliveredOnly, nil, true)
		return &tg.UpdatesDifferenceEmpty{Date: st.State.Date, Seq: st.State.Seq}, nil
	}
	st.Events = r.enrichUpdateEvents(ctx, userID, st.Events)
	diff := r.tgUpdatesDifference(ctx, userID, st)
	diff = injectEncryptedMessages(diff, encMsgs, newQts)
	diff = r.injectEncryptedOtherUpdates(ctx, userID, diff, stateUpdates, statePeerUserIDs)
	returnedCursor := st.State
	returnedCursor.Qts = newQts
	r.stageUpdatesBaselineAfterDelivery(ctx, userID, &returnedCursor, domain.UpdateStateCommitDeliveredOnly, stateEventIDs, true)
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
// media/temp 连接一律带 invokeWithoutUpdates 包装。这里只登记交付计划；membership
// sync、SetReceivesUpdates 与 pending flush 必须等成功 rpc_result 物理交付后才执行。
// 已在接收的 session 直接短路，避免每条 RPC 重复同步 channel membership。
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
	r.stageSessionUpdatesReadyAfterDelivery(ctx, userID)
}

func (r *Router) markSessionReceivesUpdatesNow(ctx context.Context, userID int64) {
	if r.deps.Sessions == nil {
		return
	}
	r.syncSessionChannelMemberships(ctx, userID)
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	r.deps.Sessions.SetReceivesUpdatesForAuthKey(rawAuthKeyIDForOrigin(ctx), sessionID, true)
}

func ptr[T any](v T) *T { return &v }
