package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// groupCallVideoJoinParams builds an uplink join JSON with video ssrc-groups (ssrc/sources are all
// written as bit-reinterpreted int32, consistent with tgcalls serialize).
func groupCallVideoJoinParams(t *testing.T, audioSSRC int32, groups []map[string]any) tg.DataJSON {
	t.Helper()
	payload := map[string]any{
		"ssrc":  audioSSRC,
		"ufrag": "clientufrag",
		"pwd":   "clientpwd-clientpwd-1234",
		"fingerprints": []map[string]any{
			{"hash": "sha-256", "fingerprint": "AA:BB:CC:DD", "setup": "passive"},
		},
		"ssrc-groups": groups,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal join payload: %v", err)
	}
	return tg.DataJSON{Data: string(data)}
}

// simulcastGroups is the standard 3-layer camera layout: SIM[s,s+2,s+4] + an FID pair per layer.
func simulcastGroups(base int64) []map[string]any {
	return []map[string]any{
		{"semantics": "SIM", "sources": []int64{base, base + 2, base + 4}},
		{"semantics": "FID", "sources": []int64{base, base + 1}},
		{"semantics": "FID", "sources": []int64{base + 2, base + 3}},
		{"semantics": "FID", "sources": []int64{base + 4, base + 5}},
	}
}

func connectionVideoEndpoint(t *testing.T, conn *tg.UpdateGroupCallConnection) string {
	t.Helper()
	var params struct {
		Video struct {
			Endpoint     string           `json:"endpoint"`
			PayloadTypes []map[string]any `json:"payload-types"`
			RtpHdrexts   []struct {
				ID int `json:"id"`
			} `json:"rtp-hdrexts"`
		} `json:"video"`
	}
	if err := json.Unmarshal([]byte(conn.Params.Data), &params); err != nil {
		t.Fatalf("parse connection params: %v", err)
	}
	if len(params.Video.PayloadTypes) < 4 {
		t.Fatalf("video payload-types = %v, want VP8/rtx/VP9/rtx", params.Video.PayloadTypes)
	}
	ids := map[int]bool{}
	for _, h := range params.Video.RtpHdrexts {
		ids[h.ID] = true
	}
	// Client-hard-coded extension id table: 1=audio-level, 2=abs-send-time, 3=twcc,
	// 13=video-orientation.
	for _, want := range []int{1, 2, 3, 13} {
		if !ids[want] {
			t.Fatalf("rtp-hdrexts missing id=%d: %v", want, params.Video.RtpHdrexts)
		}
	}
	return params.Video.Endpoint
}

func participantRow(t *testing.T, updates tg.UpdatesClass) tg.GroupCallParticipant {
	t.Helper()
	parts := findUpdate[*tg.UpdateGroupCallParticipants](t, updates)
	if len(parts.Participants) != 1 {
		t.Fatalf("participants = %d, want 1", len(parts.Participants))
	}
	return parts.Participants[0]
}

