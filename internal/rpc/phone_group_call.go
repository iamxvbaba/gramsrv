package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
)

// Core RPCs for supergroup voice chats (group calls). The signaling source of truth is
// GroupCallStore (monotonic version); the media plane goes through deps.SFU (M0 is Disabled: pure
// signaling, so the client staying in Connecting is expected).

// groupCallErr maps domain group-call errors to RPC_ERROR.
func groupCallErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrGroupCallInvalid):
		return groupCallInvalidErr()
	case errors.Is(err, domain.ErrGroupCallDiscarded):
		return groupCallAlreadyDiscardedErr()
	case errors.Is(err, domain.ErrGroupCallAlreadyStarted):
		return groupCallAlreadyStartedErr()
	case errors.Is(err, domain.ErrGroupCallSSRCDuplicate):
		return groupCallSSRCDuplicateErr()
	case errors.Is(err, domain.ErrGroupCallNotJoined):
		return groupCallJoinMissingErr()
	case errors.Is(err, domain.ErrConferenceChainInvalid):
		return confWriteChainInvalidErr()
	default:
		return internalErr()
	}
}

// groupCallScope is the common pre-resolved result for group-call handlers.
type groupCallScope struct {
	userID  int64
	call    domain.GroupCall
	channel domain.Channel
	member  domain.ChannelMember
}

func (s *groupCallScope) canManage() bool {
	if s.call.Conference() {
		return s.userID != 0 && s.userID == s.call.CreatorUserID
	}
	return channelMemberIsAdmin(s.member)
}

// groupCallScopeFrom parses InputGroupCallClass and validates access. Ordinary group calls
// continue through channel membership; conference calls go through the
// creator/participant/invite/slug access model.
func (r *Router) groupCallScopeFrom(ctx context.Context, in tg.InputGroupCallClass) (*groupCallScope, error) {
	if r.deps.GroupCalls == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var call domain.GroupCall
	var found bool
	allowBySlug := false
	switch v := in.(type) {
	case *tg.InputGroupCall:
		call, found, err = r.deps.GroupCalls.Get(ctx, v.ID)
		if err != nil {
			return nil, internalErr()
		}
		if !found || call.AccessHash != v.AccessHash {
			return nil, groupCallInvalidErr()
		}
	case *tg.InputGroupCallSlug:
		call, found, err = r.deps.GroupCalls.GetBySlug(ctx, v.Slug)
		if err != nil {
			return nil, internalErr()
		}
		if !found || !call.Conference() {
			return nil, groupCallInvalidErr()
		}
		allowBySlug = true
	case *tg.InputGroupCallInviteMessage:
		call, _, found, err = r.deps.GroupCalls.GetByInviteMessage(ctx, userID, v.MsgID)
		if err != nil {
			return nil, internalErr()
		}
		if !found || !call.Conference() {
			return nil, groupCallInvalidErr()
		}
	default:
		return nil, groupCallInvalidErr()
	}
	if call.Conference() {
		if !allowBySlug {
			allowed, err := r.conferenceCallCanAccess(ctx, call.ID, userID)
			if err != nil {
				return nil, internalErr()
			}
			if !allowed {
				return nil, groupCallForbiddenErr()
			}
		}
		return &groupCallScope{userID: userID, call: call}, nil
	}
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, call.ChannelID)
	if err != nil {
		return nil, groupCallForbiddenErr()
	}
	if view.Self.Status != domain.ChannelMemberActive {
		return nil, groupCallForbiddenErr()
	}
	return &groupCallScope{userID: userID, call: call, channel: view.Channel, member: view.Self}, nil
}

