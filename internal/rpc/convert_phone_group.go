package rpc

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
)

// ---- TL 转换 ----

// groupCallUnmutedVideoLimit 是同时开视频的参与者上限（官方经 appConfig
// groupcall_video_participants_max 下发，缺省 30）。
// ⚠ unmuted_video_limit 是 TL 非可选 int：留 0 会让两端的视频/屏幕共享闸门
// `activeVideoSendersCount() >= limit` 恒真——「You can't share your screen in
// this chat」即此（TDesktop emitShareScreenError / DrKLO ChatObject.canStreamVideo）。
const groupCallUnmutedVideoLimit = 30

// tgGroupCall 把 call 行转为 TL groupCall。
func tgGroupCall(call domain.GroupCall, viewerUserID int64, canManage bool) tg.GroupCallClass {
	if !call.Active() {
		return &tg.GroupCallDiscarded{ID: call.ID, AccessHash: call.AccessHash, Duration: call.Duration}
	}
	out := &tg.GroupCall{
		JoinMuted:          call.JoinMuted,
		CanChangeJoinMuted: canManage,
		Creator:            call.CreatorUserID == viewerUserID && viewerUserID != 0,
		Conference:         call.Conference(),
		// can_start_video：TDesktop 不读；DrKLO 用它喂入会前 dummy self 行的
		// video_joined。RTC 通话一律放行。
		CanStartVideo:     true,
		ID:                call.ID,
		AccessHash:        call.AccessHash,
		ParticipantsCount: call.ParticipantsCount,
		UnmutedVideoLimit: groupCallUnmutedVideoLimit,
		Version:           call.Version,
	}
	if call.Title != "" {
		out.SetTitle(call.Title)
	}
	if call.Conference() {
		if link := conferenceCanonicalInviteLink(call.InviteSlug); link != "" {
			out.SetInviteLink(link)
		} else if call.InviteLink != "" {
			out.SetInviteLink(call.InviteLink)
		}
	}
	return out
}

// tgGroupCallParticipant 按 viewer 视角转换参与者行（Self flag per-viewer）。
func tgGroupCallParticipant(p domain.GroupCallParticipant, viewerUserID int64) tg.GroupCallParticipant {
	out := tg.GroupCallParticipant{
		Muted:         p.Muted,
		Left:          p.Left,
		CanSelfUnmute: !p.MutedByAdmin,
		Self:          p.UserID == viewerUserID,
		Peer:          &tg.PeerUser{UserID: p.UserID},
		Date:          p.JoinDate,
		Source:        int(int32(uint32(p.SSRC))), // uint32 按位转 int32（join JSON 同款语义）
	}
	if p.Left {
		// left 行不再携带 can_self_unmute 语义。
		out.CanSelfUnmute = false
	} else {
		// video_joined：RTC 入会一律置位。self 行缺它会让 TDesktop 置
		// _videoIsWorking=false 并强制关掉本端摄像头/屏幕共享。
		out.VideoJoined = true
	}
	if p.ActiveDate > 0 {
		out.SetActiveDate(p.ActiveDate)
	}
	if p.VolumeByAdmin > 0 {
		out.VolumeByAdmin = true
		out.SetVolume(p.VolumeByAdmin)
	}
	if p.RaiseHandRating > 0 {
		out.SetRaiseHandRating(p.RaiseHandRating)
	}
	// 视频/屏幕共享：Active 且未离会才输出字段（video_stopped ⇒ 字段消失；
	// paused ⇒ 字段保留但置 paused flag）。min 行（overrides 推送）不会走到
	// 这里改写 videoParams——applyParticipantOverride 只动 muted/volume。
	if !p.Left {
		if st, ok := decodeVideoState(p.VideoJSON); ok && st.Active {
			out.SetVideo(*tgParticipantVideo(st, false))
		}
		if st, ok := decodeVideoState(p.PresentationJSON); ok && st.Active {
			out.SetPresentation(*tgParticipantVideo(st, true))
		}
	}
	return out
}

