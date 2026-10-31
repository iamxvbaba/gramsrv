package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Group-call push strategy (updateGroupCall / updateGroupCallParticipants / connection all carry no
// pts and do not enter getDifference): the fan-out target is uniformly "online group members"
// (OnlineChannelMemberUserIDs, capped by MaxChannelRealtimeFanout) — covering panel observers who
// have not joined (the participant list behind the Android banner tap refreshes incrementally from
// it). Offline consistency, three tricks: the service message carries channel pts so it can be
// caught up; the banner relies on the channel.call_active/call_not_empty flags (rebuilt by fetching
// dialogs/getFullChannel); in-room state self-heals via checkGroupCall and a version jump that
// triggers a full getGroupParticipants reload.

// groupCallOnlineRecipients returns online group members (including originUserID themselves; on
// push, pushUserMessage's ctx except excludes the originating session but keeps other devices).
// Online index candidates must pass a PG active recheck (the same depth as channelFanoutRecipients):
// when the index is stale (e.g. a kick window), the call state/participant list must not keep being
// pushed to those who are no longer members.
func (r *Router) groupCallOnlineRecipients(ctx context.Context, channelID int64) []int64 {
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return nil
	}
	online := provider.OnlineChannelMemberUserIDs(channelID, domain.MaxChannelRealtimeFanout)
	if len(online) == 0 || r.deps.Channels == nil {
		return online
	}
	active, err := r.deps.Channels.FilterActiveMemberIDs(ctx, channelID, online)
	if err != nil {
		// If the recheck fails, prefer an under-push to a wrong push: group-call updates carry no
		// pts, and the client self-heals via checkGroupCall / a version jump, so a missed push does
		// not lose consistency.
		return nil
	}
	return active
}

// groupCallUpdateContainer wraps a single update in a viewer-perspective Updates container.
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

// pushGroupCallUpdate pushes updateGroupCall (a call row change) to online group members.
// A scheduled call needs a per-viewer backfill of schedule_start_subscribed: TDesktop's
// applyCallFields overwrites the local flag unconditionally, so omitting it silently switches the
// subscriber's "start reminder" toggle off.
func (r *Router) pushGroupCallUpdate(ctx context.Context, channel domain.Channel, call domain.GroupCall) {
	if call.Conference() {
		r.pushConferenceGroupCallUpdate(ctx, call)
		return
	}
	var subscribed map[int64]struct{}
	if call.ScheduleDate > 0 {
		if ids, err := r.deps.GroupCalls.ScheduleSubscriberIDs(ctx, call.ID); err == nil {
			subscribed = make(map[int64]struct{}, len(ids))
			for _, id := range ids {
				subscribed[id] = struct{}{}
			}
		}
	}
	recipients := r.groupCallOnlineRecipients(ctx, channel.ID)
	for _, viewerID := range recipients {
		viewerCall := call
		if subscribed != nil {
			_, viewerCall.ScheduleStartSubscribed = subscribed[viewerID]
		}
		update := &tg.UpdateGroupCall{Call: tgGroupCall(viewerCall, viewerID, false, r.cfg.PublicBaseURL)}
		update.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
		r.pushUserMessage(ctx, viewerID, "group call update",
			r.groupCallUpdateContainer(ctx, viewerID, channel, update, []int64{call.CreatorUserID}))
	}
}

// pushGroupCallParticipantsUpdate pushes the participant increment (version=N) to online group
// members. Built separately per viewer: the participant.Self flag is per-viewer.
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
	recipients := r.groupCallOnlineRecipients(ctx, channel.ID)
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
		update := &tg.UpdateGroupCall{Call: tgGroupCall(call, viewerID, viewerID == call.CreatorUserID, r.cfg.PublicBaseURL)}
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

// pushGroupCallServiceMessage pushes started/ended/invite service messages (with channel pts) to
// active members (res.Recipients). Reuses channelOperationUpdates' per-viewer construction.
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

// groupCallMutationFanout is the unified fan-out after a participant-dimension change: the
// participants increment + a channel-dimension refresh when call_not_empty flips (the Android
// banner depends more heavily on the flag).
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
