package rpc

import (
	"context"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// editGroupCallParticipant 权限矩阵（M2 完整版）：
//
//	操作          自己→自己                普通成员→他人          admin/creator→他人
//	muted=true   muted, can_self_unmute   overrides.muted_by_you  muted_by_admin（禁言）
//	muted=false  仅当 can_self_unmute       清 overrides           允许发言（清 muted_by_admin+举手）
//	volume       忽略                      overrides.volume        volume_by_admin（全员）
//	raise_hand   rating=全局单调序号        ✗                      admin 放下他人手
//
// ⚠ P1-5：self 的 video_stopped/video_paused 一律接受/忽略转发，绝不返回错误
// （edit 失败在客户端有 rejoin 风暴风险）。
func (r *Router) onPhoneEditGroupCallParticipant(ctx context.Context, req *tg.PhoneEditGroupCallParticipantRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	targetID, err := r.groupCallParticipantTarget(ctx, scope.userID, req.Participant)
	if err != nil {
		return nil, err
	}
	target, found, err := r.deps.GroupCalls.Participant(ctx, scope.call.ID, targetID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || target.Left {
		return nil, groupCallJoinMissingErr()
	}

	switch {
	case targetID == scope.userID:
		return r.editSelfParticipant(ctx, scope, req, target)
	case scope.canManage():
		return r.adminEditParticipant(ctx, scope, req, target)
	default:
		return r.memberOverrideParticipant(ctx, scope, req, targetID)
	}
}

// editSelfParticipant 处理 self 维度（mute 自由翻转、举手、视频开关/暂停）。
// ⚠ P1-5：video/presentation 维度的 self-edit 绝不返回错误（edit 失败有客户端
// rejoin 风暴风险；幂等重放也回成功空 Updates 而非 GROUPCALL_NOT_MODIFIED）。
func (r *Router) editSelfParticipant(ctx context.Context, scope *groupCallScope, req *tg.PhoneEditGroupCallParticipantRequest, self domain.GroupCallParticipant) (tg.UpdatesClass, error) {
	update := domain.GroupCallParticipantUpdate{Now: int(r.clock.Now().Unix())}
	if raise, ok := req.GetRaiseHand(); ok {
		if raise {
			rating, err := r.deps.GroupCalls.NextRaiseHandRating(ctx, scope.call.ID)
			if err != nil {
				return nil, internalErr()
			}
			update.RaiseHandRating = &rating
		} else {
			zero := int64(0)
			update.RaiseHandRating = &zero
		}
	}
	if muted, ok := req.GetMuted(); ok {
		if self.MutedByAdmin && !muted {
			// 被管理员压制时不能自行 unmute（客户端此时只会发 raise_hand）。
			return nil, groupCallForbiddenErr()
		}
		update.Muted = &muted
	}
	videoEdit := false
	// video_stopped：翻转 video 字段的出现/消失。endpoint 与 source_groups 来自
	// join 时的存档（开关摄像头不重发 join），缺档（极老会话）静默容忍。
	if stopped, ok := req.GetVideoStopped(); ok {
		videoEdit = true
		if st, found := decodeVideoState(self.VideoJSON); found {
			st.Active = !stopped && len(st.SourceGroups) > 0
			if !st.Active {
				st.Paused = false
			}
			raw := encodeVideoState(st)
			update.VideoJSON = &raw
		}
	}
	// video_paused：video 字段保留、置 paused（观看端渲染占位）。
	if paused, ok := req.GetVideoPaused(); ok {
		videoEdit = true
		if st, found := decodeVideoState(self.VideoJSON); found && st.Active {
			st.Paused = paused
			raw := encodeVideoState(st)
			update.VideoJSON = &raw
		}
	}
	if paused, ok := req.GetPresentationPaused(); ok {
		videoEdit = true
		if st, found := decodeVideoState(self.PresentationJSON); found && st.Active {
			st.Paused = paused
			raw := encodeVideoState(st)
			update.PresentationJSON = &raw
		}
	}
	mut, changed, err := r.deps.GroupCalls.UpdateParticipant(ctx, scope.call.ID, scope.userID, update)
	if err != nil {
		if videoEdit {
			return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
		}
		return nil, groupCallErr(err)
	}
	if !changed {
		if videoEdit {
			return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
		}
		return nil, groupCallNotModifiedErr()
	}
	channel := r.groupCallMutationFanout(ctx, scope.channel, mut)
	return r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{scope.userID}), nil
}

// adminEditParticipant 处理 admin→他人（禁言/允许发言/全局音量/放下举手）。
func (r *Router) adminEditParticipant(ctx context.Context, scope *groupCallScope, req *tg.PhoneEditGroupCallParticipantRequest, target domain.GroupCallParticipant) (tg.UpdatesClass, error) {
	update := domain.GroupCallParticipantUpdate{Now: int(r.clock.Now().Unix())}
	if muted, ok := req.GetMuted(); ok {
		mutedByAdmin := muted
		update.MutedByAdmin = &mutedByAdmin
		if muted {
			// 禁言：muted=true 且 can_self_unmute=false。
			update.Muted = &muted
		} else {
			// "允许发言"：只恢复 can_self_unmute 并清举手，不替用户开麦
			//（官方语义：用户收到后仍是 muted，需自行 unmute）。
			zero := int64(0)
			update.RaiseHandRating = &zero
		}
	}
	if raise, ok := req.GetRaiseHand(); ok && !raise {
		// admin 放下他人举手。
		zero := int64(0)
		update.RaiseHandRating = &zero
	}
	if volume, ok := req.GetVolume(); ok {
		update.VolumeByAdmin = &volume
	}
	return r.applyParticipantMutation(ctx, scope, target.UserID, update)
}

// memberOverrideParticipant 处理普通成员→他人（本地静音/本地音量，per-viewer，
// 仅 setter 自己可见，不进全房间 version）。
func (r *Router) memberOverrideParticipant(ctx context.Context, scope *groupCallScope, req *tg.PhoneEditGroupCallParticipantRequest, targetID int64) (tg.UpdatesClass, error) {
	ov, _, err := r.deps.GroupCalls.ParticipantOverride(ctx, scope.call.ID, scope.userID, targetID)
	if err != nil {
		return nil, internalErr()
	}
	changed := false
	if muted, ok := req.GetMuted(); ok {
		if ov.MutedByYou != muted {
			ov.MutedByYou = muted
			changed = true
		}
	}
	if volume, ok := req.GetVolume(); ok {
		if ov.Volume != volume {
			ov.Volume = volume
			changed = true
		}
	}
	if !changed {
		return nil, groupCallNotModifiedErr()
	}
	clear := !ov.MutedByYou && ov.Volume == 0
	if err := r.deps.GroupCalls.SetParticipantOverride(ctx, scope.call.ID, scope.userID, targetID, ov, clear); err != nil {
		return nil, internalErr()
	}
	// per-viewer：只推给 setter 自己（其全部设备，带 min flag 防覆盖本地状态）。
	target, found, err := r.deps.GroupCalls.Participant(ctx, scope.call.ID, targetID)
	if err != nil || !found {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	row := applyParticipantOverride(tgGroupCallParticipant(target, scope.userID), ov)
	update := &tg.UpdateGroupCallParticipants{
		Call:         &tg.InputGroupCall{ID: scope.call.ID, AccessHash: scope.call.AccessHash},
		Participants: []tg.GroupCallParticipant{row},
		Version:      scope.call.Version, // overrides 不推进 version
	}
	out := r.groupCallUpdateContainer(ctx, scope.userID, scope.channel, update, []int64{targetID})
	r.pushUserMessage(ctx, scope.userID, "group call override", out)
	return out, nil
}

// applyParticipantMutation 把全房间维度的字段级更新落库并扇出（version++）。
func (r *Router) applyParticipantMutation(ctx context.Context, scope *groupCallScope, targetID int64, update domain.GroupCallParticipantUpdate) (tg.UpdatesClass, error) {
	mut, changed, err := r.deps.GroupCalls.UpdateParticipant(ctx, scope.call.ID, targetID, update)
	if err != nil {
		return nil, groupCallErr(err)
	}
	if !changed {
		return nil, groupCallNotModifiedErr()
	}
	channel := r.groupCallMutationFanout(ctx, scope.channel, mut)
	return r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{targetID}), nil
}

func (r *Router) onPhoneEditGroupCallTitle(ctx context.Context, req *tg.PhoneEditGroupCallTitleRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.canManage() {
		return nil, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	call, changed, err := r.deps.GroupCalls.SetTitle(ctx, scope.call.ID, req.Title)
	if err != nil {
		return nil, groupCallErr(err)
	}
	if !changed {
		return nil, groupCallNotModifiedErr()
	}
	r.pushGroupCallUpdate(ctx, scope.channel, call)
	return r.groupCallUpdateContainer(ctx, scope.userID, scope.channel,
		groupCallUpdateFor(scope.channel, call, scope.userID, true, r.cfg.PublicBaseURL), nil), nil
}

func (r *Router) onPhoneToggleGroupCallSettings(ctx context.Context, req *tg.PhoneToggleGroupCallSettingsRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.canManage() {
		return nil, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	joinMuted, ok := req.GetJoinMuted()
	if !ok {
		// reset_invite_hash / messages_enabled 等范围外开关：无变化语义。
		return nil, groupCallNotModifiedErr()
	}
	call, changed, err := r.deps.GroupCalls.SetJoinMuted(ctx, scope.call.ID, joinMuted)
	if err != nil {
		return nil, groupCallErr(err)
	}
	if !changed {
		return nil, groupCallNotModifiedErr()
	}
	r.pushGroupCallUpdate(ctx, scope.channel, call)
	return r.groupCallUpdateContainer(ctx, scope.userID, scope.channel,
		groupCallUpdateFor(scope.channel, call, scope.userID, true, r.cfg.PublicBaseURL), nil), nil
}

const maxInviteToGroupCallUsers = 10

// onPhoneInviteToGroupCall 发 messageActionInviteToGroupCall 服务消息（带频道 pts），
// 被邀请者通过正常频道消息收到可点入会卡片；不直接拉人入会。
func (r *Router) onPhoneInviteToGroupCall(ctx context.Context, req *tg.PhoneInviteToGroupCallRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if scope.call.Conference() {
		return nil, notImplementedErr()
	}
	if len(req.Users) == 0 || len(req.Users) > maxInviteToGroupCallUsers {
		return nil, limitInvalidErr()
	}
	targetIDs := make([]int64, 0, len(req.Users))
	for _, in := range req.Users {
		u, found, err := r.userFromInput(ctx, scope.userID, in)
		if err != nil {
			return nil, internalErr()
		}
		if !found || u.ID == 0 {
			return nil, userIDInvalidErr()
		}
		// 目标必须是本群成员。
		member, err := r.deps.Channels.GetParticipant(ctx, scope.userID, scope.channel.ID, u.ID)
		if err != nil || member.Status != domain.ChannelMemberActive {
			return nil, tgerr400("USER_NOT_PARTICIPANT")
		}
		targetIDs = append(targetIDs, u.ID)
	}
	now := int(r.clock.Now().Unix())
	res, err := r.deps.Channels.AppendCallServiceMessage(ctx, scope.channel.ID, scope.userID, now, domain.ChannelMessageAction{
		Type:           domain.ChannelActionInviteToGroupCall,
		CallID:         scope.call.ID,
		CallAccessHash: scope.call.AccessHash,
		UserIDs:        targetIDs,
	})
	if err != nil {
		return nil, internalErr()
	}
	r.pushGroupCallServiceMessage(ctx, scope.userID, res)
	out := r.groupCallUpdateContainer(ctx, scope.userID, res.Channel, nil, targetIDs)
	out.Updates = nil
	if msgUpdate := tgChannelUpdate(scope.userID, res.Event); msgUpdate != nil {
		out.Updates = append(out.Updates, msgUpdate)
	}
	return out, nil
}

// groupCallParticipantTarget 解析 editGroupCallParticipant 的目标（仅 user peer）。
func (r *Router) groupCallParticipantTarget(ctx context.Context, currentUserID int64, peer tg.InputPeerClass) (int64, error) {
	switch v := peer.(type) {
	case *tg.InputPeerSelf:
		return currentUserID, nil
	case *tg.InputPeerUser:
		return v.UserID, nil
	default:
		return 0, peerIDInvalidErr()
	}
}
