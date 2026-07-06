package rpc

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
)

// 屏幕共享（presentation）：同一参与者的第二条媒体连接（独立 ICE/DTLS/ssrc 集），
// TL 层落在同一 participant 行的 presentation 字段。硬契约（读客户端源码定）：
//   - 响应 Updates 里的 updateGroupCallConnection 必须置 presentation flag
//     （bit0），否则被主实例误食；DrKLO 只在响应里就地找它，不能只走推送；
//   - presentation 字段必须带 audio_source：DrKLO 用它做屏幕 mySource 并塞进
//     checkGroupCall，缺失会退化取首个视频 ssrc 致 4s 循环重建；
//   - 主连接 rejoin / 整体离会从不补发 leaveGroupCallPresentation：join 整体
//     替换 video_json 时清 presentation_json，SFU 侧主 endpoint 拆除联动拆屏幕。

func (r *Router) onPhoneJoinGroupCallPresentation(ctx context.Context, req *tg.PhoneJoinGroupCallPresentationRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.call.Active() {
		return nil, groupCallAlreadyDiscardedErr()
	}
	// 前置：必须已通过主 join 在会（客户端也只在主 join ack 后才发本 RPC）。
	self, found, err := r.deps.GroupCalls.Participant(ctx, scope.call.ID, scope.userID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || self.Left {
		return nil, groupCallJoinMissingErr()
	}
	offer, ssrc, err := parseGroupCallJoinPayload(req.Params.Data)
	if err != nil {
		r.log.Warn("group call presentation payload", zap.Error(err))
		return nil, groupCallInvalidErr()
	}
	endpoint := groupCallEndpointID(sfu.EndpointPresentation, offer.AudioSSRC)
	state := participantVideoState{
		Endpoint:     endpoint,
		SourceGroups: groupCallSsrcGroupsFromOffer(offer),
		Active:       true, // joinGroupCallPresentation 即开始共享（无 video_stopped 概念）
		AudioSource:  ssrc,
	}
	// 媒体面先行：独立第二 endpoint（重复 join 替换式幂等——rejoinPresentation
	// 会带同一套 ufrag/ssrc 重发）。
	sfuService := r.deps.SFU
	if sfuService == nil {
		sfuService = sfu.Disabled()
	}
	answer, err := sfuService.Join(ctx, scope.call.ID, scope.userID, sfu.EndpointPresentation, offer)
	if err != nil {
		r.log.Warn("group call presentation sfu join", zap.Error(err))
		return nil, internalErr()
	}
	raw := encodeVideoState(state)
	update := domain.GroupCallParticipantUpdate{PresentationJSON: &raw, Now: int(r.clock.Now().Unix())}
	mut, changed, err := r.deps.GroupCalls.UpdateParticipant(ctx, scope.call.ID, scope.userID, update)
	if err != nil {
		_ = sfuService.Leave(ctx, scope.call.ID, scope.userID, sfu.EndpointPresentation)
		return nil, groupCallErr(err)
	}
	params, err := buildGroupCallConnectionParams(answer, endpoint)
	if err != nil {
		_ = sfuService.Leave(ctx, scope.call.ID, scope.userID, sfu.EndpointPresentation)
		return nil, internalErr()
	}
	var channel domain.Channel
	if changed {
		channel = r.groupCallMutationFanout(ctx, scope.channel, mut)
	} else {
		// 同参数重放（幂等 rejoin）：无状态变化也要返回完整连接参数。
		channel = scope.channel
		mut = domain.GroupCallMutation{Call: scope.call, Participant: self}
	}
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{scope.userID})
	conn := &tg.UpdateGroupCallConnection{Params: tg.DataJSON{Data: params}}
	conn.SetPresentation(true)
	out.Updates = append(out.Updates, conn)
	return out, nil
}

func (r *Router) onPhoneLeaveGroupCallPresentation(ctx context.Context, call tg.InputGroupCallClass) (tg.UpdatesClass, error) {
	scope, err := r.groupCallScopeFrom(ctx, call)
	if err != nil {
		return nil, err
	}
	if r.deps.SFU != nil {
		_ = r.deps.SFU.Leave(ctx, scope.call.ID, scope.userID, sfu.EndpointPresentation)
	}
	// 幂等快照：未在共享/已清理时返回当前态而非报错（屏幕共享既已下架即达成目的）。
	idempotentSnapshot := func() (tg.UpdatesClass, error) {
		return r.groupCallUpdateContainer(ctx, scope.userID, scope.channel,
			groupCallUpdateFor(scope.channel, scope.call, scope.userID, scope.canManage(), r.cfg.PublicBaseURL), nil), nil
	}
	self, found, err := r.deps.GroupCalls.Participant(ctx, scope.call.ID, scope.userID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || self.Left || len(self.PresentationJSON) == 0 {
		return idempotentSnapshot()
	}
	empty := []byte(nil)
	mut, changed, err := r.deps.GroupCalls.UpdateParticipant(ctx, scope.call.ID, scope.userID,
		domain.GroupCallParticipantUpdate{PresentationJSON: &empty, Now: int(r.clock.Now().Unix())})
	if err != nil {
		// 与并发的 leaveGroupCall/discard 撞车：presentation 随主连接离会被级联清掉后，
		// 本显式 leave 在写时发现 participant 已 Left（ErrGroupCallNotJoined）或 call 已
		// discarded。屏幕共享既已下架，按幂等成功返回快照而非 GROUPCALL_JOIN_MISSING 400
		// （客户端此刻正同时离会，会忽略该 400，但报错污染日志且非正确语义）。
		if errors.Is(err, domain.ErrGroupCallNotJoined) || errors.Is(err, domain.ErrGroupCallDiscarded) {
			return idempotentSnapshot()
		}
		return nil, groupCallErr(err)
	}
	if !changed {
		mut = domain.GroupCallMutation{Call: scope.call, Participant: self}
	}
	channel := scope.channel
	if changed {
		channel = r.groupCallMutationFanout(ctx, scope.channel, mut)
	}
	// 其他端靠本行 presentation 字段消失把屏幕画面下架。
	return r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{scope.userID}), nil
}
