package rpc

import (
	"context"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
)

// GroupCallSweepDispatcher cleans up ghost participants of group calls (nobody
// sends leave after a killed process / power loss).
//
// ⚠ P0-2 liveness contract: the client only sends checkGroupCall in the
// Connecting state (the heartbeat stops once media is connected), so judging
// death by last_check_date alone would kick every healthy participant that has
// connected media. The agreed criterion is "max(heartbeat, media-plane
// liveness)": with M0 (SFU disabled) the client stays Connecting and its 4s
// heartbeat never stops, so a pure watermark holds; from M1 on the SFU must
// periodically refresh last_check_date (liveness reporter) for endpoints alive
// on the media plane (ICE consent / recent SRTP reception), so a single
// watermark carries both liveness sources.
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

// channelForGroupCall fetches the channel row owning the call (used for push hydration).
func (r *Router) channelForGroupCall(ctx context.Context, call domain.GroupCall) (domain.Channel, error) {
	view, err := r.deps.Channels.GetChannel(ctx, call.CreatorUserID, call.ChannelID)
	if err != nil {
		return domain.Channel{}, err
	}
	return view.Channel, nil
}
