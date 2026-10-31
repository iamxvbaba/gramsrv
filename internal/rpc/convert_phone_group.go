package rpc

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
)

// ---- TL conversion ----

// groupCallUnmutedVideoLimit is the maximum number of participants with video on at
// once (official distributes it via appConfig groupcall_video_participants_max,
// default 30). ⚠ unmuted_video_limit is a non-optional TL int: leaving it 0 makes
// both ends' video/screen-share gate `activeVideoSendersCount() >= limit` always
// true -- that is the "You can't share your screen in this chat" case (TDesktop
// emitShareScreenError / DrKLO ChatObject.canStreamVideo).
const groupCallUnmutedVideoLimit = 30

// groupCallStreamDCID is the DC number written into the RTMP groupCall.stream_dc_id.
// This server is single-DC, initialized from cfg.DC by router New(); the default 2
// matches help.getConfig's ThisDC default. The value is only used as a client
// stream-pull DC routing marker (telesrv pulls on the same connection), avoiding a
// TDesktop fallback log.
var groupCallStreamDCID = 2

// tgGroupCall converts a call row into TL groupCall.
func tgGroupCall(call domain.GroupCall, viewerUserID int64, canManage bool, publicBaseURL ...string) tg.GroupCallClass {
	if !call.Active() {
		return &tg.GroupCallDiscarded{ID: call.ID, AccessHash: call.AccessHash, Duration: call.Duration}
	}
	out := &tg.GroupCall{
		JoinMuted:          call.JoinMuted,
		CanChangeJoinMuted: canManage,
		Creator:            call.CreatorUserID == viewerUserID && viewerUserID != 0,
		Conference:         call.Conference(),
		// can_start_video: TDesktop does not read it; DrKLO uses it to feed
		// video_joined for the dummy self row before joining. RTC calls always allow it.
		CanStartVideo:     true,
		ID:                call.ID,
		AccessHash:        call.AccessHash,
		ParticipantsCount: call.ParticipantsCount,
		UnmutedVideoLimit: groupCallUnmutedVideoLimit,
		Version:           call.Version,
	}
	if call.RtmpStream {
		// RTMP live room: viewers pull via broadcast. rtmp_stream decides that TDesktop
		// opens the live UI (rather than a voice chat); listeners_hidden makes the
		// listener count expressed only via participants_count, without sending
		// per-listener rows (RTMP viewers usually do not enter the participant list).
		out.RtmpStream = true
		out.ListenersHidden = true
		// A missing stream_dc_id makes TDesktop fall back to the main DC and log; this
		// server is single-DC, so explicitly fill in this DC id (stream pulling still
		// uses the same connection; a DC shift only affects client routing markers).
		out.SetStreamDCID(groupCallStreamDCID)
	}
	if call.ScheduleDate > 0 {
		// Scheduled call: the client shows a countdown panel from schedule_date;
		// schedule_start_subscribed is a per-viewer projection (filled by
		// applyScheduleSubscription before egress).
		out.SetScheduleDate(call.ScheduleDate)
		out.ScheduleStartSubscribed = call.ScheduleStartSubscribed
	}
	if call.Title != "" {
		out.SetTitle(call.Title)
	}
	if call.Conference() {
		baseURL := ""
		if len(publicBaseURL) > 0 {
			baseURL = publicBaseURL[0]
		}
		if link := conferenceCanonicalInviteLink(call.InviteSlug, baseURL); link != "" {
			out.SetInviteLink(link)
		} else if call.InviteLink != "" {
			out.SetInviteLink(call.InviteLink)
		}
	}
	return out
}