// groupCallJoinAsChannelID resolves joinGroupCall.join_as: self (default/self) -> 0; the channel
// itself when the viewer is an admin (anonymous admin/creator semantics; TDesktop's RTMP createBox
// hard-codes joinAs=peer for the creator) -> channelID; any other identity returns
// JOIN_AS_PEER_INVALID.
func (r *Router) groupCallJoinAsChannelID(scope *groupCallScope, joinAs tg.InputPeerClass) (int64, error) {
	switch v := joinAs.(type) {
	case nil, *tg.InputPeerSelf, *tg.InputPeerEmpty:
		return 0, nil
	case *tg.InputPeerUser:
		if v.UserID == scope.userID {
			return 0, nil
		}
	case *tg.InputPeerChannel:
		if !scope.call.Conference() && v.ChannelID == scope.channel.ID && channelMemberIsAdmin(scope.member) {
			return v.ChannelID, nil
		}
	}
	return 0, tgerr400("JOIN_AS_PEER_INVALID")
}

// onPhoneGetGroupCallJoinAs returns the selectable join identities: everyone can use themselves;
// a channel admin can additionally use the channel itself (anonymous identity). TDesktop shows a
// "join as" chooser when there is more than one candidate.
func (r *Router) onPhoneGetGroupCallJoinAs(ctx context.Context, peer tg.InputPeerClass) (*tg.PhoneJoinAsPeers, error) {
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.PhoneJoinAsPeers{
		Peers: []tg.PeerClass{&tg.PeerUser{UserID: userID}},
		Chats: []tg.ChatClass{},
		Users: r.tgUsersForIDs(ctx, userID, []int64{userID}),
	}
	finish := func() *tg.PhoneJoinAsPeers {
		r.applyPeerReadModels(ctx, userID, out.Users, out.Chats)
		return out
	}
	if r.deps.Channels == nil {
		return finish(), nil
	}
	dp, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil || dp.Type != domain.PeerTypeChannel || dp.ID == 0 {
		return finish(), nil
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, dp.ID)
	if err != nil || view.Self.Status != domain.ChannelMemberActive || !channelMemberIsAdmin(view.Self) {
		return finish(), nil
	}
	out.Peers = append(out.Peers, &tg.PeerChannel{ChannelID: view.Channel.ID})
	out.Chats = append(out.Chats, tgChannel(userID, view.Channel, &view.Self))
	return finish(), nil
}

func (r *Router) conferenceCallCanAccess(ctx context.Context, callID, userID int64) (bool, error) {
	recipients, err := r.deps.GroupCalls.ConferenceRecipients(ctx, callID)
	if err != nil {
		return false, err
	}
	for _, id := range recipients {
		if id == userID {
			return true, nil
		}
	}
	return false, nil
}

