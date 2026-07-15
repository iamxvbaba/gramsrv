package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// groupCallVideoJoinParams 构造带视频 ssrc-groups 的上行 join JSON
// （ssrc/sources 全部按 int32 位重解释写出，与 tgcalls serialize 一致）。
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

// simulcastGroups 是摄像头 3 层标准布局：SIM[s,s+2,s+4] + 每层 FID 对。
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
	// 客户端硬编码的扩展 id 表：1=audio-level、2=abs-send-time、3=twcc、13=video-orientation。
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
	// ⚠ unmuted_video_limit 是非可选 int：0 会让两端视频/屏幕共享闸门
	//（activeVideoSendersCount >= limit）恒真，按钮直接报「不能共享」。
	if call.UnmutedVideoLimit <= 0 || !call.CanStartVideo {
		t.Fatalf("call video gates = limit %d can_start_video %v, want positive limit + can_start_video",
			call.UnmutedVideoLimit, call.CanStartVideo)
	}
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	f.sessions.reset()

	// join：带 simulcast 组 + video_stopped（摄像头未开）。
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
		t.Fatalf("self row must carry video_joined（缺它 TDesktop 强制关本端摄像头）")
	}
	if _, has := row.GetVideo(); has {
		t.Fatalf("video_stopped join must not expose video field")
	}
	f.sessions.reset()

	// 开摄像头：editGroupCallParticipant(video_stopped=false) → video 字段出现，
	// endpoint 与 join 响应一致、source_groups 原样回放。
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
		t.Fatalf("participant video.endpoint = %q, want %q（必须与 join 响应逐字节一致）", video.Endpoint, wantEndpoint)
	}
	if len(video.SourceGroups) != 4 || video.SourceGroups[0].Semantics != "SIM" {
		t.Fatalf("source_groups = %+v, want SIM+3×FID 原样回放", video.SourceGroups)
	}
	if got := video.SourceGroups[0].Sources[0]; int64(int32(got)) != int64(int32(videoBase)) {
		t.Fatalf("SIM[0] = %d, want %d", got, videoBase)
	}

	// 幂等重放（video 维度绝不报错）。
	editAgain := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: &tg.InputPeerSelf{}}
	editAgain.SetVideoStopped(false)
	if _, err := f.router.onPhoneEditGroupCallParticipant(ownerCtx, editAgain); err != nil {
		t.Fatalf("redundant video edit must succeed, got %v（错误会触发客户端 rejoin 风暴）", err)
	}

	// 暂停：字段保留 + paused。
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

	// 关摄像头：字段消失。
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

	// 未入会先共享：GROUPCALL_JOIN_MISSING。
	if _, err := f.router.onPhoneJoinGroupCallPresentation(memberCtx, &tg.PhoneJoinGroupCallPresentationRequest{
		Call:   &inputCall,
		Params: groupCallVideoJoinParams(t, 7777, simulcastGroups(7778)),
	}); err == nil {
		t.Fatalf("presentation before main join must fail")
	}

	// 主 join。
	const mainSSRC = int32(424242)
	if _, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   &inputCall,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallVideoJoinParams(t, mainSSRC, simulcastGroups(int64(mainSSRC)+1)),
	}); err != nil {
		t.Fatalf("main join: %v", err)
	}
	f.sessions.reset()

	// 屏幕共享 join：独立 ssrc、2 层布局。
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
		t.Fatalf("presentation connection must carry presentation flag（否则被主实例误食）")
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
		t.Fatalf("presentation.audio_source = %d has=%v, want %d（DrKLO 缺它会 4s 循环重建）", audioSource, hasAudio, screenSSRC)
	}
	if len(pres.SourceGroups) != 3 {
		t.Fatalf("presentation source_groups = %+v", pres.SourceGroups)
	}

	// checkGroupCall 必须把 presentation 的全部 ssrc 认作活跃。
	sources := []int{int(mainSSRC), int(screenSSRC), int(screenSSRC) + 1, int(screenSSRC) + 3}
	alive, err := f.router.onPhoneCheckGroupCall(memberCtx, &tg.PhoneCheckGroupCallRequest{
		Call:    &inputCall,
		Sources: sources,
	})
	if err != nil {
		t.Fatalf("checkGroupCall: %v", err)
	}
	if len(alive) != len(sources) {
		t.Fatalf("checkGroupCall alive = %v, want all of %v（缺失会触发屏幕实例循环重建）", alive, sources)
	}

	// presentation_paused。
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

	// leaveGroupCallPresentation：字段消失。
	leaveRes, err := f.router.onPhoneLeaveGroupCallPresentation(memberCtx, &inputCall)
	if err != nil {
		t.Fatalf("leaveGroupCallPresentation: %v", err)
	}
	leaveRow := participantRow(t, leaveRes)
	if _, has := leaveRow.GetPresentation(); has {
		t.Fatalf("presentation field must disappear after leave")
	}
	// 重复 leave 幂等。
	if _, err := f.router.onPhoneLeaveGroupCallPresentation(memberCtx, &inputCall); err != nil {
		t.Fatalf("double leave presentation: %v", err)
	}

	// rejoin 主连接 ⇒ 旧 presentation 作废（客户端会重发 joinGroupCallPresentation）。
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

// raceLeavePresentationStore 包一层 GroupCallsService，把 UpdateParticipant 强制返回
// 注入的错误，用来模拟 leaveGroupCallPresentation 读到 presentation 仍在、但写阶段与
// 并发 leaveGroupCall/discard 撞车的竞态（其它方法照常委派，Participant 仍返回真实行）。
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

// TestLeaveGroupCallPresentationIdempotentOnConcurrentLeave 回归：屏幕共享结束与整体离会
// 同时发生时，leaveGroupCallPresentation 在写阶段撞 ErrGroupCallNotJoined（participant 已被
// 并发 leave 置 Left，presentation 随主连接离会级联清掉）或 ErrGroupCallDiscarded（主叫同时
// discard），必须按幂等成功返回而非 GROUPCALL_JOIN_MISSING 400（真双机 16:48 撞到过）。
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

	// 注入竞态：Participant() 仍返回带 presentation 的真实行（过早期幂等检查），
	// UpdateParticipant() 撞错误（模拟并发 leave 已置 Left / 并发 discard）。
	race := &raceLeavePresentationStore{GroupCallsService: f.router.deps.GroupCalls}
	f.router.deps.GroupCalls = race

	for _, injected := range []error{domain.ErrGroupCallNotJoined, domain.ErrGroupCallDiscarded} {
		race.updateErr = injected
		if _, err := f.router.onPhoneLeaveGroupCallPresentation(memberCtx, &inputCall); err != nil {
			t.Fatalf("leaveGroupCallPresentation 撞并发 %v 必须幂等成功，got %v（修复前=GROUPCALL_JOIN_MISSING 400）", injected, err)
		}
	}

	// 反向保险：非竞态类错误仍须如实抛出（不被幂等吞掉）。
	race.updateErr = domain.ErrGroupCallInvalid
	if _, err := f.router.onPhoneLeaveGroupCallPresentation(memberCtx, &inputCall); err == nil {
		t.Fatalf("非竞态错误必须传播，却被幂等吞掉")
	}
}