func TestGroupCallVideoLifecycle(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)

	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		RandomID: 1,
	})
	if err != nil {
		t.Fatalf("createGroupCall: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	// ⚠ unmuted_video_limit is a non-optional int: 0 would make the video/screen-share gate on both
	// platforms (activeVideoSendersCount >= limit) always true, and the button would report "cannot
	// share" outright.
	if call.UnmutedVideoLimit <= 0 || !call.CanStartVideo {
		t.Fatalf("call video gates = limit %d can_start_video %v, want positive limit + can_start_video",
			call.UnmutedVideoLimit, call.CanStartVideo)
	}
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	f.sessions.reset()

	// join: carries simulcast groups + video_stopped (camera off).
	audioSSRC := int32(-100200300)
	videoBase := int64(audioSSRC) + 1
	joinReq := &tg.PhoneJoinGroupCallRequest{
		Call:   &inputCall,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallVideoJoinParams(t, audioSSRC, simulcastGroups(videoBase)),
	}
	joinReq.SetVideoStopped(true)
	joinRes, err := f.router.onPhoneJoinGroupCall(ownerCtx, joinReq)
	if err != nil {
		t.Fatalf("joinGroupCall: %v", err)
	}
	conn := findUpdate[*tg.UpdateGroupCallConnection](t, joinRes)
	if conn.Presentation {
		t.Fatalf("main join connection must not carry presentation flag")
	}
	wantEndpoint := fmt.Sprintf("audio-%d", uint32(audioSSRC))
	if got := connectionVideoEndpoint(t, conn); got != wantEndpoint {
		t.Fatalf("join video.endpoint = %q, want %q", got, wantEndpoint)
	}
	row := participantRow(t, joinRes)
	if !row.VideoJoined {
		t.Fatalf("self row must carry video_joined (without it TDesktop forces the local camera off)")
	}
	if _, has := row.GetVideo(); has {
		t.Fatalf("video_stopped join must not expose video field")
	}
	f.sessions.reset()

	// Camera on: editGroupCallParticipant(video_stopped=false) -> the video field appears, the
	// endpoint matches the join response, and source_groups are replayed verbatim.
	editReq := &tg.PhoneEditGroupCallParticipantRequest{
		Call:        &inputCall,
		Participant: &tg.InputPeerSelf{},
	}
	editReq.SetVideoStopped(false)
	editRes, err := f.router.onPhoneEditGroupCallParticipant(ownerCtx, editReq)
	if err != nil {
		t.Fatalf("video_stopped=false: %v", err)
	}
	row = participantRow(t, editRes)
	video, has := row.GetVideo()
	if !has {
		t.Fatalf("video field missing after camera on")
	}
	if video.Endpoint != wantEndpoint {
		t.Fatalf("participant video.endpoint = %q, want %q (must be byte-for-byte identical to the join response)", video.Endpoint, wantEndpoint)
	}
	if len(video.SourceGroups) != 4 || video.SourceGroups[0].Semantics != "SIM" {
		t.Fatalf("source_groups = %+v, want SIM+3×FID replayed verbatim", video.SourceGroups)
	}
	if got := video.SourceGroups[0].Sources[0]; int64(int32(got)) != int64(int32(videoBase)) {
		t.Fatalf("SIM[0] = %d, want %d", got, videoBase)
	}

	// Idempotent replay (the video dimension never errors).
	editAgain := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: &tg.InputPeerSelf{}}
	editAgain.SetVideoStopped(false)
	if _, err := f.router.onPhoneEditGroupCallParticipant(ownerCtx, editAgain); err != nil {
		t.Fatalf("redundant video edit must succeed, got %v (an error would trigger a client rejoin storm)", err)
	}

	// Pause: keep the field + paused.
	pauseReq := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: &tg.InputPeerSelf{}}
	pauseReq.SetVideoPaused(true)
	pauseRes, err := f.router.onPhoneEditGroupCallParticipant(ownerCtx, pauseReq)
	if err != nil {
		t.Fatalf("video_paused: %v", err)
	}
	pausedRow := participantRow(t, pauseRes)
	video, has = pausedRow.GetVideo()
	if !has || !video.Paused {
		t.Fatalf("paused video = %+v has=%v, want paused field kept", video, has)
	}

	// Camera off: the field disappears.
	stopReq := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: &tg.InputPeerSelf{}}
	stopReq.SetVideoStopped(true)
	stopRes, err := f.router.onPhoneEditGroupCallParticipant(ownerCtx, stopReq)
	if err != nil {
		t.Fatalf("video_stopped=true: %v", err)
	}
	stopRow := participantRow(t, stopRes)
	if _, has := stopRow.GetVideo(); has {
		t.Fatalf("video field must disappear after camera off")
	}
}