func (r *Router) onPhoneCreateGroupCall(ctx context.Context, req *tg.PhoneCreateGroupCallRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.GroupCalls == nil || r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	scheduleDate, _ := req.GetScheduleDate()
	if scheduleDate < 0 || (scheduleDate > 0 && scheduleDate <= now) {
		// The TDesktop picker only offers future times; past times are rejected outright (tolerating
		// in-flight second-level skew is left to the client).
		return nil, tgerr400("SCHEDULE_DATE_INVALID")
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, peer.ID)
	if err != nil {
		return nil, peerIDInvalidErr()
	}
	if view.Self.Status != domain.ChannelMemberActive || !channelMemberIsAdmin(view.Self) {
		return nil, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	// Broadcast-channel livestreams and any RTMP livestream: participants are pure listeners, forced
	// muted on join and unable to unmute themselves (join_muted). RTMP is especially critical — in
	// stream mode, if TDesktop finds the self row not force-muted it does `Rejoin after unforcemute`
	// every 3s, causing an infinite loop.
	joinMuted := !view.Channel.Megagroup || req.RtmpStream
	call, err := r.deps.GroupCalls.Create(ctx, view.Channel.ID, userID, req.Title, req.RtmpStream, joinMuted, scheduleDate, now)
	if err != nil {
		return nil, groupCallErr(err)
	}
	// Service message (carries channel pts, so offline members catch up via channels difference):
	// a scheduled call sends scheduled ("scheduled a video chat"), an immediate call sends started.
	serviceAction := domain.ChannelMessageAction{
		Type:           domain.ChannelActionGroupCall,
		CallID:         call.ID,
		CallAccessHash: call.AccessHash,
	}
	if scheduleDate > 0 {
		serviceAction.Type = domain.ChannelActionGroupCallScheduled
		serviceAction.CallScheduleDate = scheduleDate
	}
	var serviceRes domain.SendChannelMessageResult
	if res, err := r.deps.Channels.AppendCallServiceMessage(ctx, view.Channel.ID, userID, now, serviceAction); err == nil {
		serviceRes = res
		_ = r.deps.GroupCalls.SetStartedMessageID(ctx, call.ID, res.Message.ID)
	} else {
		r.log.Warn("group call started service message", zap.Int64("channel_id", view.Channel.ID), zap.Error(err))
	}
	channel, err := r.deps.Channels.SetActiveCall(ctx, view.Channel.ID, call.ID, call.AccessHash, false)
	if err != nil {
		channel = view.Channel
		channel.ActiveCallID = call.ID
		channel.ActiveCallAccessHash = call.AccessHash
	}
	// Fan-out: banner flag refresh + updateGroupCall + service message.
	r.pushChannelStateToMembers(ctx, userID, channel)
	r.pushGroupCallUpdate(ctx, channel, call)
	if serviceRes.Event.Pts != 0 {
		r.pushGroupCallServiceMessage(ctx, userID, serviceRes)
	}
	// Response: updateGroupCall + service message (from the initiating device's perspective).
	// TDesktop calls joinGroupCall itself after creating.
	update := &tg.UpdateGroupCall{Call: tgGroupCall(call, userID, true, r.cfg.PublicBaseURL)}
	update.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
	out := r.groupCallUpdateContainer(ctx, userID, channel, update, []int64{userID})
	if serviceRes.Event.Pts != 0 {
		if msgUpdate := tgChannelUpdate(userID, serviceRes.Event); msgUpdate != nil {
			out.Updates = append(out.Updates, msgUpdate)
		}
	}
	return out, nil
}

func (r *Router) onPhoneJoinGroupCall(ctx context.Context, req *tg.PhoneJoinGroupCallRequest) (tg.UpdatesClass, error) {
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
	if scope.call.RtmpStream {
		return r.joinRtmpGroupCall(ctx, scope, req)
	}
	joinAsChannelID, err := r.groupCallJoinAsChannelID(scope, req.JoinAs)
	if err != nil {
		return nil, err
	}
	// Parse the uplink join JSON (tolerate flags such as video_stopped and ssrc-groups — TDesktop
	// includes them on join).
	offer, ssrc, err := parseGroupCallJoinPayload(req.Params.Data)
	if err != nil {
		r.log.Warn("group call join payload", zap.Error(err))
		return nil, groupCallInvalidErr()
	}
	// Room cap (demo scale): a rejoin (already in the call, swapping ssrc) is not limited.
	if max := r.cfg.GroupCallMaxParticipants; max > 0 && scope.call.ParticipantsCount >= max {
		if p, found, _ := r.deps.GroupCalls.Participant(ctx, scope.call.ID, scope.userID); !found || p.Left {
			return nil, groupCallForbiddenErr()
		}
	}
	now := int(r.clock.Now().Unix())
	var publicKey []byte
	if pk, ok := req.GetPublicKey(); ok {
		publicKey = append([]byte(nil), pk[:]...)
	}
	var joinBlock []byte
	if block, ok := req.GetBlock(); ok {
		joinBlock = append([]byte(nil), block...)
	}
	// Internal video state: the endpoint is minted server-side (the join response's video.endpoint
	// and later participant.video.endpoint must be byte-for-byte identical); ssrc-groups are archived
	// regardless of the camera switch — replayed verbatim when video_stopped=false (join flag or a
	// later self-edit).
	endpoint := groupCallEndpointID(sfu.EndpointMain, offer.AudioSSRC)
	videoState := participantVideoState{
		Endpoint:     endpoint,
		SourceGroups: groupCallSsrcGroupsFromOffer(offer),
		Active:       !req.VideoStopped && len(offer.SsrcGroups) > 0,
	}
	mut, err := r.deps.GroupCalls.Join(ctx, domain.JoinGroupCallRequest{
		CallID:          scope.call.ID,
		UserID:          scope.userID,
		JoinAsChannelID: joinAsChannelID,
		SSRC:            ssrc,
		Muted:           req.Muted,
		IsAdmin:         scope.canManage(),
		PublicKey:       publicKey,
		JoinBlock:       joinBlock,
		VideoJSON:       encodeVideoState(videoState),
		Now:             now,
	})
	if err != nil {
		return nil, groupCallErr(err)
	}
	// Media plane: the SFU allocates an endpoint (M0 Disabled: syntactically complete empty
	// candidates; the client stays in Connecting and keeps alive with a 4s checkGroupCall heartbeat
	// — the M0 sweeper criterion depends on this behavior).
	sfuService := r.deps.SFU
	if sfuService == nil {
		sfuService = sfu.Disabled()
	}
	answer, err := sfuService.Join(ctx, scope.call.ID, scope.userID, sfu.EndpointMain, offer)
	if err != nil {
		// If the media plane fails, roll back the signaling side (keeping the two sides consistent)
		// and return 500.
		_, _ = r.deps.GroupCalls.Leave(ctx, scope.call.ID, scope.userID, now)
		r.log.Warn("group call sfu join", zap.Error(err))
		return nil, internalErr()
	}
	params, err := buildGroupCallConnectionParams(answer, endpoint)
	if err != nil {
		_ = sfuService.Leave(ctx, scope.call.ID, scope.userID, sfu.EndpointMain)
		_, _ = r.deps.GroupCalls.Leave(ctx, scope.call.ID, scope.userID, now)
		return nil, internalErr()
	}
	var conferenceJoinBlock domain.GroupCallChainBlock
	var hasConferenceJoinBlock bool
	if scope.call.Conference() && len(joinBlock) > 0 {
		block, err := r.deps.GroupCalls.AppendChainBlock(ctx, domain.GroupCallChainBlock{
			CallID:       scope.call.ID,
			SubChainID:   0,
			Offset:       -1,
			AuthorUserID: scope.userID,
			Block:        joinBlock,
			CreatedAt:    now,
		})
		if err != nil {
			_ = sfuService.Leave(ctx, scope.call.ID, scope.userID, sfu.EndpointMain)
			_, _ = r.deps.GroupCalls.Leave(ctx, scope.call.ID, scope.userID, now)
			return nil, groupCallErr(err)
		}
		conferenceJoinBlock = block
		hasConferenceJoinBlock = true
	}
	// Fan out to the room/online group members (the operator's other devices are included; this
	// device takes it from the RPC response).
	channel := r.groupCallMutationFanout(ctx, scope.channel, mut)
	// Response (TDesktop pulls updateGroupCallConnection from this RPC's returned Updates; it does
	// not wait for a push):
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{scope.userID})
	out.Updates = append(out.Updates, &tg.UpdateGroupCallConnection{Params: tg.DataJSON{Data: params}})
	callUpdate := &tg.UpdateGroupCall{Call: tgGroupCall(mut.Call, scope.userID, scope.canManage(), r.cfg.PublicBaseURL)}
	if channel.ID != 0 {
		callUpdate.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
	}
	out.Updates = append(out.Updates, callUpdate)
	if hasConferenceJoinBlock {
		nextOffset := conferenceJoinBlock.Offset + 1
		blocks := [][]byte{conferenceJoinBlock.Block}
		r.pushConferenceChainBlocks(ctx, mut.Call, conferenceJoinBlock.SubChainID, blocks, nextOffset)
		out.Updates = append(out.Updates, conferenceChainBlocksUpdate(mut.Call, conferenceJoinBlock.SubChainID, blocks, nextOffset))
	}
	return out, nil
}