func tgGroupCallParticipants(rows []domain.GroupCallParticipant, viewerUserID int64) []tg.GroupCallParticipant {
	out := make([]tg.GroupCallParticipant, 0, len(rows))
	for _, p := range rows {
		out = append(out, tgGroupCallParticipant(p, viewerUserID))
	}
	return out
}

// applyParticipantOverride 把 setter 的 per-viewer 覆盖（本地静音/音量）叠加到推给
// 该 setter 的 participant 行上，并置 min flag——min 告诉客户端「本行的部分字段
// 不可全信，按本地状态保留」，防止 setter 其它设备用此行覆盖本地 muted/volume。
func applyParticipantOverride(p tg.GroupCallParticipant, ov domain.GroupCallParticipantOverride) tg.GroupCallParticipant {
	p.SetMin(true)
	if ov.MutedByYou {
		p.SetMutedByYou(true)
	}
	if ov.Volume > 0 {
		p.SetVolume(ov.Volume)
	}
	return p
}

// inputGroupCallRef 解析 InputGroupCallClass 的三个变体。
// slug / inviteMessage 变体属 conference 路径（范围外），返回 GROUPCALL_INVALID
// 而非类型断言 panic——TDesktop conference 迁移会真实发出 inputGroupCallSlug。
func inputGroupCallRef(call tg.InputGroupCallClass) (callID, accessHash int64, err error) {
	switch v := call.(type) {
	case *tg.InputGroupCall:
		return v.ID, v.AccessHash, nil
	default:
		return 0, 0, groupCallInvalidErr()
	}
}

// ---- join JSON（上行）----

// groupCallJoinPayload 是 phone.joinGroupCall.params 的上行 JSON
// （tgcalls GroupJoinPayloadInternal::serialize 的产物，逐字段对应）。
type groupCallJoinPayload struct {
	SSRC         int32                  `json:"ssrc"`
	Ufrag        string                 `json:"ufrag"`
	Pwd          string                 `json:"pwd"`
	Fingerprints []groupCallFingerprint `json:"fingerprints"`
	SsrcGroups   []groupCallSsrcGroup   `json:"ssrc-groups,omitempty"`
}

type groupCallFingerprint struct {
	Hash        string `json:"hash"`
	Fingerprint string `json:"fingerprint"`
	Setup       string `json:"setup"`
}

type groupCallSsrcGroup struct {
	Semantics string  `json:"semantics"`
	Sources   []int64 `json:"sources"`
}

// parseGroupCallJoinPayload 解析并校验上行 JSON；解析必须容忍多余字段。
// ssrc-groups（视频 simulcast/RTX 源组）无论摄像头开关都会随主 join 携带，
// 必须存档：等 video_stopped=false 时原样回放进 participant.video。
func parseGroupCallJoinPayload(data string) (sfu.ClientOffer, int64, error) {
	var payload groupCallJoinPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return sfu.ClientOffer{}, 0, fmt.Errorf("parse join payload: %w", err)
	}
	if payload.Ufrag == "" || payload.Pwd == "" || payload.SSRC == 0 {
		return sfu.ClientOffer{}, 0, fmt.Errorf("join payload missing ssrc/ufrag/pwd")
	}
	offer := sfu.ClientOffer{
		// ssrc 是把 uint32 按位转 int32 的有符号值，还原为 uint32。
		AudioSSRC: uint32(payload.SSRC),
		Ufrag:     payload.Ufrag,
		Pwd:       payload.Pwd,
	}
	for _, fp := range payload.Fingerprints {
		if fp.Hash == "sha-256" {
			offer.FingerprintSHA256 = fp.Fingerprint
			break
		}
	}
	if offer.FingerprintSHA256 == "" {
		return sfu.ClientOffer{}, 0, fmt.Errorf("join payload missing sha-256 fingerprint")
	}
	for _, g := range payload.SsrcGroups {
		sg := sfu.SsrcGroup{Semantics: g.Semantics, Sources: make([]uint32, 0, len(g.Sources))}
		for _, src := range g.Sources {
			// sources 同样是 int32 位重解释。
			sg.Sources = append(sg.Sources, uint32(int32(src)))
		}
		offer.SsrcGroups = append(offer.SsrcGroups, sg)
	}
	return offer, int64(offer.AudioSSRC), nil
}