func TestGroupCallPresentationLifecycle(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	memberCtx := f.userCtx(f.member, 22)

	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		RandomID: 1,
	})
	if err != nil {
		t.Fatalf("createGroupCall: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}

	// Sharing before joining: GROUPCALL_JOIN_MISSING.
	if _, err := f.router.onPhoneJoinGroupCallPresentation(memberCtx, &tg.PhoneJoinGroupCallPresentationRequest{
		Call:   &inputCall,
		Params: groupCallVideoJoinParams(t, 7777, simulcastGroups(7778)),
	}); err == nil {
		t.Fatalf("presentation before main join must fail")
	}

	// Main join.
	const mainSSRC = int32(424242)
	if _, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   &inputCall,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallVideoJoinParams(t, mainSSRC, simulcastGroups(int64(mainSSRC)+1)),
	}); err != nil {
		t.Fatalf("main join: %v", err)
	}
	f.sessions.reset()

	// Screen-share join: independent ssrc, 2-layer layout.
	const screenSSRC = int32(515151)
	screenGroups := []map[string]any{
		{"semantics": "SIM", "sources": []int64{int64(screenSSRC) + 1, int64(screenSSRC) + 3}},
		{"semantics": "FID", "sources": []int64{int64(screenSSRC) + 1, int64(screenSSRC) + 2}},
		{"semantics": "FID", "sources": []int64{int64(screenSSRC) + 3, int64(screenSSRC) + 4}},
	}
	presRes, err := f.router.onPhoneJoinGroupCallPresentation(memberCtx, &tg.PhoneJoinGroupCallPresentationRequest{
		Call:   &inputCall,
		Params: groupCallVideoJoinParams(t, screenSSRC, screenGroups),
	})
	if err != nil {
		t.Fatalf("joinGroupCallPresentation: %v", err)
	}
	conn := findUpdate[*tg.UpdateGroupCallConnection](t, presRes)
	if !conn.Presentation {
		t.Fatalf("presentation connection must carry presentation flag (otherwise the main instance consumes it by mistake)")
	}
	wantEndpoint := fmt.Sprintf("presentation-%d", uint32(screenSSRC))
	if got := connectionVideoEndpoint(t, conn); got != wantEndpoint {
		t.Fatalf("presentation video.endpoint = %q, want %q", got, wantEndpoint)
	}
	row := participantRow(t, presRes)
	pres, has := row.GetPresentation()
	if !has {
		t.Fatalf("participant presentation field missing")
	}
	if pres.Endpoint != wantEndpoint {
		t.Fatalf("presentation.endpoint = %q, want %q", pres.Endpoint, wantEndpoint)
	}
	audioSource, hasAudio := pres.GetAudioSource()
	if !hasAudio || audioSource != int(screenSSRC) {
		t.Fatalf("presentation.audio_source = %d has=%v, want %d (without it DrKLO loops rebuilding every 4s)", audioSource, hasAudio, screenSSRC)
	}
	if len(pres.SourceGroups) != 3 {
		t.Fatalf("presentation source_groups = %+v", pres.SourceGroups)
	}

	// checkGroupCall must treat all of presentation's ssrcs as active.
	sources := []int{int(mainSSRC), int(screenSSRC), int(screenSSRC) + 1, int(screenSSRC) + 3}
	alive, err := f.router.onPhoneCheckGroupCall(memberCtx, &tg.PhoneCheckGroupCallRequest{
		Call:    &inputCall,
		Sources: sources,
	})
	if err != nil {
		t.Fatalf("checkGroupCall: %v", err)
	}
	if len(alive) != len(sources) {
		t.Fatalf("checkGroupCall alive = %v, want all of %v (a missing one triggers a loop rebuilding the screen instance)", alive, sources)
	}

	// presentation_paused.
	pauseReq := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: &tg.InputPeerSelf{}}
	pauseReq.SetPresentationPaused(true)
	pauseRes, err := f.router.onPhoneEditGroupCallParticipant(memberCtx, pauseReq)
	if err != nil {
		t.Fatalf("presentation_paused: %v", err)
	}
	pausedRow := participantRow(t, pauseRes)
	pres, _ = pausedRow.GetPresentation()
	if !pres.Paused {
		t.Fatalf("presentation must be paused")
	}

	// leaveGroupCallPresentation: the field disappears.
	leaveRes, err := f.router.onPhoneLeaveGroupCallPresentation(memberCtx, &inputCall)
	if err != nil {
		t.Fatalf("leaveGroupCallPresentation: %v", err)
	}
	leaveRow := participantRow(t, leaveRes)
	if _, has := leaveRow.GetPresentation(); has {
		t.Fatalf("presentation field must disappear after leave")
	}
	// Duplicate leave is idempotent.
	if _, err := f.router.onPhoneLeaveGroupCallPresentation(memberCtx, &inputCall); err != nil {
		t.Fatalf("double leave presentation: %v", err)
	}

	// Rejoining the main connection => the old presentation is invalidated (the client re-sends
	// joinGroupCallPresentation).
	if _, err := f.router.onPhoneJoinGroupCallPresentation(memberCtx, &tg.PhoneJoinGroupCallPresentationRequest{
		Call:   &inputCall,
		Params: groupCallVideoJoinParams(t, screenSSRC, screenGroups),
	}); err != nil {
		t.Fatalf("re-share: %v", err)
	}
	rejoinRes, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   &inputCall,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallVideoJoinParams(t, mainSSRC+1, simulcastGroups(int64(mainSSRC)+100)),
	})
	if err != nil {
		t.Fatalf("main rejoin: %v", err)
	}
	rejoinRow := participantRow(t, rejoinRes)
	if _, has := rejoinRow.GetPresentation(); has {
		t.Fatalf("main rejoin must invalidate old presentation registration")
	}
}