func (r *Router) onPhoneLeaveGroupCall(ctx context.Context, req *tg.PhoneLeaveGroupCallRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	mut, err := r.deps.GroupCalls.Leave(ctx, scope.call.ID, scope.userID, now)
	if errors.Is(err, domain.ErrGroupCallNotJoined) {
		// Idempotent: duplicate leave / already cleared by the sweeper, return the current snapshot.
		return r.groupCallUpdateContainer(ctx, scope.userID, scope.channel,
			groupCallUpdateFor(scope.channel, scope.call, scope.userID, false, r.cfg.PublicBaseURL), nil), nil
	}
	if err != nil {
		return nil, groupCallErr(err)
	}
	if r.deps.SFU != nil {
		_ = r.deps.SFU.Leave(ctx, scope.call.ID, scope.userID, sfu.EndpointMain)
	}
	channel := r.groupCallMutationFanout(ctx, scope.channel, mut)
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{scope.userID})
	if mut.Call.Conference() && !mut.Call.Active() {
		out.Updates = append(out.Updates, groupCallUpdateFor(domain.Channel{}, mut.Call, scope.userID, scope.userID == mut.Call.CreatorUserID, r.cfg.PublicBaseURL))
	}
	return out, nil
}

func (r *Router) onPhoneDiscardGroupCall(ctx context.Context, in tg.InputGroupCallClass) (tg.UpdatesClass, error) {
	scope, err := r.groupCallScopeFrom(ctx, in)
	if err != nil {
		return nil, err
	}
	if !scope.canManage() {
		return nil, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	now := int(r.clock.Now().Unix())
	call, activeBeforeDiscard, err := r.deps.GroupCalls.Discard(ctx, scope.call.ID, now)
	if err != nil {
		return nil, groupCallErr(err)
	}
	if r.deps.SFU != nil {
		_ = r.deps.SFU.CloseRoom(ctx, call.ID)
	}
	if call.Conference() {
		r.pushConferenceGroupCallUpdateTo(ctx, call, groupCallParticipantUserIDs(activeBeforeDiscard))
		return r.groupCallUpdateContainer(ctx, scope.userID, domain.Channel{},
			groupCallUpdateFor(domain.Channel{}, call, scope.userID, true, r.cfg.PublicBaseURL), nil), nil
	}
	// RTMP livestream ended: disconnect the stream and flush the buffer (viewers then resync/stop on
	// their next fetch).
	if call.RtmpStream && r.deps.LiveStreams != nil {
		r.deps.LiveStreams.DropChannel(scope.channel.ID)
	}
	// Clear the channel association + ended service message (with duration).
	channel := scope.channel
	if updated, err := r.deps.Channels.SetActiveCall(ctx, channel.ID, 0, 0, false); err == nil {
		channel = updated
	}
	var serviceRes domain.SendChannelMessageResult
	if res, err := r.deps.Channels.AppendCallServiceMessage(ctx, channel.ID, scope.userID, now, domain.ChannelMessageAction{
		Type:           domain.ChannelActionGroupCall,
		CallID:         call.ID,
		CallAccessHash: call.AccessHash,
		CallDuration:   call.Duration,
	}); err == nil {
		serviceRes = res
	} else {
		r.log.Warn("group call ended service message", zap.Int64("channel_id", channel.ID), zap.Error(err))
	}
	r.pushChannelStateToMembers(ctx, scope.userID, channel)
	r.pushGroupCallUpdate(ctx, channel, call)
	if serviceRes.Event.Pts != 0 {
		r.pushGroupCallServiceMessage(ctx, scope.userID, serviceRes)
	}
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		groupCallUpdateFor(channel, call, scope.userID, true, r.cfg.PublicBaseURL), nil)
	if serviceRes.Event.Pts != 0 {
		if msgUpdate := tgChannelUpdate(scope.userID, serviceRes.Event); msgUpdate != nil {
			out.Updates = append(out.Updates, msgUpdate)
		}
	}
	return out, nil
}