// ---- participant 视频内部状态（video_json / presentation_json 列）----

// participantVideoState 是存进 video_json/presentation_json 的内部快照（非 wire
// 格式）：endpoint 由服务端铸造且必须与 join 响应 video.endpoint 逐字节一致；
// source_groups 是上行 ssrc-groups 的原样存档；Active 决定 TL 字段是否出现。
type participantVideoState struct {
	Endpoint     string               `json:"endpoint"`
	SourceGroups []groupCallSsrcGroup `json:"source_groups,omitempty"`
	Active       bool                 `json:"active"`
	Paused       bool                 `json:"paused,omitempty"`
	// AudioSource 仅 presentation：屏幕实例自己的音频 ssrc（int32 位重解释存原值）。
	AudioSource int64 `json:"audio_source,omitempty"`
}

func encodeVideoState(st participantVideoState) []byte {
	out, err := json.Marshal(st)
	if err != nil {
		return nil
	}
	return out
}

func decodeVideoState(raw []byte) (participantVideoState, bool) {
	if len(raw) == 0 {
		return participantVideoState{}, false
	}
	var st participantVideoState
	if err := json.Unmarshal(raw, &st); err != nil || st.Endpoint == "" {
		return participantVideoState{}, false
	}
	return st, true
}

// tgParticipantVideo 把内部状态转为 TL groupCallParticipantVideo（Active 才输出）。
// withAudioSource 仅 presentation 置 true（DrKLO 用 audio_source 做屏幕 mySource，
// 缺失会退化取首个视频 ssrc 致 checkGroupCall 死循环重建）。
func tgParticipantVideo(st participantVideoState, withAudioSource bool) *tg.GroupCallParticipantVideo {
	out := &tg.GroupCallParticipantVideo{
		Paused:   st.Paused,
		Endpoint: st.Endpoint,
	}
	for _, g := range st.SourceGroups {
		sg := tg.GroupCallParticipantVideoSourceGroup{Semantics: g.Semantics}
		for _, src := range g.Sources {
			sg.Sources = append(sg.Sources, int(int32(uint32(src))))
		}
		out.SourceGroups = append(out.SourceGroups, sg)
	}
	if withAudioSource && st.AudioSource != 0 {
		out.SetAudioSource(int(int32(uint32(st.AudioSource))))
	}
	return out
}

// groupCallSsrcGroupsFromOffer 把解析后的源组转回存档形态（保持 int32 位重解释
// 的有符号写法，与上行 JSON 一致）。
func groupCallSsrcGroupsFromOffer(offer sfu.ClientOffer) []groupCallSsrcGroup {
	out := make([]groupCallSsrcGroup, 0, len(offer.SsrcGroups))
	for _, g := range offer.SsrcGroups {
		sg := groupCallSsrcGroup{Semantics: g.Semantics, Sources: make([]int64, 0, len(g.Sources))}
		for _, src := range g.Sources {
			sg.Sources = append(sg.Sources, int64(int32(src)))
		}
		out = append(out, sg)
	}
	return out
}

// groupCallEndpointID 铸造 camera/presentation 的 endpoint 串：call 内唯一
// （ssrc 有活跃唯一索引兜底）、同 participant 两路互异、rejoin 换 ssrc 自然换串。
func groupCallEndpointID(kind sfu.EndpointKind, audioSSRC uint32) string {
	if kind == sfu.EndpointPresentation {
		return fmt.Sprintf("presentation-%d", audioSSRC)
	}
	return fmt.Sprintf("audio-%d", audioSSRC)
}

