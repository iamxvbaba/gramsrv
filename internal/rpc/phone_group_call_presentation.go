package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
)

// Screen sharing (presentation): the same participant's second media connection (a separate
// ICE/DTLS/ssrc set), landing on the presentation field of the same participant row at the TL
// layer. Hard contracts (determined by reading client sources):
//   - the updateGroupCallConnection in the response Updates must set the presentation flag (bit0),
//     otherwise the main instance consumes it by mistake; DrKLO looks for it in-place in the
//     response, so a push alone is not enough;
//   - the presentation field must carry audio_source: DrKLO uses it as the screen mySource and puts
//     it into checkGroupCall; without it, it falls back to the first video ssrc, causing a 4s loop
//     rebuild;
//   - a main-connection rejoin / full leave never re-sends leaveGroupCallPresentation: a join that
//     wholesale replaces video_json clears presentation_json, and tearing down the main SFU endpoint
//     tears down the screen with it.

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
	// Precondition: must already be in the call via the main join (the client also only sends this
	// RPC after the main join ack).
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
		Active:       true, // joinGroupCallPresentation starts sharing immediately (no video_stopped concept)
		AudioSource:  ssrc,
	}
	// Media plane first: a separate second endpoint (a duplicate join is replace-style idempotent —
	// rejoinPresentation re-sends the same ufrag/ssrc set).
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
		// Same-parameter replay (idempotent rejoin): return full connection params even with no state
		// change.
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
	// Idempotent snapshot: when not sharing / already cleaned up, return the current state rather
	// than erroring (the screen share is already down, so the goal is met).
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
		// Colliding with a concurrent leaveGroupCall/discard: after presentation is cascade-cleared
		// with the main connection's departure, this explicit leave finds on write that the
		// participant is already Left (ErrGroupCallNotJoined) or the call is already discarded. Since
		// the screen share is already down, return a snapshot as an idempotent success rather than
		// GROUPCALL_JOIN_MISSING 400 (the client is leaving at the same moment and would ignore the
		// 400, but the error pollutes logs and is not the correct semantics).
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
	// Other endpoints take the screen down when this row's presentation field disappears.
	return r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{scope.userID}), nil
}