func (r *Router) onPhoneGetGroupCall(ctx context.Context, req *tg.PhoneGetGroupCallRequest) (*tg.PhoneGroupCall, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	page, err := r.deps.GroupCalls.Participants(ctx, scope.call.ID, "", limit)
	if err != nil {
		return nil, groupCallErr(err)
	}
	userIDs := make([]int64, 0, len(page.Participants))
	for _, p := range page.Participants {
		userIDs = append(userIDs, p.UserID)
	}
	chats := []tg.ChatClass{}
	if !scope.call.Conference() {
		chats = append(chats, tgChannel(scope.userID, scope.channel, &scope.member))
	}
	// Scheduled call: backfill the viewer's own start-reminder subscription (the client reloads and
	// rebuilds local state in full).
	call := r.applyScheduleSubscription(ctx, scope.call, scope.userID)
	return &tg.PhoneGroupCall{
		Call:                   tgGroupCall(call, scope.userID, scope.canManage(), r.cfg.PublicBaseURL),
		Participants:           tgGroupCallParticipants(page.Participants, scope.userID),
		ParticipantsNextOffset: page.NextOffset,
		Chats:                  chats,
		Users:                  r.tgUsersForIDs(ctx, scope.userID, userIDs),
	}, nil
}

func (r *Router) onPhoneGetGroupParticipants(ctx context.Context, req *tg.PhoneGetGroupParticipantsRequest) (*tg.PhoneGroupParticipants, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	page, err := r.deps.GroupCalls.Participants(ctx, scope.call.ID, req.Offset, limit)
	if err != nil {
		return nil, groupCallErr(err)
	}
	userIDs := make([]int64, 0, len(page.Participants))
	for _, p := range page.Participants {
		userIDs = append(userIDs, p.UserID)
	}
	// The response's version = the current value: after a version jump the client rebuilds local
	// state from it and resumes applying increments.
	chats := []tg.ChatClass{}
	if !scope.call.Conference() {
		chats = append(chats, tgChannel(scope.userID, scope.channel, &scope.member))
	}
	return &tg.PhoneGroupParticipants{
		Count:        page.Count,
		Participants: tgGroupCallParticipants(page.Participants, scope.userID),
		NextOffset:   page.NextOffset,
		Chats:        chats,
		Users:        r.tgUsersForIDs(ctx, scope.userID, userIDs),
		Version:      page.Version,
	}, nil
}

