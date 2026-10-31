package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// editGroupCallParticipant permission matrix (full M2 version):
//
//	operation    self→self                 member→other           admin/creator→other
//	muted=true   muted, can_self_unmute    overrides.muted_by_you  muted_by_admin (mute)
//	muted=false  only when can_self_unmute  clear overrides        allow to speak (clear muted_by_admin + raised hand)
//	volume       ignored                   overrides.volume        volume_by_admin (everyone)
//	raise_hand   rating = global monotonic counter  ✗             admin lowers another's hand
//
// ⚠ P1-5: self's video_stopped/video_paused are always accepted/forwarded-ignored, never returning
// an error (a failed edit risks a rejoin storm on the client).
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

// editSelfParticipant handles the self dimension (free mute toggling, raised hand, video
// on/off/pause).
// ⚠ P1-5: a self-edit on the video/presentation dimension never returns an error (a failed edit
// risks a rejoin storm on the client; an idempotent replay also returns a successful empty Updates
// rather than GROUPCALL_NOT_MODIFIED).
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
			// When suppressed by an admin the user cannot unmute themselves (the client only sends
			// raise_hand at this point).
			return nil, groupCallForbiddenErr()
		}
		update.Muted = &muted
	}
	videoEdit := false
	// video_stopped: toggles the appearance/disappearance of the video field. The endpoint and
	// source_groups come from the archive made at join (toggling the camera does not re-send join);
	// a missing archive (a very old session) is silently tolerated.
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
	// video_paused: keep the video field and set paused (the viewer renders a placeholder).
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

// adminEditParticipant handles admin->other (mute/allow to speak/global volume/lower raised hand).
func (r *Router) adminEditParticipant(ctx context.Context, scope *groupCallScope, req *tg.PhoneEditGroupCallParticipantRequest, target domain.GroupCallParticipant) (tg.UpdatesClass, error) {
	update := domain.GroupCallParticipantUpdate{Now: int(r.clock.Now().Unix())}
	if muted, ok := req.GetMuted(); ok {
		mutedByAdmin := muted
		update.MutedByAdmin = &mutedByAdmin
		if muted {
			// Mute: muted=true and can_self_unmute=false.
			update.Muted = &muted
		} else {
			// "Allow to speak": only restore can_self_unmute and clear the raised hand; it does not
			// unmute on the user's behalf (official semantics: the user is still muted after
			// receiving it and must unmute themselves).
			zero := int64(0)
			update.RaiseHandRating = &zero
		}
	}
	if raise, ok := req.GetRaiseHand(); ok && !raise {
		// Admin lowers another's raised hand.
		zero := int64(0)
		update.RaiseHandRating = &zero
	}
	if volume, ok := req.GetVolume(); ok {
		update.VolumeByAdmin = &volume
	}
	return r.applyParticipantMutation(ctx, scope, target.UserID, update)
}

// memberOverrideParticipant handles member->other (local mute/local volume, per-viewer, visible
// only to the setter themselves, not advancing the whole-room version).
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
	// per-viewer: push only to the setter themselves (all their devices, with the min flag to avoid
	// overwriting local state).
	target, found, err := r.deps.GroupCalls.Participant(ctx, scope.call.ID, targetID)
	if err != nil || !found {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	row := applyParticipantOverride(tgGroupCallParticipant(target, scope.userID), ov)
	update := &tg.UpdateGroupCallParticipants{
		Call:         &tg.InputGroupCall{ID: scope.call.ID, AccessHash: scope.call.AccessHash},
		Participants: []tg.GroupCallParticipant{row},
		Version:      scope.call.Version, // overrides do not advance version
	}
	out := r.groupCallUpdateContainer(ctx, scope.userID, scope.channel, update, []int64{targetID})
	r.pushUserMessage(ctx, scope.userID, "group call override", out)
	return out, nil
}

// applyParticipantMutation persists and fans out a field-level update on the whole-room dimension
// (version++).
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
		// Out-of-scope switches such as reset_invite_hash / messages_enabled: no change semantics.
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

// onPhoneInviteToGroupCall sends a messageActionInviteToGroupCall service message (with channel
// pts); the invitee receives a tappable join card as a normal channel message. It does not pull
// anyone into the call directly.
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
		// The target must be a member of this group.
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

// groupCallParticipantTarget resolves editGroupCallParticipant's target (user peers only).
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