// tgGroupCallParticipant converts a participant row by viewer (Self flag per-viewer).
// A join_as channel identity outputs PeerChannel (the client matches itself by
// participant.peer==joinAs(); outputting a user peer would make the panel of a
// person joining as a channel show a ghost double row).
func tgGroupCallParticipant(p domain.GroupCallParticipant, viewerUserID int64) tg.GroupCallParticipant {
	var peer tg.PeerClass = &tg.PeerUser{UserID: p.UserID}
	if p.JoinAsChannelID != 0 {
		peer = &tg.PeerChannel{ChannelID: p.JoinAsChannelID}
	}
	out := tg.GroupCallParticipant{
		Muted:         p.Muted,
		Left:          p.Left,
		CanSelfUnmute: !p.MutedByAdmin,
		Self:          p.UserID == viewerUserID,
		Peer:          peer,
		Date:          p.JoinDate,
		Source:        int(int32(uint32(p.SSRC))), // uint32 bit-reinterpreted to int32 (same semantics as the join JSON)
	}
	if p.Left {
		// A left row no longer carries can_self_unmute semantics.
		out.CanSelfUnmute = false
	} else {
		// video_joined: always set for an RTC join. Missing it on the self row makes
		// TDesktop set _videoIsWorking=false and force off this end's camera / screen share.
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
	// Video/screen share: output the field only when Active and not left
	// (video_stopped ⇒ the field disappears; paused ⇒ the field remains with the
	// paused flag set). A min row (overrides push) does not reach here to rewrite
	// videoParams -- applyParticipantOverride only touches muted/volume.
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

// applyParticipantOverride overlays the setter's per-viewer override (local mute /
// volume) onto the participant row pushed to that setter and sets the min flag -- min
// tells the client "part of this row's fields cannot be fully trusted, keep the local
// state", preventing the setter's other devices from overwriting local muted/volume
// with this row.
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

// inputGroupCallRef parses the three variants of InputGroupCallClass. The slug /
// inviteMessage variants belong to the conference path (out of scope); return
// GROUPCALL_INVALID rather than a type-assertion panic -- TDesktop conference
// migration really does send inputGroupCallSlug.
func inputGroupCallRef(call tg.InputGroupCallClass) (callID, accessHash int64, err error) {
	switch v := call.(type) {
	case *tg.InputGroupCall:
		return v.ID, v.AccessHash, nil
	default:
		return 0, 0, groupCallInvalidErr()
	}
}

// ---- join JSON (uplink) ----

// groupCallJoinPayload is the uplink JSON of phone.joinGroupCall.params
// (the product of tgcalls GroupJoinPayloadInternal::serialize, field-for-field).
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

// parseGroupCallJoinPayload parses and validates the uplink JSON; parsing must
// tolerate extra fields. ssrc-groups (video simulcast/RTX source groups) are carried
// with the main join regardless of the camera toggle and must be archived: replay
// them verbatim into participant.video when video_stopped=false.
func parseGroupCallJoinPayload(data string) (sfu.ClientOffer, int64, error) {
	var payload groupCallJoinPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return sfu.ClientOffer{}, 0, fmt.Errorf("parse join payload: %w", err)
	}
	if payload.Ufrag == "" || payload.Pwd == "" || payload.SSRC == 0 {
		return sfu.ClientOffer{}, 0, fmt.Errorf("join payload missing ssrc/ufrag/pwd")
	}
	offer := sfu.ClientOffer{
		// ssrc is the signed value of a uint32 bit-reinterpreted to int32; restore it to uint32.
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
			// sources is likewise an int32 bit reinterpretation.
			sg.Sources = append(sg.Sources, uint32(int32(src)))
		}
		offer.SsrcGroups = append(offer.SsrcGroups, sg)
	}
	return offer, int64(offer.AudioSSRC), nil
}

// ---- participant video internal state (video_json / presentation_json columns) ----

// participantVideoState is the internal snapshot stored into
// video_json/presentation_json (not the wire format): endpoint is minted by the
// server and must match the join response's video.endpoint byte-for-byte;
// source_groups is the verbatim archive of the uplink ssrc-groups; Active decides
// whether the TL field appears.
type participantVideoState struct {
	Endpoint     string               `json:"endpoint"`
	SourceGroups []groupCallSsrcGroup `json:"source_groups,omitempty"`
	Active       bool                 `json:"active"`
	Paused       bool                 `json:"paused,omitempty"`
	// AudioSource is presentation only: the screen instance's own audio ssrc (stored as the raw value with the int32 bit reinterpretation).
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

// tgParticipantVideo converts the internal state into TL groupCallParticipantVideo
// (only when Active). withAudioSource is true only for presentation (DrKLO uses
// audio_source as the screen mySource; missing it degrades to the first video ssrc
// and causes checkGroupCall to rebuild in an infinite loop).
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

// groupCallSsrcGroupsFromOffer converts the parsed source groups back to the archive
// shape (keeping the signed int32 bit-reinterpretation, consistent with the uplink
// JSON).
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

// groupCallEndpointID mints the camera/presentation endpoint string: unique within
// the call (ssrc has an active unique index as a fallback), distinct between the two
// paths of the same participant, and naturally changed by a rejoin with a new ssrc.
func groupCallEndpointID(kind sfu.EndpointKind, audioSSRC uint32) string {
	if kind == sfu.EndpointPresentation {
		return fmt.Sprintf("presentation-%d", audioSSRC)
	}
	return fmt.Sprintf("audio-%d", audioSSRC)
}

// ---- downlink JSON (updateGroupCallConnection.params) ----

// videoRtcpFbs is the feedback capability table for the video codec (consistent with
// the client's local addDefaultFeedbackParams:
// goog-remb/transport-cc/ccm fir/nack/nack pli).
func videoRtcpFbs() []map[string]any {
	return []map[string]any{
		{"type": "goog-remb"},
		{"type": "transport-cc"},
		{"type": "ccm", "subtype": "fir"},
		{"type": "nack"},
		{"type": "nack", "subtype": "pli"},
	}
}

// buildGroupCallConnectionParams assembles the downlink transport JSON (the contract
// schema copied field-for-field; parsed by tgcalls GroupJoinPayloadInternal/
// GroupNetworkManager). Key points:
//   - transport.fingerprints.setup="active": the SFU is the DTLS active handshaker;
//   - all candidates fields are strings (numeric values are written as strings too);
//   - the video section must exist even for pure viewing (missing it means the client
//     builds no video channel at all);
//   - video.endpoint is **this participant's own** video endpoint identity and must
//     match byte-for-byte the string later written into its
//     participant.video.endpoint (the client uses it for self filtering);
//   - payload-types copies the client's local deterministic assignment (VP8=100/
//     rtx101, VP9=102/rtx103; the PT is configured by the client itself and this
//     table is mainly consumed by clients on other platforms); no H264 ⇒ VP8 default
//     network-wide;
//   - rtp-hdrexts copies the client's hardcoded id table (1=audio-level,
//     2=abs-send-time, 3=transport-wide-cc, 13=video-orientation).
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