// onPhoneCheckGroupCall is the unified outlet for keep-alive and "kick/restart recovery": it
// returns the subset of the input sources that still belong to the user's active endpoint; if the
// user's own ssrc is absent the client rejoins automatically. Note: the client only calls it in the
// Connecting state (the heartbeat stops once media is connected), so it must not be used alone to
// declare a participant dead.
func (r *Router) onPhoneCheckGroupCall(ctx context.Context, req *tg.PhoneCheckGroupCallRequest) ([]int, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	active, joined, err := r.deps.GroupCalls.Touch(ctx, scope.call.ID, scope.userID, now)
	if err != nil {
		return nil, groupCallErr(err)
	}
	if !joined {
		return []int{}, nil
	}
	activeSet := make(map[int]struct{}, len(active))
	for _, ssrc := range active {
		activeSet[int(int32(uint32(ssrc)))] = struct{}{}
	}
	// All of presentation's ssrcs (audio + video layers + RTX) also belong to the active endpoint:
	// TDesktop stuffs the screen ssrc into sources too, while DrKLO uses presentation.audio_source
	// (falling back to the first video ssrc by default) — any that is missing from the returned set
	// triggers a 4s loop rebuilding the screen instance.
	if p, found, err := r.deps.GroupCalls.Participant(ctx, scope.call.ID, scope.userID); err == nil && found && !p.Left {
		if st, ok := decodeVideoState(p.PresentationJSON); ok && st.Active {
			if st.AudioSource != 0 {
				activeSet[int(int32(uint32(st.AudioSource)))] = struct{}{}
			}
			for _, g := range st.SourceGroups {
				for _, src := range g.Sources {
					activeSet[int(int32(uint32(src)))] = struct{}{}
				}
			}
		}
	}
	out := make([]int, 0, len(req.Sources))
	for _, src := range req.Sources {
		if _, ok := activeSet[src]; ok {
			out = append(out, src)
		}
	}
	return out, nil
}

func groupCallUpdateFor(channel domain.Channel, call domain.GroupCall, viewerUserID int64, canManage bool, publicBaseURL ...string) *tg.UpdateGroupCall {
	update := &tg.UpdateGroupCall{Call: tgGroupCall(call, viewerUserID, canManage, publicBaseURL...)}
	if channel.ID != 0 {
		update.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
	}
	return update
}
