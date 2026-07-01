package rpc

import (
	"context"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
)

// GroupCallSweepDispatcher 清理群通话幽灵参与者（kill 进程/断电后无人发 leave）。
//
// ⚠ P0-2 活性契约：客户端只在 Connecting 态发 checkGroupCall（媒体连通后心跳停止），
// 单凭 last_check_date 判死会把所有连上媒体的健康参与者踢光。约定的判据是
// 「max(心跳, 媒体面活性)」：M0（SFU disabled）客户端恒 Connecting、4s 心跳不断，
// 纯水位成立；M1 起 SFU 必须对媒体面存活（ICE consent/近期 SRTP 收包）的 endpoint
// 周期性刷新 last_check_date（liveness reporter），使单一水位同时承载两路活性。
type GroupCallSweepDispatcher struct {
	router   *Router
	log      *zap.Logger
	interval time.Duration
	checkTTL time.Duration
	batch    int
}

func NewGroupCallSweepDispatcher(router *Router, log *zap.Logger, interval, checkTTL time.Duration) *GroupCallSweepDispatcher {
	if log == nil {
		log = zap.NewNop()
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if checkTTL <= 0 {
		checkTTL = 45 * time.Second
	}
	return &GroupCallSweepDispatcher{router: router, log: log, interval: interval, checkTTL: checkTTL, batch: 100}
}

func (d *GroupCallSweepDispatcher) Run(ctx context.Context) {
	if d == nil || d.router == nil {
		return
	}
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.DispatchOnce(ctx)
		}
	}
}

func (d *GroupCallSweepDispatcher) DispatchOnce(ctx context.Context) {
	if d == nil || d.router == nil || d.router.deps.GroupCalls == nil {
		return
	}
	now := d.router.clock.Now()
	cutoff := int(now.Add(-d.checkTTL).Unix())
	muts, err := d.router.deps.GroupCalls.SweepStale(ctx, cutoff, int(now.Unix()), d.batch)
	if err != nil {
		d.log.Warn("sweep group call participants", zap.Error(err))
		return
	}
	for _, mut := range muts {
		if d.router.deps.SFU != nil {
			_ = d.router.deps.SFU.Leave(ctx, mut.Call.ID, mut.Participant.UserID, sfu.EndpointMain)
		}
		if mut.Call.Conference() {
			d.router.groupCallMutationFanout(ctx, domain.Channel{}, mut)
			d.log.Info("conference call participant swept",
				zap.Int64("call_id", mut.Call.ID),
				zap.Int64("user_id", mut.Participant.UserID),
				zap.String("state", string(mut.Call.State)))
			continue
		}
		channel, err := d.router.channelForGroupCall(ctx, mut.Call)
		if err != nil {
			continue
		}
		d.router.groupCallMutationFanout(ctx, channel, mut)
		d.log.Info("group call participant swept",
			zap.Int64("call_id", mut.Call.ID),
			zap.Int64("user_id", mut.Participant.UserID))
	}
}

// channelForGroupCall 取通话所属频道行（推送 hydration 用）。
func (r *Router) channelForGroupCall(ctx context.Context, call domain.GroupCall) (domain.Channel, error) {
	view, err := r.deps.Channels.GetChannel(ctx, call.CreatorUserID, call.ChannelID)
	if err != nil {
		return domain.Channel{}, err
	}
	return view.Channel, nil
}