// ---- 下行 JSON（updateGroupCallConnection.params）----

// videoRtcpFbs 是视频 codec 的反馈能力表（与客户端本地 addDefaultFeedbackParams
// 一致：goog-remb/transport-cc/ccm fir/nack/nack pli）。
func videoRtcpFbs() []map[string]any {
	return []map[string]any{
		{"type": "goog-remb"},
		{"type": "transport-cc"},
		{"type": "ccm", "subtype": "fir"},
		{"type": "nack"},
		{"type": "nack", "subtype": "pli"},
	}
}

// buildGroupCallConnectionParams 组装下行 transport JSON（契约 schema 逐字段照抄，
// 解析方为 tgcalls GroupJoinPayloadInternal/GroupNetworkManager）。要点：
//   - transport.fingerprints.setup="active"：SFU 是 DTLS 主动握手方；
//   - candidates 全部字段都是字符串（数值也要写成字符串）；
//   - video 节即使纯收看也必须存在（缺失则客户端不建任何视频通道）；
//   - video.endpoint 是**本参与者自己**的视频 endpoint 标识，必须与日后写进其
//     participant.video.endpoint 的串逐字节一致（客户端靠它做自我过滤）；
//   - payload-types 照抄客户端本地确定性分配（VP8=100/rtx101、VP9=102/rtx103，
//     PT 由客户端自配，本表主要给其它平台客户端消费）；不含 H264 ⇒ 全网 VP8 缺省；
//   - rtp-hdrexts 照抄客户端硬编码 id 表（1=audio-level、2=abs-send-time、
//     3=transport-wide-cc、13=video-orientation）。
func buildGroupCallConnectionParams(answer sfu.ServerAnswer, endpoint string) (string, error) {
	candidates := make([]map[string]any, 0, len(answer.Candidates))
	for i, c := range answer.Candidates {
		candidates = append(candidates, map[string]any{
			"component":  "1",
			"protocol":   c.Protocol,
			"port":       strconv.Itoa(c.Port),
			"ip":         c.IP,
			"type":       c.Type,
			"priority":   "2130706431",
			"foundation": "1",
			"generation": "0",
			"network":    "1",
			"id":         fmt.Sprintf("c%d", i+1),
		})
	}
	params := map[string]any{
		"transport": map[string]any{
			"ufrag": answer.Ufrag,
			"pwd":   answer.Pwd,
			"fingerprints": []map[string]any{{
				"hash":        "sha-256",
				"fingerprint": answer.FingerprintSHA256,
				"setup":       "active",
			}},
			"candidates": candidates,
		},
		"video": map[string]any{
			"endpoint": endpoint,
			"payload-types": []map[string]any{
				{"id": 100, "name": "VP8", "clockrate": 90000, "channels": 0,
					"parameters": map[string]any{}, "rtcp-fbs": videoRtcpFbs()},
				{"id": 101, "name": "rtx", "clockrate": 90000, "channels": 0,
					"parameters": map[string]any{"apt": "100"}, "rtcp-fbs": []map[string]any{}},
				{"id": 102, "name": "VP9", "clockrate": 90000, "channels": 0,
					"parameters": map[string]any{}, "rtcp-fbs": videoRtcpFbs()},
				{"id": 103, "name": "rtx", "clockrate": 90000, "channels": 0,
					"parameters": map[string]any{"apt": "102"}, "rtcp-fbs": []map[string]any{}},
			},
			"rtp-hdrexts": []map[string]any{
				{"id": 1, "uri": "urn:ietf:params:rtp-hdrext:ssrc-audio-level"},
				{"id": 2, "uri": "http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time"},
				{"id": 3, "uri": "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01"},
				{"id": 13, "uri": "urn:3gpp:video-orientation"},
			},
			"server_sources": []int64{},
		},
	}
	out, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal connection params: %w", err)
	}
	return string(out), nil
}