// raceLeavePresentationStore wraps a GroupCallsService and forces UpdateParticipant to return an
// injected error, to simulate the race where leaveGroupCallPresentation reads that presentation is
// still present but the write stage collides with a concurrent leaveGroupCall/discard (other methods
// delegate as usual, and Participant still returns the real row).
type raceLeavePresentationStore struct {
	GroupCallsService
	updateErr error
}

func (s *raceLeavePresentationStore) UpdateParticipant(ctx context.Context, callID, userID int64, update domain.GroupCallParticipantUpdate) (domain.GroupCallMutation, bool, error) {
	if s.updateErr != nil {
		return domain.GroupCallMutation{}, false, s.updateErr
	}
	return s.GroupCallsService.UpdateParticipant(ctx, callID, userID, update)
}

// TestLeaveGroupCallPresentationIdempotentOnConcurrentLeave regression: when ending screen sharing
// and a full leave happen at the same time, leaveGroupCallPresentation collides at the write stage
// with ErrGroupCallNotJoined (the participant was set Left by the concurrent leave, and presentation
// is cascade-cleared with the main connection's departure) or ErrGroupCallDiscarded (the caller
// discards at the same time); it must return an idempotent success rather than GROUPCALL_JOIN_MISSING
// 400 (hit on real dual devices at 16:48).
func TestLeaveGroupCallPresentationIdempotentOnConcurrentLeave(t *testing.T) {
	f := newGroupCallFixture(t)
	memberCtx := f.userCtx(f.member, 22)

	createRes, err := f.router.onPhoneCreateGroupCall(f.userCtx(f.owner, 11), &tg.PhoneCreateGroupCallRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		RandomID: 1,
	})
	if err != nil {
		t.Fatalf("createGroupCall: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}

	const mainSSRC = int32(424242)
	if _, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   &inputCall,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallVideoJoinParams(t, mainSSRC, simulcastGroups(int64(mainSSRC)+1)),
	}); err != nil {
		t.Fatalf("main join: %v", err)
	}
	const screenSSRC = int32(515151)
	if _, err := f.router.onPhoneJoinGroupCallPresentation(memberCtx, &tg.PhoneJoinGroupCallPresentationRequest{
		Call:   &inputCall,
		Params: groupCallVideoJoinParams(t, screenSSRC, simulcastGroups(int64(screenSSRC)+1)),
	}); err != nil {
		t.Fatalf("joinGroupCallPresentation: %v", err)
	}

	// Inject the race: Participant() still returns the real row with presentation (passing the early
	// idempotency check), while UpdateParticipant() hits an error (simulating a concurrent leave that
	// already set Left / a concurrent discard).
	race := &raceLeavePresentationStore{GroupCallsService: f.router.deps.GroupCalls}
	f.router.deps.GroupCalls = race

	for _, injected := range []error{domain.ErrGroupCallNotJoined, domain.ErrGroupCallDiscarded} {
		race.updateErr = injected
		if _, err := f.router.onPhoneLeaveGroupCallPresentation(memberCtx, &inputCall); err != nil {
			t.Fatalf("leaveGroupCallPresentation with concurrent %v must succeed idempotently, got %v (before the fix = GROUPCALL_JOIN_MISSING 400)", injected, err)
		}
	}

	// Reverse safety: non-race errors must still be returned faithfully (not swallowed by the
	// idempotency).
	race.updateErr = domain.ErrGroupCallInvalid
	if _, err := f.router.onPhoneLeaveGroupCallPresentation(memberCtx, &inputCall); err == nil {
		t.Fatalf("a non-race error must propagate but was swallowed by the idempotency")
	}
}
